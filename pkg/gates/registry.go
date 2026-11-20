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

package gates

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

//go:embed delivery.schema.json
var deliverySchema []byte

//go:embed integration.schema.json
var integrationSchema []byte

//go:embed children.schema.json
var childrenSchema []byte

//go:embed acknowledgment.schema.json
var acknowledgmentSchema []byte

//go:embed base_health.schema.json
var baseHealthSchema []byte

//go:embed nightly_health.schema.json
var nightlyHealthSchema []byte

//go:embed publishing_health.schema.json
var publishingHealthSchema []byte

//go:embed pipeline_health.schema.json
var pipelineHealthSchema []byte

//go:embed checks.schema.json
var checksSchema []byte

//go:embed review.schema.json
var reviewSchema []byte

//go:embed approval.schema.json
var approvalSchema []byte

//go:embed qa_stage.schema.json
var qaStageSchema []byte

//go:embed release_qualification.schema.json
var releaseQualificationSchema []byte

const Implementation = "builtin-gate-observer-v10"

type Descriptor struct {
	Type           string
	Version        uint32
	SchemaDigest   string
	ConfigSchema   json.RawMessage
	Implementation string
}

// Discover returns immutable copies. This registry is fixed at build time.
func Discover() []Descriptor {
	items := []Descriptor{{"integration", 1, Digest(json.RawMessage(integrationSchema)), append([]byte(nil), integrationSchema...), Implementation}, {"children", 1, Digest(json.RawMessage(childrenSchema)), append([]byte(nil), childrenSchema...), Implementation}, {"checks", 1, Digest(json.RawMessage(checksSchema)), append([]byte(nil), checksSchema...), Implementation}, {"acknowledgment", 1, Digest(json.RawMessage(acknowledgmentSchema)), append([]byte(nil), acknowledgmentSchema...), Implementation}, {"delivery", 1, Digest(json.RawMessage(deliverySchema)), append([]byte(nil), deliverySchema...), Implementation}, {"review", 1, Digest(json.RawMessage(reviewSchema)), append([]byte(nil), reviewSchema...), Implementation}, {"approval", 1, Digest(json.RawMessage(approvalSchema)), append([]byte(nil), approvalSchema...), Implementation}, {"qa_stage", 1, Digest(json.RawMessage(qaStageSchema)), append([]byte(nil), qaStageSchema...), Implementation}, {"release_qualification", 1, Digest(json.RawMessage(releaseQualificationSchema)), append([]byte(nil), releaseQualificationSchema...), Implementation}}
	items = append(items, healthDescriptors()...)
	slices.SortFunc(items, func(a, b Descriptor) int { return strings.Compare(a.Type, b.Type) })
	return items
}

var registryDigest = sync.OnceValue(func() string { return Digest(Discover()) })

// RegistryDigest pins immutable built-in schemas and implementation metadata.
// Avoid decoding and canonicalizing the same registry on each scoped read.
func RegistryDigest() string { return registryDigest() }

