package antigravity

// The cross-dispatch half of resume.go: a run given the task's own HOME
// (sessionstore) picks up the conversation the *previous dispatch* of
// that task left in it, and hands back the conversation it ends in so
// the next one can do the same (agent.RunConfig.ResumeSession and
// agent.Result.ResumableSession).
//
// resume_test.go covers the same machinery inside one run, where both
// ends of the resume belong to the same Run call. What is different here
// -- and what these tests pin -- is everything that is *not* the same
// across a dispatch: the HOME has to be the task's own or none of it
// means anything, the turn sent is the whole prompt rather than a nudge,
// the sandbox is a new one and the model is told so, and a resume agy
// refuses is read as a store grain has pruned rather than as an argv the
// binary will not take.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/sessionstore"
)

// withSessionsIn is a Framework whose runs keep their HOMEs under dir --
// what the daemon builds with WithSessionStore, in a directory the test
// owns.
func withSessionsIn(dir string) Option { return WithSessionStore(sessionstore.New(dir)) }

// The two facts everything else rests on: a run with a task id works in
// the task's own HOME, and that HOME is still there with what agy wrote
// in it once Run has returned -- while this run's own MCP config,
// settings, hooks and scratch are not.
func TestATasksHomeOutlivesTheRunAndKeepsOnlyWhatAgyWrote(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(conversationInit("conv-1"), successResult("done"))},
	}}
	// A run that leaves a conversation behind, the way agy does: a file
	// under the HOME nobody in this package wrote.
	r.afterRead = func(home string) {
		os.MkdirAll(filepath.Join(home, agyConversationsDir), 0o700)
		os.WriteFile(filepath.Join(home, agyConversationsDir, "conv-1.json"), []byte("{}"), 0o600)
	}
	f := newFramework(r, "/usr/local/bin/grain", withSessionsIn(t.TempDir()))

	if _, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "do the thing", SandboxRoot: t.TempDir(), TaskID: "42",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	home := homeOf(r.calls[0].env)
	if _, err := os.Stat(filepath.Join(home, agyConversationsDir, "conv-1.json")); err != nil {
		t.Errorf("the conversation agy wrote did not outlive the run: %v", err)
	}
	for _, rel := range []string{mcpConfigRelPath, cliSettingsRelPath, hooksConfigRelPath} {
		if _, err := os.Stat(filepath.Join(home, rel)); !os.IsNotExist(err) {
			t.Errorf("%s -- which names this run's own sandbox -- was left in a HOME the next run is given: %v", rel, err)
		}
	}
	if _, err := os.Stat(runScratchDir(home)); !os.IsNotExist(err) {
		t.Errorf("this run's scratch directory, diagnostic logs and all, was left behind: %v", err)
	}
}

// A run with no task -- and a Framework with no store at all -- is this
// package exactly as it was: a temp HOME, removed when the run ends.
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
				{stdout: stream(conversationInit("conv-1"), successResult("done"))},
			}}
			f := newFramework(r, "/usr/local/bin/grain", tc.opts...)

			cfg := tc.cfg
			cfg.Prompt, cfg.SandboxRoot = "do the thing", t.TempDir()
			result, err := f.Run(context.Background(), cfg)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if home := homeOf(r.calls[0].env); home == "" {
				t.Fatal("the run was given no HOME at all")
			} else if _, err := os.Stat(home); !os.IsNotExist(err) {
				t.Errorf("the run's private HOME survived it: %v", err)
			}
			// And, because it did not survive, the conversation in it is
			// not one to hand anybody.
			if result.ResumableSession != "" {
				t.Errorf("ResumableSession = %q, want none: the HOME that conversation lives in was deleted with the run, so the next dispatch would spend an exec finding that out", result.ResumableSession)
			}
		})
	}
}

