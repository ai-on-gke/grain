package workflows

import (
	"strings"
	"testing"
	"time"
)

const goodWorkflow = `
name: tests
on:
  push:
    branches: ["main", "grain/**"]
  workflow_dispatch:
env:
  GOFLAGS: -mod=readonly
jobs:
  build:
    name: build it
    steps:
      - name: compile
        run: go build ./...
  test:
    needs: build
    sandbox:
      cpus: 4
      memory-mb: 8192
      disk-gb: 40
    timeout-minutes: 20
    env:
      SUITE: unit
    steps:
      - run: |
          go test ./...
        working-directory: sub
        continue-on-error: true
        timeout-minutes: 5
`

func TestParseReadsAWorkflowTheWayGitHubWouldSpellIt(t *testing.T) {
	w, err := Parse(".grain/workflows/tests.yml", []byte(goodWorkflow))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if w.Name != "tests" || w.Path != ".grain/workflows/tests.yml" {
		t.Errorf("name/path: got %q/%q", w.Name, w.Path)
	}
	if w.Env["GOFLAGS"] != "-mod=readonly" {
		t.Errorf("workflow env: got %v", w.Env)
	}
	if !w.On.Matches(EventPush, "grain/task-185") || !w.On.Matches(EventPush, "main") {
		t.Error("on.push should match main and grain/**")
	}
	if w.On.Matches(EventPush, "other") {
		t.Error("on.push should not match a branch outside its filter")
	}
	if !w.On.Matches(EventManual, "anything") {
		t.Error("workflow_dispatch should match any branch")
	}
	if w.On.Matches(EventPullRequest, "main") {
		t.Error("a workflow that does not name pull_request should not answer to it")
	}

	if len(w.Jobs) != 2 || w.Jobs[0].ID != "build" || w.Jobs[1].ID != "test" {
		t.Fatalf("jobs should be sorted by id: got %+v", w.Jobs)
	}
	build, test := w.Jobs[0], w.Jobs[1]
	if build.DisplayName() != "build it" || test.DisplayName() != "test" {
		t.Errorf("display names: %q, %q", build.DisplayName(), test.DisplayName())
	}
	if build.Timeout() != DefaultJobTimeout {
		t.Errorf("a job with no timeout gets the default: got %s", build.Timeout())
	}
	if test.Timeout() != 20*time.Minute {
		t.Errorf("timeout-minutes: got %s", test.Timeout())
	}
	if got := test.Needs; len(got) != 1 || got[0] != "build" {
		t.Errorf("a scalar `needs` reads as a one-element list: got %v", got)
	}
	if test.Sandbox != (SandboxSpec{CPUs: 4, MemoryMB: 8192, DiskGB: 40}) {
		t.Errorf("sandbox: got %+v", test.Sandbox)
	}
	if test.Env["SUITE"] != "unit" {
		t.Errorf("job env: got %v", test.Env)
	}
	step := test.Steps[0]
	if step.WorkingDirectory != "sub" || !step.ContinueOnError || step.TimeoutMinutes != 5 {
		t.Errorf("step: got %+v", step)
	}
	if step.DisplayName() != "go test ./..." {
		t.Errorf("an unnamed step is shown by its first line: got %q", step.DisplayName())
	}
}

func TestParseRefusesWhatItDoesNotImplement(t *testing.T) {
	for name, tc := range map[string]struct{ yaml, wants string }{
		"an action": {
			yaml:  "on: push\njobs:\n  a:\n    steps:\n      - uses: actions/checkout@v4\n",
			wants: "uses",
		},
		"another runner": {
			yaml:  "on: push\njobs:\n  a:\n    runs-on: windows-latest\n    steps:\n      - run: true\n",
			wants: "ubuntu-latest",
		},
		"an unknown top-level key": {
			yaml:  "on: push\npermissions:\n  contents: read\njobs:\n  a:\n    steps:\n      - run: true\n",
			wants: "permissions",
		},
		"a matrix": {
			yaml:  "on: push\njobs:\n  a:\n    strategy:\n      matrix:\n        go: [1, 2]\n    steps:\n      - run: true\n",
			wants: "strategy",
		},
		"a path filter": {
			yaml:  "on:\n  push:\n    paths: ['**.go']\njobs:\n  a:\n    steps:\n      - run: true\n",
			wants: "paths",
		},
		"an event grain has no notion of": {
			yaml:  "on: issue_comment\njobs:\n  a:\n    steps:\n      - run: true\n",
			wants: "issue_comment",
		},
		"no trigger at all": {
			yaml:  "jobs:\n  a:\n    steps:\n      - run: true\n",
			wants: "nothing would ever run this file",
		},
		"no jobs": {
			yaml:  "on: push\njobs: {}\n",
			wants: "at least one job",
		},
		"a job with no steps": {
			yaml:  "on: push\njobs:\n  a:\n    steps: []\n",
			wants: "at least one step",
		},
		"a step with no script": {
			yaml:  "on: push\njobs:\n  a:\n    steps:\n      - name: nothing\n",
			wants: "`run:` script",
		},
		"a need that is not there": {
			yaml:  "on: push\njobs:\n  a:\n    needs: [b]\n    steps:\n      - run: true\n",
			wants: "not a job in this workflow",
		},
		"a needs cycle": {
			yaml:  "on: push\njobs:\n  a:\n    needs: [b]\n    steps:\n      - run: true\n  b:\n    needs: [a]\n    steps:\n      - run: true\n",
			wants: "cycle",
		},
		"a negative timeout": {
			yaml:  "on: push\njobs:\n  a:\n    timeout-minutes: -1\n    steps:\n      - run: true\n",
			wants: "not a length of time",
		},
		"a second document": {
			yaml:  "on: push\njobs:\n  a:\n    steps:\n      - run: true\n---\non: push\njobs:\n  b:\n    steps:\n      - run: true\n",
			wants: "second YAML document",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(".grain/workflows/w.yml", []byte(tc.yaml))
			if err == nil {
				t.Fatalf("parsed something grain does not implement")
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("error should name %q: got %v", tc.wants, err)
			}
			if !strings.Contains(err.Error(), ".grain/workflows/w.yml") {
				t.Errorf("error should name the file it came from: got %v", err)
			}
		})
	}
}

