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

package runhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/hostd"
)

// Hostd drives each run's host CLI through grain hostd the way Host
// drives one in a run host (design/design/cli-run-location.design.md, "Upgrades
// and host runs"): a Host.Run call naming a run (RunFrom) starts the CLI
// detached, with the run's id, task and deadline (its budget plus
// Allowance), so it outlives a daemon upgrade; its stdout cursor is
// persisted through OnCursor as it is read; a daemon that stops (Detach)
// or flushes for an upgrade (Flush) leaves it running for the next
// daemon, which re-attaches from that cursor (Attach). Any other Host.Run
// call -- a probe, a version check -- is a plain protocol-1 start, as is
// every call on a hostd that cannot detach.
//
// One Hostd serves every CLI entry (Host), so one Flush lets go of them
// all.
type Hostd struct {
	// Client reaches hostd. Its Detach and OnStarted are Hostd's own.
	Client hostd.Client
	// Allowance is added to a run's budget for the deadline hostd holds
	// the process to; DefaultAllowance when zero.
	Allowance time.Duration
	// OnCursor is told where a run's stdout cursor stands, as Host's is:
	// 0 once the process has started (which is also what marks the run as
	// one driven through hostd), then as it advances -- at most every
	// CursorEvery -- and once more when this daemon stops reading.
	OnCursor func(ctx context.Context, run string, cursor int64)
	// CursorEvery spaces OnCursor's calls; DefaultCursorEvery when zero.
	CursorEvery time.Duration
	// Detach reports whether a cancelled run is the daemon stopping, as
	// Host.Detach does. Nil means never.
	Detach func(ctx context.Context) bool
	// Logf is where Hostd logs; log.Printf when nil.
	Logf func(format string, args ...any)
	// ReattachTimeout is how long a detached run's CLI is waited for when
	// the connection to hostd drops mid-stream -- hostd restarting, or a
	// hostd that dials in losing its session -- before the run fails as
	// agent.ErrHostdLost. The process runs on in hostd meanwhile, and
	// once hostd answers again it is re-attached from the cursor the
	// stream reached. Zero never waits: the drop fails the run at once.
	ReattachTimeout time.Duration
	// Reconnected, when set, blocks until hostd is reachable again or
	// ctx is done (hostd.Remote.WaitHostd for a hostd that dials in), so
	// a re-attach is tried the moment it is back; nil polls.
	Reconnected func(ctx context.Context) error
	// Name is the hostd's name in what Hostd logs and fails with; "" says
	// "grain hostd".
	Name string

	tails tails
}

// hostdName is d's hostd as a sentence names it.
func (d *Hostd) hostdName() string {
	if d.Name == "" {
		return "the host agent"
	}
	return "host " + d.Name
}

// Host is the agent.Host a run's CLI named entry on hostd's allowlist
// runs through, staging as local does.
func (d *Hostd) Host(local agent.LocalHost, entry string) agent.HostdHost {
	return agent.HostdHost{LocalHost: local, Client: d.Client, Entry: entry, Start: d.Start}
}

