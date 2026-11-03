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

package upgrade

// What an upgrade says about itself while it runs (grain/task-37).
//
// An upgrade is minutes of work -- a cold `make container-build` is
// tens of them -- and until now it said nothing at all in between: the
// status file went from "running" with an empty Detail straight to "ok"
// or to a failure whose Detail was the failing command's entire output,
// and the daemon log carried not one line about any of it. An operator
// watching the UI's Upgrade pane, or reading journalctl on a deployment
// that had gone quiet, had no way to tell a build still churning
// through a docker layer from one wedged on an unreachable registry.
//
// So every upgrade now runs as a known list of steps, decided before it
// starts (Upgrader.plan) so that "step 2 of 5" means something the
// moment the first one begins, and a progress carries that list:
//
//   - it writes each transition into the status file, which is what the
//     Upgrade pane polls -- so the pane can show which step is running,
//     which are done, and what the current one last printed;
//   - it logs each transition, and every line the step's own commands
//     print, to the daemon log with an "upgrade: <branch>:" prefix -- so
//     journalctl shows the same build output the operator would have
//     seen running make by hand, rather than only the tail of it, and
//     only on failure.
//
// The status file is written on every step transition and, while a step
// is running, at most once per progressFlushInterval as its command
// prints -- a docker build prints far more often than a poller reads,
// and there is nothing to be gained by rewriting the file for each line.

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// StepState is where one step of an upgrade stands. Every step of the
// plan carries one, from the moment the upgrade starts: the ones not
// reached yet are StepPending, which is what lets the pane draw the
// whole sequence rather than only the part already run.
type StepState string

const (
	StepPending StepState = "pending"
	StepRunning StepState = "running"
	StepDone    StepState = "done"
	StepFailed  StepState = "failed"
	// StepSkipped is a step this deployment's configuration turns off --
	// the health check on an Upgrader with no HealthCheckArgs, the
	// sandbox image on a deployment that runs no sandbox containers. It
	// is in the plan rather than absent from it so that the reason it
	// did not happen is visible instead of merely missing.
	StepSkipped StepState = "skipped"
)

