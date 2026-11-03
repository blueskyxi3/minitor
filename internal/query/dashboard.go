package query

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Point struct {
	Timestamp   int64               `json:"timestamp"`
	Values      map[string]*float64 `json:"values"`
	LowerBounds map[string]bool     `json:"lower_bounds,omitempty"`
}

type Metric struct {
	Key         string              `json:"key"`
	Unit        string              `json:"unit"`
	Histogram   bool                `json:"histogram"`
	Supported   bool                `json:"supported"`
	Reason      string              `json:"reason,omitempty"`
	Stale       bool                `json:"stale"`
	Points      []Point             `json:"points"`
	Summary     map[string]*float64 `json:"summary"`
	LowerBounds map[string]bool     `json:"lower_bounds,omitempty"`
}

type ModelRow struct {
	Model       string                         `json:"model"`
	Metrics     map[string]map[string]*float64 `json:"metrics"`
	LowerBounds map[string]map[string]bool     `json:"lower_bounds,omitempty"`
}

type Dashboard struct {
	Filter      Filter     `json:"filter"`
	Window      int64      `json:"window_seconds"`
	QueriedAt   int64      `json:"queried_at"`
	Metrics     []Metric   `json:"metrics"`
	Models      []ModelRow `json:"models"`
	Approximate bool       `json:"approximate"`
}

type definition struct {
	key, metric, unit, kind string
	dims                    int
	extra                   []string
	bound                   float64
}

type expression struct{ key, query string }

const latencyDims = DimInstance | DimModel | DimChannel | DimAPI | DimStream

