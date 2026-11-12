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
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ai-on-gke/grain/pkg/procgroup"
)

// Server is a running hostd.
type Server struct {
	// Socket is the unix socket path. Its directory is created 0700 if
	// missing, and the socket itself is 0600.
	Socket string
	// Allowlist is the allowlist path, re-read for every request so an
	// operator's edit applies to the next one without a restart.
	Allowlist string
	// AllowlistOwners are the uids that may own the allowlist; root alone
	// when nil.
	AllowlistOwners []uint32
	// StateDir records the process groups hostd has started, so a
	// restarted hostd can reap what the last one left (ReapRecorded).
	StateDir string
	// DisconnectGrace is DefaultDisconnectGrace when zero.
	DisconnectGrace time.Duration
	// Env is the environment entries draw PATH, identity and pass_env
	// from; os.Environ() when nil.
	Env []string
	// Logf is log.Printf when nil.
	Logf func(format string, args ...any)
	// Build is this hostd's version, for Hello.
	Build string
	// AgentsOnly says, in Hello, that this host agent runs agent CLIs
	// only (grain-agentd).
	AgentsOnly bool
	// CurrentLink is the installer's current link hostd was started
	// through (CurrentLinkFor), for Hello's Running and Current; "" for
	// none.
	CurrentLink string
	// ExitedRetention is how long the final spool of a detached process
	// no daemon came back for is kept; DefaultExitedRetention when zero.
	ExitedRetention time.Duration
	// Poll is how often an attached spool is read; 100 ms when zero.
	Poll time.Duration
	// Remote, when set, is how this hostd dials out to a controller that
	// cannot reach its socket -- a daemon in a Kubernetes cluster -- and
	// serves every stream that controller opens over the session as it
	// would a connection on Socket (remote_linux.go). Socket may then be
	// "" for no local listener at all.
	Remote func(ctx context.Context) (net.Conn, error)
	// RemoteMinBackoff and RemoteMaxBackoff bound the wait between
	// reconnects to Remote: 1s and defaultRemoteMaxBackoff (5s) when zero.
	RemoteMinBackoff, RemoteMaxBackoff time.Duration
	// RenewName, RenewKey, RenewInterval, CurrentToken, and OnTokenRenewed
	// silently renew this host's 30-day key-bound credential over the live
	// mux session while connected (DefaultRenewInterval when RenewInterval <= 0).
	RenewName      string
	RenewKey       ed25519.PrivateKey
	RenewInterval  time.Duration
	CurrentToken   func() string
	OnTokenRenewed func(token string, expiresAt time.Time)
	// RenewedAt is when the credential CurrentToken returns was issued --
	// its token file's modification time -- so that a restarted host
	// agent renews on its credential's schedule rather than RenewInterval
	// after it started. Zero counts from the first connection. Either
	// way the schedule outlives a reconnect: a host whose connection drops
	// more often than RenewInterval still renews (renewDue).
	RenewedAt time.Time
	// StageRoot is where OpStage may write on this machine, for a run a
	// daemon reaches over Remote: "~/.grain/hostd-stage" under hostd's
	// home when empty (stageRoot). It should be a directory nothing else
	// keeps files in: that daemon may read, write and remove anything
	// under it, and every CLI entry may be handed paths there.
	StageRoot string
	// Builtins are this build's own capabilities entries
	// (intree.HostdBuiltins). Each one the allowlist has no entry of that
	// name for is offered anyway, running BuiltinCommand -- this hostd's
	// own binary -- so a server a new build registers runs on the host as
	// soon as hostd is on that build, whether or not anything has written
	// its entry into the file yet (EnsureBuiltins, which adds the same
	// entries as root at every start, so this offers nothing the file
	// would not soon hold). Only while BuiltinCommand is a regular file
	// that only AllowlistOwners can write, as the installer's versions
	// are: hostd never runs a binary the daemon's user could have
	// replaced. An entry the file has always wins.
	Builtins       []BuiltinEntry
	BuiltinCommand string
	// Reexec, when set, replaces this process with the new hostd binary once
	// Serve returns from a handover (StartHandover): same pid, so systemd
	// keeps the unit's cgroup and the next hostd inherits every live detached
	// child as its own parent (loadSpools). Nil means this Server has no way
	// to hand live children to a replacement in the same pid, so OpDrain with
	// Handover falls back to Cutover unless AllowHandover is set (tests).
	Reexec func() error
	// AllowHandover lets StartHandover leave live detached children running
	// when Serve returns without Reexec, for an in-process test that starts
	// the replacement Server in the same process.
	AllowHandover bool

	// drainExitGrace overrides DrainExitGrace in tests when positive.
	drainExitGrace time.Duration

	uid uint32
	wg  sync.WaitGroup

	// renewMu guards renewedAt: when this host's credential was last
	// issued or renewed, across every connection this Server makes.
	renewMu   sync.Mutex
	renewedAt time.Time

	mu    sync.Mutex
	procs map[string]*proc
	// draining is a drain that refuses new CLI starts (Drain); cutover
	// one that still takes them and ends at the first moment nothing
	// runs (Cutover); handover one that leaves live detached CLI runs
	// running across Reexec (StartHandover); handedOver records that
	// waitDrained exited by handover; closing is that moment, once
	// waitDrained has seen it, after which nothing starts at all.
	draining   bool
	cutover    bool
	handover   bool
	handedOver bool
	closing    bool
	// cliStarts counts the starts of anything but a capability server
	// between the admission check in serveConn and the process's entry in
	// procs (or its failing to start), so a start in that window still
	// holds a drain or handover open.
	cliStarts int
	// listeners counts open OpListen loopback listeners, which belong to
	// the remote session and cannot cross a re-exec.
	listeners  int
	drainCh    chan struct{}
	drainOnce  sync.Once
	handoverCh chan struct{}
	// changed is signalled whenever a process ends or is dropped, so a
	// drain can tell when nothing is left running.
	changed chan struct{}
}

