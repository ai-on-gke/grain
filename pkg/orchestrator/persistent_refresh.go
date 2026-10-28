package orchestrator

// A persistent sandbox's refresh policy (model.PersistentSandbox.Refresh):
// restarting the command by itself, so that a staging environment follows
// its branch rather than drifting behind it until somebody presses
// Restart.
//
// Each sandbox whose policy is not manual gets a second goroutine beside
// its supervising one, watchRefresh. Once every persistentRefreshPoll it
// reads Ref's head -- from the forge, the same branch read the
// base-branch CI judge uses for "the branch has moved" (branchTip), or,
// for a ref the forge cannot answer for, a `git ls-remote` of the
// sandbox's own remote -- and decides whether the commit the command is
// running should move:
//
//   - on_push moves to the head once it has stayed put for
//     persistentRefreshDebounce, so a burst of pushes is one restart;
//   - interval:<d> reads the head only every d, and moves only if it
//     moved;
//   - after_green moves only to a head grain CI has a passing verdict on
//     -- the push run on that branch the base-branch judge records
//     (persistentCIVerdict) -- and stays where it is on a red or
//     cancelled one.
//
// A move is a restart like an operator's, with one difference: the
// commit is named (persistentEntry.target), and the checkout checks out
// that sha rather than whatever FETCH_HEAD is by the time it runs. Under
// after_green the name is kept, so a crash or an operator's Restart
// stays on the last green commit too instead of fetching past it.
//
// A refresh is never asked for while the sandbox is anything but
// running: while a build or a previous refresh's checkout is still in
// progress, the tick does nothing and the next one looks again.
//
// A refreshed commit whose command then exits PersistentRollbackCrashes
// times in a row without staying up is reported, on the panel and in the
// daemon log, and with RollbackOnCrash the sandbox goes back to the
// commit it ran before and never refreshes onto the broken one again.
// Rollback is offered only under after_green: that is the one policy
// whose previous commit is known to have been good, where under on_push
// it was merely older.

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/ci"
	"github.com/bwsalmon/grain/pkg/model"
)

// persistentRefreshPoll is how often a refreshing sandbox reads its ref's
// head -- at most once a minute, the bound on the ls-remote fallback --
// and persistentRefreshDebounce how long on_push waits for the head to
// stay put before it restarts.
var (
	persistentRefreshPoll     = time.Minute
	persistentRefreshDebounce = 2 * time.Minute
)

// persistentRefreshState is the refresh policy's part of an entry,
// guarded by the entry's mu.
type persistentRefreshState struct {
	// pending and pendingSince are the head on_push last saw and when it
	// first saw it, for the debounce.
	pending      string
	pendingSince time.Time
	// lastCheck is when interval last read the head.
	lastCheck time.Time
	// noted is what has already been said about a head that was not
	// moved to, so a red head is one event rather than one a minute.
	noted map[string]string
	// last is the most recent refresh, and crashes how many rapid exits
	// in a row its commit has had since.
	last    *model.PersistentSandboxRefresh
	crashes int
	// bad are commits rolled back from, never refreshed onto again.
	bad map[string]bool
	// lastErr is the last head-reading error reported.
	lastErr string
}

// persistentRefreshPolicy is def's refresh policy and interval; an
// unparseable one is manual (validation keeps it from being stored).
func persistentRefreshPolicy(def model.PersistentSandbox) (string, time.Duration) {
	policy, every, err := model.ParsePersistentRefresh(def.Refresh)
	if err != nil {
		return model.PersistentRefreshManual, 0
	}
	return policy, every
}

// watchRefresh is the refresh policy's goroutine, for as long as ctx.
func (s *PersistentSandboxSupervisor) watchRefresh(ctx context.Context, pd persistentDeps, e *persistentEntry) {
	policy, every := persistentRefreshPolicy(e.def)
	if policy == model.PersistentRefreshManual {
		return
	}
	t := time.NewTicker(persistentRefreshPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.refreshTick(ctx, pd, e, policy, every, time.Now())
	}
}

