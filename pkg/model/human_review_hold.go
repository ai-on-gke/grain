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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// The human-review hold: one record for every way a change is handed to a
// person to decide rather than merged on its own say-so.
//
// It has three sources, and one shape:
//
//   - a review run asked for it (HumanReviewRequested): the reviewer
//     called request_human_review because the code is fine but a person
//     must decide something about it -- a product call, a risky
//     migration, a security-sensitive behaviour change, an ambiguous
//     brief. A finding would either be "fixed" by an agent or read as a
//     code defect, so this is how a reviewer says it instead;
//   - a review left findings on a change held for a security review
//     (HumanReviewSecurityFindings, orchestrator.dropSecurityHoldOnFindings);
//   - a change touches a trust-boundary path only a human may release
//     (HumanReviewHumanOnly, beside LinkHumanOnlyHold).
//
// Each reason is one LinkHumanReviewHold whose target is the reason as
// JSON (HumanReviewReason), so one hold collects every reason it was
// raised for, and the task page, the inbox and `grain list` read the same
// record. While any is present:
//
//   - the change's auto-merge is off, and putTask keeps it off against
//     any write that turns it back on without dropping the hold in the
//     same write (guardAutoMergeChange) -- a clean CI run, a later clean
//     review, a trigger settling, auto-submit, a push re-classified, none
//     of them is a person's decision;
//   - only a person's Submit (ui.Client.Submit) or an explicit release
//     (Store.ReleaseHumanReviewHold, refused for an agent) drops it, and
//     each records who did in the task's events.
//
// The existing security-hold semantics are kept beside it: the reasons a
// human-only path raised go with LinkHumanOnlyHold when a later head is
// classified and no longer touches that path (normalizeHumanReviewHold),
// exactly as that hold always did, and a clean security review still
// releases LinkSecurityHold -- which is a different, earlier hold.

// LinkHumanReviewHold is one reason a change waits for a person's decision
// (-> a HumanReviewReason as JSON). See this file's doc comment.
const LinkHumanReviewHold LinkKind = "human-review-hold"

// HumanReviewSource is what raised one reason of a human-review hold.
type HumanReviewSource string

const (
	// HumanReviewRequested is a review run's request_human_review call.
	HumanReviewRequested HumanReviewSource = "requested"
	// HumanReviewSecurityFindings is a review that left findings on a
	// change held for a security review.
	HumanReviewSecurityFindings HumanReviewSource = "security-findings"
	// HumanReviewHumanOnly is a change touching a trust-boundary path only
	// a human may release (LinkHumanOnlyHold).
	HumanReviewHumanOnly HumanReviewSource = "human-only"
)

// The categories a reason may name.
const (
	HumanReviewProductDecision = "product-decision"
	HumanReviewRisk            = "risk"
	HumanReviewSecurity        = "security"
	HumanReviewIntentUnclear   = "intent-unclear"
	HumanReviewOther           = "other"
)

// HumanReviewCategories is every category request_human_review accepts.
var HumanReviewCategories = []string{HumanReviewProductDecision, HumanReviewRisk, HumanReviewSecurity,
	HumanReviewIntentUnclear, HumanReviewOther}

// MaxHumanReviewReasonLength bounds a reason, in runes: it is a one-line
// summary shown in the inbox and on a banner. The details go elsewhere.
const MaxHumanReviewReasonLength = 300

// MaxHumanReviewDetailsLength bounds request_human_review's details.
const MaxHumanReviewDetailsLength = 20000

// MaxHumanReviewRequestsPerReview is how many distinct reasons one review
// task may add to the hold on the change it reviews. A review that keeps
// asking is collected into one hold rather than flooding it.
const MaxHumanReviewRequestsPerReview = 5

// HumanReviewReason is one reason a change waits for a person.
type HumanReviewReason struct {
	Source   HumanReviewSource `json:"source"`
	Category string            `json:"category,omitempty"`
	Reason   string            `json:"reason"`
	// ReviewTask and Run are the review that raised it, "" for a reason
	// grain raised itself (a human-only path).
	ReviewTask string    `json:"reviewTask,omitempty"`
	Run        string    `json:"run,omitempty"`
	At         time.Time `json:"at"`
	// AutoMerge is whether the change was merging itself, or held by one
	// of grain's own holds that would have let it, when this reason was
	// raised -- what a release may hand back.
	AutoMerge bool `json:"autoMerge,omitempty"`
}

// Link is r as the link that records it.
func (r HumanReviewReason) Link() Link {
	r.At = r.At.UTC().Truncate(time.Second)
	b, _ := json.Marshal(r)
	return Link{Kind: LinkHumanReviewHold, Target: string(b)}
}

