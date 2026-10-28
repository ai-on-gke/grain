package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
)

// What a run may read CI about (grain/task-172).
//
// These are about the boundary rather than about the filter vocabulary:
// that a run sees its own repository without asking, that another
// repository is refused in words rather than answered empty, that a run
// id from somewhere else is refused as such rather than reported
// missing, and that the wide grant is named wherever it is the
// difference.

// twoRepoCIRunner is a deployment with two repositories in its memory
// and a run id in each: the shape every question below turns on.
type twoRepoCIRunner struct{ reads int }

func (r *twoRepoCIRunner) StartGrainCI(context.Context, []string) (ci.Run, error) {
	return ci.Run{}, fmt.Errorf("not used by these tests")
}

func (r *twoRepoCIRunner) RetriggerGrainCI(context.Context, string, string, []string, bool) (ci.Run, error) {
	return ci.Run{}, fmt.Errorf("not used by these tests")
}

func (r *twoRepoCIRunner) runs() []ci.Run {
	return []ci.Run{
		{ID: "ci-mine", Repo: "acme/widgets", Ref: "grain/task-172", Commit: "1111111",
			Conclusion: ci.ConclusionFailure, Jobs: []ci.JobRun{{Name: "go", Conclusion: ci.ConclusionFailure,
				Steps: []ci.StepRun{{Name: "go test", Conclusion: ci.ConclusionFailure, Output: "mine\n"}}}}},
		{ID: "ci-theirs", Repo: "acme/gadgets", Ref: "main", Commit: "2222222",
			Conclusion: ci.ConclusionFailure, Jobs: []ci.JobRun{{Name: "go", Conclusion: ci.ConclusionFailure,
				Steps: []ci.StepRun{{Name: "go test", Conclusion: ci.ConclusionFailure, Output: "somebody-elses-bytes\n"}}}}},
	}
}

// No fault has ever been found here: a shared fault is a fact about one
// repository's own branches (its digest is taken over the repo as well
// as the failure), so it is not what this file is about.
func (r *twoRepoCIRunner) GrainCISharedFaults(context.Context) ([]CIFault, error) {
	return nil, nil
}

func (r *twoRepoCIRunner) GrainCIRun(_ context.Context, id string) (ci.Run, error) {
	r.reads++
	for _, run := range r.runs() {
		if run.ID == id {
			return run, nil
		}
	}
	return ci.Run{}, fmt.Errorf("no such run")
}

func (r *twoRepoCIRunner) FindGrainCIRuns(_ context.Context, filter ci.RunFilter, limit int) (ci.RunPage, error) {
	return ciRunPage(r.runs(), filter, limit), nil
}

// A run that asked nothing gets its own repository's runs and no other's
// -- and the answer says that is what it got, since the count beside it
// is deployment-wide.
func TestListCIRunsShowsOnlyThisRunsOwnRepository(t *testing.T) {
	tool := fullCIList(NewGrainCIWaitTools(&twoRepoCIRunner{}, CIRepoScope{Repo: "acme/widgets"}))[1]
	result := tool.Handler(context.Background(), map[string]any{})
	if result.IsError {
		t.Fatal(result.Text)
	}
	if strings.Contains(result.Text, "ci-theirs") || strings.Contains(result.Text, "acme/gadgets") {
		t.Errorf("a scoped list reported another repository's runs:\n%s", result.Text)
	}
	if !strings.Contains(result.Text, "ci-mine") {
		t.Errorf("a scoped list dropped this run's own repository:\n%s", result.Text)
	}
	if !strings.Contains(result.Text, "repo acme/widgets (this run's own, and the only one it may read)") {
		t.Errorf("the heading does not say where the repo filter came from:\n%s", result.Text)
	}
}

// The rest of the vocabulary refuses a word it does not know rather than
// answering empty (ci.ParseRunState); a repository outside the scope is
// the same kind of word and gets the same treatment, with the one it may
// name and the grant that would widen it.
func TestListCIRunsRefusesARepositoryThisRunMayNotRead(t *testing.T) {
	tool := fullCIList(NewGrainCIWaitTools(&twoRepoCIRunner{}, CIRepoScope{Repo: "acme/widgets"}))[1]
	result := tool.Handler(context.Background(), map[string]any{"repo": "acme/gadgets"})
	if !result.IsError {
		t.Fatalf("another repository was answered rather than refused:\n%s", result.Text)
	}
	for _, want := range []string{"acme/gadgets", "acme/widgets", CIWideReadGrant} {
		if !strings.Contains(result.Text, want) {
			t.Errorf("the refusal lacks %q: %s", want, result.Text)
		}
	}
	if strings.Contains(result.Text, "No grain CI run matches") {
		t.Errorf("the refusal reads as an empty answer: %s", result.Text)
	}
}

