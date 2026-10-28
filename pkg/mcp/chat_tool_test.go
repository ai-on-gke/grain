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