func definitions(f Filter) []definition {
	if f.Scope != "billing" {
		return []definition{
			{key: "gateway", metric: "gateway_processing_seconds", unit: "ms", kind: "histogram", dims: latencyDims, bound: 30},
			{key: "ttft", metric: "ttft_seconds", unit: "ms", kind: "histogram", dims: latencyDims, bound: 300},
			{key: "tpot", metric: "tpot_seconds", unit: "ms/token", kind: "histogram", dims: latencyDims, bound: 2},
			{key: "e2e", metric: "e2e_latency_seconds", unit: "ms", kind: "histogram", dims: latencyDims, bound: 300},
			{key: "http_rps", metric: "http_requests_total", unit: "req/s", kind: "rate", dims: DimInstance, extra: []string{`path!~"/api/metrics.*|/api/plugin-bridge/.*|/api/status"`}},
			{key: "http_error_rate", metric: "http_requests_total", unit: "%", kind: "http_error", dims: DimInstance},
			{key: "http_4xx", metric: "http_requests_total", unit: "req/s", kind: "rate", dims: DimInstance, extra: []string{`code=~"4.."`}},
			{key: "http_5xx", metric: "http_requests_total", unit: "req/s", kind: "rate", dims: DimInstance, extra: []string{`code=~"5.."`}},
			{key: "http_429", metric: "http_requests_total", unit: "req/s", kind: "rate", dims: DimInstance, extra: []string{`code="429"`}},
			{key: "provider_rps", metric: "provider_requests_total", unit: "attempt/s", kind: "rate", dims: DimInstance | DimModel | DimChannel},
			{key: "provider_error_rate", metric: "provider_requests_total", unit: "%", kind: "provider_error", dims: DimInstance | DimModel | DimChannel},
			{key: "concurrency", metric: "ai_hub_llm_requests_in_flight", unit: "requests", kind: "gauge", dims: DimInstance | DimModel},
			{key: "panics", metric: "app_panics_total", unit: "events/s", kind: "rate", dims: DimInstance},
			{key: "cpu", metric: "process_cpu_seconds_total", unit: "cores", kind: "rate", dims: DimInstance},
			{key: "memory", metric: "process_resident_memory_bytes", unit: "bytes", kind: "gauge", dims: DimInstance},
			{key: "goroutines", metric: "go_goroutines", unit: "goroutines", kind: "gauge", dims: DimInstance},
			{key: "gc", metric: "go_gc_duration_seconds", unit: "s", kind: "gc", dims: DimInstance},
			{key: "db_open", metric: "db_pool_open_connections", unit: "connections", kind: "gauge", dims: DimInstance},
			{key: "db_in_use", metric: "db_pool_in_use_connections", unit: "connections", kind: "gauge", dims: DimInstance},
			{key: "db_wait", metric: "db_pool_wait_seconds_total", unit: "s/s", kind: "rate", dims: DimInstance},
			{key: "scrape_coverage", metric: "up", unit: "%", kind: "coverage", dims: DimInstance},
		}
	}
	defs := []definition{
		{key: "billing_event_batches", metric: "ai_hub_billing_stage_duration_seconds_count", unit: "batches/s", kind: "rate", dims: DimInstance, extra: []string{`stage="event_apply"`}},
		{key: "billing_errors", metric: "ai_hub_billing_stage_duration_seconds_count", unit: "%", kind: "billing_error", dims: DimInstance, extra: []string{`stage="event_apply"`}},
		{key: "billing_settled_orders", metric: "ai_hub_billing_batch_size_sum", unit: "orders/s", kind: "rate", dims: DimInstance, extra: []string{`stage="wallet_settlement"`}},
		{key: "billing_batch", metric: "ai_hub_billing_batch_size", unit: "orders", kind: "histogram", dims: DimInstance, extra: []string{`stage="wallet_settlement"`}, bound: 128},
	}
	stages := []string{"event_queue", "event_apply", "database_budget", "settlement", "wallet_lock", "financial_commit", "reconciliation"}
	if f.Stage != "" {
		stages = []string{f.Stage}
	}
	for _, stage := range stages {
		defs = append(defs, definition{key: "stage_" + stage, metric: "ai_hub_billing_stage_duration_seconds", unit: "ms", kind: "histogram", dims: DimInstance, extra: []string{"stage=" + strconv.Quote(stage)}, bound: 300})
	}
	for _, item := range []struct{ key, metric, kind, state, unit string }{
		{"billing_backlog", "ai_hub_billing_pipeline", "pipeline", "healthy_backlog", "items"},
		{"billing_oldest", "ai_hub_billing_pipeline", "pipeline", "oldest_seconds", "s"},
		{"billing_deferred", "ai_hub_billing_pipeline", "pipeline", "deferred", "items"},
		{"billing_isolated", "ai_hub_billing_pipeline", "pipeline", "isolated", "items"},
		{"billing_pending", "billing_stream", "billing_stream", "pending", "items"},
		{"billing_stream_length", "billing_stream", "billing_stream", "length", "items"},
		{"billing_dlq", "billing_stream", "billing_stream", "dlq", "items"},
		{"billing_pending_age", "billing_stream", "billing_stream", "oldest_pending_seconds", "s"},
		{"billing_retrying", "billing_orders", "orders", "retrying", "orders"},
		{"media_pending", "media_usage_stream", "media_usage_stream", "pending", "items"},
		{"media_dlq", "media_usage_stream", "media_usage_stream", "dlq", "items"},
	} {
		defs = append(defs, definition{key: item.key, metric: item.metric, unit: item.unit, kind: "snapshot:" + item.kind, dims: DimInstance, extra: []string{"state=" + strconv.Quote(item.state)}})
	}
	return append(defs,
		definition{key: "sql_settlement_backlog", metric: "ai_hub_billing_backlog_snapshot", unit: "orders", kind: "snapshot:sql", dims: DimInstance, extra: []string{`kind="settlement"`, `state="count"`}},
		definition{key: "sql_reconciliation_backlog", metric: "ai_hub_billing_backlog_snapshot", unit: "orders", kind: "snapshot:sql", dims: DimInstance, extra: []string{`kind="reconciliation"`, `state="count"`}},
	)
}

