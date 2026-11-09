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

package webauth

import (
	"net/http"
	"slices"
	"testing"
)

// A hub from before the Grain Hub rename sends only
// X-Grain-Registry-Assertion; one after it sends both. A daemon reads
// either, preferring the new name.
func TestHubAssertionReadsTheNewHeaderAndThePreRenameOne(t *testing.T) {
	old := http.Header{}
	old.Set("X-Grain-Registry-Assertion", "old-jwt")
	if got := HubAssertion(old); got != "old-jwt" {
		t.Fatalf("HubAssertion(old header only) = %q", got)
	}
	both := http.Header{}
	both.Set("X-Grain-Hub-Assertion", "new-jwt")
	both.Set("X-Grain-Registry-Assertion", "old-jwt")
	if got := HubAssertion(both); got != "new-jwt" {
		t.Fatalf("HubAssertion(both) = %q, want the new header's", got)
	}
	if got := HubAssertion(http.Header{}); got != "" {
		t.Fatalf("HubAssertion(none) = %q", got)
	}
	// Whatever strips a caller's own identity headers strips both.
	h := (&RegistryVerifier{}).Headers()
	if !slices.Contains(h, "X-Grain-Hub-Assertion") || !slices.Contains(h, "X-Grain-Registry-Assertion") {
		t.Fatalf("RegistryVerifier.Headers() = %v, want both names", h)
	}
}
