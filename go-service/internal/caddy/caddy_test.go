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
		ValidateCommand: []string{"true"}, ReloadCommand: []string{"true"},
	}
	socket := "/run/moddengine/whmcs-123-blue/http.sock"
	if err := adapter.Active(context.Background(), "whmcs-123", []string{"example.com", "example-com.staging.com"}, socket); err != nil {
		t.Fatal(err)
	}
	configured, mode, gotSocket, err := adapter.Status("whmcs-123")
	if err != nil || !configured || mode != "proxy" || gotSocket != socket {
		t.Fatalf("Status() = %v, %q, %q, %v", configured, mode, gotSocket, err)
	}
	content, _ := os.ReadFile(filepath.Join(dir, "whmcs-123.caddy"))
	if strings.Contains(string(content), "import") {
		t.Fatal("renderer allowed unexpected directives")
	}
}
