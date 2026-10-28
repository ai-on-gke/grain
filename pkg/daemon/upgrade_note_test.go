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
	"strings"
	"testing"
)

// On Kubernetes the startup line about upgrades named an empty ref file
// and said nothing restarts, when the upgrade repoints the Deployment and
// the rollout is the restart
// (docs/test-runs/2026-10-15-k8s-run-hosts-sandboxes.md, F5).
func TestUpgradeImageNoteNamesTheDeploymentOnKubernetes(t *testing.T) {
	kube := upgradeImageNote(Config{K8sSandboxes: true, UpgradeImage: "ghcr.io/bwsalmon/grain/grain", K8sDeployment: "grain", K8sContainer: "grain"})
	for _, want := range []string{"deployment/grain", "container grain", "ghcr.io/bwsalmon/grain/grain:<branch>"} {
		if !strings.Contains(kube, want) {
			t.Errorf("Kubernetes upgrade note %q does not say %q", kube, want)
		}
	}
	if strings.Contains(kube, "rewrite") || strings.Contains(kube, "nothing restarts") {
		t.Errorf("Kubernetes upgrade note %q still describes the host pipeline", kube)
	}
	host := upgradeImageNote(Config{UpgradeImage: "ghcr.io/bwsalmon/grain/grain", UpgradeImageRefFile: "/var/lib/grain/data/image-ref"})
	if !strings.Contains(host, "rewrite /var/lib/grain/data/image-ref") {
		t.Errorf("host upgrade note %q lost its ref file", host)
	}
}