// DefaultExitedRetention is Server.ExitedRetention when unset: long enough
// for a daemon that is down for a while to come back and read how its
// run ended, short enough that abandoned spools do not pile up.
const DefaultExitedRetention = 24 * time.Hour

type killKind int

const (
	killNone killKind = iota
	killCanceled
	killDeadline
	killDraining
)

// proc is one process hostd started.
type proc struct {
	info Process // guarded by Server.mu
	// detached is a protocol-2 start, spooled and attachable.
	detached bool
	// stdinOpen is true while a detached start is still receiving its
	// initial stdin from the connection that started it (guarded by Server.mu).
	stdinOpen bool
	// killKind is the first reason hostd itself killed the process
	// (guarded by Server.mu).
	killKind  killKind
	cancel    func(killKind)
	handover  chan struct{}
	watchDone chan struct{}
	done      chan struct{}
	// attachGen counts attaches; an attach whose generation is no longer
	// current lets go, so a re-attaching daemon supersedes a dead one.
	attachGen int
}

func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// loadAllowlist is LoadAllowlist of s.Allowlist, with the build's own
// entries it lacks (Builtins), and their names.
func (s *Server) loadAllowlist() (*Allowlist, []string, error) {
	a, err := LoadAllowlist(s.Allowlist, s.owners())
	if err != nil || len(s.Builtins) == 0 || s.BuiltinCommand == "" {
		return a, nil, err
	}
	if !filepath.IsAbs(s.BuiltinCommand) || filepath.Clean(s.BuiltinCommand) != s.BuiltinCommand {
		return a, nil, nil
	}
	if err := checkFileOwnedBy("grain hostd's own binary", s.BuiltinCommand, s.owners()); err != nil {
		return a, nil, nil
	}
	return a, a.withBuiltins(s.Builtins, s.BuiltinCommand), nil
}

func (s *Server) owners() []uint32 {
	if s.AllowlistOwners != nil {
		return s.AllowlistOwners
	}
	return []uint32{0}
}

// Serve reaps whatever a previous hostd left running, checks the
// allowlist once so a broken one fails the start rather than the first
// run, and serves the socket until ctx is done. Processes still running
// then are killed before Serve returns.
func (s *Server) Serve(ctx context.Context) error {
	if os.Geteuid() == 0 {
		return errors.New("grain hostd: refusing to run as root; run it as the user grain was installed for")
	}
	if s.uid == 0 {
		s.uid = uint32(os.Geteuid())
	}
	if s.DisconnectGrace == 0 {
		s.DisconnectGrace = DefaultDisconnectGrace
	}
	s.init()
	if s.StateDir != "" {
		if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
			return err
		}
		s.loadSpools()
		s.ReapRecorded()
	}
	a, implicit, err := s.loadAllowlist()
	if err != nil {
		return err
	}
	s.logf("hostd: allowlist %s offers %d entries", s.Allowlist, len(a.Offers()))
	if len(implicit) > 0 {
		s.logf("hostd: offering grain's own entries %s, which the allowlist lacks, as %s (grain-hostd-allowlist.service adds them to the file)",
			strings.Join(implicit, ", "), s.BuiltinCommand)
	} else if len(s.Builtins) > 0 && s.BuiltinCommand != "" {
		if err := checkFileOwnedBy("grain hostd's own binary", s.BuiltinCommand, s.owners()); err != nil {
			s.logf("hostd: offering only the allowlist's own entries, not grain's built-in ones it may lack: %v", err)
		}
	}
	if s.Socket == "" && s.Remote == nil {
		return errors.New("grain hostd: nothing to serve: no -socket to listen on and no -server to dial out to")
	}
	var ln *net.UnixListener
	if s.Socket != "" {
		ln, err = s.listen()
		if err != nil {
			return err
		}
		s.logf("hostd: listening on %s (hostd protocol %d, build %s)", s.Socket, ProtocolVersion, s.Build)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopOnCancel := context.AfterFunc(ctx, func() {
		if !s.handedOverNow() {
			s.cancelRunning(killCanceled)
		}
	})
	defer stopOnCancel()
	if ln != nil {
		go func() {
			<-ctx.Done()
			ln.Close()
			_ = os.Remove(s.Socket)
		}()
	}
	go s.sweepExited(ctx)
	// The drain watcher logs through s.Logf, which may be a test's t.Logf:
	// Serve waits for it below, so nothing it says outlives Serve.
	drainWatched := make(chan struct{})
	go func() {
		defer close(drainWatched)
		select {
		case <-ctx.Done():
		case <-s.drainCh:
			s.waitDrained(ctx)
			cancel()
		}
	}()
	if s.Remote != nil {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serveRemote(ctx)
		}()
	}
	if ln == nil {
		<-ctx.Done()
	}
	for ln != nil {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			s.logf("hostd: accept: %v", err)
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(ctx, conn.(*net.UnixConn))
		}()
	}
	s.wg.Wait()
	// The accept loop ends only once ctx is done, and waitDrained returns
	// on a done ctx, so this wait is short.
	cancel()
	<-drainWatched
	if s.handedOverNow() {
		s.releaseHandoverWatchers()
		if s.Reexec != nil {
			s.logf("hostd: execing new build in place (pid %d)", os.Getpid())
			if err := s.Reexec(); err != nil {
				s.logf("hostd: exec onto the new build failed (%v); stopping so systemd restarts it", err)
				s.cancelRunning(killDraining)
				s.waitRunningDone()
				return err
			}
		}
		return nil
	}
	s.cancelRunning(killCanceled)
	s.waitRunningDone()
	return nil
}

