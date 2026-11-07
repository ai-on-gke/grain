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

package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
)

const (
	mq = "qualification-mergequeue"
	cd = "qualification-ci-diagnostics"
	bt = "qualification-base-triage"
	ss = "qualification-system-smoke"
)

// agent is the scripted agent ending the newest attempt of item on rc
// the way outcome says.
func (w *world) agent(rc, sha, item, outcome string) {
	w.tick(5 * time.Minute)
	id := w.latest(rc, item)
	switch outcome {
	case "pass":
		w.finish(id, footer("PASS", rc, sha))
	case "fail":
		w.finish(id, footer("FAIL", rc, sha))
	case "infra":
		w.finish(id, footer("INFRA", rc, sha))
	case "pr":
		w.openPR(id)
		w.finish(id, footer("PASS", rc, sha))
	case "followup":
		w.propose(id, "main", "Fix a defect "+id+" found qualifying "+rc)
		w.finish(id, footer("PASS", rc, sha))
	case "scratch":
		branch := model.QualificationScratchBranch(id)
		if _, err := gitOut(w.bare, "update-ref", "refs/heads/"+branch, sha); err != nil {
			panic(err)
		}
		w.say("agent: %s left refs/heads/%s on the remote", id, branch)
		w.finish(id, footer("PASS", rc, sha))
	case "wrongcommit":
		w.finish(id, footer("PASS", rc, w.sha("v3.2.0")))
	default:
		panic(outcome)
	}
}

// stage1 cuts nothing: it is Actions running the nightly.yml run the
// train's dispatch queued on rc, to a pass, and Stage 2 starting off it.
func (w *world) stage1(rc string, outs string) {
	n := len(w.sim.Dispatches)
	w.actions("actions after the cut of "+rc, outs)
	w.dispatches(n)
	w.tick(3 * time.Hour)
	w.conclude(rc)
	w.tick(time.Minute)
	w.reconcile()
	w.showItems(rc)
}

