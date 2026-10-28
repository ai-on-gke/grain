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

package ui

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/registryauth"
)

// An Artifact Registry image is read with the host's Google token, and
// the token stays on the registry: the config blob's redirect to storage
// on another host is followed without it.
func TestRegistryReadsSendAGoogleTokenOnlyToTheRegistry(t *testing.T) {
	saved := registryauth.TokenSource
	registryauth.TokenSource = func(context.Context) (string, error) { return "ya-test-token", nil }
	registryauth.ResetCache()
	t.Cleanup(func() {
		registryauth.TokenSource = saved
		registryauth.ResetCache()
	})

	const configDigest = "sha256:c0ffee"
	var mu sync.Mutex
	seen := map[string]string{} // host+path -> Authorization
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.Host+r.URL.Path] = r.Header.Get("Authorization")
		mu.Unlock()
		if r.Host == "storage.googleapis.com" {
			w.Write([]byte(`{"config":{"Labels":{"dev.grain.sandbox.contract":"7"}}}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer ya-test-token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/v2/proj/repo/sandbox/manifests/main":
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			w.Write([]byte(`{"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"` + configDigest + `"}}`))
		case "/v2/proj/repo/sandbox/blobs/" + configDigest:
			http.Redirect(w, r, "https://storage.googleapis.com/blob/c0ffee", http.StatusTemporaryRedirect)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	addr := srv.Listener.Addr().String()
	registryTransport = &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	t.Cleanup(func() { registryTransport = nil })

	const image = "us-docker.pkg.dev/proj/repo/sandbox:main"
	labels, err := FetchImageLabels(context.Background(), image)
	if err != nil {
		t.Fatal(err)
	}
	if labels["dev.grain.sandbox.contract"] != "7" {
		t.Errorf("labels = %v", labels)
	}
	if err := checkImageManifestUncached(context.Background(), image); err != nil {
		t.Errorf("CheckImageManifest: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, path := range []string{"us-docker.pkg.dev/v2/proj/repo/sandbox/manifests/main", "us-docker.pkg.dev/v2/proj/repo/sandbox/blobs/" + configDigest} {
		if got := seen[path]; got != "Bearer ya-test-token" {
			t.Errorf("%s carried Authorization %q, want the Google token", path, got)
		}
	}
	if got, ok := seen["storage.googleapis.com/blob/c0ffee"]; !ok || got != "" {
		t.Errorf("the redirected blob read was reached=%v with Authorization %q, want reached with none", ok, got)
	}
}
