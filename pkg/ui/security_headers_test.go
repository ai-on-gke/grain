package ui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Every response -- the UI's own pages, the API, and a path nothing
// serves -- carries the CSP and anti-clickjacking headers.
func TestServerSetsSecurityHeaders(t *testing.T) {
	srv, _ := testServer(t)
	for _, path := range []string{"/", "/api/config", "/api/no-such-route"} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		h := rec.Header()
		csp := h.Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'self'", "script-src 'self'", "frame-ancestors 'none'", "object-src 'none'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: Content-Security-Policy = %q, missing %q", path, csp, want)
			}
		}
		if strings.Contains(csp, "unsafe-eval") || strings.Contains(strings.SplitN(strings.SplitN(csp, "script-src", 2)[1], ";", 2)[0], "unsafe-inline") {
			t.Errorf("%s: script-src must not allow inline or eval: %q", path, csp)
		}
		for name, want := range map[string]string{
			"X-Frame-Options":        "DENY",
			"X-Content-Type-Options": "nosniff",
			"Referrer-Policy":        "same-origin",
		} {
			if got := h.Get(name); got != want {
				t.Errorf("%s: %s = %q, want %q", path, name, got, want)
			}
		}
	}
}
