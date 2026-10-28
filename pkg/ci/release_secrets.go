package ci

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
	"strings"
)

// ReleaseSecretsDir is the fixed directory in a release job's sandbox where
// repository-scoped custom secrets are materialized. Each configured name is a
// mode-0600 file below this directory; values never enter a workflow, plan,
// run record, command line, or environment.
const ReleaseSecretsDir = "/home/debian/.grain-release-secrets"

// ReleaseSecretAddress returns the encrypted-store secret and key used for one
// repository-scoped value. The repository is hashed so owner/name punctuation
// cannot become a path and similarly named repositories cannot collide.
func ReleaseSecretAddress(repo, name string) (secret, key string) {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(repo))))
	return "repo-release-" + hex.EncodeToString(sum[:]), name
}

// ReleaseSecretPath is where a release script reads one configured value.
func ReleaseSecretPath(name string) string {
	return path.Join(ReleaseSecretsDir, name)
}
