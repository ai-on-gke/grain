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

package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/runhost"
)

// HostdLister is what RecoverHostd asks grain hostd: hostd.Client, or a
// fake listing in a test.
type HostdLister interface {
	Hello(ctx context.Context) (hostd.Hello, error)
	Processes(ctx context.Context) ([]hostd.Process, error)
	Cancel(ctx context.Context, id string) error
}

var _ HostdLister = hostd.Client{}

// RecoverHostd is RecoverRunHosts for host CLIs a previous daemon left
// running detached in grain hostd (design/design/cli-run-location.design.md,
// "Upgrades and host runs"), and runs beside it, before
// RecoverOrphanedRuns: it says hello, lists hostd's processes, and
// decides the live rows driven through hostd -- a row with a hostd cursor
// (runhost.Hostd persists cursor 0 once the process has started), or one
// a listed process names -- with runhost.DecideHostd.
//
// A matched process is offered to adopted, as RecoverRunHosts offers a run
// host, with the row's persisted hostd cursor; one it declines is
// cancelled and its row finished as interrupted. A process no live row
// claims is cancelled, and a row whose process is gone, past its deadline
// or on a hostd this build cannot talk to is finished as interrupted
// with that reason.
//
// A hostd that does not answer decides nothing: its rows are left to
// RecoverOrphanedRuns, and the error, a *HostdAwayError, says why and
// names them, so their processes -- which hostd keeps running until
// their own deadlines -- can be cancelled once it is back
// (CancelWhenBack).
func RecoverHostd(ctx context.Context, store *model.Store, client github.Client, hd HostdLister, build string,
	now time.Time, adopted func(runhost.Adoption, int64) bool) (runhost.Plan, error) {
	return RecoverHostdRuns(ctx, store, client, hd, build, now, nil, adopted)
}

// RecoverHostdRuns is RecoverHostd for one of several grain hostds: only
// the live rows owns says ran on hd are decided against hd's processes,
// so a row whose CLI runs on another hostd is neither finished as gone
// here nor claims a process here. A process hd lists for a row it does
// not own is released as unclaimed. A nil owns owns every row.
func RecoverHostdRuns(ctx context.Context, store *model.Store, client github.Client, hd HostdLister, build string,
	now time.Time, owns func(runID string) bool, adopted func(runhost.Adoption, int64) bool) (runhost.Plan, error) {
	cursors, err := store.HostdCursors(ctx)
	if err != nil {
		return runhost.Plan{}, fmt.Errorf("orchestrator: reading host cursors: %w", err)
	}
	runs, err := store.LiveRuns(ctx)
	if err != nil {
		return runhost.Plan{}, fmt.Errorf("orchestrator: reading live runs: %w", err)
	}
	// away is the error for a hostd that did not answer: the live runs
	// driven through it this pass decides nothing about, for
	// CancelWhenBack to cancel once it does.
	away := func(err error) error {
		var left []string
		for _, r := range runs {
			if _, ok := cursors[r.ID]; ok && (owns == nil || owns(r.ID)) {
				left = append(left, r.ID)
			}
		}
		return &HostdAwayError{Runs: left, Err: err}
	}
	peer, err := hd.Hello(ctx)
	if err != nil {
		return runhost.Plan{}, away(fmt.Errorf("orchestrator: the host agent did not answer hello: %w", err))
	}
	var procs []hostd.Process
	if peer.SupportsAttach() {
		if procs, err = hd.Processes(ctx); err != nil {
			return runhost.Plan{}, away(fmt.Errorf("orchestrator: listing the host agent's processes: %w", err))
		}
	}
	named := map[string]bool{}
	for _, p := range procs {
		if p.Detached {
			named[p.Run] = true
		}
	}
	var claims []runhost.Claim
	byID := map[string]model.Run{}
	for _, r := range runs {
		c, hasCursor := cursors[r.ID]
		if !hasCursor && !named[r.ID] {
			continue
		}
		if owns != nil && !owns(r.ID) {
			continue
		}
		byID[r.ID] = r
		claims = append(claims, runhost.Claim{Run: r.ID, Task: r.TaskID, Sandbox: r.Sandbox, LastSeen: c.UpdatedAt})
	}
	plan := runhost.DecideHostd(claims, procs, hostd.ThisHello(build), peer, now)

	var errs []error
	release := func(i runhost.Info, reason string) {
		id, _ := runhost.HostdID(i)
		log.Printf("orchestrator: releasing grain hostd process %s (task %s, run %s): %s", id, i.Labels.Task, i.Labels.Run, reason)
		if err := hd.Cancel(ctx, id); err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: cancelling the host agent process %s: %w", id, err))
		}
	}
	finish := func(c runhost.Claim, reason string) {
		detail := "the daemon driving this run was restarted, and at the next startup " + reason +
			"; finished as interrupted and re-queued without a strike"
		if err := recoverRun(ctx, store, client, byID[c.Run], now, detail); err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: finishing run %s: %w", c.Run, err))
		}
	}
	for _, r := range plan.Release {
		release(r.Host, r.Reason)
	}
	for _, f := range plan.Finish {
		finish(f.Claim, f.Reason)
	}
	for _, a := range plan.Adopt {
		cursor := cursors[a.Claim.Run].Offset
		if adopted != nil && adopted(a, cursor) {
			log.Printf("orchestrator: adopted grain hostd process %s for run %s at cursor %d after a %s gap",
				a.Host.Name, a.Claim.Run, cursor, a.Gap.Round(time.Second))
			continue
		}
		release(a.Host, "matched its live run, but this daemon could not adopt it")
		finish(a.Claim, fmt.Sprintf("its host process %s was still running in the host agent, but this daemon could not "+
			"adopt the run (see its journal for why)", a.Host.Name))
	}
	return plan, errors.Join(errs...)
}

