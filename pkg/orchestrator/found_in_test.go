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

package orchestrator_test

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// TestProposalsRecordTheReleaseTheyAreFoundIn: what the pipeline steward
// files from a red candidate's triage ([rc-blocker <rc>]), a backport
// proposal onto release/vX.Y.Z and a proposal that states found_in all
// carry the release they are against as data (model.LinkFoundIn), so the
// code-quality report need not read it out of a title. What the run
// stated beats what the title says, a value that is no release is
// dropped with a note, and a proposal against no release records none.
func TestProposalsRecordTheReleaseTheyAreFoundIn(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	propose := func(args map[string]any) agent.ToolCall {
		return agent.ToolCall{Name: "propose_task", Arguments: args}
	}
	result := toolResult(
		propose(map[string]any{
			"title": model.RCBlockerMarker("v2.5.0-rc.2") + " Revert the scheduler change", "body": "it broke qualification",
			"at_front": true,
		}),
		propose(map[string]any{
			"title": "Backport the scheduler fix", "body": "Backported-from: 0123456789abcdef",
			"writes": []any{map[string]any{"repo": "acme/widgets", "base": "release/v2.4.0"}},
		}),
		propose(map[string]any{
			"title": "Fix the crash on upgrade", "body": "seen on the released build", "found_in": "2.4.1",
		}),
		propose(map[string]any{
			"title": model.RCBlockerMarker("v2.5.0-rc.2") + " Fix the flaky import", "body": "rc.1 had it too",
			"found_in": "v2.5.0-rc.1",
		}),
		propose(map[string]any{
			"title": "Fix the misnamed release", "body": "some bug", "found_in": "the latest one",
		}),
		propose(map[string]any{
			"title": "Tidy the README", "body": "no release involved",
		}),
	)
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposals := proposalsByTitle(t, ctx, store, task.ID)
	for title, want := range map[string]string{
		model.RCBlockerMarker("v2.5.0-rc.2") + " Revert the scheduler change": "v2.5.0-rc.2",
		"Backport the scheduler fix":                                          "v2.4.0",
		"Fix the crash on upgrade":                                            "v2.4.1",
		model.RCBlockerMarker("v2.5.0-rc.2") + " Fix the flaky import":        "v2.5.0-rc.1",
		"Fix the misnamed release":                                            "",
		"Tidy the README":                                                     "",
	} {
		p, ok := proposals[title]
		if !ok {
			t.Errorf("proposal %q was never filed", title)
			continue
		}
		if got := model.FoundIn(p); got != want {
			t.Errorf("proposal %q found in %q, want %q (links %+v)", title, got, want, p.Links)
		}
	}
	if p := proposals["Fix the misnamed release"]; !strings.Contains(p.Body, `"the latest one"`) {
		t.Errorf("body = %q, want the version grain could not record named in it", p.Body)
	}
}
