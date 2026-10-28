package codex

// The retry loop itself, driven against a real codex.
//
// codex_live_test.go's TestLiveCodexResumesAThreadAcrossExecs proves the
// argv, the CODEX_HOME resolution and the conversation replay -- but it
// execs codex by hand, with f.codexArgs and a config it wrote itself.
// retry_test.go proves what runAttempts *decides* -- when to resume,
// what to send, when to fall back -- but against a scripted runner that
// speaks whichever event vocabulary this repository wrote down. The two
// meet nowhere, which is the same shape of blindness the live tests
// exist to close, one layer up: a codex whose refusal no longer says
// what capacity.go matches, or whose resume no longer takes the argv
// this loop builds, passes both halves and retries nothing.
//
// The probes here are that join. They need the binary and no credential,
// because a `model_providers` stanza in the run's own config.toml points
// codex at an HTTP server inside the test process (withExtraConfig, and
// see Framework.extraConfig for why a stanza is the only way to say it),
// and that server can refuse a turn on demand -- which nobody can make
// OpenAI do. They are the codex counterpart of
// agent/claude/live_resume_stub_test.go's own two, added by task 339,
// and the live-frameworks.yml job runs them ahead of its credential gate
// for the same reason.
//
// What they still do not prove: anything about OpenAI's own side of a
// resumed thread. codex 0.153 replays the conversation out of its
// on-disk rollout, so there is nothing server-side to survive; if that
// changes, these go on passing and TestLiveRunEndToEnd's credential is
// what would have to notice.

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// withExtraConfig points a Framework's runs at configuration this
// package never writes in production -- the stub provider below and
// nothing else. It lives here, beside its only callers, exactly as
// withoutBackoff does: the field it sets is production's (it has to be,
// since Framework.Run builds its own CODEX_HOME and there is no other
// way in), but nothing outside these tests ever sets it.
func withExtraConfig(toml string) Option {
	return func(f *Framework) { f.extraConfig = toml }
}

// stubProviderConfig is the stanza that makes a run's codex talk to url
// instead of to OpenAI: the selector and the provider table behind it.
// extra lines are added to that table.
//
// It is a whole provider rather than an override of codex's built-in
// "openai" because codex refuses to override a built-in one at all
// ("model_providers contains reserved built-in provider IDs"), which is
// the same wall retry.go's header runs into from the other side.
func stubProviderConfig(url string, extra ...string) string {
	table := append([]string{
		`[model_providers.stub]`,
		`name = "stub"`,
		`base_url = "` + url + `/v1"`,
		`wire_api = "responses"`,
		`env_key = "OPENAI_API_KEY"`,
	}, extra...)
	return "model_provider = \"stub\"\n\n" + strings.Join(table, "\n")
}

// noInternalRetries are the two keys that stop codex climbing its own
// retry ladder before it reports a refusal: five requests per stream
// attempt and six stream attempts, 25 seconds of them, measured in
// live_retry_stub_test.go, which is the one test here that must *not*
// pass these. What the probes below are about is what grain does after
// codex has given up, so they turn the ladder off and get the same
// refusal in about a second. (That the keys work at all is the finding
// retry.go's header records; a codex that stopped honouring them would
// show up here as a probe that got slow rather than one that failed.)
var noInternalRetries = []string{"request_max_retries = 0", "stream_max_retries = 0"}

// stubAPIKey is what a probe puts in OPENAI_API_KEY. codex requires the
// variable its provider table names to be set, and the stub never looks
// at the value -- but it is spelled out as a non-credential so nobody
// reading a failing job's environment goes looking for a leak.
const stubAPIKey = "stub-key-not-a-real-credential"

// liveProbeRuntime is the deadline the probes give a run, and it is
// deliberately generous: agent.RetryPolicy declines to retry at all
// unless the run has more than one backoff plus agent.MinimumRetryRuntime
// (two minutes) of deadline left, so a probe with a tighter one would
// measure that decline rather than the retry, and would do it by passing
// for the wrong reason.
const liveProbeRuntime = 300 * time.Second

