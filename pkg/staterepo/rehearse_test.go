package staterepo_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/staterepo"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for path, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The patches DiffTrees writes take one tree to the other, by
// ApplyChanges and by `git apply` alike: an agent handed one can commit
// it either way.
func TestDiffTreesPatchesApplyBackToTheSecondTree(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("row\n", 20)
	before := map[string]string{
		"tables/board.json":      "[\n  {\n    \"id\": \"b1\",\n    \"name\": \"Old\"\n  }\n]\n",
		"settings/schedules.yml": long + "  - id: s1\n    title: nightly\n" + long,
		"scripts/setup.sh":       "echo one",
		"tables/gone.json":       "[]\n",
	}
	after := map[string]string{
		"tables/board.json":      "[\n  {\n    \"id\": \"b1\",\n    \"name\": \"New\"\n  }\n]\n",
		"settings/schedules.yml": long + "  - id: s1\n    title: weekly\n" + long,
		"scripts/setup.sh":       "echo two",
		"tables/fresh.json":      "[\n  {}\n]\n",
	}
	a, b := writeTree(t, before), writeTree(t, after)
	changes, err := staterepo.DiffTrees(a, b)
	if err != nil {
		t.Fatal(err)
	}
	ops := map[string]string{}
	for _, c := range changes {
		ops[c.Path] = c.Op
	}
	if ops["tables/fresh.json"] != staterepo.ChangeCreate || ops["tables/gone.json"] != staterepo.ChangeDelete ||
		ops["tables/board.json"] != staterepo.ChangeModify || len(changes) != 5 {
		t.Fatalf("changes = %+v", changes)
	}
	for _, c := range changes {
		if c.Path == "settings/schedules.yml" && (c.Fragment != "    title: weekly\n" || strings.Count(c.Patch, "\n row") != 5) {
			t.Fatalf("the hunk is not the one line and its context: %+v", c)
		}
	}

	applied := writeTree(t, before)
	if err := staterepo.ApplyChanges(applied, changes); err != nil {
		t.Fatal(err)
	}
	if left, err := staterepo.DiffTrees(applied, b); err != nil || len(left) != 0 {
		t.Fatalf("ApplyChanges left %+v (%v)", left, err)
	}
	// Applied twice, the patch no longer matches, and says so rather
	// than writing the line in a second time.
	if err := staterepo.ApplyChanges(applied, changes[:1]); err == nil {
		t.Fatal("a patch applied to a file it does not describe was accepted")
	}

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git to check the patch against")
	}
	repo := writeTree(t, before)
	var patch strings.Builder
	for _, c := range changes {
		patch.WriteString(c.Patch)
	}
	cmd := exec.Command("git", "apply", "-")
	cmd.Dir = repo
	cmd.Stdin = strings.NewReader(patch.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git apply: %v\n%s\n%s", err, out, patch.String())
	}
	if left, err := staterepo.DiffTrees(repo, b); err != nil || len(left) != 0 {
		t.Fatalf("git apply left %+v (%v)", left, err)
	}
}
