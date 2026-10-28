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

//go:build linux || darwin

package console

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// terminalFramework is an agent.TerminalFramework whose "CLI" is whatever
// command the test names -- enough to drive Framework.Run without a
// claude binary, a credential or an MCP server anywhere.
type terminalFramework struct {
	cmd *agent.TerminalCommand
	err error
}

func (f terminalFramework) Run(context.Context, agent.RunConfig) (*agent.Result, error) {
	return nil, errors.New("the headless half of this fake is never called")
}

func (f terminalFramework) TerminalCommand(context.Context, agent.RunConfig) (*agent.TerminalCommand, error) {
	return f.cmd, f.err
}

// The whole shape of an interactive run: it starts, it stays running
// while somebody is using it, and it ends when they close it -- with what
// the terminal showed travelling back as the attempt's transcript.
func TestRunLastsUntilTheSessionIsClosed(t *testing.T) {
	m := NewManager()
	f := Wrap(terminalFramework{cmd: &agent.TerminalCommand{Path: "cat"}}, m, Spec{})

	type outcome struct {
		result *agent.Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := f.Run(context.Background(), agent.RunConfig{TaskID: "task-1", Repo: "acme/widgets"})
		done <- outcome{result, err}
	}()

	var session *Session
	waitFor(t, "the session to be registered", func() bool {
		session = m.Get("task-1")
		return session != nil
	})
	// The session is registered and nothing has closed it, and the command
	// behind it is a `cat` that ends when its terminal does -- so there is
	// nothing that could have ended this run, and asking once is the whole
	// of the check. Waiting 100ms first only delayed the same question
	// (grain/task-431).
	select {
	case got := <-done:
		t.Fatalf("Run returned while the session was still open: %+v, %v", got.result, got.err)
	default:
	}
	if info := session.Info(); info.Repo != "acme/widgets" {
		t.Fatalf("session repo = %q, want the run's own", info.Repo)
	}

	// Offered until it is taken, rather than once. Manager.Start puts the
	// session on its list before the process behind it exists -- so that
	// two dispatches of one task cannot both get past its check -- and
	// hands the PTY over afterwards, which is why its own comment says a
	// client can already be holding a session and asking it to do things.
	// This test is that client: it takes the session out of Manager.Get
	// while the framework's goroutine is still inside Start, so under
	// load the write lands in that window and Session.Write answers
	// "this session is still starting". That is the session behaving as
	// documented, not a failure to report.
	waitFor(t, "the session to accept what was typed into it", func() bool {
		return session.Write([]byte("typed by hand\n")) == nil
	})
	waitFor(t, "the session to echo it", func() bool {
		out, _, _ := session.Output(0)
		return strings.Contains(string(out), "typed by hand")
	})

	if err := m.Close("task-1"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("Run: %v, want a closed session to be an ordinary ending", got.err)
		}
		if !strings.Contains(got.result.Transcript, "typed by hand") {
			t.Fatalf("transcript = %q, want what the terminal showed", got.result.Transcript)
		}
		if !strings.Contains(got.result.FinalText, "closed from the UI") {
			t.Fatalf("final text = %q, want it to say the session was closed", got.result.FinalText)
		}
	case <-time.After(sessionWaitLimit):
		t.Fatal("the session was closed but the run never returned")
	}
	if m.Get("task-1") != nil {
		t.Fatal("the session outlived the run driving it")
	}
}

// A session whose CLI exits by itself ends the run too, and the run says
// which of the two happened.
func TestRunEndsWhenTheCLIExits(t *testing.T) {
	m := NewManager()
	f := Wrap(terminalFramework{cmd: &agent.TerminalCommand{Path: "true"}}, m, Spec{})
	result, err := f.Run(context.Background(), agent.RunConfig{TaskID: "task-1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(result.FinalText, "exited") {
		t.Fatalf("final text = %q, want it to say the CLI exited", result.FinalText)
	}
	if m.Get("task-1") != nil {
		t.Fatal("the session outlived the run driving it")
	}
}

// A framework that cannot describe a session at all fails the run before
// anything is registered -- there is no half-started console to leave
// behind.
func TestRunReportsAFrameworkThatCannotStartASession(t *testing.T) {
	m := NewManager()
	f := Wrap(terminalFramework{err: errors.New("no credential")}, m, Spec{})
	if _, err := f.Run(context.Background(), agent.RunConfig{TaskID: "task-1"}); err == nil {
		t.Fatal("Run succeeded with a framework that could not build a command")
	}
	if m.Get("task-1") != nil {
		t.Fatal("a session was registered for a run that never started one")
	}
}

// Cancellation -- the task closed, the daemon stopping -- ends the
// session rather than leaving an agent CLI running on the controller with
// nothing attached to it.
func TestRunEndsTheSessionWhenTheRunIsCancelled(t *testing.T) {
	m := NewManager()
	f := Wrap(terminalFramework{cmd: &agent.TerminalCommand{Path: "cat"}}, m, Spec{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := f.Run(ctx, agent.RunConfig{TaskID: "task-1"})
		done <- err
	}()
	waitFor(t, "the session to be registered", func() bool { return m.Get("task-1") != nil })
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run reported a cancelled session as an ordinary ending")
		}
	case <-time.After(sessionWaitLimit):
		t.Fatal("cancelling the run left the session running")
	}
	if m.Get("task-1") != nil {
		t.Fatal("a cancelled run left its session on the list")
	}
}
