package zfs

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type Adapter struct {
	Prefix      string
	MountPrefix string
}

func (a Adapter) Dataset(id string) string    { return a.Prefix + "/" + id }
func (a Adapter) Mountpoint(id string) string { return a.MountPrefix + "/" + id }

func (a Adapter) Exists(ctx context.Context, id string) (bool, error) {
	err := exec.CommandContext(ctx, "zfs", "list", "-H", "-o", "name", a.Dataset(id)).Run()
	if err == nil {
		return true, nil
	}
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

func (a Adapter) Create(ctx context.Context, id string) error {
	return exec.CommandContext(ctx, "zfs", "create", "-o", "mountpoint="+a.Mountpoint(id), a.Dataset(id)).Run()
}

func (a Adapter) Used(ctx context.Context, id string) (uint64, error) {
	out, err := exec.CommandContext(ctx, "zfs", "get", "-Hp", "-o", "value", "used", a.Dataset(id)).Output()
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
}

func (a Adapter) Destroy(ctx context.Context, id, dataset string) error {
	expected := a.Dataset(id)
	if dataset != expected || !strings.HasPrefix(dataset, a.Prefix+"/") {
		return fmt.Errorf("refusing to destroy unexpected dataset %q", dataset)
	}
	return exec.CommandContext(ctx, "zfs", "destroy", "-r", expected).Run()
}
