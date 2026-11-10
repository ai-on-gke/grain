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

// .github/workflows/nightly.yml is Stage 1 of release-candidate
// qualification (design/playbook/releases.playbook.md, "What pass
// means"). Nothing short of pushing a real RC tag exercises its trigger
// on GitHub, and those are not pushed for a test, so these read the
// workflow the way GitHub does: which tags start it, which jobs a
// one-job dispatch runs, what may cancel an RC's run, and whether its
// verdict job covers the whole Stage 1 table.

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The hosted jobs the playbook's Stage 1 table requires, every one of
// which has to end `success` on every RC.
var stage1Jobs = []string{
	"go-long", "staterepo-soak", "loadtest", "scalability", "merge-stress",
	"long-term-test", "long-sim", "postgres-stress", "tunnel-stress",
	"ui-e2e-long", "installer-variants", "vulns", "image-vulns", "cmd-grain-stress", "upgrade-test",
}

type nightlyWorkflowFile struct {
	On struct {
		Push struct {
			Tags     []string `yaml:"tags"`
			Branches []string `yaml:"branches"`
		} `yaml:"push"`
		Schedule         []map[string]string `yaml:"schedule"`
		WorkflowDispatch struct {
			Inputs map[string]struct {
				Required bool   `yaml:"required"`
				Default  string `yaml:"default"`
				Type     string `yaml:"type"`
			} `yaml:"inputs"`
		} `yaml:"workflow_dispatch"`
	} `yaml:"on"`
	Jobs map[string]struct {
		If          string   `yaml:"if"`
		Needs       []string `yaml:"needs"`
		Concurrency struct {
			Group            string `yaml:"group"`
			CancelInProgress string `yaml:"cancel-in-progress"`
		} `yaml:"concurrency"`
	} `yaml:"jobs"`
}

func parseNightly(t *testing.T) nightlyWorkflowFile {
	t.Helper()
	var wf nightlyWorkflowFile
	if err := yaml.Unmarshal([]byte(read(t, ".github", "workflows", "nightly.yml")), &wf); err != nil {
		t.Fatalf(".github/workflows/nightly.yml does not parse: %v", err)
	}
	return wf
}

// githubRefFilter is GitHub's filter for `on.push.tags`: patterns in
// order, the last one to match deciding, `!` negating, and `*` matching
// anything but `/` (docs: "Filter pattern cheat sheet").
func githubRefFilter(patterns []string, ref string) bool {
	matched := false
	for _, p := range patterns {
		negate := strings.HasPrefix(p, "!")
		p = strings.TrimPrefix(p, "!")
		var re strings.Builder
		re.WriteString("^")
		for i := 0; i < len(p); i++ {
			switch {
			case strings.HasPrefix(p[i:], "**"):
				re.WriteString(".*")
				i++
			case p[i] == '*':
				re.WriteString("[^/]*")
			default:
				re.WriteString(regexp.QuoteMeta(p[i : i+1]))
			}
		}
		re.WriteString("$")
		if regexp.MustCompile(re.String()).MatchString(ref) {
			matched = !negate
		}
	}
	return matched
}

func TestNightlyStartsOnEveryReleaseCandidateTagAndNoMarker(t *testing.T) {
	wf := parseNightly(t)
	if len(wf.On.Push.Branches) != 0 {
		t.Errorf("nightly.yml runs on pushes to %v; only RC tags should start it on push", wf.On.Push.Branches)
	}
	for tag, want := range map[string]bool{
		"v3.2.0-rc.1":           true,
		"v3.2.0-rc.12":          true,
		"v3.2.1-rc.1":           true,
		"v4.0.0-rc.3":           true,
		"v3.2.0-rc.1-discarded": false,
		"v3.2.0-rc.1-retracted": false,
		"v3.2.0":                false,
		"v3.2.0-pending":        false,
		"v3.2.0-retracted":      false,
		"latest":                false,
		"candidate":             false,
	} {
		if got := githubRefFilter(wf.On.Push.Tags, tag); got != want {
			t.Errorf("pushing tag %s starts nightly.yml = %v, want %v (on.push.tags %q)", tag, got, want, wf.On.Push.Tags)
		}
	}
	if len(wf.On.Schedule) == 0 {
		t.Errorf("nightly.yml lost its schedule, the safety net for main between RCs")
	}
	jobs, ok := wf.On.WorkflowDispatch.Inputs["jobs"]
	if !ok || jobs.Required || jobs.Default != "" || jobs.Type != "string" {
		t.Errorf("workflow_dispatch needs an optional string `jobs` input defaulting to every job, got %+v (present %v)", jobs, ok)
	}
}

