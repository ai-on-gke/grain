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

package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/capability/githubsandbox"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

func TestAPIHostRewritesGitHubDotComOnly(t *testing.T) {
	t.Parallel()
	if got := apiHost("github.com"); got != "api.github.com" {
		t.Errorf("apiHost(github.com) = %q, want api.github.com", got)
	}
	if got := apiHost("mock.example"); got != "mock.example" {
		t.Errorf("apiHost(mock.example) = %q, want it left unchanged for a test/mock host", got)
	}
}

func TestManifestSubmissionFormEscapesTheManifest(t *testing.T) {
	t.Parallel()
	form := manifestSubmissionForm("https://github.com/settings/apps/new?state=abc", `{"name":"a\"b"}`)
	if !strings.Contains(form, "https://github.com/settings/apps/new?state=abc") {
		t.Errorf("form does not name the submit URL: %s", form)
	}
	if strings.Contains(form, `"a\"b"`) {
		t.Errorf("form embeds an unescaped quote from the manifest, which would break out of the value attribute: %s", form)
	}
	if !strings.Contains(form, "&quot;") {
		t.Errorf("form does not escape quotes in the manifest: %s", form)
	}
}

// TestWriteAppCredentialsRoundTripsThroughTheSecretsStore is the
// write-then-resolve round trip bwsalmon/agents#495 found missing:
// bootstrapGitHubApp used to write plain files under <secrets-dir>/
// github-app/, which githubsandbox.Provider.Resolve -- backed by
// pkg/secrets.Store, one encrypted file -- never read. This asserts the
// credentials writeAppCredentials stores are the exact ones
// DefaultAppIDCredential and DefaultPrivateKeyCredential resolve back to.
func TestWriteAppCredentialsRoundTripsThroughTheSecretsStore(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	const wantAppID = "123456"
	const wantPrivateKey = "-----BEGIN RSA PRIVATE KEY-----\nfake\n-----END RSA PRIVATE KEY-----\n"

	if err := writeAppCredentials(dataDir, wantAppID, wantPrivateKey); err != nil {
		t.Fatalf("writeAppCredentials: %v", err)
	}

	// Opened through secretsConfig, the same two paths the daemon, the UI
	// and `grain secrets` all resolve -- which is the property this test
	// exists to hold: a credential written by one is readable by the rest.
	store := secrets.Open(daemon.SecretsConfig(dataDir))
	ctx := context.Background()

	gotAppID, err := store.Resolve(ctx, githubsandbox.DefaultAppIDCredential)
	if err != nil {
		t.Fatalf("resolving %s: %v", githubsandbox.DefaultAppIDCredential, err)
	}
	if gotAppID != wantAppID {
		t.Errorf("resolved app id %q, want %q", gotAppID, wantAppID)
	}

	gotPrivateKey, err := store.Resolve(ctx, githubsandbox.DefaultPrivateKeyCredential)
	if err != nil {
		t.Fatalf("resolving %s: %v", githubsandbox.DefaultPrivateKeyCredential, err)
	}
	if gotPrivateKey != wantPrivateKey {
		t.Errorf("resolved private key %q, want %q", gotPrivateKey, wantPrivateKey)
	}
}

func TestRandomHexIsUnpredictableAndTheRightLength(t *testing.T) {
	t.Parallel()
	a, err := randomHex(16)
	if err != nil {
		t.Fatal(err)
	}
	b, err := randomHex(16)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 32 {
		t.Errorf("randomHex(16) has length %d, want 32 hex characters", len(a))
	}
	if a == b {
		t.Errorf("two calls to randomHex(16) produced the same value")
	}
}

func TestManifestPermissionsIncludesPackagesWrite(t *testing.T) {
	t.Parallel()
	if got := manifestPermissions["packages"]; got != "write" {
		t.Errorf("manifestPermissions[packages] = %q, want write", got)
	}
}

func TestManifestPermissionsIncludesChecksWrite(t *testing.T) {
	t.Parallel()
	if got := manifestPermissions["checks"]; got != "write" {
		t.Errorf("manifestPermissions[checks] = %q, want write", got)
	}
}

func TestBuildAppManifestForStandaloneBotSubscribesToEventsAndDropsAdminPerms(t *testing.T) {
	t.Parallel()
	m := buildAppManifest("grain-bot", "https://grain.example.com", "http://127.0.0.1:12345/callback", "https://grain.example.com/webhooks/github", true)
	hook, ok := m["hook_attributes"].(map[string]any)
	if !ok || hook["url"] != "https://grain.example.com/webhooks/github" || hook["active"] != true {
		t.Fatalf("hook_attributes = %#v", m["hook_attributes"])
	}
	events, ok := m["default_events"].([]string)
	if !ok || len(events) == 0 {
		t.Fatalf("default_events = %#v, want non-empty event list", m["default_events"])
	}
	perms, ok := m["default_permissions"].(map[string]string)
	if !ok {
		t.Fatalf("default_permissions = %#v", m["default_permissions"])
	}
	if perms["pull_requests"] != "write" || perms["checks"] != "write" {
		t.Errorf("bot manifest missing core permissions: %#v", perms)
	}
	if _, hasOrgAdmin := perms["organization_administration"]; hasOrgAdmin {
		t.Errorf("bot-only manifest should not request organization_administration: %#v", perms)
	}
	if got := manifestSubmitURL("github.com", "acme", "s1"); got != "https://github.com/organizations/acme/settings/apps/new?state=s1" {
		t.Errorf("manifestSubmitURL(org) = %q", got)
	}
}
