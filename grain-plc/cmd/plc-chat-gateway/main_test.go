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

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/grain-plc/pkg/grainclient"
	"github.com/ai-on-gke/grain/grain-plc/pkg/perf"
	"github.com/ai-on-gke/grain/grain-plc/pkg/plcseams"
)

func TestChatGatewayCommandsAndSSEForwarding(t *testing.T) {
	defer plcseams.ResetForTest()

	const secret = "chat-secret-123"
	adapter := plcseams.NewHMACChatAdapter(secret)
	plcseams.RegisterChatAdapter(adapter)

	var (
		gotOnBehalfOf string
		gotClientHdr  string
		gotReplyText  string
	)

	daemonSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotOnBehalfOf = r.Header.Get(plcseams.HeaderOnBehalfOf)
		gotClientHdr = r.Header.Get(plcseams.HeaderClient)

		if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "id: 1\nevent: task.question\ndata: {\"id\":1,\"type\":\"task.question\",\"taskId\":\"77\",\"externalRef\":\"plc:proj/alpha\",\"summary\":\"Keep old flag?\",\"question\":\"Should we keep the legacy flag?\"}\n\n")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/tasks":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(grainclient.Task{
				ID:       "77",
				Title:    "[PLC] Fix header parser",
				State:    "queued",
				Repo:     "acme/widgets",
				FiledBy:  gotOnBehalfOf,
				FiledVia: gotClientHdr,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/tasks/77/reply":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotReplyText = body["message"]
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "queued"})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.EscapedPath(), "/api/refs/"):
			_ = json.NewEncoder(w).Encode(grainclient.RefSummary{
				ExternalRef: "plc:proj/alpha",
				Counts:      grainclient.RefRollupCounts{Total: 1, Active: 1},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer daemonSrv.Close()

	client := grainclient.New(grainclient.Config{
		GrainURL:   daemonSrv.URL,
		ClientName: grainclient.ClientPLCChatGateway,
	})
	gw := NewGateway(client, perf.NewRecorder(32), "spaces/default")
	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	// 1. File a task via signed chat command.
	filePayload, _ := json.Marshal(plcseams.ChatInvocation{
		UserEmail:   "carol@example.com",
		SpaceID:     "spaces/eng",
		ThreadID:    "threads/t1",
		RawText:     "/grain file acme/widgets Fix header parser",
		ExternalRef: "plc:proj/alpha",
	})
	fileReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/chat/events", bytes.NewReader(filePayload))
	fileReq.Header.Set(plcseams.HeaderChatSignature, plcseams.SignPayload(secret, filePayload))
	fileResp, err := http.DefaultClient.Do(fileReq)
	if err != nil {
		t.Fatalf("POST /api/chat/events (file): %v", err)
	}
	fileResp.Body.Close()
	if fileResp.StatusCode != http.StatusOK {
		t.Fatalf("file status = %d, want 200", fileResp.StatusCode)
	}
	if gotOnBehalfOf != "carol@example.com" || gotClientHdr != grainclient.ClientPLCChatGateway {
		t.Fatalf("delegation headers: on-behalf-of=%q client=%q", gotOnBehalfOf, gotClientHdr)
	}

	// 2. WatchSSE forwards task.question event to the bound thread in spaces/eng.
	if err := gw.WatchSSE(context.Background(), 0); err != nil {
		t.Fatalf("WatchSSE: %v", err)
	}
	outbox := adapter.Outbox()
	if len(outbox) < 2 {
		t.Fatalf("expected at least 2 outbox messages (file reply + question event), got %+v", outbox)
	}
	last := outbox[len(outbox)-1]
	if last.SpaceID != "spaces/eng" || last.ThreadID != "threads/t1" || last.EventType != grainclient.EventTaskQuestion {
		t.Fatalf("forwarded event message = %+v", last)
	}

	// 3. Reply to the question from chat.
	replyPayload, _ := json.Marshal(plcseams.ChatInvocation{
		UserEmail: "carol@example.com",
		SpaceID:   "spaces/eng",
		ThreadID:  "threads/t1",
		RawText:   "/grain reply #77 Remove the legacy flag",
	})
	replyReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/chat/events", bytes.NewReader(replyPayload))
	replyReq.Header.Set(plcseams.HeaderChatSignature, plcseams.SignPayload(secret, replyPayload))
	replyResp, err := http.DefaultClient.Do(replyReq)
	if err != nil {
		t.Fatalf("POST /api/chat/events (reply): %v", err)
	}
	replyResp.Body.Close()
	if replyResp.StatusCode != http.StatusOK || gotReplyText != "Remove the legacy flag" {
		t.Fatalf("reply status=%d gotReplyText=%q", replyResp.StatusCode, gotReplyText)
	}
}

// TestOutboundURLDeliversForwardedEvents runs the command as an operator
// does -- -watch-sse with -outbound-url -- and reads what reached the chat
// platform: a comment_on_issue note (task.comment) is not announced as a
// parked question, a real question is, once.
func TestOutboundURLDeliversForwardedEvents(t *testing.T) {
	defer plcseams.ResetForTest()

	const secret = "chat-secret-456"
	daemonSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w,
			"id: 1\nevent: task.comment\ndata: {\"id\":\"1\",\"type\":\"task.comment\",\"taskId\":\"41\",\"commentId\":\"7\"}\n\n"+
				"id: 2\nevent: task.state\ndata: {\"id\":\"2\",\"type\":\"task.state\",\"taskId\":\"41\",\"state\":\"awaiting_reply\",\"questionSummary\":\"Report: nothing to fix\"}\n\n"+
				"id: 3\nevent: task.question\ndata: {\"id\":\"3\",\"type\":\"task.question\",\"taskId\":\"42\",\"questionSummary\":\"Keep the v1 API?\"}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer daemonSrv.Close()

	got := make(chan plcseams.ChatMessage, 8)
	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if sig := r.Header.Get(plcseams.HeaderChatSignature); sig != plcseams.SignPayload(secret, body) {
			t.Errorf("outbound signature = %q, want the HMAC of the body", sig)
		}
		var msg plcseams.ChatMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			t.Errorf("outbound body %q: %v", body, err)
		}
		got <- msg
	}))
	defer chatSrv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, []string{
			"-listen", "127.0.0.1:0",
			"-grain-url", daemonSrv.URL,
			"-webhook-secret", secret,
			"-default-space", "spaces/ops",
			"-outbound-url", chatSrv.URL,
			"-watch-sse",
		}, io.Discard, io.Discard)
	}()
	defer func() {
		cancel()
		<-done
	}()

	var msgs []plcseams.ChatMessage
	for len(msgs) < 3 {
		select {
		case m := <-got:
			msgs = append(msgs, m)
		case <-time.After(10 * time.Second):
			t.Fatalf("chat platform got %d messages, want 3: %+v", len(msgs), msgs)
		}
	}
	parked := 0
	for _, m := range msgs {
		if m.SpaceID != "spaces/ops" {
			t.Errorf("message posted to %q, want the default space: %+v", m.SpaceID, m)
		}
		if strings.Contains(m.Text, "parked on a question") {
			parked++
			if want := "Task #42 is parked on a question: Keep the v1 API?"; m.Text != want {
				t.Errorf("question message = %q, want %q", m.Text, want)
			}
			if m.TaskID != "42" {
				t.Errorf("task %s announced as parked on a question: %q", m.TaskID, m.Text)
			}
		}
	}
	if parked != 1 {
		t.Errorf("%d parked-on-a-question messages, want 1: %+v", parked, msgs)
	}
}

