package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moddengine/whmcs-container-controller/internal/caddy"
	"github.com/moddengine/whmcs-container-controller/internal/config"
	"github.com/moddengine/whmcs-container-controller/internal/healthcheck"
	"github.com/moddengine/whmcs-container-controller/internal/model"
	"github.com/moddengine/whmcs-container-controller/internal/notify"
	"github.com/moddengine/whmcs-container-controller/internal/state"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func TestDeployDomainsReplacesRoutesAndPersistsState(t *testing.T) {
	root := t.TempDir()
	repo := state.New(filepath.Join(root, "services"), filepath.Join(root, "tombstones"))
	if err := repo.Init(); err != nil {
		t.Fatal(err)
	}
	adapter := caddy.Adapter{
		Dir: filepath.Join(root, "caddy"), SuspensionRoot: "/srv/suspended",
		ActiveTemplate:  "{domain} {\n  reverse_proxy unix//run/{service_id}-{slot}.sock\n}\n",
		ValidateCommand: []string{"true"}, ReloadCommand: []string{"true"},
	}
	service := model.Service{
		ID: "whmcs-123", State: model.Active, Phase: "running", LiveDeploy: "blue", Version: "v1",
		MainDomain: "old.example.com", StagingDomain: "old-example-com.staging.test",
	}
	if err := repo.Put(service); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Active(t.Context(), service.ID, domains(service), service.LiveDeploy); err != nil {
		t.Fatal(err)
	}
	manager := Manager{Repo: repo, Caddy: adapter}
	updated, err := manager.deployDomains(t.Context(), service.ID, "new.example.com", "new-example-com.staging.test")
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(adapter.Path(service.ID))
	if err != nil {
		t.Fatal(err)
	}
	if updated.MainDomain != "new.example.com" || !strings.Contains(string(content), "new.example.com") || strings.Contains(string(content), "old.example.com") {
		t.Fatalf("hostname was not reconciled: service=%#v caddy=%q", updated, content)
	}
	persisted, err := repo.Get(service.ID)
	if err != nil || persisted.MainDomain != updated.MainDomain || persisted.StagingDomain != updated.StagingDomain {
		t.Fatalf("deployed hostname was not persisted: %#v, %v", persisted, err)
	}
}

func TestDeployDomainsRollsBackFailedCaddyReload(t *testing.T) {
	root := t.TempDir()
	repo := state.New(filepath.Join(root, "services"), filepath.Join(root, "tombstones"))
	if err := repo.Init(); err != nil {
		t.Fatal(err)
	}
	adapter := caddy.Adapter{
		Dir: filepath.Join(root, "caddy"), ActiveTemplate: "{domain} {\n  reverse_proxy unix//run/{service_id}-{slot}.sock\n}\n",
		ValidateCommand: []string{"true"}, ReloadCommand: []string{"true"},
	}
	service := model.Service{
		ID: "whmcs-123", State: model.Active, Phase: "running", LiveDeploy: "blue", Version: "v1",
		MainDomain: "old.example.com", StagingDomain: "old-example-com.staging.test",
	}
	if err := repo.Put(service); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Active(t.Context(), service.ID, domains(service), service.LiveDeploy); err != nil {
		t.Fatal(err)
	}
	adapter.ReloadCommand = []string{"false"}
	manager := Manager{Repo: repo, Caddy: adapter}
	if _, err := manager.deployDomains(t.Context(), service.ID, "new.example.com", "new-example-com.staging.test"); err == nil {
		t.Fatal("hostname change succeeded despite failed Caddy reload")
	}
	content, _ := os.ReadFile(adapter.Path(service.ID))
	persisted, _ := repo.Get(service.ID)
	if persisted.MainDomain != service.MainDomain || !strings.Contains(string(content), service.MainDomain) || strings.Contains(string(content), "new.example.com") {
		t.Fatalf("failed hostname change was not rolled back: service=%#v caddy=%q", persisted, content)
	}
}

