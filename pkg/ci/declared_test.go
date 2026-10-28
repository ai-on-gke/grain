// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ci

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ai-on-gke/grain/pkg/model"
)

// declared is one workflow's worth of YAML, named so a test can tell
// which of two files at one path it got back.
func declared(job string) string {
	return "name: " + job + "\non: push\njobs:\n  " + job + ":\n    steps:\n      - run: true\n"
}

func declaredSourceOf(workflows ...DeclaredWorkflow) DeclaredSource {
	return DeclaredSource{
		Declared: func(context.Context, string) ([]DeclaredWorkflow, error) { return workflows, nil },
	}
}

func TestDeclaredSourceParsesWhatTheDeploymentDeclares(t *testing.T) {
	source := declaredSourceOf(
		DeclaredWorkflow{Path: ".grain/workflows/b.yml", Text: declared("b")},
		DeclaredWorkflow{Path: ".grain/workflows/a.yml", Text: declared("a")},
	)
	workflows, commit, err := source.Workflows(context.Background(), "owner/repo", "main")
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if commit != "" {
		t.Fatalf("commit = %q, want none: nothing here resolves a ref", commit)
	}
	var paths []string
	for _, w := range workflows {
		paths = append(paths, w.Path)
	}
	// Path order, the order LoadDir gives a directory, so a run's jobs do
	// not move about depending on which side declared them.
	if strings.Join(paths, ",") != ".grain/workflows/a.yml,.grain/workflows/b.yml" {
		t.Fatalf("paths = %v", paths)
	}
	if workflows[0].Name != "a" || len(workflows[0].Jobs) != 1 {
		t.Fatalf("first workflow = %+v, want the parsed a.yml", workflows[0])
	}
}

func TestDeclaredSourceNamesTheWorkflowThatDoesNotParse(t *testing.T) {
	source := declaredSourceOf(DeclaredWorkflow{
		Path: ".grain/workflows/broken.yml",
		Text: "on: push\njobs:\n  a:\n    steps:\n      - uses: actions/checkout@v4\n",
	})
	_, _, err := source.Workflows(context.Background(), "owner/repo", "main")
	if err == nil || !strings.Contains(err.Error(), "broken.yml") {
		t.Fatalf("got %v, want the path in the error", err)
	}
}

func TestDeclaredSourceWithNothingBehindItDeclaresNothing(t *testing.T) {
	workflows, _, err := DeclaredSource{}.Workflows(context.Background(), "owner/repo", "main")
	if err != nil || len(workflows) != 0 {
		t.Fatalf("got %d workflows, %v", len(workflows), err)
	}
}

// The composition rule this whole exercise turns on: union, and the
// configured side wins a path both declare.
func TestLayeredUnionsBothSidesAndLetsTheRepoObjectWinAPath(t *testing.T) {
	repository := SourceFunc(func(context.Context, string, string) ([]Workflow, string, error) {
		return loadTestDir(t, fstest.MapFS{
			".grain/workflows/tests.yml": &fstest.MapFile{Data: []byte(declared("theirs"))},
			".grain/workflows/lint.yml":  &fstest.MapFile{Data: []byte(declared("lint"))},
		}), "cafe1234", nil
	})
	configured := declaredSourceOf(
		DeclaredWorkflow{Path: ".grain/workflows/tests.yml", Text: declared("ours")},
		DeclaredWorkflow{Path: ".grain/workflows/extra.yml", Text: declared("extra")},
	)

	workflows, commit, err := Layered{repository, configured}.Workflows(context.Background(), "owner/repo", "main")
	if err != nil {
		t.Fatalf("composing: %v", err)
	}
	if commit != "cafe1234" {
		t.Fatalf("commit = %q, want the one the branch resolved to", commit)
	}
	got := map[string]string{}
	var paths []string
	for _, w := range workflows {
		got[w.Path] = w.Name
		paths = append(paths, w.Path)
	}
	if strings.Join(paths, ",") != ".grain/workflows/extra.yml,.grain/workflows/lint.yml,.grain/workflows/tests.yml" {
		t.Fatalf("paths = %v, want the union in path order", paths)
	}
	if got[".grain/workflows/tests.yml"] != "ours" {
		t.Fatalf("tests.yml is %q, want the repo object's own: an operator has to be able to "+
			"replace a workflow in a repository they cannot edit", got[".grain/workflows/tests.yml"])
	}
	if got[".grain/workflows/lint.yml"] != "lint" {
		t.Fatalf("lint.yml is %q, want the repository's own: a path only one side declares is that side's",
			got[".grain/workflows/lint.yml"])
	}
}

func TestLayeredFailsWhenASourceFails(t *testing.T) {
	boom := errors.New("the clone did not work")
	failing := SourceFunc(func(context.Context, string, string) ([]Workflow, string, error) {
		return nil, "", boom
	})
	configured := declaredSourceOf(DeclaredWorkflow{Path: ".grain/workflows/a.yml", Text: declared("a")})
	if _, _, err := (Layered{failing, configured}).Workflows(context.Background(), "owner/repo", "main"); !errors.Is(err, boom) {
		t.Fatalf("got %v, want the failure: half a repository's workflows is a green run that tested less "+
			"than somebody thinks it did", err)
	}
}

// model.WorkflowDir is this package's constant, copied because pkg/model
// sits below pkg/ci and cannot import it. Pinned here rather than there,
// on the side that owns it, so a change to the directory fails on the
// copy rather than leaving a repo's declared workflows keyed under a
// path nothing reads.
func TestWorkflowDirMatchesTheModelsCopy(t *testing.T) {
	if model.WorkflowDir != WorkflowDir {
		t.Fatalf("model.WorkflowDir = %q, ci.WorkflowDir = %q", model.WorkflowDir, WorkflowDir)
	}
}

// A path model refuses is a path this package's parse would never see,
// and a path it accepts has to be one a workflow could actually be
// found at in a repository -- the two halves of "the same file, either
// side".
func TestTheModelAcceptsExactlyTheWorkflowPathsThisPackageWouldRead(t *testing.T) {
	for _, p := range []string{".grain/workflows/tests.yml", ".grain/workflows/tests.yaml"} {
		if err := model.ValidateRepoWorkflowPath(p); err != nil {
			t.Fatalf("%q: %v", p, err)
		}
	}
	for _, p := range []string{
		"", "tests.yml", "workflows/tests.yml", ".grain/workflows/", ".grain/workflows/nested/tests.yml",
		".grain/workflows/../../etc/passwd", " .grain/workflows/tests.yml",
	} {
		if err := model.ValidateRepoWorkflowPath(p); err == nil {
			t.Fatalf("%q was accepted", p)
		}
	}
	if got := model.RepoWorkflowPath("tests.yml"); got != ".grain/workflows/tests.yml" {
		t.Fatalf("a bare file name became %q", got)
	}
}

// loadTestDir is LoadDir with the error turned into a fatal, for the
// tests above that use a MapFS as the in-repo side.
func loadTestDir(t *testing.T, tree fstest.MapFS) []Workflow {
	t.Helper()
	workflows, _, err := LoadDir(tree)
	if err != nil {
		t.Fatalf("loading the in-repo side: %v", err)
	}
	return workflows
}
