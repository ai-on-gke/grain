package repodecl_test

// This repository's own `.grain` directory, read by the package that
// reads it in production.
//
// Everywhere else in this package the trees are written by the test.
// Here the tree is the checkout, because nothing else in the repository
// looks at these files at all: a misspelt key in
// `.grain/suites/release-ui-walkthrough.yml` is otherwise discovered by
// a deployment a poll after it merges, as a line in
// `repo_declaration_sync.last_error` on a settings page nobody is
// reading, with the previous commit's declarations still live and
// nothing at all saying the new ones are not. `grain declarations
// check` exists to move that verdict earlier; this moves it earlier
// still, into a job that already runs on every branch.
//
// It asserts the shape the release ritual depends on as well as the
// parse (docs/ui-walkthrough.md): the suite runs every walkthrough
// template this repository declares, and names no branch of its own,
// because the branch is chosen when the suite is run.

import (
	"os"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/repodecl"
)

// repoRoot is this package's own directory, two levels down.
const repoRoot = "../.."

func loadRepo(t *testing.T) model.RepoDeclarations {
	t.Helper()
	declared, err := repodecl.Load(os.DirFS(repoRoot), repo)
	if err != nil {
		t.Fatalf("this repository's own .grain declarations do not load:\n%v", err)
	}
	return declared
}

func TestThisRepositoryOwnDeclarationsAreWellFormed(t *testing.T) {
	declared := loadRepo(t)
	if declared.Empty() {
		t.Fatalf("this repository declares nothing -- %s has files in it, so Load read the wrong tree",
			model.TemplateDeclarationDir)
	}
	for _, f := range declared.Templates {
		if strings.TrimSpace(f.Template.Body) == "" {
			t.Errorf("%s: a declared template with no body files a task with no instructions", f.Path)
		}
	}
}

// This repository declares no settings of its own, and the two tests
// that used to stand here are gone with the reach that made them
// necessary (grain/task-340).
//
// They held `.grain/settings.yml` to the values this repository's
// `repo_config` row needs -- the guest cache dispatch job, the CI
// capabilities, `release-workflows-disabled`, `ignore-github-ci` -- and
// they were right to, because until now that file *was* the row: a
// whole-row overwrite on every poll, which at 18:52 UTC on 20 Sep 2026
// emptied four of those fields at once (grain/task-460, 483).
//
// None of those settings is a file here's to write any more, so there is
// nothing in this tree left to assert about them. What a test in this
// repository can still hold is that the file does not come back saying
// them, and that is exactly what loadRepo does: a `ci-capabilities:` in
// `.grain/settings.yml` is a parse error naming the key, so
// TestThisRepositoryOwnDeclarationsAreWellFormed above fails on it. The
// values themselves are in the row, and .grain/README.md says which they
// are for whoever has to read it back.

