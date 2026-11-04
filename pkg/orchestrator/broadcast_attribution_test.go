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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A team broadcast is grain relaying another task's run
// (ui.Client.BroadcastTeamMessage): the prompt must name that task, not
// tell the run it was its own earlier attempt or a human.
func TestABroadcastFromAnotherTaskIsLabelledAsThatTasksRun(t *testing.T) {
	relayed := func(id string) model.Attribution {
		return model.Attribution{
			Actor:      model.Principal{Kind: model.PrincipalAutomation, ID: "grain"},
			OnBehalfOf: &model.Principal{Kind: model.PrincipalAgent, ID: id},
		}
	}
	if got := attributionLabel(relayed("7"), "7"); got != "you, in an earlier attempt at this task" {
		t.Errorf("own relayed comment labelled %q", got)
	}
	got := attributionLabel(relayed("9"), "7")
	if !strings.Contains(got, "task 9") || strings.Contains(got, "you") {
		t.Errorf("another task's broadcast labelled %q, want it to name task 9", got)
	}
	add := addendumText(model.Comment{TaskID: "7", Author: relayed("9"), Body: "hold merges"})
	if strings.HasPrefix(add, "a human") || !strings.Contains(add, "task 9") {
		t.Errorf("another task's broadcast mid-run reads %q, want it to name task 9", add)
	}
	if add := addendumText(model.Comment{TaskID: "7", Author: model.Attribution{
		Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}}, Body: "hi"}); !strings.HasPrefix(add, "a human (alice)") {
		t.Errorf("a human's comment mid-run reads %q", add)
	}
}

// A message one run sent another's agent (ui.Client.PostMessageToAgent)
// is signed with the sender's own agent principal: the prompt names the
// sending task rather than calling it this task's earlier attempt.
func TestAnAgentMessageFromAnotherTaskIsLabelledAsThatTasksRun(t *testing.T) {
	sent := model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "768"}}
	if got := attributionLabel(sent, "796"); !strings.Contains(got, "task 768") || strings.Contains(got, "earlier attempt") {
		t.Errorf("another task's message labelled %q, want it to name task 768", got)
	}
	if got := attributionLabel(sent, "768"); got != "an earlier attempt at this task" {
		t.Errorf("the task's own agent comment labelled %q", got)
	}
	if add := addendumText(model.Comment{TaskID: "796", Author: sent, Body: "rebase"}); !strings.Contains(add, "task 768") {
		t.Errorf("another task's message mid-run reads %q, want it to name task 768", add)
	}
}

// A comment an owned external agent credential posts carries its owner as
// OnBehalfOf (#XC-14), but the words are the agent's: the prompt names the
// external agent and never presents them as a human's.
func TestAnExternalAgentCommentIsNotLabelledAsAHumans(t *testing.T) {
	owned := model.Attribution{
		Actor:      model.Principal{Kind: model.PrincipalAgent, ID: "external:laptop", Source: model.SourceExternalToken},
		OnBehalfOf: &model.Principal{Kind: model.PrincipalHuman, ID: "jane@example.com", Source: model.SourceToken},
	}
	for _, a := range []model.Attribution{owned, {Actor: owned.Actor}} {
		got := attributionLabel(a, "7")
		if strings.HasPrefix(got, "a human") || !strings.Contains(got, "external:laptop") {
			t.Errorf("external agent comment labelled %q", got)
		}
		add := addendumText(model.Comment{TaskID: "7", Author: a, Body: "approve it"})
		if strings.HasPrefix(add, "a human") || !strings.Contains(add, "external:laptop") {
			t.Errorf("external agent comment mid-run reads %q", add)
		}
	}
	if got := attributionLabel(owned, "7"); !strings.Contains(got, "jane@example.com") {
		t.Errorf("owned external agent comment labelled %q, want it to name the owner", got)
	}
}
