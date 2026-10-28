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

package model

import (
	"strings"
)

// GitConnectionCredentialPrefix starts the controller-side name of every
// credential grain mints for a deployment git connection: the prefix,
// "c-", and a random suffix. The token itself remains in the credential
// store's write-only secrets directory; a connection stores only this
// non-secret selector.
const GitConnectionCredentialPrefix = "grain-git-"

// legacyTeamCredentialPrefix started the names grain minted while
// connections belonged to teams: "grain-team-", the base64url of the
// team's id ("ZGVmYXVsdA" for default), and for a connection "-c-" and
// a random suffix. Those names are stored selectors -- on the settings
// row and as file names in the secrets directory -- so they are kept as
// they are and still read as the deployment's own; only new connections
// get GitConnectionCredentialPrefix.
const legacyTeamCredentialPrefix = "grain-team-"

// DeploymentOwnsGitCredential reports whether name is a credential grain
// minted for one of the deployment's git connections, under either
// spelling, and so one it removes when the connection goes. Any other
// name -- an ambient login, a credential an operator wrote by hand -- is
// left in place.
func DeploymentOwnsGitCredential(name string) bool {
	return strings.HasPrefix(name, GitConnectionCredentialPrefix) ||
		strings.HasPrefix(name, legacyTeamCredentialPrefix)
}

// GitCredentialStem is name without whichever prefix
// DeploymentOwnsGitCredential recognised, for deriving a paired name
// (the PAT beside an SSH key). The two spellings cannot collide: a new
// stem is "c-<hex>", and a legacy one starts with a base64url team id,
// which is never the single character "c".
func GitCredentialStem(name string) string {
	if stem, ok := strings.CutPrefix(name, GitConnectionCredentialPrefix); ok {
		return stem
	}
	return strings.TrimPrefix(name, legacyTeamCredentialPrefix)
}
