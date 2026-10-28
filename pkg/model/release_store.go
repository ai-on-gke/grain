package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// releaseColumns' `repo` is the target repo's own name, kept apart from
// `name` -- the release's own user-given name ("myfeat", "2.1") -- since,
// unlike bwsalmon/agents#398's singleton ReleaseConfig, a release row is
// no longer 1:1 with a repo: `owner`+`repo` identifies the repo, `name`
// identifies which of that repo's releases this is.
const releaseColumns = "`id`,`owner`,`repo`,`name`,`status`,`created_at`,`merged_at`,`pull_request_url`,`last_error`,`merge_note`"

func scanRelease(scan func(...any) error) (Release, error) {
	var r Release
	var status string
	var mergedAt sql.NullTime
	var prURL, lastError, mergeNote sql.NullString
	if err := scan(&r.ID, &r.Repo.Owner, &r.Repo.Name, &r.Name, &status, &r.CreatedAt, &mergedAt,
		&prURL, &lastError, &mergeNote); err != nil {
		return Release{}, err
	}
	r.Status = ReleaseStatus(status)
	r.MergedAt = timePtr(mergedAt)
	r.PullRequestURL = prURL.String
	r.LastError = lastError.String
	r.MergeNote = mergeNote.String
	return r, nil
}

// GetRelease returns repo's current release by name -- the newest row
// ever created under that name, which is the live one unless every
// release by that name has already merged, in which case it is the most
// recently merged one -- or nil if repo has never had a release by that
// name at all. At most one row for a given (repo, name) can be anything
// but ReleaseMerged at once (CreateRelease's own ErrReleaseNameInUse), so
// "newest" and "the live one, or else the last merged one" agree.
func (s *Store) GetRelease(ctx context.Context, repo RepoRef, name string) (*Release, error) {
	return getRelease(ctx, s.db, repo, name)
}

func getRelease(ctx context.Context, q querier, repo RepoRef, name string) (*Release, error) {
	r, err := scanRelease(q.QueryRowContext(ctx,
		"SELECT "+releaseColumns+" FROM `release` WHERE `owner` = ? AND `repo` = ? AND `name` = ? "+
			"ORDER BY `id` DESC LIMIT 1",
		repo.Owner, repo.Name, name).Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading release %q for %s: %w", name, repo, err)
	}
	return &r, nil
}

// GetReleaseByID returns a release by its own id, or nil if none exists
// -- what the releases reconciler resolves a pending Candidate's own
// ReleaseID against, since it only ever has that id in hand, not the name
// a human last called it by.
func (s *Store) GetReleaseByID(ctx context.Context, id int64) (*Release, error) {
	r, err := scanRelease(s.db.QueryRowContext(ctx,
		"SELECT "+releaseColumns+" FROM `release` WHERE `id` = ?", id).Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading release %d: %w", id, err)
	}
	return &r, nil
}

// ListReleases returns every release ever created for repo, of any name,
// newest first -- what a repo's own release pane lists to let a human
// pick which one to act on next.
func (s *Store) ListReleases(ctx context.Context, repo RepoRef) ([]Release, error) {
	var out []Release
	err := each(ctx, s.db,
		"SELECT "+releaseColumns+" FROM `release` WHERE `owner` = ? AND `repo` = ? ORDER BY `id` DESC",
		[]any{repo.Owner, repo.Name},
		func(rows *sql.Rows) error {
			r, err := scanRelease(rows.Scan)
			if err != nil {
				return err
			}
			out = append(out, r)
			return nil
		})
	return out, err
}

