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

package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeKeyring is an OS keyring in memory; broken makes every write fail,
// as a keyring that is installed but locked does.
type fakeKeyring struct {
	secrets map[string]string
	broken  bool
}

func (k *fakeKeyring) get(account string) (string, error) {
	s, ok := k.secrets[account]
	if !ok {
		return "", errors.New("no such secret")
	}
	return s, nil
}

func (k *fakeKeyring) set(account, secret string) error {
	if k.broken {
		return errors.New("keyring locked")
	}
	k.secrets[account] = secret
	return nil
}

func (k *fakeKeyring) delete(account string) error {
	delete(k.secrets, account)
	return nil
}

// TestCredentialStoreKeyringFallbackAndRefusesWorldReadableFile is
// external-cli.spec.md#XC-7: a secret goes to the keyring as grain/<ctx>
// when there is one, to credentials/<ctx> mode 0600 when there is not
// (or it will not take it), and a credentials file anybody else could
// read is refused, exit 3, with the chmod that fixes it.
func TestCredentialStoreKeyringFallbackAndRefusesWorldReadableFile(t *testing.T) {
	dir := useConfigHome(t)
	const secret = "gma_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	kr := &fakeKeyring{secrets: map[string]string{}}
	withKeyring := credentialStore{keyring: kr}
	loc, err := withKeyring.store("team", secret)
	if err != nil || loc != "keyring:grain/team" || kr.secrets["team"] != secret {
		t.Fatalf("with a keyring, store = %q, %v (keyring %v)", loc, err, kr.secrets)
	}
	if got, err := withKeyring.load(loc); err != nil || got != secret {
		t.Fatalf("load(%q) = %q, %v", loc, got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "credentials", "team")); !os.IsNotExist(err) {
		t.Errorf("a keyring-held secret was also written to a file (%v)", err)
	}
	if _, err := (credentialStore{}).load(loc); exitOf(err) != exitAuth {
		t.Errorf("a keyring locator with no keyring = %v, want exit 3", err)
	}

	for name, store := range map[string]credentialStore{
		"headless":       {},
		"locked-keyring": {keyring: &fakeKeyring{secrets: map[string]string{}, broken: true}},
	} {
		loc, err := store.store(name, secret)
		if err != nil || loc != "file:"+name {
			t.Fatalf("%s: store = %q, %v; want the file", name, loc, err)
		}
		path := filepath.Join(dir, "credentials", name)
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %s = %v, %v; want mode 0600", name, path, info, err)
		}
		if d, _ := os.Stat(filepath.Dir(path)); d.Mode().Perm() != 0o700 {
			t.Errorf("credentials directory mode = %#o, want 0700", d.Mode().Perm())
		}
		if got, err := store.load(loc); err != nil || got != secret {
			t.Fatalf("%s: load = %q, %v", name, got, err)
		}

		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := store.load(loc)
		if got != "" || exitOf(err) != exitAuth || !strings.Contains(err.Error(), "chmod 0600 "+path) {
			t.Fatalf("%s: a world-readable file read as %q, %v; want exit 3 naming chmod 0600", name, got, err)
		}
		if _, _, fix := classifyError(err); fix != "chmod 0600 "+path {
			t.Errorf("%s: -json fix = %q", name, fix)
		}

		if err := store.delete(name, loc); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s: delete left %s (%v)", name, path, err)
		}
	}
	if err := withKeyring.delete("team", "keyring:grain/team"); err != nil || len(kr.secrets) != 0 {
		t.Errorf("delete left the keyring holding %v (%v)", kr.secrets, err)
	}
	if _, err := withKeyring.store("../escape", secret); exitOf(err) != exitUsage {
		t.Errorf("a context name with a slash = %v, want a usage error", err)
	}
}

func exitOf(err error) int {
	code, _, _ := classifyError(err)
	return code
}

// TestCredentialStoreKeepsNoStaleSecret: a context that first fell back
// to the 0600 file (no keyring, or a locked one) and later signs in again
// with the keyring reachable keeps no copy of the old secret on disk, and
// a keyring-held secret is never forgotten from a session that cannot
// reach the keyring -- that would leave it there with nothing naming it
// (docs/test-runs/2026-11-07-cli-contexts-gitlogin-liveness.md, F1/F2).
func TestCredentialStoreKeepsNoStaleSecret(t *testing.T) {
	dir := useConfigHome(t)
	path := filepath.Join(dir, "credentials", "team")

	if loc, err := (credentialStore{}).store("team", "old-secret"); err != nil || loc != "file:team" {
		t.Fatalf("headless store = %q, %v", loc, err)
	}
	kr := &fakeKeyring{secrets: map[string]string{}}
	withKeyring := credentialStore{keyring: kr}
	loc, err := withKeyring.store("team", "new-secret")
	if err != nil || loc != "keyring:grain/team" {
		t.Fatalf("keyring store = %q, %v", loc, err)
	}
	if raw, err := os.ReadFile(path); !os.IsNotExist(err) {
		t.Errorf("the keyring took the secret but %s still holds %q (%v)", path, raw, err)
	}

	err = (credentialStore{}).delete("team", loc)
	if exitOf(err) != exitAuth || !strings.Contains(err.Error(), "secret-tool clear service grain account team") {
		t.Fatalf("delete from a session with no keyring = %v; want exit 3 naming how to clear it", err)
	}
	if kr.secrets["team"] != "new-secret" {
		t.Fatalf("keyring lost the secret: %v", kr.secrets)
	}
	if err := withKeyring.delete("team", loc); err != nil || len(kr.secrets) != 0 {
		t.Fatalf("delete with the keyring = %v, keyring %v", err, kr.secrets)
	}
}

// TestMacKeychainSetKeepsTheSecretOffTheCommandLine: security(1) is
// given the secret on stdin, never in its argv, where ps shows it to
// every account on the machine.
func TestMacKeychainSetKeepsTheSecretOffTheCommandLine(t *testing.T) {
	bin := t.TempDir()
	record := filepath.Join(t.TempDir(), "security")
	script := "#!/bin/sh\necho \"argv: $*\" > " + record + "\ncat >> " + record + "\n"
	if err := os.WriteFile(filepath.Join(bin, "security"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	const secret = `gma_s3cret "quoted" \ back`
	if err := (macKeychain{}).set("work", secret); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	argv, stdin, _ := strings.Cut(string(got), "\n")
	if argv != "argv: -i" {
		t.Errorf("security ran with %q, want only -i", argv)
	}
	want := `"add-generic-password" "-U" "-s" "grain" "-a" "work" "-w" "gma_s3cret \"quoted\" \\ back"` + "\n"
	if stdin != want {
		t.Errorf("security read %q on stdin, want %q", stdin, want)
	}
	if err := (macKeychain{}).set("work", "two\nlines"); err == nil {
		t.Error("a secret spanning two lines was handed to security")
	}
}
