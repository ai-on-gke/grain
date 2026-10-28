package ui_test

// The API surface of the grain CI workflows a deployment declares for a
// repo it does not control (grain/task-245): what a save accepts, what
// it refuses, and that a refusal happens here rather than at the run.
// What the *engine* then does with them -- reading both sides through
// ci.Layered -- is pinned in pkg/ci.

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// workflowYAML is one valid workflow, named so a test can tell two of
// them apart.
func workflowYAML(job string) string {
	return "name: " + job + "\non: push\njobs:\n  " + job + ":\n    steps:\n      - run: make test\n"
}

func TestSetRepoWorkflowsStoresTheSetAndReadsItBack(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	saved, err := c.SetRepoWorkflows(ctx, "acme/widgets", []ui.RepoWorkflow{
		{Path: ".grain/workflows/tests.yml", Text: workflowYAML("tests")},
		// A bare file name is what somebody types in a form, and the
		// directory is not theirs to choose.
		{Path: "lint.yml", Text: workflowYAML("lint")},
	})
	if err != nil {
		t.Fatalf("saving workflows: %v", err)
	}
	if saved.Dir != ".grain/workflows" {
		t.Fatalf("dir = %q, want the directory an in-repo workflow lives in", saved.Dir)
	}
	if len(saved.Workflows) != 2 {
		t.Fatalf("saved %d workflows, want 2: %+v", len(saved.Workflows), saved.Workflows)
	}
	if saved.Workflows[0].Path != ".grain/workflows/lint.yml" {
		t.Fatalf("first path = %q, want the bare name filled out, in path order", saved.Workflows[0].Path)
	}
	if saved.Workflows[0].Name != "lint" {
		t.Fatalf("first name = %q, want the workflow's own name as ci.Parse read it", saved.Workflows[0].Name)
	}

	read, err := c.RepoWorkflows(ctx, "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Workflows) != 2 || read.Workflows[1].Text != workflowYAML("tests") {
		t.Fatalf("re-read = %+v, want the two as stored, text and all", read.Workflows)
	}

	// The set is replaced rather than added to, which is also the only
	// way a workflow is deleted.
	cleared, err := c.SetRepoWorkflows(ctx, "acme/widgets", nil)
	if err != nil {
		t.Fatalf("clearing: %v", err)
	}
	if len(cleared.Workflows) != 0 {
		t.Fatalf("after clearing = %+v, want none", cleared.Workflows)
	}
}

// The whole point of parsing on save: a file that could never run is an
// error under the form somebody is still looking at, not a run that
// fails minutes later.
func TestSetRepoWorkflowsRefusesAFileThatDoesNotParse(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	_, err := c.SetRepoWorkflows(ctx, "acme/widgets", []ui.RepoWorkflow{
		{Path: "tests.yml", Text: "on: push\njobs:\n  a:\n    steps:\n      - uses: actions/checkout@v4\n"},
	})
	if err == nil {
		t.Fatal("a workflow using an action was accepted; grain has no `uses:`")
	}
	if !strings.Contains(err.Error(), "tests.yml") {
		t.Fatalf("error = %v, want the file named in it", err)
	}
	var invalid *ui.ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("error = %v, want a validation error: this is somebody's typing, not a fault", err)
	}

	// And nothing was written: a refused save leaves the repo declaring
	// what it declared before.
	read, err := c.RepoWorkflows(ctx, "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Workflows) != 0 {
		t.Fatalf("after a refused save = %+v, want nothing stored", read.Workflows)
	}
}

func TestSetRepoWorkflowsRefusesAnImpossiblePath(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	for _, path := range []string{
		"",
		".github/workflows/tests.yml",
		".grain/workflows/nested/tests.yml",
		".grain/workflows/../../etc/passwd",
	} {
		if _, err := c.SetRepoWorkflows(ctx, "acme/widgets",
			[]ui.RepoWorkflow{{Path: path, Text: workflowYAML("tests")}}); err == nil {
			t.Fatalf("%q was accepted as a workflow path", path)
		}
	}
}

// The path is the key the store and ci.Layered both identify a workflow
// by, so two files at one path is a question rather than a set.
func TestSetRepoWorkflowsRefusesTwoFilesAtOnePath(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	_, err := c.SetRepoWorkflows(ctx, "acme/widgets", []ui.RepoWorkflow{
		{Path: "tests.yml", Text: workflowYAML("one")},
		{Path: ".grain/workflows/tests.yml", Text: workflowYAML("two")},
	})
	if err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("got %v, want a refusal naming the collision", err)
	}
}

// A repo's workflows are a table of their own, so saving them must not
// disturb the row the other three per-repo settings share -- nor they
// these.
func TestRepoWorkflowsAndTheOtherRepoSettingsLeaveEachOtherAlone(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	if _, err := c.SetRepoSetupCommand(ctx, "acme/widgets", "make deps"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetRepoWorkflows(ctx, "acme/widgets",
		[]ui.RepoWorkflow{{Path: "tests.yml", Text: workflowYAML("tests")}}); err != nil {
		t.Fatal(err)
	}
	defaults, err := c.RepoDefaults(ctx, "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if defaults.SetupCommand != "make deps" {
		t.Fatalf("setupCommand = %q after saving workflows, want it untouched", defaults.SetupCommand)
	}
	if _, err := c.SetRepoPromptExtension(ctx, "acme/widgets", "Migrations live in db/."); err != nil {
		t.Fatal(err)
	}
	read, err := c.RepoWorkflows(ctx, "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Workflows) != 1 {
		t.Fatalf("workflows = %+v after saving a prompt extension, want the one still there", read.Workflows)
	}
}

func TestRepoWorkflowsGetAndPut(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPut, "/api/repos/acme/widgets/workflows",
		`{"workflows":[{"path":"tests.yml","text":"on: push\njobs:\n  tests:\n    steps:\n      - run: make test\n"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put status = %d, want 200: %s", rec.Code, rec.Body)
	}
	saved := decode[ui.RepoWorkflowsResponse](t, rec)
	if saved.Repo != "acme/widgets" || len(saved.Workflows) != 1 {
		t.Fatalf("put response = %+v, want acme/widgets with its one workflow", saved)
	}

	rec = do(t, srv, http.MethodGet, "/api/repos/acme/widgets/workflows", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := decode[ui.RepoWorkflowsResponse](t, rec); len(got.Workflows) != 1 ||
		got.Workflows[0].Path != ".grain/workflows/tests.yml" {
		t.Fatalf("get = %+v, want the workflow as saved", got)
	}

	rec = do(t, srv, http.MethodPut, "/api/repos/acme/widgets/workflows",
		`{"workflows":[{"path":"tests.yml","text":"not: a workflow"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status for an unparseable workflow = %d, want 400: %s", rec.Code, rec.Body)
	}
}
