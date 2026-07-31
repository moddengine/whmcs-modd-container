package caddy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var socketPattern = regexp.MustCompile(`reverse_proxy unix/(/[^ \r\n}]+)`)

type Adapter struct {
	Dir             string
	SuspensionRoot  string
	ValidateCommand []string
	ReloadCommand   []string
}

func (a Adapter) Path(id string) string { return filepath.Join(a.Dir, id+".caddy") }

func (a Adapter) Active(ctx context.Context, id string, domains []string, socket string) error {
	return a.replace(ctx, id, fmt.Sprintf("%s {\n\treverse_proxy unix/%s\n}\n", strings.Join(domains, ", "), socket))
}

func (a Adapter) Suspended(ctx context.Context, id string, domains []string) error {
	content := fmt.Sprintf("%s {\n\troot * %s\n\ttry_files {path} /index.html\n\tfile_server\n}\n", strings.Join(domains, ", "), a.SuspensionRoot)
	return a.replace(ctx, id, content)
}

func (a Adapter) Remove(ctx context.Context, id string) error {
	path := a.Path(id)
	old, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	if err := a.apply(ctx); err != nil {
		_ = os.WriteFile(path, old, 0640)
		_ = a.apply(ctx)
		return err
	}
	return nil
}

func (a Adapter) Status(id string) (configured bool, mode, socket string, err error) {
	b, err := os.ReadFile(a.Path(id))
	if errors.Is(err, os.ErrNotExist) {
		return false, "", "", nil
	}
	if err != nil {
		return false, "", "", err
	}
	if match := socketPattern.FindSubmatch(b); len(match) == 2 {
		return true, "proxy", string(match[1]), nil
	}
	return true, "suspended", "", nil
}

func (a Adapter) replace(ctx context.Context, id, content string) error {
	if err := os.MkdirAll(a.Dir, 0750); err != nil {
		return err
	}
	path := a.Path(id)
	old, oldErr := os.ReadFile(path)
	tmp, err := os.CreateTemp(a.Dir, "."+id+".caddy-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err = tmp.WriteString(content); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmpPath, path); err != nil {
		return err
	}
	if err = a.apply(ctx); err != nil {
		if oldErr == nil {
			_ = os.WriteFile(path, old, 0640)
		} else {
			_ = os.Remove(path)
		}
		_ = a.apply(ctx)
		return err
	}
	return nil
}

func (a Adapter) apply(ctx context.Context) error {
	if err := run(ctx, a.ValidateCommand); err != nil {
		return fmt.Errorf("caddy validation: %w", err)
	}
	if err := run(ctx, a.ReloadCommand); err != nil {
		return fmt.Errorf("caddy reload: %w", err)
	}
	return nil
}

func run(ctx context.Context, command []string) error {
	if len(command) == 0 {
		return errors.New("command is not configured")
	}
	var output bytes.Buffer
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		text := output.String()
		if len(text) > 4096 {
			text = text[:4096]
		}
		return fmt.Errorf("%w: %s", err, text)
	}
	return nil
}