func TestDeployDomainsUpdatesSuspendedRoute(t *testing.T) {
	root := t.TempDir()
	repo := state.New(filepath.Join(root, "services"), filepath.Join(root, "tombstones"))
	if err := repo.Init(); err != nil {
		t.Fatal(err)
	}
	adapter := caddy.Adapter{Dir: filepath.Join(root, "caddy"), SuspensionRoot: "/srv/suspended", ValidateCommand: []string{"true"}, ReloadCommand: []string{"true"}}
	service := model.Service{ID: "whmcs-123", State: model.Suspended, Phase: "stopped", MainDomain: "old.example.com", StagingDomain: "old.staging.test"}
	if err := repo.Put(service); err != nil {
		t.Fatal(err)
	}
	manager := Manager{Repo: repo, Caddy: adapter}
	updated, err := manager.deployDomains(t.Context(), service.ID, "new.example.com", "new.staging.test")
	if err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(adapter.Path(service.ID))
	if updated.MainDomain != "new.example.com" || !strings.Contains(string(content), "new.example.com, new.staging.test") || strings.Contains(string(content), "reverse_proxy") {
		t.Fatalf("suspended hostname was not reconciled: service=%#v caddy=%q", updated, content)
	}
}

func TestRoutingDomainsUsesPendingHostname(t *testing.T) {
	service := model.Service{MainDomain: "old.example.com", StagingDomain: "old.staging.test", TargetMainDomain: "new.example.com", TargetStagingDomain: "new.staging.test"}
	if got := routingDomains(service, "upgrade"); got[0] != service.TargetMainDomain || got[1] != service.TargetStagingDomain {
		t.Fatalf("routingDomains() = %#v", got)
	}
	if got := routingDomains(service, "resume"); got[0] != service.MainDomain || got[1] != service.StagingDomain {
		t.Fatalf("resume used pending hostname: %#v", got)
	}
}

func TestCleanupOldDeploySurvivesRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	removed := false
	if err := cleanupOldDeploy(ctx, 0, func(ctx context.Context) error {
		removed = true
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("old deployment was not removed")
	}
}

func TestDeriveLegacyLifecycleState(t *testing.T) {
	active := model.Service{State: model.Active, Version: "v1", LiveDeploy: "blue", Deploy: map[string]model.Deploy{"blue": {}}}
	derive(&active)
	if active.Phase != "running" || active.Deploy["blue"].Version != "v1" || active.Deploy["blue"].Health != "unknown" {
		t.Fatalf("unexpected active defaults: %#v", active)
	}
	stopped := model.Service{State: model.Suspended}
	derive(&stopped)
	if stopped.Phase != "stopped" {
		t.Fatalf("phase = %q", stopped.Phase)
	}
}