// HostdAwayError is RecoverHostdRuns's error for a hostd that did not
// answer, hello or the listing after it: Runs are the live runs driven
// through it that the pass decided nothing about. They are left to
// RecoverOrphanedRuns, which finishes them as interrupted and re-queues
// them, while their processes may still be running in hostd -- each
// holding a host CLI slot, beside the run's next attempt -- until
// CancelWhenBack cancels them.
type HostdAwayError struct {
	Runs []string
	Err  error
}

func (e *HostdAwayError) Error() string {
	return fmt.Sprintf("%v; the %d run(s) driven through it are recovered as any other, and their processes "+
		"cancelled once it answers again", e.Err, len(e.Runs))
}

func (e *HostdAwayError) Unwrap() error { return e.Err }

// hostdBackPoll bounds how long CancelWhenBack waits between two tries
// at a hostd that has not answered yet.
var hostdBackPoll = 5 * time.Second

// CancelWhenBack is the far side of a HostdAwayError: once hd answers
// hello again and lists its processes, it cancels each detached process
// of one of runs that is no longer live -- the adoption pass gave up on
// it, and RecoverOrphanedRuns finished it -- so none runs on to its own
// deadline, and an exited one's spool is dropped. reconnected, when set, blocks until hd is reachable again
// or ctx is done (hostd.Remote.WaitHostd for a hostd that dials in);
// nil polls. It returns the processes it cancelled, and gives up when
// ctx is done.
func CancelWhenBack(ctx context.Context, store *model.Store, hd HostdLister, runs []string,
	reconnected func(ctx context.Context) error) ([]string, error) {
	if len(runs) == 0 {
		return nil, nil
	}
	abandoned := map[string]bool{}
	for _, r := range runs {
		abandoned[r] = true
	}
	var procs []hostd.Process
	wait := 100 * time.Millisecond
	for {
		if reconnected != nil {
			_ = reconnected(ctx)
		}
		peer, err := hd.Hello(ctx)
		if err == nil && peer.SupportsAttach() {
			procs, err = hd.Processes(ctx)
		}
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("orchestrator: the host agent did not answer again, so the processes of %d "+
				"abandoned run(s) may still be running there: %w", len(runs), ctx.Err())
		case <-time.After(wait):
		}
		wait = min(wait*2, hostdBackPoll)
	}
	live, err := store.LiveRuns(ctx)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: reading live runs: %w", err)
	}
	for _, r := range live {
		delete(abandoned, r.ID)
	}
	var cancelled []string
	var errs []error
	for _, p := range procs {
		if !p.Detached || !abandoned[p.Run] {
			continue
		}
		log.Printf("orchestrator: cancelling grain hostd process %s (task %s, run %s): the host agent did not answer "+
			"at startup, so its run was finished as interrupted", p.ID, p.Task, p.Run)
		if err := hd.Cancel(ctx, p.ID); err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: cancelling the host agent process %s: %w", p.ID, err))
			continue
		}
		cancelled = append(cancelled, p.ID)
	}
	return cancelled, errors.Join(errs...)
}
