package ci

import (
	"errors"
	"strings"
	"testing"
)

func mustParse(t *testing.T, path, body string) Workflow {
	t.Helper()
	w, err := Parse(path, []byte(body))
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	return w
}

func TestPlanRunTakesOnlyTheWorkflowsTheBranchAndEventMatch(t *testing.T) {
	pushOnly := mustParse(t, ".grain/workflows/a.yml",
		"on:\n  push:\n    branches: [main]\njobs:\n  one:\n    steps:\n      - run: true\n")
	manualOnly := mustParse(t, ".grain/workflows/b.yml",
		"on: workflow_dispatch\njobs:\n  two:\n    steps:\n      - run: true\n")

	plan, err := PlanRun(Request{Repo: "o/r", Ref: "main", Event: EventPush}, []Workflow{manualOnly, pushOnly})
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	if len(plan.Jobs) != 1 || plan.Jobs[0].Job.ID != "one" {
		t.Fatalf("a push to main runs the push workflow alone: got %+v", plan.Jobs)
	}

	plan, err = PlanRun(Request{Repo: "o/r", Ref: "other"}, []Workflow{manualOnly, pushOnly})
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	if len(plan.Jobs) != 1 || plan.Jobs[0].Job.ID != "two" {
		t.Fatalf("a request with no event of its own is a manual one: got %+v", plan.Jobs)
	}
}

func TestTaskPushRunsActionsPullRequestWorkflowsFromEitherDirectory(t *testing.T) {
	for _, workflowPath := range []string{
		".grain/workflows/tests.yml",
		".github/workflows/tests.yml",
	} {
		t.Run(workflowPath, func(t *testing.T) {
			workflow := mustParse(t, workflowPath, `
on:
  pull_request:
  push:
    branches: [main]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - run: go test ./...
`)
			plan, err := PlanRun(Request{
				Repo: "o/r", Ref: "grain/task-225", Event: EventPush,
			}, []Workflow{workflow})
			if err != nil {
				t.Fatalf("planning task CI: %v", err)
			}
			if len(plan.Jobs) != 1 {
				t.Fatalf("task CI jobs = %d, want 1", len(plan.Jobs))
			}
			if got := plan.Jobs[0].Job.Actions.Event; got != EventPullRequest {
				t.Fatalf("Actions event = %q, want %q", got, EventPullRequest)
			}
		})
	}
}

func TestPlanRunOrdersJobsAndResolvesNeedsAcrossFiles(t *testing.T) {
	a := mustParse(t, ".grain/workflows/a.yml",
		"on: push\njobs:\n  test:\n    needs: [build]\n    steps:\n      - run: true\n  build:\n    steps:\n      - run: true\n")
	b := mustParse(t, ".grain/workflows/b.yml",
		"on: push\njobs:\n  build:\n    steps:\n      - run: true\n")

	plan, err := PlanRun(Request{Repo: "o/r", Ref: "main", Event: EventPush}, []Workflow{b, a})
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	var ids []string
	for _, j := range plan.Jobs {
		ids = append(ids, j.ID)
	}
	want := []string{".grain/workflows/a.yml:build", ".grain/workflows/a.yml:test", ".grain/workflows/b.yml:build"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("plan order: got %v, want %v", ids, want)
	}
	// Two files each declaring "build" must not become one node, and a
	// `needs: build` must mean the one in its own file.
	if got := plan.Jobs[1].Needs; len(got) != 1 || got[0] != ".grain/workflows/a.yml:build" {
		t.Errorf("needs resolves within the workflow: got %v", got)
	}
}

func TestPlanRunCanSelectJobsWithoutDroppingTheirNeeds(t *testing.T) {
	w := mustParse(t, ".grain/workflows/tests.yml",
		"on: push\njobs:\n  build:\n    steps:\n      - run: build\n  test:\n    needs: build\n    steps:\n      - run: test\n  lint:\n    steps:\n      - run: lint\n")
	plan, err := PlanRun(Request{Repo: "o/r", Ref: "main", Event: EventPush, Jobs: []string{"test"}}, []Workflow{w})
	if err != nil {
		t.Fatalf("planning selected job: %v", err)
	}
	var ids []string
	for _, job := range plan.Jobs {
		ids = append(ids, job.Job.ID)
	}
	if got := strings.Join(ids, ","); got != "build,test" {
		t.Fatalf("selected jobs = %s, want the selection and its dependency", got)
	}
}