// Validate admits the implemented closed v1 outcome profiles.
func Validate(typ string, version uint32, raw []byte) (json.RawMessage, error) {
	if len(raw) > 64*1024 {
		return nil, fmt.Errorf("config exceeds 64 KiB")
	}
	if version != 1 {
		return nil, fmt.Errorf("unsupported gate version")
	}
	var allowedKeys map[string]bool
	switch typ {
	case "checks":
		allowedKeys = map[string]bool{"mode": true, "extra_jobs": true, "empty": true, "semantics": true}
	case "delivery":
		allowedKeys = map[string]bool{"product": true, "semantics": true}
	case "qa_stage":
		allowedKeys = map[string]bool{"stage": true, "flow_revision": true, "assignment": true}
	case "release_qualification":
		allowedKeys = map[string]bool{"predicate": true, "expected_commit": true, "release_version": true, "policy_revision": true}
	case "integration":
		allowedKeys = map[string]bool{"kind": true, "semantics": true}
	case "children":
		allowedKeys = map[string]bool{"tasks": true, "predicate": true}
	case "review":
		allowedKeys = map[string]bool{"category": true, "requirement_id": true, "semantics": true}
	case "approval":
		allowedKeys = map[string]bool{"requirement_ids": true, "semantics": true}
	case "acknowledgment":
		allowedKeys = map[string]bool{"mode": true}
	case "base_health":
		allowedKeys = map[string]bool{"semantics": true, "allow_nonreproduction": true}
	case "nightly_health":
		allowedKeys = map[string]bool{"workflow": true, "since": true, "window_seconds": true, "semantics": true}
	case "publishing_health":
		allowedKeys = map[string]bool{"semantics": true, "expected_commit": true}
	case "pipeline_health":
		allowedKeys = map[string]bool{"semantics": true, "repos": true}
	default:
		return nil, fmt.Errorf("unregistered gate type")
	}
	// Decode tokens separately so duplicate keys are rejected, not last-wins.
	// encoding/json matches struct fields without regard to case, so enforce
	// the closed schema's exact property names before typed decoding.
	d := json.NewDecoder(bytes.NewReader(raw))
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("config must be an object")
	}
	keys := map[string]bool{}
	for d.More() {
		t, e := d.Token()
		if e != nil {
			return nil, e
		}
		k, ok := t.(string)
		if !ok || keys[k] {
			return nil, fmt.Errorf("duplicate config key")
		}
		if !allowedKeys[k] {
			return nil, fmt.Errorf("unknown config key")
		}
		keys[k] = true
		var v any
		if e = d.Decode(&v); e != nil {
			return nil, e
		}
	}
	if _, err = d.Token(); err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing config data")
	}
	if isHealthType(typ) {
		if len(keys) != len(allowedKeys) {
			return nil, fmt.Errorf("all config fields are required")
		}
		return validateHealth(typ, raw)
	}
	decode := func(v any) error {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		return dec.Decode(v)
	}
	validID := func(s string) bool { return strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= 256 }
	switch typ {
	case "qa_stage", "release_qualification":
		return validateQualification(typ, raw)
	case "checks":
		if len(keys) != 4 {
			return nil, fmt.Errorf("checks config requires all canonical fields")
		}
		return validateChecks(raw)
	case "review":
		c := ReviewConfig{}
		if err := decode(&c); err != nil {
			return nil, err
		}
		if !slices.Contains([]string{"ordinary", "security", "path", "takeover"}, c.Category) || !validID(c.RequirementID) || !slices.Contains([]string{"current_head_v1", "legacy_completion_v1"}, c.Semantics) {
			return nil, fmt.Errorf("unsupported review profile")
		}
		b, _ := json.Marshal(c)
		return b, nil
	case "approval":
		c := ApprovalConfig{}
		if err := decode(&c); err != nil {
			return nil, err
		}
		if c.Semantics != "exact_head_v1" || len(c.RequirementIDs) == 0 || len(c.RequirementIDs) > 64 {
			return nil, fmt.Errorf("unsupported approval profile")
		}
		seen := map[string]bool{}
		for _, id := range c.RequirementIDs {
			if !validID(id) || seen[id] {
				return nil, fmt.Errorf("invalid or duplicate requirement ID")
			}
			seen[id] = true
		}
		b, _ := json.Marshal(c)
		return b, nil
	case "delivery":
		c := DeliveryConfig{}
		if err := decode(&c); err != nil {
			return nil, err
		}
		if !oneOf(c.Product, "report", "change", "review", "nothing_needed", "any") || !oneOf(c.Semantics, "current_v1", "legacy_finish_v1") {
			return nil, fmt.Errorf("unsupported delivery profile")
		}
		b, _ := json.Marshal(c)
		return b, nil
	case "acknowledgment":
		c := AcknowledgmentConfig{}
		if err := decode(&c); err != nil {
			return nil, err
		}
		if !oneOf(c.Mode, "explicit_receipt", "legacy_report", "legacy_child_answer", "legacy_nothing_needed_ack") {
			return nil, fmt.Errorf("unsupported acknowledgment profile")
		}
		b, _ := json.Marshal(c)
		return b, nil
	case "integration":
		c := IntegrationConfig{}
		if err := decode(&c); err != nil {
			return nil, err
		}
		if !oneOf(c.Kind, "pr_merged", "cl_submitted") || c.Semantics != "positive_v1" {
			return nil, fmt.Errorf("unsupported integration profile")
		}
		b, _ := json.Marshal(c)
		return b, nil
	case "children":
		c := ChildrenConfig{}
		if err := decode(&c); err != nil {
			return nil, err
		}
		if !oneOf(c.Predicate, "success", "closed", "merged") || len(c.Tasks) == 0 || len(c.Tasks) > 64 {
			return nil, fmt.Errorf("invalid children selection")
		}
		seen := map[string]bool{}
		for _, id := range c.Tasks {
			if strings.TrimSpace(id) == "" || utf8.RuneCountInString(id) > 256 || seen[id] {
				return nil, fmt.Errorf("invalid child identity")
			}
			seen[id] = true
		}
		b, _ := json.Marshal(c)
		return b, nil
	default:
		return nil, fmt.Errorf("unregistered gate type")
	}
}

func oneOf(s string, values ...string) bool {
	for _, v := range values {
		if s == v {
			return true
		}
	}
	return false
}

