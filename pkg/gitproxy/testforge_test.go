package gitproxy

// A made-up command-backed source for this package's tests, registered
// for the whole test binary: forgecli, speaking gh's protocol for hosts
// under code.forge.test, whose git endpoints take Basic x-access-token
// and whose front end sends a lapsed login to testLoginHost. It is
// gitproxytest's source, which this package's own tests cannot import.

import (
	"encoding/base64"
	"net/url"
	"strings"

	"github.com/ai-on-gke/grain/pkg/authfix"
)

const (
	testForgeSource = "forgecli"
	testLoginHost   = "login.forge.test"
)

func testGitBasicAuth(token string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))
}

func init() {
	RegisterAmbientSource(AmbientCommandSource{
		Name:      testForgeSource,
		Command:   "forgecli",
		Args:      []string{"auth", "token"},
		Env:       []string{"GH_HOST={host}", "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1"},
		LoginFix:  "GH_HOST={host} forgecli auth login",
		ScopeFix:  "GH_HOST={host} forgecli auth refresh --scopes repo",
		Hosts:     []string{"*.code.forge.test"},
		OnlyHosts: true,
		ForgeViaCommand: &CommandForge{
			GitAuthorization: testGitBasicAuth,
			LoginPage: func(text string) (string, bool) {
				return "https://" + testLoginHost + "/", strings.Contains(text, testLoginHost)
			},
			GitRefusedHint: func(host, owner, repo string) string {
				return host + "'s git endpoints take the token only as Basic auth with the username x-access-token"
			},
		},
	})
	authfix.RegisterRedirect("zz-gitproxy-test", func(loc *url.URL) (authfix.Fix, bool) {
		return authfix.Fix{Summary: "forge login needed", Action: "run `forgecli-login` as the daemon's account, then retry",
			Reason: "redirected to " + testLoginHost}, strings.EqualFold(loc.Hostname(), testLoginHost)
	})
}
