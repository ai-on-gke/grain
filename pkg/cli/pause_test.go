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

package cli

// `grain pause`'s own rendering and dispatch (grain/task-182). The wire
// calls behind it are pkg/ui's (pause_test.go's HTTPClient round trips);
// what is only decidable here is what an operator at a terminal actually
// reads -- in particular that a daemon serving a UI with no gate wired
// does not print anything anyone could mistake for "nothing is paused".

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// fakePause stands in for *orchestrator.Pause, the gate pkg/cli/daemon.go
// hands the UI in a real deployment -- ui.AgentPause is satisfied
// structurally, so a test needs nothing more than these methods.
type fakePause struct {
	pauses []model.UsageLimitPause
}

func (p *fakePause) Pauses(now time.Time) []model.UsageLimitPause {
	var out []model.UsageLimitPause
	for _, pause := range p.pauses {
		if now.Before(pause.Until) {
			out = append(out, pause)
		}
	}
	return out
}

func (p *fakePause) LiftWhere(match func(model.UsageLimitPause) bool) []model.UsageLimitPause {
	var kept, lifted []model.UsageLimitPause
	for _, pause := range p.pauses {
		if match(pause) {
			lifted = append(lifted, pause)
		} else {
			kept = append(kept, pause)
		}
	}
	p.pauses = kept
	return lifted
}

func TestPrintAgentPause(t *testing.T) {
	until := time.Date(2026, 9, 4, 17, 0, 0, 0, time.UTC)
	entry := ui.AgentPauseEntry{
		Connection: "alpha-claude", ConnectionName: "Alpha's Claude", Team: "alpha", Framework: "claude",
		Until: until, SecondsRemaining: (2 * time.Hour).Seconds(),
		Reason: "claude: usage limit reached; resets at " + until.Format(time.RFC3339),
	}
	paused := ui.AgentPauseStatus{
		Paused: true, Until: until, SecondsRemaining: entry.SecondsRemaining, Reason: entry.Reason,
		Pauses: []ui.AgentPauseEntry{entry},
	}
	lifted, notLifted := true, false
	for _, tc := range []struct {
		name    string
		status  ui.AgentPauseStatus
		enabled bool
		lifted  *bool
		want    []string
		wantNot []string
	}{
		{
			name:    "a paused deployment says until when, and what the provider said",
			status:  paused,
			enabled: true,
			want: []string{
				"dispatch is paused", "2026-09-04T17:00:00Z", "2h0m0s",
				"claude: usage limit reached", "grain pause -lift",
			},
		},
		{
			// A pause covers the connection whose credential met the
			// limit, so the line has to name it and its team: an operator
			// whose other connections are still moving must not read
			// this as the whole deployment having stopped.
			name:    "a pause says which connection of which team",
			status:  paused,
			enabled: true,
			want:    []string{"dispatch is paused onto connection Alpha's Claude (alpha-claude) of team alpha"},
		},
		{
			name: "a pause on no connection names its team and framework",
			status: ui.AgentPauseStatus{
				Paused: true, Until: until, SecondsRemaining: entry.SecondsRemaining,
				Pauses: []ui.AgentPauseEntry{{Team: "default", Framework: "antigravity", Until: until}},
			},
			enabled: true,
			want:    []string{"team default's antigravity runs (no connection)"},
		},
		{
			name:    "an unpaused deployment says dispatch is running",
			enabled: true,
			want:    []string{"dispatch is running"},
			// Nothing to suggest lifting: there is no pause to lift.
			wantNot: []string{"-lift", "paused until"},
		},
		{
			// The case this whole command has to get right: a daemon that
			// cannot see the gate must not answer for it. "Nothing is
			// paused" from here is exactly what an operator would act on
			// to rule the usage limit out.
			name:    "a deployment with no gate wired says so instead",
			wantNot: []string{"dispatch is running", "dispatch is paused"},
			want:    []string{"not wired"},
		},
		{
			name:    "a lift that ended a pause says so",
			enabled: true,
			lifted:  &lifted,
			want:    []string{"pause lifted", "next reconcile tick"},
			// Already said what it did; repeating the advice to lift it
			// at somebody who just did would read as it not having worked.
			wantNot: []string{"grain pause -lift"},
		},
		{
			name:    "a lift with nothing to lift says that rather than claiming one",
			enabled: true,
			lifted:  &notLifted,
			want:    []string{"nothing to lift"},
			wantNot: []string{"pause lifted"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := captureStdout(t, func() {
				view := agentPauseView{Enabled: tc.enabled, Lifted: tc.lifted}
				if tc.enabled {
					view.Pause = &tc.status
				}
				(&printer{}).agentPause(view)
			})
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("output does not mention %q:\n%s", want, out)
				}
			}
			for _, unwanted := range tc.wantNot {
				if strings.Contains(out, unwanted) {
					t.Errorf("output mentions %q, and should not:\n%s", unwanted, out)
				}
			}
		})
	}
}

