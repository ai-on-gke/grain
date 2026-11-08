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
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Where `grain login` keeps a context's secret (external-cli.spec.md#XC-7):
// the OS keyring, under service "grain" and account <context>, when this
// machine has one to reach; otherwise a file of its own under the grain
// config directory, mode 0600, which is read only while it stays that
// way. The keyring is reached through the platform's own command
// (secret-tool for the freedesktop Secret Service, security for the
// macOS Keychain) rather than a library, so a build carries no cgo and a
// headless host without one simply falls back to the file.

// keyringService is the service every context's secret is filed under.
const keyringService = "grain"

// keyring is an OS keyring, keyed by context name.
type keyring interface {
	get(account string) (string, error)
	set(account, secret string) error
	delete(account string) error
}

// systemKeyring is this machine's keyring, nil when it has none to
// reach. Tests replace it.
var systemKeyring = func() keyring {
	switch runtime.GOOS {
	case "linux", "freebsd", "openbsd":
		if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
			return nil
		}
		if _, err := exec.LookPath("secret-tool"); err != nil {
			return nil
		}
		return secretToolKeyring{}
	case "darwin":
		if _, err := exec.LookPath("security"); err != nil {
			return nil
		}
		return macKeychain{}
	}
	return nil
}

type secretToolKeyring struct{}

func (secretToolKeyring) get(account string) (string, error) {
	out, err := keyringCommand("secret-tool", "lookup", "service", keyringService, "account", account).Output()
	return strings.TrimRight(string(out), "\n"), err
}

func (secretToolKeyring) set(account, secret string) error {
	cmd := keyringCommand("secret-tool", "store", "--label="+keyringService+"/"+account,
		"service", keyringService, "account", account)
	cmd.Stdin = strings.NewReader(secret)
	return cmd.Run()
}

func (secretToolKeyring) delete(account string) error {
	return keyringCommand("secret-tool", "clear", "service", keyringService, "account", account).Run()
}

type macKeychain struct{}

func (macKeychain) get(account string) (string, error) {
	out, err := keyringCommand("security", "find-generic-password", "-s", keyringService, "-a", account, "-w").Output()
	return strings.TrimRight(string(out), "\n"), err
}

// set hands the secret to security(1) on stdin, as a command for its
// interactive mode: given with -w on the command line it would sit in
// the process table, where any other account on the machine can read it
// with ps. A secret that cannot be one quoted line is refused, and store
// falls back to the 0600 file.
func (macKeychain) set(account, secret string) error {
	if strings.ContainsAny(secret, "\r\n\x00") || strings.ContainsAny(account, "\r\n\x00") {
		return errors.New("a keychain secret must be one line")
	}
	cmd := keyringCommand("security", "-i")
	cmd.Stdin = strings.NewReader(macKeychainCommand("add-generic-password", "-U",
		"-s", keyringService, "-a", account, "-w", secret) + "\n")
	return cmd.Run()
}

// macKeychainCommand is args as one line of security(1)'s interactive
// mode, each double-quoted so whitespace and quotes in it stay its own.
func macKeychainCommand(args ...string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(a) + `"`
	}
	return strings.Join(quoted, " ")
}

func (macKeychain) delete(account string) error {
	return keyringCommand("security", "delete-generic-password", "-s", keyringService, "-a", account).Run()
}

// keyringCommand is the keyring tool's command. It needs the session's
// own environment -- $DBUS_SESSION_BUS_ADDRESS finds the Secret Service,
// $HOME the login keychain -- and the CLI is that session's, not a
// daemon's.
func keyringCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	return cmd
}

// credentialStore keeps contexts' secrets.
type credentialStore struct {
	keyring keyring // nil: none reachable
}

func newCredentialStore() credentialStore {
	return credentialStore{keyring: systemKeyring()}
}

func credentialFile(name string) (string, error) {
	if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return "", usageErrorf("context name %q cannot name a credential file", name)
	}
	dir, err := grainConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "credentials", name), nil
}

