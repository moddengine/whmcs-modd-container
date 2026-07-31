package docker

import (
	"slices"
	"testing"

	"github.com/moddengine/whmcs-container-controller/internal/config"
	"github.com/moddengine/whmcs-container-controller/internal/model"
)

func TestContainerSpec(t *testing.T) {
	service := model.Service{
		ID: "whmcs-123", MainDomain: "example.com", Version: "v21.6.24",
		Dataset: model.DatasetRecord{Mountpoint: "/modd/sites/whmcs-123"},
		Deploy:  map[string]model.Deploy{"blue": {Socket: "/run/moddengine/whmcs-123-blue/http.sock"}},
	}
	cfg := config.Docker{
		Network: "udo-net", ImageRepository: "moddengine/moddengine", RestartPolicy: "always",
		Binds: []string{"{mountpoint}/{slot}/cache:/cache"},
	}
	spec, host, networking := ContainerSpec(cfg, service, "blue")
	if spec.Image != "moddengine/moddengine:v21.6.24" ||
		spec.Labels[serviceLabel] != "whmcs-123" ||
		spec.Labels[deployLabel] != "blue" {
		t.Fatalf("unexpected container spec: %#v", spec)
	}
	if !slices.Contains(spec.Env, "ME_SITE=whmcs-123") {
		t.Fatalf("ME_SITE must contain the service ID: %#v", spec.Env)
	}
	if !slices.Contains(host.Binds, "/modd/sites/whmcs-123/blue/cache:/cache") ||
		!slices.Contains(host.Binds, "/run/moddengine/whmcs-123-blue:/run/moddengine/whmcs-123-blue") {
		t.Fatalf("unexpected binds: %#v", host.Binds)
	}
	if networking.EndpointsConfig["udo-net"] == nil {
		t.Fatal("configured network missing")
	}
}