// CreateRelease records a fresh release branch set for repo, named name
// -- the issue's own "create a new release branch." It also cuts that
// release's own first candidate (Number 1, Status CandidateCutting) in
// the same transaction, so a release is never left with LatestBranch
// provisioned but nothing yet cut from it: the releases reconciler's own
// ordering (releases before candidates, every cycle) then creates
// LatestBranch and this first candidate's own branch in the same cycle.
//
// name must satisfy ValidReleaseName (ErrInvalidReleaseName), and must
// not already name a release for repo that has not yet merged
// (ErrReleaseNameInUse) -- Release's own doc comment on why a name is
// exclusive until it merges.
func (s *Store) CreateRelease(ctx context.Context, repo RepoRef, name string, now time.Time) (Release, error) {
	if !ValidReleaseName(name) {
		return Release{}, ErrInvalidReleaseName
	}
	var out Release
	err := s.write(ctx, "create release "+name+" for "+repo.String(), func(tx *sql.Tx) error {
		existing, err := getRelease(ctx, tx, repo, name)
		if err != nil {
			return err
		}
		if existing != nil && existing.Status != ReleaseMerged {
			return ErrReleaseNameInUse
		}
		r := Release{Repo: repo, Name: name, Status: ReleaseProvisioning, CreatedAt: now}
		id, err := insertRelease(ctx, tx, r)
		if err != nil {
			return err
		}
		r.ID = id
		first := Candidate{
			Repo: repo, ReleaseID: id, Number: 1,
			Branch: RCBranch(name, 1), Status: CandidateCutting, CreatedAt: now,
		}
		if _, err := insertCandidate(ctx, tx, first); err != nil {
			return err
		}
		out = r
		return nil
	})
	return out, err
}

func insertRelease(ctx context.Context, tx *sql.Tx, r Release) (int64, error) {
	res, err := tx.ExecContext(ctx,
		"INSERT INTO `release` (`owner`,`repo`,`name`,`status`,`created_at`,`merged_at`,`pull_request_url`,`last_error`,`merge_note`) "+
			"VALUES (?,?,?,?,?,?,?,?,?)",
		r.Repo.Owner, r.Repo.Name, r.Name, string(r.Status), r.CreatedAt.UTC(), timeOf(r.MergedAt),
		nullable(r.PullRequestURL), nullable(r.LastError), nullable(r.MergeNote))
	if err != nil {
		return 0, fmt.Errorf("creating release %q for %s: %w", r.Name, r.Repo, err)
	}
	return res.LastInsertId()
}

func updateRelease(ctx context.Context, tx *sql.Tx, r Release) error {
	_, err := tx.ExecContext(ctx,
		"UPDATE `release` SET `status` = ?, `merged_at` = ?, `pull_request_url` = ?, `last_error` = ?, "+
			"`merge_note` = ? WHERE `id` = ?",
		string(r.Status), timeOf(r.MergedAt), nullable(r.PullRequestURL), nullable(r.LastError),
		nullable(r.MergeNote), r.ID)
	if err != nil {
		return fmt.Errorf("updating release %d: %w", r.ID, err)
	}
	return nil
}

// PendingReleases returns every release, across every repo, whose status
// names a GitHub-side effect the releases reconciler still owes it --
// ReleaseProvisioning or ReleaseMergeRequested -- oldest first.
func (s *Store) PendingReleases(ctx context.Context) ([]Release, error) {
	var out []Release
	err := each(ctx, s.db,
		"SELECT "+releaseColumns+" FROM `release` WHERE `status` IN (?,?) ORDER BY `id`",
		[]any{string(ReleaseProvisioning), string(ReleaseMergeRequested)},
		func(rows *sql.Rows) error {
			r, err := scanRelease(rows.Scan)
			if err != nil {
				return err
			}
			out = append(out, r)
			return nil
		})
	return out, err
}

// MarkReleaseProvisioned advances a ReleaseProvisioning row to
// ReleaseActive, clearing LastError -- the releases reconciler's report
// that LatestBranch is live on GitHub.
func (s *Store) MarkReleaseProvisioned(ctx context.Context, id int64) error {
	return s.write(ctx, "mark release provisioned", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE `release` SET `status` = ?, `last_error` = NULL WHERE `id` = ?",
			string(ReleaseActive), id)
		return err
	})
}

