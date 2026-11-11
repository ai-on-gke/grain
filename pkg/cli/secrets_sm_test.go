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

package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

func TestDaemonSecretsFlagsAndValidation(t *testing.T) {
	for _, env := range []string{
		dataDirEnvVar, "GRAIN_DB_DRIVER", "GRAIN_DB_DSN", "GRAIN_POSTGRES_URL", "DATABASE_URL",
		"GRAIN_SECRETS_BACKEND", "GRAIN_SECRETS_SM_PREFIX",
	} {
		t.Setenv(env, "")
	}

	baseArgs := []string{
		"-data-dir", t.TempDir(),
		"-sandbox-dir", t.TempDir(),
		"-host-sandboxes",
		"-allow-unisolated-host-sandboxes",
	}

	t.Run("default_file_backend", func(t *testing.T) {
		cfg, _, _, err := parseDaemonArgs(baseArgs)
		if err != nil {
			t.Fatalf("parseDaemonArgs: %v", err)
		}
		if cfg.SecretsBackend != secrets.BackendFile {
			t.Errorf("SecretsBackend = %q, want %q", cfg.SecretsBackend, secrets.BackendFile)
		}
		if cfg.SecretsSMPrefix != "" {
			t.Errorf("SecretsSMPrefix = %q, want empty", cfg.SecretsSMPrefix)
		}
		if err := checkConfig(cfg); err != nil {
			t.Fatalf("checkConfig: %v", err)
		}
	})

	t.Run("secretmanager_flags", func(t *testing.T) {
		prefix := "projects/123456789/secrets/grain-inst-alice-"
		args := append(append([]string{}, baseArgs...),
			"-secrets-backend=secretmanager",
			"-secrets-sm-prefix="+prefix,
		)
		cfg, _, _, err := parseDaemonArgs(args)
		if err != nil {
			t.Fatalf("parseDaemonArgs: %v", err)
		}
		if cfg.SecretsBackend != secrets.BackendSecretManager {
			t.Errorf("SecretsBackend = %q, want %q", cfg.SecretsBackend, secrets.BackendSecretManager)
		}
		if cfg.SecretsSMPrefix != prefix {
			t.Errorf("SecretsSMPrefix = %q, want %q", cfg.SecretsSMPrefix, prefix)
		}
		if err := checkConfig(cfg); err != nil {
			t.Fatalf("checkConfig: %v", err)
		}
	})

	t.Run("env_fallbacks_and_flag_precedence", func(t *testing.T) {
		envPrefix := "projects/123456789/secrets/grain-inst-env-"
		flagPrefix := "projects/123456789/secrets/grain-inst-flag-"
		t.Setenv("GRAIN_SECRETS_BACKEND", secrets.BackendSecretManager)
		t.Setenv("GRAIN_SECRETS_SM_PREFIX", envPrefix)

		cfg, _, _, err := parseDaemonArgs(baseArgs)
		if err != nil {
			t.Fatalf("parseDaemonArgs: %v", err)
		}
		if cfg.SecretsBackend != secrets.BackendSecretManager || cfg.SecretsSMPrefix != envPrefix {
			t.Fatalf("env fallback got (%q, %q), want (%q, %q)",
				cfg.SecretsBackend, cfg.SecretsSMPrefix, secrets.BackendSecretManager, envPrefix)
		}
		if err := checkConfig(cfg); err != nil {
			t.Fatalf("checkConfig: %v", err)
		}

		overrideArgs := append(append([]string{}, baseArgs...), "-secrets-sm-prefix="+flagPrefix)
		cfg2, _, _, err := parseDaemonArgs(overrideArgs)
		if err != nil {
			t.Fatalf("parseDaemonArgs override: %v", err)
		}
		if cfg2.SecretsSMPrefix != flagPrefix {
			t.Errorf("flag override got %q, want %q", cfg2.SecretsSMPrefix, flagPrefix)
		}
	})

	t.Run("validation_errors", func(t *testing.T) {
		cases := []struct {
			name    string
			extra   []string
			wantSub string
		}{
			{
				name:    "missing_prefix",
				extra:   []string{"-secrets-backend=secretmanager"},
				wantSub: "-secrets-sm-prefix is required",
			},
			{
				name:    "prefix_without_secretmanager",
				extra:   []string{"-secrets-sm-prefix=projects/123/secrets/grain-inst-a-"},
				wantSub: "-secrets-sm-prefix requires -secrets-backend=secretmanager",
			},
			{
				name:    "invalid_backend",
				extra:   []string{"-secrets-backend=vault"},
				wantSub: "-secrets-backend must be",
			},
			{
				name:    "invalid_prefix_shape",
				extra:   []string{"-secrets-backend=secretmanager", "-secrets-sm-prefix=bad-prefix"},
				wantSub: "projects/<project>/secrets/<prefix>",
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				args := append(append([]string{}, baseArgs...), tc.extra...)
				cfg, _, _, err := parseDaemonArgs(args)
				if err != nil {
					t.Fatalf("parseDaemonArgs: %v", err)
				}
				err = checkConfig(cfg)
				if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
					t.Fatalf("checkConfig error = %v, want substring %q", err, tc.wantSub)
				}
			})
		}
	})
}

