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

package daemon

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestCLIRunsThroughHostd(t *testing.T) {
	t.Parallel()
	viaHostd := Config{HostExec: &HostExecutor{Kind: HostExecutorHostd, Socket: "/run/grain-hostd/hostd.sock"},
		AgyRun: model.CLIRunHost}
	if !viaHostd.CLIRunsThroughHostd(model.AgentFrameworkAntigravity, nil) {
		t.Error("agy on the host by its flag, through hostd: want true")
	}
	if viaHostd.CLIRunsThroughHostd(model.AgentFrameworkClaude, nil) {
		t.Error("claude in the daemon's container: want false")
	}
	if !viaHostd.CLIRunsThroughHostd(model.AgentFrameworkClaude,
		map[string]model.CLIRunSetting{model.AgentFrameworkClaude: {Location: model.CLIRunDetached}}) {
		t.Error("claude detached by Settings: want true")
	}
	if viaHostd.CLIRunsThroughHostd(model.AgentFrameworkAntigravity,
		map[string]model.CLIRunSetting{model.AgentFrameworkAntigravity: {Location: model.CLIRunContainer}}) {
		t.Error("agy moved into the container by Settings: want false")
	}
	bare := Config{HostExec: &HostExecutor{Kind: HostExecutorLocal}}
	if bare.CLIRunsThroughHostd(model.AgentFrameworkAntigravity, nil) {
		t.Error("a development daemon's host run is its own child: want false")
	}
}

func TestImplicitHostPlacement(t *testing.T) {
	t.Parallel()
	if _, ok := (Config{HostExec: &HostExecutor{Kind: HostExecutorLocal}}).implicitHostPlacement(); ok {
		t.Error("a development daemon on the bare machine has no hostd to write down")
	}
	p, ok := Config{HostExec: &HostExecutor{Kind: HostExecutorHostd, Socket: "/run/grain-hostd/hostd.sock"},
		AgyRun: model.CLIRunHost}.implicitHostPlacement()
	if !ok || !p.LocalUsable || p.Default != model.LocalHostd {
		t.Fatalf("co-located hostd: %+v, %v", p, ok)
	}
	if !p.DaemonHost[model.AgentFrameworkAntigravity] || p.DaemonHost[model.AgentFrameworkClaude] {
		t.Errorf("DaemonHost = %v, want antigravity alone", p.DaemonHost)
	}
}
