package codex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/agent"
)

// adoptedStream is a codex run's whole `codex exec --json` stream as
// grain hostd spools it: the "type" spelling of a real capture
// (TestParseTranscriptReadsTheItemKindUnderEitherFieldName), a thread,
// two tool calls and a completed turn.
func adoptedStream(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "exec-json-adopted.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// An adopting daemon reads a codex run it never watched from the stream
// hostd spooled (agent.ResultParser), and comes to what Run would have:
// the same final text, the same tool calls, and the thread a later
// dispatch may resume. Without this an adopted codex run cannot be read,
// and orchestrator.ResumeAdoptedRun finishes it as interrupted.
func TestParseResultReadsARecordedStreamAsRunWould(t *testing.T) {
	stream := adoptedStream(t)
	f := newTestFramework(&attemptRunner{replies: []attemptReply{{stdout: stream}}}, "/usr/local/bin/grain", withoutRetries())
	ran, runErr := f.Run(context.Background(), agent.RunConfig{Prompt: "do the thing", SandboxRoot: t.TempDir()})
	parsed, err := f.ParseResult(stream, nil)
	if err != nil || runErr != nil {
		t.Fatalf("ParseResult err = %v, Run err = %v; want both clean", err, runErr)
	}
	if parsed.FinalText != "pushed the branch" || parsed.FinalText != ran.FinalText {
		t.Errorf("FinalText = %q, Run's = %q; want the stream's last message from both", parsed.FinalText, ran.FinalText)
	}
	if len(parsed.ToolCalls) != 2 || parsed.ToolCalls[1].Name != "comment_on_issue" || len(ran.ToolCalls) != len(parsed.ToolCalls) {
		t.Errorf("ToolCalls = %+v, Run's = %+v; want both calls under their bare names", parsed.ToolCalls, ran.ToolCalls)
	}
	if parsed.ResumableSession != "01a066db-4912-79b0-8254-ff2d2927a870" {
		t.Errorf("ResumableSession = %q, want the thread the stream opened", parsed.ResumableSession)
	}
	if parsed.Transcript != ran.Transcript {
		t.Errorf("transcript differs from Run's:\nParseResult: %q\nRun: %q", parsed.Transcript, ran.Transcript)
	}

	// A run hostd reports killed comes back as a failure, with whatever it
	// did before it was stopped.
	cut := strings.Join(strings.SplitAfter(stream, "\n")[:5], "")
	partial, err := f.ParseResult(cut, errors.New("signal: killed"))
	if err == nil || partial == nil || len(partial.ToolCalls) != 1 {
		t.Fatalf("ParseResult(killed) = %+v, %v; want the partial run beside an error", partial, err)
	}
}

// A run adopted after it had already retried is read whole, and leaves
// resumable the last thread any attempt named.
func TestParseAttemptsReadsTheWholeCodexRun(t *testing.T) {
	f := newTestFramework(&attemptRunner{}, "/usr/local/bin/grain", withoutBackoff())
	earlier := attemptStream(agentMessage("first try"), overloadedTurn)
	parsed, err := f.ParseAttempts(earlier, `{"type":"turn.started"}`+"\n"+agentMessage("second try")+"\n"+`{"type":"turn.completed"}`+"\n", nil)
	if err != nil || parsed == nil {
		t.Fatalf("ParseAttempts = %+v, %v; want the run's result", parsed, err)
	}
	if !strings.Contains(parsed.Transcript, "first try") || !strings.Contains(parsed.Transcript, "second try") {
		t.Errorf("transcript = %q, want both attempts", parsed.Transcript)
	}
	if parsed.ResumableSession != "t1" {
		t.Errorf("ResumableSession = %q, want the earlier attempt's thread", parsed.ResumableSession)
	}
}

// A codex run the daemon detached from as it stopped is still running in
// grain hostd: the exec is neither retried -- not even as a refused
// resume of a carried thread, which an empty stream would otherwise read
// as -- nor parsed, Run hands back agent.ErrDetached for the orchestrator
// to leave the run live, and the CODEX_HOME codex is running against,
// config and login link included, is left where it is.
func TestADetachedCodexRunIsLeftAlone(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: "", err: fmt.Errorf("hostd: %w", agent.ErrDetached)},
	}}
	f := newTestFramework(r, "/usr/local/bin/grain", withoutBackoff())
	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "do the thing", SandboxRoot: t.TempDir(),
		ResumeSession: "thread-carried"})
	if !errors.Is(err, agent.ErrDetached) || result != nil {
		t.Fatalf("Run = %+v, %v; want nil and agent.ErrDetached", result, err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("codex was exec'd %d time(s), want the one the daemon detached from", len(r.calls))
	}
	home := r.calls[0].dir
	t.Cleanup(func() { os.RemoveAll(home) })
	if _, err := os.Stat(filepath.Join(home, configFileName)); err != nil {
		t.Errorf("the detached run's %s is gone (%v): codex is still reading it in grain hostd", configFileName, err)
	}

	// The one that is not detached cleans up as before.
	r = &attemptRunner{replies: []attemptReply{{stdout: attemptStream(agentMessage("done"), `{"type":"turn.completed"}`)}}}
	f = newTestFramework(r, "/usr/local/bin/grain", withoutBackoff())
	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "do the thing", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.calls[0].dir); !os.IsNotExist(err) {
		t.Errorf("a finished run's CODEX_HOME is still there (%v)", err)
	}
}