type fakeCLISecretManager struct {
	mu           sync.Mutex
	project      string
	allowedIDPre string
	token        string
	secrets      map[string]map[int][]byte // secretID -> version -> payload (nil = destroyed)
	nextVer      map[string]int
	listCalls    int
	destroyCalls int
}

func newFakeCLISecretManager(t *testing.T, project, allowedIDPrefix, token string) (*fakeCLISecretManager, *httptest.Server) {
	t.Helper()
	f := &fakeCLISecretManager{
		project:      project,
		allowedIDPre: allowedIDPrefix,
		token:        token,
		secrets:      map[string]map[int][]byte{},
		nextVer:      map[string]int{},
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeCLISecretManager) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		http.Error(w, `{"error":{"code":401,"message":"unauthenticated"}}`, http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	prefix := "/v1/projects/" + f.project + "/secrets"
	if r.URL.Path == prefix && r.Method == http.MethodGet {
		f.listCalls++
		http.Error(w, `{"error":{"code":403,"message":"secrets.list denied"}}`, http.StatusForbidden)
		return
	}
	if r.URL.Path == prefix && r.Method == http.MethodPost {
		secretID := r.URL.Query().Get("secretId")
		if !strings.HasPrefix(secretID, f.allowedIDPre) {
			http.Error(w, `{"error":{"code":403,"message":"prefix denied"}}`, http.StatusForbidden)
			return
		}
		if _, exists := f.secrets[secretID]; exists {
			http.Error(w, `{"error":{"code":409,"message":"already exists"}}`, http.StatusConflict)
			return
		}
		f.secrets[secretID] = map[int][]byte{}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"name":"projects/%s/secrets/%s"}`, f.project, secretID)
		return
	}

	if !strings.HasPrefix(r.URL.Path, prefix+"/") {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, prefix+"/")
	secretID, tail, hasTail := strings.Cut(rest, "/")
	secretID, secretAction, hasSecretAction := strings.Cut(secretID, ":")
	if !strings.HasPrefix(secretID, f.allowedIDPre) {
		http.Error(w, `{"error":{"code":403,"message":"prefix denied"}}`, http.StatusForbidden)
		return
	}

	if !hasTail && !hasSecretAction && r.Method == http.MethodDelete {
		if _, ok := f.secrets[secretID]; !ok {
			http.Error(w, `{"error":{"code":404,"message":"not found"}}`, http.StatusNotFound)
			return
		}
		delete(f.secrets, secretID)
		delete(f.nextVer, secretID)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
		return
	}

	if !hasTail && hasSecretAction && secretAction == "addVersion" && r.Method == http.MethodPost {
		vers, ok := f.secrets[secretID]
		if !ok {
			http.Error(w, `{"error":{"code":404,"message":"secret not found"}}`, http.StatusNotFound)
			return
		}
		var body struct {
			Payload struct {
				Data string `json:"data"`
			} `json:"payload"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		raw, err := base64.StdEncoding.DecodeString(body.Payload.Data)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.nextVer[secretID]++
		v := f.nextVer[secretID]
		vers[v] = raw
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"name":"projects/%s/secrets/%s/versions/%d","state":"ENABLED"}`, f.project, secretID, v)
		return
	}

	if hasTail && tail == "versions" && r.Method == http.MethodGet {
		f.listCalls++
		http.Error(w, `{"error":{"code":403,"message":"versions.list denied"}}`, http.StatusForbidden)
		return
	}

	if hasTail && strings.HasPrefix(tail, "versions/") && r.Method == http.MethodGet && strings.HasSuffix(tail, ":access") {
		verSpec := strings.TrimSuffix(strings.TrimPrefix(tail, "versions/"), ":access")
		vers, ok := f.secrets[secretID]
		if !ok {
			http.Error(w, `{"error":{"code":404,"message":"secret not found"}}`, http.StatusNotFound)
			return
		}
		var target int
		if verSpec == "latest" {
			for v, data := range vers {
				if data != nil && v > target {
					target = v
				}
			}
		} else {
			target, _ = strconv.Atoi(verSpec)
		}
		if target == 0 || vers[target] == nil {
			http.Error(w, `{"error":{"code":404,"message":"no enabled version"}}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"name":"projects/%s/secrets/%s/versions/%d","payload":{"data":%q}}`,
			f.project, secretID, target, base64.StdEncoding.EncodeToString(vers[target]))
		return
	}

	if hasTail && strings.HasPrefix(tail, "versions/") && r.Method == http.MethodPost && strings.HasSuffix(tail, ":destroy") {
		verSpec := strings.TrimSuffix(strings.TrimPrefix(tail, "versions/"), ":destroy")
		v, err := strconv.Atoi(verSpec)
		if err != nil {
			http.Error(w, "invalid version", http.StatusBadRequest)
			return
		}
		vers, ok := f.secrets[secretID]
		if !ok {
			http.Error(w, `{"error":{"code":404,"message":"secret not found"}}`, http.StatusNotFound)
			return
		}
		if _, exists := vers[v]; !exists {
			http.Error(w, `{"error":{"code":404,"message":"version not found"}}`, http.StatusNotFound)
			return
		}
		if vers[v] == nil {
			http.Error(w, `{"error":{"code":400,"message":"already destroyed"}}`, http.StatusBadRequest)
			return
		}
		vers[v] = nil
		f.destroyCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"name":"projects/%s/secrets/%s/versions/%d","state":"DESTROYED"}`, f.project, secretID, v)
		return
	}

	http.NotFound(w, r)
}

func captureSecretsStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()
	runErr := fn()
	_ = w.Close()
	os.Stdout = orig
	<-done
	_ = r.Close()
	return buf.String(), runErr
}

func TestSecretsCLISecretManagerLifecycleAndRedaction(t *testing.T) {
	fake, srv := newFakeCLISecretManager(t, "987654321", "grain-inst-alice-", "sm-test-token")
	prefix := "projects/987654321/secrets/grain-inst-alice-"

	t.Setenv(secrets.SecretManagerEndpointEnv, srv.URL)
	t.Setenv(secrets.SecretManagerTokenEnv, "sm-test-token")
	t.Setenv("GRAIN_SECRETS_BACKEND", secrets.BackendSecretManager)
	t.Setenv("GRAIN_SECRETS_SM_PREFIX", prefix)
	t.Setenv("GRAIN_DB_DRIVER", "")
	t.Setenv("GRAIN_DB_DSN", "")
	t.Setenv("GRAIN_POSTGRES_URL", "")
	t.Setenv("DATABASE_URL", "")

	dataDir := t.TempDir()
	valFile1 := filepath.Join(t.TempDir(), "v1.txt")
	valFile2 := filepath.Join(t.TempDir(), "v2.txt")
	if err := os.WriteFile(valFile1, []byte("alpha-secret-token-v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(valFile2, []byte("alpha-secret-token-v2"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Set initial secret via CLI.
	out, err := captureSecretsStdout(t, func() error {
		return runSecrets([]string{"-data-dir", dataDir, "set", "-value-file", valFile1, "gemini-api-key", "value"})
	})
	if err != nil {
		t.Fatalf("runSecrets set v1: %v", err)
	}
	if !strings.Contains(out, "set gemini-api-key/value") {
		t.Errorf("set output = %q, want confirmation", out)
	}

	// Overwrite via CLI -> must destroy version 1 cleanly.
	if _, err := captureSecretsStdout(t, func() error {
		return runSecrets([]string{"-data-dir", dataDir, "set", "-value-file", valFile2, "gemini-api-key", "value"})
	}); err != nil {
		t.Fatalf("runSecrets set v2: %v", err)
	}
	fake.mu.Lock()
	destroys := fake.destroyCalls
	lists := fake.listCalls
	fake.mu.Unlock()
	if destroys != 1 {
		t.Errorf("destroyCalls = %d, want 1 after overwrite", destroys)
	}
	if lists != 0 {
		t.Errorf("listCalls = %d, want 0", lists)
	}

	// List prints secret name and key, never value, and creates no local secrets.key.
	listOut, err := captureSecretsStdout(t, func() error {
		return runSecrets([]string{"-data-dir", dataDir, "list"})
	})
	if err != nil {
		t.Fatalf("runSecrets list: %v", err)
	}
	if strings.TrimSpace(listOut) != "gemini-api-key: value" {
		t.Errorf("list output = %q, want %q", listOut, "gemini-api-key: value\n")
	}
	if strings.Contains(listOut, "alpha-secret-token") {
		t.Fatalf("list output leaked secret value: %q", listOut)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "secrets", secrets.DefaultKeyFileName)); !os.IsNotExist(err) {
		t.Errorf("expected no local secrets.key under -secrets-backend=secretmanager, stat err = %v", err)
	}

	// Redaction helpers read Secret Manager values without local secrets.key and without emitting audit rows.
	catStore, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	smStore, err := daemon.OpenSecretsForConfig(daemon.Config{
		DataDir:         dataDir,
		SecretsBackend:  secrets.BackendSecretManager,
		SecretsSMPrefix: prefix,
	}, catStore)
	if err != nil {
		db.Close()
		t.Fatalf("OpenSecretsForConfig: %v", err)
	}
	smStore.SetAuditLogger(catStore)
	known, err := daemon.KnownSecretValuesFromStore(context.Background(), smStore)
	if err != nil {
		db.Close()
		t.Fatalf("KnownSecretValuesFromStore: %v", err)
	}
	if len(known) != 1 || known[0].Value != "alpha-secret-token-v2" {
		db.Close()
		t.Fatalf("KnownSecretValuesFromStore = %+v, want alpha-secret-token-v2", known)
	}
	audits, err := catStore.ListSecretAudit(context.Background(), 10)
	if err != nil {
		db.Close()
		t.Fatalf("ListSecretAudit: %v", err)
	}
	if len(audits) != 0 {
		db.Close()
		t.Errorf("KnownSecretValuesFromStore emitted %d audit entries, want 0", len(audits))
	}

	// Namespace redactor via env vars also redacts Secret Manager values with no secrets.key on disk.
	nsRedactor := daemon.BuildNamespaceRedactor(dataDir)
	redacted, _ := nsRedactor.Text("Authorization: Bearer alpha-secret-token-v2")
	if strings.Contains(redacted, "alpha-secret-token-v2") {
		db.Close()
		t.Errorf("BuildNamespaceRedactor failed to redact Secret Manager secret: %q", redacted)
	}
	_ = db.Close()

	// repo subcommand is refused under secretmanager.
	if err := runSecrets([]string{"-data-dir", dataDir, "repo"}); err == nil {
		t.Fatal("expected grain secrets repo to fail under -secrets-backend=secretmanager")
	}

	// Delete removes the secret from Secret Manager and catalogue.
	if _, err := captureSecretsStdout(t, func() error {
		return runSecrets([]string{"-data-dir", dataDir, "delete", "gemini-api-key"})
	}); err != nil {
		t.Fatalf("runSecrets delete: %v", err)
	}
	afterDel, err := captureSecretsStdout(t, func() error {
		return runSecrets([]string{"-data-dir", dataDir, "list"})
	})
	if err != nil {
		t.Fatalf("runSecrets list after delete: %v", err)
	}
	if strings.TrimSpace(afterDel) != "no secrets set" {
		t.Errorf("list after delete = %q, want %q", afterDel, "no secrets set")
	}
}

// TestSecretsCLIRefusesANewSQLiteCatalogueBesideAPostgresDaemon is the
// 2026-11-07 walkthrough's orphaned secret: -data-dir alone resolved to
// SQLite next to a daemon writing to PostgreSQL, and `grain secrets set`
// catalogued the secret in a brand-new <data-dir>/store.
func TestSecretsCLIRefusesANewSQLiteCatalogueBesideAPostgresDaemon(t *testing.T) {
	t.Setenv(secrets.SecretManagerEndpointEnv, "http://127.0.0.1:1")
	t.Setenv(secrets.SecretManagerTokenEnv, "sm-test-token")
	t.Setenv("GRAIN_SECRETS_BACKEND", secrets.BackendSecretManager)
	t.Setenv("GRAIN_SECRETS_SM_PREFIX", "projects/987654321/secrets/grain-inst-alice-")
	for _, env := range []string{"GRAIN_DB_DRIVER", "GRAIN_DB_DSN", "GRAIN_POSTGRES_URL", "DATABASE_URL"} {
		t.Setenv(env, "")
	}
	dataDir := t.TempDir()
	mark := `{"lastTaskId":3,"store":"PostgreSQL at postgres://grain:%2A%2A%2A%2A%2A%2A@db:5432/grain"}`
	if err := os.WriteFile(filepath.Join(dataDir, "store-highwater.json"), []byte(mark), 0o644); err != nil {
		t.Fatal(err)
	}
	valFile := filepath.Join(t.TempDir(), "v.txt")
	if err := os.WriteFile(valFile, []byte("v"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runSecrets([]string{"-data-dir", dataDir, "set", "-value-file", valFile, "deploy", "token"})
	if err == nil || !strings.Contains(err.Error(), "-db-driver postgres -db-dsn") || !strings.Contains(err.Error(), "PostgreSQL at postgres://grain:%2A") {
		t.Fatalf("runSecrets set = %v, want a refusal naming the PostgreSQL store and -db-driver/-db-dsn", err)
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, "store")); !os.IsNotExist(statErr) {
		t.Fatalf("%s/store was created (stat err %v); want no new SQLite catalogue", dataDir, statErr)
	}
}

// TestConnectionsImportUnderSecretManagerStoresTheCredentialThere is the
// 2026-11-07 walkthrough's missing model key: under
// GRAIN_SECRETS_BACKEND=secretmanager, `grain connections import` wrote
// the credential to <data-dir>/secrets, which that daemon never reads.
func TestConnectionsImportUnderSecretManagerStoresTheCredentialThere(t *testing.T) {
	ctx := context.Background()
	fake, srv := newFakeCLISecretManager(t, "987654321", "grain-inst-alice-", "sm-test-token")
	t.Setenv(secrets.SecretManagerEndpointEnv, srv.URL)
	t.Setenv(secrets.SecretManagerTokenEnv, "sm-test-token")
	t.Setenv("GRAIN_SECRETS_BACKEND", secrets.BackendSecretManager)
	t.Setenv("GRAIN_SECRETS_SM_PREFIX", "projects/987654321/secrets/grain-inst-alice-")
	for _, env := range []string{"GRAIN_DB_DRIVER", "GRAIN_DB_DSN", "GRAIN_POSTGRES_URL", "DATABASE_URL"} {
		t.Setenv(env, "")
	}
	dataDir := t.TempDir()
	s, db, err := daemon.OpenStore("", "", dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutConfig(ctx, model.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	db.Close()
	dir := t.TempDir()
	key := filepath.Join(dir, "claude.token")
	if err := os.WriteFile(key, []byte("sk-ant-oat01-seed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seed := filepath.Join(dir, "seed.yaml")
	if err := os.WriteFile(seed, []byte("models:\n  - id: claude\n    framework: claude\n    credential_file: "+key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runConnections(ctx, []string{"-data-dir", dataDir, "import", seed}, io.Discard); err != nil {
		t.Fatalf("import: %v", err)
	}

	s, db, err = daemon.OpenStore("", "", dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sm, err := daemon.OpenSecretsForConfig(daemon.Config{
		DataDir: dataDir, SecretsBackend: secrets.BackendSecretManager,
		SecretsSMPrefix: "projects/987654321/secrets/grain-inst-alice-",
	}, s)
	if err != nil {
		t.Fatal(err)
	}
	name := secrets.AgentBackendCredentialSecret("claude") + "/" + secrets.AgentCredentialKey
	got, err := sm.Resolve(ctx, name)
	if err != nil || got != "sk-ant-oat01-seed" {
		t.Fatalf("credential in Secret Manager = (%q, %v), want the imported key", got, err)
	}
	fake.mu.Lock()
	n := len(fake.secrets)
	fake.mu.Unlock()
	if n == 0 {
		t.Fatal("no secret was created in Secret Manager")
	}
	if has, _ := daemon.OpenSecrets(dataDir).Has(secrets.AgentBackendCredentialSecret("claude"), secrets.AgentCredentialKey); has {
		t.Fatal("credential also written to <data-dir>/secrets")
	}
}

// A key the catalogue lists but Secret Manager no longer holds (deleted out of
// band) is marked by `grain secrets list` and left out of redaction rather
// than failing it, until `grain secrets delete` reconciles the two (task 3577).
func TestSecretsCLIListMarksKeysGoneFromSecretManager(t *testing.T) {
	fake, srv := newFakeCLISecretManager(t, "987654321", "grain-inst-alice-", "sm-test-token")
	prefix := "projects/987654321/secrets/grain-inst-alice-"

	t.Setenv(secrets.SecretManagerEndpointEnv, srv.URL)
	t.Setenv(secrets.SecretManagerTokenEnv, "sm-test-token")
	t.Setenv("GRAIN_SECRETS_BACKEND", secrets.BackendSecretManager)
	t.Setenv("GRAIN_SECRETS_SM_PREFIX", prefix)
	t.Setenv("GRAIN_DB_DRIVER", "")
	t.Setenv("GRAIN_DB_DSN", "")
	t.Setenv("GRAIN_POSTGRES_URL", "")
	t.Setenv("DATABASE_URL", "")

	dataDir := t.TempDir()
	for _, kv := range []struct{ key, val string }{{"a", "multi-a-value"}, {"b", "multi-b-value"}} {
		f := filepath.Join(t.TempDir(), kv.key)
		if err := os.WriteFile(f, []byte(kv.val), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := captureSecretsStdout(t, func() error {
			return runSecrets([]string{"-data-dir", dataDir, "set", "-value-file", f, "multi", kv.key})
		}); err != nil {
			t.Fatalf("set multi/%s: %v", kv.key, err)
		}
	}
	// Delete multi/b's Secret Manager secret out of band: the one whose
	// latest version holds multi-b-value.
	fake.mu.Lock()
	for id, versions := range fake.secrets {
		for _, v := range versions {
			if string(v) == "multi-b-value" {
				delete(fake.secrets, id)
			}
		}
	}
	fake.mu.Unlock()

	listOut, err := captureSecretsStdout(t, func() error {
		return runSecrets([]string{"-data-dir", dataDir, "list"})
	})
	if err != nil {
		t.Fatalf("runSecrets list: %v", err)
	}
	if want := "multi: a, b (missing in Secret Manager)"; strings.TrimSpace(listOut) != want {
		t.Fatalf("list output = %q, want %q", listOut, want)
	}

	catStore, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	smStore, err := daemon.OpenSecretsForConfig(daemon.Config{
		DataDir:         dataDir,
		SecretsBackend:  secrets.BackendSecretManager,
		SecretsSMPrefix: prefix,
	}, catStore)
	if err != nil {
		db.Close()
		t.Fatalf("OpenSecretsForConfig: %v", err)
	}
	known, err := daemon.KnownSecretValuesFromStore(context.Background(), smStore)
	_ = db.Close()
	if err != nil {
		t.Fatalf("KnownSecretValuesFromStore with multi/b gone upstream = %v, want multi/a and no error", err)
	}
	if len(known) != 1 || known[0].Value != "multi-a-value" {
		t.Fatalf("KnownSecretValuesFromStore = %+v, want only multi-a-value", known)
	}

	if _, err := captureSecretsStdout(t, func() error {
		return runSecrets([]string{"-data-dir", dataDir, "delete", "multi", "b"})
	}); err != nil {
		t.Fatalf("delete multi b: %v", err)
	}
	listOut, err = captureSecretsStdout(t, func() error {
		return runSecrets([]string{"-data-dir", dataDir, "list"})
	})
	if err != nil {
		t.Fatalf("runSecrets list after delete: %v", err)
	}
	if want := "multi: a"; strings.TrimSpace(listOut) != want {
		t.Fatalf("list output after delete = %q, want %q", listOut, want)
	}
}
