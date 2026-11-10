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
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/capability/intree"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

func boolVal(b bool) *bool             { return &b }
func foldersVal(f ...string) *[]string { s := append([]string{}, f...); return &s }

func writeRepoFile(t *testing.T, root, rel, content string, mode os.FileMode) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestRepoSkillsDiscoveryDefaultFoldersAndCustomFolders(t *testing.T) {
	t.Parallel()
	remoteBase := t.TempDir()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	seedWork := seedRemote(t, remoteBase, repo)

	writeRepoFile(t, seedWork, ".claude/skills/c-skill/SKILL.md", "---\nname: claude-one\ndescription: Claude skill\n---\nBody C\n", 0o644)
	writeRepoFile(t, seedWork, ".agents/skills/a-skill/SKILL.md", "---\ndescription: Agents skill\n---\nBody A\n", 0o644)
	writeRepoFile(t, seedWork, ".codex/skills/x-skill/SKILL.md", "# Title\n\nCodex first paragraph.\n", 0o644)
	writeRepoFile(t, seedWork, ".gemini/skills/g-skill/SKILL.md", "---\nname: gemini-one\ndescription: Gemini skill\n---\nBody G\n", 0o644)
	writeRepoFile(t, seedWork, "skills/s-skill/SKILL.md", "---\ndescription: Root skill\n---\nBody S\n", 0o644)
	writeRepoFile(t, seedWork, "custom/skills/k-skill/SKILL.md", "---\ndescription: Custom folder skill\n---\nBody K\n", 0o644)
	// Nested two levels deep: must be ignored (discovery is strictly */SKILL.md one level deep).
	writeRepoFile(t, seedWork, ".claude/skills/nested/deeper/SKILL.md", "---\ndescription: Too deep\n---\n", 0o644)

	git(t, seedWork, "add", ".")
	git(t, seedWork, "commit", "--quiet", "-m", "feat: add skills across folders")
	git(t, seedWork, "push", "--quiet", "origin", "main")

	sandboxRoot := t.TempDir()
	tools := mcp.NewSandboxTools(sandboxRoot)
	task := model.Task{ID: "101", Title: "Check skills", Body: "Test.", Target: &repo}
	prepared, err := prepareCheckout(context.Background(), tools, remoteBase, task, "", setupNotes{})
	if err != nil {
		t.Fatalf("prepareCheckout: %v", err)
	}

	gotDefault := discoverRepoSkills(context.Background(), tools, prepared, task, model.DefaultSkillFolders, "run-1", nil)
	if len(gotDefault) != 5 {
		t.Fatalf("default discovery = %+v, want 5 skills", gotDefault)
	}
	wantNames := []string{"claude-one", "a-skill", "x-skill", "gemini-one", "s-skill"}
	for i, want := range wantNames {
		if gotDefault[i].Name != want {
			t.Errorf("gotDefault[%d].Name = %q, want %q", i, gotDefault[i].Name, want)
		}
	}
	if gotDefault[2].Description != "Codex first paragraph." {
		t.Errorf("gotDefault[2].Description = %q, want first paragraph", gotDefault[2].Description)
	}

	gotCustom := discoverRepoSkills(context.Background(), tools, prepared, task, []string{"custom/skills", ".gemini/skills"}, "run-2", nil)
	if len(gotCustom) != 2 || gotCustom[0].Name != "k-skill" || gotCustom[1].Name != "gemini-one" {
		t.Fatalf("custom folder discovery = %+v, want [k-skill, gemini-one]", gotCustom)
	}
}

