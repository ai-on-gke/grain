package ui_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// On a plain git host (kind git) an ambient connection asks git's own
// credential helper, records the host's kind and base URL, and names the
// helper's username as who it acts as -- there is no GitHub /user there.
func TestTeamAmbientGitConnectionOnAPlainGitHost(t *testing.T) {
	client, store, _ := testClient(t)
	credentials, err := gitproxy.LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	credentials.AppTransport = github.NewFakeTransport(github.ApiResponse{Status: 500})
	client.Config.Credentials = credentials
	srv := ui.NewServerWithClient(client)
	ctx := context.Background()
	t.Setenv("GRAIN_IN_CONTAINER", "0")

	bin := t.TempDir()
	git := "#!/bin/sh\n[ \"$1 $2\" = \"credential fill\" ] || exit 2\n" +
		"while read -r line; do [ -z \"$line\" ] && break; done\n" +
		"echo username=bsalmon\necho password=depot-secret\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(git), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	if err := store.PutConfig(ctx, model.Config{PollInterval: model.DefaultPollInterval}); err != nil {
		t.Fatal(err)
	}
	root := map[string]string{"Content-Type": "application/json"}

	rec := doWithHeaders(t, srv, http.MethodPost, "/api/deployment/git-connections",
		`{"type":"ambient","host":"git.internal.example","kind":"git","source":"gh"}`, root)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "GitHub logins only") {
		t.Fatalf("gh on a plain git host: %d %s", rec.Code, rec.Body)
	}

	rec = doWithHeaders(t, srv, http.MethodPost, "/api/deployment/git-connections",
		`{"type":"ambient","host":"git.internal.example","kind":"git","baseUrl":"https://git.internal.example","source":"git"}`, root)
	if rec.Code != http.StatusOK {
		t.Fatalf("ambient connection on a plain git host: %d %s", rec.Code, rec.Body)
	}
	var team ui.Team
	if err := json.Unmarshal(rec.Body.Bytes(), &team); err != nil {
		t.Fatal(err)
	}
	if len(team.GitConnections) != 1 || team.GitConnections[0].Host != "git.internal.example" {
		t.Fatalf("team after adding a plain git connection: %+v", team)
	}
	name := team.GitConnections[0].Credential
	if h := credentials.HostOf(name); h.Kind != model.GitHostGit || h.BaseURL == "" {
		t.Fatalf("host record = %+v, want kind git with its base URL", h)
	}

	rec = doWithHeaders(t, srv, http.MethodGet, "/api/deployment/git-connections", "", root)
	var views []ui.TeamGitConnectionView
	if err := json.Unmarshal(rec.Body.Bytes(), &views); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	if len(views) != 1 || views[0].Status.AmbientLogin != "bsalmon" || views[0].Status.Kind != "git" || views[0].Status.Error != "" {
		t.Fatalf("plain git connection view = %+v", views)
	}
	if strings.Contains(rec.Body.String(), "depot-secret") {
		t.Fatal("the connections listing carries the helper's password")
	}
}

// Saving an ambient connection on a host whose gh never answers -- a
// credential helper that hangs, holding its output open through a child
// of its own -- returns at once, and the connections list answers within
// its short wait with the login pending rather than sitting on the tool
// (gitproxy bounds the tool itself; this is the pane not waiting for it).
func TestTeamAmbientGitConnectionSaveDoesNotWaitOnAHungHelper(t *testing.T) {
	client, store, _ := testClient(t)
	credentials, err := gitproxy.LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	client.Config.Credentials = credentials
	srv := ui.NewServerWithClient(client)
	ctx := context.Background()

	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not installed")
	}
	bin := t.TempDir()
	script := "#!/bin/sh\n" + sleep + " 20 &\n" + sleep + " 20\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("GRAIN_IN_CONTAINER", "0")

	if err := store.PutConfig(ctx, model.Config{PollInterval: model.DefaultPollInterval}); err != nil {
		t.Fatal(err)
	}
	root := map[string]string{"Content-Type": "application/json"}

	start := time.Now()
	rec := doWithHeaders(t, srv, http.MethodPost, "/api/deployment/git-connections",
		`{"type":"ambient","host":"github.com","source":"gh"}`, root)
	if rec.Code != http.StatusOK {
		t.Fatalf("saving an ambient connection: %d %s", rec.Code, rec.Body)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("saving an ambient connection took %s while gh hung", took)
	}

	start = time.Now()
	rec = doWithHeaders(t, srv, http.MethodGet, "/api/deployment/git-connections", "", root)
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("listing connections took %s while gh hung", took)
	}
	var views []ui.TeamGitConnectionView
	if err := json.Unmarshal(rec.Body.Bytes(), &views); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	if len(views) != 1 || !views[0].Status.AmbientPending || views[0].Status.AmbientLogin != "" {
		t.Fatalf("ambient connection view while gh hangs = %+v, want the login pending", views)
	}
}
