package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// declarationTree writes one well-formed tree: two templates, a suite
// over both and two schedules, one on each kind of cadence. Every test
// below starts from it and breaks whichever part it is about.
func declarationTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(path, text string) {
		t.Helper()
		full := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	write(".grain/templates/nightly.yml", `name: Nightly dependency bump
title: Bump dependencies
body: |
  Update every dependency and run the tests.
base: main
auto-merge: true
custom-fields:
  team: infra
`)
	write(".grain/templates/docs.yml", "title: Tidy the docs\n")
	write(".grain/suites/sweep.yml", `name: Nightly sweep
templates: [nightly, docs]
count: 2
`)
	write(".grain/schedules/nightly.yml", `name: Every night
suite: sweep
every:
  hours: 6
`)
	write(".grain/schedules/docs.yml", `template: docs
daily:
  at: "09:00"
`)
	return dir
}

func checkDeclarations(t *testing.T, out *printer, args ...string) (string, error) {
	t.Helper()
	var err error
	text := captureStdout(t, func() {
		err = cmdDeclarations(context.Background(), nil, out, append([]string{"check"}, args...))
	})
	return text, err
}

// A tree that parses is printed in full: the id each file gets, what a
// suite runs, and the cadence -- the three things somebody writing one
// cannot work out by reading the file, since two of them are derived
// from the repository rather than written down.
func TestDeclarationsCheckPrintsWhatATreeDeclares(t *testing.T) {
	out, err := checkDeclarations(t, &printer{}, "-repo", "acme/widgets", declarationTree(t))
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	for _, want := range []string{
		".grain/templates/nightly.yml",
		"repo:template:acme:widgets:nightly",
		"repo:suite:acme:widgets:sweep",
		"repo:schedule:acme:widgets:docs",
		"Nightly dependency bump",
		"runs:       nightly, docs",
		"fires:      suite sweep",
		"every 6h",
		"daily at 09:00",
		"team=infra",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not mention %q:\n%s", want, out)
		}
	}
	// The switch, which is what actually decides whether any of this
	// runs, and which nothing in the tree says.
	if !strings.Contains(out, "turns declarations on") {
		t.Errorf("output does not say the repository has to be switched on:\n%s", out)
	}
}