func (d *Hostd) logf(format string, args ...any) {
	if d.Logf != nil {
		d.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// handshakeTimeout bounds the hello before a detached start.
const handshakeTimeout = 5 * time.Second

// Handshake is Hello and the verdict on it: an error naming the mismatch
// for a hostd this build cannot talk to (hostd.Compatible).
func (d *Hostd) Handshake(ctx context.Context) (hostd.Hello, error) {
	hctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	peer, err := d.Client.Hello(hctx)
	if err != nil {
		return peer, err
	}
	if err := hostd.Compatible(hostd.ThisHello(d.Client.Build), peer); err != nil {
		return peer, fmt.Errorf("the host agent cannot run this daemon's host CLIs: %w", err)
	}
	return peer, nil
}

// Start is agent.HostdHost.Start: a detached start for a run, after the
// handshake. An incompatible hostd starts nothing and says why; a
// draining one is refused as hostd itself would refuse it
// (hostd.IsDraining), so the run is re-queued rather than failed.
func (d *Hostd) Start(ctx context.Context, req hostd.Request, stdin io.Reader, stdout, stderr io.Writer) (hostd.Exit, error) {
	r, ok := RunFrom(ctx)
	if !ok || r.Run == "" || (len(req.Args) == 1 && req.Args[0] == "--version") {
		return d.Client.Run(ctx, req, stdin, stdout, stderr)
	}
	peer, err := d.Handshake(ctx)
	if err != nil {
		return hostd.Exit{Code: -1}, err
	}
	if peer.Draining {
		return hostd.Exit{Code: -1}, &hostd.RefusedError{Reason: "the host agent is draining for its own upgrade", Draining: true}
	}
	if !peer.SupportsAttach() {
		d.logf("runhost: the host agent speaks protocol %d, which cannot detach; run %s's CLI ends if this daemon stops", peer.Protocol, r.Run)
		return d.Client.Run(ctx, req, stdin, stdout, stderr)
	}
	allowance := d.Allowance
	if allowance == 0 {
		allowance = DefaultAllowance
	}
	req.Detach, req.Run, req.Task = true, r.Run, r.Task
	if !r.Deadline.IsZero() {
		req.Deadline = r.Deadline.Add(allowance)
	}
	started := make(chan struct{})
	proc := &procRef{}
	return d.stream(ctx, r.Run, proc, 0, stdout, nil, stderr, started, func(sctx context.Context, c hostd.Client, out, errOut io.Writer) (hostd.Exit, error) {
		c.OnStarted = func(st hostd.Started) {
			defer close(started)
			proc.set(st.ID)
			d.logf("runhost: run %s's CLI %q started detached through the host agent as %s (pid %d)", r.Run, req.Name, st.ID, st.PID)
			if d.OnCursor != nil {
				d.OnCursor(context.WithoutCancel(ctx), r.Run, 0)
			}
		}
		return c.Run(sctx, req, stdin, out, errOut)
	})
}

// procRef is the hostd process a stream follows, once hostd has named it.
type procRef struct {
	mu sync.Mutex
	id string
}

func (p *procRef) set(id string) {
	p.mu.Lock()
	p.id = id
	p.mu.Unlock()
}

func (p *procRef) get() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.id
}

// countingWriter counts what passes through it to w (which may be nil).
type countingWriter struct {
	w  io.Writer
	mu sync.Mutex
	n  int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.n += int64(len(p))
	c.mu.Unlock()
	if c.w != nil {
		_, _ = c.w.Write(p)
	}
	return len(p), nil
}

func (c *countingWriter) count() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// Attach re-attaches to the hostd process info names (HostdInfos) for an
// adopted run, as Host.Attach tails a run host: what the spool holds past
// cursor is written to tee, and the CLI's whole stdout is returned. The
// spool is read from its start, so the whole stdout is there to parse,
// and only what lies past cursor reaches tee: nothing twice, nothing
// skipped.
func (d *Hostd) Attach(ctx context.Context, info Info, cursor int64, tee io.Writer) (string, error) {
	id, ok := HostdID(info)
	if !ok {
		return "", fmt.Errorf("runhost: %s is not a host process", info.Name)
	}
	var whole, stderr bytes.Buffer
	started := make(chan struct{})
	close(started)
	exit, err := d.stream(ctx, info.Labels.Run, &procRef{id: id}, cursor, &whole, tee, &stderr, started, func(sctx context.Context, c hostd.Client, out, errOut io.Writer) (hostd.Exit, error) {
		e, _, err := c.Attach(sctx, id, hostd.Cursor{}, out, errOut)
		return e, err
	})
	if errors.Is(err, ErrDetached) {
		return "", err
	}
	if err != nil {
		return whole.String(), err
	}
	if exit.Stopped() {
		return whole.String(), fmt.Errorf("%w: %s", agent.ErrHostdStopped, exit.String())
	}
	if exit.Draining {
		return whole.String(), fmt.Errorf("%w: %s", agent.ErrHostdDraining, exit.String())
	}
	if exit.Code != 0 || exit.Error != "" {
		tail := stderr.Bytes()
		if len(tail) > 16<<10 {
			tail = tail[len(tail)-16<<10:]
		}
		return whole.String(), fmt.Errorf("%s (stderr: %s)", exit.String(), bytes.TrimSpace(tail))
	}
	return whole.String(), nil
}

// Release cancels the hostd process info names: its group is killed if
// it still runs, and its spool dropped.
func (d *Hostd) Release(ctx context.Context, info Info, reason string) {
	id, ok := HostdID(info)
	if !ok {
		return
	}
	d.logf("runhost: releasing the host agent process %s (task %s, run %s): %s", id, info.Labels.Task, info.Labels.Run, reason)
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := d.Client.Cancel(cctx, id); err != nil {
		d.logf("runhost: cancelling the host agent process %s: %v", id, err)
	}
}

// stream runs call -- a start or an attach -- with the run tracked for
// Flush and its stdout cursor persisted. All of stdout goes to whole;
// what lies past skip also goes to tee. The process outlives ctx being
// cancelled when Detach says the daemon is stopping, or when Flush lets
// go of it, and stream then returns ErrDetached. A Flush is acted on once
// started is closed: a start already under way when it comes is let go
// of as soon as hostd has it, for the next daemon to adopt.
//
// A connection that drops before the process's exit arrives
// (hostd.ErrNoExit) leaves the process running in hostd; with a
// ReattachTimeout, stream waits for hostd to answer again and re-attaches
// to proc from where stdout and stderr stood (reattach).
func (d *Hostd) stream(ctx context.Context, run string, proc *procRef, skip int64, whole, tee, stderr io.Writer, started <-chan struct{},
	call func(context.Context, hostd.Client, io.Writer, io.Writer) (hostd.Exit, error)) (hostd.Exit, error) {
	flush, untrack := d.tails.track(run)
	defer untrack()
	// Not ctx's deadline: hostd holds the process to the request's own
	// (the budget plus the allowance), and an adopted run's wait credit
	// moves its clock past a deadline set at the start.
	sctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	var flushed atomic.Bool
	go func() {
		select {
		case <-started:
		case <-sctx.Done():
			return
		}
		select {
		case <-flush:
			flushed.Store(true)
			cancel()
		case <-sctx.Done():
		}
	}()
	c := d.Client
	c.Detach = func(context.Context) bool {
		return flushed.Load() || (d.Detach != nil && d.Detach(ctx))
	}
	every := d.CursorEvery
	if every == 0 {
		every = DefaultCursorEvery
	}
	w := &cursorWriter{whole: whole, tee: tee, skip: skip, every: every, last: time.Now(),
		persist: func(n int64) {
			if d.OnCursor != nil {
				d.OnCursor(context.WithoutCancel(ctx), run, n)
			}
		}}
	ew := &countingWriter{w: stderr}
	exit, err := call(sctx, c, w, ew)
	// An attach that never reached hostd (an adopted run's, whose process
	// is named from the start) is a drop as much as one cut off midway:
	// hostd restarting onto a new build under the daemon that adopts the
	// run is the usual reason, and the process is still there.
	if (errors.Is(err, hostd.ErrNoExit) || errors.Is(err, hostd.ErrUnreachable)) && d.ReattachTimeout > 0 && proc.get() != "" {
		exit, err = d.reattach(sctx, c, run, proc.get(), w, ew)
	}
	w.flush()
	if errors.Is(err, hostd.ErrDetached) {
		why := "daemon stopping"
		if flushed.Load() {
			why = "upgrade flushing"
		}
		d.logf("runhost: %s; leaving run %s's CLI running in the host agent at cursor %d", why, run, w.cursor())
		return exit, ErrDetached
	}
	if err != nil && ctx.Err() != nil && errors.Is(err, context.Canceled) {
		err = context.Cause(ctx)
	}
	return exit, err
}

// reattachBackoff bounds how long reattach waits between two tries at a
// hostd that has not answered yet.
var reattachBackoff = 5 * time.Second

// reattach is stream's answer to the connection dropping under process
// id: within ReattachTimeout of the drop, the same hostd is attached to
// again from where stdout and stderr stood, so nothing is written twice
// and nothing skipped; a drop after that starts the clock again. Past
// the timeout the run fails as agent.ErrHostdLost, and the process is
// cancelled should hostd come back after all. ctx done while waiting is
// the daemon stopping (c.Detach, hostd.ErrDetached), or the run's own
// cancellation, which cancels the process once hostd is back.
func (d *Hostd) reattach(ctx context.Context, c hostd.Client, run, id string, out *cursorWriter, errOut *countingWriter) (hostd.Exit, error) {
	lost := func() (hostd.Exit, error) {
		d.cancelWhenBack(id)
		return hostd.Exit{Code: -1}, fmt.Errorf("%w: %s dropped run %s's CLI (%s) at cursor %d and did not reconnect within %s",
			agent.ErrHostdLost, d.hostdName(), run, id, out.cursor(), d.ReattachTimeout)
	}
	stopped := func() (hostd.Exit, error) {
		if c.Detach != nil && c.Detach(ctx) {
			return hostd.Exit{Code: -1}, hostd.ErrDetached
		}
		d.cancelWhenBack(id)
		return hostd.Exit{Code: -1}, ctx.Err()
	}
	give := time.Now().Add(d.ReattachTimeout)
	d.logf("runhost: lost %s while streaming run %s's CLI (%s) at cursor %d; it keeps running, and is re-attached if the host agent is back within %s",
		d.hostdName(), run, id, out.cursor(), d.ReattachTimeout)
	wait := 100 * time.Millisecond
	for {
		if d.Reconnected != nil {
			wctx, cancel := context.WithDeadline(ctx, give)
			_ = d.Reconnected(wctx)
			cancel()
		}
		if ctx.Err() != nil {
			return stopped()
		}
		if !time.Now().Before(give) {
			return lost()
		}
		actx, cancel := context.WithDeadline(ctx, give)
		exit, _, err := c.Attach(actx, id, hostd.Cursor{Stdout: out.written(), Stderr: errOut.count()}, out, errOut)
		cancel()
		switch {
		case err == nil, errors.Is(err, hostd.ErrDetached):
			return exit, err
		case ctx.Err() != nil:
			return stopped()
		case errors.Is(err, hostd.ErrNoExit):
			// Re-attached, then dropped again: a new drop, a new clock.
			give = time.Now().Add(d.ReattachTimeout)
			d.logf("runhost: lost %s again while streaming run %s's CLI (%s) at cursor %d; waiting up to %s once more",
				d.hostdName(), run, id, out.cursor(), d.ReattachTimeout)
			wait = 100 * time.Millisecond
			continue
		}
		var refused *hostd.RefusedError
		if errors.As(err, &refused) {
			// hostd answered, and has no such process to attach to: it
			// is gone (hostd restarted without it), and so is the run.
			return exit, fmt.Errorf("%w: %s is back, but run %s's CLI (%s) is not: %v", agent.ErrHostdLost, d.hostdName(), run, id, err)
		}
		if !time.Now().Before(give) {
			return lost()
		}
		select {
		case <-ctx.Done():
			return stopped()
		case <-time.After(min(wait, time.Until(give))):
		}
		wait = min(wait*2, reattachBackoff)
	}
}

// cancelWhenBack cancels hostd process id once hostd answers, trying for
// ReattachTimeout and at least a minute: a run that stopped waiting for
// it must not leave its CLI running there until the process's own
// deadline.
func (d *Hostd) cancelWhenBack(id string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), max(d.ReattachTimeout, time.Minute))
		defer cancel()
		wait := 100 * time.Millisecond
		for {
			if d.Reconnected != nil {
				_ = d.Reconnected(ctx)
			}
			err := d.Client.Cancel(ctx, id)
			if err == nil {
				return
			}
			var refused *hostd.RefusedError
			if errors.As(err, &refused) || ctx.Err() != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			wait = min(wait*2, reattachBackoff)
		}
	}()
}

