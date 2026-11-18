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

// Package gates implements pure, local built-in evaluators. No type in this
// package has a lifecycle writer, action client or dispatch capability.
package gates

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"
)

type Subject struct {
	Kind       string `json:"kind"`
	TaskID     string `json:"task_id,omitempty"`
	Authority  string `json:"authority,omitempty"`
	Repository string `json:"repository,omitempty"`
	ChangeKind string `json:"change_kind,omitempty"`
	ID         string `json:"id,omitempty"`
}
type Origin struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Revision string `json:"revision"`
	Imported bool   `json:"imported"`
}
type DeliveryConfig struct {
	Product   string `json:"product"`
	Semantics string `json:"semantics"`
}
type AcknowledgmentConfig struct {
	Mode string `json:"mode"`
}
type Binding struct {
	ID               string          `json:"id"`
	Type             string          `json:"type"`
	Version          uint32          `json:"version"`
	Purpose          string          `json:"purpose"`
	Required         bool            `json:"required"`
	Action           *string         `json:"action"`
	Subject          Subject         `json:"subject"`
	Config           json.RawMessage `json:"config"`
	FreshnessSeconds uint32          `json:"freshness_seconds"`
	Origin           Origin          `json:"origin"`
}
type Evidence struct {
	Kind    string  `json:"kind"`
	ID      string  `json:"id"`
	Version string  `json:"version"`
	Subject Subject `json:"subject"`
	Head    *string `json:"head"`
	Digest  string  `json:"digest"`
}
type Dependency struct {
	Key                   string    `json:"key"`
	Subject               Subject   `json:"subject"`
	Head                  *string   `json:"head"`
	Version               string    `json:"version"`
	CollectionGeneration  string    `json:"collection_generation"`
	ScopeDigest           string    `json:"scope_digest"`
	Complete              bool      `json:"complete"`
	ObservedAt            time.Time `json:"observed_at"`
	ValidUntil            time.Time `json:"valid_until"`
	AuthorizationRevision uint64    `json:"authorization_revision"`
	DirtyGeneration       uint64    `json:"dirty_generation"`
	CoveredGeneration     uint64    `json:"covered_generation"`
}
type Wait struct {
	Event     string    `json:"event"`
	Key       string    `json:"key"`
	Version   string    `json:"version"`
	Deadline  time.Time `json:"deadline"`
	Owner     string    `json:"owner"`
	OnTimeout string    `json:"on_timeout"`
}
type EvaluationError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}
type Result struct {
	EvaluationID  string           `json:"evaluation_id"`
	BindingID     string           `json:"binding_id"`
	Type          string           `json:"type"`
	Version       uint32           `json:"version"`
	ContextDigest string           `json:"context_digest"`
	Status        string           `json:"status"`
	Summary       string           `json:"summary"`
	HumanText     *string          `json:"human_text"`
	AgentText     *string          `json:"agent_text"`
	Evidence      []Evidence       `json:"evidence"`
	Dependencies  []Dependency     `json:"dependencies"`
	Blockers      []struct{}       `json:"blockers"`
	Work          []struct{}       `json:"work"`
	Waits         []Wait           `json:"waits"`
	Actions       []struct{}       `json:"actions"`
	ObservedAt    time.Time        `json:"observed_at"`
	ValidUntil    time.Time        `json:"valid_until"`
	Error         *EvaluationError `json:"error"`
}
type EvaluationState struct {
	EvaluationID       string     `json:"evaluation_id"`
	Revision           uint64     `json:"revision"`
	Disposition        string     `json:"disposition"`
	Result             Result     `json:"result"`
	AcceptedAt         *time.Time `json:"accepted_at"`
	InvalidatedAt      *time.Time `json:"invalidated_at"`
	InvalidationReason *string    `json:"invalidation_reason"`
	RetryAt            *time.Time `json:"retry_at"`
	DirtyGeneration    uint64     `json:"dirty_generation"`
	CoveredGeneration  uint64     `json:"covered_generation"`
}
type ObservationView struct {
	APIVersion       string            `json:"api_version"`
	Mode             string            `json:"mode"`
	Authority        string            `json:"authority"`
	LifecycleEffects bool              `json:"lifecycle_effects"`
	ActionEffects    bool              `json:"action_effects"`
	DispatchEffects  bool              `json:"dispatch_effects"`
	AutomaticCutover bool              `json:"automatic_cutover"`
	SourceRevision   string            `json:"source_revision"`
	ComparedAt       time.Time         `json:"compared_at"`
	LegacyState      string            `json:"legacy_state"`
	Bindings         []Binding         `json:"bindings"`
	Results          []EvaluationState `json:"results"`
	Dirty            bool              `json:"dirty"`
}

// Digest canonicalizes the JSON-only, ASCII-keyed subset used by this slice.
// Re-encoding through maps sorts keys, independently of Go field order.
func Digest(v any) string {
	b, _ := json.Marshal(v)
	var obj any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	_ = dec.Decode(&obj)
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(obj)
	encoded := bytes.TrimSuffix(out.Bytes(), []byte("\n"))
	canonical := make([]byte, 0, len(encoded))
	for i := 0; i < len(encoded); i++ {
		if encoded[i] == '\\' && i+1 < len(encoded) {
			if i+5 < len(encoded) && (string(encoded[i+1:i+6]) == "u2028" || string(encoded[i+1:i+6]) == "u2029") {
				if encoded[i+5] == '8' {
					canonical = append(canonical, []byte("\u2028")...)
				} else {
					canonical = append(canonical, []byte("\u2029")...)
				}
				i += 5
				continue
			}
			canonical = append(canonical, encoded[i], encoded[i+1])
			i++
			continue
		}
		canonical = append(canonical, encoded[i])
	}
	sum := sha256.Sum256(canonical)
	return fmt.Sprintf("sha256:%x", sum)
}
