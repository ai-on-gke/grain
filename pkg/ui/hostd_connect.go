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

package ui

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/ai-on-gke/grain/pkg/apierror"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
)

// hostdConnectRoute reports whether r is a grain hostd dialing in to
// /api/hostd/connect. It authenticates in its own protocol -- it is let
// in only for the hostd token it presents (hostd.Remote.ServeHTTP),
// whatever else the request carries -- so it does not ask who the
// caller is (requiresAccess), and a single-tenant daemon serves it
// without its access code (singleTenantOpenRoute) and does not count
// the hostd or IAP token it sends as a bearer as a guess at it.
func hostdConnectRoute(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	return r.URL.Path == "/api/hostd/connect"
}

// handleHostdConnect hands a grain hostd's upgrade of /api/hostd/connect
// to Config.HostdRemote, which authenticates it with the hostd token and
// serves the session host CLIs and run: host capability servers start
// over. Who is connecting (hostdConnectIdentity) goes with it, so that a
// remote hostd is admitted only as its row's owner.
func (s *Server) handleHostdConnect(w http.ResponseWriter, r *http.Request) {
	if s.tasks == nil || s.tasks.Config.HostdRemote == nil {
		writeError(w, http.StatusServiceUnavailable, apierror.Public("this daemon takes no host agent dialing in: start it with -hostd-token-file"))
		return
	}
	id, code, err := s.hostdConnectIdentity(r)
	if err != nil {
		log.Printf("grain daemon: refused a grain hostd on %s connecting from %s: %v", hostdHostOf(r), r.RemoteAddr, err)
		if code == http.StatusServiceUnavailable {
			w.Header().Set("Retry-After", verifierRetryAfter)
		}
		http.Error(w, "host: "+err.Error(), code)
		return
	}
	s.tasks.Config.HostdRemote.ServeHTTP(w, r.WithContext(hostd.WithConnectIdentity(r.Context(), id)))
}

// hostdConnectIdentity is who r, a hostd dialing in, is connecting as
// (hostd.ConnectIdentity), or the status and error it is refused with:
//
//   - A verified identity (IAP, or another registered IdentityVerifier)
//     makes it a remote hostd connecting as that principal, which the
//     allowlist must admit. hostd.Remote then requires it to be the
//     hostd row's owner.
//   - With no verified identity, a request from loopback with no
//     forwarding or proxy headers (relayedByProxy) is a local hostd,
//     named by its token alone, as is any request to a daemon with no
//     verifier configured.
//   - Anything else -- relayed by a proxy, or from off this host, to a
//     daemon that has a verifier -- is a remote hostd with no identity,
//     which hostd.Remote refuses rather than fall back to the token.
func (s *Server) hostdConnectIdentity(r *http.Request) (hostd.ConnectIdentity, int, error) {
	cv := cachedOrVerifyCaller(r)
	if cv.present {
		if cv.err != nil {
			// singleTenantGate refuses this first; this is for a daemon
			// that does not run it. A verifier not available yet
			// is a refusal to retry, not a bad identity.
			if verifierUnavailable(cv.err) {
				return hostd.ConnectIdentity{}, http.StatusServiceUnavailable, fmt.Errorf("its identity could not be checked yet: %w", cv.err)
			}
			return hostd.ConnectIdentity{}, http.StatusUnauthorized, fmt.Errorf("its identity did not verify: %v", cv.err)
		}
		if cv.id.Via != nil {
			return hostd.ConnectIdentity{}, http.StatusForbidden, fmt.Errorf("a remote hostd cannot connect via a delegated identity (%s acting for %s): connect directly as the hostd's owner", cv.id.Via.Email, cv.id.Email)
		}
		if !s.isRegisteredHostdOwner(r.Context(), cv.id) {
			if err := s.checkAllowedPrincipal(r.Context(), cv.id); err != nil {
				return hostd.ConnectIdentity{}, http.StatusForbidden, err
			}
		}
		return hostd.ConnectIdentity{Remote: true, Principal: normalizeIdentityEmail(cv.id.Email), Via: string(cv.method)}, 0, nil
	}
	local := isLoopbackRequest(r) && !relayedByProxy(r)
	if local || len(activeVerifiers(r.Context())) == 0 {
		return hostd.ConnectIdentity{}, 0, nil
	}
	return hostd.ConnectIdentity{Remote: true}, 0, nil
}

// isRegisteredHostdOwner reports whether id is a human or non-pool GCP service
// account set as the owner of an active hostd row (Settings -> Hosts). Admitting
// it here lets a remote host enrolled via --join dial /api/hostd/connect (where
// hostd.Remote still requires its key-bound credential and matches its owner)
// without granting UI/API sign-in access.
func (s *Server) isRegisteredHostdOwner(ctx context.Context, id VerifiedIdentity) bool {
	if (id.Kind != model.PrincipalService && id.Kind != model.PrincipalHuman) || s.tasks == nil || s.tasks.Store == nil {
		return false
	}
	email := normalizeIdentityEmail(id.Email)
	if email == "" || (id.Kind == model.PrincipalService && s.tasks.gcpKeyPoolSet(ctx)[email]) {
		return false
	}
	rows, err := s.tasks.Store.ListHostds(ctx)
	if err != nil {
		return false
	}
	for _, h := range rows {
		if h.RevokedAt != nil {
			continue
		}
		if normalizeIdentityEmail(h.Owner) == email {
			return true
		}
	}
	return false
}

// hostdHostOf is the machine a connecting hostd says it runs on.
func hostdHostOf(r *http.Request) string {
	if host := r.Header.Get(hostd.HostHeader); host != "" {
		return host
	}
	return "an unnamed host"
}