// cursorWriter counts the stdout bytes a hostd stream delivers from the
// spool's start and persists where it stands. Bytes before skip were
// written to tee by an earlier daemon; persisting never goes back past
// them.
type cursorWriter struct {
	whole, tee io.Writer
	skip       int64
	every      time.Duration
	persist    func(int64)

	mu        sync.Mutex
	n         int64
	persisted int64
	last      time.Time
}

func (w *cursorWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.whole != nil {
		_, _ = w.whole.Write(p)
	}
	start := w.n
	w.n += int64(len(p))
	if w.tee != nil && w.n > w.skip {
		from := max(w.skip-start, 0)
		_, _ = w.tee.Write(p[from:])
	}
	if c := max(w.n, w.skip); c != w.persisted && time.Since(w.last) >= w.every {
		w.persist(c)
		w.persisted, w.last = c, time.Now()
	}
	return len(p), nil
}

// flush persists the cursor where it stands, moved or not: the time it is
// written at is when this daemon stopped reading (Decide's gap).
func (w *cursorWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	c := max(w.n, w.skip)
	w.persist(c)
	w.persisted, w.last = c, time.Now()
}

func (w *cursorWriter) cursor() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return max(w.n, w.skip)
}

// written is how much of the stream has reached whole: where a re-attach
// resumes. Not cursor, which an adopted run's attach starts at: what it
// had not read back yet below that is the stdout the framework parses.
func (w *cursorWriter) written() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.n
}

// Flush lets go of every run this Hostd is streaming, each with its
// cursor persisted, as Host.Flush does for run hosts: their processes
// run on in hostd for the next daemon. Until Resume a run that starts
// streaming lets go at once.
func (d *Hostd) Flush(ctx context.Context) ([]string, error) {
	return d.tails.flushAll(ctx)
}

// Resume undoes Flush, as Host.Resume does.
func (d *Hostd) Resume() { d.tails.resume() }
