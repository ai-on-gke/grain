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
	"os"
	"strings"
	"testing"
	"time"
)

// waitFor polls cond until it holds or the test runs out of patience --
// a session's output arrives on a goroutine copying a PTY, so every
// assertion about what it printed is an assertion about what it has
// printed *by now*.
// sessionWaitLimit is the ceiling these tests wait under, and it is a
// hang-guard rather than a budget: what they wait for is a PTY session
// starting, ending or cleaning up after itself, which either happens or
// never does. Nothing here asserts how quickly a machine forks a process
// and gets a byte back through a terminal, because a CI host running the
// whole suite at once is not quick and five seconds of it bounds nothing
// (grain/task-431). A generous ceiling costs a genuinely wedged session
// nothing but a slower red.
const sessionWaitLimit = 30 * time.Second

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(sessionWaitLimit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s (nothing after %s)", what, sessionWaitLimit)
}

// A session is a terminal in both directions: what the process printed
// comes back out of Output, and what Write sends arrives on its stdin.
func TestSessionCarriesOutputAndInput(t *testing.T) {
	m := NewManager()
	s, err := m.Start(Spec{TaskID: "task-1", StartedAt: time.Now()}, Command{Path: "cat"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Close("task-1")

	if err := s.Write([]byte("hello\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	waitFor(t, "the session to echo what was typed into it", func() bool {
		out, _, _ := s.Output(0)
		return strings.Contains(string(out), "hello")
	})
}

// A session is not a pipe with a terminal's name on it: the process
// behind it holds the PTY as its *controlling* terminal, which is what a
// ^C typed into the session signals and what an agent CLI looks for
// before deciding it is talking to a person. /dev/tty is the one file
// only a process with a controlling terminal can open, and it resolves to
// whichever terminal that is -- so a child that had merely inherited this
// test binary's own stdio would either fail to open it at all (there is
// no terminal under `go test` on a CI host) or write onto the developer's
// own screen, and nothing would come back out of Output either way.
//
// This is the assertion that attachTTY did its job, which is the half of
// the PTY dance a working openPTY does not prove on its own.
func TestSessionGivesItsProcessAControllingTerminal(t *testing.T) {
	m := NewManager()
	s, err := m.Start(Spec{TaskID: "task-1", StartedAt: time.Now()}, Command{
		Path: "sh",
		Args: []string{"-c", "echo controlling-terminal > /dev/tty"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Close("task-1")
	waitFor(t, "the session to carry what its process wrote to /dev/tty", func() bool {
		out, _, _ := s.Output(0)
		return strings.Contains(string(out), "controlling-terminal")
	})
}

// Output is a stream with a cursor: a reader hands back the offset it was
// given last time and gets only what has arrived since.
func TestOutputAnswersFromTheOffsetGiven(t *testing.T) {
	m := NewManager()
	s, err := m.Start(Spec{TaskID: "task-1", StartedAt: time.Now()}, Command{Path: "cat"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Close("task-1")

	if err := s.Write([]byte("first\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	waitFor(t, "the first line", func() bool {
		out, _, _ := s.Output(0)
		return strings.Contains(string(out), "first")
	})
	_, next, dropped := s.Output(0)
	if dropped {
		t.Fatalf("dropped = true for a session that has printed a few bytes")
	}
	if err := s.Write([]byte("second\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	waitFor(t, "the second line", func() bool {
		out, _, _ := s.Output(next)
		return strings.Contains(string(out), "second")
	})
	// The two reads have to agree about one stream: what came back from
	// `next` is exactly the tail of what came back from 0. Asserting on
	// the lines themselves would be asserting about a terminal's echo,
	// which prints each of them twice and in its own time -- which is also
	// why the two reads are retried until nothing arrives between them:
	// cat's copy of "second" can land after the echo the wait saw.
	var whole, tail []byte
	var end, tailEnd int64
	waitFor(t, "the stream to settle between two reads", func() bool {
		whole, end, _ = s.Output(0)
		tail, tailEnd, _ = s.Output(next)
		return end == tailEnd
	})
	if end != tailEnd {
		t.Fatalf("two reads disagreed about how much there is: %d and %d", end, tailEnd)
	}
	if want := string(whole[next:]); string(tail) != want {
		t.Fatalf("output from offset %d = %q, want the tail of the stream %q", next, tail, want)
	}
}

// The ring drops the oldest output rather than growing without bound, and
// says so, so that a client can redraw instead of quietly showing a hole.
func TestOutputReportsWhatTheRingDropped(t *testing.T) {
	s := &Session{done: make(chan struct{})}
	s.append([]byte(strings.Repeat("a", MaxBuffer)))
	s.append([]byte(strings.Repeat("b", 1024)))

	out, next, dropped := s.Output(0)
	if !dropped {
		t.Fatalf("dropped = false after overrunning a %d-byte ring", MaxBuffer)
	}
	if len(out) != MaxBuffer {
		t.Fatalf("len(output) = %d, want the whole ring %d", len(out), MaxBuffer)
	}
	if next != int64(MaxBuffer+1024) {
		t.Fatalf("next offset = %d, want every byte ever written %d", next, MaxBuffer+1024)
	}
}

// One session per task: a second dispatch of a task that already has a
// live console is a bug, not a second window onto the same branch.
func TestManagerRefusesASecondSessionForTheSameTask(t *testing.T) {
	m := NewManager()
	if _, err := m.Start(Spec{TaskID: "task-1"}, Command{Path: "cat"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Close("task-1")
	if _, err := m.Start(Spec{TaskID: "task-1"}, Command{Path: "cat"}); err != ErrSessionExists {
		t.Fatalf("second Start = %v, want ErrSessionExists", err)
	}
}

// Closing is the explicit end an interactive task waits for: the process
// dies, Wait returns, and the task stops being one the UI offers.
func TestCloseEndsTheSessionAndTakesItOffTheList(t *testing.T) {
	m := NewManager()
	s, err := m.Start(Spec{TaskID: "task-1", StartedAt: time.Now()}, Command{Path: "cat"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := m.List(); len(got) != 1 || !got[0].Live {
		t.Fatalf("List() = %+v, want one live session", got)
	}
	if err := m.Close("task-1"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-s.Done():
	case <-time.After(sessionWaitLimit):
		t.Fatal("the session was closed but its run never returned")
	}
	if !s.ClosedByHand() {
		t.Fatal("ClosedByHand() = false for a session somebody closed")
	}
	if got := m.List(); len(got) != 0 {
		t.Fatalf("List() = %+v, want nothing after the session was closed", got)
	}
	if err := m.Close("task-1"); err != ErrNoSession {
		t.Fatalf("closing it twice = %v, want ErrNoSession", err)
	}
}

// A CLI that exits on its own -- somebody typed /exit -- ends the session
// too. There is nothing left to type into either way.
func TestSessionEndsWhenItsCommandExits(t *testing.T) {
	m := NewManager()
	s, err := m.Start(Spec{TaskID: "task-1", StartedAt: time.Now()}, Command{Path: "true"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Close("task-1")
	if err := s.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if s.ClosedByHand() {
		t.Fatal("ClosedByHand() = true for a session whose command exited by itself")
	}
	if info := s.Info(); info.Live {
		t.Fatal("Info().Live = true after the command exited")
	}
}

// The last thing a CLI prints before exiting on its own -- an error
// message, most often -- is still in the PTY when the process is reaped,
// and a session that closed its terminal at that moment would drop it.
// So by the time Wait returns, Output has to carry everything the process
// wrote: asserted once, straight after Wait, rather than polled for.
func TestSessionKeepsWhatItsCommandPrintedBeforeExiting(t *testing.T) {
	m := NewManager()
	s, err := m.Start(Spec{TaskID: "task-1", StartedAt: time.Now()}, Command{
		Path: "sh",
		Args: []string{"-c", "echo last-words"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Close("task-1")
	if err := s.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if out := s.Transcript(); !strings.Contains(out, "last-words") {
		t.Fatalf("Transcript() = %q once the session ended, want what its command printed before exiting", out)
	}
}

// Waiting for the PTY to drain must not turn into waiting for every
// process that holds it: a CLI that exits leaving a child behind with the
// terminal still open ends the session all the same (its exit hangs the
// terminal up; drainLimit is the backstop where that does not reach the
// master), and what it printed is still kept.
func TestSessionEndsWhenItsCommandLeavesTheTerminalOpenBehindIt(t *testing.T) {
	m := NewManager()
	s, err := m.Start(Spec{TaskID: "task-1", StartedAt: time.Now()}, Command{
		Path: "sh",
		Args: []string{"-c", "sleep 60 & echo left-behind"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Close("task-1")
	ctx, cancel := context.WithTimeout(context.Background(), sessionWaitLimit)
	defer cancel()
	if err := s.Wait(ctx); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if out := s.Transcript(); !strings.Contains(out, "left-behind") {
		t.Fatalf("Transcript() = %q once the session ended, want what its command printed", out)
	}
}

// Wait belongs to the run driving the session, and a run can be cancelled
// from outside it -- the task closed, the daemon stopping.
func TestWaitReturnsWhenTheRunIsCancelled(t *testing.T) {
	m := NewManager()
	s, err := m.Start(Spec{TaskID: "task-1", StartedAt: time.Now()}, Command{Path: "cat"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Close("task-1")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Wait(ctx); err != context.Canceled {
		t.Fatalf("Wait = %v, want context.Canceled", err)
	}
}

// A cleanup belongs to the session, not to the call that started it: the
// MCP config a framework writes has to outlive that call and die with the
// process that reads it.
func TestCleanupRunsWhenTheSessionEnds(t *testing.T) {
	m := NewManager()
	cleaned := make(chan struct{})
	if _, err := m.Start(Spec{TaskID: "task-1"}, Command{
		Path:    "true",
		Cleanup: func() { close(cleaned) },
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-cleaned:
	case <-time.After(sessionWaitLimit):
		t.Fatal("the session ended without running its cleanup")
	}
}

// Resize is what stops a TUI drawing itself for the 80x24 a fresh PTY
// claims. There is nothing to read back, so this asserts what it can:
// that a real size is accepted and a nonsensical one is refused.
func TestResizeRefusesAWindowWithNoSize(t *testing.T) {
	m := NewManager()
	s, err := m.Start(Spec{TaskID: "task-1"}, Command{Path: "cat"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Close("task-1")
	if err := s.Resize(40, 120); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if err := s.Resize(0, 0); err == nil {
		t.Fatal("Resize(0, 0) was accepted")
	}
}

// A session closed while it is still starting -- registered, but with no
// process yet for Close to kill -- must not leave the process Start goes
// on to launch running with nobody able to stop it: every later Close
// returns early because the session is already marked closed.
func TestCloseWhileStartingKillsTheProcess(t *testing.T) {
	m := NewManager()
	opened := newPTY
	t.Cleanup(func() { newPTY = opened })
	newPTY = func() (*os.File, *os.File, error) {
		if err := m.Close("task-1"); err != nil {
			t.Errorf("Close during Start: %v", err)
		}
		return opened()
	}

	s, err := m.Start(Spec{TaskID: "task-1", StartedAt: time.Now()}, Command{Path: "sleep", Args: []string{"300"}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-s.Done():
	case <-time.After(sessionWaitLimit):
		s.mu.Lock()
		cmd := s.cmd
		s.mu.Unlock()
		if cmd != nil && cmd.Process != nil {
			cmd.Process.Kill()
		}
		t.Fatalf("a session closed while starting never ended (nothing after %s)", sessionWaitLimit)
	}
	if !s.ClosedByHand() {
		t.Fatalf("ClosedByHand = false for a session closed during Start")
	}
	if got := m.Get("task-1"); got != nil {
		t.Fatalf("the Manager still holds a session it was asked to close")
	}
}
