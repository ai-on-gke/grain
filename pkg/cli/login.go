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
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/cli/skill"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// `grain login`, `grain logout` and `grain whoami`
// (external-cli.spec.md#XC-8, #XC-9): signing a workstation in to a
// deployment once, as a named context (context.go) whose credential is
// kept out of config.json (credentials.go), and asking who it is signed
// in as.

// loginStdin is what --token-stdin reads; tests replace it.
var loginStdin io.Reader = os.Stdin

// defaultLoginScopeSet is what --issue-token mints when not told
// otherwise: no dangerous scope, so any signed-in person may mint it.
const defaultLoginScopeSet = "grain/external-dev"

func flagWasSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) { set = set || f.Name == name })
	return set
}

// useContextCredential has c send sel's credential to sel's own server
// (#XC-4), and a google-id-token context's audience, and returns what c's
// credential comes from: explicit (tokenSource's), else the context.
func useContextCredential(c *ui.HTTPClient, sel selectedContext, explicit string) string {
	c.ContextToken = contextToken(sel, c.BaseURL, newCredentialStore())
	if c.Audience == "" && sel.ctx != nil && sel.ctx.Audience != "" && sameOrigin(sel.ctx.Server, c.BaseURL) {
		c.Audience = sel.ctx.Audience
	}
	return contextTokenSource(explicit, c, sel)
}

// dispatchSignIn runs the sign-in commands, which need the selected
// context as well as the client dispatchCLI hands every command.
func dispatchSignIn(ctx context.Context, c *ui.HTTPClient, out *printer, sel selectedContext, cmd string, args []string) error {
	switch cmd {
	case "context":
		return cmdContext(out, sel, args)
	case "whoami":
		return cmdWhoami(ctx, c, out, sel, args)
	case "login":
		return cmdLogin(ctx, c, out, sel, args)
	}
	return cmdLogout(ctx, c, out, sel, args)
}

// contextTokenSource is explicit, or the context c's credential comes
// from when it is the one HTTPClient will send.
func contextTokenSource(explicit string, c *ui.HTTPClient, sel selectedContext) string {
	if explicit != "" || c.ContextToken == nil || sel.ctx == nil {
		return explicit
	}
	switch sel.ctx.Auth {
	case "google-id-token", "local", "none", "":
		return ""
	}
	return "context " + sel.name
}

// whoamiView is `grain whoami --json`.
type whoamiView struct {
	Context string `json:"context,omitempty"`
	Server  string `json:"server"`
	ui.AuthWhoamiResponse
}

func cmdWhoami(ctx context.Context, c *ui.HTTPClient, out *printer, sel selectedContext, args []string) error {
	fs := flag.NewFlagSet("whoami", flag.ContinueOnError)
	rest, err := parseArgs(fs, args)
	if err != nil {
		return usageErrorf("%v", err)
	}
	if err := noArgs(fs, rest); err != nil {
		return err
	}
	who, err := c.AuthWhoami(ctx)
	if err != nil {
		return expiredForContext(err, sel.name)
	}
	if !who.Authenticated {
		fix := "grain login " + c.BaseURL
		return &cliError{exit: exitAuth, code: "unauthenticated", fix: fix,
			err: fmt.Errorf("not signed in to %s (it accepts %s): run %s", c.BaseURL, strings.Join(who.AcceptedAuth, ", "), fix)}
	}
	if out.json {
		out.encode(whoamiView{Context: sel.name, Server: c.BaseURL, AuthWhoamiResponse: who})
		return nil
	}
	if sel.name != "" {
		fmt.Printf("context: %s\n", sel.name)
	}
	fmt.Printf("server: %s\nprincipal: %s\n", c.BaseURL, who.Principal)
	if who.Owner != "" {
		fmt.Printf("owner: %s\n", who.Owner)
	}
	if who.ScopeSet != "" {
		fmt.Printf("scope set: %s\n", who.ScopeSet)
	}
	if len(who.ResolvedScopes) > 0 {
		fmt.Printf("scopes: %s\n", strings.Join(who.ResolvedScopes, ", "))
	}
	if len(who.Repos) > 0 {
		fmt.Printf("repos: %s\n", strings.Join(who.Repos, ", "))
	}
	if who.ExpiresAt != nil {
		fmt.Printf("expires: %s\n", who.ExpiresAt.UTC().Format(time.RFC3339))
	}
	return nil
}

