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

package change

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Stage S4 (changelists.md §5, G3-5): the review loop on a change that is
// not a pull request. A run answers a CL's review threads as drafts
// (Replier), which are published when the run ends (Publisher.PublishRun)
// or when a person presses Publish replies (Publisher.PublishReplies); a
// person mails the CL to its reviewers (Mailer). Like stage S3's, these
// are optional interfaces a Provider may implement; core asks for them
// with a type assertion and never names the edition that answers.

// The review states an Observation carries.
const (
	ReviewStateDraft            = "draft"
	ReviewStateInReview         = "in_review"
	ReviewStateChangesRequested = "changes_requested"
	ReviewStateNeedsApproval    = "needs_approval"
	ReviewStateApproved         = "approved"
)

// ReplyResult is one Reply or Resolve. Degraded is a provider with no
// write path to its forge's threads: nothing was drafted, and Text is
// what to post in the task's conversation instead, with Note saying why.
type ReplyResult struct {
	Thread   string `json:"thread"`
	Draft    string `json:"draft,omitempty"`
	Degraded bool   `json:"degraded,omitempty"`
	Text     string `json:"text,omitempty"`
	Note     string `json:"note,omitempty"`
}

// Replier is a Provider that answers reply_to_pull_request_comment and
// resolve_review_thread on its changes' threads (IDs as Threads gave
// them), as drafts attributed to attemptID.
type Replier interface {
	Reply(ctx context.Context, task model.Task, attemptID, threadID, body string) (ReplyResult, error)
	Resolve(ctx context.Context, task model.Task, attemptID, threadID string) (ReplyResult, error)
}

// PublishedCL is one CL's drafts as a publish left them: Reason says why
// any are still unpublished.
type PublishedCL struct {
	CL        string `json:"cl"`
	Drafts    int    `json:"drafts"`
	Published int    `json:"published"`
	Reason    string `json:"reason,omitempty"`
}

// PublishResult is a PublishRun or PublishReplies.
type PublishResult struct {
	CLs []PublishedCL `json:"cls"`
}

// Waiting is the drafts a publish left unpublished, with the first
// reason given: when there are any, the task offers Publish replies.
func (r PublishResult) Waiting() (int, string) {
	n, reason := 0, ""
	for _, c := range r.CLs {
		if left := c.Drafts - c.Published; left > 0 {
			n += left
			if reason == "" {
				reason = c.Reason
			}
		}
	}
	return n, reason
}

// Publisher is a Provider that publishes the drafts a task's runs left:
// one run's at its end, or all of them at a person's say-so.
type Publisher interface {
	PublishRun(ctx context.Context, task model.Task, attemptID string) (PublishResult, error)
	PublishReplies(ctx context.Context, task model.Task, by Person) (PublishResult, error)
}

// Mailer is a Provider that mails a change to reviewers at a person's
// say-so (the Mail button).
type Mailer interface {
	Mail(ctx context.Context, task model.Task, target string, reviewers, cc []string, by Person) (Result, error)
}

// ReviewFinding is one finding a review of a change left: a remark, or --
// Path and Line set -- one about that line of the change.
type ReviewFinding struct {
	Body string `json:"body"`
	Path string `json:"path,omitempty"`
	Line int    `json:"line,omitempty"`
}

// ReviewFiler is a Provider whose changes grain reviews with a task of
// its own, as it reviews a pull request: the review is filed holding the
// grant ReviewGrant returns -- a grant string, the reference and its
// with: arguments (read-only, model.ReviewOfArg naming reviewed, and the
// snapshot) -- for deployment, this grain's deployment id, and its
// findings are saved on the change as unpublished drafts.
type ReviewFiler interface {
	ReviewGrant(deployment string, reviewed model.Task, snapshot int) (string, error)
	SaveReviewFindings(ctx context.Context, reviewed model.Task, target string, findings []ReviewFinding) error
}

// ReviewGate is a Provider that decides which reviews a change still
// needs and whether it may be submitted: ReviewsMissing is the template
// IDs of the required reviews not yet cleared on target's current
// snapshot (model.LinkCLReviewCleared), and GateSubmittable is whether a
// Submit may go ahead, with why not.
type ReviewGate interface {
	ReviewsMissing(ctx context.Context, task model.Task, target string) ([]string, error)
	GateSubmittable(ctx context.Context, task model.Task, target string) (ok bool, reason string, err error)
}

