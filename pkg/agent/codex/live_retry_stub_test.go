package codex

// The measurement retry.go's "A refusal reaches this loop late, and
// grain leaves it that way" section rests on, written down as a probe:
// codex retries a refused API call itself, several times, before the
// `turn.failed` event this package classifies exists at all.
//
// It needs a real codex and no credential, like the resume probes in
// codex_live_test.go and for the same reason: what it counts is requests
// arriving at a stub of the responses API running in this process, which
// is the only place codex's own ladder is visible. A real OPENAI_API_KEY
// would prove less, since nobody can make OpenAI refuse on demand.
//
// docs/hand-runs/2026-09-08-codex-retry.md is the hand run this came
// from, with the full numbers; what is asserted here is only the shape
// the decision depends on, because the numbers themselves are a vendor's
// and will move.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
)

// internalRetryCeiling is how long a refused exec may take before this
// test calls the measurement stale. Measured at 25 seconds on codex-cli
// 0.153.0 (six stream attempts of five requests each); the ceiling is
// four times that, because what would change grain's mind is a ladder
// that has grown into minutes -- claude's is 2m51s and is the reason
// agent/claude/retry.go had to argue the same question at length -- and
// not a codex that got half a second slower on a loaded runner.
const internalRetryCeiling = 100 * time.Second

// TestLiveCodexRetriesARefusedTurnItself points a real codex at a
// provider that refuses everything with OpenAI's own overload status,
// and reads what codex does about it before reporting anything to grain.
//
// Three facts, each load-bearing somewhere else in this package:
//
//   - codex retries the refused request itself, more than once. That is
//     the premise of retry.go's decision not to bound it: an outage
//     shorter than codex's own ladder never spends one of grain's two
//     retries, and never costs the dispatch and clone those pay for.
//   - it gives up inside internalRetryCeiling. A ladder that grew into
//     claude's three minutes would make the same decision a different
//     one, since the whole argument for leaving it alone is that it is
//     short beside agent.BaseTransientBackoff's own 30 seconds.
//   - what it finally emits is a failure grain reads as a capacity
//     refusal (`retryable`), rather than as an ordinary failed run that
//     would cost the task its retry budget. capacity.go's markers are
//     matched against real text here rather than against this
//     repository's belief about it.
//
// The argv is the one Framework.Run builds, MCP server and all, for the
// reason the claude measurement recorded: an internal retry count can
// depend on how the CLI was invoked (claude's did -- ten attempts on a
// real argv, two on a bare one), so a probe on a simplified command line
// measures nothing a dispatch will ever see. codex's does not appear to,
// which is worth knowing and not worth relying on.
func TestLiveCodexRetriesARefusedTurnItself(t *testing.T) {
	provider := newRefusingResponsesAPI(t, refuseWithOverload)
	stdout, runErr, elapsed := execAgainstRefusingProvider(t, provider)
	attempts := provider.count()
	t.Logf("codex made %d request(s) to the refusing provider in %s before reporting the refusal", attempts, elapsed.Round(time.Millisecond))

	if runErr == nil {
		t.Fatalf("the exec succeeded against a provider that refuses every request: %s", stdout)
	}
	if attempts < 2 {
		t.Errorf("codex made %d request(s) and gave up: it no longer retries a refused call itself.\n"+
			"retry.go's decision not to bound that retry assumes it exists -- with it gone, every "+
			"provider blip now reaches grain's own budget of %d, and the reasoning in that header "+
			"should be re-read rather than left standing.\nstream:\n%s",
			attempts, agent.DefaultTransientRetries, stdout)
	}
	if elapsed > internalRetryCeiling {
		t.Errorf("codex spent %s retrying internally, past the %s this repository measured it at.\n"+
			"That is the number agent/codex/retry.go and docs/hand-runs/2026-09-08-codex-retry.md "+
			"both quote, and the reason grain leaves the retry alone: it is short beside the %s of "+
			"grain's own first backoff. A ladder that has grown into minutes makes a refused run "+
			"look hung, and is worth deciding about again rather than re-measuring later.",
			elapsed.Round(time.Millisecond), internalRetryCeiling, agent.BaseTransientBackoff)
	}
	if !retryable(stdout, runErr, &turnCap{}) {
		t.Errorf("grain does not read what codex reported as a capacity refusal, so a run refused "+
			"this way is retried by nothing and counts against the task's own failure streak.\n"+
			"capacity.go's markers are what decide that.\nstream:\n%s", stdout)
	}
}

