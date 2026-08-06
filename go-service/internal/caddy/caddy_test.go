package caddy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderAndStatus(t *testing.T) {
	dir := t.TempDir()
	adapter := Adapter{
		Dir: dir, SuspensionRoot: "/srv/suspended",
		ActiveTemplate:  "{domain} {\n  reverse_proxy unix//modd/http/{service_id}-{slot}/http.sock\n}\n",
		ValidateCommand: []string{"true"}, ReloadCommand: []string{"true"},
	}
	socket := "/modd/http/whmcs-123-blue/http.sock"
	if err := adapter.Active(context.Background(), "whmcs-123", []string{"example.com", "example-com.staging.com"}, "blue"); err != nil {
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
	replacement := "/modd/http/whmcs-123-green/http.sock"
	if err := adapter.Active(context.Background(), "whmcs-123", []string{"example.com"}, "green"); err != nil {
		t.Fatal(err)
	}
	_, _, gotSocket, err = adapter.Status("whmcs-123")
	if err != nil || gotSocket != replacement {
		t.Fatalf("Active() did not replace stale config: %q, %v", gotSocket, err)
	}
}
