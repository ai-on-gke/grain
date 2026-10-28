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

// This file drives rename_session the way a standing session's run
// really does: a real `grain mcpserver` subprocess, told which daemon and
// which task it serves, asked over real stdio MCP to retitle its chat --
// against a real ui.Server over a real store with a real session on it,
// so what the call leaves behind is the row the rail and the sessions
// list actually render rather than a fake's canned answer.
//
// It is here for the reason mcpserver_update_status_test.go gives for its
// own tool: every layer is covered where it lives (pkg/mcp renders
// against a fake renamer, pkg/ui writes through a real store), and the
// switch that joins them -- a tool registered only for a task the daemon
// says is a console session -- is a condition in a flag block with no
// compiler anywhere near it. A wiring change that registered the tool
// nowhere, or for every task, would pass every one of those layer tests.
package e2e

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestMCPServerRenamesItsSessionOverStdio(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not installed")
	}
	bin := buildGrainBinary(t)

	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	cfg := withAccessCode(ui.Config{
		Actor:        ui.DefaultActor("operator"),
		Capabilities: ui.OfferedCapabilities(),
	})
	client := ui.NewClient(cfg, store)

	// A chat opened the way "New session" opens one: from its first
	// message, titled by the heuristic on that message's opening words.
	sessions, err := client.CreateConsoleSession(ctx, ui.CreateConsoleSessionRequest{
		Message: "hi, can you look at why the nightly is red",
	})
	if err != nil {
		t.Fatalf("starting a session from a message: %v", err)
	}
	var session ui.ConsoleSessionSetting
	for _, s := range sessions {
		if s.ID != model.DirectorSessionID {
			session = s
		}
	}
	if session.TaskID == "" {
		t.Fatalf("the new session has no task: %+v", sessions)
	}
	if session.Title != "Hi, can you look at why the nightly" {
		t.Fatalf("heuristic title = %q, want the opening words", session.Title)
	}
	// And an ordinary task beside it, which must not be offered the tool.
	const ordinaryID = "78"
	fileTaskForBranch(t, ctx, store, ordinaryID, model.RepoRef{Owner: "acme", Name: "widgets"})

	daemonURL := newTestServer(t, ui.NewServerWithClient(client))

	t.Run("tools/list advertises rename_session to a session's run", func(t *testing.T) {
		p := startMCPServer(t, bin,
			"-sandbox-root", t.TempDir(), "-server", daemonURL, "-task", session.TaskID, "-interactive")
		if names := toolNames(t, p); !names["rename_session"] {
			t.Errorf("tools/list is missing rename_session for a console session's task; got %v", names)
		}
	})

	// The other half of the switch: a run that is not a session has no
	// title to write and must not be shown a tool that could only refuse.
	t.Run("an ordinary task's run is not offered it", func(t *testing.T) {
		p := startMCPServer(t, bin,
			"-sandbox-root", t.TempDir(), "-server", daemonURL, "-task", ordinaryID)
		if names := toolNames(t, p); names["rename_session"] {
			t.Errorf("tools/list offers rename_session to a task that is not a session; got %v", names)
		}
	})

	t.Run("calling it retitles the session the UI lists", func(t *testing.T) {
		p := startMCPServer(t, bin,
			"-sandbox-root", t.TempDir(), "-server", daemonURL, "-task", session.TaskID, "-interactive")
		res, err := p.CallTool(context.Background(), "rename_session",
			map[string]any{"title": "Nightly CI failure"})
		if err != nil {
			t.Fatalf("tools/call rename_session: %v\nstderr:\n%s", err, p.stderr.String())
		}
		if res.IsError {
			t.Fatalf("rename_session answered with an error: %q\nstderr:\n%s", res.Text(), p.stderr.String())
		}
		if !strings.Contains(res.Text(), "Nightly CI failure") {
			t.Errorf("the answer is missing the title it wrote:\n%s", res.Text())
		}

		// What the rail would actually be handed, over the same API
		// (GET /api/console-sessions, which is what App.jsx polls).
		resp, err := accessGet(daemonURL + "/api/console-sessions")
		if err != nil {
			t.Fatalf("listing sessions over the daemon's own API: %v", err)
		}
		defer resp.Body.Close()
		var page ui.ConsoleSessionSettingsResponse
		if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
			t.Fatalf("decoding GET /api/console-sessions (%d): %v", resp.StatusCode, err)
		}
		listed := page.Sessions
		var got *ui.ConsoleSessionSetting
		for i := range listed {
			if listed[i].ID == session.ID {
				got = &listed[i]
			}
		}
		if got == nil {
			t.Fatalf("session %q is gone from the list: %+v", session.ID, listed)
		}
		if got.Title != "Nightly CI failure" {
			t.Errorf("listed title = %q, want what the run said", got.Title)
		}
		if got.TaskID != session.TaskID {
			t.Errorf("session task = %q, want %q: a rename must not move the conversation", got.TaskID, session.TaskID)
		}

		// A title too long for the rail is refused by the tool itself,
		// before the daemon is reached, with the limit in the answer.
		long, err := p.CallTool(context.Background(), "rename_session",
			map[string]any{"title": strings.Repeat("x", model.ConsoleSessionTitleMaxLength+1)})
		if err != nil {
			t.Fatalf("tools/call rename_session (long): %v", err)
		}
		if !long.IsError || !strings.Contains(long.Text(), "characters") {
			t.Errorf("a too-long title answered %+v, want a refusal naming the limit", long)
		}
	})
}