// TestLiveCodexReportsA429GrainCanClassify is the same probe pointed at
// the other refusal a busy deployment meets, and the one that was
// silently costing tasks their retry budget until task 432: an HTTP 429.
//
// codex does not retry a 429 at all, and reports it in words of its own
// with the provider's body dropped -- "exceeded retry limit, last
// status: 429 Too Many Requests", identical with a Retry-After, without
// one, and with a body worded exactly the way OpenAI words a rate limit
// (docs/hand-runs/2026-09-08-codex-retry.md §7). capacity.go's header
// has why that reads as capacity rather than as a usage limit.
//
// Two facts, and both of them are somebody else's wording:
//
//   - what codex emits still names the status at all. That single
//     "429" is the whole of what grain has left to classify on, so a
//     codex that stopped printing it would put every rate limit back to
//     being an ordinary failed run -- counted against task_streak, five
//     of them retiring a task -- without anything else here noticing.
//   - grain reads it as a capacity refusal (`retryable`), which is what
//     buys the run a 30-second backoff and a second exec in the same
//     sandbox. Nothing else retries it: codex's own ladder, which the
//     probe above counts for a 503, does not run for this status.
//
// A codex that begins passing the provider's body through is a failure
// here too, and deliberately: `rate_limit_exceeded` in that body reaches
// usagelimit.go first and pauses dispatch deployment-wide, which is a
// far larger response than a per-minute limit deserves and a decision
// somebody should take again rather than inherit.
func TestLiveCodexReportsA429GrainCanClassify(t *testing.T) {
	provider := newRefusingResponsesAPI(t, refuseWithRateLimit)
	stdout, runErr, elapsed := execAgainstRefusingProvider(t, provider)
	attempts := provider.count()
	t.Logf("codex made %d request(s) to the rate-limiting provider in %s before reporting the refusal", attempts, elapsed.Round(time.Millisecond))

	if runErr == nil {
		t.Fatalf("the exec succeeded against a provider that answers every request with a 429: %s", stdout)
	}
	failureText := parseEvents(stdout).failureText()
	t.Logf("codex reported: %q", failureText)
	if !strings.Contains(failureText, "429") {
		t.Errorf("codex no longer names the status in what it reports for a rate limit, so there is "+
			"nothing left for capacity.go to match: a 429 is read as an ordinary failed run again, "+
			"counts against the task's own failure streak, and five of them retire a task that was "+
			"never wrong.\nreported: %q\nstream:\n%s", failureText, stdout)
	}
	if limit := usageLimitFailure(failureText, runErr); limit != nil {
		t.Errorf("codex now passes enough of the provider's body through for usagelimit.go to match "+
			"a 429 (%v), so this refusal pauses dispatch deployment-wide for at least a minute "+
			"instead of being retried in place. That may well be right for a plan's ceiling and is "+
			"wrong for a per-minute limit; capacity.go's header sets out the trade and should be "+
			"re-read now that there is more to go on.\nreported: %q", limit, failureText)
	}
	if !retryable(stdout, runErr, &turnCap{}) {
		t.Errorf("grain does not read what codex reported for a 429 as a capacity refusal. codex "+
			"retries this status zero times itself, so nothing retries it at all, and the task is "+
			"charged for the provider's rate limit.\ncapacity.go's markers are what decide "+
			"that.\nstream:\n%s", stdout)
	}
}

// execAgainstRefusingProvider runs one exec of a real codex -- the argv
// Framework.Run builds and the config.toml writeConfig writes, MCP
// server and all -- against a provider that refuses everything, and
// hands back what it wrote, how it died and how long it took.
//
// The argv and the config are the real ones for the reason the claude
// measurement recorded: an internal retry count can depend on how the
// CLI was invoked (claude's did -- ten attempts on a real argv, two on a
// bare one), so a probe on a simplified command line measures nothing a
// dispatch will ever see.
//
// Deliberately *without* noInternalRetries, which every other probe
// built on stubProviderConfig passes: the ladder those keys turn off is
// among the things these probes are here to watch.
func execAgainstRefusingProvider(t *testing.T, provider *refusingResponsesAPI) (stdout string, runErr error, elapsed time.Duration) {
	t.Helper()
	codexPath, err := exec.LookPath("codex")
	if err != nil {
		skipUnlessLiveRequired(t, "no codex binary on $PATH")
	}
	grainBinaryPath := buildGrainBinary(t)

	f := New(codexPath, grainBinaryPath, withExtraConfig(stubProviderConfig(provider.URL)))
	sandboxRoot := t.TempDir()
	mcpArgs, err := f.mcpServerArgs(context.Background(), agent.RunConfig{SandboxRoot: sandboxRoot})
	if err != nil {
		t.Fatalf("mcpServerArgs: %v", err)
	}
	home, _, cleanup, err := f.writeConfig(context.Background(), mcpArgs, "")
	if err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	t.Cleanup(cleanup)
	env := []string{"CODEX_HOME=" + home, "OPENAI_API_KEY=" + stubAPIKey}

	// Comfortably past internalRetryCeiling, so a codex that retried for
	// minutes is reported by the caller's own assertion rather than cut
	// off here and reported as a cancelled run.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	run := hostRunner{host: agent.LocalHost{}, codexPath: codexPath}

	start := time.Now()
	stdout, runErr = run.Run(ctx, f.codexArgs(""), "GRAIN-INTERNAL-RETRY-PROBE", env, sandboxRoot, nil)
	return stdout, runErr, time.Since(start)
}

// refusingResponsesAPI is stubResponsesAPI's opposite number: it answers
// every request the way refuse says, and counts them.
//
// The body matters as much as the status. capacity.go matches "server is
// overloaded" against text codex passes through from the provider, so a
// stub that refused with an empty body would exercise the retry ladder
// and prove nothing about the classification at the end of it -- which
// is why refuse is one of the two refusals codex_live_test.go words the
// way OpenAI's own edge words them, and not an http.Error.
type refusingResponsesAPI struct {
	*httptest.Server
	mu       sync.Mutex
	requests int
}

func newRefusingResponsesAPI(t *testing.T, refuse func(http.ResponseWriter)) *refusingResponsesAPI {
	t.Helper()
	s := &refusingResponsesAPI{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		s.mu.Lock()
		s.requests++
		s.mu.Unlock()
		refuse(w)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *refusingResponsesAPI) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}
