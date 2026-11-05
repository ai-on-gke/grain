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

package model_test

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestFormatBranchName(t *testing.T) {
	for _, tc := range []struct {
		name, template, want string
	}{
		{"compatible default", "", "grain/task-42"},
		{"username", "alice/grain/task-{id}", "alice/grain/task-42"},
		{"installation", "grain/{instance}/task-{id}", "grain/abc123/task-42"},
		{"deployment", "grain/{deployment}/task-{id}", "grain/prod-1/task-42"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := model.FormatBranchName(tc.template, "abc123", "prod-1", "42")
			if err != nil || got != tc.want {
				t.Fatalf("FormatBranchName(%q) = %q, %v; want %q", tc.template, got, err, tc.want)
			}
		})
	}
}

func TestFormatBranchNameRefusesUnsafeOrAmbiguousTemplates(t *testing.T) {
	for _, template := range []string{"alice/tasks", "grain/{user}/task-{id}", "grain/task-{id}..lock",
		// A {slug} that could make a name that is another task's
		// (checkSlugPlacement): one running into the id, one before the
		// id or in a segment of its own, where it could spell out
		// another deployment's id, one with text after it, one sharing
		// its segment with {deployment} or {instance}, one after a
		// separator other than a hyphen.
		"grain/task-{id}{slug}", "grain/{slug}{id}", "grain/task-{id}1{slug}", "grain/{slug}-{deployment}{id}",
		"grain/{slug}9{id}", "grain/task-{id}-{slug}-{slug}", "grain/{slug}/task-{id}", "{slug}-task-{id}",
		"grain/task-{id}x{slug}", "grain/{id}/{deployment}-{slug}", "grain/task-{id}-{slug}-x", "grain/task-{id}/{slug}",
		"grain/{deployment}-task-{id}-{slug}", "grain/task-{id}-{instance}-{id}-{slug}", "grain/task-{id}.{slug}", "grain/task-{id}_{slug}",
		// Names git check-ref-format refuses within the safe charset: a
		// component ending in ".lock" or starting with ".", and a name
		// ending in ".". Settings → Agents saved the first on 2026-10-30.
		"grain/task-{id}.lock", "grain/.hidden/task-{id}", "grain/task-{id}."} {
		if _, err := model.FormatBranchName(template, "abc123", "prod-1", "42"); err == nil {
			t.Errorf("FormatBranchName(%q) succeeded, want an error", template)
		}
	}
}

func TestTaskBranchNameFallsBackForOldRows(t *testing.T) {
	if got := (model.Task{ID: "42"}).BranchName(); got != "grain/task-42" {
		t.Fatalf("BranchName() = %q", got)
	}
	if got := (model.Task{ID: "42", Branch: "alice/task-42"}).BranchName(); got != "alice/task-42" {
		t.Fatalf("stored BranchName() = %q", got)
	}
}

func TestEffectiveDeploymentIDDefaultsToTheStartOfTheInstanceID(t *testing.T) {
	const instance = "0123456789abcdef0123456789abcdef"
	if got := model.EffectiveDeploymentID("", instance); got != "01234567" {
		t.Errorf("default = %q, want 01234567", got)
	}
	if got := model.EffectiveDeploymentID("prod", instance); got != "prod" {
		t.Errorf("configured = %q, want prod", got)
	}
}

func TestValidateDeploymentID(t *testing.T) {
	for _, id := range []string{"", "prod", "us-east-1", "a", "0123456789abcdef0123456789abcdef"} {
		if err := model.ValidateDeploymentID(id); err != nil {
			t.Errorf("ValidateDeploymentID(%q) = %v, want nil", id, err)
		}
	}
	for _, id := range []string{"Prod", "a/b", "-prod", "prod-", "a b", "a.b", "{id}", "0123456789abcdef0123456789abcdef0"} {
		if err := model.ValidateDeploymentID(id); err == nil {
			t.Errorf("ValidateDeploymentID(%q) succeeded, want an error", id)
		}
	}
}

