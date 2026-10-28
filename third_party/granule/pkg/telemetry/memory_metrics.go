package telemetry

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Metric status constants for granule_memory_hotplug_events_total.
const (
	StatusSuccess = "success"
	StatusSkipped = "skipped"
	StatusFailed  = "failed"
)

// Metric names for Prometheus exposition.
const (
	MetricMemoryHotplugEventsTotal     = "granule_memory_hotplug_events_total"
	MetricMemoryHotplugLatencySeconds  = "granule_memory_hotplug_latency_seconds"
	MetricMemoryAllocatedBytes         = "granule_memory_allocated_bytes"
	MetricMemoryCeilingSaturationRatio = "granule_memory_ceiling_saturation_ratio"
)

// HotplugEvent records metadata for a single memory hotplug event.
type HotplugEvent struct {
	Timestamp       time.Time     `json:"timestamp"`
	Status          string        `json:"status"` // success, skipped, failed
	Reason          string        `json:"reason,omitempty"`
	PressureReading string        `json:"pressure_reading,omitempty"`
	CurrentBytes    uint64        `json:"current_bytes"`
	TargetBytes     uint64        `json:"target_bytes,omitempty"`
	CeilingBytes    uint64        `json:"ceiling_bytes,omitempty"`
	Latency         time.Duration `json:"latency_ns,omitempty"`
	LatencySeconds  float64       `json:"latency_seconds,omitempty"`
}

// MemoryElasticityTelemetry holds structured memory hotplug telemetry metrics for JSON export.
type MemoryElasticityTelemetry struct {
	EventsTotal            map[string]uint64 `json:"events_total"`
	HotplugLatencySeconds  float64           `json:"hotplug_latency_seconds"`
	LatencyCount           uint64            `json:"latency_count"`
	LatencySumSeconds      float64           `json:"latency_sum_seconds"`
	LatencyAvgSeconds      float64           `json:"latency_avg_seconds"`
	LatencyMinSeconds      float64           `json:"latency_min_seconds"`
	LatencyMaxSeconds      float64           `json:"latency_max_seconds"`
	LatencyP50Seconds      float64           `json:"latency_p50_seconds"`
	LatencyP90Seconds      float64           `json:"latency_p90_seconds"`
	LatencyP99Seconds      float64           `json:"latency_p99_seconds"`
	AllocatedBytes         uint64            `json:"allocated_bytes"`
	CeilingBytes           uint64            `json:"ceiling_bytes"`
	CeilingSaturationRatio float64           `json:"ceiling_saturation_ratio"`
	PrometheusExposition   string            `json:"prometheus_exposition,omitempty"`
	Events                 []HotplugEvent    `json:"events,omitempty"`
}

// MemoryHotplugMetrics tracks structured memory hotplug telemetry and Prometheus metrics.
type MemoryHotplugMetrics struct {
	mu sync.RWMutex

	// Events counts per status: "success", "skipped", "failed"
	eventsTotal map[string]uint64

	// Latency tracking in seconds
	latencySamples []float64
	latencyCount   uint64
	latencySumSec  float64
	lastLatencySec float64

	// Current memory state
	allocatedBytes         uint64
	ceilingBytes           uint64
	ceilingSaturationRatio float64

	// Historical events
	events []HotplugEvent
}

// NewMemoryHotplugMetrics initializes a new MemoryHotplugMetrics collector.
func NewMemoryHotplugMetrics() *MemoryHotplugMetrics {
	return &MemoryHotplugMetrics{
		eventsTotal: map[string]uint64{
			StatusSuccess: 0,
			StatusSkipped: 0,
			StatusFailed:  0,
		},
		latencySamples: make([]float64, 0),
		events:         make([]HotplugEvent, 0),
	}
}

// RecordSuccess records a successful memory hotplug expansion.
func (m *MemoryHotplugMetrics) RecordSuccess(currentBytes, targetBytes, ceilingBytes uint64, latency time.Duration, reading string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.eventsTotal[StatusSuccess]++
	latSec := latency.Seconds()
	m.latencySamples = append(m.latencySamples, latSec)
	m.latencyCount++
	m.latencySumSec += latSec
	m.lastLatencySec = latSec

	m.allocatedBytes = targetBytes
	m.ceilingBytes = ceilingBytes
	if ceilingBytes > 0 {
		m.ceilingSaturationRatio = math.Min(1.0, float64(targetBytes)/float64(ceilingBytes))
	}

	m.events = append(m.events, HotplugEvent{
		Timestamp:       time.Now().UTC(),
		Status:          StatusSuccess,
		PressureReading: reading,
		CurrentBytes:    currentBytes,
		TargetBytes:     targetBytes,
		CeilingBytes:    ceilingBytes,
		Latency:         latency,
		LatencySeconds:  latSec,
	})
}

