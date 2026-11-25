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
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

func writeCredentialSet(t *testing.T, patterns map[string]string, tokens map[string]string) string {
	t.Helper()
	// Nearly every test here starts with a credential set -- newTestProxy
	// and newAppProxy both do -- so this is where they claim the log,
	// first of all so that the release it registers runs last of all.
	// See log_test.go.
	logToTest(t)
	dir := t.TempDir()
	if patterns != nil {
		data, err := json.Marshal(patterns)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "credentials.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, token := range tokens {
		if err := os.WriteFile(filepath.Join(dir, name+".token"), []byte(token+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestSelectAsksTheConnectionsAndNeverCredentialsJSON: which credential
// covers a repository is the git connections' answer; a credentials.json
// left in the directory is not read.
func TestSelectAsksTheConnectionsAndNeverCredentialsJSON(t *testing.T) {
	dir := writeCredentialSet(t, map[string]string{"*": "ladder"},
		map[string]string{"ladder": "ladder-token", "conn": "conn-token"})
	cs, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cred, ok := cs.Select("acme", "widgets"); ok {
		t.Fatalf("Select with no connections = %+v, want nothing: credentials.json is not a ladder any more", cred)
	}
	var asked string
	cs.SetConnections(func(owner, repo string) (string, bool) {
		asked = owner + "/" + repo
		return "conn", true
	})
	cred, ok := cs.Select("ACME", "Widgets.git")
	if !ok || cred.Name != "conn" || cred.Token == nil || *cred.Token != "conn-token" {
		t.Fatalf("Select = %+v, %v; want the connection's credential", cred, ok)
	}
	if asked != "acme/widgets" {
		t.Errorf("connections asked about %q, want the canonical acme/widgets", asked)
	}
	cs.SetConnections(func(string, string) (string, bool) { return "missing", true })
	if cred, ok := cs.Select("acme", "widgets"); ok {
		t.Errorf("Select of a connection naming no stored credential = %+v, want nothing", cred)
	}
}

func TestGetReturnsANamedCredentialDirectly(t *testing.T) {
	dir := writeCredentialSet(t, nil, map[string]string{"workflow": "wf-token"})
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	cred, ok := set.Get("workflow")
	if !ok || cred.Name != "workflow" || cred.Token == nil || *cred.Token != "wf-token" {
		t.Fatalf("got %+v, %v", cred, ok)
	}
}

func TestGetFailsClosedForAnUnconfiguredName(t *testing.T) {
	set, err := LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := set.Get("nonexistent"); ok {
		t.Error("expected Get to fail closed for a missing token file")
	}
}

func TestNamesListsEveryConfiguredCredentialOnce(t *testing.T) {
	dir := writeCredentialSet(t, map[string]string{"*": "bot"},
		map[string]string{"bot": "bot-token", "release-bot": "release-token"})
	// A credential backed by both file shapes is still one credential --
	// load prefers the App one, so counting it twice would offer a
	// duplicate capability for a single token.
	if err := os.WriteFile(filepath.Join(dir, "release-bot.app.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := set.Names()
	want := []string{"bot", "release-bot"}
	if !slices.Equal(got, want) {
		t.Errorf("Names() = %v, want %v -- credentials.json itself is not a credential", got, want)
	}
}

func TestExtraNamesIsEveryStoredCredential(t *testing.T) {
	dir := writeCredentialSet(t, nil,
		map[string]string{"bot": "bot-token", "acme-bot": "acme-token", "release-bot": "release-token"})
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Which connection names a credential is a different question from
	// which tokens a task may ask for by name.
	got, want := set.ExtraNames(), []string{"acme-bot", "bot", "release-bot"}
	if !slices.Equal(got, want) {
		t.Errorf("ExtraNames() = %v, want %v", got, want)
	}
}

func TestExtraNamesIsEmptyWithNothingConfigured(t *testing.T) {
	set, err := LoadCredentialSet(filepath.Join(t.TempDir(), "never-created"))
	if err != nil {
		t.Fatal(err)
	}
	if got := set.ExtraNames(); len(got) != 0 {
		t.Errorf("ExtraNames() = %v, want none -- an unreadable secrets directory configures no credentials", got)
	}
}

func TestGetAnonymousNeedsNoTokenFile(t *testing.T) {
	set, err := LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cred, ok := set.Get("anonymous")
	if !ok || cred.Token != nil {
		t.Fatalf("got %+v, %v", cred, ok)
	}
}

// load's cache is the one piece of mutable state a CredentialSet carries,
// and Select/Get are called from GitProxy.Handle -- one goroutine per
// inbound HTTP request in a real deployment, so two sandboxes doing git
// operations at the same moment reach it concurrently. Run with -race:
// before mu guarded the cache, this reliably reported a concurrent map
// read/write.
func TestSelectAndGetAreSafeForConcurrentCallers(t *testing.T) {
	dir := writeCredentialSet(t,
		map[string]string{"acme/widgets": "narrow", "acme/*": "wide", "*": "global"},
		map[string]string{"narrow": "narrow-token", "wide": "wide-token", "global": "global-token", "named": "named-token"})
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}

	const goroutines = 64
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			switch i % 4 {
			case 0:
				if _, ok := useTestPatterns(set).Select("acme", "widgets"); !ok {
					t.Error("expected acme/widgets to resolve")
				}
			case 1:
				if _, ok := useTestPatterns(set).Select("acme", "other"); !ok {
					t.Error("expected acme/other to resolve")
				}
			case 2:
				if _, ok := useTestPatterns(set).Select("someone-else", "repo"); !ok {
					t.Error("expected someone-else/repo to resolve")
				}
			case 3:
				if _, ok := set.Get("named"); !ok {
					t.Error("expected named to resolve")
				}
			}
		}(i)
	}
	close(start)
	wg.Wait()
}

// An override name comes from a task grant or team config, not from the
// secrets directory, so one that climbs out of it must not be read.
func TestGetRefusesANameOutsideTheSecretsDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "github")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "outside.token"), []byte("stolen\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "global.token"), []byte("global-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logToTest(t)
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../outside", "sub/../../outside", `..\outside`, ".hidden", ""} {
		if cred, ok := set.Get(name); ok {
			t.Errorf("Get(%q) = %+v, want no such credential", name, cred)
		}
		if cred, ok := set.GetForRepo(name, "acme", "widgets"); ok {
			t.Errorf("GetForRepo(%q) = %+v, want no such credential", name, cred)
		}
		if set.IsApp(name) {
			t.Errorf("IsApp(%q) = true", name)
		}
		if cred := set.load(name, ""); cred.Token != nil {
			t.Errorf("load(%q) read token %q", name, *cred.Token)
		}
	}
	if cred, ok := set.Get("global"); !ok || cred.Token == nil || *cred.Token != "global-token" {
		t.Fatalf("Get(global) = %+v, %v", cred, ok)
	}
}

