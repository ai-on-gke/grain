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

package gitproxy

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
)

// mintedRepositories decodes the "repositories" field a mint call
// asked for, or nil for a call with no body at all (full scope).
func mintedRepositories(t *testing.T, call github.FakeCall) []string {
	t.Helper()
	if call.Body == nil {
		return nil
	}
	var body struct {
		Repositories []string `json:"repositories"`
	}
	if err := json.Unmarshal(call.Body, &body); err != nil {
		t.Fatalf("mint body %q is not JSON: %v", call.Body, err)
	}
	return body.Repositories
}

func TestMintInstallationTokenNarrowsToTheRequestedRepository(t *testing.T) {
	transport := github.NewFakeTransport(mintedTokenResponse("ghs_narrow", "2026-08-30T13:00:00Z"))
	if _, _, err := mintInstallationToken(transport, AppCredential{
		AppID: "1", InstallationID: "2", PrivateKey: testAppKey(t),
	}, time.Now(), "widgets"); err != nil {
		t.Fatal(err)
	}
	call := transport.Calls[0]
	if got := mintedRepositories(t, call); len(got) != 1 || got[0] != "widgets" {
		t.Errorf("repositories = %v, want [widgets]", got)
	}
	if call.Headers["Content-Type"] != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", call.Headers["Content-Type"])
	}
}

func TestMintInstallationTokenWithNoRepositorySendsNoBody(t *testing.T) {
	transport := github.NewFakeTransport(mintedTokenResponse("ghs_full", "2026-08-30T13:00:00Z"))
	if _, _, err := mintInstallationToken(transport, AppCredential{
		AppID: "1", InstallationID: "2", PrivateKey: testAppKey(t),
	}, time.Now(), ""); err != nil {
		t.Fatal(err)
	}
	if body := transport.Calls[0].Body; body != nil {
		t.Errorf("body = %q, want none for a full-scope mint", body)
	}
}

// TestSelectForRepoMintsAndCachesPerRepository is #2388: a token handed
// out for one repository's git traffic is minted for that repository
// alone, cached apart from every other repository's and from the
// full-scope token Select serves, so neither is ever served in the
// other's place.
func TestGetForRepoMintsAndCachesPerRepository(t *testing.T) {
	dir := writeCredentialSet(t, map[string]string{"acme/*": "ci-app"}, nil)
	writeAppCredential(t, dir, "ci-app")
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	set.Now = func() time.Time { return time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC) }
	transport := github.NewFakeTransport(
		mintedTokenResponse("ghs_widgets", "2026-08-30T13:00:00Z"),
		mintedTokenResponse("ghs_gadgets", "2026-08-30T13:00:00Z"),
		mintedTokenResponse("ghs_full", "2026-08-30T13:00:00Z"),
	)
	set.AppTransport = transport

	for _, tc := range []struct {
		lookup func() (Credential, bool)
		want   string
	}{
		{func() (Credential, bool) { return set.GetForRepo("ci-app", "Acme", "Widgets.git") }, "ghs_widgets"},
		{func() (Credential, bool) { return set.GetForRepo("ci-app", "acme", "gadgets") }, "ghs_gadgets"},
		{func() (Credential, bool) { return set.Get("ci-app") }, "ghs_full"},
		// Every one of these is a cache hit on the entry above it.
		{func() (Credential, bool) { return set.GetForRepo("ci-app", "acme", "widgets") }, "ghs_widgets"},
		{func() (Credential, bool) { return set.GetForRepo("ci-app", "acme", "gadgets") }, "ghs_gadgets"},
		{func() (Credential, bool) { return set.Get("ci-app") }, "ghs_full"},
	} {
		cred, ok := tc.lookup()
		if !ok || cred.Token == nil || *cred.Token != tc.want {
			t.Fatalf("got %+v, %v; want token %q", cred, ok, tc.want)
		}
	}

	if len(transport.Calls) != 3 {
		t.Fatalf("expected three mints (two repositories, one full scope), got %d", len(transport.Calls))
	}
	for i, want := range [][]string{{"widgets"}, {"gadgets"}, nil} {
		got := mintedRepositories(t, transport.Calls[i])
		if len(got) != len(want) || (len(want) == 1 && got[0] != want[0]) {
			t.Errorf("mint %d asked for repositories %v, want %v", i, got, want)
		}
	}
}

func TestForgetDropsEveryScopedEntryForACredential(t *testing.T) {
	dir := writeCredentialSet(t, map[string]string{"*": "ci-app"}, nil)
	writeAppCredential(t, dir, "ci-app")
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	set.AppTransport = github.NewFakeTransport(mintedTokenResponse("ghs_x", "2099-01-01T00:00:00Z"))
	set.GetForRepo("ci-app", "acme", "widgets")
	set.Get("ci-app")
	set.forget("ci-app")
	if len(set.cache) != 0 {
		t.Errorf("cache still holds %d entries after forget", len(set.cache))
	}
}
