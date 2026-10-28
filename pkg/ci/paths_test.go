package ci

import (
	"context"
	"strings"
	"testing"
)

// The workflow every test below plans: three jobs, two of which say which
// files they read and one of which says nothing at all.
const pathsWorkflow = `on: push
jobs:
  docs:
    paths: [docs/**]
    steps:
      - run: make docs
  go:
    paths:
      - "**/*.go"
      - go.mod
      - "!ui/**"
    steps:
      - run: go test ./...
  everything:
    steps:
      - run: make all
`

// treeOf is a Tree written the way a test wants one: path to content
// hash. The hashes are made up, which is the point -- nothing here reads
// a file, and "the same content" is only ever "the same string".
func treeOf(files map[string]string) *Tree {
	tree := &Tree{}
	for path, blob := range files {
		tree.Files = append(tree.Files, TreeFile{Path: path, Blob: blob})
	}
	return tree
}

// baseTree is a repository with a bit of everything in it.
func baseTree() map[string]string {
	return map[string]string{
		"docs/design.md":  "d1",
		"docs/ci.md":      "d2",
		"go.mod":          "m1",
		"pkg/ci/plan.go":  "g1",
		"pkg/ci/paths.go": "g2",
		"ui/src/app.jsx":  "u1",
		"ui/src/app.go":   "u2",
		"Makefile":        "k1",
	}
}

func keysOf(t *testing.T, req Request, tree *Tree) map[string]Key {
	t.Helper()
	plan, err := PlanRunIn(req, []Workflow{mustParse(t, ".grain/workflows/tests.yml", pathsWorkflow)}, tree)
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	keys := map[string]Key{}
	for _, job := range plan.Jobs {
		keys[job.Job.ID] = job.Key
	}
	return keys
}

func TestAJobKeysOnTheFilesItDeclaresAndNotOnTheCommit(t *testing.T) {
	req := Request{Repo: "o/r", Ref: "main", Commit: "aaa", Event: EventPush}
	tree := treeOf(baseTree())

	first := keysOf(t, req, tree)

	// The same tree at a different commit on a different branch: every
	// job that declared what it reads is the same work.
	moved := req
	moved.Commit = "bbb"
	moved.Ref = "grain/task-293"
	second := keysOf(t, moved, tree)

	for _, id := range []string{"docs", "go"} {
		if first[id] != second[id] {
			t.Errorf("job %q: a declared-paths key moved with the commit and the branch: %s vs %s", id, first[id], second[id])
		}
	}
	if first["everything"] == second["everything"] {
		t.Error("a job that declares nothing still keys on the whole tree, so its key follows the commit")
	}
}

func TestOnlyTheJobsWhoseFilesChangedGetANewKey(t *testing.T) {
	req := Request{Repo: "o/r", Ref: "main", Commit: "aaa", Event: EventPush}
	before := keysOf(t, req, treeOf(baseTree()))

	// A commit that touches only docs/.
	docsOnly := baseTree()
	docsOnly["docs/ci.md"] = "d2-edited"
	next := req
	next.Commit = "bbb"
	after := keysOf(t, next, treeOf(docsOnly))

	if before["docs"] == after["docs"] {
		t.Error("the docs job reads docs/, so a docs-only commit is new work for it")
	}
	if before["go"] != after["go"] {
		t.Error("the go job does not read docs/, so a docs-only commit is a cache hit for it")
	}

	// And a commit that touches only a Go file.
	goOnly := baseTree()
	goOnly["pkg/ci/plan.go"] = "g1-edited"
	next.Commit = "ccc"
	afterGo := keysOf(t, next, treeOf(goOnly))
	if before["go"] == afterGo["go"] {
		t.Error("the go job reads pkg/**, so a Go commit is new work for it")
	}
	if before["docs"] != afterGo["docs"] {
		t.Error("the docs job does not read pkg/, so a Go commit is a cache hit for it")
	}
}

func TestANegatedPatternTakesFilesBackOutOfADeclaration(t *testing.T) {
	req := Request{Repo: "o/r", Ref: "main", Commit: "aaa", Event: EventPush}
	before := keysOf(t, req, treeOf(baseTree()))

	// ui/src/app.go matches `**/*.go` and is then taken back out by
	// `!ui/**`, which comes after it.
	uiOnly := baseTree()
	uiOnly["ui/src/app.go"] = "u2-edited"
	next := req
	next.Commit = "bbb"
	after := keysOf(t, next, treeOf(uiOnly))

	if before["go"] != after["go"] {
		t.Error("the go job excludes ui/, so a ui-only commit is a cache hit for it")
	}
}

