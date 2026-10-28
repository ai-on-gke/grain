package model

import (
	"fmt"
	"path"
	"strings"
)

// RepoWorkflow is one grain CI workflow declared on grain's own side
// rather than in the repository it runs against (grain/task-245).
//
// pkg/ci reads a repository's workflows out of its own
// `.grain/workflows` directory, which is right for the repositories
// grain works in and impossible for the ones it does not control:
// nobody is going to accept a pull request adding a grain-specific CI
// directory to their repository, and without one grain has nowhere to
// look. So the same workflow text, in the same format, keyed by the
// repository it is for and by the path it would have had in it -- read
// back through ci.Source exactly as an in-repo file is, so nothing in
// the engine, the plan or the format knows which side a workflow
// arrived from.
//
// One row per workflow rather than one text column on RepoConfig,
// because the format already assumes a repository has several: one file
// per workflow under ci.WorkflowDir, each with its own name, its own
// events and its own jobs. Path is what makes them separate rows and is
// also what composes them with the in-repo ones -- see ci.Layered,
// where a path both sides declare is the operator's to win.
//
// # This is operator config, and it stays that way
//
// docs/data-model.md's "The tree is operator config" is the distinction
// that makes this worth doing at all. A `.grain/workflows` file in a
// target repository is a file every agent grain dispatches can rewrite;
// a row here is written through grain's own settings surface, by
// somebody with access to it, and proposed in a pull request against
// the state repository. The two are not equally trusted.
//
// They are nonetheless equally *powerful*, deliberately: pkg/ci's "What
// a workflow file is trusted with" is nothing, and it holds for both.
// A workflow declared here still spends compute in a sandbox that is
// given no capability material and no credential, and which
// repositories may have CI run against them at all is still decided
// outside any workflow (the `grain-ci` capability). Being operator
// config buys this row the right to say *what runs*; it must never
// grow the right to say what a job may touch, because then the answer
// to "can this repository's own file do that too?" would start
// mattering, and pkg/ci is built so that it does not.
type RepoWorkflow struct {
	// Repo is the repository this workflow runs against.
	Repo RepoRef
	// Path is where the workflow would have lived in that repository --
	// ".grain/workflows/tests.yml". A repo-relative slash-separated
	// path, not a path on any disk: nothing reads a file at it. It is
	// what ci.Workflow.Path is filled with, so an error, a result row
	// and a job's cache key all name this workflow the way they would
	// name an in-repo one, and it is the key a repo object's workflow
	// overrides an in-repo workflow by.
	Path string
	// Text is the workflow file itself, YAML, exactly as somebody typed
	// it. Stored as written rather than as a parsed structure: the
	// format is pkg/ci's and re-serialising it here would be a second
	// definition of it to keep in step, and a comment somebody wrote in
	// their workflow is worth keeping.
	Text string
}

// WorkflowDir is where a repository declares its grain workflows,
// repeated from ci.WorkflowDir because this package cannot import that
// one (pkg/ci is above pkg/model, not below it -- ci.Runner is where
// the two meet). ValidateRepoWorkflowPath is the only thing that reads
// it, and pkg/ci's TestWorkflowDirMatchesTheModelsCopy pins the two
// together from the side that owns the constant.
const WorkflowDir = ".grain/workflows"

// ValidateRepoWorkflowPath reports whether p is a usable workflow path,
// and says why when it is not.
//
// The rule is "what a file under ci.WorkflowDir could actually be
// called", checked here rather than left to whatever writes the row,
// because the path is a primary key and every reader of it -- the
// override rule, an error message, a result row -- assumes it is
// already the canonical spelling of one file. Two rows differing only
// in a "./" or a trailing slash would be two workflows to the store and
// one to everybody else.
//
// It deliberately does not check the extension the way LoadDir's
// isWorkflowFile does. That function is filtering a directory somebody
// else's repository owns, where a README and an editor's backup have to
// be skipped rather than failed; there is no directory here, only rows
// somebody wrote on purpose, and refusing a path is better than
// silently never running it.
func ValidateRepoWorkflowPath(p string) error {
	if strings.TrimSpace(p) == "" {
		return fmt.Errorf("a workflow needs a path")
	}
	if p != strings.TrimSpace(p) {
		return fmt.Errorf("%q: a workflow path has no leading or trailing space", p)
	}
	if p != path.Clean(p) {
		return fmt.Errorf("%q: write the path as it would appear in the repository (%s)", p, path.Clean(p))
	}
	if !strings.HasPrefix(p, WorkflowDir+"/") {
		return fmt.Errorf("%q: a workflow lives under %s/, the same place an in-repo one does", p, WorkflowDir)
	}
	if strings.ContainsAny(strings.TrimPrefix(p, WorkflowDir+"/"), "/") {
		return fmt.Errorf("%q: %s holds one file per workflow, not a tree", p, WorkflowDir)
	}
	return nil
}

// RepoWorkflowPath is ValidateRepoWorkflowPath's forgiving other half:
// the path a human typed, spelled the way this package stores it.
//
// A bare "tests.yml" is what somebody editing one repository's
// workflows in a form actually types, and the directory it goes in is
// not a choice they get to make -- so it is filled in rather than
// refused. Anything else is handed back untouched for
// ValidateRepoWorkflowPath to accept or to explain.
func RepoWorkflowPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || strings.Contains(p, "/") {
		return p
	}
	return WorkflowDir + "/" + p
}