// resumePromptMarker and restartPreambleMarker are phrases distinctive
// enough to find in a request body, taken from the constants themselves
// so a reworded prompt cannot make an assertion pass by matching
// nothing.
var (
	resumePromptMarker    = strings.SplitN(agent.ResumePrompt, ":", 2)[0]
	restartPreambleMarker = strings.SplitN(agent.RestartPreamble, ":", 2)[0]
)

// TestLiveCodexRetryResumesTheRunAfterARefusal drives Framework.Run --
// not codexArgs, not runAttempts -- through a provider refusal and its
// retry, and reads what the second attempt actually sent the model.
//
// Every link in the chain is a real one, and each has already been an
// assumption somewhere in this package:
//
//   - that capacity.go reads what a refused codex reports as a refusal
//     at all, with the config a real run gets (MCP server, read-only
//     sandbox) rather than a simplified one.
//   - that the loop then execs codex again with `exec resume` and the
//     thread id it parsed off the first attempt's own stream, and that
//     the argv it assembles is one this codex performs.
//   - that the resumed attempt is sent agent.ResumePrompt rather than
//     the task from the top.
//   - that the model on the other end is given the first attempt's
//     conversation, which is the whole saving the resume is for and the
//     one part no event stream shows.
//   - and that the run then comes back as a *successful* run: the
//     capture spans two attempts, and parseTranscript reads the second
//     one's ending as what the run ended as.
//
// It costs two codex startups and no model turn worth paying for: the
// stub answers with one word.
func TestLiveCodexRetryResumesTheRunAfterARefusal(t *testing.T) {
	// The setup the probes next door use, with the credential Run passes
	// its subprocess and no backoff -- waiting out agent/retry.go's 30
	// seconds would add them to a nightly to test a policy this package
	// does not own. c.env and the CODEX_HOME behind it go unused here:
	// Run writes a home of its own, out of the same config, which is
	// exactly the part being exercised.
	c := newLiveStubbedCodex(t, noInternalRetries, WithAPIKey(stubAPIKey), withoutBackoff())
	// One refusal, which with noInternalRetries is exactly one failed
	// attempt: codex asks once, is refused, and reports it.
	c.provider.refuseNext(1)

	ctx, cancel := context.WithTimeout(context.Background(), liveProbeRuntime)
	defer cancel()

	const marker = "GRAIN-RETRY-LOOP-PROBE-TASK-PROMPT"
	result, err := c.f.Run(ctx, agent.RunConfig{
		Prompt:      "Reply with nothing but the word ok. " + marker,
		SandboxRoot: c.dir,
	})
	if err != nil {
		t.Fatalf("Run: %v\nThe first attempt was refused by the stub and the run should have "+
			"carried on through its retry; an error here means it did not.\nresult: %+v", err, result)
	}
	t.Logf("run finished: %q\ntranscript:\n%s", result.FinalText, result.Transcript)

	bodies := c.provider.bodies()
	if len(bodies) < 2 {
		t.Fatalf("the stub saw %d request(s), want two: the attempt it refused and the retry.\n"+
			"Either capacity.go no longer reads what codex reports for a 503 as a refusal (so "+
			"nothing was retried), or the retry never reached the model.\ntranscript:\n%s",
			len(bodies), result.Transcript)
	}
	last := bodies[len(bodies)-1]
	if !strings.Contains(last, marker) {
		t.Errorf("the retried attempt's request does not carry the task's own prompt (%s), so the "+
			"resume kept the thread's name and not its contents -- which is the whole saving "+
			"retry.go is after.\nrequest:\n%s", marker, last)
	}
	if !strings.Contains(last, resumePromptMarker) {
		// Not broken, exactly -- this is what the fallback looks like --
		// but it means the run paid for a resume and got a restart.
		t.Errorf("the retried attempt was not sent agent.ResumePrompt.\nIf the transcript below "+
			"carries resumeRefusedNote then codex refused the resumed argv and this package fell "+
			"back to restarting the task, which is the fallback working and the resume buying "+
			"nothing.\ntranscript:\n%s\nrequest:\n%s", result.Transcript, last)
	}
	if strings.Contains(result.Transcript, resumeRefusedNote) {
		t.Errorf("codex refused the resumed invocation this package builds. The run still "+
			"finished, because the fallback restarted it in place -- but the resume is then "+
			"costing every retry a process start and buying nothing, and should be removed "+
			"rather than left standing.\ntranscript:\n%s", result.Transcript)
	}
	// What the run ends as is the *second* attempt's ending: a capture
	// holding a refused turn and then a completed one must not come back
	// as the refusal.
	if result.FinalText == "" {
		t.Errorf("the run came back with no final answer, though both attempts' streams are in "+
			"the capture: parseTranscript did not read the last attempt's ending as the run's.\n"+
			"transcript:\n%s", result.Transcript)
	}
	if !strings.Contains(result.Transcript, agent.RetryNote(2, true)) {
		t.Errorf("the transcript does not say the run was refused and resumed, so a reader of a "+
			"transcript that restarts half way through is not told why.\nwant: %q\ntranscript:\n%s",
			agent.RetryNote(2, true), result.Transcript)
	}
}

