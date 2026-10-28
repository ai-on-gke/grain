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

import "strings"

// SandboxBackend names the isolation mechanism used for every agent and CI
// sandbox on a deployment. It is stored as text so deployments upgraded from
// before the setting existed can keep an empty value: the daemon resolves that
// legacy value to the backend selected by its startup flags.
type SandboxBackend string

const (
	SandboxBackendContainer  SandboxBackend = "container"
	SandboxBackendGranule    SandboxBackend = "granule"
	SandboxBackendKubernetes SandboxBackend = "kubernetes"
	// SandboxBackendKubernetesGranule is -k8s-granule-sandboxes: a
	// privileged pod per run booting a real cloud-hypervisor VM over
	// /dev/kvm, scheduled by the cluster -- the same isolation
	// SandboxBackendGranule gives on a single host. See
	// docs/k8s-granule-design.md for why this is a value of its own
	// rather than a flag on SandboxBackendKubernetes: the two
	// platforms' pods disagree about whether privileged is allowed at
	// all.
	SandboxBackendKubernetesGranule SandboxBackend = "kubernetes-granule"
	// SandboxBackendHost is -host-sandboxes: one plain directory per run
	// under -sandbox-dir, on the daemon's own host, with no isolation at
	// all (orchestrator.HostSandboxes). It is for a single-user
	// workstation that wants its agents beside its own tools; a daemon
	// only offers it when started with -host-sandboxes, so a stored
	// "host" can never move a deployment that was not onto it.
	SandboxBackendHost SandboxBackend = "host"
	SandboxBackendNone SandboxBackend = "none"
)

// SandboxBackendOption describes one sandbox backend in the order Settings
// presents it, whether this deployment can select it, and why not when
// Available is false.
type SandboxBackendOption struct {
	Backend   SandboxBackend `json:"backend"`
	Available bool           `json:"available"`
	Reason    string         `json:"reason,omitempty"`
}

// SandboxBackends is the complete vocabulary in the order Settings presents it:
// local containers, local VMs, cluster pods, cluster VMs, unisolated host
// directories, and disabled (none).
func SandboxBackends() []SandboxBackend {
	return []SandboxBackend{
		SandboxBackendContainer,
		SandboxBackendGranule,
		SandboxBackendKubernetes,
		SandboxBackendKubernetesGranule,
		SandboxBackendHost,
		SandboxBackendNone,
	}
}

func ValidSandboxBackend(backend SandboxBackend) bool {
	for _, candidate := range SandboxBackends() {
		if backend == candidate {
			return true
		}
	}
	return false
}

func SandboxBackendNames() string {
	backends := SandboxBackends()
	names := make([]string, len(backends))
	for i, backend := range backends {
		names[i] = string(backend)
	}
	return strings.Join(names, ", ")
}