func (s *Server) init() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.procs == nil {
		s.procs = map[string]*proc{}
		s.drainCh = make(chan struct{})
		s.handoverCh = make(chan struct{})
		s.changed = make(chan struct{}, 1)
	}
}

func (s *Server) canHandover() bool {
	return s.Reexec != nil || s.AllowHandover
}

func (s *Server) handedOverNow() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handedOver
}

func (s *Server) cancelRunning(kind killKind) {
	s.mu.Lock()
	var live []*proc
	for _, p := range s.procs {
		if !p.info.Exited {
			live = append(live, p)
		}
	}
	s.mu.Unlock()
	for _, p := range live {
		p.cancel(kind)
	}
}

func (s *Server) waitRunningDone() {
	s.mu.Lock()
	var live []*proc
	for _, p := range s.procs {
		if !p.info.Exited {
			live = append(live, p)
		}
	}
	s.mu.Unlock()
	for _, p := range live {
		select {
		case <-p.done:
		case <-time.After(WaitDelay + time.Second):
		}
	}
}

func (s *Server) releaseHandoverWatchers() {
	s.mu.Lock()
	var live []*proc
	for _, p := range s.procs {
		if p.detached && !p.info.Exited && p.handover != nil {
			live = append(live, p)
		}
	}
	s.mu.Unlock()
	for _, p := range live {
		close(p.handover)
	}
	for _, p := range live {
		<-p.watchDone
	}
}

func (s *Server) poll() time.Duration {
	if s.Poll > 0 {
		return s.Poll
	}
	return 100 * time.Millisecond
}

// signalChanged wakes a drain waiting on the process table.
func (s *Server) signalChanged() {
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

// Drain starts a graceful drain for an upgrade: new CLI starts are
// refused with a draining Refusal, while running processes -- and
// daemons listing, attaching to and cancelling them -- carry on. A
// capability server still starts: gitproxy's ambient credentials for the
// very runs the drain waits on come from one. Once no run is left
// running, or at the allowlist's drain_cap, Serve returns, and systemd
// starts the new binary in its place. A process still running at the cap
// is cancelled with Exit.Draining set, and that is logged.
//
// A detached process that has ended before the drain finishes keeps its
// spool and its exit record on disk, and the next hostd lists it again
// (loadSpools), so a daemon that was not attached loses nothing.
func (s *Server) Drain() {
	if s.refuseStarts() {
		s.mu.Lock()
		s.startDrainLocked()
		s.mu.Unlock()
	}
}

// refuseStarts is the first half of Drain: from its return every new CLI
// start is refused, but the drain loop that exits hostd has not been
// woken. It reports whether this call began the drain, in which case the
// caller must go on to startDrainLocked.
func (s *Server) refuseStarts() bool {
	s.init()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.draining {
		return false
	}
	s.draining = true
	s.logf("hostd: draining for an upgrade: refusing new starts; %d process(es) still running", s.runningLocked())
	return true
}

// Cutover is the drain an upgrade to a build speaking this one's protocol
// asks for when handover is not used (OpDrain with Idle): new starts are
// still taken, and Serve returns at the first moment no run's process is
// running, so the new binary takes over without a daemon's run being
// refused or cut short -- a standing chat session's CLI included, which
// runs a turn at a time and is never running for good. Only if no such
// moment comes within the allowlist's drain_cap does it turn into Drain,
// which refuses new starts so the runs still going can finish, for up to
// drain_cap again.
func (s *Server) Cutover() {
	if s.markCutover() {
		s.beginCutover()
	}
}

// markCutover records a cutover, so Hello reports it, without yet
// starting the drain that may exit hostd at once; beginCutover starts it.
// OpDrain marks before it answers and begins after, so a daemon told the
// cutover began never sees a Hello that says otherwise. It reports
// whether this call marked it.
func (s *Server) markCutover() bool {
	s.init()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.draining || s.cutover {
		return false
	}
	s.cutover = true
	return true
}

// beginCutover starts the drain markCutover recorded.
func (s *Server) beginCutover() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startDrainLocked()
	s.logf("hostd: cutting over to a new build: still taking new starts, and exiting the moment no run is running; %d process(es) running now", s.runningLocked())
}

