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

// StartRollback is the one-click rollback ("Roll back to <previous>" on
// the upgrade pane, `grain upgrade rollback`): an upgrade back onto the
// build the last upgrade replaced, as StatusFile recorded it. It takes no
// target, so a caller cannot name anything but what this deployment
// already ran.
//
// On the image and Kubernetes pipelines that is StartImage onto
// Status.PreviousImage, which isRecordedRollbackImage admits past the
// allow-lists only when it is pinned by digest.
//
// On the source pipeline it is Status.PreviousCommit, checked out by
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
	if u.Mode() != ModeSource {
		image := strings.TrimSpace(prev.PreviousImage)
		if image == "" {
			return ErrNothingToRollBackTo
		}
		if !u.isRecordedRollbackImage(image) {
			return fmt.Errorf("%w: the previous image %s is not pinned by digest", ErrNothingToRollBackTo, image)
		}
		return u.StartImage("", image)
	}
	oid := strings.TrimSpace(prev.PreviousCommit)
	if oid == "" {
		return ErrNothingToRollBackTo
	}
	if err := validateObjectID(oid); err != nil {
		log.Printf("upgrade: refusing a rollback: %v", err)
		return fmt.Errorf("the recorded previous commit: %w", err)
	}
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
