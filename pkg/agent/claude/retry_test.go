package claude

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
)

// attemptRunner is a fake claude that answers a run's successive execs
// from a script, recording what each one was handed.
//
// fakeRunner replays one canned stream however many times it is called,
// which is exactly what this file must not be tested against: the
// property under test is that the second exec differs from the first --
// a different turn budget on the argv, a different prompt on stdin --
// and a runner with one answer could not tell the difference.
//
// It writes its reply a line at a time and checks cancellation between
// lines, so an attempt cancelled mid-flight really does stop where a
// real claude would.
type attemptRunner struct {
	replies []attemptReply
	calls   []attemptCall
}

type attemptReply struct {
	stdout string
	err    error
}

type attemptCall struct {
	args  []string
	stdin string
	env   []string
}

func (r *attemptRunner) Run(ctx context.Context, args []string, stdin string, env []string, tee io.Writer) (string, error) {
	r.calls = append(r.calls, attemptCall{args: slices.Clone(args), stdin: stdin, env: slices.Clone(env)})
	if len(r.calls) > len(r.replies) {
		return "", fmt.Errorf("claude was exec'd %d time(s), and this script has %d reply/replies", len(r.calls), len(r.replies))
	}
	reply := r.replies[len(r.calls)-1]
	var written strings.Builder
	for _, line := range strings.SplitAfter(reply.stdout, "\n") {
		if line == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return written.String(), err
		}
		written.WriteString(line)
		if tee != nil {
			io.WriteString(tee, line)
		}
	}
	return written.String(), reply.err
}

// withoutBackoff installs a sleep that returns at once, so a test of the
// retry loop costs the suite no wall clock. Nothing else about the loop
// changes: the same waits are still asked for, they just do not happen.
func withoutBackoff() Option {
	return func(f *Framework) {
		f.retry.Sleep = func(context.Context, time.Duration) error { return nil }
	}
}

// overloadedResult is how an Anthropic 529 reaches this package: a
// terminal result event with is_error set, carrying the provider's own
// body.
const overloadedResult = `{"type":"result","subtype":"error_during_execution","is_error":true,` +
	`"result":"API Error: 529 {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}"}`

// systemInit is claude's opening event, the first line of every
// --output-format stream-json run, carrying the session id every later
// event repeats. Modelled on a real capture and narrowed to the fields
// this package reads (see live_resume_test.go, which asserts the same
// reading against a real binary rather than against this fixture).
func systemInit(id string) string {
	return `{"type":"system","subtype":"init","session_id":"` + id +
		`","cwd":"/w","tools":[],"model":"claude-sonnet-5","permissionMode":"default"}`
}

func assistantText(text string) string {
	return `{"type":"assistant","message":{"content":[{"type":"text","text":"` + text + `"}]}}`
}

func successResult(text string, turns int) string {
	return fmt.Sprintf(`{"type":"result","result":%q,"num_turns":%d}`, text, turns)
}

func stream(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

// The whole point of retry.go: a run the provider refused mid-flight is
// picked up where it stopped -- the sandbox it is already holding, and
// the session claude named on its own stream -- rather than costing a
// dispatch, a granule VM boot, a fresh clone, everything the agent had
// written but not pushed, and every model turn it had spent.
func TestRunResumesTheSessionAfterATransientRefusal(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(systemInit("sess-7"), assistantText("cloning"), overloadedResult), err: errors.New("exit status 1")},
		{stdout: stream(systemInit("sess-7"), assistantText("pushed the branch"), successResult("done", 2))},
	}}
	f := newFramework(r, "mcpserver-path", withoutBackoff())

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "do the thing", SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("Run err = %v, want the retry to have carried the run", err)
	}
	if len(r.calls) != 2 {
		t.Fatalf("claude was exec'd %d time(s), want 2", len(r.calls))
	}
	if result.FinalText != "done" {
		t.Errorf("FinalText = %q, want the second attempt's answer -- the last result event is what a run ended as", result.FinalText)
	}

	if got := argValue(r.calls[0].args, "--resume"); got != "" {
		t.Errorf("the first attempt asked to resume %q; there was nothing to resume yet", got)
	}
	if got := argValue(r.calls[1].args, "--resume"); got != "sess-7" {
		t.Errorf("the retry resumed session %q, want sess-7 -- the id claude put on its own stream", got)
	}

	// The retry must not re-send the prompt: the session it is joining
	// already has it, and repeating it reads as "start over" to a model
	// that had already got half way.
	if r.calls[0].stdin != "do the thing" {
		t.Errorf("the first attempt's prompt = %q, want the task's prompt alone", r.calls[0].stdin)
	}
	if strings.Contains(r.calls[1].stdin, "do the thing") {
		t.Errorf("the resumed attempt sent the prompt again (%q): the session already has it", r.calls[1].stdin)
	}
	if !strings.Contains(r.calls[1].stdin, "Carry on from where you stopped") {
		t.Errorf("the resumed attempt's turn = %q, want it to say the interruption was not the agent's doing", r.calls[1].stdin)
	}

	// Both attempts are in the record, and the transcript says why it
	// appears to start twice -- and that the second one carried on
	// rather than starting again.
	for _, want := range []string{"cloning", "pushed the branch", "resumed the same conversation in the same sandbox"} {
		if !strings.Contains(result.Transcript, want) {
			t.Errorf("Transcript missing %q:\n%s", want, result.Transcript)
		}
	}
}