// StartHandover hands live detached CLI runs over to the replacement
// hostd without waiting for them to finish or refusing new starts: once
// no protocol-1 CLI, carried listener, or initial detached stdin is in
// flight, Serve detaches active stream connections (which the daemon
// re-attaches on the replacement hostd), leaves live detached children
// running, and calls Reexec in the same pid. If this Server has neither
// Reexec nor AllowHandover, it falls back to Cutover.
func (s *Server) StartHandover() {
	if !s.canHandover() {
		s.Cutover()
		return
	}
	if s.markHandover() {
		s.beginHandover()
	}
}

// markHandover is markCutover for a handover.
func (s *Server) markHandover() bool {
	s.init()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.draining || s.handover {
		return false
	}
	s.cutover = true
	s.handover = true
	return true
}

// beginHandover starts the drain markHandover recorded.
func (s *Server) beginHandover() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startDrainLocked()
	s.logf("hostd: handing over to a new build: keeping live detached runs across re-exec; %d process(es) running now", s.runningLocked())
}

// CuttingOver reports whether Cutover or StartHandover has been called.
func (s *Server) CuttingOver() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cutover
}

func (s *Server) startDrainLocked() {
	s.drainOnce.Do(func() { close(s.drainCh) })
}

// Draining reports whether hostd refuses new starts: Drain was called, or
// a cutover/handover is closing the listener to exit or re-exec.
func (s *Server) Draining() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.draining || s.closing
}

func (s *Server) runningLocked() int {
	n := 0
	for _, p := range s.procs {
		if !p.info.Exited {
			n++
		}
	}
	return n
}

// waitDrained returns once no non-handover process is running, cancelling
// whatever still is at the drain cap first. For a cutover the first
// drain_cap is spent still taking starts, and only the second one ends in
// a cancel (Cutover). For a handover (StartHandover), live detached CLI
// processes whose initial stdin has closed are kept running for the
// replacement hostd to adopt, so waitDrained finishes without waiting for
// them or refusing new starts.
//
// drain_cap is the allowlist's as it is on each pass, not as it was when
// the drain began: an operator who shortens it for a drain already under
// way is heeded, and a read that fails -- the file mid-edit -- keeps the
// cap last read rather than falling back to DefaultDrainCap for good.
//
// A run: host capability server is not waited for: it serves the
// deployment rather than a run, so it never ends by itself and would
// hold every upgrade for the whole cap. It is cancelled once the runs
// are done, and the daemon's capability registry starts it again on the
// new hostd.
func (s *Server) waitDrained(ctx context.Context) {
	limit := s.drainCap(0)
	s.logf("hostd: drain_cap %s", limit)
	since := time.Now()
	capped := false
	refused := false
	for {
		s.mu.Lock()
		n := s.cliStarts
		if s.handover && !s.draining {
			n += s.listeners
		}
		var stuck, servers []*proc
		var kept int
		for _, p := range s.procs {
			switch {
			case p.info.Exited:
			case p.info.Kind == KindCapability:
				servers = append(servers, p)
			case s.handover && !s.draining && p.detached && !p.stdinOpen:
				kept++
			default:
				n++
				if capped {
					stuck = append(stuck, p)
				}
			}
		}
		if n == 0 {
			// Taken under the same lock as the count, so no start slips
			// in between this one and hostd exiting or re-execing.
			s.closing = true
			if s.handover && !s.draining {
				s.handedOver = true
				close(s.handoverCh)
			}
		}
		refusing := s.draining
		handedOver := s.handedOver
		s.mu.Unlock()
		if n == 0 {
			for _, p := range servers {
				s.logf("hostd: drained: ending capability server %q (pid %d); the daemon starts it again on the new build",
					p.info.Name, p.info.PID)
				p.cancel(killCanceled)
			}
			if handedOver {
				s.waitExitsDelivered(ctx, true)
				s.logf("hostd: handing %d live detached process(es) over to the new build", kept)
			} else {
				s.waitExitsDelivered(ctx, false)
				s.logf("hostd: drained; exiting so the new build takes over")
			}
			return
		}
		if refusing && !refused {
			// The cap counts from when starts were first refused: a
			// cutover's time spent taking them does not shorten it.
			refused = true
			since = time.Now()
		}
		for _, p := range stuck {
			s.logf("hostd: drain cap %s reached: cancelling %s %q (pid %d, run %s) so the upgrade can go ahead",
				limit, p.info.Kind, p.info.Name, p.info.PID, p.info.Run)
			p.cancel(killDraining)
		}
		wait := time.Second
		if !capped {
			if l := s.drainCap(limit); l != limit {
				s.logf("hostd: drain_cap is now %s (was %s): the allowlist changed during the drain", l, limit)
				limit = l
			}
			left := limit - time.Since(since)
			if left <= 0 {
				if !refusing {
					s.logf("hostd: cutover: %d run(s) have kept running for all of %s; refusing new starts so the upgrade can go ahead", n, limit)
					s.Drain()
				} else {
					capped = true
				}
				continue
			}
			wait = min(wait, left)
		}
		select {
		case <-ctx.Done():
			return
		case <-s.changed:
		case <-time.After(wait):
		}
	}
}

// DrainExitGrace bounds how long attach waits, after writing a detached
// process's Exit frame, for the daemon to hang up having read it; one
// that has not by then leaves the process and its spool kept.
const DrainExitGrace = 5 * time.Second

