package githubtoken

// The credential check for one named GitHub token (grain/task-189), and
// the two questions its design turned on.
//
// **Why this capability gets a check at all.** Settings reports every
// named-token row **Ready** by construction -- a row exists because an
// operator's own file under secrets/github exists, and there is no
// deployment setting and no secrets-store entry either could be waiting
// on (ui.githubTokenStatuses). That is an honest answer to "is this
// deployment configured for it" and no answer at all to the way these
// credentials actually fail: a token revoked, expired or rotated at
// GitHub's end, or one whose access to a repo was taken away. Nothing
// in the file changes when that happens, so every pane goes on saying
// Ready and the first symptom is a push failing through the git proxy,
// mid-run, as a sandbox's own error.
//
// **What it authenticates with, given this provider holds no client.**
// A named token is a SELECT capability: it mints nothing, places
// nothing, and during a dispatch resolves no credential at all -- the
// proxy looks the material up per request (githubtoken.go's own doc
// comment). So the check cannot reuse a client the provider already
// has, because there is none. It does not grow its own way to *find*
// the material either: Config.Credentials is the credential ladder
// itself (gitproxy.CredentialSet behind pkg/cli/daemon.go's adapter),
// which is the one thing that already knows both forms a named
// credential comes in -- a <name>.token read as-is, or a <name>.app.json
// whose installation token it mints and re-mints. Handing the check the
// ladder rather than teaching it to read files is what keeps "what does
// this token authenticate as" answered in exactly one place, and it is
// what makes the check test the material a push would actually carry,
// App re-minting included.
//
// model.CredentialResolver -- the secrets store every other
// CheckCredential resolves through -- is deliberately unused here for
// the same reason Spec().Requires is empty: these credentials are files,
// not secrets-store rows, and pkg/gitproxy/credentials.go records at
// length why they stay that way.
//
// **Why /rate_limit, and then the repos.** GET /rate_limit is the
// cheapest live-or-dead answer GitHub gives: it costs nothing against
// the limit it reports, and unlike GET /user it is answerable by both
// forms of credential (an App installation token has no user, and
// answers /user with a 403). But "the token is alive" is rarely the
// interesting failure. A token that is alive and has lost access to the
// repo it exists to push to fails in exactly the same place a dead one
// does, so the check goes on to look up each repo this deployment's
// teams own and reports, per repo, whether the token can see it and
// whether it could push to it.
//
// Repo access is reported as evidence, not folded into the verdict,
// with one exception. A named token is deliberately narrower than the
// deployment default -- that is what it is for -- so a token that
// cannot reach some target repo is doing its job, and failing the check
// for it would paint a correctly-scoped token permanently red. A token
// that can reach *none* of them is the other thing: live, and useless
// for anything any task on this deployment could ask it to do, which is
// a refusal an operator wants to hear about in the same words as a dead
// token.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// apiVersion pins every request against GitHub's own dated REST
// contract, the same way pkg/capability/githubsandbox's own calls are.
const apiVersion = "2022-11-28"

// maxCheckedRepos bounds how many of this deployment's teams' repos one
// check looks up. A check is a button somebody presses, and every repo
// past this one costs another round trip to somebody else's API for an
// answer that repeats what the ones before it said. A deployment
// targeting more than this gets the first few, and the detail says so
// rather than quietly reporting on a subset.
const maxCheckedRepos = 5

