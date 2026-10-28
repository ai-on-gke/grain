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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/modelproxy"
)

// A daemon killed outright never revokes the tokens its live runs held,
// and the next one reads them back from the registry's file. Once
// startup recovery has finished those runs as interrupted, their tokens
// must stop working -- not go on forwarding with the team's credential
// until the run's deadline. A live run's token, and one owned by
// something that is not a run, are left alone.
func TestRevokeFinishedRunTokensDropsWhatRecoveredRunsHeld(t *testing.T) {
	store, ctx := openStuckTestStore(t)
	at := time.Now()
	stuckTask(t, ctx, store, "1")
	stuckRun(t, ctx, store, "1", "1-1", at)
	stuckTask(t, ctx, store, "2")
	stuckRun(t, ctx, store, "2", "2-1", at)

	path := filepath.Join(t.TempDir(), "secrets", "modelproxy-claude-tokens.json")
	connection := modelproxy.Connection{ID: "default-claude"}
	before := modelproxy.NewEndpoint()
	if err := before.Tokens.PersistTo(path); err != nil {
		t.Fatalf("PersistTo: %v", err)
	}
	_, recovered, err := before.Tokens.Mint("1-1", connection)
	if err != nil {
		t.Fatal(err)
	}
	_, live, err := before.Tokens.Mint("2-1", connection)
	if err != nil {
		t.Fatal(err)
	}
	_, custom, err := before.Tokens.Mint("3", connection)
	if err != nil {
		t.Fatal(err)
	}

	// What RecoverOrphanedRuns leaves for run 1-1 after a kill -9.
	if err := store.FinishRun(ctx, "1-1", at.Add(time.Minute), "interrupted", "recovered at startup"); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	after := modelproxy.NewEndpoint()
	if err := after.Tokens.PersistTo(path); err != nil {
		t.Fatalf("PersistTo after the restart: %v", err)
	}
	revokeFinishedRunTokens(ctx, store, map[string]*modelproxy.Endpoint{modelproxy.Claude.Name: after})

	if _, ok := after.Tokens.Authenticate(recovered); ok {
		t.Error("the recovered run's token still authenticates, want it revoked")
	}
	if owner, ok := after.Tokens.Authenticate(live); !ok || owner != "2-1" {
		t.Errorf("the live run's token = (%q, %v), want (2-1, true)", owner, ok)
	}
	if owner, ok := after.Tokens.Authenticate(custom); !ok || owner != "3" {
		t.Errorf("a token owned by no run = (%q, %v), want (3, true)", owner, ok)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"1-1"`) {
		t.Errorf("token file still names the recovered run's token:\n%s", data)
	}
}
