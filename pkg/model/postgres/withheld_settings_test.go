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
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// A merged settings change applied to a daemon on Postgres keeps the
// running Google OAuth client secret, which the state repository never
// holds (staterepo's withheldColumns). Keeping it matches the refilled
// row by its key with a NULL-safe equality, and SQLite's `"id" IS ?` is a
// syntax error here -- which failed every settings import and left the
// daemon restarting.
func TestApplyingSettingsKeepsTheGoogleOAuthClientSecret(t *testing.T) {
	postgrestest.SkipUnlessAvailable(t)
	ctx := context.Background()
	const oauthSecret = "GOCSPX-do-not-commit-this-0123456789"
	store, db := postgrestest.Open(t)
	if err := store.PutConfig(ctx, model.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	setOAuthColumns(t, ctx, store, "client-id.apps.googleusercontent.com", oauthSecret)
	dir := t.TempDir()
	if err := staterepo.ExportConfig(ctx, db, dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, staterepo.TablesDir, "grain_config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	rows[0]["google_oauth_client_id"] = "other-client.apps.googleusercontent.com"
	out, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := staterepo.ImportTables(ctx, db, dir, staterepo.SettingsTables); err != nil {
		t.Fatal(err)
	}
	if got := configColumn(t, ctx, store, "google_oauth_client_id"); got != "other-client.apps.googleusercontent.com" {
		t.Errorf("client id = %q, want the merged one", got)
	}
	if got := configColumn(t, ctx, store, "google_oauth_client_secret"); got != oauthSecret {
		t.Errorf("secret after applying = %q, want the running %q", got, oauthSecret)
	}
}
