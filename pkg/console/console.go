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

// Package console runs an agent framework's own CLI on a pseudo-terminal
// and keeps the session alive for somebody to sit in front of -- grain's
// interactive tasks, as opposed to the unattended runs every other task
// gets.
//
// The difference between a console run and an ordinary one is only how
// the agent is driven. An ordinary run hands the whole prompt to a CLI in
// its headless mode (`claude -p`) and reads the transcript back when it
// exits; a console run starts the same CLI, with the same MCP server,
// the same sandbox, the same tool roster and the same credentials, on a
// PTY -- and then does nothing but carry bytes between that PTY and
// whoever is attached to it. Everything around the run is unchanged, and
// that is the point: the task was dispatched by dispatch.Cycle like any
// other, it holds a sandbox with its repository already checked out, and
// when it ends orchestrator.ProcessResult opens a pull request for
// whatever branch it pushed exactly as it would for a run nobody watched.
//
// Two things make this a package rather than a few lines in
// orchestrator. The first is the PTY itself (pty_linux.go, and
// pty_darwin.go so that a console session also works on the laptop this
// is developed on): an agent CLI asked to run without a terminal falls
// back to a line-at-a-time mode, so there would be nothing to show that
// looks like the tool people know.
// The second is that the session has to be reachable from somewhere else
// entirely -- the run is a goroutine deep inside orchestrator, and the
// person typing into it is on the other end of an HTTP request being
// served by pkg/ui -- so the sessions live in a Manager both sides hold,
// keyed by the task the run belongs to.
//
// What is deliberately not here: any transport. The Manager buffers
// output and answers "everything since offset N", which is what a browser
// polling over plain HTTP needs and what this repository's UI already
// does everywhere else. A websocket would cost less per keystroke and
// nothing else, and there is not one in this deployment to reuse.
package console

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/procgroup"
)

// MaxBuffer is how much of a session's output the Manager keeps. A
// terminal is a window onto the recent past -- a client that has been
// attached all along has already been handed every byte, and one
// attaching now wants the screen as it stands rather than the whole
// history -- so this is a ring: once it is full the oldest bytes are
// dropped and readers are told they were (Output's `dropped`).
//
// 256KiB is a few thousand lines of a TUI redrawing itself, which is
// minutes of an agent working, and it is per live session on a
// deployment that runs a handful at a time.
const MaxBuffer = 256 << 10

// drainLimit is how long a session whose CLI has exited waits for the
// rest of its output to come out of the PTY before closing it anyway.
// Normally that is no time at all: the CLI leads its own session with
// this PTY as its controlling terminal (attachTTY), so its exit hangs the
// terminal up -- even with a grandchild still holding the slave open --
// and reading the master then hands back whatever is still buffered and
// fails (EIO on Linux), which is pump returning. The limit is a backstop
// for a platform or a process tree where that hangup never reaches the
// master, so that a session still ends rather than waiting on it.
// Darwin (pty_darwin.go) is the one that has not been checked: if its
// master does not report the hangup, a session there ends this much
// later than its CLI did, but still with all of its output.
const drainLimit = 2 * time.Second

// Command is one interactive CLI invocation, as the framework that owns
// that CLI describes it (agent.TerminalCommand, which this mirrors field
// for field so that this package imports nothing to run a session and
// pkg/agent imports nothing to describe one).
type Command struct {
	// Path is the binary to execute, resolved against $PATH by exec.
	Path string
	// Args are its arguments, not counting argv[0].
	Args []string
	// Env is added to this process's own environment rather than
	// replacing it: an agent CLI needs a $HOME and a $PATH like anything
	// else, and what a framework has to say here is the handful of
	// variables that are its own business (a credential, a tool timeout).
	Env []string
	// Dir is the working directory, or "" for this process's own.
	Dir string
	// Cleanup, when non-nil, is called once the session has ended --
	// what removes the temporary MCP config file a framework wrote for
	// this session, the way its own Run removes the one it writes per
	// run.
	Cleanup func()
}

// Spec is what a session is, apart from the process behind it: which
// task's run it is driving, and enough about that task to name it in a
// list of live sessions without the reader having to look the task up.
type Spec struct {
	TaskID    string
	RunID     string
	Title     string
	Framework string
	// Repo is "owner/name" for a task with a target, and "" for one
	// without.
	Repo      string
	StartedAt time.Time
}

