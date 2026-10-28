package antigravity

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
)

const genericAgyFailure = `{"event":"result","result":{"status":"ERROR","error":"Agent execution terminated due to error."}}`
const logPrefix = "E0908 12:34:56.123456 123 errorreport.go:224] "

// Stand in for agy's two separate output channels: its stream has only
// the generic failure, while --log-file has the provider's actual reason.
type diagnosticRunner struct {
	attemptRunner
	t     *testing.T
	logs  []string
	paths []string
}

func (r *diagnosticRunner) Run(ctx context.Context, args []string, stdin string, env []string, dir string, tee io.Writer) (string, error) {
	r.t.Helper()
	path, ok := flagValueFor(args, "--log-file")
	// In this run's own scratch directory inside that HOME, rather than
	// loose in it: a HOME that outlives the run keeps what agy wrote and
	// nothing of grain's, and one directory is what makes that a single
	// removal (runScratchDir, clearRunFiles).
	if !ok || filepath.Dir(path) != runScratchDir(homeOf(env)) {
		r.t.Fatalf("--log-file = %q; want a path in this run's own scratch directory inside its private HOME", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		r.t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 || info.Size() != 0 {
		r.t.Fatalf("new log mode=%o size=%d, want 0600 and empty", info.Mode().Perm(), info.Size())
	}
	for _, previous := range r.paths {
		if path == previous {
			r.t.Fatal("two execs shared a diagnostic log")
		}
	}
	idx := len(r.paths)
	r.paths = append(r.paths, path)
	if idx < len(r.logs) {
		if err := os.WriteFile(path, []byte(r.logs[idx]), 0o600); err != nil {
			r.t.Fatal(err)
		}
	}
	return r.attemptRunner.Run(ctx, args, stdin, env, dir, tee)
}

func TestRunRetainsAgyDiagnosticsBeforeCleaningHome(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stdout string
		err    error
	}{
		{"generic failure", stream(initLine, genericAgyFailure), errors.New("exit status 1")},
		{"terminal failure with exit zero", stream(initLine, genericAgyFailure), nil},
		{"no stdout", "", errors.New("exit status 1")},
		{"truncated stdout", stream(initLine) + `{"event":"res`, errors.New("exit status 1")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const key = "private-key/not-for-the-transcript+1"
			const reason = "calling model: ordinary diagnostic detail"
			r := &diagnosticRunner{
				t:             t,
				attemptRunner: attemptRunner{replies: []attemptReply{{stdout: tc.stdout, err: tc.err}}},
				logs:          []string{logPrefix + reason + "\n" + logPrefix + "key=" + key + " url=" + url.QueryEscape(key) + "\n"},
			}
			f := newFramework(r, "/usr/local/bin/grain", WithAPIKey(key), withoutBackoff())
			path := filepath.Join(t.TempDir(), "run-1")
			result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir(), TranscriptPath: path})
			if err == nil || result == nil || !strings.Contains(result.Transcript, reason) {
				t.Fatalf("Run = (%+v, %v), want the actual diagnostic in the failed result", result, err)
			}
			if len(r.paths) != 1 {
				t.Fatalf("diagnostic text caused %d execs; it must not alter retry policy", len(r.paths))
			}
			var capacity *agent.CapacityError
			if errors.As(err, &capacity) {
				t.Fatalf("log text changed the failure classification: %v", err)
			}
			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatalf("lost subprocess error: %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(PartialTranscript(string(data)), reason) {
				t.Fatal("diagnostics did not survive in the live transcript mirror")
			}
			for _, output := range []string{result.Transcript, string(data)} {
				if strings.Contains(output, key) || strings.Contains(output, url.QueryEscape(key)) {
					t.Fatal("credential escaped into the transcript")
				}
			}
			if _, err := os.Stat(r.paths[0]); !os.IsNotExist(err) {
				t.Fatalf("raw log survived private HOME cleanup: %v", err)
			}
		})
	}
}

