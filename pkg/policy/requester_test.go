package policy

import (
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

// derivedTask is a task filed from task "7": an agent's proposal, with
// the automation approval an auto-submitted one carries.
var widgets = model.RepoRef{Owner: "acme", Name: "widgets"}

func derivedTask(creator string, approver model.Principal) model.Task {
	return model.Task{
		ID: "12", Title: "Follow up", Target: &widgets, Creator: creator,
		Links:    []model.Link{{Kind: model.LinkProposedBy, Target: "7"}},
		Approval: &model.Attribution{Actor: approver},
	}
}

var (
	autoSubmit = model.AutoSubmitPrincipal
	lead       = model.Principal{Kind: model.PrincipalHuman, ID: "lead@example.com"}
)

// A task naming no requester is grain's own only when it was derived
// from nothing; a proposal, port or review naming none is not, and from
// the task alone its root cannot be known (GSA1-02).
func TestEmptyRequesterIsLocalOnlyOnARoot(t *testing.T) {
	p := mustParse(t, "repos: [acme/*]\nlocal_requesters: [owner@example.com]\n")

	scheduled := model.Task{ID: "3", Target: &widgets}
	if err := p.Check(scheduled, model.NetworkOpen); err != nil {
		t.Errorf("grain's own task was refused: %v", err)
	}
	if !p.IsLocal(RequesterOf(scheduled)) {
		t.Error("a root task naming no requester is not grain's own")
	}

	for name, task := range map[string]model.Task{
		"proposed":         derivedTask("", autoSubmit),
		"ported":           {ID: "13", Target: &widgets, Links: []model.Link{{Kind: model.LinkPortOf, Target: "7"}}},
		"in a task's tree": {ID: "14", Target: &widgets, WorkOrigin: model.TaskOrigin("7")},
	} {
		if p.IsLocal(RequesterOf(task)) {
			t.Errorf("%s task naming no requester is local", name)
		}
		if err := p.Check(task, model.NetworkOpen); err == nil || !strings.Contains(err.Error(), "no human approval") {
			t.Errorf("%s task naming no requester: err = %v, want it held for an approval", name, err)
		}
	}

	// A task whose origin is its own tree is still a root.
	self := model.Task{ID: "15", Target: &widgets, WorkOrigin: model.TaskOrigin("15")}
	if err := p.Check(self, model.NetworkOpen); err != nil {
		t.Errorf("a root naming its own tree was refused: %v", err)
	}
	if p.IsLocalRequester("") {
		t.Error("IsLocalRequester(\"\") = true")
	}
}

// A derived task is judged as its root requester's: grain's own and the
// owner's run on the approval that queued them, and anybody else's
// needs a human's approval of this task -- not the automation's that
// auto-submitted it, and not its own requester's.
func TestDerivedTaskIsJudgedAsItsRootRequester(t *testing.T) {
	p := mustParse(t, "repos: [acme/*]\nlocal_requesters: [owner@example.com]\n")
	task := derivedTask("", autoSubmit)

	for name, c := range map[string]struct {
		root Requester
		task model.Task
		want string
	}{
		"grain's own root":           {Requester{}, task, ""},
		"the owner's root":           {Requester{Name: "owner@example.com"}, task, ""},
		"an external root":           {Requester{Name: "friend@example.com"}, task, "no human approval"},
		"an unresolved root":         {Requester{Unresolved: true}, task, "no human approval"},
		"an external root, approved": {Requester{Name: "friend@example.com"}, derivedTask("friend@example.com", lead), ""},
		"an external root, self-approved": {Requester{Name: "friend@example.com"},
			derivedTask("", model.Principal{Kind: model.PrincipalHuman, ID: "friend@example.com"}), "its own requester"},
	} {
		err := p.CheckAs(c.task, c.root, model.NetworkOpen)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: refused: %v", name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: err = %v, want one saying %q", name, err, c.want)
		}
	}
}
