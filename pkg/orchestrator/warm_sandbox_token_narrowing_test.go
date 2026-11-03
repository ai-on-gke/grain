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

package orchestrator_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// A keep-running task's warm sandbox keeps its git proxy token between
// turns. Once a person narrows the task's reads or writes, that token must
// stop authenticating at all -- not merely lose the repository -- because
// the sandbox it was minted for was built with the wider access. This is
// TestKeepRunningWarmSandboxReclaimedOnToggleOffOrWithdrawnRepoAccess with
// the daemon's real token store (gitproxy.SandboxTokenStore, as
// daemon/run.go wires MintSandboxToken and RevokeSandboxToken) in place of
// counters, read back the way the proxy reads it.
func TestWarmSessionSandboxTokenStopsAuthenticatingOnceRepoAccessNarrows(t *testing.T) {
	t.Parallel()
	for _, narrow := range []string{"writes", "reads"} {
		t.Run(narrow, func(t *testing.T) {
			t.Parallel()
			store, ctx := openStore(t)
			task := fileKeepRunningTask(t, ctx, store, "kr-narrow-"+narrow)
			if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
				tk.Reads = []model.RepoRef{{Owner: "acme", Name: "private-lib"}}
				tk.Writes = []model.WriteTarget{{Repo: model.RepoRef{Owner: "acme", Name: "gadgets"}}}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "sandbox-tokens.json")
			tokens := gitproxy.NewSandboxTokenStore(path)
			minted := map[string]string{}
			deps := orchestrator.Deps{
				Store:     store,
				Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
				Framework: orchestrator.StaticFramework(agentFunc(func(context.Context, agent.RunConfig) (*agent.Result, error) {
					return &agent.Result{FinalText: "step", ResumableSession: "sess-narrow"}, nil
				})),
				MintSandboxToken: func(sandbox string) (string, error) {
					tok, err := tokens.EnsureToken(sandbox)
					minted[sandbox] = tok
					return tok, err
				},
				RevokeSandboxToken: tokens.Revoke,
				MaxWorkers:         1,
				Config: orchestrator.Config{
					GitRemoteBase:         "http://proxy.example",
					SessionSandboxIdleTTL: 30 * time.Minute,
				},
			}
			authenticates := func(tok string) bool {
				t.Helper()
				loaded, err := gitproxy.LoadSandboxTokens(path)
				if err != nil {
					t.Fatal(err)
				}
				_, ok := loaded.Authenticate(tok)
				return ok
			}

			if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Second)); err != nil {
				t.Fatalf("cycle 1: %v", err)
			}
			warm := orchestrator.WarmSessionSandboxID(store, task.ID)
			old := minted[warm]
			if warm == "" || old == "" || !authenticates(old) {
				t.Fatalf("after cycle 1: warm=%q token=%q authenticates=%v, want a parked sandbox with a live token",
					warm, old, old != "" && authenticates(old))
			}

			if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
				if narrow == "writes" {
					tk.Writes = nil
				} else {
					tk.Reads = nil
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(2*time.Second)); err != nil {
				t.Fatalf("cycle 2: %v", err)
			}
			if authenticates(old) {
				t.Errorf("the warm sandbox's token still authenticates after narrowing %s", narrow)
			}
			if now := orchestrator.WarmSessionSandboxID(store, task.ID); now == warm {
				t.Errorf("warm sandbox %q survived narrowing %s", warm, narrow)
			}
		})
	}
}
