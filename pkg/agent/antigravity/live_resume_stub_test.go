package antigravity

// The measurement resume.go rests on, taken without a credential.
//
// tests/e2e/live_resume_test.go asks a real agy and a real Gemini whether
// a second exec can pick up the first one's conversation. It is the
// better question and it stays; what it needs is GEMINI_API_KEY, and no
// such secret has ever reached .grain/workflows/live-agent.yml's job --
// so until this file, every claim `--conversation` rests on was neither
// green nor red anywhere, and resume.go said so at length.
//
// These probes need the binary and nothing else. They point agy at a stub
// of Gemini's own generateContent endpoint inside the test process
// (GOOGLE_GEMINI_BASE_URL, the same seam live_retry_stub_test.go refuses
// everything through), which is what pkg/agent/claude and pkg/agent/codex
// each do with their own vendor's API and for the same reason. Everything
// about resuming that lives on this side of the network is then
// measurable here, and the three questions a *cross-dispatch* resume asks
// are all on this side:
//
//   - whether `--conversation <id>` composes with the argv a grain
//     dispatch builds -- `--input-format stream-json --output-format
//     stream-json --dangerously-skip-permissions --disable-slash-commands
//     --print-timeout ... --model ... --effort ... --add-dir ...`, the
//     turn on stdin. docs/agy-surface.md records that the flag exists,
//     not that it composes with the one input format grain uses.
//   - whether agy resolves a conversation out of the HOME it is given
//     when the *working directory* is not the one that wrote it. A
//     dispatch gets a new sandbox every time and the previous one is
//     gone, so this is the question that decides whether a task's session
//     store buys anything at all.
//   - whether the resumed exec is actually *sent the earlier
//     conversation*, which is invisible from agy's own stream: a
//     conversation that came back empty looks exactly like one that came
//     back whole. The stub records request bodies, so this is read rather
//     than inferred -- and a real credential would prove it less well,
//     not better.
//
// And one the fallback rests on: what agy does with a conversation id it
// cannot find, which is what a dispatch hands it after grain's own
// pruning has removed one.
//
// By hand, wherever an agy binary is available -- no key, no network:
//
//	GRAIN_LIVE_AGENT_TEST=1 go test ./pkg/agent/antigravity/ \
//	  -run 'TestLiveAgyResumes|TestLiveAgyOpensANewConversation' -count=1 -v
//
// docs/hand-runs/2026-09-17-agy-resume.md is the run these were written
// from, against agy 1.1.27 (the Dockerfile's pinned release) and 1.2.5
// (what antigravity.google/cli/install.sh served that day, and therefore
// what a freshly built k8s sandbox image carries).

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

	"github.com/ai-on-gke/grain/pkg/agent"
)

// The two markers. Only the first exec sends one, only the second sends
// the other, and a request body carrying both is a conversation that was
// replayed rather than a name that survived.
const (
	resumeProbeFirstTurn  = "GRAIN-AGY-RESUME-PROBE-FIRST-TURN-ZANZIBAR"
	resumeProbeSecondTurn = "GRAIN-AGY-RESUME-PROBE-SECOND-TURN-CARRY-ON"
)

