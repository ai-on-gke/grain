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
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type ChecksConfig struct {
	Mode      string   `json:"mode"`
	ExtraJobs []string `json:"extra_jobs"`
	Empty     string   `json:"empty"`
	Semantics string   `json:"semantics"`
}

// CheckAttempt is immutable source evidence. Identity includes the provider app
// and job identity; suite/attempt IDs belong in Version, never in a display name.
type CheckAttempt struct {
	LeafOrder    int64
	Attempt      int64
	Workflow     bool
	Identity     string
	Names        []string
	Head         string
	Version      string
	Order        int64
	State        string
	Conclusion   string
	LegacyBudget bool
}
type CheckSource struct {
	Kind     string
	Head     string
	Version  string
	Complete bool
	Error    bool
	Required []string
	All      bool
	Attempts []CheckAttempt
	// Verdict is required for Grain's full exact-head suite, independently of jobs.
	Verdict         string
	LegacyAggregate bool
}
type ChecksFacts struct {
	TaskID                string
	Subject               Subject
	Head                  string
	PolicyVersion         string
	PolicyComplete        bool
	Mode                  string
	ExplicitNone          bool
	Sources               []CheckSource
	LegacySources         []CheckSource
	ObservedAt            time.Time
	ValidUntil            time.Time
	LegacyEmptySettled    bool
	LegacyEmptyKnown      bool
	LegacyHealth          string
	ReadinessVersion      string
	RecordedChecksVersion string
	Preconditions         []string
	Gap                   string
}

func validateChecks(raw []byte) (json.RawMessage, error) {
	var c ChecksConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	if !slices.Contains([]string{"effective", "github", "grain", "both", "provider", "none"}, c.Mode) ||
		!slices.Contains([]string{"unknown", "explicit_none", "legacy_settled"}, c.Empty) ||
		!slices.Contains([]string{"current_v1", "legacy_ci_v1"}, c.Semantics) || c.ExtraJobs == nil || len(c.ExtraJobs) > 100 {
		return nil, fmt.Errorf("unsupported checks profile")
	}
	if c.Empty == "legacy_settled" && c.Semantics != "legacy_ci_v1" {
		return nil, fmt.Errorf("legacy empty policy requires explicit legacy import")
	}
	for i, n := range c.ExtraJobs {
		if strings.TrimSpace(n) == "" || utf8.RuneCountInString(n) > 256 || slices.Contains(c.ExtraJobs[:i], n) {
			return nil, fmt.Errorf("invalid or duplicate extra job")
		}
	}
	return json.Marshal(c)
}

func ChecksBinding(f ChecksFacts, legacy bool) Binding {
	c := ChecksConfig{Mode: "effective", ExtraJobs: []string{}, Empty: "unknown", Semantics: "current_v1"}
	id := "checks-" + Digest(f.Subject)[7:23]
	if legacy {
		c.Empty = "legacy_settled"
		c.Semantics = "legacy_ci_v1"
		id += "-legacy"
	}
	raw, _ := json.Marshal(c)
	return Binding{ID: id, Type: "checks", Version: 1, Purpose: "monitor", Subject: f.Subject, Config: raw, FreshnessSeconds: 60, Origin: Origin{Kind: "task", ID: f.TaskID, Revision: Implementation, Imported: legacy}}
}

