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
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/pkg/browser"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// The deployment's own connections and keys. Single-tenant grain has one
// owner, the deployment, so these name no team: they call the
// deployment's routes (/api/deployment), which keep what they configure
// on the default team's row until the schema reset moves it to the
// settings row (docs/single-tenant-reset.md, stages 1 and 6).

// cmdGitAmbient adds an ambient git connection to the deployment: the
// installer's way of using the operator's own `gh` login instead of
// requiring GRAIN_GITHUB_TOKEN (google/scripts/cloudtop-install.sh).
func cmdGitAmbient(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain git-ambient", flag.ContinueOnError)
	host := fs.String("host", model.DefaultGitHost, "git host the login is for")
	kind := fs.String("kind", "github", "the host's kind: github (a forge API) or git (clone, fetch and push only)")
	baseURL := fs.String("base-url", "", "the host's scheme://host[:port], when that is not https on the default port")
	proxy := fs.String("proxy", "", "the http(s)://host:port proxy the host is reached through, if any "+
		"(read from the source's own tool when empty, for a registered source that has one)")
	source := fs.String("source", "auto", "which of this host's tools to ask: "+strings.Join(gitproxy.AmbientSourceNames(), ", ")+
		" (auto: a registered source on its own hosts; gh, then git, elsewhere)")
	ifNone := fs.Bool("if-none", false, "do nothing when the deployment already has a git connection on that host")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	if *ifNone {
		existing, err := c.GetDeployment(ctx)
		if err != nil {
			return err
		}
		if conn, ok := gitConnectionOn(existing, *host); ok {
			if out.json {
				out.encode(existing)
				return nil
			}
			fmt.Printf("This deployment already has a git connection on %s (%s): leaving it as it is.\n", *host, conn)
			return nil
		}
	}
	updated, err := c.AddDeploymentGitConnection(ctx, ui.AddTeamGitConnectionRequest{
		Type: "ambient", Host: *host, Kind: *kind, BaseURL: *baseURL, Proxy: *proxy, Source: *source,
	})
	if err != nil {
		return err
	}
	if out.json {
		out.encode(updated)
		return nil
	}
	if *kind == string(model.GitHostGit) {
		fmt.Printf("This deployment now clones and pushes to %s as this host's own git credential helper login (no pull requests: a plain git host).\n",
			*host)
		return nil
	}
	fmt.Printf("This deployment now clones, pushes and calls GitHub as this host's own %s login on %s.\n",
		gitproxy.AmbientCommandOf(*host, *source), *host)
	return nil
}

// gitConnectionOn names the credential the deployment (team, the row
// that holds its connections) already clones and pushes with on host, if it has one: on the default host that includes the
// legacy single credential (HasGitCredential).
func gitConnectionOn(team ui.Team, host string) (string, bool) {
	for _, conn := range team.GitConnections {
		if model.SameGitHost(conn.Host, host, model.DefaultGitHost) {
			return conn.Credential, true
		}
	}
	if model.SameGitHost(host, "", model.DefaultGitHost) && team.HasGitCredential {
		return strings.TrimSpace(team.GitCredential), true
	}
	return "", false
}

// cmdGitConnections lists what the deployment already clones and
// pushes with, so an installer can tell "this host's login failed its
// check" apart from "this deployment has no git at all".
func cmdGitConnections(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain git-connections", flag.ContinueOnError)
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if err := noArgs(fs, rest); err != nil {
		return err
	}
	existing, err := c.GetDeployment(ctx)
	if err != nil {
		return err
	}
	conns := append([]model.TeamGitConnection{}, existing.GitConnections...)
	if _, ok := gitConnectionOn(ui.Team{GitConnections: conns}, model.DefaultGitHost); !ok && existing.HasGitCredential {
		conns = append(conns, model.TeamGitConnection{Credential: strings.TrimSpace(existing.GitCredential), Host: model.DefaultGitHost})
	}
	if out.json {
		out.encode(conns)
		return nil
	}
	for _, conn := range conns {
		host := conn.Host
		if host == "" {
			host = model.DefaultGitHost
		}
		fmt.Printf("%s %s\n", host, conn.Credential)
	}
	return nil
}

const githubClientIDEnvVar = "GRAIN_GITHUB_CLIENT_ID"

var (
	gitLoginStdin       io.Reader = os.Stdin
	gitLoginOpenBrowser           = browser.OpenURL
	gitLoginHTTPClient  *http.Client
	gitLoginGHToken     = defaultGitLoginGHToken
)

func defaultGitLoginGHToken(ctx context.Context, host string) (string, error) {
	if strings.TrimSpace(host) == "" {
		host = model.DefaultGitHost
	}
	cmd := exec.CommandContext(ctx, "gh", "auth", "token", "--hostname", host)
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return "", fmt.Errorf("gh auth token --hostname %s: %s", host, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("gh auth token --hostname %s: %w", host, err)
	}
	tok := strings.TrimSpace(string(out))
	if tok == "" {
		return "", fmt.Errorf("gh auth token --hostname %s returned an empty token", host)
	}
	return tok, nil
}

