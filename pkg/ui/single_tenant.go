package ui

import (
	"net/http"
	"strings"

	"github.com/bwsalmon/grain/pkg/model"
)

// singleTenantRefusal is what a caller this daemon does not trust as
// itself hears: there is nothing it could sign in to.
const singleTenantRefusal = "this daemon is single-tenant: it keeps no accounts, and only its access code reaches its API " +
	"(`grain access-code` prints it); work from anybody else is judged by the local policy docs/local-policy.md describes"

// singleTenantProxiedRefusal is what a request a proxy vouched for hears
// from a single-tenant daemon (singleTenantRefusesIdentity).
const singleTenantProxiedRefusal = "this daemon is single-tenant: it keeps no accounts, " +
	"so it has nobody to run a person a proxy vouched for as, and it does not run them as itself either. " +
	"Present its access code (`grain access-code`)"

// singleTenantRefusesIdentity reports whether r is an API request the
// daemon refuses outright because a proxy, not the caller's own
// credential, says who it is: an IAP assertion that verifies. Dropping
// that person and judging the request by where it came from, as
// extractPrincipal does for a session, would run everybody behind the
// proxy as the daemon's own actor. A session is still dropped rather
// than refused: it is the caller's own, and the same caller without it
// is already the trusted source it would be judged as. A plaintext
// identity header names nobody at all (identifyCaller).
func singleTenantRefusesIdentity(r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		return false
	}
	p, method, ok := identifyCaller(r)
	if !ok || p.Kind != model.PrincipalHuman {
		return false
	}
	return method == AuthMethodIAP
}

// presentsAccess reports whether r carries this single-tenant daemon's
// access code, as a bearer token or an access session: such a request is
// judged by the code, and whatever a proxy's headers beside it say about
// a person is dropped (extractPrincipal) rather than refused.
func (s *Server) presentsAccess(r *http.Request) bool {
	cfg, _ := IAPConfigFromContext(r.Context())
	if presentsAccessCode(r, cfg) {
		return true
	}
	_, ok := accessSessionIssued(r, cfg)
	return ok
}