// The two-sided case, which is the whole reason the list is ordered:
// everything, except docs, except the three files under docs that a Go
// test really does read.
func TestPathsIgnoreIsEverythingExceptThoseAndTakesANegationOfItsOwn(t *testing.T) {
	w := mustParse(t, ".grain/workflows/a.yml",
		"on: push\njobs:\n  go:\n    paths-ignore:\n      - docs/**\n      - \"!docs/claude-surface.md\"\n"+
			"    steps:\n      - run: go test ./...\n")
	req := Request{Repo: "o/r", Ref: "main", Commit: "aaa", Event: EventPush}
	tree := baseTree()
	tree["docs/claude-surface.md"] = "c1"

	key := func(files map[string]string, commit string) Key {
		r := req
		r.Commit = commit
		plan, err := PlanRunIn(r, []Workflow{w}, treeOf(files))
		if err != nil {
			t.Fatalf("planning: %v", err)
		}
		if !plan.Jobs[0].KeyedOnPaths() {
			t.Fatal("`paths-ignore:` on its own is a declaration too")
		}
		return plan.Jobs[0].Key
	}
	before := key(tree, "aaa")

	ordinaryDocs := map[string]string{}
	for k, v := range tree {
		ordinaryDocs[k] = v
	}
	ordinaryDocs["docs/design.md"] = "d1-edited"
	if key(ordinaryDocs, "bbb") != before {
		t.Error("an ordinary docs change is excluded, so it is a cache hit")
	}

	capture := map[string]string{}
	for k, v := range tree {
		capture[k] = v
	}
	capture["docs/claude-surface.md"] = "c1-edited"
	if key(capture, "ccc") == before {
		t.Error("a file the ignore list put back is read again, so it is new work")
	}

	code := map[string]string{}
	for k, v := range tree {
		code[k] = v
	}
	code["pkg/ci/plan.go"] = "g1-edited"
	if key(code, "ddd") == before {
		t.Error("`paths-ignore:` means everything else, so a code change is new work")
	}
}

func TestAFileAppearingOrVanishingIsANewKey(t *testing.T) {
	req := Request{Repo: "o/r", Ref: "main", Commit: "aaa", Event: EventPush}
	before := keysOf(t, req, treeOf(baseTree()))

	added := baseTree()
	added["pkg/ci/new_test.go"] = "g1" // the same content as an existing file
	next := req
	next.Commit = "bbb"
	if after := keysOf(t, next, treeOf(added)); before["go"] == after["go"] {
		t.Error("a new file the job reads is new work, whatever is in it")
	}

	removed := baseTree()
	delete(removed, "pkg/ci/plan.go")
	if after := keysOf(t, next, treeOf(removed)); before["go"] == after["go"] {
		t.Error("a file the job read having gone is new work")
	}
}

func TestADeclarationThatMatchesNothingKeysOnTheCommit(t *testing.T) {
	// A `paths:` with a typo in it would otherwise hash an empty set of
	// files -- one key, for every commit there will ever be.
	w := mustParse(t, ".grain/workflows/a.yml",
		"on: push\njobs:\n  typo:\n    paths: [terrafrom/**]\n    steps:\n      - run: true\n")
	req := Request{Repo: "o/r", Ref: "main", Commit: "aaa", Event: EventPush}
	tree := treeOf(baseTree())

	plan, err := PlanRunIn(req, []Workflow{w}, tree)
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	if plan.Jobs[0].KeyedOnPaths() {
		t.Fatal("a declaration that matched nothing must fall back to the commit")
	}
	next := req
	next.Commit = "bbb"
	other, err := PlanRunIn(next, []Workflow{w}, tree)
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	if plan.Jobs[0].Key == other.Jobs[0].Key {
		t.Error("a job that matched no file is keyed on the commit, so a new commit is a new key")
	}
}

func TestNoTreeKeysEveryJobOnTheCommit(t *testing.T) {
	req := Request{Repo: "o/r", Ref: "main", Commit: "aaa", Event: EventPush}
	first := keysOf(t, req, nil)
	next := req
	next.Commit = "bbb"
	second := keysOf(t, next, nil)

	for id := range first {
		if first[id] == second[id] {
			t.Errorf("job %q: with no tree to read, every job keys on the commit", id)
		}
	}
	// And PlanRun, which has no tree by construction, agrees with it.
	plain, err := PlanRun(req, []Workflow{mustParse(t, ".grain/workflows/tests.yml", pathsWorkflow)})
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	for _, job := range plain.Jobs {
		if first[job.Job.ID] != job.Key {
			t.Errorf("job %q: PlanRun and PlanRunIn(nil) disagree", job.Job.ID)
		}
	}
}

