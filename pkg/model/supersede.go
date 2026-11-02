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

package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Superseding is closing a task in the one act that always has a
// replacement to point at: filing the task that replaces it
// (mcp's propose_task, its `supersedes` argument).
//
// It exists because refining a task means re-filing it. A chat session
// cannot edit a task's description, so five revisions of one design are
// five tasks -- grain/task-465 through 471 went that way in forty
// minutes -- and every revision left its predecessor queued and
// dispatchable. Two of them sat there long enough that an agent could
// have been dispatched onto a design already superseded twice, and the
// only thing that stopped it was an operator getting to the close button
// first, four times in one sitting. The supersede relation lived nowhere
// but in a sentence at the top of each body, so "why are there five
// near-identical tasks?" was answerable only by reading all five.
//
// Deliberately narrower than a close tool. A supersede with no
// replacement is not expressible, which is what makes it safe to hand to
// a run: there is always a successor on the record, and the pair of
// links (LinkSupersedes, LinkSupersededBy) says which. A general
// close_task -- for a proposal an operator made redundant by hand, or a
// duplicate filed by another task's run -- is real power and wants its
// own capability and its own decision.
//
// That decision was taken on 21 Sep 2026 (grain/task-528) and it was no.
// Every other writer of Observation.ClosedAt closes on a fact grain
// observed -- a human clicked, a pull request merged, a review finished
// its own work, a replacement was filed -- and a general close would be
// the first to close on an agent's judgement about a task it did not
// write. SupersedeRefusal bounds the damage; it cannot bound the
// judgement, and it cannot tell a duplicate from a queued task an
// operator meant. docs/closing-a-task-from-a-run.md has the whole of it,
// including what would have to be true to overturn it.
//
// SupersedeRefusal is the whole of the bound, and it is checked in both
// places a proposal is filed: live, while the run is still going
// (ui.Client.CreateTask, off a propose_task call relayed by the daemon),
// and in the end-of-run replay (orchestrator.relayProposedTasks).

// SupersedeRefusal reports why old may not be superseded by a
// replacement being filed against replacement now, in a sentence written
// to be read by whoever asked, or "" if it may.
//
// state and attempts are the two facts this cannot work out from a Task
// alone (Store.State, Store.Attempts); Store.SupersedeCandidate is the
// caller that has a database and looks them up.
//
// The bounds, and why each one:
//
//   - Never dispatched. A task with attempts behind it, a live run, or a
//     pull request open has work in it -- a sandbox spent, commits
//     pushed, possibly a branch -- and closing it throws that away.
//     Whether that is the right trade is a human's call, and this tool
//     is not how it gets made. Checked twice over, since the two
//     symptoms are separable in principle: any attempt at all, and any
//     state other than the three a never-dispatched task can be in.
//   - Only 'proposed', 'queued', or 'deferred'. Those are the states of
//     a task nothing has happened to yet (including a task scheduled via
//     not_before). Everything else -- running, failed, awaiting a reply,
//     awaiting a submit or an ack, completed, closed -- is either work
//     that happened or a task already finished, and neither is a run's
//     to overwrite. A task already closed is included: there is nothing
//     to supersede, and saying so is better than a second close nobody
//     asked for.
//   - The same repository. A propose_task call could have filed this
//     replacement anywhere the caller may file, and this closes a task
//     that already exists -- so the two are pinned together rather than
//     letting a proposal in one repository close work in another. A
//     replacement filed somewhere else is a different decision than the
//     one being made here, and it is a human's.
func SupersedeRefusal(old Task, state State, attempts int, replacement *RepoRef) string {
	if attempts > 0 {
		plural := "s"
		if attempts == 1 {
			plural = ""
		}
		return fmt.Sprintf("it has been dispatched already (%d attempt%s), and a task with a run "+
			"behind it is left for a human to close -- that is a sandbox spent and possibly a "+
			"branch pushed", attempts, plural)
	}
	if state != StateProposed && state != StateQueued && state != StateDeferred {
		return fmt.Sprintf("it is %s, and only a task still waiting to be picked up -- proposed, "+
			"queued, or deferred -- may be superseded this way", state)
	}
	for _, l := range old.Links {
		if l.Kind == LinkFixes {
			return fmt.Sprintf("it has %s open, and closing it would orphan that pull request", l.Target)
		}
	}
	switch {
	case old.Target == nil && replacement == nil:
	case old.Target == nil:
		return fmt.Sprintf("it is filed against no repository at all, and the replacement against "+
			"`%s` -- a supersede may only close a task in the repository it is filed in", replacement)
	case replacement == nil:
		return fmt.Sprintf("it is filed against `%s`, and the replacement against no repository at "+
			"all -- a supersede may only close a task in the repository it is filed in", old.Target)
	case !old.Target.Same(*replacement):
		return fmt.Sprintf("it is filed against `%s`, and the replacement against `%s` -- a "+
			"supersede may only close a task in the repository it is filed in", old.Target, replacement)
	}
	return ""
}

