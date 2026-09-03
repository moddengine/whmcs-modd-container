package docker

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/client"
	"github.com/moddengine/whmcs-container-controller/internal/config"
	"github.com/moddengine/whmcs-container-controller/internal/isolation"
	"github.com/moddengine/whmcs-container-controller/internal/model"
)

func TestPullLatestStableImage(t *testing.T) {
	page := 0
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		if page == 1 {
			_, _ = io.WriteString(w, `{"next":"page-2","results":[{"name":"dev-9","tag_last_pushed":"2026-08-10T03:00:00Z"},{"name":"v2","tag_last_pushed":"2026-08-09T03:00:00Z"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"next":null,"results":[{"name":"v3","tag_last_pushed":"2026-08-10T02:00:00Z"}]}`)
	}))
	defer hub.Close()
	version, err := latestHubVersion(t.Context(), hub.Client(), hub.URL, "moddengine/whmcs", "", "")
	if err != nil || version != "v3" || page != 2 {
		t.Fatalf("latestHubVersion() = %q after %d pages: %v", version, page, err)
	}

	var pulledImage, pulledTag, pulledAuth string
	dockerAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pulledImage, pulledTag = r.URL.Query().Get("fromImage"), r.URL.Query().Get("tag")
		pulledAuth = r.Header.Get(registry.AuthHeader)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "downloaded"})
	}))
	defer dockerAPI.Close()
	client, err := client.NewClientWithOpts(client.WithHost(dockerAPI.URL), client.WithHTTPClient(dockerAPI.Client()), client.WithVersion("1.52"))
	if err != nil {
		t.Fatal(err)
	}
	pulled, err := (&Adapter{client: client, cfg: config.Docker{ImageRepository: "moddengine/whmcs"}, configDir: t.TempDir()}).Pull(t.Context(), "pr-123")
	if err != nil || pulled.Version != "pr-123" || pulledImage != "docker.io/moddengine/whmcs" || pulledTag != "pr-123" || pulledAuth != "" {
		t.Fatalf("Pull() = %#v, image=%q tag=%q authenticated=%t: %v", pulled, pulledImage, pulledTag, pulledAuth != "", err)
	}
}

func TestLatestStableImageUsesDockerHubCredentials(t *testing.T) {
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/token":
			var login map[string]string
			if err := json.NewDecoder(r.Body).Decode(&login); err != nil || login["identifier"] != "hub-user" || login["secret"] != "hub-token" {
				t.Fatalf("unexpected Docker Hub login: %#v, %v", login, err)
			}
			_, _ = io.WriteString(w, `{"access_token":"short-lived"}`)
		case "/namespaces/moddengine/repositories/whmcs/tags":
			if r.Header.Get("Authorization") != "Bearer short-lived" {
				t.Fatalf("missing Docker Hub bearer token")
			}
			_, _ = io.WriteString(w, `{"results":[{"name":"v4","tag_last_pushed":"2026-08-20T02:00:00Z"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer hub.Close()
	version, err := latestHubVersion(t.Context(), hub.Client(), hub.URL, "moddengine/whmcs", "hub-user", "hub-token")
	if err != nil || version != "v4" {
		t.Fatalf("latestHubVersion() = %q, %v", version, err)
	}
}

func TestPullUsesDockerCLIRegistryCredentials(t *testing.T) {
	configDir := t.TempDir()
	authValue := base64.StdEncoding.EncodeToString([]byte("hub-user:hub-token"))
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"auths":{"https://index.docker.io/v1/":{"auth":"`+authValue+`"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var registryAuth string
	dockerAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		registryAuth = r.Header.Get(registry.AuthHeader)
		_, _ = io.WriteString(w, `{"status":"downloaded"}`)
	}))
	defer dockerAPI.Close()
	client, err := client.NewClientWithOpts(client.WithHost(dockerAPI.URL), client.WithHTTPClient(dockerAPI.Client()), client.WithVersion("1.52"))
	if err != nil {
		t.Fatal(err)
	}
	adapter := &Adapter{client: client, cfg: config.Docker{ImageRepository: "moddengine/moddengine"}, configDir: configDir}
	if _, err := adapter.Pull(t.Context(), "v1"); err != nil {
		t.Fatal(err)
	}
	decoded, err := registry.DecodeAuthConfig(registryAuth)
	if err != nil || registryAuth == "" || decoded.Username != "hub-user" || decoded.Password != "hub-token" {
		t.Fatalf("unexpected registry authentication: present=%t username=%q: %v", registryAuth != "", decoded.Username, err)
	}
}

func TestRegistryAuthErrorsAndAnonymousPulls(t *testing.T) {
	t.Run("missing config", func(t *testing.T) {
		auth, err := (&Adapter{configDir: t.TempDir()}).registryAuth("busybox:latest")
		if err != nil || auth != "" {
			t.Fatalf("registryAuth() = %q, %v", auth, err)
		}
	})

	t.Run("malformed config", func(t *testing.T) {
		configDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"auths":`), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := (&Adapter{configDir: configDir}).registryAuth("busybox:latest")
		if err == nil || !strings.Contains(err.Error(), "load Docker config") {
			t.Fatalf("expected actionable config error, got %v", err)
		}
	})

	t.Run("credential helper failure", func(t *testing.T) {
		configDir, binDir := t.TempDir(), t.TempDir()
		if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"credsStore":"broken"}`), 0600); err != nil {
			t.Fatal(err)
		}
		helper := filepath.Join(binDir, "docker-credential-broken")
		if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		_, err := (&Adapter{configDir: configDir}).registryAuth("busybox:latest")
		if err == nil || !strings.Contains(err.Error(), "Docker credentials") {
			t.Fatalf("expected actionable credential-helper error, got %v", err)
		}
	})
}

func TestPullErrorContainsImageButNotCredentials(t *testing.T) {
	configDir := t.TempDir()
	authValue := base64.StdEncoding.EncodeToString([]byte("secret-user:secret-token"))
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"auths":{"https://index.docker.io/v1/":{"auth":"`+authValue+`"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	dockerAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"denied"}`, http.StatusUnauthorized)
	}))
	defer dockerAPI.Close()
	client, err := client.NewClientWithOpts(client.WithHost(dockerAPI.URL), client.WithHTTPClient(dockerAPI.Client()), client.WithVersion("1.52"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&Adapter{client: client, cfg: config.Docker{ImageRepository: "moddengine/moddengine"}, configDir: configDir}).Pull(t.Context(), "private")
	if err == nil || !strings.Contains(err.Error(), "moddengine/moddengine:private") {
		t.Fatalf("pull error lost image reference: %v", err)
	}
	for _, secret := range []string{"secret-user", "secret-token", authValue} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("pull error exposed credentials: %v", err)
		}
	}
}

