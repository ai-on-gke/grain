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

	tails tails
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
		return peer, fmt.Errorf("grain hostd cannot run this daemon's host CLIs: %w", err)
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
		return hostd.Exit{Code: -1}, &hostd.RefusedError{Reason: "grain hostd is draining for its own upgrade", Draining: true}
	}
	if !peer.SupportsAttach() {
		d.logf("runhost: grain hostd speaks protocol %d, which cannot detach; run %s's CLI ends if this daemon stops", peer.Protocol, r.Run)
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
	return d.stream(ctx, r.Run, 0, stdout, nil, started, func(sctx context.Context, c hostd.Client, out io.Writer) (hostd.Exit, error) {
		c.OnStarted = func(st hostd.Started) {
			defer close(started)
			d.logf("runhost: run %s's CLI %q started detached through grain hostd as %s (pid %d)", r.Run, req.Name, st.ID, st.PID)
			if d.OnCursor != nil {
				d.OnCursor(context.WithoutCancel(ctx), r.Run, 0)
			}
		}
		return c.Run(sctx, req, stdin, out, stderr)
	})
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
		return "", fmt.Errorf("runhost: %s is not a grain hostd process", info.Name)
	}
	var whole, stderr bytes.Buffer
	started := make(chan struct{})
	close(started)
	exit, err := d.stream(ctx, info.Labels.Run, cursor, &whole, tee, started, func(sctx context.Context, c hostd.Client, out io.Writer) (hostd.Exit, error) {
		e, _, err := c.Attach(sctx, id, hostd.Cursor{}, out, &stderr)
		return e, err
	})
	if errors.Is(err, ErrDetached) {
		return "", err
	}
	if err != nil {
		return whole.String(), err
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
	d.logf("runhost: releasing grain hostd process %s (task %s, run %s): %s", id, info.Labels.Task, info.Labels.Run, reason)
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := d.Client.Cancel(cctx, id); err != nil {
		d.logf("runhost: cancelling grain hostd process %s: %v", id, err)
	}
}

// stream runs call -- a start or an attach -- with the run tracked for
// Flush and its stdout cursor persisted. All of stdout goes to whole;
// what lies past skip also goes to tee. The process outlives ctx being
// cancelled when Detach says the daemon is stopping, or when Flush lets
// go of it, and stream then returns ErrDetached. A Flush is acted on once
// started is closed: a start already under way when it comes is let go
// of as soon as hostd has it, for the next daemon to adopt.
func (d *Hostd) stream(ctx context.Context, run string, skip int64, whole, tee io.Writer, started <-chan struct{},
	call func(context.Context, hostd.Client, io.Writer) (hostd.Exit, error)) (hostd.Exit, error) {
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
	exit, err := call(sctx, c, w)
	w.flush()
	if errors.Is(err, hostd.ErrDetached) {
		why := "daemon stopping"
		if flushed.Load() {
			why = "upgrade flushing"
		}
		d.logf("runhost: %s; leaving run %s's CLI running in grain hostd at cursor %d", why, run, w.cursor())
		return exit, ErrDetached
	}
	if err != nil && ctx.Err() != nil && errors.Is(err, context.Canceled) {
		err = context.Cause(ctx)
	}
	return exit, err
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

// Flush lets go of every run this Hostd is streaming, each with its
// cursor persisted, as Host.Flush does for run hosts: their processes
// run on in hostd for the next daemon. Until Resume a run that starts
// streaming lets go at once.
func (d *Hostd) Flush(ctx context.Context) ([]string, error) {
	return d.tails.flushAll(ctx)
}

// Resume undoes Flush, as Host.Resume does.
func (d *Hostd) Resume() { d.tails.resume() }
