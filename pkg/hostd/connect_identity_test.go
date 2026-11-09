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

package hostd

import (
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

// A local connection is never held to an owner; a remote one is admitted
// only as its owner, compared case-insensitively.
func TestOwnerRefusal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		id    ConnectIdentity
		owner string
		want  string
	}{
		{"local, no owner", ConnectIdentity{}, "", ""},
		{"local, owner", ConnectIdentity{}, "alice@example.com", ""},
		{"remote owner", ConnectIdentity{Remote: true, Principal: "Alice@Example.com"}, "alice@example.com", ""},
		{"remote other", ConnectIdentity{Remote: true, Principal: "bob@example.com"}, "alice@example.com", `connected as "bob@example.com", but its owner is "alice@example.com"`},
		{"remote no identity", ConnectIdentity{Remote: true}, "alice@example.com", "no verified identity"},
		{"remote no owner", ConnectIdentity{Remote: true, Principal: "bob@example.com"}, "", "has no owner"},
	} {
		got := ownerRefusal(tc.id, "ws-1", tc.owner, false)
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("%s: ownerRefusal = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestIDTokenEmail(t *testing.T) {
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"email": "Hostd@Proj.iam.gserviceaccount.com"}).SignedString([]byte("k"))
	if err != nil {
		t.Fatal(err)
	}
	if got := IDTokenEmail("Bearer " + tok); got != "hostd@proj.iam.gserviceaccount.com" {
		t.Errorf("IDTokenEmail = %q", got)
	}
	if got := IDTokenEmail("not-a-jwt"); got != "" {
		t.Errorf("IDTokenEmail(garbage) = %q", got)
	}
}

func TestDelegatedIdentityNeverOwnsAHostd(t *testing.T) {
	id := ConnectIdentity{
		Remote:      true,
		Principal:   "hostd-owner@proj.iam.gserviceaccount.com",
		DelegatedBy: "slack-bot@proj.iam.gserviceaccount.com",
	}
	got := ownerRefusal(id, "ws-1", "hostd-owner@proj.iam.gserviceaccount.com", false)
	if !strings.Contains(got, "delegated identity") {
		t.Fatalf("ownerRefusal(delegated) = %q, want refusal naming delegated identity", got)
	}
}