// The point of the feature. A dispatch handed a suspended conversation
// joins it on its very first exec, so the work the previous run did is
// still in the conversation the model is answering from.
func TestRunResumesTheConversationAPreviousDispatchSuspended(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(conversationInit("conv-42"), successResult("carried on"))},
	}}
	f := newFramework(r, "/usr/local/bin/grain", withSessionsIn(t.TempDir()))

	result, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "do the thing", SandboxRoot: t.TempDir(), TaskID: "42", ResumeSession: "conv-42",
	})
	if err != nil {
		t.Fatalf("Run err = %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("agy was exec'd %d time(s), want 1", len(r.calls))
	}
	if got := flagValue(r.calls[0].args, "--conversation"); got != "conv-42" {
		t.Errorf("the first attempt resumed %q, want conv-42 -- the conversation the last run left suspended", got)
	}
	turn, err := promptFromStdin(r.calls[0].stdin)
	if err != nil {
		t.Fatalf("decoding the resumed dispatch's turn: %v", err)
	}
	// The whole prompt, unlike the in-run resume: the task may have been
	// edited, a person may have answered the question the last run
	// stopped on, and the conversation cannot know any of it.
	if !strings.Contains(turn, "do the thing") {
		t.Errorf("the resumed dispatch's turn = %q, want the task's own prompt in it -- the brief has moved on since that conversation", turn)
	}
	if !strings.Contains(turn, agent.SuspendedPreamble) {
		t.Errorf("the resumed dispatch's turn = %q, want agent.SuspendedPreamble in it: the sandbox is a new one and nothing else says so", turn)
	}
	if result.ResumableSession != "conv-42" {
		t.Errorf("ResumableSession = %q, want conv-42 -- the run stayed in the conversation it was resumed into", result.ResumableSession)
	}
}

// The two halves joined, which is what a dispatch actually does: one run
// reports a conversation, the caller hands it back on the next dispatch
// of the same task, and that run resumes it in the same HOME the first
// one wrote it into.
func TestTwoDispatchesOfATaskShareAHomeAndAConversation(t *testing.T) {
	store := t.TempDir()
	first := &attemptRunner{replies: []attemptReply{
		{stdout: stream(conversationInit("conv-1"), successResult("half done"))},
	}}
	f := newFramework(first, "/usr/local/bin/grain", withSessionsIn(store))
	result, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "do the thing", SandboxRoot: t.TempDir(), TaskID: "42",
	})
	if err != nil {
		t.Fatalf("the first dispatch: %v", err)
	}

	second := &attemptRunner{replies: []attemptReply{
		{stdout: stream(conversationInit("conv-1"), successResult("finished"))},
	}}
	// A Framework of its own, as a second dispatch gets: the daemon
	// builds one per run, and the store is what the two have in common.
	g := newFramework(second, "/usr/local/bin/grain", withSessionsIn(store))
	if _, err := g.Run(context.Background(), agent.RunConfig{
		Prompt: "do the thing", SandboxRoot: t.TempDir(), TaskID: "42",
		ResumeSession: result.ResumableSession,
	}); err != nil {
		t.Fatalf("the second dispatch: %v", err)
	}
	if h1, h2 := homeOf(first.calls[0].env), homeOf(second.calls[0].env); h1 == "" || h1 != h2 {
		t.Errorf("HOME = %q then %q: agy keeps its conversations under the HOME it was started with, so the two dispatches have to share one", h1, h2)
	}
	if got := flagValue(second.calls[0].args, "--conversation"); got != result.ResumableSession {
		t.Errorf("the second dispatch resumed %q, want the first dispatch's own conversation (%q)", got, result.ResumableSession)
	}
}

// A token from a previous dispatch is only worth passing on where this
// run's HOME is the one that token was written into. Told to resume with
// no store to resume out of, the run works the task from its prompt
// rather than spending an exec on a conversation that cannot be there.
func TestASuspendedConversationIsIgnoredWhenTheHomeIsNotTheTasksOwn(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(conversationInit("conv-1"), successResult("done"))},
	}}
	f := newFramework(r, "/usr/local/bin/grain")

	if _, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "do the thing", SandboxRoot: t.TempDir(), TaskID: "42", ResumeSession: "conv-42",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := flagValue(r.calls[0].args, "--conversation"); got != "" {
		t.Errorf("the first attempt asked to resume %q: this run has a temp HOME, and that conversation was never in it", got)
	}
}

