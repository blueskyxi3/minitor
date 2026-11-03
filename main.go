package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/viper"

	"github.com/blueskyxi3/minitor/internal/api"
	"github.com/blueskyxi3/minitor/internal/config"
	"github.com/blueskyxi3/minitor/internal/discovery"
	"github.com/blueskyxi3/minitor/internal/vm"
)

//go:embed plugin.json
var manifest []byte

//go:embed web/build
var frontend embed.FS

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--vm-worker" {
		if err := vm.Supervise(os.Args[2:]); err != nil {
			slog.Error("database supervisor stopped", "error", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("monitor stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	bootstrapPath := os.Getenv("AIHUB_PLUGIN_CONFIG")
	if bootstrapPath == "" {
		bootstrapPath = "host-config.yaml"
	}
	settingsPath := os.Getenv("MONITOR_CONFIG_PATH")
	if settingsPath == "" {
		settingsPath = "./data/config.yaml"
	}
	hostConfig := flag.String("config", bootstrapPath, "SDK runtime configuration supplied by the host")
	monitorConfig := flag.String("monitor-config", settingsPath, "monitor collection/storage config.yaml")
	flag.Parse()
	v := viper.New()
	v.SetConfigFile(*hostConfig)
	v.SetEnvPrefix("MONITOR")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	v.SetDefault("plugin.id", "monitor")
	v.SetDefault("server.address", "127.0.0.1:3013")
	v.SetDefault("host.base_url", "http://127.0.0.1:3000")
	if err := v.ReadInConfig(); err != nil && !os.IsNotExist(err) {
		return err
	}
	if v.GetString("plugin.id") != "monitor" {
		return errors.New("plugin ID does not match embedded manifest")
	}
	secret := v.GetString("host.secret")
	if len(secret) < 32 {
		return errors.New("host.secret must contain at least 32 characters")
	}
	persistHostConfig := *hostConfig
	if os.Getenv("AIHUB_PLUGIN_CONFIG") != "" {
		persistHostConfig = ""
	}
	host, err := api.NewHost(v.GetString("host.base_url"), persistHostConfig)
	if err != nil {
		return err
	}
	settings, err := config.Open(*monitorConfig)
	if err != nil {
		return err
	}
	engine, err := vm.New(settings)
	if err != nil {
		return err
	}
	defer engine.Close()
	assets, err := fs.Sub(frontend, "web/build")
	if err != nil {
		return err
	}
	discoverer := discovery.New(settings, engine)
	discoverer.ReadHostInstances = func(ctx context.Context) ([]discovery.Instance, error) {
		return host.MonitorInstances(ctx, secret)
	}
	service := &api.Server{Secret: secret, Host: host, Settings: settings, Discovery: discoverer, Engine: engine, Manifest: manifest, Assets: assets}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var background sync.WaitGroup
	background.Add(1)
	go func() { defer background.Done(); discoverer.Run(ctx) }()
	server := &http.Server{Addr: v.GetString("server.address"), Handler: service.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	stopped := make(chan error, 1)
	go func() { stopped <- server.ListenAndServe() }()
	slog.Info("monitor listening", "address", server.Addr, "data_path", settings.DataPath(), "database_version", engine.Version)
	select {
	case <-ctx.Done():
	case err = <-engine.Failures:
	case err = <-stopped:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}
	cancel()
	shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	err = errors.Join(err, server.Shutdown(shutdown))
	background.Wait()
	return err
}