// Facts contains only the scoped reader's immutable evidence, never private
// evaluator state. CompleteEmpty is meaningful: no report is not success.
type Facts struct {
	Health          map[string]HealthFacts
	LegacyMet       map[string]bool
	Bindings        []Binding
	Product         string
	ProductEvidence *Evidence
	ProductUnknown  string
	Blocked         bool
	ChildAnswer     bool
	NothingAck      bool
	Resources       map[string][]ResourceFact
	Children        map[string]ChildFact
	Cycle           bool

	Checks             []ChecksFacts
	Change             *ChangeFacts
	IncompleteReason   string
	TaskID             string
	Applicable         bool
	UnsupportedReason  string
	Complete           bool
	Running            bool
	Settled            bool
	Report             *Evidence
	Receipt            *Evidence
	NoWorkReceipt      *Evidence
	ReceiptUnversioned bool
	SourceVersion      string
	LocalSourceVersion string
	ObservedAt         time.Time
	ValidUntil         time.Time
}

func Bindings(taskID string) []Binding {
	s := Subject{Kind: "task", TaskID: taskID}
	origin := Origin{Kind: "task", ID: taskID, Revision: Implementation, Imported: false}
	return []Binding{
		{ID: "report", Type: "delivery", Version: 1, Purpose: "monitor", Subject: s, Config: json.RawMessage(`{"product":"report","semantics":"current_v1"}`), FreshnessSeconds: 60, Origin: origin},
		{ID: "receipt", Type: "acknowledgment", Version: 1, Purpose: "monitor", Subject: s, Config: json.RawMessage(`{"mode":"explicit_receipt"}`), FreshnessSeconds: 60, Origin: origin},
	}
}

func BindingsFor(taskID string, checks []ChecksFacts) []Binding {
	out := Bindings(taskID)
	for _, f := range checks {
		out = append(out, ChecksBinding(f, false), ChecksBinding(f, true))
	}
	return out
}

// Evaluate is pure. Predictions are text and inert wait descriptors; work and
// actions are always empty. The controller alone accepts diagnostic versions.
func Evaluate(b Binding, f Facts, now time.Time, generation uint64) Result {
	if isHealthType(b.Type) {
		return evaluateHealth(b, f, now, generation)
	}
	if b.Type == "checks" {
		for _, c := range f.Checks {
			if c.Subject == b.Subject {
				return EvaluateChecks(b, c, now, generation)
			}
		}
		return EvaluateChecks(b, ChecksFacts{TaskID: f.TaskID, Subject: b.Subject, ObservedAt: now, ValidUntil: now}, now, generation)
	}
	if b.Type == "review" || b.Type == "approval" {
		return evaluateChange(b, f, now, generation)
	}
	localVersion := f.LocalSourceVersion
	if localVersion == "" {
		localVersion = f.SourceVersion
	}
	contextDigest := Digest(struct {
		Binding       Binding
		Source, Build string
	}{b, f.SourceVersion, Implementation})
	r := Result{EvaluationID: "shadow-" + contextDigest[7:], BindingID: "task/" + f.TaskID + "/" + b.ID, Type: b.Type, Version: b.Version, ContextDigest: contextDigest, Status: "unknown", Summary: "Evidence is incomplete.", Evidence: []Evidence{}, Dependencies: []Dependency{{Key: "task/" + f.TaskID + "/report-receipt", Subject: b.Subject, Version: localVersion, CollectionGeneration: localVersion, ScopeDigest: Digest(b.Subject), Complete: f.Complete, ObservedAt: f.ObservedAt, ValidUntil: f.ValidUntil, DirtyGeneration: generation, CoveredGeneration: generation}}, Blockers: []Blocker{}, Work: []struct{}{}, Waits: []Wait{}, Actions: []struct{}{}, ObservedAt: now, ValidUntil: f.ValidUntil}
	if _, err := Validate(b.Type, b.Version, b.Config); err != nil {
		r.Summary = "Unsupported or malformed gate configuration."
		return r
	}
	if f.ObservedAt.After(now) {
		r.Summary = "Evidence was observed after the evaluation clock; refresh required."
		return r
	}
	// Standing/session policy controls lifecycle disposition, not whether an
	// independently selected resource or child outcome can be observed.
	if now.Before(f.ValidUntil) {
		if b.Type == "integration" {
			return evaluateIntegration(b, f, r)
		}
		if b.Type == "children" {
			return evaluateChildren(b, f, r)
		}
	}
	if !f.Applicable {
		r.Summary = "Not applicable: " + f.UnsupportedReason
		return r
	}
	// A typed no-work product and its receipt do not depend on report-body
	// completeness. Explicit report receipt remains a separate obligation.
	if now.Before(f.ValidUntil) && b.Type == "acknowledgment" && f.Product == "nothing_needed" && f.ProductEvidence != nil && f.ProductUnknown == "" {
		var config AcknowledgmentConfig
		_ = json.Unmarshal(b.Config, &config)
		if config.Mode == "legacy_nothing_needed_ack" {
			r.Dependencies[0].Complete = true
			return evaluateLegacyAck(config, f, r)
		}
	}
	if now.Before(f.ValidUntil) && b.Type == "delivery" && (f.Product == "change" || f.Product == "nothing_needed") && f.ProductEvidence != nil && f.ProductUnknown == "" {
		var config DeliveryConfig
		_ = json.Unmarshal(b.Config, &config)
		if config.Product != "report" {
			r.Dependencies[0].Complete = true
			return evaluateDelivery(config, f, r)
		}
	}
	if !f.Complete {
		if f.IncompleteReason != "" {
			r.Summary = f.IncompleteReason
		}
		return r
	}
	if !now.Before(f.ValidUntil) {
		r.Summary = "Evidence is stale; refresh required."
		return r
	}
	switch b.Type {
	case "delivery":
		var c DeliveryConfig
		_ = json.Unmarshal(b.Config, &c)
		if c.Product != "report" || f.Product != "" || f.ProductUnknown != "" || f.Blocked {
			return evaluateDelivery(c, f, r)
		}
		switch {
		case f.Running:
			r.Status = "pending"
			r.Summary = "Report producer is still running."
		case f.Report != nil && f.Settled:
			r.Evidence = append(r.Evidence, *f.Report)
			r.Status = "met"
			r.Summary = "Settled run delivered a durable report. This does not prove receipt or completion."
		case f.Settled:
			r.Status = "unmet"
			r.Summary = "No authenticated report for the settled run."
		default:
			r.Status = "unmet"
			r.Summary = "No current settled report is recorded."
		}
	case "acknowledgment":
		var c AcknowledgmentConfig
		_ = json.Unmarshal(b.Config, &c)
		if c.Mode != "explicit_receipt" {
			return evaluateLegacyAck(c, f, r)
		}
		reportProduct := f.Product == "" || f.Product == "report"
		switch {
		case f.Blocked:
			r.Summary = "Question, access request or suspension takes finish precedence."
		case f.ProductUnknown != "":
			r.Summary = f.ProductUnknown
		case !reportProduct || f.Report == nil || !f.Settled || f.Running:
			r.Summary = "No current settled report to acknowledge."
		case f.Receipt != nil:
			r.Evidence = append(r.Evidence, *f.Report, *f.Receipt)
			r.Status = "met"
			r.Summary = "An eligible person's recorded close acknowledges this report version."
		case f.ReceiptUnversioned:
			r.Evidence = append(r.Evidence, *f.Report)
			r.Summary = "Closure has no eligible receipt tied to the current report."
		default:
			r.Evidence = append(r.Evidence, *f.Report)
			r.Status = "pending"
			r.Summary = "Current report awaits a person's receipt; a reply is not acknowledgment."
			text := "Prediction only: the existing Acknowledge/Close flow can record receipt."
			r.HumanText = &text
			r.Waits = append(r.Waits, Wait{Event: "task_message", Key: "task/" + f.TaskID, Version: f.Report.Version, Deadline: f.ValidUntil, Owner: "task_lifecycle", OnTimeout: "escalate"})
		}
	}
	return r
}

