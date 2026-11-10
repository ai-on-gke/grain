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
	"errors"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// The vocabulary of POST /api/pull-requests/adopt, POST
// /api/pull-requests/review and GET /api/pull-requests/status
// (external-cli spec #XC-28, #XC-29). It lives here, rather than in
// pkg/ui or pkg/orchestrator, because both speak it: pkg/ui answers the
// routes and pkg/orchestrator (takeover_adopt.go) does the work, and
// neither imports the other.

var (
	// ErrPullRequestConflict wraps a pull-request route refused because
	// of the pull request's own state: closed, merged, or already the
	// work of a task that is not a shepherd. The routes answer 409.
	ErrPullRequestConflict = errors.New("pull request conflict")
	// ErrPullRequestInvalid wraps a request the routes answer 400.
	ErrPullRequestInvalid = errors.New("invalid pull request request")
	// ErrPullRequestForbidden wraps a request whose requester may not do
	// what it asks: an unapproved request to upgrade an approved
	// review-only shepherd to finish. The routes answer 403.
	ErrPullRequestForbidden = errors.New("pull request request forbidden")
	// ErrPullRequestNotFound wraps a pull request GitHub answered 404 for:
	// there is no such pull request, or the deployment's credential
	// cannot see it. The routes answer 404 rather than a 500 that reads
	// as grain having failed.
	ErrPullRequestNotFound = errors.New("pull request not found")
)

// TakeoverMode is how far a shepherd task may go with the pull request it
// adopted.
type TakeoverMode string

const (
	// TakeoverReviewOnly runs grain's CI and reviews on the pull request,
	// holds its merge until they pass at its head, and submits it to the
	// merge queue -- but never pushes to it: findings are the author's to
	// fix. The default for `grain pr adopt`.
	TakeoverReviewOnly TakeoverMode = "review-only"
	// TakeoverFinish additionally lets the shepherd push fix commits onto
	// the pull request's branch (never force-pushing). What the GitHub
	// bot's "@<slug> take over" command has always filed.
	TakeoverFinish TakeoverMode = "finish"
)

// ParseTakeoverMode reads a mode as a request spells it; "" is
// TakeoverReviewOnly.
func ParseTakeoverMode(s string) (TakeoverMode, error) {
	switch TakeoverMode(strings.ToLower(strings.TrimSpace(s))) {
	case "", TakeoverReviewOnly:
		return TakeoverReviewOnly, nil
	case TakeoverFinish:
		return TakeoverFinish, nil
	}
	return "", fmt.Errorf("mode must be %q or %q, got %q", TakeoverReviewOnly, TakeoverFinish, s)
}

const (
	// LinkTakeoverMode records, on a shepherd task, its TakeoverMode (->
	// "review-only" or "finish"). Absent is TakeoverFinish: every
	// shepherd filed before modes existed could push.
	LinkTakeoverMode LinkKind = "takeover-mode"
	// LinkImplementsSpec records a specification a task's change
	// implements (-> a repository path, "design/spec/..."): from `grain
	// pr adopt --spec`, or an "Implements:" line in the pull request's
	// description. Reviews filed for the task carry it too, and are asked
	// to check the change against it. Provenance only; blocks nothing.
	LinkImplementsSpec LinkKind = "implements-spec"
	// LinkDesignDoc is LinkImplementsSpec for a design document (->
	// "design/design/..."), from --design or a "Design:" line.
	LinkDesignDoc LinkKind = "design-doc"
)

// ParsePullRequestArg reads a pull request the way the CLI and the
// pull-request routes accept one: "owner/name#n", or a pull request's
// GitHub URL ("https://github.com/owner/name/pull/n").
func ParsePullRequestArg(s string) (PullRequestRef, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return PullRequestRef{}, fmt.Errorf("a pull request is required (owner/name#n or its URL)")
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return PullRequestRef{}, fmt.Errorf("pull request URL %q: %w", s, err)
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 4 || (parts[2] != "pull" && parts[2] != "pulls") {
			return PullRequestRef{}, fmt.Errorf("pull request URL %q is not .../<owner>/<name>/pull/<n>", s)
		}
		n, err := strconv.Atoi(parts[3])
		if err != nil || n <= 0 {
			return PullRequestRef{}, fmt.Errorf("pull request URL %q: bad number %q", s, parts[3])
		}
		// The same repository check as owner/name#n, so a URL cannot name
		// what that form would refuse.
		repo, err := ParseRepo(parts[0] + "/" + parts[1])
		if err != nil {
			return PullRequestRef{}, fmt.Errorf("pull request URL %q: %w", s, err)
		}
		return PullRequestRef{Repo: repo, Number: n}, nil
	}
	ref, err := ParsePullRequestRef(s)
	if err != nil {
		return PullRequestRef{}, err
	}
	if ref.Number <= 0 {
		return PullRequestRef{}, fmt.Errorf("pull request ref %q: bad number", s)
	}
	return ref, nil
}

// CleanDesignPath checks a --spec or --design path: relative to the
// repository root, under design/, and not climbing out of it. "" is "".
func CleanDesignPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", nil
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return "", fmt.Errorf("%q must be a path relative to the repository root", p)
	}
	// The path is written into review and shepherd prompts outside any
	// external-content quoting, and a pull request's description can name
	// it, so it may hold nothing but a path: no spaces, backticks or
	// other text an author could steer an agent with.
	if len(p) > maxDesignPathLen {
		return "", fmt.Errorf("%q is longer than %d bytes", p, maxDesignPathLen)
	}
	for i := 0; i < len(p); i++ {
		if !designPathByte(p[i]) {
			return "", fmt.Errorf("%q may hold only letters, digits, '.', '_', '-' and '/'", p)
		}
	}
	clean := path.Clean(p)
	if clean != p || strings.HasPrefix(clean, "../") || clean == ".." || !strings.HasPrefix(clean, "design/") {
		return "", fmt.Errorf("%q must be a clean path under design/", p)
	}
	return clean, nil
}

