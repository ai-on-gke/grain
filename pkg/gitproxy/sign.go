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

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

func isGitSignPath(path string) bool {
	return path == "/_grain/git-sign" || path == "/git-sign"
}

// ServeGitSignHTTP handles POST /_grain/git-sign (and /git-sign), signing a
// git commit or tag payload on the host for a sandbox.
//
// The signature is the host account owner's, so it is handed out only to a
// sandbox that could already push as that owner: the sandbox must have a
// target repository (sandboxTargetRepo), the authorizer must let it push
// there (git-receive-pack), and the credential resolveCredential picks for
// that repository must be an ambient one with an SSH signing key. A sandbox
// granted no repository, or only read access, gets 403 -- there is no
// fallback to some other credential, because a signature minted for a
// sandbox that may push nowhere is a verified object it can carry anywhere.
// The payload must look like the commit or tag object git asks to have
// signed (validGitSignPayload), so the oracle signs nothing git would not.
func (p *GitProxy) ServeGitSignHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sandbox, ok := p.authenticateSandbox(w, r)
	if !ok {
		return
	}
	credName, status, reason := p.resolveSigningCredential(r.Context(), sandbox)
	if credName == "" {
		http.Error(w, reason, status)
		return
	}
	body, err := readBody(r.Body, MaxGitSignPayloadBytes)
	if errors.Is(err, errBodyTooLarge) {
		http.Error(w, fmt.Sprintf("signing payload exceeds %d bytes", MaxGitSignPayloadBytes), http.StatusRequestEntityTooLarge)
		return
	}
	if err != nil {
		http.Error(w, "reading signing payload", http.StatusBadRequest)
		return
	}
	if len(body) == 0 {
		http.Error(w, "signing payload is empty", http.StatusBadRequest)
		return
	}
	if !validGitSignPayload(body) {
		http.Error(w, "signing payload is not a git commit or tag object", http.StatusBadRequest)
		return
	}
	sig, err := p.Credentials.SignGitPayload(r.Context(), credName, "git", body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(sig)
}

// resolveSigningCredential names the credential a signing request from
// sandbox is answered with, or says why there is none. It is the credential
// resolveCredential picks for the sandbox's target repository, and only
// once the authorizer has agreed the sandbox may push to that repository.
func (p *GitProxy) resolveSigningCredential(ctx context.Context, sandbox string) (credName string, status int, reason string) {
	if p == nil || p.Credentials == nil || p.Authorizer == nil {
		return "", http.StatusForbidden, "no git credentials configured on this proxy"
	}
	host, owner, repo := p.sandboxTargetRepo(ctx, sandbox)
	if owner == "" || repo == "" {
		return "", http.StatusForbidden, "this sandbox has no repository it may push to, so it may not sign commits"
	}
	if normHost, err := p.requestHost(host); err == nil {
		host = normHost
	} else {
		return "", http.StatusForbidden, fmt.Sprintf("git host %q is not usable: %v", host, err)
	}
	scopeOwner := GitRequest{Host: host, Owner: owner}.ScopeOwner()
	allowed, err := p.Authorizer.Authorize(ctx, sandbox, scopeOwner, repo, "git-receive-pack")
	if err != nil {
		return "", http.StatusInternalServerError, "authorization check failed"
	}
	if !allowed {
		return "", http.StatusForbidden, fmt.Sprintf("%s/%s is not a repository this sandbox may push to, so it may not sign commits", scopeOwner, repo)
	}
	cred, notConfigured, err := p.resolveCredential(ctx, sandbox, host, owner, repo)
	if err != nil {
		return "", http.StatusInternalServerError, "resolving git credential failed"
	}
	if notConfigured != "" {
		return "", http.StatusForbidden, notConfigured
	}
	if cred.WrongHost != "" {
		return "", http.StatusForbidden, cred.WrongHost
	}
	credName = cred.Name
	if credName == "" {
		return "", http.StatusForbidden, "no git credential configured for this sandbox"
	}
	if !p.Credentials.IsAmbient(credName) {
		return "", http.StatusForbidden, fmt.Sprintf("credential %q is not an ambient git credential", credName)
	}
	if id := p.Credentials.HostGitIdentity(); id.SigningKey == "" {
		return "", http.StatusBadRequest, "host account has no SSH signing key configured"
	}
	return credName, 0, ""
}

// validGitSignPayload reports whether body has the shape of what git hands
// gpg.program to sign: a commit object (first header "tree <oid>") or an
// annotated tag object (first header "object <oid>"), its header block
// ended by a blank line.
func validGitSignPayload(body []byte) bool {
	s := string(body)
	first, _, ok := strings.Cut(s, "\n")
	if !ok {
		return false
	}
	kind, oid, ok := strings.Cut(first, " ")
	if !ok || (kind != "tree" && kind != "object") || !isHexObjectID(oid) {
		return false
	}
	return strings.Contains(s, "\n\n")
}

// isHexObjectID reports whether s is a full SHA-1 or SHA-256 object id in
// lower-case hex, as git writes them in object headers.
func isHexObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// sandboxTargetRepo names the repository a signing request from sandbox is
// judged against: its run's target, else its first write grant. Read-only
// grants are not candidates -- a sandbox may sign only for a repository it
// may push to, and resolveSigningCredential checks that it can.
func (p *GitProxy) sandboxTargetRepo(ctx context.Context, sandbox string) (host, owner, repo string) {
	auth := p.Authorizer
	if ro, ok := auth.(*ReadOnlyScopeAuthorizer); ok && ro != nil {
		if ro.WriteScopes != nil {
			if repos := ro.WriteScopes.Repos(sandbox); len(repos) > 0 {
				ref := model.RepoFromStored(repos[0].StoredOwner(), repos[0].Name)
				return ref.Host, ref.Owner, ref.Name
			}
		}
		auth = ro.Next
	}
	if ma, ok := auth.(*ModelAuthorizer); ok && ma != nil && ma.Store != nil {
		if target, _, writes, err := ma.Store.GitScope(ctx, sandbox); err == nil {
			switch {
			case target != nil && target.Owner != "" && target.Name != "":
				ref := model.RepoFromStored(target.StoredOwner(), target.Name)
				return ref.Host, ref.Owner, ref.Name
			case len(writes) > 0:
				ref := model.RepoFromStored(writes[0].StoredOwner(), writes[0].Name)
				return ref.Host, ref.Owner, ref.Name
			}
		}
	}
	if p.NamespaceScopes != nil {
		if scope, live, err := p.NamespaceScopes.SandboxNamespaceScope(ctx, sandbox); err == nil && live && scope.TaskRepo != "" {
			if ref, perr := model.ParseRepo(scope.TaskRepo); perr == nil && ref.Owner != "" && ref.Name != "" {
				return ref.Host, ref.Owner, ref.Name
			}
			if o, r, ok := strings.Cut(scope.TaskRepo, "/"); ok && o != "" && r != "" {
				return "", o, r
			}
		}
	}
	return "", "", ""
}