func TestPlanRunJobFilterSelectsTargetedJob(t *testing.T) {
	w := mustParse(t, ".grain/workflows/tests.yml",
		"on: push\njobs:\n  build:\n    steps:\n      - run: build\n  test:\n    needs: build\n    steps:\n      - run: test\n  lint:\n    steps:\n      - run: lint\n")
	plan, err := PlanRun(Request{Repo: "o/r", Ref: "main", Event: EventPush, JobFilter: "test"}, []Workflow{w})
	if err != nil {
		t.Fatalf("planning with JobFilter: %v", err)
	}
	var ids []string
	for _, job := range plan.Jobs {
		ids = append(ids, job.Job.ID)
	}
	if got := strings.Join(ids, ","); got != "build,test" {
		t.Fatalf("selected jobs = %s, want the selection and its dependency", got)
	}
}

func TestPlanRunSelectedJobNamesAreUnambiguousAndMustExist(t *testing.T) {
	a := mustParse(t, ".grain/workflows/a.yml", "on: push\njobs:\n  test:\n    steps:\n      - run: a\n")
	b := mustParse(t, ".grain/workflows/b.yml", "on: push\njobs:\n  test:\n    steps:\n      - run: b\n")
	req := Request{Repo: "o/r", Ref: "main", Event: EventPush, Jobs: []string{"test"}}
	if _, err := PlanRun(req, []Workflow{a, b}); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous bare id error = %v", err)
	}
	req.Jobs = []string{".grain/workflows/b.yml:test"}
	plan, err := PlanRun(req, []Workflow{a, b})
	if err != nil || len(plan.Jobs) != 1 || plan.Jobs[0].WorkflowPath != b.Path {
		t.Fatalf("qualified selection = %+v, %v", plan.Jobs, err)
	}
	req.Jobs = []string{"missing"}
	if _, err := PlanRun(req, []Workflow{a, b}); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing selected job error = %v", err)
	}
}

// A repository that declares workflows and matches none of them gets a
// plan with no jobs -- but it is not the silent one. Whoever reads the
// run has to be able to see that nothing ran and why, because a run of
// no jobs reported as a pass is what merged an untested tree
// (grain/task-254).
func TestPlanRunSaysWhyAMatchlessPlanHasNoJobs(t *testing.T) {
	w := mustParse(t, ".grain/workflows/a.yml",
		"on:\n  push:\n    branches: [main]\njobs:\n  one:\n    steps:\n      - run: true\n")
	plan, err := PlanRun(Request{Repo: "o/r", Ref: "topic", Event: EventPush}, []Workflow{w})
	if err != nil {
		t.Fatalf("a branch no workflow answers is not a planning error: %v", err)
	}
	if len(plan.Jobs) != 0 {
		t.Fatalf("expected no jobs: got %+v", plan.Jobs)
	}
	if plan.Key == "" {
		t.Error("even an empty plan has a key -- it is what a whole-run cache would be looked up by")
	}
	// The three things whoever reads this has to compare: the event and
	// branch the run was dispatched under, and what the file answers to.
	for _, want := range []string{"topic", string(EventPush), ".grain/workflows/a.yml", "push on main"} {
		if !strings.Contains(plan.Unmatched, want) {
			t.Errorf("unmatched reason does not mention %q:\n%s", want, plan.Unmatched)
		}
	}
}

// The one empty plan that stays a success, and the reason the case above
// is told apart from it rather than made red wholesale: adding
// `.grain/workflows` to one repository must not be a change to every
// other one (PlanRun).
func TestPlanRunIsSilentlyEmptyForARepositoryThatDeclaresNothing(t *testing.T) {
	plan, err := PlanRun(Request{Repo: "o/r", Ref: "topic", Event: EventPush}, nil)
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	if len(plan.Jobs) != 0 || plan.Unmatched != "" {
		t.Fatalf("a repository with no workflows at all has nothing to report: %+v / %q", plan.Jobs, plan.Unmatched)
	}
}

