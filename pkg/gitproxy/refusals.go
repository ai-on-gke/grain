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

package gitproxy

// What a sandbox is told when GitHub refuses a request the proxy did
// forward -- the 401, 403 and 404 answers that come back with the
// credential this deployment chose on them, not the one the sandbox
// holds.
//
// The sandbox's git prints whatever body an error response carries as
// "remote: " lines (Denial's own doc comment on why they must be plain
// text), and GitHub's own bodies are written for somebody holding the
// credential: "Repository not found", "Invalid username or token",
// "Write access to repository not granted". Every one of those is a
// sentence about a credential the sandbox never saw and cannot name. The
// one fact that would send an operator to the right place -- which entry
// of credentials.json the request went out under -- is exactly the fact
// only the proxy has, so these messages supply it.
//
// Which upstream statuses arrive here was measured against github.com
// (task 123), sending the token as the Basic auth username the way
// RealForwarder does:
//
//	revoked or mistyped token, fetch or push   401  Invalid username or token.
//	no credential, push                        401  No anonymous write access.
//	no credential, fetch of a hidden repo      401  (empty)
//	token GitHub accepts, repo it cannot see   404  Repository not found.
//	token that may read but not write, push    403  Write access ... not granted.
//	token an org's SSO has not authorized      403  ... has enabled or enforced SAML SSO ...
//
// So a 404 on the git endpoints is the *narrow* case -- a credential
// that authenticated and still cannot see the repository -- and the
// commoner failures (an expired token, a fine-grained token approved for
// the repo but not the org, a Contents:read token pushing) are the two
// statuses this file added.

