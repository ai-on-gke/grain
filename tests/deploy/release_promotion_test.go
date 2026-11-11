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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/repodecl"
	"github.com/ai-on-gke/grain/pkg/trustboundary"
	"gopkg.in/yaml.v3"
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
		"scripts/granule/build-granule-image.sh", "scripts/granule/guest-inputs.sh",
		"scripts/release-images.sh", "scripts/release-notes.sh",
		"scripts/candidate-gate.sh",
		"scripts/release-patch.sh", "scripts/patch-tag-guard.sh",
		"scripts/build-version.sh", "scripts/grain-ci-host.sh",
		"scripts/release-train-actions.sh", ".github/workflows/rc-qualification.yml",
		".grain/workflows/release.yml", ".github/workflows/release.yml",
		".grain/workflows/promote.yml", ".github/workflows/promote.yml",
		".grain/workflows/release-train.yml", ".github/workflows/release-train.yml",
		"scripts/release-assets.sh", "scripts/build-cli.sh", ".github/workflows/release-train-assets.yml",
		"scripts/buildvcs.sh", "scripts/configure-dockerhub-auth.sh", "scripts/release-image-repos.sh",
	} {
		if len(trigger.Match([]string{p})) == 0 {
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
	var trainTmpl *model.Template
	for i := range decl.Templates {
		if decl.Templates[i].Path == ".grain/templates/release-train.yml" {
			trainTmpl = &decl.Templates[i].Template
		}
	}
	if trainTmpl == nil {
		t.Fatal(".grain/templates/release-train.yml is not declared")
	}
	if got := trainTmpl.NothingNeededAction.Resolved(); got != model.NothingNeededClose {
		t.Errorf("release-train nothing-needed resolves to %q, want %q so acted/noop/failed ticks close and free the overlap: skip schedule",
			got, model.NothingNeededClose)
	}
	body := trainTmpl.Body
	contains(t, body, "bash ~/work/scripts/release-train-task.sh")
	for _, outcome := range []string{"RELEASE_TRAIN_RESULT=noop", "RELEASE_TRAIN_RESULT=acted", "RELEASE_TRAIN_RESULT=failed"} {
		contains(t, body, outcome)
	}
	contains(t, body, "nothing_needed")
	contains(t, body, "grain notify")
	if strings.Contains(body, "call `comment_on_issue`") {
		t.Errorf("release-train.yml still instructs calling comment_on_issue, which parks a branchless tick in awaiting_reply and stalls the 20-minute train")
	}
}

// trainTickPriority is the least priority a release-train tick may be
// filed at: above model.PriorityReleaseGate's Stage 2 work, at the top
// of what a busy queue holds. On 2026-11-10 tick #3946 was filed at the
// default 5 and sat queued fourteen hours behind priority-9 work while
// overlap: skip held every later tick, and no rc was cut that day
// (grain/task-4086).
const trainTickPriority = 9

// A release-train tick is one script run and a close: it must never wait
// behind the backlog, because a tick that waits holds every tick after it.
func TestTheReleaseTrainTickIsFiledAtHighPriority(t *testing.T) {
	decl := declarations(t)
	for _, tmpl := range decl.Templates {
		if tmpl.Path != ".grain/templates/release-train.yml" {
			continue
		}
		if tmpl.Template.Priority == nil {
			t.Fatalf(".grain/templates/release-train.yml sets no priority:, so every tick files at %d and starves "+
				"behind the backlog; set priority: %d or more", model.DefaultTaskPriority, trainTickPriority)
		}
		if *tmpl.Template.Priority < trainTickPriority || *tmpl.Template.Priority <= model.PriorityReleaseGate {
			t.Errorf(".grain/templates/release-train.yml priority is %d, want at least %d (above the backlog and "+
				"Stage 2's %d)", *tmpl.Template.Priority, trainTickPriority, model.PriorityReleaseGate)
		}
		return
	}
	t.Fatal(".grain/templates/release-train.yml is not declared")
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
	for _, step := range strings.Split(namedJob(t, text, "publish"), "- name: ")[1:] {
		name := strings.SplitN(step, "\n", 2)[0]
		switch name {
		case "Checkout", "Setup Node.js", "Setup Go", "Verify the release is the train's, from a qualified rc":
			continue
		}
		if !strings.Contains(step, "if: steps.verify.outputs.train == 'true'") {
			t.Errorf("publish-release.yml step %q runs for a tag verify did not pass as the train's", name)
		}
	}
	// The grain CLI is built by a job that can write nothing, and published
	// by one that runs only for a tag publish verified.
	contains(t, text, "outputs:\n      train: ${{ steps.verify.outputs.train }}")
	build := namedJob(t, text, "cli")
	contains(t, build, "contents: read")
	contains(t, build, "scripts/build-cli.sh")
	if strings.Contains(build, "write") || strings.Contains(build, "secrets.") || strings.Contains(build, "github.token") {
		t.Errorf("publish-release.yml's cli job, which runs build-cli.sh, holds a write permission or a token:\n%s", build)
	}
	publishCLI := namedJob(t, text, "cli-assets")
	contains(t, publishCLI, "needs: [publish, cli]")
	contains(t, publishCLI, "if: needs.publish.outputs.train == 'true'")
	contains(t, publishCLI, "release-assets.sh cli-publish")

	buildAgentd := namedJob(t, text, "agentd")
	contains(t, buildAgentd, "contents: read")
	contains(t, buildAgentd, "scripts/build-agentd.sh")
	if strings.Contains(buildAgentd, "write") || strings.Contains(buildAgentd, "secrets.") || strings.Contains(buildAgentd, "github.token") {
		t.Errorf("publish-release.yml's agentd job, which runs build-agentd.sh, holds a write permission or a token:\n%s", buildAgentd)
	}
	publishAgentd := namedJob(t, text, "agentd-assets")
	contains(t, publishAgentd, "needs: [publish, agentd]")
	contains(t, publishAgentd, "if: needs.publish.outputs.train == 'true'")
	contains(t, publishAgentd, "release-assets.sh agentd-publish")

	// The step loop above reads the publish job alone, so hold every other
	// job to the same rule: besides publish, cli and agentd (which can write
	// nothing), a job runs only once publish has verified the tag as the
	// train's.
	jobs := text[strings.Index(text, "\njobs:\n"):]
	for _, key := range workflowJobKey.FindAllString(jobs, -1) {
		job := strings.TrimSuffix(strings.TrimSpace(key), ":")
		if job == "publish" || job == "cli" || job == "agentd" {
			continue
		}
		body := namedJob(t, text, job)
		if !strings.Contains(body, "needs.publish.outputs.train == 'true'") {
			t.Errorf("publish-release.yml job %q runs for a tag verify did not pass as the train's", job)
		}
	}
}

// Every vX.Y.Z and vX.Y.Z-rc.N release carries the grain CLI that GET
// /cli/install.sh fetches (pkg/ui/cli_install.sh): each workflow that
// publishes one builds scripts/build-cli.sh's archives in a job holding
// only `contents: read`, stamped with the release, and uploads them with
// release-assets.sh cli-publish from a job that waits for the release
// itself to be published.
func TestEveryPublishedReleaseGetsItsGrainCLI(t *testing.T) {
	for file, releaseJob := range map[string]string{
		"promote.yml":              "assets",
		"promote-release.yml":      "assets",
		"publish-release.yml":      "publish",
		"release-train-assets.yml": "publish",
	} {
		text := stripComments(read(t, ".github", "workflows", file))
		build := namedJob(t, text, "cli")
		if !strings.Contains(build, "contents: read") || strings.Contains(build, "write") ||
			strings.Contains(build, "secrets.") || strings.Contains(build, "github.token") {
			t.Errorf("%s: the cli job, which runs build-cli.sh, must hold contents: read and no token:\n%s", file, build)
		}
		for _, want := range []string{
			"actions/cache@", "~/go/pkg/mod/cache/download", "~/.cache/go-build",
			"scripts/build-cli.sh", "release-assets.sh cli-validate", "RELEASE: ", "name: grain-cli",
		} {
			if !strings.Contains(build, want) {
				t.Errorf("%s: the cli job does not say %q", file, want)
			}
		}
		if strings.Index(build, "actions/cache@") > strings.Index(build, "actions/setup-go@") {
			t.Errorf("%s: the cli job must restore actions/cache before actions/setup-go", file)
		}
		publish := namedJob(t, text, "cli-assets")
		for _, want := range []string{"contents: write", "needs: [", releaseJob, "name: grain-cli", "release-assets.sh", "cli-publish"} {
			if !strings.Contains(publish, want) {
				t.Errorf("%s: the cli-assets job does not say %q", file, want)
			}
		}
		// One concurrency group per release across the four workflows, so
		// no two runs upload the same release's CLI at once.
		if !strings.Contains(publish, "group: grain-cli-assets-${{ ") {
			t.Errorf("%s: the cli-assets job does not queue in the shared grain-cli-assets-<release> group", file)
		}
		if strings.Contains(publish, "build-cli.sh") || strings.Contains(publish, "setup-go") {
			t.Errorf("%s: the cli-assets job, which holds contents: write, builds something", file)
		}
	}
}

// Every vX.Y.Z and vX.Y.Z-rc.N release also carries the per-platform static
// grain-agentd binaries and signed manifests: built in an `agentd` job holding
// only `contents: read` and no secrets, then signed (with OpenSSL) and
// uploaded by `agentd-assets` without running setup-go.
func TestEveryPublishedReleaseGetsSignedGrainAgentd(t *testing.T) {
	for file, releaseJob := range map[string]string{
		"promote.yml":              "assets",
		"promote-release.yml":      "assets",
		"publish-release.yml":      "publish",
		"release-train-assets.yml": "publish",
	} {
		text := stripComments(read(t, ".github", "workflows", file))
		build := namedJob(t, text, "agentd")
		if !strings.Contains(build, "contents: read") || strings.Contains(build, "write") ||
			strings.Contains(build, "secrets.") || strings.Contains(build, "github.token") {
			t.Errorf("%s: the agentd job, which runs build-agentd.sh, must hold contents: read and no token:\n%s", file, build)
		}
		for _, want := range []string{
			"actions/cache@", "~/go/pkg/mod/cache/download", "~/.cache/go-build",
			"scripts/build-agentd.sh", "release-assets.sh agentd-validate", "RELEASE: ", "name: grain-agentd",
		} {
			if !strings.Contains(build, want) {
				t.Errorf("%s: the agentd job does not say %q", file, want)
			}
		}
		if strings.Index(build, "actions/cache@") > strings.Index(build, "actions/setup-go@") {
			t.Errorf("%s: the agentd job must restore actions/cache before actions/setup-go", file)
		}
		publish := namedJob(t, text, "agentd-assets")
		for _, want := range []string{
			"contents: write", "needs: [", releaseJob, "name: grain-agentd",
			"AGENTD_RELEASE_SIGNING_KEY", "agentd-sign", "agentd-publish",
		} {
			if !strings.Contains(publish, want) {
				t.Errorf("%s: the agentd-assets job does not say %q", file, want)
			}
		}
		// The signing key is an environment secret, so a workflow pushed on
		// any other branch cannot read it.
		if !strings.Contains(publish, "environment: agentd-release") {
			t.Errorf("%s: the agentd-assets job, which reads AGENTD_RELEASE_SIGNING_KEY, does not run in the agentd-release environment", file)
		}
		for _, other := range []string{"agentd", releaseJob} {
			if strings.Contains(namedJob(t, text, other), "AGENTD_RELEASE_SIGNING_KEY") {
				t.Errorf("%s: the %s job names AGENTD_RELEASE_SIGNING_KEY; only agentd-assets may", file, other)
			}
		}
		if !strings.Contains(publish, "group: grain-agentd-assets-${{ ") {
			t.Errorf("%s: the agentd-assets job does not queue in the shared grain-agentd-assets-<release> group", file)
		}
		if strings.Contains(publish, "build-agentd.sh") || strings.Contains(publish, "setup-go") {
			t.Errorf("%s: the agentd-assets job, which holds contents: write, builds something", file)
		}
	}
}

// Regression for task 3787: when a promoted release has neither a GitHub
// Release nor sha-<12 hex> release assets (so promote.yml's assets and
// cli-assets jobs skip it under RELEASE_CLI_SKIP_UNRELEASED), promote.yml's
// cli job must skip building and uploading the CLI archives instead of
// compiling and discarding them on every push to main; when either the GitHub
// Release or the commit's sha-<12 hex> assets exist (or a version was asked
// for explicitly), it still builds. And promote-release.yml's cli and
// cli-assets jobs run on human approvals alone.
func TestPromoteCLISkipsMissingReleaseAndPromoteReleaseGatesCLIOnApprove(t *testing.T) {
	var promoteWF struct {
		Jobs map[string]struct {
			Steps []struct {
				ID  string `yaml:"id"`
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(read(t, ".github", "workflows", "promote.yml")), &promoteWF); err != nil {
		t.Fatalf("promote.yml does not parse: %v", err)
	}
	var resolveScript string
	for _, step := range promoteWF.Jobs["cli"].Steps {
		if step.ID == "release" {
			resolveScript = step.Run
		}
	}
	if resolveScript == "" {
		t.Fatal("promote.yml cli job has no step with id: release")
	}

	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "release-assets.sh"),
		[]byte("#!/usr/bin/env bash\nprintf '%s\\n' \"${2:-v3.2.0}\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The stub prints curl's own "(22) ... error: 404" line when asked
	// for -S, so the step's log shows whether it still emits that noise.
	curlStub := `#!/usr/bin/env bash
status_fmt=false show_error=false url=""
miss() { [ "$show_error" = true ] && echo "curl: (22) The requested URL returned error: 404" >&2; exit 22; }
while [ $# -gt 0 ]; do
  case "$1" in
    -w) status_fmt=true; shift ;;
    -o | -H) shift ;;
    -*S*) show_error=true ;;
    -*) ;;
    *) url="$1" ;;
  esac
  shift
done
case "$url" in
  */releases/download/v3.2.0/checksums.txt)
    [ "${STUB_CHECKSUMS:-404}" = 200 ] && exit 0 || miss ;;
  */repos/ai-on-gke/grain/releases/tags/v3.2.0)
    [ "$status_fmt" = true ] && printf '%s' "${STUB_RELEASE_API:-404}"
    [ "${STUB_RELEASE_API:-404}" = 200 ] && exit 0 || exit 0 ;;
  */releases/download/sha-*/images.json)
    [ "${STUB_SHA_IMAGES:-404}" = 200 ] && exit 0 || miss ;;
  *)
    echo "unexpected curl url: $url" >&2
    exit 99 ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "curl"), []byte(curlStub), 0o755); err != nil {
		t.Fatal(err)
	}

	gitEnv := append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	for _, args := range [][]string{
		{"init", "-q"},
		{"commit", "-q", "--allow-empty", "-m", "release"},
		{"tag", "-a", "v3.2.0", "-m", "v3.2.0"},
		{"tag", "-a", "latest", "-m", "latest"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = gitEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	for _, tc := range []struct {
		name      string
		env       []string
		wantBuild string
		wantLog   string
	}{
		{
			name:      "checksums already published",
			env:       []string{"STUB_CHECKSUMS=200"},
			wantBuild: "false",
			wantLog:   "already carries the grain CLI",
		},
		{
			name:      "automatic run skips when neither GitHub Release nor sha assets exist",
			env:       []string{"STUB_CHECKSUMS=404", "STUB_RELEASE_API=404", "STUB_SHA_IMAGES=404"},
			wantBuild: "false",
			wantLog:   "GitHub Release v3.2.0 does not exist",
		},
		{
			name:      "automatic run builds when GitHub Release exists without sha assets",
			env:       []string{"STUB_CHECKSUMS=404", "STUB_RELEASE_API=200", "STUB_SHA_IMAGES=404"},
			wantBuild: "true",
			wantLog:   "promote: building the grain CLI for v3.2.0: its GitHub Release exists and carries no checksums.txt",
		},
		{
			name:      "automatic run builds when sha assets exist so assets job will create release",
			env:       []string{"STUB_CHECKSUMS=404", "STUB_RELEASE_API=404", "STUB_SHA_IMAGES=200"},
			wantBuild: "true",
			wantLog:   "promote: building the grain CLI for v3.2.0: GitHub Release v3.2.0 does not exist yet, but sha-",
		},
		{
			name:      "automatic run builds and names the status when the API is rate limited",
			env:       []string{"STUB_CHECKSUMS=404", "STUB_RELEASE_API=403", "STUB_SHA_IMAGES=404"},
			wantBuild: "true",
			wantLog:   "promote: building the grain CLI for v3.2.0: the GitHub API answered 403 for releases/tags/v3.2.0",
		},
		{
			name:      "automatic run builds and names the status when the API is unreachable",
			env:       []string{"STUB_CHECKSUMS=404", "STUB_RELEASE_API=000", "STUB_SHA_IMAGES=404"},
			wantBuild: "true",
			wantLog:   "promote: building the grain CLI for v3.2.0: the GitHub API answered 000 for releases/tags/v3.2.0",
		},
		{
			name:      "explicit PROMOTE_VERSION builds so cli-assets fails loudly on missing release",
			env:       []string{"PROMOTE_VERSION=v3.2.0", "STUB_CHECKSUMS=404", "STUB_RELEASE_API=404", "STUB_SHA_IMAGES=404"},
			wantBuild: "true",
			wantLog:   "promote: building the grain CLI for v3.2.0: asked for by name",
		},
	} {
		outFile := filepath.Join(t.TempDir(), "github_output")
		env := append([]string{
			"GITHUB_OUTPUT=" + outFile,
			"GITHUB_SERVER_URL=https://github.com",
			"GITHUB_API_URL=https://api.github.com",
			"GITHUB_REPOSITORY=ai-on-gke/grain",
		}, tc.env...)
		cmd := exec.Command("bash", "-c", resolveScript)
		cmd.Dir = dir
		cmd.Env = append([]string{"PATH=" + binDir + ":" + os.Getenv("PATH"), "HOME=" + t.TempDir()}, env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: step failed: %v\n%s", tc.name, err, out)
		}
		gotOut, err := os.ReadFile(outFile)
		if err != nil {
			t.Fatalf("%s: reading GITHUB_OUTPUT: %v", tc.name, err)
		}
		if want := "build=" + tc.wantBuild + "\n"; !strings.Contains(string(gotOut), want) {
			t.Errorf("%s: GITHUB_OUTPUT = %q, want %q (log: %s)", tc.name, gotOut, want, out)
		}
		if tc.wantLog != "" && !strings.Contains(string(out), tc.wantLog) {
			t.Errorf("%s: log = %q, want substring %q", tc.name, out, tc.wantLog)
		}
		if strings.Contains(string(out), "curl: (22)") {
			t.Errorf("%s: log = %q, want no curl error lines for the expected 404s", tc.name, out)
		}
	}

	prWF := githubPromoteReleaseWorkflow(t)
	cliJob, ok := prWF.Jobs["cli"]
	if !ok {
		t.Fatal("promote-release.yml has no cli job")
	}
	if len(cliJob.Needs) != 1 || cliJob.Needs[0] != "approve" {
		t.Errorf("promote-release.yml cli.needs = %v, want [approve]", cliJob.Needs)
	}
	if cliJob.If != "inputs.decision == 'approve' || inputs.decision == ''" {
		t.Errorf("promote-release.yml cli.if = %q, want approve-only gate", cliJob.If)
	}
	cliAssetsJob, ok := prWF.Jobs["cli-assets"]
	if !ok {
		t.Fatal("promote-release.yml has no cli-assets job")
	}
	if !strings.Contains(cliAssetsJob.If, "inputs.decision == 'approve'") || !strings.Contains(cliAssetsJob.If, "needs.cli.outputs.build == 'true'") {
		t.Errorf("promote-release.yml cli-assets.if = %q, want approve and build==true gate", cliAssetsJob.If)
	}

	var prResolveScript string
	for _, step := range cliJob.Steps {
		if step.ID == "release" {
			prResolveScript = step.Run
		}
	}
	if prResolveScript == "" {
		t.Fatal("promote-release.yml cli job has no step with id: release")
	}
	for _, bad := range []string{"v3.2.0\nFOO=bar", "3.2.0", "v3.2.0-pending", ""} {
		outFile := filepath.Join(t.TempDir(), "github_output")
		if out, err := runStep(t, dir, prResolveScript,
			"RELEASE="+bad, "DRY_RUN=true",
			"GITHUB_OUTPUT="+outFile, "GITHUB_SERVER_URL=https://github.com", "GITHUB_REPOSITORY=ai-on-gke/grain"); err == nil {
			t.Errorf("promote-release.yml cli release step accepted %q:\n%s", bad, out)
		}
	}
	// In a dry run before v3.3.0 is tagged, cli falls back to v3.3.0-pending;
	// in a live run without v3.3.0 tagged, it fails closed.
	cmd := exec.Command("git", "tag", "-a", "v3.3.0-pending", "-m", "pending")
	cmd.Dir = dir
	cmd.Env = gitEnv
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git tag v3.3.0-pending: %v\n%s", err, out)
	}
	curlPendingStub := strings.ReplaceAll(curlStub, "v3.2.0", "v3.3.0")
	if err := os.WriteFile(filepath.Join(binDir, "curl"), []byte(curlPendingStub), 0o755); err != nil {
		t.Fatal(err)
	}
	dryOutFile := filepath.Join(t.TempDir(), "github_output")
	cmd = exec.Command("bash", "-c", prResolveScript)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + binDir + ":" + os.Getenv("PATH"), "HOME=" + t.TempDir(),
		"RELEASE=v3.3.0", "DRY_RUN=true", "STUB_CHECKSUMS=404",
		"GITHUB_OUTPUT=" + dryOutFile, "GITHUB_SERVER_URL=https://github.com", "GITHUB_REPOSITORY=ai-on-gke/grain",
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("promote-release dry-run cli resolve failed: %v\n%s", err, out)
	}
	gotDry, _ := os.ReadFile(dryOutFile)
	if !strings.Contains(string(gotDry), "ref=refs/tags/v3.3.0-pending\n") || !strings.Contains(string(gotDry), "build=true\n") {
		t.Errorf("promote-release dry-run cli output = %q, want ref=refs/tags/v3.3.0-pending and build=true", gotDry)
	}
	liveOutFile := filepath.Join(t.TempDir(), "github_output")
	cmd = exec.Command("bash", "-c", prResolveScript)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + binDir + ":" + os.Getenv("PATH"), "HOME=" + t.TempDir(),
		"RELEASE=v3.3.0", "DRY_RUN=false", "STUB_CHECKSUMS=404",
		"GITHUB_OUTPUT=" + liveOutFile, "GITHUB_SERVER_URL=https://github.com", "GITHUB_REPOSITORY=ai-on-gke/grain",
	}
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Errorf("promote-release live cli resolve succeeded without refs/tags/v3.3.0:\n%s", out)
	}
}

// Regression for task 3060: ai-on-gke/grain runs with ci_mode: github, where
// applyGrainCIGrant strips grain#ci (`grain-ci`) from scheduled and suite
// compositions and CreateTask refuses it when copied from a template. Every
// release and qualification template whose body calls `grain ci` or
// `list_ci_runs` / `get_ci_log` must carry repo-scoped api:ci-read@ai-on-gke/grain.
func TestReleaseAndQualificationTemplatesHoldScopedCIRead(t *testing.T) {
	decl := declarations(t)
	want := model.APIGrantString("ci-read", model.GrainSourceRepo().String())
	need := map[string]bool{
		".grain/templates/release-train.yml":                false,
		".grain/templates/release-watchdog.yml":             false,
		".grain/templates/release-process-review.yml":       false,
		".grain/templates/qualification-ci-diagnostics.yml": false,
	}
	for _, tmpl := range decl.Templates {
		if _, ok := need[tmpl.Path]; !ok {
			continue
		}
		need[tmpl.Path] = true
		var caps []string
		for _, g := range tmpl.Template.Grants {
			caps = append(caps, model.CanonicalGrantKey(g.Capability))
			if model.IsGrainCIGrant(g.Capability) {
				t.Errorf("%s carries %q, which applyGrainCIGrant strips on ci_mode: github", tmpl.Path, g.Capability)
			}
		}
		if !containsString(caps, want) {
			t.Errorf("%s grants = %v, want %q", tmpl.Path, caps, want)
		}
	}
	for path, seen := range need {
		if !seen {
			t.Errorf("%s is not declared", path)
		}
	}
}

// Regression for task 3788: release-process-review and release-watchdog
// inspect watchdog/steward/rc-blocker/flake-budget tasks, notifications,
// metrics, release-qualification suite runs, and release/candidate status, so
// both must hold read-only task, deployment, suite, and release scopes (and
// no dangerous scopes or retired bare `self-debug` prose).
func TestReleaseReviewAndWatchdogHoldReadOnlyTaskAndSuiteScopes(t *testing.T) {
	decl := declarations(t)
	repo := model.GrainSourceRepo().String()
	wantGrants := []string{
		model.APIGrantString("ci-read", repo),
		model.APIGrantString("self-debug", ""),
		model.APIGrantString("suites:read", repo),
		model.APIGrantString("releases:read", repo),
	}
	targets := map[string]bool{
		".grain/templates/release-process-review.yml": false,
		".grain/templates/release-watchdog.yml":       false,
	}
	for _, tmpl := range decl.Templates {
		if _, ok := targets[tmpl.Path]; !ok {
			continue
		}
		targets[tmpl.Path] = true
		var caps []string
		for _, g := range tmpl.Template.Grants {
			caps = append(caps, g.Capability)
			parsed, ok, err := model.ParseAPIGrant(g.Capability)
			if !ok || err != nil {
				t.Errorf("%s carries non-API or invalid grant %q (ok=%v, err=%v)", tmpl.Path, g.Capability, ok, err)
				continue
			}
			if parsed.Dangerous() {
				t.Errorf("%s carries dangerous API grant %q", tmpl.Path, g.Capability)
			}
		}
		for _, want := range wantGrants {
			if !containsString(caps, want) {
				t.Errorf("%s grants = %v, missing %q", tmpl.Path, caps, want)
			}
		}
		if strings.Contains(tmpl.Template.Body, "`self-debug`") {
			t.Errorf("%s body still names retired bare `self-debug`; use `api:self-debug`", tmpl.Path)
		}
	}
	for path, seen := range targets {
		if !seen {
			t.Errorf("%s is not declared", path)
		}
	}
}
