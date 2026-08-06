package service

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/moddengine/whmcs-container-controller/internal/config"
)

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
	if comparison, ordered := compareVersions("v21.6.23", "v21.6.24"); !ordered || comparison >= 0 {
		t.Fatal("numeric downgrade was not detected")
	}
	if _, ordered := compareVersions("latest", "v21.6.24"); ordered {
		t.Fatal("unordered tag was treated as ordered")
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