// The third empty plan, and the one an empty run is most likely to be
// next time: a workflow that answers this very event, and a plan holding
// no job of it. Nothing in the repository's triggers explains that -- the
// trigger matched -- so explaining it with the triggers, which is what
// every empty plan used to be explained with, sends whoever reads the run
// to `on:` keys that are doing their job (grain/task-388).
//
// A workflow parsed from a file always has at least one job (Parse
// refuses `jobs: {}`), which is why this is built by hand: the plan is
// the last place that can tell the two apart, and it has to say the true
// one whichever way the jobs went missing -- a workflow that kept its
// triggers and lost its jobs is the shape this has already taken once
// (parseForSource, and the case below).
func TestPlanRunTellsADroppedJobGraphFromAnUnmatchedTrigger(t *testing.T) {
	jobless := Workflow{Path: ".grain/workflows/a.yml", Name: "a", On: Triggers{Push: &BranchTrigger{}}}
	plan, err := PlanRun(Request{Repo: "o/r", Ref: "topic", Event: EventPush}, []Workflow{jobless})
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	if len(plan.Jobs) != 0 {
		t.Fatalf("expected no jobs: %+v", plan.Jobs)
	}
	if plan.Unmatched == "" {
		t.Fatal("a run that planned nothing is never silent when a workflow answered it")
	}
	if strings.Contains(plan.Unmatched, "no workflow of o/r answers") {
		t.Errorf("a workflow did answer; the reason must not blame the triggers:\n%s", plan.Unmatched)
	}
	// What the reader needs: that a workflow answered, which one, that it
	// contributed no job, and that this is grain's fault rather than the
	// branch's.
	for _, want := range []string{"1 workflow(s) of o/r answer push of branch topic",
		".grain/workflows/a.yml: no job at all", "fault in this deployment"} {
		if !strings.Contains(plan.Unmatched, want) {
			t.Errorf("dropped-jobs reason does not mention %q:\n%s", want, plan.Unmatched)
		}
	}
}

// A file grain cannot plan keeps its triggers and loses its jobs
// (parseForSource). Where such a file is the reason a repository has no
// jobs to run, the listing of "what each workflow answers to" would
// otherwise show it with an `on:` it could never have run anything for
// -- true, and the least useful true thing to say about it.
func TestPlanRunNamesAWorkflowThatCouldNotBePlannedAmongTheTriggers(t *testing.T) {
	broken, err := parseForSource(".github/workflows/ci.yml", []byte(
		"on:\n  schedule:\n    - cron: '0 3 * * *'\njobs:\n  test:\n    runs-on: ubuntu-latest\n"+
			"    steps:\n      - run: go test ./...\n  image:\n    runs-on: ubuntu-latest\n"+
			"    needs: test\n    steps:\n      - run: ./build ${{ needs.test.outputs.tag }}\n"))
	if err != nil {
		t.Fatalf("a job-level error is kept on the workflow rather than returned: %v", err)
	}
	if broken.PlanningError() == nil || len(broken.Jobs) != 0 {
		t.Fatalf("expected a workflow with triggers and no jobs: %v / %+v", broken.PlanningError(), broken.Jobs)
	}

	// A push, which this file does not answer either way: it is skipped,
	// and it is the only workflow there is, so the run is empty.
	plan, err := PlanRun(Request{Repo: "o/r", Ref: "topic", Event: EventPush}, []Workflow{broken})
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	if len(plan.Jobs) != 0 {
		t.Fatalf("expected no jobs: %+v", plan.Jobs)
	}
	for _, want := range []string{"no workflow of o/r answers push", ".github/workflows/ci.yml",
		"could not be planned", "needs"} {
		if !strings.Contains(plan.Unmatched, want) {
			t.Errorf("unmatched reason does not mention %q:\n%s", want, plan.Unmatched)
		}
	}
}