// These are the real two-channel failures measured from agy 1.1.27: the
// stream carries only its generic sentence and the private log carries
// Gemini's actionable refusal. Classification must use the current exec's
// diagnostic without treating that diagnostic as model output.
func TestRunClassifiesProviderRefusalsFromAgyDiagnostics(t *testing.T) {
	t.Run("overload retries", func(t *testing.T) {
		const reason = "calling model: Error 503, Message: The model is overloaded. Please try again later., Status: UNAVAILABLE"
		r := &diagnosticRunner{
			t: t,
			attemptRunner: attemptRunner{replies: []attemptReply{
				{stdout: stream(conversationInit("c1"), genericAgyFailure), err: errors.New("exit status 1")},
				{stdout: okStream()},
			}},
			logs: []string{logPrefix + reason + "\n", ""},
		}
		f := newFramework(r, "/usr/local/bin/grain", withoutBackoff())
		result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
		if err != nil || result == nil {
			t.Fatalf("Run = (%+v, %v), want the retried run to succeed", result, err)
		}
		if len(r.paths) != 2 {
			t.Fatalf("agy execs = %d, want one retry", len(r.paths))
		}
	})

	t.Run("quota pauses without retry", func(t *testing.T) {
		const reason = `calling model: Error 429, Message: You exceeded your current quota, Status: RESOURCE_EXHAUSTED, Details: [{"retryDelay":"56s"}]`
		r := &diagnosticRunner{
			t: t,
			attemptRunner: attemptRunner{replies: []attemptReply{
				// With a conversation on the stream: a quota refusal is
				// terminal even where resume.go could have resumed it,
				// the ERROR status being agy's word for this too.
				{stdout: stream(conversationInit("c1"), genericAgyFailure), err: errors.New("exit status 1")},
			}},
			logs: []string{logPrefix + reason + "\n"},
		}
		f := newFramework(r, "/usr/local/bin/grain", withoutBackoff())
		result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
		limit, ok := agent.UsageLimit(err)
		if !ok || result == nil {
			t.Fatalf("Run = (%+v, %v), want a usage limit", result, err)
		}
		if limit.RetryAfter != 56*time.Second {
			t.Errorf("RetryAfter = %s, want 56s", limit.RetryAfter)
		}
		if len(r.paths) != 1 {
			t.Fatalf("agy execs = %d, quota must not be retried", len(r.paths))
		}
	})
}

func TestRunDiagnosticsDoNotContaminateSuccessOrLaterFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		replies []attemptReply
		logs    []string
		wantErr bool
	}{
		{"success", []attemptReply{{stdout: okStream()}}, []string{logPrefix + "discard-this-success-log\n"}, false},
		{"retry succeeds", []attemptReply{
			{stdout: stream(conversationInit("c1"), overloadedResult), err: errors.New("exit 1")},
			{stdout: okStream()},
		}, []string{logPrefix + "first-exec-diagnostic\n", logPrefix + "discard-this-success-log\n"}, false},
		// The retry is refused a second time, this time by agy itself
		// ending in ERROR with nothing in its log to classify -- which
		// resume.go resumes rather than reports, so the budget runs out
		// on a third exec and the run ends on that exec's own failure.
		{"retry fails differently", []attemptReply{
			{stdout: stream(conversationInit("c1"), overloadedResult), err: errors.New("exit 1")},
			{stdout: stream(initLine, genericAgyFailure), err: errors.New("exit 2")},
			{stdout: stream(initLine, genericAgyFailure), err: errors.New("exit 2")},
		}, []string{
			logPrefix + "first-exec-diagnostic\n",
			logPrefix + "ordinary second-exec-diagnostic\n",
			logPrefix + "ordinary final-exec-diagnostic\n",
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &diagnosticRunner{t: t, attemptRunner: attemptRunner{replies: tc.replies}, logs: tc.logs}
			f := newFramework(r, "/usr/local/bin/grain", withoutBackoff())
			result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
			if (err != nil) != tc.wantErr || result == nil {
				t.Fatalf("Run = (%+v, %v)", result, err)
			}
			if len(r.paths) != len(tc.replies) || strings.Contains(result.Transcript, "discard-this-success-log") {
				t.Fatalf("exec count=%d, transcript=%s", len(r.paths), result.Transcript)
			}
			if len(tc.replies) > 1 && !strings.Contains(result.Transcript, "first-exec-diagnostic") {
				t.Fatal("lost the failed exec's diagnostic after retry")
			}
			if tc.wantErr {
				var capacity *agent.CapacityError
				var limit *agent.UsageLimitError
				if errors.As(err, &capacity) || errors.As(err, &limit) {
					t.Fatalf("diagnostics changed the final outcome: %v", err)
				}
				if !strings.Contains(result.Transcript, "final-exec-diagnostic") {
					t.Fatal("lost the final exec's diagnostic")
				}
			}
		})
	}
}