func TestRepoSkillsBaseBranchOnlyReading(t *testing.T) {
	t.Parallel()
	remoteBase := t.TempDir()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	seedWork := seedRemote(t, remoteBase, repo)

	writeRepoFile(t, seedWork, ".claude/skills/legit/SKILL.md", "---\nname: legit\ndescription: Trusted base description\n---\nBase body.\n", 0o644)
	git(t, seedWork, "add", ".")
	git(t, seedWork, "commit", "--quiet", "-m", "feat: add legit skill on main")
	git(t, seedWork, "push", "--quiet", "origin", "main")

	task := model.Task{ID: "202", Title: "Attempt two", Body: "Continue.", Target: &repo}
	branch := task.BranchName()

	// Simulate an earlier attempt pushing a new skill and editing the existing skill on the task's branch.
	git(t, seedWork, "checkout", "--quiet", "-b", branch)
	writeRepoFile(t, seedWork, ".claude/skills/injected/SKILL.md", "---\nname: injected\ndescription: Branch injected skill\n---\nEvil.\n", 0o644)
	writeRepoFile(t, seedWork, ".claude/skills/legit/SKILL.md", "---\nname: legit\ndescription: Tampered on task branch\n---\nTampered.\n", 0o644)
	git(t, seedWork, "add", ".")
	git(t, seedWork, "commit", "--quiet", "-m", "attempt 1 branch commit")
	git(t, seedWork, "push", "--quiet", "origin", branch)

	sandboxRoot := t.TempDir()
	tools := mcp.NewSandboxTools(sandboxRoot)
	prepared, err := prepareCheckout(context.Background(), tools, remoteBase, task, "", setupNotes{})
	if err != nil {
		t.Fatalf("prepareCheckout: %v", err)
	}

	got := discoverRepoSkills(context.Background(), tools, prepared, task, model.DefaultSkillFolders, "run-202", nil)
	if len(got) != 1 {
		t.Fatalf("discovered = %+v, want only 1 skill from base branch", got)
	}
	if got[0].Name != "legit" || got[0].Description != "Trusted base description" {
		t.Errorf("got[0] = %+v, want Trusted base description from base branch", got[0])
	}
}

func TestRepoSkillsPathEscapeSymlinkMalformedAndCaps(t *testing.T) {
	remoteBase := t.TempDir()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	seedWork := seedRemote(t, remoteBase, repo)

	// 1. Committed symlink skill directory and symlink SKILL.md on base branch.
	if err := os.MkdirAll(filepath.Join(seedWork, ".claude", "skills", "symfile"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../README.md", filepath.Join(seedWork, ".claude", "skills", "symdir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../../README.md", filepath.Join(seedWork, ".claude", "skills", "symfile", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	// 2. Malformed SKILL.md (unterminated front matter).
	writeRepoFile(t, seedWork, ".claude/skills/malformed/SKILL.md", "---\nname: broken\ndescription: missing closing fence\n", 0o644)
	// 3. 55 valid skills to exercise MaxRepoSkills (50) and MaxSkillDescriptionChars (500).
	longDesc := strings.Repeat("d", intree.MaxSkillDescriptionChars+80)
	for i := range 55 {
		desc := fmt.Sprintf("Skill number %02d", i)
		if i == 0 {
			desc = longDesc
		}
		writeRepoFile(t, seedWork, fmt.Sprintf("skills/sk-%02d/SKILL.md", i),
			fmt.Sprintf("---\nname: sk-%02d\ndescription: %s\n---\nBody %d\n", i, desc, i), 0o644)
	}

	git(t, seedWork, "add", ".")
	git(t, seedWork, "commit", "--quiet", "-m", "feat: symlinks, malformed, and 55 skills")
	git(t, seedWork, "push", "--quiet", "origin", "main")

	sandboxRoot := t.TempDir()
	tools := mcp.NewSandboxTools(sandboxRoot)
	task := model.Task{ID: "303", Title: "Safety checks", Body: "Check.", Target: &repo}
	prepared, err := prepareCheckout(context.Background(), tools, remoteBase, task, "", setupNotes{})
	if err != nil {
		t.Fatalf("prepareCheckout: %v", err)
	}

	// Also plant a working-tree symlink replacing sk-01 in the checkout to verify checkout symlink rejection.
	sk01Dir := filepath.Join(sandboxRoot, "work", "skills", "sk-01")
	if err := os.RemoveAll(sk01Dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", sk01Dir); err != nil {
		t.Fatal(err)
	}

	var logBuf bytes.Buffer
	prevOut := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(prevOut) })

	folders := append([]string{"../escape"}, model.DefaultSkillFolders...)
	got := discoverRepoSkills(context.Background(), tools, prepared, task, folders, "run-303", nil)
	if len(got) != intree.MaxRepoSkills {
		t.Fatalf("len(got) = %d, want capped at %d", len(got), intree.MaxRepoSkills)
	}
	if utf8.RuneCountInString(got[0].Description) != intree.MaxSkillDescriptionChars+1 {
		t.Errorf("got[0].Description rune len = %d, want %d", utf8.RuneCountInString(got[0].Description), intree.MaxSkillDescriptionChars+1)
	}
	for _, entry := range got {
		if entry.Name == "sk-01" || entry.Name == "broken" || strings.Contains(entry.Path, "sym") {
			t.Errorf("unexpected unsafe or malformed skill indexed: %+v", entry)
		}
	}

	logged := logBuf.String()
	for _, wantLog := range []string{
		`rejecting configured skill folder "../escape"`,
		`.claude/skills/symdir`,
		`.claude/skills/symfile/SKILL.md`,
		`skills/sk-01`,
		`skipping malformed repo skill .claude/skills/malformed/SKILL.md`,
		`capped discovered repo skills at 50`,
	} {
		if !strings.Contains(logged, wantLog) {
			t.Errorf("run log missing %q:\n%s", wantLog, logged)
		}
	}
}

