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
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

type BaseHealthConfig struct {
	Semantics            string `json:"semantics"`
	AllowNonreproduction bool   `json:"allow_nonreproduction"`
}
type NightlyHealthConfig struct {
	Workflow      string `json:"workflow"`
	Since         string `json:"since"`
	WindowSeconds uint32 `json:"window_seconds"`
	Semantics     string `json:"semantics"`
}
type PublishingHealthConfig struct {
	Semantics      string `json:"semantics"`
	ExpectedCommit string `json:"expected_commit"`
}
type PipelineHealthConfig struct {
	Repos     []string `json:"repos"`
	Semantics string   `json:"semantics"`
}

func healthDescriptors() []Descriptor {
	out := []Descriptor{}
	for _, x := range []struct {
		name   string
		schema []byte
	}{
		{"base_health", baseHealthSchema}, {"nightly_health", nightlyHealthSchema},
		{"publishing_health", publishingHealthSchema}, {"pipeline_health", pipelineHealthSchema},
	} {
		out = append(out, Descriptor{x.name, 1, Digest(json.RawMessage(x.schema)), append([]byte(nil), x.schema...), Implementation})
	}
	return out
}
func isHealthType(t string) bool {
	return slices.Contains([]string{"base_health", "nightly_health", "publishing_health", "pipeline_health"}, t)
}
func validateHealth(typ string, raw []byte) (json.RawMessage, error) {
	// Validate already rejected duplicate/unknown/missing keys. Reject nulls too:
	// bool/slice zero values must not silently materialize a stored contract.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	for _, v := range obj {
		if string(v) == "null" {
			return nil, fmt.Errorf("null config field")
		}
	}
	var v any
	valid := false
	switch typ {
	case "base_health":
		c := BaseHealthConfig{}
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		valid = slices.Contains([]string{"current_tip_v1", "legacy_recorded_v1"}, c.Semantics)
		v = c
	case "nightly_health":
		c := NightlyHealthConfig{}
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		_, err := time.Parse(time.RFC3339Nano, c.Since)
		valid = err == nil && len(c.Since) <= 35 && utf8.RuneCountInString(c.Workflow) > 0 && utf8.RuneCountInString(c.Workflow) <= 256 && c.WindowSeconds >= 60 && c.WindowSeconds <= 604800 && slices.Contains([]string{"window_v1", "legacy_500_v1", "legacy_verified_push_v1"}, c.Semantics)
		v = c
	case "publishing_health":
		c := PublishingHealthConfig{}
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		valid = utf8.RuneCountInString(c.ExpectedCommit) > 0 && utf8.RuneCountInString(c.ExpectedCommit) <= 256 && slices.Contains([]string{"positive_current_v1", "legacy_no_finding_v1"}, c.Semantics)
		v = c
	case "pipeline_health":
		c := PipelineHealthConfig{}
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		valid = len(c.Repos) > 0 && len(c.Repos) <= 64 && slices.Contains([]string{"scoped_v1", "legacy_quiet_v1"}, c.Semantics)
		seen := map[string]bool{}
		for _, r := range c.Repos {
			if len(r) == 0 || utf8.RuneCountInString(r) > 256 || seen[r] {
				valid = false
			}
			seen[r] = true
		}
		v = c
	}
	if !valid {
		return nil, fmt.Errorf("unsupported or invalid health profile")
	}
	out, err := json.Marshal(v)
	return out, err
}

