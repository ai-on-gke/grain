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
	"log"
	"regexp"
	"strings"
)

// ErrNothingToRollBackTo is what StartRollback returns when the last
// upgrade recorded nothing it could go back to: no digest-pinned
// PreviousImage on the image and Kubernetes pipelines, no PreviousCommit
// on the source pipeline.
var ErrNothingToRollBackTo = errors.New("the last upgrade recorded no previous build to roll back to")

// objectIDPattern is a full git object id, SHA-1 or SHA-256, in the
// lower-case hex git itself writes. Only a full one: an abbreviation is
// resolved against whatever the repository holds, and the point of
// rolling back by object id is that nothing gets to resolve it.
var objectIDPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func validateObjectID(oid string) error {
	if !objectIDPattern.MatchString(oid) {
		return fmt.Errorf("%q is not a full git commit id", oid)
	}
	return nil
}

// RollbackTarget is the build a rollback goes back to: the image on the
// image and Kubernetes pipelines, the commit on the source pipeline, and
// the release either one was stamped with, for the label.
type RollbackTarget struct {
	Image   string
	Release string
	Commit  string
}

// Empty reports whether t names nothing at all.
func (t RollbackTarget) Empty() bool {
	return t.Image == "" && t.Release == "" && t.Commit == ""
}

// Label is t in the short form the pane and the CLI print, beside a
// branch or image to tell it apart from (ShortImageRef).
func (t RollbackTarget) Label(branch string) string {
	return previousDisplayFromStatus(Status{
		Branch:          branch,
		PreviousImage:   t.Image,
		PreviousRelease: t.Release,
		PreviousCommit:  t.Commit,
	})
}

// Rollback is the build a rollback from s goes back to: what the last
// upgrade that cut over replaced. That is s's own Previous* when s cut
// over, and the Rollback* it carried forward from an earlier one when it
// did not -- cancelled, failed short of the cut-over, unchanged, or still
// running. Zero when no upgrade since this record began has cut over.
func (s Status) Rollback() RollbackTarget {
	if s.cutOver() {
		return RollbackTarget{
			Image:   strings.TrimSpace(s.PreviousImage),
			Release: strings.TrimSpace(s.PreviousRelease),
			Commit:  strings.TrimSpace(s.PreviousCommit),
		}
	}
	return RollbackTarget{
		Image:   strings.TrimSpace(s.RollbackImage),
		Release: strings.TrimSpace(s.RollbackRelease),
		Commit:  strings.TrimSpace(s.RollbackCommit),
	}
}

// cutOver reports whether the upgrade s records moved this deployment
// onto its new build: one that succeeded with a change, or one whose
// staging all succeeded and only the restart failed (progress.restartFailed)
// -- the ref file, binary or Deployment already names the new build then,
// so what it replaced is what there is to go back to.
func (s Status) cutOver() bool {
	if s.Phase == PhaseOK {
		return !s.Unchanged
	}
	if s.Phase != PhaseFailed {
		return false
	}
	for _, step := range s.Steps {
		if step.Name == stepRestart && step.State == StepFailed {
			return true
		}
	}
	return false
}

// RollbackTarget is what StartRollback would go back to from status, or
// why it would refuse: ErrNothingToRollBackTo when nothing is recorded,
// wrapped with the reason when what is recorded is not something a
// rollback may go to (an image not pinned by digest, a commit that is
// not a full object id). The pane and `grain upgrade status` offer a
// rollback only when this has none.
func (u *Upgrader) RollbackTarget(status Status) (RollbackTarget, error) {
	if u.cfg.Refusal != "" {
		return RollbackTarget{}, errors.New(u.cfg.Refusal)
	}
	t := status.Rollback()
	if u.Mode() != ModeSource {
		if t.Image == "" {
			return RollbackTarget{}, ErrNothingToRollBackTo
		}
		if _, pinned := kubeDigestRef(t.Image); !pinned {
			return RollbackTarget{}, fmt.Errorf("%w: the previous image %s is not pinned by digest", ErrNothingToRollBackTo, t.Image)
		}
		return t, nil
	}
	if t.Commit == "" {
		return RollbackTarget{}, ErrNothingToRollBackTo
	}
	if err := validateObjectID(t.Commit); err != nil {
		return RollbackTarget{}, fmt.Errorf("the recorded previous commit: %w", err)
	}
	return t, nil
}

