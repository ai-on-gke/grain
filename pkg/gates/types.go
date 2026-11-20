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
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"
)

type Subject struct {
	Kind       string `json:"kind"`
	TaskID     string `json:"task_id,omitempty"`
	Repository string `json:"repository,omitempty"`
	Authority  string `json:"authority,omitempty"`
	ChangeKind string `json:"change_kind,omitempty"`
	ID         string `json:"id,omitempty"`
	Ref        string `json:"ref,omitempty"`
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
type IntegrationConfig struct {
	Kind      string `json:"kind"`
	Semantics string `json:"semantics"`
}
type ChildrenConfig struct {
	Tasks     []string `json:"tasks"`
	Predicate string   `json:"predicate"`
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
type Blocker struct {
	Code    string  `json:"code"`
	Summary string  `json:"summary"`
	Target  Subject `json:"target"`
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
	Blockers      []Blocker        `json:"blockers"`
	Work          []struct{}       `json:"work"`
	Waits         []Wait           `json:"waits"`
	Actions       []struct{}       `json:"actions"`
	ObservedAt    time.Time        `json:"observed_at"`
	ValidUntil    time.Time        `json:"valid_until"`
	Error         *EvaluationError `json:"error"`
}

// boundResultSummary keeps complete scoped diagnosis in inert guidance while
// honoring the shared v1 wire limits for both review and CI results.
func boundResultSummary(r *Result) {
	if len(r.Summary) <= 240 {
		return
	}
	summary := []rune(r.Summary)
	if len(summary) <= 240 {
		return
	}
	detail := r.Summary
	r.Summary = string(summary[:239]) + "…"
	if r.HumanText != nil {
		detail = *r.HumanText + "\n\n" + detail
	}
	text := []rune(detail)
	if len(text) > 8192 {
		detail = string(text[:8191]) + "…"
	}
	r.HumanText = &detail
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

var digestBufPool = sync.Pool{
	New: func() any { return new(bytes.Buffer) },
}

func sha256Digest(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	var out [7 + 64]byte
	copy(out[:7], "sha256:")
	hex.Encode(out[7:], sum[:])
	return string(out[:])
}

func unescapeLineSeparators(encoded []byte) []byte {
	if !bytes.Contains(encoded, []byte(`\u202`)) {
		return encoded
	}
	canonical := make([]byte, 0, len(encoded))
	for i := 0; i < len(encoded); i++ {
		if encoded[i] == '\\' && i+1 < len(encoded) {
			if i+5 < len(encoded) && (string(encoded[i+1:i+6]) == "u2028" || string(encoded[i+1:i+6]) == "u2029") {
				if encoded[i+5] == '8' {
					canonical = append(canonical, "\u2028"...)
				} else {
					canonical = append(canonical, "\u2029"...)
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
	return canonical
}

// Digest canonicalizes the JSON-only, ASCII-keyed subset used by this slice.
// Re-encoding through sorted object keys makes the digest independent of Go
// struct field order.
func Digest(v any) string {
	buf := digestBufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer func() {
		if buf.Cap() <= 1<<20 {
			digestBufPool.Put(buf)
		}
	}()
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	raw := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	switch v.(type) {
	case string, []string:
		return sha256Digest(unescapeLineSeparators(raw))
	}
	out := digestBufPool.Get().(*bytes.Buffer)
	out.Reset()
	defer func() {
		if out.Cap() <= 1<<20 {
			digestBufPool.Put(out)
		}
	}()
	if canonicalizeJSON(raw, out) {
		return sha256Digest(unescapeLineSeparators(out.Bytes()))
	}
	// Fallback to full decode/encode if canonicalization encountered unexpected input.
	var obj any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	_ = dec.Decode(&obj)
	out.Reset()
	enc2 := json.NewEncoder(out)
	enc2.SetEscapeHTML(false)
	_ = enc2.Encode(obj)
	return sha256Digest(unescapeLineSeparators(bytes.TrimSuffix(out.Bytes(), []byte("\n"))))
}

// canonicalizeJSON writes canonical JSON (compact, sorted object keys with
// last-write-wins deduplication, non-HTML-escaped strings) from valid JSON
// input into out. Returns false on malformed input so callers can fall back.
func canonicalizeJSON(src []byte, out *bytes.Buffer) bool {
	next, ok := writeCanonicalValue(src, 0, out)
	if !ok {
		return false
	}
	next = skipJSONSpace(src, next)
	return next == len(src)
}

func skipJSONSpace(src []byte, i int) int {
	for i < len(src) {
		switch src[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

func scanJSONString(src []byte, i int) (end int, hasEscape bool, ok bool) {
	if i >= len(src) || src[i] != '"' {
		return 0, false, false
	}
	i++
	for i < len(src) {
		c := src[i]
		if c == '"' {
			return i + 1, hasEscape, true
		}
		if c == '\\' {
			hasEscape = true
			i += 2
			continue
		}
		i++
	}
	return 0, false, false
}

func writeCanonicalString(src []byte, start, end int, hasEscape bool, out *bytes.Buffer) bool {
	if !hasEscape {
		out.Write(src[start:end])
		return true
	}
	// If the string contains \u or \/ (from json.RawMessage or HTML-escaped
	// sources), normalize through Go's JSON string encoder with SetEscapeHTML(false)
	// so escapes match encoding/json's canonical output exactly.
	if bytes.IndexByte(src[start:end], 'u') < 0 && !bytes.Contains(src[start:end], []byte(`\/`)) {
		out.Write(src[start:end])
		return true
	}
	var s string
	if json.Unmarshal(src[start:end], &s) != nil {
		return false
	}
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	if enc.Encode(s) != nil {
		return false
	}
	b := out.Bytes()
	if len(b) > 0 && b[len(b)-1] == '\n' {
		out.Truncate(len(b) - 1)
	}
	return true
}

func scanJSONValue(src []byte, i int) (start, end int, ok bool) {
	i = skipJSONSpace(src, i)
	if i >= len(src) {
		return 0, 0, false
	}
	start = i
	switch src[i] {
	case '"':
		e, _, ok := scanJSONString(src, i)
		return start, e, ok
	case '{', '[':
		depth := 0
		for i < len(src) {
			switch src[i] {
			case '"':
				e, _, ok := scanJSONString(src, i)
				if !ok {
					return 0, 0, false
				}
				i = e
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return start, i + 1, true
				}
			}
			i++
		}
		return 0, 0, false
	default:
		for i < len(src) {
			switch src[i] {
			case ' ', '\t', '\n', '\r', ',', ']', '}':
				return start, i, i > start
			}
			i++
		}
		return start, i, i > start
	}
}

type canonicalField struct {
	key        string
	keyStart   int
	keyEnd     int
	keyEscaped bool
	valStart   int
	valEnd     int
	order      int
}

func writeCanonicalValue(src []byte, i int, out *bytes.Buffer) (int, bool) {
	i = skipJSONSpace(src, i)
	if i >= len(src) {
		return 0, false
	}
	switch src[i] {
	case '"':
		end, hasEscape, ok := scanJSONString(src, i)
		if !ok || !writeCanonicalString(src, i, end, hasEscape, out) {
			return 0, false
		}
		return end, true
	case '[':
		out.WriteByte('[')
		i = skipJSONSpace(src, i+1)
		if i < len(src) && src[i] == ']' {
			out.WriteByte(']')
			return i + 1, true
		}
		for elem := 0; ; elem++ {
			if elem > 0 {
				out.WriteByte(',')
			}
			var ok bool
			i, ok = writeCanonicalValue(src, i, out)
			if !ok {
				return 0, false
			}
			i = skipJSONSpace(src, i)
			if i >= len(src) {
				return 0, false
			}
			if src[i] == ']' {
				out.WriteByte(']')
				return i + 1, true
			}
			if src[i] != ',' {
				return 0, false
			}
			i++
		}
	case '{':
		i = skipJSONSpace(src, i+1)
		if i < len(src) && src[i] == '}' {
			out.WriteString("{}")
			return i + 1, true
		}
		var fields []canonicalField
		for idx := 0; ; idx++ {
			i = skipJSONSpace(src, i)
			keyEnd, keyEscaped, ok := scanJSONString(src, i)
			if !ok {
				return 0, false
			}
			var key string
			if !keyEscaped {
				key = string(src[i+1 : keyEnd-1])
			} else if json.Unmarshal(src[i:keyEnd], &key) != nil {
				return 0, false
			}
			colon := skipJSONSpace(src, keyEnd)
			if colon >= len(src) || src[colon] != ':' {
				return 0, false
			}
			valStart, valEnd, ok := scanJSONValue(src, colon+1)
			if !ok {
				return 0, false
			}
			fields = append(fields, canonicalField{
				key:        key,
				keyStart:   i,
				keyEnd:     keyEnd,
				keyEscaped: keyEscaped,
				valStart:   valStart,
				valEnd:     valEnd,
				order:      idx,
			})
			i = skipJSONSpace(src, valEnd)
			if i >= len(src) {
				return 0, false
			}
			if src[i] == '}' {
				i++
				break
			}
			if src[i] != ',' {
				return 0, false
			}
			i++
		}
		slices.SortFunc(fields, func(a, b canonicalField) int {
			if c := strings.Compare(a.key, b.key); c != 0 {
				return c
			}
			return a.order - b.order
		})
		out.WriteByte('{')
		first := true
		for idx := 0; idx < len(fields); idx++ {
			// Duplicate keys keep the last occurrence, matching json.Unmarshal into map[string]any.
			if idx+1 < len(fields) && fields[idx+1].key == fields[idx].key {
				continue
			}
			if !first {
				out.WriteByte(',')
			}
			first = false
			f := fields[idx]
			if !writeCanonicalString(src, f.keyStart, f.keyEnd, f.keyEscaped, out) {
				return 0, false
			}
			out.WriteByte(':')
			if _, ok := writeCanonicalValue(src, f.valStart, out); !ok {
				return 0, false
			}
		}
		out.WriteByte('}')
		return i, true
	default:
		start, end, ok := scanJSONValue(src, i)
		if !ok {
			return 0, false
		}
		out.Write(src[start:end])
		return end, true
	}
}

// Clone returns an independent deep copy of ObservationView so cached views
// can be redacted or inspected by concurrent readers without aliasing slices
// or pointer fields.
func (v ObservationView) Clone() ObservationView {
	out := v
	if v.Bindings != nil {
		out.Bindings = make([]Binding, len(v.Bindings))
		for i, b := range v.Bindings {
			if b.Action != nil {
				a := *b.Action
				b.Action = &a
			}
			if b.Config != nil {
				b.Config = append(json.RawMessage(nil), b.Config...)
			}
			out.Bindings[i] = b
		}
	}
	if v.Results != nil {
		out.Results = make([]EvaluationState, len(v.Results))
		for i, es := range v.Results {
			if es.AcceptedAt != nil {
				t := *es.AcceptedAt
				es.AcceptedAt = &t
			}
			if es.InvalidatedAt != nil {
				t := *es.InvalidatedAt
				es.InvalidatedAt = &t
			}
			if es.InvalidationReason != nil {
				s := *es.InvalidationReason
				es.InvalidationReason = &s
			}
			if es.RetryAt != nil {
				t := *es.RetryAt
				es.RetryAt = &t
			}
			r := es.Result
			if r.HumanText != nil {
				s := *r.HumanText
				r.HumanText = &s
			}
			if r.AgentText != nil {
				s := *r.AgentText
				r.AgentText = &s
			}
			if r.Evidence != nil {
				r.Evidence = make([]Evidence, len(r.Evidence))
				for j, e := range es.Result.Evidence {
					if e.Head != nil {
						h := *e.Head
						e.Head = &h
					}
					r.Evidence[j] = e
				}
			}
			if r.Dependencies != nil {
				r.Dependencies = make([]Dependency, len(r.Dependencies))
				for j, d := range es.Result.Dependencies {
					if d.Head != nil {
						h := *d.Head
						d.Head = &h
					}
					r.Dependencies[j] = d
				}
			}
			if r.Blockers != nil {
				r.Blockers = slices.Clone(r.Blockers)
			}
			if r.Work != nil {
				r.Work = slices.Clone(r.Work)
			}
			if r.Waits != nil {
				r.Waits = slices.Clone(r.Waits)
			}
			if r.Actions != nil {
				r.Actions = slices.Clone(r.Actions)
			}
			if r.Error != nil {
				e := *r.Error
				r.Error = &e
			}
			es.Result = r
			out.Results[i] = es
		}
	}
	return out
}