// TestLiveAgyResumesAConversationFromAnotherSandbox is the cross-dispatch
// resume, measured: one exec in a sandbox that is then deleted, a second
// exec in a different sandbox out of the same HOME, and the question of
// whether the second was given the first one's conversation.
//
// The control is not optional. An exec that knows the first turn proves
// nothing unless an exec out of the same HOME that was *not* told to
// resume does not know it -- a HOME-wide memory, or an agy that continues
// the most recent conversation by default, would otherwise pass this
// while `--conversation` did nothing at all.
func TestLiveAgyResumesAConversationFromAnotherSandbox(t *testing.T) {
	p := newResumeProbe(t)

	// Dispatch one, in its own sandbox.
	first := p.exec(t, p.sandboxA, "", resumeProbeFirstTurn+": reply with nothing but the word ok.")
	if first.err != nil {
		t.Fatalf("the first exec failed (%v).\nNothing about resuming has been measured yet; "+
			"read the capture for what went wrong between this package's argv and this agy:\n%s\nstderr:\n%s",
			first.err, first.stdout, first.stderr)
	}
	conversation := first.events.conversationID
	if conversation == "" {
		t.Fatalf("agy's stream carried no conversation_id, so there is nothing to resume by:\n%s", first.stdout)
	}
	t.Logf("agy %s: first exec ran in conversation %s", agyVersion(t), conversation)

	// The sandbox that conversation was written in is gone by the time
	// the task comes round again, which is the whole difference between
	// this and the in-run resume resume_test.go covers.
	if err := os.RemoveAll(p.sandboxA); err != nil {
		t.Fatal(err)
	}

	second := p.exec(t, p.sandboxB, conversation, resumeProbeSecondTurn+": reply with nothing but the word ok.")
	if second.err != nil {
		t.Fatalf("agy refused the resumed invocation (%v).\n"+
			"--conversation does not compose with --input-format stream-json on %s, so "+
			"agent/antigravity should stop passing it and stop filling in "+
			"agent.Result.ResumableSession.\nstdout:\n%s\nstderr:\n%s",
			second.err, agyVersion(t), second.stdout, second.stderr)
	}
	if !second.events.sawInit {
		t.Fatalf("the resumed exec opened no session at all (resume.go reads that as the argv being "+
			"refused).\nstdout:\n%s\nstderr:\n%s", second.stdout, second.stderr)
	}
	if second.events.conversationID != conversation {
		t.Fatalf("the resumed exec reported conversation %s, want %s: agy opened a session of its own "+
			"rather than continuing the one in the HOME it was given, from a working directory that did "+
			"not write it.\nstdout:\n%s\nstderr:\n%s",
			second.events.conversationID, conversation, second.stdout, second.stderr)
	}

	// The question the stream cannot answer: was the conversation
	// actually sent to the model?
	turn := second.lastTurnRequest(t)
	if !strings.Contains(turn.body, resumeProbeFirstTurn) {
		t.Fatalf("the resumed exec kept the conversation's name and lost its contents: the model request "+
			"it sent carries the new turn but not %s, so a resumed dispatch would buy nothing over "+
			"starting the task again.\nrequest:\n%s", resumeProbeFirstTurn, turn.summary())
	}
	if !strings.Contains(turn.body, resumeProbeSecondTurn) {
		t.Fatalf("the resumed exec's model request does not carry the turn this exec sent:\n%s", turn.summary())
	}
	t.Logf("the resumed exec replayed %d conversation entries, the earlier turn included:\n%s",
		len(turn.contents(t)), turn.summary())

	// The control, out of the same HOME and the same new sandbox.
	control := p.exec(t, p.sandboxB, "", resumeProbeSecondTurn+": reply with nothing but the word ok.")
	if control.err != nil {
		t.Fatalf("the control exec failed (%v):\n%s\nstderr:\n%s", control.err, control.stdout, control.stderr)
	}
	if control.events.conversationID == conversation {
		t.Fatalf("an exec that passed no --conversation ran in %s anyway, so the assertion above says "+
			"nothing about the flag", conversation)
	}
	if body := control.lastTurnRequest(t).body; strings.Contains(body, resumeProbeFirstTurn) {
		t.Fatalf("an exec that was never asked to resume anything was sent the first turn as well, so "+
			"the replay above is not evidence that --conversation did anything:\n%s",
			control.lastTurnRequest(t).summary())
	}
}