// StartRollback is the one-click rollback ("Roll back to <previous>" on
// the upgrade pane, `grain upgrade rollback`): an upgrade back onto the
// build the last upgrade that cut over replaced, as StatusFile recorded
// it (Status.Rollback) -- not the build a cancelled or failed upgrade
// since then found running, which is the one running still. It takes no
// target, so a caller cannot name anything but what this deployment
// already ran.
//
// On the image and Kubernetes pipelines that is StartImage onto
// that image, which isRecordedRollbackImage admits past the
// allow-lists only when it is pinned by digest.
//
// On the source pipeline it is that commit, checked out by
// object id (checkoutCommit) rather than as a branch: Start fetches
// origin/<name>, so a rollback that named the old commit as a branch
// would build whatever somebody had pushed under that name. Because the
// commit is fetched by id and nothing else, this one commit is let past
// -upgrade-allowed-branches -- here and nowhere else. Start never makes
// that exception, for a 40-hex name or any other.
func (u *Upgrader) StartRollback() error {
	if u.cfg.Refusal != "" {
		return errors.New(u.cfg.Refusal)
	}
	if u.cfg.StatusFile == "" {
		return ErrNothingToRollBackTo
	}
	prev, err := readStatus(u.cfg.StatusFile)
	if err != nil {
		return fmt.Errorf("reading the last upgrade's status: %w", err)
	}
	t, err := u.RollbackTarget(prev)
	if err != nil {
		if !errors.Is(err, ErrNothingToRollBackTo) {
			log.Printf("upgrade: refusing a rollback: %v", err)
		}
		return err
	}
	if u.Mode() != ModeSource {
		return u.StartImage("", t.Image)
	}
	oid := t.Commit
	// The commit stands in for a branch in Status and the steps' log
	// lines; it is never handed to anything that would look it up as one.
	return u.startWithOptions(oid, "", nil, true, oid)
}

// rollbackCommit is the object id StartRollback asked the upgrade in
// flight to check out, or "" for an ordinary one.
func (u *Upgrader) rollbackCommit() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.commit
}

// checkoutCommit is checkout for a rollback by commit: it fetches oid
// from origin by object id and detaches the checkout onto it. Nothing
// here names a branch -- `git fetch origin <full oid>` asks for that
// object and does not match it against origin's refs, and a full object
// id given to rev-parse or switch is that object even where a ref of
// the same name exists -- so a branch somebody pushed under the old
// commit's name is never what gets built.
//
// What was fetched is checked before anything is built from it: the id
// has to resolve to a commit, that commit has to be oid itself, and the
// checkout's HEAD has to be oid once it has moved.
//
// A fetch that fails (a server that will not serve an object by id, say)
// falls back to the copy already in this checkout's object store, which
// is where the commit this deployment was last built from normally still
// is: an object store addresses objects by their content, so a commit
// found under oid there is that commit wherever it came from.
func (u *Upgrader) checkoutCommit(ctx context.Context, p *progress, oid string) error {
	if err := validateObjectID(oid); err != nil {
		return err
	}
	dirty, err := u.dirty(ctx)
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("%s has uncommitted changes -- refusing to overwrite them", u.cfg.SrcDir)
	}
	if ferr := u.git(ctx, p, "fetch", "--quiet", "--end-of-options", "origin", oid); ferr != nil {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if _, err := u.resolveCommit(ctx, oid); err != nil {
			return fmt.Errorf("fetching commit %s from origin: %w", oid, ferr)
		}
		p.logf("%s: fetching commit %s from origin failed (%v); using the copy already in %s", stepCheckout, oid, ferr, u.cfg.SrcDir)
	}
	got, err := u.resolveCommit(ctx, oid)
	if err != nil {
		return err
	}
	if got != oid {
		return fmt.Errorf("%s resolved to commit %s, not to itself -- refusing to build it", oid, got)
	}
	if err := u.git(ctx, p, "switch", "--quiet", "--detach", "--end-of-options", oid); err != nil {
		return err
	}
	// HEAD rather than oid: switch has just detached onto oid, and the
	// git this runs on may be older than reset's --end-of-options. That
	// HEAD is oid is checked below.
	if err := u.git(ctx, p, "reset", "--quiet", "--hard", "HEAD"); err != nil {
		return err
	}
	head, err := u.headRevision(ctx)
	if err != nil {
		return fmt.Errorf("reading the commit just checked out: %w", err)
	}
	if head != oid {
		return fmt.Errorf("the checkout is at %s after checking out %s -- refusing to build it", head, oid)
	}
	return nil
}

// resolveCommit is the commit oid names in the checkout's object store,
// with an error when there is none.
func (u *Upgrader) resolveCommit(ctx context.Context, oid string) (string, error) {
	cmd := newCommand(ctx, "", "git", "-C", u.cfg.SrcDir, "rev-parse", "--verify", "--quiet", "--end-of-options", oid+"^{commit}")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s is not a commit in %s: %w", oid, u.cfg.SrcDir, err)
	}
	return strings.TrimSpace(string(out)), nil
}