// refreshTick is one look at the head, and the restart it earns if any.
func (s *PersistentSandboxSupervisor) refreshTick(ctx context.Context, pd persistentDeps, e *persistentEntry,
	policy string, every time.Duration, now time.Time) {
	e.mu.Lock()
	state, running, started, sb := e.state, e.commit, e.startedAt, e.sandbox
	if state == PersistentRunning && policy == model.PersistentRefreshInterval {
		since := started
		if e.refresh.lastCheck.After(since) {
			since = e.refresh.lastCheck
		}
		if now.Sub(since) < every {
			state = "" // not due
		} else {
			e.refresh.lastCheck = now
		}
	}
	e.mu.Unlock()
	if state != PersistentRunning || running == "" || sb == nil {
		return
	}

	head, err := persistentHead(ctx, pd, e.def, sb)
	if err != nil {
		e.mu.Lock()
		repeat := e.refresh.lastErr == err.Error()
		e.refresh.lastErr = err.Error()
		e.mu.Unlock()
		if !repeat {
			e.event("refresh-error", "reading %s's head: %v", persistentRefName(e.def), err)
			log.Printf("orchestrator: persistent sandbox %q: reading %s's head: %v", e.def.Name, persistentRefName(e.def), err)
		}
		return
	}
	e.set(func(e *persistentEntry) { e.refresh.lastErr = "" })
	if head == "" || head == running {
		e.set(func(e *persistentEntry) { e.refresh.pending = "" })
		return
	}
	ref := persistentRefName(e.def)
	switch policy {
	case model.PersistentRefreshOnPush:
		e.mu.Lock()
		if e.refresh.pending != head {
			e.refresh.pending, e.refresh.pendingSince = head, now
			e.mu.Unlock()
			return
		}
		quiet := now.Sub(e.refresh.pendingSince)
		e.mu.Unlock()
		if quiet < persistentRefreshDebounce {
			return
		}
		e.refreshTo(running, head, fmt.Sprintf("%s moved to %s", ref, shortSHA(head)))
	case model.PersistentRefreshInterval:
		e.refreshTo(running, head, fmt.Sprintf("%s moved to %s (checked every %s)", ref, shortSHA(head), every))
	case model.PersistentRefreshAfterGreen:
		e.mu.Lock()
		bad := e.refresh.bad[head]
		e.mu.Unlock()
		if bad {
			return
		}
		verdict := ""
		if pd.verdict != nil {
			verdict = pd.verdict(ctx, e.def.Repo, e.def.Ref, head)
		}
		switch verdict {
		case string(ci.ConclusionSuccess):
			e.refreshTo(running, head, fmt.Sprintf("%s moved; CI green at %s", ref, shortSHA(head)))
		case "":
			e.noteOnce(head, "pending", "refresh-wait",
				"%s moved to %s; waiting for grain CI's verdict on it before moving", ref, shortSHA(head))
		default:
			e.noteOnce(head, verdict, "refresh-skip",
				"%s moved to %s, but grain CI's verdict on it is %s; staying on %s", ref, shortSHA(head), verdict, shortSHA(running))
		}
	}
}

// noteOnce records an event about head unless the same thing has
// already been said about it.
func (e *persistentEntry) noteOnce(head, what, kind, format string, args ...any) {
	e.mu.Lock()
	if e.refresh.noted == nil {
		e.refresh.noted = make(map[string]string)
	}
	said := e.refresh.noted[head] == what
	if !said {
		// Only the latest head's note is worth remembering.
		e.refresh.noted = map[string]string{head: what}
	}
	e.mu.Unlock()
	if !said {
		e.event(kind, format, args...)
	}
}

