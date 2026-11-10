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

package intree

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2/google"

	"github.com/ai-on-gke/grain/pkg/capability/gcpsets/gcpfake"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

const testOrgSA = "org-runner@grain-test-proj.iam.gserviceaccount.com"

// existingGCP is a gcp server configured with a pool set "reader" and a
// set "org" over the existing account testOrgSA, with org's extra fields.
func existingGCP(t *testing.T, org map[string]any) (*protocol.Server, *gcpfake.Cloud) {
	t.Helper()
	cloud := gcpfake.New()
	cloud.DefaultIdentity = "minter@grain-test-proj.iam.gserviceaccount.com"
	cloud.AddAccount("grain-test-proj", testOrgSA, "made by hand")
	s, err := newConfigurableGCP(GCPConfig{Connector: cloud, Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	set := map[string]any{"name": "org", "admin": "minter", "service_account": testOrgSA, "roles": []any{"roles/owner"}}
	for k, v := range org {
		set[k] = v
	}
	ps, err := attachConfigured(t, s, map[string]any{
		protocol.DeploymentIDConfig: "049f1e09",
		protocol.InstanceIDConfig:   "049f1e09c0ffee0123456789abcdef01",
		"admins": []any{
			map[string]any{"name": "minter", "credential_source": "configured", "projects": []any{"grain-test-proj"}, "role_ceiling": []any{"roles/storage.objectViewer"}},
		},
		"admin_credentials": "$secret:gcp-admins",
		"permission_sets": []any{
			map[string]any{"name": "reader", "admin": "minter", "project": "grain-test-proj", "roles": []any{"roles/storage.objectViewer"}},
			set,
		},
	}, map[string]string{"gcp-admins": `{"minter": ` + testMinterKey + `}`})
	if err != nil {
		t.Fatal(err)
	}
	return ps, cloud
}

// exchange posts subject to the placed config's token_url, as a client
// library does.
func exchange(t *testing.T, tokenURL, subject string) (int, map[string]any) {
	t.Helper()
	resp, err := http.PostForm(tokenURL, url.Values{
		"grant_type":         {"urn:ietf:params:oauth:grant-type:token-exchange"},
		"subject_token":      {subject},
		"subject_token_type": {"urn:ietf:params:oauth:token-type:jwt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

func TestGCPTokenModeGrantPlacesAnExternalAccountAndNoKey(t *testing.T) {
	ps, cloud := existingGCP(t, nil)
	desc := ps.Methods["gcp-key"].Description
	if !strings.Contains(desc, "set org: existing service account "+testOrgSA) || !strings.Contains(desc, "admin minter can impersonate it") {
		t.Errorf("Test report:\n%s", desc)
	}
	resp := mint(t, ps, "gcp-key", "e7-1-0", map[string]any{"permission_set": "org"})
	if resp.Status != protocol.StatusOK {
		t.Fatalf("mint = %+v", resp)
	}
	if cloud.LiveKeys() != 0 || cloud.Calls["CreateKey"] != 0 {
		t.Fatal("a token-mode grant created a key")
	}
	if len(resp.Ports) != 1 || resp.Ports[0].HostPort == 0 {
		t.Fatalf("ports = %+v", resp.Ports)
	}
	var cfg struct {
		Type             string            `json:"type"`
		TokenURL         string            `json:"token_url"`
		CredentialSource map[string]string `json:"credential_source"`
	}
	if err := json.Unmarshal([]byte(placement(t, resp, GCPKeyPath).Content), &cfg); err != nil {
		t.Fatal(err)
	}
	wantURL := "http://127.0.0.1:" + strconv.Itoa(resp.Ports[0].HostPort) + "/runs/e7-1-0/token"
	if cfg.Type != "external_account" || cfg.TokenURL != wantURL {
		t.Fatalf("config = %+v, want an external_account exchanging at %s", cfg, wantURL)
	}
	subjectPath := cfg.CredentialSource["file"]
	subject := placement(t, resp, subjectPath)
	if subject.Mode != "0600" || subject.Content == "" {
		t.Fatalf("subject token placement = %+v", subject)
	}
	if about := placement(t, resp, gcpAboutPath(GCPKeyPath)).Content; !strings.Contains(about, "service_account: "+testOrgSA) || !strings.Contains(about, "no key exists") {
		t.Errorf("about = %s", about)
	}
	// gcloud is pointed at the config, not signed in with it: `gcloud auth
	// login --cred-file` introspects the impersonated token against STS,
	// which answers {"active": false} for a token it did not issue.
	setup := placement(t, resp, resp.Setup).Content
	if !strings.Contains(setup, "gcloud config set auth/credential_file_override '"+GCPKeyPath+"' --quiet") ||
		!strings.Contains(setup, "gcloud config set project ") {
		t.Errorf("setup does not point gcloud at the config:\n%s", setup)
	}
	if strings.Contains(setup, "auth login") {
		t.Errorf("setup still runs gcloud auth login, which fails STS introspection:\n%s", setup)
	}
	assertSetupKeepsStderr(t, setup)

	// A client library reading the placed config, as ADC would, gets the
	// impersonated account's token from the forwarded listener.
	dir := t.TempDir()
	local := filepath.Join(dir, "subject")
	os.WriteFile(local, []byte(subject.Content), 0o600)
	cfgJSON := strings.Replace(placement(t, resp, GCPKeyPath).Content, subjectPath, local, 1)
	creds, err := google.CredentialsFromJSON(context.Background(), []byte(cfgJSON), "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := creds.TokenSource.Token()
	if err != nil {
		t.Fatalf("the client library's token exchange: %v", err)
	}
	if !strings.HasSuffix(tok.AccessToken, testOrgSA) || time.Until(tok.Expiry) > time.Hour+time.Minute {
		t.Fatalf("token = %+v", tok)
	}
	// Each refresh is another impersonated token, never anything kept in
	// the sandbox but the per-attempt token.
	code, body := exchange(t, cfg.TokenURL, subject.Content)
	if code != http.StatusOK || body["access_token"] == tok.AccessToken || body["token_type"] != "Bearer" {
		t.Fatalf("refresh = %d %v", code, body)
	}
	if code, _ := exchange(t, cfg.TokenURL, "not-the-token"); code != http.StatusUnauthorized {
		t.Fatalf("a wrong subject token got %d", code)
	}
	otherURL := strings.Replace(cfg.TokenURL, "e7-1-0", "e7-9-0", 1)
	if code, _ := exchange(t, otherURL, subject.Content); code != http.StatusUnauthorized {
		t.Fatalf("another attempt's path got %d", code)
	}

	// The ID-token helper's endpoint.
	idURL := strings.Replace(cfg.TokenURL, "/token", "/id-token", 1) + "?audience=https%3A%2F%2Fsvc.example.com"
	req, _ := http.NewRequest(http.MethodGet, idURL, nil)
	req.Header.Set("Authorization", "Bearer "+subject.Content)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	idTok, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != http.StatusOK || string(idTok) != "id-https://svc.example.com-"+testOrgSA {
		t.Fatalf("id-token = %d %q", r.StatusCode, idTok)
	}

	// A renewal keeps the placed per-attempt token good.
	if renewed := mint(t, ps, "gcp-key", "e7-1-0", map[string]any{"permission_set": "org"}); placement(t, renewed, subjectPath).Content != subject.Content {
		t.Fatal("a renewal changed the per-attempt token the sandbox already holds")
	}
	if _, err := ps.Release(context.Background(), "gcp-key", "e7-1-0"); err != nil {
		t.Fatal(err)
	}
	if code, _ := exchange(t, cfg.TokenURL, subject.Content); code != http.StatusUnauthorized {
		t.Fatalf("after release the exchange got %d", code)
	}
	for _, call := range []string{"CreateServiceAccount", "SetPolicy", "DeleteServiceAccount", "DeleteKey"} {
		if cloud.Calls[call] != 0 {
			t.Errorf("%s called for a set over an existing account", call)
		}
	}
}

func TestGCPKeyModeGrantOverAnExistingAccount(t *testing.T) {
	ps, cloud := existingGCP(t, map[string]any{"credential": "key"})
	resp := mint(t, ps, "gcp-key", "e8-1-0", map[string]any{"permission_set": "org"})
	if resp.Status != protocol.StatusOK || len(resp.Ports) != 0 {
		t.Fatalf("mint = %+v", resp)
	}
	if !strings.Contains(placement(t, resp, GCPKeyPath).Content, testOrgSA) || cloud.KeyCount(testOrgSA) != 1 {
		t.Fatal("no key for the existing account")
	}
	if _, err := ps.Release(context.Background(), "gcp-key", "e8-1-0"); err != nil {
		t.Fatal(err)
	}
	if cloud.KeyCount(testOrgSA) != 0 {
		t.Fatal("release left the key")
	}
	for _, call := range []string{"CreateServiceAccount", "SetPolicy", "DeleteServiceAccount"} {
		if cloud.Calls[call] != 0 {
			t.Errorf("%s called for a set over an existing account", call)
		}
	}
}

// A set over an existing account is a person's to grant: an agent that
// holds it cannot pass it on, while the pool set stays covered as before.
func TestGCPExistingAccountSetsArePersonGranted(t *testing.T) {
	ps, _ := existingGCP(t, nil)
	ref := protocol.Ref{CapabilityID: ps.ID, Method: "gcp-key"}
	org := protocol.Grant{Ref: ref, Args: validated(t, ps, "gcp-key", map[string]any{"permission_set": "org"})}
	reader := protocol.Grant{Ref: ref, Args: validated(t, ps, "gcp-key", map[string]any{"permission_set": "reader"})}
	if protocol.GrantKindOf(org) != protocol.GrantByPerson {
		t.Error("the existing-account set is not person-granted")
	}
	if protocol.Covers([]protocol.Grant{org}, org) {
		t.Error("an agent holding the existing-account set could pass it on")
	}
	// The removed account argument is rejected by the schema rather than
	// naming the reserved set.
	if _, err := protocol.ValidateArgs(ps.Methods["gcp-key"].ArgsSchema, map[string]any{"account": "org"}); err == nil {
		t.Error("account: org was accepted by gcp-key schema")
	}
	if protocol.GrantKindOf(reader) != protocol.GrantCovered || !protocol.Covers([]protocol.Grant{reader}, reader) {
		t.Error("the pool set is no longer covered by its own grant")
	}

	// person_granted: false opts out.
	ps2, _ := existingGCP(t, map[string]any{"person_granted": false})
	org2 := protocol.Grant{Ref: protocol.Ref{CapabilityID: ps2.ID, Method: "gcp-key"}, Args: validated(t, ps2, "gcp-key", map[string]any{"permission_set": "org"})}
	if protocol.GrantKindOf(org2) != protocol.GrantCovered {
		t.Error("person_granted: false still reserves the set")
	}
}

// assertSetupKeepsStderr fails if any gcloud command in an activate.sh
// discards its stderr: that is the only account of why setup failed that
// the task's conversation gets (CapabilitySetupError's output tail).
func assertSetupKeepsStderr(t *testing.T, script string) {
	t.Helper()
	for _, line := range strings.Split(script, "\n") {
		if !strings.HasPrefix(line, "gcloud ") {
			continue
		}
		if strings.Contains(line, "2>") || strings.Contains(line, "&>") {
			t.Errorf("setup line discards stderr: %q", line)
		}
		if !strings.Contains(line, "--quiet") {
			t.Errorf("setup line is not --quiet: %q", line)
		}
	}
}

// runActivate runs an activate.sh under sh with a stand-in gcloud first on
// PATH that logs its arguments and, when failArg names one of them, fails
// the way gcloud does: a message on stderr and status 1.
func runActivate(t *testing.T, script, failArg string) (log, output string, err error) {
	t.Helper()
	if _, lookErr := exec.LookPath("sh"); lookErr != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "gcloud.log")
	fake := "#!/bin/sh\n" +
		"echo \"$*\" >> " + shellQuote(logPath) + "\n" +
		"echo 'Updated property.'\n" +
		"case \"$*\" in *" + failArg + "*) echo 'ERROR: (gcloud) fake failure for " + failArg + "' >&2; exit 1;; esac\n"
	if err := os.WriteFile(filepath.Join(dir, "gcloud"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(dir, "activate.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", scriptPath)
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	logged, _ := os.ReadFile(logPath)
	return string(logged), string(out), err
}

func TestGCPActivateScriptsRunAndReportFailures(t *testing.T) {
	for _, tc := range []struct {
		name, script, want string
	}{
		{"token", gcpTokenActivateScript(GCPKeyPath, "grain-test-proj"),
			"config set auth/credential_file_override " + GCPKeyPath + " --quiet\nconfig set project grain-test-proj --quiet\n"},
		{"key", gcpKeyActivateScript(GCPKeyPath, "grain-test-proj"),
			"auth activate-service-account --key-file=" + GCPKeyPath + " --quiet\nconfig set project grain-test-proj --quiet\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertSetupKeepsStderr(t, tc.script)
			log, out, err := runActivate(t, tc.script, "no-such-arg")
			if err != nil {
				t.Fatalf("activate.sh failed: %v\n%s", err, out)
			}
			if log != tc.want {
				t.Errorf("gcloud ran\n%s\nwant\n%s", log, tc.want)
			}
			if strings.Contains(out, "Updated property") {
				t.Errorf("stdout was not discarded: %q", out)
			}
			// A failing gcloud stops the script, and what it said on stderr
			// is what setup's output carries.
			log, out, err = runActivate(t, tc.script, "project")
			if err == nil {
				t.Fatalf("activate.sh succeeded although gcloud config set project failed")
			}
			if !strings.Contains(out, "fake failure for project") {
				t.Errorf("gcloud's stderr was not kept: output = %q", out)
			}
			if strings.Count(log, "\n") != 2 {
				t.Errorf("gcloud ran\n%s", log)
			}
		})
	}
}

func TestGCPTokenModeIntrospectAndRealGcloud(t *testing.T) {
	ps, cloud := existingGCP(t, map[string]any{"credential": "token"})
	resp := mint(t, ps, "gcp-key", "e7-gcloud-0", map[string]any{"permission_set": "org"})
	if resp.Status != protocol.StatusOK || len(resp.Ports) != 1 {
		t.Fatalf("mint = %+v", resp)
	}
	var cfg struct {
		TokenURL         string            `json:"token_url"`
		TokenInfoURL     string            `json:"token_info_url"`
		CredentialSource map[string]string `json:"credential_source"`
	}
	rawCfg := placement(t, resp, GCPKeyPath).Content
	if err := json.Unmarshal([]byte(rawCfg), &cfg); err != nil {
		t.Fatal(err)
	}
	wantInfo := "http://127.0.0.1:" + strconv.Itoa(resp.Ports[0].HostPort) + "/runs/e7-gcloud-0/introspect"
	if cfg.TokenInfoURL != wantInfo {
		t.Fatalf("token_info_url = %q, want %q", cfg.TokenInfoURL, wantInfo)
	}
	subject := placement(t, resp, cfg.CredentialSource["file"]).Content
	status, exBody := exchange(t, cfg.TokenURL, subject)
	if status != http.StatusOK {
		t.Fatalf("exchange = %d %v", status, exBody)
	}
	mintedTok, _ := exBody["access_token"].(string)
	if mintedTok == "" {
		t.Fatalf("missing access_token in %v", exBody)
	}

	iResp, err := http.PostForm(cfg.TokenInfoURL, url.Values{"token": {mintedTok}, "token_type_hint": {"access_token"}})
	if err != nil {
		t.Fatal(err)
	}
	var iBody map[string]any
	json.NewDecoder(iResp.Body).Decode(&iBody)
	iResp.Body.Close()
	if iResp.StatusCode != http.StatusOK || iBody["active"] != true || iBody["username"] != testOrgSA {
		t.Fatalf("introspect = %d %v, want 200 active=true username=%s", iResp.StatusCode, iBody, testOrgSA)
	}

	// RFC 7662 §2.2: a token the endpoint does not recognise -- never
	// minted, or minted for another attempt -- is {"active": false} with
	// 200, and nothing else about it.
	assertInactive(t, "introspect(unknown)", cfg.TokenInfoURL, "not-a-minted-token")
	otherAttempt := strings.Replace(cfg.TokenInfoURL, "/runs/e7-gcloud-0/", "/runs/e7-other-0/", 1)
	assertInactive(t, "introspect(another attempt's path)", otherAttempt, mintedTok)

	if gcloudPath, err := exec.LookPath("gcloud"); err == nil {
		dir := t.TempDir()
		subjLocal := filepath.Join(dir, "subject")
		credLocal := filepath.Join(dir, "cred.json")
		actLocal := filepath.Join(dir, "activate.sh")
		if err := os.WriteFile(subjLocal, []byte(subject), 0o600); err != nil {
			t.Fatal(err)
		}
		rewrittenCfg := strings.ReplaceAll(rawCfg, cfg.CredentialSource["file"], subjLocal)
		if err := os.WriteFile(credLocal, []byte(rewrittenCfg), 0o600); err != nil {
			t.Fatal(err)
		}
		act := strings.ReplaceAll(placement(t, resp, resp.Setup).Content, GCPKeyPath, credLocal)
		if err := os.WriteFile(actLocal, []byte(act), 0o700); err != nil {
			t.Fatal(err)
		}
		env := append(os.Environ(),
			"HOME="+dir,
			"CLOUDSDK_CONFIG="+filepath.Join(dir, ".config", "gcloud"),
			"CLOUDSDK_CORE_DISABLE_USAGE_REPORTING=true",
			"HTTPS_PROXY=http://127.0.0.1:1",
			"HTTP_PROXY=http://127.0.0.1:1",
			"NO_PROXY=127.0.0.1,localhost",
		)
		setupCmd := exec.Command("/bin/sh", actLocal)
		setupCmd.Env = env
		if out, err := setupCmd.CombinedOutput(); err != nil {
			t.Fatalf("activate.sh failed: %v\n%s", err, out)
		}
		printCmd := exec.Command(gcloudPath, "auth", "print-access-token")
		printCmd.Env = env
		out, err := printCmd.CombinedOutput()
		if err != nil {
			t.Fatalf("gcloud auth print-access-token failed: %v\n%s", err, out)
		}
		if got := strings.TrimSpace(string(out)); !strings.Contains(got, testOrgSA) {
			t.Fatalf("gcloud auth print-access-token = %q, want token for %s", got, testOrgSA)
		}
	}

	if cloud.LiveKeys() != 0 || cloud.Calls["CreateKey"] != 0 {
		t.Fatalf("token-mode grant created keys: %v", cloud.Calls)
	}
	if _, err := ps.Release(context.Background(), "gcp-key", "e7-gcloud-0"); err != nil {
		t.Fatal(err)
	}
	assertInactive(t, "introspect after release", cfg.TokenInfoURL, mintedTok)
}

func TestGCPManagedTokenSetConfigureMintExchangeAndCleanup(t *testing.T) {
	cloud := gcpfake.New()
	cloud.RequireTokenCreator = true
	cloud.DefaultIdentity = "alice@example.com"
	s, err := newConfigurableGCP(GCPConfig{Connector: cloud, Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	baseCfg := func(sets []any, extra map[string]any) map[string]any {
		cfg := map[string]any{
			protocol.DeploymentIDConfig: "049f1e09",
			protocol.InstanceIDConfig:   "049f1e09c0ffee0123456789abcdef01",
			"permission_sets":           sets,
		}
		for k, v := range extra {
			cfg[k] = v
		}
		return cfg
	}

	arSet := map[string]any{
		"name":           "ar-writer",
		"project":        "grain-test-proj",
		"credential":     "token",
		"roles":          []any{"roles/artifactregistry.writer"},
		"resource_scope": "repositories/us-central1/images",
	}
	ps, err := attachConfigured(t, s, baseCfg([]any{arSet}, nil), nil)
	if err != nil {
		t.Fatalf("attach managed token set: %v", err)
	}
	wantSA := "grain-049f1e09-ar-writer@grain-test-proj.iam.gserviceaccount.com"
	arRes := "projects/grain-test-proj/locations/us-central1/repositories/images"
	saRes := "projects/grain-test-proj/serviceAccounts/" + wantSA
	if got := cloud.Members(arRes, "roles/artifactregistry.writer"); len(got) != 1 || got[0] != "serviceAccount:"+wantSA {
		t.Fatalf("AR bindings = %v, want [serviceAccount:%s]", got, wantSA)
	}
	if got := cloud.Members(saRes, "roles/iam.serviceAccountTokenCreator"); len(got) != 1 || got[0] != "user:alice@example.com" {
		t.Fatalf("SA tokenCreator bindings = %v, want [user:alice@example.com]", got)
	}
	// Managed token sets are NOT person-granted by default (unlike existing SA sets).
	if reserved := ps.Methods["gcp-key"].ArgsSchema["properties"].(map[string]any)["permission_set"].(map[string]any)[protocol.PersonGrantedArgKey]; reserved != nil {
		t.Fatalf("unexpected person-granted reservation on managed token set: %v", reserved)
	}

	resp := mint(t, ps, "gcp-key", "m1-1-0", map[string]any{"permission_set": "ar-writer"})
	if resp.Status != protocol.StatusOK {
		t.Fatalf("mint = %+v", resp)
	}
	if !strings.Contains(resp.PromptSection, "service account "+wantSA+" in project grain-test-proj, roles roles/artifactregistry.writer on repositories/us-central1/images") {
		t.Errorf("PromptSection = %s", resp.PromptSection)
	}
	var cfg struct {
		Type             string            `json:"type"`
		TokenURL         string            `json:"token_url"`
		CredentialSource map[string]string `json:"credential_source"`
	}
	if err := json.Unmarshal([]byte(placement(t, resp, GCPKeyPath).Content), &cfg); err != nil {
		t.Fatal(err)
	}
	subject := placement(t, resp, cfg.CredentialSource["file"]).Content
	code, body := exchange(t, cfg.TokenURL, subject)
	if code != http.StatusOK || !strings.HasSuffix(body["access_token"].(string), wantSA) {
		t.Fatalf("exchange = %d %v", code, body)
	}
	if cloud.LiveKeys() != 0 || cloud.Calls["CreateKey"] != 0 {
		t.Fatalf("managed token set created keys: %v", cloud.Calls)
	}

	// Idempotent reconfigure while attempt is running keeps exchange alive and writes nothing.
	setsBefore := cloud.Calls["SetPolicy"]
	if err := ps.Configure(context.Background(), baseCfg([]any{arSet}, nil)); err != nil {
		t.Fatalf("idempotent reconfigure: %v", err)
	}
	if cloud.Calls["SetPolicy"] != setsBefore {
		t.Errorf("SetPolicy calls changed on idempotent reconfigure: %d -> %d", setsBefore, cloud.Calls["SetPolicy"])
	}
	if code, _ := exchange(t, cfg.TokenURL, subject); code != http.StatusOK {
		t.Fatalf("exchange after reconfigure = %d", code)
	}
	if _, err := ps.Release(context.Background(), "gcp-key", "m1-1-0"); err != nil {
		t.Fatal(err)
	}

	// Removing ar-writer and adding it to delete_orphans unbinds AR and deletes the SA.
	otherSet := map[string]any{
		"name":       "log-reader",
		"project":    "grain-test-proj",
		"credential": "token",
		"roles":      []any{"roles/logging.viewer"},
	}
	if err := ps.Configure(context.Background(), baseCfg([]any{otherSet}, map[string]any{"delete_orphans": []any{"ar-writer"}})); err != nil {
		t.Fatalf("reconfigure with delete_orphans: %v", err)
	}
	if got := cloud.Members(arRes, "roles/artifactregistry.writer"); len(got) != 0 {
		t.Errorf("AR bindings survived delete_orphans: %v", got)
	}
	if _, exists := cloud.Accounts["grain-test-proj"][wantSA]; exists {
		t.Errorf("account %s survived delete_orphans", wantSA)
	}
}

// assertInactive posts token to an introspection endpoint and wants RFC
// 7662's answer for a token it does not recognise: 200 and exactly
// {"active": false}.
func assertInactive(t *testing.T, what, endpoint, token string) {
	t.Helper()
	resp, err := http.PostForm(endpoint, url.Values{"token": {token}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("%s: decoding the answer: %v", what, err)
	}
	if resp.StatusCode != http.StatusOK || len(body) != 1 || body["active"] != false {
		t.Fatalf("%s = %d %v, want 200 {\"active\": false}", what, resp.StatusCode, body)
	}
}
