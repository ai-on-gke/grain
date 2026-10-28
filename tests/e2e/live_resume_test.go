// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package e2e

// The measurement agent/antigravity's resume.go is built on, and the one
// thing about it nothing in this repository could otherwise prove.
//
// Framework.Run now execs agy a second time when the provider refuses a
// run mid-flight, passing --conversation <id> so the retry picks up the
// conversation the first exec left rather than starting the task again.
// docs/agy-surface.md records that the flag exists ("Resume a previous
// conversation by ID"); it records nothing about whether it composes with
// --input-format stream-json and a fresh user turn on stdin, which is the
// exact shape every grain run uses. A stub agy cannot answer that -- it
// answers whatever the test told it to -- so the answer has to come from
// the real binary, which is what this file asks for.
//
// Most of it is now answered without a credential, and answered in this
// code's favour: pkg/agent/antigravity/live_resume_stub_test.go puts the
// same questions to a real agy with a stub of Gemini's own endpoint
// behind it, so what a resumed exec *sent* can be read rather than
// inferred, and it covers the cross-dispatch case this file does not
// (a conversation resumed from a different sandbox, out of a HOME with
// its summary indexes gone). docs/hand-runs/2026-09-17-agy-resume.md has
// the measurement. What remains this file's alone is the one thing a
// stub cannot see: whether Google's own side of a resumed conversation
// survives. On agy 1.1.27 and 1.2.5 that is nothing -- the conversation
// is replayed out of agy's own on-disk database -- so a red run here,
// against a green stub probe, would mean a build that had started
// keeping conversations somewhere grain cannot carry.
//
// It costs three short model turns and no sandbox: what is under test is
// agy's session handling, not an agent doing work, so the run is given a
// nonce to remember and asked for it back. The third turn is the control,
// and it is not optional: a second exec that knows the nonce proves
// nothing unless a second exec that was *not* told to resume does not.
//
// # What a failure here means
//
// (A red run, that is, from this test. A live-agent.yml run that fails
// before it reaches any of this measures nothing: every scheduled run of
// that workflow so far has died at its "Require a credential" step --
// including the run of 2026-09-07, the first whose checkout contained
// this file -- so it has never executed against a real agy at all. See
// pkg/agent/antigravity/resume.go's header.)
//
// resume.go already degrades safely -- an exec that passes
// --conversation and dies without agy opening a session is read as the
// argv being refused, and the run retries from scratch in the same
// sandbox instead. So a red run here is not a broken deployment; it is
// grain paying for a process start it will never get anything back for,
// on every transient refusal. The fix is to stop passing the flag and
// keep the cheap variant, which is a two-line change in agyArgs.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
)

