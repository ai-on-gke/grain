package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// An orphaned pull request is one grain has stopped watching: its task
// was closed while the pull request was still open, so nothing here will
// ever merge it, sync it or read it again.
//
// That is a deliberate outcome, not a bug -- nobody wants a closed task's
// work merged, and the commits on the branch are real work whichever way
// the human went on the task. What was missing was anybody being *told*.
// The pull request sits open on GitHub with no sign that grain has looked
// away, and the task it belongs to is closed, so nobody is reading there
// either; a human who closes a task does not necessarily know they have
// just orphaned a pull request.
//
// The note below is that telling, and it is said in both places, because
// they are two different readers rather than the same one twice: on the
// task, for whoever just closed it, and on the pull request itself, for
// whoever finds it open weeks later and has no reason to look at a closed
// task at all. Store.NoteOrphanedPullRequest writes the task's copy;
// posting the other copy needs a GitHub client, so it belongs to the
// callers that have one (pkg/orchestrator on the finish path,
// pkg/cli/daemon.go's adapter for a close arriving over the UI/API).
//
// Leaving it open is not the only ending any more: a human closing a task
// can ask, in the same breath, for its pull request to be closed too
// (ui.CloseOptions.ClosePullRequest, a checkbox beside the Close button).
// That is the one thing in grain that throws work away on GitHub, so it
// happens only on that explicit ask, never by grain's own decision -- and
// when it does happen, the same two readers are told the same way, in the
// second form of the words below.

// PullRequestCloseOutcome is what grain did to the pull request itself
// when its task was closed, and it is what picks the note's words.
//
// The zero value -- nobody asked, so nothing was done -- is the ordinary
// case and the only one pkg/orchestrator's finish path ever has: a run
// discovering that its task was closed underneath it has no human in
// front of it to have asked for anything.
type PullRequestCloseOutcome struct {
	// Requested is whether the human who closed the task asked for the
	// pull request to be closed with it.
	Requested bool
	// Err is what GitHub said if it refused, and nil if it did not. A
	// refused close is not a failed close of the *task*: the task is
	// closed either way, the pull request is left open exactly as though
	// nobody had asked, and the refusal is said in the note rather than
	// returned to the caller.
	Err error
}

// closed reports whether the pull request really was closed -- asked for
// and not refused. The two fields are kept apart rather than collapsed
// into one bool because a refusal is its own outcome with its own words:
// the pull request is still open, so the note is the orphan one, plus a
// line saying grain was asked to close it and could not.
func (o PullRequestCloseOutcome) closed() bool { return o.Requested && o.Err == nil }

// OrphanedPullRequestNote is what grain says about a pull request whose
// task has been closed, in full -- either that it has stopped watching
// it, or that it closed it, depending on outcome.
//
// echo carries what happened to the copy posted on the pull request
// itself: nil means it landed, non-nil means it did not and says why.
// Folding that into the words rather than dropping it is what keeps a
// failed GitHub call from costing a close its record -- the task's own
// copy is written last, after the attempt, and a deployment whose UI is
// not wired to a GitHub client at all is one legitimate reason for a
// non-nil echo, not an error worth failing a close over.
//
// Deliberately not a template with the two halves apart: the note posted
// on the pull request is this same text without the echo line, so a
// reader who lands on either copy reads the same explanation.
func OrphanedPullRequestNote(taskID string, ref PullRequestRef, outcome PullRequestCloseOutcome, echo error) string {
	var b strings.Builder
	b.WriteString(orphanedNoteLead(ref, outcome))
	if outcome.closed() {
		fmt.Fprintf(&b, "\n\nTask %s is closed, and whoever closed it asked for this pull "+
			"request to be closed with it -- so grain closed it rather than leaving it open "+
			"with nothing watching it.\n\n"+
			"Nothing has been done to the branch: closing a pull request throws away none of "+
			"the commits on it, and reopening %s on GitHub brings it back exactly as it was. "+
			"Reopening task %s is what puts it back under grain's watch.",
			taskID, ref, taskID)
	} else {
		fmt.Fprintf(&b, "\n\nTask %s is closed, and grain only ever merges a pull request "+
			"belonging to a task that completed -- so it will not merge this one, will not "+
			"update it, and will not look at it again.\n\n", taskID)
		if outcome.Err != nil {
			fmt.Fprintf(&b, "Whoever closed task %s asked for this pull request to be closed "+
				"with it, and GitHub refused: %v. ", taskID, outcome.Err)
		} else {
			b.WriteString("Nothing has been done to the branch: the commits are real work, " +
				"and grain closes a pull request only when whoever closes its task asks for " +
				"that in the same breath. ")
		}
		fmt.Fprintf(&b, "%s is still open for a human to merge or close by hand, and "+
			"reopening task %s is what puts it back under grain's watch.", ref, taskID)
	}
	if echo != nil {
		fmt.Fprintf(&b, "\n\n(grain could not leave this note on the pull request itself: %v)", echo)
	} else {
		b.WriteString("\n\n(grain has left this note on the pull request itself too.)")
	}
	return b.String()
}