import (
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// upstreamRefusal is the answer to give the sandbox for an upstream
// response the proxy would rather explain than pass on verbatim, and
// false for every other status -- including every success, which streams
// through untouched.
func upstreamRefusal(req GitRequest, query string, credential Credential, upstream UpstreamResponse, check *loginCheck) (ProxyResponse, bool) {
	// A credential the ladder is serving anonymously *because its App
	// installation token could not be minted* has one explanation, and
	// the status GitHub answered with is not it: the request went out
	// with no credential on it, so GitHub is answering the absence
	// rather than anything about the App. 401 "Invalid username or
	// token", 401 "No anonymous write access" and 404 "Repository not
	// found" all arrive here from the same broken installation, and each
	// of the three reasons below would send an operator to check a
	// credential that is fine. This one names the App instead
	// (grain/task-158).
	//
	// The downstream status is still each case's own -- 403 in place of
	// a 401 for the reason spelled out below, 404 left alone -- because
	// what git should do about this has not changed, only what it prints.
	if credential.MintFailure != nil {
		switch upstream.Status {
		case 401, 403:
			return Denial(403, mintFailureReason(req.Owner, req.Repo, credential, upstream)), true
		case 404:
			return Denial(404, mintFailureReason(req.Owner, req.Repo, credential, upstream)), true
		}
	}
	switch upstream.Status {
	case 401:
		// Deliberately not a 401 downstream. The credential GitHub
		// refused is grain's; the one the sandbox's git holds is the
		// proxy token, which authenticated fine a few lines earlier in
		// Handle -- and git answers a 401 by rejecting the credential it
		// used, which with the credential.helper=store that
		// mcp.ConfigureGitCredentials writes means *erasing* the
		// sandbox's proxy token from ~/.git-credentials. Every git
		// command for the rest of that run then fails asking for a
		// username, and the explanation below is buried under it. 403
		// says the same thing to git ("this was refused") without
		// costing the sandbox a credential that was never at fault.
		return Denial(403, unauthorizedReason(req.Owner, req.Repo, credential, upstream, check)), true
	case 403:
		return Denial(403, forbiddenReason(req.Owner, req.Repo, isPush(req.Action, query), credential, upstream)), true
	case 404:
		return Denial(404, notFoundReason(req.Owner, req.Repo, credential)), true
	}
	return ProxyResponse{}, false
}

// maxRefusalBodyBytes is the most of a refused upstream response's body
// the proxy reads. upstreamNote quotes at most 200 bytes of it; the rest
// of the margin is for a body with whitespace around the sentence.
const maxRefusalBodyBytes = 1 << 20

// isRefusalStatus reports whether upstreamRefusal answers status itself,
// and so needs the upstream body in hand rather than streaming it on.
func isRefusalStatus(status int) bool {
	return status == 401 || status == 403 || status == 404
}

// bufferRefusalBody reads at most maxRefusalBodyBytes of upstream's
// Stream into Body and closes the Stream. A body longer than that is
// truncated, which upstreamNote then declines to quote anyway; a read
// error leaves whatever arrived before it, which is all a note about a
// refusal needs.
func bufferRefusalBody(upstream UpstreamResponse) UpstreamResponse {
	if upstream.Stream == nil {
		return upstream
	}
	defer upstream.Stream.Close()
	upstream.Body, _ = io.ReadAll(io.LimitReader(upstream.Stream, maxRefusalBodyBytes))
	upstream.Stream = nil
	return upstream
}

// notFoundReason is what a sandbox is told when GitHub answers 404 to a
// request the proxy did forward -- in place of GitHub's own body, which
// git turns into "remote: Repository not found" and nothing else.
//
// That sentence is true of two different deployments and sends an
// operator to the same wrong place from both: a repository that really
// is not there, and one that is there and private and *this credential*
// cannot see, which GitHub answers 404 rather than 403 to on purpose
// (pkg/capability/githubtoken's own repo check says the same thing about
// the same status). The second is much the likelier of the two here,
// because the repo has already been checked against the task's own
// scope by this point: something wrote owner/name down as a repository
// this deployment works in. The commonest way to arrive here is a
// git connection that does not cover the repo the way its operator
// thinks it does -- one naming a credential whose token or App
// installation was never given that repository, or a narrower one that
// never matched, leaving the request on the host's default connection.
//
// So the message names the credential the request actually went out
// under, which is the one fact a sandbox cannot discover and the one an
// operator needs before they go looking.
func notFoundReason(owner, repo string, credential Credential) string {
	return fmt.Sprintf(
		"%s/%s: GitHub answered 404 to a request sent with %s. Either no such repository exists, "+
			"or that credential cannot see it -- a private repository a credential may not read reads "+
			"as 404 rather than 403, on the REST API and on these git endpoints alike, once the "+
			"credential itself has been accepted. Check the git connection covering this repo "+
			"(Settings, Git connections), and that the token or App installation behind "+
			"it was given this repository",
		owner, repo, credentialPhrase(credential))
}

// loginCheck is what asking the host's own tool who its login is said,
// for a 401 that may or may not be that login's fault: Login is the
// account it answered for, or Err why it did not answer.
type loginCheck struct {
	Login string
	Err   error
}

// unauthorizedReason is what a sandbox is told when GitHub answers 401:
// it refused the credential itself, before ever considering the
// repository. Nothing about the repo is implicated, which is the whole
// distinction from the 404 above -- an operator who reads "Repository
// not found" and an operator who reads this should go and look at two
// different things.
//
// A request sent with no credential at all is the exception: GitHub had
// no credential to refuse. Its 401 is GitHub asking for one, which it
// does for every push and for a fetch of a repository that is private or
// does not exist -- so a team whose git credential is "None (public)"
// reaching a private repository is told that, rather than to go looking
// for an expired token it never had.
//
// An ambient credential whose tool carries the host's traffic
// (ForgeViaCommand) is asked about its login before the login is blamed
// (check): such a host's git endpoints can answer 401 to a header they do
// not take while the REST API accepts the same login, so a 401 there says
// nothing about the login until `<tool> api user` fails too.
func unauthorizedReason(owner, repo string, credential Credential, upstream UpstreamResponse, check *loginCheck) string {
	if credential.Ambient != nil && check != nil {
		host := hostName(credential.Ambient.Host)
		tool := credential.Ambient.From
		if check.Err == nil {
			who := ""
			if check.Login != "" {
				who = " as " + check.Login
			}
			hint := ""
			if src, ok := ambientCommandSource(tool); ok && src.ForgeViaCommand != nil && src.ForgeViaCommand.GitRefusedHint != nil {
				if h := src.ForgeViaCommand.GitRefusedHint(host, owner, repo); h != "" {
					hint = " " + h
				}
			}
			return fmt.Sprintf(
				"%s/%s: %s's git endpoint refused the %q credential's token (401), but the %s login it came "+
					"from is valid (`GH_HOST=%s %s api user` answers%s), so this is not a login to renew.%s%s",
				owner, repo, host, credential.Name, tool, host, tool, who, hint, upstreamNote(upstream))
		}
		if login, ok := AsLoginRedirect(check.Err); ok {
			return fmt.Sprintf("%s/%s: %s refused the %q credential's token (401), and asking %s who its login "+
				"is was answered with a login page: %s",
				owner, repo, host, credential.Name, tool, login.Error())
		}
		return fmt.Sprintf(
			"%s/%s: %s refused the %q credential's token (401), and `GH_HOST=%s %s api user` failed too, so "+
				"the %s login on the grain host is no longer valid: %s. Run `%s` as %s, then retry%s",
			owner, repo, host, credential.Name, host, tool, tool, shortMessage(check.Err.Error()),
			AmbientLoginFix(host, tool), ambientAccount(), upstreamNote(upstream))
	}
	if credential.Ambient != nil {
		// Its token came from the host's own login moments ago; the
		// next request asks again (CredentialSet.Refused), so what is
		// left to fix is the login itself.
		return fmt.Sprintf(
			"%s/%s: GitHub refused the %q credential's token (401). It is an ambient credential: its token "+
				"came from %s on the grain host, and that login is no longer valid -- run `gh auth login "+
				"--hostname %s` (or `gh auth refresh`) as %s, then retry%s",
			owner, repo, credential.Name, credential.Ambient.From, hostName(credential.Ambient.Host),
			ambientAccount(), upstreamNote(upstream))
	}
	if credential.Token == nil {
		return fmt.Sprintf(
			"%s/%s: GitHub asked for a credential (401) on a request sent with %s. A request "+
				"without one may only fetch a public repository and can push nowhere, so this "+
				"repository is private or does not exist, or this was a push. If the git "+
				"connection covering this repo is \"None (public)\", give it a credential that reaches "+
				"this repository; otherwise check the git connection covering this repo (Settings, Git "+
				"connections)%s",
			owner, repo, credentialPhrase(credential), upstreamNote(upstream))
	}
	return fmt.Sprintf(
		"%s/%s: GitHub refused the credential itself (401) on a request sent with %s -- this says "+
			"nothing about the repository. An expired, revoked or mistyped token looks exactly like "+
			"this, as does an App installation token GitHub invalidated after this deployment minted "+
			"it -- a mint that failed outright would have said so instead. Check the "+
			"git connection covering this repo (Settings, Git connections), and that the token or "+
			"App installation behind it is still live%s",
		owner, repo, credentialPhrase(credential), upstreamNote(upstream))
}

// forbiddenReason is what a sandbox is told when GitHub answers 403: the
// credential authenticated and was then refused this repository. Which
// half of that is missing depends on what was asked for, and the two
// send an operator to different settings, so the message splits on it --
// a push refused this way is a permission the credential was never
// granted, while a fetch refused this way is (almost always) a token an
// organization's SSO has not been authorized for, since an ordinary
// unreadable repository is hidden behind a 404 instead.
func forbiddenReason(owner, repo string, push bool, credential Credential, upstream UpstreamResponse) string {
	if push {
		return fmt.Sprintf(
			"%s/%s: GitHub accepted %s and refused it write access (403). The credential "+
				"authenticated, so this is a permission it was never granted rather than a missing "+
				"repository: a fine-grained token without Contents: write, an App installation granted "+
				"read only, or a ruleset on this repository that refuses the push. Check what the token "+
				"or App installation behind that git connection was granted on this repository%s",
			owner, repo, credentialPhrase(credential), upstreamNote(upstream))
	}
	return fmt.Sprintf(
		"%s/%s: GitHub accepted %s and refused it read access (403). The credential authenticated, "+
			"and a repository it merely could not see would have been a 404 -- so this is a credential "+
			"held back from an organization it otherwise reaches: most often a token that has not been "+
			"authorized for that organization's SSO, or one an IP allow list blocks. Check the git "+
			"connection covering this repo (Settings, Git connections), and that organization's own "+
			"settings for the token or App installation behind it%s",
		owner, repo, credentialPhrase(credential), upstreamNote(upstream))
}

// mintFailureReason is what a sandbox is told when the request went out
// unauthenticated because this deployment could not mint an installation
// token for the App credential covering the repo.
//
// Everything else in this file explains a credential GitHub considered
// and refused. This explains a credential GitHub never saw: the ladder
// resolved the repo to a *.app.json credential, minting its token
// failed, and load fell back to anonymous so that a public repo would go
// on being readable (MintFailure's own doc comment on why that fallback
// is worth keeping). Whatever GitHub then answered is an answer about an
// unauthenticated request, and repeating it -- "Invalid username or
// token", "No anonymous write access" -- points at a token nobody sent.
//
// So the message leads with GitHub's own reason for refusing the token
// exchange, which is the fact that actually distinguishes a suspended
// installation from a revoked key from an installation id that no longer
// exists, and names the file holding the three values to fix.
func mintFailureReason(owner, repo string, credential Credential, upstream UpstreamResponse) string {
	return fmt.Sprintf(
		"%s/%s: this request went out with no credential at all. The ladder resolves this repo to "+
			"%q, a GitHub App credential, and no installation token could be minted from it: %s. "+
			"GitHub then answered the unauthenticated request %d, which says nothing about the App. "+
			"Check that the installation still exists and is not suspended, and that the app_id, "+
			"installation_id and private_key in %s.app.json under secrets/github are current%s",
		owner, repo, credential.Name, credential.MintFailure.Describe(), upstream.Status,
		credential.Name, upstreamNote(upstream))
}

// credentialPhrase names the credential a request went out under, in a
// shape that reads inside a sentence. The ladder's "no token at all"
// rung needs its own wording: saying the "anonymous" credential was
// refused would send an operator looking for a token that does not
// exist.
func credentialPhrase(credential Credential) string {
	if credential.Token == nil {
		return fmt.Sprintf("no credential at all (the ladder resolves this repo to %q)", credential.Name)
	}
	return fmt.Sprintf("the %q credential", credential.Name)
}

// upstreamNote quotes GitHub's own sentence back, when it said one worth
// repeating. The messages above explain which credential was used, which
// GitHub cannot know; GitHub sometimes says something this proxy cannot
// know in return -- an SSO refusal carries the very URL a human has to
// visit to authorize the token -- and dropping that on the floor would
// make this substitution a net loss for exactly the case it is least
// able to diagnose on its own.
//
// Kept to a short, plain-text, single-line body: an HTML error page or a
// packfile fragment quoted into a git "remote:" line is noise, and this
// runs on a response nobody has validated.
func upstreamNote(upstream UpstreamResponse) string {
	body := strings.TrimSpace(string(upstream.Body))
	if body == "" || len(body) > 200 || !isPlainText(body) {
		return ""
	}
	return fmt.Sprintf(". GitHub said: %s", strconv.Quote(collapseSpace(body)))
}

func isPlainText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return false
		}
		if r == '<' || r == '>' {
			// The start of an HTML error page, or of the pkt-line noise a
			// git endpoint can answer with -- either way not a sentence.
			return false
		}
	}
	return true
}

