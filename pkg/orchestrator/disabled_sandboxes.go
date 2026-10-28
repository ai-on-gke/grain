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

// DisabledSandboxes is an orchestrator.Sandboxes implementation used when
// native sandboxes are disabled on this deployment. Any attempt to acquire a
// native sandbox returns an error indicating that native sandboxes are disabled
// and only remote execution sandboxes can exist.
type DisabledSandboxes struct{}

// NewDisabledSandboxes constructs a Sandboxes backend that refuses to build native sandboxes.
func NewDisabledSandboxes() *DisabledSandboxes {
	return &DisabledSandboxes{}
}

// Acquire implements Sandboxes.
func (d *DisabledSandboxes) Acquire(ctx context.Context, name string, shape Shape) (Sandbox, error) {
	return nil, errors.New("native sandboxes are disabled on this deployment; only remote execution sandboxes can exist")
}

// IsolatesNetwork implements NetworkIsolator.
func (d *DisabledSandboxes) IsolatesNetwork() bool {
	return true
}

// ReapOrphans implements the orphan cleanup sweep at startup.
func (d *DisabledSandboxes) ReapOrphans(ctx context.Context) (int, error) {
	return 0, nil
}
