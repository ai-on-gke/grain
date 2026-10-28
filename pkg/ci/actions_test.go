package ci

import (
	"context"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

func TestGranuleImageBuilderDoesNotBlockPushChecks(t *testing.T) {
	data, err := os.ReadFile("testdata/granule-runner-images.yml")
	if err != nil {
		t.Fatal(err)
	}
	tree := fstest.MapFS{
		".github/workflows/ci.yml":                   {Data: []byte(granuleActionsWorkflow)},
		".github/workflows/github-runner-images.yml": {Data: data},
	}
	ws, _, err := LoadDir(tree)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []Event{EventPush, EventPullRequest} {
		p, err := PlanRun(Request{Repo: "bwsalmon/granule", Ref: "main", Event: event}, ws)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Jobs) != 3 {
			t.Fatalf("%s: got %d jobs", event, len(p.Jobs))
		}
	}
	// The image builder's `build` job reads
	// `${{ needs.resolve.outputs.runner-images-sha }}`, which is the half
	// of `needs` two sandboxes cannot carry between them: asking for that
	// workflow by hand reports the limitation rather than running a job
	// with the value silently empty.
	_, err = PlanRun(Request{Repo: "bwsalmon/granule", Ref: "main", Event: EventManual}, ws)
	if err == nil || !strings.Contains(err.Error(), "reads the `needs` context") {
		t.Fatalf("explicit dispatch must report execution limitation: %v", err)
	}
}

func TestScheduleAndDispatchInputs(t *testing.T) {
	job := "\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: true\n"
	for _, dir := range []string{WorkflowDir, GitHubWorkflowDir} {
		w, err := Parse(dir+"/cron.yml", []byte("on:\n  schedule:\n    - cron: '17 9 * * 1'\n  workflow_dispatch:\n    inputs:\n      image:\n        type: choice\n        default: all\n        options: [all, ubuntu-slim]\n"+job))
		if err != nil {
			t.Fatal(err)
		}
		if !w.On.Matches(EventManual, "main") || w.On.Matches(EventPush, "main") {
			t.Fatal("wrong triggers")
		}
		w, err = Parse(dir+"/cron.yml", []byte("on:\n  schedule:\n    - cron: '17 9 * * 1'\n"+job))
		if err != nil {
			t.Fatal(err)
		}
		if w.On.Push != nil || w.On.PullRequest != nil || w.On.Release != nil || w.On.Manual {
			t.Fatal("cron must not create a branch or dispatch trigger")
		}
		// A cron in grain's own directory is a schedule of this
		// deployment's (cron.go); one in a borrowed Actions file stays
		// GitHub's scheduler's business.
		native := dir == WorkflowDir
		if scheduled := len(w.On.Schedule) > 0; scheduled != native {
			t.Fatalf("%s: schedule trigger %v, want %v", dir, scheduled, native)
		}
		if w.On.Any() != native {
			t.Fatalf("%s: Any() %v, want %v", dir, w.On.Any(), native)
		}
	}
	for _, declaration := range []string{"schedule: {cron: '* * * * *'}", "schedule: []", "schedule: [{other: x}]"} {
		if _, err := Parse(".github/workflows/bad.yml", []byte("on:\n  "+declaration+job)); err == nil {
			t.Fatalf("accepted %s", declaration)
		}
	}
}