func defaultOAuthClientID() string {
	if v := strings.TrimSpace(os.Getenv(githubClientIDEnvVar)); v != "" {
		return v
	}
	return github.DefaultOAuthClientID
}

// cmdGitLogin captures a delegated GitHub HTTPS token (via browser OAuth
// Device Flow, local `gh auth token`, or stdin) and registers it on the
// deployment with SignAs enabled so new pull requests and comments are
// signed with the user's name ("Signed by <Name> (via Grain)").
func cmdGitLogin(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain git-login", flag.ContinueOnError)
	host := fs.String("host", model.DefaultGitHost, "git host to authenticate against")
	baseURL := fs.String("base-url", "", "the host's scheme://host[:port], when that is not https on the default port")
	proxy := fs.String("proxy", "", "the http(s)://host:port proxy the host is reached through, if any")
	scope := fs.String("scope", "", "optional owner or owner/repo scope (empty sets the deployment-wide default on -host)")
	author := fs.String("author", "", "explicit name to sign pull requests and comments with (defaults to your GitHub profile name or login)")
	web := fs.Bool("web", false, "authenticate in a web browser using GitHub OAuth Device Flow")
	fromGH := fs.Bool("from-gh", false, "delegate the token from this machine's `gh auth token` login")
	withToken := fs.Bool("with-token", false, "read the HTTPS token from standard input")
	clientID := fs.String("client-id", defaultOAuthClientID(), "GitHub OAuth Client ID for -web device flow ($"+githubClientIDEnvVar+")")
	scopes := fs.String("scopes", strings.Join(github.DefaultOAuthScopes, ","), "comma-separated OAuth scopes requested by -web")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}

	modes := 0
	if *web {
		modes++
	}
	if *fromGH {
		modes++
	}
	if *withToken {
		modes++
	}
	if modes > 1 {
		return errors.New("choose at most one of -web, -from-gh, or -with-token")
	}

	var (
		token string
		err   error
	)
	switch {
	case *withToken:
		raw, rerr := io.ReadAll(gitLoginStdin)
		if rerr != nil {
			return fmt.Errorf("reading token from stdin: %w", rerr)
		}
		token = strings.TrimSpace(string(raw))
		if token == "" {
			return errors.New("no token received on standard input")
		}
	case *fromGH:
		token, err = gitLoginGHToken(ctx, *host)
		if err != nil {
			return err
		}
	case *web:
		token, err = runGitLoginDeviceFlow(ctx, *host, *baseURL, *clientID, *scopes)
		if err != nil {
			return err
		}
	default:
		// Auto mode: prefer an active local `gh` login when available,
		// otherwise run the interactive browser OAuth Device Flow.
		if ghTok, ghErr := gitLoginGHToken(ctx, *host); ghErr == nil && ghTok != "" {
			token = ghTok
		} else {
			token, err = runGitLoginDeviceFlow(ctx, *host, *baseURL, *clientID, *scopes)
			if err != nil {
				return err
			}
		}
	}

	updated, err := c.AddDeploymentGitConnection(ctx, ui.AddTeamGitConnectionRequest{
		Type:    "pat",
		Host:    *host,
		Kind:    "github",
		BaseURL: *baseURL,
		Proxy:   *proxy,
		Scope:   *scope,
		Value:   token,
		Author:  strings.TrimSpace(*author),
		SignAs:  true,
		Replace: true,
	})
	if err != nil {
		return err
	}
	if out.json {
		out.encode(updated)
		return nil
	}

	resolvedAuthor, commitName := strings.TrimSpace(*author), ""
	if resolvedAuthor == "" {
		if conns, cerr := c.ListDeploymentGitConnections(ctx); cerr == nil {
			resolvedAuthor, commitName = gitLoginSignature(conns, *host, *scope)
		}
	}
	fmt.Println(gitLoginSummary(*host, resolvedAuthor, commitName))
	return nil
}

// gitLoginSignature is who the deployment now signs pull requests and
// comments as for the connection on host/scope -- the recorded Author
// (gitproxy.CredentialSet.AuthorOf, the same field the GitHub client signs
// with) -- and, apart from it, who its commits are authored by. The commit
// identity is never the signature: it can be this host's git user.name,
// and printing it as one told an operator a bot-owned token's pull
// requests would be signed by them when they are not signed at all.
func gitLoginSignature(conns []ui.TeamGitConnectionView, host, scope string) (author, commitName string) {
	for _, conn := range conns {
		if model.SameGitHost(conn.Host, host, model.DefaultGitHost) && strings.EqualFold(conn.Scope, strings.TrimSpace(scope)) {
			return conn.Status.Author, conn.Status.CommitName
		}
	}
	return "", ""
}

