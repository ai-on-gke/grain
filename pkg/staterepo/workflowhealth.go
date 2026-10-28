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

package staterepo

// workflowhealth.go answers the one question the CI step installWorkflow
// writes cannot answer about itself: whether it ever runs.
//
// installWorkflow's whole story stops at the push. A deployment whose
// credential may not write workflows knows it and says so
// (WorkflowRefusedAt, format.go); a deployment whose credential may is
// told nothing more, and from every surface grain has -- the State pane,
// `grain state status`, the journal -- a repository whose check runs on
// every pull request and one whose check has never once started look
// exactly alike. The file is committed, the sync is clean, nothing is in
// error.
//
// They are not alike. A workflow run GitHub concludes before any job of
// it starts is a fact about the repository rather than about the change:
// Actions disabled or restricted for it, a runner it may not use, a
// workflow file GitHub cannot parse at that commit, or -- for a private
// repository -- an account with no Actions minutes left or a spending
// limit it has reached. The check then fails every pull request against
// the deployment's own settings, on every tree, forever, and it is a
// required-looking red X with no log under it: nobody reading the pull
// request can tell it apart from a dump that will not load, and no
// commit anybody pushes clears it. This deployment has watched two agent
// runs spend themselves proving that from the inside, one commit at a
// time.
//
// So grain looks. Config.WorkflowRuns is the seam -- cmd/grain has the
// GitHub credential and the REST client, this package has neither and
// should not grow them (Config.AnnounceResets, resets.go, is the same
// shape for the same reason) -- and what comes back is the recent
// history of the one workflow grain installed. The verdict is written
// into a marker in the git directory, and read back out by
// WorkflowStartupFailures for the State pane and `grain state status`,
// beside the refusal they already carry.
//
// What grain deliberately does not do with it is act. It does not delete
// the workflow to unblock the gate, and it does not stop installing it:
// the file is grain's only pre-merge validation of a change to what this
// deployment runs, every remedy for the condition belongs to whoever
// owns the GitHub account (raise the spending limit, allow Actions, add
// a self-hosted runner, or set "noWorkflow": true), and none of them is
// something grain can do or should decide. Removing its own gate on the
// strength of an inference from an API read is the one move here that
// can be wrong in the expensive direction. See
// docs/state-repo-check-startup-failures.md for the alternatives that
// were weighed against this one.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// WorkflowRun is one run of the check, as a Config.WorkflowRuns callback
// hands it over: GitHub's own lifecycle field, its conclusion once there
// is one, and the run's own page on github.com.
//
// Three strings rather than the github package's own type, so that this
// package goes on knowing nothing about GitHub's API -- the callback in
// cmd/grain converts. Status is "queued", "in_progress" or "completed",
// and Conclusion is empty until the last of those.
type WorkflowRun struct {
	Status     string
	Conclusion string
	URL        string
}

// StartupFailureConclusion is the conclusion GitHub gives a run that
// failed before any job of it began -- github.StartupFailure, spelled
// again here rather than imported, since a callback converting into
// WorkflowRun above is what decides which of GitHub's words reach this
// package.
const StartupFailureConclusion = "startup_failure"

// WorkflowRunsRead is how many of the check's recent runs a
// Config.WorkflowRuns callback should hand over.
//
// Enough that one green run among them is decisive -- the verdict below
// needs *every* finished run read to have died at startup, so a
// repository whose Actions have just been paid for stops being reported
// workflowHealthFile records what grain last saw of those runs.
//
// In the git directory, beside grain-workflow-refused and for the same
// reason: it is this host's own observation, made with this host's own
// credential, and committing it would push one deployment's view of a
// repository at every clone of it.
const workflowHealthFile = "grain-workflow-health"

// workflowHealthInterval is how often grain asks GitHub about those
// runs.
//
// The condition it is watching for changes when somebody pays an
// account bill, turns Actions back on, or adds a runner -- and then when
// the next pull request against this repository runs the check. An hour
// is short enough that an operator who has just fixed it does not sit in
// front of a pane still calling their repository broken, and long enough
// that this is 24 API reads a day rather than 2,880: the sync tick is
// thirty seconds, and nothing here is worth a request on each of them.
const workflowHealthInterval = time.Hour

