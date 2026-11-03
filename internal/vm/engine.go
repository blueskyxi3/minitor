package vm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"

	"github.com/blueskyxi3/minitor/internal/config"
)

//go:embed bundle/*
var bundle embed.FS

type Metadata struct {
	Version       string `json:"version"`
	Platform      string `json:"platform"`
	ArchiveSHA256 string `json:"archive_sha256"`
	BinarySHA256  string `json:"binary_sha256"`
}

func Extract(directory string) (string, Metadata, error) {
	var meta Metadata
	body, err := bundle.ReadFile("bundle/metadata.json")
	if err != nil {
		return "", meta, errors.New("VictoriaMetrics bundle missing: run make build")
	}
	if err := json.Unmarshal(body, &meta); err != nil {
		return "", meta, err
	}
	if meta.Platform != runtime.GOOS+"-"+runtime.GOARCH {
		return "", meta, errors.New("VictoriaMetrics bundle platform mismatch")
	}
	path := filepath.Join(directory, "victoria-metrics-"+meta.Version+"-"+meta.Platform)
	if saved, err := os.ReadFile(path); err == nil && hash(saved) == meta.BinarySHA256 {
		return path, meta, nil
	}
	archive, err := bundle.ReadFile("bundle/victoria-metrics.tar.gz")
	if err != nil {
		return "", meta, err
	}
	if hash(archive) != meta.ArchiveSHA256 {
		return "", meta, errors.New("VictoriaMetrics archive checksum mismatch")
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return "", meta, err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	for {
		h, err := reader.Next()
		if err != nil {
			return "", meta, fmt.Errorf("VictoriaMetrics executable missing: %w", err)
		}
		if h.Name != "victoria-metrics-prod" {
			continue
		}
		if h.Typeflag != tar.TypeReg || h.Size > 150<<20 {
			return "", meta, errors.New("invalid VictoriaMetrics executable")
		}
		binary, err := io.ReadAll(io.LimitReader(reader, 150<<20+1))
		if err != nil {
			return "", meta, err
		}
		if hash(binary) != meta.BinarySHA256 {
			return "", meta, errors.New("VictoriaMetrics executable checksum mismatch")
		}
		if err := config.WritePrivate(path, binary); err != nil {
			return "", meta, err
		}
		if err := os.Chmod(path, 0700); err != nil {
			return "", meta, err
		}
		return path, meta, nil
	}
}

func hash(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }

type running struct {
	cmd      *exec.Cmd
	pipe     *os.File
	done     chan struct{}
	err      error
	expected atomic.Bool
}

type Engine struct {
	mu         sync.Mutex
	Settings   *config.Store
	RuntimeDir string
	BaseURL    string
	Version    string
	binary     string
	lock       *os.File
	process    *running
	retention  int
	Failures   chan error
}

func New(store *config.Store) (*Engine, error) {
	dataPath := store.DataPath()
	if err := os.MkdirAll(dataPath, 0700); err != nil {
		return nil, fmt.Errorf("create storage.data_path: %w", err)
	}
	lock, err := os.OpenFile(filepath.Join(dataPath, ".monitor.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("storage.data_path is not writable: %w", err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("storage.data_path is already in use")
	}
	e := &Engine{Settings: store, RuntimeDir: filepath.Join(filepath.Dir(store.Path), ".runtime"), lock: lock, Failures: make(chan error, 1)}
	binary, meta, err := Extract(filepath.Join(e.RuntimeDir, "bin"))
	if err != nil {
		e.Close()
		return nil, err
	}
	e.binary = binary
	e.Version = meta.Version
	if err := e.WriteTargets([]Target{}); err != nil {
		e.Close()
		return nil, err
	}
	if err := e.Apply(context.Background()); err != nil {
		e.Close()
		return nil, err
	}
	return e, nil
}

func (e *Engine) Apply(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.Settings.Get()
	if err := e.writeScrapeConfig(s); err != nil {
		return err
	}
	if e.process != nil && e.retention == s.Storage.RetentionDays {
		return nil
	}
	if err := e.stop(); err != nil {
		return err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		return err
	}
	args := []string{"--vm-worker", e.binary,
		"-httpListenAddr=" + address,
		"-storageDataPath=" + e.Settings.DataPath(),
		"-retentionPeriod=" + strconv.Itoa(s.Storage.RetentionDays) + "d",
		"-promscrape.config=" + filepath.Join(e.RuntimeDir, "scrape.yaml"),
		"-promscrape.fileSDCheckInterval=1s", "-promscrape.configCheckInterval=1s",
		"-memory.allowedBytes=512MiB", "-search.latencyOffset=0s", "-loggerLevel=WARN",
	}
	cmd := exec.Command(executable, args...)
	cmd.ExtraFiles = []*os.File{readPipe}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	p := &running{cmd: cmd, pipe: writePipe, done: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		readPipe.Close()
		writePipe.Close()
		return err
	}
	readPipe.Close()
	e.process, e.retention, e.BaseURL = p, s.Storage.RetentionDays, "http://"+address
	go func() {
		p.err = cmd.Wait()
		close(p.done)
		if !p.expected.Load() {
			select {
			case e.Failures <- errors.New("embedded VictoriaMetrics stopped unexpectedly"):
			default:
			}
		}
	}()
	startup, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 500 * time.Millisecond}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		req, _ := http.NewRequestWithContext(startup, http.MethodGet, e.BaseURL+"/health", nil)
		response, err := client.Do(req)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				return nil
			}
		}
		select {
		case <-startup.Done():
			e.stop()
			return errors.New("embedded VictoriaMetrics startup timed out")
		case <-p.done:
			return errors.New("embedded VictoriaMetrics failed to start; check data permissions and database logs")
		case <-ticker.C:
		}
	}
}

