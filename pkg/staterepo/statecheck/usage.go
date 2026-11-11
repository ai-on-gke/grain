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
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// UsageSettingsFile is where the state repository carries the
// operator's token budgets and usage mode (model.UsageSettings).
const UsageSettingsFile = "tables/usage_setting.json"

// UsageSettings checks the usage settings the dump carries: the same
// model.UsageSettings.Validate that `grain usage` and PUT
// /api/usage/settings refuse a change with, plus a value that does not
// parse at all. Either fails the check. model.Store.UsageSettings reads
// a value it cannot parse as the zero settings, so a typo merged here
// would otherwise put the deployment back on advisory with no budgets
// on the next tick, with nothing to say so.
//
// A budget on a connection the dump's settings row does not have is a
// warning, not the refusal ui's checkBudgetConnections gives it. The
// row carries only this deployment's connections, so the check cannot
// tell a typo from a budget kept for a connection that has since been
// removed (which the API keeps acceptable) or one being restored onto a
// deployment that is about to add it; what a reviewer needs is to know
// that such a budget counts nothing until the connection exists. An
// unknown key in the value is a warning too, since the daemon ignores it.
func UsageSettings(ctx context.Context, db *sql.DB) ([]string, error) {
	var raw string
	err := db.QueryRowContext(ctx, "SELECT `value` FROM `usage_setting` WHERE `id` = 1").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the usage settings this dump carries: %w", err)
	}
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var settings model.UsageSettings
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return nil, fmt.Errorf("%s: the usage settings value is not usage settings grain can read "+
			"(a daemon would run on advisory with no budgets): %w", UsageSettingsFile, err)
	}
	if err := settings.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", UsageSettingsFile, err)
	}
	var out []string
	strict := json.NewDecoder(bytes.NewReader([]byte(raw)))
	strict.DisallowUnknownFields()
	if err := strict.Decode(&model.UsageSettings{}); err != nil {
		out = append(out, fmt.Sprintf("%s: %v, which grain ignores", UsageSettingsFile, err))
	}
	if len(settings.Budgets) == 0 {
		return out, nil
	}
	cfg, err := model.New(db).GetConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the settings this dump carries: %w", err)
	}
	known := map[string]bool{}
	if cfg != nil {
		for _, b := range cfg.AgentBackends {
			known[b.ID] = true
		}
	}
	for i, b := range settings.Budgets {
		if conn := strings.TrimSpace(b.Connection); !known[conn] {
			out = append(out, fmt.Sprintf("%s: budget %d is on connection %q, which this dump's settings "+
				"row does not have; it counts nothing until a connection with that id exists",
				UsageSettingsFile, i+1, conn))
		}
	}
	return out, nil
}
