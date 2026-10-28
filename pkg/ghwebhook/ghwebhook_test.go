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

package ghwebhook

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestWebhookSignatureDeduplicationAndLongPollRelay(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	storePath := filepath.Join(dir, "events.jsonl")
	store, err := OpenStore(storePath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	const whKey = "wh_test_key"
	const relayKey = "relay_test_key"

	srv := httptest.NewServer(NewServer(ServerConfig{
		Store:         store,
		WebhookSecret: whKey,
		RelaySecret:   relayKey,
	}))
	defer srv.Close()

	// 1. Bad signature is rejected with 401.
	badReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/webhooks/github", bytes.NewReader([]byte(`{}`)))
	badReq.Header.Set(GitHubEventHeader, "issue_comment")
	badReq.Header.Set(GitHubDeliveryHeader, "deliv-1")
	badReq.Header.Set(GitHubSignatureHeader, "sha256=0000000000000000000000000000000000000000000000000000000000000000")
	badResp, err := http.DefaultClient.Do(badReq)
	if err != nil {
		t.Fatal(err)
	}
	badResp.Body.Close()
	if badResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad signature status = %d, want 401", badResp.StatusCode)
	}

	// 2. Ping returns 200 OK without adding an event.
	pingBody := []byte(`{"zen":"Keep it logically awesome."}`)
	pingReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/webhooks/github", bytes.NewReader(pingBody))
	pingReq.Header.Set(GitHubEventHeader, "ping")
	pingReq.Header.Set(GitHubDeliveryHeader, "deliv-ping")
	pingReq.Header.Set(GitHubSignatureHeader, SignGitHubPayload(whKey, pingBody))
	pingResp, err := http.DefaultClient.Do(pingReq)
	if err != nil {
		t.Fatal(err)
	}
	pingResp.Body.Close()
	if pingResp.StatusCode != http.StatusOK {
		t.Fatalf("ping status = %d, want 200", pingResp.StatusCode)
	}

	// 3. Start long-polling client before event arrives; verify immediate wakeup.
	client := &Client{BaseURL: srv.URL, RelaySecret: relayKey}
	done := make(chan EventsResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		res, err := client.Poll(context.Background(), 0, 10, 5*time.Second)
		if err != nil {
			errCh <- err
			return
		}
		done <- res
	}()

	time.Sleep(50 * time.Millisecond)
	payload := []byte(`{
		"action": "created",
		"repository": {"full_name": "acme/widgets"},
		"installation": {"id": 4242},
		"sender": {"login": "alice", "id": 100, "type": "User"},
		"issue": {"number": 17, "title": "Fix bug", "state": "open", "pull_request": {"url": "https://api.github.com/repos/acme/widgets/pulls/17"}},
		"comment": {"id": 555, "body": "@grain-bot please review this PR"}
	}`)
	postWebhook := func() int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/webhooks/github", bytes.NewReader(payload))
		req.Header.Set(GitHubEventHeader, "issue_comment")
		req.Header.Set(GitHubDeliveryHeader, "deliv-comment-1")
		req.Header.Set(GitHubSignatureHeader, SignGitHubPayload(whKey, payload))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if code := postWebhook(); code != http.StatusAccepted {
		t.Fatalf("valid webhook status = %d, want 202", code)
	}
	// Redelivering the exact same delivery ID is deduplicated idempotently.
	if code := postWebhook(); code != http.StatusAccepted {
		t.Fatalf("duplicate webhook status = %d, want 202", code)
	}

	select {
	case err := <-errCh:
		t.Fatalf("Poll failed: %v", err)
	case res := <-done:
		if len(res.Events) != 1 {
			t.Fatalf("events = %d, want 1", len(res.Events))
		}
		ev := res.Events[0]
		if ev.Seq != 1 || ev.Repo != "acme/widgets" || ev.Number != 17 || !ev.IsPullRequest || ev.CommentID != 555 {
			t.Fatalf("unexpected event: %+v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for long-poll wakeup")
	}

	// Reopening the JSONL store preserves sequence and deduplication.
	_ = store.Close()
	reopened, err := OpenStore(storePath)
	if err != nil {
		t.Fatalf("reopening store: %v", err)
	}
	defer reopened.Close()
	events, latest := reopened.After(0, 10)
	if latest != 1 || len(events) != 1 || events[0].DeliveryID != "deliv-comment-1" {
		t.Fatalf("reopened events = (%+v, %d)", events, latest)
	}
}
