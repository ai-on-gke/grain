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

package ui_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/gitproxy/gitproxytest"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// The connection editor's command-backed forge comes from the registered
// source, not from anything pkg/ui or the frontend knows: its label, its
// help and its host presets are what the registration says, and a source
// registered without UI is offered by its command.
func TestGitAmbientSourcesAreTheRegisteredSources(t *testing.T) {
	srv, _ := testServer(t)
	rec := do(t, srv, http.MethodGet, "/api/git-ambient-sources", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/git-ambient-sources: %d %s", rec.Code, rec.Body)
	}
	var got []ui.GitAmbientSource
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	var forge *ui.GitAmbientSource
	for i := range got {
		if got[i].Name == gitproxy.AmbientSourceGH || got[i].Name == gitproxy.AmbientSourceGit {
			t.Errorf("core source %q listed as a registered one: %+v", got[i].Name, got)
		}
		if got[i].Name == gitproxytest.Source {
			forge = &got[i]
		}
	}
	if forge == nil {
		t.Fatalf("sources = %+v, want %s", got, gitproxytest.Source)
	}
	if forge.Command != gitproxytest.Command || forge.Label != gitproxytest.Label || !forge.ForgeViaCommand ||
		len(forge.Hosts) != 1 || forge.Hosts[0] != "*."+gitproxytest.Domain || forge.Help == "" {
		t.Errorf("%s = %+v", gitproxytest.Source, *forge)
	}
	if len(forge.Presets) != 1 || forge.Presets[0] != (ui.GitAmbientSourcePreset{Host: gitproxytest.Host, Label: gitproxytest.PresetLabel}) {
		t.Errorf("%s presets = %+v", gitproxytest.Source, forge.Presets)
	}

	unregister := gitproxy.RegisterAmbientSource(gitproxy.AmbientCommandSource{Name: "plaincli", Command: "plaincli"})
	t.Cleanup(unregister)
	for _, src := range ui.GitAmbientSources() {
		if src.Name == "plaincli" {
			if src.Label != "plaincli" || src.ForgeViaCommand || len(src.Presets) != 0 || src.Help != "" {
				t.Errorf("a source registered without UI = %+v, want it offered by its command", src)
			}
			return
		}
	}
	t.Error("a source registered without UI is not offered")
}
