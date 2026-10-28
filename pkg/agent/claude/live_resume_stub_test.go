package claude

// The half of retry.go's resume that can be measured without a
// credential, and therefore the half that is actually measured.
//
// live_resume_test.go asks a real claude and a real Anthropic whether a
// second exec can pick up the first one's session. It is the better
// question and it stays; what it needs is CLAUDE_CODE_OAUTH_TOKEN, and as
// of 2026-09-08 no such secret has ever reached
// .github/workflows/live-frameworks.yml's live-claude job -- so the claim
// retry.go rests on was, until this file, neither green nor red anywhere.
//
// These probes need the binary and nothing else: they point claude at a
// stub of the Anthropic messages API running inside the test process
// (ANTHROPIC_BASE_URL), which is what pkg/agent/codex's own
// TestLiveCodexResumesAThreadAcrossExecs does with the responses API, for
// the same reason and with the same shape. Everything about resuming that
// lives on this side of the network is then measurable here:
//
//   - whether `--resume <session-id>` composes with the argv a grain
//     dispatch builds -- `-p --tools '' --mcp-config <file>
//     --strict-mcp-config --allowedTools ... --output-format stream-json
//     --verbose --model ...`, prompt on stdin. A vendor is free to change
//     that in a release nobody here reads, and every other test in this
//     package drives a scripted runner that answers whatever the test
//     told it to.
//   - whether the resumed exec is *sent the earlier conversation*, which
//     is the whole point of resuming rather than restarting and is
//     invisible from claude's own stream: a session that came back empty
//     looks exactly like one that came back whole. The stub records
//     request bodies, so this is read rather than inferred -- and a real
//     credential would prove it less well, not better.
//   - what claude does with a session it does not have, which is the
//     premise retry.go's fallback is keyed on.
//
// What they cannot answer, and what the credentialed probe next door is
// still for: whether Anthropic's own side of a resumed session survives
// anything. claude 2.1 replays the conversation out of its own on-disk
// session store, so on that build there is nothing server-side to
// survive; if that ever changes, these tests go on passing while a
// deployment stops resuming.
//
// By hand, wherever a claude binary is available -- no key, no network:
//
//	GRAIN_LIVE_AGENT_TEST=1 go test ./pkg/agent/claude/ \
//	  -run 'TestLiveClaudeResume|TestLiveClaudeRefuses' -count=1 -v
//
// docs/hand-runs/2026-09-08-claude-resume.md is the run these were
// written from, against claude-code 2.1.197.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
)

