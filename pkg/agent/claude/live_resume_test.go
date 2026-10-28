package claude

// The measurement retry.go's resume is built on, and the one thing about
// it nothing else in this repository could establish.
//
// Framework.Run execs claude a second time when the provider refuses a
// run mid-flight, passing --resume <session-id> so the retry picks up the
// session the first exec left rather than starting the task again. That
// the flag exists is documented; that it composes with the argv a grain
// run actually uses -- `claude -p --tools '' --mcp-config <file>
// --strict-mcp-config --allowedTools ... --output-format stream-json
// --verbose`, with the prompt on stdin and no session on stdin's side to
// speak of -- is not, and it is exactly the kind of thing a vendor is
// free to change in a release nobody here reads. Every other test in this
// package drives a scripted runner, which answers whatever the test told
// it to. So the answer has to come from the real binary, which is what
// this file asks for.
//
// It costs three short model turns and no sandbox work: what is under
// test is claude's session handling, not an agent doing a task, so the
// run is given a nonce to remember and asked for it back. The third turn
// is the control, and it is not optional -- a second exec that knows the
// nonce proves nothing unless a third exec that was *not* told to resume
// does not know it.
//
// The stream is read with this package's own parseEvents rather than a
// reader of the test's own, deliberately: what production reads off a
// resumed run (the session id, and the init event its fallback turns on)
// is then measured against a real capture instead of against a fixture
// this repository wrote.
//
// # What this adds to the credential-free probes next door
//
// live_resume_stub_test.go asks most of the same question of the same
// binary with the model replaced by a stub in the test process, which is
// what makes it runnable on any machine with a claude on it -- and it is
// where the other half of the argument now lives (what claude does with a
// session it does not have, and whether a session the provider refused is
// still resumable). Those measure everything about resuming that happens
// on this side of the network, which on claude 2.1 is all of it: the
// conversation is replayed out of claude's own on-disk session store.
//
// This test is what would notice that changing. It is the only probe here
// that puts a real Anthropic behind the resumed session, so a build that
// moved the session server-side -- or a server that stopped honouring an
// id claude still holds -- fails here and passes there.
//
// # What a failure here means
//
// (A red run from this test, that is. A live-frameworks.yml run that
// fails before it reaches any of this measures nothing: as of 2026-09-08
// no CLAUDE_CODE_OAUTH_TOKEN has ever reached that workflow's live-claude
// job, so this file has never executed against a real claude at all. See
// retry.go's header, and the README's live-nightly section for what a
// maintainer has to create.)
//
// retry.go already degrades safely -- an exec that passes --resume and
// dies without claude opening a session is read as the argv being
// refused, and the run retries from scratch in the same sandbox instead.
// So a red run here is not a broken deployment; it is grain paying for a
// process start it will never get anything back for, on every transient
// refusal. The fix is to stop passing the flag and keep the cheap
// variant, which is a three-line change in attemptArgs.
//
// By hand, wherever a claude and a credential are both available:
//
//	GRAIN_LIVE_AGENT_TEST=1 CLAUDE_CODE_OAUTH_TOKEN=... \
//	  go test ./pkg/agent/claude/ -run TestLiveClaudeResumes -count=1 -v

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
)

