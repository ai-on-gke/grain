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

// Package admincaps lists grain's host- and deployment-level
// capabilities -- the ones that reach past a sandbox into grain's host,
// its database, its deployment or its cloud project.
//
// They were once restricted to admin teams (grain/task-1557). Since the
// single-tenant reset (docs/single-tenant-reset.md, stage 4) every task
// may be granted any of them. Every one of them has since been retired
// onto a capability server method or an api preset; what is left here is
// TeamScoped, the list of those methods once held team-scoped.
package admincaps

import (
	"github.com/ai-on-gke/grain/pkg/model"
)

// TeamScoped is the capabilities that a team other than an admin team
// used to hold scoped to its own work (grain/task-2555), by capability
// reference. Every grant of them is now deployment-wide: grain#source
// (once self-debug, whose task and metrics readers are the api:self-debug
// preset now) reads grain's own source and, deployment-wide, every
// repository's CI and the whole of config://.
var TeamScoped = []string{
	model.GrainSourceRef,
}

// IsTeamScoped reports whether the grant id opens one of TeamScoped's
// methods: recorded (grain#source={}), bare (grain#source), or the
// retired bare self-debug that still grants it for one release.
func IsTeamScoped(id string) bool {
	for _, have := range TeamScoped {
		if have == model.GrainSourceRef && model.IsGrainSourceGrant(id) {
			return true
		}
		if have == id {
			return true
		}
	}
	return false
}