// refreshTo restarts the command onto commit to, recording why.
func (e *persistentEntry) refreshTo(from, to, reason string) {
	e.mu.Lock()
	e.target = to
	e.refresh.last = &model.PersistentSandboxRefresh{At: time.Now(), From: from, To: to, Reason: reason}
	e.refresh.crashes = 0
	e.refresh.pending = ""
	e.refresh.noted = nil
	e.state = PersistentRestarting
	e.mu.Unlock()
	e.event("refresh", "refreshing from %s to %s: %s", shortSHA(from), shortSHA(to), reason)
	log.Printf("orchestrator: persistent sandbox %q: refreshing from %s to %s: %s", e.def.Name, shortSHA(from), shortSHA(to), reason)
	e.request("refresh")
}

// noteCrash counts an exit of the command after up against the last
// refresh, reports a refreshed commit that will not stay up, and rolls
// it back where the definition asks for that.
func (e *persistentEntry) noteCrash(up time.Duration) {
	e.mu.Lock()
	last := e.refresh.last
	if last == nil || e.commit != last.To || up >= persistentStableAfter {
		e.refresh.crashes = 0
		e.mu.Unlock()
		return
	}
	e.refresh.crashes++
	if e.refresh.crashes != model.PersistentRollbackCrashes {
		e.mu.Unlock()
		return
	}
	broken, previous := last.To, last.From
	rollback := e.def.RollbackOnCrash && previous != "" && previous != broken
	msg := fmt.Sprintf("%s has exited %d times in a row since the refresh from %s", shortSHA(broken), e.refresh.crashes, shortSHA(previous))
	e.lastError = strings.TrimPrefix(e.lastError+"; "+msg, "; ")
	if rollback {
		if e.refresh.bad == nil {
			e.refresh.bad = make(map[string]bool)
		}
		e.refresh.bad[broken] = true
		e.target = previous
		e.refresh.crashes = 0
		e.refresh.last = &model.PersistentSandboxRefresh{
			At: time.Now(), From: broken, To: previous,
			Reason: fmt.Sprintf("rolled back: %s", msg),
		}
	}
	e.mu.Unlock()
	e.event("refresh-crash", "%s", msg)
	log.Printf("orchestrator: persistent sandbox %q: %s", e.def.Name, msg)
	if rollback {
		e.event("rollback", "rolling back from %s to %s, and not refreshing onto %s again", shortSHA(broken), shortSHA(previous), shortSHA(broken))
		log.Printf("orchestrator: persistent sandbox %q: rolling back from %s to %s", e.def.Name, shortSHA(broken), shortSHA(previous))
	}
}

// persistentRefName is def's ref as an event names it.
func persistentRefName(def model.PersistentSandbox) string {
	if def.Ref == "" {
		return "the default branch"
	}
	return def.Ref
}

var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// persistentHead is def's ref's head: the forge's answer where it has one,
// else the sandbox's own remote's.
func persistentHead(ctx context.Context, pd persistentDeps, def model.PersistentSandbox, sb Sandbox) (string, error) {
	if fullSHA.MatchString(def.Ref) {
		return def.Ref, nil
	}
	if pd.head != nil && def.Ref != "" {
		if head, err := pd.head(ctx, def.Repo, def.Ref); err == nil && head != "" {
			return head, nil
		}
	}
	shell, ok := sb.(SandboxShell)
	if !ok {
		return "", nil
	}
	return persistentLsRemote(ctx, shell, def.Ref)
}

// persistentLsRemote asks the checkout's remote what ref points at.
func persistentLsRemote(ctx context.Context, shell SandboxShell, ref string) (string, error) {
	if ref == "" {
		ref = "HEAD"
	}
	res, err := shell.RunShell(ctx, ShellRequest{
		Script:  "git ls-remote origin " + shellQuote(ref),
		Dir:     persistentCheckout,
		Timeout: time.Minute,
	})
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("git ls-remote: exit %d: %s", res.ExitCode, strings.TrimSpace(res.Output))
	}
	return lsRemoteHead(res.Output, ref), nil
}

