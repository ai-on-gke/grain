package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// SyncQualifications is bwsalmon/agents#518's own scheduling step: the
// moment a candidate's own branch first goes live on GitHub (releases.
// go's own cutOnGitHub advancing it to CandidateActive), any
// qualification plan configured for its repo is instantiated as real
// tasks targeting that branch, and a run that has since succeeded is
// promoted automatically if its plan's own AutoPromote says so.
//
// Level-triggered like every other reconciler here: QualifiableActive
// Candidates only ever names a candidate this cycle still has something
// to do for (create a run, or check one already running), so a candidate
// with neither a plan nor a run is never even read, and one this cycle
// cannot yet promote is simply read again next cycle once its tasks have
// moved on. Unlike SyncReleases, this never talks to GitHub itself --
// everything it acts on and everything it decides lives in the store --
// so it takes a *model.Store directly rather than a whole Deps, the same
// shape SyncPullRequests and SyncReleases already have.
func SyncQualifications(ctx context.Context, store *model.Store, now time.Time) error {
	candidates, err := store.QualifiableActiveCandidates(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: listing qualifiable candidates: %w", err)
	}
	var errs []error
	for _, c := range candidates {
		if err := syncQualification(ctx, store, c, now); err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: qualifying candidate %d: %w", c.ID, err))
		}
	}
	return errors.Join(errs...)
}

// syncQualification advances one candidate's own qualification by
// exactly the step it is missing: a run, if its repo has a plan and none
// exists yet, or a promotion, if one does exist, has succeeded, and its
// plan says to promote automatically.
func syncQualification(ctx context.Context, store *model.Store, c model.Candidate, now time.Time) error {
	run, err := store.CandidateQualificationRun(ctx, c.ID)
	if err != nil {
		return fmt.Errorf("reading qualification run: %w", err)
	}
	plan, err := store.GetQualificationPlan(ctx, c.Repo)
	if err != nil {
		return fmt.Errorf("reading qualification plan: %w", err)
	}
	release, err := store.GetReleaseByID(ctx, c.ReleaseID)
	if err != nil {
		return fmt.Errorf("reading release %d: %w", c.ReleaseID, err)
	}
	if release == nil {
		return nil
	}

	if run == nil {
		if plan == nil || len(plan.Items) == 0 {
			return nil
		}
		if _, err := store.CreateQualificationRun(ctx, c, *plan, now); err != nil {
			return fmt.Errorf("creating qualification run: %w", err)
		}
		return nil
	}

	if plan == nil || !plan.AutoPromote {
		return nil
	}
	if model.QualificationStatus(run.Tasks) != model.QualificationSucceeded {
		return nil
	}

	// Promote c by its own ID rather than whichever candidate is current
	// by release name: c was current when it was read, but a person may
	// have promoted it and cut a newer one since, and that one must not
	// be promoted on the strength of c's qualification.
	if _, err := store.PromoteCandidateIfCurrent(ctx, c.Repo, release.Name, c.ID); err != nil {
		// ErrCandidateSuperseded/ErrCandidateActive/ErrCandidateNotReady/
		// ErrAlreadyPromoted/ErrReleaseNotActive all mean a human (or an
		// earlier cycle) already acted on this same candidate or release
		// between the read above and this write -- benign, and left for the
		// next cycle to see the outcome of rather than reported as a
		// failure of this one.
		if errors.Is(err, model.ErrCandidateSuperseded) || errors.Is(err, model.ErrCandidateActive) ||
			errors.Is(err, model.ErrCandidateNotReady) || errors.Is(err, model.ErrAlreadyPromoted) ||
			errors.Is(err, model.ErrReleaseNotActive) || errors.Is(err, model.ErrNoRelease) {
			return nil
		}
		return fmt.Errorf("auto-promoting candidate %d: %w", c.ID, err)
	}
	return nil
}