// A refusal that arrived before claude ever named a session -- a run
// killed in its first moments, a build that stopped putting session_id
// on its events -- leaves nothing to resume, and the retry falls back to
// the cheap variant: the task from the top, with the paragraph that says
// the sandbox it is starting in is not empty.
func TestRunRestartsFromThePromptWithNoSessionToResume(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(overloadedResult), err: errors.New("exit status 1")},
		{stdout: stream(assistantText("pushed the branch"), successResult("done", 2))},
	}}
	f := newFramework(r, "mcpserver-path", withoutBackoff())

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "do the thing", SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("Run err = %v, want the retry to have carried the run", err)
	}
	if got := argValue(r.calls[1].args, "--resume"); got != "" {
		t.Errorf("the retry asked to resume %q, and no attempt had named a session", got)
	}
	if !strings.Contains(r.calls[1].stdin, "do the thing") {
		t.Errorf("the retry's prompt = %q, want the task's own prompt back", r.calls[1].stdin)
	}
	if !strings.HasPrefix(r.calls[1].stdin, agent.RestartPreamble) {
		t.Errorf("the retry's prompt = %q, want agent.RestartPreamble in front of it", r.calls[1].stdin)
	}
	if !strings.Contains(result.Transcript, "restarted it from the prompt in the same sandbox") {
		t.Errorf("the transcript does not say the run started over rather than resuming:\n%s", result.Transcript)
	}
}

// The belt and braces for the one thing no scripted runner can prove:
// that --resume composes with the argv a grain run uses. An exec that
// passed the flag and died without claude opening a session at all is
// read as the argv being refused, and the run falls back to the cheap
// variant -- start again from the prompt, in the same sandbox -- rather
// than turning a provider blip into a hard failure. See retry.go, and
// live_resume_test.go for the measurement itself.
func TestRunRetriesFromScratchWhenClaudeRefusesToResume(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(systemInit("sess-9"), overloadedResult), err: errors.New("exit status 1")},
		{stdout: "", err: errors.New("exit status 1 (stderr: error: unknown option '--resume')")},
		{stdout: stream(systemInit("sess-10"), assistantText("done"), successResult("done", 1))},
	}}
	f := newFramework(r, "mcpserver-path", withoutBackoff())

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "do the thing", SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("Run err = %v, want the fresh retry to carry the run", err)
	}
	if len(r.calls) != 3 {
		t.Fatalf("claude was exec'd %d time(s), want 3 (the refusal, the rejected resume, the fresh retry)", len(r.calls))
	}
	if got := argValue(r.calls[2].args, "--resume"); got != "" {
		t.Errorf("the fresh retry still asked to resume %q after claude refused the flag", got)
	}
	if !strings.Contains(r.calls[2].stdin, "do the thing") {
		t.Errorf("the fresh retry's prompt = %q, want the task's own prompt back", r.calls[2].stdin)
	}
	if !strings.Contains(result.Transcript, "refused the invocation") {
		t.Errorf("the transcript says nothing about the refused resume, which is the one thing a maintainer has to see:\n%s", result.Transcript)
	}
}

