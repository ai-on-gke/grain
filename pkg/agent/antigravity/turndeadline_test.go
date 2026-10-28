package antigravity

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
)

func TestTurnDeadlineCancelsAStalledModelTurn(t *testing.T) {
	cancelled := make(chan struct{}, 1)
	d := newTurnDeadline(20*time.Millisecond, time.Hour, "")
	d.retarget(func() { cancelled <- struct{}{} }, "")
	t.Cleanup(d.stop)

	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("a stalled model turn was not cancelled")
	}
	if !d.tripped() {
		t.Error("deadline cancelled the attempt without recording what stopped it")
	}
}

func TestTurnDeadlineEndsAtACompletedResponseAcrossChunks(t *testing.T) {
	cancelled := make(chan struct{}, 1)
	d := newTurnDeadline(100*time.Millisecond, time.Hour, "")
	d.retarget(func() { cancelled <- struct{}{} }, "")
	t.Cleanup(d.stop)

	line := []byte(stream(agentTurn(0, "done")))
	for _, b := range line {
		if _, err := d.Write([]byte{b}); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	select {
	case <-cancelled:
		t.Fatal("completed response left its model-turn deadline running")
	case <-time.After(2 * d.cutoff):
	}
	if d.tripped() {
		t.Error("completed response was reported as a timed-out turn")
	}
}

func TestTurnDeadlinePausesForAToolAndRestartsWhenItFinishes(t *testing.T) {
	cancelled := make(chan struct{}, 1)
	d := newTurnDeadline(100*time.Millisecond, time.Hour, "")
	d.retarget(func() { cancelled <- struct{}{} }, "")
	t.Cleanup(d.stop)

	if _, err := d.Write([]byte(stream(toolActive(4, "wait_for_checks", "{}")))); err != nil {
		t.Fatalf("active tool Write: %v", err)
	}
	select {
	case <-cancelled:
		t.Fatal("tool time counted against the model-turn deadline")
	case <-time.After(2 * d.cutoff):
	}

	if _, err := d.Write([]byte(stream(toolDone(4, "wait_for_checks", "passed")))); err != nil {
		t.Fatalf("finished tool Write: %v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("the next model turn got no deadline after its tool finished")
	}
	if !d.tripped() {
		t.Error("deadline cancelled the next turn without recording what stopped it")
	}
}

func TestTurnDeadlineRetargetDoesNotLetAnOldTimerCancelANewAttempt(t *testing.T) {
	first, second := 0, 0
	d := newTurnDeadline(time.Hour, time.Hour, "")
	d.retarget(func() { first++ }, "")
	d.mu.Lock()
	oldGeneration := d.generation
	d.mu.Unlock()

	d.retarget(func() { second++ }, "")
	d.expire(oldGeneration, turnVerdict{tier: turnTierFallback, limit: time.Hour})
	d.stop()

	if first != 0 || second != 0 {
		t.Errorf("stale timer cancelled attempts: first=%d second=%d", first, second)
	}
	if d.tripped() {
		t.Error("stale timer marked the new attempt timed out")
	}
}

type stalledTurnRunner struct {
	diagnostic string
	calls      int
}

func (r *stalledTurnRunner) Run(ctx context.Context, args []string, _ string, _ []string, _ string, tee io.Writer) (string, error) {
	r.calls++
	if path, ok := flagValueFor(args, "--log-file"); ok && r.diagnostic != "" {
		if err := os.WriteFile(path, []byte(logPrefix+r.diagnostic+"\n"), 0o600); err != nil {
			return "", err
		}
	}
	stdout := stream(initLine)
	if tee != nil {
		io.WriteString(tee, stdout)
	}
	<-ctx.Done()
	return stdout, ctx.Err()
}

func TestTurnDeadlineKeepsARefusalClassificationAndRetries(t *testing.T) {
	r := &stalledTurnRunner{diagnostic: "calling model: Error 503, Message: The model is overloaded. Please try again later., Status: UNAVAILABLE"}
	f := newFramework(r, "/usr/local/bin/grain", withoutBackoff(), func(f *Framework) {
		f.retryCutoff = 20 * time.Millisecond
	})

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if _, ok := agent.Capacity(err); !ok {
		t.Fatalf("Run error = %v, want the refusal retained as a capacity error", err)
	}
	if r.calls != 1+agent.DefaultTransientRetries {
		t.Fatalf("agy was exec'd %d times, want the bounded refusal plus %d retries", r.calls, agent.DefaultTransientRetries)
	}
	if result == nil || !strings.Contains(result.Transcript, "The model is overloaded") {
		t.Fatalf("diagnostic refusal was lost when the turn was cancelled: %+v", result)
	}
}

// stallThenFinishRunner is an agy that opens a conversation, goes quiet
// long enough for the model-turn deadline to cancel it, and answers the
// exec after that normally.
type stallThenFinishRunner struct {
	calls  []attemptCall
	stalls int
}

func (r *stallThenFinishRunner) Run(ctx context.Context, args []string, stdin string, env []string, dir string, tee io.Writer) (string, error) {
	r.calls = append(r.calls, attemptCall{args: append([]string(nil), args...), stdin: stdin, dir: dir})
	if len(r.calls) <= r.stalls {
		stdout := stream(conversationInit("conv-8"))
		io.WriteString(tee, stdout)
		<-ctx.Done()
		return stdout, ctx.Err()
	}
	stdout := stream(conversationInit("conv-8"), agentTurn(0, "carried on"), successResult("carried on"))
	io.WriteString(tee, stdout)
	return stdout, nil
}

// A turn grain cancelled for going quiet is the clearest case resume.go
// has: the conversation is intact, the sandbox is intact, and the only
// thing that happened is that this package stopped a process. So the run
// carries on in it rather than costing a dispatch and a fresh sandbox.
func TestTurnDeadlineResumesTheConversationItCancelled(t *testing.T) {
	r := &stallThenFinishRunner{stalls: 1}
	f := newFramework(r, "/usr/local/bin/grain", withoutBackoff(), func(f *Framework) {
		f.retryCutoff = 100 * time.Millisecond
	})

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "do the thing", SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("Run err = %v, want the resumed attempt to carry the run", err)
	}
	if len(r.calls) != 2 {
		t.Fatalf("agy was exec'd %d time(s), want the stalled turn to be resumed", len(r.calls))
	}
	if got := flagValue(r.calls[1].args, "--conversation"); got != "conv-8" {
		t.Errorf("the second attempt resumed conversation %q, want conv-8", got)
	}
	turn, err := promptFromStdin(r.calls[1].stdin)
	if err != nil {
		t.Fatalf("decoding the resumed attempt's turn: %v", err)
	}
	if strings.Contains(turn, "do the thing") || strings.Contains(turn, "provider refused") {
		t.Errorf("the resumed attempt's turn = %q, want the interruption it actually had", turn)
	}
	if result.FinalText != "carried on" {
		t.Errorf("FinalText = %q, want the resumed attempt's answer", result.FinalText)
	}
	if !strings.Contains(result.Transcript, "went quiet for 100ms") {
		t.Errorf("the transcript does not say what cancelled the first attempt:\n%s", result.Transcript)
	}
}

// With no conversation on the stream there is nothing to resume, and a
// stall this package cannot explain is reported rather than guessed at:
// the deadline grain imposed is the whole of what it knows.
func TestTurnDeadlineNamesAStallWithNoRefusalEvidence(t *testing.T) {
	r := &stalledTurnRunner{}
	f := newFramework(r, "/usr/local/bin/grain", func(f *Framework) {
		f.retryCutoff = 20 * time.Millisecond
	})

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "model turn exceeded 20ms") {
		t.Fatalf("Run error = %v, want the deadline grain imposed", err)
	}
	if r.calls != 1 {
		t.Fatalf("ordinary stalled turn caused %d execs, want no capacity retry", r.calls)
	}
}