func TestLiveClaudeResumesASessionAcrossExecs(t *testing.T) {
	token := os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")
	if token == "" {
		skipUnlessLiveRequired(t, "CLAUDE_CODE_OAUTH_TOKEN is not set")
	}
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		skipUnlessLiveRequired(t, "no claude binary on $PATH")
	}

	// The same --mcp-config a dispatch gets, written by the same
	// function: the flags under test are the ones a run passes alongside
	// --resume, and a probe that dropped the MCP half of the argv would
	// answer an easier question than the one being asked. Nothing is
	// expected to call a tool -- each turn is told not to -- but the
	// server is loaded, which is the part that has to compose.
	configPath, err := writeMCPConfig(context.Background(), agent.LocalHost{}, buildGrainBinary(t), []string{"mcpserver", "-sandbox-root", t.TempDir()})
	if err != nil {
		t.Fatalf("writing the --mcp-config a run gets: %v", err)
	}
	t.Cleanup(func() { os.Remove(configPath) })

	nonce := liveNonce(t)

	// Turn one: the session this test is going to try to get back.
	first := runLiveClaude(t, claudePath, configPath, token, nil,
		"Remember this token, exactly as written: "+nonce+
			". Do not use any tools. Reply with nothing but the word ok.")
	if first.sessionID == "" {
		t.Fatalf("claude's stream carried no session_id, so there is nothing to resume by.\n"+
			"retry.go reads it off every event of a stream-json run; this capture had none of it:\n%s",
			first.capture)
	}
	t.Logf("first exec: session %s, answer %q (err %v)", first.sessionID, first.answer, first.err)

	// Turn two: the whole question. Same argv, plus --resume, with a
	// fresh prompt on stdin.
	second := runLiveClaude(t, claudePath, configPath, token, []string{"--resume", first.sessionID},
		"What was the token I asked you to remember? Do not use any tools. Reply with nothing but that token.")
	t.Logf("second exec: session %s, answer %q (err %v)", second.sessionID, second.answer, second.err)

	switch {
	case second.err != nil && !second.sawInit:
		t.Fatalf("claude refused the resumed invocation and opened no session (%v).\n"+
			"--resume does not compose with the rest of a grain run's argv on this build, so this package "+
			"should stop passing it and keep the fallback retry.go already has: retry from scratch in the "+
			"same sandbox. Capture:\n%s", second.err, second.capture)
	case second.err != nil:
		t.Fatalf("the resumed exec opened a session and then failed (%v).\n"+
			"That is not the argv being refused -- read the capture for what it was:\n%s", second.err, second.capture)
	}
	if !strings.Contains(strings.ToLower(second.answer), strings.ToLower(nonce)) {
		t.Fatalf("the resumed exec did not know the token %s it had just been given; it answered %q.\n"+
			"claude accepted --resume and started a session that remembers nothing of the first, which buys "+
			"a grain retry nothing over starting again. Capture:\n%s", nonce, second.answer, second.capture)
	}
	// Not an assertion. A resumed run may well continue under the id it
	// was pointed at, and may equally fork the session under a new one;
	// either is fine here, because retry.go resumes whichever id the
	// *last* attempt reported. It is logged because a change in that
	// answer is worth seeing in the nightly's output.
	if second.sessionID != first.sessionID {
		t.Logf("the resumed exec reported session %s rather than %s: this build forks a resumed session "+
			"under a new id, which retry.go already follows (it resumes the last id its capture named)",
			second.sessionID, first.sessionID)
	}

	// The control. Without it, a model that answered with the nonce for
	// any other reason -- something left in the CLI's own state, an echo
	// of a prompt -- would pass this test while proving nothing.
	control := runLiveClaude(t, claudePath, configPath, token, nil,
		"What was the token I asked you to remember? Do not use any tools. Reply with nothing but that token, "+
			"or with the word none if you were never given one.")
	t.Logf("control exec: session %s, answer %q (err %v)", control.sessionID, control.answer, control.err)
	if strings.Contains(strings.ToLower(control.answer), strings.ToLower(nonce)) {
		t.Fatalf("an exec that was never asked to resume anything also knew the token %s (%q).\n"+
			"So the assertion above is not evidence that --resume did anything, and this probe needs "+
			"rebuilding before retry.go can be said to rest on a measurement.", nonce, control.answer)
	}
}

// liveClaudeRun is one exec of claude, read back the way retry.go reads
// it.
type liveClaudeRun struct {
	capture   string
	sessionID string
	sawInit   bool
	answer    string
	err       error
}

// runLiveClaude execs claude once with the argv a grain dispatch uses --
// spelled out here rather than borrowed from Framework.Run, which builds
// it for a whole run and would hide the one flag under test behind its
// own decisions. extra is what this exec adds to it.
//
// The prompt goes on stdin, which is both the shape Run uses (untrusted
// text must never become a ps-visible argument) and half the reason this
// test exists: --resume is documented against print mode, and a session
// picked up with a fresh prompt arriving on stdin is a narrower claim
// than that.
func runLiveClaude(t *testing.T, claudePath, configPath, token string, extra []string, prompt string) liveClaudeRun {
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
	// and the bound that keeps a probe turn from running away is the
	// context, as it is for the other live tests in this package.
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// hostRunner rather than a bare exec.Command, so what is measured is
	// the subprocess contract this package actually runs claude under --
	// the environment it adds to, the process group it puts the CLI in,
	// and stdout kept alongside a non-zero exit.
	stdout, err := hostRunner{host: agent.LocalHost{}, claudePath: claudePath}.Run(ctx, args, prompt,
		[]string{"CLAUDE_CODE_OAUTH_TOKEN=" + token}, nil)

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

// liveNonce is the token the session has to carry across an exec:
// random, so nothing but the first turn could have put it in the second
// one's answer, and short enough that a model asked to repeat it
// verbatim reliably does.
func liveNonce(t *testing.T) string {
	t.Helper()
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return "grain-" + hex.EncodeToString(b[:])
}
