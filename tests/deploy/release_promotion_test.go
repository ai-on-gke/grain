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

package deploy

import (
	"os"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/repodecl"
	"github.com/ai-on-gke/grain/pkg/trustboundary"
)

// promoteReleasePath is the person-only last step of a trunk release
// (design/design/release-from-trunk.design.md, §4).
const promoteReleasePath = ".grain/workflows/promote-release.yml"

func promoteReleaseWorkflow(t *testing.T) ci.Workflow {
	t.Helper()
	workflows, _, err := ci.LoadDir(os.DirFS(repoRoot(t)))
	if err != nil {
		t.Fatalf("this repository's own %s does not parse: %v", ci.WorkflowDir, err)
	}
	for _, w := range workflows {
		if w.Path == promoteReleasePath {
			return w
		}
	}
	t.Fatalf("%s does not exist", promoteReleasePath)
	return ci.Workflow{}
}

// It answers a dispatch and nothing else: no merge, no push, no pull
// request and no cron can make a release official.
func TestPromoteReleaseIsDispatchOnly(t *testing.T) {
	w := promoteReleaseWorkflow(t)
	if !w.On.Matches(ci.EventManual, "main") {
		t.Errorf("%s does not answer workflow_dispatch on main (%s)", promoteReleasePath, w.On.Describe())
	}
	for _, event := range []ci.Event{ci.EventPush, ci.EventPullRequest, ci.EventRelease, ci.EventSchedule} {
		for _, branch := range []string{"main", "candidate"} {
			if w.On.Matches(event, branch) {
				t.Errorf("%s answers %s on %s; only a person's dispatch may run it", promoteReleasePath, event, branch)
			}
		}
	}
}

// Every job refuses to run unless grain says a person asked: the train
// script's approve, reject and rollback modes check
// GRAIN_CI_REQUESTED_BY_KIND themselves, and any other job checks it in
// its own first step. A dispatch by any route but the person-only ones
// therefore pushes nothing.
func TestEveryPromoteReleaseJobRefusesAnybodyButAPerson(t *testing.T) {
	w := promoteReleaseWorkflow(t)
	jobs := map[string]bool{}
	for _, job := range w.Jobs {
		jobs[job.ID] = true
		if len(job.Steps) == 0 {
			t.Errorf("%s job %s has no steps", promoteReleasePath, job.ID)
			continue
		}
		first := job.Steps[0].Run
		guarded := strings.Contains(first, `GRAIN_CI_REQUESTED_BY_KIND:-}" != human`) ||
			strings.Contains(first, "scripts/release-from-trunk.sh approve") ||
			strings.Contains(first, "scripts/release-from-trunk.sh reject") ||
			strings.Contains(first, "scripts/release-from-trunk.sh rollback")
		if !guarded {
			t.Errorf("%s job %s's first step does not refuse a caller that is not a person:\n%s",
				promoteReleasePath, job.ID, first)
		}
		if job.Cache {
			t.Errorf("%s job %s may be replayed from the cache; every run is a decision", promoteReleasePath, job.ID)
		}
	}
	for _, want := range []string{"approve", "promote", "reject", "rollback"} {
		if !jobs[want] {
			t.Errorf("%s declares no %s job (the approval routes start promote, reject and rollback)", promoteReleasePath, want)
		}
	}
	script := read(t, "scripts", "release-from-trunk.sh")
	if !strings.Contains(script, `if [ "${GRAIN_CI_REQUESTED_BY_KIND:-}" != human ]; then`) {
		t.Error("scripts/release-from-trunk.sh's approve, reject and rollback modes no longer refuse a caller that is not a person")
	}
}