// TestLiveAgyResumesWithoutTheSummariesIndex asks what a conversation has
// to travel with, which is the third thing resume.go could not answer.
//
// A grain sandbox is new every dispatch and the HOME is restored from the
// task's session store (pkg/agent/sessionstore), which keeps everything
// the CLI wrote and nothing grain wrote. Beside conversations/<id>.db agy
// leaves a conversation_summaries.db, a jetbox_summaries_proto.pb, a
// cache/last_conversations.json and a brain/<id>/ tree, and whether a
// resume needs any of them was unknown -- "grain kept the directory" is
// not the same claim as "agy can still open the conversation".
//
// Two shapes, and the second is deliberately harsher than anything
// production does: the whole HOME with the summaries index removed, and a
// freshly written HOME carrying nothing of the old one but the single
// conversation database.
func TestLiveAgyResumesWithoutTheSummariesIndex(t *testing.T) {
	p := newResumeProbe(t)

	first := p.exec(t, p.sandboxA, "", resumeProbeFirstTurn+": reply with nothing but the word ok.")
	if first.err != nil {
		t.Fatalf("the first exec failed (%v):\n%s\nstderr:\n%s", first.err, first.stdout, first.stderr)
	}
	conversation := first.events.conversationID
	if conversation == "" {
		t.Fatalf("agy's stream carried no conversation_id:\n%s", first.stdout)
	}
	if err := os.RemoveAll(p.sandboxA); err != nil {
		t.Fatal(err)
	}

	summaries := filepath.Join(agyStateDir, "conversation_summaries.db")
	for _, shape := range []struct {
		name string
		home func(t *testing.T) string
	}{
		{
			// Everything agy wrote, less the indexes that name a
			// conversation without holding it.
			name: "the whole HOME without its summary indexes",
			home: func(t *testing.T) string {
				home := filepath.Join(p.root, "home-without-summaries")
				copyTree(t, p.home, home)
				for _, rel := range []string{
					summaries,
					filepath.Join(agyStateDir, "jetbox_summaries_proto.pb"),
					filepath.Join(agyStateDir, "cache", "last_conversations.json"),
				} {
					if err := os.RemoveAll(filepath.Join(home, rel)); err != nil {
						t.Fatal(err)
					}
				}
				return home
			},
		},
		{
			// The harsher shape: a HOME this package has just written
			// from scratch, at a path agy has never seen, holding one
			// file of the old one.
			name: "a fresh HOME holding only conversations/<id>.db",
			home: func(t *testing.T) string {
				home, _, cleanup, err := writeAgyHome(context.Background(), agent.LocalHost{}, nil, "", "",
					p.grainBinaryPath, []string{"mcpserver", "-sandbox-root", p.sandboxB}, "gemini")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(cleanup)
				dir := filepath.Join(home, agyStateDir, "conversations")
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				copyTree(t, filepath.Join(p.home, agyStateDir, "conversations", conversation+".db"),
					filepath.Join(dir, conversation+".db"))
				return home
			},
		},
	} {
		t.Run(shape.name, func(t *testing.T) {
			home := shape.home(t)
			run := p.execIn(t, home, p.sandboxB, conversation,
				resumeProbeSecondTurn+": reply with nothing but the word ok.")
			if run.err != nil {
				t.Fatalf("agy %s refused to resume out of %s (%v):\n%s\nstderr:\n%s",
					agyVersion(t), shape.name, run.err, run.stdout, run.stderr)
			}
			if run.events.conversationID != conversation {
				t.Fatalf("resuming out of %s ran in conversation %s, want %s: what the conversation needs "+
					"beside itself is more than pkg/agent/sessionstore keeps, and resume.go's doc comment "+
					"has to say so.\nstdout:\n%s\nstderr:\n%s",
					shape.name, run.events.conversationID, conversation, run.stdout, run.stderr)
			}
			if body := run.lastTurnRequest(t).body; !strings.Contains(body, resumeProbeFirstTurn) {
				t.Fatalf("resuming out of %s kept the conversation's name and lost its contents:\n%s",
					shape.name, run.lastTurnRequest(t).summary())
			}
			if _, err := os.Stat(filepath.Join(home, summaries)); err == nil {
				t.Logf("%s: agy rebuilt conversation_summaries.db for itself", shape.name)
			}
		})
	}
}

