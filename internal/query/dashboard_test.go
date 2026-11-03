package query

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestQuantilesAggregateBucketsBeforeComputingAndUseIntervalCounts(t *testing.T) {
	f := Filter{Scope: "model", Model: "quoted\"model"}
	d := definitions(f)[0]
	expressions := buildExpressions(d, f, 60, "", 15)
	joined := ""
	for _, e := range expressions {
		joined += e.query + "\n"
	}
	if !strings.Contains(joined, "histogram_quantile(0.99,sum by (le)(increase(") || strings.Contains(joined, "avg(histogram_quantile") {
		t.Fatalf("invalid quantile aggregation: %s", joined)
	}
	if !strings.Contains(joined, `model="quoted\"model"`) {
		t.Fatal("label was not escaped")
	}
	if !strings.Contains(joined, "_count{") {
		t.Fatal("missing observation counts")
	}
}

func TestClusterBacklogUsesOneFreshObserverInsteadOfSummingWorkers(t *testing.T) {
	f := Filter{Scope: "billing"}
	var d definition
	for _, candidate := range definitions(f) {
		if candidate.key == "billing_backlog" {
			d = candidate
		}
	}
	expression := buildExpressions(d, f, 60, "", 15)[0].query
	for _, required := range []string{"topk(1,", `kind="pipeline"`, "time()-120", "up{", ">=0", "and on(gateway_instance)"} {
		if !strings.Contains(expression, required) {
			t.Fatalf("missing %s in %s", required, expression)
		}
	}
	for _, candidate := range definitions(f) {
		if candidate.key == "billing_settled_orders" && (candidate.metric != "ai_hub_billing_batch_size_sum" || !strings.Contains(strings.Join(candidate.extra, ","), "wallet_settlement")) {
			t.Fatal("incorrect settled order counter")
		}
	}
}

func TestFiltersRejectExcessiveRangesAndDoNotPretendDimensionsExist(t *testing.T) {
	now := time.Unix(1800000000, 0)
	for _, values := range []url.Values{{"start_timestamp": {"1"}}, {"grain_seconds": {"1"}}, {"stream": {"sometimes"}}, {"scope": {"billing"}, "model_name": {"test"}}, {"channel_id": {"1 or 1"}}} {
		if _, err := ParseFilter(values, now); err == nil {
			t.Fatalf("accepted invalid filters: %v", values)
		}
	}
	f, err := ParseFilter(url.Values{"model_name": {"gpt"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if f.Compatible(DimInstance) {
		t.Fatal("HTTP metrics pretended to support a model filter")
	}
	if !f.Compatible(DimInstance | DimModel) {
		t.Fatal("model concurrency was rejected")
	}
}

func TestNonFiniteSamplesRemainNullAndOverflowIsExplicit(t *testing.T) {
	for _, raw := range []string{`[123,"NaN"]`, `[123,"+Inf"]`, `[123,"-Inf"]`} {
		var sample Sample
		if err := json.Unmarshal([]byte(raw), &sample); err != nil || sample.Value != nil {
			t.Fatalf("non-finite sample was not null: %s", raw)
		}
	}
	fraction := 0.96
	flags := lowerBounds(map[string]*float64{"finite_fraction": &fraction})
	if flags["p95"] || !flags["p99"] {
		t.Fatalf("incorrect overflow flags: %v", flags)
	}
}
