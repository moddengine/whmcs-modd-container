package caddy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderAndStatus(t *testing.T) {
	dir := t.TempDir()
	adapter := Adapter{
		Dir: dir, SuspensionRoot: "/srv/suspended",
		ActiveTemplate:  "{domain} {\n  reverse_proxy unix//modd/http/{service_id}-{slot}/{socket_name}\n}\n",
		ValidateCommand: []string{"true"}, ReloadCommand: []string{"true"},
	}
	socket := "/modd/http/whmcs-123-blue/http.sock"
	if err := adapter.Active(context.Background(), "whmcs-123", []string{"example.com", "example-com.staging.com"}, "blue", "http.sock"); err != nil {
		t.Fatal(err)
	}
	configured, mode, gotSocket, err := adapter.Status("whmcs-123")
	if err != nil || !configured || mode != "proxy" || gotSocket != socket {
		t.Fatalf("Status() = %v, %q, %q, %v", configured, mode, gotSocket, err)
	}
	content, _ := os.ReadFile(filepath.Join(dir, "whmcs-123.caddy"))
	if got, want := string(content), "example.com {\n  reverse_proxy unix//modd/http/whmcs-123-blue/http.sock\n}\nexample-com.staging.com {\n  reverse_proxy unix//modd/http/whmcs-123-blue/http.sock\n}\n"; got != want {
		t.Fatalf("rendered config = %q, want %q", got, want)
	}
	if strings.Contains(string(content), "import") {
		t.Fatal("renderer allowed unexpected directives")
	}
	mapping, _ := os.ReadFile(filepath.Join(dir, "whmcs-123.map"))
	if got, want := string(mapping), "example.com "+socket+"\nexample-com.staging.com "+socket+"\n"; got != want {
		t.Fatalf("rendered map = %q, want %q", got, want)
	}
	replacement := "/modd/http/whmcs-123-green/nginx.sock"
	if err := adapter.Active(context.Background(), "whmcs-123", []string{"example.com"}, "green", "nginx.sock"); err != nil {
		t.Fatal(err)
	}
	_, _, gotSocket, err = adapter.Status("whmcs-123")
	if err != nil || gotSocket != replacement {
		t.Fatalf("Active() did not replace stale config: %q, %v", gotSocket, err)
	}
	mapping, _ = os.ReadFile(filepath.Join(dir, "whmcs-123.map"))
	if got, want := string(mapping), "example.com "+replacement+"\n"; got != want {
		t.Fatalf("Active() did not replace stale map: %q, want %q", got, want)
	}
}

func TestHardcodedSocketNameRemainsSupported(t *testing.T) {
	adapter := Adapter{ActiveTemplate: "{domain} { reverse_proxy unix//run/{service_id}-{slot}/http.sock }"}
	if got := adapter.Socket("whmcs-123", "blue", "nginx.sock"); got != "/run/whmcs-123-blue/http.sock" {
		t.Fatalf("hardcoded socket changed: %q", got)
	}
}

func TestActiveSwapsExistingRoutesThroughAdminAPI(t *testing.T) {
	dir := t.TempDir()
	domains := []string{"example.com", "example-com.staging.com"}
	oldSocket := "/modd/http/whmcs-123-blue/http.sock"
	newSocket := "/modd/http/whmcs-123-green/http.sock"
	route := func(domain, socket string) map[string]any {
		return map[string]any{
			"match": []any{map[string]any{"host": []any{domain}}},
			"handle": []any{map[string]any{
				"handler": "subroute",
				"routes": []any{map[string]any{
					"handle": []any{map[string]any{
						"handler": "reverse_proxy", "upstreams": []any{map[string]any{"dial": "unix/" + socket}},
					}},
				}},
			}},
		}
	}
	var patched []any
	requests := 0
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/config/apps/http/servers":
			w.Header().Set("Etag", `"routes-v1"`)
			_ = json.NewEncoder(w).Encode(map[string]any{"srv0": map[string]any{"routes": []any{
				route(domains[0], oldSocket), route(domains[1], oldSocket), route("other.example", "/run/other.sock"),
			}}})
		case r.Method == http.MethodPatch && r.URL.Path == "/config/apps/http/servers/srv0/routes":
			if r.Header.Get("If-Match") != `"routes-v1"` {
				t.Errorf("If-Match = %q", r.Header.Get("If-Match"))
			}
			if err := json.NewDecoder(r.Body).Decode(&patched); err != nil {
				t.Error(err)
			}
		default:
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}))
	defer admin.Close()

	adapter := Adapter{
		Dir: dir, ActiveTemplate: "{domain} { reverse_proxy unix//modd/http/{service_id}-{slot}/{socket_name} }\n",
		ValidateCommand: []string{"true"}, ReloadCommand: []string{"true"},
	}
	if err := adapter.Active(t.Context(), "whmcs-123", domains, "blue", "http.sock"); err != nil {
		t.Fatal(err)
	}
	adapter.AdminURL, adapter.ReloadCommand = admin.URL, []string{"false"}
	if err := adapter.Active(t.Context(), "whmcs-123", domains, "green", "http.sock"); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(patched)
	if got := string(payload); requests != 2 || strings.Count(got, "unix/"+newSocket) != 2 || strings.Contains(got, "unix/"+oldSocket) {
		t.Fatalf("requests=%d patched routes=%s", requests, got)
	}
	mapping, _ := os.ReadFile(filepath.Join(dir, "whmcs-123.map"))
	if strings.Count(string(mapping), newSocket) != 2 {
		t.Fatalf("updated map = %q", mapping)
	}
}

func TestActiveFallsBackToReloadWhenAdminLookupFails(t *testing.T) {
	dir := t.TempDir()
	admin := httptest.NewServer(http.NotFoundHandler())
	defer admin.Close()
	marker := filepath.Join(dir, "reloaded")
	adapter := Adapter{
		Dir: dir, ActiveTemplate: "{domain} { reverse_proxy unix//run/{service_id}-{slot}/{socket_name} }\n",
		ValidateCommand: []string{"true"}, ReloadCommand: []string{"true"},
	}
	if err := adapter.Active(t.Context(), "whmcs-123", []string{"example.com"}, "blue", "http.sock"); err != nil {
		t.Fatal(err)
	}
	adapter.AdminURL, adapter.ReloadCommand = admin.URL, []string{"touch", marker}
	if err := adapter.Active(t.Context(), "whmcs-123", []string{"example.com"}, "green", "http.sock"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("reload was not called: %v", err)
	}
}

func TestLimitedBufferCapsOutput(t *testing.T) {
	buffer := limitedBuffer{limit: 4}
	if n, err := buffer.Write([]byte("abcdefgh")); err != nil || n != 8 {
		t.Fatalf("Write() = %d, %v", n, err)
	}
	if got := buffer.String(); got != "abcd" || !buffer.truncated {
		t.Fatalf("limited output = %q, truncated=%t", got, buffer.truncated)
	}
}
