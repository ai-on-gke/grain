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
	"fmt"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	consolecap "github.com/ai-on-gke/grain/pkg/capability/console"
	"github.com/ai-on-gke/grain/pkg/console"
	"github.com/ai-on-gke/grain/pkg/model"
)

// DefaultMaxConsoleRuntime is Config.maxConsoleRuntime's fallback: how
// long a console session is allowed to last before grain ends it anyway.
//
// It is a day rather than MaxRunRuntime's two hours because the two
// numbers bound different things. MaxRunRuntime bounds an agent nobody
// is watching, where every extra hour is an hour of unsupervised spend;
// this bounds a terminal somebody opened, where the ordinary way it ends
// is that they close it and the failure mode it guards against is that
// they forgot to. A session left open overnight costs a sandbox and no
// tokens at all -- an idle CLI asks the provider for nothing -- so the
// number only has to be small enough that a forgotten session is
// eventually cleaned up.
const DefaultMaxConsoleRuntime = 24 * time.Hour

func (c Config) maxConsoleRuntime() time.Duration {
	if c.MaxConsoleRuntime > 0 {
		return c.MaxConsoleRuntime
	}
	return DefaultMaxConsoleRuntime
}

// consoleDispatch decides how this run is driven, and is the whole of
// what an interactive task changes about a dispatch.
//
// A task without the console grant is returned exactly as it arrived:
// the framework runOne built and the deployment's own config. One with
// it is wrapped so that its run is a session on a pseudo-terminal
// (console.Framework), and given the longer clock a session lives on.
// Nothing else differs -- the sandbox, the checkout, the capabilities,
// the branch and the pull request its commits become are all the same
// machinery, because RunDispatch cannot tell the two apart.
//
// Both failures here are deliberate rather than fallbacks. A deployment
// with no session manager, and a framework whose CLI cannot be driven by
// hand, each fail the dispatch with a sentence naming what is missing:
// the alternative is running the task unattended, which is not a
// smaller version of what was asked for -- it is an agent working
// through a task alone that somebody meant to sit with, and they would
// find out afterwards.
func consoleDispatch(framework agent.Framework, task model.Task, runID string, cfg Config) (agent.Framework, Config, error) {
	if !consolecap.Granted(task.Grants) {
		return framework, cfg, nil
	}
	if cfg.Consoles == nil {
		return nil, cfg, fmt.Errorf("orchestrator: %s asks for an interactive console, which this deployment does not serve", task.ID)
	}
	terminal, ok := framework.(agent.TerminalFramework)
	if !ok {
		return nil, cfg, fmt.Errorf("orchestrator: %s asks for an interactive console, which %s cannot be driven as",
			task.ID, frameworkFor(task, cfg))
	}
	var repo string
	if task.Target != nil {
		repo = task.Target.String()
	}
	wrapped := console.Wrap(terminal, cfg.Consoles, console.Spec{
		TaskID:    task.ID,
		RunID:     runID,
		Title:     task.Title,
		Framework: frameworkFor(task, cfg),
		Repo:      repo,
		StartedAt: cfg.now(),
	})
	cfg.MaxRunRuntime = cfg.maxConsoleRuntime()
	return wrapped, cfg, nil
}