func TestTheReleaseWalkthroughSuiteRunsEveryWalkthroughTemplate(t *testing.T) {
	declared := loadRepo(t)

	const stem = "release-ui-walkthrough"
	want := map[string]bool{}
	for _, f := range declared.Templates {
		// ui-walkthrough-review keeps the chunks current
		// (.grain/schedules/ui-walkthrough-review.yml); it walks nothing.
		if s := model.DeclarationStem(f.Path); strings.HasPrefix(s, "ui-walkthrough-") && s != "ui-walkthrough-review" {
			want[s] = false
		}
	}
	if len(want) == 0 {
		t.Fatalf("no ui-walkthrough-* template is declared, so %s/%s.yml has nothing to run",
			model.SuiteDeclarationDir, stem)
	}

	var suite *model.Suite
	for i, f := range declared.Suites {
		if model.DeclarationStem(f.Path) == stem {
			suite = &declared.Suites[i].Suite
		}
	}
	if suite == nil {
		t.Fatalf("%s/%s.yml is what a release runs (docs/ui-walkthrough.md) and it is not declared",
			model.SuiteDeclarationDir, stem)
	}
	for _, item := range suite.Items {
		for name := range want {
			if item.TemplateID == model.RepoDeclaredID(model.DeclaredTemplate, repo, name) {
				want[name] = true
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("%s/%s.yml declares a walkthrough chunk that %s does not run -- "+
				"a chunk left out of the suite is a chunk no release walks",
				model.TemplateDeclarationDir, name, stem)
		}
	}
	// One pass, deliberately: docs/ui-walkthrough.md has the reasoning,
	// and an until-clean sweep here would re-walk five clean areas for
	// the sake of the sixth.
	if suite.Mode != model.SuiteCount || suite.Count != 1 {
		t.Errorf("%s runs %q x%d; the release walkthrough is one pass of each chunk",
			stem, suite.Mode, suite.Count)
	}
	if suite.RequireApproval || !suite.AutoMerge {
		t.Errorf("%s: require-approval %v, auto-merge %v -- the walkthrough queues itself and lands its "+
			"own fixes", stem, suite.RequireApproval, suite.AutoMerge)
	}
}

func TestTheReleaseQualificationSuiteRunsEveryQualificationTemplate(t *testing.T) {
	declared := loadRepo(t)

	const stem = "release-qualification"
	want := map[string]bool{}
	for _, f := range declared.Templates {
		if s := model.DeclarationStem(f.Path); strings.HasPrefix(s, "qualification-") {
			want[s] = false
		}
	}
	if len(want) == 0 {
		t.Fatalf("no qualification-* template is declared, so %s/%s.yml has nothing to run",
			model.SuiteDeclarationDir, stem)
	}

	var suite *model.Suite
	for i, f := range declared.Suites {
		if model.DeclarationStem(f.Path) == stem {
			suite = &declared.Suites[i].Suite
		}
	}
	if suite == nil {
		t.Fatalf("%s/%s.yml is not declared", model.SuiteDeclarationDir, stem)
	}
	for _, item := range suite.Items {
		for name := range want {
			if item.TemplateID == model.RepoDeclaredID(model.DeclaredTemplate, repo, name) {
				want[name] = true
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("%s/%s.yml declares a qualification item that %s does not run",
				model.TemplateDeclarationDir, name, stem)
		}
	}
	if suite.Mode != model.SuiteCount || suite.Count != 1 {
		t.Errorf("%s runs %q x%d; expected mode count x1", stem, suite.Mode, suite.Count)
	}
	if suite.RequireApproval || !suite.AutoMerge {
		t.Errorf("%s: require-approval %v, auto-merge %v", stem, suite.RequireApproval, suite.AutoMerge)
	}
}

func TestTheMainUISmokeSuiteRunsCoreWalkthroughTemplates(t *testing.T) {
	declared := loadRepo(t)

	const stem = "main-ui-smoke"
	expectedTemplates := []string{
		"ui-walkthrough-tasks",
		"ui-walkthrough-shell",
		"ui-walkthrough-settings",
		"ui-walkthrough-infrastructure",
		"ui-walkthrough-boards",
	}

	var suite *model.Suite
	for i, f := range declared.Suites {
		if model.DeclarationStem(f.Path) == stem {
			suite = &declared.Suites[i].Suite
		}
	}
	if suite == nil {
		t.Fatalf("%s/%s.yml is not declared", model.SuiteDeclarationDir, stem)
	}

	items := map[string]bool{}
	for _, item := range suite.Items {
		items[item.TemplateID] = true
	}
	for _, name := range expectedTemplates {
		id := model.RepoDeclaredID(model.DeclaredTemplate, repo, name)
		if !items[id] {
			t.Errorf("%s/%s.yml missing expected template %s", model.SuiteDeclarationDir, stem, name)
		}
	}
	if suite.Mode != model.SuiteCount || suite.Count != 1 {
		t.Errorf("%s runs %q x%d; expected mode count x1", stem, suite.Mode, suite.Count)
	}
	if suite.RequireApproval || !suite.AutoMerge {
		t.Errorf("%s: require-approval %v, auto-merge %v", stem, suite.RequireApproval, suite.AutoMerge)
	}
}