// Every ordinary run hands its conversation back, whether or not it was
// given one: that is what makes a *first* dispatch leave something for
// the second to resume.
func TestRunReportsTheConversationItOpened(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(conversationInit("conv-1"), successResult("pushed"))},
	}}
	f := newFramework(r, "/usr/local/bin/grain", withSessionsIn(t.TempDir()))

	result, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "do the thing", SandboxRoot: t.TempDir(), TaskID: "42",
	})
	if err != nil {
		t.Fatalf("Run err = %v", err)
	}
	if got := flagValue(r.calls[0].args, "--conversation"); got != "" {
		t.Errorf("a first dispatch asked to resume %q, and there was nothing suspended", got)
	}
	if result.ResumableSession != "conv-1" {
		t.Errorf("ResumableSession = %q, want conv-1", result.ResumableSession)
	}
}

// The case the whole feature is aimed at: a run the wall-clock cap or the
// provider ended still leaves its conversation behind, and a partial
// result is what carries it.
func TestAFailedRunStillReportsItsConversation(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		// A FAILURE agy reported and nothing here can classify: the run
		// is over -- resume.go reads that status as a verdict rather
		// than an interruption -- and the conversation it ended in is
		// what the task's next dispatch gets.
		{stdout: stream(conversationInit("conv-3"), agentTurn(1, "half way through"),
			`{"event":"result","result":{"status":"FAILURE","error":"boom"}}`),
			err: errors.New("exit status 1")},
	}}
	f := newFramework(r, "/usr/local/bin/grain", withSessionsIn(t.TempDir()), withoutBackoff())

	result, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "do the thing", SandboxRoot: t.TempDir(), TaskID: "42",
	})
	if err == nil {
		t.Fatal("Run err = nil, want the failure")
	}
	if result == nil {
		t.Fatal("Run returned no result beside its error, so the run's conversation went with it")
	}
	if result.ResumableSession != "conv-3" {
		t.Errorf("ResumableSession = %q, want conv-3 -- a run that ended badly is the one worth resuming", result.ResumableSession)
	}
}

// A suspended conversation the store no longer has: grain's own bounds
// prune it (sessionstore), so the dispatch must fall back to working the
// task from its prompt rather than failing -- and must say in the
// transcript that it did.
func TestADispatchWhoseSuspendedConversationIsGoneRunsTheTaskAnyway(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: "", err: errors.New("exit status 1")},
		{stdout: stream(conversationInit("conv-new"), successResult("from the top"))},
	}}
	f := newFramework(r, "/usr/local/bin/grain", withSessionsIn(t.TempDir()))

	result, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "do the thing", SandboxRoot: t.TempDir(), TaskID: "42", ResumeSession: "conv-old",
	})
	if err != nil {
		t.Fatalf("Run err = %v, want the fallback to have carried the run", err)
	}
	if len(r.calls) != 2 {
		t.Fatalf("agy was exec'd %d time(s), want 2 (the refused resume, then the task from its prompt)", len(r.calls))
	}
	if got := flagValue(r.calls[1].args, "--conversation"); got != "" {
		t.Errorf("the fallback still asked to resume %q", got)
	}
	turn, err := promptFromStdin(r.calls[1].stdin)
	if err != nil {
		t.Fatalf("decoding the fallback's turn: %v", err)
	}
	// The plain prompt, not agent.RestartPreamble's: nothing was done in
	// this sandbox, which is what that preamble exists to warn about.
	if !strings.Contains(turn, "do the thing") || strings.Contains(turn, agent.RestartPreamble) {
		t.Errorf("the fallback's turn = %q, want the task's prompt without the restart preamble -- this sandbox is fresh, so there is no earlier attempt's work in it to warn about", turn)
	}
	if !strings.Contains(result.Transcript, "could not be resumed") {
		t.Errorf("the transcript does not say the suspended conversation was gone:\n%s", result.Transcript)
	}
	if result.ResumableSession != "conv-new" {
		t.Errorf("ResumableSession = %q, want conv-new -- the conversation this run actually opened is the one to keep", result.ResumableSession)
	}
}

