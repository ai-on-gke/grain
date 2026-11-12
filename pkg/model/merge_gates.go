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

package model

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Merge gate identifiers shared by the orchestrator, the HTTP API, the CLI
// (`--override-gates`) and the web UI.
const (
	GateCI             = "ci"
	GateSecurityReview = "security-review"
	GateReview         = "review"
	GateApproval       = "approval"
)

// KnownOverrideGates is the set of gate identifiers `--override-gates` and the
// force-merge API accept. Prefix `trigger:` (e.g. `trigger:foo`) is also
// accepted for a named gating path trigger, and `review` covers configured and
// path-trigger code reviews.
//
// GateApproval is not among them: a person's approval is given by Submit,
// by someone the change's approvers name (approvals.go, IsApprover), and a
// forced merge has never stood in for it -- holding merge:force does not
// make its holder an approver of a sensitive path, a `human: required`
// trigger, or a change whose merge rules say `auto_merge: never`.
var KnownOverrideGates = []string{
	GateCI,
	GateSecurityReview,
	GateReview,
}

// MergeGateState is the current status of one merge gate on a task or PR.
type MergeGateState string

const (
	GateStatePassed     MergeGateState = "passed"
	GateStateRunning    MergeGateState = "running"
	GateStateFailing    MergeGateState = "failing"
	GateStateFindings   MergeGateState = "findings"
	GateStateNeeded     MergeGateState = "needed"
	GateStateOverridden MergeGateState = "overridden"
)

// Satisfied reports whether a gate in state s lets the pull request merge.
func (s MergeGateState) Satisfied() bool {
	return s == GateStatePassed || s == GateStateOverridden
}

// MergeGate describes one required gate on a change and its current state.
type MergeGate struct {
	ID         string         `json:"id"`
	Label      string         `json:"label"`
	State      MergeGateState `json:"state"`
	Detail     string         `json:"detail,omitempty"`
	ReviewTask string         `json:"reviewTask,omitempty"`
	// Tasks lists every review task the gate stands for, when it stands for
	// more than one: the code-review gate folds every gating trigger's
	// review (one per review template) and a configured Review: task into
	// one gate, and ReviewTask can name only one of them.
	Tasks       []string `json:"tasks,omitempty"`
	ApprovedBy  string   `json:"approvedBy,omitempty"`
	Overridable bool     `json:"overridable,omitempty"`
}

// ReviewFindingsRecord records that a required review left findings, open
// questions, or unmerged fixes on a task's pull request, blocking that review
// gate until a re-review passes after fixes or an operator overrides the gate
// under merge:force.
type ReviewFindingsRecord struct {
	Gate       string    `json:"gate"`
	ReviewTask string    `json:"reviewTask,omitempty"`
	Summary    string    `json:"summary"`
	HeadSHA    string    `json:"headSha,omitempty"`
	At         time.Time `json:"at"`
}

// Link encodes r as a LinkReviewFindings link.
func (r ReviewFindingsRecord) Link() Link {
	r.At = r.At.UTC().Truncate(time.Second)
	b, _ := json.Marshal(r)
	return Link{Kind: LinkReviewFindings, Target: string(b)}
}

// ParseReviewFindingsRecord decodes a LinkReviewFindings target.
func ParseReviewFindingsRecord(target string) (ReviewFindingsRecord, bool) {
	var r ReviewFindingsRecord
	if err := json.Unmarshal([]byte(target), &r); err != nil || strings.TrimSpace(r.Gate) == "" {
		return ReviewFindingsRecord{}, false
	}
	return r, true
}

// ReviewFindings returns every unresolved review-findings gate record on t.
func ReviewFindings(t Task) []ReviewFindingsRecord {
	var out []ReviewFindingsRecord
	for _, l := range t.Links {
		if l.Kind != LinkReviewFindings {
			continue
		}
		if r, ok := ParseReviewFindingsRecord(l.Target); ok {
			out = append(out, r)
		}
	}
	return out
}

