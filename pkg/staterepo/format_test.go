package staterepo_test

// Formatting a repository nobody has seeded yet, and the CI step that
// comes with it. The properties worth pinning are all about what
// formatting does *not* do -- it writes no dump, it commits nothing, and
// it does not overwrite a workflow somebody has edited -- because each
// of those is a decision the rest of this package depends on.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/staterepo"
)

func workflowPath(dir string) string {
	return filepath.Join(dir, filepath.FromSlash(staterepo.WorkflowFile))
}

func TestFormatWritesTheThreeFilesThatAreNotTheDump(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	formatted, err := staterepo.Format(dir, "", false)
	if err != nil {
		t.Fatalf("formatting: %v", err)
	}
	for _, name := range []string{staterepo.ReadmeFile, staterepo.IgnoreFile, staterepo.WorkflowFile} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			t.Errorf("%s was not written: %v", name, err)
		}
		if !strings.Contains(strings.Join(formatted.Wrote, " "), name) {
			t.Errorf("Format did not report writing %s: %v", name, formatted.Wrote)
		}
	}
	if len(formatted.Left) != 0 {
		t.Errorf("nothing was there to leave alone, but Format reported %v", formatted.Left)
	}
	// The .gitignore is the one EnsureIgnored writes, not a second answer
	// to the same question: a stray secrets file must not be committable
	// in a repository grain has only formatted either.
	ignore, err := os.ReadFile(filepath.Join(dir, staterepo.IgnoreFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ignore), staterepo.SecretsFile) {
		t.Errorf(".gitignore does not name %s:\n%s", staterepo.SecretsFile, ignore)
	}
}

// The whole reason formatting writes no tables/: a formatted repository
// has to stay the bootstrap's "empty repository grain seeds from what it
// has", or adopting one would replace a deployment's database with
// nothing at all.
func TestFormatWritesNoDumpSoAdoptingStillSeeds(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := staterepo.Format(dir, "", false); err != nil {
		t.Fatalf("formatting: %v", err)
	}
	if staterepo.HasDump(dir) {
		t.Fatal("a formatted repository claims to hold a dump; adopting it would import it over a database")
	}
	if v, err := staterepo.ReadSchemaVersion(dir); err != nil || v != 0 {
		t.Fatalf("a formatted repository is stamped with a schema (%d, %v); nothing has written rows for it yet", v, err)
	}

	// And the seed that follows lands on top of it without complaint,
	// which is the sequence an operator actually runs: format the clone,
	// push it, point a deployment at it.
	ctx := context.Background()
	store, db := openDB(t)
	if err := store.PutTask(ctx, task("a1b2")); err != nil {
		t.Fatalf("putting a task: %v", err)
	}
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir})
	if err != nil {
		t.Fatalf("opening the formatted directory as a repository: %v", err)
	}
	if err := staterepo.Seed(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("seeding a formatted repository: %v", err)
	}
	if !staterepo.HasDump(dir) {
		t.Fatal("the seed wrote no dump")
	}
	// The workflow survives the seed: an export writes tables/ and
	// nothing else, so the CI step an operator installed is still there
	// once grain owns the repository.
	if _, err := os.Stat(workflowPath(dir)); err != nil {
		t.Errorf("the seed lost the workflow: %v", err)
	}
}