// The same conversation being gone, as a *real* agy reports it rather
// than as this package first guessed. Neither measured build refuses the
// invocation: agy warns on stderr, opens a conversation of its own and
// runs the turn it was given, exiting 0
// (TestLiveAgyOpensANewConversationForOneItCannotFind). So the only
// evidence the resume did not take is the id on the stream, one exec is
// spent rather than two, and the run carries on -- it was sent the whole
// prompt behind agent.SuspendedPreamble, which is what makes that safe.
func TestADispatchWhoseSuspendedConversationAgyReplacesSaysSo(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(conversationInit("conv-agy-opened"), successResult("from the top"))},
	}}
	f := newFramework(r, "/usr/local/bin/grain", withSessionsIn(t.TempDir()))

	result, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "do the thing", SandboxRoot: t.TempDir(), TaskID: "42", ResumeSession: "conv-pruned",
	})
	if err != nil {
		t.Fatalf("Run err = %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("agy was exec'd %d time(s), want 1: agy answered this exec, so there is nothing to fall back to", len(r.calls))
	}
	if got := flagValue(r.calls[0].args, "--conversation"); got != "conv-pruned" {
		t.Errorf("the run asked to resume %q, want conv-pruned", got)
	}
	if !strings.Contains(result.Transcript, "no longer in agy's store") {
		t.Errorf("the transcript does not say the suspended conversation had been pruned:\n%s", result.Transcript)
	}
	if result.ResumableSession != "conv-agy-opened" {
		t.Errorf("ResumableSession = %q, want conv-agy-opened -- the conversation this run is actually in is the one to hand on", result.ResumableSession)
	}
}

// And the note is only for a conversation that was *carried in*. A run
// that opened its own conversation and stayed in it has nothing to
// report, and neither has one whose retry resumed it.
func TestNoLostConversationNoteForARunsOwnConversation(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(conversationInit("conv-1"), successResult("done"))},
	}}
	f := newFramework(r, "/usr/local/bin/grain", withSessionsIn(t.TempDir()))

	result, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "do the thing", SandboxRoot: t.TempDir(), TaskID: "42",
	})
	if err != nil {
		t.Fatalf("Run err = %v", err)
	}
	if strings.Contains(result.Transcript, "no longer in agy's store") {
		t.Errorf("a first dispatch's transcript says a conversation was lost:\n%s", result.Transcript)
	}
}

// And the refused resume must not cost the run one of its retries, for
// the reason the in-run fallback does not: no model turn was bought with
// it. Here the run is refused its suspended conversation and then meets
// a capacity refusal, which must still have its full budget to recover
// from.
func TestARefusedSuspendedResumeCostsTheRunNoRetries(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: "", err: errors.New("exit status 1")},
		{stdout: stream(conversationInit("conv-1"), overloadedResult), err: errors.New("exit status 1")},
		{stdout: stream(conversationInit("conv-1"), successResult("recovered"))},
	}}
	f := newFramework(r, "/usr/local/bin/grain", withSessionsIn(t.TempDir()), withoutBackoff())

	result, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "go", SandboxRoot: t.TempDir(), TaskID: "42", ResumeSession: "conv-old",
	})
	if err != nil {
		t.Fatalf("Run err = %v, want the retry to have carried the run", err)
	}
	if len(r.calls) != 3 {
		t.Fatalf("agy was exec'd %d time(s), want 3 -- the refused resume bought no model turn and must not have cost a retry", len(r.calls))
	}
	if result.FinalText != "recovered" {
		t.Errorf("FinalText = %q, want the last attempt's answer", result.FinalText)
	}
}

// A conversation grain was told to resume, refused, and then reopened
// under a new id is still worth keeping -- but a build that refuses the
// *flag* is not, and the two have to be told apart. Here agy refuses a
// conversation it named itself, mid-run, which can only be the argv: the
// run reports no resumable conversation at all, so the next dispatch is
// not sent back into the same refusal.
func TestARunWhoseBuildRefusesTheResumeFlagReportsNoConversation(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(conversationInit("conv-9"), overloadedResult), err: errors.New("exit status 1")},
		{stdout: "", err: errors.New("exit status 1")},
		{stdout: stream(conversationInit("conv-10"), successResult("done"))},
	}}
	f := newFramework(r, "/usr/local/bin/grain", withSessionsIn(t.TempDir()), withoutBackoff())

	result, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "do the thing", SandboxRoot: t.TempDir(), TaskID: "42",
	})
	if err != nil {
		t.Fatalf("Run err = %v", err)
	}
	if result.ResumableSession != "" {
		t.Errorf("ResumableSession = %q, want none: this build will not reopen a conversation, so handing one to the next dispatch buys it a refused exec", result.ResumableSession)
	}
}

