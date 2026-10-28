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

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ghwebhook"
)

func TestGitHubWebhookConsumerPollsGatewayInvalidatesRepoAndPersistsCursor(t *testing.T) {
	dataDir := t.TempDir()
	gwStore, err := ghwebhook.OpenStore(filepath.Join(t.TempDir(), "gw.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer gwStore.Close()

	const (
		webhookSecret = "whsec_unit"
		relaySecret   = "relay_unit"
	)
	gw := httptest.NewServer(ghwebhook.NewServer(ghwebhook.ServerConfig{
		Store:         gwStore,
		WebhookSecret: webhookSecret,
		RelaySecret:   relaySecret,
	}))
	defer gw.Close()

	t.Setenv(githubWebhookRelaySecretEnv, relaySecret)
	cfg := Config{
		DataDir:          dataDir,
		GithubWebhookURL: gw.URL,
	}

	consumer, err := newGitHubWebhookConsumer(cfg, nil)
	if err != nil {
		t.Fatalf("newGitHubWebhookConsumer: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	woke := make(chan struct{}, 4)
	var invalidated atomic.Value
	if err := consumer.Start(ctx, func() {
		select {
		case woke <- struct{}{}:
		default:
		}
	}, func(repo string) {
		invalidated.Store(repo)
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	body := []byte(`{"action":"created","repository":{"full_name":"acme/widgets"},"sender":{"login":"alice","type":"User"},"issue":{"number":7,"title":"Bug"},"comment":{"id":99,"body":"@grain-bot help"}}`)
	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "issue_comment")
	req.Header.Set("X-GitHub-Delivery", "deliv-consumer-1")
	req.Header.Set("X-Hub-Signature-256", ghwebhook.SignGitHubPayload(webhookSecret, body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("gateway status = %d, want 202", resp.StatusCode)
	}

	select {
	case <-woke:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for consumer wakeup")
	}

	if got, _ := invalidated.Load().(string); got != "acme/widgets" {
		t.Errorf("invalidated repo = %q, want acme/widgets", got)
	}

	pending, err := consumer.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].CommentID != 99 {
		t.Fatalf("Pending() = %+v, want 1 event with comment 99", pending)
	}
	if err := consumer.Ack(pending[0].Seq); err != nil {
		t.Fatalf("Ack: %v", err)
	}

	afterAck, err := consumer.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(afterAck) != 0 {
		t.Fatalf("Pending() after Ack = %+v, want empty", afterAck)
	}

	// Reopening from the same DataDir restores LastAckedSeq so old events are skipped.
	reopened, err := newGitHubWebhookConsumer(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.ackedSeq != pending[0].Seq {
		t.Errorf("reopened ackedSeq = %d, want %d", reopened.ackedSeq, pending[0].Seq)
	}
}