// Selecting jobs out of a plan that matched nothing used to be an error
// naming the selection -- "selected job "test" does not exist in the
// matching workflows" -- which says the symptom and hides the cause: the
// matching workflows were empty. The selection is carried into the
// explanation instead, and the caller gets a run rather than a 500.
func TestPlanRunExplainsSelectedJobsThatNothingCouldHaveMatched(t *testing.T) {
	w := mustParse(t, ".grain/workflows/a.yml",
		"on:\n  push:\n    branches: [main]\njobs:\n  one:\n    steps:\n      - run: true\n")
	req := Request{Repo: "o/r", Ref: "topic", Event: EventPush, Jobs: []string{"test"}}
	plan, err := PlanRun(req, []Workflow{w})
	if err != nil {
		t.Fatalf("selecting a job of a branch that matched nothing: %v", err)
	}
	if len(plan.Jobs) != 0 || !strings.Contains(plan.Unmatched, "test") {
		t.Fatalf("jobs = %+v, unmatched = %q", plan.Jobs, plan.Unmatched)
	}

	// A repository with no workflows at all is silent for a run that
	// asked for nothing in particular, and not for one that asked by
	// name: that caller wanted work done and none was.
	plan, err = PlanRun(req, nil)
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	if !strings.Contains(plan.Unmatched, "declares no workflows") {
		t.Fatalf("unmatched = %q", plan.Unmatched)
	}
}

// A .github workflow whose definition grain cannot plan keeps its
// triggers and loses its jobs (parseForSource), and the Actions event
// translation used to be read off those jobs -- so on every branch but
// the one its `push:` names, the file matched nothing at all and its
// error was never reported. The run came back green with no jobs, which
// is the worst of the three possible answers.
func TestPlanRunReportsAnActionsDefinitionErrorOnATaskBranchPush(t *testing.T) {
	broken, err := parseForSource(".github/workflows/ci.yml", []byte(`
on:
  pull_request:
  push:
    branches: [main]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - run: go test ./...
  image:
    runs-on: ubuntu-latest
    needs: test
    steps:
      - run: ./build ${{ needs.test.outputs.tag }}
`))
	if err != nil {
		t.Fatalf("a job-level error is kept on the workflow rather than returned: %v", err)
	}
	if broken.PlanningError() == nil {
		t.Fatal("expected a job reading the `needs` context to be a planning error")
	}
	if len(broken.Jobs) != 0 {
		t.Fatalf("a workflow that cannot be planned keeps no jobs: %+v", broken.Jobs)
	}

	_, err = PlanRun(Request{Repo: "o/r", Ref: "grain/task-246", Event: EventPush}, []Workflow{broken})
	if err == nil || !strings.Contains(err.Error(), "needs") {
		t.Fatalf("planning a task-branch push = %v, want the workflow's own definition error", err)
	}
}