// CheckCredential implements model.CredentialChecker: it asks GitHub for
// this token's own rate limit, which is the cheapest call that proves
// the far end still accepts the credential, and then looks up each repo
// this deployment's teams own to see what the token can still reach.
//
// It reads and nothing else -- no mint, no write, nothing needing
// permission beyond what a fetch through the proxy already needs.
//
// creds is unused: a named token's material is an operator's file, not a
// secrets-store row, so it comes from the credential ladder handed to
// this provider at construction. See this file's doc comment.
func (p *Provider) CheckCredential(ctx context.Context, creds model.CredentialResolver) (model.CredentialCheck, error) {
	// Named before anything can fail, so a refusal says which file holds
	// the credential that was refused -- the whole remedy, and the same
	// reason every other provider fills Credentials in up front.
	check := model.CredentialCheck{Credentials: []string{CredentialFile(p.name, false)}}
	if p.cfg.Credentials == nil {
		return check, fmt.Errorf(
			"githubtoken: this process holds no GitHub credential ladder, so the %q token cannot be "+
				"tested from here", p.name)
	}
	cred, ok := p.cfg.Credentials.GitHubCredential(p.name)
	if !ok {
		return check, fmt.Errorf(
			"githubtoken: this deployment has no credential named %q under %s any more -- it was "+
				"removed since this process started, and the row offering it will be gone after a "+
				"restart", p.name, credentialDir)
	}
	check.Credentials = []string{CredentialFile(p.name, cred.App)}
	if strings.TrimSpace(cred.Token) == "" {
		if cred.App {
			// GitHub's own reason where the ladder kept one
			// (Credential.MintError): a suspended installation, a
			// revoked key and an installation id that no longer exists
			// are three different fixes, and this is the only place the
			// difference is stated.
			reason := "the App's private key has been regenerated, or its installation revoked"
			if cred.MintError != "" {
				reason = cred.MintError
			}
			return check, fmt.Errorf(
				"githubtoken: %s is a GitHub App credential and no installation token could be minted "+
					"from it, so every push through the %q token goes out unauthenticated: %s. Check "+
					"the installation on GitHub and replace that file with current values",
				CredentialFile(p.name, true), p.name, reason)
		}
		return check, fmt.Errorf(
			"githubtoken: %s holds no token, so every push through the %q token goes out "+
				"unauthenticated. Paste a current one into Settings -> GitHub tokens",
			CredentialFile(p.name, false), p.name)
	}

	api := api{transport: p.transport()}
	limit, tokenPackagePublish, err := api.coreRateLimit(cred.Token)
	if err != nil {
		return check, p.explainRefusal(cred, err)
	}
	check.Detail = fmt.Sprintf("%s accepted the %q token (%d of %d core API requests left this hour).",
		p.host(), p.name, limit.Remaining, limit.Limit)

	var repos []string
	if p.cfg.Repos != nil {
		repos, err = p.cfg.Repos(ctx)
		if err != nil {
			return check, fmt.Errorf(
				"githubtoken: %s accepted the %q token, but which repos this deployment's teams own "+
					"could not be read to hold it against: %w", p.host(), p.name, err)
		}
	}
	access, skipped := api.reposFor(repos, cred.Token, tokenPackagePublish)
	if len(access) == 0 {
		// Nothing to add: a deployment none of whose teams names a
		// target repo has named nothing for the check to hold the
		// token against, and inventing a repo to try would be guessing.
		return check, nil
	}
	if !anyReachable(access) {
		return check, fmt.Errorf(
			"githubtoken: %s still accepts the %q token, but it can reach none of the repos this "+
				"deployment's teams own (%s). The token is live and has lost the access it exists for: "+
				"re-grant it on GitHub, or replace it in Settings -> GitHub tokens (%s)",
			p.host(), p.name, describeAccess(access), CredentialFile(p.name, cred.App))
	}
	check.Detail += " " + describeReach(access, skipped, cred.App)
	return check, nil
}

// explainRefusal is what a rejected /rate_limit call is reported as: the
// provider's own sentence naming the file to replace, rather than
// GitHub's bare 401, and phrased for the form of credential this
// actually is -- a *.token is pasted into Settings, a *.app.json is a
// file only the host can replace (gitproxy.CredentialSet.SetToken
// refuses to write over one).
func (p *Provider) explainRefusal(cred Credential, err error) error {
	remedy := "Paste a current one into Settings -> GitHub tokens"
	cause := "it has been revoked, has expired, or was rotated at GitHub's end"
	if cred.App {
		remedy = fmt.Sprintf("Replace %s on the host", CredentialFile(p.name, true))
		cause = "the App's private key has been regenerated, or its installation revoked"
	}
	return fmt.Errorf(
		"githubtoken: %s will not accept the token in %s: %s. Settings reports this capability Ready "+
			"only because that file exists. %s: %w",
		p.host(), CredentialFile(p.name, cred.App), cause, remedy, err)
}