func (s *Server) exitGrace() time.Duration {
	if s.drainExitGrace > 0 {
		return s.drainExitGrace
	}
	return DrainExitGrace
}

// waitExitsDelivered waits until no ended process still has a stream attached
// whose daemon has not read its Exit (attach releases a detached process once
// the daemon cleanly hangs up after the Exit, lets go keeping the spool when
// its session or stream breaks, and keeps the spool when DrainExitGrace passes
// first; run waits the same way for a protocol-1 start, which has no spool).
// On a handover (nonDetachedOnly) only protocol-1 exits are waited for:
// detached spools survive on disk for the replacement hostd. It waits twice
// DrainExitGrace at most, so attach's own grace -- which starts after any
// final output is pumped -- ordinarily runs out first. Without it
// the last run to end races hostd's exit:
// Serve closes the remote session and the sockets the moment nothing
// runs, so the daemon streaming that run sees its stream drop rather than
// the Exit, and waits for a host to come back before the run can finish
// from the spool it kept (or loses a protocol-1 start outright).
func (s *Server) waitExitsDelivered(ctx context.Context, nonDetachedOnly bool) {
	wait := 2 * s.exitGrace()
	timeout := time.NewTimer(wait)
	defer timeout.Stop()
	for {
		s.mu.Lock()
		pending := 0
		for _, p := range s.procs {
			if nonDetachedOnly && p.detached {
				continue
			}
			if p.info.Exited && p.info.Attached {
				pending++
			}
		}
		s.mu.Unlock()
		if pending == 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-timeout.C:
			s.logf("hostd: drained, but %d attached stream(s) have not delivered their process's exit within %s; exiting anyway (the final spool is kept)", pending, wait)
			return
		case <-s.changed:
		case <-time.After(s.poll()):
		}
	}
}

// drainCap is the allowlist's drain_cap as it is now, or last when the
// allowlist cannot be read (DefaultDrainCap when nothing has been read).
func (s *Server) drainCap(last time.Duration) time.Duration {
	a, _, err := s.loadAllowlist()
	if err != nil {
		if last > 0 {
			return last
		}
		s.logf("hostd: reading drain_cap: %v; using %s until the allowlist reads again", err, DefaultDrainCap)
		return DefaultDrainCap
	}
	return a.drainCap()
}