func TestRequestValidateNamesWhatIsMissing(t *testing.T) {
	for name, req := range map[string]Request{
		"no repo":       {Ref: "main"},
		"no branch":     {Repo: "o/r"},
		"not a repo":    {Repo: "justaname", Ref: "main"},
		"unknown event": {Repo: "o/r", Ref: "main", Event: "release"},
	} {
		if err := (req).Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if err := (Request{Repo: "o/r", Ref: "refs/heads/main"}).Validate(); err != nil {
		t.Errorf("a fully qualified ref is a branch: %v", err)
	}
}

func TestFingerprintIsTheSameWorkTwiceAndDifferentWorkOnce(t *testing.T) {
	body := "on: push\nenv:\n  A: 1\njobs:\n  one:\n    steps:\n      - run: make test\n"
	w := mustParse(t, ".grain/workflows/a.yml", body)
	req := Request{Repo: "o/r", Ref: "main", Commit: "abc123", Event: EventPush}
	base := Fingerprint(req, w, w.Jobs[0])

	// Same everything, said differently: comments, key order and
	// quoting are not the work.
	same := mustParse(t, ".grain/workflows/a.yml",
		"# a comment\njobs:\n  one:\n    steps:\n      - run: \"make test\"\non: push\nenv:\n  A: \"1\"\n")
	if got := Fingerprint(req, same, same.Jobs[0]); got != base {
		t.Errorf("rewriting the file without changing the job changed its key:\n %s\n %s", base, got)
	}

	for name, mutate := range map[string]func(*Request, *Workflow){
		"another commit": func(r *Request, _ *Workflow) { r.Commit = "def456" },
		"another repo":   func(r *Request, _ *Workflow) { r.Repo = "o/other" },
		"another script": func(_ *Request, w *Workflow) { w.Jobs[0].Steps[0].Run = "make bench" },
		"another env":    func(_ *Request, w *Workflow) { w.Env = map[string]string{"A": "2"} },
		"another size":   func(_ *Request, w *Workflow) { w.Jobs[0].Sandbox = SandboxSpec{CPUs: 8} },
		"another file":   func(_ *Request, w *Workflow) { w.Path = ".grain/workflows/b.yml" },
		// The three below are the environment grain supplies: a step
		// reads GRAIN_CI_REF, GRAIN_CI_EVENT and GRAIN_CI_WORKFLOW, so a
		// job told something different is a job that may do something
		// different, whatever the tree says. See fingerprintVersion 2.
		"another branch":        func(r *Request, _ *Workflow) { r.Ref = "grain/task-185" },
		"another event":         func(r *Request, _ *Workflow) { r.Event = EventPullRequest },
		"another workflow":      func(_ *Request, w *Workflow) { w.Name = "renamed" },
		"another sandbox image": func(r *Request, _ *Workflow) { r.SandboxImage = "ghcr.io/bwsalmon/grain/guest:v2" },
	} {
		t.Run(name, func(t *testing.T) {
			r, changed := req, mustParse(t, ".grain/workflows/a.yml", body)
			mutate(&r, &changed)
			if got := Fingerprint(r, changed, changed.Jobs[0]); got == base {
				t.Errorf("%s should not share a key with the original", name)
			}
		})
	}

	// A request with no commit has nothing content-addressable about
	// it, so it must not collide with the same job at a known commit.
	noCommit := req
	noCommit.Commit = ""
	if Fingerprint(noCommit, w, w.Jobs[0]) == base {
		t.Error("an unresolved branch should not share a key with a commit")
	}
}

func TestPlanKeyCoversTheWorkAndNotTheAsking(t *testing.T) {
	w := mustParse(t, ".grain/workflows/a.yml", "on: push\njobs:\n  one:\n    steps:\n      - run: true\n")
	req := Request{Repo: "o/r", Ref: "main", Commit: "abc", Event: EventPush, RequestedBy: "operator"}
	first, err := PlanRun(req, []Workflow{w})
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	second := req
	second.RequestedBy = "somebody-else"
	other, err := PlanRun(second, []Workflow{w})
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	if first.Key != other.Key {
		t.Error("who asked is not part of what runs")
	}
}

func TestPlanRunSelectsOneWorkflowBeforeApplyingItsPlanningError(t *testing.T) {
	good := mustParse(t, ".github/workflows/ci.yml",
		"name: CI\non: workflow_dispatch\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: go test ./...\n")
	unsupported := mustParse(t, ".github/workflows/images.yml",
		"name: Images\non: workflow_dispatch\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - run: ./build\n")
	unsupported = unsupported.WithPlanningError(errors.New("unsupported workflow feature"))
	req := Request{
		Repo: "o/r", Ref: "main", Commit: "abc", Event: EventManual,
		Workflow: good.Path,
	}
	plan, err := PlanRun(req, []Workflow{unsupported, good})
	if err != nil {
		t.Fatalf("planning selected workflow: %v", err)
	}
	if len(plan.Jobs) != 1 || plan.Jobs[0].WorkflowPath != good.Path {
		t.Fatalf("selected plan = %+v", plan.Jobs)
	}

	req.Workflow = ".github/workflows/missing.yml"
	if _, err := PlanRun(req, []Workflow{unsupported, good}); err == nil {
		t.Fatal("a missing selected workflow was accepted")
	}
}

// A plan with no jobs has no job keys to be told apart by, which is why
// planKey folds in the ref unconditionally rather than leaving it to
// them. Nothing asserted that until now: without it every branch at one
// commit whose workflows match nothing would share a run key, and
// Service.Start deduplicates live runs by exactly that key -- so a
// second branch asking would be handed the first branch's run.
func TestPlanKeyOfAnEmptyPlanStillTellsBranchesApart(t *testing.T) {
	w := mustParse(t, ".grain/workflows/a.yml",
		"on:\n  push:\n    branches: [main]\njobs:\n  one:\n    steps:\n      - run: true\n")
	req := Request{Repo: "o/r", Ref: "topic-a", Commit: "abc", Event: EventPush}

	first, err := PlanRun(req, []Workflow{w})
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	other := req
	other.Ref = "topic-b"
	second, err := PlanRun(other, []Workflow{w})
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	if len(first.Jobs) != 0 || len(second.Jobs) != 0 {
		t.Fatalf("this test is about plans with no jobs: got %d and %d", len(first.Jobs), len(second.Jobs))
	}
	if first.Key == second.Key {
		t.Errorf("two branches at one commit share an empty run's key: %s", first.Key)
	}
}

// A recheck asks for the same jobs on the same commit as the run whose
// answer it doubts, so it has the same job keys -- and it must still be
// a run of its own, because Service.Start hands a request whose plan
// matches a live run that run instead of starting anything. Being
// answered with the run under suspicion is the one thing a confirmation
// must not be.
func TestARecheckIsNotTheSameRunAsTheOneItIsChecking(t *testing.T) {
	w := mustParse(t, ".grain/workflows/a.yml",
		"on: push\njobs:\n  one:\n    steps:\n      - run: true\n")
	req := Request{Repo: "o/r", Ref: "main", Commit: "abc", Event: EventPush}

	first, err := PlanRun(req, []Workflow{w})
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	recheck := req
	recheck.Recheck = true
	second, err := PlanRun(recheck, []Workflow{w})
	if err != nil {
		t.Fatalf("planning the recheck: %v", err)
	}
	if first.Key == second.Key {
		t.Errorf("a recheck shares the run key of the run it is checking: %s", first.Key)
	}
	// The *job* keys are deliberately identical: the recheck bypasses the
	// cache rather than addressing different work, so a key that changed
	// would hide the entry from everyone else too.
	if first.Jobs[0].Key != second.Jobs[0].Key {
		t.Errorf("a recheck changed the job's fingerprint: %s then %s",
			first.Jobs[0].Key, second.Jobs[0].Key)
	}
	// Two rechecks of the same plan are still one run, though, which is
	// what every caller that may ask twice depends on: a nightly firing
	// asked for again by the next daemon cycle joins the suite already
	// running rather than starting a second one beside it
	// (orchestrator.startScheduledWorkflowRun).
	again, err := PlanRun(recheck, []Workflow{w})
	if err != nil {
		t.Fatalf("planning the recheck again: %v", err)
	}
	if again.Key != second.Key {
		t.Errorf("asking for the same recheck twice planned two runs: %s then %s",
			second.Key, again.Key)
	}
}

func TestReleaseRunsRequireTheMergeReconciler(t *testing.T) {
	workflow := mustParse(t, ".grain/workflows/release.yml", "on: release\njobs:\n  publish:\n    steps:\n      - run: ./publish\n")
	request := Request{Repo: "o/r", Ref: "main", Commit: "abc", Event: EventRelease}
	if _, err := PlanRun(request, []Workflow{workflow}); err == nil {
		t.Fatal("a caller-created release run was accepted")
	}
	request.TrustedRelease = true
	plan, err := PlanRun(request, []Workflow{workflow})
	if err != nil || len(plan.Jobs) != 1 {
		t.Fatalf("trusted release plan = %+v, %v", plan, err)
	}
}

func TestTrustedReleaseRunsAnActionsPushWorkflowWithoutLosingItsTrustContext(t *testing.T) {
	workflow := mustParse(t, ".grain/workflows/release.yml",
		"on:\n  push:\n    branches: [main]\njobs:\n  publish:\n    runs-on: ubuntu-latest\n    steps:\n      - run: ./publish\n")
	request := Request{
		Repo: "o/r", Ref: "main", Commit: "abc", Event: EventRelease, TrustedRelease: true,
	}
	plan, err := PlanRun(request, []Workflow{workflow})
	if err != nil || len(plan.Jobs) != 1 {
		t.Fatalf("trusted Actions release plan = %+v, %v", plan, err)
	}
	if plan.Request.Event != EventRelease {
		t.Fatalf("plan security event = %q, want %q", plan.Request.Event, EventRelease)
	}
	if got := plan.Jobs[0].Job.Actions.Event; got != EventPush {
		t.Fatalf("event passed to act = %q, want %q", got, EventPush)
	}
}

// A selection names jobs by id, by plan id, or by the display name every
// report prints; a name the workflows lack is refused with the ones they
// have (grain/task-2124).
func TestSelectingJobsByNameAndRefusingAnUnknownOne(t *testing.T) {
	files := map[string]string{
		".grain/workflows/tests.yml": "on: push\njobs:\n" +
			"  go:\n    steps:\n      - run: true\n" +
			"  ui-e2e:\n    name: ui e2e\n    steps:\n      - run: true\n",
	}
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Commit: "abc", Event: EventPush, Jobs: []string{"ui e2e"}}, files)
	if len(plan.Jobs) != 1 || plan.Jobs[0].Job.ID != "ui-e2e" || plan.Jobs[0].DependencyOnly {
		t.Fatalf("selecting by display name planned %+v", plan.Jobs)
	}
	var workflows []Workflow
	for path, body := range files {
		workflows = append(workflows, mustParse(t, path, body))
	}
	_, err := PlanRun(Request{Repo: "o/r", Ref: "main", Commit: "abc", Event: EventPush, Jobs: []string{"nope"}}, workflows)
	if err == nil || !strings.Contains(err.Error(), `"nope" does not exist`) || !strings.Contains(err.Error(), "go, ui-e2e") {
		t.Fatalf("unknown job refused with %v, want the valid names listed", err)
	}
}

