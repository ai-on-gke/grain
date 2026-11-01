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
	"context"

	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// sandboxProbeAdapter is ui.Config.SandboxProbe over
// orchestrator.ProbeSandbox and the one backend run() built, converting
// field for field for sandboxRecreateAdapter's reason. A backend that
// cannot be probed (anything but -k8s-granule-sandboxes) answers with
// orchestrator.ErrSandboxProbeUnsupported rather than a 404, so the
// caller learns why.
//
// The probe is admitted against the budget as it is configured now
// (config, which is LiveConfig.Current), counted by meter and prepared
// with cache -- the steps every other sandbox this deployment builds
// goes through.
type sandboxProbeAdapter struct {
	sandboxes orchestrator.Sandboxes
	meter     orchestrator.SandboxMeter
	cache     orchestrator.PackageCacheSource
	config    func() Config
}

func (a sandboxProbeAdapter) ProbeSandbox(ctx context.Context, req ui.SandboxProbeRequest) (ui.SandboxProbeResult, error) {
	deps := orchestrator.SandboxProbeDeps{Sandboxes: a.sandboxes, Meter: a.meter, PackageCache: a.cache}
	if a.config != nil {
		c := a.config()
		deps.Ledger, deps.Budget, deps.Shape = c.Ledger, c.Budget(), c.AgentShape()
	}
	got, err := orchestrator.ProbeSandbox(ctx, deps, orchestrator.SandboxProbeRequest{
		ID:     req.ID,
		Access: req.Access,
		Argv:   req.Argv,
	})
	if err != nil {
		return ui.SandboxProbeResult{}, err
	}
	return ui.SandboxProbeResult{
		Sandbox:  got.Sandbox,
		Pod:      got.Pod,
		Access:   got.Access,
		Stdout:   got.Stdout,
		Stderr:   got.Stderr,
		ExitCode: got.ExitCode,
	}, nil
}
