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

package workflows

import (
	"github.com/ai-on-gke/grain/pkg/grit/actions"
	"gopkg.in/yaml.v3"
)

// containerCAEnv is ActionsJob.ContainerCAEnv for job id of an Actions
// workflow: the CA variables (actions.ContainerCAEnv) its steps are
// pointed at the guest's trust store with, so that a `container:` image
// grain never prepared trusts the package cache the guest's resolver
// sends it to (pkg/grit/actions' container_ca.go has the whole story).
//
// Nil for a job with no `container:`, whose steps run on the guest and
// already trust it -- which also leaves that job's fingerprint where it
// was. A variable the workflow sets itself, in its own `env:`, the job's,
// the container's or any step's, is the author's and is left off.
func containerCAEnv(doc *yaml.Node, id string) []string {
	job := mappingValue(mappingValue(doc, "jobs"), id)
	container := mappingValue(job, "container")
	if container == nil || container.Tag == "!!null" {
		return nil
	}
	set := map[string]bool{}
	addKeys := func(env *yaml.Node) {
		if env == nil || env.Kind != yaml.MappingNode {
			return
		}
		for i := 0; i+1 < len(env.Content); i += 2 {
			set[env.Content[i].Value] = true
		}
	}
	addKeys(mappingValue(doc, "env"))
	addKeys(mappingValue(job, "env"))
	addKeys(mappingValue(container, "env"))
	if steps := mappingValue(job, "steps"); steps != nil && steps.Kind == yaml.SequenceNode {
		for _, step := range steps.Content {
			addKeys(mappingValue(step, "env"))
		}
	}
	var names []string
	for _, name := range actions.ContainerCAEnv {
		if !set[name] {
			names = append(names, name)
		}
	}
	return names
}
