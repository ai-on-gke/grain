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
	"sort"
	"strings"
	"time"
)

type ReviewConfig struct {
	Category      string `json:"category"`
	RequirementID string `json:"requirement_id"`
	Semantics     string `json:"semantics"`
}
type ApprovalConfig struct {
	RequirementIDs []string `json:"requirement_ids"`
	Semantics      string   `json:"semantics"`
}

// ChangeFacts is a scoped, immutable read, not an enforcement context. A cached
// CI commit is deliberately not a current change head. Readers must distinguish
// complete local records from complete provider findings/diffs/authorization.
type ChangeFacts struct {
	Subject              Subject
	Head                 string
	PolicyVersion        string
	AuthorizationVersion string
	Complete             bool
	IncompleteReason     string
	Reviews              map[string]ReviewFacts
	Approvals            map[string]ApprovalFacts
}

// ReviewDisposition binds the output to the assignment and every version that
// can invalidate it. Authenticated means collector provenance, never a claimed
// author in a forge comment. Carry-forward needs a complete, base-owned diff
// proof for exactly this scope, policy and destination head.
type ReviewDisposition struct {
	ReviewID                                                              string
	Version, AssignmentVersion, Head, PolicyVersion, AuthorizationVersion string
	Reviewer                                                              string
	Authenticated, Eligible, Clean                                        bool
	CarryForwardHead, CarryForwardPolicy                                  string
	CarryForwardComplete, CarryForwardBaseOwned                           bool
}
type FindingsDisposition struct {
	RequirementID, Category, ReviewID, AssignmentVersion              string
	Version, ReviewVersion, Head, PolicyVersion, AuthorizationVersion string
	FindingsDigest, By, Reason                                        string
	Human, Authenticated, Eligible, SelfClear                         bool
}
type ReviewFacts struct {
	AssignedReviewID                             string
	Category, AssignmentVersion                  string
	AssignmentPresent, AssignmentReadable        bool
	Running, Failed                              bool
	CollectionsComplete, DiffComplete, BaseOwned bool
	Findings, Questions                          int
	FindingsDigest                               string
	// A complete collection includes linked dependency/fix outcomes. Deferred,
	// failed and closed-unmerged fixes cannot satisfy integration.
	FixesComplete              bool
	FixesPending, FixesFailed  int
	Disposition                *ReviewDisposition
	Acceptance                 *FindingsDisposition
	LegacyStatus, LegacyDetail string
	Evidence                   []Evidence
}
type ApprovalReceipt struct {
	Version, By, RenderedHead, Head, PolicyVersion, AuthorizationVersion string
	RequirementID                                                        string
	Human, Authenticated, Eligible, Revoked                              bool
}
type ApprovalFacts struct {
	EligibleIdentityKnown bool
	Receipt               *ApprovalReceipt
	LegacyDetail          string
	Evidence              []Evidence
}

// ChangeBindings uses the canonical change selector. IDs remain task-local;
// requirement IDs retain the category/assignment scope and are never collapsed
// to a single task-wide "review passed" flag.
func ChangeBindings(taskID string, c ChangeFacts) []Binding {
	out := []Binding{}
	add := func(id, typ string, config any, imported bool) {
		raw, _ := json.Marshal(config)
		out = append(out, Binding{ID: typ + "/" + Digest(id)[7:], Type: typ, Version: 1, Purpose: "monitor",
			Subject: c.Subject, Config: raw, FreshnessSeconds: 60,
			Origin: Origin{Kind: "task", ID: taskID, Revision: Implementation, Imported: imported}})
	}
	// Sorted keys make source replay and wire order deterministic.
	for _, id := range sortedKeys(c.Reviews) {
		r := c.Reviews[id]
		add("review/"+id, "review", ReviewConfig{r.Category, id, "current_head_v1"}, false)
		add("legacy-review/"+id, "review", ReviewConfig{r.Category, id, "legacy_completion_v1"}, true)
	}
	for _, id := range sortedKeys(c.Approvals) {
		add("approval/"+id, "approval", ApprovalConfig{[]string{id}, "exact_head_v1"}, false)
	}
	return out
}

