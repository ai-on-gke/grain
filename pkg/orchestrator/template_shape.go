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
	"log"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A template's `sandbox:` size on a backend with no size to give
// (docs/test-runs/2026-11-13-schedules-templates.md, F2).
//
// HostSandboxes refuses a run that asks for a size, because a directory
// on the host has none of its own: a task that asked for 60 GiB would
// silently get the host's disk instead. That is right for a size a person
// stated on a task. It is wrong for one the task only carries because the
// template it was filed from says `sandbox:` (model.Template.Sandbox,
// grain/task-4591): nobody asked this deployment for that size, and the
// form already leaves it off a task filed by hand there
// (ui.Config.SandboxShapeIgnored, applyTemplate). Every other filing
// point -- a schedule's firing, a suite, a path trigger, a run's proposal
// -- copies it on regardless, so before this every firing of such a
// template on a host-directory deployment failed its setup and retried
// for ever.

// shapelessSandboxes is a sandbox backend that has no shape of its own
// to give a run (HostSandboxes).
type shapelessSandboxes interface {
	Shapeless() bool
}

// isShapeless reports whether sandboxes refuses every sandbox size.
func isShapeless(sandboxes Sandboxes) bool {
	s, ok := sandboxes.(shapelessSandboxes)
	return ok && s.Shapeless()
}

// templateShapeDropped returns shape without its size when the backend
// has none to give and every dimension the task states is the one its
// template states -- the size came from the template rather than from
// anybody filing the task -- and whether it dropped it. A dimension the
// task states that the template does not, or states differently, is a
// person's, and the shape is left for the backend to refuse as before.
func templateShapeDropped(ctx context.Context, store *model.Store, sandboxes Sandboxes,
	task model.Task, shape Shape) (Shape, bool) {

	if shape.SizeIsZero() || !isShapeless(sandboxes) || store == nil {
		return shape, false
	}
	id := task.TemplateID
	if id == "" {
		id = task.SourceTemplateID
	}
	if id == "" {
		return shape, false
	}
	tmpl, err := store.GetTemplate(ctx, id)
	if err != nil || tmpl == nil || tmpl.Sandbox.IsZero() {
		return shape, false
	}
	fromTemplate := func(task, tmpl int) bool { return task == 0 || task == tmpl }
	if !fromTemplate(shape.CPUs, tmpl.Sandbox.CPUs) || !fromTemplate(shape.MemoryMB, tmpl.Sandbox.MemoryMB) ||
		!fromTemplate(shape.DiskGB, tmpl.Sandbox.DiskGB) {
		return shape, false
	}
	log.Printf("orchestrator: task %s: not asking for template %s's sandbox size (%d vCPU/%d MiB/%d GiB): "+
		"this deployment's host-directory sandboxes have no size of their own", task.ID, id,
		shape.CPUs, shape.MemoryMB, shape.DiskGB)
	shape.CPUs, shape.MemoryMB, shape.DiskGB = 0, 0, 0
	return shape, true
}
