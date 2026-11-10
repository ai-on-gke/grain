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

package ui_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestRedactSecretsCutsStoredValuesWithoutReturningThem(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	store := secrets.New(t.TempDir())
	if err := store.Set("db", "password", []byte("s3cr3t-value-xyz")); err != nil {
		t.Fatal(err)
	}
	client.Config.Secrets = store

	rec := do(t, srv, http.MethodPost, "/api/secrets/redact", `{"lines":["login with s3cr3t-value-xyz ok","nothing here"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "s3cr3t-value-xyz") {
		t.Fatalf("response carries the value: %s", rec.Body)
	}
	got := decode[ui.SecretsRedactResponse](t, rec)
	if got.Redacted != 1 || len(got.Lines) != 2 || got.Lines[0] != "login with [redacted: secret db/password] ok" || got.Lines[1] != "nothing here" {
		t.Fatalf("response = %+v", got)
	}
}

func TestRedactSecretsWithNoStoreIsNotFound(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	if rec := do(t, srv, http.MethodPost, "/api/secrets/redact", `{"lines":["x"]}`); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body)
	}
}
