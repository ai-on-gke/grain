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
	"testing"
	"time"
)

// fakeChatter records what was said and how long the tool was willing to
// wait for an answer, and hands back whatever this test wants the person
// on the other end to have said.
type fakeChatter struct {
	said        []string
	summaries   []string
	attachments [][]ResponseAttachment
	waits       []time.Duration
	turn        ChatTurn
	err         error
}

func (f *fakeChatter) Chat(_ context.Context, message, summary string, attachments []ResponseAttachment, wait time.Duration) (ChatTurn, error) {
	f.said = append(f.said, message)
	f.summaries = append(f.summaries, summary)
	f.attachments = append(f.attachments, attachments)
	f.waits = append(f.waits, wait)
	return f.turn, f.err
}

// chat calls the tool the way a client would: by name, off the
// constructor, with whatever arguments the model sent.
func chat(t *testing.T, ctx context.Context, chatter Chatter, args map[string]any) Result {
	t.Helper()
	tools := NewChatTools(chatter, nil)
	if len(tools) != 1 || tools[0].Name != "chat" {
		t.Fatalf("NewChatTools returned %+v, want one chat tool", tools)
	}
	return tools[0].Handler(ctx, args)
}

// The whole point: what the person said comes back as this call's own
// result, so the conversation happens inside the run rather than costing
// a dispatch each way.
func TestChatReturnsTheReplyToTheSameCall(t *testing.T) {
	chatter := &fakeChatter{turn: ChatTurn{Reply: "the second one, and rename the flag"}}

	res := chat(t, context.Background(), chatter, map[string]any{
		"message": "Two ways to do this. Which did you mean?",
		"attachments": []any{map[string]any{
			"filename": "diagram.png", "content_type": "image/png", "content": "cG5n",
		}},
	})
	if res.IsError {
		t.Fatalf("chat reported an error: %s", res.Text)
	}
	if len(chatter.said) != 1 || chatter.said[0] != "Two ways to do this. Which did you mean?" {
		t.Fatalf("chatter got %q, want the message as written", chatter.said)
	}
	if len(chatter.attachments) != 1 || len(chatter.attachments[0]) != 1 ||
		chatter.attachments[0][0].Filename != "diagram.png" ||
		string(chatter.attachments[0][0].Content) != "png" {
		t.Fatalf("attachments = %+v, want the decoded diagram", chatter.attachments)
	}
	if !strings.Contains(res.Text, "the second one, and rename the flag") {
		t.Errorf("text = %q, want the reply in it", res.Text)
	}
}

// Nobody answering is an ordinary ending, not a failure -- and what it
// means for the run has to be said, because the run's next move follows
// from it: stop, rather than wait again.
func TestChatSaysWhatAnUnansweredTurnMeans(t *testing.T) {
	chatter := &fakeChatter{}

	res := chat(t, context.Background(), chatter, map[string]any{"message": "still there?"})
	if res.IsError {
		t.Fatalf("an unanswered chat turn is not an error: %s", res.Text)
	}
	for _, want := range []string{"parked", "fresh run"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("text = %q, want it to mention %q", res.Text, want)
		}
	}
}

// A wait is bounded by the run as well as by the tool: a turn that
// blocked past the wall would be cancelled with the answer unread.
func TestChatClampsTheWaitToWhatTheRunHasLeft(t *testing.T) {
	for name, tc := range map[string]struct {
		ctx  context.Context
		want time.Duration
	}{
		// "nobody told this server about a deadline" is not "no time
		// left" -- the same rule runDeadlineRemaining states.
		"no deadline announced": {ctx: context.Background(), want: ChatWait},
		"time to spare":         {ctx: runWithLeft(4 * time.Hour), want: ChatWait},
		"less than a full turn": {ctx: runWithLeft(20 * time.Minute), want: 18 * time.Minute},
	} {
		t.Run(name, func(t *testing.T) {
			chatter := &fakeChatter{turn: ChatTurn{Reply: "yes"}}
			if res := chat(t, tc.ctx, chatter, map[string]any{"message": "?"}); res.IsError {
				t.Fatalf("chat reported an error: %s", res.Text)
			}
			if got := chatter.waits[0]; got != tc.want {
				t.Errorf("waited up to %v, want %v", got, tc.want)
			}
		})
	}
}

// A run with no room left to wait still says its piece: the message and
// the park are worth having on their own, since the person's reply is
// what dispatches the next run.
func TestChatPostsWithoutWaitingWhenTheRunIsNearlyOver(t *testing.T) {
	chatter := &fakeChatter{}

	res := chat(t, runWithLeft(90*time.Second), chatter, map[string]any{"message": "which did you mean?"})
	if res.IsError {
		t.Fatalf("chat reported an error: %s", res.Text)
	}
	if len(chatter.said) != 1 || chatter.waits[0] != 0 {
		t.Fatalf("chatter got %q waiting %v, want the message posted with no wait", chatter.said, chatter.waits)
	}
	if !strings.Contains(res.Text, "deadline") {
		t.Errorf("text = %q, want it to say why it did not wait", res.Text)
	}
}

// The two refusals, both before the daemon is reached. A run whose
// mcpserver has no daemon to ask is told what to use instead rather than
// left to work it out.
func TestChatRefusesWhatItCannotSend(t *testing.T) {
	if res := chat(t, context.Background(), nil, map[string]any{"message": "hello"}); !res.IsError ||
		!strings.Contains(res.Text, "comment_on_issue") {
		t.Errorf("result = %+v, want a refusal naming what to use instead", res)
	}
	chatter := &fakeChatter{}
	if res := chat(t, context.Background(), chatter, map[string]any{"message": "  "}); !res.IsError {
		t.Errorf("result = %+v, want an empty message refused", res)
	}
	if len(chatter.said) != 0 {
		t.Errorf("chatter got %q, want nothing sent", chatter.said)
	}
}

