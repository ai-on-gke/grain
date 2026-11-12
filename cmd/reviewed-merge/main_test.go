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
	"os"
	"path/filepath"
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

// TestAnAcceptedCommitPasses: a self-merged commit the -accepted file
// names is let through with the reason recorded; a malformed or missing
// file is a usage error, never a silent pass or refusal.
func TestAnAcceptedCommitPasses(t *testing.T) {
	const sha = "5335ada3c167d292e0f360c1d1bb26cc4ccb4b28"
	f := forge{sha: {Number: 7, Merged: true, MergeCommitSHA: sha, Base: "main", Author: "alice", MergedBy: "alice"}}
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := write("ok.txt", "# comment\n\n"+sha+" PR #7 reviewed out of band, accepted by bob\n")
	var out, errs bytes.Buffer
	args := []string{"-repo", "example/grain", "-commit", sha, "-mergers", "grain[bot]"}
	if code := run(args, &out, &errs, func(string) string { return "" }, f); code != 1 {
		t.Fatalf("exit %d without -accepted, want 1", code)
	}
	out.Reset()
	errs.Reset()
	if code := run(append(args, "-accepted", good), &out, &errs, func(string) string { return "" }, f); code != 0 {
		t.Fatalf("exit %d: %s", code, errs.String())
	}
	if s := out.String(); !strings.Contains(s, `"via":"accepted"`) || !strings.Contains(s, "accepted by bob") {
		t.Fatalf("record = %s", s)
	}
	for name, path := range map[string]string{
		"missing":   filepath.Join(dir, "nope.txt"),
		"short sha": write("short.txt", "5335ada reviewed\n"),
		"no reason": write("noreason.txt", sha+"\n"),
	} {
		out.Reset()
		errs.Reset()
		if code := run(append(args, "-accepted", path), &out, &errs, func(string) string { return "" }, f); code != 2 {
			t.Errorf("%s: exit %d, want 2 (%s)", name, code, errs.String())
		}
	}
}
