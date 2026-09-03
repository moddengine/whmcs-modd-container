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

func TestLimitedBufferCapsOutput(t *testing.T) {
	buffer := limitedBuffer{limit: 4}
	if n, err := buffer.Write([]byte("abcdefgh")); err != nil || n != 8 {
		t.Fatalf("Write() = %d, %v", n, err)
	}
	if got := buffer.String(); got != "abcd" || !buffer.truncated {
		t.Fatalf("limited output = %q, truncated=%t", got, buffer.truncated)
	}
}
