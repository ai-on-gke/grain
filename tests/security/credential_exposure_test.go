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

package security

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/console"
	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// plantedCredential is one fake credential of a kind the credential
// exposure audit (docs/security/audits/2026-11-10-credential-exposure.md)
// inventories, planted where grain keeps that kind.
type plantedCredential struct {
	kind  string
	value string
}

// plantedCredentials are made-up values, each unique enough that finding
// it in a response can only mean the response carries it.
var plantedCredentials = []plantedCredential{
	{"git PAT", "ghp_PLANTEDpatPLANTEDpat0123456789"},
	{"GitHub App webhook secret", "planted-webhook-secret-91c2"},
	{"Claude OAuth token", "sk-ant-oat01-PLANTEDclaudeOAUTHtoken000"},
	{"Gemini API key", "AIzaPLANTEDgeminiKEY0000000000000"},
	{"OpenAI API key", "sk-PLANTEDopenaiKEY000000000000"},
	{"agent backend credential", "planted-agent-backend-credential-44d1"},
	{"codex-auth.json refresh token", "planted-codex-refresh-token-0b9e"},
	{"codex-auth.json access token", "planted-codex-access-token-5a7c"},
	{"gcp-key minter private key", "PLANTED-GCP-MINTER-PRIVATE-KEY-c0ffee"},
	{"registry token", "grc_PLANTEDregistryTOKEN0000"},
	{"relay token", "planted-relay-token-6e1d"},
	{"loose agent key file", "planted-loose-gemini-key-file-1e1e"},
	{"state repository token file", "planted-state-repo-token-file-7c7c"},
	{"request_secret answer", "planted-request-secret-answer-3c3c"},
	{"standing sandbox secret", "planted-standing-sandbox-secret-8f8f"},
	{"capability server data-plane token", "planted-capability-env-secret-2a2a"},
	{"capability server literal header", "planted-literal-header-value-d00d"},
	{"access code / LocalAPIToken", testAccessCode},
}

