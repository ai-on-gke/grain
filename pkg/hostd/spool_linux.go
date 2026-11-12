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

//go:build linux

package hostd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ai-on-gke/grain/pkg/procgroup"
)

// Detached processes (protocol 2): a host CLI or capability server that
// outlives the daemon connection that started it, until its own
// deadline, the way a run host outlives the daemon
// (design/design/non-disruptive-upgrade.design.md, "Run hosts").
//
// Its output goes to a spool on the host rather than down the
// connection, laid out as a run host's is (runhost/spool.go) and read the
// same way, from a cursor that only ever moves forward:
//
//	process.json  the listing entry (Process), written at start
//	stdout        append-only: the process's stream, byte for byte
//	stderr        append-only
//	exit.json     written once, by rename, when the process has ended
//	              (Exit, whose code, error and finished_at are a run
//	              host's exit.json). Its presence is what "ended" means
//
// A connection attached to the process -- the one that started it, or a
// later OpAttach -- is sent what the spool holds past its cursor as it
// arrives, then the exit. Once the exit has gone to a daemon, the spool
// is removed: the daemon has all of it. A spool whose exit no daemon came
// back for is kept for ExitedRetention, across a hostd restart too.
const (
	spoolProcess = "process.json"
	spoolStdout  = "stdout"
	spoolStderr  = "stderr"
	spoolExit    = "exit.json"
)

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "p-" + hex.EncodeToString(b[:])
}

func (s *Server) add(id string, p *proc) {
	s.init()
	s.mu.Lock()
	s.procs[id] = p
	s.mu.Unlock()
}

func (s *Server) drop(id string) {
	s.mu.Lock()
	delete(s.procs, id)
	s.mu.Unlock()
	s.signalChanged()
}

func (s *Server) lookup(id string) *proc {
	s.init()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.procs[id]
}

// Processes is OpProcesses' listing: every process hostd is running, and
// every detached one whose final spool no daemon has read yet.
func (s *Server) Processes() []Process {
	s.init()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Process, 0, len(s.procs))
	for _, p := range s.procs {
		info := p.info
		if p.detached {
			info.Cursor = spoolSizes(info.Spool)
		}
		out = append(out, info)
	}
	return out
}

func spoolSizes(dir string) Cursor {
	var c Cursor
	if st, err := os.Stat(filepath.Join(dir, spoolStdout)); err == nil {
		c.Stdout = st.Size()
	}
	if st, err := os.Stat(filepath.Join(dir, spoolStderr)); err == nil {
		c.Stderr = st.Size()
	}
	return c
}

// spoolRoot is where detached processes' spools go: under StateDir, so
// they survive a hostd restart, or a directory of this process's own
// when there is none.
func (s *Server) spoolRoot() (string, error) {
	if s.StateDir != "" {
		root := filepath.Join(s.StateDir, "spool")
		return root, os.MkdirAll(root, 0o700)
	}
	root := filepath.Join(os.TempDir(), fmt.Sprintf("grain-hostd-spool-%d", os.Getuid()))
	return root, os.MkdirAll(root, 0o700)
}

// release drops id: its group is killed if it still runs, and its spool
// removed. A process nobody claims is released this way, the normal
// cancel path, and so is one whose exit a daemon has read.
func (s *Server) release(id, why string) {
	p := s.lookup(id)
	if p == nil {
		return
	}
	s.mu.Lock()
	exited := p.info.Exited
	info := p.info
	s.mu.Unlock()
	if !exited {
		s.logf("hostd: releasing %s %q (pid %d, run %s): %s", info.Kind, info.Name, info.PID, info.Run, why)
		p.cancel(killCanceled)
		_ = syscall.Kill(-info.PID, syscall.SIGKILL)
		select {
		case <-p.done:
		case <-time.After(WaitDelay + time.Second):
		}
	}
	if !p.detached {
		return
	}
	s.drop(id)
	if info.Spool != "" {
		_ = os.RemoveAll(info.Spool)
	}
}