// -json prints the route's own body, so a reading piped somewhere else
// and one fetched with curl are the same object -- including the
// enabled/pause split, which is the part a script has to branch on.
func TestPrintAgentPauseJSON(t *testing.T) {
	status := ui.AgentPauseStatus{
		Paused: true, Until: time.Date(2026, 9, 4, 17, 0, 0, 0, time.UTC),
		SecondsRemaining: 7200, Reason: "claude: usage limit reached",
	}
	out := captureStdout(t, func() {
		(&printer{json: true}).pauses(pauseView{Agent: &agentPauseView{Enabled: true, Pause: &status}})
	})
	var got pauseView
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if got.Agent == nil || !got.Agent.Enabled || got.Agent.Pause == nil ||
		!got.Agent.Pause.Paused || got.Agent.Pause.SecondsRemaining != 7200 {
		t.Fatalf("got %+v, want an enabled, paused reading two hours from resuming", got)
	}
	if got.Agent.Lifted != nil {
		t.Errorf("lifted = %v on a plain read, want it omitted", *got.Agent.Lifted)
	}
	// A half this invocation did not ask about is absent rather than
	// present-and-empty: a script branching on it must not read "the
	// pipeline is running" out of a call that never asked.
	if got.Pipeline != nil {
		t.Errorf("pipeline = %+v on an agent-only reading, want it omitted", got.Pipeline)
	}

	out = captureStdout(t, func() {
		(&printer{json: true}).pauses(pauseView{Agent: &agentPauseView{}})
	})
	got = pauseView{}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if got.Agent == nil || got.Agent.Enabled || got.Agent.Pause != nil {
		t.Fatalf("got %+v, want enabled false and no reading at all", got)
	}
}

// pauseServer starts a real daemon-shaped UI/API over an embedded store
// with gate wired, and returns its URL along with the gate itself.
func pauseServer(t *testing.T, pause ui.AgentPause) string {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(t.TempDir(), "data")))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	_ = store.PutConfig(context.Background(), model.Config{PollInterval: model.DefaultPollInterval})
	client := ui.NewClient(ui.Config{
		Actor:        ui.DefaultActor("tester"),
		Capabilities: ui.OfferedCapabilities(),
		AgentPause:   pause,
	}, store)
	srv := httptest.NewServer(accessCodeServer(client))
	t.Cleanup(srv.Close)
	return srv.URL
}

// The dispatch itself, against a real server: every test above would
// still pass if runCLI's switch never reached cmdPause at all.
func TestRunCLIPauseReadsAndLifts(t *testing.T) {
	pause := &fakePause{pauses: []model.UsageLimitPause{{
		Connection: "default-claude", Team: model.DefaultTeamID, Framework: "claude",
		Until: time.Now().Add(time.Hour), Reason: "claude: usage limit reached",
	}}}
	server := pauseServer(t, pause)

	out := captureStdout(t, func() {
		if err := runCLIWithAccessCode([]string{"-server", server, "pause"}); err != nil {
			t.Fatalf("grain pause: %v", err)
		}
	})
	if !strings.Contains(out, "dispatch is paused") || !strings.Contains(out, "claude: usage limit reached") {
		t.Fatalf("`grain pause` printed %q, want the paused reading and the provider's sentence", out)
	}
	if len(pause.pauses) == 0 {
		t.Fatal("`grain pause` cleared the gate; only the reconcile loop and an explicit -lift may do that")
	}

	out = captureStdout(t, func() {
		if err := runCLIWithAccessCode([]string{"-server", server, "pause", "-lift"}); err != nil {
			t.Fatalf("grain pause -lift: %v", err)
		}
	})
	if !strings.Contains(out, "pause lifted") {
		t.Fatalf("`grain pause -lift` printed %q, want it saying the pause was lifted", out)
	}
	if len(pause.pauses) != 0 {
		t.Fatal("`grain pause -lift` left the gate shut")
	}

	out = captureStdout(t, func() {
		if err := runCLIWithAccessCode([]string{"-json", "-server", server, "pause"}); err != nil {
			t.Fatalf("grain -json pause: %v", err)
		}
	})
	var got pauseView
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if got.Agent == nil || !got.Agent.Enabled || got.Agent.Pause == nil || got.Agent.Pause.Paused {
		t.Fatalf("got %+v, want an enabled reading of a deployment that is no longer paused", got)
	}
	// A bare read asks both routes and prints one document, so the
	// pipeline half is there beside it.
	if got.Pipeline == nil || got.Pipeline.Pause.Paused {
		t.Fatalf("got %+v, want the pipeline half present and not paused", got)
	}
}