// TestLiveCodexFallsBackWhenAResumeIsRefused is the other branch of the
// same loop, and the one that keeps a future codex from turning a
// provider blip into a hard failure: an attempt that asked to resume and
// died without codex opening a thread at all is read as the binary
// refusing the argv, so the run drops the resume for good and starts the
// task again in the same sandbox.
//
// A codex that will not perform the invocation is the thing being stood
// in for, and the stand-in is a thread id codex has no rollout for --
// substituted for the real one at the seam runAttempts already has for
// building an argv. TestLiveCodexRefusesAThreadItDoesNotHave is that
// refusal on its own, asserted directly: that codex exits non-zero
// having streamed nothing, not even the `thread.started` every exec
// announces before it authenticates, and having sent the model nothing.
// This probe is the other half of the same fact -- the loop meeting that
// refusal and carrying the run anyway -- and the two are worth having
// separately, because a codex that changed its answer should say so in
// the measurement rather than only in the loop that depends on it.
//
// The two ways the premise could go wrong are opposite faults, and
// neither is visible from a scripted runner:
//
//   - codex fails *after* announcing a thread. The fallback never fires,
//     and a run whose thread went missing ends on "no rollout found"
//     instead of retrying in place.
//   - codex quietly starts a fresh thread instead. Worse: the resumed
//     attempt sends agent.ResumePrompt -- "you were interrupted, carry
//     on" -- into a conversation that has never been told what the task
//     is, and the run spends the rest of its budget on an agent with no
//     idea what it is meant to do.
//
// It drives runAttempts rather than Run because the substitution has to
// happen at the argv, which is where "a codex that refuses this
// invocation" can be said at all -- a stub provider cannot say it, since
// codex announces its thread before it ever reaches the network.
func TestLiveCodexFallsBackWhenAResumeIsRefused(t *testing.T) {
	c := newLiveStubbedCodex(t, noInternalRetries, withoutBackoff())
	c.provider.refuseNext(1)

	ctx, cancel := context.WithTimeout(context.Background(), liveProbeRuntime)
	defer cancel()

	f := c.f
	const marker = "GRAIN-FALLBACK-PROBE-TASK-PROMPT"
	prompt := "Reply with nothing but the word ok. " + marker
	// A well-formed thread id codex cannot possibly hold, in place of the
	// one the loop parsed off the first attempt's stream.
	const missing = "00000000-0000-4000-8000-000000000000"
	var resumedThreads []string
	out := f.runAttempts(ctx, attemptSetup{
		args: func(thread string) []string {
			if thread == "" {
				return f.codexArgs("")
			}
			resumedThreads = append(resumedThreads, thread)
			return f.codexArgs(missing)
		},
		// The environment and the sandbox Framework.Run would have built:
		// the thread the loop tries to resume has to live somewhere, and
		// this run's own CODEX_HOME is the only place codex keeps one.
		env:           c.env,
		dir:           c.dir,
		prompt:        prompt,
		resumePrompt:  agent.ResumePrompt,
		restartPrompt: agent.RestartPreamble + prompt,
		cap:           &turnCap{},
	})
	t.Logf("the loop asked to resume %v and left notes %q", resumedThreads, out.notes)

	if len(resumedThreads) != 1 {
		t.Fatalf("the loop asked to resume %d time(s), want exactly 1: the first attempt was "+
			"refused by the stub, so it should have opened a thread, been retried, and had that "+
			"resume refused once.\ncapture:\n%s", len(resumedThreads), out.capture)
	}
	if out.runErr != nil {
		t.Errorf("the run ended on %v rather than being restarted in place after the refused "+
			"resume.\ncapture:\n%s", out.runErr, out.capture)
	}
	if !slices.Contains(out.notes, resumeRefusedNote) {
		t.Fatalf("the refused resume left no note, so either codex announced a thread before "+
			"failing on an id it has no rollout for -- in which case the fallback never fires and "+
			"a missing thread is a hard failure -- or it quietly started a fresh thread, in which "+
			"case a resumed attempt is sent \"carry on\" into a conversation that was never told "+
			"the task.\nnotes: %q\ncapture:\n%s", out.notes, out.capture)
	}

	bodies := c.provider.bodies()
	if len(bodies) < 2 {
		t.Fatalf("the stub saw %d request(s), want two: the attempt it refused and the restart "+
			"that followed the refused resume (the resume itself never reaches the network).\n"+
			"capture:\n%s", len(bodies), out.capture)
	}
	last := bodies[len(bodies)-1]
	if !strings.Contains(last, restartPreambleMarker) {
		t.Errorf("the attempt after the refused resume was not sent agent.RestartPreamble, so the "+
			"model meets a sandbox holding an earlier attempt's work with nothing to say where it "+
			"came from.\nrequest:\n%s", last)
	}
	if !strings.Contains(last, marker) {
		t.Errorf("the attempt after the refused resume was not sent the task's own prompt (%s): a "+
			"restart that keeps neither the conversation nor the task is an agent with nothing to "+
			"do.\nrequest:\n%s", marker, last)
	}
	if strings.Contains(last, resumePromptMarker) {
		t.Errorf("the attempt after the refused resume was still sent agent.ResumePrompt, which "+
			"asks a conversation that does not exist to carry on.\nrequest:\n%s", last)
	}
	if _, err := parseTranscript(out.capture); err != nil {
		t.Errorf("the run's whole capture does not read as a run that finished: %v\ncapture:\n%s",
			err, out.capture)
	}
}

