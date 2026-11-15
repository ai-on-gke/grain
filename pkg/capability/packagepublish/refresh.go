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

package packagepublish

// This file is how a job outlives its token. The login Materialize
// places is whatever the ladder answered when the sandbox was made, and
// most of what the ladder answers with lasts an hour: an App
// installation token, or an OAuth access token a deployment's own
// CredentialSet mints. A release job that reaches its last push more
// than an hour in fails at the registry with "authentication failed",
// which is what stopped a v3.1.0 release elsewhere.
//
// So the job is also given a way to ask again: a per-job refresh token
// for a route on grain's CI API listener (ciapi.Endpoint.Mount) that
// re-runs the same ladder for the same repository and registry, and a
// docker credential helper that asks it on every push. Nothing about
// the gate changes. The refresh token is minted by Materialize, which
// only runs for a job the event gate admitted, it is bound to the
// repository that job is for, and it dies with the job (Revoke) or at
// maxLease, whichever is first.

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

const (
	// RefreshRoute is the prefix the refresh route is mounted at on
	// grain's CI API listener. CredentialPath answers in the shape a
	// docker credential helper prints; TokenRoute answers with the bare
	// token, for a script that reads TokenPath.
	RefreshRoute    = "/package-publish/"
	CredentialRoute = RefreshRoute + "credential"
	TokenRoute      = RefreshRoute + "token"

	// RefreshURLPath and RefreshTokenPath are where a job finds the
	// route and its own token for it.
	RefreshURLPath   = "/home/debian/.package-publish-refresh-url"
	RefreshTokenPath = "/home/debian/.package-publish-refresh-token"

	// HelperName is the docker credential helper's name, as
	// ~/.docker/config.json's credHelpers says it; HelperPath is where
	// it is placed, in the one directory a placement may put a command
	// that every CI step has on its PATH (protocol.SandboxBinDir).
	HelperName       = "grain"
	HelperPath       = "/home/debian/.grain/bin/docker-credential-" + HelperName
	DockerConfigPath = "/home/debian/.docker/config.json"
)

// maxLease bounds a refresh token whose job states no deadline, as
// ciapi's own does: longer than any release job grain runs, and short
// enough that a Revoke that never comes does not leave it live for long.
const maxLease = 6 * time.Hour

// refreshGrant is what one refresh token may ask for: one repository's
// login at one registry, from the ladder the job was admitted through,
// asked exactly as Materialize asked it (owner is the stored spelling).
type refreshGrant struct {
	id       string
	runID    string
	repo     model.RepoRef
	owner    string
	registry string
	source   CredentialSource
	expires  time.Time
}

// Refresher is the refresh route and the tokens it answers. One per
// process: the capability registry is rebuilt from live configuration,
// and every build has to mint into the one being served.
type Refresher struct {
	// Log receives one line per request, answered or refused, naming
	// the run, the repository and which credential answered -- never
	// any material. nil is log.Printf.
	Log func(format string, args ...any)

	mu      sync.Mutex
	byToken map[string]refreshGrant
	now     func() time.Time
}

// NewRefresher returns a Refresher holding no tokens.
func NewRefresher() *Refresher {
	return &Refresher{byToken: map[string]refreshGrant{}, now: time.Now}
}

// mint returns the refresh token for g's run, minting one when the run
// holds none yet -- so the job's retry in a fresh sandbox is handed the
// token its first attempt was, rather than a second one.
func (r *Refresher) mint(g refreshGrant) (id, token string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepLocked()
	if g.runID != "" {
		for known, held := range r.byToken {
			if held.runID == g.runID && held.repo.Same(g.repo) {
				return held.id, known, nil
			}
		}
	}
	if g.id, err = randomHex(8); err != nil {
		return "", "", err
	}
	secret, err := randomHex(32)
	if err != nil {
		return "", "", err
	}
	token = "gpp_" + g.id + "_" + secret
	r.byToken[token] = g
	return g.id, token, nil
}

// revoke drops the token minted as id.
func (r *Refresher) revoke(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for known, g := range r.byToken {
		if g.id == id {
			delete(r.byToken, known)
		}
	}
}

func (r *Refresher) lookup(token string) (refreshGrant, bool) {
	if token == "" {
		return refreshGrant{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepLocked()
	var matched refreshGrant
	found := false
	for known, g := range r.byToken {
		if subtle.ConstantTimeCompare([]byte(known), []byte(token)) == 1 {
			matched, found = g, true
		}
	}
	return matched, found
}

func (r *Refresher) sweepLocked() {
	now := r.now()
	for token, g := range r.byToken {
		if !g.expires.IsZero() && !now.Before(g.expires) {
			delete(r.byToken, token)
		}
	}
}

func (r *Refresher) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log(format, args...)
		return
	}
	log.Printf(format, args...)
}

// helperCredential is a docker credential helper's answer to get.
type helperCredential struct {
	ServerURL string
	Username  string
	Secret    string
}

