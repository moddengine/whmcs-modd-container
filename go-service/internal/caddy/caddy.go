package caddy

import (
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
	ActiveTemplate  string
	ValidateCommand []string
	ReloadCommand   []string
}

func (a Adapter) Path(id string) string { return filepath.Join(a.Dir, id+".caddy") }

func (a Adapter) mapPath(id string) string { return filepath.Join(a.Dir, id+".map") }

func (a Adapter) Active(ctx context.Context, id string, domains []string, slot string) error {
	var content strings.Builder
	var mapping strings.Builder
	socket := a.Socket(id, slot)
	for _, domain := range domains {
		content.WriteString(strings.NewReplacer(
			"{domain}", domain,
			"{service_id}", id,
			"{slot}", slot,
		).Replace(a.ActiveTemplate))
		fmt.Fprintf(&mapping, "%s %s\n", domain, socket)
	}
	return a.replace(ctx, id, content.String(), mapping.String())
}

func (a Adapter) Socket(id, slot string) string {
	content := strings.NewReplacer("{service_id}", id, "{slot}", slot).Replace(a.ActiveTemplate)
	if match := socketPattern.FindStringSubmatch(content); len(match) == 2 {
		return match[1]
	}
	return ""
}

func (a Adapter) Suspended(ctx context.Context, id string, domains []string) error {
	content := fmt.Sprintf("%s {\n\troot * %s\n\ttry_files {path} /index.html\n\tfile_server\n}\n", strings.Join(domains, ", "), a.SuspensionRoot)
	return a.replace(ctx, id, content, "")
}

func (a Adapter) Remove(ctx context.Context, id string) error {
	_, caddyErr := os.Stat(a.Path(id))
	_, mapErr := os.Stat(a.mapPath(id))
	if errors.Is(caddyErr, os.ErrNotExist) && errors.Is(mapErr, os.ErrNotExist) {
		return nil
	}
	if caddyErr != nil && !errors.Is(caddyErr, os.ErrNotExist) {
		return caddyErr
	}
	if mapErr != nil && !errors.Is(mapErr, os.ErrNotExist) {
		return mapErr
	}
	return a.replace(ctx, id, "", "")
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

func (a Adapter) replace(ctx context.Context, id, content, mapping string) error {
	if err := os.MkdirAll(a.Dir, 0750); err != nil {
		return err
	}
	paths := []string{a.Path(id), a.mapPath(id)}
	contents := []string{content, mapping}
	old := make([][]byte, len(paths))
	oldErr := make([]error, len(paths))
	for i, path := range paths {
		old[i], oldErr[i] = os.ReadFile(path)
		if err := replaceFile(path, contents[i]); err != nil {
			restoreFiles(paths[:i], old[:i], oldErr[:i])
			return err
		}
	}
	if err := a.apply(ctx); err != nil {
		restoreFiles(paths, old, oldErr)
		_ = a.apply(ctx)
		return err
	}
	return nil
}

func replaceFile(path, content string) error {
	if content == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
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
	return os.Rename(tmpPath, path)
}

func restoreFiles(paths []string, contents [][]byte, readErrs []error) {
	for i, path := range paths {
		if readErrs[i] == nil {
			_ = os.WriteFile(path, contents[i], 0640)
		} else {
			_ = os.Remove(path)
		}
	}
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
	output := limitedBuffer{limit: 4096}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		text := output.String()
		if output.truncated {
			text += "\n[output truncated]"
		}
		return fmt.Errorf("%w: %s", err, text)
	}
	return nil
}

type limitedBuffer struct {
	buf       []byte
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - len(b.buf)
	if remaining > 0 {
		b.buf = append(b.buf, p[:min(remaining, len(p))]...)
	}
	b.truncated = b.truncated || len(p) > remaining
	return n, nil
}

func (b *limitedBuffer) String() string { return string(b.buf) }