// Naming its own repository is not a different question from naming
// none, whatever case it is typed in -- the rest of the filter matches
// repositories without regard to case (ci.RunFilter.Repo).
func TestListCIRunsAcceptsItsOwnRepositoryByName(t *testing.T) {
	tool := fullCIList(NewGrainCIWaitTools(&twoRepoCIRunner{}, CIRepoScope{Repo: "acme/widgets"}))[1]
	for _, named := range []string{"acme/widgets", "ACME/Widgets"} {
		result := tool.Handler(context.Background(), map[string]any{"repo": named})
		if result.IsError {
			t.Fatalf("repo %q was refused: %s", named, result.Text)
		}
		if !strings.Contains(result.Text, "ci-mine") {
			t.Errorf("repo %q did not answer with this run's own runs:\n%s", named, result.Text)
		}
	}
}

// The wide grant is the whole of the old behaviour, kept for the task
// that is here to work out what grain itself did.
func TestListCIRunsWithTheWideGrantSeesEveryRepository(t *testing.T) {
	tool := fullCIList(NewGrainCIWaitTools(&twoRepoCIRunner{}, CIRepoScope{AllRepos: true}))[1]
	result := tool.Handler(context.Background(), map[string]any{})
	if result.IsError {
		t.Fatal(result.Text)
	}
	for _, want := range []string{"ci-mine", "ci-theirs"} {
		if !strings.Contains(result.Text, want) {
			t.Errorf("a wide list lacks %q:\n%s", want, result.Text)
		}
	}
	if !strings.Contains(tool.Description, CIWideReadGrant) {
		t.Errorf("a run that can see everything is not told why:\n%s", tool.Description)
	}
}

// get_ci_log takes a run id and no repository, so the check is after the
// lookup -- and what comes back has to be a refusal rather than a run
// that does not exist, which is a different fact and a different next
// move.
func TestGetCILogRefusesAnotherRepositorysRunInWords(t *testing.T) {
	runner := &twoRepoCIRunner{}
	tool := NewGrainCIWaitTools(runner, CIRepoScope{Repo: "acme/widgets"})[2]
	result := tool.Handler(context.Background(), map[string]any{"run_id": "ci-theirs"})
	if !result.IsError {
		t.Fatalf("another repository's log was served:\n%s", result.Text)
	}
	if strings.Contains(result.Text, "somebody-elses-bytes") {
		t.Errorf("the refusal leaked the log it refused:\n%s", result.Text)
	}
	for _, want := range []string{"ci-theirs", "acme/gadgets", "acme/widgets", "not a run that does not exist"} {
		if !strings.Contains(result.Text, want) {
			t.Errorf("the refusal lacks %q: %s", want, result.Text)
		}
	}
}

// Its own repository's run reads exactly as it did before any of this.
func TestGetCILogStillReadsThisRunsOwnRepository(t *testing.T) {
	tool := NewGrainCIWaitTools(&twoRepoCIRunner{}, CIRepoScope{Repo: "acme/widgets"})[2]
	result := tool.Handler(context.Background(), map[string]any{"run_id": "ci-mine"})
	if result.IsError {
		t.Fatal(result.Text)
	}
	if !strings.Contains(result.Text, "mine") {
		t.Errorf("this run's own log is missing:\n%s", result.Text)
	}
}

// A task with no repository of its own may read nothing, and is told so
// -- both ways out of it named. It is refused before the lookup, since a
// run it may not be told about is one it may not be told the existence
// of either.
func TestCIToolsRefuseARunWithNoRepositoryOfItsOwn(t *testing.T) {
	runner := &twoRepoCIRunner{}
	tools := NewGrainCIWaitTools(runner, CIRepoScope{})
	for _, tc := range []struct {
		tool Tool
		args map[string]any
	}{
		{tools[1], map[string]any{}},
		{tools[2], map[string]any{"run_id": "ci-mine"}},
	} {
		result := tc.tool.Handler(context.Background(), tc.args)
		if !result.IsError {
			t.Fatalf("%s answered a run with no repository:\n%s", tc.tool.Name, result.Text)
		}
		if !strings.Contains(result.Text, "no repository of its own") ||
			!strings.Contains(result.Text, CIWideReadGrant) {
			t.Errorf("%s's refusal does not say why or what would widen it: %s", tc.tool.Name, result.Text)
		}
	}
	if runner.reads != 0 {
		t.Errorf("get_ci_log looked the run up before refusing it (%d reads)", runner.reads)
	}
}