// ParseHumanReviewReason reads one LinkHumanReviewHold target.
func ParseHumanReviewReason(target string) (HumanReviewReason, bool) {
	var r HumanReviewReason
	if err := json.Unmarshal([]byte(target), &r); err != nil || strings.TrimSpace(r.Reason) == "" {
		return HumanReviewReason{}, false
	}
	return r, true
}

// HumanReviewReasons is every reason t is held for a person's review, in
// the order they were raised.
func HumanReviewReasons(t Task) []HumanReviewReason {
	var out []HumanReviewReason
	for _, l := range t.Links {
		if l.Kind != LinkHumanReviewHold {
			continue
		}
		if r, ok := ParseHumanReviewReason(l.Target); ok {
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(a, b HumanReviewReason) int { return a.At.Compare(b.At) })
	return out
}

// HumanReviewHeld reports whether t is held for a person's review.
func HumanReviewHeld(t Task) bool {
	return slices.ContainsFunc(t.Links, func(l Link) bool { return l.Kind == LinkHumanReviewHold })
}

// HumanReviewSummary is t's hold's reasons on one line, "" when it has none.
func HumanReviewSummary(t Task) string {
	var parts []string
	for _, r := range HumanReviewReasons(t) {
		if !slices.Contains(parts, r.Reason) {
			parts = append(parts, r.Reason)
		}
	}
	return strings.Join(parts, "; ")
}

// ErrHumanReviewRateLimited is AddHumanReviewReason's refusal of a review
// that has already added MaxHumanReviewRequestsPerReview reasons.
var ErrHumanReviewRateLimited = errors.New("this review has already asked for human review " +
	fmt.Sprint(MaxHumanReviewRequestsPerReview) + " times; the hold collects those reasons, so put anything more in a finding or your closing note")

// ErrAgentCannotReleaseHumanReview refuses a release asked for by an
// agent: an agent may raise the hold, never release it.
var ErrAgentCannotReleaseHumanReview = errors.New("an agent cannot release a human-review hold: only a person's Submit or Release does")

// ErrFindingsRequireRereviewOrOverride refuses releasing review findings via
// Submit or ReleaseHumanReviewHold: findings require a clean re-review after
// fixes, a person's acceptance of them (AcceptReviewFindings), or an
// explicit merge:force gate override with a reason.
var ErrFindingsRequireRereviewOrOverride = errors.New("review findings cannot be cleared by Submit or Release hold: fix the findings for a clean re-review, or accept them as they are with Accept findings (grain accept-findings) and a reason")

// NormalizeHumanReviewReason trims r, bounds its reason and checks its
// category, filling in what is missing.
func NormalizeHumanReviewReason(r HumanReviewReason) (HumanReviewReason, error) {
	r.Reason = strings.Join(strings.Fields(r.Reason), " ")
	if r.Reason == "" {
		return r, errors.New("a reason is required: one short line saying what a person has to decide")
	}
	if utf8.RuneCountInString(r.Reason) > MaxHumanReviewReasonLength {
		runes := []rune(r.Reason)
		r.Reason = string(runes[:MaxHumanReviewReasonLength-1]) + "…"
	}
	r.Category = strings.TrimSpace(strings.ToLower(r.Category))
	if r.Category == "" {
		r.Category = HumanReviewOther
	}
	if !slices.Contains(HumanReviewCategories, r.Category) {
		return r, fmt.Errorf("category %q is not one of %s", r.Category, strings.Join(HumanReviewCategories, ", "))
	}
	if r.Source == "" {
		r.Source = HumanReviewRequested
	}
	if r.At.IsZero() {
		r.At = time.Now()
	}
	return r, nil
}

// AddHumanReviewReason puts t on a human-review hold for r, turning its
// auto-merge off. It reports whether r was new: the same reason from the
// same source and review is said once, and a review past
// MaxHumanReviewRequestsPerReview distinct reasons is refused
// (ErrHumanReviewRateLimited). A task already held -- by this hold or by
// a human-only one -- just collects the reason.
func AddHumanReviewReason(t *Task, r HumanReviewReason) (HumanReviewReason, bool, error) {
	r, err := NormalizeHumanReviewReason(r)
	if err != nil {
		return r, false, err
	}
	existing := HumanReviewReasons(*t)
	fromReview := 0
	for _, e := range existing {
		if e.Source == r.Source && e.ReviewTask == r.ReviewTask && strings.EqualFold(e.Reason, r.Reason) {
			return e, false, nil
		}
		if r.ReviewTask != "" && e.ReviewTask == r.ReviewTask && e.Source == r.Source {
			fromReview++
		}
	}
	if r.Source == HumanReviewRequested && fromReview >= MaxHumanReviewRequestsPerReview {
		return r, false, ErrHumanReviewRateLimited
	}
	if r.Source == HumanReviewHumanOnly {
		// One path-held reason at a time: a head classified again says
		// afresh which paths hold it.
		t.Links = slices.DeleteFunc(t.Links, func(l Link) bool {
			e, ok := ParseHumanReviewReason(l.Target)
			return l.Kind == LinkHumanReviewHold && ok && e.Source == HumanReviewHumanOnly
		})
	}
	if !r.AutoMerge {
		r.AutoMerge = t.AutoMerge || len(heldBy(t.Links)) > 0 ||
			slices.ContainsFunc(existing, func(e HumanReviewReason) bool { return e.AutoMerge })
	}
	t.AutoMerge = false
	r.At = r.At.UTC().Truncate(time.Second)
	t.Links = append(t.Links, r.Link())
	return r, true, nil
}

// DropHumanReviewHold ends t's human-approval review hold, for a person's
// Submit or release, and returns the reasons it released. Review findings
// (HumanReviewSecurityFindings) are a separate review gate and are never
// dropped here: clearing findings requires either a clean re-review after
// fixes or an explicit merge:force gate override.
func DropHumanReviewHold(t *Task) []HumanReviewReason {
	var released []HumanReviewReason
	for _, r := range HumanReviewReasons(*t) {
		if r.Source != HumanReviewSecurityFindings {
			released = append(released, r)
		}
	}
	t.Links = slices.DeleteFunc(t.Links, func(l Link) bool {
		if l.Kind == LinkHumanOnlyHold {
			return true
		}
		if l.Kind != LinkHumanReviewHold {
			return false
		}
		r, ok := ParseHumanReviewReason(l.Target)
		return !ok || r.Source != HumanReviewSecurityFindings
	})
	return released
}

// normalizeHumanReviewHold is putTask's: a human-only path's reasons stand
// only while LinkHumanOnlyHold does, so a later head classified clear of
// that path takes them with it -- the human-only hold's semantics, kept.
func normalizeHumanReviewHold(t *Task) {
	if slices.ContainsFunc(t.Links, func(l Link) bool { return l.Kind == LinkHumanOnlyHold }) {
		return
	}
	t.Links = slices.DeleteFunc(t.Links, func(l Link) bool {
		if l.Kind != LinkHumanReviewHold {
			return false
		}
		r, ok := ParseHumanReviewReason(l.Target)
		return ok && r.Source == HumanReviewHumanOnly
	})
}

// The task events a human-review hold records, and the timeline's names
// for them.
const (
	TaskEventHumanReviewRequested TaskEventKind = "human-review-requested"
	TaskEventHumanReviewReleased  TaskEventKind = "human-review-released"

	TransitionHumanReviewRequested State = "human_review_requested"
	TransitionHumanReviewReleased  State = "human_review_released"
)

// HumanReviewNotificationSubject is the notification a hold on taskID
// raises, for the owner per the usual channels; a release clears it.
func HumanReviewNotificationSubject(taskID string) string { return "human-review:" + taskID }

// humanReviewCommenter is the conversation author of a hold's note.
var humanReviewCommenter = Principal{Kind: PrincipalAutomation, ID: "human-review"}

// HumanReviewRequest is one call to RaiseHumanReviewHold.
type HumanReviewRequest struct {
	Reason HumanReviewReason
	// Details is markdown said on the held task's conversation.
	Details string
	// By is who raised it: the review task's agent, or grain.
	By Principal
}

// RaiseHumanReviewHold puts taskID on a human-review hold for req and,
// when the reason is new, records it: an event on the task's timeline
// attributed to req.By, a note in its conversation carrying the details,
// and a notification for its owner. It reports whether the reason was
// new.
func (s *Store) RaiseHumanReviewHold(ctx context.Context, taskID string, req HumanReviewRequest) (bool, error) {
	added := false
	var reason HumanReviewReason
	by := req.By
	if by.Kind == "" {
		by = GrainPrincipal
	}
	err := s.UpdateTask(WithAutoMergeChange(ctx, by, "held for a person's review"), taskID, func(t *Task) error {
		var err error
		reason, added, err = AddHumanReviewReason(t, req.Reason)
		return err
	})
	if err != nil || !added {
		return false, err
	}
	detail := "category " + reason.Category
	if reason.ReviewTask != "" {
		detail += "; review task " + reason.ReviewTask
	}
	if reason.Run != "" {
		detail += ", run " + reason.Run
	}
	if _, err := s.RecordTaskEvent(ctx, TaskEvent{TaskID: taskID, Kind: TaskEventHumanReviewRequested,
		At: reason.At, By: by, Reason: reason.Reason, Detail: detail}); err != nil {
		return true, err
	}
	var body strings.Builder
	fmt.Fprintf(&body, "**Held for a person's review** (%s): %s\n\n", reason.Category, reason.Reason)
	if reason.ReviewTask != "" {
		fmt.Fprintf(&body, "Raised by review [task %s](/tasks/%s)", reason.ReviewTask, reason.ReviewTask)
		if reason.Run != "" {
			fmt.Fprintf(&body, ", run %s", reason.Run)
		}
		body.WriteString(".\n\n")
	}
	if d := strings.TrimSpace(req.Details); d != "" {
		body.WriteString(d)
		body.WriteString("\n\n")
	}
	if reason.Source == HumanReviewSecurityFindings {
		body.WriteString("This change will not merge until the findings are addressed and a re-review passes, " +
			"or a person accepts them as they are with Accept findings (`grain accept-findings`, a reason required). " +
			"Pressing Submit records your approval only and does not clear review findings.")
	} else {
		body.WriteString("This change will not merge itself while the hold stands: a clean CI run, a later clean " +
			"review or a new push do not release it. Press Submit to approve it (merges once all review and CI " +
			"gates pass), or Release hold once you have decided.")
	}
	if _, err := s.AddComment(ctx, Comment{TaskID: taskID, Author: Attribution{Actor: humanReviewCommenter},
		Body: body.String(), CreatedAt: reason.At}); err != nil {
		return true, err
	}
	task, err := s.GetTask(ctx, taskID)
	if err != nil {
		return true, err
	}
	notifAction := "until somebody presses Submit or Release hold, and all required reviews pass."
	if reason.Source == HumanReviewSecurityFindings {
		notifAction = "until a re-review passes after fixes, or an operator overrides the review gate with a reason."
	}
	n := Notification{
		Subject:  HumanReviewNotificationSubject(taskID),
		Severity: NotificationWarning,
		Title:    fmt.Sprintf("Task %s needs a person's decision: %s", taskID, reason.Reason),
		Body: fmt.Sprintf("[Task #%s](/tasks/%s) is held for a person's review (%s): %s\n\nIt will not merge "+
			"itself %s", taskID, taskID, reason.Category, reason.Reason, notifAction),
		TaskID:     taskID,
		RaisedBy:   PrincipalString(by),
		RaisedAt:   reason.At,
		ClearsWhen: &NotificationClear{Kind: ClearsOnTask, Task: taskID, Until: "closed"},
	}
	if task != nil && task.Target != nil {
		n.Repo = task.Target.Owner + "/" + task.Target.Name
	}
	if _, err := s.RaiseNotification(ctx, n); err != nil {
		return true, err
	}
	return true, nil
}

// ReleaseHumanReviewHold ends taskID's human-review hold on a person's
// explicit say-so (by), recording who released it and how ("released",
// "submitted"). An agent is refused (ErrAgentCannotReleaseHumanReview).
// It returns the reasons the hold held; none when there was no hold.
func (s *Store) ReleaseHumanReviewHold(ctx context.Context, taskID string, by Principal, how string) ([]HumanReviewReason, error) {
	if by.Kind == PrincipalAgent || by.Kind == PrincipalAutomation {
		return nil, ErrAgentCannotReleaseHumanReview
	}
	var released []HumanReviewReason
	var onlyFindings bool
	if err := s.UpdateTask(WithAutoMergeChange(ctx, by, "human-review hold released"), taskID, func(t *Task) error {
		onlyFindings = false
		before := HumanReviewReasons(*t)
		released = DropHumanReviewHold(t)
		if len(before) > 0 && len(released) == 0 {
			onlyFindings = true
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if onlyFindings {
		return nil, ErrFindingsRequireRereviewOrOverride
	}
	if len(released) == 0 {
		return nil, nil
	}
	return released, s.recordHumanReviewRelease(ctx, taskID, by, how, released)
}

// RecordHumanReviewRelease records a release a caller made in its own
// write (DropHumanReviewHold inside a Submit), and clears the hold's
// notification.
func (s *Store) RecordHumanReviewRelease(ctx context.Context, taskID string, by Principal, how string, released []HumanReviewReason) error {
	if len(released) == 0 {
		return nil
	}
	return s.recordHumanReviewRelease(ctx, taskID, by, how, released)
}

func (s *Store) recordHumanReviewRelease(ctx context.Context, taskID string, by Principal, how string, released []HumanReviewReason) error {
	how = strings.TrimSpace(how)
	if how == "" {
		how = "released"
	}
	var reasons []string
	for _, r := range released {
		reasons = append(reasons, r.Reason)
	}
	if _, err := s.RecordTaskEvent(ctx, TaskEvent{TaskID: taskID, Kind: TaskEventHumanReviewReleased,
		By: by, Reason: how, Detail: strings.Join(reasons, "; ")}); err != nil {
		return err
	}
	_, err := s.ClearNotification(ctx, HumanReviewNotificationSubject(taskID), PrincipalString(by))
	return err
}