// The deliberate gate, end to end through the CLI (grain/task-38): a
// deployment is paused, says so on the next read, and resumes -- against
// a real server and a real store, since unlike the agent gate this one
// is a row rather than a field in a process, and a command that wrote it
// to the wrong place would still print the right sentence.
func TestRunCLIPausesAndResumesThePipeline(t *testing.T) {
	server := pauseServer(t, nil)

	out := captureStdout(t, func() {
		if err := runCLIWithAccessCode([]string{"-server", server, "pause", "-set", "-reason", "patching the host"}); err != nil {
			t.Fatalf("grain pause -set: %v", err)
		}
	})
	if !strings.Contains(out, "pipeline paused") {
		t.Fatalf("`grain pause -set` printed %q, want it saying the pipeline is paused", out)
	}

	out = captureStdout(t, func() {
		if err := runCLIWithAccessCode([]string{"-server", server, "pause"}); err != nil {
			t.Fatalf("grain pause: %v", err)
		}
	})
	for _, want := range []string{"paused since", "patching the host", "grain pause -resume"} {
		if !strings.Contains(out, want) {
			t.Errorf("`grain pause` printed %q, which does not mention %q", out, want)
		}
	}
	if strings.Contains(out, "waiting on") {
		t.Errorf("`grain pause` printed %q: an operator's own pause waits on no task", out)
	}

	out = captureStdout(t, func() {
		if err := runCLIWithAccessCode([]string{"-server", server, "pause", "-resume"}); err != nil {
			t.Fatalf("grain pause -resume: %v", err)
		}
	})
	if !strings.Contains(out, "pipeline resumed") {
		t.Fatalf("`grain pause -resume` printed %q, want it saying work is picked up again", out)
	}

	// And resuming a deployment that is already running says that rather
	// than claiming to have done something.
	out = captureStdout(t, func() {
		if err := runCLIWithAccessCode([]string{"-server", server, "pause", "-resume"}); err != nil {
			t.Fatalf("grain pause -resume, second time: %v", err)
		}
	})
	if !strings.Contains(out, "nothing to resume") {
		t.Fatalf("a second `grain pause -resume` printed %q, want it saying there was nothing to resume", out)
	}

	if err := runCLIWithAccessCode([]string{"-server", server, "pause", "-set", "-resume"}); err == nil {
		t.Error("`grain pause -set -resume` asked for both at once and was accepted")
	}
}

// A daemon serving a UI with no gate behind it: a read says it has been
// told nothing, and a lift -- an action that would otherwise report
// success for having done nothing at all -- fails.
func TestRunCLIPauseWithNoGateWired(t *testing.T) {
	server := pauseServer(t, nil)

	out := captureStdout(t, func() {
		if err := runCLIWithAccessCode([]string{"-server", server, "pause"}); err != nil {
			t.Fatalf("grain pause: %v", err)
		}
	})
	if strings.Contains(out, "dispatch is running") {
		t.Fatalf("`grain pause` printed %q, which reads as an answer about a gate it never saw", out)
	}

	if err := runCLIWithAccessCode([]string{"-server", server, "pause", "-lift"}); err == nil {
		t.Error("`grain pause -lift` against a deployment with no gate returned no error")
	}
}