func (s *Server) newDetachedProc(info Process, stdinOpen bool) *proc {
	p := &proc{
		info:      info,
		detached:  true,
		stdinOpen: stdinOpen,
		handover:  make(chan struct{}),
		watchDone: make(chan struct{}),
		done:      make(chan struct{}),
	}
	p.cancel = func(kind killKind) {
		s.mu.Lock()
		exited := p.info.Exited
		if !exited && p.killKind == killNone {
			p.killKind = kind
		}
		pid := p.info.PID
		s.mu.Unlock()
		if !exited && pid > 1 {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	}
	return p
}

// startDetached is OpStart with Detach: the process's output goes to its
// spool, and the connection is attached to it from cursor zero, with
// stdin. When the connection drops the process carries on until it ends
// or reaches its deadline; only its stdin is closed. Across a handover
// re-exec (StartHandover) the process keeps running and is adopted from
// its spool by the replacement hostd (loadSpools).
func (s *Server) startDetached(_ context.Context, conn net.Conn, req *Request, spec *Spec, onAdded func()) {
	deadline := time.Now().Add(spec.MaxDuration)
	if !req.Deadline.IsZero() && req.Deadline.Before(deadline) {
		deadline = req.Deadline
	}
	fail := func(err error) {
		s.logf("hostd: %s %q did not start: %v", req.Kind, req.Name, err)
		_ = writeJSON(conn, frameExit, Exit{Code: -1, Error: err.Error()})
	}
	root, err := s.spoolRoot()
	if err != nil {
		fail(err)
		return
	}
	id := newID()
	dir := filepath.Join(root, id)
	if err := os.Mkdir(dir, 0o700); err != nil {
		fail(err)
		return
	}
	open := func(name string) (*os.File, error) {
		return os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	}
	stdout, err := open(spoolStdout)
	if err != nil {
		os.RemoveAll(dir)
		fail(err)
		return
	}
	stderr, err := open(spoolStderr)
	if err != nil {
		stdout.Close()
		os.RemoveAll(dir)
		fail(err)
		return
	}
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		stdout.Close()
		stderr.Close()
		os.RemoveAll(dir)
		fail(err)
		return
	}
	cmd := exec.Command(spec.Path, spec.Args...)
	procgroup.Prepare(cmd)
	cmd.Cancel = nil // watchDetached enforces deadlines and cancels across handovers
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	cmd.Stdin = stdinR
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = cmd.Start()
	if err == nil {
		sacrificeOnOOM(cmd.Process.Pid)
	}
	stdinR.Close()
	stdout.Close()
	stderr.Close()
	if err != nil {
		stdinW.Close()
		os.RemoveAll(dir)
		fail(err)
		return
	}
	pid := cmd.Process.Pid
	s.record(pid)
	p := s.newDetachedProc(Process{
		ID: id, Run: req.Run, Task: req.Task, Kind: req.Kind, Name: req.Name, PID: pid,
		Started: time.Now().UTC(), Deadline: deadline, Detached: true, Spool: dir,
	}, true)
	if b, err := json.Marshal(p.info); err == nil {
		_ = os.WriteFile(filepath.Join(dir, spoolProcess), b, 0o600)
	}
	s.add(id, p)
	onAdded()
	s.logf("hostd: started %s %q detached as pid %d (id %s, run %s, deadline %s, spool %s)",
		req.Kind, req.Name, pid, id, req.Run, deadline.Format(time.RFC3339), dir)
	go s.watchDetached(p)
	s.attach(conn, p, Cursor{}, stdinW)
}

func (s *Server) watchDetached(p *proc) {
	defer close(p.watchDone)
	s.mu.Lock()
	info := p.info
	s.mu.Unlock()
	if !info.Deadline.IsZero() {
		d := time.Until(info.Deadline)
		if d <= 0 {
			p.cancel(killDeadline)
		} else {
			timer := time.AfterFunc(d, func() { p.cancel(killDeadline) })
			defer timer.Stop()
		}
	}
	ws, reaped, err := waitPidOrStop(info.PID, p.handover)
	if !reaped && err == nil {
		// Handover: leave the child, its process group, its state-dir
		// start-time record, and its spool untouched for the next hostd.
		return
	}
	s.mu.Lock()
	killed := p.killKind
	s.mu.Unlock()
	exit := s.reapDetached(info, ws, reaped, err, killed)
	s.forget(info.PID)
	if werr := writeExitFile(info.Spool, exit); werr != nil {
		s.logf("hostd: recording how %s %q (pid %d) ended: %v", info.Kind, info.Name, info.PID, werr)
	}
	s.mu.Lock()
	p.info.Exited = true
	p.info.Exit = &exit
	s.mu.Unlock()
	close(p.done)
	s.signalChanged()
}

