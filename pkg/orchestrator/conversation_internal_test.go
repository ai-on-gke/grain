package orchestrator

// What the bound on a task's conversation has to hold to: a thread that
// fits is untouched, one that does not keeps both of its ends, says at
// the cut that it was cut, and comes out inside the budget whatever was
// said on the task.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// noticeSlack is what commentThreadSection writes around the messages
// themselves -- its heading and the one elision notice -- which the
// budget bounds the messages rather than the section by. A kilobyte
// covers both several times over; the assertions want "the section is
// bounded", not "the section is bounded to the byte".
const noticeSlack = 2 << 10

// renderedOverhead is what commentThreadSection adds around one message's
// body to make the line it carries: "- a human (alice): " and the newline. A test
// sizing a message against the budget has to know it, since the budget is
// spent in rendered lines rather than in bodies.
const renderedOverhead = len("- a human (alice): \n")

// handoverComment is what a run's write_handover call becomes in the
// store (orchestrator.relayHandover): a comment like any other, relayed
// on the agent's behalf, with the one bit that says which comment it is.
func handoverComment(id int64, body string) model.Comment {
	agent := model.Principal{Kind: model.PrincipalAgent, ID: "287"}
	return model.Comment{
		ID:     id,
		Body:   body,
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}, OnBehalfOf: &agent},
		CreatedAt: time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC).
			Add(time.Duration(id) * time.Minute),
		Handover: true,
	}
}

// longThread is a conversation far past the budget: 200 messages of a
// kilobyte each, numbered so a test can say which end survived.
func longThread() []model.Comment {
	body := strings.Repeat("x", 1<<10)
	comments := make([]model.Comment, 0, 200)
	for i := 0; i < 200; i++ {
		comments = append(comments, humanComment(int64(i), fmt.Sprintf("message-%03d %s", i, body)))
	}
	return comments
}

// elidedCount is the number the elision notice reports, or -1 when the
// section carries no notice at all.
func elidedCount(t *testing.T, section string) int {
	t.Helper()
	notice := strings.Index(section, " message(s) from the middle")
	if notice < 0 {
		return -1
	}
	at := strings.LastIndex(section[:notice], model.PromptThreadCutNotice)
	if at < 0 {
		t.Fatalf("a notice counting messages did not open with %q: %q",
			model.PromptThreadCutNotice, section[:notice])
	}
	var n int
	if _, err := fmt.Sscanf(section[at+len(model.PromptThreadCutNotice):], "%d message(s)", &n); err != nil {
		t.Fatalf("could not read the elided count out of %q: %v", section[at:notice], err)
	}
	return n
}

func humanComment(id int64, body string) model.Comment {
	return model.Comment{
		ID:        id,
		Body:      body,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
		CreatedAt: time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC).Add(time.Duration(id) * time.Minute),
	}
}

// A thread small enough to carry is carried exactly as it always was:
// every message whole, no notice, nothing about a bound at all. This is
// the overwhelmingly common case and it must cost nothing.
func TestShortConversationIsCarriedWhole(t *testing.T) {
	t.Parallel()
	section := commentThreadSection([]model.Comment{
		humanComment(1, "the merge lane may only ever hold one branch"),
		humanComment(2, "and the guest Dockerfile's FROM is the published image, not the base"),
	})
	for _, want := range []string{"merge lane", "guest Dockerfile"} {
		if !strings.Contains(section, want) {
			t.Errorf("section lost %q from a two-message thread: %q", want, section)
		}
	}
	if strings.Contains(section, "elided here by grain") {
		t.Errorf("a two-message thread was cut: %q", section)
	}
}

