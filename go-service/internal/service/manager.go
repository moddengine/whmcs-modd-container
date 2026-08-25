package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/textproto"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
	mu            sync.Mutex
	dnsBackoffs   []time.Duration
	dnsHTTPClient *http.Client
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

func NormalizePublicIPv4(value string) (string, error) {
	ip, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil || !ip.Is4() {
		return "", errors.New("public_ipv4 must be an IPv4 address")
	}
	return ip.String(), nil
}

func (m *Manager) Provision(ctx context.Context, id string, req model.ProvisionRequest, requestID string) (*model.Status, bool, error) {
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
	publicIPv4, err := NormalizePublicIPv4(req.PublicIPv4)
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
	m.mu.Lock()
	existing, existingErr := m.Repo.Get(id)
	if existingErr == nil {
		derive(&existing)
	}
	m.mu.Unlock()
	if existingErr == nil && !(existing.Phase == "failed" && existing.Operation == "provision" && existing.MainDomain == mainDomain && existing.StagingDomain == stagingDomain && existing.PublicIPv4 == publicIPv4 && existing.Version == req.Version) {
		return m.reconcile(ctx, id, mainDomain, stagingDomain, publicIPv4, req.Version, req.DisplayName, requestID)
	}
	if existingErr != nil && !errors.Is(existingErr, state.ErrNotFound) {
		return nil, false, internal(existingErr)
	}
	ok, err := m.Docker.HasVersion(ctx, req.Version)
	if err != nil {
		return nil, false, unavailable(err)
	}
	if !ok {
		return nil, false, notFound(errors.New("image version is not locally available"))
	}
	m.mu.Lock()
	var retry *model.Service
	if existing, err := m.Repo.Get(id); err == nil {
		if existing.MainDomain == mainDomain && existing.StagingDomain == stagingDomain && existing.PublicIPv4 == publicIPv4 && existing.Version == req.Version {
			derive(&existing)
			if existing.Phase == "running" {
				m.mu.Unlock()
				status, statusErr := m.status(ctx, existing)
				return status, false, statusErr
			}
			if busy(existing.Phase) {
				m.mu.Unlock()
				return nil, false, conflict(fmt.Errorf("%s is already in progress", existing.Operation))
			}
			if existing.Phase == "failed" && existing.Operation != "provision" {
				m.mu.Unlock()
				return nil, false, conflict(errors.New("service already exists; retry its failed lifecycle operation"))
			}
			retry = &existing
		} else {
			m.mu.Unlock()
			return nil, false, conflict(errors.New("service already exists with different provisioning data"))
		}
	} else if !errors.Is(err, state.ErrNotFound) {
		m.mu.Unlock()
		return nil, false, internal(err)
	}

	var service model.Service
	if retry != nil {
		service = *retry
		service.Phase, service.Operation, service.TargetDeploy, service.TargetVersion = "provisioning", "provision", "blue", req.Version
	} else {
		now := time.Now().UTC()
		service = model.Service{
			ID: id, State: model.Active, MainDomain: mainDomain, StagingDomain: stagingDomain, PublicIPv4: publicIPv4,
			DisplayName: req.DisplayName, Version: req.Version, LiveDeploy: "blue", Phase: "provisioning", Operation: "provision", TargetDeploy: "blue", TargetVersion: req.Version,
			CreatedAt: now, UpdatedAt: now,
			Dataset: model.DatasetRecord{Name: m.ZFS.Dataset(id), Mountpoint: m.ZFS.Mountpoint(id)},
			Paths:   model.PathRecord{Caddyfile: m.Caddy.Path(id)},
			Deploy: map[string]model.Deploy{
				"blue":  deployment(m.Config.Deployment.Socket, id, "blue", req.Version),
				"green": deployment(m.Config.Deployment.Socket, id, "green", ""),
			},
		}
		if m.dnsEnabled() {
			service.DNSStatus = "pending"
		}
		if err := m.Repo.Put(service); err != nil {
			m.mu.Unlock()
			return nil, false, internal(err)
		}
	}
	if retry != nil {
		service.LastError, service.UpdatedAt = "", time.Now().UTC()
		if err := m.Repo.Put(service); err != nil {
			m.mu.Unlock()
			return nil, false, internal(err)
		}
	}
	m.mu.Unlock()
	fail := func(operationErr error) (*model.Status, bool, error) {
		m.failSync(service, requestID, operationErr)
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
	service.Phase, service.Deploy["blue"] = "waiting_for_health", withHealth(service.Deploy["blue"], "checking")
	service.UpdatedAt = time.Now().UTC()
	m.mu.Lock()
	if err := m.Repo.Put(service); err != nil {
		m.mu.Unlock()
		return fail(err)
	}
	m.mu.Unlock()
	go m.finishHealth(id, "provision", requestID)
	status, err := m.status(ctx, service)
	return status, true, err
}

func (m *Manager) reconcile(ctx context.Context, id, mainDomain, stagingDomain, publicIPv4, version, displayName, requestID string) (*model.Status, bool, error) {
	m.mu.Lock()
	service, err := m.liveService(id)
	if err != nil {
		m.mu.Unlock()
		return nil, false, err
	}
	derive(&service)
	state, phase := service.State, service.Phase
	sameVersion := service.Version == version
	m.mu.Unlock()

	if state == model.Active {
		if !sameVersion {
			return m.upgrade(ctx, id, model.UpgradeRequest{Version: version}, requestID, mainDomain, stagingDomain, publicIPv4)
		}
		if phase != "running" {
			return nil, false, conflict(fmt.Errorf("cannot deploy hostname while service is %s", phase))
		}
	} else if state == model.Suspended {
		if phase != "stopped" {
			return nil, false, conflict(fmt.Errorf("cannot deploy hostname while service is %s", phase))
		}
	} else if state == model.Terminated {
		if phase != "stopped" {
			return nil, false, conflict(fmt.Errorf("cannot redeploy service while it is %s", phase))
		}
		available, err := m.Docker.HasVersion(ctx, version)
		if err != nil {
			return nil, false, unavailable(err)
		}
		if !available {
			return nil, false, notFound(errors.New("image version is not locally available"))
		}
		m.mu.Lock()
		service, err = m.liveService(id)
		if err == nil && service.State == model.Terminated && service.Phase == "stopped" {
			service.MainDomain, service.StagingDomain, service.PublicIPv4 = mainDomain, stagingDomain, publicIPv4
			service.Version, service.DisplayName, service.UpdatedAt = version, displayName, time.Now().UTC()
			service.Deploy[service.LiveDeploy] = deployment(m.Config.Deployment.Socket, id, service.LiveDeploy, version)
			err = m.Repo.Put(service)
		} else if err == nil {
			err = conflict(errors.New("service state changed during redeployment"))
		}
		m.mu.Unlock()
		if err != nil {
			return nil, false, internal(err)
		}
		return m.Resume(ctx, id, requestID)
	} else {
		return nil, false, conflict(fmt.Errorf("cannot deploy hostname for %s service", state))
	}
	return m.changeDomains(ctx, id, mainDomain, stagingDomain, publicIPv4)
}

func (m *Manager) changeDomains(ctx context.Context, id, mainDomain, stagingDomain, publicIPv4 string) (*model.Status, bool, error) {
	service, err := m.deployDomains(ctx, id, mainDomain, stagingDomain, publicIPv4)
	if err != nil {
		return nil, false, err
	}
	status, statusErr := m.status(ctx, service)
	return status, false, statusErr
}

func (m *Manager) deployDomains(ctx context.Context, id, mainDomain, stagingDomain string, requestedIPv4 ...string) (model.Service, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	service, err := m.liveService(id)
	if err != nil {
		return service, err
	}
	derive(&service)
	publicIPv4 := service.PublicIPv4
	queueDNS := len(requestedIPv4) != 0 && m.dnsEnabled()
	if queueDNS {
		publicIPv4 = requestedIPv4[0]
	}
	oldDomains := domains(service)
	apply := m.Caddy.Active
	if service.State == model.Suspended && service.Phase == "stopped" {
		apply = func(ctx context.Context, id string, domains []string, _ string) error {
			return m.Caddy.Suspended(ctx, id, domains)
		}
	} else if service.State != model.Active || service.Phase != "running" {
		return service, conflict(fmt.Errorf("cannot deploy hostname while service is %s", service.Phase))
	}
	domainsChanged := service.MainDomain != mainDomain || service.StagingDomain != stagingDomain
	if domainsChanged {
		if err := apply(ctx, id, []string{mainDomain, stagingDomain}, service.LiveDeploy); err != nil {
			return service, unprocessable("hostname_change_failed", err)
		}
	}
	service.MainDomain, service.StagingDomain, service.PublicIPv4, service.UpdatedAt = mainDomain, stagingDomain, publicIPv4, time.Now().UTC()
	if queueDNS {
		service.DNSStatus, service.DNSLastError = "pending", ""
	}
	if err := m.Repo.Put(service); err != nil {
		if domainsChanged {
			if rollbackErr := apply(ctx, id, oldDomains, service.LiveDeploy); rollbackErr != nil {
				return service, internal(fmt.Errorf("persist hostname change: %w; Caddy rollback failed: %v", err, rollbackErr))
			}
		}
		return service, internal(fmt.Errorf("persist hostname change: %w", err))
	}
	if queueDNS {
		go m.syncDNS(id, mainDomain, publicIPv4)
	}
	return service, nil
}

func (m *Manager) Suspend(ctx context.Context, id, requestID string) (*model.Status, bool, error) {
	return m.changeState(ctx, id, requestID, model.Suspended)
}

func (m *Manager) Resume(ctx context.Context, id, requestID string) (*model.Status, bool, error) {
	m.mu.Lock()
	service, err := m.liveService(id)
	if err != nil {
		m.mu.Unlock()
		return nil, false, err
	}
	derive(&service)
	if service.State == model.Active && service.Phase == "running" {
		m.mu.Unlock()
		service, err = m.queueDNS(service)
		if err != nil {
			return nil, false, internal(err)
		}
		status, err := m.status(ctx, service)
		return status, false, err
	}
	if service.State != model.Suspended && service.State != model.Terminated {
		m.mu.Unlock()
		return nil, false, conflict(fmt.Errorf("cannot resume service from %s", service.State))
	}
	if busy(service.Phase) {
		m.mu.Unlock()
		return nil, false, conflict(fmt.Errorf("%s is already in progress", service.Operation))
	}
	service.Phase, service.Operation, service.TargetDeploy, service.TargetVersion = "starting", "resume", service.LiveDeploy, service.Version
	service.TargetMainDomain, service.TargetStagingDomain = "", ""
	service.UpdatedAt = time.Now().UTC()
	if err := m.Repo.Put(service); err != nil {
		m.mu.Unlock()
		return nil, false, internal(err)
	}
	m.mu.Unlock()
	if err := m.ensureIsolation(ctx, service); err != nil {
		m.failSync(service, requestID, err)
		return nil, false, unprocessable("isolation_failed", err)
	}
	if err := m.Docker.StartSlot(ctx, service, service.LiveDeploy); err != nil {
		m.failSync(service, requestID, err)
		return nil, false, unprocessable("resume_failed", err)
	}
	service.State, service.Phase, service.UpdatedAt = model.Active, "waiting_for_health", time.Now().UTC()
	service.Deploy[service.LiveDeploy] = withHealth(service.Deploy[service.LiveDeploy], "checking")
	m.mu.Lock()
	err = m.Repo.Put(service)
	m.mu.Unlock()
	if err != nil {
		m.failSync(service, requestID, err)
		return nil, false, internal(err)
	}
	go m.finishHealth(id, "resume", requestID)
	status, err := m.status(ctx, service)
	return status, true, err
}

func (m *Manager) Terminate(ctx context.Context, id, requestID string) (*model.Status, bool, error) {
	return m.changeState(ctx, id, requestID, model.Terminated)
}

func (m *Manager) changeState(ctx context.Context, id, requestID, target string) (*model.Status, bool, error) {
	m.mu.Lock()
	service, err := m.liveService(id)
	if err != nil {
		m.mu.Unlock()
		return nil, false, err
	}
	derive(&service)
	if service.State == target && service.Phase == "stopped" {
		m.mu.Unlock()
		status, err := m.status(ctx, service)
		return status, false, err
	}
	if busy(service.Phase) {
		m.mu.Unlock()
		return nil, false, conflict(fmt.Errorf("%s is already in progress", service.Operation))
	}
	switch target {
	case model.Suspended:
		if service.State != model.Active && service.State != model.Suspended {
			m.mu.Unlock()
			return nil, false, conflict(fmt.Errorf("cannot suspend service from %s", service.State))
		}
	case model.Terminated:
		if service.State != model.Active && service.State != model.Suspended && service.State != model.Terminated {
			m.mu.Unlock()
			return nil, false, conflict(fmt.Errorf("cannot terminate service from %s", service.State))
		}
	}
	service.Phase, service.Operation, service.TargetMainDomain, service.TargetStagingDomain, service.UpdatedAt = "stopping", target, "", "", time.Now().UTC()
	if err := m.Repo.Put(service); err != nil {
		m.mu.Unlock()
		return nil, false, internal(err)
	}
	m.mu.Unlock()
	if target == model.Suspended {
		err = m.Docker.StopAll(ctx, id)
	} else {
		err = m.Docker.RemoveAll(ctx, id)
	}
	if err != nil {
		m.failSync(service, requestID, err)
		return nil, false, unprocessable(target+"_failed", err)
	}
	service.State, service.Phase, service.LastError, service.UpdatedAt = target, "routing", "", time.Now().UTC()
	m.mu.Lock()
	err = m.Repo.Put(service)
	m.mu.Unlock()
	if err != nil {
		m.failSync(service, requestID, err)
		return nil, false, internal(err)
	}
	go m.finishRouting(id, target, requestID)
	status, err := m.status(ctx, service)
	return status, true, err
}

func (m *Manager) Delete(ctx context.Context, id, requestID string) (*model.Tombstone, bool, error) {
	m.mu.Lock()
	if tombstone, err := m.Repo.GetTombstone(id); err == nil {
		if tombstone.Phase == "" || tombstone.Phase == "deleted" {
			m.mu.Unlock()
			return &tombstone, false, m.Repo.DeleteLive(id)
		}
		if busy(tombstone.Phase) {
			m.mu.Unlock()
			return nil, false, conflict(errors.New("delete is already in progress"))
		}
		tombstone.Phase, tombstone.Operation, tombstone.LastError, tombstone.UpdatedAt = "deleting", "delete", "", time.Now().UTC()
		if err := m.Repo.PutTombstone(tombstone); err != nil {
			m.mu.Unlock()
			return nil, false, internal(err)
		}
		m.mu.Unlock()
		service := model.Service{ID: id, State: model.Deleted, MainDomain: tombstone.MainDomain, StagingDomain: tombstone.StagingDomain, Version: tombstone.LastVersion}
		go m.finishDelete(tombstone, service, requestID)
		return &tombstone, true, nil
	} else if err != nil && !errors.Is(err, state.ErrNotFound) {
		m.mu.Unlock()
		return nil, false, internal(err)
	}
	service, err := m.liveService(id)
	if err != nil {
		m.mu.Unlock()
		return nil, false, err
	}
	derive(&service)
	if service.State != model.Terminated {
		m.mu.Unlock()
		return nil, false, conflict(errors.New("service must be terminated before deletion"))
	}
	if busy(service.Phase) {
		m.mu.Unlock()
		return nil, false, conflict(fmt.Errorf("%s is already in progress", service.Operation))
	}
	service.Phase, service.Operation, service.UpdatedAt = "deleting", "delete", time.Now().UTC()
	if err := m.Repo.Put(service); err != nil {
		m.mu.Unlock()
		return nil, false, internal(err)
	}
	m.mu.Unlock()
	if err := m.Docker.RemoveAll(ctx, id); err != nil {
		m.failSync(service, requestID, err)
		return nil, false, unprocessable("delete_failed", err)
	}
	if err := m.removeSocketDirs(id); err != nil {
		m.failSync(service, requestID, err)
		return nil, false, unprocessable("socket_cleanup_failed", err)
	}
	if err := m.ZFS.Destroy(ctx, id, service.Dataset.Name); err != nil {
		m.failSync(service, requestID, err)
		return nil, false, unprocessable("zfs_destroy_failed", err)
	}
	if err := os.Remove(service.Dataset.Mountpoint); err != nil && !errors.Is(err, os.ErrNotExist) {
		m.failSync(service, requestID, err)
		return nil, false, unprocessable("mountpoint_cleanup_failed", err)
	}
	identity, err := isolation.ForService(id)
	if err != nil {
		return nil, false, badRequest(err)
	}
	if err := isolation.RemoveAccount(ctx, identity); err != nil {
		m.failSync(service, requestID, err)
		return nil, false, unprocessable("account_cleanup_failed", err)
	}
	tombstone := model.Tombstone{
		ID: id, State: model.Deleted, MainDomain: service.MainDomain, StagingDomain: service.StagingDomain,
		LastVersion: service.Version, DeletedAt: time.Now().UTC(), FormerDataset: service.Dataset.Name, Phase: "deleting", Operation: "delete", UpdatedAt: time.Now().UTC(),
	}
	m.mu.Lock()
	if err := m.Repo.PutTombstone(tombstone); err != nil {
		m.mu.Unlock()
		return nil, false, internal(err)
	}
	if err := m.Repo.DeleteLive(id); err != nil {
		m.mu.Unlock()
		return nil, false, internal(err)
	}
	m.mu.Unlock()
	go m.finishDelete(tombstone, service, requestID)
	return &tombstone, true, nil
}

func (m *Manager) Upgrade(ctx context.Context, id string, req model.UpgradeRequest, requestID string) (*model.Status, bool, error) {
	return m.upgrade(ctx, id, req, requestID, "", "", "")
}

func (m *Manager) upgrade(ctx context.Context, id string, req model.UpgradeRequest, requestID, targetMainDomain, targetStagingDomain, targetPublicIPv4 string) (*model.Status, bool, error) {
	if err := dockeradapter.ValidateVersion(req.Version); err != nil {
		return nil, false, badRequest(err)
	}
	m.mu.Lock()
	service, err := m.liveService(id)
	if err != nil {
		m.mu.Unlock()
		return nil, false, err
	}
	derive(&service)
	done, validationErr := validateUpgrade(service, req)
	m.mu.Unlock()
	if validationErr != nil {
		return nil, false, validationErr
	}
	if done {
		service, err = m.queueDNS(service)
		if err != nil {
			return nil, false, internal(err)
		}
		status, err := m.status(ctx, service)
		return status, false, err
	}
	available, err := m.Docker.HasVersion(ctx, req.Version)
	if err != nil {
		return nil, false, unavailable(err)
	}
	if !available {
		return nil, false, notFound(errors.New("image version is not locally available"))
	}
	m.mu.Lock()
	service, err = m.liveService(id)
	if err != nil {
		m.mu.Unlock()
		return nil, false, err
	}
	derive(&service)
	done, validationErr = validateUpgrade(service, req)
	if validationErr != nil {
		m.mu.Unlock()
		return nil, false, validationErr
	}
	if done {
		m.mu.Unlock()
		service, err = m.queueDNS(service)
		if err != nil {
			return nil, false, internal(err)
		}
		status, err := m.status(ctx, service)
		return status, false, err
	}
	target := opposite(service.LiveDeploy)
	service.Phase, service.Operation, service.TargetDeploy, service.TargetVersion, service.UpdatedAt = "starting", "upgrade", target, req.Version, time.Now().UTC()
	service.TargetMainDomain, service.TargetStagingDomain = targetMainDomain, targetStagingDomain
	service.TargetPublicIPv4 = targetPublicIPv4
	service.Deploy[target] = deployment(m.Config.Deployment.Socket, id, target, req.Version)
	if err := m.Repo.Put(service); err != nil {
		m.mu.Unlock()
		return nil, false, internal(err)
	}
	m.mu.Unlock()
	if err := m.ensureIsolation(ctx, service); err != nil {
		m.failSync(service, requestID, err)
		return nil, false, unprocessable("isolation_failed", err)
	}
	configured, mode, socket, err := m.Caddy.Status(id)
	if err != nil {
		m.failSync(service, requestID, err)
		return nil, false, internal(err)
	}
	if !configured || mode != "proxy" || socket != m.Caddy.Socket(id, service.LiveDeploy) {
		if err := m.Caddy.Active(ctx, id, domains(service), service.LiveDeploy); err != nil {
			m.failSync(service, requestID, err)
			return nil, false, internal(fmt.Errorf("restore Caddy live deployment: %w", err))
		}
	}
	updated := service
	updated.Version = req.Version
	if err := m.Docker.RemoveSlot(ctx, id, target); err != nil {
		m.failSync(service, requestID, err)
		return nil, false, unprocessable("upgrade_failed", err)
	}
	if err := os.Remove(updated.Deploy[target].Socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		m.failSync(service, requestID, err)
		return nil, false, unprocessable("stale_socket_remove_failed", err)
	}
	if err := m.Docker.StartSlot(ctx, updated, target); err != nil {
		m.failSync(service, requestID, err)
		return nil, false, unprocessable("upgrade_failed", err)
	}
	service.Phase, service.UpdatedAt = "waiting_for_health", time.Now().UTC()
	service.Deploy[target] = withHealth(service.Deploy[target], "checking")
	m.mu.Lock()
	err = m.Repo.Put(service)
	m.mu.Unlock()
	if err != nil {
		m.failSync(service, requestID, err)
		return nil, false, internal(err)
	}
	go m.finishHealth(id, "upgrade", requestID)
	status, err := m.status(ctx, service)
	return status, true, err
}

func validateUpgrade(service model.Service, req model.UpgradeRequest) (bool, error) {
	if service.State != model.Active {
		return false, conflict(errors.New("only active services can be upgraded"))
	}
	if service.Phase == "failed" && service.Operation != "upgrade" {
		return false, conflict(errors.New("retry the failed lifecycle operation before upgrading"))
	}
	if busy(service.Phase) {
		return false, conflict(fmt.Errorf("%s is already in progress", service.Operation))
	}
	if req.Version == service.Version {
		return true, nil
	}
	cmp, ordered := compareVersions(req.Version, service.Version)
	if (!ordered || cmp < 0) && !req.ConfirmDowngrade {
		return false, conflict(errors.New("possible downgrade requires confirm_downgrade=true"))
	}
	return false, nil
}

func (m *Manager) finishHealth(id, operation, requestID string) {
	service, ok := m.deferredService(id, operation)
	if !ok {
		return
	}
	target := service.TargetDeploy
	if err := m.Health.Check(context.Background(), service.Deploy[target].Socket); err != nil {
		service.Deploy[target] = withHealth(service.Deploy[target], "unhealthy")
		m.failDeferred(service, requestID, err)
		return
	}
	service.Deploy[target] = withHealth(service.Deploy[target], "healthy")
	service.Phase, service.UpdatedAt = "routing", time.Now().UTC()
	m.putDeferred(service)
	if err := m.Caddy.Active(context.Background(), id, routingDomains(service, operation), target); err != nil {
		m.failDeferred(service, requestID, err)
		return
	}
	if operation == "upgrade" {
		if service.TargetMainDomain != "" {
			service.MainDomain, service.StagingDomain = service.TargetMainDomain, service.TargetStagingDomain
			service.TargetMainDomain, service.TargetStagingDomain = "", ""
		}
		if service.TargetPublicIPv4 != "" {
			service.PublicIPv4, service.TargetPublicIPv4 = service.TargetPublicIPv4, ""
		}
		service.Phase, service.UpdatedAt = "draining", time.Now().UTC()
		m.putDeferred(service)
		if err := cleanupOldDeploy(context.Background(), m.Config.Deployment.TrafficDrain, func(ctx context.Context) error {
			return m.Docker.RemoveSlot(ctx, id, service.LiveDeploy)
		}); err != nil {
			m.failDeferred(service, requestID, err)
			return
		}
		service.LiveDeploy, service.Version = target, service.TargetVersion
	}
	service.Phase, service.Operation, service.TargetDeploy, service.TargetVersion, service.TargetMainDomain, service.TargetStagingDomain, service.TargetPublicIPv4, service.LastError, service.UpdatedAt = "running", "", "", "", "", "", "", "", time.Now().UTC()
	if m.dnsEnabled() {
		service.DNSStatus, service.DNSLastError = "pending", ""
	}
	m.putDeferred(service)
	if m.dnsEnabled() {
		go m.syncDNS(id, service.MainDomain, service.PublicIPv4)
	}
	m.notifyAsync(operation, true, service, requestID, "")
}

func (m *Manager) finishRouting(id, operation, requestID string) {
	service, ok := m.deferredService(id, operation)
	if !ok {
		return
	}
	var err error
	if operation == model.Suspended {
		err = m.Caddy.Suspended(context.Background(), id, domains(service))
	} else {
		err = m.Caddy.Remove(context.Background(), id)
	}
	if err != nil {
		m.failDeferred(service, requestID, err)
		return
	}
	service.Phase, service.Operation, service.TargetDeploy, service.TargetVersion, service.TargetMainDomain, service.TargetStagingDomain, service.LastError, service.UpdatedAt = "stopped", "", "", "", "", "", "", time.Now().UTC()
	for slot, deploy := range service.Deploy {
		service.Deploy[slot] = withHealth(deploy, "unknown")
	}
	m.putDeferred(service)
	m.notifyAsync(operation, true, service, requestID, "")
}

func (m *Manager) finishDelete(tombstone model.Tombstone, service model.Service, requestID string) {
	if err := m.Caddy.Remove(context.Background(), tombstone.ID); err != nil {
		tombstone.Phase, tombstone.LastError, tombstone.UpdatedAt = "failed", err.Error(), time.Now().UTC()
		m.mu.Lock()
		_ = m.Repo.PutTombstone(tombstone)
		m.mu.Unlock()
		m.notifyAsync("delete", false, service, requestID, err.Error())
		return
	}
	tombstone.Phase, tombstone.Operation, tombstone.LastError, tombstone.UpdatedAt = "deleted", "", "", time.Now().UTC()
	m.mu.Lock()
	_ = m.Repo.PutTombstone(tombstone)
	m.mu.Unlock()
	m.notifyAsync("delete", true, service, requestID, "")
}

func (m *Manager) deferredService(id, operation string) (model.Service, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	service, err := m.Repo.Get(id)
	return service, err == nil && service.Operation == operation && busy(service.Phase)
}

func (m *Manager) putDeferred(service model.Service) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.Repo.Put(service); err != nil && m.Logger != nil {
		m.Logger.Error("persist deferred state", "service_id", service.ID, "error", err)
	}
}

