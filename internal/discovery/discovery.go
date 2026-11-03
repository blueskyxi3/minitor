package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/blueskyxi3/minitor/internal/config"
	"github.com/blueskyxi3/minitor/internal/vm"
)

const RegistryKey = "ai-hub:monitor:instances"
const InstancePrefix = "ai-hub:monitor:instance:"

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
	Settings *config.Store
	Writer   TargetWriter
	mu       sync.RWMutex
	refresh  sync.Mutex
	snapshot Snapshot
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
		result.Error = "configure Redis discovery and metrics credentials"
	} else {
		result.Instances, err = readInstances(ctx, s)
		if err != nil {
			result.Error = "Redis instance discovery failed; check its address and read-only ACL"
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

func readInstances(ctx context.Context, s config.Settings) ([]Instance, error) {
	options, err := redis.ParseURL(s.Discovery.RedisURL)
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(s.Collection.TimeoutSeconds) * time.Second
	options.DialTimeout, options.ReadTimeout, options.WriteTimeout = timeout, timeout, timeout
	options.PoolSize, options.MaxRetries, options.DisableIdentity = 2, -1, true
	client := redis.NewClient(options)
	defer client.Close()
	ids, err := client.SMembers(ctx, RegistryKey).Result()
	if err != nil {
		return nil, err
	}
	if len(ids) > 1000 {
		return nil, errors.New("instance registry exceeds 1000 entries")
	}
	if len(ids) == 0 {
		return []Instance{}, nil
	}
	keys := make([]string, len(ids))
	for n, id := range ids {
		keys[n] = InstancePrefix + id
	}
	values, err := client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	instances := make([]Instance, 0, len(values))
	for n, value := range values {
		if value == nil {
			continue
		} // Expired heartbeat, not a zero-valued instance.
		body, ok := value.(string)
		var instance Instance
		if !ok || json.Unmarshal([]byte(body), &instance) != nil || instance.ID != ids[n] {
			instances = append(instances, Instance{ID: ids[n], Error: "invalid heartbeat record"})
			continue
		}
		instances = append(instances, instance)
	}
	return instances, nil
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
