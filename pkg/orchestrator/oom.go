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
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/granule"
	"github.com/ai-on-gke/grain/pkg/model"
)

// SandboxOOMReporter is implemented by a Sandbox that can say whether the
// kernel OOM-killed it -- asked once its run or CI job has failed and
// before it is released, since what it reads (a container's state, a
// pod's container statuses, a guest's console) goes with the sandbox.
//
// Under auto overcommit (design/design/auto-overcommit.design.md) grain admits
// more sandboxes than the host strictly has memory for and lets the
// kernel pick one off when it is wrong. This is how grain finds out it
// was one of its own, so that the run is retried as infrastructure
// rather than held against its task, and so that the controller backs
// off (Config.NoteOOM).
//
// Exported because a CI sandbox wraps one of these (daemon.CiSandbox)
// and forwards it.
type SandboxOOMReporter interface {
	// SandboxOOM reports whether this sandbox was OOM-killed, with a
	// sentence saying how grain knows. Best effort: anything it cannot
	// read is "not killed".
	SandboxOOM(ctx context.Context) (detail string, killed bool)
}

// oomCheckTimeout bounds one SandboxOOM, which runs on the way out of a
// run that has already failed.
const oomCheckTimeout = 20 * time.Second

// granuleOOM decides whether a granule VM died of OOM from the host's two
// accounts of it: its VM container's state (granule.DockerContainerExit)
// and its guest's serial console (granule.DockerContainerLogTail). The
// VM process being OOM-killed, or killed outright (SIGKILL, exit 137,
// which is what the host's OOM killer does to it), counts; so does the
// guest's own kernel saying it killed something for want of memory,
// which is the same shortage seen from inside the VM.
func granuleOOM(exit granule.ContainerExit, exitErr error, console string) (string, bool) {
	if exitErr == nil {
		if exit.OOMKilled {
			return fmt.Sprintf("the granule VM process was OOM-killed (container %s, exit %d)", exit.Status, exit.ExitCode), true
		}
		if dockerContainerDead[exit.Status] && exit.ExitCode == 137 {
			return "the granule VM process was killed (SIGKILL, exit 137)", true
		}
	}
	if granule.GuestOOMLine(console) != "" {
		// Not the line itself: the console is the guest's to write to,
		// and this sentence goes into a grain-authored conversation note
		// and a CI job's warning and error. SandboxOOM logs the line.
		return "the guest kernel reported an OOM kill on its serial console", true
	}
	return "", false
}

func truncateOOMLine(line string) string {
	const max = 240
	if len(line) > max {
		return line[:max] + "..."
	}
	return line
}

// SandboxOOM implements SandboxOOMReporter for a granule VM.
func (s *granuleSandbox) SandboxOOM(ctx context.Context) (string, bool) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), oomCheckTimeout)
	defer cancel()
	k := s.owner
	vmExit, consoleLog := granule.DockerContainerExit, func(ctx context.Context, vm string) (string, error) {
		return granule.DockerContainerLogTail(ctx, vm, guestConsoleTailLines)
	}
	if k.vmExit != nil {
		vmExit = k.vmExit
	}
	if k.consoleLog != nil {
		consoleLog = k.consoleLog
	}
	exit, exitErr := vmExit(ctx, s.vmName)
	console, err := consoleLog(ctx, s.vmName)
	if err != nil {
		console = ""
	}
	how, killed := granuleOOM(exit, exitErr, console)
	if line := granule.GuestOOMLine(console); killed && line != "" {
		log.Printf("orchestrator: sandbox %s: guest console OOM line: %q", s.vmName, truncateOOMLine(line))
	}
	return how, killed
}

// containerOOM decides from `docker inspect`'s State whether a container
// sandbox was OOM-killed: docker's own OOMKilled flag, which the kernel's
// cgroup OOM event sets and which exit 137 alone does not (a `docker
// kill` exits 137 too).
func containerOOM(out string) (string, bool) {
	fields := strings.Fields(out)
	if len(fields) < 2 || fields[0] != "true" {
		return "", false
	}
	code, _ := strconv.Atoi(fields[1])
	return fmt.Sprintf("the sandbox container was OOM-killed (docker State.OOMKilled, exit %d)", code), true
}

// SandboxOOM implements SandboxOOMReporter for a container sandbox.
func (s *containerSandbox) SandboxOOM(ctx context.Context) (string, bool) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), oomCheckTimeout)
	defer cancel()
	out, err := s.owner.run(ctx, "inspect", "--type", "container", "--format", "{{.State.OOMKilled}} {{.State.ExitCode}}", s.container)
	if err != nil {
		return "", false
	}
	return containerOOM(out)
}

// SandboxOOM implements SandboxOOMReporter for a pod: a container whose
// status, current or last, gives reason OOMKilled.
func (s *k8sSandbox) SandboxOOM(ctx context.Context) (string, bool) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), oomCheckTimeout)
	defer cancel()
	container, killed, err := s.owner.client.OOMKilled(ctx, s.podName)
	if err != nil || !killed {
		return "", false
	}
	return fmt.Sprintf("container %s of sandbox pod %s was OOMKilled", container, s.podName), true
}

// errSandboxOOM is the cause a run's error is wrapped in when its
// sandbox was OOM-killed (applySandboxOOM).
var errSandboxOOM = errors.New("sandbox was OOM-killed")

// oomFallbackMemoryMB is what raiseMemoryAfterOOM doubles when neither
// the task nor its backend says how much memory the killed sandbox had.
const oomFallbackMemoryMB = 4096

