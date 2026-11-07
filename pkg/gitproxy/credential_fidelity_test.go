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

package gitproxy

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

// TestGitCredentialMigrationFidelity proves that the git credential gitproxy
// selects and sends upstream is identical before and after schema migration 120
// rewrites github-credential:<name> onto repo#credential {"credential":"<name>"}:
// across target, read, and write repositories, as well as tasks falling back to
// their deployment/repo credential when no override grant is held.
func TestGitCredentialMigrationFidelity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}

	runWithGrant := func(t *testing.T, withOverride, migrate bool) (statuses map[string]int, resolvedNames map[string]string, resolvedTokens map[string]string) {
		t.Helper()
		db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		store := model.New(db)
		if err := store.Init(ctx); err != nil {
			t.Fatal(err)
		}

		if err := store.UpdateConfig(ctx, func(c *model.Config) error {
			c.GitCredential = "team-bot"
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.PutRepoConfig(ctx, model.RepoConfig{
			Repo:          model.RepoRef{Owner: "acme", Name: "docs"},
			GitCredential: "repo-bot",
		}); err != nil {
			t.Fatal(err)
		}

		target := model.RepoRef{Owner: "acme", Name: "widgets"}
		readRepo := model.RepoRef{Owner: "acme", Name: "docs"}
		writeRepo := model.RepoRef{Owner: "acme", Name: "deploy"}

		tk := model.Task{
			ID:     "t1",
			Intent: model.IntentImplement,
			Title:  "release workflow update",
			Origin: model.Origin{
				Attribution: model.Attribution{Actor: human},
				Reason:      model.ReasonDirect,
			},
			Approval:  &model.Attribution{Actor: human},
			Target:    &target,
			Reads:     []model.RepoRef{readRepo},
			Writes:    []model.WriteTarget{{Repo: writeRepo}},
			Binding:   model.BindingDirective,
			CreatedAt: &now,
		}
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatal(err)
		}

		if withOverride {
			if _, err := db.ExecContext(ctx,
				"INSERT INTO `task_grant` (`task_id`, `capability`, `via`) VALUES (?, ?, ?)",
				tk.ID, "github-credential:release-bot", string(model.GrantByLabel),
			); err != nil {
				t.Fatal(err)
			}
		}
		if migrate {
			if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 119 WHERE `id` = 1"); err != nil {
				t.Fatal(err)
			}
			if err := model.New(db).Init(ctx); err != nil {
				t.Fatalf("migrating 119 -> %d: %v", model.SchemaVersion, err)
			}
			if withOverride {
				gotTask, err := store.GetTask(ctx, tk.ID)
				if err != nil {
					t.Fatal(err)
				}
				wantGrant := model.RepoCredentialGrant("release-bot")
				if len(gotTask.Grants) != 1 || gotTask.Grants[0].Capability != wantGrant {
					t.Fatalf("migrated grants = %+v, want [%s]", gotTask.Grants, wantGrant)
				}
			}
		}

		if err := store.StartRun(ctx, model.Run{
			ID:        "run-1",
			TaskID:    tk.ID,
			Sandbox:   "sandbox-0",
			Attempt:   1,
			StartedAt: now,
		}, model.Limits{}); err != nil {
			t.Fatal(err)
		}

		upstream := newFakeGHE(t)
		proxy := buildProxyOn(t, upstream.host(), nil, map[string]string{
			"team-bot":    "secret-team",
			"repo-bot":    "secret-repo",
			"release-bot": "secret-release",
		})
		proxy.Authorizer = &ModelAuthorizer{
			Store:    store,
			Branches: stubBranchLookup{branch: "grain/task-1", live: true},
		}
		proxy.CredentialOverrides = store

		statuses = map[string]int{}
		resolvedNames = map[string]string{}
		resolvedTokens = map[string]string{}

		for _, r := range []model.RepoRef{target, readRepo, writeRepo} {
			key := r.String()
			name, ok, err := store.GitCredentialOverrideOn(ctx, "sandbox-0", r.Host, r.Owner, r.Name)
			if err != nil || !ok {
				t.Fatalf("GitCredentialOverrideOn(%s) = %q, %v, %v", key, name, ok, err)
			}
			cred, ok := proxy.Credentials.Get(name)
			if !ok || cred.Token == nil {
				t.Fatalf("Credentials.Get(%q) missing", name)
			}
			resolvedNames[key] = name
			resolvedTokens[key] = *cred.Token

			resp := gitAs(proxy, "GET", "/"+r.Owner+"/"+r.Name+".git/info/refs", "service=git-upload-pack", "", nil)
			statuses[key] = resp.Status
		}
		return statuses, resolvedNames, resolvedTokens
	}

	// 1. With github-credential:release-bot vs migrated repo#credential={"credential":"release-bot"}.
	beforeStatuses, beforeNames, beforeTokens := runWithGrant(t, true, false)
	afterStatuses, afterNames, afterTokens := runWithGrant(t, true, true)

	for _, repo := range []string{"acme/widgets", "acme/docs", "acme/deploy"} {
		if beforeStatuses[repo] != 200 || afterStatuses[repo] != 200 {
			t.Errorf("%s HTTP status: before=%d after=%d, want 200", repo, beforeStatuses[repo], afterStatuses[repo])
		}
		if beforeNames[repo] != "release-bot" || afterNames[repo] != beforeNames[repo] {
			t.Errorf("%s credential name: before=%q after=%q, want release-bot", repo, beforeNames[repo], afterNames[repo])
		}
		if beforeTokens[repo] != "secret-release" || afterTokens[repo] != beforeTokens[repo] {
			t.Errorf("%s credential token: before=%q after=%q, want secret-release", repo, beforeTokens[repo], afterTokens[repo])
		}
	}

	// 2. Without override grant: repo-specific and team default credentials match identically.
	noGrantBefore, noGrantBeforeNames, noGrantBeforeTokens := runWithGrant(t, false, false)
	noGrantAfter, noGrantAfterNames, noGrantAfterTokens := runWithGrant(t, false, true)
	wantFallback := map[string]string{
		"acme/widgets": "team-bot",
		"acme/docs":    "repo-bot",
		"acme/deploy":  "team-bot",
	}
	for repo, wantName := range wantFallback {
		if noGrantBefore[repo] != 200 || noGrantAfter[repo] != 200 {
			t.Errorf("fallback %s HTTP status: before=%d after=%d, want 200", repo, noGrantBefore[repo], noGrantAfter[repo])
		}
		if noGrantBeforeNames[repo] != wantName || noGrantAfterNames[repo] != wantName {
			t.Errorf("fallback %s name: before=%q after=%q, want %q", repo, noGrantBeforeNames[repo], noGrantAfterNames[repo], wantName)
		}
		if noGrantBeforeTokens[repo] != noGrantAfterTokens[repo] {
			t.Errorf("fallback %s token: before=%q after=%q", repo, noGrantBeforeTokens[repo], noGrantAfterTokens[repo])
		}
	}
}