// workflowHealth is the marker's contents: what grain read, when, and
// what it made of it.
//
// Both halves are kept for a reason. Finished is what the verdict is
// measured against, so that a repository whose check simply has not run
// yet -- a state repository nobody has opened a pull request against --
// is silence rather than a report. StartupFailures is how many of those
// died before a job started, and the verdict is the two being equal and
// non-zero. RunURL is the newest such run's own page, because GitHub's
// reason for concluding it that way is written there and nowhere this
// deployment can reach: every surface that reports this condition sends
// its reader to that page.
type workflowHealth struct {
	ObservedAt      time.Time `json:"observedAt"`
	Finished        int       `json:"finished"`
	StartupFailures int       `json:"startupFailures"`
	RunURL          string    `json:"runUrl,omitempty"`
}

// dead reports the verdict: every finished run of this check died before
// a job of it started, and there is at least one.
//
// "Finished", not "read": a run still queued says nothing either way,
// and a repository out of Actions minutes has none of those anyway. "All
// of them", not "the newest": one run that got a job started -- passed,
// failed, however it ended -- is a repository that can run this check,
// and whatever is wrong with it then is wrong with a tree rather than
// with the account.
func (h workflowHealth) dead() bool {
	return h.Finished > 0 && h.StartupFailures == h.Finished
}

// StartupFailures is what grain last saw of a check whose every run dies
// before it starts: when it looked, how many finished runs it read (all
// of them dead), and the newest one's own page.
//
// The page matters more than the count. GitHub's reason for concluding a
// run that way -- out of minutes, Actions disabled, a runner this
// repository may not use -- is on that page and is not in any API answer
// grain gets, so every surface reporting this condition can name the
// possibilities and none of them can name the cause. Sending a reader
// somewhere that does is the whole of what this carries.
type StartupFailures struct {
	At     time.Time
	Runs   int
	RunURL string
}

// WorkflowStartupFailures reports whether the check grain installed on
// this repository is one whose every run dies before a job of it starts,
// and what grain saw when it last looked.
//
// This is the other half of WorkflowRefusedAt. That one says the check
// was never installed; this one says it was installed and has never once
// worked -- which costs an operator exactly the same thing (nothing
// validates a change to what this deployment runs) while looking, from
// every surface, like a deployment in perfect health whose settings pull
// requests are all somehow broken.
//
// False whenever there is nothing to say: a deployment not offering the
// workflow at all (no remote, noWorkflow set), one whose repository has
// no such file, one grain has never been able to look at -- and, of
// course, every ordinary deployment, whose check runs.
func (r *Repo) WorkflowStartupFailures(ctx context.Context) (StartupFailures, bool) {
	if r.cfg.NoWorkflow || r.cfg.Remote == "" {
		return StartupFailures{}, false
	}
	if _, err := os.Stat(filepath.Join(r.cfg.Dir, filepath.FromSlash(WorkflowFile))); err != nil {
		return StartupFailures{}, false
	}
	health, ok := r.readWorkflowHealth(ctx)
	if !ok || !health.dead() {
		return StartupFailures{}, false
	}
	return StartupFailures{At: health.ObservedAt, Runs: health.Finished, RunURL: health.RunURL}, true
}