func (s *Server) listen() (*net.UnixListener, error) {
	dir := filepath.Dir(s.Socket)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(s.Socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("grain hostd: %s exists and is not a socket", s.Socket)
		}
		_ = os.Remove(s.Socket)
	}
	// The socket is bound inside a fresh directory only its owner can
	// enter, narrowed to 0600 there and only then renamed into place, so
	// there is no window in which anyone else can reach it. Not by
	// setting the umask around the bind: the umask is the whole
	// process's, and a file or directory anything else in the process
	// created in that window (a test running beside an in-process hostd)
	// came out 0600 -- a directory nobody could create anything in.
	private, err := os.MkdirTemp(dir, ".h")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(private)
	staged := filepath.Join(private, "s")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: staged, Net: "unix"})
	if err != nil {
		return nil, err
	}
	// Close removes the socket by the name it now has, not the staged
	// one (unixListener's own unlink would aim at the staged name).
	ln.SetUnlinkOnClose(false)
	if err := os.Chmod(staged, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	if err := os.Rename(staged, s.Socket); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// peerUID is the uid of the process on the other end of conn, as the
// kernel reports it -- which holds across the container boundary.
func peerUID(conn *net.UnixConn) (uint32, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *syscall.Ucred
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		cred, cerr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if cerr != nil {
		return 0, cerr
	}
	return cred.Uid, nil
}

// handle serves one connection on the socket: only hostd's own uid may
// use it.
func (s *Server) handle(ctx context.Context, conn *net.UnixConn) {
	defer conn.Close()
	uid, err := peerUID(conn)
	if err != nil || uid != s.uid {
		s.logf("hostd: refused a connection from uid %d (err %v); only uid %d may connect", uid, err, s.uid)
		return
	}
	s.serveConn(ctx, conn)
}

// serveConn serves one connection's request, whichever way it arrived:
// on the socket (handle) or as a stream the controller hostd dialed out
// to opened over that session (serveRemote). The caller closes conn.
func (s *Server) serveConn(ctx context.Context, conn net.Conn) {
	typ, payload, err := readFrame(conn)
	if err != nil {
		return
	}
	if typ != frameRequest {
		s.logf("hostd: refused a connection whose first frame was %q, not a request", typ)
		return
	}
	var req Request
	if err := json.Unmarshal(payload, &req); err != nil {
		s.refuse(conn, fmt.Sprintf("malformed request: %v", err))
		return
	}
	a, _, err := s.loadAllowlist()
	if err != nil {
		s.refuse(conn, err.Error())
		return
	}
	switch req.Op {
	case OpList:
		_ = writeJSON(conn, frameOffers, a.Offers())
		return
	case OpHello:
		h := ThisHello(s.Build)
		h.Draining = s.Draining()
		h.Cutover = !h.Draining && s.CuttingOver()
		h.Capacity, h.Labels = a.Capacity, a.Labels
		describeCopies(ctx, &h, s.CurrentLink)
		h.Logins = a.ambientLogins()
		h.AgentsOnly = s.AgentsOnly
		if sessionMux(ctx) != nil {
			s.describeRemote(&h, a)
		}
		if req.Hello != nil {
			if err := Compatible(h, *req.Hello); err != nil {
				s.logf("hostd: a daemon said hello with %v", err)
			}
		}
		_ = writeJSON(conn, frameHello, h)
		return
	case OpProcesses:
		_ = writeJSON(conn, frameProcesses, s.Processes())
		return
	case OpAttach:
		p, gen := s.claimAttach(req.ID)
		if p == nil {
			s.refuse(conn, fmt.Sprintf("no detached process %q", req.ID))
			return
		}
		s.attach(ctx, conn, p, gen, req.Cursor, nil)
		return
	case OpCancel:
		s.release(req.ID, "cancelled by the daemon")
		_ = writeFrame(conn, frameOK, nil)
		return
	case OpDrain:
		// Answered before the drain loop starts: with nothing running, it
		// exits the process at once (and a handover re-execs it), and an
		// answer written after it raced that exit -- a daemon then read
		// EOF for a drain that had in fact happened (tests/agentdcompat).
		// But for a plain drain starts are refused before the answer, so a
		// daemon that has been told the drain began never sees a start it
		// makes next taken.
		// A cutover or handover is marked before the answer too, so a
		// Hello the daemon sends once it is told reports it, and its drain
		// loop -- which may exit at once -- starts after.
		switch {
		case req.Idle && req.Handover && s.canHandover():
			began := s.markHandover()
			_ = writeFrame(conn, frameOK, nil)
			if began {
				s.beginHandover()
			}
		case req.Idle:
			began := s.markCutover()
			_ = writeFrame(conn, frameOK, nil)
			if began {
				s.beginCutover()
			}
		default:
			began := s.refuseStarts()
			_ = writeFrame(conn, frameOK, nil)
			if began {
				s.mu.Lock()
				s.startDrainLocked()
				s.mu.Unlock()
			}
		}
		return
	case OpSocketOwner:
		_ = writeJSON(conn, frameSocketOwner, SocketOwner{PID: s.socketOwner(req.Inode)})
		return
	case OpStage:
		if sessionMux(ctx) == nil {
			s.refuse(conn, "stage is served only over a session hostd dialed out on (grain hostd -server)")
			return
		}
		_ = writeJSON(conn, frameStaged, s.stageWithAllowlist(req.Stage, a))
		return
	case OpListen:
		m := sessionMux(ctx)
		if m == nil {
			s.refuse(conn, "listen is served only over a session hostd dialed out on (grain hostd -server)")
			return
		}
		s.listenCarry(ctx, conn, m, req.ID)
		return
	case OpStart:
	default:
		s.refuse(conn, fmt.Sprintf("unknown op %q", req.Op))
		return
	}
	// A capability server starts while hostd drains: the runs the drain
	// waits on mint their credentials through one, and waitDrained ends
	// it with the rest once they are done. Nothing starts once hostd is
	// exiting.
	s.mu.Lock()
	refuse := s.closing || (s.draining && req.Kind != KindCapability)
	counted := !refuse && req.Kind != KindCapability
	if counted {
		s.cliStarts++
	}
	s.mu.Unlock()
	if refuse {
		s.logf("hostd: refused: draining for an upgrade")
		_ = writeJSON(conn, frameRefused, Refusal{Reason: "grain hostd is draining for an upgrade; " +
			"the hostd that replaces it takes new starts", Draining: true})
		return
	}
	var releaseOnce sync.Once
	releaseStart := func() {
		if !counted {
			return
		}
		releaseOnce.Do(func() {
			s.mu.Lock()
			s.cliStarts--
			s.mu.Unlock()
			s.signalChanged()
		})
	}
	defer releaseStart()
	if sessionMux(ctx) != nil {
		// A run over the session was staged under StageRoot, which the
		// daemon could not have named in this machine's allowlist.
		a = a.withCLIRoot(s.stageRoot())
	}
	spec, err := a.Check(&req, s.env())
	if err != nil {
		s.refuse(conn, err.Error())
		return
	}
	if req.Detach {
		s.startDetached(ctx, conn, &req, spec, releaseStart)
		return
	}
	s.run(ctx, conn, &req, spec, releaseStart)
}

func (s *Server) env() []string {
	if s.Env != nil {
		return s.Env
	}
	return os.Environ()
}

func (s *Server) refuse(conn net.Conn, reason string) {
	reason = strings.TrimPrefix(reason, refusedPrefix)
	s.logf("hostd: refused: %s", reason)
	_ = writeJSON(conn, frameRefused, Refusal{Reason: reason})
}

// stdinBuffer is how many stdin frames hostd holds for a process that is
// slower to read them than the daemon is to send them. Past it the process
// is taken to have stopped reading, and its stdin is closed.
const stdinBuffer = 1024

func (s *Server) run(ctx context.Context, conn net.Conn, req *Request, spec *Spec, onAdded func()) {
	deadline := time.Now().Add(spec.MaxDuration)
	if !req.Deadline.IsZero() && req.Deadline.Before(deadline) {
		deadline = req.Deadline
	}
	runCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	var sendMu sync.Mutex
	send := func(typ byte, p []byte) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return writeFrame(conn, typ, p)
	}
	sendJSON := func(typ byte, v any) {
		b, _ := json.Marshal(v)
		_ = send(typ, b)
	}

	cmd := exec.CommandContext(runCtx, spec.Path, spec.Args...)
	procgroup.Prepare(cmd)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	cmd.Stdout = chunkWriter{typ: frameStdout, send: send}
	cmd.Stderr = chunkWriter{typ: frameStderr, send: send}
	cmd.WaitDelay = WaitDelay
	stdin, err := cmd.StdinPipe()
	if err != nil {
		sendJSON(frameExit, Exit{Code: -1, Error: err.Error()})
		return
	}
	s.logf("hostd: starting %s %q as %s (deadline %s)", req.Kind, req.Name, spec.Path, deadline.Format(time.RFC3339))
	// Output the process writes before Started has gone out waits for it,
	// so Started is always the daemon's first frame.
	sendMu.Lock()
	if err := cmd.Start(); err != nil {
		sendMu.Unlock()
		s.logf("hostd: %s %q did not start: %v", req.Kind, req.Name, err)
		sendJSON(frameExit, Exit{Code: -1, Error: err.Error()})
		return
	}
	pid := cmd.Process.Pid
	sacrificeOnOOM(pid)
	s.record(pid)
	defer s.forget(pid)
	// A protocol-1 start is listed and counted by a drain, but streams
	// over this connection only, so nothing can attach to it.
	id := newID()
	entry := &proc{info: Process{ID: id, Kind: req.Kind, Name: req.Name, PID: pid, Started: time.Now().UTC(),
		Deadline: deadline, Run: req.Run, Task: req.Task}, done: make(chan struct{})}
	entry.cancel = func(kind killKind) {
		s.mu.Lock()
		if entry.killKind == killNone {
			entry.killKind = kind
		}
		s.mu.Unlock()
		cancel()
	}
	s.add(id, entry)
	onAdded()
	defer s.drop(id)
	defer close(entry.done)
	started, _ := json.Marshal(Started{PID: pid})
	_ = writeFrame(conn, frameStarted, started)
	sendMu.Unlock()

	// Stdin is written from its own goroutine, and the loop below hands it
	// frames without ever waiting on it, so a process that is not reading
	// cannot stop the loop from seeing a signal or a disconnect.
	stdinCh := make(chan []byte, stdinBuffer)
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

	// The daemon's side: stdin, signals, and its going away.
	done := make(chan struct{})
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		stdinOpen := true
		closeStdin := func() {
			if stdinOpen {
				stdinOpen = false
				close(stdinCh)
			}
		}
		defer closeStdin()
		for {
			typ, payload, err := readFrame(conn)
			if err != nil {
				select {
				case <-done:
					return
				default:
				}
				s.logf("hostd: the daemon went away from %s %q (pid %d); killing it unless it exits within %s", req.Kind, req.Name, pid, s.DisconnectGrace)
				closeStdin()
				select {
				case <-done:
				case <-time.After(s.DisconnectGrace):
					_ = syscall.Kill(-pid, syscall.SIGKILL)
				}
				return
			}
			switch typ {
			case frameStdin:
				if !stdinOpen {
					continue
				}
				// Never block here: this loop is what sees KILL and the
				// daemon going away. A process that has stopped reading
				// with a full buffer behind it gets its stdin closed, and
				// the rest of what the daemon sends is dropped.
				select {
				case stdinCh <- payload:
				default:
					s.logf("hostd: %s %q (pid %d) is not reading its stdin; closing it", req.Kind, req.Name, pid)
					closeStdin()
				}
			case frameStdinClose:
				closeStdin()
			case frameSignal:
				var m SignalMsg
				if json.Unmarshal(payload, &m) != nil {
					continue
				}
				sig, ok := signals[m.Signal]
				if !ok {
					s.logf("hostd: refused signal %q for pid %d", m.Signal, pid)
					continue
				}
				select {
				case <-done:
					continue
				default:
				}
				_ = syscall.Kill(-pid, sig)
			}
		}
	}()

	err = cmd.Wait()
	close(done)
	s.mu.Lock()
	killed := entry.killKind
	s.mu.Unlock()
	exit := s.reap(cmd, err, runCtx, req, killed)
	exitBytes, _ := json.Marshal(exit)
	if req.Kind != KindCapability {
		s.mu.Lock()
		entry.info.Exited = true
		entry.info.Exit = &exit
		entry.info.Attached = true
		s.mu.Unlock()
		s.signalChanged()
		defer func() {
			s.mu.Lock()
			entry.info.Attached = false
			s.mu.Unlock()
			s.signalChanged()
		}()
	}
	if err := send(frameExit, exitBytes); err == nil && req.Kind != KindCapability {
		grace := time.NewTimer(s.exitGrace())
		select {
		case <-gone:
		case <-ctx.Done():
		case <-grace.C:
		}
		grace.Stop()
	}
}

