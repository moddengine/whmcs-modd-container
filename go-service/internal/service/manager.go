package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moddengine/whmcs-container-controller/internal/caddy"
	"github.com/moddengine/whmcs-container-controller/internal/config"
	dockeradapter "github.com/moddengine/whmcs-container-controller/internal/docker"
	"github.com/moddengine/whmcs-container-controller/internal/healthcheck"
	"github.com/moddengine/whmcs-container-controller/internal/isolation"
	"github.com/moddengine/whmcs-container-controller/internal/metrics"
	"github.com/moddengine/whmcs-container-controller/internal/model"
	"github.com/moddengine/whmcs-container-controller/internal/notify"
	"github.com/moddengine/whmcs-container-controller/internal/state"
	"github.com/moddengine/whmcs-container-controller/internal/zfs"
)

var (
	serviceIDPattern = regexp.MustCompile(`^whmcs-[1-9][0-9]*$`)
	labelPattern     = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
)

type Error struct {
	Code   string
	Status int
	Err    error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

type Manager struct {
	Config  config.Config
	Repo    *state.Repository
	Docker  *dockeradapter.Adapter
	ZFS     zfs.Adapter
	Caddy   caddy.Adapter
	Health  healthcheck.Checker
	Metrics metrics.Provider
	Notify  notify.Notifier
	Logger  *slog.Logger
	// ponytail: serialize lifecycle writes; switch to per-service locks only if parallel upgrades are needed.
	mu sync.Mutex
}

func ValidateServiceID(id string) error {
	if !serviceIDPattern.MatchString(id) {
		return fmt.Errorf("service id must match %s", serviceIDPattern)
	}
	return nil
}

func NormalizeDomain(domain string) (string, error) {
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	if len(domain) == 0 || len(domain) > 253 {
		return "", errors.New("domain length is invalid")
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return "", errors.New("domain must contain at least two labels")
	}
	for _, label := range labels {
		if !labelPattern.MatchString(label) {
			return "", fmt.Errorf("invalid domain label %q", label)
		}
	}
	return domain, nil
}

func DeriveStaging(domain, suffix string) (string, error) {
	suffix, err := NormalizeDomain(suffix)
	if err != nil {
		return "", fmt.Errorf("invalid staging suffix: %w", err)
	}
	return NormalizeDomain(strings.ReplaceAll(domain, ".", "-") + "." + suffix)
}

func (m *Manager) Provision(ctx context.Context, id string, req model.ProvisionRequest, requestID string) (*model.Status, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ValidateServiceID(id); err != nil {
		return nil, false, badRequest(err)
	}
	mainDomain, err := NormalizeDomain(req.MainDomain)
	if err != nil {
		return nil, false, badRequest(err)
	}
	stagingDomain := req.StagingDomain
	if stagingDomain == "" {
		stagingDomain, err = DeriveStaging(mainDomain, m.Config.Domains.StagingSuffix)
	} else {
		stagingDomain, err = NormalizeDomain(stagingDomain)
	}
	if err != nil {
		return nil, false, badRequest(err)
	}
	if err := dockeradapter.ValidateVersion(req.Version); err != nil {
		return nil, false, badRequest(err)
	}
	if _, err := m.Repo.GetTombstone(id); err == nil {
		return nil, false, conflict(errors.New("deleted service cannot be reprovisioned while its tombstone exists"))
	} else if !errors.Is(err, state.ErrNotFound) {
		return nil, false, internal(err)
	}
	var retry *model.Service
	if existing, err := m.Repo.Get(id); err == nil {
		if existing.MainDomain == mainDomain && existing.StagingDomain == stagingDomain && existing.Version == req.Version {
			if existing.LastError == "" {
				status, statusErr := m.status(ctx, existing)
				return status, false, statusErr
			}
			retry = &existing
		} else {
			return nil, false, conflict(errors.New("service already exists with different provisioning data"))
		}
	} else if !errors.Is(err, state.ErrNotFound) {
		return nil, false, internal(err)
	}
	ok, err := m.Docker.HasVersion(ctx, req.Version)
	if err != nil {
		return nil, false, unavailable(err)
	}
	if !ok {
		return nil, false, notFound(errors.New("image version is not locally available"))
	}

	var service model.Service
	if retry != nil {
		service = *retry
	} else {
		now := time.Now().UTC()
		service = model.Service{
			ID: id, State: model.Active, MainDomain: mainDomain, StagingDomain: stagingDomain,
			DisplayName: req.DisplayName, Version: req.Version, LiveDeploy: "blue",
			CreatedAt: now, UpdatedAt: now,
			Dataset: model.DatasetRecord{Name: m.ZFS.Dataset(id), Mountpoint: m.ZFS.Mountpoint(id)},
			Paths:   model.PathRecord{Caddyfile: m.Caddy.Path(id)},
			Deploy: map[string]model.Deploy{
				"blue":  deployment(m.Config.Deployment.SocketRoot, id, "blue"),
				"green": deployment(m.Config.Deployment.SocketRoot, id, "green"),
			},
		}
		if err := m.Repo.Put(service); err != nil {
			return nil, false, internal(err)
		}
	}
	fail := func(operationErr error) (*model.Status, bool, error) {
		service.LastError = operationErr.Error()
		service.UpdatedAt = time.Now().UTC()
		_ = m.Repo.Put(service)
		m.sendNotification(ctx, "provision", false, service, requestID, operationErr.Error())
		return nil, false, unprocessable("provision_failed", operationErr)
	}
	exists, err := m.ZFS.Exists(ctx, id)
	if err != nil {
		return fail(err)
	}
	if !exists {
		if err := m.ZFS.Create(ctx, id); err != nil {
			return fail(err)
		}
	}
	if err := m.createSkeleton(service); err != nil {
		return fail(err)
	}
	if err := m.ensureIsolation(ctx, service); err != nil {
		return fail(err)
	}
	if err := m.Docker.StartSlot(ctx, service, "blue"); err != nil {
		return fail(err)
	}
	if err := m.Health.Check(ctx, service.Deploy["blue"].Socket); err != nil {
		return fail(err)
	}
	if err := m.Caddy.Active(ctx, id, []string{mainDomain, stagingDomain}, service.Deploy["blue"].Socket); err != nil {
		return fail(err)
	}
	service.LastError = ""
	service.UpdatedAt = time.Now().UTC()
	if err := m.Repo.Put(service); err != nil {
		return fail(err)
	}
	m.sendNotification(ctx, "provision", true, service, requestID, "")
	status, err := m.status(ctx, service)
	return status, retry == nil, err
}

func (m *Manager) Suspend(ctx context.Context, id, requestID string) (*model.Status, error) {
	return m.changeState(ctx, id, requestID, model.Suspended)
}

func (m *Manager) Resume(ctx context.Context, id, requestID string) (*model.Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	service, err := m.liveService(id)
	if err != nil {
		return nil, err
	}
	if service.State == model.Active {
		return m.status(ctx, service)
	}
	if service.State != model.Suspended && service.State != model.Terminated {
		return nil, conflict(fmt.Errorf("cannot resume service from %s", service.State))
	}
	if err := m.ensureIsolation(ctx, service); err != nil {
		return nil, unprocessable("isolation_failed", err)
	}
	if err := m.Docker.StartSlot(ctx, service, service.LiveDeploy); err != nil {
		return nil, unprocessable("resume_failed", err)
	}
	if err := m.Health.Check(ctx, service.Deploy[service.LiveDeploy].Socket); err != nil {
		return nil, unprocessable("health_check_failed", err)
	}
	if err := m.Caddy.Active(ctx, id, domains(service), service.Deploy[service.LiveDeploy].Socket); err != nil {
		return nil, unprocessable("caddy_failed", err)
	}
	service.State, service.LastError, service.UpdatedAt = model.Active, "", time.Now().UTC()
	if err := m.Repo.Put(service); err != nil {
		return nil, internal(err)
	}
	m.sendNotification(ctx, "resume", true, service, requestID, "")
	return m.status(ctx, service)
}

func (m *Manager) Terminate(ctx context.Context, id, requestID string) (*model.Status, error) {
	return m.changeState(ctx, id, requestID, model.Terminated)
}

func (m *Manager) changeState(ctx context.Context, id, requestID, target string) (*model.Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	service, err := m.liveService(id)
	if err != nil {
		return nil, err
	}
	switch target {
	case model.Suspended:
		if service.State != model.Active && service.State != model.Suspended {
			return nil, conflict(fmt.Errorf("cannot suspend service from %s", service.State))
		}
		if err := m.Docker.StopAll(ctx, id); err != nil {
			return nil, unprocessable("suspend_failed", err)
		}
		if err := m.Caddy.Suspended(ctx, id, domains(service)); err != nil {
			return nil, unprocessable("caddy_failed", err)
		}
	case model.Terminated:
		if service.State != model.Active && service.State != model.Suspended && service.State != model.Terminated {
			return nil, conflict(fmt.Errorf("cannot terminate service from %s", service.State))
		}
		if err := m.Docker.StopAll(ctx, id); err != nil {
			return nil, unprocessable("terminate_failed", err)
		}
		if err := m.Caddy.Remove(ctx, id); err != nil {
			return nil, unprocessable("caddy_failed", err)
		}
	}
	service.State, service.LastError, service.UpdatedAt = target, "", time.Now().UTC()
	if err := m.Repo.Put(service); err != nil {
		return nil, internal(err)
	}
	m.sendNotification(ctx, target, true, service, requestID, "")
	return m.status(ctx, service)
}

func (m *Manager) Delete(ctx context.Context, id, requestID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if tombstone, err := m.Repo.GetTombstone(id); err == nil && tombstone.State == model.Deleted {
		return m.Repo.DeleteLive(id)
	} else if err != nil && !errors.Is(err, state.ErrNotFound) {
		return internal(err)
	}
	service, err := m.liveService(id)
	if err != nil {
		return err
	}
	if service.State != model.Terminated {
		return conflict(errors.New("service must be terminated before deletion"))
	}
	if err := m.Docker.RemoveAll(ctx, id); err != nil {
		return unprocessable("delete_failed", err)
	}
	if err := m.Caddy.Remove(ctx, id); err != nil {
		return unprocessable("caddy_failed", err)
	}
	if err := m.removeSocketDirs(id); err != nil {
		return unprocessable("socket_cleanup_failed", err)
	}
	if err := m.ZFS.Destroy(ctx, id, service.Dataset.Name); err != nil {
		return unprocessable("zfs_destroy_failed", err)
	}
	if err := os.Remove(service.Dataset.Mountpoint); err != nil && !errors.Is(err, os.ErrNotExist) {
		return unprocessable("mountpoint_cleanup_failed", err)
	}
	identity, err := isolation.ForService(id)
	if err != nil {
		return badRequest(err)
	}
	if err := isolation.RemoveAccount(ctx, identity); err != nil {
		return unprocessable("account_cleanup_failed", err)
	}
	tombstone := model.Tombstone{
		ID: id, State: model.Deleted, MainDomain: service.MainDomain, StagingDomain: service.StagingDomain,
		LastVersion: service.Version, DeletedAt: time.Now().UTC(), FormerDataset: service.Dataset.Name,
	}
	if err := m.Repo.PutTombstone(tombstone); err != nil {
		return internal(err)
	}
	if err := m.Repo.DeleteLive(id); err != nil {
		return internal(err)
	}
	m.sendNotification(ctx, "delete", true, service, requestID, "")
	return nil
}

func (m *Manager) Upgrade(ctx context.Context, id string, req model.UpgradeRequest, requestID string) (*model.Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	service, err := m.liveService(id)
	if err != nil {
		return nil, err
	}
	if service.State != model.Active {
		return nil, conflict(errors.New("only active services can be upgraded"))
	}
	if err := dockeradapter.ValidateVersion(req.Version); err != nil {
		return nil, badRequest(err)
	}
	if req.Version == service.Version {
		return m.status(ctx, service)
	}
	available, err := m.Docker.HasVersion(ctx, req.Version)
	if err != nil {
		return nil, unavailable(err)
	}
	if !available {
		return nil, notFound(errors.New("image version is not locally available"))
	}
	cmp, ordered := compareVersions(req.Version, service.Version)
	if (!ordered || cmp < 0) && !req.ConfirmDowngrade {
		return nil, conflict(errors.New("possible downgrade requires confirm_downgrade=true"))
	}
	if err := m.ensureIsolation(ctx, service); err != nil {
		return nil, unprocessable("isolation_failed", err)
	}
	configured, mode, socket, err := m.Caddy.Status(id)
	if err != nil {
		return nil, internal(err)
	}
	if !configured || mode != "proxy" || socket != service.Deploy[service.LiveDeploy].Socket {
		if err := m.Caddy.Active(ctx, id, domains(service), service.Deploy[service.LiveDeploy].Socket); err != nil {
			return nil, internal(fmt.Errorf("restore Caddy live deployment: %w", err))
		}
	}
	target := opposite(service.LiveDeploy)
	updated := service
	updated.Version = req.Version
	if err := m.Docker.RemoveSlot(ctx, id, target); err != nil {
		return nil, m.upgradeFailure(ctx, service, requestID, err)
	}
	if err := os.Remove(updated.Deploy[target].Socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, unprocessable("stale_socket_remove_failed", err)
	}
	if err := m.Docker.StartSlot(ctx, updated, target); err != nil {
		return nil, m.upgradeFailure(ctx, service, requestID, err)
	}
	if err := m.Health.Check(ctx, updated.Deploy[target].Socket); err != nil {
		return nil, m.upgradeFailure(ctx, service, requestID, err)
	}
	if err := m.Caddy.Active(ctx, id, domains(updated), updated.Deploy[target].Socket); err != nil {
		return nil, m.upgradeFailure(ctx, service, requestID, err)
	}
	if err := wait(ctx, m.Config.Deployment.TrafficDrain); err != nil {
		return nil, m.upgradeFailure(ctx, service, requestID, err)
	}
	containers, err := m.Docker.Containers(ctx, id)
	if err != nil {
		return nil, m.upgradeFailure(ctx, service, requestID, err)
	}
	for _, container := range containers {
		if container.Deploy == service.LiveDeploy {
			if err := m.Docker.Remove(ctx, container.ID, id); err != nil {
				return nil, m.upgradeFailure(ctx, service, requestID, err)
			}
		}
	}
	updated.LiveDeploy, updated.LastError, updated.UpdatedAt = target, "", time.Now().UTC()
	if err := m.Repo.Put(updated); err != nil {
		return nil, internal(err)
	}
	return m.status(ctx, updated)
}

func (m *Manager) upgradeFailure(ctx context.Context, service model.Service, requestID string, err error) error {
	service.LastError, service.UpdatedAt = err.Error(), time.Now().UTC()
	_ = m.Repo.Put(service)
	m.sendNotification(ctx, "upgrade", false, service, requestID, err.Error())
	return unprocessable("upgrade_failed", err)
}

func (m *Manager) sendNotification(ctx context.Context, operation string, success bool, service model.Service, requestID, detail string) {
	if err := m.Notify.Send(ctx, operation, success, service, requestID, detail); err != nil && m.Logger != nil {
		m.Logger.Warn("notification failed", "operation", operation, "service_id", service.ID, "request_id", requestID, "error", err)
	}
}

func (m *Manager) Get(ctx context.Context, id string) (*model.Status, error) {
	if err := ValidateServiceID(id); err != nil {
		return nil, badRequest(err)
	}
	service, err := m.liveService(id)
	if err != nil {
		return nil, err
	}
	return m.status(ctx, service)
}

func (m *Manager) List(ctx context.Context) ([]model.Status, error) {
	services, err := m.Repo.List()
	if err != nil {
		return nil, internal(err)
	}
	result := make([]model.Status, 0, len(services))
	for _, service := range services {
		status, err := m.status(ctx, service)
		if err != nil {
			return nil, err
		}
		result = append(result, *status)
	}
	return result, nil
}

func (m *Manager) status(ctx context.Context, service model.Service) (*model.Status, error) {
	result := &model.Status{Service: service}
	exists, err := m.ZFS.Exists(ctx, service.ID)
	if err != nil {
		result.Warnings = append(result.Warnings, "ZFS status unavailable: "+err.Error())
	} else {
		result.DatasetExists = exists
		if exists {
			if result.DatasetUsed, err = m.ZFS.Used(ctx, service.ID); err != nil {
				result.Warnings = append(result.Warnings, "ZFS usage unavailable: "+err.Error())
			}
		}
	}
	result.Containers, err = m.Docker.Containers(ctx, service.ID)
	if err != nil {
		result.Warnings = append(result.Warnings, "Docker status unavailable: "+err.Error())
	}
	result.Caddy.Configured, result.Caddy.Mode, result.Caddy.Socket, err = m.Caddy.Status(service.ID)
	if err != nil {
		result.Warnings = append(result.Warnings, "Caddy status unavailable: "+err.Error())
	}
	result.Metrics, err = m.Metrics.GetServiceMetrics(ctx, service.ID)
	if err != nil {
		result.Metrics = model.Metrics{Source: "unavailable"}
		result.Warnings = append(result.Warnings, "metrics unavailable: "+err.Error())
	}
	running := 0
	for _, item := range result.Containers {
		if item.Running {
			running++
		}
		if item.Labels["au.modd.service-id"] != service.ID {
			result.Warnings = append(result.Warnings, "container label mismatch")
		}
	}
	if service.State == model.Active && running == 0 {
		result.Warnings = append(result.Warnings, "service is active but no container is running")
	}
	if running > 1 {
		result.Warnings = append(result.Warnings, "more than one deployment is running")
	}
	if service.State == model.Active && result.Caddy.Socket != service.Deploy[service.LiveDeploy].Socket {
		result.Warnings = append(result.Warnings, "Caddy and TOML live deployment disagree")
	}
	if !result.DatasetExists {
		result.Warnings = append(result.Warnings, "dataset is missing")
	}
	return result, nil
}

func (m *Manager) liveService(id string) (model.Service, error) {
	if err := ValidateServiceID(id); err != nil {
		return model.Service{}, badRequest(err)
	}
	service, err := m.Repo.Get(id)
	if errors.Is(err, state.ErrNotFound) {
		return service, notFound(err)
	}
	if err != nil {
		return service, internal(err)
	}
	return service, nil
}

func (m *Manager) createSkeleton(service model.Service) error {
	dirs := []string{
		"site/data", "backup", "shared/secrets",
		"blue/cache", "blue/run", "blue/debug", "green/cache", "green/run", "green/debug",
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(filepath.Join(service.Dataset.Mountpoint, dir), 0750); err != nil {
			return err
		}
	}
	for _, deploy := range service.Deploy {
		if err := os.MkdirAll(filepath.Dir(deploy.Socket), 0750); err != nil {
			return err
		}
	}
	for _, name := range []string{"conf.json", "plug.json"} {
		path := filepath.Join(service.Dataset.Mountpoint, "site", name)
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			content := []byte("{}\n")
			templatePath := filepath.Join(m.Config.State.TemplatesDir, name)
			if template, readErr := os.ReadFile(templatePath); readErr == nil {
				content = template
			}
			if err := os.WriteFile(path, content, 0640); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) ensureIsolation(ctx context.Context, service model.Service) error {
	identity, err := isolation.ForService(service.ID)
	if err != nil {
		return err
	}
	if err := isolation.EnsureAccount(ctx, identity, service.Dataset.Mountpoint); err != nil {
		return err
	}
	if err := isolation.ChownTree(service.Dataset.Mountpoint, identity); err != nil {
		return err
	}
	for _, slot := range []string{"blue", "green"} {
		dir := filepath.Dir(service.Deploy[slot].Socket)
		if err := os.MkdirAll(dir, 0750); err != nil {
			return err
		}
		if err := isolation.ChownTree(dir, identity); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) removeSocketDirs(id string) error {
	for _, slot := range []string{"blue", "green"} {
		if err := os.RemoveAll(filepath.Dir(deployment(m.Config.Deployment.SocketRoot, id, slot).Socket)); err != nil {
			return err
		}
	}
	return nil
}

func deployment(root, id, slot string) model.Deploy {
	return model.Deploy{
		Socket:    filepath.Join(root, id+"-"+slot, "http.sock"),
		Container: "moddengine_" + id + "_" + slot,
	}
}

func domains(service model.Service) []string {
	return []string{service.MainDomain, service.StagingDomain}
}

func opposite(slot string) string {
	if slot == "blue" {
		return "green"
	}
	return "blue"
}

func compareVersions(a, b string) (int, bool) {
	parse := func(value string) ([]int, bool) {
		value = strings.TrimPrefix(value, "v")
		parts := strings.Split(value, ".")
		result := make([]int, len(parts))
		for i, part := range parts {
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, false
			}
			result[i] = n
		}
		return result, true
	}
	ap, aok := parse(a)
	bp, bok := parse(b)
	if !aok || !bok {
		return 0, false
	}
	length := max(len(ap), len(bp))
	for i := 0; i < length; i++ {
		var av, bv int
		if i < len(ap) {
			av = ap[i]
		}
		if i < len(bp) {
			bv = bp[i]
		}
		if av < bv {
			return -1, true
		}
		if av > bv {
			return 1, true
		}
	}
	return 0, true
}

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func badRequest(err error) error { return &Error{Code: "invalid_request", Status: 400, Err: err} }
func notFound(err error) error   { return &Error{Code: "not_found", Status: 404, Err: err} }
func conflict(err error) error   { return &Error{Code: "conflict", Status: 409, Err: err} }
func unprocessable(code string, err error) error {
	return &Error{Code: code, Status: 422, Err: err}
}
func internal(err error) error { return &Error{Code: "internal_error", Status: 500, Err: err} }
func unavailable(err error) error {
	return &Error{Code: "dependency_unavailable", Status: 503, Err: err}
}
