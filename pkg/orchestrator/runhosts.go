package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/runhost"
)

// RecoverRunHosts is the run-host half of startup recovery
// (docs/non-disruptive-upgrade.md, "Run hosts never touch the store"),
// and runs before RecoverOrphanedRuns: it lists this deployment's run
// hosts, matches them to live task_run rows by their run label
// (runhost.Decide), destroys every run host no live row claims, and
// finishes each live row whose run host is gone, refused or past its
// deadline as model.InterruptedOutcome with the reason the listing or the
// spool gave.
//
// Only rows that were driven in a run host take part -- a row with a
// spool cursor (runhost.Host writes cursor 0 as it creates one) or one a
// listed run host names. Any other live row is RecoverOrphanedRuns', as
// before.
//
// A matched run host is offered to adopted, which the daemon sets to
// claim the run's sandbox back (ReadoptSandbox) and to queue the run for
// ResumeAdoptedRun, which carries it to its end in this daemon. A run it
// takes is left running; one it declines, or every match when adopted is
// nil, has its run host destroyed and its row finished as interrupted with
// a reason that says so, and the conversation is still in the session
// store for the next dispatch.
//
// Last, it sweeps host's spool and staging roots (sweepRunHostDirs): a
// run host that is gone takes no spool with it, and a spool whose run
// host was never started still holds the run's env file.
func RecoverRunHosts(ctx context.Context, store *model.Store, client github.Client, host *runhost.Host,
	now time.Time, adopted func(runhost.Adoption, int64) bool) (runhost.Plan, error) {
	driver := host.Driver
	hosts, err := driver.List(ctx, host.Deployment)
	if err != nil {
		return runhost.Plan{}, fmt.Errorf("orchestrator: listing run hosts: %w", err)
	}
	cursors, err := store.SpoolCursors(ctx)
	if err != nil {
		return runhost.Plan{}, fmt.Errorf("orchestrator: reading spool cursors: %w", err)
	}
	runs, err := store.LiveRuns(ctx)
	if err != nil {
		return runhost.Plan{}, fmt.Errorf("orchestrator: reading live runs: %w", err)
	}
	named := map[string]bool{}
	for _, h := range hosts {
		named[h.Labels.Run] = true
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
	plan := runhost.Decide(claims, hosts, func(i runhost.Info) runhost.SpoolState {
		e, _ := runhost.ReadExit(i.Labels.Spool)
		return runhost.SpoolState{Exited: e != nil}
	}, now)

	var errs []error
	// keep is the spools still in use: an adopted run host's, and one
	// whose run host could not be destroyed.
	keep := map[string]bool{}
	release := func(h runhost.Info, reason string) {
		log.Printf("orchestrator: freeing run host %s (task %s, run %s, generation %s): %s",
			h.Name, h.Labels.Task, h.Labels.Run, h.Labels.Generation, reason)
		if err := driver.Destroy(ctx, h.Name); err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: destroying run host %s: %w", h.Name, err))
			keep[h.Labels.Spool] = true
			return
		}
		if h.Labels.Spool != "" {
			os.RemoveAll(h.Labels.Spool)
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
		if adopted != nil && adopted(a, cursors[a.Claim.Run].Offset) {
			log.Printf("orchestrator: adopted run host %s for run %s at cursor %d after a %s gap",
				a.Host.Name, a.Claim.Run, cursors[a.Claim.Run].Offset, a.Gap.Round(time.Second))
			keep[a.Host.Labels.Spool] = true
			continue
		}
		release(a.Host, "matched its live run, but this daemon could not adopt it")
		finish(a.Claim, fmt.Sprintf("its run host %s was still there, but this daemon could not adopt the run "+
			"(see its journal for why)", a.Host.Name))
	}
	if err := sweepRunHostDirs(ctx, store, host, keep, nil, now); err != nil {
		errs = append(errs, err)
	}
	return plan, errors.Join(errs...)
}

// runHostSweepGrace is how old a spool or staging entry must be before
// SweepOrphanRunHosts removes it: the periodic pass runs beside runs this
// daemon is starting, and an entry written in the moments before its run
// shows up anywhere the pass looks must not go out from under it.
const runHostSweepGrace = 10 * time.Minute

// SweepOrphanRunHosts is RecoverRunHosts' release half run on every
// reconcile tick rather than once at startup: a run host's own run
// destroys it as it finishes (runhost.Host.Wait), but when that one
// Destroy fails -- a kubectl or API server timeout -- the exited run host
// (a Succeeded or Failed pod, a stopped container) would otherwise sit in
// the cluster until the daemon next restarted. It destroys every run host
// of host's deployment whose run is neither in flight in this daemon nor
// a live task_run row, removes its spool, sweeps the spool and staging
// roots the same way RecoverRunHosts does (sparing anything younger than
// runHostSweepGrace), and returns how many run hosts it destroyed.
//
// inFlight is read after the listing, not before: a run that starts
// between the two is in flight by the time its run host can be listed
// (InFlight.Go registers it before the run creates one), so it is never
// mistaken for an orphan. Nothing here finishes a run or adopts one;
// that stays startup recovery's.
func SweepOrphanRunHosts(ctx context.Context, store *model.Store, host *runhost.Host, inFlight func() []string, now time.Time) (int, error) {
	hosts, err := host.Driver.List(ctx, host.Deployment)
	if err != nil {
		return 0, fmt.Errorf("orchestrator: listing run hosts: %w", err)
	}
	runs, err := store.LiveRuns(ctx)
	if err != nil {
		return 0, fmt.Errorf("orchestrator: reading live runs: %w", err)
	}
	claimed := map[string]bool{}
	for _, r := range runs {
		claimed[r.ID] = true
	}
	var flying []string
	if inFlight != nil {
		flying = inFlight()
	}
	for _, id := range flying {
		claimed[id] = true
	}
	var errs []error
	keep := map[string]bool{}
	reaped := 0
	for _, h := range hosts {
		if h.Labels.Run != "" && claimed[h.Labels.Run] {
			keep[h.Labels.Spool] = true
			continue
		}
		log.Printf("orchestrator: freeing run host %s (task %s, run %s, running %t): no live run claims it",
			h.Name, h.Labels.Task, h.Labels.Run, h.Running)
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runCleanupTimeout)
		err := host.Driver.Destroy(cctx, h.Name)
		cancel()
		if err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: destroying run host %s: %w", h.Name, err))
			keep[h.Labels.Spool] = true
			continue
		}
		reaped++
		if h.Labels.Spool != "" {
			os.RemoveAll(h.Labels.Spool)
		}
	}
	if err := sweepRunHostDirs(ctx, store, host, keep, flying, now.Add(-runHostSweepGrace)); err != nil {
		errs = append(errs, err)
	}
	return reaped, errors.Join(errs...)
}

