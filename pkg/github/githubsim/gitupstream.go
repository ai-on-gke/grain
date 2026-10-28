package githubsim

// GitUpstream is the other half of github.com: the *git* smart-HTTP
// endpoints a clone or a push talks to, which Sim (the REST API double
// this package is mostly about) does not answer for at all.
//
// It exists because every failure this package modelled was a 404. That
// is the right answer for the REST API -- `GET /repos/{owner}/{repo}` on
// a repository a credential may not see is a 404 `{"message":"Not
// Found"}`, measured -- and it is not what the git endpoints do. Sent
// grain's own way (the token as the Basic auth username, empty password,
// exactly as gitproxy.RealForwarder sends it), github.com answers, also
// measured, by hand, against real repositories:
//
//	revoked or mistyped token, fetch or push  401  Invalid username or token. ...
//	no credential, push                       401  No anonymous write access.
//	no credential, fetch of a hidden repo     401  (empty)
//	accepted token, repo it cannot see        404  Repository not found.
//	accepted token that may not write, push   403  Write access to repository not granted.
//	token an org's SSO has not authorized     403  The '<org>' organization has enabled ...
//
// So the statuses grain's git proxy has to tell apart -- an expired
// credential, a credential without write, a credential an organization
// holds back, a repository that really is invisible -- are four
// different answers, and only one of them was reachable in a test before
// this type. What it models is that layer and only that layer: whether
// this credential gets past the door. Serving the repository behind it
// is Backend's job, because a packfile is not what any of this is about
// and a test asserting on refusals should not have to build a real
// repository to reach one.

import (
	"encoding/base64"
	"net/http"
	"regexp"
	"strings"
)

// gitPathRE matches the smart-HTTP paths GitHub serves, the same four
// gitproxy.ParsePath matches -- anything else is not a request this
// upstream has an opinion about.
var gitPathRE = regexp.MustCompile(
	`^/([^/]+)/([^/]+?)(?:\.git)?/(info/refs|git-upload-pack|git-receive-pack)$`)

// GitAccess is what one token may do, as GitHub would decide it: the
// repositories it can read, the ones it can also write, and the ones an
// organization holds it back from until a human authorizes it.
//
// Repositories are named "owner/repo", matched case-insensitively and
// with or without a .git suffix, the way GitHub itself resolves them.
type GitAccess struct {
	// Read is what this token may fetch. A repository absent from it is
	// invisible to this token, which GitHub reports as a 404 rather than
	// a 403 -- the deliberate refusal to confirm that a private
	// repository exists at all.
	Read []string
	// Write is what this token may also push to. A push to a repository
	// in Read but not in Write is the 403 a read-only fine-grained token,
	// or an App installation granted Contents: read, gets.
	Write []string
	// SSOUnauthorized names repositories this token would otherwise
	// reach, and is refused with a 403 rather than hidden behind a 404,
	// because the organization owning them enforces SAML SSO and nobody
	// has authorized this token for it. It is the one read failure
	// GitHub explains instead of hiding, and the reason it matters here
	// is that its body carries the URL a human has to visit.
	//
	// Checked before Read: a token in this state is one that *does*
	// otherwise have access, which is exactly why the refusal is
	// distinguishable.
	SSOUnauthorized []string
}

// GitUpstream serves the auth layer of GitHub's git endpoints, and hands
// anything that gets past it to Backend.
//
// The zero value is a useful thing in itself: no token is known to it,
// nothing is public, so every request is refused the way an unknown
// credential is. A test that wants a request to succeed supplies both a
// token that may do it and a Backend to serve it.
type GitUpstream struct {
	// Tokens is what each token may do, keyed by the token as it arrives
	// -- the Basic auth username, which is where GitHub's own HTTPS
	// convention puts it. A token that is not a key here is refused as
	// revoked or mistyped, which is what a real one that has been
	// deleted, expired or fat-fingered gets.
	Tokens map[string]GitAccess
	// Public names the repositories an anonymous fetch may see. Nothing
	// is public by default, since the repositories grain works in are
	// private ones and it is their behavior these tests are about.
	Public []string
	// Backend serves a request that got past the door -- a real `git
	// http-backend` over a bare repository, in a test that has one. Left
	// nil, an allowed request is answered 501, so a test that meant to
	// exercise the success path hears about it rather than passing on a
	// refusal it did not intend.
	Backend http.Handler
}

