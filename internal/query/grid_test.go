package query

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestRangeQueriesAndGapFillingUseTheSameUTCGrid(t *testing.T) {
	f := Filter{Start: 1800000007, End: 1800000097, Step: 15, Scope: "gateway"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/query_range" {
			if got := r.URL.Query().Get("start"); got != "1800000015" {
				t.Errorf("unaligned query start: %s", got)
			}
			rows := []any{}
			if strings.Contains(r.URL.Query().Get("query"), `"ttft__p95"`) {
				rows = append(rows, map[string]any{
					"metric": map[string]string{"monitor_key": "ttft__p95"},
					"values": [][]any{{f.GridStart(), "120"}, {f.GridStart() + 30, "150"}},
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": rows}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": []any{}}})
	}))
	defer server.Close()
	result, err := NewClient(server.URL).Dashboard(context.Background(), f, 15)
	if err != nil {
		t.Fatal(err)
	}
	for _, metric := range result.Metrics {
		if metric.Key != "ttft" {
			continue
		}
		if len(metric.Points) != 6 || metric.Points[0].Values["p95"] == nil || *metric.Points[0].Values["p95"] != 120 || metric.Points[1].Values["p95"] != nil || metric.Points[2].Values["p95"] == nil {
			t.Fatalf("observations were dropped or missing points filled: %+v", metric.Points)
		}
		return
	}
	t.Fatal("missing TTFT metric")
}

func TestOldBillingObservationsAreReportedAsStale(t *testing.T) {
	f := Filter{Start: 1800000000, End: 1800000900, Step: 15, Scope: "billing"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resultType := "matrix"
		rows := []any{}
		if r.URL.Path == "/api/v1/query" {
			resultType = "vector"
			if strings.Contains(r.URL.Query().Get("query"), `"billing_backlog__observed_at"`) {
				rows = append(rows, map[string]any{"metric": map[string]string{"monitor_key": "billing_backlog__observed_at"}, "value": []any{f.End, strconv.FormatInt(f.End-180, 10)}})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": resultType, "result": rows}})
	}))
	defer server.Close()
	result, err := NewClient(server.URL).Dashboard(context.Background(), f, 15)
	if err != nil {
		t.Fatal(err)
	}
	for _, metric := range result.Metrics {
		if metric.Key == "billing_backlog" {
			if !metric.Stale || metric.Summary["value"] != nil {
				t.Fatalf("stale observation was not explicit: %+v", metric)
			}
			return
		}
	}
	t.Fatal("missing billing backlog")
}