// A bad release is rolled back by the same person-only dispatch that
// approves one (design/playbook/releases.playbook.md §11): the GitHub
// Actions copy's rollback job refuses anything but a person's
// workflow_dispatch before it runs release-from-trunk.sh rollback, which
// tags vX.Y.Z-retracted and re-points `latest` and checks
// GRAIN_CI_REQUESTED_BY_KIND itself; its free-text reason never reaches
// $GITHUB_ENV.
func TestPromoteReleaseRollsABadReleaseBackForAPersonOnly(t *testing.T) {
	text := stripComments(read(t, ".github", "workflows", "promote-release.yml"))
	contains(t, upTo(t, text, "jobs:"), "- rollback")
	job := namedJob(t, text, "rollback")
	contains(t, job, "if: inputs.decision == 'rollback'")
	refuse := strings.Index(job, `"${GITHUB_EVENT_NAME:-}" != "workflow_dispatch"`)
	person := strings.Index(job, `""|*"[bot]"|grain-ci*)`)
	run := strings.Index(job, `bash ./scripts/release-from-trunk.sh rollback --bad "$GRAIN_CI_RELEASE_VERSION"`)
	if refuse < 0 || person < 0 || run < 0 || refuse > run || person > run {
		t.Errorf("promote-release.yml's rollback job does not refuse anybody but a person's dispatch before it runs the rollback:\n%s", job)
	}
	contains(t, job, "GRAIN_CI_REQUESTED_BY_KIND=human")
	if strings.Contains(job, "RELEASE_ROLLBACK_REASON=") {
		t.Error("promote-release.yml's rollback job writes the free-text reason to $GITHUB_ENV, where a newline in it could set any variable")
	}
	script := read(t, "scripts", "release-from-trunk.sh")
	contains(t, script, "vX.Y.Z-retracted")
}

// human: required on the approval path, in both of the trust-boundary
// declarations: the repository's own path trigger holds a change to the
// approval workflow and the route that starts it for a person's Submit, and
// grain's built-in list puts them in a category no clean automated review
// releases (design/playbook/releases.playbook.md §0.3).
func TestPromoteReleaseIsDeclaredHumanRequired(t *testing.T) {
	trigger := declaredTrigger(t, "release-promotion")
	if !trigger.Gate || trigger.Human != model.TriggerHumanRequired || !trigger.Sensitive {
		t.Errorf("release-promotion trigger = gate %v, human %q, sensitive %v; want gate: true, human: required, sensitive: true",
			trigger.Gate, trigger.Human, trigger.Sensitive)
	}
	for _, p := range []string{promoteReleasePath, ".github/workflows/promote-release.yml", "pkg/ui/release_approval.go",
		// The Inbox approval on GitHub Actions repos (gap 4): the route that
		// signs and dispatches it, the record it signs, and the script the
		// workflow verifies it with.
		"pkg/ui/release_approval_github.go", "pkg/releaseapproval/releaseapproval.go", "scripts/verify-release-approval.sh",
		// The continuous train's own promotion (playbook section 1).
		"scripts/release-from-trunk.sh", "scripts/release.conf", "scripts/release-train-task.sh",
		".grain/schedules/release-train.yml", ".github/workflows/publish-release.yml"} {
		if !containsString(trigger.Paths, p) {
			t.Errorf("the release-promotion trigger does not hold %s (paths %v)", p, trigger.Paths)
		}
		matches := trustboundary.Classify([]string{p})
		if !trustboundary.RequiresHuman(matches) {
			t.Errorf("%s is not on a human-required trust-boundary category: %+v", p, matches)
		}
	}
}

// The release jobs and the scripts they run cannot make a release official,
// so grain's security review alone releases them (§0.3): a gating, sensitive
// trigger with no human: required, a built-in category a clean review
// releases, and no other trigger of this repository's holding them for a
// person -- grain-declarations' .grain/** included.
func TestReleaseMachineryClearsOnSecurityReview(t *testing.T) {
	trigger := declaredTrigger(t, "release-machinery")
	if !trigger.Gate || trigger.Human == model.TriggerHumanRequired || !trigger.Sensitive {
		t.Errorf("release-machinery trigger = gate %v, human %q, sensitive %v; want gate: true, no human, sensitive: true",
			trigger.Gate, trigger.Human, trigger.Sensitive)
	}
	decl := declarations(t)
	for _, p := range []string{
		"scripts/release-images.sh", "scripts/release-notes.sh",
		"scripts/candidate-gate.sh",
		"scripts/patch-tag-guard.sh", "scripts/build-version.sh", "scripts/grain-ci-host.sh",
		"scripts/release-train-actions.sh", ".github/workflows/rc-qualification.yml",
		".grain/workflows/release.yml", ".github/workflows/release.yml",
		".grain/workflows/promote.yml", ".github/workflows/promote.yml",
		".grain/workflows/release-train.yml", ".github/workflows/release-train.yml",
	} {
		if !containsString(trigger.Paths, p) {
			t.Errorf("the release-machinery trigger does not hold %s (paths %v)", p, trigger.Paths)
		}
		if matches := trustboundary.Classify([]string{p}); matches == nil || trustboundary.RequiresHuman(matches) {
			t.Errorf("%s: trust-boundary categories %+v; want one a clean review releases", p, matches)
		}
		for _, other := range decl.Triggers {
			if other.Human == model.TriggerHumanRequired && len(other.Match([]string{p})) > 0 {
				t.Errorf("%s is still held for a person by the %s trigger", p, other.Name)
			}
		}
	}
}

