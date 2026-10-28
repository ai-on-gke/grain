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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/ghwebhook"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

const (
	githubWebhookRelaySecretEnv = "GITHUB_WEBHOOK_RELAY_SECRET"
	githubWebhookSecretEnv      = "GITHUB_WEBHOOK_SECRET"
)

type webhookCursorFile struct {
	LastAckedSeq uint64 `json:"last_acked_seq"`
}

// GitHubWebhookConsumer bridges either a remote github-webhook gateway
// (-github-webhook-url) or an in-process listener (-github-webhook-addr)
// into orchestrator.GitHubEventSource, persisting the last acknowledged
// sequence number across daemon restarts.
type GitHubWebhookConsumer struct {
	cursorPath string
	client     *ghwebhook.Client
	localStore *ghwebhook.Store
	localAddr  string
	webhookSec func() string

	mu       sync.Mutex
	ackedSeq uint64
	maxSeen  uint64
	pending  []ghwebhook.Event
}

func newGitHubWebhookConsumer(cfg Config, creds *gitproxy.CredentialSet) (*GitHubWebhookConsumer, error) {
	if cfg.GithubWebhookURL == "" && cfg.GithubWebhookAddr == "" {
		return nil, nil
	}
	if cfg.GithubWebhookURL != "" && cfg.GithubWebhookAddr != "" {
		return nil, fmt.Errorf("-github-webhook-url and -github-webhook-addr are mutually exclusive")
	}

	cursorPath := filepath.Join(cfg.DataDir, "github-webhook-cursor.json")
	acked, err := loadWebhookCursor(cursorPath)
	if err != nil {
		return nil, fmt.Errorf("reading GitHub webhook cursor: %w", err)
	}

	c := &GitHubWebhookConsumer{
		cursorPath: cursorPath,
		ackedSeq:   acked,
		maxSeen:    acked,
	}

	if cfg.GithubWebhookURL != "" {
		secret := strings.TrimSpace(os.Getenv(githubWebhookRelaySecretEnv))
		if secret == "" {
			return nil, fmt.Errorf("%s is not set, so event reads to %s could not be signed",
				githubWebhookRelaySecretEnv, cfg.GithubWebhookURL)
		}
		c.client = &ghwebhook.Client{
			BaseURL:     cfg.GithubWebhookURL,
			RelaySecret: secret,
		}
		return c, nil
	}

	storePath := filepath.Join(cfg.DataDir, "github-webhooks.jsonl")
	st, err := ghwebhook.OpenStore(storePath)
	if err != nil {
		return nil, fmt.Errorf("opening local GitHub webhook store: %w", err)
	}
	envSecret := strings.TrimSpace(os.Getenv(githubWebhookSecretEnv))
	c.localStore = st
	c.localAddr = cfg.GithubWebhookAddr
	c.webhookSec = func() string {
		var parts []string
		if envSecret != "" {
			parts = append(parts, envSecret)
		}
		if creds != nil {
			for _, name := range creds.Names() {
				if sec, ok := creds.AppWebhookSecret(name); ok && sec != "" {
					parts = append(parts, sec)
				}
			}
		}
		return strings.Join(parts, ",")
	}
	if c.webhookSec() == "" {
		_ = st.Close()
		return nil, fmt.Errorf("-github-webhook-addr requires %s or a GitHub App credential with webhook_secret",
			githubWebhookSecretEnv)
	}
	return c, nil
}

func githubEventSource(c *GitHubWebhookConsumer) orchestrator.GitHubEventSource {
	if c == nil {
		return nil
	}
	return c
}

// Pending returns buffered unacknowledged events in sequence order.
func (c *GitHubWebhookConsumer) Pending() ([]ghwebhook.Event, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) == 0 {
		return nil, nil
	}
	out := make([]ghwebhook.Event, 0, len(c.pending))
	for _, ev := range c.pending {
		if ev.Seq > c.ackedSeq {
			out = append(out, ev)
		}
	}
	return out, nil
}

// Ack records seq as processed and atomically updates the cursor file.
func (c *GitHubWebhookConsumer) Ack(seq uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if seq <= c.ackedSeq {
		return nil
	}
	if err := saveWebhookCursor(c.cursorPath, seq); err != nil {
		return err
	}
	c.ackedSeq = seq
	idx := 0
	for idx < len(c.pending) && c.pending[idx].Seq <= seq {
		idx++
	}
	if idx > 0 {
		c.pending = append([]ghwebhook.Event(nil), c.pending[idx:]...)
	}
	return nil
}

