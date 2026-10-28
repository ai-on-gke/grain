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

//go:build grain_e2e

package daemon

import (
	"errors"
	"fmt"
	"os"

	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// The browser e2e suite (ui/e2e/global-setup.js) builds grain with the
// grain_e2e tag so a spec that boots a real daemon can dispatch a run
// without a container runtime of its own. See LocalSandboxes.
func init() { LocalSandboxes = newLocalSandboxes }

func newLocalSandboxes(dir string) (orchestrator.Sandboxes, error) {
	if dir == "" {
		return nil, errors.New("-sandbox-dir is required for local e2e sandboxes")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating sandbox directory: %w", err)
	}
	return orchestrator.NewHostSandboxes(dir), nil
}
