package designdoc_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/designdoc"
)

func TestTrailer(t *testing.T) {
	if got := designdoc.Trailer("docs/design/foo/README.md", "D-7"); got != "Design-Item: docs/design/foo/README.md#D-7" {
		t.Errorf("Trailer = %q", got)
	}
}

const doc = "# Foo {#not-a-requirement}\n\n" +
	"Intro.\n\n" +
	"## Requirements\n\n" +
	"### Retag latest on promotion {#D-7}\n\n" +
	"```md\n### Not a heading {#D-99}\n```\n\n" +
	"### Keep the old tag ###\n\n" +
	"### Mirror {#D-2}\n"

func TestHeadingsSkipCodeAndSplitIDs(t *testing.T) {
	got := designdoc.Headings([]byte(doc))
	want := []designdoc.Requirement{
		{ID: "not-a-requirement", Title: "Foo", Level: 1, Line: 1},
		{Title: "Requirements", Level: 2, Line: 5},
		{ID: "D-7", Title: "Retag latest on promotion", Level: 3, Line: 7},
		{Title: "Keep the old tag", Level: 3, Line: 13},
		{ID: "D-2", Title: "Mirror", Level: 3, Line: 15},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Headings =\n%+v\nwant\n%+v", got, want)
	}
}

func TestRequirements(t *testing.T) {
	got, err := designdoc.Requirements([]byte(doc))
	if err == nil || !strings.Contains(err.Error(), "{#not-a-requirement} is not shaped") {
		t.Errorf("err = %v, want the malformed id named", err)
	}
	if len(got) != 2 || got[0].ID != "D-7" || got[1].ID != "D-2" {
		t.Errorf("Requirements = %+v", got)
	}
	_, err = designdoc.Requirements([]byte("### A {#D-1}\n### B {#D-1}\n"))
	if err == nil || !strings.Contains(err.Error(), "already used on line 1") {
		t.Errorf("a repeated id: err = %v", err)
	}
}

func TestNextIDNeverReusesOne(t *testing.T) {
	inDoc := []string{"D-2", "D-7", "API-40"}
	inHistory := []string{"D-9"} // removed from the doc, still taken
	if got := designdoc.NextID("D", inDoc, inHistory); got != "D-10" {
		t.Errorf("NextID = %q, want D-10", got)
	}
	if got := designdoc.NextID("D"); got != "D-1" {
		t.Errorf("NextID of nothing = %q", got)
	}
}

// The design docs this repository ships are well formed, and no ledger
// is left beside any of them: plan.yml is gone (docs/design-docs.md).
func TestThisRepositorysDesignDocsAreWellFormed(t *testing.T) {
	root := filepath.Join("..", "..", "docs", "design")
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if base := filepath.Base(p); base == "plan.yml" || strings.HasSuffix(base, ".plan.yml") {
			t.Errorf("%s: design docs no longer have a ledger; delete it", p)
		}
		if !strings.HasSuffix(p, ".md") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if _, err := designdoc.Requirements(data); err != nil {
			t.Errorf("%s: %v", p, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The trigger no longer ignores ledgers (there are none), and the planner
// no longer mentions writing one.
func TestThePlannerKeepsNoLedger(t *testing.T) {
	for _, p := range []string{
		filepath.Join("testdata", "design-docs.yml"),
		filepath.Join("..", "..", ".grain", "templates", "design-planner.yml"),
	} {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "plan.yml") || strings.Contains(string(data), "ledger") {
			t.Errorf("%s still mentions the ledger", p)
		}
	}
}