// A rejected resume must not spend a retry: no model turn was bought
// with it, so the run still has both of its attempts at the provider.
func TestARefusedResumeCostsTheRunNoRetry(t *testing.T) {
	refused := attemptReply{
		stdout: stream(systemInit("sess-1"), overloadedResult),
		err:    errors.New("exit status 1"),
	}
	rejected := attemptReply{stdout: "", err: errors.New("exit status 1 (stderr: unknown option)")}
	r := &attemptRunner{replies: []attemptReply{refused, rejected, refused, refused, refused}}
	f := newFramework(r, "mcpserver-path", withoutBackoff())

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err == nil {
		t.Fatal("Run err = nil, want the capacity refusal")
	}
	// The rejected resume, plus the three attempts the budget allows.
	if want := 2 + agent.DefaultTransientRetries; len(r.calls) != want {
		t.Errorf("claude was exec'd %d time(s), want %d", len(r.calls), want)
	}
}

// And it must not spend the turn budget either: an exec that opened no
// session ran no model turn, so the attempt after it gets the whole of
// what was left rather than paying for a process start.
func TestARefusedResumeCostsTheRunNoTurns(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(systemInit("sess-1"), assistantText("one"),
			`{"type":"result","subtype":"error_during_execution","is_error":true,`+
				`"num_turns":2,"result":"API Error: 529 overloaded_error"}`), err: errors.New("exit status 1")},
		{stdout: "", err: errors.New("exit status 1 (stderr: unknown option)")},
		{stdout: stream(systemInit("sess-2"), successResult("done", 1))},
	}}
	f := newFramework(r, "mcpserver-path", withoutBackoff())

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir(), MaxTurns: 5}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := argValue(r.calls[2].args, "--max-turns"); got != "3" {
		t.Errorf("the fresh retry's --max-turns = %q, want 3 -- only the first attempt's two turns are gone", got)
	}
}

// The parser has to find the session id claude repeats on every event,
// and the init event that says a session was opened at all -- the two
// facts retry.go's resume and its fallback are built on.
func TestParseEventsReadsClaudesSessionID(t *testing.T) {
	for _, tc := range []struct{ name, line string }{
		{"the init event", systemInit("abc")},
		{"an assistant message", `{"type":"assistant","session_id":"abc","message":{"content":[{"type":"text","text":"hi"}]}}`},
		{"the result event", `{"type":"result","session_id":"abc","result":"done","num_turns":1}`},
	} {
		if got := parseEvents(stream(tc.line)).sessionID; got != "abc" {
			t.Errorf("sessionID off %s = %q, want abc", tc.name, got)
		}
	}
	if got := parseEvents(stream(assistantText("hi"))).sessionID; got != "" {
		t.Errorf("sessionID off a capture carrying none = %q, want empty", got)
	}
	if !parseEvents(stream(systemInit("abc"))).sawInit {
		t.Error("sawInit off a capture holding claude's init event = false, want true")
	}
	if parseEvents(stream(assistantText("hi"), successResult("done", 1))).sawInit {
		t.Error("sawInit off a capture with no init event = true: a refused argv would read as an ordinary failure")
	}
	// A run that was refused and then resumed reports two sessions, and
	// the live one is the last: a resumed exec is free to fork the
	// session under an id of its own.
	if got := parseEvents(stream(systemInit("first"), overloadedResult, systemInit("second"))).sessionID; got != "second" {
		t.Errorf("sessionID off a two-attempt capture = %q, want second", got)
	}
}

// The live transcript mirror is opened O_TRUNC once and reused, so a
// retry appends to what the run had already streamed instead of erasing
// it -- the file orchestrator renders a still-running run from.
func TestTheTranscriptMirrorSurvivesARetry(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(assistantText("first attempt"), overloadedResult), err: errors.New("exit status 1")},
		{stdout: stream(assistantText("second attempt"), successResult("done", 1))},
	}}
	f := newFramework(r, "mcpserver-path", withoutBackoff())

	path := filepath.Join(t.TempDir(), "run.jsonl")
	if _, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "go", SandboxRoot: t.TempDir(), TranscriptPath: path,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	mirrored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the mirror: %v", err)
	}
	for _, want := range []string{"first attempt", "second attempt"} {
		if !strings.Contains(string(mirrored), want) {
			t.Errorf("the mirror lost %q:\n%s", want, mirrored)
		}
	}
}

