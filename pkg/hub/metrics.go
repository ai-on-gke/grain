// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Metrics tracks operational counters and gauges for `grain-registry`
// and renders them in Prometheus exposition format (or JSON when requested).
type Metrics struct {
	requestsTotal       atomic.Int64
	assertionsSigned    atomic.Int64
	admissionRejected   atomic.Int64
	activeTunnels       atomic.Int64
	heartbeatsTotal     atomic.Int64
	peerStreamsRejected atomic.Int64
	peerForwardsTotal   atomic.Int64

	mu       sync.RWMutex
	byStatus map[int]int64
}

// NewMetrics constructs an initialized Metrics registry.
func NewMetrics() *Metrics {
	return &Metrics{
		byStatus: make(map[int]int64),
	}
}

func (m *Metrics) IncRequests(status int) {
	if m == nil {
		return
	}
	m.requestsTotal.Add(1)
	if status > 0 {
		m.mu.Lock()
		m.byStatus[status]++
		m.mu.Unlock()
	}
}

func (m *Metrics) IncAdmissionRejected() {
	if m == nil {
		return
	}
	m.admissionRejected.Add(1)
}

func (m *Metrics) SetActiveTunnels(n int64) {
	if m == nil {
		return
	}
	m.activeTunnels.Store(n)
}

func (m *Metrics) IncHeartbeats() {
	if m == nil {
		return
	}
	m.heartbeatsTotal.Add(1)
}

func (m *Metrics) IncPeerStreamRejected() {
	if m == nil {
		return
	}
	m.peerStreamsRejected.Add(1)
}

func (m *Metrics) IncPeerForwards() {
	if m == nil {
		return
	}
	m.peerForwardsTotal.Add(1)
}

// MetricsSnapshot holds a point-in-time snapshot of registry metrics.
type MetricsSnapshot struct {
	RequestsTotal       int64         `json:"requests_total"`
	AssertionsSigned    int64         `json:"assertions_signed_total"`
	AdmissionRejected   int64         `json:"admission_rejected_total"`
	ActiveTunnels       int64         `json:"active_tunnels"`
	HeartbeatsTotal     int64         `json:"heartbeats_total"`
	PeerStreamsRejected int64         `json:"peer_streams_rejected_total"`
	PeerForwardsTotal   int64         `json:"peer_forwards_total"`
	ResponsesByStatus   map[int]int64 `json:"responses_by_status"`
}

// Snapshot returns a point-in-time MetricsSnapshot.
func (m *Metrics) Snapshot() MetricsSnapshot {
	if m == nil {
		return MetricsSnapshot{}
	}
	m.mu.RLock()
	byStatus := make(map[int]int64, len(m.byStatus))
	for k, v := range m.byStatus {
		byStatus[k] = v
	}
	m.mu.RUnlock()
	return MetricsSnapshot{
		RequestsTotal:       m.requestsTotal.Load(),
		AssertionsSigned:    m.assertionsSigned.Load(),
		AdmissionRejected:   m.admissionRejected.Load(),
		ActiveTunnels:       m.activeTunnels.Load(),
		HeartbeatsTotal:     m.heartbeatsTotal.Load(),
		PeerStreamsRejected: m.peerStreamsRejected.Load(),
		PeerForwardsTotal:   m.peerForwardsTotal.Load(),
		ResponsesByStatus:   byStatus,
	}
}

// Handler returns an http.Handler serving `/metrics` in Prometheus text format
// by default, or JSON if `Accept: application/json` or `?format=json` is requested.
func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snap := m.Snapshot()
		if r.URL.Query().Get("format") == "json" || strings.Contains(r.Header.Get("Accept"), "application/json") {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_ = json.NewEncoder(w).Encode(snap)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		var sb strings.Builder
		// Every series is written as grain_hub_* and, for a release, under
		// its pre-rename grain_registry_* name too, so dashboards and alerts
		// written before the Grain Registry became the Grain Hub keep
		// reading until they are moved over.
		for _, prefix := range []string{"grain_hub", "grain_registry"} {
			metric := func(name, kind, help string, v int64) {
				fmt.Fprintf(&sb, "# HELP %s_%s %s\n", prefix, name, help)
				fmt.Fprintf(&sb, "# TYPE %s_%s %s\n", prefix, name, kind)
				fmt.Fprintf(&sb, "%s_%s %d\n", prefix, name, v)
			}
			metric("requests_total", "counter", "Total HTTP requests handled by grain-hub.", snap.RequestsTotal)
			metric("assertions_signed_total", "counter", "Total ES256 assertions signed by grain-hub.", snap.AssertionsSigned)
			metric("admission_rejected_total", "counter", "Total unauthenticated or forbidden requests rejected at admission.", snap.AdmissionRejected)
			metric("active_tunnels", "gauge", "Current active outbound connect tunnel sessions.", snap.ActiveTunnels)
			metric("heartbeats_total", "counter", "Total instance and connected entry heartbeats processed.", snap.HeartbeatsTotal)
			metric("peer_streams_rejected_total", "counter", "Total grain-initiated mux stream frames rejected on outbound tunnels.", snap.PeerStreamsRejected)
			metric("peer_forwards_total", "counter", "Total replica-to-replica tunnel requests forwarded.", snap.PeerForwardsTotal)
		}

		codes := make([]int, 0, len(snap.ResponsesByStatus))
		for code := range snap.ResponsesByStatus {
			codes = append(codes, code)
		}
		sort.Ints(codes)
		for _, prefix := range []string{"grain_hub", "grain_registry"} {
			for _, code := range codes {
				fmt.Fprintf(&sb, "%s_http_responses_total{code=\"%d\"} %d\n", prefix, code, snap.ResponsesByStatus[code])
			}
		}
		_, _ = w.Write([]byte(sb.String()))
	})
}
