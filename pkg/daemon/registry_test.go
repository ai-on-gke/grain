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
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/registry/api"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/tunnel/mux"
	"github.com/ai-on-gke/grain/pkg/tunnel/wsupgrade"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/version"
	"github.com/ai-on-gke/grain/pkg/webauth"
	"github.com/golang-jwt/jwt/v5"
)

type fakeRegistryServer struct {
	mu          sync.Mutex
	entries     map[string]api.CreateEntryRequest
	updates     []api.UpdateEntryRequest
	heartbeats  []api.HeartbeatRequest
	authHeaders []string

	nextStatus     int
	nextRetryAfter string
	nextConflicts  []api.ScopeConflict
	nextInterval   int
}

func newFakeRegistryServer(t *testing.T) (*fakeRegistryServer, *httptest.Server) {
	t.Helper()
	f := &fakeRegistryServer{
		entries: make(map[string]api.CreateEntryRequest),
	}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/entries", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.authHeaders = append(f.authHeaders, r.Header.Get("Authorization"))
		if f.nextStatus >= 400 {
			code := f.nextStatus
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "forced failure"})
			return
		}
		var req api.CreateEntryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			f.mu.Unlock()
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.entries[req.Name] = req
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(api.CreateEntryResponse{
			Entry: api.Entry{
				Name: req.Name,
				Kind: req.Kind,
			},
		})
	})

	mux.HandleFunc("PATCH /api/entries/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		f.mu.Lock()
		f.authHeaders = append(f.authHeaders, r.Header.Get("Authorization"))
		if f.nextStatus >= 400 {
			code := f.nextStatus
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "forced failure"})
			return
		}
		if _, ok := f.entries[name]; !ok {
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "entry not found"})
			return
		}
		var req api.UpdateEntryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			f.mu.Unlock()
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.updates = append(f.updates, req)
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(api.Entry{Name: name, Kind: api.KindConnected})
	})

	mux.HandleFunc("PUT /api/entries/{name}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		f.mu.Lock()
		f.authHeaders = append(f.authHeaders, r.Header.Get("Authorization"))
		if f.nextStatus >= 400 {
			code := f.nextStatus
			retryAfter := f.nextRetryAfter
			var conflict *api.ScopeConflict
			if len(f.nextConflicts) > 0 {
				c := f.nextConflicts[0]
				conflict = &c
			}
			f.mu.Unlock()
			if retryAfter != "" {
				w.Header().Set("Retry-After", retryAfter)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":    "heartbeat rejected",
				"conflict": conflict,
			})
			return
		}
		if _, ok := f.entries[name]; !ok {
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "entry not found"})
			return
		}
		var req api.HeartbeatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			f.mu.Unlock()
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.heartbeats = append(f.heartbeats, req)
		conflicts := append([]api.ScopeConflict(nil), f.nextConflicts...)
		interval := f.nextInterval
		f.mu.Unlock()

		status := api.StatusOnline
		if len(conflicts) > 0 {
			status = api.StatusDegraded
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(api.HeartbeatResponse{
			Status:              status,
			NextIntervalSeconds: interval,
			Conflicts:           conflicts,
		})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return f, srv
}