// oomMemoryCeilingFactor bounds raiseMemoryAfterOOM: a task is never
// raised past this many times its backend's default (or
// oomFallbackMemoryMB). The second and third OOM kills of one streak
// reach it from the default; without it each new streak -- one is
// started again by every ordinary failure in between, and an OOM kill is
// in part the guest's own word (granuleOOM) -- would double the task
// again, without limit.
const oomMemoryCeilingFactor = 4

// oomOverridable is whether a run that ended with outcome is one an OOM
// kill should be the explanation for: an ordinary failure. Every other
// ending already has a cause of its own -- a success, a human's
// cancellation, the runtime cap, a pause, a provider's capacity, the
// daemon stopping -- and keeps it.
func oomOverridable(outcome string) bool { return outcome == "failed" }

// applySandboxOOM asks this run's sandbox, once its agent has failed,
// whether the kernel OOM-killed it, and if so rewrites how the run ended:
//
//   - the controller is told (Config.NoteOOM), whatever else happens;
//   - the run is recorded as model.OOMOutcome, which no failure streak
//     counts, and the task goes back to the queue to resume its
//     conversation, as after a daemon restart;
//   - the task's conversation gets a note saying so;
//   - from the second in a row the task asks for twice the memory
//     (raiseMemoryAfterOOM);
//   - past model.MaxOOMRetries in a row it is an ordinary failure again,
//     so a task that needs more than the host has still retires.
func applySandboxOOM(ctx context.Context, store *model.Store, cfg Config, task model.Task, runID string, end runEnding) runEnding {
	if cfg.sandboxOOM == nil || !oomOverridable(end.outcome) {
		return end
	}
	how, killed := cfg.sandboxOOM.SandboxOOM(ctx)
	if !killed {
		return end
	}
	now := cfg.now()
	log.Printf("orchestrator: run %s: %s", runID, how)
	if cfg.NoteOOM != nil {
		cfg.NoteOOM(now)
	}
	bg := context.WithoutCancel(ctx)
	prior, err := store.ConsecutiveOOMRuns(bg, task.ID)
	if err != nil {
		log.Printf("orchestrator: run %s: counting task %s's earlier OOM kills: %v", runID, task.ID, err)
	}
	nth := prior + 1
	if nth > model.MaxOOMRetries {
		end.detail = fmt.Sprintf("sandbox was OOM-killed (%s), the %s in a row; counted as a failure: %s",
			how, ordinal(nth), end.detail)
		end.runErr = wrapOOM(runID, end.runErr)
		return end
	}
	end.outcome = model.OOMOutcome
	end.detail = model.OOMDetailPrefix + " (" + how + ")"
	end.unmatched = ""
	end.runErr = wrapOOM(runID, end.runErr)
	raised := ""
	if nth >= 2 {
		raised = raiseMemoryAfterOOM(bg, store, cfg, task, runID, nth)
	}
	noteOOM(bg, store, task.ID, runID, how, raised, now)
	return end
}

func wrapOOM(runID string, err error) error {
	if err == nil {
		return fmt.Errorf("orchestrator: run %s: %w", runID, errSandboxOOM)
	}
	return fmt.Errorf("orchestrator: run %s: %w: %w", runID, errSandboxOOM, err)
}

// raiseMemoryAfterOOM doubles the memory task asks its sandbox for, from
// whatever the OOM-killed one had: the task's own size, else its
// backend's default, up to oomMemoryCeilingFactor times that default.
// nth is which OOM kill in a row this is, for the note. It returns a
// sentence for the conversation note, "" when nothing changed.
func raiseMemoryAfterOOM(ctx context.Context, store *model.Store, cfg Config, task model.Task, runID string, nth int) string {
	base := cfg.sandboxDefaultMemoryMB
	if base <= 0 {
		base = oomFallbackMemoryMB
	}
	ceiling := base * oomMemoryCeilingFactor
	var from, to int
	err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		from = t.SandboxMemoryMB
		if from <= 0 {
			from = base
		}
		to = min(from*2, ceiling)
		if to <= from {
			to = from
			return nil
		}
		t.SandboxMemoryMB = to
		return nil
	})
	if err == nil && to == from {
		log.Printf("orchestrator: run %s: task %s already asks for %d MiB, at or past the %d MiB an OOM kill may raise it to",
			runID, task.ID, from, ceiling)
		return ""
	}
	if err != nil {
		log.Printf("orchestrator: run %s: raising task %s's sandbox memory after the %s OOM kill in a row: %v", runID, task.ID, ordinal(nth), err)
		return ""
	}
	return fmt.Sprintf("It is the %s OOM kill in a row, so the task's sandbox memory has been raised from %d MiB to %d MiB.", ordinal(nth), from, to)
}

// noteOOM tells the task why its run went back to the queue.
func noteOOM(ctx context.Context, store *model.Store, taskID, runID, how, raised string, now time.Time) {
	body := fmt.Sprintf("Run %s: %s. %s. Nothing about this task caused it, so it does not count against "+
		"the task's failure streak, and the task has been queued again. The next run picks the conversation "+
		"back up where the agent's framework kept it, in a fresh sandbox: anything the killed run wrote and "+
		"did not push is gone.", runID, model.OOMDetailPrefix, capitalize(how))
	if raised != "" {
		body += " " + raised
	}
	ctx, cancel := context.WithTimeout(ctx, runCleanupTimeout)
	defer cancel()
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    taskID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		Body:      body,
		CreatedAt: now,
	}); err != nil {
		log.Printf("orchestrator: run %s: noting on task %s that its sandbox was OOM-killed: %v", runID, taskID, err)
	}
}