// HealthFacts are immutable, scoped inputs from ordinary internal collectors.
// They carry no operational client or remediation command. A missing proof is
// distinct from an authenticated complete-empty collection.
type HealthFacts struct {
	LegacyVerifiedDisposition *bool
	SourceRevisions           []string
	Complete                  bool
	Denied                    bool
	Reason                    string
	Version                   string
	PolicyRevision            string
	AuthorizationRevision     uint64
	ObservedAt                time.Time
	ValidUntil                time.Time
	Base                      *BaseHealthFacts
	Runs                      []WorkflowHealthRun // newest first, grouped by exact run and attempt
	LegacyJobs                []HealthJob         // existing newest-500 selection order
	Publishing                *PublishingHealthFacts
	Pipeline                  *PipelineHealthFacts
}
type BaseHealthFacts struct {
	Present                                      bool
	Commit, Tip, RunID, Conclusion, Confirmation string
	InFlight, Broken                             bool
	// CurrentProof binds the source's complete plan to this exact recorded row,
	// policy and tip. NonreproductionProof covers the authentic recheck job set.
	CurrentProof, NonreproductionProof bool
}
type HealthJob struct {
	ID, Name, Workflow, Ref, Commit, Conclusion, Source, App string
	RunID                                                    int64
	Attempt                                                  int
	At                                                       time.Time
}
type WorkflowHealthRun struct {
	ID                                                                                 int64
	Attempt                                                                            int
	Repository, HeadRepository, Ref, Commit, Workflow, Path, Event, Source, Conclusion string
	StartedAt, CompletedAt                                                             time.Time
	Complete                                                                           bool
	VerifiedDefaultPush                                                                bool
	Jobs                                                                               []HealthJob
}
type PublicationReceipt struct {
	Target, Commit, Artifact, Version, Issuer string
	Successful                                bool
}
type PublishingHealthFacts struct {
	Present, LegacyFinding bool
	Commit, Conclusion     string
	InFlight               bool
	Tip                    string
	// Target set and receipts must come from the publishing authority, never
	// from a successful build/signing job or a superseded release row.
	TargetsComplete bool
	Targets         []string
	Receipts        []PublicationReceipt
}
type PipelineHealthFacts struct {
	CollectionRevision                            string
	DeploymentRead                                bool
	ScanComplete                                  bool
	ExpectedBasesKnown                            bool
	Bases                                         []BaseHealthFacts
	OpenNotifications, StalledTasks, BlockedQueue int
	Recovering                                    bool
}

