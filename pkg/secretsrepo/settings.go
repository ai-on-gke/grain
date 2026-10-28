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

package secretsrepo

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Settings names the optional repository that backs up the encrypted
// secrets file. It lives in the data directory because it describes
// something outside this host and therefore cannot be kept there itself.
// Configuration repository backup is a separate concern; grain currently
// keeps that repository controller-local.
type Settings struct {
	// Remote is the repository grain pushes its encrypted secrets file
	// to, or "" for a deployment that keeps them on this host alone. The
	// zero value is therefore "no off-host copy", which is what every
	// installation has until somebody configures one.
	Remote string `json:"remote,omitempty"`
	// Branch is the branch the file lives on; DefaultBranch when empty.
	Branch string `json:"branch,omitempty"`
	// TokenFile names a file holding the credential to push with, for a
	// secrets repository the GitHub credential ladder under
	// <data-dir>/secrets/github does not cover. Left empty, that ladder
	// is what authenticates the push.
	//
	// A path rather than the token itself: this file is not encrypted,
	// and a credential written into it would be a credential in plaintext
	// in the data directory with nothing marking it as one.
	TokenFile string `json:"tokenFile,omitempty"`
	// File overrides what the ciphertext is called inside that
	// repository. Empty uses FileName's rendering. Deployments upgraded
	// from config mirroring keep the name derived from their former mirror;
	// new deployments use LocalFileName.
	//
	// It is here to distinguish multiple controller-local grains sharing
	// one secrets repository, or for an operator whose repository has
	// a layout of its own to fit into. It may name a path with
	// directories in it ("grain/production.enc"); it may not escape the
	// repository.
	File string `json:"file,omitempty"`
}

// SettingsFileName is what the file is called in the data directory.
const SettingsFileName = "secrets-repo.json"

// LoadSettings reads the settings beside dataDir. A file that is not
// there is not an error: it is a deployment with no secrets repository,
// and the zero value says exactly that.
func LoadSettings(dataDir string) (Settings, error) {
	path := filepath.Join(dataDir, SettingsFileName)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("secretsrepo: reading %s: %w", path, err)
	}
	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		return Settings{}, fmt.Errorf("secretsrepo: %s is not valid JSON: %w", path, err)
	}
	return s, nil
}

// SaveSettings writes the settings, atomically so a daemon reading them
// at the same moment sees one version or the other and never half a
// file.
func SaveSettings(dataDir string, s Settings) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("secretsrepo: preparing %s: %w", dataDir, err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := filepath.Join(dataDir, SettingsFileName)
	tmp, err := os.CreateTemp(dataDir, ".secrets-repo-*")
	if err != nil {
		return fmt.Errorf("secretsrepo: creating a temporary file next to %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// The two halves of the name grain's ciphertext goes under, and the name
// it falls back to.
//
// "grain-" first so that a repository listed alphabetically groups every
// grain deployment's file together, and so that somebody who has never
// heard of grain can tell at a glance what put it there -- this
// repository is assumed to be shared, with other deployments and with
// whatever else an operator keeps encrypted material in.
// ".secrets.enc" last so that what the file *is* survives whatever the
// middle says: encrypted secrets, not a key and not a dump.
const (
	FilePrefix = "grain-"
	FileSuffix = ".secrets.enc"
	// LocalFileName is what a deployment whose state repository is
	// local-only gets, since there is no owner/name to name it after. It
	// is deliberately one fixed name rather than something derived from
	// the host: a container's hostname changes on every redeploy, and a
	// file named after one would leave a stale copy of a deployment's
	// secrets behind every time the host came back. Two local-only grains
	// sharing a secrets repository is the case it cannot tell apart, and
	// Settings.File is how one of them says which is which.
	LocalFileName = FilePrefix + "local" + FileSuffix
)

// FileName is what this deployment's ciphertext is called inside the
// secrets repository, derived from the state repository it runs on:
// "grain-bwsalmon-grain-home.secrets.enc" for a state repository at
// github.com/bwsalmon/grain-home.
//
// Derived rather than configured because the thing that distinguishes
// two deployments' secrets is exactly the thing that distinguishes their
// state: an operator running staging and production points both at one
// secrets repository, and gets two files without having had to think of
// two names. And it is the state repository rather than, say, the
// environment name because that name is a row in a database this file is
// needed to decrypt the credentials for -- what names the file has to be
// readable before grain has read anything.
//
// Owner and name are flattened into the name rather than made
// directories, so the file sits at the root where an operator browsing a
// shared secrets repository sees it, and characters git or a filesystem
// would argue about are replaced with "-".
func FileName(stateRemote string) string {
	owner, name, ok := repoFromRemote(stateRemote)
	if !ok {
		return LocalFileName
	}
	return FilePrefix + sanitize(owner) + "-" + sanitize(name) + FileSuffix
}

// FileNameFor is FileName unless the operator has named the file
// themselves, which is the one thing Settings.File is for.
func (s Settings) FileNameFor(stateRemote string) string {
	if file := strings.TrimSpace(s.File); file != "" {
		return file
	}
	return FileName(stateRemote)
}

// sanitize keeps a path component to what is safe in a file name on any
// host and unambiguous in a repository listing. A component that is
// nothing but punctuation ends up as "x" rather than as the empty
// string, which would run two dashes together and name a different
// deployment's file.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "x"
	}
	return out
}

// repoFromRemote pulls owner and repo out of a git URL, in either of the
// two forms an operator pastes: an https URL or scp-style ssh. A URL
// nothing can be picked out of is a "no answer" rather than an error --
// the caller falls back to LocalFileName, which is also what a
// deployment with no remote at all gets.
func repoFromRemote(remote string) (owner, repo string, ok bool) {
	remote = strings.TrimSuffix(strings.TrimSpace(remote), ".git")
	if remote == "" {
		return "", "", false
	}
	path := remote
	if u, err := url.Parse(remote); err == nil && u.Host != "" {
		path = u.Path
	} else if at := strings.Index(remote, "@"); at >= 0 {
		if colon := strings.Index(remote[at:], ":"); colon >= 0 {
			path = remote[at+colon+1:]
		}
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 {
		return "", "", false
	}
	return parts[len(parts)-2], parts[len(parts)-1], true
}
