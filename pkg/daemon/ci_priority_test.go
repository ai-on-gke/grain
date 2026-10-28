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
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// A CI job's sandbox is the lowest class, so on a full Kubernetes cluster
// it is the pod an interactive session preempts.
func TestCiJobSandboxIsTheCIClass(t *testing.T) {
	t.Parallel()
	got := CiSandboxes{}.jobShape(ci.SandboxRequest{})
	if got.Class != orchestrator.SandboxClassCI {
		t.Errorf("CI job shape class = %q, want %q", got.Class, orchestrator.SandboxClassCI)
	}
}