// A failure to reach the conversation is reported as one: the agent has
// to be able to tell "nobody answered" from "the message never arrived",
// because only one of them means their words are in the thread.
func TestChatReportsAFailureToReachTheDaemon(t *testing.T) {
	chatter := &fakeChatter{err: errors.New("connection refused")}

	res := chat(t, context.Background(), chatter, map[string]any{"message": "hello"})
	if !res.IsError || !strings.Contains(res.Text, "connection refused") {
		t.Errorf("result = %+v, want the failure reported", res)
	}
}

// The cap both agent CLIs set on a single tool call is sized from this,
// so a chat turn has to be inside it (agent/claude's mcpToolTimeout,
// agent/antigravity's mcpToolTimeoutSeconds).
func TestMaxBlockingToolWaitCoversEveryWaitingTool(t *testing.T) {
	if MaxBlockingToolWait < ChatWait || MaxBlockingToolWait < MaxWaitForChecksTimeout {
		t.Errorf("MaxBlockingToolWait = %v, want at least %v and %v",
			MaxBlockingToolWait, ChatWait, MaxWaitForChecksTimeout)
	}
}

type trackingAckAddenda struct {
	fakeAddenda
	acked []int64
}

func (a *trackingAckAddenda) Delivered(_ context.Context, through int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.acked = append(a.acked, through)
}

func (a *trackingAckAddenda) ackedIDs() []int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]int64(nil), a.acked...)
}

// When a comment arrives and is appended to the final tool call of a run,
// Delivered must NOT be called until the agent makes a subsequent tool call.
// If the agent ends its turn right after that tool call without acting on the
// addendum, DeliveredCommentID stays unadvanced so the orchestrator immediately
// re-queues the task for the unanswered comment.
func TestAddendaDeliveredOnFinalToolCallIsNotAckedUntilNextToolCall(t *testing.T) {
	t.Parallel()
	conversation := &trackingAckAddenda{}
	registry := NewRegistry()
	registry.AnnounceAddenda(0, conversation)
	registry.Register(Tool{
		Name:        "run_command",
		InputSchema: map[string]any{"type": "object"},
		Handler:     func(context.Context, map[string]any) Result { return Result{Text: "ok"} },
	})
	client := NewInProcess(context.Background(), registry)
	t.Cleanup(func() { client.Close() })

	conversation.append("ok, did that do it?")

	// First tool call: the comment is appended to the result, but NOT yet
	// acknowledged as delivered because the agent might exit its turn right after.
	res1, err := client.CallTool(context.Background(), "run_command", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res1.Text(), "> ok, did that do it?") {
		t.Fatalf("first result = %q, want addendum appended", res1.Text())
	}
	if got := conversation.ackedIDs(); len(got) != 0 {
		t.Fatalf("acked after first tool call = %v, want none until next tool call", got)
	}

	// Second tool call: proves the agent continued past the first tool result,
	// so the addendum from the first tool call is now acknowledged.
	res2, err := client.CallTool(context.Background(), "run_command", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Text() != "ok" {
		t.Fatalf("second result = %q, want plain 'ok'", res2.Text())
	}
	if got := conversation.ackedIDs(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("acked after second tool call = %v, want [1]", got)
	}
}

// Turn-ending tools (comment_on_issue, ask_question, request_secret,
// nothing_needed, resume_later, and an unanswered chat call) must not append
// or consume a newly arrived comment at the end of the tool call, because the
// agent ends its turn after calling them.
func TestTurnEndingToolsDoNotConsumeNewAddenda(t *testing.T) {
	t.Parallel()
	conversation := &trackingAckAddenda{}
	registry := NewRegistry()
	registry.AnnounceAddenda(0, conversation)
	registry.Register(Tool{
		Name:        "run_command",
		InputSchema: map[string]any{"type": "object"},
		Handler:     func(context.Context, map[string]any) Result { return Result{Text: "cmd ok"} },
	})
	registry.Register(Tool{
		Name:        "comment_on_issue",
		InputSchema: map[string]any{"type": "object"},
		Handler:     func(context.Context, map[string]any) Result { return Result{Text: "Comment posted."} },
	})
	for _, tool := range NewChatTools(&fakeChatter{}, nil) {
		registry.Register(tool)
	}
	client := NewInProcess(context.Background(), registry)
	t.Cleanup(func() { client.Close() })

	// A comment arrives before comment_on_issue (the final tool call of the run).
	conversation.append("follow-up question while finishing")
	res, err := client.CallTool(context.Background(), "comment_on_issue", map[string]any{"comment": "done"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Text(), "follow-up question while finishing") {
		t.Fatalf("comment_on_issue result = %q, want turn-ending tool not to consume addendum", res.Text())
	}
	if got := conversation.ackedIDs(); len(got) != 0 {
		t.Fatalf("acked = %v, want none", got)
	}

	// Nor does an unanswered chat call consume it as an addendum.
	resChat, err := client.CallTool(context.Background(), "chat", map[string]any{"message": "still there?"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(resChat.Text(), "follow-up question while finishing") {
		t.Fatalf("unanswered chat result = %q, want turn-ending tool not to consume addendum", resChat.Text())
	}

	// If a regular tool call runs afterwards, the comment is still waiting and
	// delivered then.
	resCmd, err := client.CallTool(context.Background(), "run_command", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resCmd.Text(), "> follow-up question while finishing") {
		t.Fatalf("run_command result = %q, want preserved addendum delivered", resCmd.Text())
	}
}