func TestStopAllReconcilesEveryContainerState(t *testing.T) {
	stopped := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/json"):
			_, _ = io.WriteString(w, `[{"Id":"restarting","State":"restarting"},{"Id":"exited","State":"exited"}]`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/stop"):
			stopped++
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	api, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithHTTPClient(server.Client()), client.WithVersion("1.52"))
	if err != nil {
		t.Fatal(err)
	}
	adapter := &Adapter{client: api}
	if err := adapter.StopAll(t.Context(), "whmcs-123"); err != nil || stopped != 2 {
		t.Fatalf("StopAll() stopped %d containers: %v", stopped, err)
	}
}

func TestSortVersionsNewestFirst(t *testing.T) {
	versions := []model.ImageVersion{
		{Version: "old", CreatedAt: time.Unix(1, 0)},
		{Version: "new", CreatedAt: time.Unix(2, 0)},
	}
	sortVersionsNewestFirst(versions)
	if versions[0].Version != "new" {
		t.Fatalf("newest version must be first: %#v", versions)
	}
}

func TestContainerSpec(t *testing.T) {
	service := model.Service{
		ID: "whmcs-123", MainDomain: "example.com", Version: "v26.1.15",
		Package: map[string]string{"plan": "small|Small Hosting Plan", "email_sends": "500", "group": "website-hosting|Website Hosting"},
		Dataset: model.DatasetRecord{Mountpoint: "/modd/sites/whmcs-123"},
		Deploy:  map[string]model.Deploy{"blue": {Socket: "/run/whmcs/whmcs-123-blue/http.sock"}},
	}
	cfg := config.Docker{
		Network: "udo-net", ImageRepository: "whmcs-runtime",
		Binds:                []string{"{mountpoint}/{slot}/cache:/cache", "/modd/host/caddy/http/{service_id}-{slot}:{socket_path}"},
		Environment:          []string{"SERVICE={service_id}", "DATA={mountpoint}", "DEPLOY={slot}"},
		CertificateMountPath: "/srv/modd/secrets",
	}
	spec, host, networking, err := ContainerSpec(cfg, service, "blue")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Image != "whmcs-runtime:v26.1.15" ||
		spec.User != "10123:10123" ||
		spec.Labels[serviceLabel] != "whmcs-123" ||
		spec.Labels[appLabel] != "whmcs" ||
		spec.Labels[deployLabel] != "blue" {
		t.Fatalf("unexpected container spec: %#v", spec)
	}
	if !slices.Contains(spec.Env, "ME_SITE=whmcs-123") {
		t.Fatalf("ME_SITE must contain the service ID: %#v", spec.Env)
	}
	for _, expected := range []string{"SERVICE=whmcs-123", "DATA=/modd/sites/whmcs-123", "DEPLOY=blue"} {
		if !slices.Contains(spec.Env, expected) {
			t.Fatalf("environment placeholder was not expanded to %q: %#v", expected, spec.Env)
		}
	}
	if !slices.Equal(spec.Env[len(spec.Env)-3:], []string{
		"ME_PACKAGE_EMAIL_SENDS=500",
		"ME_PACKAGE_GROUP=website-hosting|Website Hosting",
		"ME_PACKAGE_PLAN=small|Small Hosting Plan",
	}) {
		t.Fatalf("package environment is not deterministic: %#v", spec.Env)
	}
	if !slices.Equal(host.Binds, []string{
		"/modd/sites/whmcs-123/blue/cache:/cache",
		"/modd/host/caddy/http/whmcs-123-blue:/run/moddengine",
		"/modd/sites/whmcs-123/secrets:/srv/modd/secrets:ro",
		"/run/whmcs/whmcs-123-blue:/run/whmcs/whmcs-123-blue",
	}) {
		t.Fatalf("unexpected binds: %#v", host.Binds)
	}
	if networking.EndpointsConfig["udo-net"] == nil {
		t.Fatal("configured network missing")
	}
	if host.RestartPolicy.Name != container.RestartPolicyUnlessStopped {
		t.Fatalf("unexpected restart policy: %q", host.RestartPolicy.Name)
	}
}