// reap turns cmd's Wait into an Exit, and kills whatever the process left
// in its group.
func (s *Server) reap(cmd *exec.Cmd, err error, runCtx context.Context, req *Request, killed killKind) Exit {
	pid := cmd.Process.Pid
	exit := Exit{Code: -1}
	if errors.Is(err, exec.ErrWaitDelay) {
		s.logf("hostd: pid %d exited, but something it started still held its output open after %s; stopped waiting for it", pid, WaitDelay)
		exit.WaitDelay = true
		err = nil
	}
	if ps := cmd.ProcessState; ps != nil {
		exit.Code = ps.ExitCode()
		if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			exit.Signal = signalName(ws.Signal())
		}
	}
	switch {
	case killed == killDraining:
		exit.Draining = true
	case killed == killDeadline || (killed == killNone && errors.Is(runCtx.Err(), context.DeadlineExceeded)):
		exit.Deadline = true
	}
	if err != nil && cmd.ProcessState == nil {
		exit.Error = err.Error()
	}
	exit.FinishedAt = time.Now().UTC()
	// Whatever the CLI left in its group goes with it.
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	s.logf("hostd: %s %q (pid %d) ended: code %d signal %q deadline %v draining %v", req.Kind, req.Name, pid, exit.Code, exit.Signal, exit.Deadline, exit.Draining)
	return exit
}