// SupersedeRefusedNote is what a human reads on the replacement when
// grain would not close the task it was filed to replace -- appended to
// its body, attributed to grain, the way unresolvedDependencyNote and
// unknownProposalRepoNote are (orchestrator.relayProposedTasks).
//
// A refused supersede does not refuse the proposal: the run asking for
// one has said something true and useful either way -- this work
// replaces that work -- and losing a whole proposal over an argument
// nobody would ever see is the worse of the two failures. What it costs
// is the close, which is exactly the decision being handed back.
func SupersedeRefusedNote(named, refusal string) string {
	return "\n\n---\n\n" + SupersedeRefusedLine(named, refusal)
}

// SupersedeRefusedLine is the same sentence without the body-appendix
// rule above it, for the one caller that says it in a comment instead: a
// supersede that passed the check before the proposal was filed and
// failed it after (Store.Supersede), which has a task to comment on by
// then and a body already written.
func SupersedeRefusedLine(named, refusal string) string {
	return fmt.Sprintf("grain: the run that proposed this asked for task %s to be closed "+
		"as superseded by it. It is still open: %s. Close it by hand if the run was right.",
		named, refusal)
}

// SupersedeComment is what the closed task's own conversation records --
// the close, who caused it, and what replaced it, so a task that
// disappeared from the backlog says why on its own thread rather than
// only in a link.
func SupersedeComment(replacementID, proposerID string) string {
	by := ""
	if proposerID != "" {
		by = fmt.Sprintf(", filed by task %s's own run", proposerID)
	}
	return fmt.Sprintf("Closed as superseded by task %s%s, which replaces it. This task had never "+
		"been dispatched, so no run, sandbox or branch was thrown away -- reopen it if the "+
		"replacement was a mistake.", replacementID, by)
}

// SupersedeCandidate is SupersedeRefusal for a caller holding a store:
// the task named, and why it may not be superseded by a replacement
// filed against replacement, or "" if it may.
//
// A name that resolves to no task at all is a refusal like any other
// rather than an error -- an agent naming a task that does not exist has
// still proposed real work, and the proposal is filed with the note
// (SupersedeRefusedNote) instead of being lost.
func (s *Store) SupersedeCandidate(ctx context.Context, id string, replacement *RepoRef) (*Task, string, error) {
	return supersedeCandidate(ctx, s.db, id, replacement)
}

// supersedeCandidate is SupersedeCandidate read through q: the store,
// or the transaction Store.Supersede is about to close the task in.
func supersedeCandidate(ctx context.Context, q querier, id string, replacement *RepoRef) (*Task, string, error) {
	id = NormalizeTaskID(id)
	old, err := getTask(ctx, q, id)
	if err != nil {
		return nil, "", err
	}
	if old == nil {
		return nil, "it names no task this deployment knows", nil
	}
	var state string
	err = q.QueryRowContext(ctx,
		"SELECT `state` FROM `task_state` WHERE `task_id` = ?", id).Scan(&state)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, "", err
	}
	var attempts int
	if err := q.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM `task_run` WHERE `task_id` = ?", id).Scan(&attempts); err != nil {
		return nil, "", err
	}
	return old, SupersedeRefusal(*old, State(state), attempts, replacement), nil
}

// SupersedeResult is what Store.Supersede did.
//
// Refusal is why it did nothing, in SupersedeRefusal's words, or "" if
// old was closed. Moved and Kept are about old's open dependents --
// every open task with a blocking link (LinkKind.Blocks) to old -- and
// are only ever filled in for a supersede that happened.
type SupersedeResult struct {
	Refusal string
	// Moved is the dependents whose blocking link now names the
	// replacement instead of old, in id order.
	Moved []string
	// Kept is the dependents left as they were, each with why: the
	// replacement itself, and any whose move would have closed a cycle.
	// Their link still names old, so closing old releases them -- the
	// rule a plain close has always had.
	Kept []KeptDependent
}

// KeptDependent is one open dependent of a superseded task that
// Store.Supersede did not move onto the replacement, and the reason.
type KeptDependent struct {
	ID     string
	Reason string
}

