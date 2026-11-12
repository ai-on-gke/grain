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

package hostsrig_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/authfix"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/gitproxy/gitproxytest"
	"github.com/ai-on-gke/grain/tests/e2e/hostsrig"
)

func TestDefaultFakeLifecycle(t *testing.T) {
	hostsrig.Lifecycle(t, hostsrig.Config{})
}

func TestDefaultFakeLapsedLogin(t *testing.T) {
	hostsrig.LapsedLogin(t, hostsrig.Config{})
}

func TestDefaultFakeConnectionRemovedMidTask(t *testing.T) {
	hostsrig.ConnectionRemovedMidTask(t, hostsrig.Config{})
}

// TestCallerSuppliedCommandHost exercises the edition path: Config names an
// external command-backed host and an already-existing scratch repository, so
// the rig has no fake upstream of its own, does not call NewRepo on the
// command-backed host, reads branch tips via `git ls-remote` through the
// proxy, and checks credential isolation from the proxy's audit log.
func TestCallerSuppliedCommandHost(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(self, filepath.Join(bin, gitproxytest.Command)); err != nil {
		t.Fatal(err)
	}
	// A wrapper tool for an unauthenticated host that exits non-zero on
	// `auth token`, matching a real CLI on a host the account never logged into.
	unloggedTool := filepath.Join(bin, "extforgecli")
	script := "#!/bin/sh\nif [ \"$1\" = \"auth\" ] && [ \"$2\" = \"token\" ]; then\n" +
		"  if [ \"$GH_HOST\" = \"unlogged.ext.forge.test\" ]; then\n" +
		"    echo \"not logged into $GH_HOST\" >&2\n" +
		"    exit 1\n" +
		"  fi\n" +
		"fi\n" +
		"exec " + filepath.Join(bin, gitproxytest.Command) + " \"$@\"\n"
	if err := os.WriteFile(unloggedTool, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	host := "real.ext.forge.test"
	extUpstream := hostsrig.NewHostUpstream(t, "external forge", host, hostsrig.FakeForgeCLITokenFor(host))
	extUpstream.Owner = "scratch-org"
	extUpstream.Repo = "scratch-repo"
	extUpstream.NewRepo()
	t.Setenv(hostsrig.FakeForgeCLIUpstreamEnv, hostsrig.NewTestServer(t, extUpstream))

	src := gitproxy.AmbientCommandSource{
		Name:            "extforgecli",
		Command:         "extforgecli",
		Args:            []string{"auth", "token"},
		Env:             []string{"GH_HOST={host}"},
		LoginFix:        "GH_HOST={host} extforgecli auth login",
		Hosts:           []string{"*.ext.forge.test"},
		OnlyHosts:       true,
		ForgeViaCommand: gitproxytest.AmbientSource().ForgeViaCommand,
	}
	cfg := hostsrig.Config{
		Source:         src,
		Host:           host,
		Owner:          "scratch-org",
		Repo:           "scratch-repo",
		LapsedHost:     "unlogged.ext.forge.test",
		AuthFixSummary: gitproxytest.Summary,
	}

	t.Run("Lifecycle", func(t *testing.T) {
		hostsrig.Lifecycle(t, cfg)
	})
	t.Run("LapsedLoginNotLoggedIn", func(t *testing.T) {
		hostsrig.LapsedLogin(t, cfg)
	})
	t.Run("LapsedLoginExecAuthFix", func(t *testing.T) {
		unregisterExec := authfix.RegisterExec("zz-extforge-test", func(_ error, stderr string) (authfix.Fix, bool) {
			if strings.Contains(stderr, "not logged into unlogged.ext.forge.test") {
				return authfix.Fix{
					Summary: "workstation login expired",
					Action:  "run `ext-renew`",
					Reason:  "not logged in",
				}, true
			}
			return authfix.Fix{}, false
		})
		t.Cleanup(unregisterExec)
		noSummaryCfg := cfg
		noSummaryCfg.AuthFixSummary = ""
		hostsrig.LapsedLogin(t, noSummaryCfg)
	})
	t.Run("LapsedLoginRedirect", func(t *testing.T) {
		extUpstream.Fail(hostsrig.HostLoginPage)
		defer extUpstream.Fail(hostsrig.HostServes)
		redirectCfg := cfg
		redirectCfg.LapsedHost = ""
		hostsrig.LapsedLogin(t, redirectCfg)
	})
	t.Run("LifecycleByRegisteredName", func(t *testing.T) {
		unregister := gitproxy.RegisterAmbientSource(src)
		t.Cleanup(unregister)
		hostsrig.Lifecycle(t, hostsrig.Config{
			SourceName: "extforgecli",
			Host:       host,
			Owner:      "scratch-org",
			Repo:       "scratch-repo",
		})
	})
	t.Run("ConnectionRemovedMidTask", func(t *testing.T) {
		hostsrig.ConnectionRemovedMidTask(t, cfg)
	})
}
