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
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ghwebhook"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
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

	consumer, err := newGitHubWebhookConsumer(context.Background(), cfg, nil, nil)
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
	reopened, err := newGitHubWebhookConsumer(context.Background(), cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.ackedSeq != pending[0].Seq {
		t.Errorf("reopened ackedSeq = %d, want %d", reopened.ackedSeq, pending[0].Seq)
	}
}

func TestGitHubWebhookConsumerPersistsCursorInPostgresAcrossEphemeralDataDirs(t *testing.T) {
	postgrestest.SkipUnlessAvailable(t)
	store, _ := postgrestest.Open(t)

	gwStore, err := ghwebhook.OpenStore(filepath.Join(t.TempDir(), "gw.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer gwStore.Close()

	const (
		webhookSecret = "whsec_pg"
		relaySecret   = "relay_pg"
	)
	gw := httptest.NewServer(ghwebhook.NewServer(ghwebhook.ServerConfig{
		Store:         gwStore,
		WebhookSecret: webhookSecret,
		RelaySecret:   relaySecret,
	}))
	defer gw.Close()

	t.Setenv(githubWebhookRelaySecretEnv, relaySecret)

	scratchDir1 := t.TempDir()
	cfg1 := Config{
		DataDir:          scratchDir1,
		DbDriver:         "postgres",
		GithubWebhookURL: gw.URL,
	}

	consumer1, err := NewGitHubWebhookConsumerWithStore(context.Background(), cfg1, nil, store)
	if err != nil {
		t.Fatalf("NewGitHubWebhookConsumerWithStore(vm1): %v", err)
	}

	ctx1, cancel1 := context.WithCancel(context.Background())
	woke1 := make(chan struct{}, 4)
	if err := consumer1.Start(ctx1, func() {
		select {
		case woke1 <- struct{}{}:
		default:
		}
	}, nil); err != nil {
		cancel1()
		t.Fatalf("consumer1.Start: %v", err)
	}

	sendDelivery := func(deliveryID string, commentID string) {
		t.Helper()
		body := []byte(`{"action":"created","repository":{"full_name":"acme/widgets"},"sender":{"login":"alice","type":"User"},"issue":{"number":7,"title":"Bug"},"comment":{"id":` + commentID + `,"body":"@grain-bot ping"}}`)
		req, _ := http.NewRequest(http.MethodPost, gw.URL+"/webhooks/github", bytes.NewReader(body))
		req.Header.Set("X-GitHub-Event", "issue_comment")
		req.Header.Set("X-GitHub-Delivery", deliveryID)
		req.Header.Set("X-Hub-Signature-256", ghwebhook.SignGitHubPayload(webhookSecret, body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("gateway status = %d, want 202", resp.StatusCode)
		}
	}

	sendDelivery("deliv-pg-1", "101")
	select {
	case <-woke1:
	case <-time.After(3 * time.Second):
		cancel1()
		t.Fatal("timed out waiting for consumer1 wakeup")
	}

	pending1, err := consumer1.Pending()
	if err != nil || len(pending1) != 1 {
		cancel1()
		t.Fatalf("consumer1.Pending() = %+v, %v; want 1 event", pending1, err)
	}
	ackedSeq := pending1[0].Seq
	if err := consumer1.Ack(ackedSeq); err != nil {
		cancel1()
		t.Fatalf("consumer1.Ack(%d): %v", ackedSeq, err)
	}
	cancel1()

	// On Postgres, github-webhook-cursor.json is NOT written to scratch disk.
	if _, statErr := os.Stat(filepath.Join(scratchDir1, "github-webhook-cursor.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("expected no github-webhook-cursor.json on Postgres scratch disk, got stat err = %v", statErr)
	}

	// Simulate hosted VM replacement: scratchDir1 is discarded and a brand-new
	// empty scratchDir2 boots against the same PostgreSQL store.
	scratchDir2 := t.TempDir()
	cfg2 := Config{
		DataDir:          scratchDir2,
		DbDriver:         "postgres",
		GithubWebhookURL: gw.URL,
	}
	consumer2, err := NewGitHubWebhookConsumerWithStore(context.Background(), cfg2, nil, store)
	if err != nil {
		t.Fatalf("NewGitHubWebhookConsumerWithStore(vm2): %v", err)
	}
	if consumer2.ackedSeq != ackedSeq {
		t.Fatalf("consumer2.ackedSeq = %d, want %d from PostgreSQL", consumer2.ackedSeq, ackedSeq)
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	woke2 := make(chan struct{}, 4)
	if err := consumer2.Start(ctx2, func() {
		select {
		case woke2 <- struct{}{}:
		default:
		}
	}, nil); err != nil {
		t.Fatalf("consumer2.Start: %v", err)
	}

	// Send a second delivery and verify only the new delivery is surfaced (not #101).
	sendDelivery("deliv-pg-2", "202")
	select {
	case <-woke2:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for consumer2 wakeup")
	}
	pending2, err := consumer2.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending2) != 1 || pending2[0].CommentID != 202 {
		t.Fatalf("consumer2.Pending() = %+v, want only comment 202", pending2)
	}

	// Also verify in-process -github-webhook-addr keeps github-webhooks.jsonl on
	// local scratch disk while reading/writing the cursor in PostgreSQL.
	t.Setenv(githubWebhookSecretEnv, webhookSecret)
	scratchDir3 := t.TempDir()
	localConsumer, err := NewGitHubWebhookConsumerWithStore(context.Background(), Config{
		DataDir:           scratchDir3,
		DbDriver:          "postgres",
		GithubWebhookAddr: "127.0.0.1:0",
	}, nil, store)
	if err != nil {
		t.Fatalf("NewGitHubWebhookConsumerWithStore(localAddr): %v", err)
	}
	defer func() {
		if localConsumer.localStore != nil {
			_ = localConsumer.localStore.Close()
		}
	}()
	if _, err := os.Stat(filepath.Join(scratchDir3, "github-webhooks.jsonl")); err != nil {
		t.Errorf("expected github-webhooks.jsonl on local scratch disk, got: %v", err)
	}
	if err := localConsumer.Ack(999); err != nil {
		t.Fatalf("localConsumer.Ack(999): %v", err)
	}
	cur, err := store.GetGitHubWebhookCursor(context.Background())
	if err != nil || cur == nil || cur.LastAckedSeq != 999 {
		t.Fatalf("Postgres cursor after localConsumer.Ack(999) = %+v, %v; want 999", cur, err)
	}
}

func TestGitHubWebhookConsumerSeedsPostgresCursorFromExistingFileOnce(t *testing.T) {
	postgrestest.SkipUnlessAvailable(t)
	store, _ := postgrestest.Open(t)

	// A relay-backed consumer: its sequence numbers are the gateway's, which
	// outlive this data dir, so the seeded cursor is honoured as it stands.
	// (A local -github-webhook-addr store with no events would instead reset
	// a cursor ahead of it -- see
	// TestGitHubWebhookConsumerResetsPostgresCursorAheadOfFreshLocalStore.)
	t.Setenv(githubWebhookRelaySecretEnv, "relay_seed")
	dataDir := t.TempDir()
	cursorPath := filepath.Join(dataDir, "github-webhook-cursor.json")
	if err := saveWebhookCursor(cursorPath, 77); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		DataDir:          dataDir,
		DbDriver:         "postgres",
		GithubWebhookURL: "http://127.0.0.1:1",
	}
	consumer, err := NewGitHubWebhookConsumerWithStore(context.Background(), cfg, nil, store)
	if err != nil {
		t.Fatalf("NewGitHubWebhookConsumerWithStore: %v", err)
	}
	if consumer.localStore != nil {
		_ = consumer.localStore.Close()
	}
	if consumer.ackedSeq != 77 {
		t.Fatalf("seeded ackedSeq = %d, want 77", consumer.ackedSeq)
	}
	cur, err := store.GetGitHubWebhookCursor(context.Background())
	if err != nil || cur == nil || cur.LastAckedSeq != 77 {
		t.Fatalf("Postgres cursor after file seed = %+v, %v; want 77", cur, err)
	}

	if err := consumer.Ack(105); err != nil {
		t.Fatalf("Ack(105): %v", err)
	}

	// Subsequent boot ignores the stale 77 on disk and reads 105 from PostgreSQL.
	reopened, err := NewGitHubWebhookConsumerWithStore(context.Background(), cfg, nil, store)
	if err != nil {
		t.Fatalf("reopening consumer: %v", err)
	}
	if reopened.localStore != nil {
		_ = reopened.localStore.Close()
	}
	if reopened.ackedSeq != 105 {
		t.Fatalf("reopened ackedSeq = %d, want 105", reopened.ackedSeq)
	}
}

// TestGitHubWebhookConsumerResetsPostgresCursorAheadOfFreshLocalStore is the
// VM-replacement case for an in-process -github-webhook-addr listener: the
// cursor survives in PostgreSQL, but github-webhooks.jsonl was on the old
// VM's scratch disk, so the new store numbers its events from 1 again. Those
// events must reach the consumer rather than sit below the stale cursor.
func TestGitHubWebhookConsumerResetsPostgresCursorAheadOfFreshLocalStore(t *testing.T) {
	postgrestest.SkipUnlessAvailable(t)
	store, _ := postgrestest.Open(t)
	ctx := context.Background()

	if err := store.SetGitHubWebhookCursor(ctx, 500, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	t.Setenv(githubWebhookSecretEnv, "whsec_reset")
	consumer, err := NewGitHubWebhookConsumerWithStore(ctx, Config{
		DataDir:           t.TempDir(),
		DbDriver:          "postgres",
		GithubWebhookAddr: "127.0.0.1:0",
	}, nil, store)
	if err != nil {
		t.Fatalf("NewGitHubWebhookConsumerWithStore: %v", err)
	}
	defer consumer.localStore.Close()
	if consumer.ackedSeq != 0 || consumer.maxSeen != 0 {
		t.Fatalf("ackedSeq, maxSeen = %d, %d over an empty local store; want 0, 0", consumer.ackedSeq, consumer.maxSeen)
	}

	stored, _, err := consumer.localStore.Append(ghwebhook.Event{DeliveryID: "deliv-fresh-1", Repo: "acme/widgets", ReceivedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	events, _ := consumer.localStore.After(consumer.maxSeen, 100)
	consumer.ingest(events, nil, nil)
	pending, err := consumer.Pending()
	if err != nil || len(pending) != 1 || pending[0].Seq != stored.Seq {
		t.Fatalf("Pending() = %+v, %v; want the fresh store's event %d", pending, err, stored.Seq)
	}
	if err := consumer.Ack(stored.Seq); err != nil {
		t.Fatal(err)
	}
	cur, err := store.GetGitHubWebhookCursor(ctx)
	if err != nil || cur == nil || cur.LastAckedSeq != stored.Seq {
		t.Fatalf("Postgres cursor after Ack = %+v, %v; want %d", cur, err, stored.Seq)
	}
}