// fakeAgyLog is a probe over an agy log the test writes as it goes.
type fakeAgyLog struct {
	mu      sync.Mutex
	records []string
	err     error
}

func (l *fakeAgyLog) add(records ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, records...)
}

func (l *fakeAgyLog) probe(string) ([]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.records...), l.err
}

const agyStartupRecord = "I0908 12:34:50.000001 123 main.go:42] starting agent session"

func watchedDeadline(t *testing.T, cutoff, ceiling time.Duration, log *fakeAgyLog, path string) (*turnDeadline, chan struct{}) {
	t.Helper()
	cancelled := make(chan struct{}, 1)
	d := newTurnDeadline(cutoff, ceiling, "slow-provider")
	if log != nil {
		d.probe = log.probe
	}
	d.retarget(func() { cancelled <- struct{}{} }, path)
	t.Cleanup(d.stop)
	return d, cancelled
}

func TestTurnDeadlineLetsASlowTurnWithNoRetriesRunToTheCeiling(t *testing.T) {
	log := &fakeAgyLog{}
	log.add(agyStartupRecord, "I0908 12:34:51.000001 123 model.go:90] streaming model response")
	start := time.Now()
	d, cancelled := watchedDeadline(t, 20*time.Millisecond, 300*time.Millisecond, log, "/agy.log")

	select {
	case <-cancelled:
		t.Fatalf("a slow turn with no retries logged was cancelled after %s, before its ceiling", time.Since(start))
	case <-time.After(150 * time.Millisecond):
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("a slow turn was never cut at its ceiling")
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Errorf("slow turn cancelled after %s, want no earlier than its 300ms ceiling", elapsed)
	}
	v, ok := d.trippedVerdict()
	if !ok || v.tier != turnTierSlow || v.limit != 300*time.Millisecond {
		t.Fatalf("verdict = %+v, %v; want the slow-turn ceiling at 300ms", v, ok)
	}
	msg := d.describe(v)
	for _, want := range []string{"model turn exceeded 300ms", "slow-turn ceiling", "no provider errors logged; the model is slow", `connection "slow-provider"`, "modelTurnTimeout"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not say %q", msg, want)
		}
	}
}