// EvaluateChecks has no service clients. Source errors, incomplete collections,
// stale heads and absent policy are different from a failed test.
func EvaluateChecks(b Binding, f ChecksFacts, now time.Time, generation uint64) (r Result) {
	version := ChecksVersion(f)
	r = Result{BindingID: "task/" + f.TaskID + "/" + b.ID, Type: "checks", Version: 1, ContextDigest: Digest(struct {
		Binding Binding
		Version string
	}{b, version}), Status: "unknown", Summary: "CI evidence is incomplete.", Evidence: []Evidence{}, Dependencies: []Dependency{}, Blockers: []Blocker{}, Work: []struct{}{}, Waits: []Wait{}, Actions: []struct{}{}, ObservedAt: now, ValidUntil: f.ValidUntil}
	r.EvaluationID = "shadow-" + r.ContextDigest[7:]
	if b.Type != "checks" || b.Subject != f.Subject || f.Subject.Kind != "change" || f.Subject.ID == "" || f.Subject.Authority == "" || f.Subject.Repository == "" {
		r.Summary = "Change subject or scoped source identity is unavailable or mismatched."
		return r
	}
	head := f.Head
	dep := func(key, version string, complete bool) {
		if version == "" {
			version = "unavailable-" + Implementation
		}
		var dependencyHead *string
		if head != "" {
			dependencyHead = &head
		}
		r.Dependencies = append(r.Dependencies, Dependency{Key: key, Subject: f.Subject, Head: dependencyHead, Version: version, CollectionGeneration: version, ScopeDigest: Digest(f.Subject), Complete: complete, ObservedAt: f.ObservedAt, ValidUntil: f.ValidUntil, DirtyGeneration: generation, CoveredGeneration: generation})
	}
	dep("checks/policy", f.PolicyVersion, f.PolicyComplete)
	notes := []string{}
	defer func() {
		if len(notes) > 0 {
			pre := boundedCheckText(*r.HumanText, 1800) + " CI observations: "
			for i, n := range notes {
				if i >= 6 {
					pre += " Additional details omitted; complete collection versions remain recorded."
					break
				}
				pre += boundedCheckText(n, 220) + "; "
			}
			r.HumanText = &pre
		}
	}()
	pre := "Merge preconditions are separate from CI: " + strings.Join(f.Preconditions, "; ")
	if len(f.Preconditions) == 0 {
		pre = "CI success alone does not establish merge readiness. Review, approval, queue and executor preconditions remain authoritative."
	}
	r.HumanText = &pre
	raw, err := Validate(b.Type, b.Version, b.Config)
	if err != nil {
		r.Summary = "Unsupported or malformed checks configuration."
		return r
	}
	var c ChecksConfig
	_ = json.Unmarshal(raw, &c)
	defer func() {
		if f.Gap != "" {
			r.Summary += " " + f.Gap
		}
		if c.Semantics == "legacy_ci_v1" {
			for _, source := range f.Sources {
				if source.LegacyAggregate {
					r.Summary += " Uses the exact-head legacy CIStatus verdict, which does not prove the current complete job set."
					break
				}
			}
			r.Summary = "Legacy CI import (observation only): " + r.Summary
		}
	}()
	if c.Semantics == "legacy_ci_v1" && !b.Origin.Imported {
		r.Summary = "Legacy CI semantics require explicit import provenance."
		return r
	}
	if c.Semantics == "legacy_ci_v1" && f.LegacySources != nil {
		f.Sources = f.LegacySources
	}
	for _, s := range f.Sources {
		if s.Error {
			r.Status = "error"
			r.Summary = "CI source read failed; no current verdict is available."
			r.Error = &EvaluationError{Code: "unavailable", Message: "Scoped CI read failed.", Retryable: true}
			return r
		}
	}

	if f.Head == "" || !f.PolicyComplete || f.PolicyVersion == "" || !now.Before(f.ValidUntil) {
		r.Summary = "Current head, current policy or fresh evidence is unavailable."
		return r
	}
	mode := c.Mode
	if mode == "effective" {
		mode = f.Mode
	}
	if (f.Subject.ChangeKind == "cl" && mode != "provider") || (f.Subject.ChangeKind != "cl" && mode == "provider") {
		r.Summary = "CI source is not applicable to this change kind."
		return r
	}
	if mode != f.Mode {
		r.Summary = "Configured source cannot replace or reduce effective CI policy."
		return r
	}
	r.Evidence = append(r.Evidence, Evidence{Kind: "policy", ID: "ci-policy", Version: f.PolicyVersion, Subject: f.Subject, Head: &head, Digest: f.PolicyVersion})
	if mode == "none" {
		if f.ExplicitNone && len(c.ExtraJobs) == 0 && len(f.Sources) == 0 {
			r.Status = "met"
			r.Summary = "Authorized explicit no-CI policy; no inherited jobs. This is not a passing CI run."
		} else {
			r.Summary = "No-CI selection lacks explicit policy or conflicts with inherited jobs."
		}
		return r
	}
	if !slices.Contains([]string{"github", "grain", "both", "provider"}, mode) {
		r.Summary = "Effective CI source is unknown."
		return r
	}
	kinds := []string{mode}
	if mode == "both" {
		kinds = []string{"github", "grain"}
	}
	statuses := []string{}
	for _, kind := range kinds {
		var source *CheckSource
		for i := range f.Sources {
			if f.Sources[i].Kind == kind {
				if source != nil {
					r.Summary = "Ambiguous CI source collection."
					return r
				}
				source = &f.Sources[i]
			}
		}
		if source == nil {
			statuses = append(statuses, "unknown")
			continue
		}
		s := *source
		key := "checks/" + kind
		if c.Semantics == "legacy_ci_v1" && s.LegacyAggregate {
			key += "/legacy-status"
		}
		dep(key, s.Version, s.Complete)
		if s.Error {
			statuses = append(statuses, "error")
			continue
		}
		if !s.Complete || s.Head != f.Head || s.Version == "" {
			statuses = append(statuses, "unknown")
			continue
		}
		r.Evidence = append(r.Evidence, Evidence{Kind: "check", ID: kind, Version: s.Version, Subject: f.Subject, Head: &head, Digest: s.Version})
		if c.Semantics == "legacy_ci_v1" && s.LegacyAggregate && len(c.ExtraJobs) == 0 {
			notes = append(notes, kind+": exact-head legacy CIStatus verdict "+s.Verdict+"; complete current jobs are not proved")
			statuses = append(statuses, checkState(kind, "completed", s.Verdict))
			continue
		}
		latest := map[string]CheckAttempt{}
		ambiguous := false
		for _, a := range s.Attempts {
			if a.Head != f.Head || a.Identity == "" || a.Version == "" {
				ambiguous = true
				continue
			}
			old, ok := latest[a.Identity]
			if !ok || a.Order > old.Order || (a.Order == old.Order && (a.Attempt > old.Attempt || (a.Attempt == old.Attempt && a.LeafOrder > old.LeafOrder))) {
				latest[a.Identity] = a
			} else if a.Order == old.Order && a.Attempt == old.Attempt && a.LeafOrder == old.LeafOrder && Digest(a) != Digest(old) {
				ambiguous = true
			}
		}
		if ambiguous {
			statuses = append(statuses, "unknown")
			continue
		}
		if kind == "github" && c.Semantics == "current_v1" && len(latest) > 0 {
			checkRows, onlyPositiveWorkflows := 0, true
			for _, a := range latest {
				if !a.Workflow {
					checkRows++
				}
				if checkState(kind, a.State, a.Conclusion) != "met" {
					onlyPositiveWorkflows = false
				}
			}
			if checkRows == 0 && onlyPositiveWorkflows {
				statuses = append(statuses, "unknown")
				notes = append(notes, "GitHub workflow verdicts contain no job checks; explicit no-CI policy is required instead of inferring success")
			}
		}
		required := slices.Clone(s.Required)
		for _, n := range c.ExtraJobs {
			if !slices.Contains(required, n) {
				required = append(required, n)
			}
		}
		selected := map[string]bool{}
		for _, n := range required {
			found := false
			for id, a := range latest {
				if n == id || slices.Contains(a.Names, n) {
					selected[id] = true
					found = true
				}
			}
			if !found {
				notes = append(notes, kind+": missing required identity "+n)
				statuses = append(statuses, "pending")
			}
		}
		notes = append(notes, fmt.Sprintf("%s: complete collection, %d latest check/workflow identities, %d required selectors", kind, len(latest), len(required)))
		if s.All {
			for id := range latest {
				selected[id] = true
			}
		}
		if kind == "grain" && len(latest) == 0 && s.Verdict == "success" {
			statuses = append(statuses, "unknown")
		}
		if kind == "grain" || kind == "provider" {
			statuses = append(statuses, checkState(kind, "completed", s.Verdict))
		}
		if len(latest) == 0 && len(required) == 0 && kind == "github" {
			if c.Empty == "legacy_settled" && !f.LegacyEmptyKnown {
				statuses = append(statuses, "unknown")
			} else if c.Empty == "legacy_settled" && f.LegacyEmptySettled {
				statuses = append(statuses, "met")
			} else if c.Empty == "legacy_settled" {
				statuses = append(statuses, "pending")
			} else {
				statuses = append(statuses, "unknown")
			}
		}
		ids := make([]string, 0, len(selected))
		for id := range selected {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			a := latest[id]
			status := checkState(kind, a.State, a.Conclusion)
			if status != "met" {
				notes = append(notes, kind+": "+id+" is "+a.State+"/"+a.Conclusion)
			}
			statuses = append(statuses, status)
		}
		if len(selected) == 0 && len(required) == 0 && kind == "github" && len(latest) > 0 {
			statuses = append(statuses, "unknown")
		}
	}
	r.Status = "met"
	for _, status := range []string{"unmet", "error", "unknown", "pending"} {
		if slices.Contains(statuses, status) {
			r.Status = status
			break
		}
	}
	if len(statuses) == 0 {
		r.Status = "unknown"
	}
	switch r.Status {
	case "met":
		r.Summary = "All applicable current-head CI requirements are satisfied."
	case "unmet":
		r.Summary = "Current-head CI has a failing required verdict."
	case "pending":
		r.Summary = "Current-head CI is pending, missing a required job, cancelled or awaiting approval."
	case "error":
		r.Summary = "CI source collection or execution is unavailable."
		r.Error = &EvaluationError{Code: "unavailable", Message: "CI source could not provide a verdict.", Retryable: true}
	default:
		r.Summary = "CI coverage, current policy or source state is unknown."
	}
	if r.Status != "met" {
		action := "Prediction only: inspect CI status/logs; run or retry CI under existing authorization."
		if r.Status == "unmet" {
			action = "Prediction only: fix the failing current-head jobs and push, or use the existing authorized recheck flow."
		}
		r.AgentText = &action
		r.Waits = append(r.Waits, Wait{Event: "change", Key: "change/" + Digest(f.Subject)[7:], Version: f.PolicyVersion, Deadline: f.ValidUntil, Owner: "forge_sync", OnTimeout: "escalate"})
		r.Waits = append(r.Waits, Wait{Event: "timer", Key: "checks/" + b.ID, Version: version, Deadline: f.ValidUntil, Owner: "ci", OnTimeout: "escalate"})
		for _, kind := range kinds {
			r.Waits = append(r.Waits, Wait{Event: "checks", Key: kind + "/" + Digest(f.Subject)[7:], Version: f.Head, Deadline: f.ValidUntil, Owner: "ci", OnTimeout: "escalate"})
		}
	} else {
		action := "Prediction only: automatic merge may proceed only if legacy review, approval, queue, overlap/recheck and executor preconditions permit it."
		if f.Subject.ChangeKind == "cl" {
			action = "Prediction only: use the existing person-authorized Submit flow after expected-snapshot, review and other submit preconditions pass."
		}
		r.AgentText = &action
	}
	return r
}

func checkState(kind, state, conclusion string) string {
	if state != "completed" {
		return "pending"
	}
	if kind == "provider" {
		switch conclusion {
		case "passed":
			return "met"
		case "failed":
			return "unmet"
		case "running", "not_run":
			return "pending"
		default:
			return "unknown"
		}
	}
	switch conclusion {
	case "success":
		return "met"
	case "neutral", "skipped":
		if kind == "github" {
			return "met"
		}
		return "pending"
	case "failure", "timed_out", "startup_failure":
		return "unmet"
	case "errored", "error":
		return "error"
	case "cancelled", "action_required", "stale", "":
		return "pending"
	default:
		return "unknown"
	}
}

// Heartbeat times do not create history. Freshness is checked independently.
func ChecksVersion(f ChecksFacts) string {
	f.ObservedAt = time.Time{}
	f.ValidUntil = time.Time{}
	return Digest(f)
}

func boundedCheckText(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit-3]) + "..."
}