func (w *world) item2and3() {
	w.section("item 2/3: a fix lands on main and is known-good; train cuts v3.3.0-rc.1")
	w.tick(3 * time.Hour)
	h1 := w.commit("fix(walkthrough): a fix after v3.2.0")
	w.green("main", h1)
	status, outs := w.train("tick 8", "FORCE=true")
	rc1 := "v3.3.0-rc.1"
	expect(status == 0 && outputHas(outs, "rc_tag="+rc1) && w.sha(rc1) == h1, "%s cut on %s (exit %d)", rc1, h1[:12], status)
	w.tags()
	w.stage1(rc1, outs)

	w.section("item 2: first attempts on " + rc1 + ": a pull request, a follow-up on main, an INFRA footer")
	w.agent(rc1, h1, mq, "pr")
	w.agent(rc1, h1, cd, "followup")
	w.agent(rc1, h1, bt, "infra")
	w.tick(time.Minute)
	w.reconcile()
	w.showItems(rc1)
	its := w.items(rc1)
	for _, item := range []string{mq, cd, bt} {
		got := len(its[model.RepoDeclaredID(model.DeclaredTemplate, w.repo, item)])
		expect(got == 2, "%s re-run once (%d attempts)", item, got)
	}
	expect(len(its[model.RepoDeclaredID(model.DeclaredTemplate, w.repo, ss)]) == 0, "the final item not started")
	w.notifications("after-rc1-attempt-1")

	w.section("item 2: second attempts: the same pull request and follow-up again; the infra re-run passes")
	w.agent(rc1, h1, mq, "pr")
	w.agent(rc1, h1, cd, "followup")
	w.agent(rc1, h1, bt, "pass")

	// Stage 1 is still busy on rc.1 (a manual re-run), and main's own
	// schedule runs nightly.yml on the same commit: only the rc's run may
	// be cancelled once rc.1 is dropped (item 6).
	w.sim.WorkflowRuns = append([]githubsim.WorkflowRun{
		{ID: w.nextRunID(), Name: "nightly", Path: ".github/workflows/nightly.yml", HeadBranch: rc1, HeadSHA: h1,
			Event: "workflow_dispatch", Status: "in_progress", RunAttempt: 1, CreatedAt: w.now},
	}, w.sim.WorkflowRuns...)
	w.sim.WorkflowRuns = append([]githubsim.WorkflowRun{
		{ID: w.nextRunID(), Name: "nightly", Path: ".github/workflows/nightly.yml", HeadBranch: "main", HeadSHA: h1,
			Event: "schedule", Status: "in_progress", RunAttempt: 1, CreatedAt: w.now},
	}, w.sim.WorkflowRuns...)
	rcRun, mainRun := w.sim.WorkflowRuns[1].ID, w.sim.WorkflowRuns[0].ID
	w.say("seeded nightly.yml run %d in progress on %s and run %d in progress on main, both at %s", rcRun, rc1, mainRun, h1[:12])

	w.section("item 3: " + rc1 + " is confirmed red: grain/release-qualification fails and pipeline.rc-failed is raised")
	w.tick(time.Minute)
	w.reconcile()
	w.checks("rc1-red", h1)
	w.notifications("rc1-red")
	n := w.openNotification(model.FamilyRCFailed, rc1)
	expect(n != nil && strings.Contains(n.Body, w.sha("v3.2.0")+".."+h1), "pipeline.rc-failed:%s names the range v3.2.0..%s", rc1, h1[:12])

	w.section("item 3: a fix lands on main, and the steward files [rc-blocker " + rc1 + "]; grain holds the next rc")
	w.tick(10 * time.Minute)
	h2 := w.commit("fix(walkthrough): the merge queue fix")
	w.green("main", h2)
	blocker := w.fileTask(model.RCBlockerMarker(rc1) + " fix the merge queue")
	w.tick(time.Minute)
	w.reconcile()
	w.checks("rc1-held", h1)

	w.section("item 3/6: the next train run drops " + rc1 + ", emits rc_held and cuts nothing; actions cancel only the rc's run")
	status, outs = w.train("tick 9")
	expect(status == 0 && outputHas(outs, "discarded_rc="+rc1) && strings.Contains(outs, "rc_held=") && !strings.Contains(outs, "rc_tag="),
		"discarded_rc=%s and rc_held, no rc_tag (exit %d)", rc1, status)
	w.tags()
	w.actions("actions after tick 9", outs)
	expect(len(w.sim.CancelledRuns) == 1 && w.sim.CancelledRuns[0] == rcRun, "cancelled exactly run %d (the rc's), not %d (main's): %v", rcRun, mainRun, w.sim.CancelledRuns)
	status, outs = w.train("tick 10 (still held)")
	expect(status == 0 && strings.Contains(outs, "rc_held=") && !strings.Contains(outs, "rc_tag="), "still held, nothing cut")

	w.section("item 3: the blocker closes; grain completes the hold; the following run cuts v3.3.0-rc.2")
	w.tick(10 * time.Minute)
	w.closeTask(blocker)
	w.reconcile()
	w.checks("rc1-hold-lifted", h1)
	status, outs = w.train("tick 11")
	rc2 := "v3.3.0-rc.2"
	expect(status == 0 && outputHas(outs, "rc_tag="+rc2) && w.sha(rc2) == h2, "%s cut on %s (exit %d)", rc2, h2[:12], status)
	w.tags()
	w.tick(time.Minute)
	w.reconcile()
	expect(w.openNotification(model.FamilyRCFailed, rc1) == nil, "pipeline.rc-failed:%s cleared once %s is cut", rc1, rc2)
	w.stage1(rc2, outs)

	w.section("item 2/3: " + rc2 + ": a footer naming another commit, and a leftover scratch branch, twice each")
	w.agent(rc2, h2, mq, "wrongcommit")
	w.agent(rc2, h2, cd, "scratch")
	w.agent(rc2, h2, bt, "pass")
	w.tick(time.Minute)
	w.reconcile()
	w.showItems(rc2)
	w.agent(rc2, h2, mq, "wrongcommit")
	w.agent(rc2, h2, cd, "scratch")
	w.tick(time.Minute)
	w.reconcile()
	w.checks("rc2-red", h2)
	n = w.openNotification(model.FamilyRCFailed, rc2)
	expect(n != nil, "pipeline.rc-failed:%s raised", rc2)

	w.section("item 3: no blocker; the train drops " + rc2 + " and converges on release/v3.3.0")
	status, outs = w.train("tick 12")
	w.say("tick 12 outputs: %s", strings.ReplaceAll(strings.TrimSpace(outs), "\n", " "))
	w.tags()
	rb := w.sha("refs/heads/release/v3.3.0")
	expect(rb == h2, "release/v3.3.0 made from %s's commit %s", rc2, h2[:12])
	w.tick(20 * time.Minute)
	// A fix cherry-picked onto the convergence branch, and release.yml
	// green on it.
	tree, _ := gitOut(w.bare, "rev-parse", h2+"^{tree}")
	h3, err := gitOut(w.bare, "commit-tree", tree, "-p", h2, "-m", "fix(walkthrough): cherry-picked onto release/v3.3.0")
	if err != nil {
		panic(err)
	}
	_, _ = gitOut(w.bare, "update-ref", "refs/heads/release/v3.3.0", h3)
	w.green("release/v3.3.0", h3)
	status, outs = w.train("tick 13")
	rc3 := "v3.3.0-rc.3"
	expect(status == 0 && outputHas(outs, "rc_tag="+rc3) && w.sha(rc3) == h3, "%s cut on release/v3.3.0 at %s (exit %d)", rc3, h3[:12], status)
	w.tags()
	w.stage1(rc3, outs)

	w.section("item 3: " + rc3 + " is red too: pipeline.release-escalated, and the train cuts no rc.4")
	w.agent(rc3, h3, mq, "fail")
	w.agent(rc3, h3, cd, "pass")
	w.agent(rc3, h3, bt, "pass")
	w.tick(time.Minute)
	w.reconcile()
	w.agent(rc3, h3, mq, "fail")
	w.tick(time.Minute)
	w.reconcile()
	w.checks("rc3-red", h3)
	w.notifications("rc3-red")
	expect(w.openNotification(model.FamilyReleaseEscalated, "v3.3.0") != nil, "pipeline.release-escalated:v3.3.0 raised")
	w.tick(10 * time.Minute)
	h4 := w.commit("fix(walkthrough): yet another fix on main")
	w.green("main", h4)
	status, outs = w.train("tick 14")
	w.say("tick 14 outputs: %s", strings.ReplaceAll(strings.TrimSpace(outs), "\n", " "))
	expect(status != 0 && outputHas(outs, "escalated_version=v3.3.0") && !strings.Contains(outs, "rc_tag="),
		"escalated_version=v3.3.0, exit %d, nothing cut", status)
	w.tags()
	n2 := len(w.sim.Dispatches)
	w.actions("actions after tick 14", outs)
	w.dispatches(n2)
	expect(w.sha("v3.3.0-rc.4") == "", "no v3.3.0-rc.4")
	fmt.Println()
	w.say("the scratch clone's local git config (no identity may be written):")
	cfg, _ := gitOut(w.clone, "config", "--local", "--list")
	found := false
	for _, l := range strings.Split(cfg, "\n") {
		if strings.HasPrefix(l, "user.") {
			fmt.Println("    " + l)
			found = true
		}
	}
	expect(!found, "no user.* key in the scratch clone's .git/config")
}

func (w *world) openNotification(family, key string) *model.Notification {
	ns, err := w.store.ListNotifications(w.ctx, model.NotificationFilter{Open: true, Family: family, Limit: 100})
	if err != nil {
		panic(err)
	}
	for i := range ns {
		if ns[i].Subject == model.NotificationSubject(family, key) {
			return &ns[i]
		}
	}
	return nil
}