func TestEditingAJobsPathsInvalidatesThatJobAndNoOther(t *testing.T) {
	req := Request{Repo: "o/r", Ref: "main", Commit: "aaa", Event: EventPush}
	tree := treeOf(baseTree())
	before := keysOf(t, req, tree)

	edited := strings.Replace(pathsWorkflow, "    paths: [docs/**]", "    paths: [docs/**, README.md]", 1)
	if edited == pathsWorkflow {
		t.Fatal("the test's own edit did not apply")
	}
	plan, err := PlanRunIn(req, []Workflow{mustParse(t, ".grain/workflows/tests.yml", edited)}, tree)
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	after := map[string]Key{}
	for _, job := range plan.Jobs {
		after[job.Job.ID] = job.Key
	}

	if before["docs"] == after["docs"] {
		t.Error("a job whose paths were edited is different work")
	}
	for _, id := range []string{"go", "everything"} {
		if before[id] != after[id] {
			t.Errorf("job %q: editing another job's paths is not a change to this one", id)
		}
	}
}

// The whole point, end to end: a commit that changes nothing any job
// reads runs no job at all, and the run is a success the merge gate
// accepts rather than a skip it refuses.
func TestARunWhoseDeclaredFilesAreUnchangedRunsNothingAndPasses(t *testing.T) {
	workflows := []Workflow{mustParse(t, ".grain/workflows/tests.yml",
		"on: push\njobs:\n  go:\n    paths: [\"**/*.go\"]\n    steps:\n      - run: go test ./...\n")}
	tree := treeOf(baseTree())
	cache := newCannedCache()

	// The first commit: nothing cached, so the job runs and is saved.
	first, err := PlanRunIn(Request{Repo: "o/r", Ref: "main", Commit: "aaa", Event: EventPush}, workflows, tree)
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	sandboxes := &fakeSandboxes{}
	run := (&Engine{Sandboxes: sandboxes, Cache: cache}).Run(context.Background(), "r1", first, nil)
	if run.Conclusion != ConclusionSuccess {
		t.Fatalf("first run: %q", run.Conclusion)
	}
	if sandboxes.sandboxes != 1 {
		t.Fatalf("first run built %d sandboxes, want 1", sandboxes.sandboxes)
	}
	for key, result := range cache.saved {
		cache.entries[key] = result
	}

	// The next commit touches only docs/ -- a different tree, a different
	// commit, and no file this job reads.
	docsOnly := baseTree()
	docsOnly["docs/ci.md"] = "d2-edited"
	second, err := PlanRunIn(Request{Repo: "o/r", Ref: "main", Commit: "bbb", Event: EventPush},
		workflows, treeOf(docsOnly))
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	sandboxes = &fakeSandboxes{}
	run = (&Engine{Sandboxes: sandboxes, Cache: cache}).Run(context.Background(), "r2", second, nil)

	if sandboxes.sandboxes != 0 {
		t.Errorf("a commit touching nothing the job reads built %d sandboxes, want 0", sandboxes.sandboxes)
	}
	if run.Conclusion != ConclusionSuccess {
		t.Fatalf("second run: %q -- the gate requires a success, and a skip would never merge", run.Conclusion)
	}
	job := jobByID(t, run, "go")
	if !job.Cached {
		t.Error("the job says where its green came from")
	}
	if job.Conclusion != ConclusionSuccess {
		t.Errorf("job conclusion %q: a job nothing it reads has touched passes, it is not skipped", job.Conclusion)
	}
	// And the run is recorded against the commit that was asked about,
	// which is what the merge gate matches on.
	if run.Commit != "bbb" {
		t.Errorf("run commit %q, want the commit the run was asked about", run.Commit)
	}
}

func TestPathFiltersAreRefusedAtParseWhereTheyCouldOnlyMatchNothing(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"on: push\njobs:\n  a:\n    paths: [/etc/passwd]\n    steps:\n      - run: true\n", "absolute"},
		{"on: push\njobs:\n  a:\n    paths: [\"../elsewhere/**\"]\n    steps:\n      - run: true\n", "climbs out"},
		{"on: push\njobs:\n  a:\n    paths: [\"\"]\n    steps:\n      - run: true\n", "empty path filter"},
		{"on: push\njobs:\n  a:\n    paths: [docs/**]\n    paths-ignore: [ui/**]\n    steps:\n      - run: true\n", "one or the other"},
	} {
		_, err := Parse(".grain/workflows/a.yml", []byte(tc.body))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("parsing %q: got %v, want an error mentioning %q", tc.body, err, tc.want)
		}
	}
}