// The descriptions are where a run learns the rule, since a boundary it
// discovers by being refused costs it a turn. Both tools say which
// repositories they are about, and neither says "across all
// repositories" to a run that cannot see them.
func TestCIToolDescriptionsSayWhichRepositoriesTheyAreAbout(t *testing.T) {
	scoped := NewGrainCIWaitTools(&twoRepoCIRunner{}, CIRepoScope{Repo: "acme/widgets"})
	for _, tool := range []Tool{scoped[1], scoped[2]} {
		if !strings.Contains(tool.Description, "acme/widgets") {
			t.Errorf("%s does not name the repository it reads:\n%s", tool.Name, tool.Description)
		}
		if strings.Contains(tool.Description, "across all repositories") {
			t.Errorf("%s still claims to read every repository:\n%s", tool.Name, tool.Description)
		}
	}
	repoArg := scoped[1].InputSchema["properties"].(map[string]any)["repo"].(map[string]any)["description"].(string)
	if !strings.Contains(repoArg, "acme/widgets") || !strings.Contains(repoArg, "refused") {
		t.Errorf("the repo argument does not say what it may name:\n%s", repoArg)
	}
	none := NewGrainCIWaitTools(&twoRepoCIRunner{}, CIRepoScope{})
	for _, tool := range []Tool{none[1], none[2]} {
		if !strings.Contains(tool.Description, CIWideReadGrant) {
			t.Errorf("%s does not tell a run with no repository what it is missing:\n%s",
				tool.Name, tool.Description)
		}
	}
}

// The scope narrows what is asked; it does not change the rest of the
// vocabulary's meaning. A filter the caller did set still composes with
// the one the scope adds.
func TestScopeComposesWithTheRestOfTheFilter(t *testing.T) {
	tool := fullCIList(NewGrainCIWaitTools(&twoRepoCIRunner{}, CIRepoScope{Repo: "acme/widgets"}))[1]
	result := tool.Handler(context.Background(), map[string]any{"ref": "main"})
	if result.IsError {
		t.Fatal(result.Text)
	}
	// acme/gadgets' run is on main; this run's own is not.
	if !strings.Contains(result.Text, "No grain CI run matches") {
		t.Errorf("a ref belonging to another repository's run was answered with it:\n%s", result.Text)
	}
	if !strings.Contains(result.Text, "repo acme/widgets") || !strings.Contains(result.Text, "ref main") {
		t.Errorf("the empty answer does not say what it was asked:\n%s", result.Text)
	}
	if !strings.Contains(result.Text, "remembers 2 runs") {
		t.Errorf("the empty answer does not say the deployment is not empty:\n%s", result.Text)
	}
}

// unfilteringCIRunner is a deployment that ignores the filter it is
// given and answers with everything it has -- a daemon a version behind,
// or one that stops honouring "repo" some day. The boundary has to hold
// without its cooperation, because it is this end that promises it.
type unfilteringCIRunner struct{ twoRepoCIRunner }

func (r *unfilteringCIRunner) FindGrainCIRuns(context.Context, ci.RunFilter, int) (ci.RunPage, error) {
	runs := r.runs()
	return ci.RunPage{Runs: runs, Matched: len(runs), Stored: len(runs)}, nil
}

func TestListCIRunsDropsOutOfScopeRunsTheDeploymentAnswersWith(t *testing.T) {
	tool := fullCIList(NewGrainCIWaitTools(&unfilteringCIRunner{}, CIRepoScope{Repo: "acme/widgets"}))[1]
	result := tool.Handler(context.Background(), map[string]any{})
	if result.IsError {
		t.Fatal(result.Text)
	}
	if strings.Contains(result.Text, "ci-theirs") || strings.Contains(result.Text, "acme/gadgets") {
		t.Errorf("a run the scope forbids survived an unfiltered answer:\n%s", result.Text)
	}
	if !strings.Contains(result.Text, "ci-mine") {
		t.Errorf("this run's own runs went with them:\n%s", result.Text)
	}
	// The count has to come down with the runs, or the heading invites a
	// reader to raise "limit" for a run that will never come.
	if !strings.Contains(result.Text, "1 of 2 remembered grain CI runs match") {
		t.Errorf("the heading still counts the runs it dropped:\n%s", result.Text)
	}
}