func TestRepoSkillsMergeWithCapabilitySkills(t *testing.T) {
	t.Parallel()
	passSections := `<capability grant="https://grain.dev/capabilities/skill#skill" attempt_id="run-1">
The following was supplied by a capability server, not by the task's author.

Skills are available under /home/debian/.skills; read a skill's SKILL.md before using it:
- /home/debian/.skills/shared/SKILL.md -- Capability version of shared
- /home/debian/.skills/cap-only/SKILL.md -- Capability exclusive skill
</capability>

<capability grant="https://grain.dev/capabilities/gcp#gcp-key" attempt_id="run-2">
The following was supplied by a capability server, not by the task's author.

GCP credentials placed at /home/debian/.config/gcloud.
</capability>`

	repoSkills := []intree.SkillIndexEntry{
		{
			Name:        "shared",
			Path:        "/home/debian/work/.claude/skills/shared/SKILL.md",
			Source:      "repo: .claude/skills/shared",
			RepoRelDir:  ".claude/skills/shared",
			Description: "Repo version of shared",
		},
		{
			Name:        "repo-only",
			Path:        "/home/debian/work/.claude/skills/repo-only/SKILL.md",
			Source:      "repo: .claude/skills/repo-only",
			RepoRelDir:  ".claude/skills/repo-only",
			Description: "Repo exclusive skill",
		},
	}

	keptInProc, unified, keptPass := mergePromptSkills(nil, passSections, nil, repoSkills, "acme/widgets", "run-merge", nil)
	if len(keptInProc) != 0 {
		t.Errorf("keptInProc = %v, want empty", keptInProc)
	}
	if strings.Contains(keptPass, "skill#skill") || !strings.Contains(keptPass, "gcp#gcp-key") {
		t.Errorf("keptPass should strip skill#skill and preserve gcp#gcp-key:\n%s", keptPass)
	}
	for _, want := range []string{
		"- shared: /home/debian/.skills/shared/SKILL.md (capability) -- Capability version of shared",
		"- cap-only: /home/debian/.skills/cap-only/SKILL.md (capability) -- Capability exclusive skill",
		`<repo-skills repo="acme/widgets">`,
		"- .claude/skills/shared: /home/debian/work/.claude/skills/shared/SKILL.md (repo: .claude/skills/shared) -- Repo version of shared",
		"- repo-only: /home/debian/work/.claude/skills/repo-only/SKILL.md (repo: .claude/skills/repo-only) -- Repo exclusive skill",
	} {
		if !strings.Contains(unified, want) {
			t.Errorf("unified skills index missing %q:\n%s", want, unified)
		}
	}
}