func TestFormatTaskBranchNameSlug(t *testing.T) {
	for _, tc := range []struct {
		name, template, title, want string
	}{
		{"default", model.DefaultTaskBranchTemplate, "Task summary sufix names", "grain/prod-1/task-42-task-summary-sufix-names"},
		{"untitled drops the separator", model.DefaultTaskBranchTemplate, "", "grain/prod-1/task-42"},
		{"nothing usable in the title", model.DefaultTaskBranchTemplate, "¿¡ — !?", "grain/prod-1/task-42"},
		{"titled with digits", model.DefaultTaskBranchTemplate, "Add 2FA", "grain/prod-1/task-42-add-2fa"},
		{"a title of lock", model.DefaultTaskBranchTemplate, "lock", "grain/prod-1/task-42-lock"},
		{"no slug placeholder ignores the title", "grain/{deployment}/task-{id}", "Fix it", "grain/prod-1/task-42"},
		{"legacy empty template ignores the title", "", "Fix it", "grain/task-42"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := model.FormatTaskBranchName(tc.template, "abc123", "prod-1", "42", tc.title)
			if err != nil || got != tc.want {
				t.Fatalf("FormatTaskBranchName(%q, %q) = %q, %v; want %q", tc.template, tc.title, got, err, tc.want)
			}
		})
	}
}

func TestBranchSlug(t *testing.T) {
	for title, want := range map[string]string{
		"":                         "",
		"Task summary sufix names": "task-summary-sufix-names",
		"Fix the off-by-one in the header parser":  "fix-the-off-by-one-in-the",
		"  [UI] Don't  show__stale   PRs!! ":       "ui-don-t-show-stale-prs",
		"Port #123 to release/1.2":                 "port-123-to-release-1-2",
		"Überprüfung der Größe":                    "berpr-fung-der-gr-e",
		"supercalifragilisticexpialidociousnessly": "supercalifragilisticexpialidoc",
		"日本語": "",
	} {
		got := model.BranchSlug(title)
		if got != want {
			t.Errorf("BranchSlug(%q) = %q, want %q", title, got, want)
		}
		if len(got) > model.MaxBranchSlugLen {
			t.Errorf("BranchSlug(%q) = %q, longer than %d", title, got, model.MaxBranchSlugLen)
		}
	}
}

// A slugged branch still names its task wherever a branch is accepted
// as a task reference (depends_on, supersedes, reverts).
func TestNormalizeTaskIDTakesTheSlugOffABranch(t *testing.T) {
	for raw, want := range map[string]string{
		"grain/prod/task-56-task-summary-sufix": "56",
		"grain/task-56-fix-it":                  "56",
		"task-56-fix-it":                        "56",
		"grain/prod/task-56":                    "56",
		"56":                                    "56",
		// Not a task branch: left as it was given, as before.
		"sched-3": "sched-3",
	} {
		if got := model.NormalizeTaskID(raw); got != want {
			t.Errorf("NormalizeTaskID(%q) = %q, want %q", raw, got, want)
		}
	}
}

// No two tasks share a branch under the default template, however their
// ids and titles line up: task 5 titled "6 fix" and task 56 titled "fix"
// are the case a {slug} touching {id} would have merged (grain/task-56's
// review), and the untitled task 56 must not meet either.
func TestDefaultTaskBranchTemplateNeverGivesTwoTasksOneBranch(t *testing.T) {
	titles := []string{"", "fix", "6 fix", "6", "56 fix", "1", "12", "fix 6", "Fix the header parser", "2fa"}
	seen := map[string]string{}
	for id := 1; id <= 130; id++ {
		for _, title := range titles {
			name, err := model.FormatTaskBranchName(model.DefaultTaskBranchTemplate, "abc123", "prod-1", strconv.Itoa(id), title)
			if err != nil {
				t.Fatalf("FormatTaskBranchName(%d, %q) = %v", id, title, err)
			}
			if prev, ok := seen[name]; ok && prev != strconv.Itoa(id) {
				t.Fatalf("task %d titled %q and task %s both get %q", id, title, prev, name)
			}
			seen[name] = strconv.Itoa(id)
			if got := model.NormalizeTaskID(name); got != strconv.Itoa(id) {
				t.Fatalf("NormalizeTaskID(%q) = %q, want %d", name, got, id)
			}
		}
	}
}