func TestRecoverInterruptedMarksDeferredWorkFailed(t *testing.T) {
	root := t.TempDir()
	repo := state.New(filepath.Join(root, "services"), filepath.Join(root, "tombstones"))
	if err := repo.Init(); err != nil {
		t.Fatal(err)
	}
	if err := repo.Put(model.Service{ID: "whmcs-1", State: model.Active, Phase: "routing", Operation: "upgrade"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.PutTombstone(model.Tombstone{ID: "whmcs-2", State: model.Deleted, Phase: "deleting", Operation: "delete"}); err != nil {
		t.Fatal(err)
	}
	manager := Manager{Repo: repo}
	if err := manager.RecoverInterrupted(); err != nil {
		t.Fatal(err)
	}
	service, _ := repo.Get("whmcs-1")
	tombstone, _ := repo.GetTombstone("whmcs-2")
	if service.Phase != "failed" || tombstone.Phase != "failed" {
		t.Fatalf("service=%q tombstone=%q", service.Phase, tombstone.Phase)
	}
}

func TestDomainAndVersionHelpers(t *testing.T) {
	domain, err := NormalizeDomain("WWW.Example.COM.AU.")
	if err != nil || domain != "www.example.com.au" {
		t.Fatalf("NormalizeDomain() = %q, %v", domain, err)
	}
	staging, err := DeriveStaging(domain, "staging.com")
	if err != nil || staging != "www-example-com-au.staging.com" {
		t.Fatalf("DeriveStaging() = %q, %v", staging, err)
	}
	for _, invalid := range []string{"example", "bad value.com", "x;import.example"} {
		if _, err := NormalizeDomain(invalid); err == nil {
			t.Fatalf("NormalizeDomain(%q) accepted invalid input", invalid)
		}
	}
	if ip, err := NormalizePublicIPv4(" 203.0.113.10 "); err != nil || ip != "203.0.113.10" {
		t.Fatalf("NormalizePublicIPv4() = %q, %v", ip, err)
	}
	for _, invalid := range []string{"", "2001:db8::1", "999.0.0.1"} {
		if _, err := NormalizePublicIPv4(invalid); err == nil {
			t.Fatalf("NormalizePublicIPv4(%q) accepted invalid input", invalid)
		}
	}
	if comparison, ordered := compareVersions("v21.6.23", "v21.6.24"); !ordered || comparison >= 0 {
		t.Fatal("numeric downgrade was not detected")
	}
	if _, ordered := compareVersions("latest", "v21.6.24"); ordered {
		t.Fatal("unordered tag was treated as ordered")
	}
	service := model.Service{State: model.Active, Phase: "running", Version: "v2"}
	if _, err := validateUpgrade(service, model.UpgradeRequest{Version: "v1"}, false); err == nil {
		t.Fatal("Create-style upgrade accepted a downgrade without confirmation")
	}
	if done, err := validateUpgrade(service, model.UpgradeRequest{Version: "v2"}, false); err != nil || !done {
		t.Fatalf("ordinary same-version reconcile was not idempotent: done=%t err=%v", done, err)
	}
	if done, err := validateUpgrade(service, model.UpgradeRequest{Version: "v2"}, true); err != nil || done {
		t.Fatalf("forced same-version deploy was treated as complete: done=%t err=%v", done, err)
	}
	service.Phase, service.Operation, service.Version, service.TargetVersion = "waiting_for_health", "upgrade", "v1", "v2"
	if done, err := validateUpgrade(service, model.UpgradeRequest{Version: "v3"}, false); err != nil || done {
		t.Fatalf("in-progress unhealthy upgrade could not be superseded: done=%t err=%v", done, err)
	}
	service.Phase = "routing"
	if _, err := validateUpgrade(service, model.UpgradeRequest{Version: "v3"}, false); err == nil {
		t.Fatal("upgrade was superseded after routing started")
	}
}

func TestNormalizePackage(t *testing.T) {
	got, err := normalizePackage(map[string]string{"plan": "small|Small Hosting Plan", "email_sends": "500"})
	if err != nil || got["plan"] != "small|Small Hosting Plan" || got["email_sends"] != "500" {
		t.Fatalf("normalizePackage() = %#v, %v", got, err)
	}
	tooMany := make(map[string]string, maxPackageVariables+1)
	for i := range maxPackageVariables + 1 {
		tooMany[fmt.Sprintf("value_%d", i)] = "x"
	}
	for _, values := range []map[string]string{
		{"Bad Code": "x"},
		{"valid": "bad\x00value"},
		{"valid": strings.Repeat("x", maxPackageValueChars+1)},
		tooMany,
	} {
		if _, err := normalizePackage(values); err == nil {
			t.Fatalf("normalizePackage(%#v) accepted invalid input", values)
		}
	}
}

func TestProvisionRejectsInvalidPublicIPv4(t *testing.T) {
	manager := Manager{Config: config.Config{Domains: config.Domains{StagingSuffix: "staging.test"}}}
	_, _, err := manager.Provision(t.Context(), "whmcs-123", model.ProvisionRequest{
		MainDomain: "example.com", PublicIPv4: "2001:db8::1", Version: "v1",
	}, "request-1")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("Provision() error = %v", err)
	}
}

func TestDNSWebhookRendersJSONEscapedValues(t *testing.T) {
	var gotBody, gotAuthorization string
	manager := Manager{
		Config: config.Config{DNSWebhook: config.DNSWebhook{
			URL: "https://dns.example/hook", Timeout: time.Second,
			Body: "Authorization: Bearer token\nContent-Type: application/json\n\n{\"domain\":\"{domain}\",\"ipv4\":\"{ipv4}\"}",
		}},
		dnsHTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(request.Body)
			gotBody, gotAuthorization = string(body), request.Header.Get("Authorization")
			return &http.Response{StatusCode: 204, Status: "204 No Content", Body: io.NopCloser(bytes.NewReader(nil))}, nil
		})},
	}
	if err := manager.callDNSWebhook("quote\".example", "line\nfeed"); err != nil {
		t.Fatal(err)
	}
	if gotAuthorization != "Bearer token" || gotBody != `{"domain":"quote\".example","ipv4":"line\nfeed"}` {
		t.Fatalf("headers/body = %q / %q", gotAuthorization, gotBody)
	}
}

