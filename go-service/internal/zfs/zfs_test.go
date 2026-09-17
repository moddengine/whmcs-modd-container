package zfs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateScratch(t *testing.T) {
	bin := t.TempDir()
	zfs := filepath.Join(bin, "zfs")
	if err := os.WriteFile(zfs, []byte("#!/bin/sh\n[ \"$*\" = 'list -H -o mountpoint,mounted modd/scratch' ] || exit 2\nprintf '/modd/scratch\\tyes\\n'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	adapter := Adapter{ScratchDataset: "modd/scratch", ScratchMountpoint: "/modd/scratch"}
	if err := adapter.ValidateScratch(t.Context()); err != nil {
		t.Fatal(err)
	}
	adapter.ScratchMountpoint = "/wrong"
	if err := adapter.ValidateScratch(t.Context()); err == nil || !strings.Contains(err.Error(), "must be mounted") {
		t.Fatalf("unexpected mountpoint error: %v", err)
	}
}