func TestWorkflowRunTriggerIsLeftToGitHub(t *testing.T) {
	w, err := Parse(".github/workflows/report.yml", []byte("on:\n  workflow_run:\n    workflows: [tests]\n    types: [completed]\njobs:\n  report:\n    runs-on: ubuntu-latest\n    steps: [{run: true}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if w.On.Any() {
		t.Fatal("workflow_run must not create a Grain trigger")
	}
}

func TestSourceDoesNotHideMatchingLimitationsOrMalformedYAML(t *testing.T) {
	for _, body := range []string{
		"on: push\njobs:\n  test:\n    runs-on: windows-latest\n    steps: [{run: true}]\n",
		"on: push\njobs:\n  test:\n    uses: example/repo/.github/workflows/reuse.yml@main\n",
	} {
		ws, _, err := LoadDir(fstest.MapFS{".github/workflows/ci.yml": {Data: []byte(body)}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := PlanRun(Request{Repo: "o/r", Ref: "main", Event: EventPush}, ws); err == nil {
			t.Fatal("matching unsupported workflow was silently skipped")
		}
	}
	if _, _, err := LoadDir(fstest.MapFS{".github/workflows/ci.yml": {Data: []byte("on: [")}}); err == nil {
		t.Fatal("malformed YAML was silently retained")
	}
}

const granuleActionsWorkflow = `
name: CI
on:
  push:
    branches: [main]
  pull_request:
concurrency:
  group: ${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true
permissions:
  contents: read
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: go test ./...
  image:
    runs-on: ubuntu-latest
    strategy:
      fail-fast: false
      matrix:
        include:
          - variant: debian
          - variant: alpine
          - variant: debian12
    steps:
      - uses: actions/checkout@v4
      - uses: docker/setup-buildx-action@v3
      - if: github.event_name == 'push'
        uses: docker/build-push-action@v6
  guest-build:
    name: guest build under real KVM
    runs-on: ubuntu-latest
    timeout-minutes: 45
    steps:
      - uses: actions/checkout@v4
      - run: test -e /dev/kvm
`

func TestParseGitHubActionsLeavesActionsSemanticsToAct(t *testing.T) {
	w, err := Parse(".github/workflows/ci.yml", []byte(granuleActionsWorkflow))
	if err != nil {
		t.Fatalf("parsing Granule's workflow shape: %v", err)
	}
	if w.Name != "CI" || !w.On.Matches(EventPullRequest, "feature") {
		t.Fatalf("workflow metadata: %+v", w)
	}
	if len(w.Jobs) != 3 || w.Jobs[0].ID != "guest-build" || w.Jobs[1].ID != "image" || w.Jobs[2].ID != "test" {
		t.Fatalf("jobs should be sorted and matrices left within their job: %+v", w.Jobs)
	}
	guest := w.Jobs[0]
	if guest.DisplayName() != "guest build under real KVM" || guest.TimeoutMinutes != 45 {
		t.Errorf("job metadata: %+v", guest)
	}
	if guest.Actions == nil || guest.Actions.WorkflowPath != ".github/workflows/ci.yml" {
		t.Errorf("Actions source: %+v", guest.Actions)
	}
	if !guest.Cache {
		t.Error("an Actions job is cached like a native one unless it says otherwise")
	}
	if guest.Sandbox != (SandboxSpec{CPUs: 4, MemoryMB: 16384, DiskGB: 100}) {
		t.Errorf("GitHub-compatible sandbox shape: %+v", guest.Sandbox)
	}
	script := guest.Steps[0].Run
	for _, want := range []string{
		`act "$GRAIN_CI_EVENT"`,
		`--workflows '.github/workflows/ci.yml'`,
		`--job 'guest-build'`,
		`--platform ubuntu-latest=-self-hosted`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("runner script does not contain %q:\n%s", want, script)
		}
	}
}

func TestGitHubActionsCompatibilityFailsClosed(t *testing.T) {
	for name, tc := range map[string]struct {
		yaml string
		want string
	}{
		"another runner": {
			yaml: "on: push\njobs:\n  test:\n    runs-on: windows-latest\n    steps:\n      - run: true\n",
			want: "ubuntu-latest",
		},
		"runner expression": {
			yaml: "on: push\njobs:\n  test:\n    runs-on: ${{ matrix.os }}\n    steps:\n      - run: true\n",
			want: "ubuntu-latest",
		},
		"cross-job outputs": {
			yaml: "on: push\njobs:\n  first:\n    runs-on: ubuntu-latest\n    steps:\n      - run: true\n  second:\n    needs: first\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo ${{ needs.first.outputs.tag }}\n",
			want: "reads the `needs` context",
		},
		"cross-job artifacts": {
			yaml: "on: push\njobs:\n  first:\n    runs-on: ubuntu-latest\n    steps:\n      - run: true\n  second:\n    needs: first\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/download-artifact@v4\n",
			want: "downloads an artifact",
		},
		"needs a job that is not there": {
			yaml: "on: push\njobs:\n  second:\n    needs: first\n    runs-on: ubuntu-latest\n    steps:\n      - run: true\n",
			want: `needs "first", which is not a job in this workflow`,
		},
		"needs in a circle": {
			yaml: "on: push\njobs:\n  first:\n    needs: second\n    runs-on: ubuntu-latest\n    steps:\n      - run: true\n  second:\n    needs: first\n    runs-on: ubuntu-latest\n    steps:\n      - run: true\n",
			want: "needs form a cycle",
		},
		"reusable workflow": {
			yaml: "on: push\njobs:\n  test:\n    uses: owner/repo/.github/workflows/test.yml@main\n",
			want: "reusable-workflow jobs",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(".github/workflows/ci.yml", []byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestLoadDirFallsBackToGitHubActions(t *testing.T) {
	tree := fstest.MapFS{
		".github/workflows/ci.yml": &fstest.MapFile{Data: []byte(granuleActionsWorkflow)},
	}
	workflows, _, err := LoadDir(tree)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	if len(workflows) != 1 || workflows[0].Path != ".github/workflows/ci.yml" {
		t.Fatalf("loaded %+v", workflows)
	}
}

func TestLoadDirPrefersAnExplicitGrainWorkflowDirectory(t *testing.T) {
	tree := fstest.MapFS{
		".grain/workflows/tests.yml": &fstest.MapFile{Data: []byte("on: push\njobs:\n  test:\n    steps:\n      - run: go test ./...\n")},
		".github/workflows/ci.yml":   &fstest.MapFile{Data: []byte(granuleActionsWorkflow)},
	}
	workflows, _, err := LoadDir(tree)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	if len(workflows) != 1 || workflows[0].Path != ".grain/workflows/tests.yml" {
		t.Fatalf("loaded %+v", workflows)
	}
}

func TestTaskBranchUsesThePullRequestEvent(t *testing.T) {
	w, err := Parse(".github/workflows/ci.yml", []byte(granuleActionsWorkflow))
	if err != nil {
		t.Fatal(err)
	}
	event, matches := WorkflowEvent(w, Request{Ref: "grain/task-42", Event: EventPush})
	if !matches || event != EventPullRequest {
		t.Fatalf("event, matches = %q, %v; want pull_request, true", event, matches)
	}
	plan, err := PlanRun(Request{Repo: "o/r", Ref: "grain/task-42", Commit: "abc", Event: EventPush}, []Workflow{w})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Jobs) != 3 || !strings.Contains(plan.Jobs[0].Job.Steps[0].Run, "act 'pull_request'") {
		t.Fatalf("planned jobs do not execute the pull_request event: %+v", plan.Jobs)
	}
	if plan.Request.Event != EventPush {
		t.Fatalf("the capability event changed to %q; it must remain a push", plan.Request.Event)
	}
	event, matches = WorkflowEvent(w, Request{Ref: "main", Event: EventPush})
	if !matches || event != EventPush {
		t.Fatalf("main event, matches = %q, %v; want push, true", event, matches)
	}
}

// actionsCacheWorkflow is the smallest .github/workflows file that plans:
// one ubuntu-latest job with a step, which grain turns into one act
// invocation.
const actionsCacheWorkflow = "name: CI\non: [push]\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: go test ./...\n"

// The same Actions job on the same commit is work that was already done.
// It used to be re-done: the compatibility path hard-coded `Cache: false`,
// so a repository whose entire CI lives in .github/workflows ran every job
// of an unchanged commit in full, every time it was asked.
func TestEngineServesAnActionsJobFromCacheTheSecondTime(t *testing.T) {
	req := Request{Repo: "bwsalmon/granule", Ref: "main", Commit: "abc", Event: EventPush}
	files := map[string]string{".github/workflows/ci.yml": actionsCacheWorkflow}
	cache := newCannedCache()

	first := &fakeSandboxes{}
	plan := planFor(t, req, files)
	run := (&Engine{Sandboxes: first, Cache: cache}).Run(context.Background(), "r1", plan, nil)
	if run.Conclusion != ConclusionSuccess || first.sandboxes != 1 {
		t.Fatalf("first run = %q with %d sandboxes, want one successful execution", run.Conclusion, first.sandboxes)
	}
	if jobByID(t, run, "test").Cached {
		t.Fatal("the first run of a commit does the work")
	}
	saved, ok := cache.saved[plan.Jobs[0].Key]
	if !ok {
		t.Fatalf("an Actions job's verdict was not written back: %v", cache.saved)
	}
	cache.entries[plan.Jobs[0].Key] = saved

	// A second, identical request: same repo, same commit, same file --
	// planned from scratch, so the key is derived again rather than
	// carried over.
	second := &fakeSandboxes{}
	run = (&Engine{Sandboxes: second, Cache: cache}).Run(context.Background(), "r2", planFor(t, req, files), nil)
	if run.Conclusion != ConclusionSuccess {
		t.Fatalf("second run = %q", run.Conclusion)
	}
	if !jobByID(t, run, "test").Cached {
		t.Error("the second run of an unchanged commit is served the first one's verdict")
	}
	if second.sandboxes != 0 {
		t.Errorf("a cache hit builds no sandbox: built %d", second.sandboxes)
	}
}

// The opt-out exists in the compatibility path too, for the reason it
// exists in the native one: a job whose purpose is an external side
// effect has to happen again even when its verdict is known.
func TestActionsJobMayOptOutOfTheCache(t *testing.T) {
	for name, tc := range map[string]struct {
		field string
		want  bool
	}{
		"default":  {want: true},
		"enabled":  {field: "    cache: true\n", want: true},
		"disabled": {field: "    cache: false\n", want: false},
	} {
		t.Run(name, func(t *testing.T) {
			body := "on: push\njobs:\n  publish:\n    runs-on: ubuntu-latest\n" + tc.field +
				"    steps:\n      - run: ./publish\n"
			w, err := Parse(".github/workflows/release.yml", []byte(body))
			if err != nil {
				t.Fatalf("parsing: %v", err)
			}
			if w.Jobs[0].Actions == nil {
				t.Fatal("this file must take the GitHub Actions path")
			}
			if got := w.Jobs[0].Cache; got != tc.want {
				t.Errorf("Cache = %v, want %v", got, tc.want)
			}
		})
	}

	sandboxes := &fakeSandboxes{}
	cache := newCannedCache()
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Commit: "abc", Event: EventPush}, map[string]string{
		".github/workflows/release.yml": "on: push\njobs:\n  publish:\n    runs-on: ubuntu-latest\n" +
			"    cache: false\n    steps:\n      - run: ./publish\n",
	})
	cache.entries[plan.Jobs[0].Key] = JobRun{Conclusion: ConclusionSuccess}

	run := (&Engine{Sandboxes: sandboxes, Cache: cache}).Run(context.Background(), "r1", plan, nil)
	if run.Conclusion != ConclusionSuccess || sandboxes.sandboxes != 1 {
		t.Fatalf("an opted-out Actions job = %q with %d sandboxes, want a real execution", run.Conclusion, sandboxes.sandboxes)
	}
	if len(cache.lookedUp) != 0 || len(cache.saved) != 0 {
		t.Fatalf("an opted-out Actions job touched the cache: looked up %v, saved %v", cache.lookedUp, cache.saved)
	}
}

// What an Actions job does is the workflow file act reads out of the
// checkout, and the checkout is the commit -- so the commit is what has to
// carry an edit to that file into the key. Unlike a native job, whose
// steps are part of the Job this hashes, an Actions job's Job is a wrapper
// (one `act` invocation, a path, a job id and an event): editing the
// file's steps changes nothing in it, and everything in the commit.
func TestActionsJobKeyFollowsTheWorkflowFileThroughTheCommit(t *testing.T) {
	key := func(commit, body string) Key {
		t.Helper()
		req := Request{Repo: "bwsalmon/granule", Ref: "main", Commit: commit, Event: EventPush}
		plan := planFor(t, req, map[string]string{".github/workflows/ci.yml": body})
		if len(plan.Jobs) != 1 {
			t.Fatalf("planned %d jobs", len(plan.Jobs))
		}
		return plan.Jobs[0].Key
	}
	edited := strings.Replace(actionsCacheWorkflow, "go test ./...", "go test -race ./...", 1)
	if edited == actionsCacheWorkflow {
		t.Fatal("the edited fixture is the same file")
	}

	same := key("abc", actionsCacheWorkflow)
	if got := key("abc", actionsCacheWorkflow); got != same {
		t.Errorf("the same job on the same commit got two keys: %s and %s", same, got)
	}
	// An edited workflow file arrives as a new commit, and that is what
	// keeps the old verdict from being served for the new work.
	if got := key("def", edited); got == same {
		t.Error("a changed workflow file must not be served the verdict from before the edit")
	}
	// The commit alone is enough, edit or no edit.
	if got := key("def", actionsCacheWorkflow); got == same {
		t.Error("an Actions job is keyed on the commit")
	}
}