// RunConfig.MaxTurns is a ceiling on the run, not on each exec of
// claude: a retry is passed what is left of the budget, since claude
// enforces it itself and would otherwise start counting again from zero.
func TestTheTurnBudgetStaysCumulativeAcrossAttempts(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(assistantText("one"), `{"type":"result","subtype":"error_during_execution","is_error":true,`+
			`"num_turns":3,"result":"API Error: 529 overloaded_error"}`), err: errors.New("exit status 1")},
		{stdout: stream(assistantText("two"), successResult("done", 1))},
	}}
	f := newFramework(r, "mcpserver-path", withoutBackoff())

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir(), MaxTurns: 5}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := argValue(r.calls[0].args, "--max-turns"); got != "5" {
		t.Errorf("the first attempt's --max-turns = %q, want 5", got)
	}
	if got := argValue(r.calls[1].args, "--max-turns"); got != "2" {
		t.Errorf("the retry's --max-turns = %q, want 2 -- the three turns the first attempt spent are gone", got)
	}
}

// An attempt the provider cut off never reports a total, and reading
// that as "no turns spent" would hand the retry the whole budget back.
// The assistant messages it did stream are the count instead.
func TestAnAttemptWithNoTotalStillSpendsItsTurns(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(assistantText("one"), assistantText("two"),
			`{"type":"result","is_error":true,"result":"API Error: 529 overloaded_error"}`), err: errors.New("exit status 1")},
		{stdout: stream(successResult("done", 1))},
	}}
	f := newFramework(r, "mcpserver-path", withoutBackoff())

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir(), MaxTurns: 4}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := argValue(r.calls[1].args, "--max-turns"); got != "2" {
		t.Errorf("the retry's --max-turns = %q, want 2 -- two assistant messages went past before the refusal", got)
	}
}

// A run with no turn budget left is not retried: there would be nothing
// for the attempt to do with it.
func TestARunWithNoTurnsLeftIsNotRetried(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(assistantText("one"), `{"type":"result","is_error":true,"num_turns":2,`+
			`"result":"API Error: 529 overloaded_error"}`), err: errors.New("exit status 1")},
	}}
	f := newFramework(r, "mcpserver-path", withoutBackoff())

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir(), MaxTurns: 2}); err == nil {
		t.Fatal("Run err = nil, want the capacity refusal")
	}
	if len(r.calls) != 1 {
		t.Errorf("claude was exec'd %d time(s) with no turns left to spend, want 1", len(r.calls))
	}
}

// Only a capacity refusal is retried. An ordinary failure repeated is an
// ordinary failure that cost three times as much; a quota refusal
// belongs to dispatch's pause rather than to a 30-second backoff; a
// --max-turns budget running out is grain stopping the run on purpose.
//
// And a run that *finished* is never retried, whatever its final answer
// says. The markers capacity.go matches are matched against text this
// run produced, and a run that spent an hour reading CI logs has had
// every one of those phrases through its stream.
func TestRunRetriesNothingButACapacityRefusal(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stdout string
		err    error
	}{
		{"an ordinary failure", stream(`{"type":"result","subtype":"error_during_execution",` +
			`"is_error":true,"result":"the tests failed"}`), errors.New("exit status 1")},
		{"a quota refusal", stream(`{"type":"result","subtype":"error_during_execution","is_error":true,` +
			`"result":"Claude AI usage limit reached|1758400000"}`), errors.New("exit status 1")},
		{"a turn budget that ran out", stream(`{"type":"result","subtype":"error_max_turns","is_error":true,` +
			`"result":"reached the maximum number of turns"}`), errors.New("exit status 1")},
		{"a run that finished quoting a 503", stream(assistantText("CI said 503 Service Unavailable"),
			`{"type":"result","result":"the job logged a 503 Service Unavailable, retried and passed"}`), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &attemptRunner{replies: []attemptReply{
				{stdout: tc.stdout, err: tc.err},
				{stdout: tc.stdout, err: tc.err},
			}}
			f := newFramework(r, "mcpserver-path", withoutBackoff())
			f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir(), MaxTurns: 9})
			if len(r.calls) != 1 {
				t.Errorf("claude was exec'd %d time(s) for %s, want 1", len(r.calls), tc.name)
			}
		})
	}
}