func TestLiveAgyResumesAConversationAcrossExecs(t *testing.T) {
	t.Parallel()
	agyPath := liveAgyBinary(t)
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		skipUnlessLiveRequired(t, "GEMINI_API_KEY is not set")
	}

	grainPath := buildGrainBinary(t)
	// The same private HOME a dispatch gets, built by the same function,
	// because it is where agy keeps its conversations: a resume that
	// worked out of some other HOME would say nothing about a grain run.
	mcpArgs := []string{"mcpserver", "-sandbox-root", t.TempDir()}
	home, cleanup, err := antigravity.WriteRunHomeForTest(context.Background(), grainPath, mcpArgs, true)
	if err != nil {
		t.Fatalf("building the private HOME a run gets: %v", err)
	}
	t.Cleanup(cleanup)

	nonce := liveNonce(t)

	// Turn one: the conversation this test is going to try to get back.
	first := runLiveAgy(t, agyPath, home, apiKey, nil,
		"Remember this token, exactly as written: "+nonce+
			". Do not use any tools. Reply with nothing but the word ok.")
	if first.conversationID == "" {
		t.Fatalf("agy's stream carried no conversation_id, so there is nothing to resume by.\n"+
			"agent/antigravity reads it off the init, step_update and result events; this capture had none of it:\n%s",
			first.capture)
	}
	t.Logf("first exec: conversation %s, status %s", first.conversationID, first.status)

	// What Framework.Run does between attempts, and for the reason
	// TestLiveAgyLoadsGrainsPermissionRules records: agy rewrites part of
	// the configuration it read as it starts.
	if err := antigravity.RefreshRunHomeForTest(context.Background(), home, grainPath, mcpArgs, true); err != nil {
		t.Fatalf("refreshing the run's HOME between execs: %v", err)
	}

	// Turn two: the whole question. Same argv, same HOME, plus
	// --conversation, with a fresh user turn on stdin.
	second := runLiveAgy(t, agyPath, home, apiKey, []string{"--conversation", first.conversationID},
		"What was the token I asked you to remember? Do not use any tools. Reply with nothing but that token.")
	t.Logf("second exec: conversation %s, status %s, response %q", second.conversationID, second.status, second.response)

	if second.err != nil {
		t.Fatalf("agy refused the resumed invocation (%v).\n"+
			"--conversation does not compose with --input-format stream-json on this build, so "+
			"agent/antigravity should stop passing it and keep the fallback resume.go already has: "+
			"retry from scratch in the same sandbox. Capture:\n%s", second.err, second.capture)
	}
	if !strings.Contains(strings.ToLower(second.response), strings.ToLower(nonce)) {
		t.Fatalf("the resumed exec did not know the token %s it had just been given; it answered %q.\n"+
			"agy accepted --conversation and started a session that remembers nothing of the first, which "+
			"buys a grain retry nothing over starting again. Capture:\n%s", nonce, second.response, second.capture)
	}
	if second.conversationID != first.conversationID {
		t.Errorf("the resumed exec reported conversation %s, want %s: it opened a session of its own rather "+
			"than continuing the one it was pointed at", second.conversationID, first.conversationID)
	}

	// The control. Without it, a model that happened to answer with the
	// nonce for any other reason -- a HOME-wide memory, an echo of its
	// own prompt -- would pass this test while proving nothing.
	if err := antigravity.RefreshRunHomeForTest(context.Background(), home, grainPath, mcpArgs, true); err != nil {
		t.Fatalf("refreshing the run's HOME before the control: %v", err)
	}
	control := runLiveAgy(t, agyPath, home, apiKey, nil,
		"What was the token I asked you to remember? Do not use any tools. Reply with nothing but that token, "+
			"or with the word none if you were never given one.")
	t.Logf("control exec: conversation %s, response %q", control.conversationID, control.response)
	if strings.Contains(strings.ToLower(control.response), strings.ToLower(nonce)) {
		t.Fatalf("an exec that was never asked to resume anything also knew the token %s (%q).\n"+
			"So the assertion above is not evidence that --conversation did anything, and this probe needs "+
			"rebuilding before resume.go can be said to rest on a measurement.", nonce, control.response)
	}
}

// liveAgyRun is one exec of agy, read back the way agent/antigravity
// reads it.
type liveAgyRun struct {
	capture        string
	conversationID string
	status         string
	response       string
	err            error
}

// runLiveAgy execs agy once with the argv a grain dispatch uses --
// deliberately spelled out here rather than borrowed from Framework.Run,
// which builds it for a whole run and would hide the one flag under test
// behind its own decisions. extra is what this exec adds to it.
//
// The prompt goes on stdin as a stream-json user event, which is both the
// shape Run uses and the reason this test exists: the flag is documented
// against print mode generally, and print mode has two input formats.
func runLiveAgy(t *testing.T, agyPath, home, apiKey string, extra []string, prompt string) liveAgyRun {
	t.Helper()

	args := []string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--dangerously-skip-permissions",
		"--disable-slash-commands",
		"--print-timeout", "180s",
	}
	args = append(args, extra...)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	userEvent, err := json.Marshal(map[string]any{
		"event":   "user",
		"message": map[string]any{"role": "user", "content": prompt},
	})
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(ctx, agyPath, args...)
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "GEMINI_API_KEY=" + apiKey, "GOOGLE_API_KEY="}
	cmd.Stdin = bytes.NewReader(append(userEvent, '\n'))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	run := liveAgyRun{}
	if err := cmd.Run(); err != nil {
		run.err = err
		t.Logf("agy %v exited with %v; stderr:\n%s", args, err, stderr.String())
	}
	run.capture = stdout.String()
	run.conversationID, run.status, run.response = readAgyStream(run.capture)
	return run
}

