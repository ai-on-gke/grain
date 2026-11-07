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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
