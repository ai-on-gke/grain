// controller.go is "grain controller ...": one-shot setup verbs an
// operator runs by hand against their own machine, never against a
// running daemon -- unlike every task-management verb in main.go, these
// need no store connection at all. bootstrap-github-app is the first
// and, for now, only one: see pkg/capability/githubsandbox's own doc
// comment for why a GitHub App -- not the "username and password"
// bwsalmon/agents#354 first asked for -- is what that capability's two
// secrets hold, and why this command exists to make registering one a
// single click rather than the account's actual password ever reaching
// grain.
package cli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"

	"github.com/pkg/browser"

	"github.com/ai-on-gke/grain/pkg/capability/githubsandbox"
)

// manifestPermissions is what GitHub Apps registered through this
// command ask for -- across the github-sandbox capability (app.go's own
// createRepoPermissions, agentPermissions, and deleteRepoPermissions)
// and default git credentials (which need checks: write to read CI status
// and post check runs without degrading auto-merge).
var manifestPermissions = map[string]string{
	"contents":                    "write",
	"issues":                      "write",
	"pull_requests":               "write",
	"secrets":                     "write",
	"packages":                    "write",
	"checks":                      "write",
	"administration":              "write", // repo-level: only Revoke/Reap's controller-side delete token ever carries this
	"organization_administration": "write", // org-level: only the controller-side create/list token ever carries this
}

// botManifestPermissions is the least-privilege permission set requested
// when registering a standalone GitHub App bot credential (-git-credential)
// rather than a github-sandbox repository-creation App.
var botManifestPermissions = map[string]string{
	"contents":      "write",
	"issues":        "write",
	"pull_requests": "write",
	"checks":        "write",
	"statuses":      "read",
	// actions: read is what a workflow_job delivery needs, and what
	// reading a failed job's log for GitHub CI's failure tails needs.
	"actions":  "read",
	"metadata": "read",
}

// botWebhookEvents are the webhook events a standalone GitHub App bot
// subscribes to when -webhook-url is supplied.
var botWebhookEvents = []string{
	"check_run",
	"check_suite",
	"issue_comment",
	"issues",
	"pull_request",
	"pull_request_review",
	"pull_request_review_comment",
	"push",
	"status",
	"workflow_job",
}

// callbackTimeout bounds how long bootstrapGitHubApp waits for the
// manifest flow's browser redirect to reach the local callback server
// before giving up -- long enough for a human to notice the opened tab,
// log in if needed, and click through GitHub's own "Create GitHub App"
// confirmation, short enough that an operator who closed the tab isn't
// left wondering why the command is still running an hour later.
const callbackTimeout = 5 * time.Minute

func controller(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: grain controller bootstrap-github-app [flags]")
		os.Exit(1)
	}
	switch args[0] {
	case "bootstrap-github-app":
		if err := bootstrapGitHubApp(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "grain controller bootstrap-github-app: "+err.Error())
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "grain controller: unknown command %q\n", args[0])
		os.Exit(1)
	}
}

