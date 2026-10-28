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

package deploy

import (
	"path/filepath"
	"testing"
)

// TestTheKubernetesManifestsKeepAgentSessionsOnTheDataClaim: a durable
// run the daemon does not adopt across a restart is resumed on the next
// daemon's first cycle into the conversation it was in
// (docs/design/durable-tasks.md#DT-1, case 2), and that conversation is
// only there if the session stores outlived the pod. agy's and codex's
// live under -data-dir (pkg/agent/sessionstore, daemon.agentSessionsDir)
// and claude's in $HOME/.claude, so both manifests have to put -data-dir
// on the grain-data claim and HOME under it.
func TestTheKubernetesManifestsKeepAgentSessionsOnTheDataClaim(t *testing.T) {
	for _, file := range []string{"grain.yaml", "grain-granule.yaml"} {
		t.Run(file, func(t *testing.T) {
			objects, nodes := manifestDocsForFile(t, file)
			dep := grainDeploymentFromDocs(t, objects, nodes)
			c := grainContainer(t, dep)
			dataDir := flagValue(t, daemonArgs(t, c), "data-dir")

			claimed := map[string]bool{}
			for _, v := range dep.Spec.Template.Spec.Volumes {
				if v.PersistentVolumeClaim.ClaimName == "grain-data" {
					claimed[v.Name] = true
				}
			}
			var claimMount string
			for _, m := range c.VolumeMounts {
				if claimed[m.Name] && under(filepath.Clean(dataDir), filepath.Clean(m.MountPath)) {
					claimMount = m.MountPath
				}
			}
			if claimMount == "" {
				t.Fatalf("-data-dir=%s is not on the grain-data claim: the agy and codex session stores under it "+
					"would go with the pod", dataDir)
			}
			var home string
			for _, e := range c.Env {
				if e.Name == "HOME" {
					home = e.Value
				}
			}
			if home == "" || !under(filepath.Clean(home), filepath.Clean(claimMount)) {
				t.Errorf("HOME=%q is not on the grain-data claim (%s): claude's session store, $HOME/.claude, "+
					"would go with the pod", home, claimMount)
			}
		})
	}
}