// UnavailableObservation constructs a safe rejected diagnostic, without reusing
// evidence, waits or accepted metadata from a prior successful collection.
// SourceRevision is an unavailable build sentinel, not a captured fact version.
func UnavailableObservation(taskID string, now time.Time, checks ...ChecksFacts) ObservationView {
	return UnavailableObservationForBindings(taskID, now, BindingsFor(taskID, checks))
}

// UnavailableObservationForBindings retains only a validated diagnostic scope,
// never accepted evidence or operational predictions.
func UnavailableObservationForBindings(taskID string, now time.Time, bindings []Binding) ObservationView {
	v := ObservationView{APIVersion: "grain.gates/v1", Mode: "observation_only", Authority: "legacy", SourceRevision: Implementation, ComparedAt: now, LegacyState: "unavailable", Bindings: bindings, Results: []EvaluationState{}, Dirty: true}
	for _, b := range bindings {
		f := Facts{TaskID: taskID, ObservedAt: now, ValidUntil: now, SourceVersion: Implementation}
		if isHealthType(b.Type) {
			f.Health = map[string]HealthFacts{b.ID: {Version: Implementation, ObservedAt: now, ValidUntil: now}}
		}
		r := Evaluate(b, f, now, 0)
		r.Status = "error"
		r.Summary = "Observation collection unavailable; legacy behavior is unaffected."
		r.Error = &EvaluationError{Code: "unavailable", Message: "Diagnostic read failed or timed out.", Retryable: true}
		retry := now.Add(time.Second)
		v.Results = append(v.Results, EvaluationState{EvaluationID: r.EvaluationID, Disposition: "rejected", Result: r, RetryAt: &retry})
	}
	return v
}
