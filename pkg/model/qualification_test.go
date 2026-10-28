package model_test

import (
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

func TestQualificationPlanValidateRejectsBlankTemplateID(t *testing.T) {
	plan := model.QualificationPlan{Items: []model.QualificationItem{{TemplateID: "", Repeat: 1}}}
	if err := plan.Validate(); err == nil {
		t.Fatal("want an error for a blank template id")
	}
}

func TestQualificationPlanValidateRejectsTheSameTemplateTwice(t *testing.T) {
	plan := model.QualificationPlan{Items: []model.QualificationItem{
		{TemplateID: "template-1", Repeat: 1},
		{TemplateID: "template-1", Repeat: 2},
	}}
	if err := plan.Validate(); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("got %v, want a used-more-than-once error", err)
	}
}

func TestQualificationPlanValidateRejectsRepeatLessThanOne(t *testing.T) {
	plan := model.QualificationPlan{Items: []model.QualificationItem{{TemplateID: "template-1", Repeat: 0}}}
	if err := plan.Validate(); err == nil {
		t.Fatal("want an error for repeat < 1")
	}
}

func TestQualificationPlanValidateRejectsSelfDependency(t *testing.T) {
	plan := model.QualificationPlan{Items: []model.QualificationItem{
		{TemplateID: "template-1", Repeat: 1, DependsOn: []string{"template-1"}},
	}}
	if err := plan.Validate(); err == nil || !strings.Contains(err.Error(), "itself") {
		t.Fatalf("got %v, want a self-dependency error", err)
	}
}

func TestQualificationPlanValidateRejectsUnknownDependency(t *testing.T) {
	plan := model.QualificationPlan{Items: []model.QualificationItem{
		{TemplateID: "template-1", Repeat: 1, DependsOn: []string{"template-missing"}},
	}}
	if err := plan.Validate(); err == nil || !strings.Contains(err.Error(), "not in this plan") {
		t.Fatalf("got %v, want a not-in-this-plan error", err)
	}
}

func TestQualificationPlanValidateRejectsACycle(t *testing.T) {
	plan := model.QualificationPlan{Items: []model.QualificationItem{
		{TemplateID: "a", Repeat: 1, DependsOn: []string{"b"}},
		{TemplateID: "b", Repeat: 1, DependsOn: []string{"a"}},
	}}
	if err := plan.Validate(); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("got %v, want a cycle error", err)
	}
}

func TestQualificationPlanValidateAcceptsAValidDiamond(t *testing.T) {
	plan := model.QualificationPlan{Items: []model.QualificationItem{
		{TemplateID: "build", Repeat: 1},
		{TemplateID: "unit", Repeat: 3, DependsOn: []string{"build"}},
		{TemplateID: "integration", Repeat: 1, DependsOn: []string{"build"}},
		{TemplateID: "release-notes", Repeat: 1, DependsOn: []string{"unit", "integration"}},
	}}
	if err := plan.Validate(); err != nil {
		t.Fatalf("want no error, got %v", err)
	}
}

func status(taskState model.State, approved bool) model.QualificationTaskStatus {
	return model.QualificationTaskStatus{TaskID: "x", Approved: approved, State: taskState}
}

func TestQualificationStatusPendingApprovalOutranksEverything(t *testing.T) {
	tasks := []model.QualificationTaskStatus{
		status(model.StateFailed, true),
		status(model.StateQueued, false),
	}
	if got := model.QualificationStatus(tasks); got != model.QualificationPendingApproval {
		t.Fatalf("got %v, want pending_approval", got)
	}
}

func TestQualificationStatusFailedOnAnyFailure(t *testing.T) {
	tasks := []model.QualificationTaskStatus{
		status(model.StateCompleted, true),
		status(model.StateFailed, true),
		status(model.StateRunning, true),
	}
	if got := model.QualificationStatus(tasks); got != model.QualificationFailed {
		t.Fatalf("got %v, want failed", got)
	}
}

func TestQualificationStatusSucceededOnceEveryTaskCompletes(t *testing.T) {
	tasks := []model.QualificationTaskStatus{
		status(model.StateCompleted, true),
		status(model.StateCompleted, true),
	}
	if got := model.QualificationStatus(tasks); got != model.QualificationSucceeded {
		t.Fatalf("got %v, want succeeded", got)
	}
}

func TestQualificationStatusRunningWhileStillInFlight(t *testing.T) {
	tasks := []model.QualificationTaskStatus{
		status(model.StateCompleted, true),
		status(model.StateRunning, true),
	}
	if got := model.QualificationStatus(tasks); got != model.QualificationRunning {
		t.Fatalf("got %v, want running", got)
	}
}

// A task closed without ever completing counts as a failure, the same as
// one that ran out of retries and failed outright -- QualificationStatus's
// own doc comment on why StateClosed is folded into anyFailed rather than
// treated as just another non-terminal state.
func TestQualificationStatusFailedOnATaskClosedWithoutCompleting(t *testing.T) {
	tasks := []model.QualificationTaskStatus{
		status(model.StateCompleted, true),
		status(model.StateClosed, true),
	}
	if got := model.QualificationStatus(tasks); got != model.QualificationFailed {
		t.Fatalf("got %v, want failed", got)
	}
}