// expiredForContext is err, said as #XC-8 says an expired credential.
func expiredForContext(err error, name string) error {
	var expired *ui.TokenExpiredError
	if !errors.As(err, &expired) {
		return err
	}
	if name == "" {
		name = "(none)"
	}
	return &cliError{exit: exitAuth, code: "auth_expired", fix: "grain login --refresh",
		err: fmt.Errorf("token for context %s expired on %s: run grain login --refresh", name, expired.ExpiredAt.UTC().Format("2006-01-02"))}
}

func cmdLogin(ctx context.Context, base *ui.HTTPClient, out *printer, sel selectedContext, args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	name := fs.String("name", "", "the context's name (default: from the server's host name)")
	issue := fs.Bool("issue-token", false, "mint a scoped gma_ token for this workstation and keep it")
	scopeSet := fs.String("scope-set", defaultLoginScopeSet, "the scope set --issue-token mints")
	repos := fs.String("repos", "", "comma-separated repositories --issue-token limits the token to")
	expires := fs.String("expires", "30d", "how long --issue-token's token lasts (30d, 12h, ...)")
	tokenStdin := fs.Bool("token-stdin", false, "read a gma_ token (or, with --auth access-code, the access code) from stdin and store it; not with --issue-token or --refresh")
	refresh := fs.Bool("refresh", false, "re-mint the context's expired or expiring token")
	auth := fs.String("auth", "", "how to sign in: access-code, or a credential source this build registers")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return usageErrorf("%v", err)
	}
	life, err := parseLoginExpiry(*expires)
	if err != nil {
		return err
	}
	cfg, err := loadContexts()
	if err != nil {
		return err
	}

	var server string
	switch {
	case len(rest) == 1:
		server = strings.TrimRight(rest[0], "/")
	case len(rest) == 0 && *refresh:
		if *name == "" {
			*name = sel.name
		}
		c, ok := cfg.Contexts[*name]
		if !ok || *name == "" {
			return unknownContext(*name, "--refresh")
		}
		server = c.Server
	default:
		return usageErrorf("usage: grain login <url> [--name <ctx>] [--issue-token] [--token-stdin] [--refresh]")
	}
	u, err := url.Parse(server)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return usageErrorf("grain login needs the deployment's URL (https://grain.example.com), not %q", server)
	}
	if *tokenStdin && *issue {
		// --token-stdin stores what stdin holds as the credential, so an
		// access code piped in alongside --issue-token would be kept as
		// it is rather than spent on minting a gma_ token.
		return &cliError{exit: exitUsage, code: "token_stdin_issue_token", fix: "grain -token CODE login " + server + " --issue-token",
			err: fmt.Errorf("--token-stdin and --issue-token do not combine: --token-stdin stores the secret it reads; "+
				"to mint a gma_ token with an access code, pass the code with -token: grain -token CODE login %s --issue-token", server)}
	}
	if *tokenStdin && *refresh {
		// --token-stdin replaces the context's credential with what stdin
		// holds; --refresh would have been silently ignored.
		return &cliError{exit: exitUsage, code: "token_stdin_refresh", fix: "grain login --refresh",
			err: fmt.Errorf("--token-stdin and --refresh do not combine: --refresh re-mints the context's own token, " +
				"--token-stdin stores the one stdin holds; pass one of them")}
	}
	if !*issue {
		// --scope-set, --repos and --expires shape the token --issue-token
		// mints; without it (a --refresh rotation keeps the token's own)
		// they would be silently ignored.
		var stray []string
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "scope-set" || f.Name == "repos" || f.Name == "expires" {
				stray = append(stray, "--"+f.Name)
			}
		})
		if len(stray) > 0 {
			return &cliError{exit: exitUsage, code: "needs_issue_token", fix: "grain login " + server + " --issue-token " + strings.Join(stray, " ") + " ...",
				err: fmt.Errorf("%s only shape the token --issue-token mints; add --issue-token", strings.Join(stray, ", "))}
		}
	}
	if *name == "" {
		*name = contextSlug(u.Hostname())
	}
	if _, err := credentialFile(*name); err != nil {
		return err
	}

	// The human session login authenticates as: -token or the
	// environment's token, a registered source, or the deployment's own
	// sign-in (the local API token, a Google ID token).
	lc := ui.NewHTTPClient(server)
	lc.HTTP, lc.RetryPolicy, lc.Token, lc.Audience, lc.User = base.HTTP, base.RetryPolicy, base.Token, base.Audience, base.User
	if *auth != "" && !builtinAuth[*auth] {
		if _, ok := credentialSource(*auth); !ok {
			return usageErrorf("--auth %q: this grain build has no such credential source", *auth)
		}
		lc.ContextToken = contextToken(selectedContext{name: *name, ctx: &cliContext{Server: server, Auth: *auth}}, server, credentialStore{})
	}
	who, err := lc.AuthWhoami(ctx)
	if err != nil {
		return err
	}

	store := newCredentialStore()
	entry := &cliContext{Server: server}
	prior := cfg.Contexts[*name]
	switch {
	case *tokenStdin:
		secret, err := readSecretLine(loginStdin)
		if err != nil {
			return err
		}
		entry.Auth = "external-token"
		if !model.IsExternalAgentToken(secret) {
			if *auth != "access-code" {
				return usageErrorf("stdin did not hold a gma_ token; pass --auth access-code to store an access code")
			}
			entry.Auth = "access-code"
			warnAccessCode(who.ExternalAgentAPI)
		}
		check := ui.NewHTTPClient(server)
		check.HTTP, check.RetryPolicy, check.Token = base.HTTP, base.RetryPolicy, secret
		if v, err := check.AuthWhoami(ctx); err != nil || !v.Authenticated {
			return loginRefused(server, err)
		}
		if entry.Credential, err = store.store(*name, secret); err != nil {
			return err
		}
	case *issue || (*refresh && prior != nil && prior.TokenID != ""):
		if !who.ExternalAgentAPI {
			return &cliError{exit: exitUsage, code: "external_agent_api_off",
				err: fmt.Errorf("%s does not accept gma_ tokens (externalAgentApi is off in its Settings); an operator turns it on", server)}
		}
		if !who.Authenticated {
			return loginRefused(server, nil)
		}
		var issued ui.IssuedExternalAgentToken
		if *refresh && prior != nil && prior.TokenID != "" {
			issued, err = lc.RotateExternalAgentToken(ctx, prior.TokenID)
		} else {
			at := time.Now().Add(life).UTC()
			issued, err = lc.CreateExternalAgentToken(ctx, ui.CreateExternalAgentTokenRequest{
				Name: loginTokenName(*name), ScopeSet: *scopeSet, Repos: splitList(*repos), ExpiresAt: &at, SelfService: true,
			})
		}
		if err != nil {
			return err
		}
		entry.Auth, entry.TokenID = "external-token", issued.ID
		if entry.Credential, err = store.store(*name, issued.Token); err != nil {
			return err
		}
	case *auth != "" && !builtinAuth[*auth]:
		if !who.Authenticated {
			return loginRefused(server, nil)
		}
		entry.Auth = *auth
	case lc.Token != "" || tokenSource("") != "":
		// An access code (or some other secret) given with -token.
		if who.ExternalAgentAPI && *auth != "access-code" {
			return &cliError{exit: exitUsage, code: "access_code_refused", fix: "grain login " + server + " --issue-token",
				err: fmt.Errorf("not storing the token -token gave: %s accepts gma_ tokens, so mint one with --issue-token "+
					"(or pass --auth access-code to keep the access code anyway)", server)}
		}
		if !who.Authenticated {
			return loginRefused(server, nil)
		}
		secret := lc.Token
		if secret == "" {
			for _, env := range ui.TokenEnvVars {
				if secret = strings.TrimSpace(os.Getenv(env)); secret != "" {
					break
				}
			}
		}
		warnAccessCode(who.ExternalAgentAPI)
		entry.Auth = "access-code"
		if entry.Credential, err = store.store(*name, secret); err != nil {
			return err
		}
	case ui.IsLoopbackURL(server):
		if !who.Authenticated {
			return loginRefused(server, nil)
		}
		entry.Auth = "local"
	default:
		if !who.Authenticated {
			return &cliError{exit: exitAuth, code: "login_required", fix: "gcloud auth login",
				err: fmt.Errorf("%s did not accept a Google ID token from Application Default Credentials or gcloud: run gcloud auth login", server)}
		}
		entry.Auth = "google-id-token"
		entry.Audience = lc.Audience
		if entry.Audience == "" {
			entry.Audience = server
		}
	}
	if prior != nil && prior.TokenID != "" && entry.TokenID == "" && prior.Credential != "" && prior.Credential != entry.Credential {
		fmt.Fprintf(os.Stderr, "grain login: context %s had token %s, which is still valid; `grain logout` revokes those\n", *name, prior.TokenID)
	}
	cfg.Contexts[*name] = entry
	cfg.Current = *name
	if err := saveContexts(cfg); err != nil {
		return err
	}
	if out.json {
		out.encode(struct {
			Name string `json:"name"`
			cliContext
		}{*name, *entry})
		return nil
	}
	// Never echo a password typed into the URL (http://user:pass@host)
	// back to a terminal or a log, as `grain skill install` never writes it.
	fmt.Printf("signed in to %s as context %s (%s)\n", skill.ScrubServerURL(server), *name, entry.Auth)
	return nil
}