// ReviewFindingsForGate returns the findings records for gate on t.
func ReviewFindingsForGate(t Task, gate string) []ReviewFindingsRecord {
	var out []ReviewFindingsRecord
	for _, r := range ReviewFindings(t) {
		if r.Gate == gate {
			out = append(out, r)
		}
	}
	return out
}

// HasReviewFindings reports whether any review gate on t is blocked on
// findings or questions.
func HasReviewFindings(t Task) bool {
	if slices.ContainsFunc(t.Links, func(l Link) bool {
		return l.Kind == LinkReviewFindings || l.Kind == LinkReviewQuestions
	}) {
		return true
	}
	return slices.ContainsFunc(HumanReviewReasons(t), func(r HumanReviewReason) bool {
		return r.Source == HumanReviewSecurityFindings
	})
}

// AddReviewFindings records r on t (replacing any earlier record from the same
// gate and review task) and turns t's auto-merge off.
func AddReviewFindings(t *Task, r ReviewFindingsRecord) {
	r.Gate = strings.TrimSpace(r.Gate)
	if r.Gate == "" {
		r.Gate = GateReview
	}
	r.Summary = strings.TrimSpace(r.Summary)
	if r.Summary == "" {
		r.Summary = "review left findings"
	}
	if r.At.IsZero() {
		r.At = time.Now().UTC()
	}
	t.Links = slices.DeleteFunc(t.Links, func(l Link) bool {
		if l.Kind != LinkReviewFindings {
			return false
		}
		existing, ok := ParseReviewFindingsRecord(l.Target)
		if !ok {
			return true
		}
		return existing.Gate == r.Gate && (r.ReviewTask == "" || existing.ReviewTask == r.ReviewTask || existing.ReviewTask == "")
	})
	t.AutoMerge = false
	t.Links = append(t.Links, r.Link())
}

// ClearReviewFindingsForGate removes any review-findings records for gate on t.
// When gate is GateSecurityReview, any HumanReviewSecurityFindings hold reason
// is removed alongside it.
func ClearReviewFindingsForGate(t *Task, gate string) {
	t.Links = slices.DeleteFunc(t.Links, func(l Link) bool {
		if l.Kind == LinkReviewFindings {
			r, ok := ParseReviewFindingsRecord(l.Target)
			return !ok || r.Gate == gate || (gate == GateReview && strings.HasPrefix(r.Gate, "trigger:"))
		}
		if gate == GateSecurityReview && l.Kind == LinkHumanReviewHold {
			r, ok := ParseHumanReviewReason(l.Target)
			return ok && r.Source == HumanReviewSecurityFindings
		}
		return false
	})
	if !HasReviewFindings(*t) {
		// Drop stale "review X left findings" awaits-person mark once no review
		// findings remain, so restoreAfterHolds can hand auto-merge back when
		// all gates have passed.
		t.Links = slices.DeleteFunc(t.Links, func(l Link) bool {
			return l.Kind == LinkAutoMergeAwaitsPerson && strings.HasSuffix(l.Target, "left findings")
		})
	}
}

// HasPendingReviewHold reports whether t carries an active review gate hold
// that prevents automatic merging even after a human's Submit: an unfinished
// security review, an unfinished gating path-trigger review task, or unresolved
// review findings or questions.
func HasPendingReviewHold(t Task) bool {
	if HasReviewFindings(t) {
		return true
	}
	if slices.ContainsFunc(t.Links, func(l Link) bool { return l.Kind == LinkSecurityHold }) {
		return true
	}
	if slices.ContainsFunc(t.Links, func(l Link) bool { return l.Kind == LinkPathTriggerHold }) &&
		slices.ContainsFunc(t.Links, func(l Link) bool { return l.Kind == LinkPathTriggerTask }) {
		return true
	}
	return false
}

