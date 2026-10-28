package daemon

import "github.com/bwsalmon/grain/pkg/ui"

// testAccessCode is the access code the tests in this package give a
// server they reach over a real listener (httptest.NewServer). Such a
// request comes from loopback, which a single-tenant daemon does not
// trust by itself: only its access code -- the CLI's local API token --
// gets a caller in, the way the daemon's own client gets in.
const testAccessCode = "TEST-ACCESS-CODE-0123"

// accessCodeClient is ui.NewHTTPClient for url, presenting
// testAccessCode, for a server whose ui.Config has it as its
// LocalAPIToken.
func accessCodeClient(url string) *ui.HTTPClient {
	hc := ui.NewHTTPClient(url)
	hc.Token = testAccessCode
	return hc
}