// A dispatch with jobs=<id> -- the train's flake retry -- runs that job
// and the verdict, and nothing else; any other event runs everything.
func TestANightlyDispatchNamingOneJobRunsOnlyThatJob(t *testing.T) {
	wf := parseNightly(t)
	for id, job := range wf.Jobs {
		if id == "stage-1" {
			if job.If != "always()" {
				t.Errorf("stage-1 if = %q, want always() so it reports on failed and skipped jobs too", job.If)
			}
			continue
		}
		want := "github.event_name != 'workflow_dispatch' || inputs.jobs == '' || contains(format(',{0},', inputs.jobs), '," + id + ",')"
		if job.If != want {
			t.Errorf("job %s if = %q, want %q so a dispatch naming other jobs skips it", id, job.If, want)
		}
	}
}

// Neither main's run nor an RC's is cancelled by the next one on its ref:
// a flake retry on vX.Y.Z-rc.N queues behind the run it retries.
func TestNothingCancelsAReleaseCandidatesNightly(t *testing.T) {
	wf := parseNightly(t)
	const want = "${{ github.ref != 'refs/heads/main' && !startsWith(github.ref, 'refs/tags/v') }}"
	for id, job := range wf.Jobs {
		if job.Concurrency.Group == "" {
			continue
		}
		if !strings.Contains(job.Concurrency.Group, "${{ github.ref }}") {
			t.Errorf("job %s concurrency group %q is not keyed on the ref, so two RCs would share it", id, job.Concurrency.Group)
		}
		if job.Concurrency.CancelInProgress != want {
			t.Errorf("job %s cancel-in-progress = %q, want %q", id, job.Concurrency.CancelInProgress, want)
		}
	}
}

func TestTheStage1VerdictCoversEveryRequiredJob(t *testing.T) {
	wf := parseNightly(t)
	verdict, ok := wf.Jobs["stage-1"]
	if !ok {
		t.Fatalf("nightly.yml has no stage-1 job")
	}
	got := slices.Clone(verdict.Needs)
	slices.Sort(got)
	want := slices.Clone(stage1Jobs)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("stage-1 needs %v, want the Stage 1 table's %v", got, want)
	}
	for _, id := range stage1Jobs {
		if _, ok := wf.Jobs[id]; !ok {
			t.Errorf("Stage 1 job %s is missing from nightly.yml", id)
		}
	}
	raw := read(t, ".github", "workflows", "nightly.yml")
	step := raw[strings.Index(raw, "  stage-1:"):]
	for _, want := range []string{`"$result" != success`, "SELECTED: ${{ inputs.jobs }}", "NEEDS: ${{ toJSON(needs) }}"} {
		if !strings.Contains(step, want) {
			t.Errorf("stage-1 does not contain %q", want)
		}
	}
}

func TestNightlysUpgradeTestUpgradesFromLatestAndTheFloor(t *testing.T) {
	raw := read(t, ".github", "workflows", "nightly.yml")
	start := strings.Index(raw, "  upgrade-test:")
	if start < 0 {
		t.Fatalf("nightly.yml has no upgrade-test job")
	}
	job := raw[start:]
	job = job[:strings.Index(job[1:], "\n  real-kubernetes:")+1]
	for _, want := range []string{
		"GRAIN_UPGRADE_RELEASE_IMAGE=",
		"GRAIN_UPGRADE_TARGET=",
		"TestUpgradeFromReleasedVersions/latest",
		"TestUpgradeFromReleasedVersions/floor",
		"--- SKIP:",
	} {
		if !strings.Contains(job, want) {
			t.Errorf("upgrade-test does not contain %q", want)
		}
	}
	contains(t, read(t, "Makefile"), "TestUpgradeFromReleasedVersions")
}

// release.yml runs all of tests/container and fails on any skip, so it
// has to name the published releases TestUpgradeFromReleasedVersions
// upgrades from or every release run goes red on that skip.
func TestReleasesContainerSuiteNamesThePublishedReleases(t *testing.T) {
	raw := read(t, ".github", "workflows", "release.yml")
	start := strings.Index(raw, "- name: Run the container e2e suite")
	if start < 0 {
		t.Fatalf("release.yml has no container e2e step")
	}
	step := raw[start:]
	if end := strings.Index(step[1:], "\n      - name:"); end >= 0 {
		step = step[:end+1]
	}
	for _, want := range []string{
		`GRAIN_UPGRADE_RELEASE_IMAGE="$(release_image_repo_for daemon)"`,
		"export GRAIN_UPGRADE_RELEASE_IMAGE",
		"go test ./tests/container/...",
		"--- SKIP",
	} {
		if !strings.Contains(step, want) {
			t.Errorf("release.yml's container e2e step does not contain %q", want)
		}
	}
}