// Every verdict at once, and a non-zero exit. Both halves are the
// command's whole reason for existing: one push per parse error is the
// loop it replaces, and a CI step that printed the errors and exited 0
// would not fail the pull request that introduced them.
func TestDeclarationsCheckReportsEveryProblemAtOnce(t *testing.T) {
	dir := declarationTree(t)
	if err := os.WriteFile(filepath.Join(dir, ".grain/templates/docs.yml"),
		[]byte("title: Tidy the docs\ngrants: [github-sandbox]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".grain/schedules/release.yml"),
		[]byte("template: nosuch\nevery:\n  hours: 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := checkDeclarations(t, &printer{}, "-repo", "acme/widgets", dir)
	if err == nil {
		t.Fatal("a tree with two bad files was accepted")
	}
	// Five problems from two bad files, and every one of them printed:
	// the two files themselves, plus the suite that runs the refused
	// template, the schedule that runs that suite, and the schedule that
	// fires the refused template. A refused declaration is not declared,
	// so everything naming it is refused too.
	if !strings.Contains(err.Error(), "5 problems") {
		t.Errorf("verdict = %q, want it to count every problem printed", err)
	}
	if !strings.Contains(out, ".grain/templates/docs.yml") || !strings.Contains(out, ".grain/schedules/release.yml") {
		t.Errorf("output does not name both bad files:\n%s", out)
	}
	// Nothing of the tree is declared while any of it is refused
	// (repodecl.Load's own rule), so the listing must not also be
	// printed -- an operator reading "template nightly ..." under two
	// errors would take the good half as live.
	if strings.Contains(out, "repo:template:acme:widgets:nightly") {
		t.Errorf("a refused tree still printed its declarations:\n%s", out)
	}
}

// A path that is not there is an error, not "declares nothing".
// repodecl.Load reads a missing `.grain/templates` as a repository that
// declares nothing -- right for a repository, and exactly wrong for a
// typo'd path, which would otherwise be a green CI step that checked
// nothing at all.
func TestDeclarationsCheckRefusesAPathThatIsNotADirectory(t *testing.T) {
	dir := declarationTree(t)
	if _, err := checkDeclarations(t, &printer{}, "-repo", "acme/widgets", filepath.Join(dir, "nope")); err == nil {
		t.Error("a directory that does not exist was read as declaring nothing")
	}
	file := filepath.Join(dir, ".grain/templates/nightly.yml")
	_, err := checkDeclarations(t, &printer{}, "-repo", "acme/widgets", file)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("checking a file = %v, want it to ask for a checkout's root", err)
	}
}

// With no -repo the checkout's own origin decides, which is what a CI
// job in a cloned repository has. The ids prove it got there: they are
// derived from the repository (model.RepoDeclaredID) and from nothing in
// the tree.
//
// Not parallel, and it pins the default git host: whether "github.com"
// is dropped from what it prints depends on the process-wide default
// (pinDefaultGitHost), which the in-process daemon tests here leave set
// to their fake GitHub.
func TestDeclarationsCheckTakesTheRepoFromOrigin(t *testing.T) {
	pinDefaultGitHost(t)
	dir := declarationTree(t)
	for _, args := range [][]string{
		{"init", "--quiet", "-b", "main", "."},
		{"remote", "add", "origin", "git@github.com:acme/widgets.git"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", args[0], err, out)
		}
	}
	out, err := checkDeclarations(t, &printer{}, dir)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !strings.Contains(out, "read as acme/widgets (from origin)") {
		t.Errorf("output does not say where the repository came from:\n%s", out)
	}
	if !strings.Contains(out, "repo:template:acme:widgets:nightly") {
		t.Errorf("ids were not derived from origin:\n%s", out)
	}
}

// A checkout with no origin and no -repo is asked for one rather than
// guessed at: an id derived from the wrong repository is a check that
// passed for a repository nobody is going to push to.
func TestDeclarationsCheckAsksForARepoWhenThereIsNoOrigin(t *testing.T) {
	_, err := checkDeclarations(t, &printer{}, declarationTree(t))
	if err == nil || !strings.Contains(err.Error(), "-repo") {
		t.Errorf("check with no origin = %v, want it to ask for -repo", err)
	}
}

// -json is the shape a script reads, verdict included.
func TestDeclarationsCheckJSONCarriesTheVerdict(t *testing.T) {
	dir := declarationTree(t)
	text, err := checkDeclarations(t, &printer{json: true}, "-repo", "acme/widgets", "-next", "1", dir)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	var view declarationsView
	if err := json.Unmarshal([]byte(text), &view); err != nil {
		t.Fatalf("unmarshal %q: %v", text, err)
	}
	if !view.OK || len(view.Errors) > 0 {
		t.Errorf("view = %+v, want a clean verdict", view)
	}
	if len(view.Templates) != 2 || len(view.Suites) != 1 || len(view.Schedules) != 2 {
		t.Errorf("view counts = %d/%d/%d, want 2/1/2", len(view.Templates), len(view.Suites), len(view.Schedules))
	}
	if len(view.Schedules[0].NextFiring) != 1 {
		t.Errorf("-next 1 produced %d firings", len(view.Schedules[0].NextFiring))
	}

	if err := os.WriteFile(filepath.Join(dir, ".grain/suites/sweep.yml"), []byte("templates: [nosuch]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	text, err = checkDeclarations(t, &printer{json: true}, "-repo", "acme/widgets", dir)
	if err == nil {
		t.Fatal("a suite naming no template was accepted")
	}
	view = declarationsView{}
	if err := json.Unmarshal([]byte(text), &view); err != nil {
		t.Fatalf("unmarshal %q: %v", text, err)
	}
	// Two: the suite's own, and the schedule that runs a suite the first
	// error took out with it.
	if view.OK || len(view.Errors) != 2 {
		t.Errorf("view = %+v, want ok false and both errors", view)
	}
}

// The firing preview is the answer to "when would this actually run",
// so it has to agree with the reconciler that fires it: from now, on the
// zone asked for, through model.Recurrence.Next.
func TestDeclaredScheduleFiringsFollowTheCadence(t *testing.T) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skipf("no zoneinfo on this machine: %v", err)
	}
	now := time.Date(2026, 9, 18, 21, 0, 0, 0, time.UTC)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	declared := model.RepoDeclarations{Schedules: []model.DeclaredScheduleFile{
		{Path: ".grain/schedules/daily.yml", Schedule: model.Schedule{
			ID:         model.RepoDeclaredID(model.DeclaredSchedule, repo, "daily"),
			Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 9 * 60},
			Enabled:    true,
		}},
		{Path: ".grain/schedules/often.yml", Schedule: model.Schedule{
			ID:         model.RepoDeclaredID(model.DeclaredSchedule, repo, "often"),
			Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 6},
			Enabled:    true,
		}},
	}}
	var view declarationsView
	view.fill(declared, now, loc, 2)

	daily := view.Schedules[0]
	if daily.Cadence != "daily at 09:00" {
		t.Errorf("cadence = %q", daily.Cadence)
	}
	// 21:00 UTC is 14:00 in Los Angeles, so the next 09:00 there is
	// tomorrow morning -- the whole reason -zone exists.
	for i, want := range []time.Time{
		time.Date(2026, 9, 19, 9, 0, 0, 0, loc),
		time.Date(2026, 9, 20, 9, 0, 0, 0, loc),
	} {
		if !daily.NextFiring[i].Equal(want) {
			t.Errorf("firing %d = %s, want %s", i, daily.NextFiring[i], want)
		}
	}

	often := view.Schedules[1]
	if often.Cadence != "every 6h" {
		t.Errorf("cadence = %q", often.Cadence)
	}
	// Counted from now rather than from a wall clock: a declared
	// schedule's first firing is Recurrence.Next(now) at the moment
	// grain first reads it (SyncRepoDeclarations).
	if !often.NextFiring[0].Equal(now.Add(6*time.Hour)) || !often.NextFiring[1].Equal(now.Add(12*time.Hour)) {
		t.Errorf("firings = %v, want six and twelve hours from now", often.NextFiring)
	}
}

