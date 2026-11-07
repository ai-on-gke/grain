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
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

func TestCommittedContextTargetRepoCheckoutAndPrompt(t *testing.T) {
	t.Parallel()
	remoteBase := t.TempDir()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	seedWork := seedRemote(t, remoteBase, repo)

	specDir := filepath.Join(seedWork, "design", "plc", "042")
	if err := os.MkdirAll(specDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(specDir, "spec.md"), []byte("# Billing v2\nApproved spec.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifestYAML := "title: Billing v2 spec\ndescription: Approved product brief and API spec\nfiles:\n  - path: spec.md\n    kind: spec\n    title: Billing specification\n    source_url: https://specs.example.com/plc/042\n"
	if err := os.WriteFile(filepath.Join(specDir, "context.yml"), []byte(manifestYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, seedWork, "add", "design")
	git(t, seedWork, "commit", "--quiet", "-m", "docs(design): land plc/042 spec")
	specSHA := git(t, seedWork, "rev-parse", "HEAD")

	// Advance main past specSHA so specSHA is an ancestor of HEAD rather than HEAD itself.
	if err := os.WriteFile(filepath.Join(seedWork, "README.md"), []byte("seed + code\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, seedWork, "add", "README.md")
	git(t, seedWork, "commit", "--quiet", "-m", "chore: advance main")
	git(t, seedWork, "push", "--quiet", "origin", "main")

	root := t.TempDir()
	task := model.Task{
		ID:     "3134",
		Title:  "[PLC] implement billing v2",
		Body:   "Implement the spec.",
		Target: &repo,
		Links: []model.Link{
			{Kind: model.LinkDesign, Target: "design/plc/042@" + specSHA[:10]},
		},
	}

	prepared, err := prepareCheckout(context.Background(), mcp.NewSandboxTools(root), remoteBase, task, "", setupNotes{})
	if err != nil {
		t.Fatalf("prepareCheckout: %v", err)
	}
	if len(prepared.Context) != 1 {
		t.Fatalf("prepared.Context = %+v, want 1 entry", prepared.Context)
	}
	ctxItem := prepared.Context[0]
	if ctxItem.Placement != "target" {
		t.Fatalf("Placement = %q, want target", ctxItem.Placement)
	}
	if ctxItem.PinnedSHA != specSHA {
		t.Fatalf("PinnedSHA = %q, want full SHA %q", ctxItem.PinnedSHA, specSHA)
	}
	wantLocal := filepath.Join(root, "work", "design", "plc", "042")
	if ctxItem.LocalPath != wantLocal {
		t.Fatalf("LocalPath = %q, want %q", ctxItem.LocalPath, wantLocal)
	}
	if ctxItem.Manifest == nil || ctxItem.Manifest.Title != "Billing v2 spec" || len(ctxItem.Manifest.Files) != 1 {
		t.Fatalf("Manifest = %+v, want parsed context.yml", ctxItem.Manifest)
	}

	// Verify manifest and full SHA persistence onto task in Store.
	store, ctx := recreateStore(t)
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("PutTask: %v", err)
	}
	persistCommittedContextManifests(ctx, store, &task, prepared.Context)
	refs := task.ContextRefs()
	if len(refs) != 1 || refs[0].Rev != specSHA || refs[0].Manifest == nil || refs[0].Manifest.Title != "Billing v2 spec" {
		t.Fatalf("persisted ContextRefs = %+v, want full SHA and manifest", refs)
	}

	prompt := BuildPrompt(task, prepared.Dir, prepared.Root, true, DefaultMaxRunRuntime, History{
		CommittedContext: prepared.Context,
	}, nil)
	for _, want := range []string{
		"### Committed context",
		wantLocal,
		specSHA,
		"Billing v2 spec",
		"https://specs.example.com/plc/042",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("BuildPrompt missing %q:\n%s", want, prompt)
		}
	}

	suspended := BuildSuspendedPrompt(task, prepared.Dir, prepared.Root, History{
		CommittedContext: prepared.Context,
	}, nil, nil)
	if !strings.Contains(suspended, wantLocal) || !strings.Contains(suspended, specSHA) {
		t.Errorf("BuildSuspendedPrompt missing resolved path or SHA:\n%s", suspended)
	}

	// Unmerged commit on another branch in the target repo must fail verification:
	// main's checkout does not contain that commit.
	git(t, seedWork, "checkout", "--quiet", "-b", "unmerged-spec")
	if err := os.WriteFile(filepath.Join(specDir, "draft.md"), []byte("unreviewed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, seedWork, "add", "design")
	git(t, seedWork, "commit", "--quiet", "-m", "unreviewed draft")
	unmergedSHA := git(t, seedWork, "rev-parse", "HEAD")
	git(t, seedWork, "push", "--quiet", "origin", "unmerged-spec")

	badTask := model.Task{
		ID:     "3135",
		Target: &repo,
		Links: []model.Link{
			{Kind: model.LinkDesign, Target: "design/plc/042@" + unmergedSHA},
		},
	}
	if _, err := prepareCheckout(context.Background(), mcp.NewSandboxTools(t.TempDir()), remoteBase, badTask, "", setupNotes{}); err == nil ||
		!strings.Contains(err.Error(), "does not contain committed-context revision") {
		t.Fatalf("expected missing/non-ancestor revision error, got %v", err)
	}
}

func TestCommittedContextExternalReadRepoChecksOutPinnedSHA(t *testing.T) {
	t.Parallel()
	remoteBase := t.TempDir()
	codeRepo := model.RepoRef{Owner: "acme", Name: "public-app"}
	specsRepo := model.RepoRef{Owner: "acme", Name: "private-specs"}
	seedRemote(t, remoteBase, codeRepo)
	specsWork := seedRemote(t, remoteBase, specsRepo)

	specDir := filepath.Join(specsWork, "plc", "042")
	if err := os.MkdirAll(specDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(specDir, "spec.md"), []byte("approved v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(specDir, "context.yml"), []byte("title: Private Spec v1\nfiles:\n  - path: spec.md\n    kind: design\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, specsWork, "add", "plc")
	git(t, specsWork, "commit", "--quiet", "-m", "land approved v1")
	pinnedSHA := git(t, specsWork, "rev-parse", "HEAD")

	// Move default branch (main) ahead with an unapproved v2 so checking out the
	// default branch instead of pinnedSHA would expose the wrong spec.
	if err := os.WriteFile(filepath.Join(specDir, "spec.md"), []byte("unapproved draft v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, specsWork, "add", "plc/042/spec.md")
	git(t, specsWork, "commit", "--quiet", "-m", "draft v2 on main")
	mainTipSHA := git(t, specsWork, "rev-parse", "HEAD")
	git(t, specsWork, "push", "--quiet", "origin", "main")
	if pinnedSHA == mainTipSHA {
		t.Fatal("expected main tip to advance past pinnedSHA")
	}

	// Without -read acme/private-specs, checkout must refuse.
	missingReadTask := model.Task{
		ID:     "4001",
		Target: &codeRepo,
		Links: []model.Link{
			{Kind: model.LinkDesign, Target: "acme/private-specs:plc/042@" + pinnedSHA},
		},
	}
	if _, err := prepareCheckout(context.Background(), mcp.NewSandboxTools(t.TempDir()), remoteBase, missingReadTask, "", setupNotes{}); err == nil ||
		!strings.Contains(err.Error(), "requires -read acme/private-specs") {
		t.Fatalf("expected -read refusal, got %v", err)
	}

	// With -read acme/private-specs, clone lands at repos/acme/private-specs detached at pinnedSHA.
	root := t.TempDir()
	task := model.Task{
		ID:     "4002",
		Title:  "[PLC] implement from private context repo",
		Body:   "Use the pinned spec.",
		Target: &codeRepo,
		Reads:  []model.RepoRef{specsRepo},
		Links: []model.Link{
			{Kind: model.LinkDesign, Target: "acme/private-specs:plc/042@" + pinnedSHA},
		},
	}
	prepared, err := prepareCheckout(context.Background(), mcp.NewSandboxTools(root), remoteBase, task, "", setupNotes{})
	if err != nil {
		t.Fatalf("prepareCheckout: %v", err)
	}
	if len(prepared.Context) != 1 {
		t.Fatalf("prepared.Context = %+v, want 1 entry", prepared.Context)
	}

	clonePath := filepath.Join(root, "repos", "acme", "private-specs")
	if got := git(t, clonePath, "rev-parse", "HEAD"); got != pinnedSHA {
		t.Fatalf("external clone HEAD = %q, want pinnedSHA %q (main tip is %q)", got, pinnedSHA, mainTipSHA)
	}
	if got := git(t, clonePath, "rev-parse", "--abbrev-ref", "HEAD"); got != "HEAD" {
		t.Fatalf("external clone ref = %q, want detached HEAD", got)
	}
	if pushURL := git(t, clonePath, "remote", "get-url", "--push", "origin"); pushURL != "grain-read-only" {
		t.Fatalf("push URL = %q, want grain-read-only", pushURL)
	}
	content, err := os.ReadFile(filepath.Join(clonePath, "plc", "042", "spec.md"))
	if err != nil {
		t.Fatalf("reading pinned spec.md: %v", err)
	}
	if string(content) != "approved v1\n" {
		t.Fatalf("spec.md content = %q, want approved v1", string(content))
	}

	prompt := BuildPrompt(task, prepared.Dir, prepared.Root, true, DefaultMaxRunRuntime, History{
		CommittedContext: prepared.Context,
	}, nil)
	wantSpecPath := filepath.Join(clonePath, "plc", "042")
	for _, want := range []string{
		wantSpecPath,
		"detached at `" + pinnedSHA + "`",
		"Private Spec v1",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("BuildPrompt missing %q:\n%s", want, prompt)
		}
	}

	// No-repo analysis task reading the external context repo also resolves cleanly.
	noRepoRoot := t.TempDir()
	noRepoTask := model.Task{
		ID:    "4003",
		Title: "[PLC] audit spec",
		Reads: []model.RepoRef{specsRepo},
		Links: []model.Link{
			{Kind: model.LinkDesign, Target: "acme/private-specs:plc/042@" + pinnedSHA},
		},
	}
	noRepoPrepared, err := prepareCheckout(context.Background(), mcp.NewSandboxTools(noRepoRoot), remoteBase, noRepoTask, "", setupNotes{})
	if err != nil {
		t.Fatalf("prepareCheckout (no-repo): %v", err)
	}
	if len(noRepoPrepared.Context) != 1 || noRepoPrepared.Context[0].PinnedSHA != pinnedSHA {
		t.Fatalf("noRepoPrepared.Context = %+v, want pinnedSHA %s", noRepoPrepared.Context, pinnedSHA)
	}
}

func TestCommittedContextInheritsAcrossChildTasks(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	now := time.Date(2026, 11, 5, 6, 0, 0, 0, time.UTC)

	codeRepo := model.RepoRef{Owner: "acme", Name: "widgets"}
	otherCodeRepo := model.RepoRef{Owner: "acme", Name: "frontend"}
	specsRepo := model.RepoRef{Owner: "acme", Name: "specs"}
	sha1 := "1111111111111111111111111111111111111111"
	sha2 := "2222222222222222222222222222222222222222"
	sha3 := "3333333333333333333333333333333333333333"

	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: codeRepo}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: otherCodeRepo}); err != nil {
		t.Fatal(err)
	}

	human := model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}}
	parent := model.Task{
		ID:         "100",
		Intent:     model.IntentImplement,
		Title:      "[PLC] parent implementation",
		Body:       "Implement billing v2",
		Origin:     model.Origin{Attribution: human, Reason: model.ReasonDirect},
		Approval:   &human,
		ApprovedAt: &now,
		Target:     &codeRepo,
		AutoMerge:  true,
		Reads:      []model.RepoRef{specsRepo},
		Writes:     []model.WriteTarget{{Repo: otherCodeRepo}},
		Links: []model.Link{
			{Kind: model.LinkDesign, Target: "design/plc/042@" + sha1},
			{Kind: model.LinkDesign, Target: "acme/specs:plc/shared@" + sha2},
		},
		CreatedAt: &now,
	}
	if err := store.PutTask(ctx, parent); err != nil {
		t.Fatal(err)
	}

	// 1. Pre-merge review task inherits both committed-context refs and -read acme/specs.
	reviewTmpl := model.Template{ID: "code-review", Name: "Code Review", Body: "Check against spec."}
	reviewTask, err := newReview(ctx, store, parent, reviewTmpl, "Review", "Review body", false, now)
	if err != nil {
		t.Fatalf("newReview: %v", err)
	}
	if got := len(reviewTask.ContextRefs()); got != 2 {
		t.Fatalf("reviewTask.ContextRefs() = %+v, want 2 inherited refs", reviewTask.ContextRefs())
	}
	if !slices.ContainsFunc(reviewTask.Reads, specsRepo.Same) {
		t.Fatalf("reviewTask.Reads = %+v, want %s inherited", reviewTask.Reads, specsRepo)
	}

	// 2. Port task inherits committed-context refs and -read acme/specs.
	portTask, err := newPortTask(ctx, store, parent, model.PullRequestRef{Repo: codeRepo, Number: 7},
		github.PullRequestDetail{Title: parent.Title, BaseRef: "main", HeadRef: "feat", HeadSHA: sha1}, "release-1.0", now)
	if err != nil {
		t.Fatalf("newPortTask: %v", err)
	}
	if got := len(portTask.ContextRefs()); got != 2 {
		t.Fatalf("portTask.ContextRefs() = %+v, want 2 inherited refs", portTask.ContextRefs())
	}
	if !slices.ContainsFunc(portTask.Reads, specsRepo.Same) {
		t.Fatalf("portTask.Reads = %+v, want %s inherited", portTask.Reads, specsRepo)
	}

	// 3. End-of-run relayProposedTasks:
	//    - same-repo child with no designs inherits both refs + acme/specs read;
	//    - cross-repo child (acme/frontend) with no designs inherits repo-qualified
	//      acme/widgets:design/plc/042@sha1 + acme/specs:plc/shared@sha2 and reads [acme/widgets, acme/specs];
	//    - child with explicit designs overrides parent context.
	res := &agent.Result{
		ToolCalls: []agent.ToolCall{
			{
				Name: "propose_task",
				Arguments: map[string]any{
					"title": "[PLC] same-repo follow-up",
					"body":  "Follow-up for [Task #100](/tasks/100)",
				},
			},
			{
				Name: "propose_task",
				Arguments: map[string]any{
					"title": "[PLC] cross-repo frontend slice",
					"body":  "Frontend slice for [Task #100](/tasks/100)",
					"repo":  "acme/frontend",
				},
			},
			{
				Name: "propose_task",
				Arguments: map[string]any{
					"title":   "[PLC] child with own spec",
					"body":    "Custom spec child for [Task #100](/tasks/100)",
					"designs": []any{"design/plc/099@" + sha3},
				},
			},
		},
	}
	if err := relayProposedTasks(ctx, store, nil, nil, parent, res, now); err != nil {
		t.Fatalf("relayProposedTasks: %v", err)
	}

	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]model.Task{}
	for _, item := range tasks {
		byTitle[item.Title] = item
	}

	sameChild, ok := byTitle["[PLC] same-repo follow-up"]
	if !ok {
		t.Fatal("missing same-repo follow-up")
	}
	if len(sameChild.ContextRefs()) != 2 || !slices.ContainsFunc(sameChild.Reads, specsRepo.Same) {
		t.Fatalf("sameChild context=%+v reads=%+v, want 2 refs and acme/specs read", sameChild.ContextRefs(), sameChild.Reads)
	}
	if sameChild.Approval == nil {
		t.Fatalf("sameChild should self-approve within parent permissions, body:\n%s", sameChild.Body)
	}

	crossChild, ok := byTitle["[PLC] cross-repo frontend slice"]
	if !ok {
		t.Fatal("missing cross-repo frontend slice")
	}
	crossRefs := crossChild.ContextRefs()
	hasWidgetsRef := slices.ContainsFunc(crossRefs, func(r model.ContextRef) bool {
		return r.Repo.Same(codeRepo) && r.Path == "design/plc/042" && r.Rev == sha1
	})
	hasSpecsRef := slices.ContainsFunc(crossRefs, func(r model.ContextRef) bool {
		return r.Repo.Same(specsRepo) && r.Path == "plc/shared" && r.Rev == sha2
	})
	if len(crossRefs) != 2 || !hasWidgetsRef || !hasSpecsRef {
		t.Fatalf("crossChild.ContextRefs() = %+v, want repo-qualified %s and %s refs", crossRefs, codeRepo, specsRepo)
	}
	if !slices.ContainsFunc(crossChild.Reads, codeRepo.Same) || !slices.ContainsFunc(crossChild.Reads, specsRepo.Same) {
		t.Fatalf("crossChild.Reads = %+v, want both %s and %s", crossChild.Reads, codeRepo, specsRepo)
	}
	if crossChild.Approval == nil {
		t.Fatalf("crossChild should self-approve because parent could read both repos, body:\n%s", crossChild.Body)
	}

	overrideChild, ok := byTitle["[PLC] child with own spec"]
	if !ok {
		t.Fatal("missing child with own spec")
	}
	overrideRefs := overrideChild.ContextRefs()
	if len(overrideRefs) != 1 || overrideRefs[0].Path != "design/plc/099" || overrideRefs[0].Rev != sha3 {
		t.Fatalf("overrideChild.ContextRefs() = %+v, want explicit override only", overrideRefs)
	}
}
