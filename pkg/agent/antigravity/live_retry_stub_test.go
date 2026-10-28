package antigravity

// The measurement resume.go's "A refusal reaches this loop early, and
// there is nothing of agy's to bound" section rests on, written down as a
// probe: what a real agy does when the Gemini API refuses every request,
// and what is left of that refusal by the time grain reads the stream.
//
// It needs a real agy and no credential. It counts and timestamps requests
// arriving at a stub of Gemini's generateContent endpoint running in this
// process -- the only place agy's own ladder is visible -- and reads both
// the stream and the bounded private diagnostic production Run uses. A
// real GEMINI_API_KEY would prove less, since nobody can ask Google to be
// overloaded on demand.
//
// docs/hand-runs/2026-09-08-agy-retry.md is the hand run this came from,
// with the full numbers and the decision they led to.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// liveAgentTestEnv is the same opt-in tests/e2e, agent/claude and
// agent/codex read -- one name a maintainer has to remember across every
// live test in this repository, and the one the nightly sets.
const liveAgentTestEnv = "GRAIN_LIVE_AGENT_TEST"

var (
	refusedAgyProbeOnce sync.Once
	refusedAgyProbeRun  refusedAgyProbe
)

func skipUnlessLiveRequired(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv(liveAgentTestEnv) != "" {
		t.Fatalf("%s is set, but %s: this run proved nothing about a live agy", liveAgentTestEnv, reason)
	}
	t.Skipf("%s; skipping the live agy test (set %s=1 to make this a failure)", reason, liveAgentTestEnv)
}

// measuredAgyVersion is the agy release every number below was measured
// on, named once so that the numbers and the binary they describe cannot
// drift apart in silence -- which is exactly what happened between
// 2026-09-14 and 2026-09-21. These probes were rewritten around 1.2.2's
// ladder while the image still pinned 1.1.27, a release that does not
// retry an HTTP refusal at all, so the nightly failed every firing with
// "its retry schedule changed" when nothing about any schedule had
// changed: two files simply described two different binaries.
//
// Two guards hang off this constant, at the two moments the drift can
// appear. TestMeasuredAgyIsTheAgyTheImagePins is an ordinary unit test,
// so a commit that moves the Dockerfile pin off this version goes red in
// the normal suite, next to the bump, rather than in a nightly nobody is
// watching. requireMeasuredAgy is the live half, for the binary actually
// on $PATH -- a pin the installer did not honour, a self-updated agy, a
// GRAIN_AGY_PATH pointing somewhere else -- and it names both versions
// instead of reporting the mismatch as a surprising measurement.
//
// Moving it is a re-measurement, not an edit: rerun the hand run in
// docs/hand-runs/2026-09-08-agy-retry.md, write down the new schedule,
// and reconsider DefaultModelRetryCutoff and the private-diagnostic dependency
// in the light of it before changing this line.
const measuredAgyVersion = "1.2.2"

// refusalProbeRuntime is long enough to show the slow half of 1.2.2's
// schedule, and deliberately longer than DefaultModelRetryCutoff: the probe
// measures what the vendor binary would do without grain's new bound.
// Both findings below share this one exec so the package timeout cannot
// cut the second half off again.
const refusalProbeRuntime = 7 * time.Minute

// TestMeasuredAgyIsTheAgyTheImagePins ties this file's numbers to the
// binary a deployment really runs.
//
// The Dockerfile's ARG is the single source of the version everywhere it
// matters: the image installs it, .grain/workflows/live-agent.yml derives
// the agy the probes below measure from the same line and asserts the
// installed binary reports it, and Dockerfile.k8s-sandbox is held equal
// to it by tests/deploy. So the pin moving off measuredAgyVersion means
// the assertions in this file are describing a release this repository no
// longer ships -- and the schedule the ladder they guard was sized for
// belongs to nobody.
//
// It needs no agy, no network and no credential, which is the point: it
// runs in the ordinary package suite, on the commit that bumps the pin.
func TestMeasuredAgyIsTheAgyTheImagePins(t *testing.T) {
	if pin := pinnedAgyVersion(t); pin != measuredAgyVersion {
		t.Errorf("the image pins agy %s and the live retry probes in %s assert agy %s's behaviour "+
			"(at least ten provider requests across %s, no refusal text on the stream, every 503 in the "+
			"private diagnostic). Re-measure against the pinned release -- "+
			"docs/hand-runs/2026-09-08-agy-retry.md says how -- then move measuredAgyVersion and whatever "+
			"the new schedule changes about DefaultModelRetryCutoff, rather than leaving the nightly to report "+
			"the disagreement as a changed schedule.",
			pin, "live_retry_stub_test.go", measuredAgyVersion, refusalProbeRuntime)
	}
}

