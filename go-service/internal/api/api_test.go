package api

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
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
