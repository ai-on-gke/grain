package ui

import (
	"fmt"
	"net/http"
)

// crossOriginProtection refuses a state-changing request a browser sent
// on behalf of some other site (#2371). Every POST route here decodes
// its body as JSON whatever Content-Type it claims, so a plain <form> or
// a no-preflight fetch() from any page an operator's browser loads could
// otherwise drive the daemon as that operator.
//
// http.CrossOriginProtection lets GET, HEAD and OPTIONS through, and any
// request a browser marks Sec-Fetch-Site: same-origin (or none, for one
// the user typed). Without that header it falls back to comparing Origin
// with Host. A request carrying neither -- the CLI, pkg/mcp's client, a
// sandbox's curl, a webhook -- did not come from a browser and is not
// what CSRF is about, so it passes too.
var crossOriginProtection = http.NewCrossOriginProtection()

// checkCrossOrigin answers 403 and returns false for a cross-site
// browser request with an unsafe method.
func checkCrossOrigin(w http.ResponseWriter, r *http.Request) bool {
	if err := crossOriginProtection.Check(r); err != nil {
		writeError(w, http.StatusForbidden, fmt.Errorf("cross-origin %s %s refused: %w", r.Method, r.URL.Path, err))
		return false
	}
	return true
}