// bootstrapGitHubApp drives GitHub's own App-manifest flow
// (https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest):
// it opens a browser tab that POSTs a manifest -- name and
// manifestPermissions, nothing else -- to github.com, waits for the
// redirect that flow ends with (caught by a local, loopback-only HTTP
// server, never grain's own daemon), and exchanges the resulting
// one-time code for the App's id and private key over the real GitHub
// API. Nothing here ever sees, asks for, or transmits the bot account's
// password -- the operator's only action is logging into that account
// in their own browser (if not already) and clicking the one button
// GitHub itself renders.
//
// The App this produces is not yet installed on any account -- creating
// an App and installing it are two separate GitHub actions, and the
// manifest flow only ever does the first. bootstrapGitHubApp prints the
// App's own settings URL, where "Install App" is one more click, as its
// last step.
func bootstrapGitHubApp(args []string) error {
	fs := flag.NewFlagSet("grain controller bootstrap-github-app", flag.ExitOnError)
	name := fs.String("name", "grain-sandbox", "name for the new GitHub App")
	homepage := fs.String("url", "https://github.com/ai-on-gke/grain", "homepage URL the App manifest requires")
	dataDir := fs.String("data-dir", "", "root directory a colocated `grain daemon` was started with -- the App's credentials are written into <data-dir>/secrets, the same store `grain daemon` and `grain secrets` read and write (required)")
	githubHost := fs.String("github-host", "github.com", "GitHub host to run the manifest flow against")
	org := fs.String("org", "", "GitHub organization login to register the App under (empty registers under the signed-in account)")
	webhookURL := fs.String("webhook-url", "", "public HTTPS URL where GitHub delivers webhook events (e.g. https://grain.example.com/webhooks/github)")
	gitCredential := fs.String("git-credential", "", "also write <data-dir>/secrets/github/<name>.app.json as a standalone GitHub App bot git credential (uses least-privilege bot permissions unless -sandbox-permissions is set)")
	installationID := fs.String("installation-id", "", "optional installation ID to pin in <git-credential>.app.json (empty resolves per repository automatically)")
	sandboxPermissions := fs.Bool("sandbox-permissions", false, "include github-sandbox repository-creation permissions even when -git-credential is set")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dataDir == "" {
		return fmt.Errorf("-data-dir is required")
	}
	if *gitCredential != "" {
		if err := gitproxy.ValidCredentialName(*gitCredential); err != nil {
			return err
		}
	}

	state, err := randomHex(16)
	if err != nil {
		return fmt.Errorf("generating a state token: %w", err)
	}

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("state"); got != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			errCh <- fmt.Errorf("callback state %q did not match the request grain made", got)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			errCh <- fmt.Errorf("callback carried no code")
			return
		}
		fmt.Fprintln(w, "GitHub App created. You can close this tab and return to grain.")
		codeCh <- code
	})
	listener, err := newLoopbackListener()
	if err != nil {
		return fmt.Errorf("starting a local callback server: %w", err)
	}
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	defer server.Close()
	redirectURL := fmt.Sprintf("http://%s/callback", listener.Addr().String())

	manifest := buildAppManifest(*name, *homepage, redirectURL, *webhookURL, *gitCredential != "" && !*sandboxPermissions)
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("building the App manifest: %w", err)
	}

	submitURL := manifestSubmitURL(*githubHost, *org, state)
	form := manifestSubmissionForm(submitURL, string(manifestJSON))
	fmt.Printf(
		"Opening a browser to register a new GitHub App named %q. Log in as the bot\n"+
			"account this capability should act as, if you are not already, and click\n"+
			"\"Create GitHub App\" -- grain never sees that account's password.\n\n"+
			"If a browser did not open, POST the manifest below to %s yourself.\n\n",
		*name, submitURL,
	)
	if err := browser.OpenReader(strings.NewReader(form)); err != nil {
		fmt.Printf("(could not open a browser automatically: %v)\n\nmanifest:\n%s\n", err, manifestJSON)
	}

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return err
	case <-time.After(callbackTimeout):
		return fmt.Errorf("timed out after %s waiting for GitHub's redirect -- rerun this command", callbackTimeout)
	}

	converted, err := convertManifestFull(*githubHost, code)
	if err != nil {
		return fmt.Errorf("exchanging the manifest code: %w", err)
	}

	if *gitCredential == "" || *sandboxPermissions {
		if err := writeAppCredentials(*dataDir, converted.AppID, converted.PEM); err != nil {
			return err
		}
	}
	if *gitCredential != "" {
		if err := writeBotGitCredential(*dataDir, *gitCredential, *githubHost, converted.AppID, *installationID, converted.PEM, converted.WebhookSecret); err != nil {
			return err
		}
		fmt.Printf(
			"\nWrote standalone GitHub App bot credential %q to %s (bot account: %s[bot]).\n",
			*gitCredential, filepath.Join(daemon.SecretsDir(*dataDir), *gitCredential+".app.json"), converted.Slug,
		)
		if converted.WebhookSecret != "" {
			fmt.Printf("Stored webhook_secret in the App credential (set GITHUB_WEBHOOK_SECRET on github-webhook to match).\n")
		}
		fmt.Printf(
			"\nOne step left: visit %s, choose \"Install App\", and install it on the\n"+
				"organization or repositories this grain instance should serve.\n",
			converted.HTMLURL,
		)
		return nil
	}

	fmt.Printf(
		"\nWrote the %s and %s secrets to %s --\n"+
			"the github-sandbox capability's own credentials, ready for a `grain daemon`\n"+
			"(or `grain secrets list`) started against the same -data-dir to pick up.\n\n"+
			"One step left: the App exists but is not installed anywhere yet. Visit\n"+
			"%s, choose \"Install App\", and install it on the same bot\n"+
			"account -- \"All repositories\" (there is nothing to select yet, since this\n"+
			"capability creates repos on demand). Nothing dispatches against\n"+
			"github-sandbox until that install completes.\n",
		githubsandbox.DefaultAppIDCredential, githubsandbox.DefaultPrivateKeyCredential, filepath.Join(*dataDir, "secrets"), converted.HTMLURL,
	)
	return nil
}

func buildAppManifest(name, homepage, redirectURL, webhookURL string, botOnly bool) map[string]any {
	perms := manifestPermissions
	if botOnly {
		perms = botManifestPermissions
	}
	events := []string{}
	if strings.TrimSpace(webhookURL) != "" {
		events = botWebhookEvents
	}
	m := map[string]any{
		"name":                name,
		"url":                 homepage,
		"public":              false,
		"default_events":      events,
		"default_permissions": perms,
		"redirect_url":        redirectURL,
	}
	if u := strings.TrimSpace(webhookURL); u != "" {
		m["hook_attributes"] = map[string]any{
			"url":    u,
			"active": true,
		}
	}
	return m
}

func manifestSubmitURL(githubHost, org, state string) string {
	org = strings.TrimSpace(org)
	if org != "" {
		return fmt.Sprintf("https://%s/organizations/%s/settings/apps/new?state=%s", githubHost, org, state)
	}
	return fmt.Sprintf("https://%s/settings/apps/new?state=%s", githubHost, state)
}