func (e *Engine) stop() error {
	p := e.process
	if p == nil {
		return nil
	}
	p.expected.Store(true)
	p.pipe.Close()
	select {
	case <-p.done:
	case <-time.After(12 * time.Second):
		if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		<-p.done
	}
	e.process = nil
	return nil
}

func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	err := e.stop()
	if e.lock != nil {
		err = errors.Join(err, e.lock.Close())
		e.lock = nil
	}
	return err
}

type Target struct {
	Targets []string          `json:"targets"`
	Labels  map[string]string `json:"labels"`
}

func (e *Engine) Address() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.BaseURL
}

func (e *Engine) WriteTargets(targets []Target) error {
	if targets == nil {
		targets = []Target{}
	}
	body, err := json.Marshal(targets)
	if err != nil {
		return err
	}
	return config.WritePrivate(filepath.Join(e.RuntimeDir, "targets.json"), body)
}

func (e *Engine) writeScrapeConfig(s config.Settings) error {
	job := map[string]any{
		"job_name": "ai-hub-monitor", "scrape_interval": fmt.Sprintf("%ds", s.Collection.IntervalSeconds),
		"scrape_timeout": fmt.Sprintf("%ds", s.Collection.TimeoutSeconds), "metrics_path": "/api/metrics",
		"follow_redirects": false, "max_scrape_size": "16MiB",
		"file_sd_configs":        []any{map[string]any{"files": []string{filepath.Join(e.RuntimeDir, "targets.json")}}},
		"basic_auth":             map[string]string{"username": s.Metrics.Username, "password": s.Metrics.Password},
		"metric_relabel_configs": []any{map[string]any{"source_labels": []string{"__name__"}, "action": "keep", "regex": "(gateway_processing_seconds|ttft_seconds|tpot_seconds|e2e_latency_seconds|http_request_duration_seconds)_(bucket|sum|count)|http_requests_total|provider_requests_total|ai_hub_llm_requests_in_flight|app_panics_total|go_.*|process_.*|db_pool_.*|dependency_failures_total|request_error_log.*|billing_.*|media_usage_stream|ai_hub_billing_.*"}},
	}
	body, err := yaml.Marshal(map[string]any{"scrape_configs": []any{job}})
	if err != nil {
		return err
	}
	return config.WritePrivate(filepath.Join(e.RuntimeDir, "scrape.yaml"), body)
}