// OrphanedPullRequestComment is the copy posted on the pull request --
// the same words, minus the line about posting it there.
func OrphanedPullRequestComment(taskID string, ref PullRequestRef, outcome PullRequestCloseOutcome) string {
	body := OrphanedPullRequestNote(taskID, ref, outcome, nil)
	return strings.TrimSuffix(body, "\n\n(grain has left this note on the pull request itself too.)")
}

// orphanedNoteLead is the note's own first line, and the key both store
// methods below dedupe on. It names the pull request and nothing that can
// vary between two writes of the same note (no timestamps, no echo
// outcome), so "has this already been said about this pull request?" is a
// prefix comparison rather than a whole-body match -- see
// OrphanedPullRequestNoted.
//
// The two forms have two different leads on purpose. They are different
// facts, and a task closed twice (closed, reopened, closed again) can
// legitimately produce one of each: the first close left the pull request
// open, the second asked for it to be closed. Deduping them together
// would swallow the note about the close that actually threw the work
// away, which is the one nobody can afford to miss.
func orphanedNoteLead(ref PullRequestRef, outcome PullRequestCloseOutcome) string {
	if outcome.closed() {
		return "grain has closed " + ref.String() + "."
	}
	return "grain has stopped watching " + ref.String() + "."
}

// noteLead reads the lead back off a note either of the builders above
// wrote: its first line, up to the blank one that ends it.
func noteLead(body string) string {
	lead, _, _ := strings.Cut(body, "\n\n")
	return lead
}

// OrphanedPullRequestNoted reports whether the note this outcome would
// leave has already been left on taskID for ref.
//
// Its purpose is not only to keep the conversation free of duplicates: it
// is also what stops a second copy going onto the pull request on GitHub,
// which is why callers ask this *before* posting there rather than
// relying on NoteOrphanedPullRequest's own write-time check. A task can
// be closed, reopened and closed again, and each of those closes runs the
// same path.
//
// It answers yes to two different notes: the one outcome would write, and
// -- whatever outcome is -- the one saying grain closed this pull
// request. The second is what keeps a plain close arriving after a close
// that really did close the pull request from announcing that grain has
// "stopped watching" something it shut. Once grain has said it closed a
// pull request, it has nothing further to say about it.
//
// Only outcome's own two fields matter here, never the echo: whether the
// copy on GitHub landed is not part of which note this is.
func (s *Store) OrphanedPullRequestNoted(ctx context.Context, taskID string,
	ref PullRequestRef, outcome PullRequestCloseOutcome) (bool, error) {

	noted, err := orphanedNoteExists(ctx, s.db.QueryRowContext, taskID, ref, orphanedNoteLead(ref, outcome))
	if err != nil {
		return false, fmt.Errorf("reading whether task %s was told about %s: %w", taskID, ref, err)
	}
	return noted, nil
}

// NoteOrphanedPullRequest leaves body on taskID as grain's own comment,
// unless the same note is already there.
//
// The check is repeated inside the write transaction rather than trusted
// from OrphanedPullRequestNoted above: two closes racing each other (a
// human's, and a run's finish path discovering the same close) would
// otherwise both read "not noted" and both write.
//
// Which of the two notes body is, it reads off body itself -- its own
// first line is the lead -- rather than being told a second time by the
// caller. There is then no way for the check to be asking about one note
// while the write lands another.
//
// Attributed to grain itself, with no OnBehalfOf -- unlike
// orchestrator.relayComment, whose words are an agent's and are only
// carried by grain. These words are grain's own.
func (s *Store) NoteOrphanedPullRequest(ctx context.Context, taskID string,
	ref PullRequestRef, body string, now time.Time) error {

	return s.write(ctx, "note the orphaned pull request on task "+taskID, func(tx *sql.Tx) error {
		noted, err := orphanedNoteExists(ctx, tx.QueryRowContext, taskID, ref, noteLead(body))
		if err != nil {
			return fmt.Errorf("reading whether task %s was told about %s: %w", taskID, ref, err)
		}
		if noted {
			return nil
		}
		_, err = addComment(ctx, tx, Comment{
			TaskID:    taskID,
			Author:    Attribution{Actor: Principal{Kind: PrincipalAutomation, ID: "grain"}},
			Body:      body,
			CreatedAt: now,
		})
		return err
	})
}

// orphanedNoteExists is the prefix comparison both of the above make,
// against the database handle or the transaction the caller is already
// holding.
//
// substr rather than LIKE: a GitHub repo name can contain '_', which LIKE
// would read as a wildcard.
func orphanedNoteExists(ctx context.Context,
	queryRow func(context.Context, string, ...any) *sql.Row,
	taskID string, ref PullRequestRef, lead string) (bool, error) {

	// The closed form always counts as already said, whichever form is
	// being asked about -- see OrphanedPullRequestNoted. When outcome is
	// itself the closed one the two are the same string, and the second
	// comparison is simply redundant rather than wrong.
	closedLead := orphanedNoteLead(ref, PullRequestCloseOutcome{Requested: true})
	var one int
	err := queryRow(ctx, "SELECT 1 FROM `task_comment` WHERE `task_id` = ? "+
		"AND (substr(`body`, 1, ?) = ? OR substr(`body`, 1, ?) = ?) LIMIT 1",
		taskID, len(lead), lead, len(closedLead), closedLead).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