// TestLiveClaudeResumeReplaysTheEarlierConversation is the measurement
// retry.go's resume rests on: two execs of a real claude, the second
// passing --resume, with the model behind a stub so what the second exec
// *sent* can be read.
//
// The markers are what make it an assertion rather than a hope. Only the
// first exec sends one; only the second sends the other; a request body
// carrying both is a conversation that was replayed, and one carrying
// only the second is a session that kept its name and lost its contents
// -- which would buy a grain retry nothing at all over starting again.
func TestLiveClaudeResumeReplaysTheEarlierConversation(t *testing.T) {
	claudePath := liveClaudeBinary(t)
	api := newStubMessagesAPI(t)
	env := stubClaudeEnv(t, api.URL)
	configPath := liveMCPConfig(t)

	const first = "GRAIN-RESUME-PROBE-FIRST-TURN"
	one := runStubbedClaude(t, claudePath, configPath, env, nil, first)
	if one.err != nil {
		t.Fatalf("the first exec failed (%v).\n"+
			"Nothing about resuming has been measured yet; read the capture for what went wrong "+
			"between this package's argv and this claude:\n%s", one.err, one.capture)
	}
	if one.sessionID == "" {
		t.Fatalf("claude's stream carried no session_id, so there is nothing for a retry to resume by.\n"+
			"retry.go reads it off every event of a stream-json run; this capture had none of it:\n%s", one.capture)
	}
	t.Logf("first exec: session %s, answer %q", one.sessionID, one.answer)

	const second = "GRAIN-RESUME-PROBE-SECOND-TURN"
	two := runStubbedClaude(t, claudePath, configPath, env, []string{"--resume", one.sessionID}, second)
	switch {
	case two.err != nil && !two.sawInit:
		// The failure retry.go's fallback is written for. Said plainly,
		// because the answer is "stop passing the flag" rather than
		// "look at this run".
		t.Fatalf("claude refused the resumed invocation and opened no session (%v).\n"+
			"--resume does not compose with the rest of a grain run's argv on this build. The fallback in "+
			"retry.go keeps a deployment working -- an attempt like this one is retried from scratch in the "+
			"same sandbox -- but the flag should then be removed rather than left to cost every retry an "+
			"extra process start. Capture:\n%s", two.err, two.capture)
	case two.err != nil:
		t.Fatalf("the resumed exec opened a session and then failed (%v).\n"+
			"That is not the argv being refused -- read the capture for what it was:\n%s", two.err, two.capture)
	}
	t.Logf("second exec: session %s, answer %q", two.sessionID, two.answer)

	// Not a failure. A resumed run may continue under the id it was
	// pointed at (claude 2.1.197 does) or fork under a new one; either is
	// fine, because retry.go resumes whichever id the *last* attempt
	// reported. Logged because a change in the answer is worth seeing in
	// the nightly's output.
	if two.sessionID != one.sessionID {
		t.Logf("the resumed exec reported session %s rather than %s: this build forks a resumed session "+
			"under a new id, which retry.go already follows (it resumes the last id its capture named)",
			two.sessionID, one.sessionID)
	}

	bodies := api.bodies()
	if len(bodies) < 2 {
		t.Fatalf("the stub saw %d completion request(s), want at least one per exec", len(bodies))
	}
	last := bodies[len(bodies)-1]
	if !strings.Contains(last, first) {
		t.Errorf("the resumed exec's request does not carry the first exec's own prompt (%s), so "+
			"resuming keeps the session's name and not its contents -- which is the whole saving "+
			"retry.go is after.\nrequest:\n%s", first, summarizeMessages(last))
	}
	if !strings.Contains(last, second) {
		t.Errorf("the resumed exec's request does not carry the prompt it was given on stdin (%s): "+
			"claude read no new turn from stdin, so a resumed attempt would send the model nothing "+
			"to do.\nrequest:\n%s", second, summarizeMessages(last))
	}
}

// TestLiveClaudeResumesASessionTheProviderRefused narrows the probe above
// to the only case grain ever resumes from, which is not the happy one: a
// first attempt whose turn the provider refused mid-flight.
//
// It is worth its own test because the two are not the same claim. A
// session whose turn ended in an Anthropic 529 has no assistant answer in
// it, and a build that only wrote its session to disk on a completed turn
// -- or that recorded the failure and not the prompt -- would pass the
// test above and lose every retry's context in production. What is
// checked here is the thing retry.go actually needs: the refused
// attempt still names a session, that session is still resumable, and the
// prompt the refused turn carried is replayed to the model.
func TestLiveClaudeResumesASessionTheProviderRefused(t *testing.T) {
	claudePath := liveClaudeBinary(t)
	api := newStubMessagesAPI(t)
	// CLAUDE_CODE_MAX_RETRIES=0 on top of the usual stub environment.
	// claude retries a 529 ten times by itself, backing off to about
	// three minutes in total before it reports the failure grain reads
	// (measured: docs/hand-runs/2026-09-08-claude-resume.md), and what is
	// under test here is the session it leaves behind, not its backoff.
	env := append(stubClaudeEnv(t, api.URL), "CLAUDE_CODE_MAX_RETRIES=0")
	configPath := liveMCPConfig(t)

	// An Anthropic overloaded_error, which is one of the refusals
	// capacity.go matches and the reason this retry loop exists.
	api.refuse(true)
	const first = "GRAIN-RESUME-PROBE-REFUSED-TURN"
	one := runStubbedClaude(t, claudePath, configPath, env, nil, first)
	t.Logf("refused exec: session %s, err %v, answer %q", one.sessionID, one.err, one.answer)
	if one.sessionID == "" {
		t.Fatalf("the exec the provider refused named no session, so retry.go has nothing to resume "+
			"and every refusal costs the run its context. Capture:\n%s", one.capture)
	}

	api.refuse(false)
	api.forget()
	const second = "GRAIN-RESUME-PROBE-RETRY-TURN"
	two := runStubbedClaude(t, claudePath, configPath, env, []string{"--resume", one.sessionID}, second)
	if two.err != nil {
		t.Fatalf("resuming the session a refused attempt left failed (%v, init %v).\n"+
			"This is the exact invocation retry.go makes after a provider refusal; on this build it "+
			"buys nothing, and the fallback (retry from scratch in the same sandbox) is what carries "+
			"the deployment. Capture:\n%s", two.err, two.sawInit, two.capture)
	}
	bodies := api.bodies()
	if len(bodies) == 0 {
		t.Fatalf("the resumed exec sent the stub no completion request at all. Capture:\n%s", two.capture)
	}
	if got := bodies[0]; !strings.Contains(got, first) {
		t.Errorf("the resumed exec's request does not carry the refused turn's prompt (%s).\n"+
			"claude keeps the session but not the turn the provider refused, so a grain retry resumes "+
			"a conversation that was never told what the task is -- and retry.go sends only the short "+
			"'carry on' nudge (agent.ResumePrompt), not the prompt again.\nrequest:\n%s",
			first, summarizeMessages(got))
	}
}

