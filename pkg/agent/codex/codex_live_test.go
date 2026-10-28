package codex

// The live half of this package's tests: the two assertions that need a
// real `codex` binary, and that nothing driven by this package's `runner`
// seam can make. Everything else here drives a scripted runner, which
// reads whichever file the test told it to read and speaks whichever
// event vocabulary the test wrote down -- a faithful fake of this
// package's own beliefs about codex, and therefore blind to the one
// failure that matters most: a codex that never loads grain's MCP server
// at all. agent/antigravity shipped exactly that for weeks with a green
// suite (README's "Proving a live run actually gets the tools"), and the
// shape of the mistake transplants here directly, since codex has no
// --mcp-config either: the config file grain writes is found by codex
// through CODEX_HOME and nothing else.
//
// .github/workflows/live-frameworks.yml runs both nightly, with a
// credential of its own. A maintainer can run them by hand:
//
//	GRAIN_LIVE_AGENT_TEST=1 OPENAI_API_KEY=... \
//	  go test ./pkg/agent/codex/ -run TestLive -v
//
// GRAIN_LIVE_AGENT_TEST is the opt-in that makes the gates below loud,
// for the reason tests/e2e's live_test.go gives: a skip and a pass are
// the same `ok` in go test's summary, so a run that could not do the one
// thing these tests exist for has to say so rather than printing "ok".

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/mcp"
)

// liveAgentTestEnv is the same opt-in tests/e2e and agent/claude read --
// one name a maintainer has to remember across every live test in this
// repository, and the one the nightly sets.
const liveAgentTestEnv = "GRAIN_LIVE_AGENT_TEST"

func skipUnlessLiveRequired(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv(liveAgentTestEnv) != "" {
		t.Fatalf("%s is set, but %s: this run proved nothing about a live codex", liveAgentTestEnv, reason)
	}
	t.Skipf("%s; skipping the live codex test (set %s=1 to make this a failure)", reason, liveAgentTestEnv)
}

// TestLiveCodexLoadsTheConfigGrainWrites asks a real codex what it loaded
// out of the CODEX_HOME this package builds for one run -- `codex mcp
// list --json`, which reads config.toml and prints the servers it found
// without starting a model turn or spending a token. It is the codex
// analogue of tests/e2e's TestLiveAgyLoadsGrainsHookConfig, and it is
// here for the same reason: what it checks (the file's name, its
// location, and the schema of what this package writes into it) is a fact
// about codex's on-disk layout that drifts when codex ships, and that
// every scripted test in this package asserts rather than measures.
//
// It needs no credential, only the binary, so it is the one live
// assertion that still runs on a machine with no OPENAI_API_KEY.
func TestLiveCodexLoadsTheConfigGrainWrites(t *testing.T) {
	codexPath, err := exec.LookPath("codex")
	if err != nil {
		skipUnlessLiveRequired(t, "no codex binary on $PATH")
	}

	// Built the way Run builds it, rather than by hand: the point is to
	// measure the file a real dispatch would write, so a change to
	// mcpServerArgs or configTOML that codex stops understanding fails
	// here instead of at 3am in a deployment.
	f := newTerminalTestFramework(codexPath, "/usr/local/bin/grain")
	sandboxRoot := t.TempDir()
	mcpArgs, err := f.mcpServerArgs(context.Background(), agent.RunConfig{SandboxRoot: sandboxRoot})
	if err != nil {
		t.Fatalf("mcpServerArgs: %v", err)
	}
	home, _, cleanup, err := f.writeConfig(context.Background(), mcpArgs, "")
	if err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, codexPath, "mcp", "list", "--json")
	// CODEX_HOME and nothing else, exactly as Run passes it: what is
	// under test is that codex finds this run's config there, and an
	// inherited HOME with a real ~/.codex in it would let a passing
	// result mean "found the maintainer's own registration".
	cmd.Env = []string{"CODEX_HOME=" + home, "PATH=" + os.Getenv("PATH")}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("codex mcp list --json: %v\nstdout: %s", err, out)
	}
	// codex writes a warning to stderr for a CODEX_HOME under /tmp
	// ("Refusing to create helper binaries under temporary dir"), which
	// is where os.MkdirTemp puts every run's home. It is not fatal --
	// the helper binaries are shell aliases the MCP path does not use --
	// and it is recorded here so the next person reading a run's stderr
	// does not go looking for a fault behind it.

	var servers []struct {
		Name      string `json:"name"`
		Enabled   bool   `json:"enabled"`
		Transport struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
			URL     string   `json:"url"`
		} `json:"transport"`
	}
	if err := json.Unmarshal(out, &servers); err != nil {
		t.Fatalf("parsing `codex mcp list --json`: %v\noutput: %s", err, out)
	}
	if len(servers) != 1 {
		t.Fatalf("codex loaded %d MCP servers from the home grain wrote, want exactly 1 (a private CODEX_HOME is what makes this run's own server the only one it can see)\noutput: %s", len(servers), out)
	}
	got := servers[0]
	if got.Name != mcp.ToolNamespace {
		t.Errorf("server name = %q, want %q: transcript.go strips this prefix off every reported tool call, so a rename here silently records tool calls under the wrong names", got.Name, mcp.ToolNamespace)
	}
	if !got.Enabled {
		t.Errorf("codex reports grain's server as disabled: %s", out)
	}
	if got.Transport.URL != "http://127.0.0.1:12345/mcp" || got.Transport.Command != "" || len(got.Transport.Args) != 0 {
		t.Errorf("expected HTTP transport without a local command: %+v", got.Transport)
	}
	t.Logf("codex loaded: %s", out)
}

