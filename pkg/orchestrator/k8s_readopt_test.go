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

package orchestrator

import (
	"context"
	"path/filepath"
	"testing"
)

// The Kubernetes backends take back a sandbox pod a previous process
// built, so the startup reap spares it for the run being adopted, and
// refuse a name with no grain sandbox pod behind it.
func TestK8sBackendsReadoptTheirPods(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	kubectl := filepath.Join(dir, "kubectl")
	plain := NewK8sSandboxes(K8sConfig{Image: "img", Kubectl: kubectl})
	granule := NewK8sGranuleSandboxes(K8sGranuleConfig{Image: "img", Kubectl: kubectl})
	plainPod, err := plain.PodNameFor("4-1")
	if err != nil {
		t.Fatal(err)
	}
	granulePod, err := granule.PodNameFor("4-1")
	if err != nil {
		t.Fatal(err)
	}
	// Every sandbox pod either backend could have built for run 4-1, and
	// one stray.
	writePATHStub(t, dir, "kubectl", "#!/bin/sh\ncase \"$*\" in\n"+
		"*'get pods'*) printf '"+plainPod+"\\n"+granulePod+"\\nstray\\n' ;;\nesac\n")
	ctx := context.Background()

	for _, backend := range []interface {
		SandboxReadopter
		PodNameFor(string) (string, error)
		ReapOrphans(context.Context) (int, error)
	}{plain, granule} {
		pod, err := backend.PodNameFor("4-1")
		if err != nil {
			t.Fatal(err)
		}
		sb, err := backend.Readopt(ctx, "4-1")
		if err != nil {
			t.Fatalf("%T.Readopt: %v", backend, err)
		}
		if sb.Name() != "4-1" || sb.(interface{ PodName() string }).PodName() != pod {
			t.Fatalf("%T readopted %s (pod %s), want 4-1 in %s", backend, sb.Name(), sb.(interface{ PodName() string }).PodName(), pod)
		}
		if reaped, err := backend.ReapOrphans(ctx); err != nil || reaped != 2 {
			t.Fatalf("%T reaped %d (%v), want every pod but the readopted %s", backend, reaped, err, pod)
		}
		if _, err := backend.Readopt(ctx, "5-1"); err == nil {
			t.Fatalf("%T readopted a sandbox with no pod", backend)
		}
	}
}
