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

package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type localComparisonStub struct {
	called bool
	result ui.BranchComparison
}

func (s *localComparisonStub) Compare(_ context.Context, _ model.RepoRef, _, _ string) (ui.BranchComparison, error) {
	s.called = true
	return s.result, nil
}

func TestRepoComparisonGateKeepsConfigComparisonsLocal(t *testing.T) {
	// Serial: liveRepoNames is process-global and all three tests in
	// this file write it.
	t.Cleanup(func() { liveRepoNames.Store(nil) })
	liveRepoNames.Store(nil)
	local := &localComparisonStub{result: ui.BranchComparison{
		Base: "main", Head: "grain/task-1", Diff: "+setting",
	}}
	got, err := (repoComparisonGate{local: local}).Compare(
		context.Background(), model.ConfigRepo(), "main", "grain/task-1",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !local.called || got.Diff != "+setting" {
		t.Fatalf("comparison = %+v, local called = %t", got, local.called)
	}
}

func TestRepoComparisonGateReadsGitHubDiffAndResolvesDefaultBase(t *testing.T) {
	// Serial: see above.
	t.Cleanup(func() { liveRepoNames.Store(nil) })
	transport := github.NewFakeTransport(
		github.ApiResponse{Status: 200, Body: []byte(`{"default_branch":"trunk"}`)},
		github.ApiResponse{Status: 200, Body: []byte(
			`{"status":"ahead","merge_base_commit":{"sha":"fork00"},"commits":[]}`)},
		github.ApiResponse{Status: 200, Body: []byte("diff --git a/a b/a\n+live change\n")},
	)
	liveRepoNames.Store(github.NewClient(transport, nil))

	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	got, err := (repoComparisonGate{}).Compare(
		context.Background(), repo, "", "grain/task-12",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.Base != "trunk" || got.Head != "grain/task-12" || !strings.Contains(got.Diff, "+live change") {
		t.Fatalf("comparison = %+v", got)
	}
	if got.Merged || got.MergeBase != "fork00" {
		t.Errorf("comparison = %+v, want an open branch forked at fork00", got)
	}
	if len(transport.Calls) != 3 {
		t.Fatalf("calls = %+v", transport.Calls)
	}
	if transport.Calls[1].Path != "/repos/acme/widgets/compare/trunk...grain/task-12" {
		t.Errorf("standing path = %q", transport.Calls[1].Path)
	}
	// The diff is taken from the merge base rather than from whatever
	// base points at now, which is the same patch while the branch is
	// open and the only readable one once it has landed.
	if transport.Calls[2].Path != "/repos/acme/widgets/compare/fork00...grain/task-12" {
		t.Errorf("diff path = %q", transport.Calls[2].Path)
	}
	if transport.Calls[2].Headers["Accept"] != "application/vnd.github.diff" {
		t.Errorf("Accept = %q", transport.Calls[2].Headers["Accept"])
	}
}

// A merged branch is the case grain/task-375 is about: GitHub's merge
// base for a head its base already contains is head's own tip, so the
// comparison a finished task links to showed an empty diff. The fork
// point is recovered from the merge commit instead.
func TestRepoComparisonGateRebuildsAMergedBranchFromItsMergeCommit(t *testing.T) {
	// Serial: see above.
	t.Cleanup(func() { liveRepoNames.Store(nil) })
	transport := github.NewFakeTransport(
		// main...grain/task-12: main has the branch already.
		github.ApiResponse{Status: 200, Body: []byte(
			`{"status":"behind","merge_base_commit":{"sha":"head99"},"commits":[]}`)},
		// grain/task-12...main: the commits main carries that the branch
		// does not, with the merge that brought it in among them.
		github.ApiResponse{Status: 200, Body: []byte(`{"status":"ahead","commits":[
			{"sha":"later1","parents":[{"sha":"base01"}]},
			{"sha":"merge1","parents":[{"sha":"base01"},{"sha":"head99"}]},
			{"sha":"later2","parents":[{"sha":"merge1"}]}
		]}`)},
		// base01...grain/task-12: where the branch forked.
		github.ApiResponse{Status: 200, Body: []byte(
			`{"status":"diverged","merge_base_commit":{"sha":"fork42"},"commits":[]}`)},
		github.ApiResponse{Status: 200, Body: []byte("diff --git a/a b/a\n+what it changed\n")},
	)
	liveRepoNames.Store(github.NewClient(transport, nil))

	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	got, err := (repoComparisonGate{}).Compare(
		context.Background(), repo, "main", "grain/task-12",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Merged || got.MergeBase != "fork42" {
		t.Fatalf("comparison = %+v, want a merged branch forked at fork42", got)
	}
	if !strings.Contains(got.Diff, "+what it changed") {
		t.Fatalf("diff = %q", got.Diff)
	}
	if len(transport.Calls) != 4 {
		t.Fatalf("calls = %+v", transport.Calls)
	}
	if transport.Calls[3].Path != "/repos/acme/widgets/compare/fork42...grain/task-12" {
		t.Errorf("diff path = %q", transport.Calls[3].Path)
	}
}

// And when the merge cannot be found -- a branch squashed in, one merged
// further back than GitHub's own compare will report -- the view falls
// back to the plain comparison rather than failing.
func TestRepoComparisonGateFallsBackWhenNoMergeCommitIsFound(t *testing.T) {
	// Serial: see above.
	t.Cleanup(func() { liveRepoNames.Store(nil) })
	transport := github.NewFakeTransport(
		github.ApiResponse{Status: 200, Body: []byte(
			`{"status":"behind","merge_base_commit":{"sha":"head99"},"commits":[]}`)},
		github.ApiResponse{Status: 200, Body: []byte(`{"status":"ahead","commits":[
			{"sha":"squash","parents":[{"sha":"base01"}]}
		]}`)},
		github.ApiResponse{Status: 200, Body: []byte("")},
	)
	liveRepoNames.Store(github.NewClient(transport, nil))

	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	got, err := (repoComparisonGate{}).Compare(
		context.Background(), repo, "main", "grain/task-12",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Merged || got.MergeBase != "head99" {
		t.Fatalf("comparison = %+v, want the plain merge base back", got)
	}
	if len(transport.Calls) != 3 {
		t.Fatalf("calls = %+v", transport.Calls)
	}
}

func TestRepoComparisonGateRefusesGitHubBeforeTheReconcileLoop(t *testing.T) {
	// Serial: see above.
	t.Cleanup(func() { liveRepoNames.Store(nil) })
	liveRepoNames.Store(nil)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	_, err := (repoComparisonGate{}).Compare(
		context.Background(), repo, "main", "grain/task-12",
	)
	if err == nil || !strings.Contains(err.Error(), "reconcile loop") {
		t.Fatalf("error = %v", err)
	}
}
