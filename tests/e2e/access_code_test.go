// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package e2e

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

// testAccessCodeFile is the token file useTestAccessCode wrote.
var testAccessCodeFile string

// useTestAccessCode points this process -- and every grain binary it
// forks, the CLI and `grain mcpserver` -- at a token file holding
// testAccessCode, as LocalAPITokenFileEnv points the grain CLI at the
// daemon's: ui.HTTPClient then presents it to a loopback server unasked.
// It returns a func that removes the file.
func useTestAccessCode() (func(), error) {
	dir, err := os.MkdirTemp("", "grain-e2e-access-code")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, ui.LocalAPITokenFileName)
	if err := os.WriteFile(path, []byte(testAccessCode+"\n"), 0o600); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	testAccessCodeFile = path
	os.Setenv(ui.LocalAPITokenFileEnv, path)
	return func() { os.RemoveAll(dir) }, nil
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

// presentAccessCode sets req's bearer token to testAccessCode.
func presentAccessCode(req *http.Request) *http.Request {
	req.Header.Set("Authorization", "Bearer "+testAccessCode)
	return req
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

// seedTestAccessCode writes testAccessCode where a daemon started with
// -data-dir dataDir keeps its access code, before it starts: the daemon
// keeps a code it finds there (ui.EnsureLocalAPIToken). A daemon run in
// this process also names that file in LocalAPITokenFileEnv, for every
// test here; this points it back at testAccessCodeFile once t is done,
// before dataDir goes.
func seedTestAccessCode(t *testing.T, dataDir string) {
	t.Helper()
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ui.LocalAPITokenPath(dataDir), []byte(testAccessCode+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Setenv(ui.LocalAPITokenFileEnv, testAccessCodeFile) })
}