// Step is one stage of an upgrade, as Status reports it.
type Step struct {
	Name  string    `json:"name"`
	State StepState `json:"state"`
	// Detail is whatever that step has to say for itself: the last line
	// its command printed while it runs, what it accomplished once it is
	// done, why it was skipped, or the error that failed it.
	Detail     string     `json:"detail,omitempty"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// The step names, shared by both pipelines where they mean the same
// thing (a health check is a health check whether it ran a binary or an
// image, and an operator reading the pane should not have to learn two
// vocabularies for it).
const (
	stepCheckout    = "checkout"
	stepBuild       = "build"
	stepInstall     = "install"
	stepPull        = "pull image"
	stepSandbox     = "sandbox image"
	stepRecord      = "record image"
	stepHealthCheck = "health check"
	// Does the image about to be cut over to accept the flags this
	// deployment is started with -- a systemd unit's ExecStart, or a
	// Deployment's args? Either is written once and the image moves under
	// it, so this is the one step that is about the deployment's *command
	// line* rather than about the build (flags.go, grain/task-233 and
	// grain/task-244).
	stepFlags = "unit flags"
	// The one step that is about this deployment rather than about the
	// build arriving: does that build know the same database schema, and
	// if not, can it take these rows (schema.go).
	stepSchema  = "schema"
	stepRestart = "restart"
)

// progressFlushInterval bounds how often a running step's own output
// rewrites the status file. The pane polls every three seconds
// (ui/src/components/UpgradePanel.jsx's STATUS_POLL_MS), so a second is
// already finer than anything anyone can see, and a `docker build` can
// print hundreds of lines in that second.
const progressFlushInterval = time.Second

// maxStepDetail caps a step's Detail, which is a line of someone else's
// output and so is not bounded by anything this package controls -- a
// docker build can print a single line thousands of characters long,
// and the status file is read by a browser.
const maxStepDetail = 300

// progress owns an upgrade's Status while it runs: the phase, the step
// list, and every write of the status file. Its methods are safe to
// call from the goroutine draining a command's output as well as from
// the one sequencing the steps.
type progress struct {
	statusFile string
	// prefix is what every log line this upgrade writes starts with, so
	// that a journal holding two deployments' worth of noise still says
	// which branch these lines are about.
	prefix string

	mu            sync.Mutex
	status        Status
	current       int // index into status.Steps, or -1 between steps
	unchangedFlag bool
	lastFlush     time.Time
	dirty         bool
	startedAt     time.Time

	// stopped ends the flush loop below; stopOnce makes stop safe to
	// call from run's defer as well as from wherever the upgrade
	// actually ended. flushDone is closed by the loop itself as it
	// returns, so stop can wait for it -- see stop.
	stopped   chan struct{}
	flushDone chan struct{}
	stopOnce  sync.Once
}

// newProgress records the plan against status and writes it, so that
// the very first poll after Start returns sees the whole sequence with
// nothing yet begun.
func newProgress(statusFile, branch string, status Status, plan []string) *progress {
	steps := make([]Step, 0, len(plan))
	for _, name := range plan {
		steps = append(steps, Step{Name: name, State: StepPending})
	}
	status.Steps = steps
	p := &progress{
		statusFile: statusFile,
		prefix:     "upgrade: " + branch + ": ",
		status:     status,
		current:    -1,
		startedAt:  time.Now(),
		stopped:    make(chan struct{}),
		flushDone:  make(chan struct{}),
	}
	p.mu.Lock()
	p.flushLocked()
	p.mu.Unlock()
	go p.flushLoop()
	return p
}

// flushLoop persists whatever the running step has said since the last
// write, once per progressFlushInterval. note deliberately does not
// write on every line -- a build prints far faster than anything reads
// the file -- but a command that prints one line and then stalls for
// twenty minutes is precisely the case an operator needs that line for,
// and without this loop that line would sit in memory until something
// else happened to write.
func (p *progress) flushLoop() {
	defer close(p.flushDone)
	ticker := time.NewTicker(progressFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.stopped:
			return
		case <-ticker.C:
			p.mu.Lock()
			if p.dirty {
				p.flushLocked()
			}
			p.mu.Unlock()
		}
	}
}

// stop ends the flush loop, writing anything still unwritten. Called
// from run's own defer, so it runs however the upgrade ended.
//
// It waits for that loop to actually be gone before returning, so that
// when it does no goroutine of this upgrade's can write the status file
// again: a stop that only signalled the loop could return while the
// loop was on its way into a flush of its own, leaving one more write
// to land at an unpredictable moment after the upgrade was, as far as
// anything watching could tell, over.
func (p *progress) stop() {
	p.stopOnce.Do(func() {
		close(p.stopped)
		<-p.flushDone
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.dirty {
			p.flushLocked()
		}
	})
}

// logf writes one line to the daemon log, prefixed with this upgrade's
// branch.
func (p *progress) logf(format string, args ...any) {
	log.Printf(p.prefix+format, args...)
}

// start marks a step running. The step must be in the plan -- a
// mismatch between the plan and what run actually does is a bug in this
// package, and one that would otherwise show up as a pane that quietly
// never advances, so it is logged rather than ignored.
func (p *progress) start(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	i := p.indexLocked(name)
	if i < 0 {
		p.logf("step %q ran without being planned -- this is a bug in pkg/upgrade", name)
		return
	}
	now := time.Now().UTC()
	p.status.Steps[i].State = StepRunning
	p.status.Steps[i].StartedAt = &now
	p.current = i
	// The pane's own Detail line, for the sake of anything (an older
	// UI, curl) that reads the status without understanding steps --
	// but only while the upgrade is still running: once succeed has
	// written what the upgrade achieved, overwriting that with "step 5
	// of 5" would throw away the one line that says which image is now
	// installed.
	if p.status.Phase == PhaseRunning {
		p.status.Detail = fmt.Sprintf("step %d of %d: %s", i+1, len(p.status.Steps), name)
	}
	p.logf("step %d/%d %s: started", i+1, len(p.status.Steps), name)
	p.flushLocked()
}

// done marks the running step finished, with detail as what it
// accomplished ("pulled ghcr.io/...", "" where the name says it all).
func (p *progress) done(name, detail string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	i := p.indexLocked(name)
	if i < 0 {
		return
	}
	now := time.Now().UTC()
	p.status.Steps[i].State = StepDone
	p.status.Steps[i].FinishedAt = &now
	p.status.Steps[i].Detail = truncate(detail, maxStepDetail)
	p.current = -1
	took := elapsed(p.status.Steps[i].StartedAt, now)
	if detail != "" {
		p.logf("step %d/%d %s: done in %s: %s", i+1, len(p.status.Steps), name, took, detail)
	} else {
		p.logf("step %d/%d %s: done in %s", i+1, len(p.status.Steps), name, took)
	}
	p.flushLocked()
}

// skip marks a step this deployment does not do, with why as the
// reason -- "no health check configured", "this deployment runs no
// sandbox containers".
func (p *progress) skip(name, why string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	i := p.indexLocked(name)
	if i < 0 {
		return
	}
	p.status.Steps[i].State = StepSkipped
	p.status.Steps[i].Detail = truncate(why, maxStepDetail)
	p.logf("step %d/%d %s: skipped: %s", i+1, len(p.status.Steps), name, why)
	p.flushLocked()
}

// note records the latest thing the running step's command printed.
// This is the one call made from inside a command's output stream, so it
// is the one that throttles: a line arriving inside
// progressFlushInterval of the last write is left for flushLoop to
// persist rather than rewriting the status file for every line of a
// docker build. Every other transition here flushes immediately.
func (p *progress) note(line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.current < 0 {
		return
	}
	p.status.Steps[p.current].Detail = truncate(line, maxStepDetail)
	p.dirty = true
	if time.Since(p.lastFlush) < progressFlushInterval {
		// flushLoop picks it up within the interval.
		return
	}
	p.flushLocked()
}

// succeed closes the upgrade out as PhaseOK. restarting says whether a
// RestartCmd is still to come, which is the difference between "this
// deployment is now running the new build" and "it is about to be".
func (p *progress) succeed(detail string, restarting bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now().UTC()
	p.status.Phase = PhaseOK
	p.status.Detail = detail
	if restarting {
		p.status.Detail = detail + "; restarting"
	}
	p.status.FinishedAt = &now
	p.logf("succeeded in %s: %s", shortDuration(time.Since(p.startedAt)), p.status.Detail)
	p.flushLocked()
}

// markUnchanged notes during staging that the target carries the same
// version this deployment already runs, and returns detail for stage to
// hand back to run.
func (p *progress) markUnchanged(detail string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.unchangedFlag = true
	return detail
}

// isUnchanged reports whether staging stopped early because the target
// version has not changed.
func (p *progress) isUnchanged() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.unchangedFlag
}

// unchanged closes the upgrade out as PhaseOK with Status.Unchanged set,
// marking every step that had not run yet as StepSkipped so the pane
// shows why the rest of the plan was not needed.
func (p *progress) unchanged(detail string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now().UTC()
	for i := range p.status.Steps {
		if p.status.Steps[i].State == StepPending {
			p.status.Steps[i].State = StepSkipped
			p.status.Steps[i].Detail = "version has not changed"
		}
	}
	p.current = -1
	p.status.Phase = PhaseOK
	p.status.Unchanged = true
	p.status.Detail = detail
	p.status.FinishedAt = &now
	p.logf("unchanged after %s: %s", shortDuration(time.Since(p.startedAt)), p.status.Detail)
	p.flushLocked()
}

// fail closes the upgrade out as PhaseFailed and marks whichever step
// was running as the one that failed, so the pane can point at it
// rather than only reporting that something, somewhere, went wrong.
func (p *progress) fail(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now().UTC()
	if p.current >= 0 {
		step := &p.status.Steps[p.current]
		step.State = StepFailed
		step.FinishedAt = &now
		step.Detail = truncate(firstLine(err.Error()), maxStepDetail)
		p.logf("step %d/%d %s: failed after %s: %v",
			p.current+1, len(p.status.Steps), step.Name, elapsed(step.StartedAt, now), err)
		p.current = -1
	}
	p.status.Phase = PhaseFailed
	p.status.Detail = err.Error()
	p.status.FinishedAt = &now
	p.logf("failed after %s: %v", shortDuration(time.Since(p.startedAt)), err)
	p.flushLocked()
}

// restartFailed records a restart command that came back with an error
// -- which, on a deployment whose restart replaces this very process,
// can only be observed at all when the restart did not happen. The
// staging all succeeded, so nothing needs undoing; what is wrong is
// that this deployment is still running the old build and, without
// this, the last thing the pane would ever have said about it is
// "restarting".
func (p *progress) restartFailed(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now().UTC()
	if i := p.indexLocked(stepRestart); i >= 0 {
		p.status.Steps[i].State = StepFailed
		p.status.Steps[i].FinishedAt = &now
		p.status.Steps[i].Detail = truncate(firstLine(err.Error()), maxStepDetail)
	}
	p.status.Phase = PhaseFailed
	p.status.Detail = "the new build is staged but the restart failed, so this deployment is still running the old one: " + err.Error()
	p.status.FinishedAt = &now
	p.logf("restart failed after %s -- staged but not running: %v", shortDuration(time.Since(p.startedAt)), err)
	p.flushLocked()
}

func (p *progress) indexLocked(name string) int {
	for i := range p.status.Steps {
		if p.status.Steps[i].Name == name {
			return i
		}
	}
	return -1
}

// flushLocked persists the status. A write failure is logged and
// otherwise ignored: the upgrade itself is unaffected by the pane
// missing an update, and there is no caller left to return an error to.
func (p *progress) flushLocked() {
	p.lastFlush = time.Now()
	p.dirty = false
	if err := writeStatus(p.statusFile, p.status); err != nil {
		p.logf("recording progress in %s: %v", p.statusFile, err)
	}
}

// elapsed is how long a step took, given the start time it recorded --
// nil (a step that somehow never started) reads as unknown rather than
// as an enormous duration since the zero time.
func elapsed(started *time.Time, now time.Time) string {
	if started == nil {
		return "an unknown time"
	}
	return shortDuration(now.Sub(*started))
}

// shortDuration is a duration an operator reads rather than one a
// benchmark does: whole seconds for anything above one, milliseconds
// below.
func shortDuration(d time.Duration) string {
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
