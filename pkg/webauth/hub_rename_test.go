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
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
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
	h := (&HubVerifier{}).Headers()
	if !slices.Contains(h, "X-Grain-Hub-Assertion") || !slices.Contains(h, "X-Grain-Registry-Assertion") {
		t.Fatalf("HubVerifier.Headers() = %v, want both names", h)
	}
}

// A pre-rename grain-registry strips only X-Grain-Registry-Assertion and sets
// only X-Grain-Registry-Assertion, while a post-rename grain-hub strips both
// and sets both headers to the identical JWT. VerifyRequest accepts either
// header alone and accepts both when identical, and refuses requests where the
// two headers carry different values (for example a smuggled
// X-Grain-Hub-Assertion arriving through an un-upgraded grain-registry) or
// where either header has multiple distinct values.
func TestHubVerifierRefusesConflictingHubAndRegistryAssertions(t *testing.T) {
	v := &HubVerifier{
		audience: "https://alpha.grain.example.com",
		validator: func(_ context.Context, tok, _ string) (*HubClaims, error) {
			switch tok {
			case "alice-jwt":
				return &HubClaims{Email: "alice@example.com", Kind: PrincipalHuman}, nil
			case "mallory-jwt":
				return &HubClaims{Email: "mallory@example.com", Kind: PrincipalHuman}, nil
			default:
				return nil, errors.New("invalid token")
			}
		},
	}

	// Pre-rename hub: only X-Grain-Registry-Assertion is set.
	rOld := httptest.NewRequest(http.MethodGet, "https://alpha.grain.example.com/api/tasks", nil)
	rOld.Header.Set(RegistryAssertionHeader, "mallory-jwt")
	if id, ok, err := v.VerifyRequest(rOld); err != nil || !ok || id.Email != "mallory@example.com" {
		t.Fatalf("VerifyRequest(old header) = (%+v, %v, %v), want mallory@example.com", id, ok, err)
	}

	// Future hub: only X-Grain-Hub-Assertion is set.
	rNew := httptest.NewRequest(http.MethodGet, "https://alpha.grain.example.com/api/tasks", nil)
	rNew.Header.Set(HubAssertionHeader, "alice-jwt")
	if id, ok, err := v.VerifyRequest(rNew); err != nil || !ok || id.Email != "alice@example.com" {
		t.Fatalf("VerifyRequest(new header) = (%+v, %v, %v), want alice@example.com", id, ok, err)
	}

	// Post-rename hub during transition: both headers carry the identical JWT.
	rSame := httptest.NewRequest(http.MethodGet, "https://alpha.grain.example.com/api/tasks", nil)
	rSame.Header.Set(HubAssertionHeader, "alice-jwt")
	rSame.Header.Set(RegistryAssertionHeader, "alice-jwt")
	if id, ok, err := v.VerifyRequest(rSame); err != nil || !ok || id.Email != "alice@example.com" {
		t.Fatalf("VerifyRequest(identical both headers) = (%+v, %v, %v), want alice@example.com", id, ok, err)
	}

	// Smuggled X-Grain-Hub-Assertion past a pre-rename grain-registry that set
	// X-Grain-Registry-Assertion for the real caller: must fail closed.
	rSmuggled := httptest.NewRequest(http.MethodGet, "https://alpha.grain.example.com/api/tasks", nil)
	rSmuggled.Header.Set(HubAssertionHeader, "alice-jwt")
	rSmuggled.Header.Set(RegistryAssertionHeader, "mallory-jwt")
	if id, ok, err := v.VerifyRequest(rSmuggled); err == nil || ok || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("VerifyRequest(conflicting headers) = (%+v, %v, %v), want conflicting headers error", id, ok, err)
	}

	// Multiple distinct values on a single assertion header must also fail closed.
	for _, hdr := range []string{HubAssertionHeader, RegistryAssertionHeader} {
		rMulti := httptest.NewRequest(http.MethodGet, "https://alpha.grain.example.com/api/tasks", nil)
		rMulti.Header.Add(hdr, "alice-jwt")
		rMulti.Header.Add(hdr, "mallory-jwt")
		if id, ok, err := v.VerifyRequest(rMulti); err == nil || ok {
			t.Fatalf("VerifyRequest(multiple %s) = (%+v, %v, %v), want error", hdr, id, ok, err)
		}
	}
}
