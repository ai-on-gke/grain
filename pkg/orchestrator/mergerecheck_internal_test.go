package orchestrator

import (
	"reflect"
	"testing"

	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/model"
)

// The overlap rule's clauses, one at a time (mergerecheck.go,
// docs/merge-queue-behind.md "The overlap re-check").
func TestOverlapRuleClauses(t *testing.T) {
	cases := []struct {
		name            string
		changed, landed []string
		complete        bool
		want            []string
	}{
		{
			name:     "disjoint files in different packages merge on their own green",
			changed:  []string{"pkg/ui/route_scopes.go", "docs/ui.md"},
			landed:   []string{"pkg/model/task.go", "README.md"},
			complete: true,
		},
		{
			name:     "clause 1: the same file touched on both sides",
			changed:  []string{"docs/merge-queue-behind.md"},
			landed:   []string{"docs/merge-queue-behind.md"},
			complete: true,
			want:     []string{model.MergeRecheckClauseSameFile},
		},
		{
			name:     "clause 2: different files in one Go package",
			changed:  []string{"pkg/ui/route_scopes.go"},
			landed:   []string{"pkg/ui/routes_test.go"},
			complete: true,
			want:     []string{model.MergeRecheckClauseSamePackage},
		},
		{
			name:     "clause 2 is about packages, not directories: a non-Go file beside Go files is not one",
			changed:  []string{"pkg/ui/route_scopes.go"},
			landed:   []string{"pkg/ui/testdata/routes.json"},
			complete: true,
		},
		{
			name:     "clause 2 is the directory, not its parent: pkg/ui and pkg/ui/wire are two packages",
			changed:  []string{"pkg/ui/route_scopes.go"},
			landed:   []string{"pkg/ui/wire/wire.go"},
			complete: true,
		},
		{
			name:     "clause 1 and 2 both fire for the same Go file, and both are recorded",
			changed:  []string{"pkg/ui/route_scopes.go"},
			landed:   []string{"pkg/ui/route_scopes.go"},
			complete: true,
			want:     []string{model.MergeRecheckClauseSameFile, model.MergeRecheckClauseSamePackage},
		},
		{
			name:     "clause 3: go.mod landed",
			changed:  []string{"pkg/ui/route_scopes.go"},
			landed:   []string{"go.mod"},
			complete: true,
			want:     []string{model.MergeRecheckClauseGlobalInput},
		},
		{
			name:     "clause 3: a nested lockfile landed",
			changed:  []string{"pkg/ui/route_scopes.go"},
			landed:   []string{"ui/package-lock.json"},
			complete: true,
			want:     []string{model.MergeRecheckClauseGlobalInput},
		},
		{
			name:     "clause 3: a CI workflow landed",
			changed:  []string{"pkg/ui/route_scopes.go"},
			landed:   []string{".grain/workflows/tests.yml"},
			complete: true,
			want:     []string{model.MergeRecheckClauseGlobalInput},
		},
		{
			name:     "clause 3: a Makefile landed",
			changed:  []string{"docs/x.md"},
			landed:   []string{"Makefile"},
			complete: true,
			want:     []string{model.MergeRecheckClauseGlobalInput},
		},
		{
			name:     "clause 3 is about what landed: the change itself touching go.mod is not a trigger",
			changed:  []string{"go.mod", "go.sum"},
			landed:   []string{"pkg/model/task.go"},
			complete: true,
		},
		{
			name:     "a file list cut short cannot be said not to overlap",
			changed:  []string{"pkg/ui/route_scopes.go"},
			landed:   []string{"pkg/model/task.go"},
			complete: false,
			want:     []string{model.MergeRecheckClauseUnknown},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := overlapRule(c.changed, c.landed, c.complete)
			if v.trigger != (len(c.want) > 0) {
				t.Fatalf("trigger = %v, want %v (clauses %v, detail %q)", v.trigger, len(c.want) > 0,
					v.clauses, v.detail)
			}
			if !reflect.DeepEqual(v.clauses, c.want) {
				t.Fatalf("clauses = %v, want %v", v.clauses, c.want)
			}
		})
	}
}

// A rename is an overlap under its old name as well as its new one.
func TestComparedPathsIncludesRenamedFrom(t *testing.T) {
	got := comparedPaths([]github.ComparedFile{
		{Filename: "pkg/a/new.go", PreviousFilename: "pkg/a/old.go", Status: "renamed"},
		{Filename: "README.md"},
	})
	want := []string{"pkg/a/new.go", "pkg/a/old.go", "README.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("comparedPaths = %v, want %v", got, want)
	}
	v := overlapRule([]string{"pkg/b/x.md"}, got, true)
	if v.trigger {
		t.Fatalf("unrelated change triggered: %v", v.clauses)
	}
	v = overlapRule([]string{"pkg/a/old.go"}, got, true)
	if !v.trigger {
		t.Fatalf("a change to the renamed file's old path did not trigger")
	}
}