// Session is one live agent CLI on a PTY: the process, everything it has
// printed lately, and the two ways it can end (the CLI exiting, or
// somebody closing the session).
type Session struct {
	spec Spec

	pty     *os.File
	cmd     *exec.Cmd
	cleanup func()

	mu sync.Mutex
	// buf holds the last MaxBuffer bytes written by the process, and
	// base is the offset of buf[0] in the whole output stream -- so
	// base > 0 is exactly "some output has been dropped", which is what
	// Output reports to a reader that asked for a byte no longer here.
	buf     []byte
	base    int64
	ended   bool
	closed  bool
	endErr  error
	endedAt time.Time

	done chan struct{}
	// pumped is closed when pump returns: the PTY has been read to its
	// end (or closed under it), so wait knows there is nothing left in
	// it worth keeping.
	pumped chan struct{}
}

// Info is a session as a list of them shows it -- Spec plus the two
// facts that only exist once it is running.
type Info struct {
	Spec
	// Live is false for a session whose CLI has exited but which nobody
	// has closed yet: its output is still readable, and closing it is
	// still what takes it off the list.
	Live bool
	// Bytes is how much output this session has produced in total,
	// including whatever the ring has since dropped.
	Bytes int64
}

// Manager is the set of live sessions, keyed by task. It is what the
// orchestrator writes to when it starts one and what pkg/ui reads when
// somebody attaches -- see this package's own doc comment for why those
// two are so far apart.
//
// One session per task, not per run: a task's console run is the task,
// for as long as it lasts, and a second session against the same task
// would be two people typing into two agents with one branch between
// them.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
}

// newPTY is openPTY, held in a variable so a test can widen the window
// between a session being registered and its process starting.
var newPTY = openPTY

// NewManager builds an empty Manager. A daemon has exactly one.
func NewManager() *Manager { return &Manager{sessions: map[string]*Session{}} }

// ErrNoSession is what the Manager reports for a task with no session of
// its own -- one that was never a console task, or whose session has been
// closed.
var ErrNoSession = errors.New("console: no session for this task")

// ErrSessionExists is Start's answer to a task that already has one. It
// means a run was dispatched for a task whose previous console run has
// not finished letting go of the Manager yet, which is a bug rather than
// a race a caller should retry through.
var ErrSessionExists = errors.New("console: this task already has a session")

// Start runs cmd on a fresh PTY and registers the session under
// spec.TaskID. It returns as soon as the process is running: reading its
// output and waiting for it to exit are this package's own goroutines,
// so the caller is free to block on Wait instead.
func (m *Manager) Start(spec Spec, cmd Command) (*Session, error) {
	if spec.TaskID == "" {
		return nil, errors.New("console: a session needs a task to belong to")
	}
	if cmd.Path == "" {
		return nil, errors.New("console: a session needs a command to run")
	}

	m.mu.Lock()
	if _, ok := m.sessions[spec.TaskID]; ok {
		m.mu.Unlock()
		return nil, ErrSessionExists
	}
	// Registered before the process starts, so that two dispatches of the
	// same task cannot both get past the check above.
	s := &Session{spec: spec, cleanup: cmd.Cleanup, done: make(chan struct{}), pumped: make(chan struct{})}
	m.sessions[spec.TaskID] = s
	m.mu.Unlock()

	pty, tty, err := newPTY()
	if err != nil {
		m.drop(spec.TaskID, s)
		return nil, fmt.Errorf("console: opening a pseudo-terminal: %w", err)
	}

	proc := exec.Command(cmd.Path, cmd.Args...)
	proc.Env = append(os.Environ(), cmd.Env...)
	proc.Dir = cmd.Dir
	proc.Stdin, proc.Stdout, proc.Stderr = tty, tty, tty
	// A session of its own with this PTY as its controlling terminal --
	// which also puts the CLI in a fresh process group, so that Close
	// kills the MCP server child it forks rather than orphaning it, the
	// same thing procgroup.Prepare buys every framework's own exec.
	attachTTY(proc)

	if err := proc.Start(); err != nil {
		tty.Close()
		pty.Close()
		m.drop(spec.TaskID, s)
		return nil, fmt.Errorf("console: starting %s: %w", cmd.Path, err)
	}
	_ = procgroup.SacrificeOnOOM(proc.Process.Pid)
	// The child holds its own copy of the slave side now; this one would
	// otherwise keep the PTY open forever and readers would never see EOF.
	tty.Close()

	// Under the lock: the session has been on the Manager's own list
	// since before the process started (see above), so a client can
	// already be holding it and asking it to do things.
	s.mu.Lock()
	s.pty, s.cmd = pty, proc
	closed := s.closed
	s.mu.Unlock()
	// A Close that landed while the process was starting found nothing to
	// kill, and every Close after it returns early because the session is
	// already marked closed -- so this is the only place left to honour
	// it. The goroutines below still run, so Done fires once the killed
	// process has been reaped.
	if closed {
		procgroup.Kill(proc)
		pty.Close()
	}

	go s.pump()
	go s.wait()
	return s, nil
}