// requireMeasuredAgy refuses to read a schedule off a binary these
// measurements were not taken from.
//
// Every assertion in this file is a fact about one vendor release, and an
// agy that is not that release fails them for a reason that has nothing
// to do with grain. It reports through skipUnlessLiveRequired so the two
// callers get what each needs: the nightly, which sets
// GRAIN_LIVE_AGENT_TEST, gets a failure naming both versions, and a
// developer with some other agy on $PATH gets a skip saying which one.
func requireMeasuredAgy(t *testing.T) {
	t.Helper()
	if got := agyVersion(t); got != measuredAgyVersion {
		skipUnlessLiveRequired(t, fmt.Sprintf("the agy on $PATH is %s and these probes measure %s "+
			"(docs/hand-runs/2026-09-08-agy-retry.md); a schedule read off %s would say nothing about "+
			"the release this repository ships", got, measuredAgyVersion, got))
	}
}

// TestLiveAgyRetriesARefusedTurnItself points a real agy at a provider
// that refuses everything with Gemini's own overload status, and reads
// what agy does about it before reporting anything to grain.
//
// Two facts, both load-bearing in this package:
//
//   - agy is still retrying when refusalProbeRuntime expires, after at
//     least ten provider requests. That is the 1.2.2 ladder turnDeadline
//     exists to bound; a later binary returning sooner changes the
//     decision and should make this probe ask to be re-measured again.
//   - no provider refusal reaches the stream during that whole schedule.
//     The private diagnostic remains the evidence production Run reads;
//     TestLiveAgyLosesTheProvidersRefusalText below keeps that finding
//     visible without launching the seven-minute exec a second time.
//
// It invokes the binary directly rather than Framework.Run so the new
// model-turn deadline does not hide the vendor schedule being measured.
// Framework.Run's preservation of the diagnostic and capacity outcome
// when that deadline fires is covered deterministically in
// turndeadline_test.go.
//
// The argv is the one Framework.Run builds, MCP server and private HOME
// and all, for the reason the claude measurement recorded: an internal
// retry count can depend on how the CLI was invoked (claude's did -- ten
// attempts on a real argv, two on a bare one), so a probe on a simplified
// command line measures nothing a dispatch will ever see.
func TestLiveAgyRetriesARefusedTurnItself(t *testing.T) {
	requireMeasuredAgy(t)
	probe := cachedRefusedAgyProbe(t)

	t.Logf("agy %s made %d request(s) to the refusing provider in %s before the probe stopped it",
		agyVersion(t), probe.requests, probe.elapsed.Round(time.Millisecond))
	for i, at := range probe.times {
		t.Logf("request %d at +%s", i+1, at.Round(time.Millisecond))
	}
	t.Logf("terminal text grain reads: %q", probe.events.resultText)
	t.Logf("bounded private diagnostic: %q", probe.diagnostic)

	if probe.runErr == nil {
		t.Fatalf("the exec succeeded against a provider that refuses every request: %s", probe.stdout)
	}
	if probe.requests < 10 {
		t.Fatalf("agy made only %d request(s); %s made at least ten before this guard, so its retry "+
			"schedule changed and should be re-measured rather than silently weakening the bound.\nstream:\n%s",
			probe.requests, measuredAgyVersion, probe.stdout)
	}
	if !probe.guardExpired {
		t.Errorf("agy returned after %s rather than remaining in its refusal ladder through %s; "+
			"re-measure the schedule and reconsider DefaultModelRetryCutoff", probe.elapsed.Round(time.Millisecond), refusalProbeRuntime)
	}
}

// TestLiveAgyLosesTheProvidersRefusalText is the other half of the same
// measurement. agy 1.2.2 writes each 503 to its private diagnostic, but
// puts none of the provider's words on its stream through seven minutes
// of retries. Production Run deliberately reads that diagnostic now, so
// overloads are classified; this probe remains the signal for when that
// undocumented dependency can be removed.
//
// It reports the known gap as a skip rather than making every nightly red.
// It still fails loudly in the useful direction: once agy's stream carries
// the provider refusal, capacityFailure is non-nil and the skip disappears.
func TestLiveAgyLosesTheProvidersRefusalText(t *testing.T) {
	requireMeasuredAgy(t)
	probe := cachedRefusedAgyProbe(t)

	capacity := capacityFailure(probe.stdout, probe.runErr)
	t.Logf("stream after %d refused requests -> capacityFailure %v", probe.requests, capacity)
	if capacity == nil {
		t.Skipf("agy %s still put none of the provider's refusal text on its stream across %d "+
			"requests and %s. Grain therefore still needs the private diagnostic to classify this "+
			"overload; this test says when that dependency can be removed (see "+
			"docs/hand-runs/2026-09-08-agy-retry.md).\nstream:\n%s",
			agyVersion(t), probe.requests, probe.elapsed.Round(time.Millisecond), probe.stdout)
	}
	t.Logf("agy now passes the provider's refusal through and grain classifies it: %v", capacity)
}