// store keeps secret for context name and returns its locator.
func (s credentialStore) store(name, secret string) (string, error) {
	path, err := credentialFile(name)
	if err != nil {
		return "", err
	}
	if s.keyring != nil {
		if err := s.keyring.set(name, secret); err == nil {
			// A file an earlier login fell back to (no keyring then, or
			// a locked one) would otherwise go on holding the old
			// secret on disk, readable by anything running as this
			// account, after the keyring took over.
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return "", fmt.Errorf("removing the old credential file for context %s: %w", name, err)
			}
			return "keyring:" + keyringService + "/" + name, nil
		}
	}
	if err := writePrivateFile(path, []byte(secret+"\n")); err != nil {
		return "", fmt.Errorf("storing the credential for context %s: %w", name, err)
	}
	return "file:" + name, nil
}

// load reads the secret locator names. A file another account could
// read is refused, with the chmod that fixes it.
func (s credentialStore) load(locator string) (string, error) {
	switch {
	case strings.HasPrefix(locator, "keyring:"):
		account := strings.TrimPrefix(strings.TrimPrefix(locator, "keyring:"), keyringService+"/")
		if s.keyring == nil {
			return "", &cliError{exit: exitAuth, code: "credential_unavailable", fix: "grain login --refresh",
				err: fmt.Errorf("the credential for context %s is in the OS keyring, which this session cannot reach", account)}
		}
		secret, err := s.keyring.get(account)
		if err != nil || secret == "" {
			return "", &cliError{exit: exitAuth, code: "credential_unavailable", fix: "grain login --refresh",
				err: fmt.Errorf("reading the credential for context %s from the OS keyring: %v", account, err)}
		}
		return secret, nil
	case strings.HasPrefix(locator, "file:"):
		path, err := credentialFile(strings.TrimPrefix(locator, "file:"))
		if err != nil {
			return "", err
		}
		info, err := os.Stat(path)
		if err != nil {
			return "", &cliError{exit: exitAuth, code: "credential_unavailable", fix: "grain login --refresh",
				err: fmt.Errorf("reading the credential file %s: %w", path, err)}
		}
		if info.Mode().Perm()&0o077 != 0 {
			return "", &cliError{exit: exitAuth, code: "insecure_credential", fix: "chmod 0600 " + path,
				err: fmt.Errorf("refusing to read %s: its mode is %#o, so other accounts can read it; run chmod 0600 %s",
					path, info.Mode().Perm(), path)}
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return string(bytes.TrimSpace(raw)), nil
	case locator == "":
		return "", nil
	}
	return "", fmt.Errorf("unknown credential locator %q", locator)
}

// delete removes context name's secret wherever it is kept; one that is
// not there is already deleted. A secret in a keyring this session cannot
// reach is refused rather than forgotten: dropping the locator would
// leave the secret in the keyring with nothing left that names it.
func (s credentialStore) delete(name, locator string) error {
	if strings.HasPrefix(locator, "keyring:") {
		if err := s.keyringReachable(name, locator); err != nil {
			return err
		}
		_ = s.keyring.delete(name)
	}
	path, err := credentialFile(name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// keyringReachable is nil when locator is not a keyring's or this session
// can reach the keyring it names, and otherwise the error `grain logout`
// stops on before revoking or forgetting anything.
func (s credentialStore) keyringReachable(name, locator string) error {
	if !strings.HasPrefix(locator, "keyring:") || s.keyring != nil {
		return nil
	}
	return &cliError{exit: exitAuth, code: "credential_unavailable",
		fix: "grain logout " + name + " (from a session that reaches the OS keyring)",
		err: fmt.Errorf("the credential for context %s is in the OS keyring, which this session cannot reach, "+
			"so signing out here would leave it there: run grain logout %s from a session that reaches the keyring "+
			"(or clear it with: secret-tool clear service %s account %s)", name, name, keyringService, name)}
}