// RecordSkipped records a skipped memory hotplug attempt.
func (m *MemoryHotplugMetrics) RecordSkipped(reason, reading string, currentBytes, ceilingBytes uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.eventsTotal[StatusSkipped]++
	if currentBytes > 0 {
		m.allocatedBytes = currentBytes
	}
	if ceilingBytes > 0 {
		m.ceilingBytes = ceilingBytes
	}
	if m.ceilingBytes > 0 && m.allocatedBytes > 0 {
		m.ceilingSaturationRatio = math.Min(1.0, float64(m.allocatedBytes)/float64(m.ceilingBytes))
	}

	m.events = append(m.events, HotplugEvent{
		Timestamp:       time.Now().UTC(),
		Status:          StatusSkipped,
		Reason:          reason,
		PressureReading: reading,
		CurrentBytes:    currentBytes,
		CeilingBytes:    ceilingBytes,
	})
}

// RecordFailed records a failed memory hotplug attempt.
func (m *MemoryHotplugMetrics) RecordFailed(reason, reading string, currentBytes uint64, latency time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.eventsTotal[StatusFailed]++
	if currentBytes > 0 {
		m.allocatedBytes = currentBytes
	}

	var latSec float64
	if latency > 0 {
		latSec = latency.Seconds()
	}

	m.events = append(m.events, HotplugEvent{
		Timestamp:       time.Now().UTC(),
		Status:          StatusFailed,
		Reason:          reason,
		PressureReading: reading,
		CurrentBytes:    currentBytes,
		CeilingBytes:    m.ceilingBytes,
		Latency:         latency,
		LatencySeconds:  latSec,
	})
}

// RecordHotplugEvent increments the total count for the specified status.
func (m *MemoryHotplugMetrics) RecordHotplugEvent(status string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.eventsTotal[status]++
}

// RecordHotplugLatency records an observed hotplug latency in seconds.
func (m *MemoryHotplugMetrics) RecordHotplugLatency(seconds float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.latencySamples = append(m.latencySamples, seconds)
	m.latencyCount++
	m.latencySumSec += seconds
	m.lastLatencySec = seconds
}

// SetAllocatedBytes updates the current allocated memory in bytes.
func (m *MemoryHotplugMetrics) SetAllocatedBytes(bytes uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.allocatedBytes = bytes
	if m.ceilingBytes > 0 {
		m.ceilingSaturationRatio = math.Min(1.0, float64(bytes)/float64(m.ceilingBytes))
	}
}

// SetCeilingBytes updates the maximum memory ceiling in bytes.
func (m *MemoryHotplugMetrics) SetCeilingBytes(bytes uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.ceilingBytes = bytes
	if bytes > 0 && m.allocatedBytes > 0 {
		m.ceilingSaturationRatio = math.Min(1.0, float64(m.allocatedBytes)/float64(bytes))
	}
}

// SetCeilingSaturationRatio directly sets the saturation ratio.
func (m *MemoryHotplugMetrics) SetCeilingSaturationRatio(ratio float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.ceilingSaturationRatio = math.Max(0.0, math.Min(1.0, ratio))
}

// EventsTotal returns the count of hotplug events for a given status.
func (m *MemoryHotplugMetrics) EventsTotal(status string) uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.eventsTotal[status]
}

// AllocatedBytes returns the current allocated memory in bytes.
func (m *MemoryHotplugMetrics) AllocatedBytes() uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.allocatedBytes
}

// CeilingBytes returns the ceiling memory in bytes.
func (m *MemoryHotplugMetrics) CeilingBytes() uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.ceilingBytes
}

// CeilingSaturationRatio returns the current ceiling saturation ratio.
func (m *MemoryHotplugMetrics) CeilingSaturationRatio() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.ceilingSaturationRatio
}

// LatencyStats computes summary statistics for hotplug latency in seconds.
func (m *MemoryHotplugMetrics) LatencyStats() (count uint64, sumSec, avgSec, minSec, maxSec, p50Sec, p90Sec, p99Sec float64) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	count = m.latencyCount
	sumSec = m.latencySumSec
	if count == 0 {
		return
	}

	avgSec = sumSec / float64(count)
	sorted := make([]float64, len(m.latencySamples))
	copy(sorted, m.latencySamples)
	sort.Float64s(sorted)

	minSec = sorted[0]
	maxSec = sorted[len(sorted)-1]
	p50Sec = percentile(sorted, 0.50)
	p90Sec = percentile(sorted, 0.90)
	p99Sec = percentile(sorted, 0.99)
	return
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	index := p * float64(len(sorted)-1)
	lower := int(math.Floor(index))
	upper := int(math.Ceil(index))
	if lower == upper {
		return sorted[lower]
	}
	weight := index - float64(lower)
	return sorted[lower]*(1-weight) + sorted[upper]*weight
}

// Events returns a copy of all recorded hotplug events.
func (m *MemoryHotplugMetrics) Events() []HotplugEvent {
	m.mu.RLock()
	defer m.mu.RUnlock()

	res := make([]HotplugEvent, len(m.events))
	copy(res, m.events)
	return res
}

