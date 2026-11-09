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

package server

import (
	"net/http/httptest"
	"testing"
)

// For a release the hub sends its assertion under both the Grain Hub's
// header and the Grain Registry's, so a daemon from before the rename
// still reads it -- and strips a caller's own copies of either first.
func TestSanitizeProxyRequestSendsTheAssertionUnderBothNames(t *testing.T) {
	r := httptest.NewRequest("GET", "https://alpha.grain.example.com/", nil)
	r.Header.Set("X-Grain-Hub-Assertion", "forged")
	r.Header.Set("X-Grain-Registry-Assertion", "forged")
	r.Header.Set("X-Grain-Hub-Token", "grc_forged")
	SanitizeProxyRequest(r, "alpha.grain.example.com", "grain.example.com", "", "signed", "req-1")
	if got := r.Header.Get("X-Grain-Hub-Assertion"); got != "signed" {
		t.Errorf("X-Grain-Hub-Assertion = %q, want the signed assertion", got)
	}
	if got := r.Header.Get("X-Grain-Registry-Assertion"); got != "signed" {
		t.Errorf("X-Grain-Registry-Assertion = %q, want the signed assertion", got)
	}
	if got := r.Header.Get("X-Grain-Hub-Token"); got != "" {
		t.Errorf("X-Grain-Hub-Token = %q, want it stripped", got)
	}
}

// A connecting daemon's credential is read under either name.
func TestExtractHubTokenReadsBothHeaderNames(t *testing.T) {
	for _, h := range []string{"X-Grain-Hub-Token", "X-Grain-Registry-Token"} {
		r := httptest.NewRequest("GET", "/api/entries/a/connect", nil)
		r.Header.Set(h, "grc_abc")
		if got := ExtractHubToken(r); got != "grc_abc" {
			t.Errorf("ExtractHubToken(%s) = %q", h, got)
		}
	}
}
