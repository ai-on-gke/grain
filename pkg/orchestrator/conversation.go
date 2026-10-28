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

package orchestrator

// The task's conversation, as a dispatch's prompt carries it -- and the
// bound on how much of it that may be.
//
// Everything else in a prompt was already bounded. A tool result is
// capped and says so where it bites (mcp.elideMiddle); the attempt list
// keeps the last few and says how many it left out
// (maxPreviousAttempts); an automated comment's body is trimmed to a
// couple of hundred characters (maxAutomatedCommentPromptLength). The
// thread itself was the one unbounded thing: every message a task had
// ever collected, replayed whole into every attempt's prompt, growing
// monotonically for as long as the task stayed open. An interactive task
// open for a week is the worst case and not a rare one -- a Director
// task's answers are long, and every one of them was paid for again on
// every later dispatch.
//
// The ceiling that catches is the model's context window, and it does
// not announce itself: the run does not fail at dispatch, it behaves
// worse than the same run did on the same task last week, with its
// actual instructions crowded into the tail of something enormous. On a
// task that retries, that is a consecutive-failure budget spent with
// nothing in the record naming the reason (grain/task-268).
//
// # Which policy this is, and why
//
// Four were on the table: window the thread, summarise its older half
// with grain's memory agent, have the run itself write a handover
// comment when it is told its prompt is near the bound, or offer to
// split a long-running chat into a fresh task. This is the first, with
// the flaw the first one is usually dismissed for designed out.
//
// A naive window keeps the last N messages, and in the conversation that
// prompted this work that is exactly wrong: the *earliest* turns carry
// the decisions the later ones depend on -- the invariant somebody
// agreed to on day one, the semantic a later "yes, do that" refers back
// to -- and a tail-only window drops precisely the load-bearing part.
// So both ends are kept and the middle is what goes: the oldest turns,
// where a task's terms of reference are, and the most recent, where the
// live conversation is, with the gap marked at the point it was cut.
//
// The third is here too, on top of the first rather than instead of it
// (grain/task-287). Where the cut bites, the run is asked in the same
// prompt to write down what the part it cannot see settled, and the most
// recent such handover is carried into every later prompt outside the
// head/tail budget -- because the editorial judgement of what a
// conversation actually settled belongs where the context is, which is
// inside the run reading it, and because position is the only thing a
// bound can go on. It is strictly additive: a task with no handover is
// bounded exactly as it was, the summary is carried on top of the budget
// rather than in place of the cut, and it is capped in its own right. A
// summary a run never wrote, or wrote badly, must not be the only thing
// standing between a week-old chat and the context window.
//
// The second is not foreclosed. Summarising in the dispatch path means a
// model call between a task and every run of it -- a new way for a
// dispatch to fail, and a summary nobody reviewed standing in for what a
// human actually said, which is the one thing the handover above is
// careful not to be: it replaces nothing, and every message it
// summarises is still on the task.
//
// The fourth is decided, and the answer is that nothing here does it
// (grain/task-309). Splitting a week-old chat into a new task carrying
// its handover was always a product decision for an operator rather than
// one a prompt builder may make on their behalf, and asked of an
// operator it turns out to need no mechanism: a standing session's task
// closed is a session refiled with a fresh conversation and the same
// capabilities the cycle after (orchestrator.reconcileConsoleSession),
// and an interactive task that is not a session has a branch and a
// grant set that make a second task a duplicate rather than a
// continuation. So this file keeps bounding the thread of a chat that
// never ends, and the handover keeps being what survives the cut. See
// "A chat that never ends is not split for you" in
// docs/implementation-notes.md.
//
// What a bound has over all of them is that it holds even when nobody is
// watching, which is the state this was found in.
//
// # What is never dropped
//
// The task's own description and title are not here at all -- they are
// the head of the prompt (BuildPrompt), outside this budget, and nothing
// in this file can reach them. Of the thread, the most recent message is
// kept whatever its size (cut in its own middle if it alone is over
// budget, never dropped), the oldest are kept ahead of anything in
// between, and the most recent handover is kept whether or not it falls
// in the part that was cut.