// TestLiveAgyOpensANewConversationForOneItCannotFind is the premise
// resume.go's cross-dispatch fallback is keyed on, and the one the
// measurement answered against this repository's guess.
//
// A dispatch may hand agy a conversation grain's own pruning has since
// removed. resume.go expected that to look like a refusal -- an exec that
// fails with no init event, which is how a codex or claude that will not
// take an argv behaves -- and to be reported as
// agent.SuspendedResumeRefusedNote. What agy does instead is open a *new*
// conversation, with a new id, and exit successfully: the run is fine
// (the suspended turn carries the whole prompt, so nothing is lost but
// the earlier thinking), and the only way to know the resume did not take
// is that the id on the stream is not the id that was asked for.
//
// If a later agy starts failing this invocation instead, that is the
// refusal resume.go's fallback was written for and this test says so.
func TestLiveAgyOpensANewConversationForOneItCannotFind(t *testing.T) {
	p := newResumeProbe(t)

	const missing = "00000000-0000-0000-0000-000000000000"
	run := p.exec(t, p.sandboxA, missing, resumeProbeSecondTurn+": reply with nothing but the word ok.")
	t.Logf("agy %s asked for a conversation that does not exist: err=%v, conversation=%q, sawInit=%v",
		agyVersion(t), run.err, run.events.conversationID, run.events.sawInit)
	if run.stderr != "" {
		t.Logf("stderr: %s", strings.TrimSpace(run.stderr))
	}

	if run.err != nil || !run.events.sawInit {
		// The behaviour resume.go guessed at. Still a supported outcome
		// -- the fallback is written for it -- but a changed one, and the
		// doc comment claims the opposite is measured.
		t.Fatalf("agy %s now refuses a conversation it cannot find (err=%v, sawInit=%v) rather than "+
			"opening a new one. That is what resume.go's SuspendedResumeRefusedNote path was written "+
			"for, so the behaviour is handled -- but resume.go's doc comment records the opposite as "+
			"measured and has to be corrected.\nstdout:\n%s\nstderr:\n%s",
			agyVersion(t), run.err, run.events.sawInit, run.stdout, run.stderr)
	}
	if run.events.conversationID == "" {
		t.Fatalf("agy neither refused the invocation nor named a conversation, so a dispatch cannot "+
			"tell whether the resume took:\n%s", run.stdout)
	}
	if run.events.conversationID == missing {
		t.Fatalf("agy adopted the id it was given (%s) for a conversation it never had, so comparing the "+
			"reported conversation with the requested one no longer detects a resume that did not take -- "+
			"which is what runAttempts does.\n%s", missing, run.stdout)
	}
	if body := run.lastTurnRequest(t).body; strings.Contains(body, resumeProbeFirstTurn) {
		t.Fatalf("a conversation agy never had came back with contents:\n%s", run.lastTurnRequest(t).summary())
	}
}

// agyStateDir is agy's own directory under the HOME it is given: where
// the conversations, their indexes and everything else the CLI writes for
// itself live. pkg/agent/sessionstore keeps all of it between dispatches.
const agyStateDir = ".gemini/antigravity-cli"

// resumeProbe is the fixture the three tests share: a real agy, a stub
// Gemini that answers every turn and records what it was sent, the
// private HOME writeAgyHome builds, and two sandboxes standing in for two
// dispatches.
type resumeProbe struct {
	agyPath         string
	grainBinaryPath string
	provider        *answeringGeminiAPI
	framework       *Framework
	root            string
	home            string
	sandboxA        string
	sandboxB        string
	// allowSelfUpdate drops AGY_CLI_DISABLE_AUTO_UPDATE from the
	// production environment below, which only the control exec in
	// live_selfupdate_stub_test.go wants: everything else here measures
	// the binary a dispatch would actually run, and that one is pinned.
	allowSelfUpdate bool
}

func newResumeProbe(t *testing.T) *resumeProbe {
	t.Helper()
	return newAgyProbe(t, liveAgyPath(t))
}