// A release no workflow answers is the one unmatched plan that is not a
// problem: the repository publishes nothing. It still says why it is
// empty -- NoWorkflowAnswered reads that -- and is marked so the engine
// concludes it skipped rather than red. Everything else that matched
// nothing keeps the errored verdict: a push, a release that asked for
// jobs by name, and a release beside a workflow that could not be
// planned, which may be the very `release` workflow that would have
// answered.
func TestPlanRunMarksOnlyAnUnansweredAutomaticReleaseAsNothingToPublish(t *testing.T) {
	w := mustParse(t, ".grain/workflows/v3.yml",
		"on: [push, pull_request, workflow_dispatch]\njobs:\n  one:\n    steps:\n      - run: true\n")
	release := Request{Repo: "o/r", Ref: "main", Commit: "abc", Event: EventRelease, TrustedRelease: true}
	plan, err := PlanRun(release, []Workflow{w})
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	if len(plan.Jobs) != 0 || !strings.HasPrefix(plan.Unmatched, NoWorkflowAnswersPrefix) {
		t.Fatalf("jobs = %+v, unmatched = %q", plan.Jobs, plan.Unmatched)
	}
	if !plan.NothingToPublish {
		t.Error("an automatic release no workflow answers has nothing to publish")
	}

	for name, req := range map[string]Request{
		"push":         {Repo: "o/r", Ref: "topic", Event: EventPush},
		"pull request": {Repo: "o/r", Ref: "topic", Event: EventPullRequest},
		"named jobs":   {Repo: "o/r", Ref: "main", Commit: "abc", Event: EventRelease, TrustedRelease: true, Jobs: []string{"publish"}},
	} {
		plan, err := PlanRun(req, []Workflow{mustParse(t, ".grain/workflows/a.yml",
			"on:\n  push:\n    branches: [main]\njobs:\n  one:\n    steps:\n      - run: true\n")})
		if err != nil {
			t.Fatalf("%s: planning: %v", name, err)
		}
		if plan.Unmatched == "" || plan.NothingToPublish {
			t.Errorf("%s: unmatched = %q, nothing to publish = %v; want an errored explanation",
				name, plan.Unmatched, plan.NothingToPublish)
		}
	}

	broken, err := parseForSource(".github/workflows/ci.yml", []byte(
		"on:\n  schedule:\n    - cron: '0 3 * * *'\njobs:\n  test:\n    runs-on: ubuntu-latest\n"+
			"    steps:\n      - run: go test ./...\n  image:\n    runs-on: ubuntu-latest\n"+
			"    needs: test\n    steps:\n      - run: ./build ${{ needs.test.outputs.tag }}\n"))
	if err != nil || broken.PlanningError() == nil {
		t.Fatalf("expected a workflow that could not be planned: %v / %v", err, broken.PlanningError())
	}
	plan, err = PlanRun(release, []Workflow{w, broken})
	if err != nil {
		t.Fatalf("planning beside a broken workflow: %v", err)
	}
	if plan.Unmatched == "" || plan.NothingToPublish {
		t.Errorf("a release beside an unplannable workflow: unmatched = %q, nothing to publish = %v",
			plan.Unmatched, plan.NothingToPublish)
	}
}
