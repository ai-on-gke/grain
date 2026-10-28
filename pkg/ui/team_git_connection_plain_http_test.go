package ui_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/gitproxy"
	"github.com/bwsalmon/grain/pkg/ui"
)

// The single-tenant walkthrough's B8: a connection to a plain git host on
// http://172.17.0.5:8088 was saved, and the git proxy then refused every
// clone of it. It is refused when saved instead, as the client's mistake
// and in the proxy's own words; on loopback (a local mock) it still saves.
func TestAPlainHTTPConnectionOffLoopbackIsRefusedWhenSaved(t *testing.T) {
	client, _, _ := testClient(t)
	credentials, err := gitproxy.LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	client.Config.Credentials = credentials
	srv := ui.NewServerWithClient(client)
	headers := map[string]string{"Content-Type": "application/json"}
	rec := doWithHeaders(t, srv, http.MethodPost, "/api/deployment/git-connections",
		`{"type": "pat", "host": "172.17.0.5:8088", "kind": "git", "baseUrl": "http://172.17.0.5:8088", "value": "pw"}`, headers)
	want := `refusing to forward to "172.17.0.5:8088" over plain HTTP: ` +
		`that is only allowed to a loopback address (a local mock); use HTTPS for any other host`
	var refusal struct{ Error string }
	_ = json.Unmarshal(rec.Body.Bytes(), &refusal)
	if rec.Code != http.StatusBadRequest || !strings.Contains(refusal.Error, want) {
		t.Fatalf("code %d, body %s; want 400 carrying %q", rec.Code, rec.Body.String(), want)
	}
	for _, name := range credentials.Names() {
		t.Errorf("credential %q was written for a refused connection", name)
	}

	rec = doWithHeaders(t, srv, http.MethodPost, "/api/deployment/git-connections",
		`{"type": "pat", "host": "127.0.0.1:8088", "kind": "git", "baseUrl": "http://127.0.0.1:8088", "value": "pw"}`, headers)
	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("loopback: code %d, body: %s", rec.Code, rec.Body.String())
	}
}
