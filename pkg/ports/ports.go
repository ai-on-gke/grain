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

// Package ports is grain's table of fixed, well-known ports: the ones
// an operator types, a firewall rule names or a bookmark remembers.
//
// The convention (docs/ports.md) is that every port grain listens on by
// default and by convention starts with 42 -- 4200-4299 -- so a port in
// that range on a grain host is grain's, and one outside it is somebody
// else's. Ports picked at run time (ephemeral listeners, sandbox
// forwards, test servers on :0) are not fixed and are not in here.
//
// Code that needs one of these numbers names the constant rather than
// writing the literal, and lint_test.go fails a new port literal outside
// the range that is not on its allowlist with a reason. This package is
// not pkg/portpick, which is about porting commits between branches.
package ports

// The range the convention reserves.
const (
	ConventionMin = 4200
	ConventionMax = 4299
)

// The UI and API.
const (
	// UI is the daemon's UI/API port: -ui-addr's 0.0.0.0:4242 and the
	// CLI's http://127.0.0.1:4242.
	UI = 4242
)

// 4243-4249: further daemon services an operator or remote machine
// connects to. The remote executor listener (-executor-addr) that once
// took 4243 is retired; the range stays reserved for daemon services.
const (
	DaemonServicesMin = 4243
	DaemonServicesMax = 4249
)

// SandboxAccess is the sandbox-access acceptor every k8s-granule sandbox
// pod's wrapper dials out to (-sandbox-access-addr, the grain-access
// Service). pkg/sandboxaccess.DefaultPort repeats it rather than
// importing this package, to stay neutral; pkg/daemon's tests hold the
// two equal.
const SandboxAccess = 4244

// 4280-4289: persistent sandboxes' host_port (model.PersistentSandbox),
// the staging environments grain keeps running beside itself.
const (
	PersistentSandboxMin = 4280
	PersistentSandboxMax = 4289

	// StagingGrain is the host port of the staging grain persistent
	// sandbox, the first port allocated under the convention.
	StagingGrain = 4280
)

// 4290-4299: development and demo servers.
const (
	DevMin = 4290
	DevMax = 4299

	// Demo is `grain demo`'s default -addr port.
	Demo = 4290
)

// InConvention is whether port is in the 42xx range.
func InConvention(port int) bool {
	return port >= ConventionMin && port <= ConventionMax
}
