// Package ui provides the HTTP interface, API handlers, and static asset serving for Grain.
package ui

import (
	"net/http"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// requestIsHTTPS reports whether r reached grain over https, directly or
// through a TLS-terminating proxy in front of it (Cloud Run, IAP, a load
// balancer) that says so in X-Forwarded-Proto. A client that forges the
// header on a plain-http request only gets cookies its own browser will
// not send back over http.
func requestIsHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// handleAuthLogout ends a single-tenant daemon's access session. There is
// no other session to end: grain keeps no sign-in accounts.
func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	clearAccessCookie(w, r)

	if r.Method == http.MethodPost || strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, http.StatusOK, map[string]any{"loggedOut": true})
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// AuthUserResponse is the response for GET /api/auth/user and /api/auth/status.
type AuthUserResponse struct {
	Authenticated bool   `json:"authenticated"`
	User          string `json:"user,omitempty"`
	// IdentityNotAccepted is configResponse.IdentityNotAccepted.
	IdentityNotAccepted string `json:"identityNotAccepted,omitempty"`
	// Tenancy is always "single": this daemon has no accounts to sign in
	// to. The frontend and the cloudtop installer still read it.
	Tenancy string `json:"tenancy"`
	// AuthMethod is how the caller is authenticated (Server.authMethod):
	// "iap", "header", or "trusted" for nobody signed in from a source
	// this daemon trusts as itself. Empty for anybody else.
	AuthMethod AuthMethod `json:"authMethod,omitempty"`
}

// identityNotAccepted is, for a request that is not authenticated, the
// sentence saying which identity it presented that did not count and
// why, or "" when it presented none or is authenticated. The headers
// grain's own frontend sends are left out (unverifiedIdentity), so a
// signed-out browser is not told its remembered name did not count. The caller
// wrote that identity itself, so telling it back discloses nothing; the
// sentence names flags, not their values.
func identityNotAccepted(r *http.Request, authenticated bool) string {
	if authenticated {
		return ""
	}
	if why := unverifiedIdentity(r, true); why != "" {
		return "This request's identity was not accepted because " + why + "."
	}
	return ""
}

// authMethod is how r's caller is authenticated, and who as. Nobody
// signed in from a trusted source is the operator (singleTenantTrusted), who
// runs as this deployment's own actor.
func (s *Server) authMethod(r *http.Request) (method AuthMethod, user string) {
	p, ok := PrincipalFromContext(r.Context())
	if ok {
		method = authMethodFromContext(r.Context())
	} else {
		p, method, ok = extractPrincipal(r)
	}
	if !ok || p.Kind != model.PrincipalHuman {
		if !ok && singleTenantTrusted(r) {
			return AuthMethodTrusted, ""
		}
		return AuthMethodNone, ""
	}
	return method, p.ID
}

func (s *Server) handleAuthUser(w http.ResponseWriter, r *http.Request) {
	var resp AuthUserResponse
	resp.AuthMethod, resp.User = s.authMethod(r)
	resp.Authenticated = resp.User != ""
	resp.IdentityNotAccepted = identityNotAccepted(r, resp.Authenticated)

	resp.Tenancy = "single"

	writeJSON(w, http.StatusOK, resp)
}
