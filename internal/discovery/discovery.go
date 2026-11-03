package discovery

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/blueskyxi3/minitor/internal/config"
	"github.com/blueskyxi3/minitor/internal/vm"
)

type Instance struct {
	ID                string `json:"id"`
	Hostname          string `json:"hostname"`
	NodeRole          string `json:"node_role"`
	BillingWorkerMode string `json:"billing_worker_mode"`
	WorkerEnabled     bool   `json:"worker_enabled"`
	WorkerReady       bool   `json:"worker_ready"`
	RouteWorkerMode   string `json:"route_worker_mode"`
	MetricsURL        string `json:"metrics_url"`
	LastHeartbeatAt   int64  `json:"last_heartbeat_at"`
	Available         bool   `json:"available"`
	Error             string `json:"error,omitempty"`
}

func (i Instance) API() bool { return i.BillingWorkerMode != "only" && i.RouteWorkerMode != "only" }

type Snapshot struct {
	Configured    bool       `json:"configured"`
	Complete      bool       `json:"complete"`
	LastSuccessAt int64      `json:"last_success_at"`
	CheckedAt     int64      `json:"checked_at"`
	Error         string     `json:"error,omitempty"`
	Instances     []Instance `json:"instances"`
}

type TargetWriter interface{ WriteTargets([]vm.Target) error }

type Discoverer struct {
	ReadHostInstances func(context.Context) ([]Instance, error)
	Settings          *config.Store
	Writer            TargetWriter
	mu                sync.RWMutex
	refresh           sync.Mutex
	snapshot          Snapshot
}

func New(store *config.Store, writer TargetWriter) *Discoverer {
	return &Discoverer{Settings: store, Writer: writer, snapshot: Snapshot{Instances: []Instance{}}}
}

func (d *Discoverer) Snapshot() Snapshot {
	d.mu.RLock()
	defer d.mu.RUnlock()
	s := d.snapshot
	s.Instances = append([]Instance{}, s.Instances...)
	return s
}

func (d *Discoverer) Refresh(ctx context.Context) error {
	d.refresh.Lock()
	defer d.refresh.Unlock()
	s := d.Settings.Get()
	result := Snapshot{Configured: s.Configured(), CheckedAt: time.Now().Unix(), Instances: []Instance{}}
	var targets []vm.Target
	var err error
	if !result.Configured {
		result.Error = "configure metrics credentials; instance discovery follows the host"
	} else {
		if d.ReadHostInstances == nil {
			err = errors.New("host instance reader is unavailable")
		} else {
			result.Instances, err = d.ReadHostInstances(ctx)
		}
		if err != nil {
			result.Error = "host instance discovery failed; check host Redis, host version and monitor service identity"
		} else {
			targets, result.Instances, result.Complete = BuildTargets(result.Instances)
			result.LastSuccessAt = time.Now().Unix()
		}
	}
	// A failed discovery is never replaced by a cached or SQL-derived target list.
	if writeErr := d.Writer.WriteTargets(targets); writeErr != nil {
		result.Error = "cannot publish local scrape targets"
		result.Complete = false
		err = errors.Join(err, writeErr)
	}
	d.mu.Lock()
	if result.LastSuccessAt == 0 {
		result.LastSuccessAt = d.snapshot.LastSuccessAt
	}
	d.snapshot = result
	d.mu.Unlock()
	return err
}

func (d *Discoverer) Run(ctx context.Context) {
	for {
		call, cancel := context.WithTimeout(ctx, time.Duration(d.Settings.Get().Collection.TimeoutSeconds)*time.Second)
		_ = d.Refresh(call)
		cancel()
		timer := time.NewTimer(time.Duration(d.Settings.Get().Collection.IntervalSeconds) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func BuildTargets(instances []Instance) ([]vm.Target, []Instance, bool) {
	selected := make([]Instance, 0, len(instances))
	counts := make(map[string]int)
	for _, instance := range instances {
		if instance.NodeRole == "plugin-deployer" || (!instance.API() && !instance.WorkerEnabled) {
			continue
		}
		if instance.MetricsURL != "" {
			counts[instance.MetricsURL]++
		}
		selected = append(selected, instance)
	}
	sort.Slice(selected, func(a, b int) bool { return selected[a].ID < selected[b].ID })
	targets := []vm.Target{}
	complete := true
	for n := range selected {
		i := &selected[n]
		i.Available = false
		u, err := url.Parse(i.MetricsURL)
		if i.Error == "" && (err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/api/metrics") {
			i.Error = "missing or invalid metrics URL; upgrade worker/gateway and check advertised address"
		}
		if counts[i.MetricsURL] > 1 {
			i.Error = "duplicate instance metrics address"
		}
		if i.Error != "" {
			complete = false
			continue
		}
		i.Available = true
		targets = append(targets, vm.Target{Targets: []string{u.Host}, Labels: map[string]string{
			"gateway_instance": i.ID, "gateway_role": i.NodeRole, "hostname": i.Hostname,
			"gateway_api": boolLabel(i.API()), "billing_worker": boolLabel(i.WorkerEnabled),
			"__scheme__": u.Scheme, "__metrics_path__": "/api/metrics",
		}})
	}
	return targets, selected, complete
}

func boolLabel(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
