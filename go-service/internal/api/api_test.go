package api

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "controller.log")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 300; i++ {
		fmt.Fprintln(file, i)
	}
	file.Close()
	lines, err := Tail(path, 250, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 250 || lines[0] != "50" || lines[249] != "299" {
		t.Fatalf("unexpected tail: count=%d first=%q last=%q", len(lines), lines[0], lines[len(lines)-1])
	}
	missing, err := Tail(path+".missing", 250, 1024)
	if err != nil || len(missing) != 0 {
		t.Fatalf("missing file: %#v, %v", missing, err)
	}
}

func TestMonitorTokenScopeTamperingAndExpiry(t *testing.T) {
	secret := "controller-secret"
	token, err := signMonitorToken(monitorClaims{ServiceID: "whmcs-405", Origin: "https://billing.example", Expires: time.Now().Add(time.Hour).Unix()}, secret)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := verifyMonitorToken(token, secret)
	if err != nil || claims.ServiceID != "whmcs-405" || claims.Origin != "https://billing.example" {
		t.Fatalf("unexpected claims: %#v, %v", claims, err)
	}
	if _, err := verifyMonitorToken(token+"x", secret); err == nil {
		t.Fatal("accepted tampered token")
	}
	expired, _ := signMonitorToken(monitorClaims{ServiceID: "whmcs-405", Origin: "https://billing.example", Expires: time.Now().Add(-time.Second).Unix()}, secret)
	if _, err := verifyMonitorToken(expired, secret); err == nil {
		t.Fatal("accepted expired token")
	}
	if _, err := validOrigin("https://billing.example/path"); err == nil {
		t.Fatal("accepted an origin with a path")
	}
}