// The retry budget is two, and a provider still refusing after that is
// not having a blip: the task goes back to the queue, where dispatch's
// own backoff -- minutes, not seconds -- can wait it out without holding
// a sandbox open the whole time.
func TestRunStopsRetryingAfterItsBudget(t *testing.T) {
	refused := attemptReply{stdout: stream(overloadedResult), err: errors.New("exit status 1")}
	r := &attemptRunner{replies: []attemptReply{refused, refused, refused, refused}}
	f := newFramework(r, "mcpserver-path", withoutBackoff())

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if _, ok := agent.Capacity(err); !ok {
		t.Fatalf("Run err = %v, want the capacity refusal to be what the run ends on", err)
	}
	if want := 1 + agent.DefaultTransientRetries; len(r.calls) != want {
		t.Errorf("claude was exec'd %d time(s), want %d", len(r.calls), want)
	}
}

// A retry has to fit inside the runtime grain gave this run. One that
// would be cancelled a minute after it started has spent the backoff and
// a model call to end up exactly where it began.
func TestRunDoesNotRetryWithoutTheRuntimeToDoIt(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(overloadedResult), err: errors.New("exit status 1")},
		{stdout: stream(successResult("done", 1))},
	}}
	f := newFramework(r, "mcpserver-path", withoutBackoff())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := f.Run(ctx, agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err == nil {
		t.Fatal("Run err = nil, want the capacity refusal")
	}
	if len(r.calls) != 1 {
		t.Errorf("claude was exec'd %d time(s) with five seconds of runtime left, want 1", len(r.calls))
	}
}

// A multi-attempt capture holds a result event per attempt, and what the
// run ended as is what the last one says. Without this a run that was
// refused and then finished would be handed back to the caller as the
// refusal.
func TestParseTranscriptTakesTheLastResultEvent(t *testing.T) {
	got, err := parseTranscript(stream(
		assistantText("first go"),
		overloadedResult,
		assistantText("second go"),
		successResult("finished", 1),
	))
	if err != nil {
		t.Fatalf("parseTranscript: %v -- the last result event is what the run ended as", err)
	}
	if got.FinalText != "finished" {
		t.Errorf("FinalText = %q, want the last attempt's answer", got.FinalText)
	}
}

// withoutRetries turns the in-run retry off altogether -- for a test
// about something other than the loop, whose one scripted reply would
// otherwise be replayed at every attempt.
func withoutRetries() Option {
	return func(f *Framework) { f.retry = agent.RetryPolicy{} }
}

// A run the daemon detached from as it stopped is still running in its
// run host: the exec is neither retried -- not even as a refused resume of
// a carried session, which is what an empty stream would otherwise read
// as -- nor parsed, and Run hands back agent.ErrDetached for the
// orchestrator to leave the run live. The adopting daemon reads the
// stream instead, through ParseResult.
func TestADetachedRunIsNotRetried(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: "", err: fmt.Errorf("runhost: %w", agent.ErrDetached)},
	}}
	f := newFramework(r, "mcpserver-path", withoutBackoff())
	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "do the thing", SandboxRoot: t.TempDir(),
		ResumeSession: "sess-carried"})
	if !errors.Is(err, agent.ErrDetached) || result != nil {
		t.Fatalf("Run = %v, %v; want nil and agent.ErrDetached", result, err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("claude was exec'd %d time(s), want the one the daemon detached from", len(r.calls))
	}

	parsed, err := f.ParseResult(stream(systemInit("sess-9"), assistantText("done"), successResult("done", 1)), nil)
	if err != nil || parsed == nil || parsed.ResumableSession != "sess-9" {
		t.Fatalf("ParseResult = %+v, %v; want the run's result and the session it ran in", parsed, err)
	}
}

