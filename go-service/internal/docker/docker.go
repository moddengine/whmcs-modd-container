package docker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/moddengine/whmcs-container-controller/internal/config"
	"github.com/moddengine/whmcs-container-controller/internal/isolation"
	"github.com/moddengine/whmcs-container-controller/internal/model"
)

const (
	managedLabel = "au.modd.managed"
	serviceLabel = "au.modd.service-id"
	versionLabel = "au.modd.version"
	appLabel     = "au.modd.app"
	deployLabel  = "au.modd.deploy"
)

type Adapter struct {
	client *client.Client
	cfg    config.Docker
}

func New(cfg config.Docker) (*Adapter, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &Adapter{client: cli, cfg: cfg}, nil
}

func (a *Adapter) Close() error { return a.client.Close() }

func (a *Adapter) Ping(ctx context.Context) (string, error) {
	ping, err := a.client.Ping(ctx)
	return ping.APIVersion, err
}

func (a *Adapter) ValidateNetwork(ctx context.Context) error {
	_, err := a.client.NetworkInspect(ctx, a.cfg.Network, network.InspectOptions{})
	return err
}

func (a *Adapter) Containers(ctx context.Context, serviceID string) ([]model.ContainerStatus, error) {
	args := filters.NewArgs(filters.Arg("label", managedLabel+"=true"))
	if serviceID != "" {
		args.Add("label", serviceLabel+"="+serviceID)
	}
	items, err := a.client.ContainerList(ctx, container.ListOptions{All: true, Filters: args})
	if err != nil {
		return nil, err
	}
	result := make([]model.ContainerStatus, 0, len(items))
	for _, item := range items {
		name := ""
		if len(item.Names) > 0 {
			name = strings.TrimPrefix(item.Names[0], "/")
		}
		result = append(result, model.ContainerStatus{
			ID: item.ID, Name: name, Deploy: item.Labels[deployLabel], Version: item.Labels[versionLabel],
			Exists: true, Running: item.State == "running", Labels: item.Labels,
		})
	}
	return result, nil
}

func (a *Adapter) Create(ctx context.Context, service model.Service, slot string) (string, error) {
	spec, host, netConfig, err := ContainerSpec(a.cfg, service, slot)
	if err != nil {
		return "", err
	}
	response, err := a.client.ContainerCreate(ctx, spec, host, netConfig, nil, service.Deploy[slot].Container)
	return response.ID, err
}

func ContainerSpec(cfg config.Docker, service model.Service, slot string) (*container.Config, *container.HostConfig, *network.NetworkingConfig, error) {
	identity, err := isolation.ForService(service.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	labels := map[string]string{
		managedLabel: "true", serviceLabel: service.ID, versionLabel: service.Version,
		appLabel: "moddengine", deployLabel: slot,
	}
	replacer := strings.NewReplacer(
		"{mountpoint}", service.Dataset.Mountpoint,
		"{slot}", slot,
		"{service_id}", service.ID,
	)
	env := make([]string, 0, len(cfg.Environment)+2)
	for _, value := range cfg.Environment {
		env = append(env, replacer.Replace(value))
	}
	env = append(env, "ME_SITE="+service.ID, "ME_INSTANCE="+service.ID+"-"+slot)
	binds := make([]string, 0, len(cfg.Binds)+2)
	for _, bind := range cfg.Binds {
		binds = append(binds, replacer.Replace(bind))
	}
	socketDir := filepath.Dir(service.Deploy[slot].Socket)
	binds = append(binds, socketDir+":"+socketDir)
	return &container.Config{
			Image: cfg.ImageRepository + ":" + service.Version,
			User:  fmt.Sprintf("%d:%d", identity.UID, identity.GID), Env: env, Labels: labels,
		}, &container.HostConfig{
			Binds:         binds,
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
		}, &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{cfg.Network: {}},
		}, nil
}

func (a *Adapter) Start(ctx context.Context, id string) error {
	return a.client.ContainerStart(ctx, id, container.StartOptions{})
}

func (a *Adapter) Stop(ctx context.Context, id string) error {
	timeout := 20
	return a.client.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout})
}