// Snapshot returns a JSON-serializable snapshot of memory hotplug metrics.
func (m *MemoryHotplugMetrics) Snapshot() MemoryElasticityTelemetry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	eventsCopy := make(map[string]uint64, len(m.eventsTotal))
	for k, v := range m.eventsTotal {
		eventsCopy[k] = v
	}

	count := m.latencyCount
	sumSec := m.latencySumSec
	var avgSec, minSec, maxSec, p50Sec, p90Sec, p99Sec float64
	if count > 0 {
		avgSec = sumSec / float64(count)
		sorted := make([]float64, len(m.latencySamples))
		copy(sorted, m.latencySamples)
		sort.Float64s(sorted)

		minSec = sorted[0]
		maxSec = sorted[len(sorted)-1]
		p50Sec = percentile(sorted, 0.50)
		p90Sec = percentile(sorted, 0.90)
		p99Sec = percentile(sorted, 0.99)
	}

	histEvents := make([]HotplugEvent, len(m.events))
	copy(histEvents, m.events)

	snap := MemoryElasticityTelemetry{
		EventsTotal:            eventsCopy,
		HotplugLatencySeconds:  m.lastLatencySec,
		LatencyCount:           count,
		LatencySumSeconds:      sumSec,
		LatencyAvgSeconds:      avgSec,
		LatencyMinSeconds:      minSec,
		LatencyMaxSeconds:      maxSec,
		LatencyP50Seconds:      p50Sec,
		LatencyP90Seconds:      p90Sec,
		LatencyP99Seconds:      p99Sec,
		AllocatedBytes:         m.allocatedBytes,
		CeilingBytes:           m.ceilingBytes,
		CeilingSaturationRatio: m.ceilingSaturationRatio,
		Events:                 histEvents,
	}
	snap.PrometheusExposition = m.renderPrometheusLocked()
	return snap
}

// RenderPrometheus renders the metrics in standard Prometheus text exposition format.
func (m *MemoryHotplugMetrics) RenderPrometheus() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.renderPrometheusLocked()
}

func (m *MemoryHotplugMetrics) renderPrometheusLocked() string {
	var b strings.Builder

	// 1. granule_memory_hotplug_events_total{status="..."}
	b.WriteString("# HELP granule_memory_hotplug_events_total Total number of memory hotplug events partitioned by status.\n")
	b.WriteString("# TYPE granule_memory_hotplug_events_total counter\n")
	statuses := []string{StatusFailed, StatusSkipped, StatusSuccess}
	for _, status := range statuses {
		count := m.eventsTotal[status]
		fmt.Fprintf(&b, "%s{status=\"%s\"} %d\n", MetricMemoryHotplugEventsTotal, status, count)
	}

	// 2. granule_memory_hotplug_latency_seconds
	b.WriteString("# HELP granule_memory_hotplug_latency_seconds Memory hotplug expansion latency in seconds.\n")
	b.WriteString("# TYPE granule_memory_hotplug_latency_seconds gauge\n")
	fmt.Fprintf(&b, "%s %.6f\n", MetricMemoryHotplugLatencySeconds, m.lastLatencySec)

	// 3. granule_memory_allocated_bytes
	b.WriteString("# HELP granule_memory_allocated_bytes Current allocated memory in bytes.\n")
	b.WriteString("# TYPE granule_memory_allocated_bytes gauge\n")
	fmt.Fprintf(&b, "%s %d\n", MetricMemoryAllocatedBytes, m.allocatedBytes)

	// 4. granule_memory_ceiling_saturation_ratio
	b.WriteString("# HELP granule_memory_ceiling_saturation_ratio Ratio of current allocated memory to maximum memory ceiling.\n")
	b.WriteString("# TYPE granule_memory_ceiling_saturation_ratio gauge\n")
	fmt.Fprintf(&b, "%s %.4f\n", MetricMemoryCeilingSaturationRatio, m.ceilingSaturationRatio)

	return b.String()
}

// WritePrometheus writes Prometheus exposition format to an io.Writer.
func (m *MemoryHotplugMetrics) WritePrometheus(w io.Writer) error {
	_, err := io.WriteString(w, m.RenderPrometheus())
	return err
}

// ServeHTTP implements http.Handler to export Prometheus metrics on standard HTTP endpoints.
func (m *MemoryHotplugMetrics) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_ = m.WritePrometheus(w)
}

// HTTPHandler returns an http.Handler exporting Prometheus metrics.
func (m *MemoryHotplugMetrics) HTTPHandler() http.Handler {
	return m
}

// MarshalJSON returns JSON representation of the metrics snapshot.
func (m *MemoryHotplugMetrics) MarshalJSON() ([]byte, error) {
	return json.Marshal(m.Snapshot())
}
