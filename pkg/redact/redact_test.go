package redact_test

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/redact"
)

// The tokens a host sandbox carrying its operator's logins hands any run
// that asks -- `gcloud auth print-access-token`, a gcloud refresh token,
// a GitHub OAuth token -- are cut by shape whether or not this process
// holds them (GSA1-12), and a git SHA beside them is not.
func TestTextCutsLoginTokenShapes(t *testing.T) {
	text := "token: ya29.a0AfB_byC1234567890abcdefghijklmnop\n" +
		`"refresh_token": "1//0gAbCdEfGhIjKlMnOpQrStUvWxYz0123456789"` + "\n" +
		"GH_TOKEN=gho_abcdefghijklmnopqrstuvwxyz0123\n" +
		"AWS_ACCESS_KEY_ID=AKIAABCDEFGHIJKLMNOP\n" +
		"GRAIN_RELEASE_GATE_TOKEN=rg_0123456789abcdef_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n" +
		"GRAIN_API_TOKEN=ga_fedcba9876543210_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n" +
		"commit 0123456789abcdef0123456789abcdef01234567"
	got, n := (*redact.Redactor)(nil).Text(text)
	for _, leaked := range []string{"ya29.a0AfB", "1//0gAbCd", "gho_abcdef", "AKIAABCDEFGHIJKLMNOP", "rg_0123456789abcdef", "ga_fedcba9876543210"} {
		if strings.Contains(got, leaked) {
			t.Errorf("Text left %q in:\n%s", leaked, got)
		}
	}
	if n != 6 {
		t.Errorf("Text counted %d redactions, want 6:\n%s", n, got)
	}
	if !strings.Contains(got, "commit 0123456789abcdef0123456789abcdef01234567") {
		t.Errorf("Text cut a git SHA:\n%s", got)
	}
	if !strings.Contains(got, `"refresh_token": "[redacted: google oauth token]"`) {
		t.Errorf("Text did not keep what surrounds a refresh token:\n%s", got)
	}
}

// Known values are cut across a whole block, and text with nothing in it
// comes back exactly as it went in.
func TestTextCutsKnownValuesAndLeavesCleanTextAlone(t *testing.T) {
	r := redact.New([]redact.KnownValue{{Label: "sandbox env HTTPS_PROXY", Value: "http://alice:pw@proxy:3128"}})
	got, n := r.Text("line one\nproxy is http://alice:pw@proxy:3128\n")
	if strings.Contains(got, "alice:pw") || n != 1 {
		t.Errorf("Text = %q, %d; want the proxy URL cut once", got, n)
	}
	if !strings.HasSuffix(got, "\n") || !strings.HasPrefix(got, "line one\n") {
		t.Errorf("Text changed the text around what it cut: %q", got)
	}
	clean := "nothing\nto see\n"
	if got, n := r.Text(clean); got != clean || n != 0 {
		t.Errorf("Text(clean) = %q, %d", got, n)
	}
}