func declarations(t *testing.T) model.RepoDeclarations {
	t.Helper()
	decl, err := repodecl.Load(os.DirFS(repoRoot(t)), model.GrainSourceRepo())
	if err != nil {
		t.Fatalf("this repository's declarations do not load: %v", err)
	}
	return decl
}

func declaredTrigger(t *testing.T, name string) model.PathTrigger {
	t.Helper()
	decl := declarations(t)
	for _, tr := range decl.Triggers {
		if tr.Name == name {
			return tr
		}
	}
	t.Fatalf(".grain/triggers/%s.yml is not among the declared triggers: %+v", name, decl.Triggers)
	return model.PathTrigger{}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// The core release train runs continuously (operator decision, 2026-11-04;
// design/playbook/releases.playbook.md §0.4): a schedule fires the
// release-train template every 10-30 minutes, never over its own previous
// tick, and the tick is the one wrapper command that ends a no-op cheaply.
func TestTheReleaseTrainIsScheduledContinuously(t *testing.T) {
	decl := declarations(t)
	var sched *model.Schedule
	for i := range decl.Schedules {
		if decl.Schedules[i].Path == ".grain/schedules/release-train.yml" {
			sched = &decl.Schedules[i].Schedule
		}
	}
	if sched == nil {
		t.Fatal(".grain/schedules/release-train.yml is not declared: nothing runs the release train")
	}
	if sched.TemplateID == nil || !strings.HasSuffix(*sched.TemplateID, "release-train") {
		t.Errorf("the release-train schedule fires %v, want the release-train template", sched.TemplateID)
	}
	if r := sched.Recurrence; r.Kind != model.RecurrenceEveryNMinutes || r.EveryNMinutes < 10 || r.EveryNMinutes > 30 {
		t.Errorf("the release-train schedule recurs %+v, want every 10-30 minutes", r)
	}
	if sched.Overlap != model.OverlapSkip {
		t.Errorf("the release-train schedule's overlap is %q, want skip: two ticks would race for the same tags", sched.Overlap)
	}
	var body string
	for _, tmpl := range decl.Templates {
		if tmpl.Path == ".grain/templates/release-train.yml" {
			body = tmpl.Template.Body
		}
	}
	contains(t, body, "bash ~/work/scripts/release-train-task.sh")
	contains(t, body, "RELEASE_TRAIN_RESULT=noop")
	contains(t, body, "nothing_needed")
}

// The train's grant reaches the release now (playbook §1): vM.Y.0 created,
// latest force-moved -- and nothing that would move or delete a release,
// tag a patch or retract one. The script's own train_ref names the same.
func TestTheReleaseTrainsGrantReachesTheReleaseAndNoMore(t *testing.T) {
	decl := declarations(t)
	var push model.RefPushSet
	for _, tmpl := range decl.Templates {
		if tmpl.Path == ".grain/templates/release-train.yml" {
			push = tmpl.Push
		}
	}
	for ref, want := range map[string][3]bool{ // matched, force, delete
		"refs/tags/v3.2.0":           {true, false, false},
		"refs/tags/v3.12.0":          {true, false, false},
		"refs/tags/v10.100.0":        {true, false, false},
		"refs/tags/latest":           {true, true, false},
		"refs/tags/v3.2.0-rc.1":      {true, false, false},
		"refs/tags/v3.1.1":           {false, false, false},
		"refs/tags/v3.1.0-retracted": {false, false, false},
		"refs/heads/main":            {false, false, false},
	} {
		matched, force, del := push.Allows(ref)
		if got := [3]bool{matched, force, del}; got != want {
			t.Errorf("the release train's grant on %s = (matched, force, delete) %v, want %v", ref, got, want)
		}
	}
	script := read(t, "scripts", "release-from-trunk.sh")
	contains(t, script, `refs/tags/latest) return 0 ;;`)
	contains(t, script, `refs/tags/v*) [[ "$1" =~ ^refs/tags/v[0-9]{1,2}\.[0-9]{1,3}\.0$ ]] ;;`)
	contains(t, script, `promotion_allowed "$dst"`)
}

// publish-release.yml publishes what the train released, and only after
// release-from-trunk.sh verify has checked the tag is the train's, on its
// qualified rc's own commit: it runs on that tag push alone.
func TestPublishReleaseVerifiesBeforeItPublishes(t *testing.T) {
	text := stripComments(read(t, ".github", "workflows", "publish-release.yml"))
	on := upTo(t, text, "permissions:")
	contains(t, on, `- "v[0-9]+.[0-9]+.0"`)
	for _, bad := range []string{"workflow_dispatch", "schedule:", "pull_request", "workflow_run", "branches:"} {
		if strings.Contains(on, bad) {
			t.Errorf("publish-release.yml runs on %s; it publishes a release on its tag push alone", bad)
		}
	}
	verify := strings.Index(text, `release-from-trunk.sh verify`)
	promote := strings.Index(text, `release-images.sh promote`)
	if verify < 0 || promote < 0 || verify > promote {
		t.Errorf("publish-release.yml does not verify the release (at %d) before it publishes its images (at %d)", verify, promote)
	}
	contains(t, text, "if: steps.verify.outputs.train == 'true'")
	contains(t, text, "RELEASE_QUALIFICATION_APP: ${{ vars.RELEASE_QUALIFICATION_APP }}")
	contains(t, text, "scripts/release-notes.sh write")
	contains(t, text, "gh release create")
	// Every step that publishes -- images, credentials, the GitHub Release
	// -- runs only for a tag verify passed as the train's: a vX.Y.0 with no
	// Promoted-From (verify's exit 4) publishes nothing, since the train's
	// grant can push one on any commit of main.
	for _, step := range strings.Split(text, "- name: ")[1:] {
		name := strings.SplitN(step, "\n", 2)[0]
		switch name {
		case "Checkout", "Setup Node.js", "Setup Go", "Verify the release is the train's, from a qualified rc":
			continue
		}
		if !strings.Contains(step, "if: steps.verify.outputs.train == 'true'") {
			t.Errorf("publish-release.yml step %q runs for a tag verify did not pass as the train's", name)
		}
	}
}

// release-tags.yml publishes the images of the tags the train's grain task
// pushes with RELEASE_IMAGES=off: rc, pending and discard-rc, on those tag
// pushes alone. It never runs promote or retract and never names latest,
// so it cannot move a :vX.Y.Z or :latest image (§0.3).
func TestReleaseTagsPublishesTheTrainsTagsAndNeverPromotes(t *testing.T) {
	text := stripComments(read(t, ".github", "workflows", "release-tags.yml"))
	on := upTo(t, text, "permissions:")
	for _, tag := range []string{`- "v*-rc.*"`, `- "v*-pending"`, `- "v*-discarded"`} {
		contains(t, on, tag)
	}
	for _, bad := range []string{"workflow_dispatch", "schedule:", "pull_request", "workflow_run", "branches:", "release:"} {
		if strings.Contains(on, bad) {
			t.Errorf("release-tags.yml runs on %s; it publishes on the train's tag pushes alone", bad)
		}
	}
	for _, mode := range []string{`release-images.sh rc "$ARG"`, `release-images.sh pending "$ARG"`, `release-images.sh discard-rc "$ARG"`} {
		contains(t, text, mode)
	}
	if got := strings.Count(text, "release-images.sh"); got != 3 {
		t.Errorf("release-tags.yml runs release-images.sh %d times; want exactly its rc, pending and discard-rc modes", got)
	}
	for _, bad := range []string{"promote", "retract", "discard-pending", "latest", "--dry-run"} {
		absent(t, strings.ReplaceAll(text, "runs-on: ubuntu-latest", ""), bad)
	}
	// v*-rc.* matches vX.Y.Z-rc.N-discarded too, so -discarded is asked first.
	discarded := strings.Index(text, `-discarded$ ]]`)
	rc := strings.Index(text, `-rc\.$num$ ]]`)
	if discarded < 0 || rc < 0 || discarded > rc {
		t.Errorf("release-tags.yml does not tell a -discarded tag (at %d) from an rc (at %d) before it decides", discarded, rc)
	}
	// rc and pending publish only for a commit main or its release branch has.
	contains(t, text, `git merge-base --is-ancestor "$commit" "$ref"`)
	for _, step := range strings.Split(text, "- name: ")[1:] {
		name := strings.SplitN(step, "\n", 2)[0]
		switch name {
		case "Checkout", "Decide what the tag publishes":
			continue
		}
		if !strings.Contains(step, "if: steps.mode.outputs.mode != 'none'") {
			t.Errorf("release-tags.yml step %q runs for a tag that is none of the train's", name)
		}
	}
}