// A run adopted after it had already retried is read whole: the stored
// transcript has every attempt's narrative, not just the attempt the
// adopting daemon tailed, while the run is classified by its last attempt
// and leaves resumable the last session any attempt named.
func TestParseAttemptsReadsTheWholeRun(t *testing.T) {
	f := newFramework(&attemptRunner{}, "mcpserver-path", withoutBackoff())
	earlier := stream(systemInit("sess-1"), assistantText("first try"))
	last := stream(assistantText("second try"), successResult("done", 2))
	parsed, err := f.ParseAttempts(earlier, last, nil)
	if err != nil || parsed == nil {
		t.Fatalf("ParseAttempts = %+v, %v; want the run's result", parsed, err)
	}
	if !strings.Contains(parsed.Transcript, "first try") || !strings.Contains(parsed.Transcript, "second try") {
		t.Fatalf("transcript = %q, want both attempts", parsed.Transcript)
	}
	if parsed.ResumableSession != "sess-1" {
		t.Fatalf("session = %q, want the earlier attempt's, which the last did not replace", parsed.ResumableSession)
	}
	if _, err := f.ParseAttempts(stream(successResult("done", 1)), stream(assistantText("cut off")), errors.New("killed")); err == nil {
		t.Fatal("a run whose last attempt was killed read as the earlier attempt's success")
	}
}

// A run adopted after the provider refused an earlier attempt reads back
// as the same transcript Run writes for a run one daemon watched to the
// end, retry note included: the run-host walkthrough
// (docs/test-runs/2026-10-15-run-host-adoption-resume.md, finding 1)
// found the note missing from every adopted run that had retried, so its
// transcript restarted half way through with nothing saying why.
func TestParseAttemptsKeepsTheRetryNotesRunWrites(t *testing.T) {
	for _, tc := range []struct {
		name    string
		replies []attemptReply
	}{
		{"resumed", []attemptReply{
			{stdout: stream(systemInit("sess-7"), assistantText("cloning"), overloadedResult), err: errors.New("exit status 1")},
			{stdout: stream(systemInit("sess-7"), assistantText("pushed the branch"), successResult("done", 2))},
		}},
		{"twice", []attemptReply{
			{stdout: stream(systemInit("sess-7"), assistantText("cloning"), overloadedResult), err: errors.New("exit status 1")},
			{stdout: stream(systemInit("sess-7"), assistantText("building"), overloadedResult), err: errors.New("exit status 1")},
			{stdout: stream(systemInit("sess-7"), assistantText("pushed the branch"), successResult("done", 3))},
		}},
		{"no session to resume", []attemptReply{
			{stdout: stream(overloadedResult), err: errors.New("exit status 1")},
			{stdout: stream(systemInit("sess-8"), assistantText("pushed the branch"), successResult("done", 1))},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFramework(&attemptRunner{replies: tc.replies}, "mcpserver-path", withoutBackoff())
			watched, err := f.Run(context.Background(), agent.RunConfig{Prompt: "do the thing", SandboxRoot: t.TempDir()})
			if err != nil {
				t.Fatalf("Run = %v", err)
			}
			if !strings.Contains(watched.Transcript, "* the provider refused this run mid-flight") {
				t.Fatalf("Run's transcript has no retry note to compare with:\n%s", watched.Transcript)
			}
			var earlier strings.Builder
			for _, r := range tc.replies[:len(tc.replies)-1] {
				earlier.WriteString(r.stdout)
			}
			adopted, err := f.ParseAttempts(earlier.String(), tc.replies[len(tc.replies)-1].stdout, nil)
			if err != nil || adopted == nil {
				t.Fatalf("ParseAttempts = %+v, %v", adopted, err)
			}
			if adopted.Transcript != watched.Transcript {
				t.Errorf("adopted transcript differs from the watched run's:\nadopted:\n%s\nwatched:\n%s", adopted.Transcript, watched.Transcript)
			}
		})
	}
	f := newFramework(&attemptRunner{}, "mcpserver-path", withoutBackoff())
	parsed, err := f.ParseAttempts("", stream(systemInit("sess-1"), assistantText("only try"), successResult("done", 1)), nil)
	if err != nil || strings.Contains(parsed.Transcript, "refused this run") {
		t.Fatalf("ParseAttempts with no earlier attempt = %+v, %v; want no retry note", parsed, err)
	}
}
