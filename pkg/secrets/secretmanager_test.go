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

package secrets

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeSMVersion struct {
	number int
	state  string // "ENABLED" or "DESTROYED"
	data   []byte
}

type fakeSMSecret struct {
	name     string // projects/<p>/secrets/<id>
	labels   map[string]string
	versions []*fakeSMVersion
}

// fakeSecretManagerServer implements the Google Cloud Secret Manager v1 REST
// endpoints granted to grainInstanceSecrets:
//   - secrets.{create,get,delete}
//   - versions.{add,access,destroy}
//
// Project-wide secrets.list and versions.list are deliberately denied with 403
// and counted so tests can assert grain never calls them.
type fakeSecretManagerServer struct {
	mu            sync.Mutex
	token         string
	allowedPrefix string
	secrets       map[string]*fakeSMSecret

	listCalls       int
	accessCalls     int
	createCalls     int
	addVersionCalls int
	destroyCalls    int
	deleteCalls     int

	failNextDestroyStatus int
	conflictNextCreate    bool
}

func newFakeSecretManager(t *testing.T, allowedPrefix string) (*fakeSecretManagerServer, *httptest.Server) {
	t.Helper()
	f := &fakeSecretManagerServer{
		token:         "fake-sm-oauth-token",
		allowedPrefix: allowedPrefix,
		secrets:       make(map[string]*fakeSMSecret),
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeSecretManagerServer) checkPrefix(w http.ResponseWriter, resource string) bool {
	if f.allowedPrefix != "" && !strings.HasPrefix(resource, f.allowedPrefix) {
		http.Error(w, fmt.Sprintf(`{"error":{"code":403,"message":"IAM condition denied access to %s"}}`, resource), http.StatusForbidden)
		return false
	}
	return true
}

func (f *fakeSecretManagerServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.token != "" && r.Header.Get("Authorization") != "Bearer "+f.token {
		http.Error(w, `{"error":{"code":401,"status":"UNAUTHENTICATED"}}`, http.StatusUnauthorized)
		return
	}

	if !strings.HasPrefix(r.URL.Path, "/v1/") {
		http.NotFound(w, r)
		return
	}
	sub := strings.TrimPrefix(r.URL.Path, "/v1/")

	// POST /v1/projects/<p>/secrets?secretId=<id> or GET /v1/projects/<p>/secrets (denied)
	if strings.HasPrefix(sub, "projects/") && strings.HasSuffix(sub, "/secrets") && strings.Count(sub, "/") == 2 {
		if r.Method == http.MethodGet {
			f.listCalls++
			http.Error(w, `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"secretmanager.secrets.list denied"}}`, http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost {
			proj := strings.TrimSuffix(strings.TrimPrefix(sub, "projects/"), "/secrets")
			secretID := r.URL.Query().Get("secretId")
			if !isValidSecretID(secretID) {
				http.Error(w, `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"invalid secretId"}}`, http.StatusBadRequest)
				return
			}
			resName := "projects/" + proj + "/secrets/" + secretID
			if !f.checkPrefix(w, resName) {
				return
			}
			f.createCalls++
			body, _ := io.ReadAll(r.Body)
			var req smCreateSecretRequest
			if err := json.Unmarshal(body, &req); err != nil || req.Replication.Automatic == nil {
				http.Error(w, `{"error":{"code":400,"message":"missing replication"}}`, http.StatusBadRequest)
				return
			}
			if f.conflictNextCreate {
				f.conflictNextCreate = false
				f.secrets[resName] = &fakeSMSecret{name: resName, labels: req.Labels}
				http.Error(w, `{"error":{"code":409,"status":"ALREADY_EXISTS"}}`, http.StatusConflict)
				return
			}
			if _, exists := f.secrets[resName]; exists {
				http.Error(w, `{"error":{"code":409,"status":"ALREADY_EXISTS"}}`, http.StatusConflict)
				return
			}
			labelsCopy := make(map[string]string, len(req.Labels))
			for k, v := range req.Labels {
				labelsCopy[k] = v
			}
			f.secrets[resName] = &fakeSMSecret{name: resName, labels: labelsCopy}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"name": resName, "labels": labelsCopy})
			return
		}
	}

	// POST /v1/projects/<p>/secrets/<id>:addVersion
	if strings.HasSuffix(sub, ":addVersion") && r.Method == http.MethodPost {
		resName := strings.TrimSuffix(sub, ":addVersion")
		if !f.checkPrefix(w, resName) {
			return
		}
		f.addVersionCalls++
		sec, exists := f.secrets[resName]
		if !exists {
			http.Error(w, `{"error":{"code":404,"status":"NOT_FOUND"}}`, http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req smAddVersionRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, `{"error":{"code":400,"status":"INVALID_ARGUMENT"}}`, http.StatusBadRequest)
			return
		}
		raw, err := base64.StdEncoding.DecodeString(req.Payload.Data)
		if err != nil {
			http.Error(w, `{"error":{"code":400,"status":"INVALID_ARGUMENT"}}`, http.StatusBadRequest)
			return
		}
		num := len(sec.versions) + 1
		sec.versions = append(sec.versions, &fakeSMVersion{
			number: num,
			state:  "ENABLED",
			data:   raw,
		})
		verName := fmt.Sprintf("%s/versions/%d", resName, num)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(smVersionResponse{Name: verName, State: "ENABLED"})
		return
	}

	// GET /v1/projects/<p>/secrets/<id>/versions (versions.list - denied!)
	if strings.HasSuffix(sub, "/versions") && r.Method == http.MethodGet {
		f.listCalls++
		http.Error(w, `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"secretmanager.versions.list denied"}}`, http.StatusForbidden)
		return
	}

	// GET /v1/projects/<p>/secrets/<id>/versions/<ver>:access
	if strings.HasSuffix(sub, ":access") && r.Method == http.MethodGet {
		trimmed := strings.TrimSuffix(sub, ":access")
		resName, verStr, ok := strings.Cut(trimmed, "/versions/")
		if !ok {
			http.NotFound(w, r)
			return
		}
		if !f.checkPrefix(w, resName) {
			return
		}
		f.accessCalls++
		sec, exists := f.secrets[resName]
		if !exists {
			http.Error(w, `{"error":{"code":404,"status":"NOT_FOUND"}}`, http.StatusNotFound)
			return
		}
		var chosen *fakeSMVersion
		if verStr == "latest" {
			for i := len(sec.versions) - 1; i >= 0; i-- {
				if sec.versions[i].state == "ENABLED" {
					chosen = sec.versions[i]
					break
				}
			}
		} else if n, err := strconv.Atoi(verStr); err == nil && n >= 1 && n <= len(sec.versions) {
			if sec.versions[n-1].state == "ENABLED" {
				chosen = sec.versions[n-1]
			}
		}
		if chosen == nil {
			http.Error(w, `{"error":{"code":404,"status":"NOT_FOUND"}}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(smAccessResponse{
			Name:    fmt.Sprintf("%s/versions/%d", resName, chosen.number),
			Payload: smPayloadData{Data: base64.StdEncoding.EncodeToString(chosen.data)},
		})
		return
	}

	// POST /v1/projects/<p>/secrets/<id>/versions/<ver>:destroy
	if strings.HasSuffix(sub, ":destroy") && r.Method == http.MethodPost {
		trimmed := strings.TrimSuffix(sub, ":destroy")
		resName, verStr, ok := strings.Cut(trimmed, "/versions/")
		if !ok {
			http.NotFound(w, r)
			return
		}
		if !f.checkPrefix(w, resName) {
			return
		}
		f.destroyCalls++
		if f.failNextDestroyStatus != 0 {
			st := f.failNextDestroyStatus
			f.failNextDestroyStatus = 0
			http.Error(w, `{"error":{"message":"injected destroy error"}}`, st)
			return
		}
		sec, exists := f.secrets[resName]
		if !exists {
			http.Error(w, `{"error":{"code":404,"status":"NOT_FOUND"}}`, http.StatusNotFound)
			return
		}
		n, err := strconv.Atoi(verStr)
		if err != nil || n < 1 || n > len(sec.versions) {
			http.Error(w, `{"error":{"code":404,"status":"NOT_FOUND"}}`, http.StatusNotFound)
			return
		}
		v := sec.versions[n-1]
		if v.state == "DESTROYED" {
			http.Error(w, `{"error":{"code":400,"status":"FAILED_PRECONDITION","message":"SecretVersion is in DESTROYED state"}}`, http.StatusBadRequest)
			return
		}
		v.state = "DESTROYED"
		v.data = nil
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(smVersionResponse{
			Name:  fmt.Sprintf("%s/versions/%d", resName, v.number),
			State: "DESTROYED",
		})
		return
	}

	// DELETE /v1/projects/<p>/secrets/<id>
	if strings.HasPrefix(sub, "projects/") && strings.Count(sub, "/") == 3 && r.Method == http.MethodDelete {
		if !f.checkPrefix(w, sub) {
			return
		}
		f.deleteCalls++
		if _, exists := f.secrets[sub]; !exists {
			http.Error(w, `{"error":{"code":404,"status":"NOT_FOUND"}}`, http.StatusNotFound)
			return
		}
		delete(f.secrets, sub)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
		return
	}

	http.NotFound(w, r)
}

func (f *fakeSecretManagerServer) assertZeroListCalls(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listCalls != 0 {
		t.Fatalf("expected 0 secrets.list/versions.list calls, got %d", f.listCalls)
	}
}

func (f *fakeSecretManagerServer) versionStates(resName string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	sec := f.secrets[resName]
	if sec == nil {
		return nil
	}
	out := make([]string, len(sec.versions))
	for i, v := range sec.versions {
		out[i] = fmt.Sprintf("%d:%s", v.number, v.state)
	}
	return out
}

func newTestSMStore(t *testing.T) (*Store, *fakeSecretManagerServer, *MemoryCatalogue) {
	t.Helper()
	const prefix = "projects/123456789/secrets/grain-inst-alice-"
	fake, srv := newFakeSecretManager(t, prefix)
	cat := NewMemoryCatalogue()
	store := OpenSecretManager(SecretManagerConfig{
		Prefix:     prefix,
		Catalogue:  cat,
		Endpoint:   srv.URL,
		HTTPClient: srv.Client(),
		Token:      func(context.Context) (string, error) { return fake.token, nil },
		Labels: map[string]string{
			"grain-registry": "prod",
			"grain-entry":    "alice",
		},
	})
	return store, fake, cat
}

func TestParseSecretManagerPrefix(t *testing.T) {
	proj, idPref, err := ParseSecretManagerPrefix("projects/123456789/secrets/grain-inst-alice-")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if proj != "123456789" || idPref != "grain-inst-alice-" {
		t.Fatalf("got project=%q idPrefix=%q", proj, idPref)
	}

	bad := []string{
		"",
		"grain-inst-alice-",
		"projects//secrets/grain-",
		"projects/123/secrets/",
		"projects/123/secrets/a/b",
		"projects/123/other/grain-",
		"projects/123/secrets/bad.dot-",
		"projects/123/secrets/" + strings.Repeat("a", 245),
	}
	for _, p := range bad {
		if err := ValidateSecretManagerPrefix(p); err == nil {
			t.Errorf("ValidateSecretManagerPrefix(%q) = nil, want error", p)
		}
	}
}

func TestSecretManagerFullContract(t *testing.T) {
	store, fake, _ := newTestSMStore(t)
	ctx := context.Background()

	if got := store.Backend(); got != BackendSecretManager {
		t.Fatalf("Backend() = %q, want %q", got, BackendSecretManager)
	}
	if err := store.Check(); err != nil {
		t.Fatalf("Check() on fresh store: %v", err)
	}
	if store.KeyCreated() || store.KeyFile() != "" || store.File() != "" {
		t.Fatalf("unexpected file metadata on SM store: created=%v keyFile=%q file=%q", store.KeyCreated(), store.KeyFile(), store.File())
	}
	if pub, err := store.PublicKey(); err != nil || pub != "" {
		t.Fatalf("PublicKey() = %q, %v; want empty, nil", pub, err)
	}
	if rec, err := store.FileRecipient(); err != nil || rec != "" {
		t.Fatalf("FileRecipient() = %q, %v; want empty, nil", rec, err)
	}
	if k, err := GenerateKey(); err != nil {
		t.Fatal(err)
	} else if err := store.ImportKey(k); err == nil {
		t.Fatal("ImportKey succeeded on secretmanager backend, want error")
	}

	// Empty list initially.
	if list, err := store.List(); err != nil || len(list) != 0 {
		t.Fatalf("initial List() = %+v, %v; want empty", list, err)
	}

	// Missing secret returns ErrNoSecret for both bare and explicit forms.
	for _, q := range []string{"nope", "nope/value"} {
		_, err := store.Resolve(ctx, q)
		if !errors.Is(err, ErrNoSecret) {
			t.Fatalf("Resolve(%q) = %v, want ErrNoSecret", q, err)
		}
	}

	// Single-key clean slug convention: gemini-api-key/value -> grain-inst-alice-gemini-api-key.
	if err := store.Set(GeminiAPIKeySecret, AgentCredentialKey, []byte("  sk-gemini-123\n")); err != nil {
		t.Fatalf("Set gemini-api-key: %v", err)
	}
	got, err := store.Resolve(ctx, GeminiAPIKeySecret)
	if err != nil || got != "  sk-gemini-123\n" {
		t.Fatalf("Resolve bare gemini-api-key = %q, %v", got, err)
	}
	const wantCleanRes = "projects/123456789/secrets/grain-inst-alice-gemini-api-key"
	fake.mu.Lock()
	cleanSec := fake.secrets[wantCleanRes]
	fake.mu.Unlock()
	if cleanSec == nil {
		t.Fatalf("expected Secret Manager secret at %s", wantCleanRes)
	}
	if cleanSec.labels["grain-registry"] != "prod" || cleanSec.labels["grain-entry"] != "alice" {
		t.Fatalf("unexpected secret labels: %+v", cleanSec.labels)
	}

	// Multi-key secret and key with dots (e.g. key.json).
	seedSecret(t, store, "gcp-key-minter", map[string]string{"key.json": `{"type":"service_account"}`})
	if got, err := store.Resolve(ctx, "gcp-key-minter"); err != nil || got != `{"type":"service_account"}` {
		t.Fatalf("Resolve gcp-key-minter = %q, %v", got, err)
	}

	seedSecret(t, store, "db", map[string]string{"username": "app", "password": "hunter2"})
	if got, err := store.Resolve(ctx, "db/password"); err != nil || got != "hunter2" {
		t.Fatalf("Resolve db/password = %q, %v", got, err)
	}
	// Bare resolve on multi-key secret fails with sorted key names and no secret value.
	if _, err := store.Resolve(ctx, "db"); err == nil {
		t.Fatal("expected error resolving ambiguous bare multi-key secret")
	} else if msg := err.Error(); !strings.Contains(msg, "password, username") || strings.Contains(msg, "hunter2") {
		t.Fatalf("unexpected ambiguous error: %q", msg)
	}
	// Missing key on existing secret is a plain error, NOT ErrNoSecret.
	if _, err := store.Resolve(ctx, "db/missing"); err == nil || errors.Is(err, ErrNoSecret) {
		t.Fatalf("Resolve(db/missing) = %v, want non-ErrNoSecret error", err)
	}

	// Empty byte slice round-trips cleanly.
	if err := store.Set("empty-val", "value", []byte{}); err != nil {
		t.Fatalf("Set empty value: %v", err)
	}
	if got, err := store.Resolve(ctx, "empty-val"); err != nil || got != "" {
		t.Fatalf("Resolve empty-val = %q, %v", got, err)
	}

	// Has & List.
	if ok, err := store.Has("db", "username"); err != nil || !ok {
		t.Fatalf("Has(db, username) = %v, %v", ok, err)
	}
	if ok, err := store.Has("db", "nope"); err != nil || ok {
		t.Fatalf("Has(db, nope) = %v, %v", ok, err)
	}
	list, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	wantList := []SecretInfo{
		{Name: "db", Keys: []string{"password", "username"}},
		{Name: "empty-val", Keys: []string{"value"}},
		{Name: "gcp-key-minter", Keys: []string{"key.json"}},
		{Name: GeminiAPIKeySecret, Keys: []string{"value"}},
	}
	if !reflect.DeepEqual(list, wantList) {
		t.Fatalf("List() = %+v, want %+v", list, wantList)
	}

	// DeleteKey removes one key and deletes its underlying Secret Manager secret.
	if err := store.DeleteKey("db", "password"); err != nil {
		t.Fatalf("DeleteKey: %v", err)
	}
	if _, err := store.Resolve(ctx, "db/password"); err == nil {
		t.Fatal("deleted key db/password still resolved")
	}
	if err := store.DeleteKey("db", "password"); err == nil {
		t.Fatal("DeleteKey on missing key should fail closed")
	}

	// DeleteSecret removes every remaining key and fails closed when missing.
	if err := store.DeleteSecret("db"); err != nil {
		t.Fatalf("DeleteSecret(db): %v", err)
	}
	if _, err := store.Resolve(ctx, "db"); !errors.Is(err, ErrNoSecret) {
		t.Fatalf("after DeleteSecret(db), Resolve(db) = %v, want ErrNoSecret", err)
	}
	if err := store.DeleteSecret("db"); !errors.Is(err, ErrNoSecret) {
		t.Fatalf("second DeleteSecret(db) = %v, want ErrNoSecret", err)
	}

	// Ensure & Rotate.
	calls := 0
	if err := store.Ensure("cap-server/token", func() (string, error) {
		calls++
		return "tok-1", nil
	}); err != nil {
		t.Fatalf("Ensure 1: %v", err)
	}
	if err := store.Ensure("cap-server/token", func() (string, error) {
		calls++
		return "tok-2", nil
	}); err != nil {
		t.Fatalf("Ensure 2: %v", err)
	}
	if calls != 1 {
		t.Fatalf("Ensure called generator %d times, want 1", calls)
	}
	if got, err := store.Resolve(ctx, "cap-server/token"); err != nil || got != "tok-1" {
		t.Fatalf("Resolve cap-server/token = %q, %v", got, err)
	}
	if err := store.Rotate("cap-server/token", func() (string, error) { return "tok-rotated", nil }); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if got, err := store.Resolve(ctx, "cap-server/token"); err != nil || got != "tok-rotated" {
		t.Fatalf("after Rotate, Resolve = %q, %v", got, err)
	}

	fake.assertZeroListCalls(t)
}

func TestSecretManagerSlugCollisionResistanceAndLengthBound(t *testing.T) {
	store, fake, _ := newTestSMStore(t)
	ctx := context.Background()

	// Pairs that could collide under naive "-" joining must stay distinct.
	pairs := []struct {
		secret, key, val string
	}{
		{"a-b", "c", "val-1"},
		{"a", "b-c", "val-2"},
		{"foo", "value", "val-3"},
		{"foo_bar", "value", "val-4"},
		{"Foo", "value", "val-5"},
		{strings.Repeat("long-secret-name-", 20), strings.Repeat("long-key-", 20), "val-long"},
	}
	for _, p := range pairs {
		if err := store.Set(p.secret, p.key, []byte(p.val)); err != nil {
			t.Fatalf("Set(%q, %q): %v", p.secret, p.key, err)
		}
	}
	for _, p := range pairs {
		got, err := store.Resolve(ctx, p.secret+"/"+p.key)
		if err != nil {
			t.Fatalf("Resolve(%q/%q): %v", p.secret, p.key, err)
		}
		if got != p.val {
			t.Fatalf("Resolve(%q/%q) = %q, want %q", p.secret, p.key, got, p.val)
		}
	}
	fake.mu.Lock()
	numSecrets := len(fake.secrets)
	fake.mu.Unlock()
	if numSecrets != len(pairs) {
		t.Fatalf("expected %d distinct Secret Manager secrets, got %d", len(pairs), numSecrets)
	}
	fake.assertZeroListCalls(t)
}

func TestSecretManagerDestroysSupersededVersionsOnUpdate(t *testing.T) {
	store, fake, cat := newTestSMStore(t)
	ctx := context.Background()
	const resName = "projects/123456789/secrets/grain-inst-alice-openai-api-key"

	// Version 1: created and enabled.
	if err := store.Set(OpenAIAPIKeySecret, AgentCredentialKey, []byte("v1")); err != nil {
		t.Fatalf("Set v1: %v", err)
	}
	if got := fake.versionStates(resName); !reflect.DeepEqual(got, []string{"1:ENABLED"}) {
		t.Fatalf("after v1: %v", got)
	}

	// Version 2: version 2 enabled, version 1 destroyed.
	if err := store.Set(OpenAIAPIKeySecret, AgentCredentialKey, []byte("val-2")); err != nil {
		t.Fatalf("Set val-2: %v", err)
	}
	if got := fake.versionStates(resName); !reflect.DeepEqual(got, []string{"1:DESTROYED", "2:ENABLED"}) {
		t.Fatalf("after v2: %v", got)
	}

	// Inject a transient 500 failure on the next :destroy during v3 update:
	// Set returns an error, keeps Version="2" in the catalogue, and the next
	// Set (v4) sweeps and destroys both v2 and v3!
	fake.mu.Lock()
	fake.failNextDestroyStatus = http.StatusInternalServerError
	fake.mu.Unlock()

	if err := store.Set(OpenAIAPIKeySecret, AgentCredentialKey, []byte("v3")); err == nil {
		t.Fatal("expected error when :destroy returns HTTP 500")
	}
	entry, ok, err := cat.GetSecretEntry(ctx, OpenAIAPIKeySecret, AgentCredentialKey)
	if err != nil || !ok || entry.Version != "2" {
		t.Fatalf("catalogue entry after failed destroy = %+v, ok=%v, err=%v; want Version=2", entry, ok, err)
	}

	// Next Set writes v4 and cleans up both v2 and v3.
	if err := store.Set(OpenAIAPIKeySecret, AgentCredentialKey, []byte("v4")); err != nil {
		t.Fatalf("Set v4: %v", err)
	}
	wantStates := []string{"1:DESTROYED", "2:DESTROYED", "3:DESTROYED", "4:ENABLED"}
	if got := fake.versionStates(resName); !reflect.DeepEqual(got, wantStates) {
		t.Fatalf("after v4 recovery: got %v, want %v", got, wantStates)
	}
	if got, err := store.Resolve(ctx, OpenAIAPIKeySecret); err != nil || got != "v4" {
		t.Fatalf("Resolve after v4 = %q, %v", got, err)
	}

	// Concurrent secret creation returning 409 Conflict succeeds cleanly.
	fake.mu.Lock()
	fake.conflictNextCreate = true
	fake.mu.Unlock()
	if err := store.Set(ClaudeOAuthTokenSecret, AgentCredentialKey, []byte("claude-tok")); err != nil {
		t.Fatalf("Set with 409 on createSecret: %v", err)
	}
	if got, err := store.Resolve(ctx, ClaudeOAuthTokenSecret); err != nil || got != "claude-tok" {
		t.Fatalf("Resolve after 409 create = %q, %v", got, err)
	}

	fake.assertZeroListCalls(t)
}

func TestSecretManagerCacheAndInvalidation(t *testing.T) {
	const prefix = "projects/123456789/secrets/grain-inst-bob-"
	fake, srv := newFakeSecretManager(t, prefix)
	cat := NewMemoryCatalogue()

	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	nowFn := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		clockMu.Lock()
		now = now.Add(d)
		clockMu.Unlock()
	}

	cfg := SecretManagerConfig{
		Prefix:     prefix,
		Catalogue:  cat,
		Endpoint:   srv.URL,
		HTTPClient: srv.Client(),
		Token:      func(context.Context) (string, error) { return fake.token, nil },
		CacheTTL:   10 * time.Second,
		Now:        nowFn,
	}
	writer := OpenSecretManager(cfg)
	// Open a second Store sharing the catalogue + fake SM to simulate cold reads
	// that populate the read cache via :access.
	reader := OpenSecretManager(cfg)
	ctx := context.Background()

	if err := writer.Set("api-key", "value", []byte("initial")); err != nil {
		t.Fatal(err)
	}

	// First read on reader hits Secret Manager :access once; second read within TTL hits cache.
	for i := 0; i < 3; i++ {
		got, err := reader.Resolve(ctx, "api-key")
		if err != nil || got != "initial" {
			t.Fatalf("reader.Resolve #%d = %q, %v", i, got, err)
		}
	}
	fake.mu.Lock()
	accessAfterWarm := fake.accessCalls
	fake.mu.Unlock()
	if accessAfterWarm != 1 {
		t.Fatalf("expected 1 :access call across 3 cached reads, got %d", accessAfterWarm)
	}

	// Local Set on reader updates its own cache immediately without needing :access.
	if err := reader.Set("api-key", "value", []byte("updated-locally")); err != nil {
		t.Fatal(err)
	}
	if got, err := reader.Resolve(ctx, "api-key"); err != nil || got != "updated-locally" {
		t.Fatalf("after local Set, Resolve = %q, %v", got, err)
	}

	// Advance past TTL: next Resolve re-fetches from Secret Manager.
	advance(11 * time.Second)
	if got, err := reader.Resolve(ctx, "api-key"); err != nil || got != "updated-locally" {
		t.Fatalf("after TTL expiry, Resolve = %q, %v", got, err)
	}
	fake.mu.Lock()
	accessAfterExpiry := fake.accessCalls
	fake.mu.Unlock()
	if accessAfterExpiry != 2 {
		t.Fatalf("expected 2 :access calls after TTL expiry, got %d", accessAfterExpiry)
	}

	// Local DeleteKey evicts cache immediately.
	if err := reader.DeleteKey("api-key", "value"); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Resolve(ctx, "api-key"); !errors.Is(err, ErrNoSecret) {
		t.Fatalf("after DeleteKey, Resolve = %v, want ErrNoSecret", err)
	}

	fake.assertZeroListCalls(t)
}

func TestSecretManagerSurvivesInstanceReplacementViaCatalogue(t *testing.T) {
	const prefix = "projects/123456789/secrets/grain-inst-carol-"
	fake, srv := newFakeSecretManager(t, prefix)
	sharedCatalogue := NewMemoryCatalogue()
	ctx := context.Background()

	vm1 := OpenSecretManager(SecretManagerConfig{
		Prefix:     prefix,
		Catalogue:  sharedCatalogue,
		Endpoint:   srv.URL,
		HTTPClient: srv.Client(),
		Token:      func(context.Context) (string, error) { return fake.token, nil },
	})
	if err := vm1.Set(GeminiAPIKeySecret, AgentCredentialKey, []byte("gem-secret")); err != nil {
		t.Fatal(err)
	}
	if err := vm1.Set("github-app", "private-key.pem", []byte("-----BEGIN PRIVATE KEY-----\nabc\n")); err != nil {
		t.Fatal(err)
	}

	// Simulate VM replacement: brand-new Store attached to the same external
	// Catalogue and Secret Manager prefix, with empty memory cache and zero local files.
	vm2 := OpenSecretManager(SecretManagerConfig{
		Prefix:     prefix,
		Catalogue:  sharedCatalogue,
		Endpoint:   srv.URL,
		HTTPClient: srv.Client(),
		Token:      func(context.Context) (string, error) { return fake.token, nil },
	})
	list, err := vm2.List()
	if err != nil {
		t.Fatalf("vm2.List: %v", err)
	}
	want := []SecretInfo{
		{Name: GeminiAPIKeySecret, Keys: []string{"value"}},
		{Name: "github-app", Keys: []string{"private-key.pem"}},
	}
	if !reflect.DeepEqual(list, want) {
		t.Fatalf("vm2.List() = %+v, want %+v", list, want)
	}
	if got, err := vm2.Resolve(ctx, GeminiAPIKeySecret); err != nil || got != "gem-secret" {
		t.Fatalf("vm2.Resolve(gemini-api-key) = %q, %v", got, err)
	}
	if got, err := vm2.Resolve(ctx, "github-app/private-key.pem"); err != nil || got != "-----BEGIN PRIVATE KEY-----\nabc\n" {
		t.Fatalf("vm2.Resolve(github-app/private-key.pem) = %q, %v", got, err)
	}
	fake.assertZeroListCalls(t)
}

func TestSecretManagerStoreErrorsAreNotErrNoSecret(t *testing.T) {
	ctx := context.Background()

	// 1. Nil catalogue.
	badCat := OpenSecretManager(SecretManagerConfig{Prefix: "projects/123/secrets/grain-"})
	if _, err := badCat.Resolve(ctx, "gemini-api-key"); err == nil || errors.Is(err, ErrNoSecret) {
		t.Fatalf("nil catalogue Resolve = %v, want non-ErrNoSecret error", err)
	}

	// 2. Bad prefix.
	badPref := OpenSecretManager(SecretManagerConfig{Prefix: "bad-prefix", Catalogue: NewMemoryCatalogue()})
	if _, err := badPref.Resolve(ctx, "gemini-api-key"); err == nil || errors.Is(err, ErrNoSecret) {
		t.Fatalf("bad prefix Resolve = %v, want non-ErrNoSecret error", err)
	}

	// 3. Unauthorized token (HTTP 401 from Secret Manager).
	const prefix = "projects/123/secrets/grain-"
	fake, srv := newFakeSecretManager(t, prefix)
	cat := NewMemoryCatalogue()
	_ = cat.PutSecretEntry(ctx, CatalogueEntry{SecretName: "gemini-api-key", Key: "value", SMSecret: "grain-gemini-api-key"})
	unauth := OpenSecretManager(SecretManagerConfig{
		Prefix:     prefix,
		Catalogue:  cat,
		Endpoint:   srv.URL,
		HTTPClient: srv.Client(),
		Token:      func(context.Context) (string, error) { return "wrong-token", nil },
	})
	if _, err := unauth.Resolve(ctx, "gemini-api-key"); err == nil || errors.Is(err, ErrNoSecret) {
		t.Fatalf("HTTP 401 Resolve = %v, want non-ErrNoSecret error", err)
	}

	// 4. Secret recorded in catalogue but missing in Secret Manager (HTTP 404).
	missingRemote := OpenSecretManager(SecretManagerConfig{
		Prefix:     prefix,
		Catalogue:  cat,
		Endpoint:   srv.URL,
		HTTPClient: srv.Client(),
		Token:      func(context.Context) (string, error) { return fake.token, nil },
	})
	if _, err := missingRemote.Resolve(ctx, "gemini-api-key"); err == nil || errors.Is(err, ErrNoSecret) {
		t.Fatalf("dangling catalogue entry Resolve = %v, want non-ErrNoSecret error", err)
	}
}

func TestSecretManagerAuditAndKnownValues(t *testing.T) {
	smStore, _, _ := newTestSMStore(t)
	fileStore := New(t.TempDir())

	for _, tc := range []struct {
		name  string
		store *Store
	}{
		{"file", fileStore},
		{"secretmanager", smStore},
	} {
		t.Run(tc.name, func(t *testing.T) {
			audit := &mockAuditLogger{}
			tc.store.SetAuditLogger(audit)

			if err := tc.store.Set("zeta", "token", []byte("z-secret")); err != nil {
				t.Fatal(err)
			}
			if err := tc.store.Set("alpha", "key", []byte("a-secret")); err != nil {
				t.Fatal(err)
			}

			// KnownValues returns sorted entries and emits ZERO audit records.
			known, err := tc.store.KnownValues(context.Background())
			if err != nil {
				t.Fatalf("KnownValues: %v", err)
			}
			wantKnown := []SecretValue{
				{SecretName: "alpha", Key: "key", Value: "a-secret"},
				{SecretName: "zeta", Key: "token", Value: "z-secret"},
			}
			if !reflect.DeepEqual(known, wantKnown) {
				t.Fatalf("KnownValues = %+v, want %+v", known, wantKnown)
			}
			audit.mu.Lock()
			if len(audit.entries) != 0 {
				t.Fatalf("KnownValues emitted %d audit entries, want 0", len(audit.entries))
			}
			audit.mu.Unlock()

			// Resolve DOES emit an audit record with resolved key name (even on bare name).
			ctx := WithPrincipal(WithRun(WithTask(context.Background(), "t-1"), "r-1"), "alice@example.com")
			got, err := tc.store.Resolve(ctx, "alpha")
			if err != nil || got != "a-secret" {
				t.Fatalf("Resolve(alpha) = %q, %v", got, err)
			}
			audit.mu.Lock()
			defer audit.mu.Unlock()
			if len(audit.entries) != 1 {
				t.Fatalf("Resolve emitted %d audit entries, want 1", len(audit.entries))
			}
			e := audit.entries[0]
			if e.SecretName != "alpha" || e.Key != "key" || e.TaskID != "t-1" || e.RunID != "r-1" || e.Principal != "alice@example.com" {
				t.Fatalf("unexpected audit entry: %+v", e)
			}
		})
	}
}

func TestSecretManagerMetadataServerTokenCaching(t *testing.T) {
	const prefix = "projects/123/secrets/grain-"
	fake, smSrv := newFakeSecretManager(t, prefix)

	metaCalls := 0
	metaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Metadata-Flavor") != "Google" {
			http.Error(w, "missing Metadata-Flavor", http.StatusForbidden)
			return
		}
		metaCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":%q,"expires_in":3600}`, fake.token)
	}))
	defer metaSrv.Close()

	t.Setenv("GCE_METADATA_HOST", strings.TrimPrefix(metaSrv.URL, "http://"))
	t.Setenv(SecretManagerTokenEnv, "")
	t.Setenv(SecretManagerEndpointEnv, smSrv.URL)

	store := OpenSecretManager(SecretManagerConfig{
		Prefix:    prefix,
		Catalogue: NewMemoryCatalogue(),
		CacheTTL:  -1, // disable value cache so both Resolves hit SM
	})
	if err := store.Set("k1", "value", []byte("v1")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := store.Resolve(context.Background(), "k1"); err != nil {
		t.Fatalf("Resolve 1: %v", err)
	}
	if _, err := store.Resolve(context.Background(), "k1"); err != nil {
		t.Fatalf("Resolve 2: %v", err)
	}
	if metaCalls != 1 {
		t.Fatalf("expected metadata token to be cached across calls (got %d calls, want 1)", metaCalls)
	}

	// Verify GRAIN_SECRET_MANAGER_TOKEN env override also works without hitting metadata.
	t.Setenv(SecretManagerTokenEnv, fake.token)
	storeEnvTok := OpenSecretManager(SecretManagerConfig{
		Prefix:    prefix,
		Catalogue: NewMemoryCatalogue(),
		CacheTTL:  -1,
	})
	if err := storeEnvTok.Set("k2", "value", []byte("val-2")); err != nil {
		t.Fatalf("Set with env token: %v", err)
	}
	if metaCalls != 1 {
		t.Fatalf("GRAIN_SECRET_MANAGER_TOKEN hit metadata server (%d calls)", metaCalls)
	}
}

// A catalogued key deleted out of band in Secret Manager (HTTP 404) or left
// with no enabled version must not fail KnownValues as a whole -- that refused
// every transcript upload and its database fallback (task 3577) -- but it must
// still be surfaced, by MissingUpstream and by Resolve's ErrGoneUpstream.
func TestSecretManagerKnownValuesSkipsKeysGoneUpstream(t *testing.T) {
	ctx := context.Background()
	writer, fake, cat := newTestSMStore(t)
	for _, kv := range []struct{ secret, key, val string }{
		{"multi", "a", "a-secret"},
		{"multi", "b", "b-secret"},
		{"solo", "value", "solo-secret"},
		{"zeta", "token", "z-secret"},
	} {
		if err := writer.Set(kv.secret, kv.key, []byte(kv.val)); err != nil {
			t.Fatal(err)
		}
	}
	resourceOf := func(secret, key string) string {
		t.Helper()
		entry, ok, err := cat.GetSecretEntry(ctx, secret, key)
		if err != nil || !ok {
			t.Fatalf("catalogue entry %s/%s: ok=%v err=%v", secret, key, ok, err)
		}
		id, err := writer.sm.secretIDForEntry(entry)
		if err != nil {
			t.Fatal(err)
		}
		res, err := writer.sm.resourceName(id)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	// multi/b: the secret itself deleted out of band.
	// zeta/token: the secret kept, every version destroyed.
	deleted, destroyed := resourceOf("multi", "b"), resourceOf("zeta", "token")
	fake.mu.Lock()
	delete(fake.secrets, deleted)
	for _, v := range fake.secrets[destroyed].versions {
		v.state, v.data = "DESTROYED", nil
	}
	fake.mu.Unlock()

	// A fresh instance over the same catalogue: nothing cached, as after a
	// restart or an instance replacement.
	reader := OpenSecretManager(SecretManagerConfig{
		Prefix:     writer.sm.prefix,
		Catalogue:  cat,
		Endpoint:   writer.sm.endpoint,
		HTTPClient: writer.sm.httpClient,
		Token:      func(context.Context) (string, error) { return fake.token, nil },
	})
	for _, s := range []*Store{reader, writer} {
		got, err := s.MissingUpstream(ctx)
		if err != nil {
			t.Fatalf("MissingUpstream: %v", err)
		}
		want := []SecretKeyRef{{SecretName: "multi", Key: "b"}, {SecretName: "zeta", Key: "token"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("MissingUpstream = %+v, want %+v", got, want)
		}
	}

	known, err := reader.KnownValues(ctx)
	if err != nil {
		t.Fatalf("KnownValues with keys gone upstream = %v, want the rest and no error", err)
	}
	wantKnown := []SecretValue{
		{SecretName: "multi", Key: "a", Value: "a-secret"},
		{SecretName: "solo", Key: "value", Value: "solo-secret"},
	}
	if !reflect.DeepEqual(known, wantKnown) {
		t.Fatalf("KnownValues = %+v, want %+v", known, wantKnown)
	}

	_, err = reader.Resolve(ctx, "multi/b")
	if !errors.Is(err, ErrGoneUpstream) || errors.Is(err, ErrNoSecret) {
		t.Fatalf("Resolve(multi/b) = %v, want ErrGoneUpstream and not ErrNoSecret", err)
	}
	if !strings.Contains(err.Error(), "grain secrets delete multi b") {
		t.Fatalf("Resolve(multi/b) = %v, want it to name the reconciling command", err)
	}

	// Gone upstream is the only failure KnownValues forgives: one it cannot
	// read at all still fails closed.
	unauth := OpenSecretManager(SecretManagerConfig{
		Prefix:     writer.sm.prefix,
		Catalogue:  cat,
		Endpoint:   writer.sm.endpoint,
		HTTPClient: writer.sm.httpClient,
		Token:      func(context.Context) (string, error) { return "wrong-token", nil },
	})
	if _, err := unauth.KnownValues(ctx); err == nil || errors.Is(err, ErrGoneUpstream) {
		t.Fatalf("KnownValues with HTTP 401 = %v, want a non-ErrGoneUpstream error", err)
	}
	if _, err := unauth.MissingUpstream(ctx); err == nil {
		t.Fatal("MissingUpstream with HTTP 401 = nil, want an error")
	}

	// Once reconciled, nothing is reported missing.
	if err := reader.DeleteKey("multi", "b"); err != nil {
		t.Fatal(err)
	}
	if err := reader.Set("zeta", "token", []byte("z-again")); err != nil {
		t.Fatal(err)
	}
	if got, err := reader.MissingUpstream(ctx); err != nil || len(got) != 0 {
		t.Fatalf("MissingUpstream after reconciling = %+v, %v; want none", got, err)
	}
}

func TestAccessGoneUpstream(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   bool
	}{
		{http.StatusNotFound, `{"error":{"code":404,"status":"NOT_FOUND"}}`, true},
		{http.StatusBadRequest, `{"error":{"code":400,"status":"FAILED_PRECONDITION","message":"Secret Version [projects/1/secrets/x/versions/3] is in DESTROYED state."}}`, true},
		{http.StatusBadRequest, `{"error":{"code":400,"status":"FAILED_PRECONDITION","message":"Secret Version [projects/1/secrets/x/versions/3] is in DISABLED state."}}`, true},
		{http.StatusBadRequest, `{"error":{"code":400,"status":"INVALID_ARGUMENT"}}`, false},
		{http.StatusForbidden, `{"error":{"code":403,"status":"PERMISSION_DENIED"}}`, false},
		{http.StatusUnauthorized, `{"error":{"code":401}}`, false},
		{http.StatusServiceUnavailable, `unavailable`, false},
		// A 404 that is not a Secret Manager error -- a wrong endpoint, a
		// proxy -- says nothing about the secret and must stay fatal, or it
		// would drop every value from redaction at once.
		{http.StatusNotFound, "404 page not found\n", false},
		{http.StatusNotFound, `<html><body>Not Found</body></html>`, false},
		{http.StatusNotFound, `{"error":{"code":403,"status":"PERMISSION_DENIED"}}`, false},
		{http.StatusBadRequest, `FAILED_PRECONDITION: version is DESTROYED`, false},
	} {
		if got := accessGoneUpstream(tc.status, []byte(tc.body)); got != tc.want {
			t.Errorf("accessGoneUpstream(%d, %s) = %v, want %v", tc.status, tc.body, got, tc.want)
		}
	}
}

// A value this process read before its key went from Secret Manager may still
// be in a running sandbox's output, so it stays in KnownValues -- even past the
// read cache's expiry -- while it no longer resolves, and until the key is
// reconciled.
func TestSecretManagerKnownValuesRetainsValueReadBeforeItWentUpstream(t *testing.T) {
	ctx := context.Background()
	writer, fake, cat := newTestSMStore(t)
	if err := writer.Set("tok", "value", []byte("handed-out-secret")); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	reader := OpenSecretManager(SecretManagerConfig{
		Prefix:     writer.sm.prefix,
		Catalogue:  cat,
		Endpoint:   writer.sm.endpoint,
		HTTPClient: writer.sm.httpClient,
		Token:      func(context.Context) (string, error) { return fake.token, nil },
		Now:        func() time.Time { return now },
	})
	if _, err := reader.Resolve(ctx, "tok"); err != nil {
		t.Fatalf("Resolve(tok): %v", err)
	}

	fake.mu.Lock()
	for res := range fake.secrets {
		delete(fake.secrets, res)
	}
	fake.mu.Unlock()
	now = now.Add(2 * DefaultSecretManagerCacheTTL)

	if _, err := reader.Resolve(ctx, "tok"); !errors.Is(err, ErrGoneUpstream) {
		t.Fatalf("Resolve(tok) after deletion upstream = %v, want ErrGoneUpstream", err)
	}
	want := []SecretValue{{SecretName: "tok", Key: "value", Value: "handed-out-secret"}}
	for i := 0; i < 2; i++ {
		known, err := reader.KnownValues(ctx)
		if err != nil {
			t.Fatalf("KnownValues: %v", err)
		}
		if !reflect.DeepEqual(known, want) {
			t.Fatalf("KnownValues #%d = %+v, want the value read before it went, %+v", i, known, want)
		}
	}

	if err := reader.DeleteKey("tok", "value"); err != nil {
		t.Fatal(err)
	}
	if known, err := reader.KnownValues(ctx); err != nil || len(known) != 0 {
		t.Fatalf("KnownValues after reconciling = %+v, %v; want none", known, err)
	}
}