func TestCIRepoScopePermits(t *testing.T) {
	for _, tc := range []struct {
		name  string
		scope CIRepoScope
		repo  string
		want  bool
	}{
		{"its own", CIRepoScope{Repo: "acme/widgets"}, "acme/widgets", true},
		{"its own, differently cased", CIRepoScope{Repo: "acme/widgets"}, "Acme/Widgets", true},
		{"somebody else's", CIRepoScope{Repo: "acme/widgets"}, "acme/gadgets", false},
		{"a run recording no repository", CIRepoScope{Repo: "acme/widgets"}, "", false},
		{"nothing at all", CIRepoScope{}, "acme/widgets", false},
		{"the wide grant", CIRepoScope{AllRepos: true}, "acme/gadgets", true},
		{"the wide grant over a run recording no repository", CIRepoScope{AllRepos: true}, "", true},
	} {
		if got := tc.scope.permits(tc.repo); got != tc.want {
			t.Errorf("%s: permits(%q) = %v, want %v", tc.name, tc.repo, got, tc.want)
		}
	}
}

// A repository the task may read is listed when it is named, not quietly
// replaced by the run's own.
func TestListCIRunsNamesARepositoryTheTaskReads(t *testing.T) {
	scope := CIRepoScope{Repo: "acme/widgets", Reads: []string{"acme/gadgets"}}
	f, err := scope.narrow(ci.RunFilter{Repo: "acme/gadgets"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Repo != "acme/gadgets" {
		t.Errorf("filter repo = %q, want the read repository acme/gadgets", f.Repo)
	}
	if f, _ := scope.narrow(ci.RunFilter{}); f.Repo != "acme/widgets" {
		t.Errorf("filter repo = %q with none named, want the run's own", f.Repo)
	}
	if _, err := scope.narrow(ci.RunFilter{Repo: "acme/other"}); err == nil {
		t.Error("a repository outside both was not refused")
	}
}

// A run that may read another repository is told so, everywhere the scope
// is put into words: the heading over a read repository's runs does not
// call it this run's own and only one, and a refusal names the read
// repository among those that may be named.
// (docs/test-runs/2026-10-02-ci-cache-glob-wait-credit.md, F2.)
func TestListCIRunsSaysWhichRepositoriesTheTaskReads(t *testing.T) {
	scope := CIRepoScope{Repo: "acme/widgets", Reads: []string{"acme/gadgets"}}
	tool := NewGrainCIWaitTools(&twoRepoCIRunner{}, scope)[1]

	read := tool.Handler(context.Background(), map[string]any{"repo": "acme/gadgets"})
	if read.IsError {
		t.Fatal(read.Text)
	}
	if strings.Contains(read.Text, "this run's own") || !strings.Contains(read.Text, "repo acme/gadgets (one this run may read, besides its own)") {
		t.Errorf("the heading over a read repository's runs misnames it:\n%s", read.Text)
	}

	own := tool.Handler(context.Background(), map[string]any{})
	if !strings.Contains(own.Text, "repo acme/widgets (this run's own; it may also read acme/gadgets)") {
		t.Errorf("the heading over this run's own runs hides the read repository:\n%s", own.Text)
	}

	refused := tool.Handler(context.Background(), map[string]any{"repo": "acme/other"})
	if !refused.IsError || !strings.Contains(refused.Text, "acme/gadgets, which it may read") {
		t.Errorf("the refusal does not name the read repository:\n%s", refused.Text)
	}

	for _, text := range []string{scope.listedRepos(), scope.repoArgument(), scope.logScopeSentence(), scope.refuseRun(ci.Run{ID: "r", Repo: "acme/other"}).Error()} {
		if !strings.Contains(text, "acme/gadgets") || strings.Contains(text, "the only one it may read") {
			t.Errorf("a scope sentence leaves out the read repository: %q", text)
		}
	}
}