func evaluateHealth(b Binding, facts Facts, now time.Time, generation uint64) Result {
	h := facts.Health[b.ID]
	digest := Digest(struct {
		Binding       Binding
		Source, Build string
	}{b, h.Version, Implementation})
	r := Result{EvaluationID: "shadow-" + digest[7:], BindingID: "task/" + facts.TaskID + "/" + b.ID, Type: b.Type, Version: b.Version, ContextDigest: digest, Status: "unknown", Summary: "Shadow prediction: scoped health evidence is missing.",
		Evidence: []Evidence{}, Dependencies: []Dependency{{Key: b.Type + "/" + Digest(b.Subject)[7:], Subject: b.Subject, Version: h.Version, CollectionGeneration: h.Version, ScopeDigest: Digest(b.Subject), Complete: h.Complete, ObservedAt: h.ObservedAt, ValidUntil: h.ValidUntil, AuthorizationRevision: h.AuthorizationRevision, DirtyGeneration: generation, CoveredGeneration: generation}},
		Blockers: []Blocker{}, Work: []struct{}{}, Actions: []struct{}{}, Waits: []Wait{}, ObservedAt: now, ValidUntil: facts.ValidUntil}
	if !h.ValidUntil.IsZero() && h.ValidUntil.Before(r.ValidUntil) {
		r.ValidUntil = h.ValidUntil
	}
	if _, err := Validate(b.Type, b.Version, b.Config); err != nil {
		r.Summary = "Shadow prediction: unsupported health configuration."
		return r
	}
	var config struct {
		Semantics string `json:"semantics"`
	}
	_ = json.Unmarshal(b.Config, &config)
	label := "Current profile " + config.Semantics
	if strings.HasPrefix(config.Semantics, "legacy_") {
		label = "Legacy observation profile " + config.Semantics
	}
	finish := func(status, summary string) Result {
		r.Status = status
		r.Summary = label + ": " + summary
		if status == "pending" {
			r.Waits = append(r.Waits, Wait{Event: "health", Key: Digest(b.Subject), Version: h.Version, Deadline: r.ValidUntil, Owner: "health_reconciler", OnTimeout: "escalate"})
		}
		if status == "unmet" {
			s := "Shadow prediction only: existing authorized health repair and status tools may be useful. No repair, retry, publication or notification change is requested."
			r.AgentText = &s
		}
		return r
	}
	if h.Denied {
		return finish("unknown", "health source visibility is denied; no evidence is displayed.")
	}
	if h.Version == "" {
		return finish("unknown", "scoped source evidence has not been collected.")
	}
	if !now.Before(r.ValidUntil) || now.Before(h.ObservedAt) {
		return finish("unknown", "evidence is stale or has an invalid observation time.")
	}
	if h.Reason != "" && !h.Complete {
		return finish("unknown", h.Reason)
	}
	if !h.Complete {
		return finish("unknown", "collection is incomplete; empty evidence cannot prove health.")
	}
	head := (*string)(nil)
	if h.Base != nil && h.Base.Commit != "" {
		v := h.Base.Commit
		head = &v
	}
	r.Dependencies[0].Head = head
	kind := map[string]string{"base_health": "base", "nightly_health": "nightly", "publishing_health": "publication", "pipeline_health": "pipeline"}[b.Type]
	evidenceID := b.Type + ":" + Digest(b.Subject)[7:]
	if h.Base != nil && h.Base.RunID != "" {
		evidenceID = "grain/run/" + h.Base.RunID
		if len(evidenceID) > 256 {
			evidenceID = Digest(evidenceID)
		}
	}
	r.Evidence = append(r.Evidence, Evidence{Kind: kind, ID: evidenceID, Version: h.Version, Subject: b.Subject, Head: head, Digest: h.Version})
	switch b.Type {
	case "base_health":
		c := BaseHealthConfig{}
		_ = json.Unmarshal(b.Config, &c)
		status, summary := baseHealth(c, h.Base)
		return finish(status, summary)
	case "nightly_health":
		c := NightlyHealthConfig{}
		_ = json.Unmarshal(b.Config, &c)
		status, summary, expiry := nightlyHealth(c, b.Subject, h, now)
		if c.Semantics != "legacy_500_v1" {
			if run := selectNightlyRun(c, b.Subject, h, now); run != nil {
				version := Digest(*run)
				commit := run.Commit
				r.Evidence = append(r.Evidence, Evidence{Kind: "nightly", ID: fmt.Sprintf("github/run/%d/attempt/%d", run.ID, run.Attempt), Version: version, Subject: b.Subject, Head: &commit, Digest: version})
			}
		}
		if !expiry.IsZero() && expiry.Before(r.ValidUntil) {
			r.ValidUntil = expiry
			r.Dependencies[0].ValidUntil = expiry
		}
		return finish(status, summary)
	case "publishing_health":
		c := PublishingHealthConfig{}
		_ = json.Unmarshal(b.Config, &c)
		p := h.Publishing
		if c.Semantics == "legacy_no_finding_v1" {
			if p != nil && p.LegacyFinding {
				return finish("unmet", "the legacy publishing threshold reports no shipping.")
			}
			return finish("met", "no legacy publishing finding (including no release row); this is weaker evidence than publication.")
		}
		if p == nil || !p.Present {
			return finish("unknown", "no publication record; absence is not a receipt.")
		}
		expected := c.ExpectedCommit
		if expected == "current_tip" {
			expected = p.Tip
		}
		if expected == "" {
			return finish("unknown", "the exact expected commit is unavailable.")
		}
		if !p.TargetsComplete || len(p.Targets) == 0 {
			return finish("unknown", "publication target/commit/artifact receipts are incomplete; build success and supersession are insufficient.")
		}
		covered := map[string]bool{}
		for _, receipt := range p.Receipts {
			if receipt.Commit == expected && receipt.Issuer != "" && receipt.Artifact != "" && receipt.Version != "" && receipt.Successful {
				covered[receipt.Target] = true
			}
		}
		for _, target := range p.Targets {
			if target == "" || !covered[target] {
				if p.Commit == expected && slices.Contains([]string{"failure", "timed_out", "startup_failure"}, p.Conclusion) {
					return finish("unmet", "a required publication failed.")
				}
				return finish("pending", "required publication receipts are outstanding.")
			}
		}
		return finish("met", "authenticated receipts cover the exact expected commit and every publication target/artifact.")
	case "pipeline_health":
		c := PipelineHealthConfig{}
		_ = json.Unmarshal(b.Config, &c)
		if !slices.Contains(c.Repos, b.Subject.Repository) {
			return finish("unknown", "subject repository is outside the configured scope.")
		}
		p := h.Pipeline
		if p == nil {
			return finish("unknown", "no pipeline scan.")
		}
		if c.Semantics == "legacy_quiet_v1" && !p.DeploymentRead {
			return finish("unknown", "legacy quiet requires explicit deployment-wide read scope.")
		}
		if p.OpenNotifications > 0 || p.StalledTasks > 0 || p.BlockedQueue > 0 {
			return finish("unmet", "pipeline notifications, stalled tasks or blocked/stalled merge queue entries were observed.")
		}
		if c.Semantics == "scoped_v1" {
			if !p.ExpectedBasesKnown || len(p.Bases) == 0 {
				return finish("unknown", "the configured managed base-ref set is absent or incomplete.")
			}
			for i := range p.Bases {
				s, _ := baseHealth(BaseHealthConfig{Semantics: "current_tip_v1", AllowNonreproduction: true}, &p.Bases[i])
				if s != "met" {
					return finish(s, "a configured current base verdict is not healthy or complete.")
				}
			}
		} else {
			for _, base := range p.Bases {
				if base.Broken || base.Conclusion != "" && base.Conclusion != "success" {
					return finish("unmet", "a deployment base verdict is broken or has a non-success conclusion.")
				}
			}
		}
		if !p.ScanComplete {
			return finish("unknown", "pipeline scan is incomplete; quiet has not been established.")
		}
		if p.Recovering {
			return finish("pending", "active pipeline recovery is in progress.")
		}
		return finish("met", "the declared scan is complete and quiet, with paused/self tasks excluded.")
	}
	return r
}
func baseHealth(c BaseHealthConfig, b *BaseHealthFacts) (string, string) {
	if b == nil || !b.Present {
		return "unknown", "no base CI verdict is recorded."
	}
	if c.Semantics == "current_tip_v1" {
		if b.Tip == "" || b.Commit == "" {
			return "unknown", "the exact current base tip is unavailable."
		}
		if b.Tip != b.Commit {
			return "unknown", "the recorded verdict is for an older base tip."
		}
		if !b.CurrentProof {
			return "unknown", "the exact configured CI source/plan is incomplete or unverified."
		}
	}
	if b.InFlight {
		return "pending", "base CI is still checking."
	}
	if b.Broken {
		return "unmet", "the recorded base verdict is broken."
	}
	if b.Conclusion == "success" {
		return "met", "the base verdict is successful."
	}
	if b.Confirmation == "cleared" && c.AllowNonreproduction {
		if c.Semantics == "current_tip_v1" && !b.NonreproductionProof {
			return "unknown", "BaseCICleared has no authentic exact-commit recheck coverage."
		}
		return "met", "the explicitly recorded failure did not reproduce; this is a disposition, not a waiver."
	}
	if b.Conclusion == "cancelled" || b.Conclusion == "errored" || b.Conclusion == "" {
		return "unknown", "the base run supplied no verdict."
	}
	return "unmet", "base CI has no accepted successful or non-reproduction disposition."
}

