package orchestrator

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The overlap rule replayed over a branch's own history: what it would
// have said about each of the last N merges, and the trigger rate -- the
// number an operator wants before switching a repository to enforce
// (docs/merge-queue-behind.md "The overlap re-check").
//
// Run it against any full clone:
//
//	GRAIN_RECHECK_REPLAY=/path/to/clone GRAIN_RECHECK_REPLAY_REF=origin/main \
//	GRAIN_RECHECK_REPLAY_N=300 go test ./pkg/orchestrator -run TestReplayOverlapRule -v
//
// It reads git alone, no GitHub and no store: each first-parent merge
// commit M on the ref is one merge, M^1 the tip it landed on, M^2 the
// head that merged, and their merge base the fork. A squash or rebase
// merge has no second parent and is not counted. Unset, the test replays
// a small history it builds itself, so the replay is exercised on every
// run of the suite.

// replayedMerge is one merge and the rule's verdict on it.
type replayedMerge struct {
	merge, subject string
	verdict        overlapVerdict
}

// replayOverlapRule runs the rule over the last n first-parent merges on
// ref in the repository at dir, newest first.
func replayOverlapRule(dir, ref string, n int) ([]replayedMerge, error) {
	git := func(args ...string) (string, error) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	names := func(from, to string) ([]string, error) {
		out, err := git("diff", "--name-only", "--no-renames", from, to)
		if err != nil || out == "" {
			return nil, err
		}
		return strings.Split(out, "\n"), nil
	}
	list, err := git("rev-list", "--first-parent", "--merges", "-n", strconv.Itoa(n), ref)
	if err != nil {
		return nil, err
	}
	var out []replayedMerge
	for _, merge := range strings.Fields(list) {
		parents, err := git("rev-list", "--parents", "-n", "1", merge)
		if err != nil {
			return nil, err
		}
		p := strings.Fields(parents)
		if len(p) < 3 {
			continue
		}
		tip, head := p[1], p[2]
		fork, err := git("merge-base", tip, head)
		if err != nil {
			return nil, err
		}
		subject, _ := git("log", "-1", "--format=%s", merge)
		v := overlapVerdict{fork: fork, tip: tip, head: head}
		if fork != tip {
			changed, err := names(fork, head)
			if err != nil {
				return nil, err
			}
			landed, err := names(fork, tip)
			if err != nil {
				return nil, err
			}
			v = overlapRule(changed, landed, true)
			v.fork, v.tip, v.head = fork, tip, head
		}
		out = append(out, replayedMerge{merge: merge, subject: subject, verdict: v})
	}
	return out, nil
}

// replaySummary is the replay as a person reads it: the trigger rate,
// each clause's count, and the packages clause 2 fired in most.
func replaySummary(merges []replayedMerge) string {
	var b strings.Builder
	triggered, current := 0, 0
	clauses := map[string]int{}
	packages := map[string]int{}
	for _, m := range merges {
		v := m.verdict
		if v.fork == v.tip {
			current++
		}
		if !v.trigger {
			continue
		}
		triggered++
		for _, c := range v.clauses {
			clauses[c]++
		}
		for _, part := range strings.Split(v.detail, "; ") {
			if pkg, ok := strings.CutPrefix(part, "same-package "); ok {
				packages[pkg]++
			}
		}
	}
	rate := 0.0
	if len(merges) > 0 {
		rate = float64(triggered) / float64(len(merges))
	}
	fmt.Fprintf(&b, "%d merges replayed: %d would be re-checked (%.1f%%), %d skipped, %d of them with "+
		"nothing landed since their fork\n", len(merges), triggered, 100*rate, len(merges)-triggered, current)
	for _, c := range recheckClauseOrder {
		if clauses[c] > 0 {
			fmt.Fprintf(&b, "  %-13s %d\n", c, clauses[c])
		}
	}
	type count struct {
		name string
		n    int
	}
	var top []count
	for name, n := range packages {
		top = append(top, count{name, n})
	}
	sort.Slice(top, func(i, j int) bool {
		if top[i].n != top[j].n {
			return top[i].n > top[j].n
		}
		return top[i].name < top[j].name
	})
	if len(top) > 0 {
		b.WriteString("  same-package, by package:\n")
	}
	for i, c := range top {
		if i == 10 {
			break
		}
		fmt.Fprintf(&b, "    %-40s %d\n", c.name, c.n)
	}
	return b.String()
}