func TestParseAcceptsEveryShapeOfOnGitHubDoes(t *testing.T) {
	steps := "\njobs:\n  a:\n    steps:\n      - run: true\n"
	for name, tc := range map[string]struct {
		on    string
		event Event
		match string
	}{
		"a scalar":         {"on: push", EventPush, "anything"},
		"a list":           {"on: [push, workflow_dispatch]", EventManual, "anything"},
		"an empty mapping": {"on:\n  pull_request:", EventPullRequest, "anything"},
		"a release":        {"on:\n  release:\n    branches: [main]", EventRelease, "main"},
		"branches-ignore":  {"on:\n  push:\n    branches-ignore: [main]", EventPush, "feature"},
	} {
		t.Run(name, func(t *testing.T) {
			w, err := Parse("w.yml", []byte(tc.on+steps))
			if err != nil {
				t.Fatalf("parsing: %v", err)
			}
			if !w.On.Matches(tc.event, tc.match) {
				t.Errorf("%s should answer to %s on %q", tc.on, tc.event, tc.match)
			}
		})
	}

	w, err := Parse("w.yml", []byte("on:\n  push:\n    branches-ignore: [main]"+steps))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if w.On.Matches(EventPush, "main") {
		t.Error("branches-ignore should keep main out")
	}
}

func TestJobsAreCachedByDefaultAndMayOptOut(t *testing.T) {
	for name, tc := range map[string]struct {
		field string
		want  bool
	}{
		"default":  {want: true},
		"enabled":  {field: "    cache: true\n", want: true},
		"disabled": {field: "    cache: false\n", want: false},
	} {
		t.Run(name, func(t *testing.T) {
			body := "on: push\njobs:\n  publish:\n" + tc.field + "    steps:\n      - run: ./publish\n"
			workflow, err := Parse(".grain/workflows/release.yml", []byte(body))
			if err != nil {
				t.Fatalf("parsing: %v", err)
			}
			if got := workflow.Jobs[0].Cache; got != tc.want {
				t.Errorf("Cache = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBranchPatternsMatchTheWayTheyLook(t *testing.T) {
	for _, tc := range []struct {
		pattern, branch string
		want            bool
	}{
		{"main", "main", true},
		{"main", "mainline", false},
		{"grain/*", "grain/task-1", true},
		{"grain/*", "grain/task/1", false},
		{"grain/**", "grain/task/1", true},
		{"**", "anything/at/all", true},
		{"*", "top", true},
		{"*", "not/top", false},
		{"release-*", "release-1.2", true},
	} {
		if got := matchBranch(tc.pattern, tc.branch); got != tc.want {
			t.Errorf("matchBranch(%q, %q) = %v, want %v", tc.pattern, tc.branch, got, tc.want)
		}
	}
	if !(&BranchTrigger{Branches: []string{"main"}}).Matches("refs/heads/main") {
		t.Error("a fully qualified ref should match the branch it names")
	}
}

// A job with no runs-on is native whatever a comment in the file says
// about runs-on: granule's e2e-ipv6.yml explained its missing key in a
// comment reading "# No runs-on: ...", and the textual check Parse used
// to dispatch on sent it to the Actions path, which refused it.
func TestParseDispatchesOnTheRunsOnKeyNotTheText(t *testing.T) {
	const native = `
name: IPv6 end to end
# runs-on: is left out below on purpose.
on:
  schedule:
    - cron: "30 3 * * *"
  workflow_dispatch:
jobs:
  e2e-ipv6:
    # No runs-on: grain runs the job natively in a sandbox of its own.
    timeout-minutes: 90
    sandbox:
      cpus: 4
      memory-mb: 16384
      disk-gb: 50
    steps:
      - run: |
          echo "runs-on: is only text here"
`
	w, err := Parse(".grain/workflows/e2e-ipv6.yml", []byte(native))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(w.Jobs) != 1 {
		t.Fatalf("jobs = %d, want 1", len(w.Jobs))
	}
	j := w.Jobs[0]
	if j.Actions != nil {
		t.Fatalf("job went to the Actions path; want it native")
	}
	if j.Sandbox != (SandboxSpec{CPUs: 4, MemoryMB: 16384, DiskGB: 50}) || j.TimeoutMinutes != 90 {
		t.Fatalf("sandbox = %+v, timeout = %d; want the declared 4/16384/50 and 90", j.Sandbox, j.TimeoutMinutes)
	}

	const actions = "name: CI\non: workflow_dispatch\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: go test ./...\n"
	w, err = Parse(".grain/workflows/ci.yml", []byte(actions))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if w.Jobs[0].Actions == nil {
		t.Fatalf("a job declaring runs-on was not given to the Actions path")
	}
}
