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

package ui_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/grainlog"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

const previewDiff = `diff --git a/pkg/a.go b/pkg/a.go
index 1111111..2222222 100644
--- a/pkg/a.go
+++ b/pkg/a.go
@@ -1,3 +1,4 @@ package a
 package a
-const x = 1
+const x = 2
+const token = "grc_supersecrettokenvalue"
@@ -10,2 +11,2 @@ func f() {
-	return
+	return nil
diff --git a/docs/new.md b/docs/new.md
new file mode 100644
--- /dev/null
+++ b/docs/new.md
@@ -0,0 +1,2 @@
+# New
+text
\ No newline at end of file
diff --git a/old.txt b/renamed.txt
similarity index 100%
rename from old.txt
rename to renamed.txt
diff --git a/img.png b/img.png
Binary files a/img.png and b/img.png differ
`

// GET /api/tasks/{id}/diff-preview answers the first hunks of the task's
// branch against its base, file by file, with the deployment's secret
// values cut out, and counts for the whole change (task 4284).
func TestTaskDiffPreviewIsTheFirstHunksRedacted(t *testing.T) {
	t.Parallel()
	client, _, ctx := testClient(t)
	task := create(t, client, ctx)
	comparer := &repoComparer{result: ui.BranchComparison{Base: "main", Head: "grain/task-" + task.ID, Diff: previewDiff}}
	client.Config.RepoComparer = comparer
	client.Config.LogRedactor = func() (*grainlog.Redactor, error) {
		return grainlog.NewRedactor([]grainlog.KnownValue{
			{Label: "secret registry-token", Value: "grc_supersecrettokenvalue"},
		}), nil
	}
	srv := ui.NewServerWithClient(client)

	rec := do(t, srv, http.MethodGet, "/api/tasks/"+task.ID+"/diff-preview", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "grc_supersecrettokenvalue") {
		t.Fatalf("the preview carried a secret value: %s", rec.Body.String())
	}
	if comparer.gotRepo.String() != "acme/widgets" || !strings.Contains(comparer.gotHead, task.ID) {
		t.Fatalf("compared %s ...%q, want the task's own repo and branch", comparer.gotRepo, comparer.gotHead)
	}
	var p ui.DiffPreview
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.TotalFiles != 4 || p.TotalHunks != 3 || p.Truncated || len(p.Files) != 4 {
		t.Fatalf("preview counts %+v: want 4 files, 3 hunks, all kept", p)
	}
	a := p.Files[0]
	if a.Path != "pkg/a.go" || a.Status != "modified" || a.Additions != 3 || a.Deletions != 2 || len(a.Hunks) != 2 {
		t.Fatalf("first file %+v", a)
	}
	if h := a.Hunks[0]; h.Header != "@@ -1,3 +1,4 @@ package a" || len(h.Lines) != 4 ||
		h.Lines[0] != (ui.DiffPreviewLine{Kind: "ctx", Text: "package a"}) ||
		h.Lines[1] != (ui.DiffPreviewLine{Kind: "del", Text: "const x = 1"}) ||
		h.Lines[2] != (ui.DiffPreviewLine{Kind: "add", Text: "const x = 2"}) ||
		!strings.Contains(h.Lines[3].Text, "[redacted") {
		t.Fatalf("first hunk %+v", h)
	}
	if f := p.Files[1]; f.Path != "docs/new.md" || f.Status != "added" || f.Additions != 2 || len(f.Hunks[0].Lines) != 2 {
		t.Fatalf("added file %+v", f)
	}
	if f := p.Files[2]; f.Path != "renamed.txt" || f.OldPath != "old.txt" || f.Status != "renamed" || len(f.Hunks) != 0 {
		t.Fatalf("renamed file %+v", f)
	}
	if f := p.Files[3]; f.Path != "img.png" || !f.Binary {
		t.Fatalf("binary file %+v", f)
	}

	// ?max= keeps the first hunks only, and says what it left out.
	rec = do(t, srv, http.MethodGet, "/api/tasks/"+task.ID+"/diff-preview?max=1", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if !p.Truncated || p.TotalHunks != 3 || len(p.Files) != 1 || len(p.Files[0].Hunks) != 1 || p.Files[0].HunksOmitted != 1 {
		t.Fatalf("max=1 preview %+v: want only the first file's first hunk, the rest counted", p)
	}

	if rec := do(t, srv, http.MethodGet, "/api/tasks/"+task.ID+"/diff-preview?max=zero", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("max=zero: %d, want 400", rec.Code)
	}
	if rec := do(t, srv, http.MethodGet, "/api/tasks/9999/diff-preview", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("an unknown task: %d, want 404", rec.Code)
	}
	// A branch nothing has been pushed to yet is a 404 in grain's words,
	// not an upstream outage.
	comparer.err = fmt.Errorf("comparing: %w", &github.Error{Status: http.StatusNotFound})
	rec = do(t, srv, http.MethodGet, "/api/tasks/"+task.ID+"/diff-preview", "")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "nothing to compare") {
		t.Fatalf("missing branch: %d %s, want 404 saying so", rec.Code, rec.Body.String())
	}
}