// A title cannot make another deployment's branch under the default
// template either: not by spelling out a deployment id
// (grain/{slug}/task-{id} would have put task 5 titled "d4b120da" on
// deployment d4b120da's grain/d4b120da/task-5), and not by spelling out
// the rest of a deployment id that starts like this one's
// ({deployment}-{id}-{slug} would have put deployment x's task 5 titled
// "7 y" on deployment x-5's task 7 titled "y").
func TestDefaultTaskBranchTemplateKeepsDeploymentsApart(t *testing.T) {
	deployments := []string{"x", "x-5", "d4b120da", "prod-1", "prod"}
	titles := []string{"", "y", "7 y", "d4b120da", "x 5", "prod 1", "grain/prod/task-5", "5 7 y", "task 5"}
	seen := map[string]string{}
	for _, dep := range deployments {
		for id := 1; id <= 60; id++ {
			owner := dep + "/" + strconv.Itoa(id)
			for _, title := range titles {
				name, err := model.FormatTaskBranchName(model.DefaultTaskBranchTemplate, "abc123", dep, strconv.Itoa(id), title)
				if err != nil {
					t.Fatalf("FormatTaskBranchName(%s, %d, %q) = %v", dep, id, title, err)
				}
				if prev, ok := seen[name]; ok && prev != owner {
					t.Fatalf("deployment %s task %d titled %q and %s both get %q", dep, id, title, prev, name)
				}
				seen[name] = owner
			}
		}
	}
}

// Templates other than the default that end in {id}, a hyphen and
// {slug}, with no {deployment} or {instance} beside it, are accepted.
func TestFormatTaskBranchNameAcceptsASeparatedSlug(t *testing.T) {
	for _, template := range []string{"grain/task-{id}-{slug}", "alice/{instance}/{id}-{slug}",
		"grain/{deployment}/{id}/task-{id}-{slug}", "{id}-{slug}"} {
		if _, err := model.FormatTaskBranchName(template, "abc123", "prod-1", "42", "6 fix"); err != nil {
			t.Errorf("FormatTaskBranchName(%q) = %v, want it accepted", template, err)
		}
	}
}

