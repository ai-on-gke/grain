package demo

import (
	"net/http"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// demoTestAccessCode is the access code the tests in this package give
// the server they put in front of the demo's store. `grain demo` runs
// single-tenant with an access code of its own (Options.AccessCodeFile),
// and a single-tenant daemon trusts no request -- loopback included --
// that does not present it, so the tests present one the way the grain
// CLI presents the local API token.
const demoTestAccessCode = "DEMO-TEST-ACCESS-0123"

// withAccessCode is cfg with demoTestAccessCode as its access code.
func withAccessCode(cfg ui.Config) ui.Config {
	cfg.LocalAPIToken = demoTestAccessCode
	return cfg
}

// presentAccessCode has req present demoTestAccessCode as a bearer
// token, and returns it.
func presentAccessCode(req *http.Request) *http.Request {
	req.Header.Set("Authorization", "Bearer "+demoTestAccessCode)
	return req
}