func buildExpressions(d definition, f Filter, window int64, group string, interval int) []expression {
	if f.Scope == "billing" {
		f.Stage = ""
	} // Stage selects latency panels, not the whole worker's throughput.
	base := []string{`gateway_api="true"`}
	if f.Scope == "billing" {
		base = []string{`billing_worker="true"`}
	}
	extra := append(append([]string{}, base...), d.extra...)
	selectMetric := func(name string, labels ...string) string {
		return f.Selector(name, d.dims, append(append([]string{}, extra...), labels...)...)
	}
	span := fmt.Sprintf("[%ds]", window)
	agg := "sum"
	bucketsAgg := "sum by (le)"
	if group != "" {
		agg = "sum by (" + group + ")"
		bucketsAgg = "sum by (le," + group + ")"
	}
	wrap := func(stat, expr string) expression { return expression{key: d.key + "__" + stat, query: expr} }
	if d.kind == "histogram" {
		buckets := bucketsAgg + "(increase(" + selectMetric(d.metric+"_bucket") + span + "))"
		count := agg + "(increase(" + selectMetric(d.metric+"_count") + span + "))"
		factor := "1000"
		if d.unit == "orders" {
			factor = "1"
		}
		result := []expression{wrap("count", count), wrap("mean", factor+"*"+agg+"(increase("+selectMetric(d.metric+"_sum")+span+"))/"+count)}
		for _, q := range []string{"50", "90", "95", "99"} {
			result = append(result, wrap("p"+q, factor+"*histogram_quantile(0."+q+","+buckets+")"))
		}
		result = append(result, wrap("finite_fraction", agg+"(increase("+selectMetric(d.metric+"_bucket", "le="+strconv.Quote(strconv.FormatFloat(d.bound, 'g', -1, 64)))+span+"))/"+count))
		return result
	}
	rate := func(labels ...string) string { return agg + "(rate(" + selectMetric(d.metric, labels...) + span + "))" }
	var expr, observed string
	switch d.kind {
	case "rate":
		expr = rate()
	case "http_error":
		expr = "100*(" + rate(`code=~"4..|5.."`) + " or (0*" + rate() + "))/" + rate()
	case "provider_error":
		expr = "100*(" + rate(`type=~"4..|5.."`) + " or (0*" + rate() + "))/" + rate()
	case "billing_error":
		expr = "100*(" + rate(`result="error"`) + " or (0*" + rate() + "))/" + rate()
	case "gc":
		expr = "sum(rate(" + selectMetric(d.metric+"_sum") + span + "))/sum(rate(" + selectMetric(d.metric+"_count") + span + "))"
	case "coverage":
		expr = "100*sum(" + selectMetric("up") + ")/count(" + selectMetric("up") + ")"
	default:
		up := f.Selector("up", DimInstance, base...)
		if strings.HasPrefix(d.kind, "snapshot:") {
			kind := strings.TrimPrefix(d.kind, "snapshot:")
			freshMetric := "ai_hub_billing_health_sampled_at_seconds"
			freshLabels := []string{`kind=` + strconv.Quote(kind)}
			if kind == "sql" {
				freshMetric, freshLabels = "ai_hub_billing_backlog_sampled_at_seconds", nil
			}
			fresh := f.Selector(freshMetric, DimInstance, append(append([]string{}, base...), freshLabels...)...)
			observed = "max(" + fresh + " and on(gateway_instance) (" + up + "==1))"
			ttl := max(120, 4*interval)
			latest := "topk(1,(" + fresh + ">(time()-" + strconv.Itoa(ttl) + ")) and on(gateway_instance) (" + up + "==1))"
			expr = "sum((" + selectMetric(d.metric) + ">=0) and on(gateway_instance) " + latest + ")"
		} else {
			expr = agg + "(" + selectMetric(d.metric) + " and on(gateway_instance) (" + up + "==1))"
		}
	}
	result := []expression{wrap("value", expr)}
	if observed != "" {
		result = append(result, wrap("observed_at", observed))
	}
	return result
}

// Query batches independent expressions under bounded concurrency. The browser
// never submits a raw expression; only the fixed definitions above are queried.
func (c *Client) execute(ctx context.Context, expressions []expression, f Filter, instant bool) (map[string][]Series, error) {
	result := make(map[string][]Series)
	if len(expressions) == 0 {
		return result, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	var firstErr error
	var wg sync.WaitGroup
	jobs := make(chan []expression)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for chunk := range jobs {
				parts := make([]string, len(chunk))
				for i, e := range chunk {
					parts[i] = `label_replace((` + e.query + `),"monitor_key",` + strconv.Quote(e.key) + `,"__name__",".*")`
				}
				series, err := c.Query(ctx, strings.Join(parts, " or "), f, instant)
				mu.Lock()
				if err != nil {
					if firstErr == nil {
						firstErr = err
					}
					cancel()
				} else {
					for _, s := range series {
						key := s.Metric["monitor_key"]
						result[key] = append(result[key], s)
					}
				}
				mu.Unlock()
			}
		}()
	}
	for start := 0; start < len(expressions); start += 6 {
		select {
		case jobs <- expressions[start:min(start+6, len(expressions))]:
		case <-ctx.Done():
		}
	}
	close(jobs)
	wg.Wait()
	return result, firstErr
}