func TestTurnDeadlineCutsATurnAgyIsRetryingAtTheCutoff(t *testing.T) {
	log := &fakeAgyLog{}
	log.add(agyStartupRecord,
		logPrefix+"calling model: Error 503, Message: Service is temporarily unavailable, Status: UNAVAILABLE",
		logPrefix+"calling model: Error 503, Message: Service is temporarily unavailable, Status: UNAVAILABLE (retrying)")
	start := time.Now()
	d, cancelled := watchedDeadline(t, 30*time.Millisecond, time.Hour, log, "/agy.log")

	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("a turn agy was logging 503 retries for was not cut at the retry cutoff")
	}
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Errorf("retrying turn cancelled after %s, before its 30ms cutoff", elapsed)
	}
	v, _ := d.trippedVerdict()
	if v.tier != turnTierRetry || v.limit != 30*time.Millisecond {
		t.Fatalf("verdict = %+v, want the retry cutoff at 30ms", v)
	}
	msg := d.describe(v)
	for _, want := range []string{"model turn exceeded 30ms", "retry cutoff", "retrying provider errors", "Error 503", `connection "slow-provider"`, "modelRetryCutoff"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not say %q", msg, want)
		}
	}
}

// Retries that start partway through a slow turn end it at the next look
// at the log rather than at the ceiling.
func TestTurnDeadlineCutsASlowTurnOnceRetriesAppear(t *testing.T) {
	log := &fakeAgyLog{}
	log.add(agyStartupRecord)
	d, cancelled := watchedDeadline(t, 20*time.Millisecond, time.Hour, log, "/agy.log")

	select {
	case <-cancelled:
		t.Fatal("a turn with no retries logged was cancelled at the cutoff")
	case <-time.After(60 * time.Millisecond):
	}
	log.add(logPrefix + "generateContent: googleapi: Error 429: RESOURCE_EXHAUSTED")
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("retries logged after the cutoff did not end the turn")
	}
	if v, _ := d.trippedVerdict(); v.tier != turnTierRetry || !strings.Contains(v.excerpt, "429") {
		t.Errorf("verdict = %+v, want the retry cutoff quoting the 429", v)
	}
}

func TestTurnDeadlineFallsBackToTheCutoffWithoutALog(t *testing.T) {
	for name, tc := range map[string]struct {
		log  *fakeAgyLog
		path string
	}{
		"no log file":         {log: &fakeAgyLog{}, path: ""},
		"no probe":            {log: nil, path: "/agy.log"},
		"unreadable log":      {log: &fakeAgyLog{err: os.ErrPermission}, path: "/agy.log"},
		"no records it reads": {log: &fakeAgyLog{}, path: "/agy.log"},
	} {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			d, cancelled := watchedDeadline(t, 20*time.Millisecond, time.Hour, tc.log, tc.path)
			select {
			case <-cancelled:
			case <-time.After(2 * time.Second):
				t.Fatal("a turn with no readable log was not cut at the retry cutoff")
			}
			if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
				t.Errorf("cancelled after %s, before the 20ms cutoff", elapsed)
			}
			v, _ := d.trippedVerdict()
			if v.tier != turnTierFallback || v.limit != 20*time.Millisecond {
				t.Fatalf("verdict = %+v, want the fallback retry cutoff at 20ms", v)
			}
			if msg := d.describe(v); !strings.Contains(msg, "model turn exceeded 20ms") || !strings.Contains(msg, "log could not be read") || !strings.Contains(msg, "modelRetryCutoff") {
				t.Errorf("fallback message %q does not name its tier, value and setting", msg)
			}
		})
	}
}