// ServeHTTP answers GET CredentialRoute and GET TokenRoute with a login
// the ladder resolves now, for the repository and registry the token
// was minted for and no other.
func (r *Refresher) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if req.Method != http.MethodGet {
		http.Error(w, "the package-publish refresh route answers GET only", http.StatusMethodNotAllowed)
		return
	}
	if req.URL.Path != CredentialRoute && req.URL.Path != TokenRoute {
		http.Error(w, fmt.Sprintf("the package-publish refresh route answers %s and %s only", CredentialRoute, TokenRoute),
			http.StatusNotFound)
		return
	}
	token, _ := strings.CutPrefix(strings.TrimSpace(req.Header.Get("Authorization")), "Bearer ")
	g, ok := r.lookup(strings.TrimSpace(token))
	if !ok {
		r.logf("package-publish refresh: refused %s: no live token", req.URL.Path)
		http.Error(w, "the package-publish refresh route needs this job's own live token (Authorization: Bearer, from "+
			RefreshTokenPath+"); a job's token ends with the job", http.StatusUnauthorized)
		return
	}
	c, err := g.source.PackageCredential(g.registry, g.owner, g.repo.Name)
	if err == nil {
		err = complete(c, g.repo)
	}
	if err != nil {
		// The ladder's error is the operator-facing reason a repository
		// has no credential (CredentialSource), and names no material.
		r.logf("package-publish refresh: run %s (%s): %v", g.runID, g.repo, err)
		http.Error(w, fmt.Sprintf("re-minting %s's publishing login for %s: %v", g.repo, g.registry, err), http.StatusBadGateway)
		return
	}
	r.logf("package-publish refresh: run %s (%s) re-minted its %s login from %s", g.runID, g.repo, c.Registry, c.Name)
	if req.URL.Path == TokenRoute {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintln(w, c.Token)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(helperCredential{ServerURL: c.Registry, Username: c.Username, Secret: c.Token})
}

// refreshPlacements are the files that let the job ask again: the route
// and its token, the docker credential helper that asks on every push,
// and the docker client configuration naming it for registry.
func refreshPlacements(listener, token, registry string) ([]model.Placement, error) {
	config, err := json.MarshalIndent(map[string]any{
		"credHelpers": map[string]string{registry: HelperName},
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	return []model.Placement{
		{Side: model.SideSandbox, Path: RefreshURLPath, Content: listener + "\n"},
		{Side: model.SideSandbox, Path: RefreshTokenPath, Content: token + "\n"},
		{Side: model.SideSandbox, Path: HelperPath, Content: HelperScript, Mode: "0755"},
		{Side: model.SideSandbox, Path: DockerConfigPath, Content: string(config) + "\n"},
	}, nil
}

// HelperScript is docker-credential-grain: the docker credential helper
// protocol (get, store, erase, list) over the refresh route, plus
// `refresh`, which rewrites TokenPath for a script that reads it.
//
// get asks the route every time, so a push an hour into a job is handed
// a token that is valid then. store and erase are no-ops: `docker login`
// with the placed token still works, and stores nothing, since what it
// would store is the token that is about to expire. The refresh token
// travels in a header read from stdin rather than in argv, and nothing
// here traces its commands.
const HelperScript = `#!/bin/sh
# docker-credential-grain: the release#package-publish CI capability's docker
# credential helper. Every get re-mints this job's registry login through
# grain, for this job's own repository only.
set -eu
url_file=` + RefreshURLPath + `
token_file=` + RefreshTokenPath + `

ask() {
  if [ ! -r "$url_file" ] || [ ! -r "$token_file" ]; then
    echo "docker-credential-grain: $url_file or $token_file is missing; grain places them for a job granted release#package-publish"
    return 1
  fi
  out=$(printf 'Authorization: Bearer %s\n' "$(cat "$token_file")" |
    curl -sS --max-time 60 -H @- -w '\n%{http_code}' "$(cat "$url_file")$1") || {
    echo "docker-credential-grain: grain's refresh route did not answer"
    return 1
  }
  code=${out##*
}
  body=${out%
*}
  if [ "$code" != 200 ]; then
    printf 'docker-credential-grain: HTTP %s: %s\n' "$code" "$body"
    return 1
  fi
  printf '%s\n' "$body"
}

case "${1:-}" in
get)
  cat >/dev/null
  ask ` + CredentialRoute + `
  ;;
store|erase)
  cat >/dev/null
  ;;
list)
  printf '{}\n'
  ;;
refresh)
  tmp=$(mktemp "` + TokenPath + `.XXXXXX")
  if ! ask ` + TokenRoute + ` >"$tmp"; then
    cat "$tmp" >&2
    rm -f "$tmp"
    exit 1
  fi
  chmod 600 "$tmp"
  mv "$tmp" ` + TokenPath + `
  ;;
*)
  echo "usage: docker-credential-grain get|store|erase|list|refresh" >&2
  exit 1
  ;;
esac
`

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("packagepublish: reading random bytes: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// Revoke ends the job's refresh token with the job.
func (p *Provider) Revoke(ctx context.Context, cc model.CapabilityContext, lease model.Lease) error {
	if p.cfg.Refresh != nil {
		p.cfg.Refresh.revoke(lease.Resource)
	}
	return nil
}
