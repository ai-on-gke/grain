package model_test

import (
	"slices"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

// One predicate: a console session (which holds durable) and a holder of
// priority-dispatch both have priority dispatch, and nothing else does --
// not an interactive task, not a title, and not a bare session tag without
// the grant.
func TestHasPriorityDispatchIsSessionsAndHoldersOfTheGrant(t *testing.T) {
	t.Parallel()
	session := model.Task{
		Origin:      model.Origin{Reason: model.ReasonConsoleSession},
		Interactive: true,
		Tags:        []string{model.ConsoleSessionTag(model.DirectorSessionID)},
		Grants:      model.ConsoleSessionGrants(model.ConsoleSession{ID: model.DirectorSessionID}),
	}
	bareSession := model.Task{
		Origin:      model.Origin{Reason: model.ReasonConsoleSession},
		Interactive: true,
		Tags:        []string{model.ConsoleSessionTag(model.DirectorSessionID)},
	}
	holder := model.Task{Grants: []model.Grant{{Capability: model.PriorityDispatchCapability}}}
	for name, c := range map[string]struct {
		task model.Task
		want bool
	}{
		"session":          {session, true},
		"bare session":     {bareSession, false},
		"holder":           {holder, true},
		"ordinary":         {model.Task{}, false},
		"interactive":      {model.Task{Interactive: true, Title: "Director"}, false},
		"other capability": {model.Task{Grants: []model.Grant{{Capability: "self-debug"}}}, false},
	} {
		if got := c.task.HasPriorityDispatch(); got != c.want {
			t.Errorf("%s: HasPriorityDispatch = %v, want %v", name, got, c.want)
		}
	}
}

// Only a person may grant priority-dispatch: a proposal naming it is an
// escalation even when its proposer holds it, and a template carrying it
// is refused to an agent that holds it too.
func TestPriorityDispatchIsAPersonsToGrant(t *testing.T) {
	t.Parallel()
	if !model.IsPersonOnlyGrant(model.PriorityDispatchCapability) {
		t.Fatal("priority-dispatch is not a person-only grant")
	}
	if model.IsPersonOnlyGrant("self-debug") {
		t.Fatal("self-debug reads as a person-only grant")
	}
	parent := model.Task{ID: "steward", Grants: []model.Grant{
		{Capability: model.PriorityDispatchCapability, Via: model.GrantByGrain},
		{Capability: "self-debug", Via: model.GrantByGrain}}}
	child := model.Task{Grants: []model.Grant{
		{Capability: model.PriorityDispatchCapability, Via: model.GrantByLabel},
		{Capability: "self-debug", Via: model.GrantByLabel}}}
	if got := model.EscalatedCapabilities(child, parent); !slices.Equal(got, []string{model.PriorityDispatchCapability}) {
		t.Errorf("EscalatedCapabilities = %v, want priority-dispatch alone", got)
	}
	tmpl := model.Template{ID: "jumper", Grants: []model.Grant{{Capability: model.PriorityDispatchCapability}}}
	if err := model.TemplateWithinTask(parent, tmpl); err == nil {
		t.Error("an agent holding priority-dispatch may file from a template granting it")
	}
}
