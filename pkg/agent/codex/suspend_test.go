package codex

// The cross-dispatch half of retry.go: a run given the task's own
// CODEX_HOME (sessionstore) picks up the thread the *previous dispatch*
// of that task left in it, and hands back the thread it ends in so the
// next one can do the same (agent.RunConfig.ResumeSession and
// agent.Result.ResumableSession).
//
// retry_test.go covers the same machinery inside one run, where both
// ends of the resume belong to the same Run call. What is different here
// -- and what these tests pin -- is everything that is *not* the same
// across a dispatch: the home has to be the task's own or none of it
// means anything, the turn sent is the whole prompt rather than a nudge,
// the sandbox is a new one and the model is told so, and a resume codex
// refuses is read as a store grain has pruned rather than as an
// invocation the binary will not perform.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/sessionstore"
)

// withSessionsIn is a Framework whose runs keep their homes under dir --
// what the daemon builds with WithSessionStore, in a directory the test
// owns.
func withSessionsIn(dir string) Option { return WithSessionStore(sessionstore.New(dir)) }

// The two facts everything else rests on: a run with a task id works in
// the task's own home, and that home is still there with what codex
// wrote in it once Run has returned -- while this run's own config and
// its link to a login are not.
func TestATasksHomeOutlivesTheRunAndKeepsOnlyWhatCodexWrote(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: attemptStream(agentMessage("done"), `{"type":"turn.completed"}`)},
	}}
	f := newTestFramework(r, "/usr/local/bin/grain", withSessionsIn(t.TempDir()), withoutRetries())
	// A run that leaves a thread behind, the way codex does: a file in
	// the home nobody here wrote.
	r.onCall = func(call int, dir string) {
		os.MkdirAll(filepath.Join(dir, "sessions"), 0o700)
		os.WriteFile(filepath.Join(dir, "sessions", "rollout.jsonl"), []byte("t1"), 0o600)
	}

	if _, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "do the thing", SandboxRoot: t.TempDir(), TaskID: "42",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	home := r.calls[0].dir
	if _, err := os.Stat(filepath.Join(home, "sessions", "rollout.jsonl")); err != nil {
		t.Errorf("the thread codex wrote did not outlive the run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, configFileName)); !os.IsNotExist(err) {
		t.Errorf("this run's own config.toml -- which names its sandbox's MCP endpoint and the bearer token for it -- was left in a home the next run is given: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, environmentsFileName)); !os.IsNotExist(err) {
		t.Errorf("this run's own environments.toml was left behind: %v", err)
	}
}

// A run with no task -- and a Framework with no store at all -- is this
// package exactly as it was: a temp home, removed when the run ends.
func TestARunWithNoTaskStillGetsATempHomeThatGoesWithIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []Option
		cfg  agent.RunConfig
	}{
		{"no store configured", nil, agent.RunConfig{TaskID: "42"}},
		{"no task id", []Option{withSessionsIn(t.TempDir())}, agent.RunConfig{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &attemptRunner{replies: []attemptReply{
				{stdout: attemptStream(agentMessage("done"), `{"type":"turn.completed"}`)},
			}}
			f := newTestFramework(r, "/usr/local/bin/grain", append(tc.opts, withoutRetries())...)

			cfg := tc.cfg
			cfg.Prompt, cfg.SandboxRoot = "do the thing", t.TempDir()
			result, err := f.Run(context.Background(), cfg)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if _, err := os.Stat(r.calls[0].dir); !os.IsNotExist(err) {
				t.Errorf("the run's CODEX_HOME survived it: %v", err)
			}
			// And, because it did not survive, the thread in it is not
			// one to hand anybody.
			if result.ResumableSession != "" {
				t.Errorf("ResumableSession = %q, want none: the home that thread lives in was deleted with the run, so the next dispatch would spend an exec finding that out", result.ResumableSession)
			}
		})
	}
}

// The point of the feature. A dispatch handed a suspended thread joins it
// on its very first exec, so the work the previous run did is still in
// the conversation the model is answering from.
func TestRunResumesTheThreadAPreviousDispatchSuspended(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: attemptStream(agentMessage("carrying on"), `{"type":"turn.completed"}`)},
	}}
	f := newTestFramework(r, "/usr/local/bin/grain", withSessionsIn(t.TempDir()), withoutRetries())

	result, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "do the thing", SandboxRoot: t.TempDir(), TaskID: "42", ResumeSession: "t-42",
	})
	if err != nil {
		t.Fatalf("Run err = %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("codex was exec'd %d time(s), want 1", len(r.calls))
	}
	if !argsHave(r.calls[0].args, "exec", "resume") || !argsHave(r.calls[0].args, "t-42", "-") {
		t.Errorf("the first attempt's args = %v, want `codex exec resume ... t-42 -` -- the thread the last run left suspended", r.calls[0].args)
	}
	// The whole prompt, unlike the in-run resume: the task may have been
	// edited, a person may have answered the question the last run
	// stopped on, and the conversation cannot know any of it.
	if !strings.Contains(r.calls[0].stdin, "do the thing") {
		t.Errorf("the resumed dispatch's turn = %q, want the task's own prompt in it -- the brief has moved on since that conversation", r.calls[0].stdin)
	}
	if !strings.HasPrefix(r.calls[0].stdin, agent.SuspendedPreamble) {
		t.Errorf("the resumed dispatch's turn = %q, want agent.SuspendedPreamble in front of it: the sandbox is a new one and nothing else says so", r.calls[0].stdin)
	}
	if result.ResumableSession != "t1" {
		t.Errorf("ResumableSession = %q, want t1 -- the thread this run is in at the end of it", result.ResumableSession)
	}
}