// Get returns the session for a task, or nil.
func (m *Manager) Get(taskID string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[taskID]
}

// List returns every session the Manager holds, most recently started
// first -- what the UI's own section of live consoles is built from.
func (m *Manager) List() []Info {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()

	infos := make([]Info, 0, len(sessions))
	for _, s := range sessions {
		infos = append(infos, s.Info())
	}
	// Newest first, and by task id where two started in the same
	// instant, so the order is stable for a client that polls this.
	sortInfos(infos)
	return infos
}

// Close ends a task's session: the CLI is killed, the run driving it
// returns, and the session leaves the Manager. It is the "explicitly
// closed" half of an interactive task -- nothing else here ends one that
// is behaving.
func (m *Manager) Close(taskID string) error {
	s := m.Get(taskID)
	if s == nil {
		return ErrNoSession
	}
	s.Close()
	m.drop(taskID, s)
	return nil
}

// drop removes s from the Manager, but only if s is still the session
// registered under taskID: a task whose console run has already ended and
// been dispatched again must not have its new session unregistered by the
// old one's cleanup.
func (m *Manager) drop(taskID string, s *Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions[taskID] == s {
		delete(m.sessions, taskID)
	}
}

// sortInfos orders a list of sessions newest first, by task id where two
// started in the same instant -- stable, so a client polling the list
// does not see it shuffle.
func sortInfos(infos []Info) {
	sort.Slice(infos, func(i, j int) bool {
		if !infos[i].StartedAt.Equal(infos[j].StartedAt) {
			return infos[i].StartedAt.After(infos[j].StartedAt)
		}
		return infos[i].TaskID < infos[j].TaskID
	})
}

// Info is Spec plus how the session is getting on.
func (s *Session) Info() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Info{Spec: s.spec, Live: !s.ended, Bytes: s.base + int64(len(s.buf))}
}

// Output returns everything the session has printed since offset, the
// offset to ask for next time, and whether anything between the two was
// dropped from the ring before this reader got to it (MaxBuffer).
//
// An offset of 0 is "give me what you have", which for a session that has
// overrun the ring is its recent past rather than its beginning --
// dropped says so, and a terminal emulator on the other end recovers by
// redrawing rather than by asking again.
func (s *Session) Output(offset int64) (data []byte, next int64, dropped bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	end := s.base + int64(len(s.buf))
	switch {
	case offset < s.base:
		dropped = offset > 0 || s.base > 0
		offset = s.base
	case offset > end:
		// A client asking about output that does not exist yet -- a
		// restarted session under the same task, most likely. Give it
		// what there is rather than nothing at all.
		dropped = true
		offset = s.base
	}
	out := make([]byte, end-offset)
	copy(out, s.buf[offset-s.base:])
	return out, end, dropped
}

// Write sends input to the session, exactly as typing it would. It is
// bytes rather than a line because that is what a terminal carries:
// arrow keys, ^C and a bare Return are all things somebody sitting in
// front of an agent CLI needs to send.
func (s *Session) Write(p []byte) error {
	pty, err := s.terminal()
	if err != nil {
		return err
	}
	if _, err := pty.Write(p); err != nil {
		return fmt.Errorf("console: writing to the session: %w", err)
	}
	return nil
}

