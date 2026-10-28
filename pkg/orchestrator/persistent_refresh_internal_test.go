package orchestrator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
)

func fastPersistentRefresh(t *testing.T, debounce time.Duration) {
	poll, quiet := persistentRefreshPoll, persistentRefreshDebounce
	persistentRefreshPoll, persistentRefreshDebounce = 20*time.Millisecond, debounce
	t.Cleanup(func() { persistentRefreshPoll, persistentRefreshDebounce = poll, quiet })
}

// persistentCommit commits files to repo's checked-out branch and returns
// the new head.
func persistentCommit(t *testing.T, repo string, files map[string]string) string {
	t.Helper()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"add", "."},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "next"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return persistentRepoHead(t, repo)
}

func persistentRepoHead(t *testing.T, repo string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func countEvents(st model.PersistentSandboxStatus, kind string) int {
	n := 0
	for _, ev := range st.Events {
		if ev.Kind == kind {
			n++
		}
	}
	return n
}

// fakeRefreshSource is a head and a set of verdicts a test controls.
type fakeRefreshSource struct {
	mu       sync.Mutex
	head     string
	verdicts map[string]string
	reads    int
}

func (f *fakeRefreshSource) set(head string, verdict string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.head = head
	if f.verdicts == nil {
		f.verdicts = make(map[string]string)
	}
	f.verdicts[head] = verdict
}

func (f *fakeRefreshSource) Head(context.Context, string, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return f.head, nil
}

func (f *fakeRefreshSource) Verdict(_ context.Context, _, _, commit string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.verdicts[commit]
}

const persistentServe = "#!/bin/sh\necho up\nexec sleep 600\n"

func TestPersistentRefreshManualNeverRefreshes(t *testing.T) {
	fastPersistentRefresh(t, 0)
	repo := persistentTestRepo(t, map[string]string{"serve.sh": persistentServe})
	first := persistentRepoHead(t, repo)
	s := newTestPersistentSupervisor(repo)
	t.Cleanup(s.Close)
	def := model.PersistentSandbox{Name: "man", Repo: "acme/app", Ref: "main", AutoUpdate: true, Command: "./serve.sh", Enabled: true}
	s.Reconcile(context.Background(), Deps{Sandboxes: NewHostSandboxes(t.TempDir())}, []model.PersistentSandbox{def})
	waitFor(t, "the command to run", func() bool { return persistentStatus(s, def).State == PersistentRunning })

	persistentCommit(t, repo, map[string]string{"later": "1"})
	time.Sleep(300 * time.Millisecond) // fifteen polls, were there a watcher

	st := persistentStatus(s, def)
	if st.Commit != first || st.Restarts != 0 || st.LastRefresh != nil || countEvents(st, "refresh") != 0 {
		t.Errorf("a manual sandbox refreshed itself: commit %s (want %s), restarts %d, last %+v", st.Commit, first, st.Restarts, st.LastRefresh)
	}
	if st.Refresh != model.PersistentRefreshManual {
		t.Errorf("Refresh = %q, want manual for an unset policy", st.Refresh)
	}
}

func TestPersistentRefreshOnPushRestartsOncePerDebouncedBurst(t *testing.T) {
	fastPersistentRefresh(t, 400*time.Millisecond)
	repo := persistentTestRepo(t, map[string]string{"serve.sh": persistentServe})
	s := newTestPersistentSupervisor(repo)
	t.Cleanup(s.Close)
	// No Head: with no forge client the head is read with ls-remote in the
	// sandbox, the fallback for a ref grain does not otherwise watch.
	def := model.PersistentSandbox{Name: "push", Repo: "acme/app", Ref: "main", AutoUpdate: true,
		Refresh: "on_push", Command: "./serve.sh", Enabled: true}
	s.Reconcile(context.Background(), Deps{Sandboxes: NewHostSandboxes(t.TempDir())}, []model.PersistentSandbox{def})
	waitFor(t, "the command to run", func() bool {
		st := persistentStatus(s, def)
		return st.State == PersistentRunning && st.Commit != ""
	})
	first := persistentStatus(s, def).Commit

	// A burst: three pushes, each inside the debounce of the one before.
	var last string
	for i := 0; i < 3; i++ {
		last = persistentCommit(t, repo, map[string]string{"n": strings.Repeat("x", i+1)})
		time.Sleep(100 * time.Millisecond)
	}
	waitFor(t, "the refresh onto the burst's last commit", func() bool {
		st := persistentStatus(s, def)
		return st.State == PersistentRunning && st.Commit == last
	})
	time.Sleep(600 * time.Millisecond) // past another debounce: nothing more should happen
	st := persistentStatus(s, def)
	if n := countEvents(st, "refresh"); n != 1 {
		t.Errorf("%d refresh events for one burst, want 1: %+v", n, st.Events)
	}
	if st.LastRefresh == nil || st.LastRefresh.From != first || st.LastRefresh.To != last ||
		!strings.Contains(st.LastRefresh.Reason, "main moved to") {
		t.Errorf("LastRefresh = %+v, want %s -> %s", st.LastRefresh, first, last)
	}
}

func TestPersistentRefreshIntervalSkipsWhenNothingMoved(t *testing.T) {
	sbs := NewHostSandboxes(t.TempDir())
	sb, err := sbs.Acquire(context.Background(), "pinterval", Shape{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sb.Release(context.Background()) })
	src := &fakeRefreshSource{head: "aaaa"}
	pd := persistentDeps{head: src.Head}
	def := model.PersistentSandbox{Name: "iv", Repo: "acme/app", Ref: "main", AutoUpdate: true, Refresh: "interval:1h"}
	now := time.Now()
	e := &persistentEntry{def: def, ctl: make(chan string, 1), state: PersistentRunning,
		sandbox: sb, commit: "aaaa", startedAt: now.Add(-30 * time.Minute)}
	s := NewPersistentSandboxSupervisor()
	pending := func() string {
		select {
		case req := <-e.ctl:
			return req
		default:
			return ""
		}
	}

	src.set("bbbb", "")
	s.refreshTick(context.Background(), pd, e, model.PersistentRefreshInterval, time.Hour, now)
	if req := pending(); req != "" || src.reads != 0 {
		t.Fatalf("before the interval is due: request %q, %d head reads; want neither", req, src.reads)
	}

	src.set("aaaa", "")
	s.refreshTick(context.Background(), pd, e, model.PersistentRefreshInterval, time.Hour, now.Add(time.Hour))
	if req := pending(); req != "" {
		t.Fatalf("due, head unmoved: requested %q", req)
	}
	if src.reads != 1 {
		t.Fatalf("due: %d head reads, want 1", src.reads)
	}

	// The interval is counted from that check, not from the start.
	src.set("bbbb", "")
	s.refreshTick(context.Background(), pd, e, model.PersistentRefreshInterval, time.Hour, now.Add(90*time.Minute))
	if req := pending(); req != "" {
		t.Fatalf("half an interval after the last check: requested %q", req)
	}
	s.refreshTick(context.Background(), pd, e, model.PersistentRefreshInterval, time.Hour, now.Add(2*time.Hour))
	if req := pending(); req != "refresh" {
		t.Fatalf("due, head moved: requested %q, want refresh", req)
	}
	if e.target != "bbbb" || e.refresh.last == nil || e.refresh.last.From != "aaaa" {
		t.Errorf("target %q, last %+v; want a refresh from aaaa to bbbb", e.target, e.refresh.last)
	}
}

func TestPersistentRefreshAfterGreenWaitsSkipsAndPinsTheGreenCommit(t *testing.T) {
	fastPersistentBackoff(t)
	fastPersistentRefresh(t, 0)
	repo := persistentTestRepo(t, map[string]string{"serve.sh": persistentServe})
	first := persistentRepoHead(t, repo)
	src := &fakeRefreshSource{}
	src.set(first, "")
	s := newTestPersistentSupervisor(repo)
	s.Head, s.Verdict = src.Head, src.Verdict
	t.Cleanup(s.Close)
	def := model.PersistentSandbox{Name: "green", Repo: "acme/app", Ref: "main", AutoUpdate: true,
		Refresh: "after_green", Command: "./serve.sh", Enabled: true}
	s.Reconcile(context.Background(), Deps{Sandboxes: NewHostSandboxes(t.TempDir())}, []model.PersistentSandbox{def})
	waitFor(t, "the command to run", func() bool {
		st := persistentStatus(s, def)
		return st.State == PersistentRunning && st.Commit == first
	})

	for _, verdict := range []string{string(ci.ConclusionFailure), string(ci.ConclusionCancelled)} {
		head := persistentCommit(t, repo, map[string]string{"v": verdict})
		src.set(head, verdict)
		waitFor(t, "the "+verdict+" head to be skipped", func() bool {
			for _, ev := range persistentStatus(s, def).Events {
				if ev.Kind == "refresh-skip" && strings.Contains(ev.Message, shortSHA(head)) && strings.Contains(ev.Message, verdict) {
					return true
				}
			}
			return false
		})
		if st := persistentStatus(s, def); st.Commit != first || st.Restarts != 0 {
			t.Fatalf("a %s head moved the sandbox: commit %s, restarts %d", verdict, st.Commit, st.Restarts)
		}
	}

	// No verdict yet: it waits.
	green := persistentCommit(t, repo, map[string]string{"v": "green"})
	src.set(green, "")
	waitFor(t, "a wait for the verdict", func() bool { return countEvents(persistentStatus(s, def), "refresh-wait") > 0 })
	if st := persistentStatus(s, def); st.Commit != first {
		t.Fatalf("moved before the verdict: %s", st.Commit)
	}

	// The verdict comes in green -- and by now somebody has pushed past
	// it, so the branch's tip is a commit nobody has verified. The
	// refresh must land on the green commit, not on FETCH_HEAD.
	unverified := persistentCommit(t, repo, map[string]string{"v": "unverified"})
	src.mu.Lock()
	src.verdicts[green] = string(ci.ConclusionSuccess)
	src.mu.Unlock()
	waitFor(t, "the refresh onto the green commit", func() bool {
		st := persistentStatus(s, def)
		return st.State == PersistentRunning && st.Commit == green
	})
	st := persistentStatus(s, def)
	if st.LastRefresh == nil || st.LastRefresh.From != first || st.LastRefresh.To != green ||
		!strings.Contains(st.LastRefresh.Reason, "CI green at "+shortSHA(green)) {
		t.Errorf("LastRefresh = %+v", st.LastRefresh)
	}

	// An operator's restart stays on the last green commit too.
	if err := s.Restart("green"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the operator's restart", func() bool {
		st := persistentStatus(s, def)
		return st.State == PersistentRunning && st.Restarts >= 2
	})
	if got := persistentStatus(s, def).Commit; got != green {
		t.Errorf("after a restart the sandbox is on %s, want the green %s rather than the unverified %s", got, green, unverified)
	}
}