func TestFormatIsSafeToRunTwice(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := staterepo.Format(dir, "", false); err != nil {
		t.Fatalf("formatting: %v", err)
	}
	before, err := os.ReadFile(workflowPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	formatted, err := staterepo.Format(dir, "", false)
	if err != nil {
		t.Fatalf("formatting again: %v", err)
	}
	after, err := os.ReadFile(workflowPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("formatting twice rewrote the workflow")
	}
	if len(formatted.Left) != 1 || formatted.Left[0] != staterepo.WorkflowFile {
		t.Errorf("the second format did not report leaving the workflow alone: %v", formatted.Left)
	}
}

// A workflow an operator has edited -- a different runner, a different
// trigger, a step of their own -- is theirs, and a command that "adds a
// CI step" must not silently take it back.
func TestTheWorkflowIsNotOverwrittenWithoutForce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := staterepo.Format(dir, "", false); err != nil {
		t.Fatalf("formatting: %v", err)
	}
	const edited = "# mine\nname: theirs\n"
	if err := os.WriteFile(workflowPath(dir), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	wrote, err := staterepo.EnsureWorkflow(dir, "", false)
	if err != nil {
		t.Fatalf("ensuring the workflow: %v", err)
	}
	if wrote {
		t.Error("EnsureWorkflow reported writing over a workflow that was already there")
	}
	got, err := os.ReadFile(workflowPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != edited {
		t.Errorf("an edited workflow was overwritten:\n%s", got)
	}

	wrote, err = staterepo.EnsureWorkflow(dir, "", true)
	if err != nil {
		t.Fatalf("forcing the workflow: %v", err)
	}
	if !wrote {
		t.Error("-force did not replace the workflow")
	}
	got, err = os.ReadFile(workflowPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) == edited {
		t.Error("-force left the edited workflow in place")
	}
}

// The one line of the file grain keeps for itself. A workflow written
// yesterday names yesterday's build, and `grain state check` refuses a
// dump stamped with a schema it does not know -- so a check nobody
// repoints fails every pull request against the repository the day the
// schema moves, over nothing in the change at all.
//
// The rule that makes that safe is byte equality with grain's own
// rendering: the image moves only in a file that is otherwise word for
// word what grain would have written.
func TestTheImageIsRepointedAndTheRestOfTheFileIsNot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const was = "ghcr.io/bwsalmon/grain/grain:sha-0000000"
	if _, err := staterepo.Format(dir, was, false); err != nil {
		t.Fatalf("formatting: %v", err)
	}
	if got, ok := staterepo.RenderedImage([]byte(read(t, workflowPath(dir)))); !ok || got != was {
		t.Fatalf("grain does not recognise its own rendering: %q, %v", got, ok)
	}

	const now = "ghcr.io/bwsalmon/grain/grain:sha-abc1234"
	wrote, err := staterepo.EnsureWorkflow(dir, now, false)
	if err != nil {
		t.Fatalf("ensuring the workflow: %v", err)
	}
	if !wrote {
		t.Error("the check was left pointing at a build this deployment does not run")
	}
	if got := read(t, workflowPath(dir)); !strings.Contains(got, now) || strings.Contains(got, was) {
		t.Errorf("the image was not repointed:\n%s", got)
	}
	// And having been repointed it stays put: the next tick has nothing
	// to do, which is what keeps this from being a commit per sync.
	if wrote, err := staterepo.EnsureWorkflow(dir, now, false); err != nil || wrote {
		t.Errorf("the workflow was rewritten a second time (%v, %v)", wrote, err)
	}

	// A file with anything else of somebody's own in it is theirs whole,
	// image included -- there is no telling which line they meant.
	edited := strings.Replace(read(t, workflowPath(dir)), "runs-on: ubuntu-latest", "runs-on: self-hosted", 1)
	if err := os.WriteFile(workflowPath(dir), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := staterepo.RenderedImage([]byte(edited)); ok {
		t.Error("a workflow somebody edited is still read as grain's own")
	}
	if wrote, err := staterepo.EnsureWorkflow(dir, "ghcr.io/bwsalmon/grain/grain:sha-9999999", false); err != nil || wrote {
		t.Errorf("grain rewrote a workflow somebody had edited (%v, %v)", wrote, err)
	}
	if got := read(t, workflowPath(dir)); got != edited {
		t.Errorf("an edited workflow was overwritten:\n%s", got)
	}
}

// What the generated step has to say for it to do anything at all: which
// image it runs, that it runs the check against the checkout, and that
// it does not run on grain's own pushes.
func TestTheWorkflowRunsTheCheckAgainstTheCheckout(t *testing.T) {
	t.Parallel()
	body := string(staterepo.Workflow(""))
	for _, want := range []string{
		staterepo.DefaultCheckImage,
		"state check /state",
		"pull_request:",
		staterepo.TablesDir,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the workflow does not mention %q:\n%s", want, body)
		}
	}
	// A push trigger would spend a CI run every time grain's own timer
	// committed, which on a busy deployment is dozens a day.
	if strings.Contains(body, "\n  push:") {
		t.Errorf("the workflow runs on pushes, which are grain's own:\n%s", body)
	}

	pinned := string(staterepo.Workflow("ghcr.io/bwsalmon/grain/grain:sha-abc1234"))
	if !strings.Contains(pinned, "ghcr.io/bwsalmon/grain/grain:sha-abc1234") {
		t.Errorf("a pinned image did not reach the workflow:\n%s", pinned)
	}
	if strings.Contains(pinned, staterepo.DefaultCheckImage) {
		t.Errorf("a pinned image left the default in the workflow too:\n%s", pinned)
	}
}