// Resize tells the session how big the window it is being displayed in
// is, so the CLI redraws to fit rather than to the 80x24 a fresh PTY
// claims. A client sends this when it attaches and whenever the window
// changes.
func (s *Session) Resize(rows, cols int) error {
	if rows <= 0 || cols <= 0 {
		return fmt.Errorf("console: a window of %dx%d is not a size", cols, rows)
	}
	pty, err := s.terminal()
	if err != nil {
		return err
	}
	return resizePTY(pty, rows, cols)
}

// terminal is the master side of the PTY, for a caller that is about to
// write to it -- or the reason there is nothing to write to: a session
// that has ended, or one whose process has not started yet. Both are
// reachable from a client, which holds the session from the moment
// Manager.Start registers it.
func (s *Session) terminal() (*os.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.ended:
		return nil, errors.New("console: this session has ended")
	case s.pty == nil:
		return nil, errors.New("console: this session is still starting")
	}
	return s.pty, nil
}

// Close ends the session: the CLI's whole process group is killed and
// the PTY is closed. It is safe to call more than once, and on a session
// whose CLI has already exited.
func (s *Session) Close() {
	s.mu.Lock()
	already := s.closed
	s.closed = true
	cmd, pty := s.cmd, s.pty
	s.mu.Unlock()
	if already {
		return
	}
	// Best-effort, both of them: a process that has already exited and a
	// terminal that is already closed are what a second closer, or a
	// session whose CLI ended by itself a moment ago, is looking at.
	procgroup.Kill(cmd)
	if pty != nil {
		pty.Close()
	}
}

// Wait blocks until the session ends -- the CLI exiting on its own, or
// somebody closing it -- or until ctx is done, and reports why. It is
// what the run driving this session spends its life in.
func (s *Session) Wait(ctx context.Context) error {
	select {
	case <-s.done:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.endErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Done is Wait for a caller that has its own select to put it in.
func (s *Session) Done() <-chan struct{} { return s.done }

// Transcript is everything the ring still holds, for the run's own record
// once the session has ended. It is the raw bytes the CLI wrote, escape
// sequences and all: what a person saw, not a tidied version of it.
func (s *Session) Transcript() string {
	out, _, _ := s.Output(0)
	return string(out)
}

// pump copies the PTY into the ring until the far end closes it.
func (s *Session) pump() {
	defer close(s.pumped)
	buf := make([]byte, 4096)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			s.append(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// append adds one read's worth of output to the ring, dropping as much
// off the front as it has to.
func (s *Session) append(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf = append(s.buf, p...)
	if over := len(s.buf) - MaxBuffer; over > 0 {
		s.buf = s.buf[over:]
		s.base += int64(over)
	}
}

// wait reaps the CLI and ends the session. A session whose CLI exits on
// its own ends here just as one somebody closed does: there is nothing
// left to type into either way.
func (s *Session) wait() {
	err := s.cmd.Wait()
	s.mu.Lock()
	s.ended, s.endedAt = true, time.Now()
	pty := s.pty
	// A process killed by Close exited because somebody asked it to,
	// which is not a failure of the run -- the wrapper reads `closed`
	// rather than this error to tell the two apart, and the error is
	// kept only for the case nobody asked.
	if !s.closed {
		s.endErr = err
	}
	s.mu.Unlock()
	// The process is gone, but what it printed just before going may not
	// have been read yet -- and closing the master now would throw it
	// away, which for a CLI that exits on its own is usually the error
	// message saying why. Let pump read the PTY dry first (drainLimit).
	drain := time.NewTimer(drainLimit)
	select {
	case <-s.pumped:
	case <-drain.C:
	}
	drain.Stop()
	if pty != nil {
		pty.Close()
	}
	if s.cleanup != nil {
		s.cleanup()
	}
	close(s.done)
}

// ClosedByHand reports whether this session ended because somebody closed
// it rather than because its CLI exited.
func (s *Session) ClosedByHand() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// Duration is how long the session lasted, or has lasted so far.
func (s *Session) Duration(now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended && !s.endedAt.IsZero() {
		return s.endedAt.Sub(s.spec.StartedAt)
	}
	return now.Sub(s.spec.StartedAt)
}
