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

package daemon

import "github.com/ai-on-gke/grain/pkg/ui"

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