// gitLoginSummary is the line `grain git-login` ends on.
func gitLoginSummary(host, author, commitName string) string {
	if author != "" {
		return fmt.Sprintf("Authenticated to %s as %s: this deployment now clones, pushes, and signs new pull requests and comments as %q.",
			host, author, github.PullRequestSignatureLine(author))
	}
	line := fmt.Sprintf("Authenticated to %s: this deployment now clones, pushes, and calls GitHub with the delegated token", host)
	if commitName != "" {
		line += fmt.Sprintf(" (commits authored as %s)", commitName)
	}
	return line + "; the host reported no person's name for it, so pull requests and comments are not signed -- pass -author to sign them."
}

func runGitLoginDeviceFlow(ctx context.Context, host, baseURL, clientID, rawScopes string) (string, error) {
	var scopeList []string
	for _, s := range strings.Split(rawScopes, ",") {
		if trimmed := strings.TrimSpace(s); trimmed != "" {
			scopeList = append(scopeList, trimmed)
		}
	}
	webBase := github.DeviceWebBaseURL(host, baseURL)
	dc, err := github.RequestDeviceCode(ctx, gitLoginHTTPClient, webBase, clientID, scopeList)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(os.Stderr, "! First copy your one-time code: %s\n", dc.UserCode)
	fmt.Fprintf(os.Stderr, "- Open %s in your browser to authorize Grain...\n", dc.VerificationURI)
	if err := gitLoginOpenBrowser(dc.VerificationURI); err != nil {
		fmt.Fprintf(os.Stderr, "  (Could not open browser automatically: %v)\n", err)
	}
	return github.PollDeviceToken(ctx, gitLoginHTTPClient, webBase, clientID, dc.DeviceCode, time.Duration(dc.Interval)*time.Second)
}

func cmdDefaultModel(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain default-model", flag.ContinueOnError)
	ifNone := fs.Bool("if-none", false, "do nothing when the deployment already has a default model")
	ifUnserved := fs.Bool("if-unserved", false,
		"do nothing when the deployment already has a default model one of its enabled connections serves")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	models := model.ParseDefaultModels(strings.Join(rest, ","))
	if len(models) == 0 {
		return errors.New("usage: grain default-model [--if-none | --if-unserved] <model> [<model>...]")
	}
	name := model.JoinDefaultModels(models)
	if *ifNone || *ifUnserved {
		existing, err := c.GetDeployment(ctx)
		if err != nil {
			return err
		}
		keep := existing.DefaultModel != ""
		if keep && !*ifNone {
			// A default no enabled connection serves is one every task
			// left on it waits behind forever.
			settings, err := c.GetSettings(ctx)
			if err != nil {
				return err
			}
			keep = servesModel(settings.AgentBackends, existing.DefaultModel)
		}
		if keep {
			if out.json {
				out.encode(existing)
				return nil
			}
			fmt.Printf("This deployment already runs on %s by default: leaving it as it is.\n", existing.DefaultModel)
			return nil
		}
		if existing.DefaultModel != "" && !out.json {
			fmt.Printf("No enabled connection serves the default model %s: replacing it.\n", existing.DefaultModel)
		}
	}
	updated, err := c.UpdateDeployment(ctx, ui.UpdateDeploymentRequest{DefaultModel: &name})
	if err != nil {
		return err
	}
	if out.json {
		out.encode(updated)
		return nil
	}
	fmt.Printf("This deployment now runs on %s by default.\n", updated.DefaultModel)
	return nil
}

// servesModel reports whether an enabled connection of the deployment
// serves name (or any model in a comma-separated default model list).
func servesModel(backends []model.AgentBackend, name string) bool {
	enabled := model.DeploymentAgentBackends(backends, false)
	for _, m := range model.ParseDefaultModels(name) {
		for _, b := range enabled {
			if b.SupportsModel(m) {
				return true
			}
		}
	}
	return false
}

// cmdBroadcast sends a notice to all running task agents (or, with -all,
// to every active task of the deployment). Running agents poll human
// comments on their task conversation via daemonAddenda and receive the
// notice on their next tool call.
func cmdBroadcast(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain broadcast", flag.ContinueOnError)
	all := fs.Bool("all", false, "deliver to queued and awaiting-reply tasks too, not only running task agents")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if name, ok := misplacedFlag(fs, fs.Args()); ok {
		return fmt.Errorf("-%s has to come before the message: grain broadcast -%s <message>", name, name)
	}
	msg := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if msg == "" {
		return errors.New("usage: grain broadcast [-all] <message...>")
	}
	res, err := c.BroadcastDeploymentMessage(ctx, ui.BroadcastTeamRequest{
		Message:     msg,
		RunningOnly: !*all,
	})
	if err != nil {
		return err
	}
	if out.json {
		out.encode(res)
		return nil
	}
	if res.DeliveredCount == 0 {
		if *all {
			fmt.Println("No active tasks to notify.")
		} else {
			fmt.Println("No running task agents to notify.")
		}
		return nil
	}
	ids := make([]string, len(res.TaskIDs))
	for i, id := range res.TaskIDs {
		ids[i] = "#" + id
	}
	fmt.Printf("Sent notice to %d task(s): %s\n", res.DeliveredCount, strings.Join(ids, ", "))
	return nil
}
