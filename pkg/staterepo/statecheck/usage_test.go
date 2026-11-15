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

package statecheck

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func putRawUsageSettings(t *testing.T, ctx context.Context, db *sql.DB, raw string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, "DELETE FROM `usage_setting`"); err != nil {
		t.Fatalf("clearing usage settings: %v", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO `usage_setting` (`id`,`value`,`updated_at`) VALUES (1,?,?)",
		raw, time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("writing usage settings: %v", err)
	}
}

func TestUsageSettingsFailsWhatTheAPIWouldRefuse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, cleanup, err := ScratchStore()
	if err != nil {
		t.Fatalf("opening a store: %v", err)
	}
	defer cleanup()

	// No row, and an empty value, are a deployment that never chose.
	if w, err := UsageSettings(ctx, db); err != nil || len(w) != 0 {
		t.Fatalf("no row: warnings %v, err %v; want neither", w, err)
	}
	putRawUsageSettings(t, ctx, db, "")
	if w, err := UsageSettings(ctx, db); err != nil || len(w) != 0 {
		t.Fatalf("empty value: warnings %v, err %v; want neither", w, err)
	}

	for _, tc := range []struct{ raw, want string }{
		{`{"mode":"auto"`, "not usage settings grain can read"},
		{`{"mode":"sometimes"}`, `usage mode "sometimes"`},
		{`{"minWorkers":-1}`, "minWorkers -1"},
		{`{"budgets":[{"connection":"agy-1","tokens":0,"window":"5h"}]}`, "tokens must be positive"},
		{`{"budgets":[{"connection":"agy-1","tokens":10,"window":"soon"}]}`, "not a positive duration"},
	} {
		putRawUsageSettings(t, ctx, db, tc.raw)
		_, err := UsageSettings(ctx, db)
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), UsageSettingsFile) {
			t.Errorf("%s: err = %v, want one naming %s and saying %q", tc.raw, err, UsageSettingsFile, tc.want)
		}
	}
}

func TestUsageSettingsWarnsAboutABudgetOnAnUnknownConnection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, cleanup, err := ScratchStore()
	if err != nil {
		t.Fatalf("opening a store: %v", err)
	}
	defer cleanup()
	store := model.New(db)
	cfg := model.DefaultConfig()
	cfg.AgentBackends = []model.AgentBackend{{ID: "agy-1", Framework: "antigravity"}}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("putting a settings row: %v", err)
	}
	if err := store.PutUsageSettings(ctx, model.UsageSettings{
		Mode: model.UsageModeAuto,
		Budgets: []model.TokenBudget{
			{Connection: "agy-1", Tokens: 1000, Window: "5h"},
			{Connection: "claude-gone", Tokens: 1000, Window: "5h"},
		},
	}, time.Now()); err != nil {
		t.Fatalf("putting usage settings: %v", err)
	}
	warnings, err := UsageSettings(ctx, db)
	if err != nil {
		t.Fatalf("a budget on a connection the dump lacks failed the check: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], `"claude-gone"`) || !strings.Contains(warnings[0], "budget 2") {
		t.Fatalf("warnings = %v, want one naming budget 2 on claude-gone", warnings)
	}

	putRawUsageSettings(t, ctx, db, `{"mode":"auto","budget":[]}`)
	warnings, err = UsageSettings(ctx, db)
	if err != nil || len(warnings) != 1 || !strings.Contains(warnings[0], `"budget"`) {
		t.Fatalf("an unknown key: warnings %v, err %v; want one warning naming it", warnings, err)
	}
}