// TestExtraConfigLandsWhereItWasWritten is the one test here that needs
// no codex, and it guards the trap the whole seam turns on: TOML puts
// every bare key after a table header *inside* that table, so a stanza
// appended to the end of the config would put `model_provider` in this
// run's [mcp_servers.<name>] table -- where codex never looks -- and
// every probe above would quietly talk to the real OpenAI and fail on a
// 401 that says nothing about why.
//
// The complementary half, that a Framework nobody handed extra config to
// writes none, is TestRunLeavesCodexsOwnAPIRetryAlone in codex_test.go:
// it asserts a real run's config has no provider table and no retry keys
// at all.
func TestExtraConfigLandsWhereItWasWritten(t *testing.T) {
	r := &recordingRunner{stdout: okStream()}
	f := newTestFramework(r, "/usr/local/bin/grain", withExtraConfig(stubProviderConfig("http://127.0.0.1:1", noInternalRetries...)))

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	config := r.configAtRun
	selector := strings.Index(config, "model_provider = ")
	firstTable := strings.Index(config, "\n[")
	switch {
	case selector < 0:
		t.Fatalf("the extra config is not in the file at all:\n%s", config)
	case firstTable >= 0 && selector > firstTable:
		t.Errorf("`model_provider` is written after the first table header, so it belongs to that "+
			"table rather than to the run:\n%s", config)
	}
	for _, want := range []string{"[model_providers.stub]", "request_max_retries = 0", "[features]", "[mcp_servers."} {
		if !strings.Contains(config, want) {
			t.Errorf("the config is missing %q -- the splice dropped either the extra config or "+
				"what this package writes for every run:\n%s", want, config)
		}
	}
}
