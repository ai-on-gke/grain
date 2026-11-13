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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// The hosted jobs the playbook's Stage 1 table requires, every one of
// which has to end `success` on every RC.
var stage1Jobs = []string{
	"go-long", "staterepo-soak", "loadtest", "scalability", "merge-stress",
	"long-term-test", "long-sim", "postgres-stress", "tunnel-stress",
	"ui-e2e-long", "installer-variants", "vulns", "image-vulns", "cmd-grain-stress", "upgrade-test",
	"kvm-sandboxes", "kvm-route-stress", "real-kubernetes",
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
		Name        string            `yaml:"name"`
		If          string            `yaml:"if"`
		Needs       []string          `yaml:"needs"`
		Permissions map[string]string `yaml:"permissions"`
		Outputs     map[string]string `yaml:"outputs"`
		Concurrency struct {
			Group            string `yaml:"group"`
			CancelInProgress string `yaml:"cancel-in-progress"`
		} `yaml:"concurrency"`
		Steps []struct {
			Name string            `yaml:"name"`
			ID   string            `yaml:"id"`
			Env  map[string]string `yaml:"env"`
			Run  string            `yaml:"run"`
		} `yaml:"steps"`
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

// A dispatch with jobs=<id> -- the train's flake retry -- runs idle-gate,
// that job and the verdict, and nothing else; an rc tag runs everything;
// and a scheduled run idle-gate turns off skips every job, stage-1
// included, so the no-op run is green rather than a red Stage 1 that grain
// would file a repair for (pkg/orchestrator/stage1repair.go).
func TestANightlyDispatchNamingOneJobRunsOnlyThatJob(t *testing.T) {
	wf := parseNightly(t)
	if _, ok := wf.Jobs["idle-gate"]; !ok {
		t.Fatalf("nightly.yml has no idle-gate job")
	}
	for id, job := range wf.Jobs {
		switch id {
		case "idle-gate":
			if job.If != "" || len(job.Needs) != 0 {
				t.Errorf("idle-gate if = %q, needs = %v; want neither, so every event evaluates the gate", job.If, job.Needs)
			}
			if job.Outputs["run"] != "${{ steps.gate.outputs.run }}" {
				t.Errorf("idle-gate outputs.run = %q, want ${{ steps.gate.outputs.run }}", job.Outputs["run"])
			}
		case "stage-1":
			const want = "always() && needs.idle-gate.outputs.run == 'true'"
			if job.If != want {
				t.Errorf("stage-1 if = %q, want %q: it reports on failed and skipped jobs, and skips (not fails) when idle-gate turns a scheduled run off", job.If, want)
			}
		default:
			if !slices.Equal(job.Needs, []string{"idle-gate"}) {
				t.Errorf("job %s needs = %v, want [idle-gate]", id, job.Needs)
			}
			want := "needs.idle-gate.outputs.run == 'true' && (github.event_name != 'workflow_dispatch' || inputs.jobs == '' || contains(format(',{0},', inputs.jobs), '," + id + ",'))"
			if job.If != want {
				t.Errorf("job %s if = %q, want %q so idle-gate and a dispatch naming other jobs skip it", id, job.If, want)
			}
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
	want := append([]string{"idle-gate"}, stage1Jobs...)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("stage-1 needs %v, want idle-gate and the Stage 1 table's %v", got, want)
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

	// Run stage-1's script: idle-gate in NEEDS is neither judged nor able
	// to mask a skipped Stage 1 job.
	if len(verdict.Steps) != 1 {
		t.Fatalf("stage-1 has %d steps, want 1", len(verdict.Steps))
	}
	runStage1 := func(results map[string]string, selected string) (string, error) {
		needs := map[string]map[string]string{"idle-gate": {"result": "success"}}
		for id, r := range results {
			needs[id] = map[string]string{"result": r}
		}
		b, _ := json.Marshal(needs)
		cmd := exec.Command("bash", "-c", verdict.Steps[0].Run)
		cmd.Env = append(os.Environ(), "NEEDS="+string(b), "SELECTED="+selected)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	green, retry := map[string]string{}, map[string]string{}
	for _, id := range stage1Jobs {
		green[id], retry[id] = "success", "skipped"
	}
	retry["upgrade-test"] = "success"
	if out, err := runStage1(green, ""); err != nil || !strings.Contains(out, "judged "+strconv.Itoa(len(stage1Jobs))+" Stage 1 job(s)") {
		t.Errorf("stage-1 on an all-green run: %v\n%s", err, out)
	}
	if out, err := runStage1(retry, "upgrade-test"); err != nil || !strings.Contains(out, "judged 1 Stage 1 job(s)") {
		t.Errorf("stage-1 on a one-job retry: %v\n%s", err, out)
	}
	if out, err := runStage1(retry, ""); err == nil {
		t.Errorf("stage-1 passed a full run with skipped Stage 1 jobs:\n%s", out)
	}
}

// scripts/candidate-gate.sh maps GitHub Actions jobs back to workflow job IDs
// via CHECK_QUALIFICATION_JOBS (required), CHECK_QUALIFICATION_OPTIONAL
// (judged when they ran, optional otherwise) and CHECK_QUALIFICATION_IGNORE
// (the roll-up), so every job in nightly.yml must appear in exactly one of
// them with its exact display name.
func TestCandidateGateMatchesNightlyStage1Jobs(t *testing.T) {
	wf := parseNightly(t)
	gate := read(t, "scripts", "candidate-gate.sh")
	header, _, ok := strings.Cut(gate, "\nusage() {")
	if !ok {
		t.Fatal("scripts/candidate-gate.sh has no usage() boundary after its default variables")
	}
	cmd := exec.Command("bash", "-c", "unset CHECK_QUALIFICATION_JOBS CHECK_QUALIFICATION_OPTIONAL CHECK_QUALIFICATION_IGNORE CANDIDATE_GATE_ADVISORY\n"+
		header+"\nprintf '%s\\0%s\\0%s\\0%s' \"$CHECK_QUALIFICATION_JOBS\" \"$CHECK_QUALIFICATION_OPTIONAL\" \"$CHECK_QUALIFICATION_IGNORE\" \"$CANDIDATE_GATE_ADVISORY\"")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("evaluating candidate-gate.sh defaults: %v\n%s", err, out)
	}
	parts := strings.Split(string(out), "\x00")
	if len(parts) != 4 {
		t.Fatalf("unexpected candidate-gate.sh defaults output: %q", string(out))
	}
	parsePairs := func(label, raw string) ([]string, map[string]string) {
		var ids []string
		byID := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			id, name, ok := strings.Cut(line, "=")
			id, name = strings.TrimSpace(id), strings.TrimSpace(name)
			if !ok || id == "" || name == "" {
				t.Errorf("%s entry %q is not id=name", label, line)
				continue
			}
			if prev, dup := byID[id]; dup {
				t.Errorf("%s lists %q twice (%q and %q)", label, id, prev, name)
				continue
			}
			ids = append(ids, id)
			byID[id] = name
		}
		return ids, byID
	}

	gotIDs, gotNames := parsePairs("CHECK_QUALIFICATION_JOBS", parts[0])
	slices.Sort(gotIDs)
	wantIDs := slices.Clone(stage1Jobs)
	slices.Sort(wantIDs)
	if !slices.Equal(gotIDs, wantIDs) {
		t.Errorf("CHECK_QUALIFICATION_JOBS IDs = %v, want Stage 1 jobs %v", gotIDs, wantIDs)
	}

	optIDs, optNames := parsePairs("CHECK_QUALIFICATION_OPTIONAL", parts[1])
	slices.Sort(optIDs)
	if !slices.Contains(optIDs, "grant-matrix-ui") {
		t.Errorf("CHECK_QUALIFICATION_OPTIONAL IDs = %v, want grant-matrix-ui mapped while it stays outside stage-1 needs", optIDs)
	}

	ignoreIDs, ignoreNames := parsePairs("CHECK_QUALIFICATION_IGNORE", parts[2])
	if !slices.Equal(ignoreIDs, []string{"stage-1"}) {
		t.Errorf("CHECK_QUALIFICATION_IGNORE IDs = %v, want [stage-1]", ignoreIDs)
	}

	mapped := map[string]string{}
	for _, group := range []struct {
		label string
		pairs map[string]string
	}{
		{"CHECK_QUALIFICATION_JOBS", gotNames},
		{"CHECK_QUALIFICATION_OPTIONAL", optNames},
		{"CHECK_QUALIFICATION_IGNORE", ignoreNames},
	} {
		for id, gotName := range group.pairs {
			if prev, dup := mapped[id]; dup {
				t.Errorf("job %q appears in both %s and %s", id, prev, group.label)
				continue
			}
			mapped[id] = group.label
			job, exists := wf.Jobs[id]
			if !exists {
				t.Errorf("%s job %q does not exist in nightly.yml", group.label, id)
				continue
			}
			if job.Name != gotName {
				t.Errorf("%s[%q] = %q, want nightly.yml jobs.%s.name %q", group.label, id, gotName, id, job.Name)
			}
		}
	}
	for id, job := range wf.Jobs {
		if _, ok := mapped[id]; !ok {
			t.Errorf("nightly.yml job %q (name %q) is missing from candidate-gate.sh's CHECK_QUALIFICATION_JOBS / CHECK_QUALIFICATION_OPTIONAL / CHECK_QUALIFICATION_IGNORE, so selective retries would pass its display name instead of its id", id, job.Name)
		}
	}

	if !slices.Contains(strings.Fields(parts[3]), "gcp-smoke") {
		t.Errorf("CANDIDATE_GATE_ADVISORY = %q, want it to retain gcp-smoke while WIF variables can skip gcp-smoke.yml", parts[3])
	}
}

// idle-gate turns a scheduled run off only when a vX.Y.Z-rc.N run of
// nightly.yml concluded (not cancelled) in the last 24h, never a tag push
// or a dispatch, and fails open when the Actions API cannot be read.
func TestNightlyIdleGateSkipsAScheduledRunOnlyAfterARecentRCStage1(t *testing.T) {
	wf := parseNightly(t)
	gate, ok := wf.Jobs["idle-gate"]
	if !ok {
		t.Fatalf("nightly.yml has no idle-gate job")
	}
	if len(gate.Permissions) != 1 || gate.Permissions["actions"] != "read" {
		t.Errorf("idle-gate permissions = %v, want only actions: read", gate.Permissions)
	}
	if len(gate.Steps) != 1 || gate.Steps[0].ID != "gate" {
		t.Fatalf("idle-gate wants one step with id gate, has %+v", gate.Steps)
	}
	step := gate.Steps[0]
	if strings.Contains(step.Run, "${{") {
		t.Errorf("idle-gate expands ${{ ... }} inside run:; pass context through env:")
	}

	now := time.Date(2026, 11, 11, 2, 11, 0, 0, time.UTC)
	type apiRun struct {
		ID         int64  `json:"id"`
		HeadBranch string `json:"head_branch"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion,omitempty"`
		UpdatedAt  string `json:"updated_at,omitempty"`
	}
	at := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	gateSays := func(t *testing.T, event string, status int, runs []apiRun) (string, int32) {
		t.Helper()
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			if r.URL.Path != "/repos/acme/widgets/actions/workflows/nightly.yml/runs" || r.Header.Get("Authorization") != "Bearer test-token" {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"workflow_runs": runs})
		}))
		defer srv.Close()
		out := filepath.Join(t.TempDir(), "github_output")
		cmd := exec.Command("bash", "-c", step.Run)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GITHUB_OUTPUT=" + out, "EVENT_NAME=" + event,
			"GITHUB_TOKEN=test-token", "GITHUB_REPOSITORY=acme/widgets", "GITHUB_API_URL=" + srv.URL,
			"IDLE_GATE_NOW_EPOCH=" + strconv.FormatInt(now.Unix(), 10)}
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("idle-gate failed: %v\n%s", err, b)
		}
		b, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(b)), hits.Load()
	}

	for _, event := range []string{"push", "workflow_dispatch"} {
		if got, hits := gateSays(t, event, http.StatusOK, nil); got != "run=true" || hits != 0 {
			t.Errorf("%s: %q after %d API calls, want run=true without asking", event, got, hits)
		}
	}
	if got, _ := gateSays(t, "schedule", http.StatusOK, []apiRun{
		{ID: 1, HeadBranch: "v3.2.0-rc.4", Status: "completed", Conclusion: "success", UpdatedAt: at(4 * time.Hour)},
	}); got != "run=false" {
		t.Errorf("schedule after a green rc 4h ago: %q, want run=false", got)
	}
	if got, _ := gateSays(t, "schedule", http.StatusOK, []apiRun{
		{ID: 2, HeadBranch: "v3.2.0-rc.5", Status: "completed", Conclusion: "failure", UpdatedAt: at(23 * time.Hour)},
	}); got != "run=false" {
		t.Errorf("schedule after a red rc 23h ago: %q, want run=false", got)
	}
	if got, _ := gateSays(t, "schedule", http.StatusOK, []apiRun{
		{ID: 3, HeadBranch: "v3.2.0-rc.3", Status: "completed", Conclusion: "success", UpdatedAt: at(25 * time.Hour)},
		{ID: 4, HeadBranch: "main", Status: "completed", Conclusion: "success", UpdatedAt: at(2 * time.Hour)},
		{ID: 5, HeadBranch: "v3.2.0-rc.4-discarded", Status: "completed", Conclusion: "success", UpdatedAt: at(time.Hour)},
		{ID: 6, HeadBranch: "v3.2.0-rc.6", Status: "completed", Conclusion: "cancelled", UpdatedAt: at(time.Hour)},
	}); got != "run=true" {
		t.Errorf("schedule with no rc Stage 1 concluding in 24h: %q, want run=true", got)
	}
	if got, _ := gateSays(t, "schedule", http.StatusInternalServerError, nil); got != "run=true" {
		t.Errorf("schedule when the Actions API fails: %q, want run=true (fail open)", got)
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
