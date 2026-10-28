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

// The bound as a dispatch's whole prompt sees it: a task whose
// conversation has been running for a week still produces a prompt a
// model can read, with the two parts that must never go -- what the task
// asked for, and what was most recently said about it -- intact.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

func TestBuildPromptBoundsALongConversationAndKeepsWhatMatters(t *testing.T) {
	t.Parallel()
	task := model.Task{
		ID:    "t1",
		Title: "Keep the merge lane honest",
		Body:  "THE-INVARIANT: the merge lane may only ever hold one branch at a time.",
	}
	filler := strings.Repeat("y", 1<<10)
	var comments []model.Comment
	for i := 0; i < 300; i++ {
		comments = append(comments, model.Comment{
			ID:        int64(i),
			Body:      fmt.Sprintf("turn-%03d %s", i, filler),
			Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
			CreatedAt: time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute),
		})
	}

	prompt := orchestrator.BuildPrompt(task, orchestrator.CheckoutDir, "/home/debian", false,
		orchestrator.DefaultMaxRunRuntime, orchestrator.History{Attempt: 10, Comments: comments}, nil)

	// 300 KB of conversation alone; a prompt that carried it whole would
	// be most of a context window before the run's first turn.
	if size := model.MeasurePrompt(prompt); size.Large() {
		t.Errorf("prompt came out %s, which is the failure this bound exists to prevent", size)
	}
	for _, want := range []string{
		"THE-INVARIANT",        // the description, which is outside the budget entirely
		"turn-000",             // the oldest turn, where a task's terms of reference are
		"turn-299",             // the most recent turn, which is what the run is about to act on
		"elided here by grain", // and the plain statement that there is a gap
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lost %q", want)
		}
	}
}
