package isolation

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureAccountValidatesExistingIdentity(t *testing.T) {
	if err := EnsureAccount(context.Background(), Identity{Name: "root", UID: 0, GID: 0}, "/root"); err != nil {
		t.Fatal(err)
	}
	if err := EnsureAccount(context.Background(), Identity{Name: "root", UID: 12345, GID: 0}, "/root"); err == nil {
		t.Fatal("accepted an existing user with the wrong uid")
	}
}

func TestForService(t *testing.T) {
	identity, err := ForService("whmcs-123")
	if err != nil || identity.Name != "whmcs-123" || identity.UID != 10123 || identity.GID != 10123 {
		t.Fatalf("ForService() = %#v, %v", identity, err)
	}
	for _, id := range []string{"123", "whmcs-0", "whmcs-01", "whmcs-4294967295"} {
		if _, err := ForService(id); err == nil {
			t.Fatalf("ForService(%q) accepted an invalid id", id)
		}
	}
}

func TestChownTreeDoesNotFollowSymlinks(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file"), nil, 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	if err := ChownTree(root, Identity{UID: os.Getuid(), GID: os.Getgid()}); err != nil {
		t.Fatal(err)
	}
}
