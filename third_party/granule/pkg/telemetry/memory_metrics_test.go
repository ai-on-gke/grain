package telemetry

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMemoryHotplugMetrics_RecordSuccess(t *testing.T) {
	m := NewMemoryHotplugMetrics()
	const (
		current = 1024 * 1024 * 1024
		target  = 2048 * 1024 * 1024
		ceiling = 4096 * 1024 * 1024
	)
	latency := 85 * time.Millisecond

	m.RecordSuccess(current, target, ceiling, latency, "42.50")

	if got := m.EventsTotal(StatusSuccess); got != 1 {
		t.Errorf("EventsTotal(StatusSuccess) = %d, want 1", got)
	}
	if got := m.EventsTotal(StatusSkipped); got != 0 {
		t.Errorf("EventsTotal(StatusSkipped) = %d, want 0", got)
	}
	if got := m.EventsTotal(StatusFailed); got != 0 {
		t.Errorf("EventsTotal(StatusFailed) = %d, want 0", got)
	}
	if got := m.AllocatedBytes(); got != target {
		t.Errorf("AllocatedBytes() = %d, want %d", got, target)
	}
	if got := m.CeilingBytes(); got != ceiling {
		t.Errorf("CeilingBytes() = %d, want %d", got, ceiling)
	}
	if got := m.CeilingSaturationRatio(); got != 0.5 {
		t.Errorf("CeilingSaturationRatio() = %f, want 0.5", got)
	}

	count, sumSec, avgSec, minSec, maxSec, p50Sec, p90Sec, p99Sec := m.LatencyStats()
	if count != 1 {
		t.Errorf("latency count = %d, want 1", count)
	}
	if sumSec < 0.084 || sumSec > 0.086 {
		t.Errorf("latency sum = %f, want ~0.085", sumSec)
	}
	if avgSec != sumSec || minSec != sumSec || maxSec != sumSec || p50Sec != sumSec || p90Sec != sumSec || p99Sec != sumSec {
		t.Errorf("latency stats mismatch for single sample: avg=%f, min=%f, max=%f", avgSec, minSec, maxSec)
	}

	events := m.Events()
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].Status != StatusSuccess || events[0].PressureReading != "42.50" {
		t.Errorf("unexpected event: %+v", events[0])
	}
}

func TestMemoryHotplugMetrics_RecordSkippedAndFailed(t *testing.T) {
	m := NewMemoryHotplugMetrics()
	const (
		current = 2048 * 1024 * 1024
		ceiling = 2048 * 1024 * 1024
	)

	m.RecordSkipped("already_at_ceiling", "10.0", current, ceiling)
	if got := m.EventsTotal(StatusSkipped); got != 1 {
		t.Errorf("EventsTotal(StatusSkipped) = %d, want 1", got)
	}
	if got := m.CeilingSaturationRatio(); got != 1.0 {
		t.Errorf("CeilingSaturationRatio() = %f, want 1.0", got)
	}

	m.RecordFailed("resize_error", "10.0", current, 12*time.Millisecond)
	if got := m.EventsTotal(StatusFailed); got != 1 {
		t.Errorf("EventsTotal(StatusFailed) = %d, want 1", got)
	}

	events := m.Events()
	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(events))
	}
	if events[0].Status != StatusSkipped || events[0].Reason != "already_at_ceiling" {
		t.Errorf("unexpected event[0]: %+v", events[0])
	}
	if events[1].Status != StatusFailed || events[1].Reason != "resize_error" {
		t.Errorf("unexpected event[1]: %+v", events[1])
	}
}

func TestMemoryHotplugMetrics_Percentiles(t *testing.T) {
	m := NewMemoryHotplugMetrics()
	latencies := []float64{0.010, 0.020, 0.030, 0.040, 0.050, 0.060, 0.070, 0.080, 0.090, 0.100}
	for _, lat := range latencies {
		m.RecordHotplugLatency(lat)
	}

	count, sumSec, avgSec, minSec, maxSec, p50Sec, p90Sec, p99Sec := m.LatencyStats()
	if count != 10 {
		t.Errorf("count = %d, want 10", count)
	}
	if minSec != 0.010 {
		t.Errorf("min = %f, want 0.010", minSec)
	}
	if maxSec != 0.100 {
		t.Errorf("max = %f, want 0.100", maxSec)
	}
	if mathAbs(avgSec-0.055) > 1e-6 {
		t.Errorf("avg = %f, want 0.055", avgSec)
	}
	if mathAbs(p50Sec-0.055) > 1e-6 {
		t.Errorf("p50 = %f, want 0.055", p50Sec)
	}
	if p90Sec < 0.085 || p90Sec > 0.095 {
		t.Errorf("p90 = %f, want ~0.091", p90Sec)
	}
	if p99Sec < 0.095 || p99Sec > 0.100 {
		t.Errorf("p99 = %f, want ~0.099", p99Sec)
	}
	_ = sumSec
}