// TestLiveRunEndToEnd drives the real codex binary, a real grain binary's
// "mcpserver" subcommand and a real OpenAI credential through
// Framework.Run, and asserts the thing no fake can: that an unscripted
// model, handed only the tools this package registers, reaches this run's
// own sandbox and changes something in it.
//
// Three separate assumptions ride on it, and each one has already been
// wrong somewhere in this repository for a different agent:
//
//   - that codex authenticates from OPENAI_API_KEY alone, in a private
//     CODEX_HOME holding a config.toml and no auth.json -- rather than
//     wanting a `codex login --with-api-key` first. codex-cli 0.153.0
//     does send it, and drops it silently when it does not parse as a
//     key at all (the API then answers "Missing bearer or basic
//     authentication in header", which reads like a missing credential
//     rather than a rejected one). Whether a *valid* key is accepted on
//     that path is what only a live run can say.
//
//   - that the config file written into that home is one codex reads and
//     starts a server from (the half TestLiveCodexLoadsTheConfigGrainWrites
//     above checks statically, checked here end to end).
//
//   - that a run under sandbox_mode = "read-only" can still write through
//     grain's MCP tools -- codex's sandbox policy applies to its own
//     shell and patch tools, and an MCP child it forks is outside it.
//     If that ever stops being true, every codex dispatch in a deployment
//     fails at the first write, and this is where it should be found.
//
//     GRAIN_LIVE_AGENT_TEST=1 OPENAI_API_KEY=... \
//     go test ./pkg/agent/codex/ -run TestLiveRunEndToEnd -v
func TestLiveRunEndToEnd(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		skipUnlessLiveRequired(t, "OPENAI_API_KEY is not set")
	}
	codexPath, err := exec.LookPath("codex")
	if err != nil {
		skipUnlessLiveRequired(t, "no codex binary on $PATH")
	}

	grainBinaryPath := buildGrainBinary(t)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	f := New(codexPath, grainBinaryPath, WithAPIKey(apiKey))

	root := t.TempDir()
	// The raw event stream is kept, and logged below whichever way this
	// goes: when a live run fails, what codex actually streamed is the
	// only account of why, and a failure message reading "ok.txt is
	// missing" says nothing about whether the model declined, the tools
	// were absent, or the credential was refused.
	transcriptPath := filepath.Join(t.TempDir(), "codex.jsonl")
	result, runErr := f.Run(ctx, agent.RunConfig{
		Prompt: "Use your write_file tool to create a file named ok.txt in your sandbox " +
			"workspace containing exactly the text PONG (no extra whitespace or " +
			"punctuation). Then use read_file to confirm it, and reply with a short " +
			"confirmation once you have done both.",
		SandboxRoot:    root,
		TranscriptPath: transcriptPath,
	})
	if raw, err := os.ReadFile(transcriptPath); err == nil {
		t.Logf("codex event stream:\n%s", raw)
	}
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}

	data, err := os.ReadFile(filepath.Join(root, "ok.txt"))
	if err != nil {
		t.Fatalf("expected ok.txt to have been written inside the sandbox root: %v", err)
	}
	if strings.TrimSpace(string(data)) != "PONG" {
		t.Errorf("ok.txt content = %q, want PONG", string(data))
	}

	var sawWrite bool
	for _, call := range result.ToolCalls {
		if call.Name == "write_file" {
			sawWrite = true
		}
		t.Logf("tool call: %s(%v) -> error=%v text=%q", call.Name, call.Arguments, call.IsError, call.Text)
	}
	if !sawWrite {
		// Recorded under its bare name, which is the other half of what
		// this proves: orchestrator.ProcessResult reads ToolCall.Name to
		// find that a run asked a question or left a closing comment, so
		// a call that landed but arrived here spelled
		// "grain-sandbox__write_file" would be a call the daemon cannot
		// see.
		t.Errorf("expected the model to have called write_file at least once; got calls: %+v", result.ToolCalls)
	}
	if result.FinalText == "" {
		t.Errorf("expected a non-empty final answer")
	}
}