func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// ReadOnlyPushReason is the refusal for a push to a repository the
// sandbox may fetch but not push to -- a CI job's read scope on the repo
// under test, or a task's Reads entry. HandleRequest gives it at the
// receive-pack advertisement, where git prints it as "remote: ...".
func ReadOnlyPushReason(owner, repo string) string {
	return fmt.Sprintf("%s/%s is read-only for this sandbox: it may fetch it but not push to it", owner, repo)
}

// isPush reports whether a request is git's push half. The action alone
// does not say: a push begins with an info/refs GET like a fetch does,
// and only its service parameter tells the two apart. HandleRequest
// authorizes that GET as "git-receive-pack" on the strength of this, so
// an Authorizer sees every push as a push from its first request.
func isPush(action, query string) bool {
	if action == "git-receive-pack" {
		return true
	}
	if action != "info/refs" {
		return false
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		return false
	}
	return values.Get("service") == "git-receive-pack"
}

// accessRefusalPhrases are the parts of the proxy's own refusals that say
// the credential a request went out under cannot reach the repository at
// all -- as against a request that failed on the way (a 5xx, a forward
// that never arrived) or an App whose token could not be minted this
// minute, either of which another attempt may well get past.
var accessRefusalPhrases = []string{
	"GitHub answered 404 to a request sent with", // notFoundReason
	"GitHub refused the credential itself (401)", // unauthorizedReason
	"GitHub asked for a credential (401)",        // unauthorizedReason, no credential
	"and refused it read access (403)",           // forbiddenReason, fetch
	"and refused it write access (403)",          // forbiddenReason, push
	"has no GitHub credential",                   // model.NoTeamGitCredentialError
	"has no git connection covering",             // NoTeamCredentialMessage
	"is not configured on this deployment, so",   // selectTeamCredential
}

// IsAccessRefusal reports whether output -- what a sandbox's git printed,
// "remote: " lines and all -- carries one of this proxy's refusals for a
// repository the credential cannot reach. It is what lets the dispatcher
// stop a task whose clone was refused this way at the first attempt
// rather than retrying it to the failure limit: nothing about another
// attempt changes what the team's credential was granted
// (orchestrator.stopOnAccessRefused, grain/task-1850).
func IsAccessRefusal(output string) bool {
	for _, phrase := range accessRefusalPhrases {
		if strings.Contains(output, phrase) {
			return true
		}
	}
	return false
}