// MarkReleaseMerged advances a ReleaseMergeRequested row to ReleaseMerged,
// recording the merge-back pull request's own URL and clearing LastError
// -- the releases reconciler's report that the pull request is open. Its
// Name becomes free again for CreateRelease the moment this lands.
func (s *Store) MarkReleaseMerged(ctx context.Context, id int64, pullRequestURL string, now time.Time) error {
	return s.write(ctx, "mark release merged", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE `release` SET `status` = ?, `merged_at` = ?, `pull_request_url` = ?, `last_error` = NULL WHERE `id` = ?",
			string(ReleaseMerged), now.UTC(), pullRequestURL, id)
		return err
	})
}

// MarkReleaseNothingToMerge is the other way a ReleaseMergeRequested row
// reaches ReleaseMerged: there was no pull request to open, because the
// release's own ProdBranch carries no commits the default branch does not
// already have. note is the sentence saying so, for whoever reads the
// release afterwards; MergedAt is set and PullRequestURL left empty, and
// LastError is cleared, since nothing here failed.
//
// Merged rather than some terminal error status of its own: a release
// whose commits are already on the default branch is as merged back as it
// will ever be, and this is the status that says "grain is done with it"
// -- terminal (ReleaseMerged's own doc comment), out of PendingReleases,
// and freeing the Name for the next release in the same line, which a
// status meaning "stuck" would hold hostage with nothing a human could do
// about it. What is *not* the same as an ordinary merge is that no pull
// request exists, and MergeNote rather than a silently empty
// PullRequestURL is what keeps that from reading as a lost URL.
func (s *Store) MarkReleaseNothingToMerge(ctx context.Context, id int64, note string, now time.Time) error {
	return s.write(ctx, "mark release as having nothing to merge", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE `release` SET `status` = ?, `merged_at` = ?, `pull_request_url` = NULL, "+
				"`last_error` = NULL, `merge_note` = ? WHERE `id` = ?",
			string(ReleaseMerged), now.UTC(), note, id)
		return err
	})
}

// MarkReleaseError records why the releases reconciler's current step for
// a release has not landed yet, leaving Status unchanged -- the next
// cycle retries the same step.
func (s *Store) MarkReleaseError(ctx context.Context, id int64, message string) error {
	return s.write(ctx, "record release error", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE `release` SET `last_error` = ? WHERE `id` = ?", message, id)
		return err
	})
}

// RequestReleaseMerge requests that repo's release named name have its
// ProdBranch merged back into the repo's own default branch -- the
// issue's own "The prod branch can be merged back into the default
// branch when ready." release must be ReleaseActive (ErrReleaseNotActive
// for one still provisioning, ErrReleaseAlreadyMergeRequested for one
// already requested or already merged); this only records the request,
// the same declarative handoff CutCandidate and PromoteCandidate already
// make to the releases reconciler, which is what actually opens the pull
// request.
func (s *Store) RequestReleaseMerge(ctx context.Context, repo RepoRef, name string) (Release, error) {
	var out Release
	err := s.write(ctx, "request merge for release "+name+" of "+repo.String(), func(tx *sql.Tx) error {
		r, err := getRelease(ctx, tx, repo, name)
		if err != nil {
			return err
		}
		if r == nil {
			return ErrNoRelease
		}
		switch r.Status {
		case ReleaseActive:
		case ReleaseProvisioning:
			return ErrReleaseNotActive
		default:
			return ErrReleaseAlreadyMergeRequested
		}
		r.Status = ReleaseMergeRequested
		if err := updateRelease(ctx, tx, *r); err != nil {
			return err
		}
		out = *r
		return nil
	})
	return out, err
}

const candidateColumns = "`id`,`release_id`,`owner`,`repo`,`number`,`branch`,`status`,`created_at`,`promoted_at`,`last_error`"

