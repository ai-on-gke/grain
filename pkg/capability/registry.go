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

// Package capability is the registry through which a package outside core
// grain contributes a method of an in-process capability server --
// https://grain.dev/capabilities/corp#research, say -- with Register
// (methods.go). A registered method is resolved, listed and forwarded
// wherever core's own in-process grain server's methods are, and it can
// retire a bare name of the build's own as an alias.
//
// A method can also say it is on by default (Method.DefaultOn): a fresh
// deployment of a build that registers it starts with it among its
// default capabilities (NewDeploymentConfig), and an existing one is only
// told so in Settings. Core grain declares no defaults; this is a seam for
// a build such as an edition's cmd/grain to use.
//
// There is no longer a way to contribute a bare in-process provider
// (model.CapabilityProvider) here: every legacy provider was retired onto
// a capability server method, an api preset or a task setting, and the
// RegisterProvider seam that served them went with them (task 2876).
package capability

import (
	"sync"

	"github.com/ai-on-gke/grain/pkg/model"
)

// mu guards what methods.go registers.
var mu sync.RWMutex

// registerDefaultConsoleSessionCapability seeds id into the shipped
// director's capabilities, saying whether it needs sandbox egress.
func registerDefaultConsoleSessionCapability(id string, sandboxEgress bool) (unregister func()) {
	if sandboxEgress {
		return model.RegisterDefaultConsoleSessionEgressCapability(id)
	}
	return model.RegisterDefaultConsoleSessionCapability(id)
}

// RunGrants returns every grant name of the registered methods with
// RunGrant set (Method.RunGrant): its reference, its recorded grant and
// its aliases.
func RunGrants() []string {
	return methodRunGrants()
}

// DefaultOn returns the recorded grant of each registered method with
// DefaultOn set, in Methods' order.
func DefaultOn() []string {
	return methodDefaultOn()
}

// NewDeploymentConfig is model.NewDeploymentConfig with DefaultOn as its
// DefaultCapabilities: the configuration a deployment of this build
// stores in its first grain_config row. It lives here rather than in
// pkg/model because only this package knows what the build registered.
//
// Only the paths that write or describe a deployment with nothing stored
// call it (the daemon's seed for a fresh database, ui.UpdateSettings'
// first save, ui.GetSettings before either); a stored row is read back as
// it is, so the defaults of an existing deployment are its operator's.
func NewDeploymentConfig() model.Config {
	c := model.NewDeploymentConfig()
	c.DefaultCapabilities = DefaultOn()
	return c
}