import (
	"fmt"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// maxConversationBytes is how much of a task's thread one prompt
// carries: 64 KB, about 16,000 tokens, roughly 8% of the window
// model.PromptContextTokens measures against.
//
// It was chosen before there was a measurement to choose it from -- the
// same position mcp.maxToolResultBytes started from at 64 KB before a
// census re-priced it to 16 KB. It was re-read in grain/task-286 and
// left exactly where it is, and what that re-reading settled and did not
// settle is worth writing down, because the next reader will otherwise
// re-open the argument from the same standing start.
//
// What it settled is the ceiling, measured rather than argued
// (promptbudget_internal_test.go). Everything grain bounds, all of it at
// its bound at once -- this budget, the handover carried beside it, the
// attempt history at maxPreviousAttempts, grain's own standing sections,
// a 2 KB description -- comes to about 82 KB: roughly 21,000 tokens, 11%
// of the window. Of that the thread is 72 KB and grain's own sections
// are 6 KB, so the boilerplate is not what a prompt is made of and the
// thread is. Two things follow. A long-running Director task cannot walk
// into the window on its conversation alone, however long it runs -- a
// prompt big enough to warn about (model.PromptWarnTokens) has to have
// got that way through something nobody bounds: a description with a
// build log pasted into it, this deployment's memory, its prompt
// extension, a capability's own section. And there is room above this
// number if anything ever wants it: the ceiling first trips that warning
// at a budget of about 160 KB, so the bound can double and cannot
// quadruple. Both are asserted rather than recorded, so a later change
// that spends the headroom has to say so out loud.
//
// What it did not settle is whether the bound is too *tight* -- whether
// it cuts threads a run would have wanted whole. That is not answerable
// from the code, because every prompt recorded since the bound landed is
// censored at it: a task whose thread is ten times the budget and one
// whose thread just fits are the same number in the record. The
// measurement that survives the censoring is the share of prompts
// carrying a cut notice (metrics.Prompts.ThreadCut, over
// model.PromptThreadCutNotice), reported by `grain metrics` and the
// Metrics pane over a window of dispatches beginning after grain/task-268
// landed on 18 Sep 2026. A deployment cutting most of its threads is
// paying this bound on most of its dispatches and has the headroom above
// to spend on them; one cutting almost none has a bound that costs
// nothing, and the question of lowering it is then about what grain's
// sections and the unbounded material cost, not about this number.
//
// A var rather than a const so a test can shrink it instead of
// generating 64 KB of conversation to prove the cut works -- exactly
// what mcp.maxToolResultBytes is a var for.
var maxConversationBytes = 64 << 10

// conversationHeadBudget is how much of that budget the oldest messages
// may take: a third.
//
// The split is deliberately lopsided towards the recent end. The oldest
// turns are kept because they carry the task's terms of reference, and a
// task's terms of reference are short -- what somebody asked for, what
// was agreed, what the thing is called. The live conversation is where
// the detail is, and it is also the part a run is about to act on, so it
// gets the larger share. The head only ever takes less than its third
// here, never more: what the tail leaves unspent stays unspent rather
// than pulling the section back towards material nobody is acting on.
func conversationHeadBudget(budget int) int { return budget / 3 }

// conversationHandoverBudget is how much a carried handover may spend --
// a quarter of the conversation budget, and *on top of* it rather than
// out of it.
//
// Outside the budget because a handover is the one message written to be
// read after the thread it summarises is gone: taking it out of the same
// budget the cut is enforcing would mean the summary competed with the
// live conversation for room, and would sometimes lose -- which is the
// exact failure it exists to prevent, one layer up. So the section a
// cut thread produces may come to the budget plus this, and that is the
// price of the policy stated out loud.
//
// Bounded all the same, and generously: 16 KB against the default budget
// is several thousand words, far more than the few hundred write_handover
// asks for. The cap is not there to make a good handover fit, it is there
// so that a run which pasted a build log into one cannot spend a prompt
// on it -- "a summary the run never wrote, or wrote badly, must not be
// the only thing standing between a week-old chat and the context
// window".
func conversationHandoverBudget(budget int) int { return budget / 4 }

// commentThreadSection renders task's conversation into a prompt
// section, or "" if there is none yet -- a task's first dispatch always
// gets "", since nothing has been said about it until a run itself says
// something or a human replies to it.
//
// Without this, a redispatched run has no way to see a human's answer to
// a question it parked on with ask_question, or any other comment left
// while it wasn't running: RunDispatch used to build every dispatch's
// prompt from task.Title and task.Body alone, so a task that asked a
// question and got answered would redispatch, ask the identical question
// again, and go straight back to awaiting_reply -- forever, since nothing
// about the run ever changed (bwsalmon/agents#402).
//
// It is rendered by BuildPrompt, next to previousAttemptsSection: the
// conversation and the attempt history are the same kind of fact about
// what has already happened to this task, and a run told to read the
// thread first should not have to find it three paragraphs past the one
// telling it how to push.
//
// Bounded by maxConversationBytes, and where the bound bites the run is
// told so in the same plain way the attempt list says how many attempts
// it left out. A run that silently could not see what it was told on
// Tuesday would contradict itself, and neither it nor the operator
// reading the contradiction would know why.
//
// The notice opens with model.PromptThreadCutNotice rather than with its
// own words, because it is read back as well as read: the recorded
// prompt is the only place a cut survives the dispatch, and a report over
// prompt sizes cannot be read without knowing how many of them were cut
// down to the bound (metrics.Prompts). One of those notices is written
// whenever the thread did not fit, so that measurement counts every cut
// prompt -- including the one case below where nothing is missing from
// the prompt at all, because the only message the cut reached was the
// handover and that one is carried.
//
// Where the bound bites, two more things happen, and they are the meaning
// half of the policy the bound is the position half of (grain/task-287).
// The most recent handover on the task -- a run's own standing summary of
// what the older part of the thread settled (model.Comment.Handover) --
// is carried whole even when the cut would have dropped it, outside the
// budget the rest of the thread is bounded by. And the run is asked to
// write the next one before it finishes, since the editorial judgement of
// what the elided middle actually settled belongs where the context is,
// which is inside the run reading it.
//
// Neither weakens the bound. A task with no handover, or one whose
// handover is useless, is cut exactly as it was before; the summary is
// carried on top of the budget rather than in place of the cut, and is
// itself capped (conversationHandoverBudget).
func commentThreadSection(comments []model.Comment) string {
	if len(comments) == 0 {
		return ""
	}
	// Omit synthetic resting markers ("_Waiting for you._") between console
	// session exchanges: they are parking punctuation rather than conversation
	// content, and omitting them keeps prompts clean and avoids token bloat.
	// Only grain's own line: a person who types the same words has said
	// something, and a run dispatched for it must be able to read it.
	//
	// Likewise omit earlier handover comments superseded by a newer one: each
	// handover replaces the one before it (handoverRequest), so keeping older
	// handovers in the rendered slice would only crowd out real conversation
	// messages inside the tail budget.
	renderComment := func(c model.Comment) string {
		return fmt.Sprintf("- %s: %s\n", attributionLabel(c.Author, c.TaskID), promptCommentBody(c))
	}
	capHandover := func(line string) string {
		return elideMessageMiddle(line, conversationHandoverBudget(maxConversationBytes))
	}
	lastHandover := -1
	for i, c := range comments {
		if c.Handover {
			lastHandover = i
		}
	}
	var filtered []model.Comment
	var superseded, supersededBytes int
	for i, c := range comments {
		if isConsoleSessionResting(c) {
			continue
		}
		if c.Handover && i != lastHandover {
			superseded++
			supersededBytes += len(renderComment(c))
			continue
		}
		filtered = append(filtered, c)
	}
	if len(filtered) == 0 {
		return ""
	}
	comments = filtered

	rendered := make([]string, len(comments))
	handover := -1
	totalBytes := supersededBytes
	for i, c := range comments {
		rendered[i] = renderComment(c)
		totalBytes += len(rendered[i])
		if c.Handover {
			handover = i
		}
	}

	// When a handover has been written on a thread that exceeds
	// maxConversationBytes, it stands in for the pre-handover middle of the
	// conversation and is carried under its own conversationHandoverBudget
	// rather than competing with post-handover messages for tailBudget.
	// Measure the active tail window strictly over the messages after the
	// handover so the run still gets PromptThreadCutNotice and the carried
	// handover provenance framing, while only re-requesting write_handover once
	// the post-handover messages themselves overflow tailBudget.
	postHandoverBytes := 0
	if handover >= 0 {
		for _, line := range rendered[handover+1:] {
			postHandoverBytes += len(line)
		}
	}
	headBudget := conversationHeadBudget(maxConversationBytes)
	tailBudget := maxConversationBytes - headBudget

	var head, tail, dropped, droppedBytes int
	carried := ""
	if handover >= 0 && totalBytes > maxConversationBytes {
		carried = capHandover(rendered[handover])
		if postHandoverBytes <= tailBudget {
			headBytes := 0
			for head < handover && headBytes+len(rendered[head]) <= headBudget {
				headBytes += len(rendered[head])
				head++
			}
			tail = handover + 1
			dropped = (handover - head) + superseded
			droppedBytes = supersededBytes
			for _, line := range rendered[head:handover] {
				droppedBytes += len(line)
			}
		} else {
			tail = len(rendered)
			used := 0
			for i := len(rendered) - 1; i > handover; i-- {
				if used+len(rendered[i]) > tailBudget {
					break
				}
				used += len(rendered[i])
				tail = i
			}
			if tail == len(rendered) && handover+1 < len(rendered) {
				last := len(rendered) - 1
				rendered[last] = elideMessageMiddle(rendered[last], tailBudget)
				used = len(rendered[last])
				tail = last
			}
			headBytes := 0
			for head < handover && headBytes+len(rendered[head]) <= headBudget && used+headBytes+len(rendered[head]) <= maxConversationBytes {
				headBytes += len(rendered[head])
				head++
			}
			dropped = (handover - head) + (tail - (handover + 1)) + superseded
			droppedBytes = supersededBytes
			for _, line := range rendered[head:handover] {
				droppedBytes += len(line)
			}
			for _, line := range rendered[handover+1 : tail] {
				droppedBytes += len(line)
			}
		}
	} else {
		head, tail, droppedBytes = boundThread(rendered, maxConversationBytes)
		dropped = tail - head
	}
	cut := dropped > 0 || carried != ""

	var b strings.Builder
	b.WriteString("Conversation on this task so far, oldest first -- read it before doing " +
		"anything else, since it may already answer a question you would otherwise ask again")
	if cut {
		b.WriteString(". It is longer than one prompt can carry, so what follows is its " +
			"oldest messages and its most recent ones, with the gap marked where the " +
			"rest was cut")
	}
	b.WriteString(":\n")
	for _, line := range rendered[:head] {
		b.WriteString(line)
	}
	switch {
	case dropped > 0:
		fmt.Fprintf(&b, model.PromptThreadCutNotice+"%d message(s) from the middle of it, %s "+
			"of text, elided here by grain, not by anyone who wrote them -- a task's thread "+
			"is capped so that a long-running conversation cannot crowd out the rest of this prompt. "+
			"The oldest messages above and the most recent ones below are whole; only the "+
			"middle is gone. The whole thread is on the task in grain's UI, so ask for any "+
			"part of it you need rather than assuming nothing was said there.\n",
			dropped, model.PromptBytes(droppedBytes))
	case carried != "":
		// The thread did not fit, and the only message the cut reached
		// was the handover -- which is carried below rather than dropped,
		// so the count above would have been nought messages and the gap
		// it marks is not a gap. Said anyway, and in the same words,
		// because the notice is the one channel between a cut and a
		// measurement of it: a prompt that went silent here would be a
		// cut prompt that metrics.Prompts counts as uncut.
		b.WriteString(model.PromptThreadCutNotice + "the only message the cut reached was " +
			"this task's handover, which is carried below rather than dropped -- so the " +
			"thread is over the cap grain holds it to, and nothing else anybody wrote is " +
			"missing from it.\n")
	}
	if carried != "" {
		b.WriteString("[grain] The next line is this task's handover: a standing summary of " +
			"what the older part of this conversation settled, written by an earlier run on " +
			"this task for whoever came next. It is carried here out of the part of the " +
			"thread that did not fit, rather than cut along with the messages around it, " +
			"because it is the one message written to be read after the rest is gone. It is " +
			"an agent's summary and not anybody's own words, so where it disagrees with a " +
			"message above or below it, the message is what was actually said.\n")
		b.WriteString(carried)
	}
	for _, line := range rendered[tail:] {
		b.WriteString(line)
	}
	if cut && (handover < 0 || tail > handover+1) {
		b.WriteString(handoverRequest(carried != ""))
	}
	return b.String()
}

// handoverRequest is what a run whose thread was cut is asked to do about
// it: write the summary that the next run on this task will read in place
// of the middle this one could not see.
//
// It is asked for only where the cut actually happened. A run on a task
// whose whole conversation fits has nothing to summarise that the next
// run will not simply be shown, and asking every run for a handover would
// buy grain a stack of summaries of threads nobody needed summarised --
// paid for in the prompt, every dispatch, forever.
//
// The ask is deliberately unconditional about the *rest* of the run: a
// handover accompanies whatever else the run was dispatched to do, is not
// a closing note, and does not complete the task
// (orchestrator.relayHandover, mcp's write_handover). A run that read the
// ask as an ending would answer the prompt by summarising a conversation
// instead of doing the work in it.
//
// revising is a run that can already see a carried handover above. It is
// told to replace rather than append, and told it may leave a still-true
// handover alone: only the most recent one is ever carried, so a run that
// rewrites a good summary into a worse one has thrown the good one away,
// and a run that adds nothing costs nothing by saying nothing.
func handoverRequest(revising bool) string {
	if revising {
		return "[grain] Before you finish, consider replacing that handover with a fresh one " +
			"(write_handover), folding into it whatever has been settled since it was " +
			"written -- grain carries only the most recent handover, so a new one replaces " +
			"that one rather than adding to it, and anything you leave out of yours is gone " +
			"from every later prompt on this task. If nothing has been settled since, leave " +
			"it alone: it stays in place until a run replaces it. Either way this is not a " +
			"closing note and does not complete the task.\n"
	}
	return "[grain] Before you finish, write this task's handover (write_handover): a standing " +
		"summary of what the part of this conversation you cannot see settled -- the " +
		"decisions, the invariants, the names, what was already ruled out -- written for the " +
		"next run on this task rather than for a person. grain carries the most recent " +
		"handover into every later prompt on this task, outside the part of the thread that " +
		"gets cut, so it is the only way anything from the middle of this conversation " +
		"reaches whoever works on this next. Do it whatever else this run ends up doing: it " +
		"is not a closing note and does not complete the task.\n"
}

// boundThread decides which of the rendered messages fit in budget
// bytes: everything before head and everything from tail on is kept, and
// what lies between them is dropped, droppedBytes being how much text
// that was.
//
// head == tail is a thread that fits whole, which is the overwhelmingly
// common case and the one that costs nothing: a task with a handful of
// comments is measured, found to fit, and rendered exactly as it always
// was.
//
// The tail is filled first, from the newest backwards, because the most
// recent turns are the ones the run is about to act on; the head then
// takes what is left of its own share, oldest first. A single message
// too big for the whole tail budget is cut in its own middle rather than
// dropped -- rendered is written through for that -- so that the newest
// message is present in every outcome this function can produce.
func boundThread(rendered []string, budget int) (head, tail, droppedBytes int) {
	total := 0
	for _, line := range rendered {
		total += len(line)
	}
	if total <= budget || len(rendered) == 0 {
		return len(rendered), len(rendered), 0
	}

	headBudget := conversationHeadBudget(budget)
	tailBudget := budget - headBudget
	used := 0
	tail = len(rendered)
	for i := len(rendered) - 1; i >= 0; i-- {
		if used+len(rendered[i]) > tailBudget {
			break
		}
		used += len(rendered[i])
		tail = i
	}
	if tail == len(rendered) {
		// Not even the newest message fits. It is kept anyway, with its
		// own middle taken out: a run that cannot see the thing that was
		// said to it last is worse off than one reading half of it, and
		// a message that large is one somebody pasted a file into.
		last := len(rendered) - 1
		rendered[last] = elideMessageMiddle(rendered[last], tailBudget)
		used = len(rendered[last])
		tail = last
	}
	headUsed := 0
	for i := 0; i < tail; i++ {
		if headUsed+len(rendered[i]) > headBudget || used+len(rendered[i]) > budget {
			break
		}
		headUsed += len(rendered[i])
		used += len(rendered[i])
		head = i + 1
	}
	for _, line := range rendered[head:tail] {
		droppedBytes += len(line)
	}
	return head, tail, droppedBytes
}

// elideMessageMiddle keeps the head and the tail of one message that is
// too long for the whole conversation budget by itself, with a notice
// between them saying what went.
//
// Head and tail rather than head alone, for mcp.elideMiddle's reason: a
// message's point is as often at its end as at its start, and the middle
// is what nobody needed. The cut snaps to line boundaries so neither
// half ends mid-line.
func elideMessageMiddle(line string, limit int) string {
	if limit <= 0 || len(line) <= limit {
		return line
	}
	headEnd := lineStartBefore(line, limit/2)
	tailStart := lineStartAfter(line, len(line)-(limit-limit/2))
	if tailStart <= headEnd {
		tailStart = len(line)
	}
	var b strings.Builder
	b.WriteString(line[:headEnd])
	if headEnd > 0 && !strings.HasSuffix(line[:headEnd], "\n") {
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, model.PromptMessageCutNotice+"%s of its middle elided here by grain, not "+
		"by whoever wrote it -- one message may not take the space the whole conversation has. "+
		"The whole of it is on the task in grain's UI.\n",
		model.PromptBytes(tailStart-headEnd))
	b.WriteString(line[tailStart:])
	if !strings.HasSuffix(b.String(), "\n") {
		b.WriteByte('\n')
	}
	return b.String()
}

// lineStartBefore returns the offset just past the last newline at or
// before pos, or pos itself when there is no newline to snap to. The
// twin of mcp.lineBoundaryBefore, which cuts tool output the same way;
// duplicated rather than exported across the seam because what the two
// packages share here is four lines of arithmetic and not a policy.
func lineStartBefore(s string, pos int) int {
	if i := strings.LastIndexByte(s[:pos], '\n'); i >= 0 {
		return i + 1
	}
	return pos
}

// lineStartAfter returns the offset just past the first newline at or
// after pos, or pos itself when there is no newline to snap to -- which
// includes a newline that only ends s. Every rendered message ends in
// one, so snapping to it would put the cut at the very end and keep none
// of the tail: a message written as one long paragraph lost its whole
// second half rather than its middle.
func lineStartAfter(s string, pos int) int {
	if pos < 0 {
		pos = 0
	}
	if i := strings.IndexByte(s[pos:], '\n'); i >= 0 && pos+i+1 < len(s) {
		return pos + i + 1
	}
	return pos
}

// Automated notices accumulate across retries and every later agent pays
// for them in its prompt. Keep those summaries small here, while retaining
// human replies and words relayed on an agent's behalf verbatim.
const maxAutomatedCommentPromptLength = 240

func promptCommentBody(c model.Comment) string {
	if c.Author.OnBehalfOf != nil || c.Author.Actor.Kind != model.PrincipalAutomation {
		return c.Body
	}
	body := strings.Join(strings.Fields(c.Body), " ")
	runes := []rune(body)
	if len(runes) <= maxAutomatedCommentPromptLength {
		return body
	}
	return string(runes[:maxAutomatedCommentPromptLength]) + "..."
}