// Supersede closes old as replaced by replacement: the two links that
// make the relation a fact in the record (LinkSupersedes was written on
// replacement when it was filed; LinkSupersededBy goes on old here), the
// closure itself, and a comment on old saying who closed it and why.
//
// It also moves old's open dependents onto the replacement, because the
// replacement takes over old's job. A blocking link is satisfied by its
// target closing for any reason (IsBlocked, the task_blocked view), and
// a supersede is a close -- so before this, every task waiting on old was
// released the moment old was replaced, and dispatched beside the very
// task that was doing old's work (the director's supersedes of 22 Oct
// 2026). Each open task with a blocking link to old has that link
// retargeted to the replacement, keeping its kind, and a comment saying
// so. Two are left alone, with the reason in SupersedeResult.Kept: the
// replacement itself (a link to itself would never come unstuck), and a
// dependent the replacement already waits on, directly or through other
// tasks, since moving that one would make the two wait on each other for
// good. Closed tasks are not touched: they have nothing left to wait for.
// A plain close or cancel does none of this -- it is only a supersede
// that names a task to wait on instead.
//
// The bound is checked again here, against the store as it is now rather
// than as SupersedeCandidate found it a moment ago, and a refusal leaves
// old untouched and comes back in SupersedeResult.Refusal. Two writers
// file proposals and neither holds a lock over the dispatch loop, so a
// task that was queued when the replacement was written can be running
// by the time this runs -- which is the one outcome this whole path
// promises not to close over. The check, the close, the moves and every
// comment are one transaction, so a dependent is never released by the
// close without being moved by the same write.
//
// proposerID is the task whose propose_task call asked for it, recorded
// as the comments' author on behalf of grain -- the same attribution a
// proposal itself is filed under (ui.Client.CreateTask's origin).
func (s *Store) Supersede(ctx context.Context, oldID string, replacement Task, proposerID string,
	now time.Time) (SupersedeResult, error) {

	oldID = NormalizeTaskID(oldID)
	var result SupersedeResult
	err := s.write(ctx, "supersede task "+oldID, func(tx *sql.Tx) error {
		result = SupersedeResult{}
		old, refusal, err := supersedeCandidate(ctx, tx, oldID, replacement.Target)
		if err != nil {
			return err
		}
		if refusal != "" {
			result.Refusal = refusal
			return nil
		}
		linked := *old
		linked.Links = append(append([]Link{}, old.Links...),
			Link{Kind: LinkSupersededBy, Target: replacement.ID})
		if err := putTask(ctx, tx, linked); err != nil {
			return err
		}
		author := Attribution{Actor: GrainPrincipal}
		reason := "superseded by task " + replacement.ID
		if proposerID != "" {
			author.OnBehalfOf = &Principal{Kind: PrincipalAgent, ID: proposerID}
			reason += ", proposed by task " + proposerID
		}
		if err := closeInTx(ctx, tx, oldID, now, author.Actor, reason); err != nil {
			return err
		}
		// After the close, the way ui.Client.setClosed leaves its own
		// note: a comment saying the task is closed, in front of a close
		// that then failed, says something untrue.
		if _, err := addComment(ctx, tx, Comment{
			TaskID:    oldID,
			Author:    author,
			Body:      SupersedeComment(replacement.ID, proposerID),
			CreatedAt: now,
		}); err != nil {
			return err
		}
		return retargetDependents(ctx, tx, oldID, replacement.ID, author, now, &result)
	})
	return result, err
}

// closeInTx stamps Observation.ClosedAt on taskID inside tx and records
// who closed it and why, the way ObserveClose does on a transaction of
// its own.
func closeInTx(ctx context.Context, tx *sql.Tx, taskID string, now time.Time, by Principal, reason string) error {
	obs, err := getObservation(ctx, tx, taskID)
	if err != nil {
		return fmt.Errorf("reading observation for %s: %w", taskID, err)
	}
	if obs == nil {
		obs = &Observation{TaskID: taskID}
	}
	var before time.Time
	if obs.ClosedAt != nil {
		before = *obs.ClosedAt
	}
	obs.ClosedAt = &now
	obs.ObservedAt = &now
	if err := observe(ctx, tx, *obs); err != nil {
		return err
	}
	return recordCloseInTx(ctx, tx, taskID, before, obs.ClosedAt, by, reason)
}

