package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// What each red stretch of a base branch held (model.BaseHoldPass,
// grain/task-986).
//
// The merge queue no longer holds a change in base_broken for a red base
// branch -- what to do about one is system:pipeline-steward's -- so the
// walk passes recordBaseHolds nothing held, and a pass records only how
// long the branch was red. The held counts stay in the schema for the
// passes recorded before that, and for a hold anything adds back.

// baseHoldKey is one base branch.
type baseHoldKey struct {
	repo   model.RepoRef
	branch string
}

// recordBaseHolds folds one cycle's base_broken counts into the hold
// history.
//
// A pass is open on a branch for as long as the branch is red
// (brokenBaseBranch) or the walk held something on it, and is closed by
// the first cycle that finds neither. Both, rather than only the red,
// because the two disagree for a cycle at each end: a change waiting on a
// base repair task (waitingOnRepairNote) can be held while the row is
// between runs, and a red found this cycle has held nothing yet. A pass
// that closes having held nothing is still recorded: a repair nobody was
// waiting on is an answer too.
func recordBaseHolds(ctx context.Context, store *model.Store, held map[baseHoldKey][]string,
	now time.Time) error {

	if store == nil {
		return nil
	}
	rows, err := store.ListBaseBranchCI(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading base branch CI: %w", err)
	}
	repairs := map[baseHoldKey]string{}
	live := map[baseHoldKey]bool{}
	for k := range held {
		live[k] = true
	}
	for i := range rows {
		row := &rows[i]
		k := baseHoldKey{repo: row.Repo, branch: row.Branch}
		if !row.Broken() {
			continue
		}
		if off, err := baseBranchCIOff(ctx, store, row.Repo); err != nil || off {
			if err != nil {
				return err
			}
			continue
		}
		live[k] = true
		repairs[k] = row.RepairTaskID
	}
	open, err := store.OpenBaseHoldPasses(ctx)
	if err != nil {
		return err
	}
	var errs []error
	seen := map[baseHoldKey]bool{}
	for _, p := range open {
		k := baseHoldKey{repo: p.Repo, branch: p.Branch}
		if seen[k] {
			// A second open pass for one branch cannot be written by this
			// function; close it rather than double-count.
			p.Finish(now)
			_, err := store.PutBaseHoldPass(ctx, p)
			errs = append(errs, err)
			continue
		}
		seen[k] = true
		if !live[k] {
			p.Finish(now)
			_, err := store.PutBaseHoldPass(ctx, p)
			errs = append(errs, err)
			continue
		}
		p.Sample(held[k], now)
		if r := repairs[k]; r != "" {
			p.RepairTaskID = r
		}
		_, err := store.PutBaseHoldPass(ctx, p)
		errs = append(errs, err)
	}
	// New passes in a stable order, so the ids say which began first.
	var fresh []baseHoldKey
	for k := range live {
		if !seen[k] {
			fresh = append(fresh, k)
		}
	}
	sort.Slice(fresh, func(i, j int) bool {
		if fresh[i].repo.String() != fresh[j].repo.String() {
			return fresh[i].repo.String() < fresh[j].repo.String()
		}
		return fresh[i].branch < fresh[j].branch
	})
	for _, k := range fresh {
		p := model.BaseHoldPass{Repo: k.repo, Branch: k.branch, RepairTaskID: repairs[k],
			StartedAt: now, SampledAt: now}
		p.Sample(held[k], now)
		_, err := store.PutBaseHoldPass(ctx, p)
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
