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

package upgrade

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/k8s"
)

// An upgrade makes sure the PriorityClasses sandbox pods name exist --
// creating grain's own where the cluster has none -- before it repoints
// the Deployment, so a cluster set up before grain shipped them is not
// left refusing every pod the new build creates.
func TestStageKubeCreatesMissingPriorityClassesBeforeTheRollout(t *testing.T) {
	inner, calls, _ := fakeKubectl(t, kubeSucceeded, "")
	dir := t.TempDir()
	created := filepath.Join(dir, "created")
	script := "#!/bin/sh\n" +
		"if [ \"$3 $4\" = 'get priorityclass' ]; then\n" +
		"  echo \"Error from server (NotFound): priorityclasses.scheduling.k8s.io $5 not found\" >&2; exit 1\n" +
		"fi\n" +
		"if [ \"$3\" = create ]; then sed -n 's/^  name: //p' >> " + created + "; fi\n" +
		"exec " + inner + " \"$@\"\n"
	kubectl := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	u := kubeUpgrader(t, kubectl, []string{"schema-version"})
	u.cfg.Kube.PriorityClasses = []string{k8s.PriorityClassInteractive, k8s.PriorityClassAgent, k8s.PriorityClassCI, ""}
	p := newProgress(u.cfg.StatusFile, "main", Status{}, u.plan())
	defer p.stop()

	if _, err := u.stageKube(context.Background(), p, "main"); err != nil {
		t.Fatalf("stageKube: %v", err)
	}
	data, _ := os.ReadFile(created)
	if got := strings.Fields(string(data)); strings.Join(got, ",") != "grain-interactive,grain-agent,grain-ci" {
		t.Errorf("created PriorityClasses %v, want grain's three", got)
	}
	log := calls()
	create, rollout := strings.Index(log, " create "), strings.Index(log, " set image ")
	if create < 0 || rollout < 0 || create > rollout {
		t.Errorf("want the PriorityClasses created before the Deployment is repointed; calls: %s", log)
	}
}
