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

package mcp

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestConfigureGitCredentialsWritesACredentialLineMatchingGitCredentialStore(t *testing.T) {
	root := t.TempDir()
	if err := ConfigureGitCredentials(root, "http://10.100.0.1:8080/owner/repo.git", "secret-token", GitIdentity{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".git-credentials"))
	if err != nil {
		t.Fatal(err)
	}
	want := "http://sandbox:secret-token@10.100.0.1:8080\n"
	if string(data) != want {
		t.Errorf("got %q, want %q", data, want)
	}
}

func TestConfigureGitCredentialsWritesAGitconfigWithTheCredentialHelperAndAnIdentity(t *testing.T) {
	root := t.TempDir()
	if err := ConfigureGitCredentials(root, "http://proxy:8080/owner/repo.git", "tok", GitIdentity{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".gitconfig"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{"helper = store", "name = " + DefaultGitIdentityName, "email = " + DefaultGitIdentityEmail} {
		if !strings.Contains(got, want) {
			t.Errorf(".gitconfig missing %q, got:\n%s", want, got)
		}
	}
}

// The point of the setting: a deployment that has chosen a committer gets
// that committer on every commit the sandbox makes, rather than grain's.
func TestConfigureGitCredentialsWritesTheConfiguredIdentity(t *testing.T) {
	root := t.TempDir()
	identity := GitIdentity{Name: "acme bot", Email: "bot@acme.example"}
	if err := ConfigureGitCredentials(root, "http://proxy:8080/owner/repo.git", "tok", identity); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".gitconfig"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{"name = acme bot", "email = bot@acme.example"} {
		if !strings.Contains(got, want) {
			t.Errorf(".gitconfig missing %q, got:\n%s", want, got)
		}
	}
	if strings.Contains(got, DefaultGitIdentityName) {
		t.Errorf(".gitconfig still carries grain's own name, got:\n%s", got)
	}
}

// Half an identity is a real answer -- a deployment that renames the
// committer and says nothing about the address -- and the half it left
// out has to come from grain's own, not from nothing: a sandbox with no
// user.email set is one where `git commit` fails outright.
func TestConfigureGitCredentialsFillsTheHalfOfAnIdentityLeftEmpty(t *testing.T) {
	root := t.TempDir()
	if err := ConfigureGitCredentials(root, "http://proxy:8080/owner/repo.git", "tok",
		GitIdentity{Name: "acme bot"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".gitconfig"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{"name = acme bot", "email = " + DefaultGitIdentityEmail} {
		if !strings.Contains(got, want) {
			t.Errorf(".gitconfig missing %q, got:\n%s", want, got)
		}
	}
}

// ui.UpdateSettings refuses a line break on the way in, so this is about
// a value that reached grain_config some other way -- a hand-edited
// state-repo dump. It must come out as a mangled name rather than as a
// .gitconfig git can no longer parse: everything after the newline would
// otherwise be read as a key of its own.
func TestConfigureGitCredentialsFoldsALineBreakOutOfAnIdentity(t *testing.T) {
	root := t.TempDir()
	if err := ConfigureGitCredentials(root, "http://proxy:8080/owner/repo.git", "tok",
		GitIdentity{Name: "acme\nhelper = evil", Email: "bot@acme.example"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".gitconfig"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "name = acme helper = evil\n") {
		t.Errorf(".gitconfig did not fold the line break, got:\n%s", got)
	}
	// The helper lines are grain's two and only grain's two: a name that
	// folded into "name = acme helper = evil" is a mangled name, not a
	// third helper. Compared as whole lines rather than counted as a
	// substring, since the identity's own text contains "helper = " too.
	if want := []string{"helper =", "helper = store"}; !reflect.DeepEqual(helperLines(got), want) {
		t.Errorf(".gitconfig helper lines = %q, want %q, got:\n%s", helperLines(got), want, got)
	}
}

// git appends credential helpers rather than replacing them: every helper
// configured at any level runs for every credential, and it runs on the
// way *out* as well as in -- git hands the credential it just used to
// `<helper> store`. So without an empty `helper =` to discard the list
// built up before grain's, a helper the *host operator* configured runs
// beside grain's `store` and is given this sandbox's gitproxy token to
// file away in the operator's own credential store. macOS has such a
// helper by default, with nobody having chosen it: Apple's Command Line
// Tools ship /Library/Developer/CommandLineTools/usr/share/git-core/
// gitconfig containing `helper = osxkeychain`, so every authenticated
// push out of a sandbox wrote a sandbox token into the operator's login
// keychain (and raised a GUI dialog doing it). A Linux host with a helper
// configured leaks the same thing silently.
//
// The ordering is half of what is asserted, not a tidiness point: git
// applies these in file order, so a reset placed *after* `helper = store`
// would discard grain's own helper too and leave the sandbox with no way
// to authenticate to the proxy at all.
func TestConfigureGitCredentialsResetsTheHelperListBeforeNamingGrainsOwn(t *testing.T) {
	root := t.TempDir()
	if err := ConfigureGitCredentials(root, "http://proxy:8080/owner/repo.git", "tok", GitIdentity{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".gitconfig"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if want := []string{"helper =", "helper = store"}; !reflect.DeepEqual(helperLines(got), want) {
		t.Errorf(".gitconfig helper lines = %q, want %q (the reset first), got:\n%s",
			helperLines(got), want, got)
	}
	// And the reset is under [credential], not stranded in some other
	// section where git would read it as a different key entirely.
	if !strings.HasPrefix(got, "[credential]\n\thelper =\n\thelper = store\n") {
		t.Errorf(".gitconfig does not open with the credential section grain writes, got:\n%s", got)
	}
}

// helperLines is every credential-helper line in a .gitconfig, in file
// order and with its indentation trimmed -- the form the order and the
// count of helpers can be asserted in without the identity's own text
// (which may legitimately contain "helper = ", see the line-break test)
// being mistaken for one.
func helperLines(gitconfig string) []string {
	var lines []string
	for _, line := range strings.Split(gitconfig, "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "helper") {
			lines = append(lines, line)
		}
	}
	return lines
}

func TestConfigureGitCredentialsFilesAreReadableOnlyByTheOwner(t *testing.T) {
	root := t.TempDir()
	if err := ConfigureGitCredentials(root, "http://proxy:8080/owner/repo.git", "tok", GitIdentity{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".git-credentials", ".gitconfig"} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", name, info.Mode().Perm())
		}
	}
}

func TestConfigureGitCredentialsRejectsAMalformedRemoteURL(t *testing.T) {
	if err := ConfigureGitCredentials(t.TempDir(), "not-a-url", "tok", GitIdentity{}); err == nil {
		t.Fatal("expected an error for a remote URL with no scheme or host")
	}
}
