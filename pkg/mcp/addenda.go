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

package mcp

// addenda.go is the delivery half of "a person can say something to a run
// that is already going".
//
// A task carries a conversation, and a person can already type into it
// while a run is in flight (ui.Client.AddComment). What happened to what
// they typed was nothing: the run's prompt was built at dispatch
// (orchestrator.commentThreadSection) and never read again, so a comment
// posted five minutes into a two-hour run waited for the run to finish
// and the task to be dispatched again before anything looked at it. By
// then the thing the person was trying to prevent had usually already
// been done.
//
// agent.RunConfig.Addenda is the seam that was meant to close this, and
// it cannot: it is polled between turns, and no Framework here has a
// "between turns" to poll at -- each runs one CLI as a blocking
// subprocess with the whole prompt written to stdin before the process
// starts. Nothing polls it and nothing ever has.
//
// So delivery goes the way the run-deadline notice already goes, and for
// the reason run_deadline.go states in full: a tool result is the one
// piece of text a run reads every single turn. This server polls the
// daemon for what a person has said since the run's prompt was built,
// and whatever it finds rides back on the next tool result the run asks
// for -- inside seconds on an ordinary turn, and on the far side of a
// fifteen-minute wait_for_checks for a run that is blocked, since the
// poll happens as the answer is composed rather than when the call
// arrived.
//
// Three properties worth stating rather than discovering:
//
//   - It works for every framework. What it needs of a Framework is that
//     it forks this process at all, which claude, codex, antigravity and
//     custom all do -- where the stdin route that would make an addendum
//     a real user turn is one CLI's flag, and a different one's, and a
//     third's silence.
//   - It is advice, not a turn. The words arrive appended to an answer
//     the run asked for, so a model mid-plan can read past them in a way
//     it cannot read past a user message. That is the price of the seam,
//     and it buys delivery to every framework and a change to none of
//     them.
//   - Delivery is once per process. The cursor advances when the words go
//     into a notice, so a person's comment is not repeated on every
//     subsequent answer eating the context it was meant to redirect.
//     Once per *process* rather than once per run, because a retry is a
//     second exec of the CLI (agent/claude's retry.go) and so a second
//     fork of this server, seeded from the same -addenda-after the first
//     one got: a comment delivered before a usage limit hit is delivered
//     again to the attempt that resumes the conversation. That is the
//     cheaper way round -- the resumed conversation already holds the
//     words, so the repeat costs a paragraph, where a cursor carried
//     across execs would have to survive a process that was killed.
//     Nothing is riding on delivery being lossless either: a comment this
//     run never acts on is still in the conversation, and the task's next
//     dispatch folds the whole thread into its prompt exactly as it
//     always has.

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// addendaPollTimeout bounds the one request a tool answer makes on its
// way back to the run.
//
// Short, and short on purpose. The daemon is on the same host as this
// process -- the poll is a loopback GET that is either answered at once
// or not going to be -- and what is on the other side of the trade is a
// run's own clock: this poll happens on every tool result, so a wedged
// daemon and a generous timeout would quietly spend a run's wall-clock
// budget waiting to be told nothing. A poll that times out delivers no
// addenda and advances no cursor, so the next answer carries the same
// words instead.
//
// A var, not a const, for maxToolResultBytes's reason: a test shrinks it
// rather than standing still for five seconds to prove that a wedged
// daemon does not stall a run.
var addendaPollTimeout = 5 * time.Second

// maxAddendaNoticeBytes caps how much of a person's typing one answer
// carries.
//
// A comment is a text box with no length on it, and the notice built
// from one rides on a tool result whose own budget is
// maxToolResultBytes. Somebody who pastes a stack trace, a diff or a log
// into the conversation should not be able to spend a run's context on
// it by accident -- so the notice is cut at a quarter of a capped result
// and says that it was cut, leaving the rest where it already is. The
// task's conversation holds every word; this is only what is read out to
// a run mid-flight.
//
// A quarter rather than a number of its own so that the two cannot drift:
// a deployment or a test that moves the result cap moves this with it,
// and the notice is never most of the answer it is appended to. Derived
// on each call for the same reason -- maxToolResultBytes is a var a test
// shrinks, and a package-level copy of it would be read before the test
// had.
func maxAddendaNoticeBytes() int { return int(maxToolResultBytes.Load()) / 4 }

// addendaNoticeMarker opens the notice, and is the string this package's
// own tests recognise it by. It is the "[grain] " prefix every notice
// here wears, for run_deadline.go's reason: what follows is grain
// speaking, not the tool's own output.
const addendaNoticeMarker = "[grain] A person added this to the task's conversation"

