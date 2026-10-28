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
// git commit or tag payload on the host for a sandbox whose target repository
// uses an ambient credential with SSH commit signing configured.
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
	sig, err := p.Credentials.SignGitPayload(r.Context(), credName, "git", body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(sig)
}

func (p *GitProxy) resolveSigningCredential(ctx context.Context, sandbox string) (credName string, status int, reason string) {
	if p == nil || p.Credentials == nil {
		return "", http.StatusForbidden, "no git credentials configured on this proxy"
	}
	host, owner, repo := p.sandboxTargetRepo(ctx, sandbox)
	if owner != "" && repo != "" {
		normHost, err := p.requestHost(host)
		if err == nil {
			host = normHost
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
	}
	if credName == "" {
		if cred, _, err := p.resolveCredential(ctx, sandbox, "", "owner", "repo"); err == nil && cred.WrongHost == "" && cred.Name != "" {
			credName = cred.Name
		}
	}
	if credName == "" {
		credName = p.Credentials.PrimaryAmbientCredential()
	}
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
		if target, reads, writes, err := ma.Store.GitScope(ctx, sandbox); err == nil {
			switch {
			case target != nil && target.Owner != "" && target.Name != "":
				ref := model.RepoFromStored(target.StoredOwner(), target.Name)
				return ref.Host, ref.Owner, ref.Name
			case len(writes) > 0:
				ref := model.RepoFromStored(writes[0].StoredOwner(), writes[0].Name)
				return ref.Host, ref.Owner, ref.Name
			case len(reads) > 0:
				ref := model.RepoFromStored(reads[0].StoredOwner(), reads[0].Name)
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
