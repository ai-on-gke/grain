package orchestrator

import (
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
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