// observeWorkflowHealth asks GitHub how the check has been getting on,
// at most once an interval, and records what it hears.
//
// Called from Sync, where the repository is already in hand and a
// credential is already being used, and best effort throughout: a
// deployment must not stop exporting its settings because it could not
// find out whether something was watching them. Every failure here
// leaves the previous verdict in place -- which is the right answer for
// a GitHub that is briefly unreachable, and no worse than silence for
// one that is not.
func (r *Repo) observeWorkflowHealth(ctx context.Context) {
	if r.cfg.WorkflowRuns == nil || r.cfg.NoWorkflow || r.cfg.Remote == "" {
		return
	}
	// Nothing to ask about a file that is not there. The marker goes with
	// it, so that a merge which drops the workflow does not leave the
	// pane reporting the run history of a check this repository no longer
	// has -- installWorkflow is about to offer it again anyway, and what
	// happens to *those* runs is what the next observation is about.
	if _, err := os.Stat(filepath.Join(r.cfg.Dir, filepath.FromSlash(WorkflowFile))); err != nil {
		r.clearWorkflowHealth(ctx)
		return
	}
	if !r.workflowHealthDue(ctx, r.now()) {
		return
	}
	runs, err := r.cfg.WorkflowRuns(ctx, WorkflowFile)
	if err != nil {
		// Said once here rather than swallowed, because the case that
		// matters is a credential that may not read Actions at all: that
		// is a condition of its own, it will not fix itself, and the only
		// symptom of it otherwise is a pane that goes on saying nothing
		// about a check nobody is watching. The interval still applies --
		// this is one line an hour, not one every tick.
		log.Printf("staterepo: could not read how %s has been running on %s (%v), so grain cannot "+
			"tell whether pull requests against this deployment's own settings are being checked. "+
			"The credential needs read access to that repository's Actions.",
			WorkflowFile, r.cfg.Remote, err)
		return
	}
	health := workflowHealth{ObservedAt: r.now()}
	for _, run := range runs {
		if run.Status != "completed" {
			continue
		}
		health.Finished++
		if run.Conclusion != StartupFailureConclusion {
			continue
		}
		health.StartupFailures++
		// The newest of them, and the runs arrive newest first, so this
		// is the first one seen.
		if health.RunURL == "" {
			health.RunURL = run.URL
		}
	}
	if err := r.recordWorkflowHealth(ctx, health); err != nil {
		log.Printf("staterepo: could not record what %s has been doing on %s: %v",
			WorkflowFile, r.cfg.Remote, err)
		return
	}
	if !health.dead() {
		return
	}
	// One line whenever grain sees it, which is once an hour at most and
	// only while it is true. It is worth repeating rather than saying
	// once and going quiet: this is the condition under which every
	// settings change this deployment will ever be offered sits blocked
	// behind a red check that no commit can turn green, and the journal
	// is where the person who has not opened the pane is reading.
	//
	// What the last sentence offers depends on whether this deployment
	// posts a check of its own (postcheck.go), and only on that: the
	// marker decides what grain says here and never whether it posts. A
	// host that posts gives up GitHub's second opinion by setting
	// "noWorkflow" and keeps the validation, so telling it that the
	// remedy costs the validation would be telling it to leave a dead
	// workflow in place for a reason that is not true of it.
	remedy := "which unblocks those pull requests at the cost of the validation."
	if r.cfg.PostCheck {
		remedy = "which costs no validation on this deployment: it imports every open pull " +
			"request here itself and posts %q on it, so the red mark those pull requests " +
			"carry is this dead workflow rather than a verdict on the change."
		remedy = fmt.Sprintf(remedy, PostedCheckName)
	}
	log.Printf("staterepo: every one of the last %d finished runs of %s on %s was concluded by GitHub "+
		"before a job of it started, so the check that validates pull requests against this "+
		"deployment's own settings has never once passed and no commit can make it. That is a fact "+
		"about the repository rather than about any change: Actions disabled or restricted for it, a "+
		"runner it may not use, or -- for a private repository -- an account out of Actions minutes "+
		"or at its spending limit. GitHub's own reason is on %s. Raise the account's limit, give the "+
		"repository a runner it may use, or set \"noWorkflow\": true in %s to stop grain offering the "+
		"check at all, %s",
		health.Finished, WorkflowFile, r.cfg.Remote, health.RunURL, SettingsFileName, remedy)
}

// workflowHealthDue reports whether it is time to ask again. A marker
// that is missing, empty or unreadable means yes: the cost of asking is
// one API read, and the cost of never asking is the condition this file
// exists to report going unnoticed for the life of the deployment.
func (r *Repo) workflowHealthDue(ctx context.Context, now time.Time) bool {
	health, ok := r.readWorkflowHealth(ctx)
	if !ok || health.ObservedAt.IsZero() {
		return true
	}
	return !now.Before(health.ObservedAt.Add(workflowHealthInterval))
}

func (r *Repo) readWorkflowHealth(ctx context.Context) (workflowHealth, bool) {
	path, err := r.gitDirFile(ctx, workflowHealthFile)
	if err != nil {
		return workflowHealth{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return workflowHealth{}, false
	}
	var health workflowHealth
	if err := json.Unmarshal(data, &health); err != nil {
		return workflowHealth{}, false
	}
	return health, true
}

func (r *Repo) recordWorkflowHealth(ctx context.Context, health workflowHealth) error {
	path, err := r.gitDirFile(ctx, workflowHealthFile)
	if err != nil {
		return err
	}
	data, err := json.Marshal(health)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("staterepo: writing %s: %w", path, err)
	}
	return nil
}

// clearWorkflowHealth forgets an observation about a workflow this
// repository no longer has. Best effort and deliberately silent, like
// clearWorkflowRefused: what a marker left behind costs is one stale
// sentence until the next observation replaces it, and that is not worth
// failing a sync over.
func (r *Repo) clearWorkflowHealth(ctx context.Context) {
	path, err := r.gitDirFile(ctx, workflowHealthFile)
	if err != nil {
		return
	}
	_ = os.Remove(path)
}
