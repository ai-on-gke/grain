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
	"errors"
)

// ErrNoSandboxBackend is what every sandbox acquisition fails with on a
// deployment whose sandbox backend is "none": the run's failure reason, so
// it is written for the person reading why their task did not run.
var ErrNoSandboxBackend = errors.New(`this deployment's sandbox backend is "none" ` +
	`(-disable-native-sandboxes): it runs grain's control plane only and creates no sandboxes, ` +
	`so no task can run here. Choose a sandbox backend in Settings -> Sandboxes, or restart the daemon ` +
	`with -container-sandboxes, -granule-sandboxes, -k8s-sandboxes, -k8s-granule-sandboxes or -host-sandboxes`)

// DisabledSandboxes is the Sandboxes of a deployment whose sandbox backend
// is "none" (-disable-native-sandboxes): a control-plane-only daemon, such
// as one a walkthrough or an end-to-end test starts to exercise settings,
// the inbox and the config repository with no agent ever running. It
// creates nothing; every Acquire fails with ErrNoSandboxBackend.
type DisabledSandboxes struct{}

// NewDisabledSandboxes constructs a Sandboxes backend that refuses to build any sandbox.
func NewDisabledSandboxes() *DisabledSandboxes {
	return &DisabledSandboxes{}
}

// Acquire implements Sandboxes.
func (d *DisabledSandboxes) Acquire(ctx context.Context, name string, shape Shape) (Sandbox, error) {
	return nil, ErrNoSandboxBackend
}

// IsolatesNetwork implements NetworkIsolator.
func (d *DisabledSandboxes) IsolatesNetwork() bool {
	return true
}

// ReapOrphans implements the orphan cleanup sweep at startup.
func (d *DisabledSandboxes) ReapOrphans(ctx context.Context) (int, error) {
	return 0, nil
}