func cmdLogout(ctx context.Context, base *ui.HTTPClient, out *printer, sel selectedContext, args []string) error {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	local := fs.Bool("local", false, "only forget the credential here; do not revoke the token on the server")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return usageErrorf("%v", err)
	}
	if len(rest) > 1 {
		return usageErrorf("usage: grain logout [<context>] [--local]")
	}
	cfg, err := loadContexts()
	if err != nil {
		return err
	}
	name := sel.name
	if len(rest) == 1 {
		name = rest[0]
	}
	entry, ok := cfg.Contexts[name]
	if !ok || name == "" {
		return unknownContext(name, "")
	}
	store := newCredentialStore()
	if err := store.keyringReachable(name, entry.Credential); err != nil {
		return err
	}
	revoked := false
	if entry.TokenID != "" && !*local {
		rc := ui.NewHTTPClient(entry.Server)
		rc.HTTP, rc.RetryPolicy = base.HTTP, base.RetryPolicy
		rc.ContextToken = contextToken(selectedContext{name: name, ctx: entry}, entry.Server, store)
		if err := rc.RevokeExternalAgentToken(ctx, entry.TokenID); err != nil {
			fmt.Fprintf(os.Stderr, "grain logout: could not confirm %s revoked token %s (%v); revoke it in Settings -> Agents\n",
				entry.Server, entry.TokenID, err)
		} else {
			revoked = true
		}
	}
	if err := store.delete(name, entry.Credential); err != nil {
		return err
	}
	entry.Credential, entry.TokenID = "", ""
	if err := saveContexts(cfg); err != nil {
		return err
	}
	if out.json {
		out.encode(map[string]any{"context": name, "revoked": revoked})
		return nil
	}
	fmt.Printf("signed out of context %s\n", name)
	return nil
}