func TestRegistryManagerOIDCSelfRegistersAndSendsHeartbeatPayload(t *testing.T) {
	t.Parallel()
	fake, srv := newFakeRegistryServer(t)

	dataDir := t.TempDir()
	store, db, err := OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	// Configure repos + work ownership + scope override + grain/config (excluded).
	primaryRepo := model.RepoRef{Owner: "acme", Name: "widgets"}
	secondaryRepo := model.RepoRef{Owner: "acme", Name: "docs"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: primaryRepo,
		Work: model.WorkOwnership{Default: "on"},
	}); err != nil {
		t.Fatalf("PutRepoConfig primary: %v", err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: secondaryRepo,
		Work: model.WorkOwnership{Default: "off"},
	}); err != nil {
		t.Fatalf("PutRepoConfig secondary: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	// Seed 1 running task and 1 awaiting_reply task.
	if err := store.PutTask(ctx, model.Task{
		ID: "1", Title: "Running task", ApprovedAt: &now, CreatedAt: &now,
	}); err != nil {
		t.Fatalf("PutTask 1: %v", err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "run-1", TaskID: "1", Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatalf("StartRun 1: %v", err)
	}
	if err := store.PutTask(ctx, model.Task{
		ID: "2", Title: "Awaiting reply task", ApprovedAt: &now, CreatedAt: &now,
	}); err != nil {
		t.Fatalf("PutTask 2: %v", err)
	}
	finished := now.Add(time.Minute)
	if err := store.ObserveField(ctx, "2", finished, func(o *model.Observation) {
		o.CompletedAt = &finished
	}); err != nil {
		t.Fatalf("ObserveField CompletedAt: %v", err)
	}

	cfg := model.DefaultConfig()
	cfg.TargetRepos = []string{"acme/widgets", "acme/docs", "grain/config"}
	cfg.Registry = model.RegistryConfig{
		URL:         srv.URL,
		Entry:       "dev-workstation",
		AuthMode:    model.RegistryAuthOIDC,
		DisplayName: "Dev Workstation",
		Purpose:     "Autoscaler development",
		HostKind:    api.HostKindWorkstation,
		Listed:      true,
		Tags:        []string{"autoscaler", "team-infra"},
		Scopes: []model.RegistryScopeRule{
			{Repo: "acme/widgets", PathPrefix: "pkg/autoscaler"},
		},
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	mgr := NewRegistryManager(RegistryManagerOptions{
		Store:      store,
		HTTPClient: srv.Client(),
		LiveConfig: func() Config { return Config{GranuleSandboxes: true} },
		Version: func() version.Info {
			return version.Info{Release: "v2.4.0", Revision: "deadbeef1234567890"}
		},
		Health: func() (api.HealthState, string) {
			return api.HealthOK, ""
		},
		OIDCTokenSource: func(_ context.Context, audience string) (string, time.Time, error) {
			if audience != srv.URL {
				t.Errorf("OIDC audience = %q, want %q", audience, srv.URL)
			}
			return "oidc-jwt-token-1", time.Now().Add(30 * time.Minute), nil
		},
		BaseInterval:   40 * time.Millisecond,
		JitterFraction: -1,
	})

	snap, err := mgr.TriggerHeartbeat(ctx)
	if err != nil {
		t.Fatalf("TriggerHeartbeat: %v", err)
	}
	if snap.State != model.RegistryStateConnected {
		t.Fatalf("Snapshot.State = %q, want %q (err=%q)", snap.State, model.RegistryStateConnected, snap.LastError)
	}
	if !snap.Registered || snap.LastHeartbeatAt == nil {
		t.Fatalf("expected Registered=true and non-nil LastHeartbeatAt, got %+v", snap)
	}

	fake.mu.Lock()
	created, ok := fake.entries["dev-workstation"]
	hbs := append([]api.HeartbeatRequest(nil), fake.heartbeats...)
	fake.mu.Unlock()

	if !ok {
		t.Fatalf("expected connected entry dev-workstation to be created on registry")
	}
	if created.Kind != api.KindConnected || created.HostKind != api.HostKindWorkstation {
		t.Errorf("created entry = %+v, want kind=connected host_kind=workstation", created)
	}
	if created.DisplayName != "Dev Workstation" || created.Purpose != "Autoscaler development" || created.Listed == nil || !*created.Listed {
		t.Errorf("created catalog metadata = %+v", created)
	}
	if len(hbs) != 1 {
		t.Fatalf("expected 1 heartbeat, got %d", len(hbs))
	}
	hb := hbs[0]
	if hb.GrainVersion != "v2.4.0" || hb.GrainCommit != "deadbeef1234567890" {
		t.Errorf("heartbeat version/commit = %q/%q, want v2.4.0/deadbeef1234567890", hb.GrainVersion, hb.GrainCommit)
	}
	if hb.Health != api.HealthOK || hb.ActiveTasks != 1 || hb.AwaitingReplyTasks != 1 {
		t.Errorf("heartbeat health/counts = %q active=%d awaiting=%d, want ok/1/1", hb.Health, hb.ActiveTasks, hb.AwaitingReplyTasks)
	}
	wantScopes := []api.HeartbeatScope{
		{Repo: "acme/docs", PathPrefix: "", Role: api.RoleSecondary},
		{Repo: "acme/widgets", PathPrefix: "pkg/autoscaler", Role: api.RolePrimary},
	}
	if len(hb.Scopes) != len(wantScopes) {
		t.Fatalf("heartbeat scopes = %+v, want %+v", hb.Scopes, wantScopes)
	}
	for i := range wantScopes {
		if hb.Scopes[i] != wantScopes[i] {
			t.Errorf("scope[%d] = %+v, want %+v", i, hb.Scopes[i], wantScopes[i])
		}
	}

	// Updating catalog metadata triggers PATCH /api/entries/{name} on next heartbeat.
	cfg.Registry.Purpose = "Updated purpose"
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("PutConfig update: %v", err)
	}
	if _, err := mgr.TriggerHeartbeat(ctx); err != nil {
		t.Fatalf("second TriggerHeartbeat: %v", err)
	}
	fake.mu.Lock()
	updates := len(fake.updates)
	fake.mu.Unlock()
	if updates != 1 {
		t.Errorf("expected 1 PATCH update after metadata change, got %d", updates)
	}
}

type countingAuditLogger struct {
	count atomic.Int32
}

func (c *countingAuditLogger) RecordSecretResolution(_ context.Context, _ secrets.AuditEntry) error {
	c.count.Add(1)
	return nil
}

func TestRegistryManagerTokenSecretRefreshBackoffAndConflicts(t *testing.T) {
	t.Parallel()
	fake, srv := newFakeRegistryServer(t)
	// Pre-create entry as `grain registry register` would do.
	fake.mu.Lock()
	fake.entries["ci-runner"] = api.CreateEntryRequest{Name: "ci-runner", Kind: api.KindConnected}
	fake.mu.Unlock()

	dataDir := t.TempDir()
	store, db, err := OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	secretStore := secrets.New(t.TempDir())
	auditor := &countingAuditLogger{}
	secretStore.SetAuditLogger(auditor)

	cfg := model.DefaultConfig()
	cfg.TargetRepos = []string{"acme/runner"}
	cfg.Registry = model.RegistryConfig{
		URL:      srv.URL,
		Entry:    "ci-runner",
		AuthMode: model.RegistryAuthToken,
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	mgr := NewRegistryManager(RegistryManagerOptions{
		Store:          store,
		Secrets:        secretStore,
		HTTPClient:     srv.Client(),
		LiveConfig:     func() Config { return Config{K8sSandboxes: true} },
		BaseInterval:   50 * time.Millisecond,
		BaseBackoff:    20 * time.Millisecond,
		MaxBackoff:     200 * time.Millisecond,
		JitterFraction: -1,
	})

	// 1. Missing registration secret fails cleanly without hitting the server.
	snap, err := mgr.TriggerHeartbeat(ctx)
	if err == nil || !strings.Contains(err.Error(), secrets.RegistryRegistrationTokenSecret) {
		t.Fatalf("expected missing-credential error mentioning %s, got %v", secrets.RegistryRegistrationTokenSecret, err)
	}
	if snap.State != model.RegistryStateError || snap.TokenConfigured {
		t.Fatalf("expected error state with TokenConfigured=false, got %+v", snap)
	}

	// 2. Setting secret in secrets.Store works immediately without daemon restart.
	if err := secretStore.Set(secrets.RegistryRegistrationTokenSecret, secrets.AgentCredentialKey, []byte("grc_first_token")); err != nil {
		t.Fatalf("secretStore.Set: %v", err)
	}
	snap, err = mgr.TriggerHeartbeat(ctx)
	if err != nil {
		t.Fatalf("TriggerHeartbeat after setting secret: %v", err)
	}
	if snap.State != model.RegistryStateConnected || !snap.TokenConfigured || snap.EffectiveHostKind != api.HostKindKubernetes {
		t.Fatalf("unexpected snapshot after connect: %+v", snap)
	}
	// Sending a second heartbeat without changing secrets.age must not add another audit row.
	if _, err := mgr.TriggerHeartbeat(ctx); err != nil {
		t.Fatalf("second TriggerHeartbeat: %v", err)
	}
	if got := auditor.count.Load(); got != 1 {
		t.Errorf("secret audit count across 2 unchanged heartbeats = %d, want 1", got)
	}

	// 3. Rotating the secret in secrets.Store reloads the new token on the next heartbeat.
	if err := secretStore.Set(secrets.RegistryRegistrationTokenSecret, secrets.AgentCredentialKey, []byte("grc_rotated_token")); err != nil {
		t.Fatalf("secretStore.Set rotated: %v", err)
	}
	if _, err := mgr.TriggerHeartbeat(ctx); err != nil {
		t.Fatalf("TriggerHeartbeat after rotation: %v", err)
	}
	fake.mu.Lock()
	lastAuth := fake.authHeaders[len(fake.authHeaders)-1]
	fake.mu.Unlock()
	if lastAuth != "Bearer grc_rotated_token" {
		t.Errorf("Authorization header after secret rotation = %q, want %q", lastAuth, "Bearer grc_rotated_token")
	}
	if got := auditor.count.Load(); got != 2 {
		t.Errorf("secret audit count after rotation = %d, want 2", got)
	}

	// 4. Scope conflicts mark state degraded and surface in Snapshot.
	fake.mu.Lock()
	fake.nextConflicts = []api.ScopeConflict{
		{Repo: "acme/runner", PathPrefix: "", ConflictingEntry: "other-grain", ConflictingOwner: "alice@example.com"},
	}
	fake.mu.Unlock()
	snap, err = mgr.TriggerHeartbeat(ctx)
	if err != nil {
		t.Fatalf("TriggerHeartbeat with warning conflicts: %v", err)
	}
	if snap.State != model.RegistryStateDegraded || len(snap.ScopeConflicts) != 1 {
		t.Fatalf("expected degraded state with 1 conflict, got state=%q conflicts=%+v", snap.State, snap.ScopeConflicts)
	}

	// 5. Server 503 / 429 triggers exponential backoff and honours Retry-After.
	fake.mu.Lock()
	fake.nextConflicts = nil
	fake.nextStatus = http.StatusServiceUnavailable
	fake.mu.Unlock()

	d1, err := mgr.step(ctx)
	if err == nil || d1 != 20*time.Millisecond {
		t.Fatalf("backoff 1 = (%s, %v), want 20ms error", d1, err)
	}
	d2, err := mgr.step(ctx)
	if err == nil || d2 != 40*time.Millisecond {
		t.Fatalf("backoff 2 = (%s, %v), want 40ms error", d2, err)
	}

	fake.mu.Lock()
	fake.nextStatus = http.StatusTooManyRequests
	fake.nextRetryAfter = "2"
	fake.mu.Unlock()
	d3, err := mgr.step(ctx)
	if err == nil || d3 != 2*time.Second {
		t.Fatalf("Retry-After backoff = (%s, %v), want 2s error", d3, err)
	}

	// 6. Server recovers -> consecutiveFailures resets to 0.
	fake.mu.Lock()
	fake.nextStatus = 0
	fake.nextRetryAfter = ""
	fake.mu.Unlock()
	snap, err = mgr.TriggerHeartbeat(ctx)
	if err != nil || snap.State != model.RegistryStateConnected || snap.ConsecutiveFailures != 0 || snap.LastError != "" {
		t.Fatalf("expected clean recovery, got err=%v snap=%+v", err, snap)
	}
}

type fakeTunnelConnector struct {
	started chan string
	stopped chan struct{}
}

func (f *fakeTunnelConnector) RunTunnel(ctx context.Context, cfg api.ConnectConfig) error {
	tok := cfg.RegistrationToken
	if tok == "" && cfg.IDToken != nil {
		var err error
		tok, err = cfg.IDToken(ctx)
		if err != nil {
			return err
		}
	}
	select {
	case f.started <- cfg.Entry + ":" + tok:
	default:
	}
	<-ctx.Done()
	close(f.stopped)
	return ctx.Err()
}

func TestRegistryManagerOIDCRefreshAndTunnelSeam(t *testing.T) {
	t.Parallel()
	fake, srv := newFakeRegistryServer(t)
	fake.mu.Lock()
	fake.entries["edge-grain"] = api.CreateEntryRequest{Name: "edge-grain", Kind: api.KindConnected}
	fake.mu.Unlock()

	dataDir := t.TempDir()
	store, db, err := OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	cfg := model.DefaultConfig()
	cfg.Registry = model.RegistryConfig{
		URL:      srv.URL,
		Entry:    "edge-grain",
		AuthMode: model.RegistryAuthOIDC,
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	var mintCalls atomic.Int32
	currentTime := time.Date(2026, 11, 5, 6, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex

	tun := &fakeTunnelConnector{
		started: make(chan string, 1),
		stopped: make(chan struct{}),
	}

	mgr := NewRegistryManager(RegistryManagerOptions{
		Store:      store,
		HTTPClient: srv.Client(),
		Now: func() time.Time {
			clockMu.Lock()
			defer clockMu.Unlock()
			return currentTime
		},
		OIDCTokenSource: func(_ context.Context, _ string) (string, time.Time, error) {
			n := mintCalls.Add(1)
			clockMu.Lock()
			exp := currentTime.Add(10 * time.Minute)
			clockMu.Unlock()
			return "oidc-tok-" + string(rune('0'+n)), exp, nil
		},
		TunnelConnector: tun,
		BaseInterval:    100 * time.Millisecond,
		JitterFraction:  -1,
	})

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		mgr.Run(runCtx)
	}()

	select {
	case got := <-tun.started:
		if got != "edge-grain:oidc-tok-1" {
			t.Fatalf("tunnel started with %q, want edge-grain:oidc-tok-1", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for tunnel seam to start")
	}

	// Advance clock past token expiry -> next TriggerHeartbeat mints oidc-tok-2.
	clockMu.Lock()
	currentTime = currentTime.Add(15 * time.Minute)
	clockMu.Unlock()
	if _, err := mgr.TriggerHeartbeat(runCtx); err != nil {
		t.Fatalf("TriggerHeartbeat after expiry: %v", err)
	}
	if got := mintCalls.Load(); got != 2 {
		t.Fatalf("mintCalls after expiry = %d, want 2", got)
	}

	cancel()
	<-done
	select {
	case <-tun.stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for tunnel seam to stop cleanly")
	}
}

func TestRegistryManagerValidatesStoredConfigAndRedactsFailureLogs(t *testing.T) {
	t.Parallel()

	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"rejected token grc_AbCdEfGhIjKlMnOpQrStUvWxYz0123"}`))
	}))
	defer errSrv.Close()

	dataDir := t.TempDir()
	store, db, err := OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	// 1. Unnormalized/invalid Registry URL stored directly in grain_config is
	// rejected before any network call.
	cfg := model.DefaultConfig()
	cfg.Registry = model.RegistryConfig{
		URL:      "http://insecure.example.com",
		Entry:    "edge-grain",
		AuthMode: model.RegistryAuthToken,
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	secretStore := secrets.New(t.TempDir())
	if err := secretStore.Set(secrets.RegistryRegistrationTokenSecret, secrets.AgentCredentialKey, []byte("grc_AbCdEfGhIjKlMnOpQrStUvWxYz0123")); err != nil {
		t.Fatalf("secretStore.Set: %v", err)
	}

	var logged []string
	var logMu sync.Mutex
	mgr := NewRegistryManager(RegistryManagerOptions{
		Store:          store,
		Secrets:        secretStore,
		HTTPClient:     errSrv.Client(),
		JitterFraction: -1,
		Logf: func(format string, args ...any) {
			logMu.Lock()
			defer logMu.Unlock()
			logged = append(logged, fmt.Sprintf(format, args...))
		},
	})

	if _, err := mgr.TriggerHeartbeat(ctx); err == nil || !strings.Contains(err.Error(), "insecure http://") {
		t.Fatalf("expected insecure http:// error from stored config, got %v", err)
	}

	// 2. When the remote server echoes a grc_ token in its error body, both
	// Snapshot().LastError and daemon logs shape-redact the token.
	cfg.Registry.URL = errSrv.URL
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("PutConfig valid: %v", err)
	}
	snap, err := mgr.TriggerHeartbeat(ctx)
	if err == nil {
		t.Fatal("expected 401 failure from errSrv")
	}
	if strings.Contains(err.Error(), "grc_AbCdEfGh") {
		t.Errorf("TriggerHeartbeat error leaked grc_ token: %q", err.Error())
	}
	if strings.Contains(snap.LastError, "grc_AbCdEfGh") {
		t.Errorf("Snapshot().LastError leaked grc_ token: %q", snap.LastError)
	}
	logMu.Lock()
	allLogs := strings.Join(logged, "\n")
	logMu.Unlock()
	if strings.Contains(allLogs, "grc_AbCdEfGh") {
		t.Errorf("daemon Logf leaked grc_ token:\n%s", allLogs)
	}
}

type rotatingTunnelConnector struct {
	started chan string
	mu      sync.Mutex
	failErr error
}

func (r *rotatingTunnelConnector) setFailErr(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failErr = err
}

func (r *rotatingTunnelConnector) RunTunnel(ctx context.Context, cfg api.ConnectConfig) error {
	select {
	case r.started <- cfg.RegistrationToken:
	default:
	}
	r.mu.Lock()
	err := r.failErr
	r.mu.Unlock()
	if err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestRegistryManagerRotatesStaticTokenTunnelAndRedactsTunnelErrors(t *testing.T) {
	t.Parallel()
	fake, srv := newFakeRegistryServer(t)
	fake.mu.Lock()
	fake.entries["edge-grain"] = api.CreateEntryRequest{Name: "edge-grain", Kind: api.KindConnected}
	fake.mu.Unlock()

	dataDir := t.TempDir()
	store, db, err := OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	cfg := model.DefaultConfig()
	cfg.Registry = model.RegistryConfig{
		URL:      srv.URL,
		Entry:    "edge-grain",
		AuthMode: model.RegistryAuthToken,
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	secStore := secrets.New(t.TempDir())
	if err := secStore.Set(secrets.RegistryRegistrationTokenSecret, secrets.AgentCredentialKey, []byte("grc_first_static_token_123456")); err != nil {
		t.Fatalf("secStore.Set: %v", err)
	}

	tun := &rotatingTunnelConnector{started: make(chan string, 4)}
	var logMu sync.Mutex
	var logged []string
	mgr := NewRegistryManager(RegistryManagerOptions{
		Store:           store,
		Secrets:         secStore,
		HTTPClient:      srv.Client(),
		TunnelConnector: tun,
		BaseInterval:    time.Hour,
		JitterFraction:  -1,
		Logf: func(format string, args ...any) {
			logMu.Lock()
			defer logMu.Unlock()
			logged = append(logged, fmt.Sprintf(format, args...))
		},
	})

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		mgr.Run(runCtx)
	}()
	defer func() {
		cancel()
		<-done
	}()

	select {
	case tok := <-tun.started:
		if tok != "grc_first_static_token_123456" {
			t.Fatalf("initial tunnel token = %q", tok)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for initial tunnel")
	}

	// Rotate the static grc_ credential in secrets.Store using a short-lived
	// request context (as POST /api/registry/heartbeat does): the next heartbeat
	// must tear down the old tunnel, reconnect with the new token rooted in
	// Run's lifecycle context, and redact any token echoed in tunnel errors.
	if err := secStore.Set(secrets.RegistryRegistrationTokenSecret, secrets.AgentCredentialKey, []byte("grc_second_static_token_654321")); err != nil {
		t.Fatalf("secStore.Set rotated: %v", err)
	}
	tun.setFailErr(fmt.Errorf("tunnel rejected credential grc_second_static_token_654321"))

	reqCtx, reqCancel := context.WithCancel(ctx)
	if _, err := mgr.TriggerHeartbeat(reqCtx); err != nil {
		reqCancel()
		t.Fatalf("TriggerHeartbeat after rotation: %v", err)
	}
	reqCancel()
	select {
	case tok := <-tun.started:
		if tok != "grc_second_static_token_654321" {
			t.Fatalf("rotated tunnel token = %q, want grc_second_static_token_654321", tok)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for tunnel restart after grc_ rotation")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		logMu.Lock()
		joined := strings.Join(logged, "\n")
		logMu.Unlock()
		if strings.Contains(joined, "hub tunnel (edge-grain) stopped") {
			if strings.Contains(joined, "grc_second_static_token_654321") {
				t.Fatalf("tunnel stop log leaked grc_ token:\n%s", joined)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for redacted tunnel stop log")
}

func TestCheckRegistryConnectFailsClosedWithoutVerifierFlags(t *testing.T) {
	t.Parallel()
	if err := CheckRegistryConnect(Config{RegistryConnect: false}); err != nil {
		t.Fatalf("CheckRegistryConnect(false) = %v, want nil", err)
	}
	if err := CheckRegistryConnect(Config{RegistryConnect: true}); err == nil ||
		!strings.Contains(err.Error(), "-ui-registry-issuer") ||
		!strings.Contains(err.Error(), "-ui-registry-audience") ||
		!strings.Contains(err.Error(), "-ui-registry-keys-url") {
		t.Fatalf("CheckRegistryConnect(missing all) = %v, want missing flags error", err)
	}
	if err := CheckRegistryConnect(Config{
		RegistryConnect:  true,
		UiRegistryIssuer: "https://grain.example.com",
	}); err == nil || !strings.Contains(err.Error(), "-ui-registry-audience") {
		t.Fatalf("CheckRegistryConnect(partial) = %v, want missing audience error", err)
	}
	if err := CheckRegistryConnect(Config{
		RegistryConnect:    true,
		UiRegistryIssuer:   "https://grain.example.com",
		UiRegistryAudience: "https://my-grain.grain.example.com",
		UiRegistryKeysURL:  "https://grain.example.com/.well-known/jwks.json",
	}); err != nil {
		t.Fatalf("CheckRegistryConnect(complete) = %v, want nil", err)
	}
}

func TestRegistryTunnelSupervisorBackoffAndStableReset(t *testing.T) {
	t.Parallel()
	sup := NewRegistryTunnelSupervisor(RegistryTunnelSupervisorOptions{
		Handler:       http.NotFoundHandler(),
		BaseBackoff:   250 * time.Millisecond,
		MaxBackoff:    30 * time.Second,
		AuthBackoff:   60 * time.Second,
		MaxRetryAfter: 5 * time.Minute,
		DisableJitter: true,
	})

	// 1. Exponential progression: 250ms -> 500ms -> 1s -> ... -> 30s cap.
	wantSeq := []time.Duration{
		250 * time.Millisecond,
		500 * time.Millisecond,
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		30 * time.Second,
		30 * time.Second,
	}
	for i, want := range wantSeq {
		if got := sup.ComputeBackoff(i+1, fmt.Errorf("dial error")); got != want {
			t.Errorf("ComputeBackoff(failures=%d) = %s, want %s", i+1, got, want)
		}
	}

	// 2. With ±25% jitter enabled, backoff stays within [0.75*base, 1.25*base].
	jitterSup := NewRegistryTunnelSupervisor(RegistryTunnelSupervisorOptions{
		Handler:        http.NotFoundHandler(),
		BaseBackoff:    250 * time.Millisecond,
		MaxBackoff:     30 * time.Second,
		JitterFraction: 0.25,
	})
	for i := 0; i < 20; i++ {
		d := jitterSup.ComputeBackoff(1, fmt.Errorf("dial error"))
		if d < 187*time.Millisecond || d > 313*time.Millisecond {
			t.Fatalf("jittered base backoff = %s, out of 250ms ±25%%", d)
		}
	}

	// 3. 401/403 enforce >= 60s backoff; 429/503 honour Retry-After (capped at 5m).
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		d := sup.ComputeBackoff(1, &api.APIError{StatusCode: code, Message: "denied"})
		if d < 60*time.Second {
			t.Errorf("ComputeBackoff(%d) = %s, want >= 60s", code, d)
		}
	}
	if d := sup.ComputeBackoff(1, &api.APIError{StatusCode: http.StatusTooManyRequests, RetryAfter: 45 * time.Second}); d != 45*time.Second {
		t.Errorf("ComputeBackoff(429 Retry-After=45s) = %s, want 45s", d)
	}
	if d := sup.ComputeBackoff(1, &api.APIError{StatusCode: http.StatusServiceUnavailable, RetryAfter: 10 * time.Minute}); d != 5*time.Minute {
		t.Errorf("ComputeBackoff(503 Retry-After=10m) = %s, want 5m cap", d)
	}

	// 4. Verify stable session (>= StableSession) resets consecutive failure count,
	// and 401 triggers OnUnauthorized.
	var dialAttempt atomic.Int32
	var unauthorizedCalls atomic.Int32
	registryTLS := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := dialAttempt.Add(1)
		switch n {
		case 1:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"expired oidc token"}`))
		case 2:
			wsConn, err := wsupgrade.Upgrade(w, r, http.Header{
				api.HeaderTunnelSession:   []string{"sess-stable"},
				api.HeaderConnectProtocol: []string{api.ConnectProtocolVersion},
			})
			if err != nil {
				return
			}
			m := mux.NewMux(wsConn, mux.Config{IsServer: true})
			time.Sleep(45 * time.Millisecond)
			_ = m.Close()
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer registryTLS.Close()

	rootPool := x509.NewCertPool()
	rootPool.AddCert(registryTLS.Certificate())

	connectedCh := make(chan string, 2)
	runSup := NewRegistryTunnelSupervisor(RegistryTunnelSupervisorOptions{
		Handler:        http.NotFoundHandler(),
		BaseBackoff:    10 * time.Millisecond,
		MaxBackoff:     100 * time.Millisecond,
		AuthBackoff:    15 * time.Millisecond,
		StableSession:  25 * time.Millisecond,
		DisableJitter:  true,
		TLSConfig:      &tls.Config{RootCAs: rootPool},
		OnUnauthorized: func() { unauthorizedCalls.Add(1) },
		OnSessionStart: func(id string) {
			select {
			case connectedCh <- id:
			default:
			}
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = runSup.RunTunnel(ctx, api.ConnectConfig{
			BaseURL:           registryTLS.URL,
			Entry:             "alice",
			RegistrationToken: "grc_test_token_12345",
		})
	}()

	select {
	case id := <-connectedCh:
		if id != "sess-stable" {
			t.Fatalf("connected session = %q, want sess-stable", id)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for stable session after initial 401")
	}
	if unauthorizedCalls.Load() != 1 {
		t.Fatalf("OnUnauthorized calls = %d, want 1", unauthorizedCalls.Load())
	}
}

func TestRegistryTunnelSupervisorConnectServeAndGracefulRotation(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)

	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	byteLen := (ecKey.PublicKey.Curve.Params().BitSize + 7) / 8
	xBytes := ecKey.PublicKey.X.FillBytes(make([]byte, byteLen))
	yBytes := ecKey.PublicKey.Y.FillBytes(make([]byte, byteLen))

	jwksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{
				{
					"kty": "EC",
					"crv": "P-256",
					"alg": "ES256",
					"use": "sig",
					"kid": "reg-key-1",
					"x":   base64.RawURLEncoding.EncodeToString(xBytes),
					"y":   base64.RawURLEncoding.EncodeToString(yBytes),
				},
			},
		})
	}))
	defer jwksSrv.Close()

	// Simulated grain-registry serving /api/entries/alice/connect over TLS.
	var sessionSeq atomic.Int32
	type acceptedSession struct {
		id  string
		mux *mux.Mux
	}
	sessionsCh := make(chan acceptedSession, 4)

	registryTLS := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/entries/alice/connect":
			if r.Header.Get(api.HeaderConnectProtocol) != api.ConnectProtocolVersion ||
				r.Header.Get(api.HeaderEntry) != "alice" ||
				r.Header.Get(api.HeaderRegistryToken) != "grc_live_tunnel_secret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			seq := sessionSeq.Add(1)
			sessID := fmt.Sprintf("sess-%d", seq)
			wsConn, err := wsupgrade.Upgrade(w, r, http.Header{
				api.HeaderTunnelSession:   []string{sessID},
				api.HeaderConnectProtocol: []string{api.ConnectProtocolVersion},
			})
			if err != nil {
				return
			}
			m := mux.NewMux(wsConn, mux.Config{
				IsServer:         true,
				PingInterval:     5 * time.Second,
				PingTimeout:      5 * time.Second,
				MaxBufferedBytes: 1 << 20,
			})
			sessionsCh <- acceptedSession{id: sessID, mux: m}
			<-m.Done()
		default:
			http.NotFound(w, r)
		}
	}))
	defer registryTLS.Close()

	// Forward HTTP CONNECT proxy for HTTPS_PROXY verification.
	var proxyConnects atomic.Int32
	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen proxy: %v", err)
	}
	defer proxyLn.Close()
	go func() {
		for {
			c, err := proxyLn.Accept()
			if err != nil {
				return
			}
			go func(clientConn net.Conn) {
				defer clientConn.Close()
				br := bufio.NewReader(clientConn)
				req, err := http.ReadRequest(br)
				if err != nil || req.Method != http.MethodConnect {
					return
				}
				proxyConnects.Add(1)
				up, err := net.Dial("tcp", req.Host)
				if err != nil {
					_, _ = clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
					return
				}
				defer up.Close()
				_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
				done := make(chan struct{}, 2)
				cp := wsupgrade.BufferedConn(clientConn, br)
				go func() { _, _ = io.Copy(up, cp); done <- struct{}{} }()
				go func() { _, _ = io.Copy(cp, up); done <- struct{}{} }()
				<-done
			}(c)
		}
	}()

	// Configure a grain UI server with local access code AND registry verifier.
	dataDir := t.TempDir()
	store, db, err := OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	cfg := model.DefaultConfig()
	cfg.TargetRepos = []string{"acme/widgets", model.ConfigRepo().String()}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	const (
		issuer     = "https://grain.example.com"
		audience   = "https://alice.grain.example.com"
		accessCode = "local-access-code-xyz"
	)

	uiClient := ui.NewClient(ui.Config{
		LocalAPIToken:     accessCode,
		RegistryIssuer:    issuer,
		RegistryAudience:  audience,
		RegistryKeysURL:   jwksSrv.URL,
		AllowedPrincipals: []string{"user:alice@example.com"},
	}, store)
	uiSrv := ui.NewServerWithClient(uiClient)

	// Wrap RegistryTunnelHandler so we can also pause one slow request on sess-1
	// across the graceful session rotation boundary.
	slowEntered := make(chan struct{})
	slowRelease := make(chan struct{})
	baseTunnelHandler := uiSrv.RegistryTunnelHandler()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/registry/status" && r.URL.Query().Get("slow") == "1" {
			// Still goes through the full RegistryTunnelHandler verifier gate first!
			rec := httptest.NewRecorder()
			baseTunnelHandler.ServeHTTP(rec, r)
			if rec.Code != http.StatusOK {
				w.WriteHeader(rec.Code)
				_, _ = w.Write(rec.Body.Bytes())
				return
			}
			close(slowEntered)
			<-slowRelease
			for k, vals := range rec.Header() {
				for _, v := range vals {
					w.Header().Add(k, v)
				}
			}
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
			return
		}
		baseTunnelHandler.ServeHTTP(w, r)
	})

	rootPool := x509.NewCertPool()
	rootPool.AddCert(registryTLS.Certificate())
	proxyURL, _ := url.Parse("http://" + proxyLn.Addr().String())

	rotatedCh := make(chan string, 2)
	sup := NewRegistryTunnelSupervisor(RegistryTunnelSupervisorOptions{
		Handler:         handler,
		BaseBackoff:     20 * time.Millisecond,
		MaxBackoff:      100 * time.Millisecond,
		MaxSessionAge:   180 * time.Millisecond,
		DrainTimeout:    2 * time.Second,
		DisableJitter:   true,
		TLSConfig:       &tls.Config{RootCAs: rootPool},
		Proxy:           http.ProxyURL(proxyURL),
		OnSessionRotate: func(oldID, newID string) { rotatedCh <- oldID + "->" + newID },
	})

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	supDone := make(chan struct{})
	go func() {
		defer close(supDone)
		_ = sup.RunTunnel(runCtx, api.ConnectConfig{
			BaseURL:           registryTLS.URL,
			Entry:             "alice",
			RegistrationToken: "grc_live_tunnel_secret",
		})
	}()

	// Wait for session 1.
	var sess1 acceptedSession
	select {
	case sess1 = <-sessionsCh:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for sess-1")
	}
	defer sess1.mux.Close()
	if proxyConnects.Load() < 1 {
		t.Fatalf("expected HTTPS_PROXY CONNECT to be used, got %d", proxyConnects.Load())
	}

	mintAssertion := func() string {
		now := time.Now()
		claims := webauth.RegistryClaims{
			RegisteredClaims: jwt.RegisteredClaims{
				Issuer:    issuer,
				Audience:  jwt.ClaimStrings{audience},
				Subject:   "alice@example.com",
				IssuedAt:  jwt.NewNumericDate(now.Add(-5 * time.Second)),
				NotBefore: jwt.NewNumericDate(now.Add(-5 * time.Second)),
				ExpiresAt: jwt.NewNumericDate(now.Add(55 * time.Second)),
				ID:        "jti-1",
			},
			Email:     "alice@example.com",
			Kind:      webauth.PrincipalHuman,
			RequestID: "req_1",
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
		tok.Header["kid"] = "reg-key-1"
		s, err := tok.SignedString(ecKey)
		if err != nil {
			t.Fatalf("SignedString: %v", err)
		}
		return s
	}

	clientForMux := func(m *mux.Mux) *http.Client {
		return &http.Client{
			Timeout: 3 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return m.OpenStream(ctx)
				},
			},
		}
	}
	c1 := clientForMux(sess1.mux)
	assertion := mintAssertion()

	// 1. Tunnel request without X-Grain-Registry-Assertion or with access code is rejected (401)
	// even on open routes (/api/config).
	unauthReq, _ := http.NewRequest(http.MethodGet, "http://alice.grain.example.com/api/config", nil)
	unauthReq.Header.Set("X-Grain-Access-Code", accessCode)
	unauthResp, err := c1.Do(unauthReq)
	if err != nil {
		t.Fatalf("c1.Do(unauth /api/config): %v", err)
	}
	_ = unauthResp.Body.Close()
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("tunnel /api/config with access code and no assertion = %d, want 401", unauthResp.StatusCode)
	}

	// 2. Start an in-flight slow request on sess-1 with a valid assertion before rotation triggers.
	slowErrCh := make(chan error, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodGet, "http://alice.grain.example.com/api/registry/status?slow=1", nil)
		req.Header.Set(ui.RegistryAssertionHeader, assertion)
		resp, err := c1.Do(req)
		if err != nil {
			slowErrCh <- err
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			slowErrCh <- fmt.Errorf("slow status = %d", resp.StatusCode)
			return
		}
		var st api.InstanceStatusResponse
		if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
			slowErrCh <- err
			return
		}
		if len(st.Scopes) != 1 || st.Scopes[0].Repo != "acme/widgets" {
			slowErrCh <- fmt.Errorf("unexpected scopes: %+v", st.Scopes)
			return
		}
		slowErrCh <- nil
	}()

	select {
	case <-slowEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for slow request to enter handler on sess-1")
	}

	// 3. Wait for proactive session rotation to dial sess-2 while sess-1 is still draining!
	var sess2 acceptedSession
	select {
	case sess2 = <-sessionsCh:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for proactive rotation to dial sess-2")
	}
	defer sess2.mux.Close()

	select {
	case rot := <-rotatedCh:
		if rot != "sess-1->sess-2" {
			t.Fatalf("rotation event = %q, want sess-1->sess-2", rot)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for OnSessionRotate callback")
	}

	// 4. Verify new requests on sess-2 are served while sess-1's slow request is still in-flight!
	c2 := clientForMux(sess2.mux)
	req2, _ := http.NewRequest(http.MethodGet, "http://alice.grain.example.com/api/registry/status", nil)
	req2.Header.Set(ui.RegistryAssertionHeader, assertion)
	resp2, err := c2.Do(req2)
	if err != nil {
		t.Fatalf("c2.Do on rotated sess-2: %v", err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("sess-2 GET /api/registry/status = %d, want 200", resp2.StatusCode)
	}

	// 5. Release the slow request on sess-1 and verify it drains and completes cleanly!
	close(slowRelease)
	select {
	case err := <-slowErrCh:
		if err != nil {
			t.Fatalf("in-flight request on sess-1 failed during graceful rotation drain: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for in-flight request on sess-1 to finish draining")
	}

	cancel()
	<-supDone
}

func TestRegistryManagerPreservesRemoteMetadataAndHostKindWhenUnsetLocally(t *testing.T) {
	t.Parallel()
	fake, srv := newFakeRegistryServer(t)
	fake.mu.Lock()
	fake.entries["conn-alice"] = api.CreateEntryRequest{
		Name:        "conn-alice",
		Kind:        api.KindConnected,
		HostKind:    api.HostKindOther,
		DisplayName: "Alice",
		Purpose:     "API",
	}
	fake.mu.Unlock()

	dataDir := t.TempDir()
	store, db, err := OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	// OIDC mode with unset DisplayName, Purpose, and HostKind while LiveConfig
	// infers HostKindWorkstation: neither PATCH nor Heartbeat may overwrite
	// existing remote metadata or CLI-declared HostKind.
	cfg := model.DefaultConfig()
	cfg.Registry = model.RegistryConfig{
		URL:      srv.URL,
		Entry:    "conn-alice",
		AuthMode: model.RegistryAuthOIDC,
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	mgr := NewRegistryManager(RegistryManagerOptions{
		Store:      store,
		HTTPClient: srv.Client(),
		LiveConfig: func() Config { return Config{HostSandboxes: true} },
		OIDCTokenSource: func(_ context.Context, _ string) (string, time.Time, error) {
			return "oidc-jwt", time.Now().Add(10 * time.Minute), nil
		},
		JitterFraction: -1,
	})

	if _, err := mgr.TriggerHeartbeat(ctx); err != nil {
		t.Fatalf("TriggerHeartbeat: %v", err)
	}

	fake.mu.Lock()
	updates := append([]api.UpdateEntryRequest(nil), fake.updates...)
	hbs := append([]api.HeartbeatRequest(nil), fake.heartbeats...)
	fake.mu.Unlock()

	if len(updates) != 1 {
		t.Fatalf("expected 1 initial OIDC PATCH, got %d", len(updates))
	}
	if updates[0].DisplayName != nil || updates[0].Purpose != nil || updates[0].HostKind != nil || updates[0].Listed != nil {
		t.Fatalf("OIDC PATCH with unset local metadata would overwrite remote values: %+v", updates[0])
	}
	if len(hbs) != 1 || hbs[0].HostKind != "" {
		t.Fatalf("heartbeat sent HostKind=%q when local config left host_kind unset, want empty", hbs[0].HostKind)
	}

	// Explicitly configuring HostKind on the daemon sends it on both PATCH and Heartbeat.
	cfg.Registry.HostKind = api.HostKindKubernetes
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("PutConfig explicit host_kind: %v", err)
	}
	if _, err := mgr.TriggerHeartbeat(ctx); err != nil {
		t.Fatalf("TriggerHeartbeat with explicit host_kind: %v", err)
	}

	fake.mu.Lock()
	lastUpd := fake.updates[len(fake.updates)-1]
	lastHB := fake.heartbeats[len(fake.heartbeats)-1]
	fake.mu.Unlock()
	if lastUpd.HostKind == nil || *lastUpd.HostKind != api.HostKindKubernetes {
		t.Fatalf("expected explicit HostKind=kubernetes in PATCH, got %+v", lastUpd.HostKind)
	}
	if lastHB.HostKind != api.HostKindKubernetes {
		t.Fatalf("expected explicit HostKind=kubernetes in Heartbeat, got %q", lastHB.HostKind)
	}
}