// ServeHTTP implements http.Handler.
func (u *GitUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m := gitPathRE.FindStringSubmatch(r.URL.Path)
	if m == nil {
		gitError(w, http.StatusNotFound, "Not Found")
		return
	}
	repo := m[1] + "/" + m[2]
	push := m[3] == "git-receive-pack" ||
		(m[3] == "info/refs" && r.URL.Query().Get("service") == "git-receive-pack")

	token, hasToken := basicAuthToken(r.Header.Get("Authorization"))
	if !hasToken {
		// GitHub never lets an anonymous push get as far as being
		// refused for what it is, and it will not confirm that a private
		// repository exists to a caller it cannot identify: both are 401
		// with an auth challenge, and the reason differs only in the
		// body it bothers to send.
		if push {
			gitUnauthorized(w, "No anonymous write access.")
			return
		}
		if !names(u.Public, repo) {
			gitUnauthorized(w, "")
			return
		}
		u.serve(w, r)
		return
	}

	access, known := u.Tokens[token]
	if !known {
		gitUnauthorized(w, "Invalid username or token. Password authentication is not supported for Git operations.")
		return
	}
	if names(access.SSOUnauthorized, repo) {
		gitError(w, http.StatusForbidden, "The '"+m[1]+"' organization has enabled or enforced SAML SSO. "+
			"To access this repository, visit https://github.com/orgs/"+m[1]+"/sso and try again.")
		return
	}
	if !names(access.Read, repo) && !names(u.Public, repo) {
		gitError(w, http.StatusNotFound, "Repository not found.")
		return
	}
	if push && !names(access.Write, repo) {
		gitError(w, http.StatusForbidden, "Write access to repository not granted.")
		return
	}
	u.serve(w, r)
}

func (u *GitUpstream) serve(w http.ResponseWriter, r *http.Request) {
	if u.Backend == nil {
		gitError(w, http.StatusNotImplemented,
			"githubsim: GitUpstream models GitHub's auth layer only -- this request was allowed "+
				"through and there is no Backend to serve the repository")
		return
	}
	u.Backend.ServeHTTP(w, r)
}

// gitUnauthorized is GitHub's 401, challenge header and all. git reads
// that header: without it a client never retries with a credential, so a
// double that omitted it would be answering something no real GitHub
// ever sends.
func gitUnauthorized(w http.ResponseWriter, message string) {
	w.Header().Set("WWW-Authenticate", `Basic realm="GitHub"`)
	gitError(w, http.StatusUnauthorized, message)
}

// gitError writes one of GitHub's own plain-text refusals. text/plain
// matters rather than being incidental: a git client only prints a body
// it is told is text (see gitproxy.Denial), so the content type is part
// of what is being simulated.
func gitError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	if message != "" {
		w.Write([]byte(message + "\n"))
	}
}

// basicAuthToken pulls the token out of an Authorization header written
// either of the two ways GitHub accepts one over HTTPS: the token as the
// username, password empty or ignored (gitproxy.RealForwarder's way), or
// the username "x-access-token" with the token as the password (the
// GitHub App convention, and ciWorkflowSource's askpass).
func basicAuthToken(header string) (string, bool) {
	const prefix = "Basic "
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(header, prefix))
	if err != nil {
		return "", false
	}
	user, password, ok := strings.Cut(string(decoded), ":")
	if !ok || user == "" {
		return "", false
	}
	if user == "x-access-token" && password != "" {
		return password, true
	}
	return user, true
}

// names reports whether repos lists repo, matched the way GitHub matches
// a repository: case-insensitively, .git or no .git.
func names(repos []string, repo string) bool {
	want := canonicalRepo(repo)
	for _, r := range repos {
		if canonicalRepo(r) == want {
			return true
		}
	}
	return false
}

func canonicalRepo(repo string) string {
	return strings.TrimSuffix(strings.ToLower(repo), ".git")
}
