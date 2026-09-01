package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExampleConfigLoads(t *testing.T) {
	config, err := Load(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if config.Server.Listen != "127.0.0.1:8443" ||
		config.Deployment.TrafficDrain != 10*time.Second ||
		config.Deployment.HealthAttempts != 30 || config.DNS.Endpoint == "" || config.Docker.PullTimeout != 30*time.Minute ||
		config.Docker.CertificateMountPath != "/srv/modd/secrets" {
		t.Fatalf("unexpected example config: %#v", config)
	}
}

func TestDockerPullTimeoutDefaultsWhenOmitted(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "pull_timeout = \"30m\"\n", "", 1))
	path := filepath.Join(t.TempDir(), "controller.toml")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path)
	if err != nil || config.Docker.PullTimeout != 30*time.Minute {
		t.Fatalf("default pull timeout = %s, %v", config.Docker.PullTimeout, err)
	}
}

func TestDockerPullTimeoutMustBePositive(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "controller.toml")
	if err := os.WriteFile(path, []byte(strings.Replace(string(content), `pull_timeout = "30m"`, `pull_timeout = "0s"`, 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("zero docker.pull_timeout was accepted")
	}
}

func TestDNSCanBeDisabledByOmittingSection(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	start, end := strings.Index(string(content), "[dns]"), strings.Index(string(content), "[google_chat]")
	path := filepath.Join(t.TempDir(), "controller.toml")
	if err := os.WriteFile(path, append(content[:start], content[end:]...), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path)
	if err != nil || config.DNS.Endpoint != "" {
		t.Fatalf("disabled DNS config = %#v, %v", config.DNS, err)
	}
}

func TestDNSConfigValidation(t *testing.T) {
	config, err := Load(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for name, dns := range map[string]DNS{
		"missing key":      {Endpoint: "https://dns.example/dns.php"},
		"missing endpoint": {KeyFile: "/run/dns-key"},
		"invalid endpoint": {Endpoint: "file:///dns.php", KeyFile: "/run/dns-key"},
		"relative key":     {Endpoint: "https://dns.example/dns.php", KeyFile: "dns-key"},
	} {
		config.DNS = dns
		if err := config.Validate(); err == nil {
			t.Errorf("%s DNS config was accepted", name)
		}
	}
}

func TestSocketTemplateValidation(t *testing.T) {
	config, err := Load(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for socket, valid := range map[string]bool{
		"/run/whmcs/{service_id}-{slot}.sock":           true,
		"/run/whmcs/{service_id}.sock":                  false,
		"/run/whmcs/{service_id}-{slot}-{service}.sock": false,
	} {
		config.Deployment.Socket = socket
		if err := config.Validate(); (err == nil) != valid {
			t.Errorf("Validate() error for %q = %v, want valid %t", socket, err, valid)
		}
	}
}
