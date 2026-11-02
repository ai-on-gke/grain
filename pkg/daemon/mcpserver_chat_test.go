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

package daemon

// daemonChat is the half of a chat turn that actually waits: it posts the
// run's message over the daemon's REST API and then reads that
// conversation back until a person answers. These drive it against a
// stand-in daemon, because what is worth pinning down here is the loop --
// that it keeps asking, that it stops on the first thing a person says,
// and that a wait which ends with nobody there is an ordinary answer
// rather than an error.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// chatDaemon is a grain daemon as far as daemonChat is concerned: the two
// /messages routes, with the replies this test wants handed out on the
// polls it names.
type chatDaemon struct {
	mu sync.Mutex
	// posted is what the run said, in order.
	posted []string
	// polls counts the reads of the conversation.
	polls int
	// replyOnPoll is the poll that finally answers; 0 never answers.
	replyOnPoll   int
	reply         []string
	replyMessages []ui.Message
	attachments   map[int64][]byte
}

func (d *chatDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if strings.Contains(r.URL.Path, "/attachments/") {
		parts := strings.Split(r.URL.Path, "/")
		idStr := parts[len(parts)-1]
		if id, err := strconv.ParseInt(idStr, 10, 64); err == nil && d.attachments != nil {
			if data, ok := d.attachments[id]; ok {
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Write(data)
				return
			}
		}
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodPost {
		var req struct {
			Body string `json:"body"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		d.posted = append(d.posted, req.Body)
		json.NewEncoder(w).Encode(ui.Message{ID: 7, Body: req.Body, AuthorKind: "automation"})
		return
	}
	d.polls++
	messages := []ui.Message{}
	if d.replyOnPoll > 0 && d.polls >= d.replyOnPoll {
		if len(d.replyMessages) > 0 {
			messages = append(messages, d.replyMessages...)
		} else {
			for i, body := range d.reply {
				messages = append(messages, ui.Message{ID: int64(8 + i), AuthorKind: "human", Body: body})
			}
		}
	}
	json.NewEncoder(w).Encode(messages)
}

func chatAgainst(t *testing.T, daemon *chatDaemon) daemonChat {
	t.Helper()
	srv := httptest.NewServer(daemon)
	t.Cleanup(srv.Close)
	return daemonChat{client: ui.NewHTTPClient(srv.URL), taskID: "t-1", poll: time.Millisecond}
}

// The turn: say something, keep asking, and come back with what the
// person said the moment they say it.
func TestDaemonChatWaitsForTheReply(t *testing.T) {
	t.Parallel()
	daemon := &chatDaemon{replyOnPoll: 3, reply: []string{"the second one"}}
	chat := chatAgainst(t, daemon)

	turn, err := chat.Chat(context.Background(), "which did you mean?", "", nil, time.Minute)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if turn.Reply != "the second one" {
		t.Errorf("reply = %q, want what the person said", turn.Reply)
	}
	if len(daemon.posted) != 1 || daemon.posted[0] != "which did you mean?" {
		t.Errorf("posted %q, want the run's message", daemon.posted)
	}
	if daemon.polls < 3 {
		t.Errorf("polled %d times, want it to have kept asking until answered", daemon.polls)
	}
}

// Somebody typing three lines has said one thing in three messages, so
// all of them come back: answering only the first would act on a third of
// what the run was told.
func TestDaemonChatReturnsEverythingSaidSince(t *testing.T) {
	t.Parallel()
	daemon := &chatDaemon{replyOnPoll: 1, reply: []string{"the second one", "and rename the flag"}}

	turn, err := chatAgainst(t, daemon).Chat(context.Background(), "which?", "", nil, time.Minute)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.Contains(turn.Reply, "the second one") || !strings.Contains(turn.Reply, "rename the flag") {
		t.Errorf("reply = %q, want both messages", turn.Reply)
	}
}

// Nobody there is an answer of its own, not a failure: the message is in
// the conversation, the task is parked on it, and the tool has a sentence
// to say about what that means for the run.
func TestDaemonChatGivesUpQuietlyWhenNobodyAnswers(t *testing.T) {
	t.Parallel()
	daemon := &chatDaemon{}

	turn, err := chatAgainst(t, daemon).Chat(context.Background(), "still there?", "", nil, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("an unanswered turn is not an error: %v", err)
	}
	if turn.Reply != "" {
		t.Errorf("reply = %q, want nothing", turn.Reply)
	}
	if len(daemon.posted) != 1 {
		t.Errorf("posted %q, want the message said anyway", daemon.posted)
	}
}

// A run with no room left to wait still says its piece -- and does not
// spend a single poll on a wait it was told not to take.
func TestDaemonChatPostsWithoutWaiting(t *testing.T) {
	t.Parallel()
	daemon := &chatDaemon{}

	turn, err := chatAgainst(t, daemon).Chat(context.Background(), "one last thing", "", nil, 0)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if turn.Reply != "" || len(daemon.posted) != 1 || daemon.polls != 0 {
		t.Errorf("turn = %+v after %d polls, want the message posted and no wait", turn, daemon.polls)
	}
}

// A message that never arrives is reported, because the agent's next move
// depends on the difference: "nobody answered" means the words are in the
// thread, and this does not.
func TestDaemonChatReportsAFailedPost(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"no such task"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	chat := daemonChat{client: ui.NewHTTPClient(srv.URL), taskID: "t-1", poll: time.Millisecond}
	if _, err := chat.Chat(context.Background(), "hello", "", nil, time.Minute); err == nil {
		t.Fatal("Chat returned no error for a message that was never posted")
	}
}

func TestDaemonChatPlacesAttachmentsAndFormatsReply(t *testing.T) {
	t.Parallel()
	daemon := &chatDaemon{
		replyOnPoll: 1,
		replyMessages: []ui.Message{
			{
				ID:         12,
				AuthorKind: "human",
				Body:       "see attached spec",
				Attachments: []ui.Attachment{
					{ID: 55, Filename: "spec.pdf", Size: 4},
				},
			},
		},
		attachments: map[int64][]byte{
			55: []byte("pdf-bytes"),
		},
	}
	var writtenPath, writtenContent string
	writeFile := func(ctx context.Context, args map[string]any) mcp.Result {
		writtenPath, _ = args["file_path"].(string)
		writtenContent, _ = args["content"].(string)
		return mcp.Result{Text: "ok"}
	}
	srv := httptest.NewServer(daemon)
	t.Cleanup(srv.Close)
	chat := daemonChat{client: ui.NewHTTPClient(srv.URL), taskID: "t-1", poll: time.Millisecond, writeFile: writeFile}

	turn, err := chat.Chat(context.Background(), "can you share the spec?", "", nil, time.Minute)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if writtenPath != "attachments/55-spec.pdf" {
		t.Errorf("writtenPath = %q, want 'attachments/55-spec.pdf'", writtenPath)
	}
	if writtenContent != "pdf-bytes" {
		t.Errorf("writtenContent = %q, want 'pdf-bytes'", writtenContent)
	}
	if !strings.Contains(turn.Reply, "see attached spec") || !strings.Contains(turn.Reply, "./attachments/55-spec.pdf") {
		t.Errorf("turn.Reply = %q, want text and attachment link", turn.Reply)
	}
}

// restartingDaemon is chatDaemon through an upgrade: after the run's
// message is posted, the polls from down through up-1 are answered 503,
// as the reverse proxy in front of a restarting daemon answers, and the
// person's reply lands on the new daemon at poll replyOn.
type restartingDaemon struct {
	chatDaemon
	down, up int
}

func (d *restartingDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		d.mu.Lock()
		poll := d.polls + 1
		d.mu.Unlock()
		if poll >= d.down && poll < d.up {
			d.mu.Lock()
			d.polls++
			d.mu.Unlock()
			http.Error(w, "restarting", http.StatusServiceUnavailable)
			return
		}
	}
	d.chatDaemon.ServeHTTP(w, r)
}

// A chat turn waiting on a person outlives a daemon upgrade: the wait is
// in the CLI's own mcpserver, so a daemon that stops answering part way
// through is a gap in the polls, not the end of the turn, and a reply
// posted to the new daemon after the restart comes back as the answer.
// The client is given no retry of its own here, so it is the poll loop
// -- not ui.RetryPolicy -- that carries the wait across the gap.
func TestDaemonChatWaitsThroughADaemonRestart(t *testing.T) {
	t.Parallel()
	daemon := &restartingDaemon{chatDaemon: chatDaemon{replyOnPoll: 12, reply: []string{"after the upgrade: the second one"}},
		down: 2, up: 10}
	srv := httptest.NewServer(daemon)
	t.Cleanup(srv.Close)
	client := ui.NewHTTPClient(srv.URL)
	client.RetryPolicy = &ui.RetryPolicy{}
	chat := daemonChat{client: client, taskID: "t-1", poll: time.Millisecond}

	turn, err := chat.Chat(context.Background(), "which did you mean?", "", nil, time.Minute)
	if err != nil {
		t.Fatalf("Chat across the restart: %v", err)
	}
	if turn.Reply != "after the upgrade: the second one" {
		t.Errorf("reply = %q, want the reply posted after the restart", turn.Reply)
	}
	if daemon.polls < 12 {
		t.Errorf("polled %d times, want it to have kept asking through the gap", daemon.polls)
	}
}

// A daemon that is still down when the wait runs out does not hold the
// turn past it: the client's restart-gap retry (two minutes by default)
// is bounded by the wait's own deadline, and the turn ends as one nobody
// answered, not as an error.
func TestDaemonChatDoesNotOverrunItsWaitInARestartGap(t *testing.T) {
	t.Parallel()
	daemon := &restartingDaemon{down: 1, up: 1 << 30}
	srv := httptest.NewServer(daemon)
	t.Cleanup(srv.Close)
	chat := daemonChat{client: ui.NewHTTPClient(srv.URL), taskID: "t-1", poll: time.Millisecond}

	started := time.Now()
	turn, err := chat.Chat(context.Background(), "still there?", "", nil, 300*time.Millisecond)
	if err != nil || turn.Reply != "" {
		t.Fatalf("Chat = %+v, %v; want an unanswered turn", turn, err)
	}
	if took := time.Since(started); took > 10*time.Second {
		t.Errorf("the turn took %s, past its 300ms wait: a poll's retry held it", took)
	}
}

func TestDaemonChatRetriesWhenAttachmentPlacementFailsTransiently(t *testing.T) {
	t.Parallel()
	daemon := &chatDaemon{
		replyOnPoll: 1,
		replyMessages: []ui.Message{
			{
				ID:         15,
				AuthorKind: "human",
				Body:       "attached log",
				Attachments: []ui.Attachment{
					{ID: 66, Filename: "app.log", Size: 4},
				},
			},
		},
		attachments: map[int64][]byte{
			66: []byte("boom"),
		},
	}
	var attempts int
	var writtenContent string
	writeFile := func(ctx context.Context, args map[string]any) mcp.Result {
		attempts++
		if attempts == 1 {
			return mcp.Result{Text: "transient exec error", IsError: true}
		}
		writtenContent, _ = args["content"].(string)
		return mcp.Result{Text: "ok"}
	}
	srv := httptest.NewServer(daemon)
	t.Cleanup(srv.Close)
	var acked []int64
	cursor := &deliveredCursor{
		report: func(_ context.Context, through int64) error {
			acked = append(acked, through)
			return nil
		},
		byChat: map[int64]bool{},
	}
	chat := daemonChat{client: ui.NewHTTPClient(srv.URL), taskID: "t-1", poll: time.Millisecond, writeFile: writeFile, delivered: cursor}

	turn, err := chat.Chat(context.Background(), "send log?", "", nil, time.Minute)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if attempts < 2 {
		t.Fatalf("writeFile attempts = %d, want retry after initial failure", attempts)
	}
	if writtenContent != "boom" {
		t.Errorf("writtenContent = %q, want 'boom'", writtenContent)
	}
	if !strings.Contains(turn.Reply, "./attachments/66-app.log") {
		t.Errorf("turn.Reply = %q, want attachment path", turn.Reply)
	}
	if len(acked) != 1 || acked[0] != 15 {
		t.Errorf("acked = %v, want [15] recorded only once placement succeeded", acked)
	}
}