func writeBotGitCredential(dataDir, credName, githubHost, appID, installationID, privateKey, webhookSecret string) error {
	creds, err := gitproxy.LoadCredentialSet(daemon.SecretsDir(dataDir))
	if err != nil {
		return err
	}
	if err := creds.SetAppWithWebhookSecret(credName, appID, installationID, privateKey, webhookSecret); err != nil {
		return err
	}
	host := strings.TrimSpace(githubHost)
	if host != "" && host != model.DefaultGitHost {
		if err := creds.SetHost(credName, gitproxy.CredentialHost{Host: host, Kind: model.GitHostGitHub}); err != nil {
			return err
		}
	}
	return nil
}

// writeAppCredentials stores appID and privateKey in the same
// pkg/secrets file (secretsConfig's own two paths) that
// githubsandbox.Provider.Resolve -- and `grain secrets` -- read from,
// under the one "github-app" secret's "app-id" and "private-key" keys:
// exactly the pair DefaultAppIDCredential and DefaultPrivateKeyCredential
// name. Earlier, this command wrote plain files under <secrets-dir>/
// github-app/ instead, which nothing that resolves credentials through
// pkg/secrets.Store (one encrypted file, not a directory of files) ever
// read -- an operator had to separately run `grain secrets set` by hand
// for bootstrap-github-app's output to take effect at all.
func writeAppCredentials(dataDir, appID, privateKey string) error {
	store := daemon.OpenSecrets(dataDir)
	if err := store.Set("github-app", "app-id", []byte(appID)); err != nil {
		return fmt.Errorf("writing app-id: %w", err)
	}
	if err := store.Set("github-app", "private-key", []byte(privateKey)); err != nil {
		return fmt.Errorf("writing private-key: %w", err)
	}
	return nil
}

type convertedAppManifest struct {
	AppID         string
	Slug          string
	PEM           string
	WebhookSecret string
	HTMLURL       string
}

// convertManifest exchanges a one-time manifest code for the new App's
// credentials -- the one real API call in the whole flow
// (docs.github.com's own "app-manifests/{code}/conversions"),
// unauthenticated beyond the code itself, which only GitHub's redirect
// (never anything grain constructs) can produce.
func convertManifest(host, code string) (appID, privateKey, htmlURL string, err error) {
	c, err := convertManifestFull(host, code)
	if err != nil {
		return "", "", "", err
	}
	return c.AppID, c.PEM, c.HTMLURL, nil
}

func convertManifestFull(host, code string) (convertedAppManifest, error) {
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("https://%s/app-manifests/%s/conversions", apiHost(host), code), nil)
	if err != nil {
		return convertedAppManifest{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return convertedAppManifest{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return convertedAppManifest{}, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return convertedAppManifest{}, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, body)
	}
	var out struct {
		ID            int64  `json:"id"`
		Slug          string `json:"slug"`
		PEM           string `json:"pem"`
		WebhookSecret string `json:"webhook_secret"`
		HTMLURL       string `json:"html_url"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return convertedAppManifest{}, fmt.Errorf("parsing the conversion response: %w", err)
	}
	if out.ID == 0 || out.PEM == "" {
		return convertedAppManifest{}, fmt.Errorf("conversion response carried no id/pem: %s", body)
	}
	return convertedAppManifest{
		AppID:         fmt.Sprintf("%d", out.ID),
		Slug:          out.Slug,
		PEM:           out.PEM,
		WebhookSecret: out.WebhookSecret,
		HTMLURL:       out.HTMLURL,
	}, nil
}

// apiHost turns "github.com" into "api.github.com" -- the one host
// whose REST API lives on a different hostname than its web UI -- and
// leaves anything else (a local mock server standing in for GitHub,
// -github-host's own "override to point at a mock for local testing")
// unchanged, matching how pkg/github.RealTransport already treats that
// flag: one hostname serving every path uniformly.
func apiHost(host string) string {
	if host == "github.com" {
		return "api.github.com"
	}
	return host
}

// manifestSubmissionForm is a minimal HTML page that POSTs manifestJSON
// to submitURL the instant it loads -- the only way GitHub's manifest
// flow accepts a manifest (docs.github.com: "a form... with a manifest
// parameter"), so a real form submission, not a GET, is unavoidable.
func manifestSubmissionForm(submitURL, manifestJSON string) string {
	escaped := strings.NewReplacer(`&`, "&amp;", `<`, "&lt;", `>`, "&gt;", `"`, "&quot;").Replace(manifestJSON)
	return fmt.Sprintf(`<!DOCTYPE html>
<html><body onload="document.forms[0].submit()">
Redirecting to GitHub to create the App&hellip;
<form action=%q method="post">
<input type="hidden" name="manifest" value="%s">
</form>
</body></html>`, submitURL, escaped)
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// newLoopbackListener binds 127.0.0.1:0 -- loopback only, the same
// "never reachable from outside this machine" bound
// pkg/cli/daemon.go's own startGitProxy holds its callback server to,
// since this one briefly carries a live manifest-conversion code.
func newLoopbackListener() (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}