func (s *Server) reapDetached(info Process, ws syscall.WaitStatus, reaped bool, err error, killed killKind) Exit {
	exit := Exit{Code: -1}
	if reaped {
		if ws.Exited() {
			exit.Code = ws.ExitStatus()
		} else if ws.Signaled() {
			exit.Signal = signalName(ws.Signal())
		}
	} else if err != nil {
		exit.Error = err.Error()
	}
	switch {
	case killed == killDraining:
		exit.Draining = true
	case killed == killDeadline:
		exit.Deadline = true
	}
	exit.FinishedAt = time.Now().UTC()
	if info.PID > 1 {
		_ = syscall.Kill(-info.PID, syscall.SIGKILL)
	}
	s.logf("hostd: %s %q (pid %d) ended: code %d signal %q deadline %v draining %v",
		info.Kind, info.Name, info.PID, exit.Code, exit.Signal, exit.Deadline, exit.Draining)
	return exit
}

// waitPidOrStop waits for child pid to terminate and reaps it with Wait4,
// or returns (0, false, nil) without reaping if stop closes first (handover).
func waitPidOrStop(pid int, stop <-chan struct{}) (syscall.WaitStatus, bool, error) {
	var ws syscall.WaitStatus
	select {
	case <-stop:
		return 0, false, nil
	default:
	}
	if wpid, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil); wpid == pid {
		return ws, true, nil
	} else if err != nil && !errors.Is(err, syscall.EINTR) {
		return 0, false, err
	}
	pidfd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			if wpid, werr := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil); wpid == pid {
				return ws, true, nil
			} else if werr != nil {
				return 0, false, werr
			}
		}
		return waitPidPollFallback(pid, stop)
	}
	defer syscall.Close(pidfd)
	efd, err := unix.Eventfd(0, unix.EFD_NONBLOCK|unix.EFD_CLOEXEC)
	if err != nil {
		return waitPidPollFallback(pid, stop)
	}
	defer syscall.Close(efd)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-stop:
			one := [8]byte{1}
			_, _ = syscall.Write(efd, one[:])
		case <-done:
		}
	}()
	for {
		select {
		case <-stop:
			return 0, false, nil
		default:
		}
		if wpid, werr := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil); wpid == pid {
			return ws, true, nil
		} else if werr != nil && !errors.Is(werr, syscall.EINTR) {
			return 0, false, werr
		}
		fds := []unix.PollFd{
			{Fd: int32(pidfd), Events: unix.POLLIN},
			{Fd: int32(efd), Events: unix.POLLIN},
		}
		if _, perr := unix.Poll(fds, -1); perr != nil && !errors.Is(perr, syscall.EINTR) {
			return waitPidPollFallback(pid, stop)
		}
	}
}

func waitPidPollFallback(pid int, stop <-chan struct{}) (syscall.WaitStatus, bool, error) {
	var ws syscall.WaitStatus
	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return 0, false, nil
		default:
		}
		if wpid, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil); wpid == pid {
			return ws, true, nil
		} else if err != nil && !errors.Is(err, syscall.EINTR) {
			return 0, false, err
		}
		select {
		case <-stop:
			return 0, false, nil
		case <-t.C:
		}
	}
}

// writeExitFile writes exit.json by rename, so a reader sees all of it or
// none -- as runhost's writeExit does.
func writeExitFile(dir string, e Exit) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, spoolExit+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, spoolExit))
}

