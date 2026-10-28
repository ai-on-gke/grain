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
	"strings"
	"testing"
)

func TestTheKubernetesGitHubWebhookManifestIsUnprivilegedAndIsolated(t *testing.T) {
	objects, nodes := manifestDocsForFile(t, "github-webhook.yaml")
	want := []string{"ServiceAccount", "PersistentVolumeClaim", "Deployment", "BackendConfig", "Service"}
	var got []string
	for _, obj := range objects {
		got = append(got, obj.Kind)
		if obj.Metadata.Namespace != "grain" {
			t.Errorf("%s/%s is in namespace %q, want %q", obj.Kind, obj.Metadata.Name, obj.Metadata.Namespace, "grain")
		}
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("github-webhook.yaml ships %v; want %v", got, want)
	}

	var dep k8sDeployment
	found := false
	for i, obj := range objects {
		if obj.Kind == "Deployment" {
			if err := nodes[i].Decode(&dep); err != nil {
				t.Fatalf("decoding Deployment/grain-webhook: %v", err)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("github-webhook.yaml has no Deployment")
	}
	sc := dep.Spec.Template.Spec.SecurityContext
	if sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot {
		t.Error("grain-webhook pod does not set runAsNonRoot: true")
	}
	if sc.RunAsUser == nil || *sc.RunAsUser == 0 {
		t.Error("grain-webhook pod runs as root")
	}
	if len(dep.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("grain-webhook containers = %d, want 1", len(dep.Spec.Template.Spec.Containers))
	}
	c := dep.Spec.Template.Spec.Containers[0]
	if len(c.Command) != 1 || c.Command[0] != "/usr/local/bin/github-webhook" {
		t.Errorf("grain-webhook command = %v, want [/usr/local/bin/github-webhook]", c.Command)
	}
	if !strings.Contains(read(t, "Dockerfile"), "COPY --from=build /out/github-webhook /usr/local/bin/github-webhook") {
		t.Error("Dockerfile does not copy /usr/local/bin/github-webhook into the runtime image")
	}
}
