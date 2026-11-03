package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blueskyxi3/minitor/internal/config"
)

func TestConfigurationPathsAndPrivatePersistence(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "settings", "config.yaml")
	store, err := config.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.DataPath(); got != filepath.Join(directory, "settings", "victoria-metrics-data") {
		t.Fatalf("relative path resolved incorrectly: %s", got)
	}
	s := store.Get()
	s.Discovery.RedisURL = "redis://reader:redis-secret@localhost:6379/2"
	s.Metrics.Username = "metrics"
	s.Metrics.Password = "metrics-secret"
	if err := store.Save(s); err != nil {
		t.Fatal(err)
	}
	encoded, _ := os.ReadFile(path)
	if !strings.Contains(string(encoded), "redis-secret") {
		t.Fatal("configuration was not persisted")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("configuration permissions: %v", info.Mode())
	}
	public := store.Public()
	if public["discovery"].(map[string]any)["source"] != "host" {
		t.Fatal("Redis secret leaked")
	}
	if _, ok := public["metrics"].(map[string]any)["password"]; ok {
		t.Fatal("metrics secret leaked")
	}
	s.Storage.DataPath = "/different/path"
	if err := store.Save(s); err == nil {
		t.Fatal("data path changed through the settings API")
	}
	reopened, err := config.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Get().Metrics.Password != "metrics-secret" {
		t.Fatal("settings did not survive restart")
	}
}

func TestAbsoluteDataPathAndInvalidConfiguration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	abs := filepath.Join(dir, "outside", "database")
	body := "version: 1\nstorage:\n  data_path: " + abs + "\n  retention_days: 30\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := config.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if store.DataPath() != abs {
		t.Fatalf("absolute data path changed: %s", store.DataPath())
	}
	for _, body := range []string{"version: 2\n", "version: 1\nunapproved_field: true\n", "version: 1\n---\nversion: 1\n", "version: 1\ncollection:\n  interval_seconds: 5\n  timeout_seconds: 5\n"} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := config.Open(path); err == nil {
			t.Fatalf("accepted invalid settings: %s", body)
		}
	}
}
