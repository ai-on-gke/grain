package mcp

import (
	"bytes"
	"fmt"
	"os"
)

// WriteSandboxEnvFile writes extra -- NewSandboxTools' NAME=VALUE entries
// -- to a new file only this process's user can read, and returns its
// path, for a forked "grain mcpserver" to read back with
// ReadSandboxEnvFile (its -sandbox-env-file).
//
// It exists so that those values never sit on that process's argv. An
// argv is world-readable for as long as the process lives
// (/proc/<pid>/cmdline, ps), and these entries are the operator's own
// -host-sandbox-env: a proxy URL with a password in it, a token-bearing
// variable (GSA1-12). The file is 0600 in dir (os.TempDir when empty),
// which is no more than this user can already read out of that
// process's own environment; removing it once the run is over is the
// caller's.
//
// Entries are NUL-terminated rather than newline-separated, so a value
// that carries a newline round-trips whole.
func WriteSandboxEnvFile(dir string, extra []string) (string, error) {
	if err := ValidateSandboxEnv(extra); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "grain-sandbox-env-*")
	if err != nil {
		return "", fmt.Errorf("creating sandbox environment file: %w", err)
	}
	path := f.Name()
	// CreateTemp already opens 0600; saying so again is what keeps it that
	// way under a umask or a platform that reads the mode differently.
	err = f.Chmod(0o600)
	for _, kv := range extra {
		if err != nil {
			break
		}
		_, err = f.WriteString(kv + "\x00")
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path)
		return "", fmt.Errorf("writing sandbox environment file: %w", err)
	}
	return path, nil
}

// ReadSandboxEnvFile reads back what WriteSandboxEnvFile wrote, checked by
// ValidateSandboxEnv.
func ReadSandboxEnvFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading sandbox environment file: %w", err)
	}
	var extra []string
	for _, kv := range bytes.Split(data, []byte{0}) {
		if len(kv) == 0 {
			continue
		}
		extra = append(extra, string(kv))
	}
	if err := ValidateSandboxEnv(extra); err != nil {
		return nil, fmt.Errorf("sandbox environment file %s: %w", path, err)
	}
	return extra, nil
}
