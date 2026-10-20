package metrics

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// TelemetryCounters manages all SLP monitoring counters and latencies
type TelemetryCounters struct {
	TotalRequests   atomic.Int64
	UpRequests      atomic.Int64
	UpSuccess       atomic.Int64
	UpFailed        atomic.Int64
	CpRequests      atomic.Int64
	CpSuccess       atomic.Int64
	CpFailed        atomic.Int64
	FallbackUpToCp  atomic.Int64
	FallbackCpToUp  atomic.Int64
	SmsSentSuccess  atomic.Int64
	SmsSentFailure  atomic.Int64
	PosInitReceived atomic.Int64
	VerMatch        atomic.Int64
	VerMismatch     atomic.Int64

	// Latency trackers in microseconds
	TotalLatencyUs atomic.Int64
	RequestCount   atomic.Int64
	MaxLatencyUs   atomic.Int64
}

// Global instance
var DefaultCounters = &TelemetryCounters{}

// RecordRequest tracks a completed request and its duration
func (c *TelemetryCounters) RecordDuration(d time.Duration) {
	us := d.Microseconds()
	c.TotalLatencyUs.Add(us)
	c.RequestCount.Add(1)

	for {
		curMax := c.MaxLatencyUs.Load()
		if us <= curMax {
			break
		}
		if c.MaxLatencyUs.CompareAndSwap(curMax, us) {
			break
		}
	}
}

// PrometheusFormat formats all metrics into Prometheus exposition text format
func (c *TelemetryCounters) PrometheusFormat() string {
	var sb strings.Builder

	writeMetric := func(name, help string, val int64) {
		sb.WriteString(fmt.Sprintf("# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, val))
	}

	writeMetric("slp_requests_total", "Total incoming provide location requests", c.TotalRequests.Load())
	writeMetric("slp_up_requests_total", "Total User Plane requests initiated", c.UpRequests.Load())
	writeMetric("slp_up_success_total", "Total User Plane requests succeeded", c.UpSuccess.Load())
	writeMetric("slp_up_failed_total", "Total User Plane requests failed", c.UpFailed.Load())
	writeMetric("slp_cp_requests_total", "Total Control Plane requests initiated", c.CpRequests.Load())
	writeMetric("slp_cp_success_total", "Total Control Plane requests succeeded", c.CpSuccess.Load())
	writeMetric("slp_cp_failed_total", "Total Control Plane requests failed", c.CpFailed.Load())
	writeMetric("slp_fallback_up_to_cp_total", "Total fallbacks from User Plane to Control Plane", c.FallbackUpToCp.Load())
	writeMetric("slp_fallback_cp_to_up_total", "Total fallbacks from Control Plane to User Plane", c.FallbackCpToUp.Load())
	writeMetric("slp_sms_sent_success_total", "Total SUPL INIT SMS wakeups delivered to SMSC", c.SmsSentSuccess.Load())
	writeMetric("slp_sms_sent_failure_total", "Total SUPL INIT SMS deliveries failed", c.SmsSentFailure.Load())
	writeMetric("slp_pos_init_received_total", "Total SUPL POS INIT messages received from terminals", c.PosInitReceived.Load())
	writeMetric("slp_ver_match_total", "Total successful VER HMAC-SHA256 validations", c.VerMatch.Load())
	writeMetric("slp_ver_mismatch_total", "Total failed VER HMAC-SHA256 validations", c.VerMismatch.Load())

	// Latency gauge
	reqCount := c.RequestCount.Load()
	var avgUs int64
	if reqCount > 0 {
		avgUs = c.TotalLatencyUs.Load() / reqCount
	}
	sb.WriteString(fmt.Sprintf("# HELP slp_avg_latency_ms Average request latency in milliseconds\n# TYPE slp_avg_latency_ms gauge\nslp_avg_latency_ms %.2f\n", float64(avgUs)/1000.0))
	sb.WriteString(fmt.Sprintf("# HELP slp_max_latency_ms Max request latency in milliseconds\n# TYPE slp_max_latency_ms gauge\nslp_max_latency_ms %.2f\n", float64(c.MaxLatencyUs.Load())/1000.0))

	return sb.String()
}

// Snapshot returns a struct snapshot for JSON serialization
func (c *TelemetryCounters) Snapshot() map[string]any {
	reqCount := c.RequestCount.Load()
	var avgMs float64
	if reqCount > 0 {
		avgMs = float64(c.TotalLatencyUs.Load()/reqCount) / 1000.0
	}

	return map[string]any{
		"total_requests":     c.TotalRequests.Load(),
		"up_requests":        c.UpRequests.Load(),
		"up_success":         c.UpSuccess.Load(),
		"up_failed":          c.UpFailed.Load(),
		"cp_requests":        c.CpRequests.Load(),
		"cp_success":         c.CpSuccess.Load(),
		"cp_failed":          c.CpFailed.Load(),
		"fallback_up_to_cp":  c.FallbackUpToCp.Load(),
		"fallback_cp_to_up":  c.FallbackCpToUp.Load(),
		"sms_sent_success":   c.SmsSentSuccess.Load(),
		"sms_sent_failure":   c.SmsSentFailure.Load(),
		"pos_init_received":  c.PosInitReceived.Load(),
		"ver_match":          c.VerMatch.Load(),
		"ver_mismatch":       c.VerMismatch.Load(),
		"avg_latency_ms":     avgMs,
		"max_latency_ms":     float64(c.MaxLatencyUs.Load()) / 1000.0,
	}
}

// ToJSON returns JSON encoded metrics
func (c *TelemetryCounters) ToJSON() ([]byte, error) {
	return json.MarshalIndent(c.Snapshot(), "", "  ")
}