// retargetDependents is Store.Supersede's second half: every open task
// with a blocking link to oldID gets it pointed at replacementID instead,
// apart from the ones SupersedeResult.Kept describes.
func retargetDependents(ctx context.Context, tx *sql.Tx, oldID, replacementID string,
	author Attribution, now time.Time, result *SupersedeResult) error {

	var dependents []string
	if err := each(ctx, tx,
		"SELECT DISTINCT `task_id` FROM `task_link` WHERE `target` = ? AND `kind` IN (?, ?) "+
			"ORDER BY `task_id`",
		[]any{oldID, string(LinkDependsOn), string(LinkChildOf)},
		func(rows *sql.Rows) error {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			dependents = append(dependents, id)
			return nil
		}); err != nil {
		return fmt.Errorf("listing the dependents of task %s: %w", oldID, err)
	}
	for _, id := range dependents {
		if id == oldID {
			continue
		}
		obs, err := getObservation(ctx, tx, id)
		if err != nil {
			return fmt.Errorf("reading observation for %s: %w", id, err)
		}
		if obs != nil && obs.ClosedAt != nil {
			continue
		}
		if id == replacementID {
			result.Kept = append(result.Kept, KeptDependent{ID: id, Reason: "it is the " +
				"replacement itself, and a task cannot wait on itself"})
			continue
		}
		cycle, err := blockingPathExists(ctx, tx, replacementID, id)
		if err != nil {
			return err
		}
		if cycle {
			reason := fmt.Sprintf("task %s already waits on it, so moving it would make the "+
				"two wait on each other", replacementID)
			result.Kept = append(result.Kept, KeptDependent{ID: id, Reason: reason})
			if _, err := addComment(ctx, tx, Comment{
				TaskID:    id,
				Author:    author,
				Body:      SupersedeKeptComment(oldID, replacementID, reason),
				CreatedAt: now,
			}); err != nil {
				return err
			}
			continue
		}
		dependent, err := getTask(ctx, tx, id)
		if err != nil {
			return err
		}
		if dependent == nil {
			continue
		}
		dependent.Links = retargetLinks(dependent.Links, oldID, replacementID)
		if err := putTask(ctx, tx, *dependent); err != nil {
			return err
		}
		if _, err := addComment(ctx, tx, Comment{
			TaskID:    id,
			Author:    author,
			Body:      SupersedeMovedComment(oldID, replacementID),
			CreatedAt: now,
		}); err != nil {
			return err
		}
		result.Moved = append(result.Moved, id)
	}
	return nil
}

// retargetLinks is links with every blocking link to from pointed at to
// instead, each keeping its kind, and without a duplicate where the task
// already had the same link to to.
func retargetLinks(links []Link, from, to string) []Link {
	out := make([]Link, 0, len(links))
	seen := map[Link]bool{}
	for _, l := range links {
		if l.Kind.Blocks() && l.Target == from {
			l.Target = to
		}
		if seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	return out
}

// blockingPathExists reports whether from waits on to through blocking
// links, directly or through any chain of other tasks. Closed tasks are
// walked through like open ones: a closed task can be reopened, and a
// cycle that comes back with it is one this would have made.
func blockingPathExists(ctx context.Context, tx *sql.Tx, from, to string) (bool, error) {
	seen := map[string]bool{from: true}
	frontier := []string{from}
	for len(frontier) > 0 {
		id := frontier[0]
		frontier = frontier[1:]
		var next []string
		if err := each(ctx, tx,
			"SELECT `target` FROM `task_link` WHERE `task_id` = ? AND `kind` IN (?, ?)",
			[]any{id, string(LinkDependsOn), string(LinkChildOf)},
			func(rows *sql.Rows) error {
				var target string
				if err := rows.Scan(&target); err != nil {
					return err
				}
				next = append(next, target)
				return nil
			}); err != nil {
			return false, fmt.Errorf("walking the dependencies of task %s: %w", id, err)
		}
		for _, target := range next {
			if target == to {
				return true, nil
			}
			if !seen[target] {
				seen[target] = true
				frontier = append(frontier, target)
			}
		}
	}
	return false, nil
}

// SupersedeMovedComment is what a dependent's own conversation records
// when Store.Supersede moves it onto the replacement of the task it was
// waiting on.
func SupersedeMovedComment(oldID, replacementID string) string {
	return fmt.Sprintf("Now waits on task %s, which superseded task %s.", replacementID, oldID)
}

// SupersedeKeptComment is what a dependent's own conversation records
// when Store.Supersede could not move it onto the replacement, and so
// left it to be released by the close.
func SupersedeKeptComment(oldID, replacementID, reason string) string {
	return fmt.Sprintf("Task %s, which this waited on, was superseded by task %s. This was not "+
		"moved to wait on task %s instead: %s. With task %s closed it no longer waits on it.",
		oldID, replacementID, replacementID, reason, oldID)
}
