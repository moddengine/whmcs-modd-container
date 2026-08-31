package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/distribution/reference"
	dockerconfig "github.com/docker/cli/cli/config"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/moddengine/whmcs-container-controller/internal/config"
	"github.com/moddengine/whmcs-container-controller/internal/isolation"
	"github.com/moddengine/whmcs-container-controller/internal/model"
)

const dockerHubAPI = "https://hub.docker.com/v2"

const (
	managedLabel = "au.modd.managed"
	serviceLabel = "au.modd.service-id"
	versionLabel = "au.modd.version"
	appLabel     = "au.modd.app"
	deployLabel  = "au.modd.deploy"
)

type Adapter struct {
	client    *client.Client
	cfg       config.Docker
	configDir string
}

func New(cfg config.Docker) (*Adapter, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &Adapter{client: cli, cfg: cfg, configDir: dockerconfig.Dir()}, nil
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
		appLabel: "whmcs", deployLabel: slot,
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
	packageCodes := make([]string, 0, len(service.Package))
	for code := range service.Package {
		packageCodes = append(packageCodes, code)
	}
	sort.Strings(packageCodes)
	for _, code := range packageCodes {
		env = append(env, "ME_PACKAGE_"+strings.ToUpper(code)+"="+service.Package[code])
	}
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

func (a *Adapter) Pull(ctx context.Context, version string) (model.ImageVersion, error) {
	if version == "" {
		credentials, credentialsErr := a.registryCredentials(a.cfg.ImageRepository)
		if credentialsErr != nil {
			return model.ImageVersion{}, credentialsErr
		}
		var err error
		version, err = latestHubVersion(ctx, http.DefaultClient, dockerHubAPI, a.cfg.ImageRepository, credentials.Username, credentials.Password)
		if err != nil {
			return model.ImageVersion{}, err
		}
	}
	if err := ValidateVersion(version); err != nil {
		return model.ImageVersion{}, err
	}
	reference := a.cfg.ImageRepository + ":" + version
	auth, err := a.registryAuth(reference)
	if err != nil {
		return model.ImageVersion{}, fmt.Errorf("pull %s: %w", reference, err)
	}
	stream, err := a.client.ImagePull(ctx, reference, image.PullOptions{RegistryAuth: auth})
	if err != nil {
		return model.ImageVersion{}, fmt.Errorf("pull %s: %w", reference, err)
	}
	defer stream.Close()
	decoder := json.NewDecoder(stream)
	for {
		var message struct {
			Error string `json:"error"`
		}
		if err := decoder.Decode(&message); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return model.ImageVersion{}, fmt.Errorf("pull %s: read response: %w", reference, err)
		} else if message.Error != "" {
			return model.ImageVersion{}, fmt.Errorf("pull %s: %s", reference, message.Error)
		}
	}
	return model.ImageVersion{Version: version, ImageReference: reference, Local: true}, nil
}

func (a *Adapter) registryAuth(imageReference string) (string, error) {
	auth, err := a.registryCredentials(imageReference)
	if err != nil {
		return "", err
	}
	if auth.Username == "" && auth.Password == "" && auth.Auth == "" && auth.IdentityToken == "" && auth.RegistryToken == "" {
		return "", nil
	}
	encoded, err := registry.EncodeAuthConfig(auth)
	if err != nil {
		return "", fmt.Errorf("encode Docker registry credentials: %w", err)
	}
	return encoded, nil
}

func (a *Adapter) registryCredentials(imageReference string) (registry.AuthConfig, error) {
	cfg, err := dockerconfig.Load(a.configDir)
	if err != nil {
		return registry.AuthConfig{}, fmt.Errorf("load Docker config: %w", err)
	}
	named, err := reference.ParseNormalizedNamed(imageReference)
	if err != nil {
		return registry.AuthConfig{}, fmt.Errorf("resolve image registry: %w", err)
	}
	auth, err := cfg.GetAuthConfig(reference.Domain(named))
	if err != nil {
		return registry.AuthConfig{}, fmt.Errorf("load Docker credentials for registry %q: %w", reference.Domain(named), err)
	}
	return registry.AuthConfig{
		Username: auth.Username, Password: auth.Password, Auth: auth.Auth,
		ServerAddress: auth.ServerAddress, IdentityToken: auth.IdentityToken, RegistryToken: auth.RegistryToken,
	}, nil
}

func latestHubVersion(ctx context.Context, client *http.Client, baseURL, repository, username, secret string) (string, error) {
	repository = strings.TrimPrefix(repository, "docker.io/")
	repository = strings.TrimPrefix(repository, "index.docker.io/")
	parts := strings.Split(repository, "/")
	if len(parts) == 1 {
		parts = append([]string{"library"}, parts...)
	}
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("image repository %q is not a Docker Hub namespace/repository", repository)
	}
	token := ""
	if username != "" || secret != "" {
		if username == "" || secret == "" {
			return "", errors.New("Docker Hub tag lookup requires both a username and password or access token")
		}
		body, err := json.Marshal(map[string]string{"identifier": username, "secret": secret})
		if err != nil {
			return "", err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/auth/token", strings.NewReader(string(body)))
		if err != nil {
			return "", err
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			return "", fmt.Errorf("authenticate with Docker Hub: %w", err)
		}
		var auth struct {
			AccessToken string `json:"access_token"`
		}
		err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&auth)
		response.Body.Close()
		if response.StatusCode != http.StatusOK || err != nil || auth.AccessToken == "" {
			return "", fmt.Errorf("Docker Hub authentication failed with %s", response.Status)
		}
		token = auth.AccessToken
	}
	var newest string
	var newestAt time.Time
	for page := 1; ; page++ {
		endpoint := fmt.Sprintf("%s/namespaces/%s/repositories/%s/tags?page=%d&page_size=100", baseURL, url.PathEscape(parts[0]), url.PathEscape(parts[1]), page)
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return "", err
		}
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(request)
		if err != nil {
			return "", fmt.Errorf("list Docker Hub tags: %w", err)
		}
		var pageData struct {
			Next    string `json:"next"`
			Results []struct {
				Name       string    `json:"name"`
				LastPushed time.Time `json:"tag_last_pushed"`
			} `json:"results"`
		}
		err = json.NewDecoder(io.LimitReader(response.Body, 10<<20)).Decode(&pageData)
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return "", fmt.Errorf("Docker Hub returned %s", response.Status)
		}
		if err != nil {
			return "", fmt.Errorf("decode Docker Hub tags: %w", err)
		}
		for _, tag := range pageData.Results {
			if strings.HasPrefix(tag.Name, "v") && (newest == "" || tag.LastPushed.After(newestAt)) {
				newest, newestAt = tag.Name, tag.LastPushed
			}
		}
		if pageData.Next == "" {
			break
		}
	}
	if newest == "" {
		return "", errors.New("Docker Hub has no image tag starting with v")
	}
	return newest, nil
}

func ValidateVersion(version string) error {
	if version == "" || strings.ContainsAny(version, "@/ \t\r\n") {
		return fmt.Errorf("invalid image version %q", version)
	}
	return nil
}
