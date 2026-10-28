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
	"sync"
	"syscall"
	"time"

	"github.com/bwsalmon/grain/pkg/procgroup"
)

// Detached processes (protocol 2): a host CLI or capability server that
// outlives the daemon connection that started it, until its own
// deadline, the way a run host outlives the daemon
// (docs/non-disruptive-upgrade.md, "Run hosts").
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
		p.cancel()
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

// startDetached is OpStart with Detach: the process's output goes to its
// spool, and the connection is attached to it from cursor zero, with
// stdin. When the connection drops the process carries on until it ends
// or reaches its deadline; only its stdin is closed.
func (s *Server) startDetached(ctx context.Context, conn *net.UnixConn, req *Request, spec *Spec) {
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
		fail(err)
		return
	}
	stderr, err := open(spoolStderr)
	if err != nil {
		stdout.Close()
		fail(err)
		return
	}
	// The process lives on hostd's own context, not the connection's: a
	// detached process is bounded by its deadline, a cancel, a drain's
	// cap and hostd stopping -- never by the daemon going away.
	runCtx, cancel := context.WithDeadline(ctx, deadline)
	cmd := exec.CommandContext(runCtx, spec.Path, spec.Args...)
	procgroup.Prepare(cmd)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = WaitDelay
	stdin, err := cmd.StdinPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err == nil {
		sacrificeOnOOM(cmd.Process.Pid)
	}
	stdout.Close()
	stderr.Close()
	if err != nil {
		cancel()
		os.RemoveAll(dir)
		fail(err)
		return
	}
	pid := cmd.Process.Pid
	s.record(pid)
	p := &proc{
		info: Process{ID: id, Run: req.Run, Task: req.Task, Kind: req.Kind, Name: req.Name, PID: pid,
			Started: time.Now().UTC(), Deadline: deadline, Detached: true, Spool: dir},
		detached: true,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
	if b, err := json.Marshal(p.info); err == nil {
		_ = os.WriteFile(filepath.Join(dir, spoolProcess), b, 0o600)
	}
	s.add(id, p)
	s.logf("hostd: started %s %q detached as pid %d (id %s, run %s, deadline %s, spool %s)",
		req.Kind, req.Name, pid, id, req.Run, deadline.Format(time.RFC3339), dir)
	go func() {
		err := cmd.Wait()
		exit := s.reap(cmd, err, runCtx, req)
		cancel()
		s.forget(pid)
		if err := writeExitFile(dir, exit); err != nil {
			s.logf("hostd: recording how %s %q (pid %d) ended: %v", req.Kind, req.Name, pid, err)
		}
		s.mu.Lock()
		p.info.Exited = true
		p.info.Exit = &exit
		s.mu.Unlock()
		close(p.done)
		s.signalChanged()
	}()
	s.attach(conn, p, Cursor{}, stdin)
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
// The connection going away, or a newer attach superseding this one,
// ends the stream and leaves the process running.
func (s *Server) attach(conn *net.UnixConn, p *proc, cur Cursor, stdin io.WriteCloser) {
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
	}()
	if err := writeJSON(conn, frameStarted, Started{PID: info.PID, ID: info.ID}); err != nil {
		if stdin != nil {
			stdin.Close()
		}
		return
	}
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		s.control(conn, info, stdin)
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
			s.release(info.ID, "its exit was read")
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
		case <-p.done:
		case <-ticker.C:
		}
	}
}

// control reads a detached process's connection: stdin, its close, and
// signals, until the connection ends. stdin is closed then -- input that
// arrives while no daemon is attached is not kept.
func (s *Server) control(conn *net.UnixConn, info Process, stdin io.WriteCloser) {
	var once sync.Once
	stdinCh := make(chan []byte, stdinBuffer)
	closeStdin := func() {}
	if stdin != nil {
		go func() {
			defer stdin.Close()
			for b := range stdinCh {
				if _, err := stdin.Write(b); err != nil {
					for range stdinCh {
					}
					return
				}
			}
		}()
		closeStdin = func() { once.Do(func() { close(stdinCh) }) }
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
			_ = syscall.Kill(-info.PID, sig)
		}
	}
}

// loadSpools lists again what a previous hostd left in the spool root: a
// detached process that ended with no daemon attached is attachable
// still, for its final output and its exit. One without an exit record
// was running when that hostd stopped, and went with it (systemd stops
// the unit's whole cgroup, and ReapRecorded the rest), so its spool is
// closed with an exit that says so.
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
			exit = Exit{Code: -1, Error: "grain hostd stopped while it ran", FinishedAt: time.Now().UTC()}
			_ = writeExitFile(dir, exit)
		}
		info.Spool = dir
		info.Exited = true
		info.Exit = &exit
		info.Attached = false
		done := make(chan struct{})
		close(done)
		s.add(info.ID, &proc{info: info, detached: true, cancel: func() {}, done: done})
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