var healthRCTag = regexp.MustCompile("^v[0-9]+[.][0-9]+[.][0-9]+-rc[.][0-9]+$")

func legacyRef(ref string) bool {
	ref = strings.TrimPrefix(strings.TrimPrefix(ref, "refs/heads/"), "refs/tags/")
	return ref == "main" || ref == "master" || healthRCTag.MatchString(ref)
}
func failedJob(c string) bool {
	return slices.Contains([]string{"failure", "timed_out", "startup_failure"}, strings.ToLower(c))
}
func skippedRun(jobs []HealthJob) bool {
	skipped := false
	for _, j := range jobs {
		if failedJob(j.Conclusion) {
			return false
		}
		if strings.EqualFold(strings.TrimSpace(j.Name), "idle-gate") {
			continue
		}
		if !strings.EqualFold(j.Conclusion, "skipped") {
			return false
		}
		skipped = true
	}
	return skipped
}
func cancelledUnjudged(jobs []HealthJob) bool {
	cancelled := false
	for _, j := range jobs {
		if failedJob(j.Conclusion) {
			return false
		}
		cancelled = cancelled || strings.EqualFold(j.Conclusion, "cancelled")
	}
	return cancelled
}
func nightlyHealth(c NightlyHealthConfig, subject Subject, h HealthFacts, now time.Time) (string, string, time.Time) {
	since, _ := time.Parse(time.RFC3339Nano, c.Since)
	var selected *WorkflowHealthRun
	if c.Semantics == "legacy_500_v1" {
		byRun := map[int64][]HealthJob{}
		order := []int64{}
		for _, j := range h.LegacyJobs {
			if j.At.Before(since) || !strings.EqualFold(j.Workflow, c.Workflow) || j.RunID == 0 || !legacyRef(j.Ref) {
				continue
			}
			if _, ok := byRun[j.RunID]; !ok {
				order = append(order, j.RunID)
			}
			byRun[j.RunID] = append(byRun[j.RunID], j)
		}
		for _, id := range order {
			jobs := byRun[id]
			if skippedRun(jobs) || cancelledUnjudged(jobs) {
				continue
			}
			for _, j := range jobs {
				if !slices.Contains([]string{"success", "neutral", "skipped"}, strings.ToLower(j.Conclusion)) {
					return "unmet", "a job in the newest eligible legacy run is not green (newest 500 stored rows; provenance/completeness is weaker).", time.Time{}
				}
			}
			return "met", "newest eligible legacy run is green (newest 500 stored rows; no current-window or complete-source proof).", time.Time{}
		}
		return "pending", "no eligible judged legacy run since declaration in the newest 500 rows.", time.Time{}
	}
	if c.Semantics == "legacy_verified_push_v1" && h.LegacyVerifiedDisposition != nil {
		if *h.LegacyVerifiedDisposition {
			return "met", "the legacy base collector recorded a verified-push goal disposition; exact run provenance is not claimed by this legacy reading.", time.Time{}
		}
		return "pending", "the legacy goal awaits its verified default-branch push disposition.", time.Time{}
	}
	selected = selectNightlyRun(c, subject, h, now)
	if selected == nil {
		return "unknown", "no authenticated eligible run in the declared time window; expired evidence cannot establish health.", time.Time{}
	}
	run := selected
	if run.CompletedAt.IsZero() || run.CompletedAt.After(now) || run.StartedAt.IsZero() || run.StartedAt.After(run.CompletedAt) {
		return "unknown", "workflow run time provenance is missing or inconsistent.", time.Time{}
	}
	expiry := time.Time{}
	if c.Semantics == "window_v1" {
		expiry = run.CompletedAt.Add(time.Duration(c.WindowSeconds) * time.Second)
		if !now.Before(expiry) {
			return "unknown", "the newest eligible run expired.", expiry
		}
	}
	if !run.Complete || len(run.Jobs) == 0 || run.ID == 0 || run.Attempt < 1 || run.Path == "" || run.Commit == "" || run.HeadRepository != run.Repository || run.Source != "github" || !slices.Contains([]string{"push", "schedule", "workflow_dispatch"}, run.Event) {
		return "unknown", "workflow issuer/source/run/job provenance or complete job collection is missing.", expiry
	}
	// A complete green job listing does not override the workflow's own
	// verdict (for example a workflow-level failure or cancellation). Older
	// collector records without that verdict cannot establish current health.
	if failedJob(run.Conclusion) {
		return "unmet", "the newest eligible authenticated workflow run failed.", expiry
	}
	switch run.Conclusion {
	case "success", "neutral", "cancelled", "skipped":
	default:
		return "unknown", "the workflow run conclusion is missing or supplies no accepted verdict.", expiry
	}
	idleOnly := len(run.Jobs) > 0
	for _, j := range run.Jobs {
		if !strings.EqualFold(strings.TrimSpace(j.Name), "idle-gate") {
			idleOnly = false
		}
	}
	if idleOnly || skippedRun(run.Jobs) || cancelledUnjudged(run.Jobs) {
		return "pending", "the newest run was idle-skipped or cancelled without a verdict.", expiry
	}
	seen := map[string]bool{}
	pending := false
	for _, j := range run.Jobs {
		if j.ID == "" || seen[j.ID] || j.RunID != run.ID || j.Attempt != run.Attempt || j.Workflow != run.Workflow || j.Ref != run.Ref || j.Commit != run.Commit || j.Source != run.Source || j.App != "github-actions" {
			return "unknown", "job provenance does not match the exact workflow run attempt.", expiry
		}
		seen[j.ID] = true
		if failedJob(j.Conclusion) {
			return "unmet", "the newest eligible authenticated run has a failing job.", expiry
		}
		if !slices.Contains([]string{"success", "neutral", "skipped"}, strings.ToLower(j.Conclusion)) {
			pending = true
		}
	}
	if run.Conclusion == "cancelled" || run.Conclusion == "skipped" {
		return "pending", "the newest workflow run was cancelled or skipped without a healthy verdict.", expiry
	}
	if pending {
		return "pending", "the newest eligible run has jobs without an accepted verdict.", expiry
	}
	return "met", "the newest eligible authenticated run has a complete green job set.", expiry
}

