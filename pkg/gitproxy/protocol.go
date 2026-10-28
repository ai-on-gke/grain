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

// Package gitproxy is the only path from a sandbox to GitHub: docs/design.md's
// git proxy, ported from grain/proxy (Python) to sit alongside the rest of
// v2. The decision logic is unchanged in spirit -- match one of the four
// legal smart-HTTP paths, authenticate the caller by its per-sandbox
// bearer token, decide whether it may touch (owner, repo), select a
// credential, stream the request through, and audit the tuple -- but one
// piece moves: which repos a sandbox may touch is no longer a hand-edited
// allowlist file. It is read live from model's task store (Authorizer,
// in authorize.go), since a running task's Target and Reads already say
// exactly that, and a file an operator maintains by hand can only drift
// from it.
//
// Which refs a push may update is checked here too, but only as far as
// the plain-text command list at the head of a push (refupdates.go): a
// sandbox may push its own task's branch and nothing else, to GitHub or
// to a repository served locally alike. The packfile
// after it is never parsed, for the reason docs/design.md gives -- a
// hand-rolled pack parser subtly wrong fails open -- and the command-list
// reader refuses anything it cannot read in full. What happens to the
// run's own branch -- force-pushes, deletion -- is the run's business;
// required reviews stay with GitHub rulesets and the merge queue.
package gitproxy

import (
	"fmt"
	"regexp"
	"strings"
)

// pathRE matches the four legal smart-HTTP paths, e.g.
// /owner/repo.git/info/refs, or /host/owner/repo.git/info/refs for a
// repository on a git host other than the default (docs/git-hosts.md). A
// leading segment is a host only when it contains a dot or a colon,
// which no GitHub owner can.
var pathRE = regexp.MustCompile(
	`^/(?:(?P<host>[^/]*[.:][^/]*)/)?(?P<owner>[^/]+)/(?P<repo>[^/]+?)(?:\.git)?/` +
		`(?P<action>info/refs|git-upload-pack|git-receive-pack)$`)

// GitRequest is one of the four legal smart-HTTP paths, decomposed.
type GitRequest struct {
	Owner  string
	Repo   string
	Action string // "info/refs", "git-upload-pack", or "git-receive-pack"
	// Host is the git host the path named, as written, or empty for the
	// old /owner/repo form, which means the default host. HandleRequest
	// normalizes it, and empties it for the default host, before
	// anything else reads it.
	Host string
}

// ScopeOwner is the owner a request is authorized against: Owner, or
// host/Owner off the default host -- model.RepoRef.StoredOwner's form, so
// an Authorizer comparing it with a scope's RepoRefs keeps two hosts'
// owner/repo apart without an interface of its own for the host.
func (r GitRequest) ScopeOwner() string {
	if r.Host != "" {
		return r.Host + "/" + r.Owner
	}
	return r.Owner
}

// ParsePath matches path against the four legal smart-HTTP shapes. The
// second return is false if it isn't one of them.
func ParsePath(path string) (GitRequest, bool) {
	m := pathRE.FindStringSubmatch(path)
	if m == nil {
		return GitRequest{}, false
	}
	req := GitRequest{}
	for i, name := range pathRE.SubexpNames() {
		switch name {
		case "host":
			req.Host = m[i]
		case "owner":
			req.Owner = m[i]
		case "repo":
			req.Repo = m[i]
		case "action":
			req.Action = m[i]
		}
	}
	return req, true
}

// IsValidGitRequest is a tight allowlist of the client shapes a real git
// client produces -- mirrors what FINOS Git Proxy's validGitRequest()
// checks (docs/design.md says it's worth stealing): a git/ user agent,
// and, for the pack endpoints, an Accept header naming the matching
// x-git-* result type. Rejecting anything else means a browser poking at
// these paths gets a plain 404 rather than a confusing partial response.
func IsValidGitRequest(userAgent, accept, action string) bool {
	if !strings.HasPrefix(userAgent, "git/") {
		return false
	}
	if action == "info/refs" {
		return true // git sends Accept: */* here; nothing more to check
	}
	expected := fmt.Sprintf("application/x-%s-result", action)
	return strings.Contains(accept, expected)
}

// Denial is how this proxy refuses one request: an HTTP status, and the
// reason as plain text.
//
// Plain text, not the ERR pkt-line this used to send. A git client only
// parses pkt-lines out of a *200* response carrying a git content type;
// against the 401/403/500 a refusal is, it falls back to printing the
// body verbatim as "remote: " lines, so the pkt-line's own 4-hex length
// prefix ended up in front of the sentence an operator reads:
//
//	remote: 00c6ERR no credential configured for owner/repo -- ...
//
// which is what the first clone of a deployment with no GitHub
// credential yet actually printed (found by hand, task 244). The
// Content-Type is explicit for the same reason: git only shows the body
// at all when it is told the body is text.
func Denial(status int, message string) ProxyResponse {
	return ProxyResponse{
		Status:  status,
		Headers: map[string]string{"Content-Type": "text/plain; charset=utf-8"},
		Body:    []byte(message + "\n"),
	}
}