// TestNonAdminReadsNoPlantedCredential plants a fake credential of every
// kind grain holds where grain holds it, signs in a person whose role is
// grain/viewer -- every non-dangerous :read scope, which is as much as a
// non-admin is ever given to read -- and reads every GET route the server
// has. No response may carry any planted value, plainly or base64'd (the
// console's output is base64), whatever status it answers with.
//
// The nightly go-long job runs this package; the security-attacks job
// that follows #3983 names it as its credential-exposure entry point.
func TestNonAdminReadsNoPlantedCredential(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	t.Setenv("GRAIN_ALLOWED_PRINCIPALS", "")
	const viewer = "viewer@example.com"

	creds := append([]plantedCredential(nil), plantedCredentials...)
	var codexAuthFile, secretsDir string
	consoles := console.NewManager()
	t.Cleanup(func() {
		for _, info := range consoles.List() {
			_ = consoles.Close(info.TaskID)
		}
	})
	f := newRouteFixtureWith(t, true, true, func(cfg *ui.Config) {
		secretsDir = filepath.Join(cfg.DataDir, "secrets")
		cfg.CodexAuthFile = filepath.Join(secretsDir, "codex-auth.json")
		creds = append(creds, plantCredentialFiles(t, cfg, secretsDir)...)
		plantSecrets(t, cfg.Secrets)
		codexAuthFile = cfg.CodexAuthFile
		cfg.Consoles = consoles
		// Exactly what a daemon hands its UI: every value in the secrets
		// store, the loose credential files beside it, and every
		// credential shape.
		cfg.LogRedactor = daemon.LogRedactor(secretsDir, cfg.Secrets)
	})
	f.from = publicAddr
	f.accessCookie = ""
	ctx := context.Background()
	plantStoredCredentials(t, ctx, f)
	if _, err := os.Stat(codexAuthFile); err != nil {
		t.Fatalf("codex auth file was not planted: %v", err)
	}

	// A run's console that prints the credentials it was handed -- every
	// one the secrets store holds, and the access code -- as an agent
	// told to print its own credentials would.
	//
	// Not the capability server's literal header, which no run is handed,
	// nor the GCP minter key, which mints on the host and never enters a
	// sandbox; what a gcp-key grant does put in one -- an access token and
	// an ID token minted mid-run -- is printed instead.
	printed := []string{
		"ya29.PLANTEDmintedACCESStoken0123456789abcdef",
		"eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJwbGFudGVkIn0.PLANTEDidTOKENsignature0123",
	}
	for _, c := range plantedCredentials {
		if c.kind != "capability server literal header" && c.kind != "gcp-key minter private key" {
			printed = append(printed, c.value)
		}
	}
	if _, err := consoles.Start(console.Spec{TaskID: f.taskID, StartedAt: time.Now()}, console.Command{
		Path: "/bin/sh",
		Args: append([]string{"-c", `for v in "$@"; do printf 'leak: %s\n' "$v"; done; echo done; sleep 30`, "sh"}, printed...),
	}); err != nil {
		t.Fatal(err)
	}
	readConsole := func() string {
		rec := f.send(t, http.MethodGet, "/api/console/"+f.taskID+"/output", "", viewer)
		if rec.code != http.StatusOK {
			t.Fatalf("GET /api/console/{id}/output as grain/viewer answered %d: %s", rec.code, rec.excerpt())
		}
		var out ui.ConsoleOutputResponse
		if err := json.Unmarshal(rec.body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		data, err := base64.StdEncoding.DecodeString(out.Output)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	if _, _, err := f.store.AddAllowedPrincipalWithRole(ctx, "user:"+viewer, "test", "test", "grain/viewer", time.Now()); err != nil {
		t.Fatal(err)
	}

	read := 0
	for _, pattern := range f.server.Routes() {
		method, _, ok := strings.Cut(pattern, " ")
		if !ok || method != http.MethodGet {
			continue
		}
		rec := f.send(t, method, f.path(pattern), "", viewer)
		if rec.code < 300 {
			read++
		}
		body := rec.body.String()
		for _, c := range creds {
			if leaks(body, c.value) {
				t.Errorf("%s as grain/viewer answered %d carrying the planted %s:\n%s", pattern, rec.code, c.kind, rec.excerpt())
			}
		}
	}
	// A sweep that every route refused proves nothing: the viewer must
	// actually have been let in to read.
	t.Logf("grain/viewer read %d GET routes", read)
	if read < 50 {
		t.Fatalf("grain/viewer read only %d GET routes successfully; the sweep is not reaching the handlers", read)
	}
	// The console has printed everything by now, or will within moments.
	deadline := time.Now().Add(10 * time.Second)
	output := readConsole()
	for !strings.Contains(output, "done") && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		output = readConsole()
	}
	if !strings.Contains(output, "done") {
		t.Fatalf("the console never finished printing: %q", output)
	}
	for _, v := range printed {
		if strings.Contains(output, v) {
			t.Errorf("GET /api/console/{id}/output as grain/viewer carries a credential the run printed: %q", v)
		}
	}
	if !strings.Contains(output, "[redacted") {
		t.Errorf("the console output shows no redaction at all: %q", output)
	}

	for _, pattern := range []string{"GET /api/capability-servers", "GET /api/secrets", "GET /api/agent-keys", "GET /api/github-tokens"} {
		if rec := f.send(t, http.MethodGet, f.path(pattern), "", viewer); rec.code != http.StatusOK {
			t.Errorf("%s as grain/viewer answered %d, want 200 (the sweep must reach the credential routes): %s", pattern, rec.code, rec.excerpt())
		}
	}
}

// leaks says whether body carries value, plainly, JSON-escaped or base64'd.
func leaks(body, value string) bool {
	if strings.Contains(body, value) {
		return true
	}
	if esc, _ := json.Marshal(value); strings.Contains(body, strings.Trim(string(esc), `"`)) {
		return true
	}
	// A base64 encoding of a longer stream carries value at one of three
	// alignments; checking the inner run of each is enough to find it.
	for shift := 0; shift < 3; shift++ {
		enc := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", shift) + value))
		inner := enc[4 : len(enc)-4]
		if len(inner) >= 12 && strings.Contains(body, inner) {
			return true
		}
	}
	return false
}

func planted(kind string) string {
	for _, c := range plantedCredentials {
		if c.kind == kind {
			return c.value
		}
	}
	panic("no planted credential " + kind)
}

// plantCredentialFiles writes the loose credential files grain keeps under
// <data-dir>/secrets: a PAT and a GitHub App, and a Codex login. It
// returns the App's private key, which is made fresh so the git proxy
// loads it, as one more planted credential.
func plantCredentialFiles(t *testing.T, cfg *ui.Config, secretsDir string) []plantedCredential {
	t.Helper()
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	appPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey)}))
	// The PEM's second line: 64 characters of the key's own body.
	appKeyLine := strings.Split(appPEM, "\n")[1]
	dir := filepath.Join(secretsDir, "github")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "planted.token"), []byte(planted("git PAT")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	app, _ := json.Marshal(map[string]string{
		"app_id": "12345", "installation_id": "67890",
		"private_key":    appPEM,
		"webhook_secret": planted("GitHub App webhook secret"),
	})
	if err := os.WriteFile(filepath.Join(dir, "planted-app.app.json"), app, 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := gitproxy.LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !set.IsApp("planted-app") {
		t.Fatal("the git proxy did not load the planted GitHub App")
	}
	cfg.Credentials = set
	codex, _ := json.Marshal(map[string]any{
		"OPENAI_API_KEY": nil,
		"tokens": map[string]string{
			"refresh_token": planted("codex-auth.json refresh token"),
			"access_token":  planted("codex-auth.json access token"),
			"account_id":    "acct-planted",
		},
		"last_refresh": time.Now().UTC().Format(time.RFC3339),
	})
	if err := os.WriteFile(cfg.CodexAuthFile, codex, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, kind := range map[string]string{"gemini-api-key": "loose agent key file", "state-repo-token": "state repository token file"} {
		if err := os.WriteFile(filepath.Join(secretsDir, name), []byte(planted(kind)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return []plantedCredential{{"GitHub App private key", appKeyLine}}
}

// plantSecrets puts the rest in the secrets store: agent keys, a
// backend's credential, the GCP minter key behind a gcp-key, the registry
// and relay tokens, a request_secret answer, a standing sandbox's secret
// and a capability server's data-plane token.
func plantSecrets(t *testing.T, sec *secrets.Store) {
	t.Helper()
	minter, _ := json.Marshal(map[string]string{
		"type": "service_account", "client_email": "minter@example-project.iam.gserviceaccount.com",
		"private_key": "-----BEGIN PRIVATE KEY-----\n" + planted("gcp-key minter private key") + "\n-----END PRIVATE KEY-----\n",
	})
	for _, s := range []struct{ secret, key, value string }{
		{"claude-oauth-token", "", planted("Claude OAuth token")},
		{"gemini-api-key", "", planted("Gemini API key")},
		{"openai-api-key", "", planted("OpenAI API key")},
		{"agent-backend-planted", "", planted("agent backend credential")},
		{model.GCPKeyMinterCredential("planted"), "", string(minter)},
		{"registry-registration-token", "", planted("registry token")},
		{"relay", "token", planted("relay token")},
		{"planted-request", "", planted("request_secret answer")},
		{"planted-standing", "", planted("standing sandbox secret")},
		{"planted-cap-token", "", planted("capability server data-plane token")},
	} {
		key := s.key
		if key == "" {
			key = "value"
		}
		if err := sec.Set(s.secret, key, []byte(s.value)); err != nil {
			t.Fatalf("planting %s: %v", s.secret, err)
		}
	}
}

// plantStoredCredentials puts in the configuration that names them: a
// gcp-key minting through the planted minter key, and a capability server
// referencing one secret and -- as an entry saved before saves refused it
// -- carrying another typed in.
func plantStoredCredentials(t *testing.T, ctx context.Context, f *routeFixture) {
	t.Helper()
	if err := f.store.PutGCPKey(ctx, model.GCPKey{
		Name: "planted", ProjectID: "example-project",
		ServiceAccountEmail: "planted@example-project.iam.gserviceaccount.com", ServiceAccountCount: 1,
		MinterCredential: model.GCPKeyMinterCredential("planted"),
	}); err != nil {
		t.Fatalf("planting a gcp key: %v", err)
	}
	if err := f.store.UpdateConfig(ctx, func(cfg *model.Config) error {
		if cfg.MCPServers == nil {
			cfg.MCPServers = map[string]protocol.ServerConfig{}
		}
		cfg.MCPServers["planted-http"] = protocol.ServerConfig{
			ServerURL: "https://cap.example.com/mcp",
			Headers: map[string]string{
				"Authorization": "Bearer $secret:planted-cap-token",
				"X-Api-Key":     planted("capability server literal header"),
			},
		}
		return nil
	}); err != nil {
		t.Fatalf("planting a capability server: %v", err)
	}
}
