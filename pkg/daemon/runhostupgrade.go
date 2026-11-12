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

package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/runhost"
)

// runHostUpgrade is the daemon's half of the upgrade protocol for runs in
// run hosts (design/design/non-disruptive-upgrade.design.md, piece 6): the flush an
// upgrade makes before it restarts this daemon (upgrade.Config.Flush),
// the re-adoption if that restart leaves this process running, and the
// report of what the startup adoption pass did, for the post-upgrade
// check (orchestrator.Config.Adoption).
//
// It covers host CLIs a run drives through grain hostd the same way
// (runhost.Hostd): flushed with the run hosts, and adopted beside them.
//
// A nil *runHostUpgrade -- a deployment with neither -run-host-image nor
// grain hostd -- flushes nothing and reports nothing.
type runHostUpgrade struct {
	host     *runhost.Host
	hostd    *hostdRunSet
	stopping <-chan struct{}

	mu sync.Mutex
	// report is what this process's startup adoption pass did.
	report *orchestrator.AdoptionReport
	// readopt adopts back the runs a flush let go of; set once the
	// orchestrator's dependencies exist.
	readopt func()
}

func newRunHostUpgrade(host *runhost.Host, hd *hostdRunSet, ci bool) *runHostUpgrade {
	if host == nil && hd == nil && !ci {
		return nil
	}
	return &runHostUpgrade{host: host, hostd: hd}
}

func (u *runHostUpgrade) withStopping(ch <-chan struct{}) *runHostUpgrade {
	if u != nil {
		u.stopping = ch
	}
	return u
}

// flushHook is upgrade.Config.Flush, or nil.
func (u *runHostUpgrade) flushHook() func(context.Context) (func(), error) {
	if u == nil {
		return nil
	}
	return u.flush
}

// flush stops tailing every run in a run host, each with its cursor
// persisted (runhost.Host.Flush). The rows stay live, the leases held and
// the sandboxes standing, exactly as a stopping daemon leaves them, for
// the next build to adopt.
func (u *runHostUpgrade) flush(ctx context.Context) (func(), error) {
	var errs []error
	if u.host != nil {
		runs, err := u.host.Flush(ctx)
		log.Printf("grain daemon: upgrade: flushed the spool cursors of %d run(s) in run hosts before the restart: %v", len(runs), runs)
		errs = append(errs, err)
	}
	if u.hostd != nil {
		runs, err := u.hostd.Flush(ctx)
		log.Printf("grain daemon: upgrade: flushed the cursors of %d host CLI run(s) in grain hostd before the restart: %v", len(runs), runs)
		errs = append(errs, err)
	}
	if ciService := LiveCI.embedded(); ciService != nil {
		ciService.Detach()
	}
	return u.resume, errors.Join(errs...)
}

// resume is the far side of a flush whose restart did not happen: this
// daemon tails again, and adopts the runs it let go of as a new daemon
// would have. Once shutdown has started, it is a no-op so a late resume
// cannot adopt flushed runs back into a draining process.
func (u *runHostUpgrade) resume() {
	select {
	case <-u.stopping:
		return
	default:
	}
	if u.host != nil {
		u.host.Resume()
	}
	if u.hostd != nil {
		u.hostd.Resume()
	}
	if ciService := LiveCI.embedded(); ciService != nil {
		AdoptCIRuns(context.Background(), ciService)
	}
	u.mu.Lock()
	readopt := u.readopt
	u.mu.Unlock()
	if readopt != nil {
		log.Printf("grain daemon: upgrade: the restart left this daemon running; adopting back the runs it flushed")
		readopt()
	}
}

func (u *runHostUpgrade) setReport(r *orchestrator.AdoptionReport) {
	if u == nil {
		return
	}
	u.mu.Lock()
	if r != nil && u.report != nil {
		r.CIAdopted = u.report.CIAdopted
		r.CILost = u.report.CILost
	}
	u.report = r
	u.mu.Unlock()
}

