package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/moddengine/whmcs-container-controller/internal/api"
	"github.com/moddengine/whmcs-container-controller/internal/caddy"
	"github.com/moddengine/whmcs-container-controller/internal/config"
	dockeradapter "github.com/moddengine/whmcs-container-controller/internal/docker"
	"github.com/moddengine/whmcs-container-controller/internal/healthcheck"
	"github.com/moddengine/whmcs-container-controller/internal/metrics"
	"github.com/moddengine/whmcs-container-controller/internal/notify"
	"github.com/moddengine/whmcs-container-controller/internal/service"
	"github.com/moddengine/whmcs-container-controller/internal/state"
	"github.com/moddengine/whmcs-container-controller/internal/zfs"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	configPath := flag.String("config", "/etc/modd-hosting/controller.toml", "controller TOML configuration")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal(fmt.Errorf("load config %q: %w", *configPath, err))
	}
	token, err := config.ReadSecret(cfg.Auth.BearerTokenFile)
	if err != nil {
		fatal(fmt.Errorf("read bearer token: %w", err))
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Logging.Path), 0750); err != nil {
		fatal(err)
	}
	logFile, err := os.OpenFile(cfg.Logging.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
	if err != nil {
		fatal(err)
	}
	defer logFile.Close()
	logger := slog.New(slog.NewJSONHandler(io.MultiWriter(os.Stderr, logFile), nil))

	repo := state.New(cfg.State.ServicesDir, cfg.State.TombstonesDir)
	if err := repo.Init(); err != nil {
		fatal(err)
	}
	if err := os.MkdirAll(cfg.Caddy.ServiceConfigDir, 0750); err != nil {
		fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Caddy.SuspensionRoot, "index.html")); err != nil {
		fatal(fmt.Errorf("suspension page: %w", err))
	}
	docker, err := dockeradapter.New(cfg.Docker)
	if err != nil {
		fatal(err)
	}
	defer docker.Close()
	startupContext, cancelStartup := context.WithTimeout(context.Background(), 10*time.Second)
	dockerAPI, err := docker.Ping(startupContext)
	if err != nil {
		cancelStartup()
		fatal(fmt.Errorf("docker unavailable: %w", err))
	}
	if err := docker.ValidateNetwork(startupContext); err != nil {
		cancelStartup()
		fatal(fmt.Errorf("docker network %q unavailable: %w", cfg.Docker.Network, err))
	}
	cancelStartup()

	var notifier notify.Notifier = notify.Disabled{}
	if cfg.GoogleChat.WebhookURLFile != "" {
		if webhook, readErr := config.ReadSecret(cfg.GoogleChat.WebhookURLFile); readErr != nil {
			logger.Warn("Google Chat disabled", "error", readErr)
		} else {
			host, _ := os.Hostname()
			notifier = notify.GoogleChat{Webhook: webhook, Host: host}
		}
	}
	manager := &service.Manager{
		Config: cfg, Repo: repo, Docker: docker,
		ZFS: zfs.Adapter{Prefix: cfg.ZFS.DatasetPrefix, MountPrefix: cfg.ZFS.MountPrefix},
		Caddy: caddy.Adapter{
			Dir: cfg.Caddy.ServiceConfigDir, SuspensionRoot: cfg.Caddy.SuspensionRoot, ActiveTemplate: cfg.Caddy.ActiveTemplate,
			ValidateCommand: cfg.Caddy.ValidateCommand, ReloadCommand: cfg.Caddy.ReloadCommand,
		},
		Health: healthcheck.Checker{
			Path: cfg.Deployment.HealthPath, Attempts: cfg.Deployment.HealthAttempts,
			InitialDelay: cfg.Deployment.HealthInitialDelay, Backoff: cfg.Deployment.HealthBackoff,
		},
		Metrics: metrics.Mock{}, Notify: notifier, Logger: logger,
	}
	if err := manager.RecoverInterrupted(); err != nil {
		fatal(fmt.Errorf("recover interrupted operations: %w", err))
	}
	handler := (&api.API{
		Manager: manager, Config: cfg, Token: token, Logger: logger,
		Version: version, Commit: commit, BuildDate: buildDate, DockerAPI: dockerAPI,
	}).Handler()
	server := &http.Server{
		Addr: cfg.Server.Listen, Handler: handler,
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: cfg.Server.RequestTimeout,
		WriteTimeout: cfg.Server.RequestTimeout, IdleTimeout: 60 * time.Second,
	}
	host, _, _ := net.SplitHostPort(cfg.Server.Listen)
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		logger.Warn("unencrypted HTTP listener is not loopback; restrict access to the Caddy proxy", "listen", cfg.Server.Listen)
	}
	go func() {
		logger.Info("controller starting", "listen", "http://"+cfg.Server.Listen, "version", version)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("controller stopped unexpectedly", "error", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
	}
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "modd-hosting-controller: startup failed: %v\n", err)
	os.Exit(1)
}