// Start launches the background long-poller or local HTTP listener until
// ctx is cancelled.
func (c *GitHubWebhookConsumer) Start(ctx context.Context, wake func(), invalidateRepo func(repo string)) error {
	if c == nil {
		return nil
	}
	if c.client != nil {
		go c.pollRemote(ctx, wake, invalidateRepo)
		return nil
	}
	if c.localStore != nil {
		ln, err := net.Listen("tcp", c.localAddr)
		if err != nil {
			return fmt.Errorf("listening on -github-webhook-addr %s: %w", c.localAddr, err)
		}
		srv := &http.Server{
			Handler: ghwebhook.NewServer(ghwebhook.ServerConfig{
				Store:             c.localStore,
				WebhookSecretFunc: c.webhookSec,
			}).WebhookHandler(),
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      35 * time.Second,
			IdleTimeout:       60 * time.Second,
		}
		go func() {
			<-ctx.Done()
			shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutCtx)
			_ = c.localStore.Close()
		}()
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("grain daemon: github webhook server error: %v", err)
			}
		}()
		go c.watchLocal(ctx, wake, invalidateRepo)
	}
	return nil
}

func (c *GitHubWebhookConsumer) ingest(events []ghwebhook.Event, wake func(), invalidateRepo func(string)) {
	if len(events) == 0 {
		return
	}
	var added []ghwebhook.Event
	c.mu.Lock()
	for _, ev := range events {
		if ev.Seq <= c.maxSeen {
			continue
		}
		c.maxSeen = ev.Seq
		if ev.Seq > c.ackedSeq {
			c.pending = append(c.pending, ev)
			added = append(added, ev)
		}
	}
	c.mu.Unlock()

	if len(added) == 0 {
		return
	}
	if invalidateRepo != nil {
		seen := map[string]bool{}
		for _, ev := range added {
			if ev.Repo != "" && !seen[ev.Repo] {
				seen[ev.Repo] = true
				invalidateRepo(ev.Repo)
			}
		}
	}
	if wake != nil {
		wake()
	}
}

func (c *GitHubWebhookConsumer) pollRemote(ctx context.Context, wake func(), invalidateRepo func(string)) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		c.mu.Lock()
		after := c.maxSeen
		c.mu.Unlock()

		events, err := c.client.Poll(ctx, after, 100, 20*time.Second)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("grain daemon: polling GitHub webhook gateway %s: %v", c.client.BaseURL, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 15*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		c.ingest(events.Events, wake, invalidateRepo)
	}
}

func (c *GitHubWebhookConsumer) watchLocal(ctx context.Context, wake func(), invalidateRepo func(string)) {
	for {
		if ctx.Err() != nil {
			return
		}
		c.mu.Lock()
		after := c.maxSeen
		c.mu.Unlock()

		events, _ := c.localStore.WaitAfter(ctx, after, 100)
		if ctx.Err() != nil {
			return
		}
		c.ingest(events, wake, invalidateRepo)
	}
}

func loadWebhookCursor(path string) (uint64, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var cur webhookCursorFile
	if err := json.Unmarshal(raw, &cur); err != nil {
		return 0, err
	}
	return cur.LastAckedSeq, nil
}

func saveWebhookCursor(path string, seq uint64) error {
	raw, err := json.Marshal(webhookCursorFile{LastAckedSeq: seq})
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func appBotIdentity(creds *gitproxy.CredentialSet, store *model.Store) orchestrator.AppBotIdentityFunc {
	if creds == nil {
		return nil
	}
	return func(ctx context.Context, repo model.RepoRef) (string, bool) {
		var name string
		if store != nil {
			resolved, err := store.GitConnectionOn(ctx, repo.Host, repo.Owner, repo.Name)
			if err == nil {
				name = resolved
			} else if !errors.Is(err, model.ErrNoTeamGitCredential) {
				log.Printf("grain daemon: resolving git credential for %s bot identity: %v", repo, err)
			}
		}
		if name == "" && repo.OffDefaultHost() == "" {
			if selected, ok := creds.Select(repo.Owner, repo.Name); ok {
				name = selected.Name
			}
		}
		if name == "" || name == "anonymous" {
			return "", false
		}
		login, isApp, err := creds.AppBotLogin(name)
		if err != nil {
			log.Printf("grain daemon: reading GitHub App bot login for credential %s: %v", name, err)
			return "", false
		}
		return login, isApp
	}
}