func scanCandidate(scan func(...any) error) (Candidate, error) {
	var c Candidate
	var status string
	var lastError sql.NullString
	var promotedAt sql.NullTime
	if err := scan(&c.ID, &c.ReleaseID, &c.Repo.Owner, &c.Repo.Name, &c.Number,
		&c.Branch, &status, &c.CreatedAt, &promotedAt, &lastError); err != nil {
		return Candidate{}, err
	}
	c.CreatedAt = c.CreatedAt.UTC()
	c.Status = CandidateStatus(status)
	c.LastError = lastError.String
	c.PromotedAt = timePtr(promotedAt)
	return c, nil
}

// CurrentCandidateForRelease returns releaseID's own most recently cut
// candidate, whatever its status, or nil if none has ever been cut for
// it -- what a UI reads to render "the current rc" for one release.
func (s *Store) CurrentCandidateForRelease(ctx context.Context, releaseID int64) (*Candidate, error) {
	return currentCandidateForRelease(ctx, s.db, releaseID)
}

func currentCandidateForRelease(ctx context.Context, q querier, releaseID int64) (*Candidate, error) {
	c, err := scanCandidate(q.QueryRowContext(ctx,
		"SELECT "+candidateColumns+" FROM `release_candidate` "+
			"WHERE `release_id` = ? ORDER BY `id` DESC LIMIT 1", releaseID).Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading current candidate for release %d: %w", releaseID, err)
	}
	return &c, nil
}

// ListCandidates returns every candidate ever cut for repo's release
// named releaseName, newest first -- or nil if repo has no release by
// that name.
func (s *Store) ListCandidates(ctx context.Context, repo RepoRef, releaseName string) ([]Candidate, error) {
	release, err := s.GetRelease(ctx, repo, releaseName)
	if err != nil {
		return nil, err
	}
	if release == nil {
		return nil, nil
	}
	var out []Candidate
	err = each(ctx, s.db,
		"SELECT "+candidateColumns+" FROM `release_candidate` WHERE `release_id` = ? ORDER BY `id` DESC",
		[]any{release.ID},
		func(rows *sql.Rows) error {
			c, err := scanCandidate(rows.Scan)
			if err != nil {
				return err
			}
			out = append(out, c)
			return nil
		})
	return out, err
}

// CutCandidate allocates and records a fresh release candidate for
// repo's release named releaseName -- the issue's own "create a new rc",
// cut from that release's own LatestBranch. Number is one past the
// highest ever allocated within this release (never reused, never reset
// except by starting a whole new Release under the same name). Status
// starts at CandidateCutting -- the releases reconciler is what actually
// creates Branch on GitHub and advances it to CandidateActive.
//
// ErrNoRelease: repo has no release by that name. ErrReleaseNotActive:
// the release has not finished provisioning yet, or its merge back has
// already been requested. ErrCandidateActive: the release's own "current
// rc" is singular, and promoting is what retires one.
func (s *Store) CutCandidate(ctx context.Context, repo RepoRef, releaseName string, now time.Time) (Candidate, error) {
	var out Candidate
	err := s.write(ctx, "cut release candidate for "+repo.String()+" "+releaseName, func(tx *sql.Tx) error {
		release, err := getRelease(ctx, tx, repo, releaseName)
		if err != nil {
			return err
		}
		if release == nil {
			return ErrNoRelease
		}
		if release.Status != ReleaseActive {
			return ErrReleaseNotActive
		}
		current, err := currentCandidateForRelease(ctx, tx, release.ID)
		if err != nil {
			return err
		}
		if current != nil && current.Status != CandidatePromoted {
			return ErrCandidateActive
		}
		number := 1
		if current != nil {
			number = current.Number + 1
		}
		c := Candidate{
			Repo: repo, ReleaseID: release.ID, Number: number,
			Branch: RCBranch(releaseName, number), Status: CandidateCutting, CreatedAt: now,
		}
		id, err := insertCandidate(ctx, tx, c)
		if err != nil {
			return err
		}
		c.ID = id
		out = c
		return nil
	})
	return out, err
}

