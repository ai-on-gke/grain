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
)

// A refusal grain has a marker for pauses the credential it came from
// (Pause.Begin, driven by agent.UsageLimitError). A refusal it has no
// marker for -- the provider reworded it, which is how the 2026-10-02
// Claude "You've hit your session limit · resets 5:30pm (UTC)" outage
// happened -- arrives as an ordinary failure instead, and dispatch
// retries it: every task in the queue spends an attempt on the same
// wall, five at a time, for as long as the window lasts.
//
// What such a refusal looks like from here, whatever its words, is a
// burst: several runs on one credential, each failing within moments of
// starting, before any tool call, with exactly the same error text. An
// agent's own failures do not look like that -- they come after work,
// and say different things about different tasks -- so a burst is read
// as the provider refusing every run on that credential, and handled as
// a usage limit that named no reset: the credential -- the model
// connection that owns it, or the team and framework of a run on no
// connection (PauseScope) -- is paused for the default window,
// and the runs in the burst are recorded as paused rather than failed,
// so no task's attempt budget pays for it.
//
// It is a safety net and not a classifier. The words still belong in a
// marker (agent/<framework>/usagelimit.go), and the log line this
// writes, and `grain failures`, are what say which words.
const (
	// refusalBurstRuns is how many identical fast failures make a burst.
	// Three rather than two: two tasks can fail on the same broken
	// setup step in the same minute, and the cost of a burst read
	// wrongly is a quarter of an hour's idle connection.
	refusalBurstRuns = 3
	// refusalBurstWindow is how close together they have to be.
	refusalBurstWindow = 5 * time.Minute
	// refusalBurstFastRun is how quickly each of them has to have failed
	// after its agent started.
	refusalBurstFastRun = time.Minute
)

// burstFailure is one fast failure observeFastFailure is holding.
type burstFailure struct {
	runID string
	at    time.Time
}

// burstKey is what makes two failures "the same": the credential they
// were spending (PauseScope.key) and the error's own text, exactly. Two
// teams' connections failing alike are two separate counts, so one
// team's broken setup can never add up to a pause of another's.
func burstKey(scope PauseScope, text string) string {
	return scope.key() + "\x00" + text
}

// isFastFailure reports whether a failed run is the shape a refusal
// takes: over within refusalBurstFastRun of its agent starting, with no
// tool call made.
func isFastFailure(result *agent.Result, ran time.Duration) bool {
	if ran < 0 || ran >= refusalBurstFastRun {
		return false
	}
	return result == nil || len(result.ToolCalls) == 0
}

// observeFastFailure records that runID, spending scope, failed fast with
// text at now, and reports the burst it completes: every run in it,
// this one last, once refusalBurstRuns of them have failed alike within
// refusalBurstWindow -- nil until then. A burst once reported is
// forgotten, so the runs after it (which the pause will mostly stop
// anyway) start counting afresh.
func (p *Pause) observeFastFailure(now time.Time, scope PauseScope, runID, text string) []string {
	if p == nil || text == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bursts == nil {
		p.bursts = map[string][]burstFailure{}
	}
	// Forget every failure that has aged out of the window, under every
	// key, so a deployment that fails fast now and then holds nothing.
	for key, held := range p.bursts {
		kept := held[:0]
		for _, f := range held {
			if now.Sub(f.at) < refusalBurstWindow {
				kept = append(kept, f)
			}
		}
		if len(kept) == 0 {
			delete(p.bursts, key)
		} else {
			p.bursts[key] = kept
		}
	}
	key := burstKey(scope.normalized(), text)
	held := append(p.bursts[key], burstFailure{runID: runID, at: now})
	if len(held) < refusalBurstRuns {
		p.bursts[key] = held
		return nil
	}
	delete(p.bursts, key)
	runs := make([]string, len(held))
	for i, f := range held {
		runs[i] = f.runID
	}
	return runs
}

// refusalBurstLimit is the usage limit a burst is handled as: the
// framework's, with no reset named, so Pause.Begin holds the run's
// credential for defaultUsageLimitPause.
func refusalBurstLimit(framework, text string, runs int) *agent.UsageLimitError {
	return &agent.UsageLimitError{
		Framework: framework,
		Message: fmt.Sprintf("%d runs failed within %s of each other, each in under %s and before any tool call, "+
			"all with the same error -- read as the provider refusing %s: %s",
			runs, refusalBurstWindow, refusalBurstFastRun, framework, agent.TrimLimitMessage(text)),
	}
}