var signals = map[string]syscall.Signal{
	"TERM": syscall.SIGTERM,
	"INT":  syscall.SIGINT,
	"HUP":  syscall.SIGHUP,
	"KILL": syscall.SIGKILL,
}

func signalName(sig syscall.Signal) string {
	for n, s := range signals {
		if s == sig {
			return n
		}
	}
	return sig.String()
}

// record notes a started process group, with the process's start time so
// a reused pid is never mistaken for it.
func (s *Server) record(pid int) {
	if s.StateDir == "" {
		return
	}
	start, err := procStartTime(pid)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(s.StateDir, strconv.Itoa(pid)), []byte(start), 0o600)
}

func (s *Server) forget(pid int) {
	if s.StateDir == "" {
		return
	}
	_ = os.Remove(filepath.Join(s.StateDir, strconv.Itoa(pid)))
}

func (s *Server) hasLivePID(pid int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.procs {
		if !p.info.Exited && p.info.PID == pid {
			return true
		}
	}
	return false
}

// ReapRecorded kills every process group a previous hostd recorded and
// that is still the same process, except live detached runs loadSpools
// has already adopted across a handover.
func (s *Server) ReapRecorded() {
	entries, err := os.ReadDir(s.StateDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			// The detached processes' spools (loadSpools).
			continue
		}
		p := filepath.Join(s.StateDir, e.Name())
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 1 {
			_ = os.Remove(p)
			continue
		}
		if s.hasLivePID(pid) {
			// Adopted by loadSpools; keep its start-time record for any
			// later handover until watchDetached reaps it.
			continue
		}
		want, _ := os.ReadFile(p)
		if got, err := procStartTime(pid); err == nil && got == string(want) {
			s.logf("hostd: reaping process group %d left by a previous hostd", pid)
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
		_ = os.Remove(p)
	}
}

// procStatFields reads field 4 (ppid) and field 22 (starttime, in clock
// ticks since boot) of /proc/<pid>/stat.
func procStatFields(pid int) (ppid int, startTicks string, err error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, "", err
	}
	// The command name (field 2) may hold spaces; fields resume after its
	// closing parenthesis.
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 {
		return 0, "", errors.New("malformed stat")
	}
	f := strings.Fields(string(b[i+1:]))
	// f[0] is field 3 (state), f[1] is field 4 (ppid), so field 22 is f[19].
	if len(f) < 20 {
		return 0, "", errors.New("short stat")
	}
	ppid, err = strconv.Atoi(f[1])
	if err != nil {
		return 0, "", err
	}
	return ppid, f[19], nil
}

// procStartTime is field 22 of /proc/<pid>/stat: when the process started,
// in clock ticks since boot.
func procStartTime(pid int) (string, error) {
	_, start, err := procStatFields(pid)
	return start, err
}

// socketOwner finds the process holding socket inode open, answering with
// its pid only when that process belongs to the process tree of a still-running
// process this hostd started.
func (s *Server) socketOwner(inode uint64) int {
	if inode == 0 {
		return 0
	}
	s.mu.Lock()
	roots := map[int]bool{}
	for _, p := range s.procs {
		if !p.info.Exited && p.info.PID > 0 {
			roots[p.info.PID] = true
		}
	}
	s.mu.Unlock()
	if len(roots) == 0 {
		return 0
	}
	want := "socket:[" + strconv.FormatUint(inode, 10) + "]"
	procs, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	for _, p := range procs {
		pid, err := strconv.Atoi(p.Name())
		if err != nil || pid <= 0 {
			continue
		}
		if !holdsSocket(p.Name(), want) {
			continue
		}
		if inProcessTree(pid, roots) {
			return pid
		}
	}
	return 0
}

func holdsSocket(pidDir, want string) bool {
	dir := filepath.Join("/proc", pidDir, "fd")
	fds, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, fd := range fds {
		if link, err := os.Readlink(filepath.Join(dir, fd.Name())); err == nil && link == want {
			return true
		}
	}
	return false
}

func inProcessTree(pid int, roots map[int]bool) bool {
	for i := 0; i < 1024 && pid > 1; i++ {
		if roots[pid] {
			return true
		}
		parent, err := procParent(pid)
		if err != nil {
			return false
		}
		pid = parent
	}
	return roots[pid]
}

func procParent(pid int) (int, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "PPid:"); ok {
			return strconv.Atoi(strings.TrimSpace(v))
		}
	}
	return 0, fmt.Errorf("process %d has no PPid", pid)
}
