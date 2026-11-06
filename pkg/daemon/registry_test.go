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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/registry/api"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/version"
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

	mux.HandleFunc("POST /api/entries/{name}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
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
		if strings.Contains(joined, "registry tunnel (edge-grain) stopped") {
			if strings.Contains(joined, "grc_second_static_token_654321") {
				t.Fatalf("tunnel stop log leaked grc_ token:\n%s", joined)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for redacted tunnel stop log")
}
