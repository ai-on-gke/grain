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

package engine

import (
	"context"
	"errors"
	"sync"
	"time"

	gritsteps "github.com/ai-on-gke/grain/pkg/grit/steps"
	"github.com/ai-on-gke/grain/pkg/sandboxaccess"
)

// DefaultAttachmentCheckpoint is how often a running step's output cursor
// is written back while it moves (design/design/grain-on-gke.design.md#GKE-4: at
// least every 10 s, and at step end).
const DefaultAttachmentCheckpoint = 10 * time.Second

// StepAttachment is where one running step's command stands in its
// sandbox: the exec the sandbox's wrapper runs it as and how much of its
// output has been read. It is what a restarted grain reattaches the step
// by (design/design/grain-on-gke.design.md, "Restart: reattach rather than
// interrupt").
type StepAttachment struct {
	RunID string
	// JobID is the job's PlannedJob.ID.
	JobID string
	// StepID is gritsteps.StepIDFor of the attempt and step.
	StepID  string
	Sandbox string
	ExecID  string
	Cursor  int64
}

// StepAttachments is where an Engine persists its running steps'
// attachments: written when a step's exec starts, checkpointed while it
// runs and at its end, and cleared once the job is done with its
// sandbox. ci.StoredRunStore is the one a daemon uses.
type StepAttachments interface {
	PutStepAttachment(ctx context.Context, a StepAttachment) error
	ClearStepAttachments(ctx context.Context, runID, jobID string) error
}

// stepAttacher is one job's view of Engine.Attachments: what it persists
// for each step, and the attachment a resumed job's in-flight step is to
// be reattached by.
type stepAttacher struct {
	e       *Engine
	runID   string
	jobID   string
	sandbox string
	// resume is the stored attachment of the step that was running when
	// grain last stopped, used once by the step it names.
	resume *StepAttachment
}

func (e *Engine) attacherFor(runID string, pj string, sandbox gritsteps.Sandbox, adopted *AdoptedJob) *stepAttacher {
	a := &stepAttacher{e: e, runID: runID, jobID: pj}
	if sandbox != nil {
		a.sandbox = sandbox.Name()
	}
	if adopted != nil && adopted.Attachment != nil && adopted.Attachment.ExecID != "" {
		resume := *adopted.Attachment
		a.resume = &resume
	}
	if e.Attachments == nil && a.resume == nil {
		return nil
	}
	return a
}

func (e *Engine) attachmentCheckpoint() time.Duration {
	if e.AttachmentCheckpoint > 0 {
		return e.AttachmentCheckpoint
	}
	return DefaultAttachmentCheckpoint
}

// forStep returns the attachment to run stepID with, and the function that
// writes its final cursor once the step has ended.
func (a *stepAttacher) forStep(ctx context.Context, stepID string) (*sandboxaccess.Attachment, func()) {
	if a == nil {
		return nil, func() {}
	}
	att := &sandboxaccess.Attachment{}
	rec := StepAttachment{RunID: a.runID, JobID: a.jobID, StepID: stepID, Sandbox: a.sandbox}
	if a.resume != nil && a.resume.StepID == stepID {
		att.ExecID, att.Cursor = a.resume.ExecID, a.resume.Cursor
		rec.ExecID, rec.Cursor = a.resume.ExecID, a.resume.Cursor
		a.resume = nil
	}
	store := a.e.Attachments
	if store == nil {
		return att, func() {}
	}
	// Written past the step's own context: the end-of-step checkpoint is
	// made after that context is done, and one cancelled under it is
	// exactly the step a restart wants to find.
	writeCtx := context.WithoutCancel(ctx)
	var (
		mu      sync.Mutex
		written = int64(-1)
		started bool
	)
	put := func() {
		mu.Lock()
		if !started || rec.Cursor == written {
			mu.Unlock()
			return
		}
		snapshot := rec
		written = rec.Cursor
		mu.Unlock()
		if err := store.PutStepAttachment(writeCtx, snapshot); err != nil {
			a.e.log("ci: run %s: job %s: step %s: recording its exec for a restart to reattach: %v", a.runID, a.jobID, stepID, err)
		}
	}
	att.Started = func(execID string) {
		mu.Lock()
		rec.ExecID, started = execID, true
		written = -1
		mu.Unlock()
		put()
	}
	att.Progress = func(cursor int64) {
		mu.Lock()
		rec.Cursor = cursor
		mu.Unlock()
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(a.e.attachmentCheckpoint())
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				put()
			}
		}
	}()
	return att, func() {
		close(stop)
		<-done
		put()
	}
}

// clear forgets the job's attachments once it is done with its sandbox.
func (a *stepAttacher) clear(ctx context.Context) {
	if a == nil || a.e.Attachments == nil {
		return
	}
	if err := a.e.Attachments.ClearStepAttachments(context.WithoutCancel(ctx), a.runID, a.jobID); err != nil {
		a.e.log("ci: run %s: job %s: clearing its step attachments: %v", a.runID, a.jobID, err)
	}
}

// lostSandbox stands in for a job sandbox that did not come back across a
// restart (AdoptedJob.Lost): the step that was running in it ends with
// the loss, which runSteps records as sandboxaccess.ChannelLostFault so
// the job goes the infra way -- errored, and re-run uncached.
type lostSandbox struct {
	name string
	err  error
}

func (s lostSandbox) Name() string { return s.name }

func (s lostSandbox) Exec(context.Context, gritsteps.Command) (gritsteps.CommandResult, error) {
	return gritsteps.CommandResult{ExitCode: -1}, s.err
}

func (lostSandbox) Release(context.Context) error { return nil }

// lostErr is the error a lost adopted job's step ends with: Lost itself
// where it already says ErrChannelLost, and otherwise a ChannelLostError
// wrapping it.
func lostErr(sandbox string, lost error) error {
	if errors.Is(lost, sandboxaccess.ErrChannelLost) {
		return lost
	}
	return &sandboxaccess.ChannelLostError{Sandbox: sandbox, Cause: lost}
}
