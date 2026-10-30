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

package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The same change made on two branches reads the same once line numbers
// and blob ids are gone, and reads differently the moment a line of it
// does.
func TestFilePatchesComparesChangesTheWayPatchIDDoes(t *testing.T) {
	onMain := "diff --git a/pkg/ui/server.go b/pkg/ui/server.go\n" +
		"index 1111111..2222222 100644\n" +
		"--- a/pkg/ui/server.go\n" +
		"+++ b/pkg/ui/server.go\n" +
		"@@ -10,6 +10,7 @@ func serve() {\n" +
		" \tmux := http.NewServeMux()\n" +
		"+\tmux.Handle(\"/api/x\", authorize(x))\n" +
		" \treturn mux\n" +
		"diff --git a/README.md b/README.md\n" +
		"index 3333333..4444444 100644\n" +
		"--- a/README.md\n" +
		"+++ b/README.md\n" +
		"@@ -1 +1 @@\n" +
		"-old\n" +
		"+new\n"
	onCandidate := "diff --git a/pkg/ui/server.go b/pkg/ui/server.go\n" +
		"index 5555555..6666666 100644\n" +
		"--- a/pkg/ui/server.go\n" +
		"+++ b/pkg/ui/server.go\n" +
		"@@ -42,6 +42,7 @@ func serveAll() {\n" +
		" \tmux := http.NewServeMux()\n" +
		"+\tmux.Handle(\"/api/x\", authorize(x))\n" +
		" \treturn mux\n"

	a, ok := filePatches(onMain)
	if !ok {
		t.Fatal("a plain diff did not parse")
	}
	b, ok := filePatches(onCandidate)
	if !ok {
		t.Fatal("a plain diff did not parse")
	}
	if a["pkg/ui/server.go"] == "" || a["pkg/ui/server.go"] != b["pkg/ui/server.go"] {
		t.Fatalf("the same change on two bases reads differently:\n%q\n%q", a["pkg/ui/server.go"], b["pkg/ui/server.go"])
	}
	if _, ok := a["README.md"]; !ok {
		t.Fatalf("second file missing: %v", a)
	}

	changed, _ := filePatches(onCandidate[:len(onCandidate)-len(" \treturn mux\n")] + "+\treturn nil\n")
	if changed["pkg/ui/server.go"] == a["pkg/ui/server.go"] {
		t.Fatal("a different change read the same")
	}
}

func TestFilePatchesKeysARenameByBothPaths(t *testing.T) {
	diff := "diff --git a/pkg/secrets/old.go b/pkg/secrets/new.go\n" +
		"similarity index 90%\n" +
		"rename from pkg/secrets/old.go\n" +
		"rename to pkg/secrets/new.go\n" +
		"index 1111111..2222222 100644\n" +
		"--- a/pkg/secrets/old.go\n" +
		"+++ b/pkg/secrets/new.go\n" +
		"@@ -1 +1 @@\n" +
		"-a\n" +
		"+b\n"
	got, ok := filePatches(diff)
	if !ok {
		t.Fatal("a rename did not parse")
	}
	if got["pkg/secrets/old.go"] == "" || got["pkg/secrets/old.go"] != got["pkg/secrets/new.go"] {
		t.Fatalf("rename not keyed by both paths: %v", got)
	}
}

// A patch whose text says nothing about what changed cannot be compared
// on its text, so a diff holding one compares as nothing at all.
func TestFilePatchesRefusesWhatItCannotCompare(t *testing.T) {
	for name, diff := range map[string]string{
		"binary": "diff --git a/pkg/secrets/key.bin b/pkg/secrets/key.bin\n" +
			"index 1111111..2222222 100644\n" +
			"Binary files a/pkg/secrets/key.bin and b/pkg/secrets/key.bin differ\n",
		"quoted path": "diff --git \"a/pkg/secrets/\\303\\251.go\" \"b/pkg/secrets/\\303\\251.go\"\n" +
			"--- \"a/pkg/secrets/\\303\\251.go\"\n" +
			"+++ \"b/pkg/secrets/\\303\\251.go\"\n" +
			"@@ -1 +1 @@\n-a\n+b\n",
		"not a diff": "<html>too large</html>\n",
	} {
		if _, ok := filePatches(diff); ok {
			t.Errorf("%s: compared as if its text said what changed", name)
		}
	}
}

// A task with two open pull requests has one security hold, about the one
// model.TaskPullRequest names. The other is neither merged on that hold's
// clearance nor classified in its place, which would overwrite it.
func TestSecurityHoldIsKeyedToTheTasksOwnPullRequest(t *testing.T) {
	repo := model.RepoRef{Owner: "ai-on-gke", Name: "grain"}
	task := model.Task{ID: "t1", Target: &repo, AutoMerge: true, Links: []model.Link{
		{Kind: model.LinkFixes, Target: "ai-on-gke/grain#100"},
		{Kind: model.LinkFixes, Target: "ai-on-gke/grain#99"},
		{Kind: model.LinkTrustBoundaryCleared, Target: clearedTarget("abc", model.RepoRules{})},
	}}
	own := model.PullRequestRef{Repo: repo, Number: 100}
	stale := model.PullRequestRef{Repo: repo, Number: 99}
	if !holdIsAbout(task, own) || holdIsAbout(task, stale) {
		t.Fatalf("holdIsAbout(#100, #99) = %v, %v; want true, false", holdIsAbout(task, own), holdIsAbout(task, stale))
	}
	cleared, err := clearedForHead(context.Background(), nil, nil, task, stale, "def", time.Time{})
	if err != nil || cleared {
		t.Fatalf("clearedForHead(#99) = %v, %v; want false, nil", cleared, err)
	}
	if !holdIsAbout(model.Task{ID: "t2", Target: &repo}, stale) {
		t.Fatal("a task with no pull request linked yet refused the one being classified")
	}
}