// A retry an earlier turn of the same exec logged and recovered from is
// already in the log when the next turn opens, and must not cut that one.
func TestTurnDeadlineIgnoresRetriesAnEarlierTurnLogged(t *testing.T) {
	log := &fakeAgyLog{}
	log.add(agyStartupRecord, logPrefix+"calling model: Error 503, Message: The model is overloaded. Please try again later., Status: UNAVAILABLE")
	d, cancelled := watchedDeadline(t, 20*time.Millisecond, time.Hour, log, "/agy.log")

	if _, err := d.Write([]byte(stream(toolActive(1, "run_command", "{}")))); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write([]byte(stream(toolDone(1, "run_command", "ok")))); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
		t.Fatalf("the next turn was cut for a retry the previous one logged: %+v", d.verdict)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestProviderRetryRecordReadsTheRefusalShapes(t *testing.T) {
	for _, record := range []string{
		logPrefix + "calling model: Error 503, Message: The model is overloaded. Please try again later., Status: UNAVAILABLE",
		logPrefix + "googleapi: Error 429: Quota exceeded for quota metric",
		logPrefix + "status code: 500, retrying request",
		logPrefix + "rpc error: code = ResourceExhausted desc = RESOURCE_EXHAUSTED",
		logPrefix + "HTTP 502 from upstream",
		logPrefix + "request failed, will retry in 4s",
	} {
		if !providerRetryRecord(record) {
			t.Errorf("providerRetryRecord(%q) = false, want a retry", record)
		}
	}
	for _, record := range []string{
		agyStartupRecord,
		logPrefix + "tool run_command finished in 503ms",
		logPrefix + "wrote 429 bytes to transcript",
	} {
		if providerRetryRecord(record) {
			t.Errorf("providerRetryRecord(%q) = true, want an ordinary record", record)
		}
	}
}

// slowTurnRunner is an agy that logs its startup and then thinks for
// longer than the retry cutoff without ever logging a provider error.
type slowTurnRunner struct{ calls int }

func (r *slowTurnRunner) Run(ctx context.Context, args []string, _ string, _ []string, _ string, tee io.Writer) (string, error) {
	r.calls++
	if path, ok := flagValueFor(args, "--log-file"); ok {
		if err := os.WriteFile(path, []byte(agyStartupRecord+"\n"), 0o600); err != nil {
			return "", err
		}
	}
	stdout := stream(initLine)
	io.WriteString(tee, stdout)
	<-ctx.Done()
	return stdout, ctx.Err()
}

func TestTurnLimitsFromTheConnectionAreHonoured(t *testing.T) {
	r := &slowTurnRunner{}
	f := newFramework(r, "/usr/local/bin/grain", WithTurnLimits(200*time.Millisecond, 20*time.Millisecond, "corp-agy"))
	if f.turnCeiling != 200*time.Millisecond || f.retryCutoff != 20*time.Millisecond {
		t.Fatalf("WithTurnLimits set ceiling %s, cutoff %s", f.turnCeiling, f.retryCutoff)
	}

	start := time.Now()
	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("a turn past its connection's ceiling was not cancelled")
	}
	if elapsed < 200*time.Millisecond {
		t.Errorf("slow turn cancelled after %s, before the connection's 200ms ceiling", elapsed)
	}
	for _, want := range []string{"model turn exceeded 200ms", "no provider errors logged; the model is slow", `connection "corp-agy"`, "modelTurnTimeout"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Run error %q does not say %q", err, want)
		}
	}
}

func TestTurnLimitsDefaultWhenTheConnectionSetsNone(t *testing.T) {
	f := newFramework(&slowTurnRunner{}, "/usr/local/bin/grain", WithTurnLimits(0, 0, "plain"))
	if f.turnCeiling != DefaultModelTurnTimeout || f.retryCutoff != DefaultModelRetryCutoff {
		t.Errorf("unset limits = ceiling %s, cutoff %s; want the defaults %s and %s",
			f.turnCeiling, f.retryCutoff, DefaultModelTurnTimeout, DefaultModelRetryCutoff)
	}
}
