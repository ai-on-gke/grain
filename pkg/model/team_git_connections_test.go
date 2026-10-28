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
