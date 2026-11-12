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
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// refuseCompositionSandboxAboveMax refuses a composition whose sandbox
// shape (Composition.Sandbox, a template's own Template.Sandbox) is above
// this deployment's maximum (SandboxShapeLimits.Max).
//
// ValidateTemplateSandbox holds a template to grain's default ceiling
// when it is written, because neither a repository's declaration nor the
// state repository has the deployment's settings to hand; an operator may
// have set a lower maximum, or lowered it since. ui.Client asks the
// maximum of every filing, approval and run-now that passes through it,
// but a schedule, suite, path trigger or qualification run files its tasks
// approved straight through the store, so this is asked here instead --
// refusing rather than clamping, as everything else that holds a shape to
// that maximum does.
// RefuseCompositionSandboxAboveMax is refuseCompositionSandboxAboveMax for
// a filer outside this package that composes a template's task itself and
// files it approved without passing through ui.Client
// (orchestrator.reconcileReleaseNotes).
func (s *Store) RefuseCompositionSandboxAboveMax(ctx context.Context, comp Composition) error {
	return refuseCompositionSandboxAboveMax(ctx, s.db, comp)
}

func refuseCompositionSandboxAboveMax(ctx context.Context, q querier, comp Composition) error {
	if comp.Sandbox.IsZero() {
		return nil
	}
	var text sql.NullString
	err := q.QueryRowContext(ctx, "SELECT `sandbox_shape_limits` FROM `grain_config` WHERE `id` = 1").Scan(&text)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("reading sandbox_shape_limits: %w", err)
	}
	limits, err := DecodeSandboxShapeLimits(text.String)
	if err != nil {
		return err
	}
	if above := comp.Sandbox.Above(limits.ResolvedMax()); len(above) > 0 {
		return fmt.Errorf("template %s asks for a sandbox above this deployment's maximum: %s; lower the "+
			"template's sandbox, or have an operator raise sandboxShapeLimits in settings",
			comp.TemplateID, strings.Join(above, ", "))
	}
	return nil
}