// The shape the whole change is for: a thread far past the budget keeps
// its oldest messages (where a task's terms of reference are) and its
// most recent (where the live conversation is), drops what is between
// them, and says so at the point it was cut.
func TestLongConversationKeepsBothEndsAndSaysWhatItCut(t *testing.T) {
	t.Parallel()
	section := commentThreadSection(longThread())

	if len(section) > maxConversationBytes+noticeSlack {
		t.Errorf("section is %d bytes, over the %d-byte budget", len(section), maxConversationBytes)
	}
	if !strings.Contains(section, "message-000") {
		t.Errorf("the oldest message went, which is the part a naive window drops: %q", firstOf(section))
	}
	if !strings.Contains(section, "message-199") {
		t.Errorf("the most recent message went, which must never happen: %q", lastOf(section))
	}
	if strings.Contains(section, "message-100") {
		t.Error("a message from the middle survived a thread this far over budget")
	}
	if !strings.Contains(section, "elided here by grain") {
		t.Errorf("the cut was silent, so the run cannot know it is reading a gap: %q", section)
	}
	// The heading itself says the thread was cut, the way the attempt
	// list says how many attempts it left out: the notice is at the gap,
	// but a run reads the heading first.
	if !strings.Contains(section, "longer than one prompt can carry") {
		t.Errorf("the heading does not say the thread was cut: %q", firstOf(section))
	}
}

// A single message bigger than the whole budget is the one case where
// something has to give: the newest message is kept anyway, with its own
// middle taken out, because a run that cannot see what was last said to
// it is worse off than one reading half of it.
func TestNewestMessageSurvivesEvenWhenItAloneIsOverBudget(t *testing.T) {
	t.Parallel()
	giant := "FIRST-LINE\n" + strings.Repeat("filler\n", maxConversationBytes/4) + "LAST-LINE"
	section := commentThreadSection([]model.Comment{
		humanComment(1, "the original terms"),
		humanComment(2, giant),
	})
	if len(section) > maxConversationBytes+noticeSlack {
		t.Errorf("section is %d bytes, over the %d-byte budget", len(section), maxConversationBytes)
	}
	for _, want := range []string{"FIRST-LINE", "LAST-LINE"} {
		if !strings.Contains(section, want) {
			t.Errorf("the newest message lost %q, so neither end of it survived: %q", want, lastOf(section))
		}
	}
	if !strings.Contains(section, "one message may not take the space") {
		t.Errorf("the message was cut without saying so: %q", section)
	}
}

// The same, for a message written as one long paragraph: no line inside
// it to snap the tail's cut to but the newline every rendered message
// ends with, which put the cut at the very end and kept none of the tail.
func TestNewestMessageKeepsBothEndsWhenItIsOneLongLine(t *testing.T) {
	t.Parallel()
	giant := "FIRST-WORDS " + strings.Repeat("filler ", maxConversationBytes/4) + "LAST-WORDS"
	section := commentThreadSection([]model.Comment{
		humanComment(1, "the original terms"),
		humanComment(2, giant),
	})
	if len(section) > maxConversationBytes+noticeSlack {
		t.Errorf("section is %d bytes, over the %d-byte budget", len(section), maxConversationBytes)
	}
	for _, want := range []string{"FIRST-WORDS", "LAST-WORDS"} {
		if !strings.Contains(section, want) {
			t.Errorf("the newest message lost %q: %q", want, lastOf(section))
		}
	}
}

// boundThread's own arithmetic, at the boundary the rest of the tests
// approach from far away: a thread exactly at the budget is whole, and
// one message past it is not.
func TestBoundThreadCutsOnlyWhatDoesNotFit(t *testing.T) {
	t.Parallel()
	line := strings.Repeat("a", 99) + "\n"
	fits := make([]string, 10)
	for i := range fits {
		fits[i] = line
	}
	keepHead, keepTail, dropped := boundThread(fits, len(line)*10)
	if keepHead != len(fits) || keepTail != len(fits) || dropped != 0 {
		t.Errorf("a thread exactly at budget was cut: head=%d tail=%d dropped=%d", keepHead, keepTail, dropped)
	}
	keepHead, keepTail, dropped = boundThread(fits, len(line)*9)
	if keepHead == keepTail {
		t.Fatal("a thread over budget was carried whole")
	}
	if dropped != (keepTail-keepHead)*len(line) {
		t.Errorf("dropped %d bytes for %d messages of %d", dropped, keepTail-keepHead, len(line))
	}
	if keepTail >= len(fits) {
		t.Error("nothing was kept from the recent end")
	}
}