func selectNightlyRun(c NightlyHealthConfig, subject Subject, h HealthFacts, now time.Time) *WorkflowHealthRun {
	since, _ := time.Parse(time.RFC3339Nano, c.Since)
	var selected *WorkflowHealthRun
	start := since
	if c.Semantics == "window_v1" && now.Add(-time.Duration(c.WindowSeconds)*time.Second).After(start) {
		start = now.Add(-time.Duration(c.WindowSeconds) * time.Second)
	}
	latestAttempt := map[int64]int{}
	for _, run := range h.Runs {
		if run.Attempt > latestAttempt[run.ID] {
			latestAttempt[run.ID] = run.Attempt
		}
	}
	for i := range h.Runs {
		run := &h.Runs[i]
		if run.Attempt != latestAttempt[run.ID] {
			continue
		}
		if run.Workflow != c.Workflow || run.Repository != subject.Repository || run.Ref != subject.Ref {
			continue
		}
		if run.CompletedAt.IsZero() || run.CompletedAt.After(now) {
			selected = run
			break
		}
		if run.CompletedAt.Before(start) {
			continue
		}
		if c.Semantics == "legacy_verified_push_v1" && (!run.VerifiedDefaultPush || run.Event != "push") {
			continue
		}
		selected = run
		break
	}
	return selected
}