func TestPersistentRefreshRollsBackACrashingGreenCommit(t *testing.T) {
	fastPersistentBackoff(t)
	fastPersistentRefresh(t, 0)
	repo := persistentTestRepo(t, map[string]string{"serve.sh": persistentServe})
	first := persistentRepoHead(t, repo)
	src := &fakeRefreshSource{}
	src.set(first, "")
	s := newTestPersistentSupervisor(repo)
	s.Head, s.Verdict = src.Head, src.Verdict
	t.Cleanup(s.Close)
	def := model.PersistentSandbox{Name: "rb", Repo: "acme/app", Ref: "main", AutoUpdate: true,
		Refresh: "after_green", RollbackOnCrash: true, Command: "./serve.sh", Enabled: true}
	s.Reconcile(context.Background(), Deps{Sandboxes: NewHostSandboxes(t.TempDir())}, []model.PersistentSandbox{def})
	waitFor(t, "the command to run", func() bool {
		st := persistentStatus(s, def)
		return st.State == PersistentRunning && st.Commit == first
	})

	// Green in CI, broken in staging.
	broken := persistentCommit(t, repo, map[string]string{"serve.sh": "#!/bin/sh\necho broken\nexit 1\n"})
	src.set(broken, string(ci.ConclusionSuccess))
	waitFor(t, "the rollback", func() bool { return countEvents(persistentStatus(s, def), "rollback") > 0 })
	waitFor(t, "the previous commit to run again", func() bool {
		st := persistentStatus(s, def)
		return st.State == PersistentRunning && st.Commit == first
	})
	st := persistentStatus(s, def)
	if countEvents(st, "refresh-crash") != 1 {
		t.Errorf("want one crash-loop report: %+v", st.Events)
	}
	if !strings.Contains(st.LastError, "exited 3 times in a row") {
		t.Errorf("LastError = %q, want the crash loop on the panel", st.LastError)
	}
	if st.LastRefresh == nil || st.LastRefresh.To != first || !strings.HasPrefix(st.LastRefresh.Reason, "rolled back") {
		t.Errorf("LastRefresh = %+v, want the rollback", st.LastRefresh)
	}

	// The broken commit is still the head and still green; it is not
	// refreshed onto again.
	time.Sleep(300 * time.Millisecond)
	if st := persistentStatus(s, def); st.Commit != first || countEvents(st, "refresh") != 1 {
		t.Errorf("refreshed back onto the broken commit: commit %s, %d refreshes", st.Commit, countEvents(st, "refresh"))
	}
}