func TestDisabledDNSSkipsQueue(t *testing.T) {
	root := t.TempDir()
	repo := state.New(filepath.Join(root, "services"), filepath.Join(root, "tombstones"))
	if err := repo.Init(); err != nil {
		t.Fatal(err)
	}
	service := model.Service{ID: "whmcs-123", MainDomain: "example.com", PublicIPv4: "203.0.113.10"}
	if err := repo.Put(service); err != nil {
		t.Fatal(err)
	}
	manager := Manager{Repo: repo}
	got, err := manager.queueDNS(service)
	if err != nil || got.DNSStatus != "" {
		t.Fatalf("disabled DNS queue = %#v, %v", got, err)
	}
}

func TestDNSFailureDoesNotChangeRunningWorkload(t *testing.T) {
	repo := state.New(filepath.Join(t.TempDir(), "services"), filepath.Join(t.TempDir(), "tombstones"))
	if err := repo.Init(); err != nil {
		t.Fatal(err)
	}
	service := model.Service{ID: "whmcs-123", State: model.Active, Phase: "running", MainDomain: "example.com", PublicIPv4: "203.0.113.10", DNSStatus: "pending"}
	if err := repo.Put(service); err != nil {
		t.Fatal(err)
	}
	manager := Manager{
		Repo: repo, dnsBackoffs: []time.Duration{0},
		Config: config.Config{DNSWebhook: config.DNSWebhook{URL: "https://dns.example/hook", Timeout: time.Second, Body: "Content-Type: application/json\n\n{}"}},
		dnsHTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 500, Status: "500 Internal Server Error", Body: io.NopCloser(bytes.NewReader(nil))}, nil
		})},
	}
	manager.syncDNS(service.ID, service.MainDomain, service.PublicIPv4)
	got, err := repo.Get(service.ID)
	if err != nil || got.DNSStatus != "error" || got.DNSLastError == "" || got.State != model.Active || got.Phase != "running" {
		t.Fatalf("service after DNS failure = %#v, %v", got, err)
	}
	got.DNSStatus = "pending"
	if err := repo.Put(got); err != nil {
		t.Fatal(err)
	}
	manager.Config.DNSWebhook.Timeout = time.Millisecond
	manager.dnsHTTPClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	manager.syncDNS(service.ID, service.MainDomain, service.PublicIPv4)
	got, _ = repo.Get(service.ID)
	if got.DNSStatus != "error" || !strings.Contains(got.DNSLastError, "deadline exceeded") || got.Phase != "running" {
		t.Fatalf("service after DNS timeout = %#v", got)
	}
}

func TestDNSRetriesAndIdempotentQueue(t *testing.T) {
	root := t.TempDir()
	repo := state.New(filepath.Join(root, "services"), filepath.Join(root, "tombstones"))
	if err := repo.Init(); err != nil {
		t.Fatal(err)
	}
	service := model.Service{ID: "whmcs-123", State: model.Active, Phase: "running", MainDomain: "example.com", PublicIPv4: "203.0.113.10", DNSStatus: "error"}
	if err := repo.Put(service); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	manager := Manager{
		Repo: repo, dnsBackoffs: []time.Duration{0, 0, 0},
		Config: config.Config{DNSWebhook: config.DNSWebhook{URL: "https://dns.example/hook", Timeout: time.Second, Body: "Content-Type: application/json\n\n{}"}},
		dnsHTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			status := 500
			if calls.Add(1) >= 3 {
				status = 204
			}
			return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(bytes.NewReader(nil))}, nil
		})},
	}
	if _, err := manager.queueDNS(service); err != nil {
		t.Fatal(err)
	}
	synced := false
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		got, _ := repo.Get(service.ID)
		if got.DNSStatus == "in_sync" {
			if calls.Load() != 3 || got.DNSSyncedAt == "" {
				t.Fatalf("calls/status = %d / %#v", calls.Load(), got)
			}
			synced = true
			break
		}
	}
	if !synced {
		t.Fatal("asynchronous DNS result was not persisted")
	}
	latest, _ := repo.Get(service.ID)
	if _, err := manager.queueDNS(latest); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		latest, _ = repo.Get(service.ID)
		if calls.Load() == 4 && latest.DNSStatus == "in_sync" {
			break
		}
	}
	if calls.Load() != 4 || latest.DNSStatus != "in_sync" {
		t.Fatalf("successful DNS deployment was not safely repeated: calls=%d service=%#v", calls.Load(), latest)
	}
}

