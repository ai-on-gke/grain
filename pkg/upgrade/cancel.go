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

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DefaultGraceWindow is the default countdown between confirming a manual
// upgrade and starting its work on a daemon deployment (-upgrade-grace-window;
// 0 disables the countdown).
const DefaultGraceWindow = 10 * time.Second

// PhaseCancelled is recorded when an operator or authorized caller cancels a
// running upgrade before its point of no return.
const PhaseCancelled Phase = "cancelled"

// StepCancelled marks the step that was running when an upgrade was cancelled.
const StepCancelled StepState = "cancelled"

// stepGrace is the initial countdown step on manual upgrades configured with a
// positive Config.GraceWindow.
const stepGrace = "grace window"

// TooLateError is returned by Cancel once an upgrade has reached its point of
// no return -- rewriting the image ref file, repointing the Kubernetes
// Deployment, or requesting the service restart -- or while the process is
// waiting on the restart that follows. HTTP callers map it to 409 Conflict.
type TooLateError struct {
	Target   string
	Previous string
	Message  string
}

func (e *TooLateError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// IsTooLate reports whether err is a *TooLateError, returning it when so.
func IsTooLate(err error) (*TooLateError, bool) {
	var target *TooLateError
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

func formatTooLateMessage(target, previous string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		target = "the new build"
	}
	previous = strings.TrimSpace(previous)
	if previous != "" {
		return fmt.Sprintf("restarting onto %s — too late to cancel; upgrade back to %s afterwards", target, previous)
	}
	return fmt.Sprintf("restarting onto %s — too late to cancel", target)
}

// ShortImageRef formats an image reference concisely for operator-facing copy
// ("Roll back to <previous>" and the 409 message after the point of no return)
// while Status.PreviousImage preserves the full digest-pinned reference.
func ShortImageRef(ref, targetRefOrBranch string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	named, digest, pinned := strings.Cut(ref, "@")
	if !pinned {
		return ref
	}
	shortDigest := digest
	if hex, ok := strings.CutPrefix(digest, "sha256:"); ok && len(hex) > 12 {
		shortDigest = "sha256:" + hex[:12]
	}
	hasTag := repositoryOf(named) != named
	if !hasTag {
		return named + "@" + shortDigest
	}
	// Keep the short digest when the tag alone would look identical to what is
	// being upgraded onto (for example rolling back from one :main build to the
	// :main digest it replaced).
	targetTrimmed := strings.TrimSpace(targetRefOrBranch)
	targetNamed, _, _ := strings.Cut(targetTrimmed, "@")
	if targetNamed == named || named == repositoryOf(named)+":"+TagForBranch(targetTrimmed) {
		return named + "@" + shortDigest
	}
	return named
}

func previousDisplayFromStatus(s Status) string {
	if s.PreviousImage != "" {
		return ShortImageRef(s.PreviousImage, s.Branch)
	}
	if s.PreviousRelease != "" {
		return s.PreviousRelease
	}
	if s.PreviousCommit != "" {
		if len(s.PreviousCommit) > 7 {
			return s.PreviousCommit[:7]
		}
		return s.PreviousCommit
	}
	return ""
}

// GraceWindow returns the configured manual-upgrade grace window (0 when disabled).
func (u *Upgrader) GraceWindow() time.Duration {
	if u.cfg.GraceWindow <= 0 {
		return 0
	}
	return u.cfg.GraceWindow
}

// StartScheduled validates branch and starts an upgrade that has already drained
// through the scheduled/automatic upgrade queue. Unlike a manual Start or
// StartImage, it skips Config.GraceWindow because the pending schedule was
// already cancellable throughout its drain.
func (u *Upgrader) StartScheduled(branch string) error {
	branch = strings.TrimSpace(branch)
	if err := u.checkBranch(branch); err != nil {
		return err
	}
	if err := u.checkConfiguredEdition(); err != nil {
		return err
	}
	return u.startWithOptions(branch, "", nil, false, "")
}

// Cancel stops an in-progress upgrade if it has not yet reached its point of no
// return, waiting for cleanup, run re-adoption, queue-hold release, and the
// cancelled status write to finish before returning.
//
// It returns:
//   - (true, nil) when a running upgrade was cancelled;
//   - (false, *TooLateError) once the upgrade has reached cut-over / restart
//     (or while this process is waiting for its requested restart to finish);
//   - (false, nil) when no upgrade is running.
func (u *Upgrader) Cancel(by, reason string) (bool, error) {
	by = flattenSingleLine(by)
	reason = flattenSingleLine(reason)

	u.mu.Lock()
	if u.done != nil {
		if u.irreversible {
			msg := formatTooLateMessage(u.targetLabel, u.previousLabel)
			t, p := u.targetLabel, u.previousLabel
			u.mu.Unlock()
			return false, &TooLateError{Target: t, Previous: p, Message: msg}
		}
		if !u.cancelled {
			u.cancelled = true
			u.cancelledBy = by
			u.cancelReason = reason
			if u.cancelFn != nil {
				u.cancelFn()
			}
		}
		done := u.done
		u.mu.Unlock()
		<-done
		return true, nil
	}
	u.mu.Unlock()

	status, err := u.Status()
	if err == nil && status.RestartPending(u.startedAt) {
		prev := previousDisplayFromStatus(status)
		msg := formatTooLateMessage(status.Branch, prev)
		return false, &TooLateError{Target: status.Branch, Previous: prev, Message: msg}
	}
	return false, nil
}

// CancelState reports whether the deployment's upgrade can currently be
// cancelled and, once past the point of no return, the operator-facing reason
// why it cannot.
func (u *Upgrader) CancelState(status Status) (cancellable bool, blockedReason string) {
	u.mu.Lock()
	if u.done != nil {
		if u.irreversible {
			msg := formatTooLateMessage(u.targetLabel, u.previousLabel)
			u.mu.Unlock()
			return false, msg
		}
		cancelling := u.cancelled
		u.mu.Unlock()
		if cancelling {
			return false, ""
		}
		return true, ""
	}
	u.mu.Unlock()

	if status.RestartPending(u.startedAt) {
		prev := previousDisplayFromStatus(status)
		return false, formatTooLateMessage(status.Branch, prev)
	}
	return false, ""
}

func (u *Upgrader) bindCancelLocked(cancel context.CancelFunc) bool {
	u.cancelFn = cancel
	if u.cancelled {
		cancel()
		return true
	}
	return false
}

func (u *Upgrader) isCancelled() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.cancelled
}

