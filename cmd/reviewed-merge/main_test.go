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
