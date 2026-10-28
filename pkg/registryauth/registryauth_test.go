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

package registryauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsGoogleRegistry(t *testing.T) {
	for host, want := range map[string]bool{
		"us-docker.pkg.dev":           true,
		"europe-west4-docker.pkg.dev": true,
		"gcr.io":                      true,
		"us.gcr.io":                   true,
		"ghcr.io":                     false,
		"docker.pkg.dev.example.test": false,
		"notgcr.io":                   false,
		"":                            false,
	} {
		if got := IsGoogleRegistry(host); got != want {
			t.Errorf("IsGoogleRegistry(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestHostOf(t *testing.T) {
	for ref, want := range map[string]string{
		"us-docker.pkg.dev/p/r/grain:main": "us-docker.pkg.dev",
		"gcr.io/p/grain":                   "gcr.io",
		"localhost:5000/grain":             "localhost:5000",
		"library/grain":                    "",
		"grain:main":                       "",
	} {
		if got := HostOf(ref); got != want {
			t.Errorf("HostOf(%q) = %q, want %q", ref, got, want)
		}
	}
}

func stubSource(t *testing.T, token string, err error) *int {
	t.Helper()
	calls := new(int)
	saved := TokenSource
	TokenSource = func(context.Context) (string, error) {
		*calls++
		return token, err
	}
	ResetCache()
	t.Cleanup(func() {
		TokenSource = saved
		ResetCache()
	})
	return calls
}

// A token, and the absence of one, are both remembered: probes come in
// bursts, and neither gcloud nor a metadata server timeout should run
// once per probe.
func TestTokenIsCached(t *testing.T) {
	calls := stubSource(t, "tok\n", nil)
	for range 3 {
		if got, err := Token(context.Background()); err != nil || got != "tok" {
			t.Fatalf("Token = %q, %v", got, err)
		}
	}
	if *calls != 1 {
		t.Errorf("source called %d times, want 1", *calls)
	}

	calls = stubSource(t, "", errors.New("none"))
	for range 3 {
		if got := TokenFor(context.Background(), "gcr.io"); got != "" {
			t.Fatalf("TokenFor = %q with no credentials", got)
		}
	}
	if *calls != 1 {
		t.Errorf("failing source called %d times, want 1", *calls)
	}
}

func TestTokenForLeavesOtherRegistriesAlone(t *testing.T) {
	calls := stubSource(t, "tok", nil)
	if got := TokenFor(context.Background(), "ghcr.io"); got != "" {
		t.Errorf("TokenFor(ghcr.io) = %q", got)
	}
	if *calls != 0 {
		t.Error("a GHCR host looked up a Google token")
	}
	if got := TokenFor(context.Background(), "us-docker.pkg.dev"); got != "tok" {
		t.Errorf("TokenFor(us-docker.pkg.dev) = %q, want tok", got)
	}
}

func TestMetadataToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Metadata-Flavor") != "Google" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Write([]byte(`{"access_token":"meta-tok","expires_in":3599,"token_type":"Bearer"}`))
	}))
	defer srv.Close()
	saved := MetadataTokenURL
	MetadataTokenURL = srv.URL
	defer func() { MetadataTokenURL = saved }()
	got, err := metadataToken(context.Background())
	if err != nil || got != "meta-tok" {
		t.Errorf("metadataToken = %q, %v; want meta-tok", got, err)
	}
}