func insertCandidate(ctx context.Context, tx *sql.Tx, c Candidate) (int64, error) {
	res, err := tx.ExecContext(ctx,
		"INSERT INTO `release_candidate` "+
			"(`release_id`,`owner`,`repo`,`number`,`branch`,`status`,`created_at`,`promoted_at`,`last_error`) "+
			"VALUES (?,?,?,?,?,?,?,?,?)",
		c.ReleaseID, c.Repo.Owner, c.Repo.Name, c.Number,
		c.Branch, string(c.Status), c.CreatedAt.UTC(), timeOf(c.PromotedAt), nullable(c.LastError))
	if err != nil {
		return 0, fmt.Errorf("cutting a release candidate for %s: %w", c.Repo, err)
	}
	return res.LastInsertId()
}

// PromoteCandidate requests promotion of releaseName's current candidate
// to its release's own ProdBranch -- the issue's own "promote the current
// rc". The candidate must already be CandidateActive; this only advances
// Status to CandidatePromoting, the same declarative-intent handoff
// CutCandidate makes to the releases reconciler, which is what actually
// moves ProdBranch on GitHub, then advances Status to CandidatePromoted.
//
// ErrNoRelease: repo has no release by that name. ErrReleaseNotActive:
// the release has not finished provisioning yet, or its merge back has
// already been requested. ErrNoCandidate: the release has never had one
// cut. ErrCandidateNotReady: the current one is still cutting, or already
// mid-promotion. ErrAlreadyPromoted: "it cannot be promoted twice."
func (s *Store) PromoteCandidate(ctx context.Context, repo RepoRef, releaseName string) (Candidate, error) {
	return s.promoteCandidate(ctx, repo, releaseName, 0)
}

// PromoteCandidateIfCurrent is PromoteCandidate for a caller that has
// already decided which candidate it means -- the qualifications
// reconciler's auto-promotion, which promotes the candidate whose own
// qualification run succeeded. It returns ErrCandidateSuperseded when
// candidateID is no longer releaseName's current candidate, so that a
// candidate cut between the caller's read and this write is never
// promoted on the strength of another candidate's qualification. The
// check is made inside the same transaction as the write, so the two
// cannot interleave.
func (s *Store) PromoteCandidateIfCurrent(ctx context.Context, repo RepoRef, releaseName string, candidateID int64) (Candidate, error) {
	return s.promoteCandidate(ctx, repo, releaseName, candidateID)
}

// promoteCandidate is PromoteCandidate and PromoteCandidateIfCurrent
// both; an expectedID of 0 promotes whichever candidate is current.
func (s *Store) promoteCandidate(ctx context.Context, repo RepoRef, releaseName string, expectedID int64) (Candidate, error) {
	var out Candidate
	err := s.write(ctx, "promote release candidate for "+repo.String()+" "+releaseName, func(tx *sql.Tx) error {
		release, err := getRelease(ctx, tx, repo, releaseName)
		if err != nil {
			return err
		}
		if release == nil {
			return ErrNoRelease
		}
		if release.Status != ReleaseActive {
			return ErrReleaseNotActive
		}
		current, err := currentCandidateForRelease(ctx, tx, release.ID)
		if err != nil {
			return err
		}
		if current == nil {
			return ErrNoCandidate
		}
		if expectedID != 0 && current.ID != expectedID {
			return ErrCandidateSuperseded
		}
		switch current.Status {
		case CandidatePromoted:
			return ErrAlreadyPromoted
		case CandidateCutting, CandidatePromoting:
			return ErrCandidateNotReady
		}
		current.Status = CandidatePromoting
		if err := updateCandidate(ctx, tx, *current); err != nil {
			return err
		}
		out = *current
		return nil
	})
	return out, err
}

func updateCandidate(ctx context.Context, tx *sql.Tx, c Candidate) error {
	_, err := tx.ExecContext(ctx,
		"UPDATE `release_candidate` SET `status` = ?, `promoted_at` = ?, `last_error` = ? WHERE `id` = ?",
		string(c.Status), timeOf(c.PromotedAt), nullable(c.LastError), c.ID)
	if err != nil {
		return fmt.Errorf("updating release candidate %d: %w", c.ID, err)
	}
	return nil
}