func (m *Manager) failSync(service model.Service, requestID string, operationErr error) {
	service.Phase, service.LastError, service.UpdatedAt = "failed", operationErr.Error(), time.Now().UTC()
	m.putDeferred(service)
	m.notifyAsync(service.Operation, false, service, requestID, operationErr.Error())
}

func (m *Manager) failDeferred(service model.Service, requestID string, operationErr error) {
	m.failSync(service, requestID, operationErr)
}

func (m *Manager) notifyAsync(operation string, success bool, service model.Service, requestID, detail string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.Notify.Send(ctx, operation, success, service, requestID, detail); err != nil && m.Logger != nil {
			m.Logger.Warn("notification failed", "operation", operation, "service_id", service.ID, "request_id", requestID, "error", err)
		}
	}()
}

func (m *Manager) queueDNS(service model.Service) (model.Service, error) {
	if !m.dnsEnabled() {
		return service, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	latest, err := m.Repo.Get(service.ID)
	if err != nil {
		return service, err
	}
	latest.DNSStatus, latest.DNSLastError, latest.UpdatedAt = "pending", "", time.Now().UTC()
	if err := m.Repo.Put(latest); err != nil {
		return service, err
	}
	go m.syncDNS(latest.ID, latest.MainDomain, latest.PublicIPv4)
	return latest, nil
}

func (m *Manager) ReconnectDNS(id string) error {
	service, err := m.liveService(id)
	if err != nil {
		return err
	}
	if !m.dnsEnabled() {
		return conflict(errors.New("DNS updates are disabled"))
	}
	if _, err := m.queueDNS(service); err != nil {
		return internal(err)
	}
	return nil
}

func (m *Manager) dnsEnabled() bool { return m.Config.DNSWebhook.URL != "" }

func (m *Manager) syncDNS(id, domain, publicIPv4 string) {
	backoffs := m.dnsBackoffs
	if backoffs == nil {
		backoffs = []time.Duration{0, 30 * time.Second, 5 * time.Minute}
	}
	var err error
	for _, backoff := range backoffs {
		if backoff > 0 {
			time.Sleep(backoff)
		}
		err = m.callDNSWebhook(domain, publicIPv4)
		if err == nil {
			break
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	service, getErr := m.Repo.Get(id)
	if getErr != nil || service.MainDomain != domain || service.PublicIPv4 != publicIPv4 || err != nil && service.DNSStatus != "pending" {
		return
	}
	service.UpdatedAt = time.Now().UTC()
	if err == nil {
		service.DNSStatus, service.DNSLastError, service.DNSSyncedAt = "in_sync", "", service.UpdatedAt.Format(time.RFC3339Nano)
	} else {
		service.DNSStatus, service.DNSLastError = "error", err.Error()
	}
	if putErr := m.Repo.Put(service); putErr != nil && m.Logger != nil {
		m.Logger.Error("persist DNS status", "service_id", id, "error", putErr)
	}
}

func (m *Manager) callDNSWebhook(domain, publicIPv4 string) error {
	rendered := strings.NewReplacer("{domain}", jsonString(domain), "{ipv4}", jsonString(publicIPv4)).Replace(m.Config.DNSWebhook.Body)
	headerBlock, body, ok := strings.Cut(strings.ReplaceAll(rendered, "\r\n", "\n"), "\n\n")
	if !ok {
		return errors.New("dns_webhook.body must separate headers and content with a blank line")
	}
	headers, err := textproto.NewReader(bufio.NewReader(strings.NewReader(headerBlock + "\n\n"))).ReadMIMEHeader()
	if err != nil {
		return fmt.Errorf("parse dns_webhook.body headers: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), m.Config.DNSWebhook.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.Config.DNSWebhook.URL, bytes.NewBufferString(body))
	if err != nil {
		return err
	}
	for name, values := range headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	client := m.dnsHTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("DNS webhook returned %s", response.Status)
	}
	return nil
}

func jsonString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded[1 : len(encoded)-1])
}

func (m *Manager) RecoverInterrupted() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	services, err := m.Repo.List()
	if err != nil {
		return err
	}
	for _, service := range services {
		derive(&service)
		if busy(service.Phase) {
			service.Phase, service.LastError, service.UpdatedAt = "failed", "controller restarted during deferred operation", time.Now().UTC()
			if err := m.Repo.Put(service); err != nil {
				return err
			}
		}
	}
	tombstones, err := m.Repo.ListTombstones()
	if err != nil {
		return err
	}
	for _, tombstone := range tombstones {
		if tombstone.Phase == "deleting" {
			tombstone.Phase, tombstone.LastError, tombstone.UpdatedAt = "failed", "controller restarted during deferred operation", time.Now().UTC()
			if err := m.Repo.PutTombstone(tombstone); err != nil {
				return err
			}
		}
	}
	return nil
}

func derive(service *model.Service) {
	if service.Phase == "" {
		if service.State == model.Active {
			service.Phase = "running"
		} else {
			service.Phase = "stopped"
		}
	}
	if service.Deploy == nil {
		service.Deploy = map[string]model.Deploy{}
	}
	for slot, deploy := range service.Deploy {
		if deploy.Health == "" {
			deploy.Health = "unknown"
		}
		if deploy.Version == "" && slot == service.LiveDeploy {
			deploy.Version = service.Version
		}
		service.Deploy[slot] = deploy
	}
}

func busy(phase string) bool {
	switch phase {
	case "provisioning", "starting", "waiting_for_health", "routing", "draining", "stopping", "deleting":
		return true
	default:
		return false
	}
}

func withHealth(deploy model.Deploy, health string) model.Deploy {
	deploy.Health = health
	return deploy
}

func (m *Manager) Get(ctx context.Context, id string) (*model.Status, error) {
	if err := ValidateServiceID(id); err != nil {
		return nil, badRequest(err)
	}
	service, err := m.liveService(id)
	if err != nil {
		return nil, err
	}
	derive(&service)
	return m.status(ctx, service)
}

func (m *Manager) MonitorSnapshot(ctx context.Context, id string) (model.MonitorSnapshot, bool, error) {
	if err := ValidateServiceID(id); err != nil {
		return model.MonitorSnapshot{}, false, badRequest(err)
	}
	service, err := m.Repo.Get(id)
	if err == nil {
		derive(&service)
		containers, dockerErr := m.Docker.Containers(ctx, id)
		configured, mode, socket, caddyErr := m.Caddy.Status(id)
		if dockerErr != nil || caddyErr != nil {
			return model.MonitorSnapshot{}, false, fmt.Errorf("observe service status: docker=%v caddy=%v", dockerErr, caddyErr)
		}
		bySlot := map[string]model.ContainerStatus{}
		for _, container := range containers {
			bySlot[container.Deploy] = container
		}
		deployments := map[string]model.DeploymentStatus{}
		for _, slot := range []string{"blue", "green"} {
			deploy := service.Deploy[slot]
			runtime := "absent"
			if container, ok := bySlot[slot]; ok {
				runtime = "stopped"
				if container.Running {
					runtime = "running"
				}
				if deploy.Version == "" {
					deploy.Version = container.Version
				}
			}
			deployments[slot] = model.DeploymentStatus{
				Version: deploy.Version, Runtime: runtime, Health: defaultHealth(deploy.Health),
				ReceivesTraffic: configured && mode == "proxy" && socket == m.Caddy.Socket(id, slot),
			}
		}
		snapshot := map[string]any{
			"id": service.ID, "state": service.State, "phase": service.Phase, "operation": service.Operation,
			"live_deploy": service.LiveDeploy, "target_deploy": service.TargetDeploy, "target_version": service.TargetVersion,
			"updated_at": service.UpdatedAt, "message": phaseMessage(service.Phase, service.TargetDeploy, service.LiveDeploy), "deployments": deployments,
		}
		if service.DNSStatus != "" {
			snapshot["dns_status"], snapshot["dns_last_error"], snapshot["dns_synced_at"] = service.DNSStatus, service.DNSLastError, service.DNSSyncedAt
		}
		return model.MonitorSnapshot{Type: "status", ObservedAt: time.Now().UTC(), Service: snapshot}, false, nil
	}
	if !errors.Is(err, state.ErrNotFound) {
		return model.MonitorSnapshot{}, false, internal(err)
	}
	tombstone, tombErr := m.Repo.GetTombstone(id)
	if tombErr != nil {
		if errors.Is(tombErr, state.ErrNotFound) {
			return model.MonitorSnapshot{}, false, notFound(tombErr)
		}
		return model.MonitorSnapshot{}, false, internal(tombErr)
	}
	if tombstone.Phase == "" {
		tombstone.Phase = "deleted"
	}
	return model.MonitorSnapshot{Type: "status", ObservedAt: time.Now().UTC(), Service: map[string]any{
		"id": tombstone.ID, "state": model.Deleted, "phase": tombstone.Phase, "operation": tombstone.Operation,
		"updated_at": tombstone.UpdatedAt, "message": phaseMessage(tombstone.Phase, "", ""), "deployments": map[string]model.DeploymentStatus{},
	}}, tombstone.Phase == "deleted", nil
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
	derive(&service)
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
	result.Deployments = map[string]model.DeploymentStatus{}
	containers := map[string]model.ContainerStatus{}
	for _, item := range result.Containers {
		containers[item.Deploy] = item
		if item.Running {
			running++
		}
		if item.Labels["au.modd.service-id"] != service.ID {
			result.Warnings = append(result.Warnings, "container label mismatch")
		}
	}
	for _, slot := range []string{"blue", "green"} {
		deploy := service.Deploy[slot]
		runtime := "absent"
		if container, ok := containers[slot]; ok {
			runtime = "stopped"
			if container.Running {
				runtime = "running"
			}
			if deploy.Version == "" {
				deploy.Version = container.Version
			}
		}
		result.Deployments[slot] = model.DeploymentStatus{
			Version: deploy.Version, Runtime: runtime, Health: defaultHealth(deploy.Health),
			ReceivesTraffic: result.Caddy.Configured && result.Caddy.Mode == "proxy" && result.Caddy.Socket == m.Caddy.Socket(service.ID, slot),
		}
	}
	result.Message = phaseMessage(service.Phase, service.TargetDeploy, service.LiveDeploy)
	healthyTraffic := false
	for _, deploy := range result.Deployments {
		healthyTraffic = healthyTraffic || deploy.ReceivesTraffic && deploy.Health == "healthy"
	}
	if service.State == model.Active && !healthyTraffic {
		result.Warnings = append(result.Warnings, "no healthy instance is receiving traffic")
	}
	if service.State == model.Active && running == 0 {
		result.Warnings = append(result.Warnings, "service is active but no container is running")
	}
	if running > 1 && service.Phase == "running" {
		result.Warnings = append(result.Warnings, "more than one deployment is running")
	}
	if service.State == model.Active && result.Caddy.Socket != m.Caddy.Socket(service.ID, service.LiveDeploy) {
		result.Warnings = append(result.Warnings, "Caddy and TOML live deployment disagree")
	}
	if !result.DatasetExists {
		result.Warnings = append(result.Warnings, "dataset is missing")
	}
	return result, nil
}

func defaultHealth(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

func phaseMessage(phase, target, live string) string {
	switch phase {
	case "provisioning":
		return "Preparing storage and the first instance."
	case "starting":
		return "Starting the requested instance."
	case "waiting_for_health":
		if target != "" && target != live && live != "" {
			return strings.ToUpper(target[:1]) + target[1:] + " is starting; " + live + " remains available and receives traffic."
		}
		return "The instance is starting and waiting to pass health checks."
	case "routing":
		return "Updating traffic routing."
	case "draining":
		return "Traffic has switched; the previous instance is draining."
	case "running":
		return "The service is running."
	case "stopping":
		return "Stopping service instances."
	case "stopped":
		return "The service is stopped."
	case "deleting":
		return "Deleting the service."
	case "deleted":
		return "The service has been deleted."
	case "failed":
		return "The last lifecycle operation failed; an administrator can retry it."
	default:
		return "Service status is available."
	}
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
	dirPattern := filepath.Dir(m.Config.Deployment.Socket)
	removeDir := strings.Contains(dirPattern, "{service_id}") || strings.Contains(dirPattern, "{slot}")
	removeAll := strings.Contains(dirPattern, "{service_id}")
	for _, slot := range []string{"blue", "green"} {
		socket := deployment(m.Config.Deployment.Socket, id, slot).Socket
		if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if removeDir {
			var err error
			if removeAll {
				err = os.RemoveAll(filepath.Dir(socket))
			} else {
				err = os.Remove(filepath.Dir(socket))
			}
			if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOTEMPTY) {
				return err
			}
		}
	}
	return nil
}

func deployment(socket, id, slot string, version ...string) model.Deploy {
	deploy := model.Deploy{
		Socket:    strings.NewReplacer("{service_id}", id, "{slot}", slot).Replace(socket),
		Container: "WHMCS-" + strings.TrimPrefix(id, "whmcs-") + "-" + slot,
		Health:    "unknown",
	}
	if len(version) > 0 {
		deploy.Version = version[0]
	}
	return deploy
}

func domains(service model.Service) []string {
	return []string{service.MainDomain, service.StagingDomain}
}

func routingDomains(service model.Service, operation string) []string {
	if operation == "upgrade" && service.TargetMainDomain != "" {
		return []string{service.TargetMainDomain, service.TargetStagingDomain}
	}
	return domains(service)
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

func cleanupOldDeploy(ctx context.Context, drain time.Duration, remove func(context.Context) error) error {
	ctx = context.WithoutCancel(ctx)
	if err := wait(ctx, drain); err != nil {
		return err
	}
	return remove(ctx)
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
