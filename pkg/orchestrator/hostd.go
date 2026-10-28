package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/hostd"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/runhost"
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
// running detached in grain hostd (docs/design/cli-run-location.md,
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
// RecoverOrphanedRuns, and the error says why.
func RecoverHostd(ctx context.Context, store *model.Store, client github.Client, hd HostdLister, build string,
	now time.Time, adopted func(runhost.Adoption, int64) bool) (runhost.Plan, error) {
	cursors, err := store.HostdCursors(ctx)
	if err != nil {
		return runhost.Plan{}, fmt.Errorf("orchestrator: reading hostd cursors: %w", err)
	}
	peer, err := hd.Hello(ctx)
	if err != nil {
		return runhost.Plan{}, fmt.Errorf("orchestrator: grain hostd did not answer hello, so the %d run(s) driven "+
			"through it are recovered as any other: %w", len(cursors), err)
	}
	var procs []hostd.Process
	if peer.SupportsAttach() {
		if procs, err = hd.Processes(ctx); err != nil {
			return runhost.Plan{}, fmt.Errorf("orchestrator: listing grain hostd's processes: %w", err)
		}
	}
	runs, err := store.LiveRuns(ctx)
	if err != nil {
		return runhost.Plan{}, fmt.Errorf("orchestrator: reading live runs: %w", err)
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
		byID[r.ID] = r
		claims = append(claims, runhost.Claim{Run: r.ID, Task: r.TaskID, LastSeen: c.UpdatedAt})
	}
	plan := runhost.DecideHostd(claims, procs, hostd.ThisHello(build), peer, now)

	var errs []error
	release := func(i runhost.Info, reason string) {
		id, _ := runhost.HostdID(i)
		log.Printf("orchestrator: releasing grain hostd process %s (task %s, run %s): %s", id, i.Labels.Task, i.Labels.Run, reason)
		if err := hd.Cancel(ctx, id); err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: cancelling grain hostd process %s: %w", id, err))
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
		finish(a.Claim, fmt.Sprintf("its host process %s was still running in grain hostd, but this daemon could not "+
			"adopt the run (see its journal for why)", a.Host.Name))
	}
	return plan, errors.Join(errs...)
}
