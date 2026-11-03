package api

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blueskyxi3/minitor/internal/config"
	"github.com/blueskyxi3/minitor/internal/discovery"
	"github.com/blueskyxi3/minitor/internal/query"
)

type Engine interface {
	Address() string
	Apply(context.Context) error
}

type Server struct {
	Secret     string
	Host       *Host
	Settings   *config.Store
	Discovery  *discovery.Discoverer
	Engine     Engine
	Manifest   []byte
	Assets     fs.FS
	settingsMu sync.Mutex
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.Engine.Address()+"/health", nil)
		resp, err := (&http.Client{Timeout: time.Second}).Do(req)
		if err != nil {
			respond(w, 503, nil, "embedded database is unavailable")
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			respond(w, 503, nil, "embedded database is not ready")
			return
		}
		respond(w, 200, map[string]string{"status": "ok"}, "")
	})
	mux.Handle("GET /internal/manifest", s.internal(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(s.Manifest)
	})))
	mux.Handle("GET /internal/runtime", s.internal(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"host_url": s.Host.Base()})
	})))
	mux.Handle("PUT /internal/runtime", s.internal(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			HostURL string `json:"host_url"`
		}
		if decode(w, r, &input) != nil {
			respond(w, 400, nil, "invalid runtime configuration")
			return
		}
		if err := s.Host.SetBase(input.HostURL); err != nil {
			respond(w, 400, nil, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"host_url": s.Host.Base()})
	})))
	mux.Handle("GET /api/v1/self", s.authorize(false, false, func(w http.ResponseWriter, r *http.Request, user User) {
		respond(w, 200, map[string]any{"user_id": user.ID, "can_view": user.CanView(), "can_configure": user.CanConfigure()}, "")
	}))
	mux.Handle("GET /api/v1/settings", s.authorize(true, true, func(w http.ResponseWriter, r *http.Request, user User) { respond(w, 200, s.Settings.Public(), "") }))
	mux.Handle("PUT /api/v1/settings", s.authorize(true, true, s.saveSettings))
	mux.Handle("GET /api/v1/series", s.authorize(true, false, s.series))
	mux.Handle("GET /api/v1/options", s.authorize(true, false, s.options))
	mux.Handle("GET /api/v1/status", s.authorize(true, false, s.status))
	mux.Handle("GET /assets/", http.FileServer(http.FS(s.Assets)))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { respond(w, 404, nil, "not found") })
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/internal/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) internal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.Secret) < 32 || !hmac.Equal([]byte(s.Secret), []byte(r.Header.Get("X-AIHub-Plugin-Secret"))) {
			respond(w, 401, nil, "invalid service credentials")
			return
		}
		if r.Method != http.MethodGet {
			if base := r.Header.Get("X-AIHub-Host-Base-URL"); base != "" {
				if err := s.Host.SetBase(base); err != nil {
					respond(w, 400, nil, err.Error())
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authorize(view, configure bool, next func(http.ResponseWriter, *http.Request, User)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := Verify(s.Secret, r.Header.Get(IdentityHeader), r, time.Now())
		if !ok {
			respond(w, 401, nil, "invalid or expired host identity")
			return
		}
		user, err := s.Host.CurrentUser(r.Context(), r.Header.Get(DelegationHeader))
		if err != nil || user.ID != claims.UserID {
			respond(w, 401, nil, "cannot verify host user identity and permissions")
			return
		}
		if view && !user.CanView() {
			respond(w, 403, nil, "monitor read permission is required")
			return
		}
		if configure && !user.CanConfigure() {
			respond(w, 403, nil, "root permission is required to configure monitoring")
			return
		}
		next(w, r, user)
	})
}

func (s *Server) client() *query.Client { return query.NewClient(s.Engine.Address()) }

func (s *Server) series(w http.ResponseWriter, r *http.Request, _ User) {
	f, err := query.ParseFilter(r.URL.Query(), time.Now())
	if err != nil {
		respond(w, 400, nil, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	result, err := s.client().Dashboard(ctx, f, s.Settings.Get().Collection.IntervalSeconds)
	if err != nil {
		respond(w, 503, nil, err.Error())
		return
	}
	respond(w, 200, result, "")
}

func (s *Server) options(w http.ResponseWriter, r *http.Request, _ User) {
	f, err := query.ParseFilter(r.URL.Query(), time.Now())
	if err != nil {
		respond(w, 400, nil, err.Error())
		return
	}
	var rows []map[string]string
	params := url.Values{"match[]": []string{`{job="ai-hub-monitor",__name__=~"e2e_latency_seconds_count|ai_hub_llm_requests_in_flight|ai_hub_billing_stage_duration_seconds_count"}`}, "start": []string{strconv.FormatInt(f.Start, 10)}, "end": []string{strconv.FormatInt(f.End, 10)}}
	if err := s.client().Get(r.Context(), "/api/v1/series", params, &rows); err != nil {
		respond(w, 503, nil, err.Error())
		return
	}
	sets := map[string]map[string]bool{}
	for _, name := range []string{"model", "channel_id", "gateway_instance", "api_mode", "stage"} {
		sets[name] = map[string]bool{}
	}
	for _, row := range rows {
		for name, values := range sets {
			if value := row[name]; value != "" {
				values[value] = true
			}
		}
	}
	result := map[string][]string{}
	for name, values := range sets {
		result[name] = []string{}
		for value := range values {
			result[name] = append(result[name], value)
		}
		sort.Strings(result[name])
	}
	respond(w, 200, result, "")
}

func (s *Server) status(w http.ResponseWriter, r *http.Request, _ User) {
	snapshot := s.Discovery.Snapshot()
	now := time.Now().Unix()
	client := s.client()
	var targets struct {
		ActiveTargets []struct {
			Labels     map[string]string `json:"labels"`
			Health     string            `json:"health"`
			LastScrape string            `json:"lastScrape"`
			LastError  string            `json:"lastError"`
		} `json:"activeTargets"`
	}
	err := client.Get(r.Context(), "/api/v1/targets", nil, &targets)
	probes, probeErr := client.Query(r.Context(), `label_replace(max by(gateway_instance)(timestamp(go_goroutines{job="ai-hub-monitor"})),"monitor_probe","runtime","__name__",".*") or label_replace(max by(gateway_instance)(ai_hub_billing_health_sampled_at_seconds{job="ai-hub-monitor"}),"monitor_probe","billing","__name__",".*")`, query.Filter{End: now}, true)
	sampled := map[string]map[string]int64{}
	for _, probe := range probes {
		if probe.Value.Value == nil {
			continue
		}
		id := probe.Metric["gateway_instance"]
		if sampled[id] == nil {
			sampled[id] = map[string]int64{}
		}
		sampled[id][probe.Metric["monitor_probe"]] = int64(*probe.Value.Value)
	}
	type instanceStatus struct {
		discovery.Instance
		Health       string `json:"health"`
		LastScrape   string `json:"last_scrape,omitempty"`
		LastError    string `json:"last_error,omitempty"`
		LastSampleAt int64  `json:"last_sample_at,omitempty"`
	}
	rows := []instanceStatus{}
	complete := snapshot.Complete && snapshot.Configured && err == nil && probeErr == nil
	for _, i := range snapshot.Instances {
		row := instanceStatus{Instance: i, Health: "unknown"}
		for _, target := range targets.ActiveTargets {
			if target.Labels["gateway_instance"] == i.ID {
				row.Health, row.LastScrape = target.Health, target.LastScrape
				if target.LastError != "" {
					row.LastError = "metrics scrape failed; check instance address and metrics credentials"
				}
				break
			}
		}
		row.LastSampleAt = sampled[i.ID]["runtime"]
		if row.Health == "up" {
			if probeErr != nil {
				row.Health, row.LastError = "unknown", "cannot verify collected metrics"
			} else if row.LastSampleAt == 0 {
				row.Health, row.LastError = "missing", "required runtime metrics are missing"
			} else if row.LastSampleAt < now-int64(max(120, 4*s.Settings.Get().Collection.IntervalSeconds)) {
				row.Health, row.LastError = "stale", "collected metrics are stale"
			} else if i.WorkerEnabled && sampled[i.ID]["billing"] == 0 {
				row.Health, row.LastError = "missing", "billing observation metrics are missing; check worker health and version"
			} else if i.WorkerEnabled && sampled[i.ID]["billing"] < now-int64(max(120, 4*s.Settings.Get().Collection.IntervalSeconds)) {
				row.Health, row.LastError = "stale", "billing observations are stale"
			}
		}
		if row.Health != "up" || !i.Available {
			complete = false
		}
		rows = append(rows, row)
	}
	respond(w, 200, map[string]any{"configured": snapshot.Configured, "complete": complete, "database_available": err == nil && probeErr == nil, "discovery_error": snapshot.Error, "discovery_checked_at": snapshot.CheckedAt, "discovery_last_success_at": snapshot.LastSuccessAt, "instances": rows, "queried_at": now}, "")
}

func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request, _ User) {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	var input struct {
		Discovery *struct {
			RedisURL *string `json:"redis_url"`
		} `json:"discovery"`
		Metrics *struct {
			Username *string `json:"username"`
			Password *string `json:"password"`
		} `json:"metrics"`
		Collection *struct {
			IntervalSeconds int `json:"interval_seconds"`
			TimeoutSeconds  int `json:"timeout_seconds"`
		} `json:"collection"`
		Storage *struct {
			RetentionDays int `json:"retention_days"`
		} `json:"storage"`
	}
	if err := decode(w, r, &input); err != nil {
		respond(w, 400, nil, "invalid monitoring settings; storage.data_path is read-only")
		return
	}
	old := s.Settings.Get()
	next := old
	if input.Discovery != nil && input.Discovery.RedisURL != nil {
		next.Discovery.RedisURL = *input.Discovery.RedisURL
	}
	if input.Metrics != nil {
		if input.Metrics.Username != nil {
			next.Metrics.Username = *input.Metrics.Username
		}
		if input.Metrics.Password != nil {
			next.Metrics.Password = *input.Metrics.Password
		}
	}
	if input.Collection != nil {
		next.Collection.IntervalSeconds, next.Collection.TimeoutSeconds = input.Collection.IntervalSeconds, input.Collection.TimeoutSeconds
	}
	if input.Storage != nil {
		next.Storage.RetentionDays = input.Storage.RetentionDays
	}
	if err := s.Settings.Save(next); err != nil {
		respond(w, 400, nil, err.Error())
		return
	}
	if err := s.Engine.Apply(r.Context()); err != nil {
		rollback := s.Settings.Save(old)
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		rollback = errors.Join(rollback, s.Engine.Apply(ctx))
		message := "cannot apply monitoring settings; previous settings restored"
		if rollback != nil {
			message = "cannot apply or restore monitoring settings; inspect plugin logs and config.yaml"
		}
		respond(w, 503, nil, message)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(next.Collection.TimeoutSeconds)*time.Second)
	defer cancel()
	_ = s.Discovery.Refresh(ctx)
	respond(w, 200, s.Settings.Public(), "")
}

func decode(w http.ResponseWriter, r *http.Request, value any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("one JSON object is required")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
func respond(w http.ResponseWriter, status int, data any, message string) {
	writeJSON(w, status, map[string]any{"success": status >= 200 && status < 300, "message": message, "data": data})
}