// TestLiveAgyBoundsARefusedTurn drives the production path against the
// same real binary and refusing stub. The schedule probe above bypasses
// turnDeadline on purpose; this one proves the live stream keeps its clock
// armed until the refusal ladder is cancelled, and that cancellation does
// not lose the private diagnostic or its capacity classification.
func TestLiveAgyBoundsARefusedTurn(t *testing.T) {
	requireMeasuredAgy(t)
	agyPath := liveAgyPath(t)
	provider := newRefusingGeminiAPI(t)
	t.Setenv("GOOGLE_GEMINI_BASE_URL", provider.URL)
	f := New(agyPath, buildGrainBinary(t), WithAPIKey("stub-key-not-a-real-credential"), func(f *Framework) {
		f.retry.Retries = 0
	})

	ctx, cancel := context.WithTimeout(context.Background(), DefaultModelRetryCutoff+30*time.Second)
	defer cancel()
	start := time.Now()
	result, err := f.Run(ctx, agent.RunConfig{
		Prompt:      "GRAIN-TURN-DEADLINE-PROBE: reply with the word ok.",
		SandboxRoot: t.TempDir(),
	})
	elapsed := time.Since(start)

	if ctx.Err() != nil {
		t.Fatalf("the %s parent guard stopped agy instead of the %s model-turn deadline", DefaultModelRetryCutoff+30*time.Second, DefaultModelRetryCutoff)
	}
	if provider.count() == 0 {
		t.Fatal("agy sent nothing to the refusing provider before the model-turn deadline")
	}
	if elapsed > DefaultModelRetryCutoff+15*time.Second {
		t.Fatalf("the bounded refusal returned after %s, want it close to the %s model-turn deadline", elapsed.Round(time.Millisecond), DefaultModelRetryCutoff)
	}
	_, ok := agent.Capacity(err)
	if !ok {
		t.Fatalf("Run error after %s = %v, want the cancelled refusal classified as capacity", elapsed.Round(time.Millisecond), err)
	}
	if result == nil || !strings.Contains(result.Transcript, "The model is overloaded") {
		t.Fatalf("the cancelled refusal lost its private diagnostic: %+v", result)
	}
	t.Logf("agy %s was cancelled after %s and %d provider requests; refusal classified as capacity",
		agyVersion(t), elapsed.Round(time.Millisecond), provider.count())
}

// refusedAgyProbe is one exec of agy against a provider that refuses
// everything, read back the way Framework.Run reads it.
type refusedAgyProbe struct {
	stdout       string
	runErr       error
	events       *parsedEvents
	diagnostic   string
	requests     int
	times        []time.Duration
	elapsed      time.Duration
	guardExpired bool
}

// runRefusedAgy execs agy once with the argv, the private HOME and the
// stdin turn a grain dispatch builds, pointed by GOOGLE_GEMINI_BASE_URL
// at a Gemini endpoint that answers every request with the API's own
// overload status.
//
// Framework.Run itself is deliberately not driven here. What is being
// measured is one exec's own behaviour, and Run would cancel its model
// turn at 100 seconds before wrapping the refusal in agent.RetryPolicy's
// 30 and 60 second backoffs. The argv still includes the per-exec
// --log-file production adds, so the only omitted behavior is the bound
// this probe exists to measure the vendor without.
func cachedRefusedAgyProbe(t *testing.T) refusedAgyProbe {
	t.Helper()
	refusedAgyProbeOnce.Do(func() {
		refusedAgyProbeRun = runRefusedAgy(t)
	})
	return refusedAgyProbeRun
}

