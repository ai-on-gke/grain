package model_test

import (
	"strconv"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
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
		"grain/{deployment}-task-{id}-{slug}", "grain/task-{id}-{instance}-{id}-{slug}", "grain/task-{id}.{slug}", "grain/task-{id}_{slug}"} {
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