// api is the two read-only GitHub REST calls this check makes, over a
// github.Transport -- narrow enough that this package's own tests fake
// it and need no network, the same bar the rest of pkg/capability holds
// to.
type api struct{ transport github.Transport }

func (a api) get(token, path string) (github.ApiResponse, error) {
	// "token <value>", the header every other REST call grain makes uses
	// (github.RESTClient.headers). Git traffic through the proxy carries
	// the same material as Basic auth instead (gitproxy.forward); what
	// is being tested here is the credential, not the header form.
	return a.transport.Request("GET", path, map[string]string{
		"Accept":               "application/vnd.github+json",
		"User-Agent":           "grain-automation",
		"X-GitHub-Api-Version": apiVersion,
		"Authorization":        "token " + token,
	}, nil)
}

// coreLimit is the "core" bucket of GET /rate_limit -- the only part of
// that answer worth showing, and the part that is evidence the token was
// read as a credential at all (an unauthenticated caller gets 60 an
// hour; a real token gets thousands).
type coreLimit struct {
	Limit     int
	Remaining int
}

func (a api) coreRateLimit(token string) (coreLimit, bool, error) {
	resp, err := a.get(token, "/rate_limit")
	if err != nil {
		return coreLimit{}, false, err
	}
	if resp.Status != 200 {
		return coreLimit{}, false, &github.Error{Status: resp.Status, Body: resp.Body}
	}
	var body struct {
		Resources struct {
			Core coreLimit `json:"core"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		return coreLimit{}, false, fmt.Errorf("reading GitHub's own rate limit answer: %w", err)
	}
	hasPublish := hasPackagePublishScope(resp.Headers)
	return body.Resources.Core, hasPublish, nil
}

// repoAccess is what one repo's own lookup said about this token: seen
// or not, pushable or not, and -- when it could not be seen -- a few
// words on why, since "no access" and "GitHub refused the call" send an
// operator to different places.
type repoAccess struct {
	Repo           string
	Reachable      bool
	Push           bool
	PackagePublish bool
	Reason         string
}

// reposFor looks up as many of repos as maxCheckedRepos allows,
// returning what each one said and how many were left unchecked. Never
// an error: a repo that cannot be reached is the answer, not a failure
// of the check, and one repo's refusal must not hide what the others
// said.
func (a api) reposFor(repos []string, token string, tokenPackagePublish bool) (access []repoAccess, skipped int) {
	for _, entry := range repos {
		repo, err := model.ParseRepo(entry)
		if err != nil {
			// Not this check's business to report: what may be entered
			// as a target repo is Settings' own validation, and a
			// malformed entry there is already its problem.
			continue
		}
		if len(access) == maxCheckedRepos {
			skipped++
			continue
		}
		access = append(access, a.repoAccess(token, repo, tokenPackagePublish))
	}
	return access, skipped
}

func (a api) repoAccess(token string, repo model.RepoRef, tokenPackagePublish bool) repoAccess {
	out := repoAccess{Repo: repo.String()}
	resp, err := a.get(token, "/repos/"+repo.Owner+"/"+repo.Name)
	if err != nil {
		out.Reason = err.Error()
		return out
	}
	switch resp.Status {
	case 200:
		var body struct {
			Permissions struct {
				Push     bool            `json:"push"`
				Packages json.RawMessage `json:"packages"`
			} `json:"permissions"`
		}
		// A 200 is already the whole of "the token can see this repo";
		// only the push and package permissions are read out of the body/headers,
		// and a body that will not parse leaves that unknown rather than
		// discarding the reachability this call just proved.
		_ = json.Unmarshal(resp.Body, &body)
		out.Reachable = true
		out.Push = body.Permissions.Push
		out.PackagePublish = tokenPackagePublish || hasPackagePublishScope(resp.Headers) || hasPackagePublishBody(body.Permissions.Packages)
	case 404:
		// GitHub answers 404, not 403, for a private repo the credential
		// cannot see -- so this one status covers both "access was taken
		// away" and "it was renamed or deleted", and the sentence says
		// both rather than guessing between them.
		out.Reason = "no access, or no such repo under that name"
	case 403:
		out.Reason = "forbidden -- an organization policy, an IP allow list, or a rate limit"
	default:
		out.Reason = fmt.Sprintf("GitHub answered %d", resp.Status)
	}
	return out
}

func headerValue(headers map[string]string, name string) string {
	if headers == nil {
		return ""
	}
	if v, ok := headers[name]; ok {
		return v
	}
	if v, ok := headers[http.CanonicalHeaderKey(name)]; ok {
		return v
	}
	for k, v := range headers {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

func hasPackagePublishScope(headers map[string]string) bool {
	if headers == nil {
		return false
	}
	scopesHeader := headerValue(headers, "X-OAuth-Scopes")
	if scopesHeader != "" {
		for _, s := range strings.Split(scopesHeader, ",") {
			s = strings.TrimSpace(s)
			if s == "write:packages" || s == "packages:write" || s == "packages: write" {
				return true
			}
		}
	}
	for _, key := range []string{"X-GitHub-Permissions", "X-Accepted-GitHub-Permissions", "X-Permissions"} {
		v := headerValue(headers, key)
		if v != "" {
			if strings.Contains(v, "packages: write") ||
				strings.Contains(v, "packages=write") ||
				strings.Contains(v, "packages:write") ||
				strings.Contains(v, "write:packages") {
				return true
			}
		}
	}
	return false
}

func hasPackagePublishBody(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s == "write" || s == "admin"
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b
	}
	return false
}

func anyReachable(access []repoAccess) bool {
	for _, a := range access {
		if a.Reachable {
			return true
		}
	}
	return false
}

// describeReach is the repo half of a successful check's Detail: what
// the token can push to, what it can only read, what it cannot see,
// and whether package publishing is permitted
// -- grouped rather than listed one repo per clause, since the useful
// shape of this answer is "these it can, those it cannot".
func describeReach(access []repoAccess, skipped int, isApp bool) string {
	var push, read []string
	var lost []string
	var canPub, noPub []string
	for _, a := range access {
		switch {
		case a.Reachable && a.Push:
			push = append(push, a.Repo)
		case a.Reachable:
			read = append(read, a.Repo)
		default:
			lost = append(lost, fmt.Sprintf("%s (%s)", a.Repo, a.Reason))
		}
		if a.Reachable {
			if a.PackagePublish {
				canPub = append(canPub, a.Repo)
			} else {
				noPub = append(noPub, a.Repo)
			}
		}
	}
	var parts []string
	if len(push) > 0 {
		parts = append(parts, "It can push to "+strings.Join(push, ", "))
	}
	if len(read) > 0 {
		parts = append(parts, "it can read but not push to "+strings.Join(read, ", "))
	}
	if len(lost) > 0 {
		parts = append(parts, "it cannot see "+strings.Join(lost, ", "))
	}
	out := strings.Join(parts, "; ") + "."

	var pkgParts []string
	missingDiag := "missing 'write:packages' scope on personal access token"
	if isApp {
		missingDiag = "missing 'packages: write' permission on the GitHub App installation"
	}
	if len(canPub) > 0 {
		pkgParts = append(pkgParts, "It can publish packages to "+strings.Join(canPub, ", "))
	}
	if len(noPub) > 0 {
		if len(canPub) == 0 {
			pkgParts = append(pkgParts, fmt.Sprintf("it cannot publish packages to target repos (%s)", missingDiag))
		} else {
			pkgParts = append(pkgParts, fmt.Sprintf("it cannot publish packages to %s (%s)", strings.Join(noPub, ", "), missingDiag))
		}
	}
	if len(pkgParts) > 0 {
		out += " " + strings.Join(pkgParts, "; ") + "."
	}

	if skipped > 0 {
		out += fmt.Sprintf(" %d further target repo(s) were not checked.", skipped)
	}
	return out
}

// describeAccess is the same listing for the refusal above, where every
// entry is one the token could not reach.
func describeAccess(access []repoAccess) string {
	out := make([]string, 0, len(access))
	for _, a := range access {
		out = append(out, fmt.Sprintf("%s: %s", a.Repo, a.Reason))
	}
	return strings.Join(out, "; ")
}
