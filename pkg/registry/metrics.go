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

package registry

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

func (m *Metrics) IncAssertionsSigned() {
	if m == nil {
		return
	}
	m.assertionsSigned.Add(1)
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
		fmt.Fprintf(&sb, "# HELP grain_registry_requests_total Total HTTP requests handled by grain-registry.\n")
		fmt.Fprintf(&sb, "# TYPE grain_registry_requests_total counter\n")
		fmt.Fprintf(&sb, "grain_registry_requests_total %d\n", snap.RequestsTotal)

		fmt.Fprintf(&sb, "# HELP grain_registry_assertions_signed_total Total ES256 assertions signed by grain-registry.\n")
		fmt.Fprintf(&sb, "# TYPE grain_registry_assertions_signed_total counter\n")
		fmt.Fprintf(&sb, "grain_registry_assertions_signed_total %d\n", snap.AssertionsSigned)

		fmt.Fprintf(&sb, "# HELP grain_registry_admission_rejected_total Total unauthenticated or forbidden requests rejected at admission.\n")
		fmt.Fprintf(&sb, "# TYPE grain_registry_admission_rejected_total counter\n")
		fmt.Fprintf(&sb, "grain_registry_admission_rejected_total %d\n", snap.AdmissionRejected)

		fmt.Fprintf(&sb, "# HELP grain_registry_active_tunnels Current active outbound connect tunnel sessions.\n")
		fmt.Fprintf(&sb, "# TYPE grain_registry_active_tunnels gauge\n")
		fmt.Fprintf(&sb, "grain_registry_active_tunnels %d\n", snap.ActiveTunnels)

		fmt.Fprintf(&sb, "# HELP grain_registry_heartbeats_total Total instance and connected entry heartbeats processed.\n")
		fmt.Fprintf(&sb, "# TYPE grain_registry_heartbeats_total counter\n")
		fmt.Fprintf(&sb, "grain_registry_heartbeats_total %d\n", snap.HeartbeatsTotal)

		fmt.Fprintf(&sb, "# HELP grain_registry_peer_streams_rejected_total Total grain-initiated mux stream frames rejected on outbound tunnels.\n")
		fmt.Fprintf(&sb, "# TYPE grain_registry_peer_streams_rejected_total counter\n")
		fmt.Fprintf(&sb, "grain_registry_peer_streams_rejected_total %d\n", snap.PeerStreamsRejected)

		fmt.Fprintf(&sb, "# HELP grain_registry_peer_forwards_total Total replica-to-replica tunnel requests forwarded.\n")
		fmt.Fprintf(&sb, "# TYPE grain_registry_peer_forwards_total counter\n")
		fmt.Fprintf(&sb, "grain_registry_peer_forwards_total %d\n", snap.PeerForwardsTotal)

		codes := make([]int, 0, len(snap.ResponsesByStatus))
		for code := range snap.ResponsesByStatus {
			codes = append(codes, code)
		}
		sort.Ints(codes)
		for _, code := range codes {
			fmt.Fprintf(&sb, "grain_registry_http_responses_total{code=\"%d\"} %d\n", code, snap.ResponsesByStatus[code])
		}
		_, _ = w.Write([]byte(sb.String()))
	})
}