// When SuspendedPrompt is supplied, a resumed dispatch sends only the delta
// prompt (omitting toolPreamble and the full Prompt); if agy silently replaces
// the conversation with a brand-new one instead of joining ResumeSession, the
// run falls back to a fresh invocation with the full Prompt so the new
// conversation is not left without the task instructions.
func TestRunSendsDeltaSuspendedPromptAndFallsBackToFullPromptWhenConversationIsReplaced(t *testing.T) {
	t.Run("resumed conversation receives only delta prompt", func(t *testing.T) {
		r := &attemptRunner{replies: []attemptReply{
			{stdout: stream(conversationInit("conv-42"), successResult("carried on"))},
		}}
		f := newFramework(r, "/usr/local/bin/grain", withSessionsIn(t.TempDir()))

		result, err := f.Run(context.Background(), agent.RunConfig{
			Prompt:          "FULL-STATIC-TASK-PROMPT",
			SuspendedPrompt: "DELTA-NEW-COMMENT-ONLY",
			SandboxRoot:     t.TempDir(),
			TaskID:          "42",
			ResumeSession:   "conv-42",
		})
		if err != nil {
			t.Fatalf("Run err = %v", err)
		}
		if len(r.calls) != 1 {
			t.Fatalf("agy was exec'd %d time(s), want 1", len(r.calls))
		}
		turn, err := promptFromStdin(r.calls[0].stdin)
		if err != nil {
			t.Fatalf("decoding turn: %v", err)
		}
		if !strings.Contains(turn, "DELTA-NEW-COMMENT-ONLY") {
			t.Errorf("resumed turn = %q, want delta prompt", turn)
		}
		if strings.Contains(turn, "FULL-STATIC-TASK-PROMPT") {
			t.Errorf("resumed turn = %q, unexpectedly repeated full prompt", turn)
		}
		if strings.Contains(turn, "Treat every one of your own built-in tools as unavailable") {
			t.Errorf("resumed turn = %q, unexpectedly repeated toolPreamble", turn)
		}
		if result.ResumableSession != "conv-42" {
			t.Errorf("ResumableSession = %q, want conv-42", result.ResumableSession)
		}
	})

	t.Run("replaced conversation falls back to full prompt", func(t *testing.T) {
		r := &attemptRunner{replies: []attemptReply{
			{stdout: stream(conversationInit("conv-pruned-replacement"), successResult("lost context"))},
			{stdout: stream(conversationInit("conv-fresh"), successResult("from full prompt"))},
		}}
		f := newFramework(r, "/usr/local/bin/grain", withSessionsIn(t.TempDir()))

		result, err := f.Run(context.Background(), agent.RunConfig{
			Prompt:          "FULL-STATIC-TASK-PROMPT",
			SuspendedPrompt: "DELTA-NEW-COMMENT-ONLY",
			SandboxRoot:     t.TempDir(),
			TaskID:          "42",
			ResumeSession:   "conv-pruned",
		})
		if err != nil {
			t.Fatalf("Run err = %v", err)
		}
		if len(r.calls) != 2 {
			t.Fatalf("agy was exec'd %d time(s), want 2 (delta attempt on pruned conversation, then full prompt fallback)", len(r.calls))
		}
		if got := flagValue(r.calls[1].args, "--conversation"); got != "" {
			t.Errorf("fallback call still passed --conversation %q, want empty", got)
		}
		fallbackTurn, err := promptFromStdin(r.calls[1].stdin)
		if err != nil {
			t.Fatalf("decoding fallback turn: %v", err)
		}
		if !strings.Contains(fallbackTurn, "FULL-STATIC-TASK-PROMPT") {
			t.Errorf("fallback turn = %q, want full prompt", fallbackTurn)
		}
		if !strings.Contains(result.Transcript, "no longer in agy's store") {
			t.Errorf("transcript missing lost conversation note:\n%s", result.Transcript)
		}
		if result.ResumableSession != "conv-fresh" {
			t.Errorf("ResumableSession = %q, want conv-fresh", result.ResumableSession)
		}
	})
}
