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

import "testing"

// IsEmbeddedBootstrapOnlyGrant is what lets a grant through on a sandbox
// that is a directory on grain's own host (hasExternalCapabilityRef,
// onlyEmbeddedBootstrapPlacements), so it answers true only for arguments
// that provably select nothing but the embedded bootstrap map: no operator
// config map or secret, and nothing malformed the mint would read past.
func TestIsEmbeddedBootstrapOnlyGrant(t *testing.T) {
	skill := SkillCapabilityServerID + "#skill"
	for grant, want := range map[string]bool{
		BootstrapPlaybooksGrant: true,
		ConfigMapRef + `={"keys":["bootstrap/host-migration.md"]}`:      true,
		ConfigMapRef + `={"config_maps":["bootstrap"],"secrets":[]}`:    true,
		skill + `={"skills":["bootstrap"]}`:                             true,
		"bootstrap-playbooks":                                           false,
		ConfigMapRef:                                                    false,
		ConfigMapRef + `={}`:                                            false,
		ConfigMapRef + `={"config_maps":[]}`:                            false,
		ConfigMapRef + `={"config_maps":["bootstrap","tools"]}`:         false,
		ConfigMapRef + `={"config_maps":["bootstrap"],"secrets":["t"]}`: false,
		ConfigMapRef + `={"keys":["bootstrap/a.md","tools/b"]}`:         false,
		// A non-string beside a string: the mint skips the non-string
		// and places the string's map, so the list is not "empty".
		ConfigMapRef + `={"config_maps":["bootstrap"],"secrets":["t",1]}`: false,
		ConfigMapRef + `={"config_maps":["bootstrap"],"secrets":"t"}`:     false,
		ConfigMapRef + `={"config_maps":["bootstrap",1]}`:                 false,
		ConfigMapRef + `={"config_maps":["bootstrap"],"other":["x"]}`:     false,
		skill: false,
		skill + `={"skills":["bootstrap","ops"]}`:    false,
		skill + `={"skills":["bootstrap"],"x":1}`:    false,
		skill + `={"skills":["bootstrap",1]}`:        false,
		ModelProxyRef + `={"vendors":["anthropic"]}`: false,
	} {
		if got := IsEmbeddedBootstrapOnlyGrant(grant); got != want {
			t.Errorf("IsEmbeddedBootstrapOnlyGrant(%s) = %v, want %v", grant, got, want)
		}
	}
}
