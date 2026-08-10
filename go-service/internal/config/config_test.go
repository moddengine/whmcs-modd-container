package config

import (
	"path/filepath"
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
		config.Deployment.HealthAttempts != 30 {
		t.Fatalf("unexpected example config: %#v", config)
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
