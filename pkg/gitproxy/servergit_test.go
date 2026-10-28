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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerGitRemoteSurfacesLoginRedirectErrorToGit(t *testing.T) {
	f := newDepotFixture(t, "ggrain")
	f.seed(t, "ggrain")

	cred, ok := f.creds.GetForRepoOn("depot", depotHost, "experimental", "ggrain")
	if !ok {
		t.Fatal("expected depot credential for experimental/ggrain")
	}

	// Simulate an expired login that redirects upstream requests to the login page.
	writeRaw(t, f.state, "corp-login", "")
	defer os.Remove(filepath.Join(f.state, "corp-login"))

	remote, err := f.creds.ServerGitRemoteFor(t.Context(), cred, depotHost, "experimental", "ggrain")
	if err != nil || remote == nil {
		t.Fatalf("ServerGitRemoteFor = %v, %v, want remote", remote, err)
	}
	defer remote.Close()

	out := gitFails(t, "", "ls-remote", "--", remote.URL, "HEAD")
	for _, want := range []string{"forge login needed: run `forgecli-login`", "redirected to " + testLoginHost} {
		if !strings.Contains(out, want) {
			t.Errorf("git ls-remote output %q lacks %q", out, want)
		}
	}
	for _, unwanted := range []string{"could not read Username", "terminal prompts disabled"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("git ls-remote output %q hid the login error behind %q", out, unwanted)
		}
	}
	if login, ok := AsLoginRedirect(remote.Err()); !ok || login == nil {
		t.Fatalf("remote.Err() = %v, want *LoginRedirectError", remote.Err())
	}

	// Once the login is restored, a fresh ServerGitRemote succeeds and records no error.
	os.Remove(filepath.Join(f.state, "corp-login"))
	okRemote, err := f.creds.ServerGitRemoteFor(t.Context(), cred, depotHost, "experimental", "ggrain")
	if err != nil || okRemote == nil {
		t.Fatalf("ServerGitRemoteFor = %v, %v, want remote", okRemote, err)
	}
	defer okRemote.Close()

	lsOut := gitIn(t, "", "ls-remote", "--", okRemote.URL, "HEAD")
	if !strings.Contains(lsOut, "\tHEAD") {
		t.Fatalf("git ls-remote HEAD = %q, want HEAD ref", lsOut)
	}
	if okRemote.Err() != nil {
		t.Fatalf("okRemote.Err() = %v, want nil", okRemote.Err())
	}
}
