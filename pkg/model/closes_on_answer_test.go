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

package model_test

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// TestClosesOnAnswerOnlyForAgentFiledSubTasks pins which tasks a closing
// note alone may close (grain/task-4384). Closing is what satisfies a
// depends_on link and takes a task out of a person's inbox, so every
// exclusion here -- a review (whose finish feeds a merge gate), an
// interactive task, a task filed for acknowledgement, a console session's
// relayed request, a person's own task -- must keep completing into
// awaiting_reply instead.
func TestClosesOnAnswerOnlyForAgentFiledSubTasks(t *testing.T) {
	agentFiled := func() model.Task {
		return model.Task{Proposer: model.TaskProposer{
			TaskID: "41",
			Attribution: model.Attribution{Actor: model.Principal{
				Kind: model.PrincipalAgent, ID: "41", Source: model.SourceSandbox,
			}},
		}}
	}
	cases := []struct {
		name string
		edit func(*model.Task)
		want bool
	}{
		{"agent-filed sub-task", func(*model.Task) {}, true},
		{"explicit close", func(t *model.Task) { t.NothingNeededAction = model.NothingNeededClose }, true},
		{"filed for acknowledgement", func(t *model.Task) { t.NothingNeededAction = model.NothingNeededAck }, false},
		{"review", func(t *model.Task) { t.Origin.Reason = model.ReasonReview }, false},
		{"interactive", func(t *model.Task) { t.Interactive = true }, false},
		{"console session proposal", func(t *model.Task) { t.Proposer.Attribution.Actor.Source = model.SourceSession }, false},
		{"no proposing task", func(t *model.Task) { t.Proposer.TaskID = "" }, false},
		{"person-filed", func(t *model.Task) {
			t.Proposer.Attribution.Actor = model.Principal{Kind: model.PrincipalHuman, ID: "someone"}
		}, false},
	}
	for _, c := range cases {
		task := agentFiled()
		c.edit(&task)
		if got := model.ClosesOnAnswer(task); got != c.want {
			t.Errorf("%s: ClosesOnAnswer = %v, want %v", c.name, got, c.want)
		}
	}
}