func (a *Adapter) Remove(ctx context.Context, id, serviceID string) error {
	info, err := a.client.ContainerInspect(ctx, id)
	if err != nil {
		return err
	}
	if info.Config.Labels[managedLabel] != "true" || info.Config.Labels[serviceLabel] != serviceID {
		return errors.New("refusing to remove container with mismatched labels")
	}
	return a.client.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
}

func (a *Adapter) StartSlot(ctx context.Context, service model.Service, slot string) error {
	_, host, _, err := ContainerSpec(a.cfg, service, slot)
	if err != nil {
		return err
	}
	identity, err := isolation.ForService(service.ID)
	if err != nil {
		return err
	}
	if err := prepareBindSources(host.Binds, identity); err != nil {
		return err
	}
	items, err := a.Containers(ctx, service.ID)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.Deploy == slot {
			if item.Running {
				return nil
			}
			return a.Start(ctx, item.ID)
		}
	}
	id, err := a.Create(ctx, service, slot)
	if err != nil {
		return err
	}
	return a.Start(ctx, id)
}

func prepareBindSources(binds []string, identity isolation.Identity) error {
	for _, bind := range binds {
		source, target, ok := strings.Cut(bind, ":")
		if !ok || !filepath.IsAbs(source) {
			return fmt.Errorf("invalid bind %q", bind)
		}
		if err := os.MkdirAll(source, 0750); err != nil {
			return fmt.Errorf("create bind source %q: %w", source, err)
		}
		_, options, _ := strings.Cut(target, ":")
		if strings.Contains(","+options+",", ",ro,") {
			continue
		}
		if err := isolation.ChownTree(source, identity); err != nil {
			return fmt.Errorf("chown bind source %q: %w", source, err)
		}
	}
	return nil
}

func (a *Adapter) StopAll(ctx context.Context, serviceID string) error {
	items, err := a.Containers(ctx, serviceID)
	if err != nil {
		return err
	}
	for _, item := range items {
		if err := a.Stop(ctx, item.ID); err != nil && !errdefs.IsNotModified(err) {
			return err
		}
	}
	return nil
}

func (a *Adapter) RemoveAll(ctx context.Context, serviceID string) error {
	items, err := a.Containers(ctx, serviceID)
	if err != nil {
		return err
	}
	for _, item := range items {
		if err := a.Remove(ctx, item.ID, serviceID); err != nil {
			return err
		}
	}
	return nil
}

func (a *Adapter) RemoveSlot(ctx context.Context, serviceID, slot string) error {
	items, err := a.Containers(ctx, serviceID)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.Deploy == slot {
			if err := a.Remove(ctx, item.ID, serviceID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *Adapter) Versions(ctx context.Context) ([]model.ImageVersion, error) {
	args := filters.NewArgs(filters.Arg("reference", a.cfg.ImageRepository+":*"))
	images, err := a.client.ImageList(ctx, image.ListOptions{Filters: args})
	if err != nil {
		return nil, err
	}
	var result []model.ImageVersion
	for _, item := range images {
		for _, tag := range item.RepoTags {
			prefix := a.cfg.ImageRepository + ":"
			if !strings.HasPrefix(tag, prefix) {
				continue
			}
			result = append(result, model.ImageVersion{
				Version: strings.TrimPrefix(tag, prefix), ImageReference: tag, Local: true,
				CreatedAt: time.Unix(item.Created, 0).UTC(),
			})
		}
	}
	sortVersionsNewestFirst(result)
	return result, nil
}

func sortVersionsNewestFirst(versions []model.ImageVersion) {
	sort.Slice(versions, func(i, j int) bool { return versions[i].CreatedAt.After(versions[j].CreatedAt) })
}

func (a *Adapter) HasVersion(ctx context.Context, version string) (bool, error) {
	versions, err := a.Versions(ctx)
	if err != nil {
		return false, err
	}
	for _, available := range versions {
		if available.Version == version {
			return true, nil
		}
	}
	return false, nil
}

func ValidateVersion(version string) error {
	if version == "" || strings.ContainsAny(version, "@/ \t\r\n") {
		return fmt.Errorf("invalid image version %q", version)
	}
	return nil
}
