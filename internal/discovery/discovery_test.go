package discovery_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blueskyxi3/minitor/internal/config"
	"github.com/blueskyxi3/minitor/internal/discovery"
	"github.com/blueskyxi3/minitor/internal/vm"
)

type writer struct{ targets []vm.Target }

func (w *writer) WriteTargets(targets []vm.Target) error { w.targets = targets; return nil }

func TestGatewayEmbeddedAndIndependentWorkersAreScrapedOnce(t *testing.T) {
	instances := []discovery.Instance{
		{ID: "master", NodeRole: "master", WorkerEnabled: true, BillingWorkerMode: "embedded", MetricsURL: "http://192.0.2.1:3000/api/metrics"},
		{ID: "slave", NodeRole: "slave", BillingWorkerMode: "disabled", MetricsURL: "http://192.0.2.2:3000/api/metrics"},
		{ID: "billing", NodeRole: "slave", WorkerEnabled: true, BillingWorkerMode: "only", MetricsURL: "http://192.0.2.3:3000/api/metrics"},
		{ID: "route", NodeRole: "slave", RouteWorkerMode: "only", MetricsURL: "http://192.0.2.4:3000/api/metrics"},
		{ID: "deployer", NodeRole: "plugin-deployer"},
	}
	targets, rows, complete := discovery.BuildTargets(instances)
	if !complete || len(targets) != 3 || len(rows) != 3 {
		t.Fatalf("unexpected discovery: %+v %+v %v", targets, rows, complete)
	}
	for _, target := range targets {
		if target.Labels["gateway_instance"] == "billing" && (target.Labels["gateway_api"] != "false" || target.Labels["billing_worker"] != "true") {
			t.Fatal("worker identity was lost")
		}
	}
}

func TestDuplicateAndUnsafeTargetsAreUnavailable(t *testing.T) {
	for _, address := range []string{"http://user:secret@localhost/api/metrics", "http://localhost/metrics", "http://localhost/api/metrics?secret=x", "file:///api/metrics"} {
		targets, rows, complete := discovery.BuildTargets([]discovery.Instance{{ID: "bad", MetricsURL: address}})
		if complete || len(targets) != 0 || rows[0].Error == "" {
			t.Fatalf("accepted unsafe target: %s", address)
		}
	}
	targets, rows, complete := discovery.BuildTargets([]discovery.Instance{{ID: "a", MetricsURL: "http://localhost:3000/api/metrics"}, {ID: "b", MetricsURL: "http://localhost:3000/api/metrics"}})
	if complete || len(targets) != 0 || len(rows) != 2 {
		t.Fatal("duplicate target was counted twice")
	}
}

func TestRedisFailureClearsTargetsWithoutExposingCredentials(t *testing.T) {
	store, err := config.Open(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := store.Get()
	s.Discovery.RedisURL = "redis://user:must-not-leak@127.0.0.1:1/0"
	s.Metrics.Username = "test"
	s.Metrics.Password = "secret"
	if err := store.Save(s); err != nil {
		t.Fatal(err)
	}
	w := &writer{targets: []vm.Target{{Targets: []string{"old"}}}}
	d := discovery.New(store, w)
	if err := d.Refresh(context.Background()); err == nil {
		t.Fatal("Redis failure was swallowed")
	}
	if len(w.targets) != 0 {
		t.Fatal("retained stale collection targets")
	}
	snapshot := d.Snapshot()
	if snapshot.Complete || snapshot.Error == "" || strings.Contains(snapshot.Error, "must-not-leak") {
		t.Fatal("invalid failure snapshot")
	}
}
