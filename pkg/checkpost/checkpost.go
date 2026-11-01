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

// Package checkpost is where CI hands in its verdict on a commit, and
// where whoever gates on that verdict comes to read it.
//
// It exists because grain polls. A merge queue that wants to know how CI
// ended asks GitHub, every thirty seconds, for every pull request it is
// watching -- 240 to 360 requests an hour each, against a budget of
// 5,000, and that is what put this deployment into rate limiting on
// 2026-09-08 (docs/github-api-rate-limits.md). The poll is the wrong
// shape rather than the wrong size: CI already knows when it finished,
// and asking it repeatedly whether it has is work nobody needs done.
//
// So the flow is inverted. A workflow's last job reports here once, and
// the queue reads the answer from here instead of from the forge that
// ran it.
//
// # Why this is its own binary
//
// Two reasons, and the second is the one that matters.
//
// It is the one thing in this deployment that has to be reachable from
// outside it. A GitHub Actions runner is on the public internet and the
// grain daemon is not -- it sits behind IAP, holds every credential this
// deployment has, and serves a UI that can approve, dispatch and merge.
// Putting a public endpoint on that process would mean the blast radius
// of a bug in the endpoint is everything.
//
// And it is the first piece of the forge to live outside the daemon
// (design/design/architecture.design.md). Nothing here knows what a task is, what a
// sandbox is, or what grain's store looks like. It holds no GitHub
// credential -- it never calls GitHub, only receives from it -- and the
// only secret it has is the one it verifies signatures with. That is
// what makes "run it somewhere else" a deployment decision rather than a
// refactor.
//
// # It is not transitional
//
// Nine of the twelve checks a pull request gets in this repository could
// move to grain's own CI (pkg/ci). The other three cannot: they build
// and push container images to ghcr.io, which needs a credential, and a
// CI job holding a credential is precisely the property that makes an
// in-repo workflow file safe to let every agent rewrite (.grain/README.md,
// and pkg/cli/ci_hostile_test.go, which asserts it). Those three stay
// on GitHub permanently, so something permanent has to hear about them.
// See docs/leaving-the-github-api.md.
//
// # What it is not
//
// Not a CI service: it runs nothing and knows nothing about jobs. Not a
// webhook endpoint: it receives a verdict a workflow chose to report,
// not an event stream, so there is nothing here to keep in step with
// somebody else's payload schema. Not a source of truth about anything
// except "this is what was reported, and when" -- a commit nobody
// reported on is absent, which every reader has to treat as silence
// rather than as a pass.
package checkpost

import (
	"fmt"
	"strings"
	"time"
)

// Verdict is one check's answer about one commit: everything a merge
// gate needs and nothing else.
//
// It is deliberately the same shape a caller can turn into a
// github.CheckRun without inventing anything -- name, an answer, and a
// page for a person to open -- because the queue that reads these
// already has one code path for judging checks and adding a second
// would be two ways to be wrong about a red build. There is no Status
// field for the same reason: a verdict is by definition something that
// has finished. Anything still running has not reported yet, and is
// absent here.
type Verdict struct {
	// Repo is "owner/name", the spelling every other part of grain uses
	// for a repository.
	Repo string `json:"repo"`
	// Commit is the full 40-character sha the checks ran against. Full,
	// not abbreviated: this is a lookup key, and an abbreviation is a
	// prefix match waiting to be written by somebody.
	Commit string `json:"commit"`
	// Name is the check's name, as a person would see it on the pull
	// request -- "go", "frontend", "sandbox guest image".
	Name string `json:"name"`
	// Conclusion is how it ended, in GitHub's own vocabulary, because
	// the reader's existing judgement is written against those words
	// (orchestrator's failingChecks): success, failure, cancelled,
	// timed_out, skipped, neutral, action_required, startup_failure.
	Conclusion string `json:"conclusion"`
	// URL is where a person goes to look, and is optional: a reader with
	// nothing here renders the name alone, exactly as it does for a
	// GitHub check run that carries no HTML url.
	URL string `json:"url,omitempty"`
	// Attempt is which run of the workflow this answer came from --
	// GitHub's run_attempt, 1 for the first run and one more for each
	// re-run. Optional: zero is "not said", and is ordered before every
	// attempt that was. It is what lets a re-run's green replace the
	// failure it was re-run to clear (Store.Put); without it, a failure
	// stands.
	Attempt int `json:"attempt,omitempty"`
	// ReportedAt is when this arrived, filled in by the server rather
	// than by the reporter -- a clock grain controls, so that pruning and
	// staleness are not something a caller can lie about.
	ReportedAt time.Time `json:"reported_at"`
}

// Conclusions a Verdict may carry. GitHub's set, unchanged, for the
// reason Verdict.Conclusion gives -- the reader's judgement is already
// written against these words, and a vocabulary of our own would mean
// translating at both ends and being wrong in the middle. Accepting one
// here is not a judgement on it: "action_required", like "cancelled",
// reaches forge.HealthFrom as a completed run with no verdict and holds
// the pull request PENDING rather than passing it.
var conclusions = map[string]bool{
	"success":         true,
	"failure":         true,
	"cancelled":       true,
	"timed_out":       true,
	"skipped":         true,
	"neutral":         true,
	"action_required": true,
	"startup_failure": true,
}

