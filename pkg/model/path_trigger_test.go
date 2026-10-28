package model

import (
	"reflect"
	"testing"
)

func TestMatchPath(t *testing.T) {
	cases := []struct {
		pattern, file string
		want          bool
	}{
		{"docs/design", "docs/design/foo/README.md", true},
		{"docs/design", "docs/design", true},
		{"docs/design/", "docs/design/x.md", true},
		{"docs/design", "docs/designs/x.md", false},
		{"docs/design/**", "docs/design/foo/README.md", true},
		{"docs/design/**", "docs/design/x.md", true},
		{"docs/design/**", "docs/other/x.md", false},
		{"**/plan.yml", "docs/design/foo/plan.yml", true},
		{"**/plan.yml", "plan.yml", true},
		{"**/plan.yml", "docs/plan.yaml", false},
		{"pkg/*/secret.go", "pkg/a/secret.go", true},
		{"pkg/*/secret.go", "pkg/a/b/secret.go", false},
		{"pkg/**/secret.go", "pkg/a/b/secret.go", true},
		{"*.md", "README.md", true},
		{"*.md", "docs/README.md", false},
	}
	for _, c := range cases {
		if got := MatchPath(c.pattern, c.file); got != c.want {
			t.Errorf("MatchPath(%q, %q) = %v, want %v", c.pattern, c.file, got, c.want)
		}
	}
}

func TestValidatePathPattern(t *testing.T) {
	for _, ok := range []string{"docs", "docs/design/**", "**/plan.yml", "a/[bc]/d"} {
		if err := ValidatePathPattern(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "/", "a/[b", "docs/**.md"} {
		if err := ValidatePathPattern(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestPathTriggerMatchAppliesIgnores(t *testing.T) {
	trigger := PathTrigger{Paths: []string{"docs/design/**"}, Ignore: []string{"**/plan.yml"}}
	got := trigger.Match([]string{"pkg/x.go", "docs/design/foo/plan.yml", "docs/design/foo/README.md"})
	if want := []string{"docs/design/foo/README.md"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Match = %v, want %v", got, want)
	}
	if got := trigger.Match([]string{"docs/design/foo/plan.yml"}); len(got) != 0 {
		t.Errorf("a change only to ignored paths fired: %v", got)
	}
}

func TestPathTriggerGroups(t *testing.T) {
	files := []string{"docs/design/bar/a.md", "docs/design/foo/README.md", "docs/design/foo/sub/b.md",
		"docs/design/index.md", "rfcs/1.md"}
	perFolder := PathTrigger{Paths: []string{"docs/design/**", "rfcs"}, Per: TriggerPerFolder}
	want := map[string][]string{
		"docs/design/bar":      {"docs/design/bar/a.md"},
		"docs/design/foo":      {"docs/design/foo/README.md", "docs/design/foo/sub/b.md"},
		"docs/design/index.md": {"docs/design/index.md"},
		"rfcs/1.md":            {"rfcs/1.md"},
	}
	if got := perFolder.Groups(perFolder.Match(files)); !reflect.DeepEqual(got, want) {
		t.Errorf("per folder = %v, want %v", got, want)
	}
	perChange := PathTrigger{Paths: []string{"docs/design/**"}, Per: TriggerPerChange}
	if got := perChange.Groups(perChange.Match(files)); len(got) != 1 || len(got[""]) != 4 {
		t.Errorf("per change = %v, want one group of four", got)
	}
}

func TestWatchesBranch(t *testing.T) {
	if !(PathTrigger{}).WatchesBranch("main", "main") || (PathTrigger{}).WatchesBranch("dev", "main") {
		t.Error("no branches should mean the default branch only")
	}
	rc := PathTrigger{Branches: []string{"rc/v*"}}
	if !rc.WatchesBranch("rc/v1.2", "main") || rc.WatchesBranch("main", "main") {
		t.Error("a branch glob is not matched")
	}
}

func TestStricterHuman(t *testing.T) {
	if got := StricterHuman(TriggerHumanNo, TriggerHumanRequired); got != TriggerHumanRequired {
		t.Errorf("got %q", got)
	}
	if got := StricterHuman(TriggerHumanOnFindings, TriggerHumanNo); got != TriggerHumanOnFindings {
		t.Errorf("got %q", got)
	}
	if got := StricterHuman("", TriggerHumanNo); got != TriggerHumanNo {
		t.Errorf("got %q", got)
	}
}
