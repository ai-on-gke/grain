package ui_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/bwsalmon/grain/pkg/gitproxy"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

// A team admin who is not a system admin saves each auth type -- PAT,
// SSH key (with and without a paired API token), GitHub App and None --
// for the team and for one repository, and nothing secret ever comes
// back: not in the save's answer, not in the team, not in the status.
func TestTeamAdminSavesEveryCredentialType(t *testing.T) {
	client, store, _ := testClient(t)
	credentials, err := gitproxy.LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	client.Config.Credentials = credentials
	srv := ui.NewServerWithClient(client)
	ctx := context.Background()
	if err := store.PutConfig(ctx, model.Config{PollInterval: model.DefaultPollInterval}); err != nil {
		t.Fatal(err)
	}
	admitUser(t, store, "dana")
	dana := map[string]string{"Content-Type": "application/json", "X-Grain-User": "dana"}
	if rec := doWithHeaders(t, srv, http.MethodPut, "/api/deployment",
		`{"targetRepos":["acme/widgets"]}`, dana); rec.Code != http.StatusOK {
		t.Fatalf("targeting acme/widgets: %d %s", rec.Code, rec.Body)
	}

	_, edPriv, _ := ed25519.GenerateKey(rand.Reader)
	block, _ := ssh.MarshalPrivateKey(edPriv, "")
	sshKey := string(pem.EncodeToMemory(block))
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	appKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey)}))
	_, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	hostSigner, _ := ssh.NewSignerFromKey(hostPriv)
	knownHosts := "git.example.com " + string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey()))

	secrets := []string{"ghp_typed_pat_secret", "OPENSSH PRIVATE KEY", "ghp_paired_api_secret", "RSA PRIVATE KEY", "s3cret-pass"}
	mustNotEcho := func(t *testing.T, where, body string) {
		t.Helper()
		for _, s := range secrets {
			if strings.Contains(body, s) {
				t.Errorf("%s echoed %q back:\n%s", where, s, body)
			}
		}
	}
	marshal := func(v any) string {
		b, _ := json.Marshal(v)
		return string(b)
	}

	// Every type is a git connection now, for the team as a whole or
	// scoped to one repository (grain/task-3929).
	const base = "/api/deployment/git-connections"
	for _, scope := range []string{"", "acme/widgets"} {
		for _, tc := range []struct {
			name, body, wantType string
			check                func(t *testing.T, st ui.GitCredentialStatus)
		}{
			{"pat", marshal(map[string]any{"type": "pat", "value": "ghp_typed_pat_secret"}), "pat", nil},
			{"ssh on github, git-only", marshal(map[string]any{"type": "ssh", "ssh": map[string]any{"privateKey": sshKey}}), "ssh",
				func(t *testing.T, st ui.GitCredentialStatus) {
					if st.Host != "github.com" || !strings.HasPrefix(st.Fingerprint, "SHA256:") || st.APIPaired || st.GitOnly == "" {
						t.Errorf("status = %+v, want github.com, a fingerprint and git-only", st)
					}
				}},
			{"ssh on github, paired", marshal(map[string]any{"type": "ssh", "ssh": map[string]any{"privateKey": sshKey},
				"apiToken": "ghp_paired_api_secret"}), "ssh",
				func(t *testing.T, st ui.GitCredentialStatus) {
					if !st.APIPaired || st.GitOnly != "" {
						t.Errorf("status = %+v, want the API token paired", st)
					}
				}},
			{"ssh on a git host", marshal(map[string]any{"type": "ssh", "host": "git.example.com", "kind": "git",
				"ssh": map[string]any{"privateKey": sshKey, "knownHosts": knownHosts, "passphrase": ""}}), "ssh",
				func(t *testing.T, st ui.GitCredentialStatus) {
					if st.Host != "git.example.com" || st.Kind != "git" || st.GitOnly != "" {
						t.Errorf("status = %+v", st)
					}
				}},
			{"app", marshal(map[string]any{"type": "app", "app": map[string]any{"appId": "1", "installationId": "2", "privateKey": appKey}}), "app", nil},
		} {
			t.Run(scope+" "+tc.name, func(t *testing.T) {
				var body map[string]any
				if err := json.Unmarshal([]byte(tc.body), &body); err != nil {
					t.Fatal(err)
				}
				if body["host"] == nil {
					body["host"] = "github.com"
				}
				body["scope"] = scope
				rec := doWithHeaders(t, srv, http.MethodPost, base, marshal(body), dana)
				if rec.Code != http.StatusOK {
					t.Fatalf("POST %s: %d %s", base, rec.Code, rec.Body)
				}
				mustNotEcho(t, "the save", rec.Body.String())
				var team ui.Team
				if err := json.Unmarshal(rec.Body.Bytes(), &team); err != nil {
					t.Fatal(err)
				}
				added := team.GitConnections[len(team.GitConnections)-1].Credential
				rec = doWithHeaders(t, srv, http.MethodGet, base, "", dana)
				if rec.Code != http.StatusOK {
					t.Fatalf("GET %s: %d %s", base, rec.Code, rec.Body)
				}
				mustNotEcho(t, "the status", rec.Body.String())
				var views []ui.TeamGitConnectionView
				if err := json.Unmarshal(rec.Body.Bytes(), &views); err != nil {
					t.Fatal(err)
				}
				var st ui.GitCredentialStatus
				for _, v := range views {
					if v.Credential == added {
						st = v.Status
					}
				}
				if !st.Configured || st.Type != tc.wantType {
					t.Fatalf("status = %+v, want %s configured", st, tc.wantType)
				}
				if tc.check != nil {
					tc.check(t, st)
				}
				rec = doWithHeaders(t, srv, http.MethodGet, "/api/deployment", "", dana)
				mustNotEcho(t, "the team", rec.Body.String())
			})
		}
	}

	// "None" is the absence of a connection, not one.
	if rec := doWithHeaders(t, srv, http.MethodPost, base, `{"type":"none","host":"github.com"}`, dana); rec.Code != http.StatusBadRequest {
		t.Errorf("a none connection: %d %s", rec.Code, rec.Body)
	}

	// An SSH key for a host whose key is not pinned is refused, with
	// nothing written.
	rec := doWithHeaders(t, srv, http.MethodPost, base,
		marshal(map[string]any{"type": "ssh", "host": "git.other.example", "kind": "git", "ssh": map[string]any{"privateKey": sshKey}}), dana)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "never trusts a host key on first use") {
		t.Errorf("SSH key with no pinned host key: %d %s", rec.Code, rec.Body)
	}
}