// lsRemoteHead picks ref's commit out of ls-remote output: the branch,
// else a tag's commit, else whatever is named ref exactly.
func lsRemoteHead(out, ref string) string {
	found := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		sha, name, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if ok {
			found[name] = sha
		}
	}
	for _, name := range []string{"refs/heads/" + ref, "refs/tags/" + ref + "^{}", "refs/tags/" + ref, ref} {
		if sha := found[name]; sha != "" {
			return sha
		}
	}
	return ""
}

// persistentForgeHead reads a branch's head the way the base-branch CI
// judge does (branchTip); nil without a forge client.
func persistentForgeHead(deps Deps) func(ctx context.Context, repo, ref string) (string, error) {
	if deps.Client == nil {
		return nil
	}
	return func(ctx context.Context, repo, ref string) (string, error) {
		r, err := model.ParseRepo(repo)
		if err != nil {
			return "", err
		}
		return branchTip(ctx, deps, r, ref)
	}
}

// persistentCIVerdict is grain CI's conclusion on one commit of a branch:
// the base-branch judge's own row when it is about that commit, else the
// latest finished push run of the repository's workflows on that branch
// at that commit. "" is no verdict yet -- including a run that was
// interrupted, which said nothing about the tree.
func persistentCIVerdict(deps Deps) func(ctx context.Context, repo, branch, commit string) string {
	return func(ctx context.Context, repo, branch, commit string) string {
		r, err := model.ParseRepo(repo)
		if err != nil || branch == "" || commit == "" {
			return ""
		}
		if deps.Store != nil {
			row, err := deps.Store.GetBaseBranchCI(ctx, r, branch)
			if err == nil && row != nil && row.Commit == commit && row.Conclusion != "" {
				return row.Conclusion
			}
		}
		if deps.CI == nil {
			return ""
		}
		return latestPushVerdict(deps.CI.List(), repo, branch, commit)
	}
}

// latestPushVerdict is the conclusion of the most recently finished push
// run in runs about commit of repo's branch, "" for none.
func latestPushVerdict(runs []ci.Run, repo, branch, commit string) string {
	var best *ci.Run
	for i := range runs {
		run := &runs[i]
		if !strings.EqualFold(run.Repo, repo) || run.Commit != commit || run.Event != ci.EventPush {
			continue
		}
		if run.Ref != branch && run.Ref != "refs/heads/"+branch {
			continue
		}
		if run.Conclusion == "" || run.Interrupted || run.FinishedAt == nil {
			continue
		}
		if best == nil || run.FinishedAt.After(*best.FinishedAt) {
			best = run
		}
	}
	if best == nil {
		return ""
	}
	return string(best.Conclusion)
}

// pinGreenHead, for an after_green sandbox being built with no commit to
// stay on yet -- its first start since the daemon did -- starts it on its
// ref's head only by name, and only if grain CI is green on it. Otherwise
// the first build takes the ref's tip as a manual one would, and the next
// green head is the first refresh.
func (s *PersistentSandboxSupervisor) pinGreenHead(ctx context.Context, pd persistentDeps, e *persistentEntry) {
	if policy, _ := persistentRefreshPolicy(e.def); policy != model.PersistentRefreshAfterGreen {
		return
	}
	e.mu.Lock()
	pinned := e.target != ""
	e.mu.Unlock()
	if pinned || pd.head == nil || pd.verdict == nil {
		return
	}
	head, err := pd.head(ctx, e.def.Repo, e.def.Ref)
	if err != nil || head == "" {
		return
	}
	if v := pd.verdict(ctx, e.def.Repo, e.def.Ref, head); v != string(ci.ConclusionSuccess) {
		e.event("refresh-wait", "starting on %s's tip: grain CI has no passing verdict on %s yet", persistentRefName(e.def), shortSHA(head))
		return
	}
	e.set(func(e *persistentEntry) { e.target = head })
}
