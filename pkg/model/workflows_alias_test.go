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

package model

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/workflows"
)

// pkg/workflows restates LifecycleImageEnv because it may not import this
// package; a job's key and its environment both read that copy, so the
// two must not drift.
func TestWorkflowsSpellsTheLifecycleImageVariableTheSameWay(t *testing.T) {
	if workflows.LifecycleImageEnv != LifecycleImageEnv {
		t.Fatalf("workflows.LifecycleImageEnv = %q, model.LifecycleImageEnv = %q", workflows.LifecycleImageEnv, LifecycleImageEnv)
	}
}
