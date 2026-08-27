package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	Server     Server     `toml:"server"`
	Auth       Auth       `toml:"auth"`
	ZFS        ZFS        `toml:"zfs"`
	State      State      `toml:"state"`
	Caddy      Caddy      `toml:"caddy"`
	Docker     Docker     `toml:"docker"`
	Deployment Deployment `toml:"deployment"`
	Domains    Domains    `toml:"domains"`
	DNSWebhook DNSWebhook `toml:"dns_webhook"`
	GoogleChat GoogleChat `toml:"google_chat"`
}

type Server struct {
	Listen          string        `toml:"listen"`
	RequestTimeout  time.Duration `toml:"-"`
	ShutdownTimeout time.Duration `toml:"-"`
	RequestRaw      string        `toml:"request_timeout"`
	ShutdownRaw     string        `toml:"shutdown_timeout"`
}
type Auth struct {
	BearerTokenFile string `toml:"bearer_token_file"`
}
type ZFS struct {
	DatasetPrefix string `toml:"dataset_prefix"`
	MountPrefix   string `toml:"mount_prefix"`
}
type State struct {
	ServicesDir   string `toml:"services_dir"`
	TombstonesDir string `toml:"tombstones_dir"`
}
type Caddy struct {
	ServiceConfigDir string   `toml:"service_config_dir"`
	SuspensionRoot   string   `toml:"suspension_root"`
	ActiveTemplate   string   `toml:"active_template"`
	ValidateCommand  []string `toml:"validate_command"`
	ReloadCommand    []string `toml:"reload_command"`
}
type Docker struct {
	Network         string   `toml:"network"`
	ImageRepository string   `toml:"image_repository"`
	Binds           []string `toml:"binds"`
	Environment     []string `toml:"environment"`
}
type Deployment struct {
	HealthPath         string        `toml:"health_path"`
	HealthAttempts     int           `toml:"health_attempts"`
	HealthInitialDelay time.Duration `toml:"-"`
	HealthBackoff      time.Duration `toml:"-"`
	TrafficDrain       time.Duration `toml:"-"`
	Socket             string        `toml:"socket"`
	HealthInitialRaw   string        `toml:"health_initial_delay"`
	HealthBackoffRaw   string        `toml:"health_backoff_increment"`
	TrafficDrainRaw    string        `toml:"traffic_drain"`
}
type Domains struct {
	StagingSuffix string `toml:"staging_suffix"`
}
type DNSWebhook struct {
	URL        string        `toml:"url"`
	Body       string        `toml:"body"`
	Timeout    time.Duration `toml:"-"`
	TimeoutRaw string        `toml:"timeout"`
}
type GoogleChat struct {
	WebhookURLFile string `toml:"webhook_url_file"`
}

func Load(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err = toml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("parse config: %w", err)
	}
	durations := []struct {
		raw string
		dst *time.Duration
	}{
		{c.Server.RequestRaw, &c.Server.RequestTimeout},
		{c.Server.ShutdownRaw, &c.Server.ShutdownTimeout},
		{c.Deployment.HealthInitialRaw, &c.Deployment.HealthInitialDelay},
		{c.Deployment.HealthBackoffRaw, &c.Deployment.HealthBackoff},
		{c.Deployment.TrafficDrainRaw, &c.Deployment.TrafficDrain},
	}
	if c.DNSWebhook.URL != "" {
		durations = append(durations, struct {
			raw string
			dst *time.Duration
		}{c.DNSWebhook.TimeoutRaw, &c.DNSWebhook.Timeout})
	}
	for _, item := range durations {
		if *item.dst, err = time.ParseDuration(item.raw); err != nil {
			return c, fmt.Errorf("invalid duration %q: %w", item.raw, err)
		}
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	if _, _, err := net.SplitHostPort(c.Server.Listen); err != nil {
		return fmt.Errorf("invalid server.listen: %w", err)
	}
	if c.Server.RequestTimeout <= 0 || c.Server.ShutdownTimeout <= 0 {
		return errors.New("server timeouts must be positive")
	}
	if c.ZFS.DatasetPrefix == "" || strings.HasPrefix(c.ZFS.DatasetPrefix, "/") || strings.Contains(c.ZFS.DatasetPrefix, "..") {
		return errors.New("zfs.dataset_prefix must be a safe dataset name")
	}
	for name, path := range map[string]string{
		"zfs.mount_prefix": c.ZFS.MountPrefix, "state.services_dir": c.State.ServicesDir,
		"state.tombstones_dir": c.State.TombstonesDir, "caddy.service_config_dir": c.Caddy.ServiceConfigDir,
		"deployment.socket": c.Deployment.Socket,
	} {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("%s must be absolute", name)
		}
	}
	unknown := strings.NewReplacer("{service_id}", "", "{slot}", "").Replace(c.Deployment.Socket)
	if strings.ContainsAny(unknown, "{}") {
		return errors.New("deployment.socket only supports {service_id} and {slot} placeholders")
	}
	if !strings.Contains(c.Deployment.Socket, "{service_id}") || !strings.Contains(c.Deployment.Socket, "{slot}") {
		return errors.New("deployment.socket must contain {service_id} and {slot}")
	}
	if c.Docker.ImageRepository == "" || c.Docker.Network == "" {
		return errors.New("docker image_repository and network are required")
	}
	if c.Deployment.HealthAttempts < 1 || c.Deployment.HealthInitialDelay < 0 || c.Deployment.HealthBackoff < 0 || c.Deployment.TrafficDrain < 0 {
		return errors.New("deployment health settings and traffic drain are invalid")
	}
	if c.Domains.StagingSuffix == "" {
		return errors.New("domains.staging_suffix is required")
	}
	if c.DNSWebhook.URL != "" {
		if c.DNSWebhook.Body == "" || c.DNSWebhook.Timeout <= 0 {
			return errors.New("dns_webhook body and positive timeout are required when url is configured")
		}
		if webhookURL, err := url.ParseRequestURI(c.DNSWebhook.URL); err != nil || webhookURL.Scheme != "http" && webhookURL.Scheme != "https" || webhookURL.Host == "" {
			return errors.New("dns_webhook.url must be an HTTP(S) URL")
		}
		if !strings.Contains(c.DNSWebhook.Body, "{domain}") || !strings.Contains(c.DNSWebhook.Body, "{ipv4}") {
			return errors.New("dns_webhook.body must contain {domain} and {ipv4}")
		}
	}
	if c.Caddy.ActiveTemplate == "" {
		return errors.New("caddy.active_template is required")
	}
	if c.Auth.BearerTokenFile == "" {
		return errors.New("auth.bearer_token_file is required")
	}
	return nil
}

func ReadSecret(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(b))
	if value == "" {
		return "", errors.New("secret file is empty")
	}
	return value, nil
}