// The two halves joined, which is what a dispatch actually does: one run
// reports a thread, the caller hands it back on the next dispatch of the
// same task, and that run resumes it in the same directory the first one
// wrote it into.
func TestTwoDispatchesOfATaskShareAHomeAndAThread(t *testing.T) {
	store := t.TempDir()
	first := &attemptRunner{replies: []attemptReply{
		{stdout: attemptStream(agentMessage("half done"), `{"type":"turn.completed"}`)},
	}}
	f := newTestFramework(first, "/usr/local/bin/grain", withSessionsIn(store), withoutRetries())
	result, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "do the thing", SandboxRoot: t.TempDir(), TaskID: "42",
	})
	if err != nil {
		t.Fatalf("the first dispatch: %v", err)
	}

	second := &attemptRunner{replies: []attemptReply{
		{stdout: attemptStream(agentMessage("finished"), `{"type":"turn.completed"}`)},
	}}
	// A Framework of its own, as a second dispatch gets: the daemon
	// builds one per run, and the store is what the two have in common.
	g := newTestFramework(second, "/usr/local/bin/grain", withSessionsIn(store), withoutRetries())
	if _, err := g.Run(context.Background(), agent.RunConfig{
		Prompt: "do the thing", SandboxRoot: t.TempDir(), TaskID: "42",
		ResumeSession: result.ResumableSession,
	}); err != nil {
		t.Fatalf("the second dispatch: %v", err)
	}
	if second.calls[0].dir != first.calls[0].dir {
		t.Errorf("the second dispatch worked in %q, the first in %q: the thread is resolved out of CODEX_HOME and nothing else, so they have to be the same directory", second.calls[0].dir, first.calls[0].dir)
	}
	if !argsHave(second.calls[0].args, result.ResumableSession, "-") {
		t.Errorf("the second dispatch's args = %v, want the first dispatch's thread (%q)", second.calls[0].args, result.ResumableSession)
	}
}

// A token from a previous dispatch is only worth passing on where this
// run's home is the one that token was written into. Told to resume with
// no store to resume out of, the run works the task from its prompt
// rather than spending an exec on a thread that cannot be there.
func TestASuspendedThreadIsIgnoredWhenTheHomeIsNotTheTasksOwn(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: attemptStream(agentMessage("done"), `{"type":"turn.completed"}`)},
	}}
	f := newTestFramework(r, "/usr/local/bin/grain", withoutRetries())

	if _, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "do the thing", SandboxRoot: t.TempDir(), TaskID: "42", ResumeSession: "t-42",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if slices.Contains(r.calls[0].args, "resume") {
		t.Errorf("the first attempt's args = %v, want no resume: this run has a temp home, and the thread that token names was never in it", r.calls[0].args)
	}
	if r.calls[0].stdin != "do the thing" {
		t.Errorf("the run's turn = %q, want the task's prompt alone", r.calls[0].stdin)
	}
}

// Every ordinary run hands its thread back, whether or not it was given
// one: that is what makes a *first* dispatch leave something for the
// second to resume.
func TestRunReportsTheThreadItOpened(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: attemptStream(agentMessage("pushed"), `{"type":"turn.completed"}`)},
	}}
	f := newTestFramework(r, "/usr/local/bin/grain", withSessionsIn(t.TempDir()), withoutRetries())

	result, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "do the thing", SandboxRoot: t.TempDir(), TaskID: "42",
	})
	if err != nil {
		t.Fatalf("Run err = %v", err)
	}
	if slices.Contains(r.calls[0].args, "resume") {
		t.Errorf("a first dispatch's args = %v, want no resume: there is nothing suspended", r.calls[0].args)
	}
	if r.calls[0].stdin != "do the thing" {
		t.Errorf("a first dispatch's turn = %q, want the task's prompt alone", r.calls[0].stdin)
	}
	if result.ResumableSession != "t1" {
		t.Errorf("ResumableSession = %q, want t1", result.ResumableSession)
	}
}