func TestReconnectDNSQueuesRepeatedRequests(t *testing.T) {
	root := t.TempDir()
	repo := state.New(filepath.Join(root, "services"), filepath.Join(root, "tombstones"))
	if err := repo.Init(); err != nil {
		t.Fatal(err)
	}
	service := model.Service{ID: "whmcs-123", MainDomain: "example.com", PublicIPv4: "203.0.113.10", DNSStatus: "error", DNSLastError: "previous failure"}
	if err := repo.Put(service); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}, 2), make(chan struct{}, 2)
	manager := Manager{
		Repo: repo, dnsBackoffs: []time.Duration{0},
		Config: config.Config{DNSWebhook: config.DNSWebhook{URL: "https://dns.example/hook", Timeout: time.Second, Body: "Content-Type: application/json\n\n{}"}},
		dnsHTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			started <- struct{}{}
			<-release
			return &http.Response{StatusCode: http.StatusNoContent, Status: "204 No Content", Body: io.NopCloser(bytes.NewReader(nil))}, nil
		})},
	}
	if err := manager.ReconnectDNS(service.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.ReconnectDNS(service.ID); err != nil {
		t.Fatal(err)
	}
	<-started
	<-started
	queued, _ := repo.Get(service.ID)
	if queued.DNSStatus != "pending" || queued.DNSLastError != "" {
		t.Fatalf("queued DNS state = %#v", queued)
	}
	release <- struct{}{}
	release <- struct{}{}
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		queued, _ = repo.Get(service.ID)
		if queued.DNSStatus == "in_sync" {
			return
		}
	}
	t.Fatalf("reconnected DNS state = %#v", queued)
}

func TestReconnectDNSRejectsDisabledAndMissingServices(t *testing.T) {
	root := t.TempDir()
	repo := state.New(filepath.Join(root, "services"), filepath.Join(root, "tombstones"))
	if err := repo.Init(); err != nil {
		t.Fatal(err)
	}
	if err := repo.Put(model.Service{ID: "whmcs-123"}); err != nil {
		t.Fatal(err)
	}
	manager := Manager{Repo: repo}
	for _, test := range []struct {
		id         string
		wantStatus int
	}{{"whmcs-123", http.StatusConflict}, {"whmcs-999", http.StatusNotFound}} {
		id, wantStatus := test.id, test.wantStatus
		if id == "whmcs-999" {
			manager.Config.DNSWebhook.URL = "https://dns.example/hook"
		}
		err := manager.ReconnectDNS(id)
		var apiErr *Error
		if !errors.As(err, &apiErr) || apiErr.Status != wantStatus {
			t.Fatalf("ReconnectDNS(%q) error = %v", id, err)
		}
	}
}