// newAgyProbe is newResumeProbe against a named binary rather than the
// installed one -- what live_selfupdate_stub_test.go needs, since the agy
// an image installs is deliberately not writable and the question there
// is what agy does to a copy that is.
func newAgyProbe(t *testing.T, agyPath string) *resumeProbe {
	t.Helper()
	p := &resumeProbe{
		agyPath:         agyPath,
		grainBinaryPath: buildGrainBinary(t),
		provider:        newAnsweringGeminiAPI(t),
		root:            t.TempDir(),
	}
	p.framework = New(p.agyPath, p.grainBinaryPath)
	p.sandboxA = filepath.Join(p.root, "sandbox-a")
	p.sandboxB = filepath.Join(p.root, "sandbox-b")
	for _, dir := range []string{p.sandboxA, p.sandboxB} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	home, _, cleanup, err := writeAgyHome(context.Background(), agent.LocalHost{}, nil, "", "",
		p.grainBinaryPath, []string{"mcpserver", "-sandbox-root", p.sandboxA}, "gemini")
	if err != nil {
		t.Fatalf("building the private HOME a run gets: %v", err)
	}
	t.Cleanup(cleanup)
	p.home = home
	return p
}

func (p *resumeProbe) exec(t *testing.T, dir, conversation, prompt string) agyExec {
	t.Helper()
	return p.execIn(t, p.home, dir, conversation, prompt)
}

// execIn runs agy once the way a dispatch does: the production argv
// (agyArgs), the production environment, the turn on stdin as a
// stream-json user event, and this run's own configuration written into
// the HOME first -- which is what Framework.Run does between execs, agy
// having rewritten part of it as it started.
func (p *resumeProbe) execIn(t *testing.T, home, dir, conversation, prompt string) agyExec {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cfg := agent.RunConfig{SandboxRoot: dir, Prompt: prompt}
	mcpArgs, err := p.framework.mcpServerArgs(ctx, cfg)
	if err != nil {
		t.Fatalf("building the MCP argv: %v", err)
	}
	if err := refreshAgyHome(context.Background(), agent.LocalHost{}, home, p.grainBinaryPath, mcpArgs, "gemini"); err != nil {
		t.Fatalf("writing this run's configuration into the HOME: %v", err)
	}
	turn, err := userEvent(prompt)
	if err != nil {
		t.Fatalf("building the stdin turn: %v", err)
	}

	args := p.framework.agyArgs(ctx, cfg, conversation)
	before := p.provider.count()
	cmd := exec.CommandContext(ctx, p.agyPath, args...)
	cmd.Dir = dir
	// The environment production builds -- the private HOME, the key with
	// GOOGLE_API_KEY cleared beside it, and agy's own updater switched
	// off (agyEnv, selfupdate.go) -- plus the two entries only a probe
	// needs: a PATH, since this env replaces the test process's own
	// rather than adding to it the way agent.Host does, and agy's
	// variable for pointing the direct Gemini route at an endpoint of
	// one's own (docs/agy-surface.md's 1.1.9 and 1.1.11 changelog
	// entries).
	env := agyEnv(home, stubAPIKey)
	if p.allowSelfUpdate {
		env = slices.DeleteFunc(env, func(e string) bool { return e == disableAgyAutoUpdate })
	}
	cmd.Env = append(env,
		"PATH="+os.Getenv("PATH"),
		"GOOGLE_GEMINI_BASE_URL="+p.provider.URL,
	)
	cmd.Stdin = strings.NewReader(turn)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()

	run := agyExec{stdout: stdout.String(), stderr: stderr.String(), err: err}
	run.events = parseEvents(run.stdout)
	run.requests = p.provider.since(before)
	return run
}

// agyExec is one exec of agy read back three ways: the stream grain
// parses, what agy said on stderr, and what it sent the model.
type agyExec struct {
	stdout   string
	stderr   string
	err      error
	events   *parsedEvents
	requests []modelRequest
}