// A long hunk is cut to its first lines, and the cut counted.
func TestTaskDiffPreviewCutsALongHunk(t *testing.T) {
	t.Parallel()
	client, _, ctx := testClient(t)
	task := create(t, client, ctx)
	var b strings.Builder
	b.WriteString("diff --git a/big.txt b/big.txt\n--- a/big.txt\n+++ b/big.txt\n@@ -0,0 +1,100 @@\n")
	for i := range 100 {
		fmt.Fprintf(&b, "+line %d %s\n", i, strings.Repeat("x", 400))
	}
	client.Config.RepoComparer = &repoComparer{result: ui.BranchComparison{Diff: b.String()}}
	rec := do(t, ui.NewServerWithClient(client), http.MethodGet, "/api/tasks/"+task.ID+"/diff-preview", "")
	var p ui.DiffPreview
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	h := p.Files[0].Hunks[0]
	if !p.Truncated || len(h.Lines) != 40 || h.LinesOmitted != 60 || p.Files[0].Additions != 100 {
		t.Fatalf("long hunk: %d lines kept, %d omitted, truncated %v", len(h.Lines), h.LinesOmitted, p.Truncated)
	}
	if n := len([]rune(h.Lines[0].Text)); n > 240 {
		t.Fatalf("a kept line is %d runes, want at most 240", n)
	}
}

// A task with no repository has no branch to preview.
func TestTaskDiffPreviewOfANoRepoTask(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	client.Config.RepoComparer = &repoComparer{}
	if err := store.PutTask(ctx, model.Task{ID: "77", Title: "a chore"}); err != nil {
		t.Fatal(err)
	}
	if rec := do(t, ui.NewServerWithClient(client), http.MethodGet, "/api/tasks/77/diff-preview", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("no-repo task: %d %s, want 404", rec.Code, rec.Body.String())
	}
}

// A reader limited to one repository cannot preview another's change:
// the route's tasks:read is held to the task's own repository.
func TestTaskDiffPreviewIsHeldToTheTasksRepository(t *testing.T) {
	ui.LogToTest(t)
	h := newRoleHarness(t)
	installScopeSets(t, model.APIScopeSet{
		Name:        "widgets-reader",
		Description: "Task reader restricted to acme/widgets",
		Scopes:      []string{"tasks:read"},
		Repos:       []string{"acme/widgets"},
	})
	h.allow(t, "user:widgets@example.com", "widgets-reader")
	h.client.Config.RepoComparer = &repoComparer{result: ui.BranchComparison{Diff: previewDiff}}
	seedAPITask(t, h.client, "731", "acme/widgets")
	seedAPITask(t, h.client, "733", "acme/secret")
	if code, body := h.call("widgets@example.com", http.MethodGet, "/api/tasks/731/diff-preview", ""); code != http.StatusOK {
		t.Fatalf("widgets-reader previewing acme/widgets: %d %s", code, body)
	}
	code, body := h.call("widgets@example.com", http.MethodGet, "/api/tasks/733/diff-preview", "")
	if code != http.StatusForbidden && code != http.StatusNotFound {
		t.Fatalf("widgets-reader previewing acme/secret: %d %s, want 403 or 404", code, body)
	}
	if strings.Contains(body, "pkg/a.go") {
		t.Fatalf("a refused preview carried the diff: %s", body)
	}
}