// GateOverrideRecord records an operator's explicit force-merge override of
// specific merge gates for one commit SHA.
type GateOverrideRecord struct {
	Gates   []string  `json:"gates"`
	Skipped string    `json:"skipped,omitempty"`
	Reason  string    `json:"reason"`
	By      string    `json:"by"`
	HeadSHA string    `json:"headSha"`
	At      time.Time `json:"at"`
	// WithdrawnAt is set once the branch moved on from HeadSHA before the
	// forced merge happened (orchestrator.dropMovedForcedMerge). The
	// record stays on the task as the audit of who asked and why, but a
	// withdrawn override skips no gate.
	WithdrawnAt *time.Time `json:"withdrawnAt,omitempty"`
}

// Link encodes r as a LinkGateOverride link.
func (r GateOverrideRecord) Link() Link {
	r.At = r.At.UTC().Truncate(time.Second)
	b, _ := json.Marshal(r)
	return Link{Kind: LinkGateOverride, Target: string(b)}
}

// ParseGateOverrideRecord decodes a LinkGateOverride target.
func ParseGateOverrideRecord(target string) (GateOverrideRecord, bool) {
	var r GateOverrideRecord
	if err := json.Unmarshal([]byte(target), &r); err != nil || strings.TrimSpace(r.Reason) == "" {
		return GateOverrideRecord{}, false
	}
	return r, true
}

// GateOverrides returns every recorded gate override on t, oldest first.
func GateOverrides(t Task) []GateOverrideRecord {
	var out []GateOverrideRecord
	for _, l := range t.Links {
		if l.Kind != LinkGateOverride {
			continue
		}
		if r, ok := ParseGateOverrideRecord(l.Target); ok {
			out = append(out, r)
		}
	}
	return out
}

// LatestGateOverride returns the most recent GateOverrideRecord on t, if any
// and if it still stands: an override withdrawn because the branch moved on
// from the commit it was for (WithdrawnAt) skips nothing, and neither does
// any older one it replaced. GateOverrides still lists every record.
func LatestGateOverride(t Task) (GateOverrideRecord, bool) {
	all := GateOverrides(t)
	if len(all) == 0 || all[len(all)-1].WithdrawnAt != nil {
		return GateOverrideRecord{}, false
	}
	return all[len(all)-1], true
}

// WithdrawGateOverride marks t's standing gate override, if any, withdrawn
// at at, keeping it as an audit record. It reports whether it changed t.
func WithdrawGateOverride(t *Task, at time.Time) bool {
	for i := len(t.Links) - 1; i >= 0; i-- {
		if t.Links[i].Kind != LinkGateOverride {
			continue
		}
		r, ok := ParseGateOverrideRecord(t.Links[i].Target)
		if !ok {
			continue
		}
		if r.WithdrawnAt != nil {
			return false
		}
		w := at.UTC().Truncate(time.Second)
		r.WithdrawnAt = &w
		t.Links[i] = r.Link()
		return true
	}
	return false
}

// NormalizeOverrideGates parses and validates explicit gate names supplied via
// `--override-gates` or the force-merge API. Empty entries are ignored; an
// unrecognised gate name returns an error. No wildcard ("all", "*") is accepted:
// each skipped gate must be named explicitly.
func NormalizeOverrideGates(raw []string) ([]string, error) {
	var out []string
	for _, item := range raw {
		for _, part := range strings.Split(item, ",") {
			g := strings.ToLower(strings.TrimSpace(part))
			if g == "" {
				continue
			}
			if !isValidOverrideGate(g) {
				return nil, fmt.Errorf("unknown gate %q: name each skipped gate explicitly (%s)",
					g, strings.Join(KnownOverrideGates, ", "))
			}
			if !slices.Contains(out, g) {
				out = append(out, g)
			}
		}
	}
	return out, nil
}

func isValidOverrideGate(g string) bool {
	if slices.Contains(KnownOverrideGates, g) {
		return true
	}
	if name, ok := strings.CutPrefix(g, "trigger:"); ok && strings.TrimSpace(name) != "" {
		return true
	}
	return false
}

