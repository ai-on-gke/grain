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

package staterepo

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// ModeMirror keeps grain/config controller-local while mirroring commits to
	// and from Remote (for example over a per-instance deploy key).
	ModeMirror = "mirror"
	// ModeExternal uses Remote as a read-only external IaC source of truth.
	ModeExternal = "external"
)

// Settings holds the host-side options for the controller-local config
// repository and its optional external Git mirror.
type Settings struct {
	// Remote is the optional external Git repository URL that grain/config
	// mirrors to (pulling upstream commits and pushing local exports and
	// merged grain/config pull requests). Empty keeps grain/config on this
	// host only.
	Remote string `json:"remote,omitempty"`
	// Branch is the branch state lives on; DefaultBranch when empty.
	Branch string `json:"branch,omitempty"`
	// Mode selects how Remote is used when non-empty: "mirror" keeps
	// grain/config controller-local while pushing/pulling to Remote; empty or
	// "external" uses Remote in read-only external IaC mode.
	Mode string `json:"mode,omitempty"`
	// TokenFile is an optional path to a file containing a personal access
	// token for Remote. When empty, Grain uses the GitHub connection in its
	// store (or GRAIN_STATE_REPO_TOKEN / GITHUB_TOKEN).
	TokenFile string `json:"tokenFile,omitempty"`
	// SSHKeyFile is an optional path to an SSH private key (such as a
	// per-instance GitHub deploy key at /run/grain-secrets/config-repo-key)
	// used for SSH operations against Remote.
	SSHKeyFile string `json:"sshKeyFile,omitempty"`
	// CheckImage is the container image agents use to validate proposed
	// grain/config changes. Empty selects the current Grain build.
	CheckImage string `json:"checkImage,omitempty"`
	// DirectConfigWrites lets the HTTP API write non-bootstrap
	// configuration straight into the database in internal mode, as it did
	// before grain/config became the only write path for it
	// (design/design/config-repo-settings.design.md, section 4.1.1). It is a
	// host-local escape hatch for an operator mid-migration and for test
	// harnesses that configure a real daemon through its API; it has no
	// effect in external mode, which always refuses.
	DirectConfigWrites bool `json:"directConfigWrites,omitempty"`
	// RequireVerified is require_verified mode for an external config
	// repository (verify.go): a commit there is applied only if grain
	// can vouch for it -- the merge of a pull request approved on the
	// forge (RequiredApprovals) or merged by one of VerifiedMergers, a
	// commit signed by a key in AllowedSignersFile, or one an operator
	// accepted (AcceptedCommits). Anything else is held. It lives here,
	// on this host, and not in the repository it guards: a writer there
	// must not be able to turn it off.
	RequireVerified bool `json:"requireVerified,omitempty"`
	// RequiredApprovals is how many forge approvals a pull request needs;
	// one when zero.
	RequiredApprovals int `json:"requiredApprovals,omitempty"`
	// VerifiedMergers are the forge accounts grain merges pull requests
	// as, comma-separated.
	VerifiedMergers string `json:"verifiedMergers,omitempty"`
	// AllowedSignersFile is an ssh allowed_signers file of the keys whose
	// signed commits verify.
	AllowedSignersFile string `json:"allowedSignersFile,omitempty"`
	// AcceptedCommits are commits an operator holding an unlimited role
	// accepted by name, comma-separated.
	AcceptedCommits string `json:"acceptedCommits,omitempty"`
	// These fields belonged to external-mirror workflow and warning
	// behavior. They remain only so older settings files still decode.
	NoWorkflow            bool `json:"noWorkflow,omitempty"`
	PostCheck             bool `json:"postCheck,omitempty"`
	LocalOnlyAcknowledged bool `json:"localOnlyAcknowledged,omitempty"`
}

// IsMirror reports whether s configures a controller-local grain/config
// repository that mirrors commits to Remote.
func (s Settings) IsMirror() bool {
	return strings.TrimSpace(s.Remote) != "" && strings.EqualFold(strings.TrimSpace(s.Mode), ModeMirror)
}

// IsExternal reports whether s configures read-only external IaC mode against
// Remote.
func (s Settings) IsExternal() bool {
	return strings.TrimSpace(s.Remote) != "" && !s.IsMirror()
}

// SettingsFileName is what the file is called in the data directory.
const SettingsFileName = "state-repo.json"

// LoadSettings reads the settings beside dataDir. A file that is not
// there is not an error and not a prompt: it is the local-only
// deployment, and returning the zero value is what lets `grain daemon`
// start on a machine nobody has configured.
func LoadSettings(dataDir string) (Settings, error) {
	path := filepath.Join(dataDir, SettingsFileName)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("staterepo: reading %s: %w", path, err)
	}
	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		return Settings{}, fmt.Errorf("staterepo: %s is not valid JSON: %w", path, err)
	}
	return s, nil
}

// SaveSettings writes the settings, atomically so a daemon reading them
// at the same moment sees one version or the other and never half a
// file.
func SaveSettings(dataDir string, s Settings) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("staterepo: preparing %s: %w", dataDir, err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := filepath.Join(dataDir, SettingsFileName)
	tmp, err := os.CreateTemp(dataDir, ".state-repo-*")
	if err != nil {
		return fmt.Errorf("staterepo: creating a temporary file next to %s: %w", path, err)
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
