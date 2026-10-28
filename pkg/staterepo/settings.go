package staterepo

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	// TokenFile is an optional path to a file containing a personal access
	// token for Remote. When empty, Grain uses the GitHub connection in its
	// store (or GRAIN_STATE_REPO_TOKEN / GITHUB_TOKEN).
	TokenFile string `json:"tokenFile,omitempty"`
	// CheckImage is the container image agents use to validate proposed
	// grain/config changes. Empty selects the current Grain build.
	CheckImage string `json:"checkImage,omitempty"`
	// These fields belonged to external-mirror workflow and warning
	// behavior. They remain only so older settings files still decode.
	NoWorkflow            bool `json:"noWorkflow,omitempty"`
	PostCheck             bool `json:"postCheck,omitempty"`
	LocalOnlyAcknowledged bool `json:"localOnlyAcknowledged,omitempty"`
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