// TestOutboundURLOutlivesASlowOrFailingChatPlatform: a POST the chat
// platform never answers, and one it refuses, each cost only their own
// message; the events after them still arrive.
func TestOutboundURLOutlivesASlowOrFailingChatPlatform(t *testing.T) {
	defer plcseams.ResetForTest()
	defer func(d time.Duration) { outboundTimeout = d }(outboundTimeout)
	outboundTimeout = 200 * time.Millisecond

	daemonSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for i, task := range []string{"1", "2", "3"} {
			_, _ = fmt.Fprintf(w, "id: %d\nevent: task.question\ndata: {\"id\":\"%d\",\"type\":\"task.question\",\"taskId\":\"%s\",\"questionSummary\":\"Q%s\"}\n\n", i+1, i+1, task, task)
		}
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer daemonSrv.Close()

	got := make(chan plcseams.ChatMessage, 8)
	release := make(chan struct{})
	defer close(release)
	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg plcseams.ChatMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		switch msg.TaskID {
		case "1": // slow: never answers within the timeout
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		case "2": // failing
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		got <- msg
	}))
	defer chatSrv.Close()

	var stderr syncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, []string{
			"-listen", "127.0.0.1:0",
			"-grain-url", daemonSrv.URL,
			"-default-space", "spaces/ops",
			"-outbound-url", chatSrv.URL,
			"-watch-sse",
		}, io.Discard, &stderr)
	}()
	defer func() {
		cancel()
		<-done
	}()

	select {
	case m := <-got:
		if m.TaskID != "3" {
			t.Fatalf("delivered %+v, want task 3's message", m)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("task 3's message never arrived behind a slow and a failing POST; stderr:\n%s", stderr.String())
	}
	for _, want := range []string{"task 1 task.question not delivered", "task 2 task.question not delivered"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr.String())
		}
	}
	if strings.Contains(stderr.String(), "stream ended") {
		t.Errorf("a delivery failure ended the event stream:\n%s", stderr.String())
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