// sweepRunHostDirs removes what is left under host's spool and staging
// roots by runs no run host carries any more: a run host that was gone
// at startup leaves its spool behind (the path is only on its label),
// and one created but never started leaves the spool's env file, with
// the run's credentials in it.
//
// The pass may run while this daemon starts runs of its own (the
// re-adoption after an upgrade that did not restart), so only an entry
// last modified before now is touched, and of those a spool is kept when
// it is in keep or named for a run still live ("run-<run>-..."), and a
// staging entry when it is no older than the oldest live run. A spool
// named for one of inFlight -- runs this daemon is driving that the store
// may not list as live -- is kept too.
func sweepRunHostDirs(ctx context.Context, store *model.Store, host *runhost.Host, keep map[string]bool, inFlight []string, now time.Time) error {
	if host.SpoolRoot == "" && host.StagingRoot == "" {
		return nil
	}
	runs, err := store.LiveRuns(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading live runs to sweep run-host directories: %w", err)
	}
	stagingCutoff := now
	for _, r := range runs {
		if r.StartedAt.Before(stagingCutoff) {
			stagingCutoff = r.StartedAt
		}
	}
	live := func(name string) bool {
		for _, r := range runs {
			if strings.HasPrefix(name, "run-"+r.ID+"-") {
				return true
			}
		}
		for _, id := range inFlight {
			if strings.HasPrefix(name, "run-"+id+"-") {
				return true
			}
		}
		return false
	}
	var errs []error
	sweep := func(root, what string, cutoff time.Time, spare func(path, name string) bool) {
		if root == "" {
			return
		}
		entries, err := os.ReadDir(root)
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: reading %s root: %w", what, err))
			return
		}
		for _, e := range entries {
			path := filepath.Join(root, e.Name())
			info, err := e.Info()
			if err != nil || !info.ModTime().Before(cutoff) || spare(path, e.Name()) {
				continue
			}
			log.Printf("orchestrator: removing %s %s: no run host is left for it", what, path)
			if err := os.RemoveAll(path); err != nil {
				errs = append(errs, fmt.Errorf("orchestrator: removing %s %s: %w", what, path, err))
			}
		}
	}
	sweep(host.SpoolRoot, "spool", now, func(path, name string) bool { return keep[path] || live(name) })
	sweep(host.StagingRoot, "staging entry", stagingCutoff, func(string, string) bool { return false })
	return errors.Join(errs...)
}