func (c *Client) Dashboard(ctx context.Context, f Filter, interval int) (Dashboard, error) {
	result := Dashboard{Filter: f, Window: max(60, max(f.Step, int64(4*interval))), QueriedAt: time.Now().Unix(), Metrics: []Metric{}, Models: []ModelRow{}, Approximate: true}
	defs := definitions(f)
	var trendExpressions, summaryExpressions []expression
	for _, d := range defs {
		check := f
		if f.Scope == "billing" {
			check.Stage = ""
		}
		supported := check.Compatible(d.dims)
		metric := Metric{Key: d.key, Unit: d.unit, Histogram: d.kind == "histogram", Supported: supported, Points: []Point{}, Summary: map[string]*float64{}}
		if !supported {
			metric.Reason = "unsupported_filters"
		} else {
			trendExpressions = append(trendExpressions, buildExpressions(d, f, result.Window, "", interval)...)
			summaryExpressions = append(summaryExpressions, buildExpressions(d, f, f.End-f.Start, "", interval)...)
		}
		result.Metrics = append(result.Metrics, metric)
	}
	trend, err := c.execute(ctx, trendExpressions, f, false)
	if err != nil {
		return result, err
	}
	summary, err := c.execute(ctx, summaryExpressions, f, true)
	if err != nil {
		return result, err
	}
	for n := range result.Metrics {
		m := &result.Metrics[n]
		if !m.Supported {
			continue
		}
		stats := []string{"value"}
		if m.Histogram {
			stats = []string{"p50", "p90", "p95", "p99", "mean", "count", "finite_fraction"}
		} else if strings.HasPrefix(defs[n].kind, "snapshot:") {
			stats = append(stats, "observed_at")
		}
		points := make(map[int64]*Point)
		for at := f.GridStart(); at <= f.End; at += f.Step {
			p := &Point{Timestamp: at, Values: map[string]*float64{}}
			for _, stat := range stats {
				p.Values[stat] = nil
			}
			points[at] = p
		}
		for _, stat := range stats {
			key := m.Key + "__" + stat
			m.Summary[stat] = nil
			if len(summary[key]) > 1 {
				return result, errors.New("unexpected aggregate series count")
			}
			if len(summary[key]) == 1 {
				m.Summary[stat] = summary[key][0].Value.Value
			}
			if len(trend[key]) > 1 {
				return result, errors.New("unexpected aggregate series count")
			}
			for _, series := range trend[key] {
				for _, value := range series.Values {
					if p := points[value.Timestamp]; p != nil {
						p.Values[stat] = value.Value
					}
				}
			}
		}
		m.LowerBounds = lowerBounds(m.Summary)
		if observed := m.Summary["observed_at"]; observed != nil {
			m.Stale = *observed < float64(f.End-int64(max(120, 4*interval)))
		}
		for at := f.GridStart(); at <= f.End; at += f.Step {
			p := points[at]
			p.LowerBounds = lowerBounds(p.Values)
			m.Points = append(m.Points, *p)
		}
	}
	if f.Scope == "model" && f.Model == "" {
		var expressions []expression
		for _, d := range defs[:4] {
			expressions = append(expressions, buildExpressions(d, f, f.End-f.Start, "model", interval)...)
		}
		groups, err := c.execute(ctx, expressions, f, true)
		if err != nil {
			return result, err
		}
		rows := make(map[string]*ModelRow)
		for key, series := range groups {
			metric, stat, _ := strings.Cut(key, "__")
			for _, s := range series {
				model := s.Metric["model"]
				if rows[model] == nil {
					rows[model] = &ModelRow{Model: model, Metrics: map[string]map[string]*float64{}, LowerBounds: map[string]map[string]bool{}}
				}
				if rows[model].Metrics[metric] == nil {
					rows[model].Metrics[metric] = map[string]*float64{}
				}
				rows[model].Metrics[metric][stat] = s.Value.Value
			}
		}
		for _, row := range rows {
			for key, stats := range row.Metrics {
				row.LowerBounds[key] = lowerBounds(stats)
			}
			result.Models = append(result.Models, *row)
		}
		sort.Slice(result.Models, func(a, b int) bool { return result.Models[a].Model < result.Models[b].Model })
	}
	return result, nil
}

func lowerBounds(values map[string]*float64) map[string]bool {
	flags := map[string]bool{}
	if fraction := values["finite_fraction"]; fraction != nil {
		for _, q := range []int{50, 90, 95, 99} {
			if float64(q)/100 > *fraction {
				flags[fmt.Sprintf("p%d", q)] = true
			}
		}
	}
	return flags
}