func TestDiagnosticLogBoundsAndRedaction(t *testing.T) {
	path, warning := prepareDiagnosticLog(context.Background(), agent.LocalHost{}, t.TempDir())
	if warning != "" {
		t.Fatal(warning)
	}
	var log strings.Builder
	log.WriteString(logPrefix + "old-record\n")
	log.WriteString(strings.Repeat("unprefixed-secret", diagnosticReadLimit))
	log.WriteString("\n")
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&log, "%srecord-%02d\n", logPrefix, i)
	}
	log.WriteString(logPrefix + "API key: abc\n")
	log.WriteString(logPrefix + "Authorization: Bearer sensitive-token\n")
	log.WriteString(logPrefix + "ghp_abcdefghijklmnopqrstuvwxyz123456\n")
	log.WriteString(logPrefix + "-----BEGIN PRIVATE KEY-----\nprivate-key-material\n-----END PRIVATE KEY-----\n")
	if err := os.WriteFile(path, []byte(log.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	got := readDiagnosticLog(context.Background(), agent.LocalHost{}, path, []string{"abc"})
	for _, absent := range []string{"old-record", "unprefixed-secret", "record-00", "abc", "sensitive-token", "ghp_", "private-key-material"} {
		if strings.Contains(got, absent) {
			t.Errorf("retained %q in diagnostic: %s", absent, got)
		}
	}
	for _, present := range []string{"record-49", "omitted", "redacted"} {
		if !strings.Contains(got, present) {
			t.Errorf("missing %q in diagnostic: %s", present, got)
		}
	}
	if len(got) > diagnosticTextLimit+200 || strings.Count(got, logPrefix) > diagnosticLineLimit {
		t.Fatalf("diagnostic exceeded bounds: %d bytes", len(got))
	}
	// An oversized last record must not evict a useful earlier record.
	if err := os.WriteFile(path, []byte(logPrefix+"useful-error\n"+logPrefix+strings.Repeat("x", diagnosticTextLimit+1)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readDiagnosticLog(context.Background(), agent.LocalHost{}, path, nil); !strings.Contains(got, "useful-error") || len(got) > diagnosticTextLimit+200 {
		t.Fatalf("oversized record not bounded: %q", got)
	}
}

func TestDiagnosticLogUnavailable(t *testing.T) {
	dir := t.TempDir()
	if path, note := prepareDiagnosticLog(context.Background(), agent.LocalHost{}, filepath.Join(dir, "missing")); path != "" || !strings.Contains(note, "unavailable") {
		t.Fatalf("prepareDiagnosticLog = (%q, %q)", path, note)
	}
	for _, path := range []string{dir, filepath.Join(dir, "missing")} {
		if note := readDiagnosticLog(context.Background(), agent.LocalHost{}, path, nil); !strings.Contains(note, "unavailable") {
			t.Fatalf("readDiagnosticLog(context.Background(), agent.LocalHost{}, %q) = %q", path, note)
		}
	}
	path := filepath.Join(dir, "link")
	if err := os.Symlink(dir, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if note := readDiagnosticLog(context.Background(), agent.LocalHost{}, path, nil); !strings.Contains(note, "unavailable") {
		t.Fatalf("followed substituted symlink: %q", note)
	}
}

// cancelAwareHost is agent.LocalHost with one difference: its ReadTail
// honours a cancelled context instead of ignoring it the way a local
// filesystem read does.
//
// That is the contract readDiagnosticLog is written against rather than
// an accident of the local implementation. agent.LocalHost cannot show
// the bug -- os.ReadFile does not consult the context -- so without a
// double like this the detaching below would be untested, and would stay
// untested right up until some Host that does consult it appears.
type cancelAwareHost struct{ agent.LocalHost }

func (h cancelAwareHost) ReadTail(ctx context.Context, path string, limit int64) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	return h.LocalHost.ReadTail(ctx, path, limit)
}

// A run cut off from outside -- an operator, a deadline -- is exactly the
// run whose private log is the only account of why it ended, so reading
// that log must not inherit the cancellation that ended it.
//
// Driven through a whole Run rather than against readDiagnosticLog
// alone, because the property is that the diagnostic survives all the
// way into the transcript and the result: a read that succeeds and is
// then dropped on the way out would pass a narrower test and lose the
// only record of why the run failed.
func TestACancelledRunStillReadsItsDiagnostic(t *testing.T) {
	const reason = "calling model: ordinary diagnostic detail"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &diagnosticRunner{
		t: t,
		attemptRunner: attemptRunner{
			replies: []attemptReply{{stdout: stream(initLine), err: context.Canceled}},
			// Once the exec has been handed its HOME the diagnostic log
			// is already written, so this stands in for whatever cut the
			// run off while agy was running.
			afterRead: func(string) { cancel() },
		},
		logs: []string{logPrefix + reason + "\n"},
	}
	f := newFramework(r, "/usr/local/bin/grain", withoutBackoff())
	// Set directly rather than through an Option: agent.LocalHost is the
	// only Host a deployment has, so an exported injector would be
	// production API with no production caller.
	f.host = cancelAwareHost{}
	path := filepath.Join(t.TempDir(), "run-1")
	result, _ := f.Run(ctx, agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir(), TranscriptPath: path})
	if len(r.paths) != 1 {
		t.Fatalf("agy was exec'd %d time(s), want exactly one", len(r.paths))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), reason) {
		t.Fatalf("the cancelled run's transcript lost its diagnostic; it reads:\n%s", data)
	}
	if result != nil && !strings.Contains(result.Transcript, reason) {
		t.Fatalf("the cancelled run's result lost its diagnostic; it reads:\n%s", result.Transcript)
	}
}

// A real CLI against a loopback Gemini stub; no provider credential or
// model spend. This proves the log format we retain is what agy actually
// writes when its stream and stderr hide the provider's refusal.
func TestLiveAgyClassifiesProviderDiagnostics(t *testing.T) {
	agyPath := liveAgyPath(t)
	provider := newRefusingGeminiAPI(t)
	t.Setenv("GOOGLE_GEMINI_BASE_URL", provider.URL)
	grainBinary := buildGrainBinary(t)
	f := New(agyPath, grainBinary, WithAPIKey("stub-key-not-a-real-credential"), withoutBackoff())
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	path := filepath.Join(t.TempDir(), "run-1")
	result, err := f.Run(ctx, agent.RunConfig{
		Prompt:      "GRAIN-DIAGNOSTIC-PROBE: reply with the word ok.",
		SandboxRoot: t.TempDir(), TranscriptPath: path,
	})
	if err == nil || result == nil || provider.count() == 0 {
		t.Fatalf("Run = (%+v, %v), provider requests=%d", result, err, provider.count())
	}
	_, ok := agent.Capacity(err)
	if !ok {
		t.Fatalf("Run error = %v, want Gemini's overload as a capacity refusal", err)
	}
	for _, want := range []string{"AGY exec 1 diagnostics", "The model is overloaded", "UNAVAILABLE"} {
		if !strings.Contains(result.Transcript, want) {
			t.Fatalf("agy %s transcript lacks %q:\n%s", agyVersion(t), want, result.Transcript)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(PartialTranscript(string(data)), "The model is overloaded") {
		t.Fatal("real AGY diagnostics missing from the live transcript")
	}
	if strings.Contains(string(data), "stub-key-not-a-real-credential") {
		t.Fatal("AGY credential escaped into the live transcript")
	}
}

func TestDiagnosticLogOmitsIncompleteFinalRecord(t *testing.T) {
	path, warning := prepareDiagnosticLog(context.Background(), agent.LocalHost{}, t.TempDir())
	if warning != "" {
		t.Fatal(warning)
	}
	for _, prefix := range []string{"", logPrefix + "complete-error\n"} {
		if err := os.WriteFile(path, []byte(prefix+logPrefix+"key=partial-secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		got := readDiagnosticLog(context.Background(), agent.LocalHost{}, path, []string{"partial-secret-full-value"})
		if strings.Contains(got, "partial-secret") {
			t.Fatalf("retained incomplete credential: %s", got)
		}
		if prefix != "" && !strings.Contains(got, "complete-error") {
			t.Fatal("lost complete record")
		}
	}
}
