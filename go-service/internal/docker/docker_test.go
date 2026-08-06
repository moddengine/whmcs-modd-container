package docker

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/client"
	"github.com/moddengine/whmcs-container-controller/internal/config"
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
		Deploy:  map[string]model.Deploy{"blue": {Socket: "/run/moddengine/whmcs-123-blue/http.sock"}},
	}
	cfg := config.Docker{
		Network: "udo-net", ImageRepository: "moddengine/moddengine", RestartPolicy: "always",
		Binds:       []string{"{mountpoint}/{slot}/cache:/cache"},
		Environment: []string{"SERVICE={service_id}", "DATA={mountpoint}", "DEPLOY={slot}"},
	}
	spec, host, networking, err := ContainerSpec(cfg, service, "blue")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Image != "moddengine/moddengine:v21.6.24" ||
		spec.User != "10123:10123" ||
		spec.Labels[serviceLabel] != "whmcs-123" ||
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
	if !slices.Contains(host.Binds, "/modd/sites/whmcs-123/blue/cache:/cache") ||
		!slices.Contains(host.Binds, "/run/moddengine/whmcs-123-blue:/run/moddengine/whmcs-123-blue") {
		t.Fatalf("unexpected binds: %#v", host.Binds)
	}
	if networking.EndpointsConfig["udo-net"] == nil {
		t.Fatal("configured network missing")
	}
}