// An unknown zone is refused by the flag rather than silently read as
// UTC, which would be a preview of somebody else's mornings.
func TestDeclarationsCheckRefusesAnUnknownZone(t *testing.T) {
	_, err := checkDeclarations(t, &printer{}, "-repo", "acme/widgets", "-zone", "Mars/Olympus", declarationTree(t))
	if err == nil || !strings.Contains(err.Error(), "-zone") {
		t.Errorf("unknown zone = %v, want a flag error", err)
	}
}

// The directory is optional and so is the verb, since a repository's own
// CI step is `grain declarations check` in the checkout's root and a
// person in a clone types the shortest thing that works.
func TestDeclarationsCheckDefaultsToTheWorkingDirectory(t *testing.T) {
	dir := declarationTree(t)
	t.Chdir(dir)
	for _, args := range [][]string{{"check", "-repo", "acme/widgets"}, {"-repo", "acme/widgets"}} {
		var err error
		out := captureStdout(t, func() { err = cmdDeclarations(context.Background(), nil, &printer{}, args) })
		if err != nil {
			t.Fatalf("grain declarations %v: %v", args, err)
		}
		if !strings.Contains(out, "repo:suite:acme:widgets:sweep") {
			t.Errorf("grain declarations %v read nothing:\n%s", args, out)
		}
	}
}