// The other half of the policy (grain/task-287): a handover written by an
// earlier run is carried even when it sits in the middle the cut throws
// away. Everything around it goes; it does not.
func TestAHandoverIsCarriedOutOfThePartThatWasCut(t *testing.T) {
	t.Parallel()
	comments := longThread()
	comments[100] = handoverComment(100, "HANDOVER: the merge lane may only ever hold one branch")
	section := commentThreadSection(comments)

	if !strings.Contains(section, "HANDOVER: the merge lane") {
		t.Errorf("the handover went with the middle of the thread: %q", section)
	}
	if !strings.Contains(section, "this task's handover") {
		t.Errorf("the handover was carried without saying what it is: %q", section)
	}
	// Still a cut thread in every other respect: the bound is not
	// weakened by a handover existing, it is only added to.
	if !strings.Contains(section, "elided here by grain") {
		t.Errorf("the cut was silent: %q", section)
	}
	if strings.Contains(section, "message-100") || strings.Contains(section, "message-150") {
		t.Error("a message from the middle survived beside the handover")
	}
	for _, want := range []string{"message-000", "message-199"} {
		if !strings.Contains(section, want) {
			t.Errorf("section lost %q, so carrying the handover cost an end of the thread", want)
		}
	}
}

// The handover is carried *outside* the budget, not out of it: a message
// the cut rescued is not one of the messages the notice counts as gone,
// because the notice is what a run reads to know what it cannot see.
func TestACarriedHandoverIsNotCountedAsElided(t *testing.T) {
	t.Parallel()
	plain := longThread()
	marked := longThread()
	marked[100] = handoverComment(100, marked[100].Body)

	before := elidedCount(t, commentThreadSection(plain))
	after := elidedCount(t, commentThreadSection(marked))
	if before < 2 {
		t.Fatalf("the plain thread reported %d elided messages, want a real cut to compare against", before)
	}
	if after != before-1 {
		t.Errorf("marking one elided message as the handover changed the count %d -> %d, want %d",
			before, after, before-1)
	}
}

// A handover at the end of a cut thread is carried once with its provenance
// framing, rather than duplicated in both the carried slot and the kept tail.
func TestAHandoverInTheKeptTailIsNotRepeated(t *testing.T) {
	t.Parallel()
	comments := longThread()
	comments[199] = handoverComment(199, "HANDOVER: what the older half settled")
	section := commentThreadSection(comments)

	if n := strings.Count(section, "HANDOVER: what the older half settled"); n != 1 {
		t.Errorf("the handover appears %d times, want 1", n)
	}
	if !strings.Contains(section, "\n"+model.PromptThreadCutNotice) {
		t.Errorf("cut thread with handover at tail missing PromptThreadCutNotice: %q", firstOf(section))
	}
	if !strings.Contains(section, "The next line is this task's handover") {
		t.Errorf("handover on cut thread missing provenance framing: %q", lastOf(section))
	}
	if strings.Contains(section, "write_handover") {
		t.Errorf("thread with no post-handover overflow should not re-request write_handover: %q", lastOf(section))
	}
}

// The bound holds regardless, which is the whole point of the third
// bullet: a run that pasted a build log into its handover cannot spend
// the prompt on it. The summary is capped in its own right, cut in its
// own middle the way any over-long message is.
func TestACarriedHandoverIsCappedInItsOwnRight(t *testing.T) {
	t.Parallel()
	comments := longThread()
	comments[100] = handoverComment(100,
		"FIRST-LINE\n"+strings.Repeat("filler\n", maxConversationBytes/4)+"LAST-LINE")
	section := commentThreadSection(comments)

	limit := maxConversationBytes + conversationHandoverBudget(maxConversationBytes) + noticeSlack
	if len(section) > limit {
		t.Errorf("section is %d bytes, over the %d-byte budget plus one handover's share",
			len(section), limit)
	}
	for _, want := range []string{"FIRST-LINE", "LAST-LINE"} {
		if !strings.Contains(section, want) {
			t.Errorf("the handover lost %q, so neither end of it survived: %q", want, lastOf(section))
		}
	}
	if !strings.Contains(section, "one message may not take the space") {
		t.Errorf("the handover was cut without saying so: %q", section)
	}
}