// PendingCandidates returns every candidate, across every repo and
// release, whose status names a GitHub-side effect the releases
// reconciler still owes it -- CandidateCutting or CandidatePromoting --
// oldest first.
func (s *Store) PendingCandidates(ctx context.Context) ([]Candidate, error) {
	var out []Candidate
	err := each(ctx, s.db,
		"SELECT "+candidateColumns+" FROM `release_candidate` WHERE `status` IN (?,?) ORDER BY `id`",
		[]any{string(CandidateCutting), string(CandidatePromoting)},
		func(rows *sql.Rows) error {
			c, err := scanCandidate(rows.Scan)
			if err != nil {
				return err
			}
			out = append(out, c)
			return nil
		})
	return out, err
}

// MarkCandidateCut advances a CandidateCutting row to CandidateActive,
// clearing LastError -- the releases reconciler's report that the
// candidate's own branch is live on GitHub.
func (s *Store) MarkCandidateCut(ctx context.Context, id int64) error {
	return s.write(ctx, "mark release candidate cut", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE `release_candidate` SET `status` = ?, `last_error` = NULL WHERE `id` = ?",
			string(CandidateActive), id)
		return err
	})
}

// MarkCandidatePromoted advances a CandidatePromoting row to
// CandidatePromoted, stamping PromotedAt and clearing LastError -- the
// releases reconciler's report that ProdBranch landed on GitHub. This is
// the retirement "it cannot be promoted twice" depends on: PromoteCandidate
// refuses every status but CandidateActive, so a candidate that reaches
// this can never be promoted again.
func (s *Store) MarkCandidatePromoted(ctx context.Context, id int64, now time.Time) error {
	return s.write(ctx, "mark release candidate promoted", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE `release_candidate` SET `status` = ?, `promoted_at` = ?, `last_error` = NULL WHERE `id` = ?",
			string(CandidatePromoted), now.UTC(), id)
		return err
	})
}

// MarkCandidateError records why the releases reconciler's current step
// for a candidate has not landed yet, leaving Status unchanged -- the
// next cycle retries the same step.
func (s *Store) MarkCandidateError(ctx context.Context, id int64, message string) error {
	return s.write(ctx, "record release candidate error", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE `release_candidate` SET `last_error` = ? WHERE `id` = ?", message, id)
		return err
	})
}

// DeleteRelease removes repo's release named name and any candidates cut
// for it. If no such release exists, it returns ErrNoRelease.
func (s *Store) DeleteRelease(ctx context.Context, repo RepoRef, name string) error {
	return s.write(ctx, "delete release "+name+" of "+repo.String(), func(tx *sql.Tx) error {
		var ids []any
		if err := each(ctx, tx,
			"SELECT `id` FROM `release` WHERE `owner` = ? AND `repo` = ? AND `name` = ?",
			[]any{repo.Owner, repo.Name, name},
			func(rows *sql.Rows) error {
				var id int64
				if err := rows.Scan(&id); err != nil {
					return err
				}
				ids = append(ids, id)
				return nil
			}); err != nil {
			return err
		}
		if len(ids) == 0 {
			return ErrNoRelease
		}
		placeholders := strings.Repeat("?,", len(ids))
		placeholders = placeholders[:len(placeholders)-1]
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM `release_candidate` WHERE `release_id` IN ("+placeholders+")", ids...); err != nil {
			return fmt.Errorf("deleting candidates for release %q of %s: %w", name, repo, err)
		}
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM `release` WHERE `owner` = ? AND `repo` = ? AND `name` = ?",
			repo.Owner, repo.Name, name); err != nil {
			return fmt.Errorf("deleting release %q for %s: %w", name, repo, err)
		}
		return nil
	})
}