// GateSubmit is the provider's ReviewGate answer for task's primary
// change. A provider that files reviews (ReviewFiler) without gating them
// itself is gated on the review the task names, which is the one grain
// files for it (orchestrator.SyncCLReviews): Submit waits until that
// review has cleared the snapshot last observed. Otherwise ok, with no
// reason: nothing gates it.
func GateSubmit(ctx context.Context, task model.Task) (bool, string, error) {
	p, target := ReviewOf(task)
	if p == nil || target == "" {
		return true, "", nil
	}
	g, ok := p.(ReviewGate)
	if !ok {
		if _, files := p.(ReviewFiler); files {
			ok, reason := taskReviewCleared(task, target)
			return ok, reason, nil
		}
		return true, "", nil
	}
	return g.GateSubmittable(ctx, task, target)
}

// taskReviewCleared is the gate on target where grain files the task's
// own review template (Task.ReviewTemplateID) and the provider does not
// gate: cleared on the snapshot last observed, or why not. A CL not yet
// observed at a snapshot is never reviewed, and is not held here (Submit
// is refused for want of an observation on its own).
func taskReviewCleared(task model.Task, target string) (bool, string) {
	if task.ReviewTemplateID == "" {
		return true, ""
	}
	obs, observed := LastObserved(target)
	if !observed || obs.Snapshot <= 0 {
		return true, ""
	}
	key := model.CLReviewTarget(target, obs.Snapshot, task.ReviewTemplateID)
	filed := false
	for _, l := range task.Links {
		if l.Target != key {
			continue
		}
		switch l.Kind {
		case model.LinkCLReviewCleared:
			return true, ""
		case model.LinkCLReviewFiled:
			filed = true
		}
	}
	if filed {
		return false, fmt.Sprintf("grain's review of snapshot %d has not passed yet", obs.Snapshot)
	}
	return false, fmt.Sprintf("grain has not reviewed snapshot %d yet", obs.Snapshot)
}

// ReviewOf is the provider answering for task's CL review loop and its
// primary change's target; nil when none is registered.
func ReviewOf(task model.Task) (Provider, string) {
	p, ok := Lookup(model.LinkChangelist)
	if !ok {
		return nil, Primary(task)
	}
	return p, Primary(task)
}

// ThreadsOf asks the provider for kind for target's new threads, unless
// the kind is blocked on expired credentials.
func ThreadsOf(ctx context.Context, task model.Task, kind model.LinkKind, target string, n Notifier, now time.Time) ([]Thread, error) {
	p, ok := Lookup(kind)
	if !ok {
		return nil, fmt.Errorf("no provider for %s links", kind)
	}
	if h := HealthOf(targetKind(p)); h.Blocked != "" {
		return nil, &CredentialsExpiredError{Kind: targetKind(p), Detail: "blocked since " + h.Since.UTC().Format(time.RFC3339)}
	}
	threads, err := p.Threads(ctx, task, target)
	NoteError(ctx, p, err, n, now)
	return threads, err
}

// Drafts waiting to be published, per task, as the last publish left
// them: the Publish replies button reads it. Memory only, like Observed:
// a restart forgets it until the next run ends or a person publishes.
var (
	draftsMu sync.RWMutex
	drafts   = map[string]PublishResult{}
)

// RememberPublish records r as taskID's latest publish.
func RememberPublish(taskID string, r PublishResult) {
	draftsMu.Lock()
	defer draftsMu.Unlock()
	if n, _ := r.Waiting(); n == 0 {
		delete(drafts, taskID)
		return
	}
	drafts[taskID] = r
}

// WaitingDrafts is how many drafts taskID's last publish left, and why.
func WaitingDrafts(taskID string) (int, string) {
	draftsMu.RLock()
	defer draftsMu.RUnlock()
	return drafts[taskID].Waiting()
}

// PublishRun publishes the drafts run attemptID left on task's CLs, when
// that run ends, and remembers what stayed unpublished for the Publish
// replies button. A provider without a Publisher, a task with no CL, or a
// kind blocked on expired credentials has nothing to publish.
func PublishRun(ctx context.Context, task model.Task, attemptID string) (PublishResult, error) {
	p, target := ReviewOf(task)
	if p == nil || target == "" {
		return PublishResult{}, nil
	}
	pub, ok := p.(Publisher)
	if !ok || HealthOf(targetKind(p)).Blocked != "" {
		return PublishResult{}, nil
	}
	res, err := pub.PublishRun(ctx, task, attemptID)
	if err != nil {
		return PublishResult{}, err
	}
	RememberPublish(task.ID, res)
	return res, nil
}
