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

package daemon_test

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A CI job is judged as a CI job of its repository: a capability the
// catalogue keeps off that repository is refused before it is minted,
// and grain-ci, which every job carries, is never judged.
func TestCheckCIJobCatalogue(t *testing.T) {
	cat := &model.CapabilityCatalogue{InProcess: map[string]model.CatalogueInProcess{
		"registry-push": {Restrictions: &model.CapabilityRestrictions{
			Repos: model.Patterns{"acme/app"}, Kinds: model.Patterns{"ci"}, CIOnly: true,
		}},
		grainci.CapabilityName: {Enabled: new(bool)},
	}}
	ok := ci.SandboxRequest{Repo: "acme/app", Capabilities: []string{grainci.CapabilityName, "registry-push"}}
	if err := daemon.CheckCIJobCatalogue(cat, ok); err != nil {
		t.Fatalf("admitted job refused: %v", err)
	}
	other := ci.SandboxRequest{Repo: "acme/other", Capabilities: []string{"registry-push"}}
	if err := daemon.CheckCIJobCatalogue(cat, other); err == nil {
		t.Fatal("a job on a repository the catalogue keeps the capability off was admitted")
	}
	if err := daemon.CheckCIJobCatalogue(nil, other); err != nil {
		t.Fatalf("no catalogue refused a job: %v", err)
	}
}
