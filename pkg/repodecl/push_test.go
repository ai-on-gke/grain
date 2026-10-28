package repodecl_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

// A template's `push:` (grain/task-2536): the refs beyond their own
// branch its tasks' runs may push in this repository, as a bare ref or a
// mapping that also says force: and delete:.
func TestATemplateDeclaresTheRefsItsRunsPush(t *testing.T) {
	declared := load(t, map[string]string{
		".grain/templates/release.yml": "title: Cut a release\n" +
			"push:\n" +
			"  - refs/heads/main\n" +
			"  - ref: refs/heads/candidate\n" +
			"    force: true\n" +
			"  - refs/tags/v*\n" +
			"  - ref: refs/heads/scratch/x-*\n" +
			"    delete: true\n" +
			"  - refs/heads/main\n",
	})
	if len(declared.Templates) != 1 {
		t.Fatalf("templates = %+v", declared.Templates)
	}
	got := declared.Templates[0].Push
	want := model.RefPushSet{
		{Pattern: "refs/heads/main"},
		{Pattern: "refs/heads/candidate", Force: true},
		{Pattern: "refs/tags/v*"},
		{Pattern: "refs/heads/scratch/x-*", Delete: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("push = %+v, want %+v", got, want)
	}
	// Nothing about it lands on the template row itself: the push set is
	// the declaration's, never a setting.
	if tmpl := declared.Templates[0].Template; len(tmpl.Grants) != 0 || len(tmpl.Writes) != 0 {
		t.Errorf("push: wrote grants or writes onto the template: %+v", tmpl)
	}

	if none := load(t, map[string]string{".grain/templates/nightly.yml": "title: Bump dependencies\n"}); len(none.Templates[0].Push) != 0 {
		t.Errorf("a template with no push: declared %v", none.Templates[0].Push)
	}
}

func TestATemplatesPushIsExplicitRefsOnly(t *testing.T) {
	for _, tc := range []struct{ entry, want string }{
		{"main", "a declared push names a full ref"},
		{"refs/pull/1/head", "a declared push names a full ref"},
		{"refs/heads/*", "would match every ref"},
		{"refs/tags/**", "would match every ref"},
		{"refs/heads/", "is not a ref name grain accepts"},
		{"refs/heads/[ab]", "only `*` and `?` glob"},
		{"refs/heads/a..b", "only `*` and `?` glob"},
		{"\"\"", "is not a ref name"},
		{"{ref: refs/heads/main, forse: true}", "forse"},
		{"[refs/heads/main]", "an entry is a ref, or a mapping"},
	} {
		msg := loadErr(t, map[string]string{
			".grain/templates/release.yml": "title: Cut a release\npush:\n  - refs/heads/candidate\n  - " + tc.entry + "\n",
		})
		if !strings.Contains(msg, tc.want) || !strings.Contains(msg, ".grain/templates/release.yml") {
			t.Errorf("%s: error = %q, want the file and %q", tc.entry, msg, tc.want)
		}
	}
}

// A suite and a schedule push nothing of their own: a schedule's runs
// push what the template it fires declares.
func TestOnlyATemplateDeclaresPush(t *testing.T) {
	for path, body := range map[string]string{
		".grain/schedules/it.yml": "template: nightly\npush: [refs/heads/main]\nevery:\n  hours: 6\n",
		".grain/suites/it.yml":    "templates: [nightly]\npush: [refs/heads/main]\n",
	} {
		msg := loadErr(t, map[string]string{
			".grain/templates/nightly.yml": "title: Bump dependencies\n",
			path:                           body,
		})
		if !strings.Contains(msg, "push") {
			t.Errorf("%s: error = %q, want it to name push", path, msg)
		}
	}
}
