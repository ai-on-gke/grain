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

package upgrade

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/registryauth"
)

// googleToken makes registryauth hand out token (or fail, when token is
// empty) for the length of the test, and counts how often it was asked.
func googleToken(t *testing.T, token string) *int {
	t.Helper()
	asked := new(int)
	saved := registryauth.TokenSource
	registryauth.TokenSource = func(context.Context) (string, error) {
		*asked++
		if token == "" {
			return "", errors.New("no credentials here")
		}
		return token, nil
	}
	registryauth.ResetCache()
	t.Cleanup(func() {
		registryauth.TokenSource = saved
		registryauth.ResetCache()
	})
	return asked
}

// registryAt points registryClient at srv whatever host a request names,
// so a test can stand in for an Artifact Registry host.
func registryAt(t *testing.T, srv *httptest.Server) {
	t.Helper()
	addr := srv.Listener.Addr().String()
	saved := registryClient
	registryClient = &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	t.Cleanup(func() { registryClient = saved })
}

// An Artifact Registry image is probed with the host's Google token, sent
// as a Bearer straight to the registry: no anonymous challenge first.
func TestRegistryProbesSendAGoogleTokenToArtifactRegistry(t *testing.T) {
	googleToken(t, "ya-test-token")
	var auths []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "Bearer ya-test-token" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="https://`+r.Host+`/token",service="reg"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v2/proj/repo/grain/manifests/main":
			w.Header().Set("Docker-Content-Digest", testDigest)
		case "/v2/proj/repo/grain/tags/list":
			w.Write([]byte(`{"tags":["main","v1.2.3"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	registryAt(t, srv)

	digest, err := RegistryManifestDigest(context.Background(), "us-docker.pkg.dev/proj/repo/grain:main")
	if err != nil {
		t.Fatal(err)
	}
	if digest != testDigest {
		t.Errorf("digest = %q, want %q", digest, testDigest)
	}
	tags, err := ListRegistryTags(context.Background(), "us-docker.pkg.dev/proj/repo/grain")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(tags, ",") != "main,v1.2.3" {
		t.Errorf("tags = %v", tags)
	}
	for _, a := range auths {
		if a != "Bearer ya-test-token" {
			t.Errorf("a request carried Authorization %q, want the Google token on every one", a)
		}
	}
}

// A host with no Google token still probes a Google registry the way it
// always did, anonymously, and a registry outside Google never sees the
// lookup at all.
func TestRegistryProbesFallBackToTheAnonymousToken(t *testing.T) {
	asked := googleToken(t, "")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			w.Write([]byte(`{"token":"anon"}`))
		case r.Header.Get("Authorization") != "Bearer anon":
			w.Header().Set("WWW-Authenticate", `Bearer realm="https://`+r.Host+`/token",service="reg"`)
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.Header().Set("Docker-Content-Digest", testDigest)
		}
	}))
	defer srv.Close()
	registryAt(t, srv)

	if _, err := RegistryManifestDigest(context.Background(), "gcr.io/proj/grain:main"); err != nil {
		t.Fatalf("with no Google token: %v", err)
	}
	if *asked != 1 {
		t.Errorf("the Google token was looked up %d times, want 1", *asked)
	}
	if _, err := RegistryManifestDigest(context.Background(), "ghcr.io/owner/grain:main"); err != nil {
		t.Fatalf("GHCR: %v", err)
	}
	if *asked != 1 {
		t.Errorf("a GHCR probe looked up a Google token")
	}
}

// The default pull of an Artifact Registry image logs docker in with the
// Google token first; a GHCR image, or a configured PullCmd, does not.
func TestPullImageLogsDockerInToAGoogleRegistry(t *testing.T) {
	googleToken(t, "ya-test-token")
	dir := t.TempDir()
	pulled := filepath.Join(dir, "pulled")
	docker := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + pulled + "\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(docker), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var logins []string
	saved := dockerLogin
	dockerLogin = func(_ context.Context, host, token string) error {
		logins = append(logins, host+" "+token)
		return nil
	}
	defer func() { dockerLogin = saved }()

	u := newUpgrader(t, Config{Image: &ImageConfig{Repository: "us-docker.pkg.dev/proj/repo/grain"}})
	p := newProgress(u.cfg.StatusFile, "main", Status{}, u.plan())
	for _, ref := range []string{"us-docker.pkg.dev/proj/repo/grain:main", "ghcr.io/owner/grain:main"} {
		if err := u.pullImage(context.Background(), p, stepPull, ref); err != nil {
			t.Fatalf("pullImage(%s): %v", ref, err)
		}
	}
	if strings.Join(logins, ";") != "us-docker.pkg.dev ya-test-token" {
		t.Errorf("docker logins = %q, want one, to us-docker.pkg.dev", logins)
	}
	if got, want := readFile(t, pulled), "pull us-docker.pkg.dev/proj/repo/grain:main\npull ghcr.io/owner/grain:main\n"; got != want {
		t.Errorf("docker ran %q, want %q", got, want)
	}

	logins = nil
	u.cfg.Image.PullCmd = []string{"true"}
	if err := u.pullImage(context.Background(), p, stepPull, "us-docker.pkg.dev/proj/repo/grain:main"); err != nil {
		t.Fatal(err)
	}
	if len(logins) != 0 {
		t.Errorf("a configured PullCmd still logged docker in: %q", logins)
	}
}