// ValidBranchName refuses every name git itself would refuse within the
// charset it allows, and keeps the dotted names git takes.
func TestValidBranchNameFollowsGitRefRules(t *testing.T) {
	for name, want := range map[string]bool{
		"main":                  true,
		"release/1.0":           true,
		"grain/task-42.v2":      true,
		"feature/x.locked":      true,
		"grain/task-42.lock":    false,
		"grain/x.lock/task-42":  false,
		"grain/.hidden/task-42": false,
		"grain/task-42.":        false,
		"grain/task-42/":        false,
		"grain//task-42":        false,
		"grain/task..42":        false,
		strings.Repeat("a", model.MaxBranchNameLen):   true,
		strings.Repeat("a", model.MaxBranchNameLen+1): false,
	} {
		if got := model.ValidBranchName(name); got != want {
			t.Errorf("ValidBranchName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestFormatTaskBranchNameCapsTotalBranchLength(t *testing.T) {
	longTitle := "Fix the header parser when handling an extraordinarily verbose multi-kilobyte upstream response"

	// DefaultTaskBranchTemplate with max deployment ID (32), max int64 task ID (19), and a long title.
	defBranch, err := model.FormatTaskBranchName(
		model.DefaultTaskBranchTemplate,
		strings.Repeat("f", 32),
		strings.Repeat("d", model.MaxDeploymentIDLen),
		"9223372036854775807",
		longTitle,
	)
	if err != nil {
		t.Fatalf("DefaultTaskBranchTemplate with max inputs failed: %v", err)
	}
	if len(defBranch) > model.MaxBranchNameLen {
		t.Fatalf("DefaultTaskBranchTemplate produced %d-char branch %q, want <= %d", len(defBranch), defBranch, model.MaxBranchNameLen)
	}

	// Custom template leaving room for only part of the slug (12 chars after "task-42-").
	// base = prefix + "/task-42" -> want len(base) == MaxBranchNameLen - 1 - 12 = 231 -> len(prefix) = 223.
	prefix223 := strings.Repeat("a", 100) + "/" + strings.Repeat("b", 122)
	trimmed, err := model.FormatTaskBranchName(prefix223+"/task-{id}-{slug}", "inst", "dep", "42", longTitle)
	if err != nil {
		t.Fatalf("FormatTaskBranchName with tight slug budget failed: %v", err)
	}
	if len(trimmed) > model.MaxBranchNameLen {
		t.Fatalf("trimmed branch length = %d, want <= %d: %q", len(trimmed), model.MaxBranchNameLen, trimmed)
	}
	if wantSuffix := "/task-42-fix-the"; !strings.HasSuffix(trimmed, wantSuffix) {
		t.Fatalf("trimmed branch = %q, want suffix %q", trimmed, wantSuffix)
	}

	// Custom template whose base fills MaxBranchNameLen exactly omits -{slug} rather than overflowing.
	exactPrefix := strings.Repeat("a", 100) + "/" + strings.Repeat("b", model.MaxBranchNameLen-100-1-len("/task-42"))
	exact, err := model.FormatTaskBranchName(exactPrefix+"/task-{id}-{slug}", "inst", "dep", "42", longTitle)
	if err != nil {
		t.Fatalf("FormatTaskBranchName at exact MaxBranchNameLen failed: %v", err)
	}
	if len(exact) != model.MaxBranchNameLen || !strings.HasSuffix(exact, "/task-42") {
		t.Fatalf("exact-cap branch = %q (len %d), want len %d ending in /task-42", exact, len(exact), model.MaxBranchNameLen)
	}

	// Template whose non-slug prefix already exceeds MaxBranchNameLen is refused.
	tooLongPrefix := strings.Repeat("a", model.MaxBranchNameLen)
	if _, err := model.FormatTaskBranchName(tooLongPrefix+"/task-{id}-{slug}", "inst", "dep", "42", longTitle); err == nil {
		t.Fatal("FormatTaskBranchName accepted a template exceeding MaxBranchNameLen, want an error")
	}
}

func TestVeryLongTaskNamesProduceValidBranchesAndGitRefs(t *testing.T) {
	tightPrefix := strings.Repeat("a", 100) + "/" + strings.Repeat("b", 120)
	exactPrefix := strings.Repeat("a", 100) + "/" + strings.Repeat("b", model.MaxBranchNameLen-100-1-len("/task-1000"))

	titles := []struct {
		name  string
		title string
	}{
		{
			name:  "multi-kilobyte sentence",
			title: strings.Repeat("Investigate and repair intermittent streaming timeout across multi-region clusters ", 80),
		},
		{
			name:  "single unbroken 2000-character token",
			title: strings.Repeat("supercalifragilisticexpialidocious", 60),
		},
		{
			name:  "multibyte unicode and .lock tokens",
			title: strings.Repeat("日本語テスト 🚀 fix index.lock & refs.lock handling — ", 100),
		},
		{
			name:  "multiline huge pasted stack trace title",
			title: "Panic in workergoroutine:\n" + strings.Repeat("goroutine 1 [running]: main.run() /src/main.go:123 +0x45\n", 100),
		},
	}

	templates := []string{
		model.DefaultTaskBranchTemplate,
		tightPrefix + "/task-{id}-{slug}",
		exactPrefix + "/task-{id}-{slug}",
	}

	bareDir := filepath.Join(t.TempDir(), "remote.git")
	workDir := filepath.Join(t.TempDir(), "work")
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit(t.TempDir(), "init", "--bare", bareDir)
	runGit(t.TempDir(), "clone", bareDir, workDir)
	runGit(workDir, "commit", "--allow-empty", "-m", "initial")
	runGit(workDir, "push", "origin", "HEAD:refs/heads/main")

	id := 1000
	for _, tc := range titles {
		slug := model.BranchSlug(tc.title)
		if len(slug) > model.MaxBranchSlugLen {
			t.Fatalf("%s: BranchSlug length = %d (%q), want <= %d", tc.name, len(slug), slug, model.MaxBranchSlugLen)
		}
		if slug == "" {
			t.Fatalf("%s: expected non-empty BranchSlug", tc.name)
		}
		for _, tpl := range templates {
			id++
			branch, err := model.FormatTaskBranchName(tpl, "abcdef01", "049f1e09", strconv.Itoa(id), tc.title)
			if err != nil {
				t.Fatalf("%s with template len %d: FormatTaskBranchName error: %v", tc.name, len(tpl), err)
			}
			if len(branch) > model.MaxBranchNameLen {
				t.Fatalf("%s: branch length = %d (%q), want <= %d", tc.name, len(branch), branch, model.MaxBranchNameLen)
			}
			if !model.ValidBranchName(branch) {
				t.Fatalf("%s: ValidBranchName(%q) = false", tc.name, branch)
			}
			runGit(workDir, "check-ref-format", "--branch", branch)
			runGit(workDir, "checkout", "-b", branch, "origin/main")
			runGit(workDir, "commit", "--allow-empty", "-m", "work for "+strconv.Itoa(id))
			runGit(workDir, "push", "origin", branch)
		}
	}
}
