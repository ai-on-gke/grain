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

package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The ceilings on a task's own sandbox shape (model.SandboxShapeLimits,
// grain_config.sandbox_shape_limits):
//
//   - the deployment-wide maximum binds everybody, an unlimited role
//     included, and is asked by the Client itself on create, PATCH,
//     approve and run-now (refuseSandboxShapeAboveMax, a 400);
//   - a limited role is held to its own ceiling beneath that, on every
//     body it sends (filingEscalation) and every stored task it approves,
//     runs or edits (roleTaskWithin), a 403;
//   - an agent's proposal may not ask for a larger sandbox than its own
//     task runs in (model.ProposalSandboxShapeAbove): through propose_task
//     it is filed held for a person, through the API it is refused, and a
//     limited role cannot approve it.
//
// None of them clamps: a request above a ceiling is refused, saying what
// it asked for and what the ceiling is. The one exception is a proposal
// relayed at the end of a run (orchestrator.clampProposalSandboxShape),
// whose run is no longer there to read a refusal: it is lowered to the
// deployment's maximum, and its body says so.

// sandboxShapeLimits is this deployment's stored limits; none (grain's
// defaults) when there is no settings row to read.
func (c *Client) sandboxShapeLimits(ctx context.Context) model.SandboxShapeLimits {
	if c == nil || c.Store == nil {
		return model.SandboxShapeLimits{}
	}
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil || cfg == nil {
		return model.SandboxShapeLimits{}
	}
	return cfg.SandboxShapeLimits
}

// refuseSandboxShapeAboveMax refuses a sandbox shape above this
// deployment's maximum, whoever asks.
func (c *Client) refuseSandboxShapeAboveMax(ctx context.Context, shape model.SandboxShape) error {
	if shape.IsZero() {
		return nil
	}
	if above := shape.Above(c.sandboxShapeLimits(ctx).ResolvedMax()); len(above) > 0 {
		return validationErrorf("sandbox shape is above this deployment's maximum: %s; ask for less, or have an "+
			"operator raise sandboxShapeLimits in settings", strings.Join(above, ", "))
	}
	return nil
}

// roleSandboxShapeRefusal is why limited role a may not ask for shape,
// nil when it may. what names the request ("set sandboxCpus", "approve
// task 12").
func (c *Client) roleSandboxShapeRefusal(ctx context.Context, a *accessRole, shape model.SandboxShape, what string) error {
	if a == nil || a.Full || shape.IsZero() {
		return nil
	}
	ceiling := c.sandboxShapeLimits(ctx).RoleCeiling(a.Roles)
	if above := shape.Above(ceiling); len(above) > 0 {
		return fmt.Errorf("%s cannot %s: the sandbox shape is above the role's ceiling (%s); a larger sandbox "+
			"requires an unlimited role (grain/operator) or a higher role ceiling in settings (sandboxShapeLimits)",
			a.label(), what, strings.Join(above, ", "))
	}
	return nil
}

func (s *Server) roleSandboxShapeRefusal(ctx context.Context, a *accessRole, shape model.SandboxShape, what string) error {
	return s.tasks.roleSandboxShapeRefusal(ctx, a, shape, what)
}

// sandboxShapeKey says which dimension a body key sets, matched the way
// encoding/json matches keys (without regard to case), plus the
// snake_case spelling propose_task and the CLI use.
func sandboxShapeKey(key string) (dim string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "sandboxcpus", "sandbox_cpus":
		return "cpus", true
	case "sandboxmemorymb", "sandbox_memory_mb":
		return "memory", true
	case "sandboxdiskgb", "sandbox_disk_gb":
		return "disk", true
	}
	return "", false
}

// sandboxShapeOfField is the shape one body field asks for, zero when it
// is not a sandbox field or does not hold a number.
func sandboxShapeOfField(key string, raw json.RawMessage) model.SandboxShape {
	dim, ok := sandboxShapeKey(key)
	if !ok {
		return model.SandboxShape{}
	}
	var n int
	if json.Unmarshal(raw, &n) != nil || n <= 0 {
		return model.SandboxShape{}
	}
	switch dim {
	case "cpus":
		return model.SandboxShape{CPUs: n}
	case "memory":
		return model.SandboxShape{MemoryMB: n}
	default:
		return model.SandboxShape{DiskGB: n}
	}
}
