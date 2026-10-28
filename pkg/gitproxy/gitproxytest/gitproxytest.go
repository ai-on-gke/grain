// Package gitproxytest is a made-up command-backed ambient source for
// tests outside pkg/gitproxy: what a registered forge tool looks like to
// core grain (gitproxy.RegisterAmbientSource), with no real one behind it.
//
// The tool, forgecli, speaks gh's protocol (`forgecli auth token`,
// `forgecli api --include`) for hosts under code.forge.test, the only way
// to them. Their git endpoints take a token as Basic x-access-token, and
// a lapsed login sends a request to LoginHost, which Install registers
// with authfix so that its fix reads as Summary and Action.
package gitproxytest

import (
	"encoding/base64"
	"net/url"
	"strings"
	"sync"

	"github.com/bwsalmon/grain/pkg/authfix"
	"github.com/bwsalmon/grain/pkg/gitproxy"
)

const (
	// Source and Command are the source's name and its tool.
	Source  = "forgecli"
	Command = "forgecli"
	// Domain is the domain the source claims; Host is a host on it.
	Domain = "code.forge.test"
	Host   = "depot." + Domain
	// LoginHost is where a lapsed login is sent.
	LoginHost = "login.forge.test"
	// Summary and Action are LoginHost's fix (authfix.Fix).
	Summary = "forge login needed"
	Action  = "run `forgecli-login` as the daemon's account, then retry"
	// Label and PresetLabel are the source's entry in the connection
	// editor's Source menu and Host's in its Preset menu.
	Label       = "GH_HOST=<host> forgecli auth token (Example forge)"
	PresetLabel = "Example forge: depot (ambient via forgecli)"
)

// GitBasicAuth is the Authorization header the source's git endpoints
// take for token.
func GitBasicAuth(token string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))
}

// AmbientSource is the made-up source.
func AmbientSource() gitproxy.AmbientCommandSource {
	return gitproxy.AmbientCommandSource{
		Name:      Source,
		Command:   Command,
		Args:      []string{"auth", "token"},
		Env:       []string{"GH_HOST={host}", "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1"},
		LoginFix:  "GH_HOST={host} forgecli auth login",
		ScopeFix:  "GH_HOST={host} forgecli auth refresh --scopes repo",
		Hosts:     []string{"*." + Domain},
		OnlyHosts: true,
		ForgeViaCommand: &gitproxy.CommandForge{
			GitAuthorization: GitBasicAuth,
			LoginPage: func(text string) (string, bool) {
				if !strings.Contains(text, LoginHost) {
					return "", false
				}
				return "https://" + LoginHost + "/", true
			},
			GitRefusedHint: func(host, owner, repo string) string {
				return host + "'s git endpoints take the token only as Basic auth with the username x-access-token"
			},
		},
		UI: &gitproxy.AmbientSourceUI{
			Label:   Label,
			Help:    "The token comes from `GH_HOST={host} forgecli auth token`; if the forge login lapses, run `forgecli-login`.",
			Presets: []gitproxy.AmbientSourcePreset{{Host: Host, Label: PresetLabel}},
		},
	}
}

var once sync.Once

// Install registers the source and LoginHost's redirect classifier for
// the rest of the test binary's life: call it from TestMain or init in a
// package whose tests use it.
func Install() {
	once.Do(func() {
		gitproxy.RegisterAmbientSource(AmbientSource())
		authfix.RegisterRedirect("zz-gitproxytest", func(loc *url.URL) (authfix.Fix, bool) {
			return authfix.Fix{Summary: Summary, Action: Action, Reason: "redirected to " + LoginHost},
				strings.EqualFold(loc.Hostname(), LoginHost)
		})
	})
}