// compareClient is enough of GitHub for recheckFor: a tip, and the two
// comparisons it asks for.
type compareClient struct {
	github.Client
	tip      string
	compares map[string]github.Comparison
	asked    []string
}

func (c *compareClient) GetBranchHead(_, _, _ string) (*github.BranchHead, error) {
	return &github.BranchHead{SHA: c.tip}, nil
}

func (c *compareClient) CompareRefs(_, _, base, head string) (github.Comparison, error) {
	c.asked = append(c.asked, base+"..."+head)
	return c.compares[base+"..."+head], nil
}

func files(paths ...string) []github.ComparedFile {
	out := make([]github.ComparedFile, 0, len(paths))
	for _, p := range paths {
		out = append(out, github.ComparedFile{Filename: p})
	}
	return out
}

// B0 == T: nothing has landed since the tree the change was tested on, so
// it merges at once -- and the landed side is not even asked for.
func TestRecheckForBaseCurrentNeverTriggers(t *testing.T) {
	client := &compareClient{tip: "t1", compares: map[string]github.Comparison{
		"t1...h1": {Status: "ahead", MergeBase: "t1", Files: files("go.mod", "pkg/ui/a.go")},
	}}
	v, err := recheckFor(client, model.RepoRef{Owner: "o", Name: "r"}, "main", "", "h1")
	if err != nil {
		t.Fatal(err)
	}
	if v.trigger || v.fork != "t1" || v.tip != "t1" {
		t.Fatalf("verdict = %+v, want an untriggered fork == tip", v)
	}
	if len(client.asked) != 1 {
		t.Fatalf("asked GitHub for %v, want the one comparison", client.asked)
	}
}

// The fork is where head last met the branch, not anything a CI run
// recorded, and L is fork..tip.
func TestRecheckForComparesFromTheForkPoint(t *testing.T) {
	client := &compareClient{tip: "t2", compares: map[string]github.Comparison{
		"t2...h1": {Status: "diverged", MergeBase: "f0", Files: files("pkg/ui/route_scopes.go")},
		"f0...t2": {Status: "ahead", MergeBase: "f0", Files: files("pkg/ui/route_scopes_test.go")},
	}}
	v, err := recheckFor(client, model.RepoRef{Owner: "o", Name: "r"}, "main", "", "h1")
	if err != nil {
		t.Fatal(err)
	}
	if !v.trigger || v.fork != "f0" || v.tip != "t2" || v.head != "h1" {
		t.Fatalf("verdict = %+v, want a triggered verdict from f0 to t2", v)
	}
	if !reflect.DeepEqual(v.clauses, []string{model.MergeRecheckClauseSamePackage}) {
		t.Fatalf("clauses = %v", v.clauses)
	}

	truncated := &compareClient{tip: "t2", compares: map[string]github.Comparison{
		"t2...h1": {MergeBase: "f0", Files: files("docs/a.md")},
		"f0...t2": {MergeBase: "f0", Files: files("docs/b.md"), FilesTruncated: true},
	}}
	v, err = recheckFor(truncated, model.RepoRef{Owner: "o", Name: "r"}, "main", "", "h1")
	if err != nil {
		t.Fatal(err)
	}
	if !v.trigger || !reflect.DeepEqual(v.clauses, []string{model.MergeRecheckClauseUnknown}) {
		t.Fatalf("a truncated comparison gave %+v, want a trigger on unknown", v)
	}
}

func TestParseMergeRecheckMode(t *testing.T) {
	for in, want := range map[string]model.MergeRecheckMode{
		"": "", "off": model.MergeRecheckOff, " Observe ": model.MergeRecheckObserve,
		"ENFORCE": model.MergeRecheckEnforce,
	} {
		got, err := model.ParseMergeRecheckMode(in)
		if err != nil || got != want {
			t.Errorf("ParseMergeRecheckMode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := model.ParseMergeRecheckMode("sometimes"); err == nil {
		t.Errorf("an unknown mode parsed")
	}
	if got := model.MergeRecheckModeOf(nil); got != model.MergeRecheckEnforce {
		t.Errorf("a repository with no configuration reads as %q, want enforce", got)
	}
	if got := model.MergeRecheckModeOf(&model.RepoConfig{}); got != model.MergeRecheckEnforce {
		t.Errorf("an unset mode reads as %q, want enforce", got)
	}
}