// attach streams p's spool to conn from cur until p has ended, then its
// exit, after which p is released: the daemon has all of it. stdin, when
// not nil, is the process's, fed from the connection's stdin frames and
// closed when the connection ends. Signals act on the process's group.
//
// The connection going away, a newer attach superseding this one, or
// hostd detaching streams for a handover ends the stream and leaves the
// process running.
func (s *Server) attach(conn net.Conn, p *proc, cur Cursor, stdin io.WriteCloser) {
	s.mu.Lock()
	p.attachGen++
	gen := p.attachGen
	p.info.Attached = true
	info := p.info
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if p.attachGen == gen {
			p.info.Attached = false
		}
		s.mu.Unlock()
		// A drain waits for an ended process's stream to let go.
		s.signalChanged()
	}()
	if err := writeJSON(conn, frameStarted, Started{PID: info.PID, ID: info.ID}); err != nil {
		if stdin != nil {
			stdin.Close()
			s.mu.Lock()
			p.stdinOpen = false
			s.mu.Unlock()
			s.signalChanged()
		}
		return
	}
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		s.control(conn, p, stdin)
	}()
	pump := func(name string, typ byte, at *int64) error {
		f, err := os.Open(filepath.Join(info.Spool, name))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		defer f.Close()
		if _, err := f.Seek(*at, io.SeekStart); err != nil {
			return err
		}
		buf := make([]byte, maxFrame)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				if werr := writeFrame(conn, typ, buf[:n]); werr != nil {
					return werr
				}
				*at += int64(n)
			}
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
		}
	}
	ticker := time.NewTicker(s.poll())
	defer ticker.Stop()
	for {
		var ended bool
		select {
		case <-p.done:
			ended = true
		default:
		}
		if err := pump(spoolStdout, frameStdout, &cur.Stdout); err != nil {
			return
		}
		if err := pump(spoolStderr, frameStderr, &cur.Stderr); err != nil {
			return
		}
		if ended {
			s.mu.Lock()
			exit := *p.info.Exit
			current := p.attachGen == gen
			s.mu.Unlock()
			if !current {
				return
			}
			if err := writeJSON(conn, frameExit, exit); err != nil {
				return
			}
			// Written is not yet read: a session closed now drops the
			// frames the daemon's end has not read yet, Exit included. The
			// daemon hangs up once it has the Exit, so until then -- or
			// DrainExitGrace -- the process stays attached, which holds a
			// drained hostd's exit (waitExitsDelivered) back for it.
			select {
			case <-gone:
			case <-time.After(DrainExitGrace):
			}
			// A daemon that lost this stream before reading the Exit may
			// have attached again meanwhile; that stream delivers the Exit
			// and releases the process, so this one must not remove the
			// spool from under it.
			s.mu.Lock()
			current = p.attachGen == gen
			s.mu.Unlock()
			if !current {
				return
			}
			s.release(info.ID, "its exit was read")
			return
		}
		if s.handedOverNow() {
			s.logf("hostd: detaching stream of %s %q (pid %d, id %s) for handover; the process keeps running",
				info.Kind, info.Name, info.PID, info.ID)
			return
		}
		s.mu.Lock()
		superseded := p.attachGen != gen
		s.mu.Unlock()
		if superseded {
			return
		}
		select {
		case <-gone:
			s.logf("hostd: the daemon went away from %s %q (pid %d, id %s); it keeps running until it ends or reaches its deadline",
				info.Kind, info.Name, info.PID, info.ID)
			return
		case <-s.handoverCh:
		case <-p.done:
		case <-ticker.C:
		}
	}
}

// control reads a detached process's connection: stdin, its close, and
// signals, until the connection ends. stdin is closed then -- input that
// arrives while no daemon is attached is not kept.
func (s *Server) control(conn net.Conn, p *proc, stdin io.WriteCloser) {
	s.mu.Lock()
	info := p.info
	s.mu.Unlock()
	var once sync.Once
	stdinCh := make(chan []byte, stdinBuffer)
	closeStdin := func() {}
	if stdin != nil {
		doneStdin := make(chan struct{})
		go func() {
			defer close(doneStdin)
			defer func() {
				_ = stdin.Close()
				s.mu.Lock()
				p.stdinOpen = false
				s.mu.Unlock()
				s.signalChanged()
			}()
			for b := range stdinCh {
				if _, err := stdin.Write(b); err != nil {
					for range stdinCh {
					}
					return
				}
			}
		}()
		closeStdin = func() {
			once.Do(func() { close(stdinCh) })
			<-doneStdin
		}
	}
	defer closeStdin()
	open := stdin != nil
	for {
		typ, payload, err := readFrame(conn)
		if err != nil {
			return
		}
		switch typ {
		case frameStdin:
			if !open {
				continue
			}
			select {
			case stdinCh <- payload:
			default:
				s.logf("hostd: %s %q (pid %d) is not reading its stdin; closing it", info.Kind, info.Name, info.PID)
				open = false
				closeStdin()
			}
		case frameStdinClose:
			open = false
			closeStdin()
		case frameSignal:
			var m SignalMsg
			if json.Unmarshal(payload, &m) != nil {
				continue
			}
			sig, ok := signals[m.Signal]
			if !ok {
				s.logf("hostd: refused signal %q for pid %d", m.Signal, info.PID)
				continue
			}
			// Once reaped, the process group's id is free for reuse, and
			// attach keeps this connection open after the Exit until the
			// daemon hangs up: a signal sent then must not reach whatever
			// group has taken the id since.
			select {
			case <-p.done:
				continue
			default:
			}
			_ = syscall.Kill(-info.PID, sig)
		}
	}
}

