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

package protocol

// Grain's identity in configure (§4.5.7). A server whose configuration
// schema declares one of these top-level fields is sent Grain's own value
// for it on every configure, in place of anything the stored config says:
// the deployment's identity has one source, Grain, and no operator types
// a second copy of it that could drift. Settings does not draw them
// (OperatorSchema).
const (
	// DeploymentIDConfig is the deployment id Grain puts in its branch
	// names (grain/<deployment>/task-<n>): grain_config.deployment_id when
	// a system admin set one, otherwise the default derived from the
	// installation id. A system admin can rename it.
	DeploymentIDConfig = "grainDeploymentId"
	// InstanceIDConfig is the installation's random id
	// (grain_instance.id), which never changes for the life of its
	// database, whatever the deployment id is renamed to.
	InstanceIDConfig = "grainInstanceId"
)

// Identity is what Grain fills the identity fields with.
type Identity struct {
	DeploymentID string
	InstanceID   string
}

// identityFields are the identity fields and their values in id.
func (id Identity) fields() map[string]string {
	return map[string]string{DeploymentIDConfig: id.DeploymentID, InstanceIDConfig: id.InstanceID}
}

// WithIdentity is config with each identity field schema declares set to
// id's value, overriding what config said; fields id has no value for are
// removed. config itself is not changed.
func WithIdentity(schema, config map[string]any, id Identity) map[string]any {
	props, _ := schema["properties"].(map[string]any)
	out := make(map[string]any, len(config)+2)
	for k, v := range config {
		out[k] = v
	}
	for name, v := range id.fields() {
		if _, declared := props[name]; !declared {
			continue
		}
		delete(out, name)
		if v != "" {
			out[name] = v
		}
	}
	return out
}

func isIdentityField(name string) bool {
	return name == DeploymentIDConfig || name == InstanceIDConfig
}

// OperatorSchema is a configuration schema without the identity fields
// Grain fills in itself: the form Settings draws and the operator saves.
// A schema declaring none is returned as it is.
func OperatorSchema(schema map[string]any) map[string]any {
	props, _ := schema["properties"].(map[string]any)
	_, hasDeployment := props[DeploymentIDConfig]
	_, hasInstance := props[InstanceIDConfig]
	if !hasDeployment && !hasInstance {
		return schema
	}
	out := make(map[string]any, len(schema))
	for k, v := range schema {
		out[k] = v
	}
	p := make(map[string]any, len(props))
	for k, v := range props {
		if !isIdentityField(k) {
			p[k] = v
		}
	}
	out["properties"] = p
	if req, ok := schema["required"].([]any); ok {
		var keep []any
		for _, r := range req {
			if name, _ := r.(string); !isIdentityField(name) {
				keep = append(keep, r)
			}
		}
		out["required"] = keep
	}
	return out
}
