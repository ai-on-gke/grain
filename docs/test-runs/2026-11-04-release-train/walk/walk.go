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
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
)

func (w *world) walk() error {
	w.item1()
	w.item2and3()
	return nil
}

// item1 is one rc, v3.2.0-rc.1, from the cut to v3.2.0-pending.
func (w *world) item1() {
	h0 := w.sha("main")
	parent := w.sha("main~1")
	latest0 := w.sha("latest")

	w.section("item 1: main is known-good; nightly.yml last ran on main's previous commit")
	w.green("main", h0)
	w.sim.WorkflowRuns = append(w.sim.WorkflowRuns, githubsim.WorkflowRun{
		ID: w.nextRunID(), Name: "nightly", Path: ".github/workflows/nightly.yml", HeadBranch: "main", HeadSHA: parent,
		Event: "schedule", Status: "completed", Conclusion: "success", RunAttempt: 1, CreatedAt: w.now.Add(-20 * time.Hour),
		Jobs: []githubsim.WorkflowJob{{Name: "go-long"}},
	})
	w.say("seeded tests.yml and release.yml green on main@%s, nightly.yml (schedule) on main@%s", h0[:12], parent[:12])

	w.section("item 1: train tick 1 cuts v3.2.0-rc.1 (FORCE=true: the walk does not wait out RELEASE_MIN_INTERVAL_MINUTES)")
	status, outs := w.train("tick 1", "FORCE=true")
	expect(status == 0 && outputHas(outs, "rc_tag=v3.2.0-rc.1"), "rc_tag=v3.2.0-rc.1 cut (exit %d)", status)
	w.tags()
	rc := "v3.2.0-rc.1"
	rcSHA := w.sha(rc)
	expect(rcSHA == h0, "%s is on main's head %s", rc, h0[:12])
	w.tagMessage(rc)

	w.section("item 1/6: release-train-actions.sh on tick 1's outputs starts Stage 1 on the tag")
	n := len(w.sim.Dispatches)
	status = w.actions("actions after tick 1", outs)
	n2 := w.dispatches(n)
	expect(status == 0 && n2 == n+1 && w.sim.Dispatches[n].Ref == rc && w.sim.Dispatches[n].Inputs["jobs"] == "",
		"one nightly.yml dispatch on %s with no inputs.jobs", rc)

	w.section("item 1: Stage 2 waits while Stage 1 is queued")
	w.tick(5 * time.Minute)
	w.reconcile()
	expect(len(w.items(rc)) == 0, "no qualification task filed while nightly.yml is queued")
	w.checks("stage1-queued", rcSHA)

	w.section("item 1: train tick 2 while Stage 1 runs")
	status, outs = w.train("tick 2")
	expect(status == 0 && !strings.Contains(outs, "rc_tag="), "nothing cut while %s qualifies (exit %d)", rc, status)

	w.section("item 6: Stage 1 finishes with two first failures; the train asks for one retry of each")
	w.tick(3 * time.Hour)
	w.conclude(rc, "loadtest", "tunnel-stress")
	w.reconcile()
	expect(len(w.items(rc)) == 0, "Stage 2 not started on a red Stage 1")
	status, outs = w.train("tick 3")
	w.say("tick 3 outputs: %s", strings.ReplaceAll(strings.TrimSpace(outs), "\n", " "))
	n = len(w.sim.Dispatches)
	w.actions("actions after tick 3", outs)
	n2 = w.dispatches(n)
	expect(n2 == n+1 && w.sim.Dispatches[n].Inputs["jobs"] == "loadtest,tunnel-stress",
		"one combined nightly.yml re-run dispatch with inputs.jobs=loadtest,tunnel-stress")
	w.section("item 6: a second wake while the re-run is queued dispatches nothing")
	status, outs = w.train("tick 4")
	n = len(w.sim.Dispatches)
	w.actions("actions after tick 4", outs)
	expect(w.dispatches(n) == n, "no second dispatch while the re-run is open")

	w.section("item 1: the re-run passes; Stage 2 starts off it")
	w.tick(30 * time.Minute)
	w.conclude(rc)
	w.tick(time.Minute)
	w.reconcile()
	w.showItems(rc)
	its := w.items(rc)
	expect(len(its) == 3, "three gate items filed (the final item waits for them): %d", len(its))
	w.checks("stage2-started", rcSHA)

	w.section("item 1: a same-named check run from another App does not qualify the rc")
	w.sim.CheckRuns[rcSHA] = append(w.sim.CheckRuns[rcSHA], github.CheckRun{
		ID: 7001, Name: model.ReleaseQualificationCheckName, HeadSHA: rcSHA, App: "github-actions",
		Status: "completed", Conclusion: ptr("success"), StartedAt: &w.now, CompletedAt: &w.now,
	})
	status, outs = w.train("tick 5 (foreign success check present)", "DRY_RUN=true")
	expect(status == 0 && !strings.Contains(outs, "pending_version=") && !strings.Contains(outs, "promoted_version="),
		"no -pending and no release off github-actions' check run")

	w.section("item 1: Stage 2 stalls on infrastructure (neutral), waits for repair, then re-qualifies on attempt #2")
	for _, item := range []string{"qualification-mergequeue", "qualification-base-triage"} {
		w.tick(5 * time.Minute)
		w.finish(w.latest(rc, item), footer("PASS", rc, rcSHA))
	}
	for i := 0; i <= model.DefaultQualificationPolicy.MaxInfraReruns; i++ {
		w.tick(5 * time.Minute)
		w.finish(w.latest(rc, "qualification-ci-diagnostics"), footer("INFRA", rc, rcSHA))
		w.tick(time.Minute)
		w.reconcile()
	}
	w.checks("stage2-stalled-neutral", rcSHA)
	stalled := w.openNotification(model.FamilyReleaseInfraStalled, rc)
	expect(stalled != nil && stalled.AckedAt == nil, "pipeline.release-infra-stalled:%s raised and unacknowledged", rc)
	status, outs = w.train("tick 5b (Stage 2 neutral)")
	expect(status == 0 && outputHas(outs, "qualification=infra") && outputHas(outs, "qualification_stalled="+rc) &&
		!strings.Contains(outs, "discarded_rc="), "train reports qualification=infra and qualification_stalled=%s without dropping it", rc)
	w.actions("actions after tick 5b", outs)
	w.tick(5 * time.Minute)
	w.reconcile()
	if err := w.store.AckNotification(w.ctx, stalled.ID, "human:walkthrough", w.now); err != nil {
		panic(err)
	}
	w.tick(time.Minute)
	w.reconcile()
	w.checks("stage2-attempt2-started", rcSHA)
	status, outs = w.train("tick 5c (Stage 2 attempt #2 in progress)")
	expect(status == 0 && outputHas(outs, "qualification=pending") && !strings.Contains(outs, "qualification_stalled="),
		"train waits on Stage 2 attempt #2 (qualification=pending)")

	w.section("item 1: the scripted agent passes every gate on attempt #2; grain files the final item; it passes")
	for _, item := range []string{"qualification-mergequeue", "qualification-ci-diagnostics", "qualification-base-triage"} {
		w.tick(10 * time.Minute)
		w.finish(w.latest(rc, item), footer("PASS", rc, rcSHA))
	}
	w.tick(time.Minute)
	w.reconcile()
	w.showItems(rc)
	w.checks("gates-passed", rcSHA)
	final := w.latest(rc, "qualification-system-smoke")
	w.tick(20 * time.Minute)
	w.finish(final, footer("PASS", rc, rcSHA))
	w.tick(time.Minute)
	w.reconcile()
	w.checks("qualified", rcSHA)
	var own *github.CheckRun
	for i, c := range w.sim.CheckRuns[rcSHA] {
		if c.Name == model.ReleaseQualificationCheckName && c.App == app {
			own = &w.sim.CheckRuns[rcSHA][i]
		}
	}
	expect(own != nil && own.Status == "completed" && own.Conclusion != nil && *own.Conclusion == "success",
		"grain/release-qualification completed success, posted as %s", app)

	w.section("item 1: the default train would release v3.2.0 itself (dry run)")
	status, outs = w.train("tick 6 dry run", "DRY_RUN=true")
	w.say("tick 6 outputs: %s", strings.ReplaceAll(strings.TrimSpace(outs), "\n", " "))

	w.section("item 1: with RELEASE_AUTO_PROMOTE=false the train stages v3.2.0-pending")
	status, outs = w.train("tick 7", "RELEASE_AUTO_PROMOTE=false")
	expect(status == 0 && outputHas(outs, "pending_version=v3.2.0"), "pending_version=v3.2.0 (exit %d)", status)
	w.tags()
	if w.sha("v3.2.0-pending") != "" {
		w.tagMessage("v3.2.0-pending")
	}
	expect(w.sha("v3.2.0") == "" && w.sha("latest") == latest0, "v3.2.0 not tagged and latest not moved (%s, the remote's v3.1.0)", latest0[:12])

	w.section("item 1: a person approves v3.2.0 (approve mode, outside the train's grant; v3.2.0 carries #645's breaking change)")
	status, _ = w.run("approve", "release-from-trunk.sh", []string{"RELEASE_PUSH_GRANT=", "GRAIN_CI_REQUESTED_BY_KIND=human",
		"GRAIN_CI_REQUESTED_BY=walkthrough@example.com", "RELEASE_ALLOW_BREAKING=v3.2.0"}, "approve", "v3.2.0")
	expect(status == 0 && w.sha("v3.2.0") == rcSHA && w.sha("latest") == rcSHA, "v3.2.0 and latest on %s", rcSHA[:12])
	w.tags()
}

func ptr(s string) *string { return &s }