func TestPersistentCIVerdictReadsThePushRunOnThatCommit(t *testing.T) {
	done := func(at time.Time) *time.Time { return &at }
	now := time.Now()
	runs := []ci.Run{
		{Repo: "acme/app", Ref: "candidate", Commit: "c1", Event: ci.EventPush, Conclusion: ci.ConclusionFailure, FinishedAt: done(now.Add(-time.Hour))},
		{Repo: "acme/app", Ref: "candidate", Commit: "c1", Event: ci.EventPush, Conclusion: ci.ConclusionSuccess, FinishedAt: done(now)},
		{Repo: "acme/app", Ref: "candidate", Commit: "c2", Event: ci.EventPullRequest, Conclusion: ci.ConclusionSuccess, FinishedAt: done(now)},
		{Repo: "acme/app", Ref: "candidate", Commit: "c3", Event: ci.EventPush, Conclusion: ci.ConclusionErrored, Interrupted: true, FinishedAt: done(now)},
		{Repo: "acme/app", Ref: "main", Commit: "c4", Event: ci.EventPush, Conclusion: ci.ConclusionSuccess, FinishedAt: done(now)},
	}
	for commit, want := range map[string]string{"c1": "success", "c2": "", "c3": "", "c4": "", "c5": ""} {
		if got := latestPushVerdict(runs, "Acme/App", "candidate", commit); got != want {
			t.Errorf("verdict on %s = %q, want %q", commit, got, want)
		}
	}
	if got := lsRemoteHead("aaa\trefs/heads/main\nbbb\trefs/tags/v1\nccc\trefs/tags/v1^{}\nddd\tHEAD\n", "v1"); got != "ccc" {
		t.Errorf("tag head = %q, want the peeled commit", got)
	}
	if got := lsRemoteHead("aaa\trefs/heads/main\nddd\tHEAD\n", "HEAD"); got != "ddd" {
		t.Errorf("HEAD = %q", got)
	}
}
