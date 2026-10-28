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

package model_test

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/secrets"
)

// TestSecretResolutionIsAudited: a task's resolution shows on the
// deployment's audit trail (task 3140, when it had to be charged to the
// default team to show at all).
func TestSecretResolutionIsAudited(t *testing.T) {
	store, ctx := open(t)
	if err := store.RecordSecretResolution(ctx, secrets.AuditEntry{TaskID: "task-1", RunID: "run-1", SecretName: "s", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListSecretAudit(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].TaskID != "task-1" {
		t.Fatalf("audit = %+v, want task-1's resolution", rows)
	}
}
