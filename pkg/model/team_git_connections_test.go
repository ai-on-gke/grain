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

import "testing"

// A deployment whose credentials all moved into GitConnections (adding or
// deleting a connection blanks the legacy Config.GitCredential) still has a
// credential for the default host; one holding only another host's does
// not (grain/task-3079).
func TestHasGitConnectionOnCountsConnectionsAndTheLegacyCredential(t *testing.T) {
	for _, tc := range []struct {
		name      string
		settings  Config
		host, def string
		want      bool
	}{
		{name: "nothing", settings: Config{}, want: false},
		{name: "legacy only", settings: Config{GitCredential: "tok"}, want: true},
		{name: "blank legacy", settings: Config{GitCredential: "  "}, want: false},
		{name: "connections only", settings: Config{GitConnections: []TeamGitConnection{{Credential: "a"}, {Credential: "b"}}}, want: true},
		{name: "explicit default host", settings: Config{GitConnections: []TeamGitConnection{{Credential: "a", Host: "github.com"}}}, want: true},
		{name: "owner scoped on the default host", settings: Config{GitConnections: []TeamGitConnection{{Credential: "a", Scope: "acme"}}}, want: true},
		{name: "another host only", settings: Config{GitConnections: []TeamGitConnection{{Credential: "a", Host: "gitlab.example"}}}, want: false},
		{name: "another host asked for", settings: Config{GitConnections: []TeamGitConnection{{Credential: "a", Host: "gitlab.example"}}}, host: "gitlab.example", want: true},
		{name: "deployment default is GHE", settings: Config{GitConnections: []TeamGitConnection{{Credential: "a", Host: "ghe.example"}}}, def: "ghe.example", want: true},
		{name: "github.com on a GHE deployment", settings: Config{GitConnections: []TeamGitConnection{{Credential: "a", Host: "github.com"}}}, def: "ghe.example", want: false},
		{name: "anonymous legacy", settings: Config{GitCredential: "anonymous"}, want: false},
		{name: "anonymous connection", settings: Config{GitConnections: []TeamGitConnection{{Credential: "anonymous"}}}, want: false},
		{name: "anonymous beside a token", settings: Config{GitConnections: []TeamGitConnection{{Credential: "anonymous", Scope: "acme"}, {Credential: "a"}}}, want: true},
		{name: "unusable connection", settings: Config{GitConnections: []TeamGitConnection{{Credential: " "}}}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.settings.HasGitConnectionOn(tc.host, tc.def); got != tc.want {
				t.Errorf("HasGitConnectionOn(%q, %q) = %v, want %v", tc.host, tc.def, got, tc.want)
			}
		})
	}
}

func TestMatchUserGitConnectionAndDefaultPrecedence(t *testing.T) {
	conns := []TeamGitConnection{
		{Credential: "default-host", Host: "github.com"},
		{Credential: "default-acme", Host: "github.com", Scope: "acme"},
		{Credential: "alice-host", Host: "github.com", User: "alice@example.com"},
		{Credential: "alice-widgets", Host: "github.com", Scope: "acme/widgets", User: "alice@example.com"},
		{Credential: "bob-host", Host: "github.com", User: "bob@example.com"},
	}

	// Default resolution ignores user-bound connections when a default (User == "") connection exists.
	if got, ok := MatchTeamGitConnection(conns, "github.com", "github.com", "acme", "widgets"); !ok || got.Credential != "default-acme" {
		t.Fatalf("MatchTeamGitConnection(acme/widgets) = %+v, %v; want default-acme", got, ok)
	}
	if got, ok := MatchTeamGitConnection(conns, "github.com", "github.com", "other", "repo"); !ok || got.Credential != "default-host" {
		t.Fatalf("MatchTeamGitConnection(other/repo) = %+v, %v; want default-host", got, ok)
	}

	// Per-user resolution picks the user's most specific connection on the host (matching email or local-part).
	if got, ok := MatchUserGitConnection(conns, "alice", "github.com", "github.com", "acme", "widgets"); !ok || got.Credential != "alice-widgets" {
		t.Fatalf("MatchUserGitConnection(alice, acme/widgets) = %+v, %v; want alice-widgets", got, ok)
	}
	if got, ok := MatchUserGitConnection(conns, "alice@example.com", "github.com", "github.com", "acme", "other"); !ok || got.Credential != "alice-host" {
		t.Fatalf("MatchUserGitConnection(alice@example.com, acme/other) = %+v, %v; want alice-host", got, ok)
	}
	if got, ok := MatchUserGitConnection(conns, "bob", "github.com", "github.com", "acme", "widgets"); !ok || got.Credential != "bob-host" {
		t.Fatalf("MatchUserGitConnection(bob, acme/widgets) = %+v, %v; want bob-host", got, ok)
	}
	if _, ok := MatchUserGitConnection(conns, "charlie", "github.com", "github.com", "acme", "widgets"); ok {
		t.Fatalf("MatchUserGitConnection(charlie) matched unexpectedly")
	}

	// MatchTeamGitConnection falls back to the most specific user-bound connection for deployment-level
	// read checks when no default (User == "") connection exists on the host.
	userOnly := []TeamGitConnection{
		{Credential: "alice-host", Host: "github.com", User: "alice@example.com"},
		{Credential: "bob-host", Host: "github.com", User: "bob@example.com"},
	}
	if got, ok := MatchTeamGitConnection(userOnly, "github.com", "github.com", "acme", "widgets"); !ok || got.Credential != "alice-host" {
		t.Fatalf("MatchTeamGitConnection(userOnly) = %+v, %v; want alice-host for deployment-level read checks", got, ok)
	}

	// EncodeTeamGitConnectionsWithOptions / DecodeTeamGitConnectionsWithOptions preserves RequireUserGitCredential.
	raw := EncodeTeamGitConnectionsWithOptions(userOnly, true)
	decoded, reqUser := DecodeTeamGitConnectionsWithOptions(raw)
	if !reqUser || len(decoded) != 2 {
		t.Fatalf("DecodeTeamGitConnectionsWithOptions = (%+v, %v); want 2 connections and reqUser=true", decoded, reqUser)
	}
}