func TestAPathsDeclarationParsesAsGitHubSpellsOne(t *testing.T) {
	w := mustParse(t, ".grain/workflows/a.yml",
		"on: push\njobs:\n  a:\n    paths: terraform/**\n    steps:\n      - run: true\n")
	if got := w.Jobs[0].Paths; len(got) != 1 || got[0] != "terraform/**" {
		t.Errorf("paths: %v -- a lone string is a list of one, as `needs` is", got)
	}
}

// A docs-only commit started while an earlier run's slow `go` job is still
// in flight on the same content key must wait for that in-flight job to
// finish and replay its cached verdict, rather than missing DirCache for
// only the still-running job and starting a duplicate 47-minute sandbox
// (grain/task-243: run 08bb81eb36ead038 -> 24cd71775c290ee5, where all 9
// faster sibling jobs had already finished and hit DirCache while `go` was
// still running).
func TestADocsOnlyCommitStartedWhileGoIsStillInFlightReplaysGoWhenItFinishes(t *testing.T) {
	workflows := []Workflow{mustParse(t, ".grain/workflows/tests.yml", `on: push
jobs:
  lint:
    paths-ignore: &not-read-by-the-go-program
      - docs/**
      - "!docs/*-surface.md"
    steps:
      - run: golangci-lint run
  go:
    paths-ignore: *not-read-by-the-go-program
    steps:
      - run: go test ./...
`)}
	cache, err := OpenDirCache(t.TempDir(), DirCacheOptions{})
	if err != nil {
		t.Fatalf("OpenDirCache: %v", err)
	}

	goStarted := make(chan struct{}, 1)
	releaseGo := make(chan struct{})
	sandboxes := &fakeSandboxes{
		script: func(cmd Command) (CommandResult, error) {
			if strings.Contains(cmd.Script, "go test") {
				signal(goStarted, struct{}{})
				<-releaseGo
			}
			return CommandResult{}, nil
		},
	}
	engine := &Engine{Sandboxes: sandboxes, Cache: cache}

	// Run 1 (08bb81eb36ead038): touches Go code; both `lint` and `go` run.
	// `lint` finishes immediately and writes its entry to DirCache while
	// `go` stays in flight.
	first, err := PlanRunIn(
		Request{Repo: "o/r", Ref: "grain/task-237", Commit: "c779f67", Event: EventPush},
		workflows,
		treeOf(baseTree()),
	)
	if err != nil {
		t.Fatalf("planning first run: %v", err)
	}
	lintCached := make(chan struct{}, 1)
	r1Done := make(chan Run, 1)
	go func() {
		r1Done <- engine.Run(context.Background(), "08bb81eb36ead038", first, func(job JobRun) {
			if job.JobID == "lint" && job.Conclusion == ConclusionSuccess {
				signal(lintCached, struct{}{})
			}
		})
	}()
	awaitSignal(t, lintCached, "first run's fast sibling job (lint) finished and cached")
	awaitSignal(t, goStarted, "first run's slow go job in flight")

	// Run 2 (24cd71775c290ee5): a docs-only commit started while Run 1's
	// `go` job is still running. `lint` hits DirCache immediately; `go`
	// shares the exact same content key and waits for Run 1's `go` job
	// instead of starting a second sandbox.
	docsOnly := baseTree()
	docsOnly["docs/capabilities.md"] = "caps-edited"
	docsOnly["docs/capabilities-spec.md"] = "caps-spec-edited"
	second, err := PlanRunIn(
		Request{Repo: "o/r", Ref: "grain/task-242", Commit: "3f04a14", Event: EventPush},
		workflows,
		treeOf(docsOnly),
	)
	if err != nil {
		t.Fatalf("planning second run: %v", err)
	}

	r2WaitingOnGo := make(chan JobRun, 1)
	r2Done := make(chan Run, 1)
	go func() {
		r2Done <- engine.Run(context.Background(), "24cd71775c290ee5", second, func(job JobRun) {
			if job.JobID == "go" && job.Activity != "" {
				signal(r2WaitingOnGo, job)
			}
		})
	}()

	waiting := awaitSignal(t, r2WaitingOnGo, "second run reports waiting on first run's in-flight go job")
	if !strings.Contains(waiting.Activity, "08bb81eb36ead038") {
		t.Errorf("waiting activity %q should name the in-flight run 08bb81eb36ead038", waiting.Activity)
	}
	if waiting.Sandbox != "" || waiting.StartedAt != nil {
		t.Errorf("waiting job must not hold a sandbox or a start time: %+v", waiting)
	}

	// Let Run 1's `go` job finish; both runs complete, and Run 2 replays
	// `go` from the cache without ever acquiring a sandbox of its own.
	close(releaseGo)
	r1 := awaitSignal(t, r1Done, "first run finished")
	r2 := awaitSignal(t, r2Done, "second run finished")

	if r1.Conclusion != ConclusionSuccess || r2.Conclusion != ConclusionSuccess {
		t.Fatalf("conclusions: r1=%q r2=%q, want both %q", r1.Conclusion, r2.Conclusion, ConclusionSuccess)
	}
	if sandboxes.sandboxes != 2 {
		t.Errorf("built %d sandboxes across both runs, want 2 (only the first run's lint and go)", sandboxes.sandboxes)
	}
	for _, id := range []string{"lint", "go"} {
		job := jobByID(t, r2, id)
		if !job.Cached {
			t.Errorf("second run's %s job: Cached=false, want replayed from cache", id)
		}
		if job.Conclusion != ConclusionSuccess {
			t.Errorf("second run's %s job: conclusion=%q, want %q", id, job.Conclusion, ConclusionSuccess)
		}
		if job.Queued != 0 || job.Activity != "" || job.QueuedReason != "" {
			t.Errorf("second run's %s job should carry the replayed result cleanly: Queued=%v Activity=%q QueuedReason=%q",
				id, job.Queued, job.Activity, job.QueuedReason)
		}
	}
}

