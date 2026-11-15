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
	"context"
	"time"

	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
)

// implicitHostPlacement is what this daemon knows about where host work
// nothing places goes (model.ImplicitHostPlacement), for writing it down
// when there is only one host it can go to
// (model.MigrateImplicitHostPlacement): the default host, whether the
// co-located grain hostd is one, and which CLIs the daemon's own
// -<cli>-run flag (GRAIN_*_RUN) or built-in default runs on the host.
//
// Only a daemon whose host executor is grain hostd has any (false): on
// a development daemon on the bare machine a host run is the daemon's
// own child, through no hostd, and on one with no host executor there is
// no host at all.
func (c Config) implicitHostPlacement() (model.ImplicitHostPlacement, bool) {
	exec := c.hostExecutor()
	if exec.Kind != HostExecutorHostd {
		return model.ImplicitHostPlacement{}, false
	}
	p := model.ImplicitHostPlacement{Default: c.DefaultHostd()}
	p.LocalUsable = exec.Remote == nil
	p.DaemonHost = map[string]bool{}
	for _, fw := range model.CLIRunFrameworks {
		loc := c.cliRunFlag(fw)
		if loc == "" {
			loc = c.defaultCLIRun(fw)
		}
		if loc == model.CLIRunHost {
			p.DaemonHost[fw] = true
		}
	}
	return p, true
}

// localHostdLoginsTimeout bounds the hello localHostdLogins asks.
const localHostdLoginsTimeout = 2 * time.Second

// localHostdLogins is the agent CLI logins on the co-located grain
// hostd's machine (hostd.Hello.Logins), for the host pickers beside
// "local", and whether it said: false where there is no co-located hostd,
// it does not answer, or it is too old to report them.
func (c Config) localHostdLogins(ctx context.Context) ([]string, bool) {
	exec := c.hostExecutor()
	if exec.Kind != HostExecutorHostd || exec.Remote != nil || exec.Socket == "" {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(hostd.ForDisplay(ctx), localHostdLoginsTimeout)
	defer cancel()
	h, err := exec.hostdClient().Hello(ctx)
	if err != nil || (!h.Has(hostd.FeatureLogins) && len(h.Logins) == 0) {
		return nil, false
	}
	return h.Logins, true
}

// CLIRunsThroughHostd reports whether framework's CLI starts through
// grain hostd -- host or detached, on a daemon whose host executor is
// grain hostd -- with stored as Settings -> Models' run locations and
// then the daemon's flags and defaults (CLIRunSetting). It is what a
// connection with ambient credentials needs a host named for
// (ui.Config.AmbientNeedsHost): its login is otherwise the default
// host's by implication.
func (c Config) CLIRunsThroughHostd(framework string, stored map[string]model.CLIRunSetting) bool {
	if c.hostExecutor().Kind != HostExecutorHostd {
		return false
	}
	live := c
	live.CLIRun = stored
	loc, _ := live.CLIRunSetting(model.NormalizeAgentFrameworkName(framework))
	return loc == model.CLIRunHost || loc == model.CLIRunDetached
}