func TestInitialRoutingRecordsAsynchronousDNSResult(t *testing.T) {
	root := t.TempDir()
	socket := filepath.Join(root, "health.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })}
	go server.Serve(listener)
	t.Cleanup(func() { _ = server.Close() })
	repo := state.New(filepath.Join(root, "services"), filepath.Join(root, "tombstones"))
	if err := repo.Init(); err != nil {
		t.Fatal(err)
	}
	service := model.Service{
		ID: "whmcs-123", State: model.Active, Phase: "waiting_for_health", Operation: "provision", TargetDeploy: "blue", LiveDeploy: "blue",
		MainDomain: "example.com", StagingDomain: "example.staging.test", PublicIPv4: "203.0.113.10", DNSStatus: "pending",
		Deploy: map[string]model.Deploy{"blue": {Socket: socket, Health: "checking"}},
	}
	if err := repo.Put(service); err != nil {
		t.Fatal(err)
	}
	manager := Manager{
		Repo: repo, Health: healthcheck.Checker{Path: "/health", Attempts: 1}, Notify: notify.Disabled{}, dnsBackoffs: []time.Duration{0},
		Caddy:          caddy.Adapter{Dir: filepath.Join(root, "caddy"), ActiveTemplate: "{domain} {\n reverse_proxy unix/" + socket + "\n}\n", ValidateCommand: []string{"true"}, ReloadCommand: []string{"true"}},
		Config:         config.Config{DNSWebhook: config.DNSWebhook{URL: "https://dns.example/hook", Timeout: time.Second, Body: "Content-Type: application/json\n\n{}"}},
		healthAttempts: map[string]uint64{service.ID: 1},
		dnsHTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 204, Status: "204 No Content", Body: io.NopCloser(bytes.NewReader(nil))}, nil
		})},
	}
	manager.finishHealth(service, "provision", "request-1", 1)
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		got, _ := repo.Get(service.ID)
		if got.DNSStatus == "in_sync" {
			if got.Phase != "running" || got.Deploy["blue"].Health != "healthy" || got.DNSSyncedAt == "" {
				t.Fatalf("eventual service = %#v", got)
			}
			return
		}
	}
	t.Fatal("initial asynchronous DNS result was not recorded")
}

