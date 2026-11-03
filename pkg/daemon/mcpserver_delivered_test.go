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

// deliveredCursor is how the daemon learns what a run has been shown of
// its conversation, which is what the end of an interactive run is judged
// by (model.Observation.DeliveredCommentID, grain/task-1774).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// deliveryDaemon serves the three routes a run's conversation goes
// through: the run's own message, the read of what was said since, and
// the report of what the run has now been shown.
type deliveryDaemon struct {
	mu        sync.Mutex
	said      []ui.Message
	delivered []int64
}

func (d *deliveryDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/delivered"):
		var req struct {
			Through int64 `json:"through"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		d.delivered = append(d.delivered, req.Through)
		json.NewEncoder(w).Encode(map[string]any{"through": req.Through})
	case r.Method == http.MethodPost:
		json.NewEncoder(w).Encode(ui.Message{ID: 7, AuthorKind: "automation"})
	default:
		var after int64
		json.Unmarshal([]byte(r.URL.Query().Get("after")), &after)
		out := []ui.Message{}
		for _, m := range d.said {
			if m.ID > after {
				out = append(out, m)
			}
		}
		json.NewEncoder(w).Encode(out)
	}
}

func (d *deliveryDaemon) reports() []int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]int64(nil), d.delivered...)
}

// A chat reply counts as delivered the moment it is handed back, and is
// not read out to the run a second time as an addendum on the same
// answer -- while a message from before the run's question, which the
// reply did not carry, still is.
func TestAChatReplyIsDeliveredOnceAndReported(t *testing.T) {
	t.Parallel()
	daemon := &deliveryDaemon{said: []ui.Message{
		{ID: 5, AuthorKind: "human", Body: "said before the question"},
		{ID: 8, AuthorKind: "human", Body: "the reply"},
	}}
	srv := httptest.NewServer(daemon)
	t.Cleanup(srv.Close)
	client := ui.NewHTTPClient(srv.URL)
	cursor := newDeliveredCursor(client, "t-1", 3)
	chat := daemonChat{client: client, taskID: "t-1", poll: time.Millisecond, delivered: cursor}
	addenda := daemonAddenda{client: client, taskID: "t-1", delivered: cursor}

	turn, err := chat.Chat(context.Background(), "which?", "", nil, time.Minute)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if turn.Reply != "the reply" {
		t.Fatalf("reply = %q, want the person's reply", turn.Reply)
	}
	if got := daemon.reports(); len(got) != 1 || got[0] != 8 {
		t.Fatalf("reported %v, want the reply reported delivered through 8", got)
	}

	found, err := addenda.Addenda(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].ID != 5 {
		t.Fatalf("addenda = %+v, want only the message the reply did not carry", found)
	}
	// Delivering that one moves nothing: the mark is already past it.
	addenda.Delivered(context.Background(), 5)
	if got := daemon.reports(); len(got) != 1 {
		t.Errorf("reported %v, want no report that would move the mark backwards", got)
	}
}

func TestMultiMessageChatTurnAdvancesCursorToMaxIDAndFiltersAllFromAddenda(t *testing.T) {
	t.Parallel()
	daemon := &deliveryDaemon{said: []ui.Message{
		{ID: 4, AuthorKind: "human", Body: "comment posted before chat question"},
		{ID: 9, AuthorKind: "human", Body: "first part of reply"},
		{ID: 12, AuthorKind: "human", Body: "second part of reply"},
	}}
	srv := httptest.NewServer(daemon)
	t.Cleanup(srv.Close)
	client := ui.NewHTTPClient(srv.URL)
	cursor := newDeliveredCursor(client, "t-multi", 3)
	chat := daemonChat{client: client, taskID: "t-multi", poll: time.Millisecond, delivered: cursor}
	addenda := daemonAddenda{client: client, taskID: "t-multi", delivered: cursor}

	turn, err := chat.Chat(context.Background(), "status?", "", nil, time.Minute)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if turn.Reply != "first part of reply\n\nsecond part of reply" {
		t.Fatalf("reply = %q, want both human replies joined", turn.Reply)
	}
	if got := daemon.reports(); len(got) != 1 || got[0] != 12 {
		t.Fatalf("reported %v, want single delivery report through max ID 12", got)
	}

	// Both chat reply messages (9 and 12) must be filtered out of Addenda, leaving only ID 4.
	found, err := addenda.Addenda(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].ID != 4 {
		t.Fatalf("addenda = %+v, want only message 4 (with 9 and 12 filtered as chat replies)", found)
	}

	// Out-of-order slice passed to deliveredByChat still advances cursor to the maximum ID.
	cursor.deliveredByChat(context.Background(), []ui.Message{{ID: 20}, {ID: 15}})
	if got := daemon.reports(); len(got) != 2 || got[1] != 20 {
		t.Fatalf("reported %v after out-of-order deliveredByChat, want [12 20]", got)
	}
}