func TestSocketLayout(t *testing.T) {
	tests := []struct {
		version, path, name string
	}{
		{"v26.0.52", "/run/nginx", "nginx.sock"},
		{"v26.1.14", "/run/nginx", "nginx.sock"},
		{"v26.1.15", "/run/moddengine", "http.sock"},
		{"v26.2.0", "/run/moddengine", "http.sock"},
		{"v26.6.10", "/run/moddengine", "http.sock"},
		{"pr-123", "/run/moddengine", "http.sock"},
	}
	for _, test := range tests {
		path, name := SocketLayout(test.version)
		if path != test.path || name != test.name {
			t.Errorf("SocketLayout(%q) = %q, %q; want %q, %q", test.version, path, name, test.path, test.name)
		}
	}
}

func TestLegacySocketPathBind(t *testing.T) {
	service := model.Service{
		ID: "whmcs-123", Version: "v26.1.14",
		Dataset: model.DatasetRecord{Mountpoint: "/modd/sites/whmcs-123"},
		Deploy:  map[string]model.Deploy{"blue": {Socket: "/modd/host/caddy/http/whmcs-123-blue/nginx.sock"}},
	}
	cfg := config.Docker{
		Network: "udo-net", ImageRepository: "whmcs-runtime", CertificateMountPath: "/srv/modd/secrets",
		Binds: []string{"/modd/host/caddy/http/{service_id}-{slot}:{socket_path}"},
	}
	_, host, _, err := ContainerSpec(cfg, service, "blue")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(host.Binds, "/modd/host/caddy/http/whmcs-123-blue:/run/nginx") {
		t.Fatalf("legacy socket bind missing: %#v", host.Binds)
	}
}

func TestPrepareBindSourcesCreatesAndOwnsSources(t *testing.T) {
	source := filepath.Join(t.TempDir(), "missing", "bind")
	identity := isolation.Identity{UID: os.Getuid(), GID: os.Getgid()}
	if err := prepareBindSources([]string{source + ":/srv/modd/cache"}, identity); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(source)
	if err != nil || !info.IsDir() {
		t.Fatalf("bind source was not created as a directory: %v", err)
	}
}

func TestPrepareBindSourcesDoesNotChownReadOnlySources(t *testing.T) {
	source := filepath.Join(t.TempDir(), "readonly")
	if err := os.Mkdir(source, 0750); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	identity := isolation.Identity{UID: os.Getuid() + 1, GID: os.Getgid() + 1}
	if err := prepareBindSources([]string{source + ":/srv/modd/secrets:ro,z"}, identity); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if before.Sys().(*syscall.Stat_t).Uid != after.Sys().(*syscall.Stat_t).Uid ||
		before.Sys().(*syscall.Stat_t).Gid != after.Sys().(*syscall.Stat_t).Gid {
		t.Fatal("read-only bind source ownership changed")
	}
}