// If the in-flight run is cancelled before saving a cacheable verdict, a
// second run waiting on the same content key wakes up, claims the key and
// runs the job itself rather than failing or hanging.
func TestAWaitingRunTakesOverWhenTheInFlightJobIsCancelled(t *testing.T) {
	workflows := []Workflow{mustParse(t, ".grain/workflows/tests.yml",
		"on: push\njobs:\n  go:\n    paths: [\"**/*.go\"]\n    steps:\n      - run: go test ./...\n")}
	cache, err := OpenDirCache(t.TempDir(), DirCacheOptions{})
	if err != nil {
		t.Fatalf("OpenDirCache: %v", err)
	}

	sandboxes := &fakeSandboxes{
		execs: make(chan Command, 2),
		block: make(chan struct{}),
	}
	engine := &Engine{Sandboxes: sandboxes, Cache: cache}

	first, err := PlanRunIn(
		Request{Repo: "o/r", Ref: "grain/task-1", Commit: "aaa", Event: EventPush},
		workflows,
		treeOf(baseTree()),
	)
	if err != nil {
		t.Fatalf("planning first run: %v", err)
	}
	docsOnly := baseTree()
	docsOnly["docs/ci.md"] = "edited"
	second, err := PlanRunIn(
		Request{Repo: "o/r", Ref: "grain/task-2", Commit: "bbb", Event: EventPush},
		workflows,
		treeOf(docsOnly),
	)
	if err != nil {
		t.Fatalf("planning second run: %v", err)
	}

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	r1Done := make(chan Run, 1)
	go func() {
		r1Done <- engine.Run(ctx1, "r1", first, nil)
	}()
	awaitSignal(t, sandboxes.execs, "r1 started executing go job")

	r2Waiting := make(chan struct{}, 1)
	r2Done := make(chan Run, 1)
	go func() {
		r2Done <- engine.Run(context.Background(), "r2", second, func(job JobRun) {
			if job.JobID == "go" && job.Activity != "" {
				signal(r2Waiting, struct{}{})
			}
		})
	}()
	awaitSignal(t, r2Waiting, "r2 waiting on r1's in-flight go job")

	// Cancel r1 while its step is blocked, then unblock the sandbox so r2's
	// takeover execution can complete.
	cancel1()
	r1 := awaitSignal(t, r1Done, "r1 finished after cancel")
	if r1.Conclusion != ConclusionCancelled {
		t.Fatalf("r1 conclusion: got %q, want %q", r1.Conclusion, ConclusionCancelled)
	}
	close(sandboxes.block)

	r2 := awaitSignal(t, r2Done, "r2 took over and finished")
	if r2.Conclusion != ConclusionSuccess {
		t.Fatalf("r2 conclusion: got %q, want %q", r2.Conclusion, ConclusionSuccess)
	}
	job := jobByID(t, r2, "go")
	if job.Cached {
		t.Error("r2 had to run the job itself after r1 was cancelled, so Cached must be false")
	}
	if sandboxes.sandboxes != 2 {
		t.Errorf("built %d sandboxes, want 2 (r1's cancelled attempt + r2's takeover)", sandboxes.sandboxes)
	}
}
