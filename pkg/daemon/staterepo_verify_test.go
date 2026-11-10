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

package daemon

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// require_verified mode's settings come from the remote route, live on
// this host, and build the Verifier the external pull runs through.
func TestRequireVerifiedSettingsBuildTheVerifier(t *testing.T) {
	dir := t.TempDir()
	on, two := true, 2
	mergers, signers := "acme-grain[bot], other", "alice@example.com ssh-ed25519 AAAAC3Nza"
	settings := staterepo.Settings{Remote: "https://github.com/acme/grain-config.git"}
	sha := strings.Repeat("a", 40)
	if err := applyVerification(dir, &settings, ui.StateRepoRemoteRequest{
		RequireVerified: &on, RequiredApprovals: &two, VerifiedMergers: &mergers, AllowedSigners: &signers, AcceptCommit: sha,
	}, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(settings.AllowedSignersFile)
	if err != nil || strings.TrimSpace(string(data)) != signers {
		t.Fatalf("allowed signers file = %q, %v; want %q", data, err, signers)
	}
	v := stateRepoVerifier(dir, settings)
	if v == nil || v.RequiredApprovals != 2 || !reflect.DeepEqual(v.Mergers, []string{"acme-grain[bot]", "other"}) ||
		!reflect.DeepEqual(v.Accepted, []string{sha}) || v.Reviews == nil {
		t.Fatalf("stateRepoVerifier = %+v", v)
	}
	// Another repository's acceptances do not carry over to this one.
	if err := applyVerification(dir, &settings, ui.StateRepoRemoteRequest{}, true); err != nil || settings.AcceptedCommits != "" {
		t.Fatalf("after repointing: accepted %q, %v; want none", settings.AcceptedCommits, err)
	}
	for _, bad := range []ui.StateRepoRemoteRequest{{AcceptCommit: "abc"}, {AllowedSigners: &mergers}} {
		if err := applyVerification(dir, &settings, bad, false); err == nil {
			t.Errorf("applyVerification(%+v) accepted it", bad)
		}
	}
	// Off, or internal, is no Verifier.
	settings.RequireVerified = false
	if v := stateRepoVerifier(dir, settings); v != nil {
		t.Errorf("stateRepoVerifier with require_verified off = %+v, want nil", v)
	}
}
