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
	"log"
	"time"
	"unicode/utf8"

	"github.com/ai-on-gke/grain/pkg/model"
)

// setupNotes is grain narrating the one stretch of a run that no agent is
// driving: everything between dispatch.Cycle making the run durable and
// framework.Run taking its first turn -- a sandbox acquired (a granule VM
// boot, minutes of it), a repo cloned, the repo's setup command run,
// capability credentials minted.
//
// task_run.activity was written only by the run's own update_status tool
// (ui.Client.SetTaskActivity, mcp.NewStatusTools), so a task that had
// just been dispatched read 'running' with nothing beside it for exactly
// the part of its life where "what is it doing?" has a precise answer --
// and the precise answer is one grain is holding, since there is no agent
// yet to hold it. These phrases are that answer, put where a person is
// already looking (ui.Task.Activity, on the task list and the task page).
//
// They go through the same model.Store.SetTaskActivity write the tool
// reaches, so there is no second column, no second read and no second
// renderer: a setup phrase is shown by everything that already shows a
// run's own.
//
// Whose words they are is not left to the phrasing, and not left to a
// prefix an agent could type for itself either. A note is grain's exactly
// while task_run.agent_started_at is unset, and handOver clears the last
// one in the same breath as Store.SetRunAgentStarted -- so the row itself
// carries the distinction (model.RunActivity.BySetup), and the UI marks
// such a phrase as grain's rather than passing it off as something the
// run said.
//
// The zero value is a working no-op, which is what the recreate path
// passes: see restoreCheckout.
type setupNotes struct {
	store  *model.Store
	taskID string
	now    func() time.Time
}

// maxSetupNote is the bound ui.MaxActivityLength puts on a note an agent
// writes, restated rather than imported: pkg/ui is the daemon's HTTP
// surface and this package does not depend on it. That limit is enforced
// at ui.Client, which a setup phrase never passes through, so it is
// applied here instead -- every phrase below is short by construction,
// but a repository with a very long name is not this package's to
// promise anything about. setup_notes_test.go holds the two numbers
// together.
const maxSetupNote = 120

// The phrases themselves, in the order a dispatch reaches them. Short,
// lowercase and in grain's own voice, to read as an aside beside a task
// title rather than as a log line: "cloning acme/widgets", not "Cloning
// repository acme/widgets into work/".
const (
	// buildingSandboxNote covers Sandboxes.Acquire, which on a granule
	// deployment is a VM boot with a ReadyTimeout measured in minutes --
	// the longest silence in a run's setup, and the one this whole file
	// exists for.
	buildingSandboxNote = "building a sandbox"
	// sandboxCredentialsNote covers the sandbox's own git identity: the
	// proxy token minted for it and the git config written into it
	// (Deps.MintSandboxToken, Sandbox.ConfigureGitCredentials). Quick
	// where the guest answers and a real wait where it does not, which is
	// the case worth being able to see.
	sandboxCredentialsNote = "giving the sandbox its git credentials"
	// setupCommandNote covers model.RepoConfig.SetupCommand, run in the
	// fresh checkout (runSetupCommand). It gets its own phrase because it
	// is arbitrary shell somebody configured, bounded only by
	// setupCommandTimeout -- ten minutes in which "cloning" would have
	// been a lie.
	setupCommandNote = "running the repo's setup command"
	// capabilityCredentialsNote covers prepareCapabilities: minting and
	// placing whatever the task was granted, which reaches GCP, a GitHub
	// App or whatever else a capability talks to.
	capabilityCredentialsNote = "minting the task's credentials"
)

// cloningNote is the clone's own phrase, naming the repository because
// that is the part a reader cannot infer from the task row they are
// already looking at -- a task in a deployment with several repos says
// which one it is waiting on.
func cloningNote(repo model.RepoRef) string {
	return "cloning " + repo.String()
}

// say puts one phrase on the task's row, for as long as it stands or
// until the next one replaces it.
//
// Best-effort, like every other bookkeeping write on this path
// (Store.SetRunAgentStarted's own doc comment states the rule): a run
// must not fail because grain could not say what it was doing. A store
// error is logged; a cancelled ctx -- a daemon shutting down, a task
// closed mid-setup -- takes the write with it, which is right, since
// there is nobody left to read the phrase anyway.
//
// A task whose run has already finished silently records nothing
// (SetTaskActivity's own live=false): a setup that lost its race with a
// cancellation has nothing to narrate.
func (n setupNotes) say(ctx context.Context, note string) {
	if n.store == nil || note == "" {
		return
	}
	if utf8.RuneCountInString(note) > maxSetupNote {
		note = string([]rune(note)[:maxSetupNote])
	}
	if _, err := n.store.SetTaskActivity(ctx, n.taskID, note, n.at()); err != nil {
		log.Printf("orchestrator: task %s: recording what its setup is doing: %v", n.taskID, err)
	}
}

// handOver clears grain's last setup phrase, at the moment the run stops
// being grain's to narrate and becomes the agent's.
//
// Clearing rather than leaving it to be replaced by the agent's own first
// update_status call: a run is under no obligation to narrate itself, and
// most say nothing for their first several minutes, so the alternative is
// "minting the task's credentials" standing on the row for half an hour
// while the agent works on something else entirely. An empty row already
// means "running, and nothing has been said", which is the truth here.
//
// It is also what keeps model.RunActivity.BySetup honest: the moment
// agent_started_at is stamped, every note the row could still be carrying
// would read as the agent's, so grain's last one must not be one of them.
func (n setupNotes) handOver(ctx context.Context) {
	if n.store == nil {
		return
	}
	if _, err := n.store.SetTaskActivity(ctx, n.taskID, "", n.at()); err != nil {
		log.Printf("orchestrator: task %s: clearing its setup status: %v", n.taskID, err)
	}
}

// at is the moment a phrase is stamped with -- Config.now where the
// caller had a Config to take it from, so a test with a fixed clock reads
// the same times everywhere else in this package does.
func (n setupNotes) at() time.Time {
	if n.now == nil {
		return time.Now().UTC()
	}
	return n.now()
}