// The one case where discounting the carried handover from the notice's
// count would have taken it to nought: a thread over the bound by exactly
// one message, and that message the handover. Nothing anybody wrote is
// missing from the prompt -- and the notice is written anyway, because it
// is also the only channel between a cut and a measurement of one
// (model.PromptThreadCutNotice, metrics.Prompts). A prompt that went
// silent here would be a cut prompt counted as uncut.
func TestAThreadCutOnlyOfItsHandoverStillSaysItWasCut(t *testing.T) {
	t.Parallel()
	// Sized off the budget rather than written out, so that re-pricing
	// the bound -- which maxConversationBytes' own doc comment expects --
	// moves this with it instead of turning it into a test of nothing.
	// The oldest message fills the head budget exactly and the newest
	// fills the tail's, so the only message that does not fit is the
	// handover between them.
	headBudget := conversationHeadBudget(maxConversationBytes)
	comments := []model.Comment{
		humanComment(1, strings.Repeat("o", headBudget-renderedOverhead)),
		handoverComment(2, "HANDOVER: the merge lane may only ever hold one branch"),
		humanComment(3, strings.Repeat("n", maxConversationBytes-headBudget-renderedOverhead)),
	}
	section := commentThreadSection(comments)

	if n := elidedCount(t, section); n != -1 {
		t.Fatalf("the notice counted %d elided message(s), want the thread's only cut message "+
			"to be the handover that was carried instead", n)
	}
	if !strings.Contains(section, "\n"+model.PromptThreadCutNotice) {
		t.Errorf("a thread that did not fit carries no %q, so metrics.Prompts counts it as uncut: %q",
			model.PromptThreadCutNotice, section[:min(len(section), 600)])
	}
	if !strings.Contains(section, "the only message the cut reached") {
		t.Errorf("the notice does not say what was cut: %q", section[:min(len(section), 600)])
	}
	if !strings.Contains(section, "HANDOVER: the merge lane") {
		t.Errorf("the handover was not carried: %q", section)
	}
	if !strings.Contains(section, "The next line is this task's handover") {
		t.Errorf("the carried handover is missing its provenance framing: %q", section[:min(len(section), 600)])
	}
	if strings.Contains(section, "write_handover") {
		t.Error("a thread whose post-handover messages fit within tailBudget was asked for another handover")
	}
}

// The request, which is the half of this that reaches the run: a cut
// thread asks for the summary the next run will read in place of the
// middle, and a thread that fits asks for nothing. Every dispatch of
// every task would otherwise pay for a paragraph about a problem it does
// not have -- and grain would collect summaries of threads nobody needed
// summarised.
func TestOnlyACutThreadIsAskedForAHandover(t *testing.T) {
	t.Parallel()
	whole := commentThreadSection([]model.Comment{
		humanComment(1, "the merge lane may only ever hold one branch"),
	})
	if strings.Contains(whole, "write_handover") {
		t.Errorf("a thread that fits whole was asked for a handover: %q", whole)
	}

	cut := commentThreadSection(longThread())
	if !strings.Contains(cut, "write_handover") {
		t.Errorf("a cut thread was not asked for a handover: %q", lastOf(cut))
	}
	if !strings.Contains(cut, "write this task's handover") {
		t.Errorf("the ask does not say what to write: %q", lastOf(cut))
	}
	// And it must not read as an ending: a run that treats the ask as its
	// instructions answers the prompt by summarising a conversation
	// instead of doing the work in it.
	if !strings.Contains(cut, "does not complete the task") {
		t.Errorf("the ask does not say it is not a closing note: %q", lastOf(cut))
	}
}

// A run that can see a handover is asked to replace it rather than to
// write one from nothing -- and told it may leave a still-true one alone,
// since only the most recent is ever carried and a rewrite throws the
// last one away.
func TestARunThatCanSeeAHandoverIsAskedToReplaceIt(t *testing.T) {
	t.Parallel()
	comments := longThread()
	comments[100] = handoverComment(100, "HANDOVER: the merge lane may only ever hold one branch")
	section := commentThreadSection(comments)

	for _, want := range []string{"replacing that handover", "leave it alone", "only the most recent"} {
		if !strings.Contains(section, want) {
			t.Errorf("the ask does not say %q: %q", want, lastOf(section))
		}
	}
}

