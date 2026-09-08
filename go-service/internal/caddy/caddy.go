package caddy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	AdminURL        string
	ValidateCommand []string
	ReloadCommand   []string
}

func (a Adapter) Path(id string) string { return filepath.Join(a.Dir, id+".caddy") }

func (a Adapter) mapPath(id string) string { return filepath.Join(a.Dir, id+".map") }

func (a Adapter) Active(ctx context.Context, id string, domains []string, slot, socketName string) error {
	var content strings.Builder
	var mapping strings.Builder
	socket := a.Socket(id, slot, socketName)
	for _, domain := range domains {
		content.WriteString(strings.NewReplacer(
			"{domain}", domain,
			"{service_id}", id,
			"{slot}", slot,
			"{socket_name}", socketName,
		).Replace(a.ActiveTemplate))
		fmt.Fprintf(&mapping, "%s %s\n", domain, socket)
	}
	return a.replace(ctx, id, content.String(), mapping.String())
}

func (a Adapter) Socket(id, slot, socketName string) string {
	content := strings.NewReplacer("{service_id}", id, "{slot}", slot, "{socket_name}", socketName).Replace(a.ActiveTemplate)
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
	if err := a.validate(ctx); err != nil {
		restoreFiles(paths, old, oldErr)
		_ = a.apply(ctx)
		return err
	}
	if a.AdminURL != "" && oldErr[0] == nil && oldErr[1] == nil {
		if domains, oldSocket, newSocket, ok := socketSwap(old[1], []byte(mapping)); ok {
			if err := a.swapRoutes(ctx, domains, oldSocket, newSocket); err == nil {
				return nil
			}
		}
	}
	if err := a.reload(ctx); err != nil {
		restoreFiles(paths, old, oldErr)
		_ = a.apply(ctx)
		return err
	}
	return nil
}

func socketSwap(old, next []byte) (map[string]struct{}, string, string, bool) {
	parse := func(data []byte) (map[string]string, bool) {
		fields := strings.Fields(string(data))
		if len(fields) == 0 || len(fields)%2 != 0 {
			return nil, false
		}
		mapping := make(map[string]string, len(fields)/2)
		for i := 0; i < len(fields); i += 2 {
			if _, exists := mapping[fields[i]]; exists {
				return nil, false
			}
			mapping[fields[i]] = fields[i+1]
		}
		return mapping, true
	}
	oldMap, ok := parse(old)
	if !ok {
		return nil, "", "", false
	}
	newMap, ok := parse(next)
	if !ok || len(oldMap) != len(newMap) {
		return nil, "", "", false
	}
	domains := make(map[string]struct{}, len(oldMap))
	oldSocket, newSocket := "", ""
	for domain, socket := range oldMap {
		nextSocket, exists := newMap[domain]
		if !exists || socket == "" || nextSocket == "" {
			return nil, "", "", false
		}
		if oldSocket == "" {
			oldSocket, newSocket = socket, nextSocket
		} else if socket != oldSocket || nextSocket != newSocket {
			return nil, "", "", false
		}
		domains[domain] = struct{}{}
	}
	return domains, oldSocket, newSocket, oldSocket != newSocket
}

func (a Adapter) swapRoutes(ctx context.Context, domains map[string]struct{}, oldSocket, newSocket string) error {
	endpoint := strings.TrimRight(a.AdminURL, "/") + "/config/apps/http/servers"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	response.Body.Close()
	if readErr != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("caddy config lookup returned %s", response.Status)
	}
	etag := response.Header.Get("Etag")
	if etag == "" {
		return errors.New("caddy config lookup omitted Etag")
	}
	var servers map[string]struct {
		Routes []any `json:"routes"`
	}
	if err := json.Unmarshal(body, &servers); err != nil {
		return err
	}
	serverName := ""
	var replacement []any
	for name, server := range servers {
		if replaceRoutes(server.Routes, domains, "unix/"+oldSocket, "unix/"+newSocket) {
			if serverName != "" {
				return errors.New("Caddy routes matched more than one server")
			}
			serverName, replacement = name, server.Routes
		}
	}
	if serverName == "" {
		return errors.New("Caddy routes did not match the hostname map")
	}
	payload, err := json.Marshal(replacement)
	if err != nil {
		return err
	}
	endpoint += "/" + url.PathEscape(serverName) + "/routes"
	req, err = http.NewRequestWithContext(ctx, http.MethodPatch, endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", etag)
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("caddy route update returned %s", response.Status)
	}
	return nil
}

func replaceRoutes(routes []any, domains map[string]struct{}, oldDial, newDial string) bool {
	matches := make(map[string]int, len(domains))
	for _, value := range routes {
		route, ok := value.(map[string]any)
		if !ok {
			continue
		}
		hosts := routeHosts(route)
		if len(hosts) == 0 {
			continue
		}
		for _, host := range hosts {
			if _, ok := domains[host]; !ok {
				hosts = nil
				break
			}
		}
		if len(hosts) == 0 || replaceDials(route["handle"], oldDial, oldDial) != 1 {
			continue
		}
		replaceDials(route["handle"], oldDial, newDial)
		for _, host := range hosts {
			matches[host]++
		}
	}
	if len(matches) != len(domains) {
		return false
	}
	for domain := range domains {
		if matches[domain] != 1 {
			return false
		}
	}
	return true
}

func routeHosts(route map[string]any) []string {
	var hosts []string
	for _, value := range values(route["match"]) {
		matcher, ok := value.(map[string]any)
		if !ok {
			continue
		}
		for _, host := range values(matcher["host"]) {
			if host, ok := host.(string); ok {
				hosts = append(hosts, host)
			}
		}
	}
	return hosts
}

func replaceDials(value any, oldDial, newDial string) int {
	count := 0
	switch value := value.(type) {
	case []any:
		for _, item := range value {
			count += replaceDials(item, oldDial, newDial)
		}
	case map[string]any:
		if value["handler"] == "reverse_proxy" {
			for _, upstream := range values(value["upstreams"]) {
				if upstream, ok := upstream.(map[string]any); ok && upstream["dial"] == oldDial {
					upstream["dial"] = newDial
					count++
				}
			}
		}
		for key, item := range value {
			if key != "upstreams" {
				count += replaceDials(item, oldDial, newDial)
			}
		}
	}
	return count
}

func values(value any) []any {
	values, _ := value.([]any)
	return values
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
	if err := a.validate(ctx); err != nil {
		return err
	}
	return a.reload(ctx)
}

func (a Adapter) validate(ctx context.Context) error {
	if err := run(ctx, a.ValidateCommand); err != nil {
		return fmt.Errorf("caddy validation: %w", err)
	}
	return nil
}

func (a Adapter) reload(ctx context.Context) error {
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
