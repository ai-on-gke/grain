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

package ui_test

import "github.com/ai-on-gke/grain/pkg/ui"

// testAccessCode is the access code the tests in this directory give a
// server they reach over a real listener (httptest.NewServer). Such a
// request comes from loopback, which a single-tenant daemon does not
// trust by itself: only its access code -- the CLI's local API token --
// gets a caller in, the way the grain CLI on the daemon's own host gets
// in.
const testAccessCode = "TEST-ACCESS-CODE-0123"

// accessCodeClient is ui.NewHTTPClient for url, presenting c's access
// code as the grain CLI presents the local API token. It gives c
// testAccessCode first when c has none, so it has to be called before
// the server c is behind answers its first request.
func accessCodeClient(c *ui.Client, url string) *ui.HTTPClient {
	if c.Config.LocalAPIToken == "" {
		c.Config.LocalAPIToken = testAccessCode
	}
	hc := ui.NewHTTPClient(url)
	hc.Token = c.Config.LocalAPIToken
	return hc
}

// serverAccessClient is accessCodeClient for a test that holds srv
// itself, which may already have answered requests of its own.
func serverAccessClient(srv *ui.Server, url string) *ui.HTTPClient {
	ui.SetAccessCodeForTest(srv, testAccessCode)
	hc := ui.NewHTTPClient(url)
	hc.Token = testAccessCode
	return hc
}