func (s *Server) canAdoptLiveSpool(pid int) bool {
	if pid <= 1 || s.StateDir == "" {
		return false
	}
	want, err := os.ReadFile(filepath.Join(s.StateDir, strconv.Itoa(pid)))
	if err != nil || len(want) == 0 {
		return false
	}
	ppid, got, err := procStatFields(pid)
	if err != nil || got != string(want) {
		return false
	}
	return ppid == os.Getpid()
}

// loadSpools lists again what a previous hostd left in the spool root: a
// detached process that ended with no daemon attached is attachable
// still, for its final output and its exit; one still running as a child
// of this process (after an in-place handover re-exec) is adopted live
// with its deadline and process group intact. One without an exit record
// whose process did not survive hostd stopping is closed with an
// Exit.Draining record saying so.
func (s *Server) loadSpools() {
	if s.StateDir == "" {
		return
	}
	root := filepath.Join(s.StateDir, "spool")
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		dir := filepath.Join(root, e.Name())
		var info Process
		b, err := os.ReadFile(filepath.Join(dir, spoolProcess))
		if err != nil || json.Unmarshal(b, &info) != nil || info.ID != e.Name() {
			s.logf("hostd: removing %s: not a spool this hostd can read", dir)
			_ = os.RemoveAll(dir)
			continue
		}
		var exit Exit
		b, err = os.ReadFile(filepath.Join(dir, spoolExit))
		if err != nil || json.Unmarshal(b, &exit) != nil {
			if s.canAdoptLiveSpool(info.PID) {
				info.Spool = dir
				info.Exited = false
				info.Exit = nil
				info.Attached = false
				p := s.newDetachedProc(info, false)
				s.add(info.ID, p)
				go s.watchDetached(p)
				s.logf("hostd: adopted live detached %s %q (pid %d, run %s, id %s)",
					info.Kind, info.Name, info.PID, info.Run, info.ID)
				continue
			}
			exit = Exit{Code: -1, Draining: true, Error: "the host agent stopped while it ran", FinishedAt: time.Now().UTC()}
			_ = writeExitFile(dir, exit)
		}
		// The pid's start-time record is left for ReapRecorded, which runs
		// next: a process that survived the previous hostd without being
		// this one's child is still its to kill, not to leave running
		// unsupervised while its spool says it ended.
		info.Spool = dir
		info.Exited = true
		info.Exit = &exit
		info.Attached = false
		done := make(chan struct{})
		close(done)
		s.add(info.ID, &proc{info: info, detached: true, cancel: func(killKind) {}, done: done})
		s.logf("hostd: kept the final spool of %s %q (run %s, id %s) for a daemon to read", info.Kind, info.Name, info.Run, info.ID)
	}
}

// sweepExited drops the final spools no daemon came back for within
// ExitedRetention.
func (s *Server) sweepExited(ctx context.Context) {
	keep := s.ExitedRetention
	if keep == 0 {
		keep = DefaultExitedRetention
	}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		var old []string
		s.mu.Lock()
		for id, p := range s.procs {
			if p.detached && p.info.Exited && !p.info.Attached && p.info.Exit != nil &&
				time.Since(p.info.Exit.FinishedAt) > keep {
				old = append(old, id)
			}
		}
		s.mu.Unlock()
		for _, id := range old {
			s.release(id, fmt.Sprintf("no daemon read its exit within %s", keep))
		}
	}
}
