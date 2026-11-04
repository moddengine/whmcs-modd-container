package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moddengine/whmcs-container-controller/internal/caddy"
	"github.com/moddengine/whmcs-container-controller/internal/config"
	dockeradapter "github.com/moddengine/whmcs-container-controller/internal/docker"
	"github.com/moddengine/whmcs-container-controller/internal/metrics"
	"github.com/moddengine/whmcs-container-controller/internal/model"
	"github.com/moddengine/whmcs-container-controller/internal/notify"
	"github.com/moddengine/whmcs-container-controller/internal/state"
)

func TestMatomoLifecycleCache(t *testing.T) {
	assigned, cleared, unchanged := uint32(42), uint32(0), uint32(7)
	for _, tc := range []struct {
		name, state, phase, operation string
		requestID                     *uint32
		resume, force, recreate       bool
		want                          uint32
	}{
		{name: "initial", requestID: &assigned, recreate: true, want: 42},
		{name: "provision retry omitted", state: model.Active, phase: "failed", operation: "provision", recreate: true, want: 7},
		{name: "provision retry changed", state: model.Active, phase: "failed", operation: "provision", requestID: &assigned, recreate: true, want: 42},
		{name: "deploy changed", state: model.Active, phase: "running", requestID: &assigned, recreate: true, want: 42},
		{name: "deploy cleared", state: model.Active, phase: "running", requestID: &cleared, recreate: true},
		{name: "deploy omitted", state: model.Active, phase: "running", want: 7},
		{name: "deploy unchanged", state: model.Active, phase: "running", requestID: &unchanged, want: 7},
		{name: "force omitted", state: model.Active, phase: "running", force: true, recreate: true, want: 7},
		{name: "failed same-version retry", state: model.Active, phase: "failed", operation: "upgrade", recreate: true, want: 7},
		{name: "busy rejects changed", state: model.Active, phase: "starting", operation: "upgrade", requestID: &assigned, want: 7},
		{name: "suspended deploy caches", state: model.Suspended, phase: "stopped", requestID: &assigned, want: 42},
		{name: "terminated restore changed", state: model.Terminated, phase: "stopped", requestID: &assigned, recreate: true, want: 42},
		{name: "terminated restore omitted", state: model.Terminated, phase: "stopped", recreate: true, want: 7},
		{name: "resume changed", state: model.Suspended, phase: "stopped", resume: true, requestID: &assigned, recreate: true, want: 42},
		{name: "resume cleared", state: model.Suspended, phase: "stopped", resume: true, requestID: &cleared, recreate: true},
		{name: "resume omitted", state: model.Suspended, phase: "stopped", resume: true, recreate: true, want: 7},
		{name: "active resume changed", state: model.Active, phase: "running", resume: true, requestID: &assigned, recreate: true, want: 42},
		{name: "busy resume rejects changed", state: model.Suspended, phase: "starting", operation: "resume", resume: true, requestID: &assigned, want: 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			repo := state.New(filepath.Join(root, "services"), filepath.Join(root, "tombstones"))
			if err := repo.Init(); err != nil {
				t.Fatal(err)
			}
			// Stop safely after lifecycle persistence: this valid API ID cannot map to a Unix account.
			service := model.Service{ID: "whmcs-4294967295", MatomoID: 7, MainDomain: "example.com", PublicIPv4: "203.0.113.10", Version: "v1", TargetVersion: "v1", LiveDeploy: "blue", State: tc.state, Phase: tc.phase, Operation: tc.operation,
				Deploy: map[string]model.Deploy{"blue": {}, "green": {}}}
			if tc.state != "" {
				if err := repo.Put(service); err != nil {
					t.Fatal(err)
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/images/json") {
					io.WriteString(w, `[{"RepoTags":["runtime:v1"]}]`)
				} else {
					io.WriteString(w, `[]`)
				}
			}))
			defer server.Close()
			t.Setenv("DOCKER_HOST", server.URL)
			t.Setenv("DOCKER_API_VERSION", "1.52")
			// Initial provisioning also stops before touching host datasets.
			if err := os.WriteFile(filepath.Join(root, "zfs"), []byte("#!/bin/sh\nexit 2\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", root+":"+os.Getenv("PATH"))
			docker, err := dockeradapter.New(config.Docker{ImageRepository: "runtime"})
			if err != nil {
				t.Fatal(err)
			}
			defer docker.Close()
			manager := Manager{Repo: repo, Docker: docker, Metrics: metrics.Mock{}, Notify: notify.Disabled{}, Caddy: caddy.Adapter{Dir: filepath.Join(root, "caddy")}}
			if tc.resume {
				_, _, err = manager.Resume(t.Context(), service.ID, model.ResumeRequest{MatomoID: tc.requestID}, "test")
			} else {
				_, _, err = manager.Provision(t.Context(), service.ID, model.ProvisionRequest{MainDomain: service.MainDomain, PublicIPv4: service.PublicIPv4, Version: "v1", MatomoID: tc.requestID, ForceRedeploy: tc.force}, "test")
			}
			got, readErr := repo.Get(service.ID)
			if readErr != nil || got.MatomoID != tc.want {
				t.Fatalf("cached ID = %d, want %d: %v (operation: %v)", got.MatomoID, tc.want, readErr, err)
			}
			if tc.recreate && (err == nil || got.Phase != "failed" || got.LastError == "") {
				t.Fatalf("container recreation was not attempted: %#v, %v", got, err)
			}
			if !tc.recreate && tc.phase != "starting" && err != nil {
				t.Fatalf("unchanged or suspended deploy failed: %v", err)
			}
			if tc.phase == "starting" && err == nil {
				t.Fatal("busy operation was accepted")
			}
		})
	}
}
