package docker

import (
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
