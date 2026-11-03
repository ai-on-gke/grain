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

package demo

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os/exec"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestDemoReconcileProvisionsAReleaseOnTheSimulatedGitHub is -reconcile
// with -github-sim: a release created through the store leaves
// Provisioning on the loop's own, with its latest branch on the
// simulated GitHub, and nothing calls SyncReleases by hand.
func TestDemoReconcileProvisionsAReleaseOnTheSimulatedGitHub(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("the simulated GitHub needs git")
	}
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg, bare, err := configureGitHubSimDemo(dir, ui.Config{}, repo)
	if err != nil {
		t.Fatalf("configureGitHubSimDemo: %v", err)
	}
	releases, ok := cfg.GitHubBranches.(github.Client)
	if !ok {
		t.Fatalf("-github-sim wired a %T, which cannot sync releases", cfg.GitHubBranches)
	}
	r, err := store.CreateRelease(ctx, repo, "walk", time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		runDemoReconcile(ctx, store, releases, 10*time.Millisecond)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, err := store.GetRelease(ctx, repo, r.Name)
		if err != nil {
			t.Fatalf("GetRelease: %v", err)
		}
		if got.Status != model.ReleaseProvisioning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("release still provisioning after 10s of -reconcile (last error %q)", got.LastError)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := demoGit(bare, "rev-parse", "--verify", "refs/heads/"+r.LatestBranch()); err != nil {
		t.Errorf("the simulated GitHub has no %s: %v", r.LatestBranch(), err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("runDemoReconcile did not return once its context was done")
	}
}

// TestDemoReconcileCycleWithoutGitHubSim is -reconcile alone: suites
// still sync, and a release is left where it is rather than synced
// against a GitHub the demo does not have.
func TestDemoReconcileCycleWithoutGitHubSim(t *testing.T) {
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if _, err := store.CreateRelease(ctx, repo, "walk", time.Now().UTC()); err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	if err := demoReconcileCycle(ctx, store, nil, time.Now().UTC()); err != nil {
		t.Fatalf("demoReconcileCycle: %v", err)
	}
	got, err := store.GetRelease(ctx, repo, "walk")
	if err != nil {
		t.Fatalf("GetRelease: %v", err)
	}
	if got.Status != model.ReleaseProvisioning || got.LastError != "" {
		t.Errorf("release is %s (error %q), want provisioning with no error", got.Status, got.LastError)
	}
}

func TestDemoReconcileAdvancesPollScheduleAndWebhookResetsBackoff(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("the simulated GitHub needs git")
	}
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	ghCalls := newDemoGitHubCalls()
	cfg := ui.Config{
		Actor:         ui.DefaultActor("operator"),
		DefaultTarget: &repo,
		Capabilities:  append(ui.OfferedCapabilities(), ui.APICapabilities()...),
		GitHubCalls:   ghCalls,
	}
	cfg, _, err = configureGitHubSimDemo(dir, cfg, repo)
	if err != nil {
		t.Fatalf("configureGitHubSimDemo: %v", err)
	}
	if err := seedDemo(ctx, store, cfg); err != nil {
		t.Fatalf("seedDemo: %v", err)
	}
	ghCalls.enableLive()

	const webhookSecret = "demo-webhook-test-secret"
	t.Setenv("GITHUB_WEBHOOK_SECRET", webhookSecret)
	releases, _ := cfg.GitHubBranches.(github.Client)
	consumer, err := startDemoGitHubWebhooks(ctx, dir, Options{
		GithubWebhookAddr: "127.0.0.1:0",
	}, nil, store, releases, ghCalls)
	if err != nil {
		t.Fatalf("startDemoGitHubWebhooks: %v", err)
	}
	if v, ok := demoSimClients.Load(releases); ok {
		v.(*demoSimState).events = consumer
	}

	before := ghCalls.Stats()
	if err := demoReconcileCycle(ctx, store, releases, time.Now().UTC()); err != nil {
		t.Fatalf("demoReconcileCycle 1: %v", err)
	}
	after1 := ghCalls.Stats()
	if after1.Sent <= before.Sent || after1.Callers["sync"].Sent <= before.Callers["sync"].Sent {
		t.Fatalf("after cycle 1: Sent=%d (sync=%d), want > %d (sync=%d)",
			after1.Sent, after1.Callers["sync"].Sent, before.Sent, before.Callers["sync"].Sent)
	}
	if after1.Polls.Tracked < 3 || after1.Polls.BackedOff < 2 || after1.Polls.MaxSeconds != 60 {
		t.Fatalf("after cycle 1: Polls = %+v, want Tracked>=3 BackedOff>=2 MaxSeconds=60", after1.Polls)
	}

	for i := 0; i < 6; i++ {
		if err := demoReconcileCycle(ctx, store, releases, time.Now().UTC()); err != nil {
			t.Fatalf("demoReconcileCycle %d: %v", i+2, err)
		}
	}
	after7 := ghCalls.Stats()
	if after7.Polls.MaxSeconds <= after1.Polls.MaxSeconds {
		t.Fatalf("after 7 cycles: Polls.MaxSeconds = %d, want > %d", after7.Polls.MaxSeconds, after1.Polls.MaxSeconds)
	}

	payload := []byte(`{"action":"synchronize","number":102,"repository":{"full_name":"acme/widgets"}}`)
	mac := hmac.New(sha256.New, []byte(webhookSecret))
	mac.Write(payload)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+consumer.ListenAddr()+"/webhooks/github", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-GitHub-Delivery", "delivery-102")
	req.Header.Set("X-Hub-Signature-256", sig)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST webhook: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		t.Fatalf("POST webhook status = %d, want 200/202", resp.StatusCode)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		st := ghCalls.Stats()
		if st.Polls.BackedOff < after7.Polls.BackedOff {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Polls.BackedOff = %d after webhook on #102, want < %d (Polls=%+v)",
				st.Polls.BackedOff, after7.Polls.BackedOff, st.Polls)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
