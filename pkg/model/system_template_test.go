package model_test

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A document a person wrote is marked with nothing but the bare fact --
// ", written by hand" on its index line (MemoryIndex) and in what the
// document tools answer -- and the reasoning behind that mark lives in
// Go doc comments no agent ever reads. MemoryHandNote says the whole of
// it in band for the short memory, which every run reads with the memory
// itself; there is no such sentence for a document, so this prompt is
// the only place it can be said, and it is said to the one run that is
// told to correct or delete whatever has stopped being true.
//
// In pkg/model rather than beside the other memory agent tests in
// pkg/orchestrator because what it asserts is this package's own text,
// and because the prompt is grain's rather than a deployment's
// (SystemTemplate's doc comment on why): a deployment cannot put this
// paragraph back by configuring anything.
func TestTheMemoryAgentIsToldWhatAHandWrittenDocumentIs(t *testing.T) {
	tmpl := model.SystemTemplate(model.MemoryAgentTemplateID,
		model.RepoRef{Owner: "bwsalmon", Name: "grain-home"})
	if tmpl == nil {
		t.Fatal("this build ships no memory agent template")
	}
	if !strings.Contains(tmpl.Body, "written by hand") {
		t.Fatal("the memory agent's prompt says nothing about a document somebody wrote by " +
			"hand, so the run that audits the documents is free to overwrite one for being " +
			"phrased differently -- the mark it sees on the index line explains nothing")
	}
	if !strings.Contains(tmpl.Body, "closing note") {
		t.Error("the prompt does not ask for an overwritten hand-written document in the " +
			"closing note, which is the only place the person who wrote it would find out")
	}
}

// One memory agent writes the deployment's memory again since memory
// stopped having teams (single-tenant reset, stage 4d): it is seeded, and
// the per-team agents' ids are retired but still resolve to it, so a row
// that fires before the reconciler deletes it files a task rather than
// failing.
func TestTheMemoryAgentIsSeededAndTheTeamAgentsAreRetired(t *testing.T) {
	t.Parallel()
	stateRepo := model.RepoRef{Owner: "bwsalmon", Name: "grain-home"}
	seeded := false
	for _, tmpl := range model.SystemTemplates(stateRepo) {
		seeded = seeded || tmpl.ID == model.MemoryAgentTemplateID
		if model.RetiredSystemScheduleID(tmpl.ID) {
			t.Errorf("SystemTemplates seeds %s, which is retired", tmpl.ID)
		}
	}
	if !seeded {
		t.Errorf("SystemTemplates does not seed %s, so no deployment gets a memory agent", model.MemoryAgentTemplateID)
	}
	if model.RetiredSystemScheduleID(model.MemoryAgentTemplateID) {
		t.Errorf("%s is retired, so the reconciler would delete the row it seeds", model.MemoryAgentTemplateID)
	}
	team := model.MemoryAgentTemplateID + ":" + model.DefaultTeamID
	if !model.RetiredSystemScheduleID(team) {
		t.Errorf("%s is not retired, so a deployment that has the row keeps a second memory agent", team)
	}
	tmpl := model.SystemTemplate(team, stateRepo)
	if tmpl == nil || tmpl.ID != model.MemoryAgentTemplateID {
		t.Errorf("SystemTemplate(%s) = %+v, want the deployment's memory agent", team, tmpl)
	}
}

func TestBaseRepairTemplateBodyHolisticInstructions(t *testing.T) {
	t.Parallel()
	tmpl := model.SystemTemplate(model.BaseRepairTemplateID, model.RepoRef{})
	if tmpl == nil {
		t.Fatal("this build ships no base repair template")
	}
	if strings.Contains(tmpl.Body, "smallest change that turns the suite green") {
		t.Errorf("the base repair template should not encourage narrow smallest change fixes, got:\n%s", tmpl.Body)
	}
	for _, want := range []string{
		"repairing the root base branch itself",
		"Take a holistic approach",
		"inspect and resolve all failing jobs in the suite",
		"shared base faults",
		"source of truth for `{{branch}}`",
		"Iterate and re-run CI until all required jobs in the suite pass",
	} {
		if !strings.Contains(tmpl.Body, want) {
			t.Errorf("the base repair template prompt is missing instruction %q:\n%s", want, tmpl.Body)
		}
	}
}
