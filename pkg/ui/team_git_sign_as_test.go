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
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// userTransport answers GET /user with user and everything else with an
// empty list, safely across the goroutines an identity lookup runs on.
type userTransport struct {
	mu   sync.Mutex
	user string
}

func (u *userTransport) Request(method, path string, headers map[string]string, body []byte) (github.ApiResponse, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if method == http.MethodGet && path == "/user" {
		return github.ApiResponse{Status: 200, Body: []byte(u.user)}, nil
	}
	return github.ApiResponse{Status: 200, Body: []byte("[]")}, nil
}

// `grain git-login` (SignAs with no Author) signs new pull requests and
// comments as the person who owns the token -- the name its own GET /user
// answers, else its login -- and not as this host's `git config
// user.name`, even on a host where that is readable (ambient logins
// available). The walkthrough of 2026-11-07 (F5) saw a token whose /user
// was octo-dev signed "Signed by <host user> (via Grain)". The commit
// identity may still prefer the host's git config; only the signature is
// the owner's.
func TestGitLoginSignsAsTheTokensOwnerNotTheHostGitUser(t *testing.T) {
	for _, tc := range []struct {
		name, user, want string
	}{
		{"display name", `{"login":"octo-dev","id":5150,"name":"Octo Dev"}`, "Octo Dev"},
		{"login when no name", `{"login":"octo-dev","id":5150}`, "octo-dev"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, store, _ := testClient(t)
			credentials, err := gitproxy.LoadCredentialSet(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			credentials.AppTransport = &userTransport{user: tc.user}
			client.Config.Credentials = credentials
			srv := ui.NewServerWithClient(client)
			ctx := context.Background()

			// A host with ambient logins available, whose own account's git
			// config names somebody else.
			t.Setenv("GRAIN_IN_CONTAINER", "0")
			bin := t.TempDir()
			git := "#!/bin/sh\n" +
				"if [ \"$1 $2 $3 $4\" = \"config --global --get user.name\" ]; then echo 'Host Person'; exit 0; fi\n" +
				"if [ \"$1 $2 $3 $4\" = \"config --global --get user.email\" ]; then echo 'host@example.com'; exit 0; fi\n" +
				"exit 1\n"
			if err := os.WriteFile(filepath.Join(bin, "git"), []byte(git), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			if got := credentials.HostGitIdentity(); got.Name != "Host Person" {
				t.Fatalf("HostGitIdentity = %+v, want the fake host's Host Person", got)
			}

			if err := store.PutConfig(ctx, model.Config{PollInterval: model.DefaultPollInterval}); err != nil {
				t.Fatal(err)
			}
			root := map[string]string{"Content-Type": "application/json"}
			rec := doWithHeaders(t, srv, http.MethodPost, "/api/deployment/git-connections",
				`{"type":"pat","host":"github.com","kind":"github","value":"ghp_octo","signAs":true,"replace":true}`, root)
			if rec.Code != http.StatusOK {
				t.Fatalf("git-login connection: %d %s", rec.Code, rec.Body)
			}
			var team ui.Team
			if err := json.Unmarshal(rec.Body.Bytes(), &team); err != nil {
				t.Fatal(err)
			}
			if len(team.GitConnections) != 1 {
				t.Fatalf("team after git-login: %+v", team)
			}
			name := team.GitConnections[0].Credential
			if got := credentials.HostOf(name).Author; got != tc.want {
				t.Fatalf("connection signs as %q, want the token owner's %q", got, tc.want)
			}
			if got := credentials.AuthorOf(name); got != tc.want {
				t.Fatalf("AuthorOf = %q, want %q", got, tc.want)
			}
		})
	}
}
