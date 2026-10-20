package metrics_test

import (
	"strings"
	"testing"
	"time"

	"slp/pkg/metrics"
)

func TestMetricsCounters(t *testing.T) {
	c := &metrics.TelemetryCounters{}

	c.TotalRequests.Add(10)
	c.UpRequests.Add(8)
	c.UpSuccess.Add(7)
	c.UpFailed.Add(1)
	c.FallbackUpToCp.Add(1)
	c.CpRequests.Add(3)
	c.CpSuccess.Add(3)

	c.RecordDuration(10 * time.Millisecond)
	c.RecordDuration(20 * time.Millisecond)

	prom := c.PrometheusFormat()
	if !strings.Contains(prom, "slp_requests_total 10") {
		t.Fatalf("expected slp_requests_total 10 in prom output:\n%s", prom)
	}
	if !strings.Contains(prom, "slp_fallback_up_to_cp_total 1") {
		t.Fatalf("expected slp_fallback_up_to_cp_total 1 in prom output:\n%s", prom)
	}

	snap := c.Snapshot()
	if snap["total_requests"].(int64) != 10 {
		t.Fatalf("snapshot mismatch: %v", snap)
	}
	if snap["avg_latency_ms"].(float64) != 15.0 {
		t.Fatalf("expected avg latency 15ms, got %v", snap["avg_latency_ms"])
	}

	jsonBytes, err := c.ToJSON()
	if err != nil || len(jsonBytes) == 0 {
		t.Fatalf("ToJSON error: %v", err)
	}
}