// Addendum is one thing a person said on this run's task after the run's
// prompt was built.
//
// ID is the comment's own id in the daemon's store, which is what makes
// delivery once rather than every turn: it is a cursor, not a display
// field, and the notice never shows it.
type Addendum struct {
	ID   int64
	Body string
}

// AddendaReader is whoever can say what a person has typed on this run's
// task since the comment named by after.
//
// It names no run for the reason WaitCreditReporter and StatusReporter
// name none: which task a poll belongs to is fixed at process start
// (`grain mcpserver -task`), never by anything in a tool call, so there
// is nothing here for a run to point somewhere else.
//
// Human-authored comments only. Grain's own automation posts on a task
// for reasons that have nothing to do with the run reading this -- a
// park, a merge-queue verdict, a CI notice -- and none of them is
// somebody asking the run to do something differently. The same rule
// ui.Client.MessagesSince already states for a chat turn, and the same
// implementation answers both.
type AddendaReader interface {
	Addenda(ctx context.Context, after int64) ([]Addendum, error)
}

// AddendaAcknowledger is an AddendaReader that also wants to be told
// once a poll's words have actually gone into an answer, so that the
// daemon can record them as delivered (model.Observation.
// DeliveredCommentID). Optional: a reader that does not implement it is
// polled exactly as before.
//
// It is told after the cursor has moved, with the id the cursor moved
// to. That is what "this run has now been shown everything up to here"
// means, and it is the one thing an interactive run's ending is judged
// by: a person's comment after that mark is one no run has seen, and
// the task is dispatched again for it rather than parked.
type AddendaAcknowledger interface {
	Delivered(ctx context.Context, through int64)
}

// runAddenda is the cursor a Registry polls from and the reader it polls.
//
// mu guards it for the reason runDeadline's own does: a Registry answers
// one call at a time today, and that is an accident of the transport
// rather than anything this depends on.
type runAddenda struct {
	reader AddendaReader

	mu      sync.Mutex
	after   int64
	unacked int64
}

// AnnounceAddenda tells r to carry what a person says on this run's task
// back to the run on its next tool result, and who to ask for it.
//
// after is the last comment already folded into the run's prompt
// (orchestrator.commentThreadSection's own reading of the thread,
// carried here through the forked mcpserver's -addenda-after flag and
// agent.AddendaArgs). It is the seed rather than a starting point of
// zero because everything up to it is already in front of the run: a
// server that seeded itself would open the run by reading the prompt's
// own conversation back to it as breaking news.
//
// A nil reader is a server with no daemon to ask -- pkg/mcp's own tests,
// tests/e2e, a `grain mcpserver` run by hand -- and leaves every tool
// result exactly as it was.
func (r *Registry) AnnounceAddenda(after int64, reader AddendaReader) {
	if reader == nil {
		return
	}
	r.addenda = &runAddenda{reader: reader, after: after}
}

// ackPendingAddenda reports any addenda appended to a prior tool result as
// delivered once the agent makes a subsequent tool call, proving the turn did
// not exit immediately on the tool call that carried them. If a comment is
// appended to the final tool call of a run and the agent exits without making
// another tool call, it remains unacknowledged so ProcessResult re-queues the
// task to answer it.
func (r *Registry) ackPendingAddenda(ctx context.Context) {
	if r.addenda == nil {
		return
	}
	r.addenda.ackPending(ctx)
}

func (a *runAddenda) ackPending(ctx context.Context) {
	a.mu.Lock()
	through := a.unacked
	a.unacked = 0
	a.mu.Unlock()
	if through <= 0 {
		return
	}
	if ack, ok := a.reader.(AddendaAcknowledger); ok {
		ackCtx, cancel := context.WithTimeout(ctx, addendaPollTimeout)
		defer cancel()
		ack.Delivered(ackCtx, through)
	}
}

// addendaNotice is what a person has said since the last answer, worded
// for the run, or "" when nobody has said anything -- which is every
// turn of almost every run.
func (r *Registry) addendaNotice(ctx context.Context) string {
	if r.addenda == nil {
		return ""
	}
	return addendaNotice(r.addenda.poll(ctx))
}

