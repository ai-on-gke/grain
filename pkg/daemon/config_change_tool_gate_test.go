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

	"github.com/ai-on-gke/grain/pkg/ui"
)

// propose_config_change is offered only where the orchestrator makes the
// grain-state checkout its script starts in: a repo-less task holding
// grain/config in its Writes.
func TestProposeConfigChangeOfferedOnlyToRepolessConfigWriters(t *testing.T) {
	t.Parallel()
	writes := []ui.WriteTarget{{Repo: "grain/config"}}
	for _, tc := range []struct {
		name string
		task ui.Task
		want bool
	}{
		{"session writing grain/config", ui.Task{ConsoleSession: "ops-chat", Interactive: true, Writes: writes}, true},
		{"repo-less task writing grain/config", ui.Task{Writes: writes}, true},
		{"session writing another repo", ui.Task{ConsoleSession: "ops-chat", Writes: []ui.WriteTarget{{Repo: "acme/widgets"}}}, false},
		{"repo task writing grain/config", ui.Task{Repo: "acme/widgets", Writes: writes}, false},
	} {
		if got := writesConfigRepo(tc.task); got != tc.want {
			t.Errorf("%s: writesConfigRepo = %v, want %v", tc.name, got, tc.want)
		}
	}
}