func (u *Upgrader) cancelActor() (by, reason string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.cancelledBy, u.cancelReason
}

// markIrreversible atomically transitions the active upgrade past its point of
// no return. It returns false if Cancel has already been called, in which case
// the caller must abort cut-over, clean up, and finish in PhaseCancelled.
func (u *Upgrader) markIrreversible() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.cancelled {
		return false
	}
	u.irreversible = true
	return true
}

func flattenSingleLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.TrimSpace(s)
}

func formatCancelledDetail(by, reason string) string {
	switch {
	case by != "" && reason != "":
		return fmt.Sprintf("cancelled by %s: %s", by, reason)
	case by != "":
		return fmt.Sprintf("cancelled by %s", by)
	case reason != "":
		return fmt.Sprintf("cancelled: %s", reason)
	default:
		return "cancelled"
	}
}

// imageKeptBy reports whether candidate matches any kept reference by tag,
// repository-qualified name, or digest.
func imageKeptBy(candidate string, kept ...string) bool {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return true
	}
	candNames := make(map[string]bool)
	for _, n := range imageNames(candidate) {
		candNames[n] = true
	}
	candDigest, hasCandDigest := kubeDigestRef(candidate)
	for _, k := range kept {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if hasCandDigest {
			if kd, ok := kubeDigestRef(k); ok && kd == candDigest {
				return true
			}
		}
		for _, kn := range imageNames(k) {
			if candNames[kn] {
				return true
			}
		}
	}
	return false
}

func (u *Upgrader) snapshotPrevious() (prevImg, prevRel, prevCommit string) {
	if r, err := u.Running(); err == nil {
		prevImg = strings.TrimSpace(r.Ref)
		prevRel = strings.TrimSpace(r.Build.Release)
		prevCommit = strings.TrimSpace(r.Build.Revision)
	}
	if prevRel == "" {
		prevRel = strings.TrimSpace(u.runningRelease())
	}
	return prevImg, prevRel, prevCommit
}

func (u *Upgrader) runGraceWindow(ctx context.Context, p *progress, until *time.Time, window time.Duration) error {
	if until == nil || window <= 0 {
		return nil
	}
	p.start(stepGrace)
	wait := time.Until(*until)
	if wait > 0 {
		p.note(fmt.Sprintf("waiting %s before starting (cancel any time)", shortDuration(wait)))
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.done(stepGrace, fmt.Sprintf("waited %s grace window", shortDuration(window)))
	return nil
}
