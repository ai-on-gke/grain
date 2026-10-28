// This file drives update_status the way a dispatched run really does: a
// real `grain mcpserver` subprocess, told which daemon and which task it
// serves, asked over real stdio MCP to say what it is doing -- against a
// real ui.Server over a real store with a real live run on it, so what
// the call leaves behind is the row the task list actually renders rather
// than a fake's canned answer.
//
// It is here for the reason mcpserver_open_pull_request_test.go gives for
// its own tool: every layer of this is covered where it lives (pkg/mcp
// renders against a fake reporter, pkg/ui writes through a real store,
// pkg/model migrates and keys the write to the live run), and the switch
// that joins them is a flag block with no compiler anywhere near it. A
// wiring change that registered the tool nowhere at all would pass every
// one of those layer tests.
//
// Unlike that tool and recreate_sandbox, this one needs nothing wired
// into ui.Config: the write lands in the store the daemon already holds,
// which is itself worth pinning -- a deployment gets this the moment it
// serves an API at all.
//
// The same file covers the other writer of that row, since it is the same
// switch: the registry narrating every call it answers
// (mcp.Registry.AnnounceActivity) -- run_command from the description it
// was already given, and every other tool from its own name -- which is
// how the row keeps up with a run that never calls the tool at all.
package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestMCPServerUpdatesItsTaskStatusOverStdio(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not installed")
	}
	bin := buildGrainBinary(t)

	const taskID = "78"
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
	fileTaskForBranch(t, ctx, store, taskID, model.RepoRef{Owner: "acme", Name: "widgets"})
	// A live run, because that is the only thing a status can attach to:
	// the row the daemon writes is this run's own.
	if err := store.StartRun(ctx, model.Run{
		ID: taskID + "-1", TaskID: taskID, Sandbox: taskID + "-1",
		Attempt: 1, StartedAt: time.Now().UTC(),
	}, model.Limits{}); err != nil {
		t.Fatalf("starting a run on %s: %v", taskID, err)
	}

	daemonURL := newTestServer(t, ui.NewServer(withAccessCode(ui.Config{
		Actor:        ui.DefaultActor("operator"),
		Capabilities: ui.OfferedCapabilities(),
	}), store))

	t.Run("tools/list advertises update_status with -server and -task", func(t *testing.T) {
		p := startMCPServer(t, bin,
			"-sandbox-root", t.TempDir(), "-server", daemonURL, "-task", taskID)
		if names := toolNames(t, p); !names["update_status"] {
			t.Errorf("tools/list is missing update_status despite -server/-task; got %v", names)
		}
	})

	// The other half of the switch: a run with no daemon to ask must not
	// be offered a tool that could only ever refuse.
	t.Run("a bare mcpserver does not advertise it", func(t *testing.T) {
		p := startMCPServer(t, bin, "-sandbox-root", t.TempDir())
		if names := toolNames(t, p); names["update_status"] {
			t.Errorf("tools/list offers update_status with no -server/-task; got %v", names)
		}
	})

	t.Run("calling it puts the phrase on the task the UI serves", func(t *testing.T) {
		p := startMCPServer(t, bin,
			"-sandbox-root", t.TempDir(), "-server", daemonURL, "-task", taskID)
		res, err := p.CallTool(context.Background(), "update_status",
			map[string]any{"status": "waiting for CI on the second push"})
		if err != nil {
			t.Fatalf("tools/call update_status: %v\nstderr:\n%s", err, p.stderr.String())
		}
		if res.IsError {
			t.Fatalf("update_status answered with an error: %q\nstderr:\n%s", res.Text(), p.stderr.String())
		}
		if !strings.Contains(res.Text(), "waiting for CI on the second push") {
			t.Errorf("the answer is missing the note it recorded:\n%s", res.Text())
		}

		// What the browser would actually be handed, over the same API:
		// the task, carrying the phrase and the moment it was said.
		tasks, err := ui.NewHTTPClient(daemonURL).ListTasks(ctx)
		if err != nil {
			t.Fatalf("listing tasks over the daemon's own API: %v", err)
		}
		if len(tasks) != 1 {
			t.Fatalf("listed %d tasks, want the one this test filed", len(tasks))
		}
		if tasks[0].Activity != "waiting for CI on the second push" {
			t.Errorf("task activity = %q, want what the run said", tasks[0].Activity)
		}
		if tasks[0].ActivityAt == nil {
			t.Error("task activityAt = nil, want the moment the run said it")
		}

		// And a second call replaces the first: the row says what the run
		// is doing, never a log of what it has done.
		if _, err := p.CallTool(context.Background(), "update_status",
			map[string]any{"status": "fixing the failing test"}); err != nil {
			t.Fatalf("tools/call update_status again: %v", err)
		}
		again, err := ui.NewHTTPClient(daemonURL).ListTasks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if again[0].Activity != "fixing the failing test" {
			t.Errorf("task activity = %q, want the most recent phrase", again[0].Activity)
		}
	})

	// The same row, written by the tool a run spends most of its calls on
	// rather than by a turn spent on narration: run_command reports the
	// description it was given before it runs the command
	// (mcp.Registry.AnnounceActivity). It is turned on by the same flag
	// block, with the same compiler nowhere near it, and it is the half a
	// run gets without having to remember anything.
	t.Run("a run_command's description lands on the same row", func(t *testing.T) {
		p := startMCPServer(t, bin,
			"-sandbox-root", t.TempDir(), "-server", daemonURL, "-task", taskID)
		res, err := p.CallTool(context.Background(), "run_command",
			map[string]any{"command": "echo hello", "description": "running the test suite"})
		if err != nil {
			t.Fatalf("tools/call run_command: %v\nstderr:\n%s", err, p.stderr.String())
		}
		if res.IsError {
			t.Fatalf("run_command answered with an error: %q\nstderr:\n%s", res.Text(), p.stderr.String())
		}
		// The agent is told about its command and nothing else: the status
		// is a courtesy to somebody outside the run, not part of the
		// answer the run is reading.
		if !strings.Contains(res.Text(), "hello") {
			t.Errorf("the answer is missing the command's own output:\n%s", res.Text())
		}
		if strings.Contains(res.Text(), "running the test suite") {
			t.Errorf("the answer talks about the status it recorded:\n%s", res.Text())
		}

		tasks, err := ui.NewHTTPClient(daemonURL).ListTasks(ctx)
		if err != nil {
			t.Fatalf("listing tasks over the daemon's own API: %v", err)
		}
		if tasks[0].Activity != "running the test suite" {
			t.Errorf("task activity = %q, want what the command said it was for", tasks[0].Activity)
		}
	})

	// And the half this file exists to pin now: a call that is not
	// run_command writes the row too, from the tool's own name. A run
	// reading its way around a package for twenty minutes used to leave
	// whatever it last said standing, which is the stale line an operator
	// cannot tell from a live one.
	t.Run("a call that is not run_command lands on the same row", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("hello\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		p := startMCPServer(t, bin, "-sandbox-root", root, "-server", daemonURL, "-task", taskID)
		res, err := p.CallTool(context.Background(), "read_file", map[string]any{"file_path": "notes.txt"})
		if err != nil {
			t.Fatalf("tools/call read_file: %v\nstderr:\n%s", err, p.stderr.String())
		}
		if res.IsError || !strings.Contains(res.Text(), "hello") {
			t.Fatalf("read_file answered %q, want the file's own contents\nstderr:\n%s",
				res.Text(), p.stderr.String())
		}

		tasks, err := ui.NewHTTPClient(daemonURL).ListTasks(ctx)
		if err != nil {
			t.Fatalf("listing tasks over the daemon's own API: %v", err)
		}
		if tasks[0].Activity != "reading a file in the sandbox" {
			t.Errorf("task activity = %q, want the phrase for the tool that was called", tasks[0].Activity)
		}
	})

	// And with no daemon to tell, the description reaches nobody and costs
	// the command nothing: the same call, the same answer, no row.
	t.Run("a bare mcpserver runs the same command anyway", func(t *testing.T) {
		p := startMCPServer(t, bin, "-sandbox-root", t.TempDir())
		res, err := p.CallTool(context.Background(), "run_command",
			map[string]any{"command": "echo hello", "description": "running the test suite"})
		if err != nil {
			t.Fatalf("tools/call run_command: %v\nstderr:\n%s", err, p.stderr.String())
		}
		if res.IsError || !strings.Contains(res.Text(), "hello") {
			t.Errorf("run_command answered %+v, want the command's own output", res)
		}
	})

	// Once the run is over there is no row to write on, and the answer
	// says so plainly rather than failing: a status that raced the end of
	// its own run is nobody's mistake.
	t.Run("a run grain has already finished is told nothing is listening", func(t *testing.T) {
		if err := store.FinishRun(ctx, taskID+"-1", time.Now().UTC(), "succeeded", ""); err != nil {
			t.Fatalf("finishing the run: %v", err)
		}
		p := startMCPServer(t, bin,
			"-sandbox-root", t.TempDir(), "-server", daemonURL, "-task", taskID)
		res, err := p.CallTool(context.Background(), "update_status",
			map[string]any{"status": "still going"})
		if err != nil {
			t.Fatalf("tools/call update_status: %v\nstderr:\n%s", err, p.stderr.String())
		}
		if res.IsError {
			t.Fatalf("a finished run is not a failed call: %q", res.Text())
		}
		if !strings.Contains(res.Text(), "no longer") {
			t.Errorf("answer = %q, want it to say the run is no longer in flight", res.Text())
		}
		tasks, err := ui.NewHTTPClient(daemonURL).ListTasks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if tasks[0].Activity != "" {
			t.Errorf("task activity = %q, want nothing shown once the run is over", tasks[0].Activity)
		}

		// Nor does a narrated call put one back. A phrase left standing on
		// a run that is over is exactly the lie this row is for avoiding:
		// the write is keyed to the live run (model.Store.SetTaskActivity)
		// and there is no longer one.
		if _, err := p.CallTool(context.Background(), "run_command",
			map[string]any{"command": "echo hello", "description": "still working"}); err != nil {
			t.Fatalf("tools/call run_command: %v\nstderr:\n%s", err, p.stderr.String())
		}
		after, err := ui.NewHTTPClient(daemonURL).ListTasks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if after[0].Activity != "" {
			t.Errorf("task activity = %q, want a finished run to leave no live-looking line", after[0].Activity)
		}
	})
}