// poll asks the reader what has been said since the cursor and advances
// the cursor over whatever it returns.
//
// A failed poll -- a timeout, a daemon restarting, the run's own ctx
// already cancelled -- delivers nothing and moves nothing, so the words
// it did not fetch are fetched again by the next answer. That is the
// safe direction to be wrong in: the failure this exists to prevent is a
// person's correction going unread, and a poll that advanced its cursor
// on an error would turn one dropped request into exactly that.
//
// The cursor is re-read under the lock after the request rather than
// trusted from before it, and entries at or below it are dropped: a
// reader that answers with more than was asked for, or two polls that
// overlap, must not be able to put the same words in front of the run
// twice.
func (a *runAddenda) poll(ctx context.Context) []Addendum {
	a.mu.Lock()
	after := a.after
	a.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, addendaPollTimeout)
	defer cancel()
	found, err := a.reader.Addenda(ctx, after)
	if err != nil {
		return nil
	}

	a.mu.Lock()
	var fresh []Addendum
	for _, addendum := range found {
		if addendum.ID <= a.after {
			continue
		}
		a.after = addendum.ID
		fresh = append(fresh, addendum)
	}
	if len(fresh) > 0 && a.after > a.unacked {
		a.unacked = a.after
	}
	a.mu.Unlock()
	return fresh
}

// addendaNotice words what a person said for a run that is in the middle
// of something else.
//
// Three things the wording has to do, none of them decoration. It has to
// say the words are a person's rather than grain's, because a run acts
// on the two differently and the whole point of the interruption is that
// somebody has changed their mind about the work. It has to say the run
// will not be shown them again, because a model that assumes it can come
// back to this will come back to nothing. And it has to quote, so that
// text somebody pasted -- which may itself read like an instruction, a
// tool result or a system notice -- cannot be mistaken for grain
// speaking in its own voice.
//
// One paragraph, always: every line of every comment is prefixed, blank
// lines included, so the notice cannot split into two. That is what
// keeps the notices this package appends countable from the end of a
// result (telemetry.go's trailingParagraphs), and it is also the cheapest
// possible statement of where the person's words stop.
func addendaNotice(addenda []Addendum) string {
	if len(addenda) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(addendaNoticeMarker)
	b.WriteString(" while you were working on it. Read it and factor it in -- it may change " +
		"what you should be doing, and this is the only answer that will carry it. The " +
		"quoted text is a person's own words, not grain's:")
	for i, addendum := range addenda {
		// A bare ">" between comments rather than nothing, so that three
		// short messages read as three things somebody said rather than
		// as one run-on paragraph -- and a quoted empty line rather than
		// a real one, because a real one would end the paragraph.
		if i > 0 {
			b.WriteString("\n>")
		}
		b.WriteString("\n")
		b.WriteString(quoteAddendum(addendum.Body))
	}
	return truncateAddendaNotice(b.String())
}

// quoteAddendum renders one comment as a blockquote whose every line is
// prefixed, so that the notice stays one paragraph however the person
// laid their text out, and an empty body still reads as a comment that
// was posted rather than as nothing at all.
func quoteAddendum(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.TrimRight(body, "\n")
	if strings.TrimSpace(body) == "" {
		return "> (an empty comment, perhaps an attachment -- it is in the task's conversation)"
	}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if line == "" {
			lines[i] = ">"
			continue
		}
		lines[i] = "> " + line
	}
	return strings.Join(lines, "\n")
}

// truncateAddendaNotice cuts an over-long notice and says so in the same
// quoted voice the rest of it is in, so that the cut cannot end the
// paragraph early and cannot read as the person's own last word.
//
// The head is kept rather than the tail, which is the opposite of what
// result_size.go does for a command's output and right for the same
// reason: a command's verdict is in its last lines, and a person says
// what they want first.
//
// Two boundaries, in that order. A rune boundary always, because the
// budget is in bytes and a comment is text somebody typed: cutting a
// multi-byte character in half would put invalid UTF-8 into a JSON-RPC
// result, which is a malformed answer rather than a short one. Then a
// line boundary, but only one in the back half of what is left -- a
// single pasted line with no newline in it at all would otherwise cut
// back to the header and deliver none of what the person actually said,
// which is the one outcome worse than a cut mid-sentence.
func truncateAddendaNotice(notice string) string {
	limit := maxAddendaNoticeBytes()
	if len(notice) <= limit {
		return notice
	}
	cut := notice[:limit]
	for len(cut) > 0 {
		r, size := utf8.DecodeLastRuneInString(cut)
		if r != utf8.RuneError || size > 1 {
			break
		}
		cut = cut[:len(cut)-1]
	}
	if at := strings.LastIndex(cut, "\n"); at > limit/2 {
		cut = cut[:at]
	}
	return cut + "\n> [grain] ...cut here. The rest of what was said is in the task's conversation."
}