func mathAbs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func TestMemoryHotplugMetrics_RenderPrometheus(t *testing.T) {
	m := NewMemoryHotplugMetrics()
	m.RecordSuccess(1024*1024*1024, 2048*1024*1024, 4096*1024*1024, 84*time.Millisecond, "20.00")
	m.RecordSkipped("cooldown", "20.00", 2048*1024*1024, 4096*1024*1024)
	m.RecordFailed("api_500", "20.00", 2048*1024*1024, 5*time.Millisecond)

	out := m.RenderPrometheus()

	expectedLines := []string{
		"# HELP granule_memory_hotplug_events_total Total number of memory hotplug events partitioned by status.",
		"# TYPE granule_memory_hotplug_events_total counter",
		"granule_memory_hotplug_events_total{status=\"failed\"} 1",
		"granule_memory_hotplug_events_total{status=\"skipped\"} 1",
		"granule_memory_hotplug_events_total{status=\"success\"} 1",
		"# HELP granule_memory_hotplug_latency_seconds Memory hotplug expansion latency in seconds.",
		"# TYPE granule_memory_hotplug_latency_seconds gauge",
		"granule_memory_hotplug_latency_seconds 0.084000",
		"# HELP granule_memory_allocated_bytes Current allocated memory in bytes.",
		"# TYPE granule_memory_allocated_bytes gauge",
		"granule_memory_allocated_bytes 2147483648",
		"# HELP granule_memory_ceiling_saturation_ratio Ratio of current allocated memory to maximum memory ceiling.",
		"# TYPE granule_memory_ceiling_saturation_ratio gauge",
		"granule_memory_ceiling_saturation_ratio 0.5000",
	}

	for _, line := range expectedLines {
		if !strings.Contains(out, line) {
			t.Errorf("Prometheus output missing expected line %q\nFull output:\n%s", line, out)
		}
	}
}

func TestMemoryHotplugMetrics_HTTPHandler(t *testing.T) {
	m := NewMemoryHotplugMetrics()
	m.RecordSuccess(1024*1024*1024, 1536*1024*1024, 2048*1024*1024, 50*time.Millisecond, "15.00")

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()

	m.HTTPHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP status = %d, want 200", rec.Code)
	}
	contentType := rec.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/plain") {
		t.Errorf("Content-Type = %q, expected text/plain", contentType)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "granule_memory_hotplug_events_total{status=\"success\"} 1") {
		t.Errorf("body missing expected metric: %s", body)
	}
}

func TestMemoryHotplugMetrics_SnapshotAndJSON(t *testing.T) {
	m := NewMemoryHotplugMetrics()
	m.RecordSuccess(1024*1024*1024, 2048*1024*1024, 4096*1024*1024, 75*time.Millisecond, "30.00")

	snap := m.Snapshot()
	if snap.EventsTotal[StatusSuccess] != 1 {
		t.Errorf("snap.EventsTotal[StatusSuccess] = %d, want 1", snap.EventsTotal[StatusSuccess])
	}
	if snap.AllocatedBytes != 2048*1024*1024 {
		t.Errorf("snap.AllocatedBytes = %d, want %d", snap.AllocatedBytes, 2048*1024*1024)
	}

	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var parsed MemoryElasticityTelemetry
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}
	if parsed.EventsTotal[StatusSuccess] != 1 {
		t.Errorf("parsed.EventsTotal[StatusSuccess] = %d, want 1", parsed.EventsTotal[StatusSuccess])
	}
	if parsed.AllocatedBytes != 2048*1024*1024 {
		t.Errorf("parsed.AllocatedBytes = %d, want %d", parsed.AllocatedBytes, 2048*1024*1024)
	}
	if len(parsed.PrometheusExposition) == 0 {
		t.Errorf("expected non-empty PrometheusExposition in JSON")
	}
}
