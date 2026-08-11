package docker

import (
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
	version, err := latestHubVersion(t.Context(), hub.Client(), hub.URL, "moddengine/whmcs")
	if err != nil || version != "v3" || page != 2 {
		t.Fatalf("latestHubVersion() = %q after %d pages: %v", version, page, err)
	}

	var pulledImage, pulledTag string
	dockerAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pulledImage, pulledTag = r.URL.Query().Get("fromImage"), r.URL.Query().Get("tag")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "downloaded"})
	}))
	defer dockerAPI.Close()
	client, err := client.NewClientWithOpts(client.WithHost(dockerAPI.URL), client.WithHTTPClient(dockerAPI.Client()), client.WithVersion("1.52"))
	if err != nil {
		t.Fatal(err)
	}
	pulled, err := (&Adapter{client: client, cfg: config.Docker{ImageRepository: "moddengine/whmcs"}}).Pull(t.Context(), "pr-123")
	if err != nil || pulled.Version != "pr-123" || pulledImage != "docker.io/moddengine/whmcs" || pulledTag != "pr-123" {
		t.Fatalf("Pull() = %#v, image=%q tag=%q: %v", pulled, pulledImage, pulledTag, err)
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
		ID: "whmcs-123", MainDomain: "example.com", Version: "v21.6.24",
		Dataset: model.DatasetRecord{Mountpoint: "/modd/sites/whmcs-123"},
		Deploy:  map[string]model.Deploy{"blue": {Socket: "/run/whmcs/whmcs-123-blue/http.sock"}},
	}
	cfg := config.Docker{
		Network: "udo-net", ImageRepository: "whmcs-runtime",
		Binds:       []string{"{mountpoint}/{slot}/cache:/cache"},
		Environment: []string{"SERVICE={service_id}", "DATA={mountpoint}", "DEPLOY={slot}"},
	}
	spec, host, networking, err := ContainerSpec(cfg, service, "blue")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Image != "whmcs-runtime:v21.6.24" ||
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
	if !slices.Equal(host.Binds, []string{
		"/modd/sites/whmcs-123/blue/cache:/cache",
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
