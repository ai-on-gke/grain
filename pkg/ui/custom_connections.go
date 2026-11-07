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

import "github.com/ai-on-gke/grain/pkg/model"

// removedCustomConnectionError is what a request naming the retired custom
// command framework (model.RemovedAgentFrameworkCustom) is refused with.
const removedCustomConnectionError = "custom command connections were removed in v3.2.0"

// findStoredBackend is the stored connection with id, or nil.
func findStoredBackend(backends []model.AgentBackend, id string) *model.AgentBackend {
	for i := range backends {
		if backends[i].ID == id {
			return &backends[i]
		}
	}
	return nil
}