func runRefusedAgy(t *testing.T) refusedAgyProbe {
	t.Helper()

	agyPath := liveAgyPath(t)
	provider := newRefusingGeminiAPI(t)
	grainBinaryPath := buildGrainBinary(t)

	f := New(agyPath, grainBinaryPath)
	sandboxRoot := t.TempDir()
	cfg := agent.RunConfig{SandboxRoot: sandboxRoot, Prompt: "GRAIN-INTERNAL-RETRY-PROBE: reply with the word ok."}

	// Long enough to capture the slow half of 1.2.2's ladder, including
	// the request measured at +6m30s, while leaving the opt-in package test
	// its own margin to report the result.
	ctx, cancel := context.WithTimeout(context.Background(), refusalProbeRuntime)
	defer cancel()

	mcpArgs, err := f.mcpServerArgs(ctx, cfg)
	if err != nil {
		t.Fatalf("mcpServerArgs: %v", err)
	}
	// The private HOME a dispatch gets, built by the same function: it is
	// what makes agy read GEMINI_API_KEY at all (settingsJSON's
	// modelProvider), and a probe out of some other HOME would be
	// measuring an agy asking for a browser login.
	home, _, cleanup, err := writeAgyHome(context.Background(), agent.LocalHost{}, nil, "", "", grainBinaryPath, mcpArgs, "gemini")
	if err != nil {
		t.Fatalf("building the private HOME a run gets: %v", err)
	}
	t.Cleanup(cleanup)

	env := []string{
		"HOME=" + home,
		"GEMINI_API_KEY=stub-key-not-a-real-credential",
		"GOOGLE_API_KEY=",
		// The one variable agy's own changelog names for pointing the
		// direct Gemini route at an endpoint of one's own; see
		// docs/agy-surface.md's 1.1.9 and 1.1.11 entries.
		"GOOGLE_GEMINI_BASE_URL=" + provider.URL,
	}
	turn, err := userEvent(toolPreamble(cfg) + cfg.Prompt)
	if err != nil {
		t.Fatalf("building the stdin turn: %v", err)
	}

	args := f.agyArgs(ctx, cfg, "")
	logPath, warning := prepareDiagnosticLog(context.Background(), agent.LocalHost{}, home)
	if warning != "" {
		t.Fatal(warning)
	}
	args = append(args, "--log-file", logPath)

	run := hostRunner{host: agent.LocalHost{}, agyPath: agyPath}
	start := time.Now()
	stdout, runErr := run.Run(ctx, args, turn, env, workDir(cfg, home), nil)
	elapsed := time.Since(start)
	diagnostic := readDiagnosticLog(context.Background(), agent.LocalHost{}, logPath, []string{"stub-key-not-a-real-credential"})

	return refusedAgyProbe{
		stdout:       stdout,
		runErr:       runErr,
		events:       parseEvents(stdout),
		diagnostic:   diagnostic,
		requests:     provider.count(),
		times:        provider.elapsedSince(start),
		elapsed:      elapsed,
		guardExpired: ctx.Err() != nil,
	}
}

// refusingGeminiAPI answers every request with the status, the sentence
// and the JSON shape Gemini's own edge uses for an overloaded model, and
// counts them.
//
// The body matters as much as the status: capacity.go matches "model is
// overloaded" and `"status": "UNAVAILABLE"` against text this package
// hopes agy passes through, so a stub that refused with an empty body
// would exercise whatever ladder exists and prove nothing about the
// classification at the end of it.
type refusingGeminiAPI struct {
	*httptest.Server
	mu       sync.Mutex
	requests []time.Time
}

func newRefusingGeminiAPI(t *testing.T) *refusingGeminiAPI {
	t.Helper()
	s := &refusingGeminiAPI{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, time.Now())
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":{"code":503,"message":"The model is overloaded. `+
			`Please try again later.","status":"UNAVAILABLE"}}`)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *refusingGeminiAPI) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func (s *refusingGeminiAPI) elapsedSince(start time.Time) []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	times := make([]time.Duration, len(s.requests))
	for i, at := range s.requests {
		times[i] = at.Sub(start)
	}
	return times
}

// liveAgyPath finds the agy a dispatch would run: GRAIN_AGY_PATH first,
// the way the daemon resolves it, then $PATH.
func liveAgyPath(t *testing.T) string {
	t.Helper()
	if p := strings.TrimSpace(os.Getenv("GRAIN_AGY_PATH")); p != "" {
		return p
	}
	p, err := exec.LookPath("agy")
	if err != nil {
		skipUnlessLiveRequired(t, "no agy binary on $PATH")
	}
	return p
}

// agyVersion is what the binary calls itself, for the skip message above:
// the whole value of that message is telling a reader which agy the
// behaviour was seen on.
//
// It is the first line only, which is how both Dockerfiles and
// .grain/workflows/live-agent.yml check an installed agy against the pin
// (`agy --version | head -n1`); requireMeasuredAgy compares against the
// same pin, so it has to read the version the same way.
func agyVersion(t *testing.T) string {
	t.Helper()
	out, err := exec.Command(liveAgyPath(t), "--version").Output()
	if err != nil {
		return "(version unknown)"
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(line)
}

// buildGrainBinary compiles cmd/grain into t.TempDir() so the private
// HOME above can point agy's MCP config at its "mcpserver" subcommand --
// the same binary a real deployment builds once and reuses across every
// dispatch. (agent/claude and agent/codex each have a twin of this; they
// are three packages, and a helper shared between them would have to live
// in a fourth that only tests import.)
func buildGrainBinary(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "grain")
	cmd := exec.Command("go", "build", "-o", out, "github.com/ai-on-gke/grain/cmd/grain")
	cmd.Dir = repoRoot(t)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building cmd/grain: %v\n%s", err, output)
	}
	return out
}

// repoRoot walks up from the current package directory to the module
// root, so `go build`'s package path resolves the same way regardless of
// which directory `go test` was invoked from.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod above the current test directory")
		}
		dir = parent
	}
}