func firstOf(s string) string {
	if len(s) > 400 {
		return s[:400] + "..."
	}
	return s
}

func lastOf(s string) string {
	if len(s) > 400 {
		return "..." + s[len(s)-400:]
	}
	return s
}

// The cut notices are read back as well as read: a prompt is the only
// place a cut survives its dispatch, so metrics.Prompts counts the
// prompts carrying one of them off the recorded text
// (Store.RunPrompts). That read matches at the start of a line, so a
// real cut has to put the notice there -- this is the test that fails if
// either notice is ever reworded away from its constant.
func TestACutThreadCarriesItsNoticeAtTheStartOfALine(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("x", 1<<10)
	comments := make([]model.Comment, 0, 200)
	for i := 0; i < 200; i++ {
		comments = append(comments, humanComment(int64(i), fmt.Sprintf("message-%03d %s", i, body)))
	}
	if section := commentThreadSection(comments); !strings.Contains(section, "\n"+model.PromptThreadCutNotice) {
		t.Errorf("a cut thread does not carry %q at the start of a line: %q",
			model.PromptThreadCutNotice, section)
	}

	giant := "FIRST-LINE\n" + strings.Repeat("filler\n", maxConversationBytes/4) + "LAST-LINE"
	section := commentThreadSection([]model.Comment{humanComment(1, giant)})
	if !strings.Contains(section, "\n"+model.PromptMessageCutNotice) {
		t.Errorf("a cut message does not carry %q at the start of a line: %q",
			model.PromptMessageCutNotice, firstOf(section))
	}
}

// Resting comments (_Waiting for you._) added by console sessions must be
// omitted from prompt conversation threads so they do not bloat prompt tokens.
func TestCommentThreadSectionOmitsConsoleSessionRestingComments(t *testing.T) {
	t.Parallel()
	section := commentThreadSection([]model.Comment{
		humanComment(1, "hello"),
		{
			ID:        2,
			Body:      consoleSessionResting,
			Author:    model.Attribution{Actor: consoleSessionPrincipal},
			CreatedAt: time.Date(2026, 9, 14, 9, 1, 0, 0, time.UTC),
		},
		humanComment(3, "can you help me?"),
		{
			ID:        4,
			Body:      "  " + consoleSessionResting + " \n",
			Author:    model.Attribution{Actor: consoleSessionPrincipal},
			CreatedAt: time.Date(2026, 9, 14, 9, 3, 0, 0, time.UTC),
		},
	})
	if strings.Contains(section, consoleSessionResting) {
		t.Errorf("section contains resting marker %q: %s", consoleSessionResting, section)
	}
	if !strings.Contains(section, "hello") || !strings.Contains(section, "can you help me?") {
		t.Errorf("section missing real comments: %s", section)
	}
}

func TestCommentThreadSectionReturnsEmptyIfOnlyRestingComments(t *testing.T) {
	t.Parallel()
	section := commentThreadSection([]model.Comment{
		{
			ID:        1,
			Body:      consoleSessionResting,
			Author:    model.Attribution{Actor: consoleSessionPrincipal},
			CreatedAt: time.Date(2026, 9, 14, 9, 1, 0, 0, time.UTC),
		},
	})
	if section != "" {
		t.Errorf("section = %q, want empty", section)
	}
}

// Only grain's own resting line is left out: a person who types the same
// words has said something, and the run dispatched for it has to see it
// (docs/test-runs/2026-10-12-console-resting-notes.md).
func TestCommentThreadSectionKeepsRestingWordsFromAPerson(t *testing.T) {
	t.Parallel()
	section := commentThreadSection([]model.Comment{
		{
			ID:        1,
			Body:      consoleSessionResting,
			Author:    model.Attribution{Actor: consoleSessionPrincipal},
			CreatedAt: time.Date(2026, 9, 14, 9, 1, 0, 0, time.UTC),
		},
		humanComment(2, consoleSessionResting),
	})
	if strings.Count(section, consoleSessionResting) != 1 {
		t.Errorf("section = %q, want the person's message and not grain's park line", section)
	}
	if !strings.Contains(section, "a human") {
		t.Errorf("section = %q, want the person's message attributed to them", section)
	}
}

