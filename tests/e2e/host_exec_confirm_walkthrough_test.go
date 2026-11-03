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

package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/capability/selfrepair"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// TestHostExecConfirmsEveryCommandInChatWalkthrough is
// docs/test-runs/2026-11-02-grain-capability-server-upgrade.md's
// finding 4, closed: an interactive task holding grain#host-exec,
// dispatched through the real reconcile loop onto a framework that forks
// the real `grain mcpserver` with the arguments agent/claude passes it
// (agent.GrantArgs), is offered run_host_command; each call posts its
// command into the task's chat and parks the task on it; nothing runs
// while nobody has answered; a "deny" runs nothing; and only an
// "approve" posted after the question runs the command.
func TestHostExecConfirmsEveryCommandInChatWalkthrough(t *testing.T) {
	w := newDeliveryWorld(t)
	w.mcpArgs = agent.GrantArgs
	marker := filepath.Join(t.TempDir(), "ran")
	command := "touch " + marker

	type outcome struct {
		res *mcp.CallResult
		err error
	}
	// ask calls run_host_command in the background and returns once its
	// question is in the task's chat, with the task parked on it. The
	// script runs on the orchestrator's own goroutine, so a failure here
	// is an Errorf and a zero ID, never a Fatalf.
	ask := func(p *mcp.Client, taskID string, before int64) (<-chan outcome, int64) {
		done := make(chan outcome, 1)
		go func() {
			res, err := p.CallTool(w.ctx, "run_host_command", map[string]any{"command": command})
			done <- outcome{res, err}
		}()
		deadline := time.Now().Add(20 * time.Second)
		for {
			cs, _ := w.store.Comments(w.ctx, taskID)
			for _, c := range cs {
				if c.ID > before && c.Body == selfrepair.ConfirmQuestion(command) {
					if s := w.snap(taskID); s.Pending == c.ID && s.State == model.StateAwaitingReply {
						return done, c.ID
					}
				}
			}
			if time.Now().After(deadline) {
				t.Errorf("run_host_command's question never reached the chat parked: %s", w.snap(taskID))
				return done, 0
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	ran := func() bool { _, err := os.Stat(marker); return err == nil }

	w.script <- func(ctx context.Context, cfg agent.RunConfig, p *mcp.Client) *agent.Result {
		if !slices.ContainsFunc(cfg.Grants, model.IsGrainHostExecGrant) {
			t.Errorf("run's Grants = %v, want grain#host-exec passed to an interactive run", cfg.Grants)
		}
		infos, err := p.ListTools(ctx)
		if err != nil {
			t.Errorf("tools/list: %v", err)
			return &agent.Result{FinalText: "done"}
		}
		names := map[string]bool{}
		for _, info := range infos {
			names[info.Name] = true
		}
		if !names["run_host_command"] || !names["chat"] {
			t.Errorf("tools/list = %v, want run_host_command beside chat", names)
			return &agent.Result{FinalText: "done"}
		}

		// Unanswered: the call blocks and nothing runs. A comment
		// written before the question is no answer to it.
		early := w.comment(cfg.TaskID, "approve (said before anything was asked)")
		done, q1 := ask(p, cfg.TaskID, early)
		if q1 == 0 {
			return &agent.Result{FinalText: "done"}
		}
		select {
		case o := <-done:
			t.Errorf("run_host_command returned with nobody answering: %+v", o)
			return &agent.Result{FinalText: "done"}
		case <-time.After(2 * time.Second):
		}
		if ran() {
			t.Error("the command ran before anybody replied")
		}

		w.comment(cfg.TaskID, "deny: not on this host")
		o := <-done
		if o.err != nil || !o.res.IsError || !strings.Contains(o.res.Text(), "denied: deny: not on this host") {
			t.Errorf("after deny: %+v, %v; want a refusal quoting the reply", o.res, o.err)
		}
		if ran() {
			t.Error("the command ran after it was denied")
		}

		done, q2 := ask(p, cfg.TaskID, q1)
		w.comment(cfg.TaskID, "approve")
		o = <-done
		if o.err != nil || o.res.IsError || !strings.HasPrefix(o.res.Text(), "exit=0") {
			t.Errorf("after approve: %+v, %v; want the command's own exit=0", o.res, o.err)
		}
		if !ran() {
			t.Error("the command did not run after it was approved")
		}
		t.Logf("questions %d and %d answered; %s", q1, q2, w.snap(cfg.TaskID))
		return &agent.Result{FinalText: "done"}
	}
	id := w.createTask(fmt.Sprintf(`{"title":"host-exec walkthrough","description":"restart it","interactive":true,"noRepo":true,"capabilities":[%q]}`,
		model.GrainHostExecGrant))
	w.settle(id, 1)
}

// TestHostExecIsNotOfferedToABackgroundRunWalkthrough is the other half
// of that gate: the same grant on a task nobody is sitting with puts no
// run_host_command on the run's roster, since its every call would wait
// on a person who is not there.
func TestHostExecIsNotOfferedToABackgroundRunWalkthrough(t *testing.T) {
	w := newDeliveryWorld(t)
	w.mcpArgs = agent.GrantArgs
	w.script <- func(ctx context.Context, cfg agent.RunConfig, p *mcp.Client) *agent.Result {
		if slices.ContainsFunc(cfg.Grants, model.IsGrainHostExecGrant) {
			t.Errorf("run's Grants = %v, want grain#host-exec withheld from a background run", cfg.Grants)
		}
		infos, err := p.ListTools(ctx)
		if err != nil {
			t.Errorf("tools/list: %v", err)
		}
		for _, info := range infos {
			if info.Name == "run_host_command" {
				t.Errorf("a background run was offered run_host_command")
			}
		}
		return &agent.Result{FinalText: "done"}
	}
	id := w.createTask(fmt.Sprintf(`{"title":"host-exec background walkthrough","description":"look around","noRepo":true,"approved":true,"capabilities":[%q]}`,
		model.GrainHostExecGrant))
	w.settle(id, 1)
}