// TestLiveClaudeRetryResumesTheRunAfterARefusal is the one probe here
// that drives none of the argv itself: it calls Framework.Run, refuses
// the first attempt's turn from the stub, and reads what the *second*
// attempt -- the one retry.go builds -- sent the model.
//
// So it measures the production path end to end against a real claude,
// which is more than the sum of the probes above: that capacity.go reads
// claude's own "API Error: 529 Overloaded" as a refusal at all, that the
// loop then execs claude again with --resume and the session id it
// parsed off the first attempt's stream, that the argv it assembles is
// one this claude performs, that the resumed attempt is sent
// agent.ResumePrompt rather than the task again -- and that the model on
// the other end is given the conversation the first attempt started. Any
// one of those breaking makes the retry buy nothing, and none of them is
// visible from a scripted runner.
//
// It costs no credential and about two claude startups: the stub refuses
// the first completion request and answers the second, and the retry
// policy's backoff is stubbed out so the probe does not sit through 30
// seconds of it.
func TestLiveClaudeRetryResumesTheRunAfterARefusal(t *testing.T) {
	claudePath := liveClaudeBinary(t)
	api := newStubMessagesAPI(t)
	// Built before HOME is moved, since `go build` keeps its module cache
	// under it: a build run against the test's own HOME re-downloads the
	// module graph and then leaves a read-only cache t.TempDir cannot
	// clean up.
	grainBinaryPath := buildGrainBinary(t)
	// Framework.Run builds its own environment and adds it to this
	// process's, so this is where a stubbed model and a private session
	// store have to be said. CLAUDE_CODE_MAX_RETRIES=0 is the one entry
	// that is here for the test's sake rather than the stub's: claude
	// retries a 529 ten times on its own, with a backoff that runs to
	// about three minutes (docs/hand-runs/2026-09-08-claude-resume.md),
	// and what is under test is what grain does *after* claude has given
	// up.
	for _, kv := range append(stubClaudeEnv(t, api.URL), "CLAUDE_CODE_MAX_RETRIES=0") {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	api.refuseFirstRequest()

	f := New(claudePath, grainBinaryPath)
	// The backoff itself is agent/retry.go's to test; waiting it out here
	// would add 30 seconds to a nightly for nothing.
	f.retry.Sleep = func(context.Context, time.Duration) error { return nil }

	// Longer than the two execs need, and deliberately longer than
	// agent.MinimumRetryRuntime plus the first backoff: a run whose
	// deadline is closer than that is one the policy declines to retry at
	// all, and this probe would then measure the decline.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	const marker = "GRAIN-RETRY-PROBE-TASK-PROMPT"
	result, err := f.Run(ctx, agent.RunConfig{
		Prompt:      "Reply with nothing but the word ok. " + marker,
		SandboxRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Logf("run finished: %q", result.FinalText)

	bodies := api.bodies()
	if len(bodies) < 2 {
		t.Fatalf("the stub saw %d completion request(s), want two: the attempt it refused and the "+
			"retry.\nEither capacity.go no longer reads claude's own words for a 529 as a refusal "+
			"(so nothing was retried), or the retry never reached the model.\ntranscript:\n%s",
			len(bodies), result.Transcript)
	}
	last := bodies[len(bodies)-1]
	switch {
	case !strings.Contains(last, marker):
		t.Errorf("the retried attempt's request does not carry the task's own prompt (%s), so the "+
			"resume kept the session's name and not its contents.\nrequest:\n%s", marker, summarizeMessages(last))
	case !strings.Contains(last, resumePromptMarker):
		// Not necessarily broken -- this is what the fallback looks like
		// -- but it means the run paid for a resume and got a restart.
		t.Errorf("the retried attempt was not sent agent.ResumePrompt.\n"+
			"If the transcript carries resumeRefusedNote then claude refused the resumed argv and this "+
			"package fell back to restarting the task, which is the fallback working and the resume "+
			"buying nothing.\ntranscript:\n%s\nrequest:\n%s", result.Transcript, summarizeMessages(last))
	}
}

// TestLiveClaudeCarriesAProposeTaskCallThroughGrainsMCPServer is the
// smoke run a Claude Code CLI bump owes (docs/test-plans/2026-10-01.md
// §8): Framework.Run against a real claude, with the model behind the
// stub, which answers the first turn with one call to
// mcp__grain-sandbox__propose_task and the second with text.
//
// What it measures is everything between those two answers that a
// scripted runner cannot see: that claude still loads grain's
// --mcp-config under --strict-mcp-config, that --allowedTools still lets
// an MCP tool through with --tools ” emptying the native roster, that
// the call reaches `grain mcpserver` and its answer comes back to the
// model as a tool_result, and that transcript.go records the call under
// its bare name with the arguments the model sent -- which is what
// orchestrator.ProcessResult relays a proposal from.
func TestLiveClaudeCarriesAProposeTaskCallThroughGrainsMCPServer(t *testing.T) {
	claudePath := liveClaudeBinary(t)
	api := newStubMessagesAPI(t)
	grainBinaryPath := buildGrainBinary(t)
	for _, kv := range stubClaudeEnv(t, api.URL) {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	dependsOn := []any{"grain/task-1", "#2", "https://grain.example/tasks/3"}
	api.firstToolUse = &stubToolUse{
		name: "mcp__" + mcpServerName + "__propose_task",
		input: map[string]any{
			"title":      "GRAIN-MCP-PROBE proposal",
			"body":       "filed by a stubbed model",
			"depends_on": dependsOn,
			"supersedes": "grain/task-4",
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	result, err := New(claudePath, grainBinaryPath).Run(ctx, agent.RunConfig{
		Prompt:      "Propose the follow-up task, then reply ok.",
		SandboxRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var call *agent.ToolCall
	for i := range result.ToolCalls {
		if result.ToolCalls[i].Name == "propose_task" {
			call = &result.ToolCalls[i]
		}
	}
	if call == nil {
		t.Fatalf("the run recorded no propose_task call (calls: %v): claude did not carry the "+
			"stub's tool_use through grain's MCP server.\ntranscript:\n%s", result.ToolCalls, result.Transcript)
	}
	got, _ := json.Marshal(call.Arguments["depends_on"])
	want, _ := json.Marshal(dependsOn)
	if string(got) != string(want) || call.Arguments["supersedes"] != "grain/task-4" {
		t.Errorf("propose_task was recorded with %v, want depends_on %s and supersedes grain/task-4 "+
			"exactly as the model sent them", call.Arguments, want)
	}

	bodies := api.bodies()
	if len(bodies) < 2 {
		t.Fatalf("the stub saw %d completion request(s), want the tool call and the turn after it", len(bodies))
	}
	if last := bodies[len(bodies)-1]; !strings.Contains(last, "tool_result") ||
		!strings.Contains(last, "proposed task(s) so far this run") {
		t.Errorf("the turn after the call does not carry grain mcpserver's answer to it as a "+
			"tool_result.\nrequest:\n%s", summarizeMessages(last))
	}
}

// resumePromptMarker is a phrase from agent.ResumePrompt distinctive
// enough to find in a request body, taken from the constant rather than
// written out so a reworded prompt cannot make the assertion above pass
// by matching nothing.
var resumePromptMarker = strings.SplitN(agent.ResumePrompt, ":", 2)[0]

// TestLiveClaudeRefusesASessionItDoesNotHave measures the premise
// retry.go's fallback is keyed on, and costs nothing at all: that claude
// asked to resume a session it has never heard of fails *before* it opens
// one, so a capture with no "system"/"init" event really is the signature
// of a resume that did not take.
//
// It matters because the two ways that could go wrong are opposite faults
// and neither is visible from here:
//
//   - claude fails *after* writing an init event. The fallback never
//     fires, and a run whose session went missing ends on "no such
//     session" instead of retrying in place -- a provider blip turned
//     into a hard failure, which is the one thing the retry exists to
//     prevent.
//   - claude quietly starts a *fresh* session instead of failing. Worse:
//     the resumed attempt sends agent.ResumePrompt -- "you were
//     interrupted, carry on" -- into a conversation that has never been
//     told what the task is, and the run spends its remaining budget on
//     an agent with no idea what it is meant to do. This package would
//     then have to send the whole prompt on every resumed attempt, or
//     verify the session before trusting it.
//
// The stub is here to catch the second of those rather than to answer
// anything: on the build this expects, claude never reaches the API at
// all, and the assertion that it sent no request is what says so.
func TestLiveClaudeRefusesASessionItDoesNotHave(t *testing.T) {
	claudePath := liveClaudeBinary(t)
	api := newStubMessagesAPI(t)
	env := stubClaudeEnv(t, api.URL)
	configPath := liveMCPConfig(t)

	// A well-formed session id claude cannot possibly hold: the shape its
	// own stream reports, with random bytes in it.
	missing := "00000000-0000-4000-8000-" + strings.TrimPrefix(liveNonce(t), "grain-")
	run := runStubbedClaude(t, claudePath, configPath, env, []string{"--resume", missing},
		"Reply with nothing but the word ok.")
	t.Logf("resuming %s: session %s, init %v, answer %q (err %v)", missing, run.sessionID, run.sawInit, run.answer, run.err)

	switch {
	case run.err == nil:
		t.Fatalf("claude accepted a session id it has never seen and ran anyway (answer %q).\n"+
			"retry.go's resumed attempt sends the short 'carry on' nudge rather than the task, so on this "+
			"build a run whose session went missing would spend its remaining budget on an agent that was "+
			"never told what to do. Capture:\n%s", run.answer, run.capture)
	case run.sawInit:
		t.Fatalf("claude opened a session (init event present) and then failed on a resume it cannot serve.\n"+
			"retry.go reads 'failed with no init event' as the argv being refused, so on this build that "+
			"fallback never fires and a transient refusal becomes a hard failure. Capture:\n%s", run.capture)
	}
	if n := len(api.bodies()); n != 0 {
		t.Errorf("claude sent the model %d request(s) for a session it does not have: it started a "+
			"conversation of some kind rather than refusing outright, which is what retry.go's "+
			"'no init event' test assumes it does not do", n)
	}
}

// liveClaudeBinary is the one thing every probe in this file needs and
// the only thing: no credential, no network.
func liveClaudeBinary(t *testing.T) string {
	t.Helper()
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		skipUnlessLiveRequired(t, "no claude binary on $PATH")
	}
	return claudePath
}

// liveMCPConfig writes the --mcp-config a dispatch gets, with the same
// function that writes one for a real run: the flags under test are the
// ones a run passes alongside --resume, and a probe that dropped the MCP
// half of the argv would answer an easier question than the one being
// asked. Nothing here calls a tool, but the server is loaded, which is
// the part that has to compose.
func liveMCPConfig(t *testing.T) string {
	t.Helper()
	configPath, err := writeMCPConfig(context.Background(), agent.LocalHost{}, buildGrainBinary(t), []string{"mcpserver", "-sandbox-root", t.TempDir()})
	if err != nil {
		t.Fatalf("writing the --mcp-config a run gets: %v", err)
	}
	t.Cleanup(func() { os.Remove(configPath) })
	return configPath
}

// stubClaudeEnv is what points a real claude at the stub instead of
// Anthropic, and what keeps the probe off the network entirely.
//
// HOME is the one deliberate departure from what a dispatch does.
// Production gives claude the controller's own environment -- which is
// why a retry finds the session at all, since the store is keyed on that
// home and the working directory (retry.go's header) -- but a probe that
// used the developer's or the runner's home would be measuring a session
// store somebody else has been writing to. A private one per test is what
// makes "the second exec found the first exec's session" a fact about
// these two execs.
func stubClaudeEnv(t *testing.T, baseURL string) []string {
	t.Helper()
	return []string{
		"HOME=" + t.TempDir(),
		"ANTHROPIC_BASE_URL=" + baseURL,
		// Not a credential and not treated as one by anything: it exists
		// because claude declines to start without some key, and the
		// stub never looks at it.
		"ANTHROPIC_API_KEY=stub-key-not-a-real-credential",
		// Everything claude would otherwise send somewhere that is not
		// the stub. A probe that runs in a job with no secret must not
		// reach Anthropic at all, or "the stub saw no request" above
		// stops meaning what it says.
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"DISABLE_TELEMETRY=1",
		"DISABLE_ERROR_REPORTING=1",
		"DISABLE_AUTOUPDATER=1",
		"DISABLE_BUG_COMMAND=1",
		"DISABLE_NON_ESSENTIAL_MODEL_CALLS=1",
	}
}

// runStubbedClaude execs claude once with the argv a grain dispatch uses
// -- spelled out here rather than borrowed from Framework.Run, which
// builds it for a whole run and would hide the one flag under test behind
// its own decisions -- and reads the stream back the way retry.go does.
//
// The prompt goes on stdin, which is both the shape Run uses (untrusted
// text must never become a ps-visible argument) and half the reason these
// tests exist: --resume is documented against print mode, and a session
// picked up with a fresh prompt arriving on stdin is a narrower claim
// than that.
func runStubbedClaude(t *testing.T, claudePath, configPath string, env, extra []string, prompt string) liveClaudeRun {
	t.Helper()

	args := []string{
		"-p",
		"--tools", "",
		"--mcp-config", configPath,
		"--strict-mcp-config",
		"--allowedTools", strings.Join(allowedTools(), ","),
		"--output-format", "stream-json",
		"--verbose",
	}
	args = append(args, extra...)

	// No --max-turns: a dispatch passes none by default (defaultMaxTurns),
	// and the bound that keeps a probe from running away is the context,
	// as it is for the other live tests in this package. Generous, because
	// one of these execs is answered with a 529 and claude backs off and
	// retries the API itself before giving up on the turn.
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// hostRunner rather than a bare exec.Command, so what is measured is
	// the subprocess contract this package actually runs claude under --
	// the environment it adds to, the process group it puts the CLI in,
	// and stdout kept alongside a non-zero exit.
	stdout, err := hostRunner{host: agent.LocalHost{}, claudePath: claudePath}.Run(ctx, args, prompt, env, nil)

	p := parseEvents(stdout)
	answer := p.result.FinalText
	if answer == "" {
		answer = p.resultText
	}
	return liveClaudeRun{
		capture:   stdout,
		sessionID: p.sessionID,
		sawInit:   p.sawInit,
		answer:    answer,
		err:       err,
	}
}

// stubMessagesAPI is a stand-in for Anthropic's /v1/messages endpoint: it
// records every completion request body and answers each one with the
// shortest complete turn -- one text block and a stop -- or, when told
// to, with the overloaded_error a refused run gets.
//
// It exists so the probes above can read what claude *sent*, which is the
// only place the answer to "was the earlier conversation replayed?" is
// written down. Nothing here models the API beyond what claude 2.1 needs
// to finish a turn; a future claude that wants more fails the first exec,
// loudly, which is the right way for this to stop working.
type stubMessagesAPI struct {
	*httptest.Server
	mu sync.Mutex
	// requests holds one entry per completion request, in order.
	requests []string
	// refusing refuses every turn; refuseFirst refuses only the first,
	// which is how a run gets exactly one refused attempt and one that
	// works.
	refusing    bool
	refuseFirst bool
	otherPath   []string
	// firstToolUse, when set, answers the first completion request with
	// a call to this tool instead of text, so a probe can watch a real
	// claude carry one tool call through grain's MCP server and back.
	firstToolUse *stubToolUse
}

// stubToolUse is the one tool call the stub's scripted model makes.
type stubToolUse struct {
	name  string
	input map[string]any
}

func newStubMessagesAPI(t *testing.T) *stubMessagesAPI {
	t.Helper()
	s := &stubMessagesAPI{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		switch {
		case r.URL.Path == "/v1/messages/count_tokens":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"input_tokens":100}`)
			return
		case r.URL.Path != "/v1/messages":
			// Anything else claude asks the base URL for -- the
			// reachability check it opens with, endpoints a later
			// version may add. Recorded rather than served, so a probe
			// that starts failing has the paths in its output.
			s.mu.Lock()
			s.otherPath = append(s.otherPath, r.Method+" "+r.URL.Path)
			s.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{}`)
			return
		}

		s.mu.Lock()
		s.requests = append(s.requests, string(body))
		refusing := s.refusing || (s.refuseFirst && len(s.requests) == 1)
		toolUse := s.firstToolUse
		if len(s.requests) != 1 {
			toolUse = nil
		}
		s.mu.Unlock()

		if refusing {
			w.Header().Set("Content-Type", "application/json")
			// 529 overloaded_error, which net/http has no constant for
			// and Anthropic sends for exactly this.
			w.WriteHeader(529)
			io.WriteString(w, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
			return
		}

		type event struct {
			name string
			data map[string]any
		}
		block := []event{
			{"content_block_start", map[string]any{"type": "content_block_start", "index": 0,
				"content_block": map[string]any{"type": "text", "text": ""}}},
			{"content_block_delta", map[string]any{"type": "content_block_delta", "index": 0,
				"delta": map[string]any{"type": "text_delta", "text": "ok"}}},
		}
		stop := "end_turn"
		if toolUse != nil {
			input, err := json.Marshal(toolUse.input)
			if err != nil {
				t.Errorf("marshalling the stub's tool input: %v", err)
				return
			}
			block = []event{
				{"content_block_start", map[string]any{"type": "content_block_start", "index": 0,
					"content_block": map[string]any{"type": "tool_use", "id": "toolu_stub_1",
						"name": toolUse.name, "input": map[string]any{}}}},
				{"content_block_delta", map[string]any{"type": "content_block_delta", "index": 0,
					"delta": map[string]any{"type": "input_json_delta", "partial_json": string(input)}}},
			}
			stop = "tool_use"
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		events := []event{
			{"message_start", map[string]any{"type": "message_start", "message": map[string]any{
				"id": "msg_stub", "type": "message", "role": "assistant", "model": "claude-stub",
				"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
				"usage": map[string]any{"input_tokens": 10, "output_tokens": 1},
			}}},
		}
		events = append(events, block...)
		events = append(events,
			event{"content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}},
			event{"message_delta", map[string]any{"type": "message_delta",
				"delta": map[string]any{"stop_reason": stop, "stop_sequence": nil},
				"usage": map[string]any{"output_tokens": 1}}},
			event{"message_stop", map[string]any{"type": "message_stop"}},
		)
		for _, ev := range events {
			data, err := json.Marshal(ev.data)
			if err != nil {
				t.Errorf("marshalling the stub's %s event: %v", ev.name, err)
				return
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.name, data)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// refuse switches the stub between answering turns and refusing them the
// way an overloaded Anthropic does.
func (s *stubMessagesAPI) refuse(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refusing = on
}

// refuseFirstRequest refuses one turn and answers everything after it:
// one refused attempt, one retry that works, which is the shape a
// transient provider refusal has.
func (s *stubMessagesAPI) refuseFirstRequest() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refuseFirst = true
}

// forget drops the requests recorded so far, so a test can ask what one
// exec sent rather than what every exec did.
func (s *stubMessagesAPI) forget() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = nil
}

func (s *stubMessagesAPI) bodies() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

// summarizeMessages renders a /v1/messages body as the conversation it
// carries, since that -- and not the 7KB of tool schemas around it -- is
// what a failure here is about. Falls back to the raw body if it is not
// the shape this expects, which is itself worth seeing.
func summarizeMessages(body string) string {
	var v struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &v); err != nil || len(v.Messages) == 0 {
		return truncate(body, 2000)
	}
	var b strings.Builder
	for _, m := range v.Messages {
		fmt.Fprintf(&b, "  %s: %s\n", m.Role, truncate(string(m.Content), 400))
	}
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "... (truncated)"
}
