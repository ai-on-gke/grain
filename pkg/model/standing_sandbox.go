package model

import (
	"fmt"
	"strings"
)

// StandingSandbox defines a long-standing sandbox managed by Grain.
//
// Long-standing sandboxes are defined in the grain/config state repository,
// backed by a Granule OCI image from an accessible docker repository,
// and configured with secrets resolved from Grain's encrypted secret store.
//
// Grain supervises standing sandboxes: starting them on system boot,
// monitoring their health, and automatically restarting them if they fail.
type StandingSandbox struct {
	// Name is the unique identifier of the standing sandbox (e.g. "preview-env", "staging-db").
	Name string `json:"name"`
	// Image is the Granule OCI image reference (e.g. "docker.io/repo/granule-runner:v1").
	Image string `json:"image"`
	// Command is the optional shell command or service entrypoint executed in the sandbox.
	Command string `json:"command,omitempty"`
	// Dir is the working directory for Command. Empty means the guest's default.
	Dir string `json:"dir,omitempty"`
	// Env is literal environment variables passed to the sandbox.
	Env map[string]string `json:"env,omitempty"`
	// Secrets is the list of secrets pulled from Grain's secret store and injected into the sandbox.
	Secrets []StandingSandboxSecret `json:"secrets,omitempty"`
	// CPUs is the CPU resource allocation (0 for default).
	CPUs int `json:"cpus,omitempty"`
	// MemoryMB is the memory resource allocation in MB (0 for default).
	MemoryMB int `json:"memory_mb,omitempty"`
	// DiskGB is the root disk size in GB (0 for default).
	DiskGB int `json:"disk_gb,omitempty"`
	// NetworkAccess defines the network isolation level.
	NetworkAccess NetworkAccess `json:"network_access,omitempty"`
	// Enabled determines whether Grain should maintain this sandbox running.
	Enabled bool `json:"enabled"`
}

// StandingSandboxSecret maps a secret from Grain's secret store into the sandbox.
type StandingSandboxSecret struct {
	// SandboxName is the parent standing sandbox identifier.
	SandboxName string `json:"sandbox_name"`
	// SecretName is the secret key in Grain's encrypted secret store (e.g. "gcp-credentials" or "db/password").
	SecretName string `json:"secret_name"`
	// EnvVar is the environment variable name to inject the resolved secret plaintext into.
	EnvVar string `json:"env_var,omitempty"`
	// FilePath is the file path inside the sandbox guest to write the secret plaintext to.
	FilePath string `json:"file_path,omitempty"`
}

// ValidateStandingSandboxName checks that a standing sandbox name is non-empty and well-formed.
func ValidateStandingSandboxName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("standing sandbox name cannot be empty")
	}
	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.') {
			return fmt.Errorf("standing sandbox name %q contains invalid characters (must be alphanumeric, '-', '_', or '.')", name)
		}
	}
	return nil
}

// ValidateStandingSandbox validates a standing sandbox definition.
func ValidateStandingSandbox(sb StandingSandbox) error {
	if err := ValidateStandingSandboxName(sb.Name); err != nil {
		return err
	}
	if strings.TrimSpace(sb.Image) == "" {
		return fmt.Errorf("standing sandbox %q requires a Granule OCI image", sb.Name)
	}
	for _, s := range sb.Secrets {
		if strings.TrimSpace(s.SecretName) == "" {
			return fmt.Errorf("standing sandbox %q has a secret with an empty secret name", sb.Name)
		}
		if strings.TrimSpace(s.EnvVar) == "" && strings.TrimSpace(s.FilePath) == "" {
			return fmt.Errorf("standing sandbox %q secret %q must specify either an env_var or a file_path", sb.Name, s.SecretName)
		}
	}
	return nil
}