func TestResumeHealthSwitchesToOppositeSlot(t *testing.T) {
	root := t.TempDir()
	socket := filepath.Join(root, "green.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })}
	go server.Serve(listener)
	t.Cleanup(func() { _ = server.Close() })
	repo := state.New(filepath.Join(root, "services"), filepath.Join(root, "tombstones"))
	if err := repo.Init(); err != nil {
		t.Fatal(err)
	}
	service := model.Service{
		ID: "whmcs-123", State: model.Suspended, Phase: "waiting_for_health", Operation: "resume", LiveDeploy: "blue", TargetDeploy: "green", Version: "v1", TargetVersion: "v1",
		MainDomain: "example.com", StagingDomain: "example.staging.test", Package: map[string]string{"plan": "old"}, TargetPackage: map[string]string{"plan": "new"},
		Deploy: map[string]model.Deploy{"blue": {Health: "unknown"}, "green": {Socket: socket, Version: "v1", Health: "checking"}},
	}
	if err := repo.Put(service); err != nil {
		t.Fatal(err)
	}
	manager := Manager{
		Repo: repo, Health: healthcheck.Checker{Path: "/health", Attempts: 1}, Notify: notify.Disabled{}, healthAttempts: map[string]uint64{service.ID: 1},
		Caddy: caddy.Adapter{Dir: filepath.Join(root, "caddy"), ActiveTemplate: "{domain} {\n reverse_proxy unix/" + socket + "\n}\n", ValidateCommand: []string{"true"}, ReloadCommand: []string{"true"}},
	}
	manager.finishHealth(service, "resume", "request-1", 1)
	got, err := repo.Get(service.ID)
	if err != nil || got.State != model.Active || got.Phase != "running" || got.LiveDeploy != "green" || got.Package["plan"] != "new" || got.TargetPackage != nil {
		t.Fatalf("resumed service = %#v, %v", got, err)
	}
}

func TestSupersededHealthResultIsIgnored(t *testing.T) {
	root := t.TempDir()
	socket := filepath.Join(root, "health.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })}
	go server.Serve(listener)
	t.Cleanup(func() { _ = server.Close() })
	repo := state.New(filepath.Join(root, "services"), filepath.Join(root, "tombstones"))
	if err := repo.Init(); err != nil {
		t.Fatal(err)
	}
	service := model.Service{
		ID: "whmcs-123", State: model.Active, Phase: "waiting_for_health", Operation: "upgrade", LiveDeploy: "blue", TargetDeploy: "green", Version: "v1", TargetVersion: "v3",
		Deploy: map[string]model.Deploy{"green": {Socket: socket, Version: "v3", Health: "checking"}},
	}
	if err := repo.Put(service); err != nil {
		t.Fatal(err)
	}
	manager := Manager{Repo: repo, Health: healthcheck.Checker{Path: "/health", Attempts: 1}, healthAttempts: map[string]uint64{service.ID: 2}}
	manager.finishHealth(service, "upgrade", "old-request", 1)
	got, err := repo.Get(service.ID)
	if err != nil || got.Phase != "waiting_for_health" || got.TargetVersion != "v3" || got.Deploy["green"].Health != "checking" {
		t.Fatalf("replacement was overwritten by stale health result: %#v, %v", got, err)
	}
}

func TestServiceID(t *testing.T) {
	if err := ValidateServiceID("whmcs-123"); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"123", "whmcs-0", "whmcs-1/../../x"} {
		if ValidateServiceID(invalid) == nil {
			t.Fatalf("accepted invalid service ID %q", invalid)
		}
	}
}

func TestDeploymentContainerName(t *testing.T) {
	if got := deployment("/run/whmcs/{service_id}-{slot}/http.sock", "whmcs-123", "blue").Container; got != "WHMCS-123-blue" {
		t.Fatalf("unexpected container name %q", got)
	}
}

func TestRemoveSocketDirs(t *testing.T) {
	root := t.TempDir()
	socket := filepath.Join(root, "{service_id}-{slot}", "http.sock")
	manager := Manager{Config: config.Config{Deployment: config.Deployment{Socket: socket}}}
	for _, slot := range []string{"blue", "green"} {
		dir := filepath.Dir(deployment(socket, "whmcs-123", slot).Socket)
		if err := os.MkdirAll(dir, 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "http.sock"), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	sibling := filepath.Join(root, "whmcs-123-other")
	if err := os.Mkdir(sibling, 0750); err != nil {
		t.Fatal(err)
	}
	if err := manager.removeSocketDirs("whmcs-123"); err != nil {
		t.Fatal(err)
	}
	for _, slot := range []string{"blue", "green"} {
		if _, err := os.Stat(deployment(socket, "whmcs-123", slot).Socket); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s socket was not removed", slot)
		}
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Fatal("socket cleanup removed an unrelated directory")
	}
}

func TestRemoveSocketDirsPreservesSharedDirectory(t *testing.T) {
	root := t.TempDir()
	socket := filepath.Join(root, "{slot}", "{service_id}.sock")
	manager := Manager{Config: config.Config{Deployment: config.Deployment{Socket: socket}}}
	for _, path := range []string{
		deployment(socket, "whmcs-123", "blue").Socket,
		deployment(socket, "whmcs-123", "green").Socket,
		deployment(socket, "whmcs-456", "blue").Socket,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.removeSocketDirs("whmcs-123"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(deployment(socket, "whmcs-456", "blue").Socket); err != nil {
		t.Fatal("socket cleanup removed another service's socket")
	}
}

func TestRemoveSocketDirsWithInvertedTemplate(t *testing.T) {
	root := t.TempDir()
	socket := filepath.Join(root, "{slot}", "{service_id}", "http.sock")
	manager := Manager{Config: config.Config{Deployment: config.Deployment{Socket: socket}}}
	for _, slot := range []string{"blue", "green"} {
		path := deployment(socket, "whmcs-123", slot).Socket
		if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	other := deployment(socket, "whmcs-456", "blue").Socket
	if err := os.MkdirAll(filepath.Dir(other), 0750); err != nil {
		t.Fatal(err)
	}
	if err := manager.removeSocketDirs("whmcs-123"); err != nil {
		t.Fatal(err)
	}
	for _, slot := range []string{"blue", "green"} {
		if _, err := os.Stat(filepath.Dir(deployment(socket, "whmcs-123", slot).Socket)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s socket directory was not removed", slot)
		}
	}
	if _, err := os.Stat(filepath.Dir(other)); err != nil {
		t.Fatal("socket cleanup removed another service's directory")
	}
}