type funcFramework func(context.Context, agent.RunConfig) (*agent.Result, error)

func (f funcFramework) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	return f(ctx, cfg)
}

func TestRepoSkillsEndToEndPromptIndexAndSandboxReadFile(t *testing.T) {
	t.Parallel()
	remoteBase := t.TempDir()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	seedWork := seedRemote(t, remoteBase, repo)

	const secretSkillBody = "DETAILED_SKILL_INSTRUCTIONS_ONLY_VIA_READ_FILE_98765"
	writeRepoFile(t, seedWork, ".claude/skills/x/SKILL.md",
		"---\nname: x\ndescription: Helper skill x in claude folder\n---\n# Skill X\n\n"+secretSkillBody+"\n", 0o644)
	writeRepoFile(t, seedWork, ".claude/skills/x/scripts/hello.sh", "#!/bin/sh\necho skill-script-ran\n", 0o755)
	writeRepoFile(t, seedWork, ".gemini/skills/y/SKILL.md",
		"---\nname: y\ndescription: Helper skill y in gemini folder\n---\n# Skill Y\n\nGemini body.\n", 0o644)
	git(t, seedWork, "add", ".")
	git(t, seedWork, "commit", "--quiet", "-m", "feat: add claude skill x and gemini skill y")
	git(t, seedWork, "push", "--quiet", "origin", "main")

	store, ctx := recreateStore(t)
	now := time.Date(2026, 11, 10, 2, 0, 0, 0, time.UTC)

	runTask := func(task model.Task, runID string) (string, string, string) {
		t.Helper()
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatalf("PutTask(%s): %v", task.ID, err)
		}
		if err := store.StartRun(ctx, model.Run{ID: runID, TaskID: task.ID, Attempt: 1, StartedAt: now}, model.Limits{Workers: 10}); err != nil {
			t.Fatalf("StartRun(%s): %v", runID, err)
		}
		sandboxRoot := t.TempDir()
		tools := mcp.NewSandboxTools(sandboxRoot)
		var readTool, cmdTool mcp.Tool
		for _, tool := range tools {
			switch tool.Name {
			case "read_file":
				readTool = tool
			case "run_command":
				cmdTool = tool
			}
		}

		var gotPrompt, readOut, scriptOut string
		fw := funcFramework(func(ctx context.Context, rc agent.RunConfig) (*agent.Result, error) {
			gotPrompt = rc.Prompt
			entries := intree.ParsePromptSkills(rc.Prompt)
			for _, e := range entries {
				if e.Name == "x" {
					r := readTool.Handler(ctx, map[string]any{"file_path": e.Path})
					readOut = r.Text
					sr := cmdTool.Handler(ctx, map[string]any{"command": filepath.ToSlash(filepath.Join(filepath.Dir(e.Path), "scripts/hello.sh"))})
					scriptOut = sr.Text
				}
			}
			return &agent.Result{
				ToolCalls: []agent.ToolCall{{
					Name:      "nothing_needed",
					Arguments: map[string]any{"reason": "verified"},
				}},
			}, nil
		})
		cfg := Config{GitRemoteBase: remoteBase, Now: func() time.Time { return now }}
		d := dispatch.Dispatch{TaskID: task.ID, RunID: runID, Attempt: 1}
		if _, err := RunDispatch(ctx, store, fw, cfg, task, d, tools, sandboxRoot, "", nil, now); err != nil {
			t.Fatalf("RunDispatch(%s): %v", runID, err)
		}
		return gotPrompt, readOut, scriptOut
	}

	// 1. Default config discovers both .claude/skills/x and .gemini/skills/y; read_file reads x in place, and body is NOT in prompt.
	prompt1, read1, script1 := runTask(model.Task{
		ID:        "e2e-1",
		Intent:    model.IntentImplement,
		Title:     "Use skill x",
		Body:      "Check skill x.",
		Origin:    model.Origin{Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "op"}}, Reason: model.ReasonDirect},
		Target:    &repo,
		CreatedAt: &now,
	}, "run-e2e-1")

	if !strings.Contains(prompt1, "(repo: .claude/skills/x) -- Helper skill x in claude folder") {
		t.Errorf("prompt1 missing indexed skill x:\n%s", prompt1)
	}
	if !strings.Contains(prompt1, "(repo: .gemini/skills/y) -- Helper skill y in gemini folder") {
		t.Errorf("prompt1 missing indexed skill y:\n%s", prompt1)
	}
	if strings.Contains(prompt1, secretSkillBody) {
		t.Errorf("prompt1 leaked SKILL.md body instead of progressive disclosure:\n%s", prompt1)
	}
	if !strings.Contains(read1, secretSkillBody) {
		t.Errorf("read_file on indexed path did not reach SKILL.md body; got: %q", read1)
	}
	if !strings.Contains(script1, "skill-script-ran") {
		t.Errorf("run_command on skill helper script failed; got: %q", script1)
	}

	// Recorded prompt in store parses back the shown skills for the task prompt view.
	storedPrompt, found, err := store.RunPrompt(ctx, "e2e-1", 1)
	if err != nil || !found {
		t.Fatalf("RunPrompt: found=%v err=%v", found, err)
	}
	if parsed := intree.ParsePromptSkills(storedPrompt); len(parsed) != 2 {
		t.Errorf("ParsePromptSkills(storedPrompt) = %+v, want 2 skills", parsed)
	}

	// 2. Per-repo config restricting to .claude/skills excludes .gemini/skills/y.
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:   repo,
		Skills: model.NewSkillsConfig(boolVal(true), foldersVal(".claude/skills")),
	}); err != nil {
		t.Fatal(err)
	}
	prompt2, _, _ := runTask(model.Task{
		ID:        "e2e-2",
		Intent:    model.IntentImplement,
		Title:     "Repo claude only",
		Body:      "Check.",
		Origin:    model.Origin{Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "op"}}, Reason: model.ReasonDirect},
		Target:    &repo,
		CreatedAt: &now,
	}, "run-e2e-2")
	if !strings.Contains(prompt2, "(repo: .claude/skills/x)") || strings.Contains(prompt2, "(repo: .gemini/skills/y)") {
		t.Errorf("prompt2 did not apply per-repo folder filter:\n%s", prompt2)
	}

	// 3. Per-template / per-task override selecting only .gemini/skills overrides repo_config.
	tmpl := model.Template{
		ID:        "tmpl-gemini",
		Name:      "Gemini template",
		Title:     "Template task",
		Body:      "Run with gemini.",
		Skills:    model.NewSkillsConfig(nil, foldersVal(".gemini/skills")),
		CreatedAt: now,
	}
	comp := model.ComposeTemplateTask(tmpl, nil, repo, "", true)
	task3 := comp.Task("e2e-3", model.Principal{Kind: model.PrincipalHuman, ID: "op"}, model.ReasonDirect, now)
	prompt3, _, _ := runTask(task3, "run-e2e-3")
	if strings.Contains(prompt3, "(repo: .claude/skills/x)") || !strings.Contains(prompt3, "(repo: .gemini/skills/y)") {
		t.Errorf("prompt3 did not apply template/task override:\n%s", prompt3)
	}

	// 4. Disabled via folders: [] or enabled: false produces no skills index.
	task4 := model.Task{
		ID:        "e2e-4",
		Intent:    model.IntentImplement,
		Title:     "Disabled skills",
		Body:      "Check.",
		Origin:    model.Origin{Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "op"}}, Reason: model.ReasonDirect},
		Target:    &repo,
		Skills:    model.NewSkillsConfig(boolVal(false), nil),
		CreatedAt: &now,
	}
	prompt4, _, _ := runTask(task4, "run-e2e-4")
	if strings.Contains(prompt4, "<repo-skills") || strings.Contains(prompt4, intree.SkillsIndexHeader) {
		t.Errorf("prompt4 should omit skills index when disabled:\n%s", prompt4)
	}
}
