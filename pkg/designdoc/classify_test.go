package designdoc_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/designdoc"
)

const docPath = "docs/design/foo/README.md"

// fixture is a throwaway git repository the planner's history is read
// from, the way the planner reads its own branch.
type fixture struct {
	t   *testing.T
	dir string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	f := &fixture{t: t, dir: t.TempDir()}
	f.git("init", "-q", "-b", "main")
	f.git("config", "user.email", "test@example.com")
	f.git("config", "user.name", "test")
	return f
}

func (f *fixture) git(args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = f.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes doc (when not empty) and a code file, and commits with
// message; it returns the new commit.
func (f *fixture) commit(doc, message string) string {
	f.t.Helper()
	if doc != "" {
		p := filepath.Join(f.dir, docPath)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			f.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(doc), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
	code := filepath.Join(f.dir, "code.txt")
	prev, _ := os.ReadFile(code)
	if err := os.WriteFile(code, append(prev, []byte(message+"\n")...), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.git("add", "-A")
	f.git("commit", "-q", "-m", message)
	return f.git("rev-parse", "HEAD")
}

// log is `git log` on main as History reads it.
func (f *fixture) log() []designdoc.Commit {
	f.t.Helper()
	out := f.git("log", "--format=%H%x00%B%x1e", "main")
	var commits []designdoc.Commit
	for _, rec := range strings.Split(out, "\x1e") {
		sha, msg, ok := strings.Cut(strings.TrimSpace(rec), "\x00")
		if ok {
			commits = append(commits, designdoc.Commit{SHA: sha, Message: msg})
		}
	}
	return commits
}

func (f *fixture) at(rev string) ([]byte, error) {
	cmd := exec.Command("git", "show", rev+":"+docPath)
	cmd.Dir = f.dir
	return cmd.Output()
}

func fixtureDoc(sections ...string) string {
	return "# Foo\n\nIntro.\n\n## Requirements\n\n" + strings.Join(sections, "\n") + "\n## Non-goals\n\nNone.\n"
}

const (
	d1    = "### Retag latest {#D-1}\n\nRetag latest on promotion.\n"
	d2    = "### Keep the old tag {#D-2}\n\nKeep it a week.\n"
	d2New = "### Keep the old tag {#D-2}\n\nKeep it a month.\n"
	d3    = "### Mirror {#D-3}\n\nMirror to a second registry.\n"
	d4    = "### Sign {#D-4}\n\nSign every image.\n"
	d4New = "### Sign {#D-4}\n\nSign every image with\ncosign.\n"
	d5    = "### Audit {#D-5}\n\nAudit every push.\n"
	d6    = "### Notify {#D-6}\n\nNotify on failure.\n"
	d7    = "### Prune {#D-7}\n\nPrune old images.\n"
)

func trailers(lines ...string) string {
	return "Implement it\n\nWhat changed.\n\n" + strings.Join(lines, "\n")
}

// Every row of the classification table, read from a real repository's
// trailers: nothing in a ledger, nothing written back to the doc.
func TestClassifyFromTrailers(t *testing.T) {
	f := newFixture(t)
	rev1 := f.commit(fixtureDoc(d1, d2, d3, d4, d5, d7), "Add the design doc")

	item := func(id string) string { return "Design-Item: " + docPath + "#" + id }
	// D-1 and D-2 are built against rev1; D-3 is built and later leaves
	// the doc; D-7 is built, leaves the doc, and its removal lands.
	f.commit("", trailers(item("D-1"), item("D-2"), "Design-Rev: "+rev1, "Origin: bwsalmon/grain#10", "Filed-by: task 11"))
	f.commit("", trailers(item("D-3"), "Design-Rev: "+rev1))
	f.commit("", trailers(item("D-7"), "Design-Rev: "+rev1))
	// A trailer for another doc's D-5 does not build this one's.
	f.commit("", trailers("Design-Item: docs/design/bar.md#D-5", "Design-Rev: "+rev1))

	// Today's revision: D-2 and D-4 reworded, D-3 and D-7 and D-5 gone,
	// D-6 added, D-1 reflowed only.
	rev2 := f.commit(fixtureDoc(strings.Replace(d1, "Retag latest on", "Retag latest\non", 1), d2New, d4New, d6), "Revise the doc")
	f.commit("", "Take mirroring out\n\nIt left the doc.\n\nDesign-Removed: "+docPath+"#D-7\nDesign-Rev: "+rev2)

	current, err := f.at("main")
	if err != nil {
		t.Fatal(err)
	}
	open := map[string][]designdoc.OpenTask{
		"D-4": {{ID: "40", Rev: rev1}}, // filed against the old wording
		"D-5": {{ID: "50", Rev: rev1}}, // filed, then the item left the doc
	}
	items, err := designdoc.Classify(current, designdoc.History(docPath, f.log()), open, f.at)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]designdoc.Class{}
	for _, it := range items {
		got[it.ID] = it.Class
	}
	want := map[string]designdoc.Class{
		"D-1": designdoc.ClassDone,
		"D-2": designdoc.ClassChangedDone,
		"D-3": designdoc.ClassRemovedDone,
		"D-4": designdoc.ClassChangedOpen,
		"D-5": designdoc.ClassRemovedOpen,
		"D-6": designdoc.ClassNew,
		"D-7": designdoc.ClassGone,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("classes =\n%v\nwant\n%v", got, want)
	}

	// D-4 filed against the revision it still has is open, not changed.
	items, err = designdoc.Classify(current, nil, map[string][]designdoc.OpenTask{"D-4": {{ID: "41", Rev: rev2}}}, f.at)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.ID == "D-4" && it.Class != designdoc.ClassOpen {
			t.Errorf("D-4 open against today's revision = %q", it.Class)
		}
	}
}

func TestHistoryTakesTheNewestRevision(t *testing.T) {
	commits := []designdoc.Commit{
		{SHA: "c3", Message: "Fix\n\nDesign-Item: a.md#D-1\nDesign-Rev: r2"},
		{SHA: "c2", Message: "Not a trailer paragraph\n\nDesign-Item: a.md#D-1 is mentioned in prose\nwith words."},
		{SHA: "c1", Message: "First\n\nDesign-Item: a.md#D-1\nDesign-Rev: r1"},
	}
	h := designdoc.History("a.md", commits)
	if b := h["D-1"]; b.Rev != "r2" || !reflect.DeepEqual(b.Commits, []string{"c3", "c1"}) {
		t.Errorf("D-1 = %+v", b)
	}
}

func TestSectionIgnoresTheIDAndWhitespace(t *testing.T) {
	a, ok := designdoc.Section([]byte("## R\n### X {#D-1}\n\nSome  text\nwrapped.\n#### Sub\nmore\n### Y {#D-2}\nother\n"), "D-1")
	b, _ := designdoc.Section([]byte("### X\n\nSome text wrapped.\n#### Sub\nmore\n"), "")
	if !ok || !strings.Contains(a, "more") || strings.Contains(a, "other") || strings.Contains(a, "{#") {
		t.Errorf("Section = %q", a)
	}
	_ = b
	if _, ok := designdoc.Section([]byte("### X {#D-1}\n"), "D-9"); ok {
		t.Error("found a section the doc does not have")
	}
}

func TestTrailers(t *testing.T) {
	got := designdoc.Trailers("Subject\n\nBody: with a colon in prose.\n\nDesign-Item: a.md#D-1\nOrigin: task 5\nnot a trailer line")
	want := [][2]string{{"Design-Item", "a.md#D-1"}, {"Origin", "task 5"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Trailers = %v", got)
	}
	if designdoc.Trailers("Only a subject: here") != nil {
		t.Error("a subject line read as a trailer")
	}
}
