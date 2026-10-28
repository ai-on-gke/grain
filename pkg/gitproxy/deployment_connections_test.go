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
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

func openStoreForTest(t *testing.T) (*model.Store, context.Context) {
	t.Helper()
	logToTest(t)
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	return store, ctx
}

// ladderConnections is the deployment's git connections a test spells as
// a pattern map in its fixture directory (writeCredentialSet's
// credentials.json, read only here): "owner/repo", "owner/*" and "*"
// become a repository scope, an owner scope and the default host's
// default. Grain itself reads no such file (docs/single-tenant-reset.md,
// stage 2); it is only this package's shorthand for a test's
// connections, which the test hands the proxy as its TeamCredentials.
//
// proxy, when set, is read for its Credentials at each lookup instead of
// creds, for a test that swaps them after building the proxy.
type ladderConnections struct {
	creds *CredentialSet
	proxy *GitProxy
}

func (l ladderConnections) TeamGitCredential(ctx context.Context, team, owner, repo string) (string, error) {
	return l.TeamGitCredentialOn(ctx, team, "", owner, repo)
}

func (l ladderConnections) TeamGitCredentialOn(_ context.Context, _, host, owner, repo string) (string, error) {
	if l.proxy != nil {
		l.creds = l.proxy.Credentials
	}
	var conns []model.TeamGitConnection
	if l.creds != nil {
		for pattern, name := range testPatterns(l.creds.Dir()) {
			scope := pattern
			if scope == "*" {
				scope = ""
			}
			conns = append(conns, model.TeamGitConnection{Credential: name, Scope: scope})
		}
	}
	defaultHost := model.DefaultGitHost
	if l.creds != nil {
		defaultHost = l.creds.DefaultHost()
	}
	if c, ok := model.MatchTeamGitConnection(conns, host, defaultHost, owner, repo); ok {
		return c.Credential, nil
	}
	missing := &model.NoTeamGitCredentialError{Team: model.DefaultTeamID}
	if !model.SameGitHost(host, "", defaultHost) {
		missing.Host = host
	}
	return "", missing
}

// testPatterns is the pattern map a test fixture wrote to dir, empty
// when it wrote none.
func testPatterns(dir string) map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(filepath.Join(dir, "credentials.json"))
	if err != nil {
		return out
	}
	_ = json.Unmarshal(data, &out)
	return out
}

// useTestPatterns points set's Select at the pattern map its fixture
// directory spells, the way a deployment's git connections would.
func useTestPatterns(set *CredentialSet) *CredentialSet {
	set.SetConnections(func(owner, repo string) (string, bool) {
		patterns := testPatterns(set.Dir())
		for _, p := range []string{owner + "/" + repo, owner + "/*", "*"} {
			if name, ok := patterns[p]; ok && name != "" {
				return name, true
			}
		}
		return "", false
	})
	return set
}
