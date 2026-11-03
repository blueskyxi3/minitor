package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

type Settings struct {
	Version int `yaml:"version" json:"version"`
	// Deprecated RedisURL is accepted for 0.0.1 files; discovery always uses the host.
	Discovery struct {
		RedisURL string `yaml:"redis_url" json:"redis_url"`
	} `yaml:"discovery,omitempty" json:"discovery"`
	Metrics struct {
		Username string `yaml:"username" json:"username"`
		Password string `yaml:"password" json:"password"`
	} `yaml:"metrics" json:"metrics"`
	Collection struct {
		IntervalSeconds int `yaml:"interval_seconds" json:"interval_seconds"`
		TimeoutSeconds  int `yaml:"timeout_seconds" json:"timeout_seconds"`
	} `yaml:"collection" json:"collection"`
	Storage struct {
		DataPath      string `yaml:"data_path" json:"data_path"`
		RetentionDays int    `yaml:"retention_days" json:"retention_days"`
	} `yaml:"storage" json:"storage"`
}

func Defaults() Settings {
	var s Settings
	s.Version = 1
	s.Collection.IntervalSeconds, s.Collection.TimeoutSeconds = 15, 5
	s.Storage.DataPath, s.Storage.RetentionDays = "./victoria-metrics-data", 30
	return s
}

func (s Settings) Validate() error {
	if s.Version != 1 {
		return errors.New("unsupported configuration version")
	}
	if s.Collection.IntervalSeconds < 5 || s.Collection.IntervalSeconds > 300 || s.Collection.TimeoutSeconds < 1 || s.Collection.TimeoutSeconds >= s.Collection.IntervalSeconds {
		return errors.New("collection interval must be 5–300s and timeout must be positive and shorter than interval")
	}
	if s.Storage.DataPath == "" || s.Storage.RetentionDays < 1 || s.Storage.RetentionDays > 3650 {
		return errors.New("storage path is required; retention must be 1–3650 days")
	}
	return nil
}

func (s Settings) Configured() bool {
	return s.Metrics.Username != "" && s.Metrics.Password != ""
}

type Store struct {
	mu       sync.RWMutex
	Path     string
	settings Settings
}

func Open(path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	s := Defaults()
	body, err := os.ReadFile(abs)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		decoder := yaml.NewDecoder(bytes.NewReader(body))
		var document any
		if err := decoder.Decode(&document); err != nil {
			return nil, fmt.Errorf("read monitor config: %w", err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return nil, errors.New("monitor config must contain one YAML document")
		}
		v := viper.New()
		v.SetConfigType("yaml")
		if err := v.ReadConfig(bytes.NewReader(body)); err != nil {
			return nil, fmt.Errorf("read monitor config: %w", err)
		}
		if err := v.UnmarshalExact(&s, func(c *mapstructure.DecoderConfig) { c.TagName = "yaml"; c.WeaklyTypedInput = false }); err != nil {
			return nil, fmt.Errorf("decode monitor config: %w", err)
		}
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	store := &Store{Path: abs, settings: s}
	if os.IsNotExist(err) {
		body, err = yaml.Marshal(s)
		if err != nil {
			return nil, err
		}
		if err := WritePrivate(abs, body); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func (s *Store) Get() Settings { s.mu.RLock(); defer s.mu.RUnlock(); return s.settings }

func (s *Store) DataPath() string {
	path := s.Get().Storage.DataPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(filepath.Dir(s.Path), path)
	}
	return filepath.Clean(path)
}

func (s *Store) Save(next Settings) error {
	if err := next.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if next.Storage.DataPath != s.settings.Storage.DataPath {
		return errors.New("edit storage.data_path in config.yaml and restart; data is not automatically migrated")
	}
	body, err := yaml.Marshal(next)
	if err != nil {
		return err
	}
	if err := WritePrivate(s.Path, body); err != nil {
		return err
	}
	s.settings = next
	return nil
}

func WritePrivate(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".monitor-*")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if _, err := f.Write(body); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

func (s *Store) Public() map[string]any {
	v := s.Get()
	return map[string]any{
		"version": v.Version, "configured": v.Configured(),
		"discovery":  map[string]any{"source": "host"},
		"metrics":    map[string]any{"username": v.Metrics.Username, "password_configured": v.Metrics.Password != ""},
		"collection": v.Collection, "storage": v.Storage,
		"resolved_data_path": s.DataPath(), "config_path": s.Path,
	}
}