// TestLiveCodexResumesAThreadAcrossExecs is the measurement retry.go's
// resume rests on, kept where a codex release has to walk past it.
//
// What it asks of a real codex is the whole of what resuming a refused
// run depends on, and every part of it is a fact about the binary that
// no scripted runner in this package can check:
//
//   - that `codex exec resume <thread> -` is an invocation this codex
//     performs at all, with the same flags Framework.Run passes an
//     ordinary exec (--json, --skip-git-repo-check, --model) -- which is
//     why the argv comes from codexArgs rather than being written out
//     here. A flag added to a run's argv that resume does not take fails
//     here rather than in the middle of a provider outage.
//   - that it finds the thread in the private CODEX_HOME this package
//     builds for one run, which is the only place an earlier attempt's
//     thread can be: writeConfig makes a fresh directory per run, and
//     nothing here shares one.
//   - that the resumed exec re-announces the *same* thread_id, which is
//     how retry.go knows the resume took rather than quietly starting a
//     new thread.
//   - that the model is actually sent the earlier conversation. This is
//     the point of resuming at all -- keeping the turns already spent --
//     and it is invisible from codex's own event stream, which looks the
//     same either way.
//
// It needs the binary and no credential, like
// TestLiveCodexLoadsTheConfigGrainWrites above, because it points codex
// at a stub of the responses API running in the test process. That stub
// is what makes the last assertion possible at all: it records the
// request bodies, so the test can read what codex sent rather than
// inferring it from what came back. A real OpenAI key would prove less
// here, not more.
//
// What it therefore does not prove: that a resumed thread survives
// anything OpenAI does server-side. codex 0.153 replays the conversation
// from its own on-disk rollout, so there is nothing server-side to
// survive, and if that ever changes this test goes on passing while the
// deployment stops resuming. That is the gap TestLiveRunEndToEnd's own
// credential would have to cover.
func TestLiveCodexResumesAThreadAcrossExecs(t *testing.T) {
	c := newLiveStubbedCodex(t, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// A marker only the first exec sends, so "the model was given the
	// earlier conversation" can be checked by looking for it in the
	// second exec's request rather than taken on trust.
	const marker = "GRAIN-RESUME-PROBE-FIRST-TURN"
	first, err := c.run.Run(ctx, c.f.codexArgs(""), marker, c.env, c.dir, nil)
	if err != nil {
		t.Fatalf("the first exec failed: %v\nstream:\n%s", err, first)
	}
	thread := parseEvents(first).threadID
	if thread == "" {
		t.Fatalf("the first exec announced no thread_id, which is the id a resume is pointed back at\nstream:\n%s", first)
	}

	const nudge = "GRAIN-RESUME-PROBE-SECOND-TURN"
	second, err := c.run.Run(ctx, c.f.codexArgs(thread), nudge, c.env, c.dir, nil)
	if err != nil {
		// The failure retry.go's fallback is written for: an argv this
		// codex will not perform. Said plainly, because the answer is
		// "stop passing it" rather than "look at this run".
		t.Fatalf("`codex exec resume` failed with the argv this package builds (%v): %v\n"+
			"stream:\n%s\n"+
			"If this codex no longer performs that invocation, retry.go's fallback keeps a "+
			"deployment working -- but the resume should then be removed rather than left to "+
			"cost every retry an extra process start.", c.f.codexArgs(thread), err, second)
	}
	if got := parseEvents(second).threadID; got != thread {
		t.Errorf("the resumed exec reported thread %q, want the first exec's %q: a resume that "+
			"quietly starts a new thread keeps none of the turns it was asked to keep\nstream:\n%s", got, thread, second)
	}

	bodies := c.provider.bodies()
	if len(bodies) < 2 {
		t.Fatalf("the stub saw %d request(s), want at least one per exec", len(bodies))
	}
	last := bodies[len(bodies)-1]
	if !strings.Contains(last, marker) {
		t.Errorf("the resumed exec's request does not carry the first exec's own prompt (%s), "+
			"so resuming keeps the thread's name and not its contents -- which is the whole "+
			"saving retry.go is after\nrequest:\n%s", marker, last)
	}
	if !strings.Contains(last, nudge) {
		t.Errorf("the resumed exec's request does not carry the prompt it was given on stdin (%s): "+
			"`codex exec resume <thread> -` did not read the new turn from stdin\nrequest:\n%s", nudge, last)
	}
}

// TestLiveCodexRefusesAThreadItDoesNotHave pins the premise retry.go's
// *fallback* is keyed on, which the probe above does not touch: that a
// resume this codex cannot serve fails before it opens a thread at all.
//
// retry.go reads "asked to resume, died, and announced no thread" as the
// binary refusing the invocation -- it drops the resume for the rest of
// the run, restarts the task in place from its prompt, and says so in
// the transcript. That reading is only sound if the two other things
// codex could do here are things it does not do:
//
//   - fail *after* announcing a thread. The fallback never fires, and a
//     run whose thread went missing ends on codex's error instead of
//     restarting in the sandbox it is already holding -- a provider blip
//     turned into a hard failure, which is the one thing the retry
//     exists to prevent.
//   - quietly start a *fresh* thread instead of failing. Worse: the
//     resumed attempt sends agent.ResumePrompt -- "you were interrupted,
//     carry on" -- into a conversation that has never been told what the
//     task is, and the run spends the rest of its budget on an agent
//     with no idea what it is meant to do. This package would then have
//     to send the whole prompt on every resumed attempt, or verify the
//     thread before trusting it.
//
// Both were measured by hand on 0.153.0
// (docs/hand-runs/2026-09-07-codex-resume.md, section 4); this is that
// section kept where a codex release has to walk past it, which the
// hand-run alone is not. It is agent/claude's
// TestLiveClaudeRefusesASessionItDoesNotHave asked of the other binary,
// and it costs what that one costs: no credential, and no model turn at
// all -- on the build this expects codex never reaches the API, and the
// assertion that the stub saw no request is what says so.
func TestLiveCodexRefusesAThreadItDoesNotHave(t *testing.T) {
	c := newLiveStubbedCodex(t, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// A well-formed thread id, in the shape thread.started reports, that
	// this codex cannot hold: newLiveStubbedCodex builds a fresh
	// CODEX_HOME per probe and nothing has ever run in this one, so there
	// is no rollout behind any id at all. Well-formed on purpose -- a
	// malformed id would be refused by codex's own argument parsing,
	// which is a different refusal from the one retry.go meets, where the
	// id came off a real thread whose rollout has gone.
	const missing = "0195f3c1-77aa-7a04-9b12-6d0e5c4318bf"

	// agent.ResumePrompt rather than the task's prompt, because that is
	// what a resumed attempt actually sends: if a future codex answers
	// this by starting a fresh thread, that is the prompt the thread
	// would be opened with, and the assertions below are about what it
	// would cost.
	stdout, err := c.run.Run(ctx, c.f.codexArgs(missing), agent.ResumePrompt, c.env, c.dir, nil)
	events := parseEvents(stdout)
	t.Logf("resuming %s: err %v, thread %q, sawThread %v", missing, err, events.threadID, events.sawThread)

	switch {
	case err == nil:
		t.Fatalf("codex accepted a thread id it has never seen and ran anyway.\n"+
			"retry.go's resumed attempt sends the short 'carry on' nudge rather than the task, so on "+
			"this build a run whose thread went missing would spend the rest of its budget on an agent "+
			"that was never told what to do.\nstream:\n%s", stdout)
	case events.sawThread:
		t.Fatalf("codex opened a thread (thread.started, id %q) and then failed on a resume it cannot "+
			"serve.\nretry.go reads 'died with no thread event' as the argv being refused, so on this "+
			"build that fallback never fires and a transient refusal becomes a hard failure.\nstream:\n%s",
			events.threadID, stdout)
	}
	if n := len(c.provider.bodies()); n != 0 {
		t.Errorf("codex sent the model %d request(s) for a thread it does not have: it opened a "+
			"conversation of some kind rather than refusing outright, which is what retry.go's "+
			"'no thread event' test assumes it does not do -- and what makes this fallback free", n)
	}
}

// liveStubbedCodex is one probe's worth of live codex: the real binary,
// the argv and configuration a dispatch builds, a private CODEX_HOME to
// keep threads in, and a stub of the responses API standing in for
// OpenAI.
//
// Everything except the provider stanza comes from the functions
// Framework.Run itself uses -- codexArgs, mcpServerArgs, configTOML --
// because what is under test is `codex exec resume` composing with *this
// package's own* configuration, MCP server and sandbox policy included.
// A probe that wrote a simpler config of its own would answer an easier
// question than the one being asked, and a flag added to a run's first
// exec that resume will not take would go on passing here.
type liveStubbedCodex struct {
	f        *Framework
	run      hostRunner
	provider *stubResponsesAPI
	// env carries the CODEX_HOME the config was written into -- fresh
	// per probe, which is what makes "the second exec found the first
	// one's thread" a fact about those two execs and nothing else.
	env []string
	dir string
}

// providerKeys are extra keys for the stub's own provider table --
// noInternalRetries, for a probe about what grain does once codex has
// given up rather than about codex's own ladder. opts are anything else
// the probe's Framework needs; they are applied after the stanza, so a
// probe driving Framework.Run can add its credential and turn the
// backoff off.
func newLiveStubbedCodex(t *testing.T, providerKeys []string, opts ...Option) liveStubbedCodex {
	t.Helper()
	codexPath, err := exec.LookPath("codex")
	if err != nil {
		skipUnlessLiveRequired(t, "no codex binary on $PATH")
	}

	provider := newStubResponsesAPI(t)
	grainBinaryPath := buildGrainBinary(t)

	// The config a real run gets, plus the stanza that points codex at
	// the stub instead of OpenAI (stubProviderConfig). Written by
	// writeConfig rather than by hand here for the reason the argv comes
	// from codexArgs: what is under test is the resume composing with
	// *this package's own* configuration, MCP server and sandbox policy
	// included.
	f := New(codexPath, grainBinaryPath, append([]Option{
		withExtraConfig(stubProviderConfig(provider.URL, providerKeys...)),
	}, opts...)...)
	sandboxRoot := t.TempDir()
	mcpArgs, err := f.mcpServerArgs(context.Background(), agent.RunConfig{SandboxRoot: sandboxRoot})
	if err != nil {
		t.Fatalf("mcpServerArgs: %v", err)
	}
	home, _, cleanup, err := f.writeConfig(context.Background(), mcpArgs, "")
	if err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	// t.Cleanup rather than a defer: this home has to outlive the
	// constructor, and it is the one place a probe's threads live.
	t.Cleanup(cleanup)

	return liveStubbedCodex{
		f:        f,
		run:      hostRunner{host: agent.LocalHost{}, codexPath: codexPath},
		provider: provider,
		env:      []string{"CODEX_HOME=" + home, "OPENAI_API_KEY=" + stubAPIKey},
		dir:      sandboxRoot,
	}
}

// stubResponsesAPI is a stand-in for OpenAI's responses endpoint: it
// records every request body and answers each one with the shortest
// complete turn -- one assistant message and a completed response.
//
// It exists so the resume probes above can read what codex *sent*: for
// one of them because "was the earlier conversation replayed?" is a
// question only the request body answers, and for the other because "was
// the model reached at all?" is. Nothing here models the API beyond what
// codex 0.153 needs to finish a turn; a future codex that wants more
// will fail the probe's first exec, loudly, which is the right way for
// this to stop working.
//
// refuseNext is the one thing it does beyond answering: it lets a probe
// have a turn refused on demand, which is what the retry loop's own live
// tests are built on (live_retry_loop_test.go) and what no real provider
// can be asked for.
type stubResponsesAPI struct {
	*httptest.Server
	mu       sync.Mutex
	requests []string
	refusals int
}

func newStubResponsesAPI(t *testing.T) *stubResponsesAPI {
	t.Helper()
	s := &stubResponsesAPI{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, string(body))
		refuse := s.refusals > 0
		if refuse {
			s.refusals--
		}
		s.mu.Unlock()
		if refuse {
			// Recorded above before being refused: what a refused
			// attempt sent is as much a part of the record as what an
			// answered one did.
			refuseWithOverload(w)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, ev := range []struct {
			name string
			data map[string]any
		}{
			{"response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_stub"}}},
			{"response.output_item.done", map[string]any{"type": "response.output_item.done", "item": map[string]any{
				"type": "message", "role": "assistant", "id": "msg_stub",
				"content": []any{map[string]any{"type": "output_text", "text": "ack"}},
			}}},
			{"response.completed", map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_stub"}}},
		} {
			data, err := json.Marshal(ev.data)
			if err != nil {
				t.Errorf("marshalling the stub's %s event: %v", ev.name, err)
				return
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.name, data)
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// refuseNext makes the next n requests capacity refusals. Set before the
// server is first asked for anything, so there is no window in which a
// probe expecting a refusal gets an answer.
func (s *stubResponsesAPI) refuseNext(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refusals = n
}

func (s *stubResponsesAPI) bodies() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

// refuseWithOverload answers one request the way OpenAI's own edge
// answers an overloaded model: the status and, as much to the point, the
// sentence. capacity.go matches phrases against text codex passes
// through from the provider, so a stub that refused with an empty body
// would prove nothing about the classification at the end of it. Shared
// with live_retry_stub_test.go's refusingResponsesAPI, which answers
// every request this way.
func refuseWithOverload(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	fmt.Fprint(w, `{"error":{"message":"The server is overloaded or not ready yet.","type":"server_error","code":null}}`)
}

// refuseWithRateLimit answers one request the way OpenAI's own edge
// answers a rate limit: a 429, a Retry-After, and the body OpenAI writes
// -- `rate_limit_exceeded` and a "please try again in" of its own.
//
// Every part of that except the status was measured to be dropped by
// codex 0.153.0 before grain sees anything
// (docs/hand-runs/2026-09-08-codex-retry.md §7), which is exactly why
// the stub sends it: the probe built on this
// (TestLiveCodexReportsA429GrainCanClassify) is watching for the day one
// of them survives, since a body carrying `rate_limit_exceeded` reaches
// usagelimit.go first and pauses the whole deployment.
func refuseWithRateLimit(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "20")
	w.WriteHeader(http.StatusTooManyRequests)
	fmt.Fprint(w, `{"error":{"message":"Rate limit reached for gpt-5.1-codex in organization org-stub on tokens per min (TPM): Limit 30000, Used 30000. Please try again in 20s.","type":"tokens","code":"rate_limit_exceeded"}}`)
}

// buildGrainBinary compiles cmd/grain into t.TempDir() so the live test
// above can point a real codex config.toml at its "mcpserver"
// subcommand -- the same binary a real deployment builds once and reuses
// across every dispatch. (agent/claude's own live test has a twin of
// this; they are two packages, and a helper shared between them would
// have to live in a third that only tests import.)
func buildGrainBinary(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "grain")
	cmd := exec.Command("go", "build", "-o", out, "github.com/bwsalmon/grain/cmd/grain")
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