// Once a run writes a handover at the end of a cut thread, subsequent runs
// with small incremental messages must not remain permanently in the cut state
// or ask for another write_handover on every single turn: the window resets at
// the latest handover and only cuts again once post-handover messages exceed
// tailBudget on their own.
func TestHandoverResetsConversationWindowForSubsequentTurns(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("x", 1<<10)
	comments := make([]model.Comment, 0, 85)
	for i := 0; i < 80; i++ {
		comments = append(comments, humanComment(int64(i+1), fmt.Sprintf("pre-handover-%03d %s", i, body)))
	}

	// Before the handover is written, the ~80 KiB thread exceeds the 64 KiB cap
	// and asks the run to write a handover.
	before := commentThreadSection(comments)
	if !strings.Contains(before, model.PromptThreadCutNotice) {
		t.Fatalf("expected thread >64 KiB before handover to be cut:\n%s", firstOf(before))
	}
	if !strings.Contains(before, "write_handover") {
		t.Fatalf("expected cut thread before handover to ask for write_handover:\n%s", lastOf(before))
	}

	// Record a handover and a few small incremental follow-up comments.
	handover := humanComment(81, "HANDOVER-SUMMARY: decisions and invariants settled in the first 80 messages")
	handover.Handover = true
	comments = append(comments,
		handover,
		humanComment(82, "follow-up-1: short incremental reply"),
		humanComment(83, "follow-up-2: another short incremental turn"),
	)

	after := commentThreadSection(comments)
	if !strings.Contains(after, "\n"+model.PromptThreadCutNotice) {
		t.Errorf("post-handover thread with elided pre-handover middle is missing PromptThreadCutNotice:\n%s", firstOf(after))
	}
	if !strings.Contains(after, "longer than one prompt can carry") {
		t.Errorf("post-handover thread with elided pre-handover middle does not say it was cut:\n%s", firstOf(after))
	}
	if !strings.Contains(after, "The next line is this task's handover") {
		t.Errorf("post-handover thread missing handover provenance framing:\n%s", lastOf(after))
	}
	if n := elidedCount(t, after); n <= 0 {
		t.Errorf("expected positive elided pre-handover message count, got %d", n)
	}
	if strings.Contains(after, "write_handover") {
		t.Errorf("post-handover thread with small delta still asks for write_handover:\n%s", lastOf(after))
	}
	for _, want := range []string{
		"pre-handover-000",
		"HANDOVER-SUMMARY: decisions and invariants settled in the first 80 messages",
		"follow-up-1: short incremental reply",
		"follow-up-2: another short incremental turn",
	} {
		if !strings.Contains(after, want) {
			t.Errorf("post-handover thread missing %q", want)
		}
	}

	// Once post-handover messages accumulate another full tailBudget (~43 KiB),
	// the window cuts again and asks the run to consider replacing the handover.
	for i := 0; i < 50; i++ {
		comments = append(comments, humanComment(int64(100+i), fmt.Sprintf("post-handover-%03d %s", i, body)))
	}
	overflowed := commentThreadSection(comments)
	if !strings.Contains(overflowed, model.PromptThreadCutNotice) {
		t.Errorf("expected thread to cut again once post-handover comments exceed tailBudget:\n%s", firstOf(overflowed))
	}
	if !strings.Contains(overflowed, "consider replacing that handover") {
		t.Errorf("expected re-cut thread to ask the run to consider replacing the handover:\n%s", lastOf(overflowed))
	}
}