// The case the whole feature is aimed at: a run the wall-clock cap or
// the provider ended still leaves its conversation behind, and a partial
// result is what carries it.
func TestAFailedRunStillReportsItsThread(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: attemptStream(agentMessage("half way through"),
			`{"type":"turn.failed","error":{"message":"boom"}}`), err: errors.New("exit status 1")},
	}}
	f := newTestFramework(r, "/usr/local/bin/grain", withSessionsIn(t.TempDir()), withoutRetries())

	result, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "do the thing", SandboxRoot: t.TempDir(), TaskID: "42",
	})
	if err == nil {
		t.Fatal("Run err = nil, want the failure")
	}
	if result == nil {
		t.Fatal("Run returned no result beside its error, so the run's thread went with it")
	}
	if result.ResumableSession != "t1" {
		t.Errorf("ResumableSession = %q, want t1 -- a run that ended badly is the one worth resuming", result.ResumableSession)
	}
}

// A suspended thread the store no longer has: grain's own bounds prune
// it (sessionstore), so the dispatch must fall back to working the task
// from its prompt rather than failing -- and must say in the transcript
// that it did.
func TestADispatchWhoseSuspendedThreadIsGoneRunsTheTaskAnyway(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: "", err: errors.New("exit status 1 (stderr: no rollout found for thread id t-old)")},
		{stdout: attemptStream(agentMessage("from the top"), `{"type":"turn.completed"}`)},
	}}
	f := newTestFramework(r, "/usr/local/bin/grain", withSessionsIn(t.TempDir()), withoutRetries())

	result, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "do the thing", SandboxRoot: t.TempDir(), TaskID: "42", ResumeSession: "t-old",
	})
	if err != nil {
		t.Fatalf("Run err = %v, want the fallback to have carried the run", err)
	}
	if len(r.calls) != 2 {
		t.Fatalf("codex was exec'd %d time(s), want 2 (the refused resume, then the task from its prompt)", len(r.calls))
	}
	if slices.Contains(r.calls[1].args, "resume") {
		t.Errorf("the fallback's args = %v, want no resume", r.calls[1].args)
	}
	// The plain prompt, not agent.RestartPreamble's: nothing was done in
	// this sandbox, which is what that preamble exists to warn about.
	if r.calls[1].stdin != "do the thing" {
		t.Errorf("the fallback's turn = %q, want the task's prompt alone -- this sandbox is fresh, so there is no earlier attempt's work in it to warn about", r.calls[1].stdin)
	}
	if !strings.Contains(result.Transcript, "could not be resumed") {
		t.Errorf("the transcript does not say the suspended thread was gone:\n%s", result.Transcript)
	}
	if result.ResumableSession != "t1" {
		t.Errorf("ResumableSession = %q, want t1 -- the thread this run actually opened is the one to keep", result.ResumableSession)
	}
}

// And the refused resume must not cost the run one of its retries, for
// the reason the in-run fallback does not: no model turn was bought with
// it. Here the run is refused its suspended thread and then meets a
// capacity refusal, which must still have its full budget to recover
// from.
func TestARefusedSuspendedResumeCostsTheRunNoRetries(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: "", err: errors.New("exit status 1 (stderr: no rollout found for thread id t-old)")},
		{stdout: attemptStream(agentMessage("started"), overloadedTurn), err: errors.New("exit status 1")},
		{stdout: attemptStream(agentMessage("recovered"), `{"type":"turn.completed"}`)},
	}}
	f := newTestFramework(r, "/usr/local/bin/grain", withSessionsIn(t.TempDir()), withoutBackoff())

	result, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "go", SandboxRoot: t.TempDir(), TaskID: "42", ResumeSession: "t-old",
	})
	if err != nil {
		t.Fatalf("Run err = %v, want the retry to have carried the run", err)
	}
	if len(r.calls) != 3 {
		t.Fatalf("codex was exec'd %d time(s), want 3 -- the refused resume bought no model turn and must not have cost a retry", len(r.calls))
	}
	if result.FinalText != "recovered" {
		t.Errorf("FinalText = %q, want the last attempt's answer", result.FinalText)
	}
}

// A thread grain was told to resume, refused, and then reopened under a
// new id is still worth keeping -- but a build that refuses the
// *invocation* is not, and the two have to be told apart. Here codex
// refuses a thread it named itself, mid-run, which can only be the argv:
// the run reports no resumable thread at all, so the next dispatch is
// not sent back into the same refusal.
func TestARunWhoseBuildRefusesTheResumeInvocationReportsNoThread(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: attemptStream(agentMessage("started"), overloadedTurn), err: errors.New("exit status 1")},
		{stdout: "", err: errors.New("exit status 2 (stderr: error: unrecognized subcommand 'resume')")},
		{stdout: attemptStream(agentMessage("done"), `{"type":"turn.completed"}`)},
	}}
	f := newTestFramework(r, "/usr/local/bin/grain", withSessionsIn(t.TempDir()), withoutBackoff())

	result, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "do the thing", SandboxRoot: t.TempDir(), TaskID: "42",
	})
	if err != nil {
		t.Fatalf("Run err = %v", err)
	}
	if result.ResumableSession != "" {
		t.Errorf("ResumableSession = %q, want none: this build will not reopen a thread, so handing one to the next dispatch buys it a refused exec", result.ResumableSession)
	}
}
