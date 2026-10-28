package ui

import (
	"log"
	"strings"
	"sync"

	"github.com/golang-jwt/jwt/v5"
)

// This file is the one place pkg/ui reads a JWT it has not verified, and
// it only logs what it read (tests/security's jwt-parse-unverified rule
// allows this file and no other in the package). Nothing here decides
// who anybody is.

// refusedAssertionsLogged is which kinds of refused assertion have been
// logged already, so an operator setting up a proxy sees what it sends
// once, rather than on every request.
var (
	refusedAssertionsMu     sync.Mutex
	refusedAssertionsLogged = map[string]bool{}
)

// logRefusedAssertion logs, once per issuer and audience, an assertion
// this daemon refused and the issuer and audience it carried. Those are
// read without verifying anything, and only to say what the operator
// would have to configure if the proxy that sent it is theirs: it is
// how a deployment behind a BeyondCorp tunnel or a new IAP backend
// finds the audience to set (an identity-aware proxy's docs).
func logRefusedAssertion(tokenStr string, err error) {
	claims := &IAPClaims{}
	_, _, perr := jwt.NewParser().ParseUnverified(tokenStr, claims)
	iss, aud := "", ""
	if perr == nil {
		iss, aud = claims.Issuer, strings.Join(claims.Audience, ",")
	}
	key := iss + "\x00" + aud
	refusedAssertionsMu.Lock()
	seen := refusedAssertionsLogged[key]
	if !seen && len(refusedAssertionsLogged) < 64 {
		refusedAssertionsLogged[key] = true
	}
	refusedAssertionsMu.Unlock()
	if seen {
		return
	}
	if perr != nil {
		log.Printf("ui: refused an %s that is not a JWT: %v", IAPAssertionHeader, err)
		return
	}
	log.Printf("ui: refused an %s (issuer %q, audience %q): %v -- if the proxy that sent it is this deployment's own, "+
		"set -ui-iap-audience (GRAIN_IAP_AUDIENCE) and, for a proxy other than IAP, -ui-iap-issuer and -ui-iap-keys-url to match",
		IAPAssertionHeader, iss, aud, err)
}
