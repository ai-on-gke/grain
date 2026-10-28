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

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// fakeAddenda is a conversation a test types into: append posts what a
// person said, and Addenda answers the way the daemon's own
// /api/tasks/{id}/messages does -- everything after the cursor, in id
// order.
type fakeAddenda struct {
	mu       sync.Mutex
	next     int64
	comments []Addendum
	err      error
	// calls counts polls, for the tests about when one is made at all.
	calls int
	// blockFor is how long a poll stalls before answering, for the test
	// that a wedged daemon cannot stall the run.
	blockFor time.Duration
}

func (f *fakeAddenda) append(bodies ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, body := range bodies {
		f.next++
		f.comments = append(f.comments, Addendum{ID: f.next, Body: body})
	}
}

func (f *fakeAddenda) Addenda(ctx context.Context, after int64) ([]Addendum, error) {
	f.mu.Lock()
	f.calls++
	err, block := f.err, f.blockFor
	var out []Addendum
	for _, c := range f.comments {
		if c.ID > after {
			out = append(out, c)
		}
	}
	f.mu.Unlock()

	if block > 0 {
		select {
		case <-time.After(block):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (f *fakeAddenda) pollCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// addendaClient is a one-tool server whose task conversation the test
// holds: the tool answers a fixed sentence, and everything else these
// tests read is what the registry appended to it.
func addendaClient(t *testing.T, after int64, reader AddendaReader) *Client {
	t.Helper()
	registry := NewRegistry()
	registry.AnnounceAddenda(after, reader)
	registry.Register(Tool{
		Name:        "say",
		Description: "answers the same thing every time",
		InputSchema: map[string]any{"type": "object"},
		Handler:     func(context.Context, map[string]any) Result { return Result{Text: "the answer"} },
	})
	client := NewInProcess(context.Background(), registry)
	t.Cleanup(func() { client.Close() })
	return client
}

func callAddendaSay(t *testing.T, client *Client) string {
	t.Helper()
	res, err := client.CallTool(context.Background(), "say", nil)
	if err != nil {
		t.Fatal(err)
	}
	return res.Text()
}

// The ordinary turn of the ordinary run: nobody has said anything, and
// the answer is the tool's own, untouched. This is almost every result
// grain ever produces, so it is the one that has to stay exactly as it
// was.
func TestToolResultsCarryNothingWhenNobodyHasSaidAnything(t *testing.T) {
	conversation := &fakeAddenda{}
	client := addendaClient(t, 0, conversation)

	if got := callAddendaSay(t, client); got != "the answer" {
		t.Errorf("text = %q, want the tool's own answer untouched", got)
	}
}

// A server nobody told about a conversation -- pkg/mcp's own tests,
// tests/e2e, a `grain mcpserver` run by hand -- polls nothing and
// appends nothing.
func TestAServerWithNoReaderIsLeftExactlyAsItWas(t *testing.T) {
	registry := NewRegistry()
	registry.AnnounceAddenda(7, nil)
	registry.Register(Tool{
		Name:        "say",
		InputSchema: map[string]any{"type": "object"},
		Handler:     func(context.Context, map[string]any) Result { return Result{Text: "the answer"} },
	})
	client := NewInProcess(context.Background(), registry)
	t.Cleanup(func() { client.Close() })

	if got := callAddendaSay(t, client); got != "the answer" {
		t.Errorf("text = %q, want the tool's own answer untouched", got)
	}
}

// What the whole thing is for: a person types while the run is in
// flight, and the next answer the run asks for carries their words --
// quoted, attributed to a person, and after the tool's own output rather
// than in front of it.
func TestSomethingSaidMidRunRidesBackOnTheNextToolResult(t *testing.T) {
	conversation := &fakeAddenda{}
	client := addendaClient(t, 0, conversation)
	conversation.append("stop rewriting the parser, just fix the nil deref")

	got := callAddendaSay(t, client)
	if !strings.HasPrefix(got, "the answer\n\n") {
		t.Fatalf("text = %q, want the tool's own answer first, then the notice", got)
	}
	if !strings.Contains(got, addendaNoticeMarker) {
		t.Errorf("text = %q, want it to say a person added something", got)
	}
	if !strings.Contains(got, "> stop rewriting the parser, just fix the nil deref") {
		t.Errorf("text = %q, want the comment quoted", got)
	}
}

// Delivery is once. A comment repeated on every subsequent answer would
// spend the context it was posted to redirect, and would read to a model
// as somebody saying the same thing over and over.
func TestACommentIsDeliveredOnceAndNotOnEveryAnswerAfterIt(t *testing.T) {
	conversation := &fakeAddenda{}
	client := addendaClient(t, 0, conversation)
	conversation.append("use the existing helper")

	if got := callAddendaSay(t, client); !strings.Contains(got, "> use the existing helper") {
		t.Fatalf("first result = %q, want the comment delivered", got)
	}
	if got := callAddendaSay(t, client); got != "the answer" {
		t.Errorf("second result = %q, want the comment not repeated", got)
	}
}

// Everything already quoted in the run's prompt is behind the cursor the
// Framework passed (-addenda-after), so the first answer does not read
// the prompt's own conversation back to the run as breaking news.
func TestCommentsAlreadyInThePromptAreNotDeliveredAgain(t *testing.T) {
	conversation := &fakeAddenda{}
	conversation.append("the original request", "a clarification before dispatch")
	client := addendaClient(t, 2, conversation)

	if got := callAddendaSay(t, client); got != "the answer" {
		t.Fatalf("text = %q, want nothing delivered: both comments are in the prompt", got)
	}

	conversation.append("and one more thing, after the run started")
	if got := callAddendaSay(t, client); !strings.Contains(got, "> and one more thing, after the run started") {
		t.Errorf("text = %q, want only the comment posted after dispatch", got)
	}
}

// Several comments in one gap are one notice rather than one each:
// somebody typing three short lines has said one thing in three
// messages, the same reading cmd/grain's joinMessages makes of a chat
// turn.
func TestEverythingSaidSinceTheLastAnswerArrivesTogether(t *testing.T) {
	conversation := &fakeAddenda{}
	client := addendaClient(t, 0, conversation)
	conversation.append("actually", "target the release branch", "and leave the tests alone")

	got := callAddendaSay(t, client)
	for _, want := range []string{"> actually", "> target the release branch", "> and leave the tests alone"} {
		if !strings.Contains(got, want) {
			t.Errorf("text = %q, want it to carry %q", got, want)
		}
	}
	if n := strings.Count(got, addendaNoticeMarker); n != 1 {
		t.Errorf("notice appeared %d times, want one notice carrying all three", n)
	}
}

// A poll that fails delivers nothing and moves nothing, so the words it
// did not fetch are fetched by the next answer instead. The safe
// direction to be wrong in is the one where a person's correction is
// late, never the one where it is lost.
func TestAFailedPollDeliversNothingAndKeepsTheWordsForTheNextAnswer(t *testing.T) {
	conversation := &fakeAddenda{err: errors.New("daemon restarting")}
	client := addendaClient(t, 0, conversation)
	conversation.append("wrong branch")

	if got := callAddendaSay(t, client); got != "the answer" {
		t.Fatalf("text = %q, want the tool's own answer while the daemon is unreachable", got)
	}

	conversation.mu.Lock()
	conversation.err = nil
	conversation.mu.Unlock()
	if got := callAddendaSay(t, client); !strings.Contains(got, "> wrong branch") {
		t.Errorf("text = %q, want the comment once the daemon answers again", got)
	}
}

// A wedged daemon costs one addendaPollTimeout, not the run: the poll is
// bounded, and a bounded poll that answers nothing is a tool result that
// reads exactly as it did before any of this existed.
func TestAWedgedDaemonDoesNotStallTheRunPastThePollTimeout(t *testing.T) {
	restore := addendaPollTimeout
	addendaPollTimeout = 50 * time.Millisecond
	t.Cleanup(func() { addendaPollTimeout = restore })

	conversation := &fakeAddenda{blockFor: time.Hour}
	client := addendaClient(t, 0, conversation)
	conversation.append("anybody there")

	done := make(chan string, 1)
	go func() { done <- callAddendaSay(t, client) }()
	select {
	case got := <-done:
		if got != "the answer" {
			t.Errorf("text = %q, want the tool's own answer when the poll timed out", got)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("a tool call is still waiting on a wedged daemon well past the poll timeout")
	}
}

// A reader that answers with more than was asked for cannot put the same
// words in front of the run twice: the cursor is re-read after the
// request and anything at or below it is dropped.
func TestAReaderThatOveranswersCannotRepeatItself(t *testing.T) {
	conversation := &fakeAddenda{}
	client := addendaClient(t, 0, conversation)
	conversation.append("first")

	if got := callAddendaSay(t, client); !strings.Contains(got, "> first") {
		t.Fatalf("text = %q, want the comment delivered", got)
	}
	// The reader now answers from zero however it is asked, which is the
	// shape of a daemon that lost the cursor or a client that sent the
	// wrong one.
	conversation.append("second")
	got := callAddendaSay(t, client)
	if strings.Contains(got, "> first") {
		t.Errorf("text = %q, want the already-delivered comment dropped", got)
	}
	if !strings.Contains(got, "> second") {
		t.Errorf("text = %q, want the new comment delivered", got)
	}
}

// The notice is one paragraph however the person laid their text out.
// That is what keeps a command's own verdict inside the window
// RunCommandTimedOut reads from the end of a result.
func TestTheNoticeStaysOneParagraphThroughBlankLines(t *testing.T) {
	notice := addendaNotice([]Addendum{{ID: 1, Body: "first line\n\nsecond paragraph\n\n\nthird"}})
	if strings.Contains(notice, "\n\n") {
		t.Errorf("notice = %q, want no blank line in it", notice)
	}
	for _, want := range []string{"> first line", "> second paragraph", "> third"} {
		if !strings.Contains(notice, want) {
			t.Errorf("notice = %q, want it to carry %q", notice, want)
		}
	}
}

// A command that timed out is still countable as one with both notices
// appended to it -- the census reads the marker from the end of a result
// (orchestrator.census, mcp.RunCommandTimedOut), and a person's typing
// must not be able to push it out of view.
func TestATimedOutCommandIsStillRecognisableUnderBothNotices(t *testing.T) {
	text := withDeadlineNotice("some output\n\n"+timedOutText(t), runDeadlineNotice(3*time.Minute))
	text = withDeadlineNotice(text, addendaNotice([]Addendum{{ID: 1, Body: "never mind\n\nI fixed it"}}))
	if !RunCommandTimedOut(text) {
		t.Errorf("RunCommandTimedOut(%q) = false, want the killed marker still found", text)
	}
}

func timedOutText(t *testing.T) string {
	t.Helper()
	return runCommandKilledMarker + "30s by the tool's own bound. The command did not finish."
}

// Somebody who pastes a log into the conversation does not get to spend
// the run's context on it: the notice is cut, says it was cut in the
// same quoted voice, and stays one paragraph across the cut.
func TestAVeryLongCommentIsCutAndSaysSo(t *testing.T) {
	huge := strings.TrimSuffix(strings.Repeat("a line of pasted log output\n", 4000), "\n")
	notice := addendaNotice([]Addendum{{ID: 1, Body: huge}})

	if len(notice) > maxAddendaNoticeBytes()+200 {
		t.Errorf("notice is %d bytes, want it cut to about %d", len(notice), maxAddendaNoticeBytes())
	}
	if !strings.Contains(notice, "cut here") {
		t.Errorf("notice = %q..., want it to say it was cut", notice[:200])
	}
	if strings.Contains(notice, "\n\n") {
		t.Error("the cut notice has a blank line in it, want one paragraph")
	}
}

// A single pasted line longer than the whole budget still delivers what
// the person actually typed: the cut falls back to a rune boundary
// rather than back to the header, which would carry none of it.
func TestAVeryLongSingleLineCommentStillCarriesWhatWasSaid(t *testing.T) {
	huge := "please stop and " + strings.Repeat("x", 3*maxAddendaNoticeBytes())
	notice := addendaNotice([]Addendum{{ID: 1, Body: huge}})

	if !strings.Contains(notice, "> please stop and ") {
		t.Errorf("notice = %q..., want the head of what was typed", notice[:200])
	}
	if !utf8.ValidString(notice) {
		t.Error("the cut notice is not valid UTF-8")
	}
	if strings.Contains(notice, "\n\n") {
		t.Error("the cut notice has a blank line in it, want one paragraph")
	}
}

// A cut that lands inside a multi-byte character backs off to the
// character boundary: an answer carrying invalid UTF-8 is malformed
// rather than merely short.
func TestACutNeverSplitsACharacter(t *testing.T) {
	notice := addendaNotice([]Addendum{{ID: 1, Body: strings.Repeat("\u3053\u3093\u306b\u3061\u306f", maxAddendaNoticeBytes())}})
	if !utf8.ValidString(notice) {
		t.Error("the cut notice is not valid UTF-8")
	}
}

// Three short messages read as three things somebody said, not as one
// run-on block -- and still as one paragraph.
func TestSuccessiveCommentsAreSeparatedWithinTheOneParagraph(t *testing.T) {
	notice := addendaNotice([]Addendum{{ID: 1, Body: "actually"}, {ID: 2, Body: "use the release branch"}})
	if !strings.Contains(notice, "> actually\n>\n> use the release branch") {
		t.Errorf("notice = %q, want a quoted blank line between the two comments", notice)
	}
	if strings.Contains(notice, "\n\n") {
		t.Errorf("notice = %q, want one paragraph", notice)
	}
}

// A comment with no body at all -- somebody attaching a screenshot and
// typing nothing -- reads as a comment that was posted rather than as an
// empty quote the run has to guess at.
func TestAnEmptyCommentStillReadsAsSomethingSaid(t *testing.T) {
	notice := addendaNotice([]Addendum{{ID: 1, Body: "   \n  "}})
	if !strings.Contains(notice, "task's conversation") {
		t.Errorf("notice = %q, want it to point at the conversation", notice)
	}
	if strings.Contains(notice, "\n\n") {
		t.Errorf("notice = %q, want one paragraph", notice)
	}
}

// Nothing is polled for a tool call on a server that has a reader but a
// conversation nobody is typing into -- one poll per answer, not one per
// tool registered or one per retry.
func TestOnePollPerAnswer(t *testing.T) {
	conversation := &fakeAddenda{}
	client := addendaClient(t, 0, conversation)

	callAddendaSay(t, client)
	callAddendaSay(t, client)
	if got := conversation.pollCount(); got != 2 {
		t.Errorf("polls = %d, want one per tool answer", got)
	}
}

// ackingAddenda is a reader that also wants to know what went out
// (AddendaAcknowledger), recording every report.
type ackingAddenda struct {
	fakeAddenda
	acked []int64
}

func (a *ackingAddenda) Delivered(_ context.Context, through int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.acked = append(a.acked, through)
}

// What a person said is reported delivered once it has gone into an
// answer, with the id the cursor moved to -- the mark an interactive
// run's ending is judged by -- and a quiet answer reports nothing.
func TestDeliveredAddendaAreAcknowledged(t *testing.T) {
	conversation := &ackingAddenda{}
	client := addendaClient(t, 0, conversation)

	callAddendaSay(t, client)
	conversation.append("first", "second")
	if got := callAddendaSay(t, client); !strings.Contains(got, "> second") {
		t.Fatalf("result = %q, want the comments delivered", got)
	}
	callAddendaSay(t, client)

	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	if len(conversation.acked) != 1 || conversation.acked[0] != 2 {
		t.Errorf("acknowledged %v, want one report through comment 2", conversation.acked)
	}
}
