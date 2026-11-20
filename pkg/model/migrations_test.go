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

package model

import (
	"strings"
	"testing"
)

func TestAlterColumnReMatchesEveryMigrationColumnDDL(t *testing.T) {
	t.Parallel()
	matched := 0
	for _, m := range Migrations {
		for _, stmt := range m.Statements {
			upper := strings.ToUpper(stmt)
			if !strings.Contains(upper, " ADD COLUMN ") && !strings.Contains(upper, " DROP COLUMN ") {
				continue
			}
			parts := alterColumnRe.FindStringSubmatch(stmt)
			if len(parts) != 4 {
				t.Errorf("migration %d: statement not matched by alterColumnRe (would fall back to savepoint rollback): %s", m.Version, stmt)
				continue
			}
			matched++
		}
	}
	if matched == 0 {
		t.Fatal("expected at least one ADD/DROP COLUMN migration statement")
	}
}

func TestAlterColumnReParsing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		stmt      string
		wantMatch bool
		table     string
		op        string
		col       string
	}{
		{
			stmt:      "ALTER TABLE `grain_config` ADD COLUMN `k8s_namespace` TEXT NOT NULL DEFAULT 'grain'",
			wantMatch: true, table: "grain_config", op: "ADD", col: "k8s_namespace",
		},
		{
			stmt:      "ALTER TABLE `grain_config` DROP COLUMN `sandbox_setup_script`",
			wantMatch: true, table: "grain_config", op: "DROP", col: "sandbox_setup_script",
		},
		{
			stmt:      "ALTER TABLE `task` RENAME TO `task_old`",
			wantMatch: false,
		},
	}
	for _, tc := range cases {
		parts := alterColumnRe.FindStringSubmatch(tc.stmt)
		if got := len(parts) == 4; got != tc.wantMatch {
			t.Errorf("%q: matched = %v, want %v", tc.stmt, got, tc.wantMatch)
			continue
		}
		if !tc.wantMatch {
			continue
		}
		if parts[1] != tc.table || strings.ToUpper(parts[2]) != tc.op || parts[3] != tc.col {
			t.Errorf("%q: got (%q, %q, %q), want (%q, %q, %q)", tc.stmt, parts[1], parts[2], parts[3], tc.table, tc.op, tc.col)
		}
	}
}
