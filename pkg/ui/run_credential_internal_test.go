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

package ui

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A scope dangerous enough that only a person may grant it is never one a
// run credential reaches as the deployment: a new dangerous scope joins
// the cap, or this fails.
func TestRunCredentialOperatorScopesCoverEveryDangerousScope(t *testing.T) {
	for _, info := range model.APIScopes() {
		if info.Dangerous && !runCredentialOperatorScopes[info.Scope] {
			t.Errorf("dangerous scope %s is missing from runCredentialOperatorScopes", info.Scope)
		}
	}
	known := map[APIScope]bool{}
	for _, info := range model.APIScopes() {
		known[info.Scope] = true
	}
	for scope := range runCredentialOperatorScopes {
		if !known[scope] {
			t.Errorf("runCredentialOperatorScopes names %s, which is not a scope of the vocabulary", scope)
		}
	}
	for pattern := range runCredentialOwnTaskRoutes {
		if !runCredentialOperatorScopes[routeScopes[pattern]] {
			t.Errorf("runCredentialOwnTaskRoutes names %s, whose scope %q the cap does not hold", pattern, routeScopes[pattern])
		}
	}
}
