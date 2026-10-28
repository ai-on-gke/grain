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
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/runhost"
)

// runHostUpgrade is the daemon's half of the upgrade protocol for runs in
// run hosts (docs/non-disruptive-upgrade.md, piece 6): the flush an
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
	host  *runhost.Host
	hostd *runhost.Hostd

	mu sync.Mutex
	// report is what this process's startup adoption pass did.
	report *orchestrator.AdoptionReport
	// readopt adopts back the runs a flush let go of; set once the
	// orchestrator's dependencies exist.
	readopt func()
}

func newRunHostUpgrade(host *runhost.Host, hd *runhost.Hostd, ci bool) *runHostUpgrade {
	if host == nil && hd == nil && !ci {
		return nil
	}
	return &runHostUpgrade{host: host, hostd: hd}
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
// would have.
func (u *runHostUpgrade) resume() {
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
			sb, err := orchestrator.ReadoptSandbox(ctx, sandboxes, a.Claim.Run)
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
	// runHost are resumed through the run-host Host, hostd through
	// runhost.Hostd (orchestrator.ResumeAdoptedRun).
	runHost, hostd []orchestrator.AdoptedRun
	// took is every adopted run, for RecoverOrphanedRunsExcept to spare.
	took map[string]bool
}

// adoptAll is the whole adoption pass: run hosts (adoptRunHosts) when this
// deployment has them, and grain hostd's detached host CLIs
// (adoptHostd) when it has that, with one report for the post-upgrade
// check. A pass that errs still returns what it adopted.
func adoptAll(ctx context.Context, store *model.Store, client github.Client, host *runhost.Host, hd *runhost.Hostd,
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
		adopted, took, r, err := adoptHostd(ctx, store, client, hd, sandboxes)
		errs = append(errs, err)
		out.hostd = adopted
		for run := range took {
			out.took[run] = true
		}
		report.Adopted = append(report.Adopted, r.Adopted...)
		report.Lost = append(report.Lost, r.Lost...)
		report.Freed += r.Freed
	}
	return out, report, errors.Join(errs...)
}

// adoptHostd is adoptRunHosts for grain hostd (orchestrator.RecoverHostd):
// a detached host CLI whose run's sandbox this backend can claim back is
// adopted, for the caller to resume through hd.
func adoptHostd(ctx context.Context, store *model.Store, client github.Client, hd *runhost.Hostd,
	sandboxes orchestrator.Sandboxes) ([]orchestrator.AdoptedRun, map[string]bool, *orchestrator.AdoptionReport, error) {
	var adopted []orchestrator.AdoptedRun
	took := map[string]bool{}
	now := time.Now().UTC()
	plan, err := orchestrator.RecoverHostd(ctx, store, client, hd.Client, hd.Client.Build, now,
		func(a runhost.Adoption, cursor int64) bool {
			sb, err := orchestrator.ReadoptSandbox(ctx, sandboxes, a.Claim.Run)
			if err != nil {
				log.Printf("grain daemon: not adopting run %s from grain hostd: its sandbox: %v", a.Claim.Run, err)
				return false
			}
			adopted = append(adopted, orchestrator.AdoptedRun{Adoption: a, Cursor: cursor, Sandbox: sb})
			took[a.Claim.Run] = true
			return true
		})
	if err != nil {
		err = fmt.Errorf("recovering grain hostd's host CLIs: %w", err)
	}
	report := orchestrator.NewAdoptionReport(now, plan, took)
	log.Printf("grain daemon: grain hostd adoption: %d adopted, %d lost, %d unclaimed process(es) released",
		len(report.Adopted), len(report.Lost), report.Freed)
	return adopted, took, report, err
}