// lastTurnRequest is the model request this exec's turn produced, which
// is not the last request it made: agy sends a short side call to a
// flash-lite model to title the conversation alongside every turn, and
// that call carries the new prompt alone whatever the conversation holds.
func (e agyExec) lastTurnRequest(t *testing.T) modelRequest {
	t.Helper()
	for i := len(e.requests) - 1; i >= 0; i-- {
		if !strings.Contains(e.requests[i].path, "flash-lite") {
			return e.requests[i]
		}
	}
	t.Fatalf("this exec sent the model nothing at all; it made %d request(s):\n%s\nstderr:\n%s",
		len(e.requests), e.stdout, e.stderr)
	return modelRequest{}
}

// modelRequest is one request the stub was sent, kept whole: the body is
// where "was the earlier conversation replayed?" is actually written.
type modelRequest struct {
	path string
	body string
}

type geminiContent struct {
	Role  string `json:"role"`
	Parts []struct {
		Text string `json:"text"`
	} `json:"parts"`
}

func (r modelRequest) contents(t *testing.T) []geminiContent {
	t.Helper()
	var doc struct {
		Contents []geminiContent `json:"contents"`
	}
	if err := json.Unmarshal([]byte(r.body), &doc); err != nil {
		t.Fatalf("the request agy sent is not the JSON this probe reads (%v):\n%s", err, r.body)
	}
	return doc.Contents
}

// summary is the request as a failure message wants it: the conversation
// agy sent, one line per entry, without the 70KB of system instruction
// and tool declarations around it.
func (r modelRequest) summary() string {
	var doc struct {
		Contents []geminiContent `json:"contents"`
	}
	if err := json.Unmarshal([]byte(r.body), &doc); err != nil {
		return r.path + ": " + r.body
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s, %d conversation entries:", r.path, len(doc.Contents))
	for _, c := range doc.Contents {
		for _, part := range c.Parts {
			text := strings.ReplaceAll(part.Text, "\n", " ")
			if len(text) > 200 {
				text = text[:200] + "…"
			}
			fmt.Fprintf(&b, "\n  %s: %s", c.Role, text)
		}
	}
	return b.String()
}

// stubAPIKey is what makes agy take the direct Gemini route at all
// (settingsJSON's modelProvider), and is not a credential: the endpoint
// it authenticates to is in this process.
const stubAPIKey = "stub-key-not-a-real-credential"

// answeringGeminiAPI is the other half of live_retry_stub_test.go's
// refusing stub: it answers every request with one complete SSE turn, so
// a conversation is actually written to disk and can be resumed, and it
// keeps every request body for the assertions above.
type answeringGeminiAPI struct {
	*httptest.Server
	mu       sync.Mutex
	requests []modelRequest
}

func newAnsweringGeminiAPI(t *testing.T) *answeringGeminiAPI {
	t.Helper()
	s := &answeringGeminiAPI{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, modelRequest{path: r.URL.Path, body: string(body)})
		n := len(s.requests)
		s.mu.Unlock()

		answer, err := json.Marshal(map[string]any{
			"candidates": []any{map[string]any{
				"content":      map[string]any{"role": "model", "parts": []any{map[string]any{"text": "ok"}}},
				"finishReason": "STOP",
				"index":        0,
			}},
			"usageMetadata": map[string]any{"promptTokenCount": 1, "candidatesTokenCount": 1, "totalTokenCount": 2},
			"modelVersion":  "gemini-3.1-pro",
			"responseId":    fmt.Sprintf("grain-stub-%d", n),
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", answer)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *answeringGeminiAPI) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func (s *answeringGeminiAPI) since(n int) []modelRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]modelRequest(nil), s.requests[n:]...)
}

// copyTree copies a file or directory as it stands, permissions and all,
// which is what a session store restoring a task's HOME amounts to.
func copyTree(t *testing.T, from, to string) {
	t.Helper()
	if out, err := exec.Command("cp", "-a", from, to).CombinedOutput(); err != nil {
		t.Fatalf("cp -a %s %s: %v\n%s", from, to, err, out)
	}
}