func evaluateChange(b Binding, f Facts, now time.Time, generation uint64) (r Result) {
	digest := Digest(struct {
		Binding       Binding
		Source, Build string
	}{b, f.SourceVersion, Implementation})
	r = Result{EvaluationID: "shadow-" + digest[7:], BindingID: "task/" + f.TaskID + "/" + b.ID, Type: b.Type, Version: b.Version,
		ContextDigest: digest, Status: "unknown", Summary: "Scoped change evidence is unavailable.",
		Evidence: []Evidence{}, Dependencies: []Dependency{}, Blockers: []struct{}{}, Work: []struct{}{}, Waits: []Wait{}, Actions: []struct{}{},
		ObservedAt: now, ValidUntil: f.ValidUntil}
	r.Dependencies = []Dependency{{Key: "change/" + b.ID, Subject: b.Subject, Version: f.SourceVersion, CollectionGeneration: f.SourceVersion, ScopeDigest: Digest(b.Config), ObservedAt: f.ObservedAt, ValidUntil: f.ValidUntil, DirtyGeneration: generation, CoveredGeneration: generation}}
	defer func() {
		// The canonical summary is <=240 characters. Longer scope/legacy diagnosis
		// belongs in bounded inert guidance, not an expanded public wire contract.
		if len([]rune(r.Summary)) > 240 {
			detail := r.Summary
			r.Summary = string([]rune(detail)[:239]) + "…"
			if r.HumanText != nil {
				detail = *r.HumanText + "\n\n" + detail
			}
			if len([]rune(detail)) > 8192 {
				detail = string([]rune(detail)[:8191]) + "…"
			}
			r.HumanText = &detail
		}
	}()
	if _, err := Validate(b.Type, b.Version, b.Config); err != nil {
		r.Summary = "Unsupported or malformed gate configuration."
		return r
	}
	c := f.Change
	if c == nil || c.Subject != b.Subject || c.Subject.Kind != "change" {
		return r
	}
	head := c.Head
	var headRef *string
	if head != "" {
		headRef = &head
	}
	complete := c.Complete && c.Head != "" && c.PolicyVersion != "" && c.AuthorizationVersion != ""
	if b.Type == "review" {
		var cfg ReviewConfig
		_ = json.Unmarshal(b.Config, &cfg)
		scope, ok := c.Reviews[cfg.RequirementID]
		complete = complete && ok && scope.AssignmentPresent && scope.AssignmentReadable && scope.CollectionsComplete && scope.DiffComplete && scope.BaseOwned && scope.FixesComplete
		if cfg.Semantics == "legacy_completion_v1" {
			complete = c.Complete && scope.AssignmentReadable
		}
	}
	r.Dependencies[0] = Dependency{Key: "change/" + c.Subject.Repository + "/" + c.Subject.ID + "/" + b.ID, Subject: c.Subject, Head: headRef,
		Version: f.SourceVersion, CollectionGeneration: f.SourceVersion, ScopeDigest: Digest(b.Config), Complete: complete,
		ObservedAt: f.ObservedAt, ValidUntil: f.ValidUntil, DirtyGeneration: generation, CoveredGeneration: generation}
	if len(r.Dependencies[0].Key) > 220 {
		r.Dependencies[0].Key = "change/" + Digest([]any{c.Subject, b.ID})[7:]
	}
	for _, version := range []struct{ key, value string }{{"head", c.Head}, {"policy", c.PolicyVersion}, {"authorization", c.AuthorizationVersion}} {
		dep := r.Dependencies[0]
		dep.Key += "/" + version.key
		dep.Version = version.value
		dep.Complete = version.value != ""
		if dep.Version == "" {
			dep.Version = "unavailable:" + version.key
		}
		r.Dependencies = append(r.Dependencies, dep)
	}
	if !now.Before(f.ValidUntil) {
		r.Summary = "Evidence is stale; refresh required."
		return r
	}
	if b.Type == "review" {
		var cfg ReviewConfig
		_ = json.Unmarshal(b.Config, &cfg)
		s, ok := c.Reviews[cfg.RequirementID]
		if !ok || s.Category != cfg.Category {
			r.Summary = "Review assignment is missing or outside this category."
			return r
		}
		prefix := fmt.Sprintf("%s review %s: ", cfg.Category, cfg.RequirementID)
		r.Evidence = append(r.Evidence, s.Evidence...)
		agent := "Prediction only: use the existing review/reply flow, push fixes, integrate linked fixes, or obtain a clean subsequent review for this scope. No review is filed by this monitor."
		human := "Prediction only: an eligible person may use Accept findings with a reason for this exact review and head. Submit, receipt acknowledgment and thread resolution do not accept findings."
		if s.Findings == 0 && s.Questions == 0 {
			human = "Prediction only: no findings acceptance is proposed for this scope. A person's Submit and report receipt remain separate decisions."
		}
		if !s.AssignmentPresent || !s.AssignmentReadable {
			human = "Prediction only: verify assignment, access and current policy evidence before making a scoped disposition. Existing services own any review or human decision."
		}
		if s.Running {
			agent = "Prediction only: the assigned reviewer is active; existing scheduling and review services own its completion. No replacement review is requested."
			human = "Prediction only: no self-clear is proposed for an active review. Submit cannot finish the reviewer or accept its findings."
		}
		r.AgentText, r.HumanText = &agent, &human
		if cfg.Semantics == "legacy_completion_v1" {
			if !b.Origin.Imported || b.Purpose != "monitor" || b.Required || b.Action != nil {
				r.Summary = prefix + "Legacy completion is admitted only as an explicit imported shadow monitor."
				return r
			}
			if s.LegacyStatus == "met" || s.LegacyStatus == "pending" || s.LegacyStatus == "unmet" {
				r.Status = s.LegacyStatus
			}
			r.Summary = prefix + "Imported legacy predicate only: " + s.LegacyDetail + " This is not current-head approval."
			return r
		}
		if !c.Complete {
			r.Summary = prefix + c.IncompleteReason
			return r
		}
		if !s.AssignmentReadable || !s.AssignmentPresent || s.AssignmentVersion == "" {
			r.Summary = prefix + "Missing or inaccessible assignment; closed or missing review tasks cannot pass current mode."
			return r
		}
		if c.Head == "" || c.PolicyVersion == "" || c.AuthorizationVersion == "" {
			r.Summary = prefix + fmt.Sprintf("Current head, policy or authorization version is unavailable. Local active=%t, failed=%t, findings=%d, questions=%d, pending fixes=%d, failed fixes=%d. Legacy: %s", s.Running, s.Failed, s.Findings, s.Questions, s.FixesPending, s.FixesFailed, s.LegacyDetail)
			return r
		}
		if !s.CollectionsComplete || !s.DiffComplete || !s.BaseOwned || !s.FixesComplete {
			r.Dependencies[0].Complete = false
			r.Summary = prefix + "Complete current findings/questions, base-owned diff classification and linked fix outcomes are required; an empty local collection does not prove a clean review."
			return r
		}
		switch {
		case s.Running:
			r.Status = "pending"
			r.Summary = prefix + "Assigned reviewer is active; no self-clear or timeout release is predicted."
			return r
		case s.Failed:
			r.Status = "unmet"
			r.Summary = prefix + "Review failed; completion is not an acceptable disposition."
			return r
		case s.FixesFailed > 0:
			r.Status = "unmet"
			r.Summary = prefix + "Linked fixes failed or closed without integration."
			return r
		case s.FixesPending > 0:
			r.Status = "unmet"
			r.Summary = prefix + "Linked fixes remain unintegrated or deferred."
			return r
		}
		d := s.Disposition
		if d == nil || d.Version == "" || d.ReviewID == "" || d.ReviewID != s.AssignedReviewID || strings.TrimSpace(d.Reviewer) == "" || !d.Authenticated || !d.Eligible ||
			d.AssignmentVersion != s.AssignmentVersion || d.PolicyVersion != c.PolicyVersion || d.AuthorizationVersion != c.AuthorizationVersion {
			r.Summary = prefix + "No authenticated eligible review disposition for the current assignment and policy."
			return r
		}
		exact := d.Head != "" && d.Head == c.Head
		carried := d.Head != "" && d.CarryForwardHead == c.Head && d.CarryForwardPolicy == c.PolicyVersion && d.CarryForwardComplete && d.CarryForwardBaseOwned
		if !exact && !carried {
			r.Summary = prefix + "Review is for another head; approved exact-scope carry-forward proof is absent."
			return r
		}
		r.Evidence = append(r.Evidence, Evidence{Kind: "review", ID: d.ReviewID, Version: d.Version, Subject: c.Subject, Head: &head, Digest: Digest(d)})
		if s.Findings > 0 || s.Questions > 0 {
			a := s.Acceptance
			if a == nil || a.Version == "" || a.RequirementID != cfg.RequirementID || a.Category != cfg.Category || a.ReviewID != d.ReviewID || a.AssignmentVersion != s.AssignmentVersion || a.ReviewVersion != d.Version || a.Head != c.Head || a.PolicyVersion != c.PolicyVersion ||
				a.AuthorizationVersion != c.AuthorizationVersion || s.FindingsDigest == "" || a.FindingsDigest != s.FindingsDigest ||
				strings.TrimSpace(a.By) == "" || strings.TrimSpace(a.Reason) == "" || !a.Human || !a.Authenticated || !a.Eligible || a.SelfClear || a.By == d.Reviewer {
				r.Status = "unmet"
				r.Summary = prefix + "Current findings/questions remain; no eligible exact-scope human acceptance."
				return r
			}
			r.Evidence = append(r.Evidence, Evidence{Kind: "approval", ID: cfg.RequirementID, Version: a.Version, Subject: c.Subject, Head: &head, Digest: Digest(a)})
			r.Status = "met"
			r.Summary = prefix + "Current findings/questions explicitly accepted for this review version, head and policy only."
			return r
		}
		if !d.Clean {
			r.Status = "unmet"
			r.Summary = prefix + "Review delivered without a clean or accepted disposition."
			return r
		}
		r.Status = "met"
		r.Summary = prefix + "Authenticated clean review covers this exact head or validated carry-forward scope."
		return r
	}
	var cfg ApprovalConfig
	_ = json.Unmarshal(b.Config, &cfg)
	human := "Prediction only: an eligible person can use the existing Submit service for their named requirements. This monitor cannot grant approval. Submit does not finish reviews or accept security findings; AutoMerge is queue intent."
	r.HumanText = &human
	if !c.Complete || c.Head == "" || c.PolicyVersion == "" || c.AuthorizationVersion == "" {
		r.Summary = "Approval scope " + strings.Join(cfg.RequirementIDs, ", ") + ": current head, policy, authorization or complete collection is unavailable."
		for _, id := range cfg.RequirementIDs {
			if a, ok := c.Approvals[id]; ok {
				r.Summary += " Legacy " + id + ": " + a.LegacyDetail
			}
		}
		return r
	}
	pending := []string{}
	unknown := []string{}
	passed := []string{}
	for _, id := range cfg.RequirementIDs {
		a, ok := c.Approvals[id]
		if !ok || !a.EligibleIdentityKnown {
			unknown = append(unknown, id)
			continue
		}
		r.Evidence = append(r.Evidence, a.Evidence...)
		p := a.Receipt
		if p == nil {
			pending = append(pending, id)
			continue
		}
		if p.Version == "" || p.RequirementID != id || strings.TrimSpace(p.By) == "" || !p.Human || !p.Authenticated {
			unknown = append(unknown, id)
			continue
		}
		if !p.Eligible || p.Revoked || p.Head != c.Head || p.RenderedHead != c.Head || p.PolicyVersion != c.PolicyVersion || p.AuthorizationVersion != c.AuthorizationVersion {
			pending = append(pending, id)
			continue
		}
		r.Evidence = append(r.Evidence, Evidence{Kind: "approval", ID: id, Version: p.Version, Subject: c.Subject, Head: &head, Digest: Digest(p)})
		passed = append(passed, id)
	}
	r.Summary = fmt.Sprintf("Approval scopes: passed [%s]; pending [%s]; unknown [%s]. Submit is separate from review completion, acceptance and queue intent.", strings.Join(passed, ", "), strings.Join(pending, ", "), strings.Join(unknown, ", "))
	switch {
	case len(unknown) > 0:
		r.Status = "unknown"
	case len(pending) > 0:
		r.Status = "pending"
	default:
		r.Status = "met"
	}
	return r
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