func loginRefused(server string, err error) error {
	msg := fmt.Sprintf("%s did not accept this sign-in", server)
	if err != nil {
		msg += ": " + err.Error()
	}
	return &cliError{exit: exitAuth, code: "unauthenticated", err: errors.New(msg)}
}

func warnAccessCode(gmaEnabled bool) {
	advice := "turn on externalAgentApi in Settings and use `grain login --issue-token` for a scoped token"
	if gmaEnabled {
		advice = "`grain login --issue-token` mints a scoped token instead"
	}
	fmt.Fprintf(os.Stderr, "grain login: warning: the access code grants full operator authority over the deployment; %s\n", advice)
}

func readSecretLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", usageErrorf("--token-stdin read nothing from stdin")
	}
	return line, nil
}

var slugJunk = regexp.MustCompile(`[^a-z0-9]+`)

// contextSlug is a context name from a host name.
func contextSlug(host string) string {
	s := strings.Trim(slugJunk.ReplaceAllString(strings.ToLower(host), "-"), "-")
	if s == "" {
		return "default"
	}
	return s
}

// loginTokenName is the external agent name a context's token is minted
// under: unique per mint, so a second workstation's login does not
// collide with the first's.
func loginTokenName(ctxName string) string {
	host, _ := os.Hostname()
	name := "cli-" + contextSlug(host) + "-" + ctxName
	if len(name) > model.MaxExternalAgentNameLen-11 {
		name = name[:model.MaxExternalAgentNameLen-11]
	}
	return name + "-" + strconv.FormatInt(time.Now().Unix(), 36)
}

// parseLoginExpiry reads --expires: a Go duration, or whole days ("30d").
func parseLoginExpiry(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if days, ok := strings.CutSuffix(s, "d"); ok {
		if n, err := strconv.Atoi(days); err == nil && n > 0 {
			return time.Duration(n) * 24 * time.Hour, nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d, nil
	}
	return 0, usageErrorf("--expires %q: give a duration such as 30d or 12h", s)
}
