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

package gitproxy

import (
	"slices"
	"testing"
)

func TestRegisterAmbientSourceAddsAndRemovesASource(t *testing.T) {
	unregister := RegisterAmbientSource(AmbientCommandSource{
		Name:     "madeup",
		Command:  "madeup-cli",
		LoginFix: "madeup-cli login --host {host}",
		Hosts:    []string{"forge.madeup.test"},
	})
	if got := AmbientAutoSource("forge.madeup.test"); got != "madeup" {
		t.Errorf("AmbientAutoSource = %q, want the registered source for the host it claims", got)
	}
	if got := AmbientLoginFix("forge.madeup.test", AmbientSourceAuto); got != "madeup-cli login --host forge.madeup.test" {
		t.Errorf("AmbientLoginFix = %q", got)
	}
	if got := AmbientAutoSource("other.madeup.test"); got != AmbientSourceGH {
		t.Errorf("AmbientAutoSource(unclaimed) = %q, want gh", got)
	}
	if AmbientForgeViaCommand("forge.madeup.test", "madeup") {
		t.Error("a source without ForgeViaCommand is command-backed")
	}
	names := AmbientSourceNames()
	if i, j := slices.Index(names, "madeup"), slices.Index(names, AmbientSourceGH); i < 0 || j < 0 || i > j {
		t.Errorf("AmbientSourceNames = %q, want the registered source ahead of gh", names)
	}
	if s, err := NormalizeAmbientSource("MadeUp"); err != nil || s != "madeup" {
		t.Errorf("NormalizeAmbientSource = %q, %v", s, err)
	}

	unregister()
	if _, err := NormalizeAmbientSource("madeup"); err == nil {
		t.Error("an unregistered source is still accepted")
	}
	if got := AmbientAutoSource("forge.madeup.test"); got != AmbientSourceGH {
		t.Errorf("after unregistering, AmbientAutoSource = %q, want gh", got)
	}
}

func TestRegisterAmbientSourceRefusesTakenNames(t *testing.T) {
	for _, name := range []string{AmbientSourceGH, AmbientSourceGit, AmbientSourceAuto, testForgeSource, ""} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("registering %q did not panic", name)
				}
			}()
			RegisterAmbientSource(AmbientCommandSource{Name: name, Command: "x"})
		}()
	}
}