func TestReplayOverlapRule(t *testing.T) {
	dir, ref, n := os.Getenv("GRAIN_RECHECK_REPLAY"), os.Getenv("GRAIN_RECHECK_REPLAY_REF"), 300
	if v := os.Getenv("GRAIN_RECHECK_REPLAY_N"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("GRAIN_RECHECK_REPLAY_N=%q: %v", v, err)
		}
		n = parsed
	}
	if dir != "" {
		if ref == "" {
			ref = "HEAD"
		}
		merges, err := replayOverlapRule(dir, ref, n)
		if err != nil {
			t.Fatal(err)
		}
		if testing.Verbose() {
			for _, m := range merges {
				state := "skip"
				if m.verdict.trigger {
					state = "RECHECK " + m.verdict.detail
				}
				t.Logf("%s %-60.60s %s", shortSHA(m.merge), m.subject, state)
			}
		}
		t.Logf("\n%s", replaySummary(merges))
		return
	}

	// No clone named: replay a history built here. main gets one merge
	// that touched nothing the other side did, one in the same package as
	// a change that landed after it forked, and one that forked from the
	// tip it merged into.
	dir = t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(path, content string) {
		t.Helper()
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", path)
	}
	run("init", "-q", "-b", "main")
	write("pkg/ui/a.go", "package ui\n")
	write("docs/x.md", "x\n")
	run("commit", "-q", "-m", "root")

	// Branch one forks, main moves in docs, branch one (pkg/model) merges.
	run("checkout", "-q", "-b", "one")
	write("pkg/model/m.go", "package model\n")
	run("commit", "-q", "-m", "one")
	run("checkout", "-q", "main")
	write("docs/y.md", "y\n")
	run("commit", "-q", "-m", "docs on main")
	run("merge", "-q", "--no-ff", "-m", "merge one", "one")

	// Branch two forks, main moves in pkg/ui, branch two (pkg/ui) merges.
	run("checkout", "-q", "-b", "two")
	write("pkg/ui/b.go", "package ui\n")
	run("commit", "-q", "-m", "two")
	run("checkout", "-q", "main")
	write("pkg/ui/c.go", "package ui\n")
	run("commit", "-q", "-m", "ui on main")
	run("merge", "-q", "--no-ff", "-m", "merge two", "two")

	// Branch three forks from the tip and merges with nothing in between.
	run("checkout", "-q", "-b", "three")
	write("go.mod", "module x\n")
	run("commit", "-q", "-m", "three")
	run("checkout", "-q", "main")
	run("merge", "-q", "--no-ff", "-m", "merge three", "three")

	merges, err := replayOverlapRule(dir, "main", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(merges) != 3 {
		t.Fatalf("replayed %d merges, want 3", len(merges))
	}
	got := map[string]overlapVerdict{}
	for _, m := range merges {
		got[m.subject] = m.verdict
	}
	if v := got["merge one"]; v.trigger || v.fork == v.tip {
		t.Errorf("merge one = %+v, want skipped with main ahead of its fork", v)
	}
	if v := got["merge two"]; !v.trigger || len(v.clauses) != 1 || v.clauses[0] != "same-package" {
		t.Errorf("merge two = %+v, want a same-package trigger", v)
	}
	if v := got["merge three"]; v.trigger || v.fork != v.tip {
		t.Errorf("merge three = %+v, want fork == tip and no trigger", v)
	}
	summary := replaySummary(merges)
	if !strings.Contains(summary, "3 merges replayed: 1 would be re-checked (33.3%)") ||
		!strings.Contains(summary, "pkg/ui") {
		t.Errorf("summary:\n%s", summary)
	}
}
