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

package postgres_test

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

	"github.com/ai-on-gke/grain/pkg/secrets"
)

func TestPostgresSecretManagerCatalogueEndToEnd(t *testing.T) {
	t.Parallel()
	pgStore, db, ctx := openStore(t)

	// Minimal in-process Secret Manager v1 HTTP server enforcing IAM prefix
	// and denying secrets.list / versions.list.
	const prefix = "projects/987654321/secrets/grain-inst-pgtest-"
	type ver struct {
		num   int
		state string
		data  []byte
	}
	var mu sync.Mutex
	smSecrets := map[string][]ver{}
	listCalls := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer pg-test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		sub := strings.TrimPrefix(r.URL.Path, "/v1/")
		if r.Method == http.MethodGet && (strings.HasSuffix(sub, "/secrets") || strings.HasSuffix(sub, "/versions")) {
			listCalls++
			http.Error(w, "list denied", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(sub, "/secrets") {
			proj := strings.TrimSuffix(strings.TrimPrefix(sub, "projects/"), "/secrets")
			res := "projects/" + proj + "/secrets/" + r.URL.Query().Get("secretId")
			if !strings.HasPrefix(res, prefix) {
				http.Error(w, "forbidden prefix", http.StatusForbidden)
				return
			}
			if _, ok := smSecrets[res]; ok {
				http.Error(w, "conflict", http.StatusConflict)
				return
			}
			smSecrets[res] = nil
			_ = json.NewEncoder(w).Encode(map[string]any{"name": res})
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(sub, ":addVersion") {
			res := strings.TrimSuffix(sub, ":addVersion")
			vers, ok := smSecrets[res]
			if !ok {
				http.NotFound(w, r)
				return
			}
			var body struct {
				Payload struct {
					Data string `json:"data"`
				} `json:"payload"`
			}
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			decoded, _ := base64.StdEncoding.DecodeString(body.Payload.Data)
			num := len(vers) + 1
			smSecrets[res] = append(vers, ver{num: num, state: "ENABLED", data: decoded})
			_ = json.NewEncoder(w).Encode(map[string]any{"name": fmt.Sprintf("%s/versions/%d", res, num), "state": "ENABLED"})
			return
		}
		if r.Method == http.MethodGet && strings.HasSuffix(sub, ":access") {
			res, _, _ := strings.Cut(strings.TrimSuffix(sub, ":access"), "/versions/")
			vers, ok := smSecrets[res]
			if !ok {
				http.NotFound(w, r)
				return
			}
			for i := len(vers) - 1; i >= 0; i-- {
				if vers[i].state == "ENABLED" {
					_ = json.NewEncoder(w).Encode(map[string]any{
						"name":    fmt.Sprintf("%s/versions/%d", res, vers[i].num),
						"payload": map[string]string{"data": base64.StdEncoding.EncodeToString(vers[i].data)},
					})
					return
				}
			}
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(sub, ":destroy") {
			res, vStr, _ := strings.Cut(strings.TrimSuffix(sub, ":destroy"), "/versions/")
			vers, ok := smSecrets[res]
			n, _ := strconv.Atoi(vStr)
			if !ok || n < 1 || n > len(vers) {
				http.NotFound(w, r)
				return
			}
			if vers[n-1].state == "DESTROYED" {
				http.Error(w, "already DESTROYED", http.StatusBadRequest)
				return
			}
			vers[n-1].state = "DESTROYED"
			vers[n-1].data = nil
			_ = json.NewEncoder(w).Encode(map[string]any{"name": fmt.Sprintf("%s/versions/%d", res, n), "state": "DESTROYED"})
			return
		}
		if r.Method == http.MethodDelete {
			delete(smSecrets, sub)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	newStore := func() *secrets.Store {
		s := secrets.OpenSecretManager(secrets.SecretManagerConfig{
			Prefix:     prefix,
			Catalogue:  pgStore,
			Endpoint:   srv.URL,
			HTTPClient: srv.Client(),
			Token:      func(context.Context) (string, error) { return "pg-test-token", nil },
		})
		s.SetAuditLogger(pgStore)
		return s
	}

	// Instance 1 writes and updates credentials in Secret Manager + PostgreSQL catalogue.
	inst1 := newStore()
	if err := inst1.Set(secrets.GeminiAPIKeySecret, secrets.AgentCredentialKey, []byte("old-gemini-secret-material")); err != nil {
		t.Fatalf("Set v1: %v", err)
	}
	if err := inst1.Set(secrets.GeminiAPIKeySecret, secrets.AgentCredentialKey, []byte("new-gemini-secret-material")); err != nil {
		t.Fatalf("Set v2: %v", err)
	}
	if err := inst1.Set("github-app", "app-id", []byte("4242")); err != nil {
		t.Fatalf("Set github-app/app-id: %v", err)
	}
	if err := inst1.Set("github-app", "private-key", []byte("pem-secret-bytes")); err != nil {
		t.Fatalf("Set github-app/private-key: %v", err)
	}

	// Verify PostgreSQL secret_entries contains ONLY metadata and NEVER any secret value.
	rows, err := db.QueryContext(ctx, `SELECT secret_name, key, sm_secret, version FROM secret_entries ORDER BY secret_name, key`)
	if err != nil {
		t.Fatalf("querying secret_entries: %v", err)
	}
	defer rows.Close()
	var rawDump strings.Builder
	rowCount := 0
	for rows.Next() {
		var sName, k, smSec, verStr string
		if err := rows.Scan(&sName, &k, &smSec, &verStr); err != nil {
			t.Fatal(err)
		}
		rowCount++
		fmt.Fprintf(&rawDump, "%s|%s|%s|%s\n", sName, k, smSec, verStr)
	}
	if rowCount != 3 {
		t.Fatalf("expected 3 rows in secret_entries, got %d (%s)", rowCount, rawDump.String())
	}
	for _, secretVal := range []string{"old-gemini-secret-material", "new-gemini-secret-material", "4242", "pem-secret-bytes"} {
		if strings.Contains(rawDump.String(), secretVal) {
			t.Fatalf("secret value %q leaked into PostgreSQL secret_entries: %s", secretVal, rawDump.String())
		}
	}

	// Instance 2 (after VM replacement) opens against the same PostgreSQL DB + Secret Manager.
	inst2 := newStore()
	list, err := inst2.List()
	if err != nil {
		t.Fatalf("inst2.List: %v", err)
	}
	wantList := []secrets.SecretInfo{
		{Name: secrets.GeminiAPIKeySecret, Keys: []string{"value"}},
		{Name: "github-app", Keys: []string{"app-id", "private-key"}},
	}
	if !reflect.DeepEqual(list, wantList) {
		t.Fatalf("inst2.List = %+v, want %+v", list, wantList)
	}

	runCtx := secrets.WithPrincipal(secrets.WithRun(secrets.WithTask(ctx, "task-9"), "run-9"), "owner@example.com")
	val, err := inst2.Resolve(runCtx, secrets.GeminiAPIKeySecret)
	if err != nil || val != "new-gemini-secret-material" {
		t.Fatalf("inst2.Resolve(gemini-api-key) = %q, %v", val, err)
	}

	// Audit trail recorded in PostgreSQL secret_resolution_audit.
	audits, err := pgStore.ListSecretAudit(ctx, 10)
	if err != nil || len(audits) != 1 || audits[0].SecretName != secrets.GeminiAPIKeySecret || audits[0].TaskID != "task-9" {
		t.Fatalf("unexpected Postgres secret audit: %+v, %v", audits, err)
	}

	// DeleteSecret cleans up both Secret Manager and PostgreSQL secret_entries.
	if err := inst2.DeleteSecret("github-app"); err != nil {
		t.Fatalf("DeleteSecret(github-app): %v", err)
	}
	if _, err := inst2.Resolve(ctx, "github-app/app-id"); !errors.Is(err, secrets.ErrNoSecret) {
		t.Fatalf("after DeleteSecret, Resolve = %v, want ErrNoSecret", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if listCalls != 0 {
		t.Fatalf("expected 0 secrets.list / versions.list calls, got %d", listCalls)
	}
	vers := smSecrets[prefix+secrets.GeminiAPIKeySecret]
	if len(vers) != 2 || vers[0].state != "DESTROYED" || vers[1].state != "ENABLED" {
		t.Fatalf("unexpected SM version states: %+v", vers)
	}
}