// GateOverriddenBy reports whether gateID is covered by the explicit override
// list gates.
func GateOverriddenBy(gateID string, gates []string) bool {
	if slices.Contains(gates, gateID) {
		return true
	}
	if strings.HasPrefix(gateID, "trigger:") && slices.Contains(gates, GateReview) {
		return true
	}
	return false
}

// ComputeMergeGates derives the ordered list of required merge gates and their
// current states for t's pull request from t, obs and optional targetStates
// (live states of linked review tasks). Returns nil when t has no pull request.
func ComputeMergeGates(t Task, obs *Observation, targetStates map[string]State) []MergeGate {
	if TaskPullRequest(t) == nil {
		return nil
	}
	var secHold, secTask, reviewTask, pathTriggerHold, humanOnlyHold, brokenRulesHold string
	var approvalMarker string
	var triggerTasks []string
	hasMergePolicyWithdrawn := false
	for _, l := range t.Links {
		switch l.Kind {
		case LinkSecurityHold:
			secHold = l.Target
		case LinkSecurityReviewTask:
			secTask = l.Target
		case LinkReviewTask:
			reviewTask = l.Target
		case LinkPathTriggerHold:
			pathTriggerHold = l.Target
		case LinkPathTriggerTask:
			triggerTasks = append(triggerTasks, l.Target)
		case LinkHumanOnlyHold:
			humanOnlyHold = l.Target
		case LinkBrokenRulesHold:
			brokenRulesHold = l.Target
		case LinkMergePolicyWithdrawn:
			hasMergePolicyWithdrawn = true
		case LinkHumanSubmitted:
			approvalMarker = l.Target
		case LinkHumanApproved:
			if approvalMarker == "" {
				approvalMarker = l.Target
			}
		}
	}

	ov, hasOv := LatestGateOverride(t)
	var ovGates []string
	if hasOv {
		ovGates = ov.Gates
	}

	var gates []MergeGate

	// 1. CI gate.
	ciGate := MergeGate{ID: GateCI, Label: "CI"}
	switch {
	case hasOv && GateOverriddenBy(GateCI, ovGates):
		ciGate.State = GateStateOverridden
		ciGate.Detail = formatOverrideDetail(ov)
	case obs != nil && obs.PrMergedAt != nil:
		ciGate.State = GateStatePassed
		ciGate.Detail = "passed"
	case obs != nil && obs.CI != nil:
		switch {
		case !obs.CI.Done():
			ciGate.State = GateStateRunning
			ciGate.Detail = "running"
			ciGate.Overridable = true
		case obs.CI.Conclusion == "success":
			ciGate.State = GateStatePassed
			ciGate.Detail = "passed"
		case obs.CI.Conclusion == "failure" || obs.CI.Conclusion == "error" || obs.CI.Conclusion == "timed_out":
			ciGate.State = GateStateFailing
			ciGate.Detail = firstGateText(obs.CI.Detail, obs.CI.Conclusion)
			ciGate.Overridable = true
		default:
			ciGate.State = GateStateRunning
			ciGate.Detail = firstGateText(obs.CI.Detail, "waiting for checks")
			ciGate.Overridable = true
		}
	case obs != nil && (obs.MergeQueueState == "repairing" || obs.MergeQueueState == "repair_needed" || obs.MergeQueueState == "repair_queued" || obs.MergeQueueState == "checks_unfixable" || obs.MergeQueueState == "merge_failed"):
		ciGate.State = GateStateFailing
		ciGate.Detail = firstGateText(obs.MergeQueueNote, "checks failing")
		ciGate.Overridable = true
	case obs != nil && (obs.MergeQueueState == "merging" || obs.MergeQueueState == "waiting_queue"):
		ciGate.State = GateStatePassed
		ciGate.Detail = "passed"
	default:
		ciGate.State = GateStateRunning
		if obs != nil && obs.MergeQueueNote != "" {
			ciGate.Detail = obs.MergeQueueNote
		} else {
			ciGate.Detail = "waiting for checks"
		}
		ciGate.Overridable = true
	}
	gates = append(gates, ciGate)

	// 2. Security review gate (when trust boundary is touched).
	secFindings := ReviewFindingsForGate(t, GateSecurityReview)
	var secHumanFinding string
	var humanHoldReasons []HumanReviewReason
	for _, r := range HumanReviewReasons(t) {
		if r.Source == HumanReviewSecurityFindings {
			if secHumanFinding == "" {
				secHumanFinding = r.Reason
			}
			if secTask == "" && r.ReviewTask != "" {
				secTask = r.ReviewTask
			}
		} else {
			humanHoldReasons = append(humanHoldReasons, r)
		}
	}
	if secHold != "" || secTask != "" || len(secFindings) > 0 || secHumanFinding != "" {
		g := MergeGate{ID: GateSecurityReview, Label: "Security review", ReviewTask: secTask}
		switch {
		case hasOv && GateOverriddenBy(GateSecurityReview, ovGates):
			g.State = GateStateOverridden
			g.Detail = formatOverrideDetail(ov)
		case len(secFindings) > 0 || secHumanFinding != "":
			g.State = GateStateFindings
			if len(secFindings) > 0 {
				g.Detail = secFindings[len(secFindings)-1].Summary
				if g.ReviewTask == "" {
					g.ReviewTask = secFindings[len(secFindings)-1].ReviewTask
				}
			} else {
				g.Detail = secHumanFinding
			}
			g.Overridable = true
		case secHold != "":
			g.State = GateStateRunning
			if secTask != "" {
				g.Detail = "running (task #" + secTask + ")"
			} else {
				g.Detail = "running"
			}
			g.Overridable = true
		default:
			g.State = GateStatePassed
			if secTask != "" {
				g.Detail = "passed (task #" + secTask + ")"
			} else {
				g.Detail = "passed"
			}
		}
		gates = append(gates, g)
	}

	// 3. Configured / trigger code review gate.
	var codeFindings []ReviewFindingsRecord
	for _, rf := range ReviewFindings(t) {
		if rf.Gate != GateSecurityReview {
			codeFindings = append(codeFindings, rf)
		}
	}
	q, hasQuestions := ReviewQuestionsOf(t)
	hasCodeQuestions := hasQuestions && (secTask == "" || q.ReviewTaskID != secTask)
	triggerPending := false
	var firstTriggerTask string
	var codeTasks, pendingTasks []string
	if reviewTask != "" {
		codeTasks = append(codeTasks, reviewTask)
		if isCodeReviewPending(t, reviewTask, targetStates) {
			pendingTasks = append(pendingTasks, reviewTask)
		}
	}
	for _, raw := range triggerTasks {
		_, tid, ok := strings.Cut(raw, "=")
		if !ok || tid == "" {
			continue
		}
		if firstTriggerTask == "" {
			firstTriggerTask = tid
		}
		if !slices.Contains(codeTasks, tid) {
			codeTasks = append(codeTasks, tid)
		}
		if pathTriggerHold == "" {
			continue
		}
		if targetStates == nil {
			triggerPending = true
		} else if st, exists := targetStates[tid]; !exists || (st != StateClosed && st != StateCompleted) {
			triggerPending = true
		} else {
			continue
		}
		if !slices.Contains(pendingTasks, tid) {
			pendingTasks = append(pendingTasks, tid)
		}
	}
	codeReviewTask := firstGateText(reviewTask, firstTriggerTask)
	codePending := triggerPending || isCodeReviewPending(t, reviewTask, targetStates)
	if t.ReviewTemplateID != "" || reviewTask != "" || len(triggerTasks) > 0 || len(codeFindings) > 0 || hasCodeQuestions {
		g := MergeGate{ID: GateReview, Label: "Code review", ReviewTask: codeReviewTask}
		if len(codeTasks) > 1 {
			g.Tasks = codeTasks
		}
		switch {
		case hasOv && GateOverriddenBy(GateReview, ovGates):
			g.State = GateStateOverridden
			g.Detail = formatOverrideDetail(ov)
		case len(codeFindings) > 0 || hasCodeQuestions:
			g.State = GateStateFindings
			if len(codeFindings) > 0 {
				last := codeFindings[len(codeFindings)-1]
				g.Detail = last.Summary
				if last.ReviewTask != "" {
					g.ReviewTask = last.ReviewTask
				}
			} else {
				g.Detail = "open review questions (task #" + q.ReviewTaskID + ")"
				g.ReviewTask = q.ReviewTaskID
			}
			g.Overridable = true
		case codePending:
			g.State = GateStateRunning
			// Name the reviews still running, not merely the first one
			// filed: with one per review template, that one may be done.
			if len(pendingTasks) > 0 {
				g.ReviewTask = pendingTasks[0]
			}
			if len(pendingTasks) > 1 {
				g.Detail = "running (tasks #" + strings.Join(pendingTasks, ", #") + ")"
			} else if g.ReviewTask != "" {
				g.Detail = "running (task #" + g.ReviewTask + ")"
			} else {
				g.Detail = "queued"
			}
			g.Overridable = true
		default:
			g.State = GateStatePassed
			if len(codeTasks) > 1 {
				g.Detail = "passed (tasks #" + strings.Join(codeTasks, ", #") + ")"
			} else if g.ReviewTask != "" {
				g.Detail = "passed (task #" + g.ReviewTask + ")"
			} else {
				g.Detail = "passed"
			}
		}
		gates = append(gates, g)
	}

	// 4. Human approval (Submit) gate.
	pendingApprovals := PendingApprovals(t)
	allApprovals := ApprovalRequirements(t)
	approvalNeeded := AutoMergeDeclined(t) || humanOnlyHold != "" || brokenRulesHold != "" ||
		len(humanHoldReasons) > 0 || len(pendingApprovals) > 0 || hasMergePolicyWithdrawn ||
		(pathTriggerHold == string(TriggerHumanRequired) && approvalMarker == "") ||
		(!t.AutoMerge && !HasPendingReviewHold(t) && approvalMarker == "" && (obs == nil || obs.PrMergedAt == nil))
	if approvalNeeded || approvalMarker != "" || len(allApprovals) > 0 {
		g := MergeGate{ID: GateApproval, Label: "Approval"}
		by := HumanSubmittedBy(approvalMarker)
		if by == "" {
			for _, ar := range allApprovals {
				if ar.ApprovedBy != "" {
					by = ar.ApprovedBy
					break
				}
			}
		}
		switch {
		case approvalNeeded:
			// Not overridable (KnownOverrideGates): only a Submit gives it.
			g.State = GateStateNeeded
			if len(pendingApprovals) > 0 {
				g.Detail = "needed from " + DescribeApprovers(pendingApprovals[0].Approvers)
			} else {
				g.Detail = "needed"
			}
		default:
			g.State = GateStatePassed
			g.ApprovedBy = by
			if by != "" {
				g.Detail = "given by " + by
			} else {
				g.Detail = "given"
			}
		}
		gates = append(gates, g)
	}

	return gates
}

func isCodeReviewPending(t Task, reviewTaskID string, targetStates map[string]State) bool {
	if reviewTaskID == "" {
		return t.ReviewTemplateID != ""
	}
	if targetStates == nil {
		return false
	}
	st, ok := targetStates[reviewTaskID]
	if !ok {
		return false
	}
	return st != StateClosed && st != StateCompleted
}

func formatOverrideDetail(ov GateOverrideRecord) string {
	who := strings.TrimSpace(ov.By)
	if who == "" {
		who = "operator"
	}
	return "overridden by " + who + ": " + ov.Reason
}

func firstGateText(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