func TestUnreadableCredentialFilesLogErrorsAndRecoverWhenReadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, which may open a 0o000 file regardless of its mode")
	}

	t.Run("app.json", func(t *testing.T) {
		dir := writeCredentialSet(t, nil, map[string]string{"ci-app": "stale-token"})
		writeAppCredential(t, dir, "ci-app")
		appPath := filepath.Join(dir, "ci-app.app.json")
		if err := os.Chmod(appPath, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(appPath, 0o600) })

		logs := logToTest(t)
		set, err := LoadCredentialSet(dir)
		if err != nil {
			t.Fatal(err)
		}
		set.AppTransport = github.NewFakeTransport(
			mintedTokenResponse("ghs_after_chmod_get", "2099-01-01T00:00:00Z"),
			mintedTokenResponse("ghs_after_chmod_repo", "2099-01-01T00:00:00Z"),
		)

		cred, ok := set.Get("ci-app")
		if !ok || cred.Token != nil {
			t.Fatalf("Get(ci-app) while unreadable = (%+v, %v), want (Token: nil, true) without falling through to .token", cred, ok)
		}
		credRepo, ok := set.GetForRepoOn("ci-app", "", "acme", "widgets")
		if !ok || credRepo.Token != nil {
			t.Fatalf("GetForRepoOn(ci-app) while unreadable = (%+v, %v), want (Token: nil, true)", credRepo, ok)
		}
		if !strings.Contains(logs.String(), "ci-app.app.json") || !strings.Contains(logs.String(), "permission denied") {
			t.Fatalf("expected read error in logs, got:\n%s", logs.String())
		}

		// Fix file permissions on disk: the same CredentialSet recovers without
		// rebuilding or calling forget.
		if err := os.Chmod(appPath, 0o600); err != nil {
			t.Fatal(err)
		}
		cred, ok = set.Get("ci-app")
		if !ok || cred.Token == nil || *cred.Token != "ghs_after_chmod_get" {
			t.Fatalf("Get(ci-app) after chmod 0600 = (%+v, %v), want minted token", cred, ok)
		}
		credRepo, ok = set.GetForRepoOn("ci-app", "", "acme", "widgets")
		if !ok || credRepo.Token == nil || *credRepo.Token != "ghs_after_chmod_repo" {
			t.Fatalf("GetForRepoOn(ci-app) after chmod 0600 = (%+v, %v), want minted token", credRepo, ok)
		}
	})

	t.Run("token", func(t *testing.T) {
		dir := writeCredentialSet(t, nil, map[string]string{"bot": "ghp_recovered"})
		tokenPath := filepath.Join(dir, "bot.token")
		if err := os.Chmod(tokenPath, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(tokenPath, 0o600) })

		logs := logToTest(t)
		set, err := LoadCredentialSet(dir)
		if err != nil {
			t.Fatal(err)
		}

		cred, ok := set.Get("bot")
		if !ok || cred.Token != nil {
			t.Fatalf("Get(bot) while unreadable = (%+v, %v), want (Token: nil, true)", cred, ok)
		}
		credRepo, ok := set.GetForRepoOn("bot", "", "acme", "widgets")
		if !ok || credRepo.Token != nil {
			t.Fatalf("GetForRepoOn(bot) while unreadable = (%+v, %v), want (Token: nil, true)", credRepo, ok)
		}
		if !strings.Contains(logs.String(), "bot.token") || !strings.Contains(logs.String(), "permission denied") {
			t.Fatalf("expected read error in logs, got:\n%s", logs.String())
		}

		if err := os.Chmod(tokenPath, 0o600); err != nil {
			t.Fatal(err)
		}
		cred, ok = set.Get("bot")
		if !ok || cred.Token == nil || *cred.Token != "ghp_recovered" {
			t.Fatalf("Get(bot) after chmod 0600 = (%+v, %v), want ghp_recovered", cred, ok)
		}
		credRepo, ok = set.GetForRepoOn("bot", "", "acme", "widgets")
		if !ok || credRepo.Token == nil || *credRepo.Token != "ghp_recovered" {
			t.Fatalf("GetForRepoOn(bot) after chmod 0600 = (%+v, %v), want ghp_recovered", credRepo, ok)
		}
	})

	t.Run("ssh.json", func(t *testing.T) {
		logs := logToTest(t)
		dir := t.TempDir()
		set, err := LoadCredentialSet(dir)
		if err != nil {
			t.Fatal(err)
		}
		key, _ := testSSHKey(t, "")
		if err := set.SetSSHKey("deploy", SSHKeyFile{Host: "github.com", PrivateKey: key}, model.GitHostGitHub); err != nil {
			t.Fatal(err)
		}
		sshPath := filepath.Join(dir, "deploy.ssh.json")
		if err := os.Chmod(sshPath, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(sshPath, 0o600) })

		cred, ok := set.Get("deploy")
		if !ok || cred.SSH != nil || !strings.Contains(cred.Unusable, "permission denied") {
			t.Fatalf("Get(deploy) while unreadable = (%+v, %v), want Unusable permission denied", cred, ok)
		}
		credRepo, ok := set.GetForRepoOn("deploy", "", "acme", "widgets")
		if !ok || credRepo.SSH != nil || !strings.Contains(credRepo.Unusable, "permission denied") {
			t.Fatalf("GetForRepoOn(deploy) while unreadable = (%+v, %v), want Unusable permission denied", credRepo, ok)
		}
		if !strings.Contains(logs.String(), "not using SSH credential") || !strings.Contains(logs.String(), "permission denied") {
			t.Fatalf("expected SSH read error in logs, got:\n%s", logs.String())
		}

		if err := os.Chmod(sshPath, 0o600); err != nil {
			t.Fatal(err)
		}
		cred, ok = set.Get("deploy")
		if !ok || cred.SSH == nil || cred.Unusable != "" {
			t.Fatalf("Get(deploy) after chmod 0600 = (%+v, %v), want usable SSH credential", cred, ok)
		}
		credRepo, ok = set.GetForRepoOn("deploy", "", "acme", "widgets")
		if !ok || credRepo.SSH == nil || credRepo.Unusable != "" {
			t.Fatalf("GetForRepoOn(deploy) after chmod 0600 = (%+v, %v), want usable SSH credential", credRepo, ok)
		}
	})
}

func TestMissingCredentialStaysAnonymousWithoutReadErrorLog(t *testing.T) {
	logs := logToTest(t)
	set, err := LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cred, ok := set.Get("missing"); ok {
		t.Fatalf("Get(missing) = (%+v, %v), want (zero, false)", cred, ok)
	}
	if cred, ok := set.GetForRepoOn("missing", "", "acme", "widgets"); ok {
		t.Fatalf("GetForRepoOn(missing) = (%+v, %v), want (zero, false)", cred, ok)
	}
	if cred := set.loadOn("missing", "", "acme/widgets"); cred.Name != "missing" || cred.Token != nil || cred.Unusable != "" || cred.MintFailure != nil {
		t.Fatalf("loadOn(missing) = %+v, want anonymous credential", cred)
	}
	if cred, ok := set.Get("anonymous"); !ok || cred.Token != nil {
		t.Fatalf("Get(anonymous) = (%+v, %v), want anonymous", cred, ok)
	}
	if logs.String() != "" {
		t.Fatalf("missing credential logged unexpected output:\n%s", logs.String())
	}
}