// Earlier handover comments that have been superseded by a newer handover must
// not remain in the rendered slice and crowd out real conversation messages in
// the tail budget, while still being counted in the elision notice alongside
// dropped pre-handover messages.
func TestSupersededHandoversDoNotConsumeTailBudget(t *testing.T) {
	t.Parallel()
	chunk := strings.Repeat("y", 1<<10)
	var comments []model.Comment
	for i := 0; i < 40; i++ {
		comments = append(comments, humanComment(int64(i+1), fmt.Sprintf("initial-%03d %s", i, chunk)))
	}
	// Three older 12 KiB handover comments that would otherwise eat 36 KiB of the
	// 43 KiB tail budget.
	for i := 1; i <= 3; i++ {
		old := humanComment(int64(100+i), fmt.Sprintf("OLD-HANDOVER-%d %s", i, strings.Repeat("h", 12<<10)))
		old.Handover = true
		comments = append(comments, old)
	}
	latest := humanComment(200, "LATEST-HANDOVER: current standing summary")
	latest.Handover = true
	comments = append(comments, latest)

	// Add 30 comments (~30 KiB) after the latest handover, which easily fit in
	// tailBudget (~43 KiB) when superseded handovers are excluded, but would
	// overflow if the 36 KiB of superseded handovers were kept.
	for i := 0; i < 30; i++ {
		comments = append(comments, humanComment(int64(300+i), fmt.Sprintf("recent-turn-%03d %s", i, chunk)))
	}

	section := commentThreadSection(comments)
	for i := 1; i <= 3; i++ {
		marker := fmt.Sprintf("OLD-HANDOVER-%d", i)
		if strings.Contains(section, marker) {
			t.Errorf("superseded handover %q was still rendered in thread section", marker)
		}
	}
	if !strings.Contains(section, "LATEST-HANDOVER: current standing summary") {
		t.Errorf("latest handover missing from thread section")
	}
	if !strings.Contains(section, "The next line is this task's handover") {
		t.Errorf("latest handover missing provenance framing")
	}
	for i := 0; i < 30; i++ {
		marker := fmt.Sprintf("recent-turn-%03d", i)
		if !strings.Contains(section, marker) {
			t.Errorf("recent post-handover comment %q was crowded out of tailBudget", marker)
		}
	}
	if !strings.Contains(section, "\n"+model.PromptThreadCutNotice) {
		t.Errorf("thread section missing PromptThreadCutNotice despite elided pre-handover messages:\n%s", firstOf(section))
	}
	if n := elidedCount(t, section); n != 23 {
		t.Errorf("elidedCount = %d, want 23 (20 initial comments + 3 superseded handovers)", n)
	}
	if strings.Contains(section, "write_handover") {
		t.Errorf("thread section asked for write_handover despite post-handover messages fitting within tailBudget:\n%s", lastOf(section))
	}
}

// A handover has its own conversationHandoverBudget on top of
// maxConversationBytes and must not be counted against tailBudget when
// measuring post-handover messages.
func TestLargeHandoverDoesNotConsumePostHandoverTailBudget(t *testing.T) {
	t.Parallel()
	chunk := strings.Repeat("z", 1<<10)
	var comments []model.Comment
	for i := 0; i < 40; i++ {
		comments = append(comments, humanComment(int64(i+1), fmt.Sprintf("pre-msg-%03d %s", i, chunk)))
	}
	// A 25 KiB handover (> 16 KiB conversationHandoverBudget) that would push
	// 35 KiB of post-handover comments over the ~43 KiB tailBudget if counted
	// inside postHandoverBytes.
	giantHandover := humanComment(100, "FIRST-HANDOVER-LINE\n"+strings.Repeat("summary-line\n", 2048)+"LAST-HANDOVER-LINE")
	giantHandover.Handover = true
	comments = append(comments, giantHandover)

	for i := 0; i < 35; i++ {
		comments = append(comments, humanComment(int64(200+i), fmt.Sprintf("post-msg-%03d %s", i, chunk)))
	}

	section := commentThreadSection(comments)
	for _, want := range []string{
		"FIRST-HANDOVER-LINE",
		"LAST-HANDOVER-LINE",
		model.PromptMessageCutNotice,
		model.PromptThreadCutNotice,
		"The next line is this task's handover",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("section missing %q", want)
		}
	}
	for i := 0; i < 35; i++ {
		marker := fmt.Sprintf("post-msg-%03d", i)
		if !strings.Contains(section, marker) {
			t.Errorf("post-handover comment %q was crowded out by large handover", marker)
		}
	}
	if strings.Contains(section, "write_handover") {
		t.Errorf("section asked for write_handover even though post-handover messages fit in tailBudget:\n%s", lastOf(section))
	}
}