// maxFieldLen bounds every string a reporter sends. Nothing legitimate
// is near it -- a check name is a few words and a sha is forty
// characters -- and it is here because this is the one endpoint in this
// deployment an untrusted network can reach: a field with no ceiling is
// a way to fill a disk one request at a time.
const maxFieldLen = 512

// Validate reports what is wrong with v, or nil.
//
// Strict, and deliberately so. This is the boundary: everything past it
// is trusted to be a well-formed verdict, and a reader that has to
// re-check is a reader that will eventually forget to. A reporter that
// gets it wrong is told exactly which field, because the reporter is a
// shell script in somebody's workflow file and the only debugging it has
// is what this says back.
func (v Verdict) Validate() error {
	switch {
	case v.Repo == "":
		return &InvalidError{Field: "repo", Problem: "is required, as owner/name"}
	case strings.Count(v.Repo, "/") != 1, strings.HasPrefix(v.Repo, "/"), strings.HasSuffix(v.Repo, "/"):
		return &InvalidError{Field: "repo", Value: v.Repo, Problem: "must be owner/name"}
	case v.Commit == "":
		return &InvalidError{Field: "commit", Problem: "is required"}
	case !isFullSHA(v.Commit):
		return &InvalidError{Field: "commit", Value: v.Commit, Problem: "must be a full 40-character sha"}
	case v.Name == "":
		return &InvalidError{Field: "name", Problem: "is required"}
	case v.Conclusion == "":
		return &InvalidError{Field: "conclusion", Problem: "is required"}
	case !conclusions[v.Conclusion]:
		return &InvalidError{Field: "conclusion", Value: v.Conclusion, Problem: "is not one GitHub uses"}
	case v.Attempt < 0:
		return &InvalidError{Field: "attempt", Value: fmt.Sprint(v.Attempt), Problem: "must not be negative"}
	}
	for _, f := range []struct {
		name  string
		value string
	}{{"repo", v.Repo}, {"commit", v.Commit}, {"name", v.Name}, {"conclusion", v.Conclusion}, {"url", v.URL}} {
		if len(f.value) > maxFieldLen {
			return &InvalidError{Field: f.name, Problem: fmt.Sprintf("is longer than %d bytes", maxFieldLen)}
		}
	}
	return nil
}

// InvalidError is a verdict Validate refused: which field, and what is
// wrong with it.
type InvalidError struct {
	Field   string
	Problem string
	// Value is what the field held, when showing it helps; empty
	// otherwise.
	Value string
}

// Error names the field and quotes the value, for the reporter's own
// step output and the server's log.
func (e *InvalidError) Error() string {
	if e.Value == "" {
		return e.Field + " " + e.Problem
	}
	return fmt.Sprintf("%s %q %s", e.Field, e.Value, e.Problem)
}

// Summary names the field without repeating the value, for a response
// body: it tells the reporter what to fix without echoing what it sent.
func (e *InvalidError) Summary() string { return e.Field + " " + e.Problem }

// isFullSHA is the shape of a git object name written out in full.
func isFullSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return true
}

// key is how a commit's verdicts are looked up, and what one report
// may replace.
//
// Repo, commit and name together: a workflow that re-runs a failed job
// reports the same name about the same commit again. Whether that
// second answer replaces the first is Store.Put's decision (supersedes),
// not the arrival order's -- a later report that merely says "success"
// does not erase a failure on its own say-so (#2401).
type key struct {
	repo   string
	commit string
	name   string
}

func (v Verdict) key() key { return key{repo: v.Repo, commit: v.Commit, name: v.Name} }

// passing is whether a conclusion lets a merge gate through: the same
// three GitHub treats as not failing a required check.
func passing(conclusion string) bool {
	switch conclusion {
	case "success", "neutral", "skipped":
		return true
	}
	return false
}

// supersedes reports whether next may replace held, which was reported
// earlier for the same repo, commit and name.
//
// A later attempt always does: a re-run's answer is the true one, which
// is the rule GitHub applies to a re-run check. An earlier attempt never
// does: that is a report that arrived late, not a newer answer. Within
// one attempt -- including two reports that say nothing about attempts
// -- a failure is sticky: a passing answer cannot replace a failing one,
// while a failing answer replaces anything. Otherwise whichever of two
// contradictory reports landed second would decide what the merge queue
// sees, and a genuine failure could be quietly erased by a success
// nobody can tell apart from it (#2401).
func supersedes(next, held Verdict) bool {
	switch {
	case next.Attempt > held.Attempt:
		return true
	case next.Attempt < held.Attempt:
		return false
	}
	return !passing(next.Conclusion) || passing(held.Conclusion)
}

// Superseded is what Store.Put returns for a report it declined to
// record because what it already holds outranks it (supersedes).
type Superseded struct {
	Held Verdict
	Sent Verdict
}

func (e *Superseded) Error() string {
	return fmt.Sprintf("%s %s %s already has %s (attempt %d) on record; %s (attempt %d) does not replace it -- "+
		"a failure is only cleared by a report from a later run attempt",
		e.Held.Repo, e.Held.Commit, e.Held.Name, e.Held.Conclusion, e.Held.Attempt, e.Sent.Conclusion, e.Sent.Attempt)
}