// "None (public)" on a deployment that holds other credentials saves the
// selector "anonymous", which no credential file carries: UpdateTeam has
// to accept it rather than answer 400 "not a GitHub credential"
// (grain/task-3185). The team then reads hasGitCredential=false -- it
// reaches public repositories and pushes nothing -- so the "has no GitHub
// credential" warning still shows. A name that is neither a credential nor
// "anonymous" is still refused.
func TestTeamAdminChoosesNoGitCredential(t *testing.T) {
	client, store, _ := testClient(t)
	credentials, err := gitproxy.LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := credentials.SetPAT("default", "ghp_deployment"); err != nil {
		t.Fatal(err)
	}
	client.Config.Credentials = credentials
	srv := ui.NewServerWithClient(client)
	ctx := context.Background()
	if err := store.PutConfig(ctx, model.Config{PollInterval: model.DefaultPollInterval}); err != nil {
		t.Fatal(err)
	}
	admitUser(t, store, "dana")
	dana := map[string]string{"Content-Type": "application/json", "X-Grain-User": "dana"}
	// The selector a team set to "None" before connections existed, and
	// which its migration keeps as a connection.
	team, err := client.UpdateTeam(ctx, model.DefaultTeamID, ui.UpdateTeamRequest{GitCredential: strPtr("anonymous")})
	if err != nil {
		t.Fatalf("UpdateTeam(anonymous): %v", err)
	}
	if team.GitCredential != "anonymous" || team.HasGitCredential {
		t.Errorf("team = gitCredential %q, hasGitCredential %v; want anonymous, false", team.GitCredential, team.HasGitCredential)
	}

	// Adding a token counts again.
	rec := doWithHeaders(t, srv, http.MethodPost, "/api/deployment/git-connections", `{"type":"pat","host":"github.com","value":"ghp_team"}`, dana)
	if err := json.Unmarshal(rec.Body.Bytes(), &team); rec.Code != http.StatusOK || err != nil || !team.HasGitCredential {
		t.Errorf("POST pat: %d %s", rec.Code, rec.Body)
	}

	if _, err := client.UpdateTeam(actingAs(ctx, "root"), model.DefaultTeamID, ui.UpdateTeamRequest{GitCredential: strPtr("no-such-credential"), User: "root"}); err == nil ||
		!strings.Contains(err.Error(), "is not a GitHub credential") {
		t.Errorf("UpdateTeam(no-such-credential) = %v, want refused", err)
	}
}