// readAgyStream pulls the three facts this test needs off a stream-json
// capture: the session agy ran it in, its terminal status, and its final
// answer. It models only those fields, and skips a line it cannot parse,
// for the same reason agent/antigravity's own parser does.
//
// The conversation id is read off all three event shapes agy writes it
// in -- top level on init, nested inside step_update and result -- which
// is the same reading agent/antigravity does, asserted against this
// binary rather than against a fixture.
func readAgyStream(capture string) (conversationID, status, response string) {
	for _, line := range strings.Split(capture, "\n") {
		var ev agyStreamLine
		if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &ev); err != nil {
			continue
		}
		for _, id := range []string{ev.ConversationID, ev.Step.id(), ev.Result.id()} {
			if id != "" {
				conversationID = id
			}
		}
		if ev.Event == "result" && ev.Result != nil {
			status, response = ev.Result.Status, ev.Result.Response
		}
	}
	return conversationID, status, response
}

type agyStreamLine struct {
	Event          string         `json:"event"`
	ConversationID string         `json:"conversation_id"`
	Step           *agyStepLine   `json:"step_update"`
	Result         *agyResultLine `json:"result"`
}

type agyStepLine struct {
	ConversationID string `json:"conversation_id"`
}

type agyResultLine struct {
	ConversationID string `json:"conversation_id"`
	Status         string `json:"status"`
	Response       string `json:"response"`
}

func (s *agyStepLine) id() string {
	if s == nil {
		return ""
	}
	return s.ConversationID
}

func (r *agyResultLine) id() string {
	if r == nil {
		return ""
	}
	return r.ConversationID
}

// The live assertion above runs nightly at most, so nothing would
// otherwise notice a typo in the one thing it reads -- and a broken
// reader fails the live run for a reason that has nothing to do with
// what the live run was there to check. The same argument
// TestAdvertisedToolsReadsAgysInitEvent makes, against the same event
// shapes.
func TestReadAgyStreamFindsTheConversationWhereverAgyWritesIt(t *testing.T) {
	t.Parallel()
	capture := strings.Join([]string{
		`{"event":"init","conversation_id":"conv-1","init":{"tools":[]}}`,
		`{"event":"step_update","step_update":{"conversation_id":"conv-1","step_index":0,"state":"DONE","step_type":"agent_response"}}`,
		`{"event":"result","result":{"conversation_id":"conv-1","status":"SUCCESS","response":"grain-abc"}}`,
	}, "\n") + "\n"

	id, status, response := readAgyStream(capture)
	if id != "conv-1" || status != "SUCCESS" || response != "grain-abc" {
		t.Errorf("readAgyStream = %q, %q, %q; want conv-1, SUCCESS, grain-abc", id, status, response)
	}

	// Each shape on its own, since a build that drops the id from one of
	// them must not leave this test reading nothing.
	for _, line := range strings.Split(strings.TrimSpace(capture), "\n") {
		if id, _, _ := readAgyStream(line + "\n"); id != "conv-1" {
			t.Errorf("readAgyStream(%s) found conversation %q, want conv-1", line, id)
		}
	}

	// A capture read while agy is still writing can end mid-line, and a
	// run that never started holds nothing at all.
	if id, _, _ := readAgyStream(`{"event":"ini`); id != "" {
		t.Errorf("readAgyStream read a conversation %q out of a truncated line", id)
	}
	if id, status, response := readAgyStream(""); id != "" || status != "" || response != "" {
		t.Errorf("readAgyStream of an empty capture = %q, %q, %q; want three empties", id, status, response)
	}
}

// liveNonce is the token the conversation has to carry across an exec:
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
