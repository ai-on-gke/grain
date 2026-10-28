package cli

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// testAccessCode is the access code the tests in this package give a
// server they reach over a real listener (httptest.NewServer, or a
// daemon they start). Such a request comes from loopback, which a
// single-tenant daemon does not trust by itself: only its access code --
// the local API token the grain CLI on the daemon's host presents --
// gets a caller in.
const testAccessCode = "TEST-ACCESS-CODE-0123"

// accessCodeClient is ui.NewHTTPClient for url, presenting
// testAccessCode as the grain CLI presents the local API token.
func accessCodeClient(url string) *ui.HTTPClient {
	hc := ui.NewHTTPClient(url)
	hc.Token = testAccessCode
	return hc
}

// runCLIWithAccessCode is runCLI(args) presenting testAccessCode (-token),
// for a server withAccessCode or accessCodeServer built.
func runCLIWithAccessCode(args []string) error {
	return runCLI(append([]string{"-token", testAccessCode}, args...))
}

// presentAccessCode sets req's bearer token to testAccessCode.
func presentAccessCode(req *http.Request) *http.Request {
	req.Header.Set("Authorization", "Bearer "+testAccessCode)
	return req
}

// withAccessCode is cfg with testAccessCode as its access code.
func withAccessCode(cfg ui.Config) ui.Config {
	cfg.LocalAPIToken = testAccessCode
	return cfg
}

// accessCodeServer is ui.NewServerWithClient(c), with testAccessCode as
// c's access code.
func accessCodeServer(c *ui.Client) *ui.Server {
	c.Config.LocalAPIToken = testAccessCode
	return ui.NewServerWithClient(c)
}

// seedTestAccessCode writes testAccessCode where a daemon started with
// -data-dir dataDir keeps its access code, before it starts: the daemon
// keeps a code it finds there (ui.EnsureLocalAPIToken), so
// accessCodeClient presents the code it has.
func seedTestAccessCode(t *testing.T, dataDir string) {
	t.Helper()
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ui.LocalAPITokenPath(dataDir), []byte(testAccessCode+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// useAccessCodeFile points LocalAPITokenFileEnv at a file holding
// testAccessCode for the rest of t, for a command that reads the local
// API token the way the grain CLI does and takes no -token (grain sync).
func useAccessCodeFile(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), ui.LocalAPITokenFileName)
	if err := os.WriteFile(path, []byte(testAccessCode+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ui.LocalAPITokenFileEnv, path)
}

// accessGet is http.Get presenting testAccessCode.
func accessGet(url string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(presentAccessCode(req))
}

// accessPost is http.Post presenting testAccessCode.
func accessPost(url, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	return http.DefaultClient.Do(presentAccessCode(req))
}
