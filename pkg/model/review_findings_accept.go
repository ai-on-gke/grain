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
	"slices"
	"strings"
	"time"
)

// Accepting a review's findings is the owner's decision to merge a change
// over what one review found, without fixing it and without overriding
// anything else: the step between Submit, which never clears findings, and
// a merge:force override, which sets every named gate aside.
//
// It is scoped as narrowly as the findings it answers: one review task, on
// one merge gate, at one head. It removes that review's LinkReviewFindings
// records for the gate, its LinkReviewQuestions (the questions were that
// review's), and -- for the security-review gate -- the
// HumanReviewSecurityFindings hold reasons that review raised. Nothing else:
// CI, other gates, other reviews' findings, a running security hold, a
// `human: required` trigger's approvers and a person's Submit all still
// stand. A later push is classified and reviewed as any push is, and
// findings a new review leaves need a new decision.
//
// Only an explicit acceptance counts. Answering the review's questions with
// "merge as-is", or resolving its threads, does not: a reply is free text
// the next run reads and carries out, and threads can be resolved by the
// change's own agent, so neither says on the record who decided, at which
// head, or why (docs/declarations.md#gating-reviews).

// LinkReviewFindingsAccepted records a person's acceptance of one review's
// findings on one gate at one head (-> ReviewFindingsAcceptance JSON).
// Written by ui.Client.AcceptReviewFindings, never by an agent; it is the
// audit record, and clears nothing by being there.
const LinkReviewFindingsAccepted LinkKind = "review-findings-accepted"

// The task event an acceptance records, and the timeline's name for it.
const (
	TaskEventReviewFindingsAccepted  TaskEventKind = "review-findings-accepted"
	TransitionReviewFindingsAccepted State         = "review_findings_accepted"
)

// MaxAcceptFindingsReasonLength bounds an acceptance's reason: a short
// line, like a force-merge's.
const MaxAcceptFindingsReasonLength = 500

// ReviewFindingsAcceptance is what LinkReviewFindingsAccepted records: who
// accepted which review's findings on which gate, why, at which head, and
// what the findings said when they did.
type ReviewFindingsAcceptance struct {
	Gate       string    `json:"gate"`
	ReviewTask string    `json:"reviewTask"`
	Reason     string    `json:"reason"`
	By         string    `json:"by"`
	HeadSHA    string    `json:"headSha,omitempty"`
	Findings   string    `json:"findings,omitempty"`
	At         time.Time `json:"at"`
}

// Link encodes a as a LinkReviewFindingsAccepted link.
func (a ReviewFindingsAcceptance) Link() Link {
	a.At = a.At.UTC().Truncate(time.Second)
	b, _ := json.Marshal(a)
	return Link{Kind: LinkReviewFindingsAccepted, Target: string(b)}
}

// ParseReviewFindingsAcceptance decodes a LinkReviewFindingsAccepted target.
func ParseReviewFindingsAcceptance(target string) (ReviewFindingsAcceptance, bool) {
	var a ReviewFindingsAcceptance
	if err := json.Unmarshal([]byte(target), &a); err != nil ||
		strings.TrimSpace(a.Gate) == "" || strings.TrimSpace(a.ReviewTask) == "" {
		return ReviewFindingsAcceptance{}, false
	}
	return a, true
}

// ReviewFindingsAcceptances returns every acceptance recorded on t, oldest
// first.
func ReviewFindingsAcceptances(t Task) []ReviewFindingsAcceptance {
	var out []ReviewFindingsAcceptance
	for _, l := range t.Links {
		if l.Kind != LinkReviewFindingsAccepted {
			continue
		}
		if a, ok := ParseReviewFindingsAcceptance(l.Target); ok {
			out = append(out, a)
		}
	}
	return out
}

// findingsGateCovers reports whether a findings record on recGate belongs
// to the merge gate gate as ComputeMergeGates shows it: the code-review
// gate ("review") lists the findings of every gating trigger review
// ("trigger:<name>") as well as its own.
func findingsGateCovers(gate, recGate string) bool {
	return recGate == gate || (gate == GateReview && strings.HasPrefix(recGate, "trigger:"))
}

// AcceptableReviewFindings returns the findings records on t that accepting
// reviewTask's findings on gate would clear, and whether that review's
// questions or security-findings hold reasons stand on gate too. ok is
// false when there is nothing of that review's to accept on that gate.
func AcceptableReviewFindings(t Task, gate, reviewTask string) (records []ReviewFindingsRecord, ok bool) {
	for _, r := range ReviewFindings(t) {
		if r.ReviewTask == reviewTask && findingsGateCovers(gate, r.Gate) {
			records = append(records, r)
		}
	}
	if len(records) > 0 {
		return records, true
	}
	if q, has := ReviewQuestionsOf(t); has && q.ReviewTaskID == reviewTask && questionsGate(t, reviewTask) == gate {
		return nil, true
	}
	if gate == GateSecurityReview {
		for _, r := range HumanReviewReasons(t) {
			if r.Source == HumanReviewSecurityFindings && r.ReviewTask == reviewTask {
				return nil, true
			}
		}
	}
	return nil, false
}

// questionsGate is the gate ComputeMergeGates lists reviewTask's open
// questions under: the security review's when it is t's security review,
// the code review's otherwise.
func questionsGate(t Task, reviewTask string) string {
	for _, l := range t.Links {
		if l.Kind == LinkSecurityReviewTask && l.Target == reviewTask {
			return GateSecurityReview
		}
	}
	return GateReview
}

// AcceptReviewFindings clears a.ReviewTask's findings on a.Gate from t and
// records a on it (see this file's doc comment). It reports whether
// anything was cleared; nothing is recorded when nothing was.
func AcceptReviewFindings(t *Task, a ReviewFindingsAcceptance) bool {
	if _, ok := AcceptableReviewFindings(*t, a.Gate, a.ReviewTask); !ok {
		return false
	}
	clearQuestions := false
	if q, has := ReviewQuestionsOf(*t); has && q.ReviewTaskID == a.ReviewTask && questionsGate(*t, a.ReviewTask) == a.Gate {
		clearQuestions = true
	}
	t.Links = slices.DeleteFunc(t.Links, func(l Link) bool {
		switch l.Kind {
		case LinkReviewFindings:
			r, ok := ParseReviewFindingsRecord(l.Target)
			return ok && r.ReviewTask == a.ReviewTask && findingsGateCovers(a.Gate, r.Gate)
		case LinkReviewQuestions:
			return clearQuestions
		case LinkHumanReviewHold:
			if a.Gate != GateSecurityReview {
				return false
			}
			r, ok := ParseHumanReviewReason(l.Target)
			return ok && r.Source == HumanReviewSecurityFindings && r.ReviewTask == a.ReviewTask
		}
		return false
	})
	if !HasReviewFindings(*t) {
		// The same stale "review X left findings" mark
		// ClearReviewFindingsForGate drops once no findings remain.
		t.Links = slices.DeleteFunc(t.Links, func(l Link) bool {
			return l.Kind == LinkAutoMergeAwaitsPerson && strings.HasSuffix(l.Target, "left findings")
		})
	}
	t.Links = append(t.Links, a.Link())
	return true
}
