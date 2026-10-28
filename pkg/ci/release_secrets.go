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
