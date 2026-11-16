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

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/reviewedmerge"
)

type forge map[string]reviewedmerge.Pull

func (f forge) PullsForCommit(_ context.Context, _, sha string) ([]reviewedmerge.Pull, error) {
	if pr, ok := f[sha]; ok {
		return []reviewedmerge.Pull{pr}, nil
	}
	return nil, nil
}

func (forge) Reviews(context.Context, string, int) ([]reviewedmerge.Review, error) { return nil, nil }

func (forge) FirstParent(context.Context, string, string) (string, error) { return "", nil }

func TestPrintsTheRecordOrRefuses(t *testing.T) {
	f := forge{"abc": {Number: 1, Merged: true, MergeCommitSHA: "abc", Base: "main", Author: "grain[bot]", MergedBy: "grain[bot]"}}
	var out, errs bytes.Buffer
	if code := run([]string{"-repo", "example/grain", "-commit", "abc", "-mergers", "grain[bot]"}, &out, &errs, func(string) string { return "" }, f); code != 0 {
		t.Fatalf("exit %d: %s", code, errs.String())
	}
	if !strings.Contains(out.String(), `"via":"grain"`) {
		t.Fatalf("record = %s", out.String())
	}

	out.Reset()
	errs.Reset()
	if code := run([]string{"-repo", "example/grain", "-commit", "abc"}, &out, &errs, func(string) string { return "" }, f); code != 1 {
		t.Fatalf("exit %d with no mergers, want 1", code)
	}
	if !strings.Contains(errs.String(), "GRAIN_MERGE_LOGINS") || out.Len() != 0 {
		t.Fatalf("stdout %q stderr %q", out.String(), errs.String())
	}

	if code := run([]string{"-repo", "example/grain"}, &out, &errs, func(string) string { return "" }, f); code != 2 {
		t.Fatalf("exit %d with no commit, want 2", code)
	}
}

type downForge struct{ forge }

func (downForge) PullsForCommit(context.Context, string, string) ([]reviewedmerge.Pull, error) {
	return nil, errors.New("after 4 attempts: GET /repos/example/grain/commits/abc/pulls: 503 Service Unavailable")
}

func TestAForgeErrorIsNotReportedAsARefusal(t *testing.T) {
	var out, errs bytes.Buffer
	if code := run([]string{"-repo", "example/grain", "-commit", "abc", "-mergers", "grain[bot]"}, &out, &errs, func(string) string { return "" }, downForge{}); code != 3 {
		t.Fatalf("exit %d, want 3: %s", code, errs.String())
	}
	if s := errs.String(); strings.Contains(s, "refused") || !strings.Contains(s, "forge error") || !strings.Contains(s, "could not read the pull requests for abc: after 4 attempts") {
		t.Fatalf("stderr %q", s)
	}
}

type chainForge struct {
	pulls   map[string]reviewedmerge.Pull
	parents map[string]string
}

func (f chainForge) PullsForCommit(_ context.Context, _, sha string) ([]reviewedmerge.Pull, error) {
	if pr, ok := f.pulls[sha]; ok {
		return []reviewedmerge.Pull{pr}, nil
	}
	return nil, nil
}

func (chainForge) Reviews(context.Context, string, int) ([]reviewedmerge.Review, error) {
	return nil, nil
}

func (f chainForge) FirstParent(_ context.Context, _, sha string) (string, error) {
	return f.parents[sha], nil
}

func TestAllListsEveryRefusedPullRequestInChain(t *testing.T) {
	f := chainForge{
		pulls: map[string]reviewedmerge.Pull{
			"c3": {Number: 1626, Merged: true, MergeCommitSHA: "c3", Base: "main", Author: "alice", MergedBy: "alice", HeadSHA: "head1626"},
			"c2": {Number: 1620, Merged: true, MergeCommitSHA: "c2", Base: "main", Author: "grain[bot]", MergedBy: "grain[bot]", HeadSHA: "head1620"},
			"c1": {Number: 1608, Merged: true, MergeCommitSHA: "c1", Base: "main", Author: "bob", MergedBy: "bob", HeadSHA: "head1608"},
		},
		parents: map[string]string{
			"c3": "c2",
			"c2": "c1",
			"c1": "base",
		},
	}

	var out, errs bytes.Buffer
	code := run([]string{"-all", "-repo", "example/grain", "-commit", "c3", "-since", "base", "-mergers", "grain[bot]"}, &out, &errs, func(string) string { return "" }, f)
	if code != 1 {
		t.Fatalf("exit %d, want 1; stdout=%q stderr=%q", code, out.String(), errs.String())
	}
	if out.Len() != 0 {
		t.Fatalf("expected empty stdout on refusal, got %q", out.String())
	}
	refusals, raw, matched := reviewedmerge.ParseRefusalLog(errs.String())
	if !matched || len(raw) != 0 || len(refusals) != 2 {
		t.Fatalf("ParseRefusalLog(%q) = refusals=%+v raw=%q matched=%v", errs.String(), refusals, raw, matched)
	}
	if refusals[0].Pull != 1626 || refusals[0].MergedBy != "alice" || refusals[0].HeadSHA != "head1626" {
		t.Errorf("refusals[0] = %+v", refusals[0])
	}
	if refusals[1].Pull != 1608 || refusals[1].MergedBy != "bob" || refusals[1].HeadSHA != "head1608" {
		t.Errorf("refusals[1] = %+v", refusals[1])
	}

	// Passing -all still outputs the JSON record and exits 0.
	out.Reset()
	errs.Reset()
	code = run([]string{"-all", "-repo", "example/grain", "-commit", "c2", "-since", "c1", "-mergers", "grain[bot]"}, &out, &errs, func(string) string { return "" }, f)
	if code != 0 {
		t.Fatalf("passing -all exit %d: %s", code, errs.String())
	}
	if !strings.Contains(out.String(), `"pull":1620`) {
		t.Fatalf("stdout = %q", out.String())
	}

	// Forge error with -all exits 3, not 1.
	out.Reset()
	errs.Reset()
	code = run([]string{"-all", "-repo", "example/grain", "-commit", "c3", "-mergers", "grain[bot]"}, &out, &errs, func(string) string { return "" }, downForge{})
	if code != 3 {
		t.Fatalf("forge error -all exit %d, want 3: %s", code, errs.String())
	}
}