const maxDesignPathLen = 256

func designPathByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '.' || c == '_' || c == '-' || c == '/'
}

// AdoptPullRequestInput is what the adopt route hands the orchestrator:
// the request as parsed, plus who asked and whether the filing is
// approved (decided by pkg/ui under #XC-17's proposal rules).
type AdoptPullRequestInput struct {
	PR     PullRequestRef
	Mode   TakeoverMode
	Spec   string
	Design string
	// Requester is who the shepherd is filed for: its Origin, and its
	// Approval when Approved (unless Approval overrides it, e.g. for
	// owner-trust).
	Requester Attribution
	// Approval, when non-nil and Approved is true, records who approved a
	// newly filed shepherd task instead of Requester (such as
	// OwnerTrustPrincipal on behalf of an external credential's owner).
	Approval *Attribution
	// Approved files the shepherd queued; false files it proposed, and
	// grain files nothing on the pull request until a person approves it.
	Approved bool
	// AllowAutoMerge permits turning AutoMerge on when adopting a trusted
	// collaborator's pull request. True only when the requester is an
	// unlimited human principal -- never an agent, external credential,
	// service account, delegated app, or limited role.
	AllowAutoMerge bool
	// CheckApprove, when non-nil, gates approving an already-stored
	// unapproved shepherd task in updateAdoptedShepherd under the same
	// role ceiling and approval rails as POST /api/tasks/{id}/approve.
	CheckApprove func(ctx context.Context, task Task) error
}

// PullRequestAdoption is POST /api/pull-requests/adopt's answer.
type PullRequestAdoption struct {
	TaskID string `json:"taskId"`
	State  string `json:"state"`
	Mode   string `json:"mode"`
	PR     string `json:"pr"`
	// Existing is true when an active shepherd already held the pull
	// request and was updated in place rather than a new one filed.
	Existing bool `json:"existing,omitempty"`
	// Upgraded is true when that shepherd went from review-only to finish.
	Upgraded  bool   `json:"upgraded,omitempty"`
	AutoMerge bool   `json:"autoMerge"`
	Spec      string `json:"spec,omitempty"`
	Design    string `json:"design,omitempty"`
}

// ReviewPullRequestInput is POST /api/pull-requests/review's request as
// the orchestrator reads it.
type ReviewPullRequestInput struct {
	PR PullRequestRef
	// Design asks the reviewer to check design-folder conventions
	// (design/spec/contributions/contributions.spec.md) and overlapping
	// designs under design/.
	Design    bool
	Spec      string
	Requester Attribution
	Approved  bool
}

// PullRequestReviewFiling is POST /api/pull-requests/review's answer.
type PullRequestReviewFiling struct {
	TaskID string `json:"taskId"`
	State  string `json:"state"`
	PR     string `json:"pr"`
	Design bool   `json:"design,omitempty"`
	Spec   string `json:"spec,omitempty"`
}

// PullRequestReport is GET /api/pull-requests/status's answer.
type PullRequestReport struct {
	PR    string `json:"pr"`
	Head  string `json:"head"`
	State string `json:"state,omitempty"`
	// Shepherd is the task that adopted the pull request, nil when none
	// has.
	Shepherd *PullRequestShepherd `json:"shepherd,omitempty"`
	// CI is the latest grain CI run for the head branch, nil when grain
	// has run none.
	CI      *PullRequestCI          `json:"ci,omitempty"`
	Reviews []PullRequestReviewInfo `json:"reviews"`
	// Threads is every inline review thread on the pull request, filled
	// only when asked for (--threads).
	Threads []PullRequestThread `json:"threads,omitempty"`
	// MergeQueue is "queued", "held: review", "held: ci", "merged" or
	// "not-queued".
	MergeQueue string `json:"mergeQueue"`
}

// PullRequestShepherd is the shepherd half of a PullRequestReport.
type PullRequestShepherd struct {
	TaskID string `json:"taskId"`
	Mode   string `json:"mode"`
	State  string `json:"state"`
	Spec   string `json:"spec,omitempty"`
	Design string `json:"design,omitempty"`
}

// PullRequestCI is the CI half of a PullRequestReport.
type PullRequestCI struct {
	RunID       string   `json:"runId"`
	Commit      string   `json:"commit,omitempty"`
	Status      string   `json:"status"`
	Conclusion  string   `json:"conclusion,omitempty"`
	FailingJobs []string `json:"failingJobs,omitempty"`
}

// PullRequestReviewInfo is one grain review of the pull request.
type PullRequestReviewInfo struct {
	TaskID string `json:"taskId"`
	// Kind is "code", "security" or "design".
	Kind string `json:"kind"`
	Head string `json:"head,omitempty"`
	// State is "pending", "passed" or "findings".
	State    string `json:"state"`
	Findings int    `json:"findings"`
	// UnresolvedThreads is how many of the pull request's inline threads
	// are unresolved, counted on the newest review only (GitHub does not
	// say which review a thread came from once it is replied to).
	UnresolvedThreads int `json:"unresolvedThreads"`
}

// PullRequestThread is one inline review thread.
type PullRequestThread struct {
	Path     string `json:"path"`
	Line     int    `json:"line,omitempty"`
	Author   string `json:"author,omitempty"`
	Body     string `json:"body"`
	Resolved bool   `json:"resolved"`
	Replies  int    `json:"replies,omitempty"`
}