func (u *runHostUpgrade) setCIAdoption(adopted, lost []orchestrator.AdoptedCIJobNote) {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.report == nil {
		u.report = &orchestrator.AdoptionReport{At: time.Now().UTC()}
	}
	u.report.CIAdopted = adopted
	u.report.CILost = lost
}

func (u *runHostUpgrade) hasAdoptedCI() bool {
	if u == nil {
		return false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.report != nil && len(u.report.CIAdopted) > 0
}

func (u *runHostUpgrade) setReadopt(f func()) {
	if u == nil {
		return
	}
	u.mu.Lock()
	u.readopt = f
	u.mu.Unlock()
}

// adoption is orchestrator.Config.Adoption, or nil.
func (u *runHostUpgrade) adoption() func() *orchestrator.AdoptionReport {
	if u == nil {
		return nil
	}
	return func() *orchestrator.AdoptionReport {
		u.mu.Lock()
		defer u.mu.Unlock()
		return u.report
	}
}

// adoptRunHosts is the adoption pass (orchestrator.RecoverRunHosts) with
// the daemon's seam: a matched run whose sandbox this backend can claim
// back is adopted, for the caller to resume (orchestrator.ResumeAdoptedRun);
// one whose sandbox cannot be claimed back is finished as interrupted.
// It runs at startup, and again in a daemon whose upgrade's restart did
// not happen.
func adoptRunHosts(ctx context.Context, store *model.Store, client github.Client, host *runhost.Host,
	sandboxes orchestrator.Sandboxes) ([]orchestrator.AdoptedRun, map[string]bool, *orchestrator.AdoptionReport, error) {
	var adopted []orchestrator.AdoptedRun
	took := map[string]bool{}
	now := time.Now().UTC()
	plan, err := orchestrator.RecoverRunHosts(ctx, store, client, host, now,
		func(a runhost.Adoption, cursor int64) bool {
			sb, err := orchestrator.ReadoptSandbox(ctx, sandboxes, a.Claim.SandboxName())
			if err != nil {
				log.Printf("grain daemon: not adopting run %s: its sandbox: %v", a.Claim.Run, err)
				return false
			}
			adopted = append(adopted, orchestrator.AdoptedRun{Adoption: a, Cursor: cursor, Sandbox: sb})
			took[a.Claim.Run] = true
			return true
		})
	if err != nil {
		err = fmt.Errorf("recovering run hosts: %w", err)
	}
	report := orchestrator.NewAdoptionReport(now, plan, took)
	log.Printf("grain daemon: run-host adoption: %d adopted, %d lost, %d unclaimed run host(s) freed",
		len(report.Adopted), len(report.Lost), report.Freed)
	return adopted, took, report, err
}

// adoptionPass is what one adoption pass took, by where each run's CLI is
// still running.
type adoptionPass struct {
	// runHost are resumed through the run-host Host, hostd through the
	// runhost.Hostd of the grain hostd each ran on
	// (orchestrator.ResumeAdoptedRun).
	runHost []orchestrator.AdoptedRun
	hostd   []hostdAdopted
	// took is every adopted run, for RecoverOrphanedRunsExcept to spare.
	took map[string]bool
	// away is, per grain hostd that did not answer, the live runs the pass
	// left to RecoverOrphanedRunsExcept (hostdAdopted.abandoned), for
	// cancelAbandoned once those are finished.
	away []hostdAdopted
}

// cancelAbandoned cancels, in the background, the processes of the runs
// each away hostd did not answer for (orchestrator.CancelWhenBack), once
// it answers again. Call it only after RecoverOrphanedRunsExcept has
// finished those runs: CancelWhenBack spares a run that is still live, so
// a hostd back before then would have nothing cancelled.
func (p adoptionPass) cancelAbandoned(ctx context.Context, store *model.Store) {
	for _, h := range p.away {
		go func() {
			wctx, cancel := context.WithTimeout(ctx, hostdAbandonedWait)
			defer cancel()
			cancelled, err := orchestrator.CancelWhenBack(wctx, store, h.host.Client, h.abandoned, h.host.Reconnected)
			if err != nil {
				log.Printf("grain daemon: grain hostd %s: %v", h.name, err)
			}
			if len(cancelled) > 0 {
				log.Printf("grain daemon: grain hostd %s is back; cancelled %d process(es) of runs it did not answer for at startup",
					h.name, len(cancelled))
			}
		}()
	}
}

// hostdAdopted is the runs one grain hostd's adoption took, and the
// runhost.Hostd they are resumed through.
type hostdAdopted struct {
	name string
	host *runhost.Hostd
	runs []orchestrator.AdoptedRun
	// abandoned is, in adoptionPass.away, the runs it did not answer for.
	abandoned []string
}

// adoptAll is the whole adoption pass: run hosts (adoptRunHosts) when this
// deployment has them, and grain hostd's detached host CLIs
// (adoptHostd) on every hostd a live run ran on when it has that, with
// one report for the post-upgrade check. A pass that errs still returns
// what it adopted.
func adoptAll(ctx context.Context, store *model.Store, client github.Client, host *runhost.Host, hd *hostdRunSet,
	sandboxes orchestrator.Sandboxes) (adoptionPass, *orchestrator.AdoptionReport, error) {
	out := adoptionPass{took: map[string]bool{}}
	if host == nil && hd == nil {
		return out, nil, nil
	}
	var errs []error
	report := &orchestrator.AdoptionReport{At: time.Now().UTC()}
	if host != nil {
		adopted, took, r, err := adoptRunHosts(ctx, store, client, host, sandboxes)
		errs = append(errs, err)
		out.runHost = adopted
		for run := range took {
			out.took[run] = true
		}
		report = r
	}
	if hd != nil {
		targets, err := hostdAdoptionTargets(ctx, store, hd)
		errs = append(errs, err)
		for _, t := range targets {
			adopted, took, away, r, err := adoptHostd(ctx, store, client, t, sandboxes)
			errs = append(errs, err)
			if len(adopted) > 0 {
				out.hostd = append(out.hostd, hostdAdopted{name: t.name, host: t.host, runs: adopted})
			}
			if len(away) > 0 {
				out.away = append(out.away, hostdAdopted{name: t.name, host: t.host, abandoned: away})
			}
			for run := range took {
				out.took[run] = true
			}
			report.Adopted = append(report.Adopted, r.Adopted...)
			report.Lost = append(report.Lost, r.Lost...)
			report.Freed += r.Freed
		}
	}
	return out, report, errors.Join(errs...)
}

// hostdAdoptWait bounds how long the adoption pass waits for the grain
// hostds its live runs ran on to dial in again after a restart, all of
// them together, before deciding without the ones still away.
const hostdAdoptWait = 30 * time.Second

// hostdAbandonedWait bounds how long the processes of runs a hostd did
// not answer for at startup are waited on to cancel
// (orchestrator.CancelWhenBack): past a run's budget and allowance,
// hostd ends the process itself.
const hostdAbandonedWait = 24 * time.Hour

// hostdAdoptionTarget is one grain hostd an adoption pass asks, and the
// live runs it decides there: those whose recorded hostd
// (model.TaskRunHostd) is this one, or -- for the default hostd -- that
// recorded none, as every run did before runs were routed.
type hostdAdoptionTarget struct {
	name string
	host *runhost.Hostd
	owns func(runID string) bool
}

// hostdAdoptionTargets is every hostd hd's adoption pass asks: the
// default hostd, where the host executor is one, and each other hostd a
// live run driven through grain hostd recorded, waited for (hostdAdoptWait)
// where it dials in. A hostd that cannot be reached at all is left out,
// its runs to RecoverOrphanedRuns, and the error says so.
func hostdAdoptionTargets(ctx context.Context, store *model.Store, hd *hostdRunSet) ([]hostdAdoptionTarget, error) {
	owner, err := liveRunHostds(ctx, store, hd.defaultName)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	if hd.defaultRuns() != nil {
		names[hd.defaultName] = true
	}
	for _, name := range owner {
		names[name] = true
	}
	sorted := make([]string, 0, len(names))
	for name := range names {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)

	var errs []error
	waitCtx, cancel := context.WithTimeout(ctx, hostdAdoptWait)
	defer cancel()
	var out []hostdAdoptionTarget
	for _, name := range sorted {
		host, ok := hd.named(name)
		if !ok {
			errs = append(errs, fmt.Errorf("the host agent %s, which a live run ran on, cannot be reached from this daemon; its runs are recovered as any other", name))
			continue
		}
		if hd.wait != nil && hasOwned(owner, name) {
			if err := hd.wait(waitCtx, name); err != nil {
				log.Printf("grain daemon: grain hostd %s has not dialed in again after %s; deciding its runs without it", name, hostdAdoptWait)
			}
		}
		name := name
		out = append(out, hostdAdoptionTarget{name: name, host: host, owns: func(run string) bool {
			if got, ok := owner[run]; ok {
				return got == name
			}
			return name == hd.defaultName
		}})
	}
	return out, errors.Join(errs...)
}

// hasOwned reports whether any run in owner ran on name.
func hasOwned(owner map[string]string, name string) bool {
	for _, n := range owner {
		if n == name {
			return true
		}
	}
	return false
}

// liveRunHostds is the hostd each live run driven through grain hostd (a
// hostd cursor) recorded it ran on, by run id; a run that recorded none
// ran on the default hostd, named defaultName.
func liveRunHostds(ctx context.Context, store *model.Store, defaultName string) (map[string]string, error) {
	cursors, err := store.HostdCursors(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading host cursors: %w", err)
	}
	runs, err := store.LiveRuns(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading live runs: %w", err)
	}
	out := map[string]string{}
	for _, r := range runs {
		if _, ok := cursors[r.ID]; !ok {
			continue
		}
		rec, err := store.RunHostd(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		if rec != nil && rec.Hostd != "" {
			out[r.ID] = rec.Hostd
		} else {
			out[r.ID] = defaultName
		}
	}
	return out, nil
}

// adoptHostd is adoptRunHosts for one grain hostd
// (orchestrator.RecoverHostdRuns): a detached host CLI of a run t owns
// whose sandbox this backend can claim back is adopted, for the caller to
// resume through t.host. away is the live runs it decided nothing about
// because the hostd did not answer (orchestrator.HostdAwayError).
func adoptHostd(ctx context.Context, store *model.Store, client github.Client, t hostdAdoptionTarget,
	sandboxes orchestrator.Sandboxes) ([]orchestrator.AdoptedRun, map[string]bool, []string, *orchestrator.AdoptionReport, error) {
	var adopted []orchestrator.AdoptedRun
	took := map[string]bool{}
	now := time.Now().UTC()
	hd := t.host
	plan, err := orchestrator.RecoverHostdRuns(ctx, store, client, hd.Client, hd.Client.Build, now, t.owns,
		func(a runhost.Adoption, cursor int64) bool {
			sb, err := orchestrator.ReadoptSandbox(ctx, sandboxes, a.Claim.SandboxName())
			if err != nil {
				log.Printf("grain daemon: not adopting run %s from grain hostd %s: its sandbox: %v", a.Claim.Run, t.name, err)
				return false
			}
			adopted = append(adopted, orchestrator.AdoptedRun{Adoption: a, Cursor: cursor, Sandbox: sb})
			took[a.Claim.Run] = true
			return true
		})
	var away []string
	var awayErr *orchestrator.HostdAwayError
	if errors.As(err, &awayErr) {
		away = awayErr.Runs
	}
	if err != nil {
		err = fmt.Errorf("recovering the host agent %s's host CLIs: %w", t.name, err)
	}
	report := orchestrator.NewAdoptionReport(now, plan, took)
	log.Printf("grain daemon: grain hostd %s adoption: %d adopted, %d lost, %d unclaimed process(es) released",
		t.name, len(report.Adopted), len(report.Lost), report.Freed)
	return adopted, took, away, report, err
}
