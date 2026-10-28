package model_test

// CIStepOutputChange: the question "is a single UPDATE the same write as
// replacing this whole run?", which is what lets a step publishing its
// log every five seconds cost one cell rather than every row under the
// run (ci.LiveOutputInterval, StoredRunStore.Update).
//
// A pure comparison of two runs, so it is tested as one. What it is
// guarding is a false yes -- a narrow write taken where the whole-run one
// would have left different rows behind -- so most of these cases are
// changes that must *not* be mistaken for a live publication.

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func changeRun() model.CIRun {
	started := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	return model.CIRun{
		ID: "3f2a1c", Key: "key", Repo: "owner/payments-api", Ref: "grain/task-1",
		Commit: "9f8e7d6c", Event: "push", StartedAt: started,
		Jobs: []model.CIJob{
			{
				Workflow: "tests", WorkflowPath: ".grain/workflows/tests.yml", JobID: "unit",
				StartedAt: started,
				Steps: []model.CIStep{
					{Name: "go build", Conclusion: "success", StartedAt: started},
					{Name: "go test", Output: "ok  pkg/ci\n", StartedAt: started},
				},
			},
			{
				Workflow: "tests", WorkflowPath: ".grain/workflows/tests.yml", JobID: "frontend",
				Steps: []model.CIStep{{Name: "npm test"}},
			},
		},
	}
}

func TestOneStepsGrowingOutputIsRecognisedAsTheOnlyChange(t *testing.T) {
	before := changeRun()
	after := changeRun()
	after.Jobs[0].Steps[1].Output += "ok  pkg/model\n"

	job, step, ok := model.CIStepOutputChange(before, after)
	if !ok || job != 0 || step != 1 {
		t.Fatalf("a live publication was read as job %d step %d, ok %v", job, step, ok)
	}
}

// A run's times go through the database and come back as values a caller
// did not construct. The comparison is of columns, so that is not a
// difference: if it were, the narrow write would never once be taken on a
// real deployment, which is the way this optimisation would fail quietly.
func TestATimeThatHasBeenThroughTheDatabaseIsNotAChange(t *testing.T) {
	before := changeRun()
	after := changeRun()
	local := before.Jobs[0].StartedAt.In(time.FixedZone("elsewhere", 3600))
	after.Jobs[0].StartedAt = local
	after.StartedAt = before.StartedAt.In(time.FixedZone("elsewhere", 3600))
	after.Jobs[0].Steps[1].Output += "more\n"

	if job, step, ok := model.CIStepOutputChange(before, after); !ok || job != 0 || step != 1 {
		t.Fatalf("the same instant in another zone was read as a change: job %d step %d, ok %v", job, step, ok)
	}
}

func TestAnythingButOneStepsOutputIsTheWholeRunsWrite(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(*model.CIRun)
	}{
		{"nothing at all", func(*model.CIRun) {}},
		{"the run concluding", func(r *model.CIRun) { r.Conclusion = "success" }},
		{"a job finishing", func(r *model.CIRun) { r.Jobs[0].Conclusion = "failure" }},
		{"a job's activity", func(r *model.CIRun) { r.Jobs[0].Activity = "waiting for a sandbox" }},
		{"a step starting", func(r *model.CIRun) {
			r.Jobs[0].Steps[0].StartedAt = r.StartedAt.Add(time.Minute)
		}},
		{"a step finishing", func(r *model.CIRun) {
			r.Jobs[0].Steps[1].Output += "PASS\n"
			r.Jobs[0].Steps[1].Conclusion = "success"
		}},
		{"two steps' output at once", func(r *model.CIRun) {
			r.Jobs[0].Steps[1].Output += "more\n"
			r.Jobs[1].Steps[0].Output += "and more\n"
		}},
		{"a step appearing", func(r *model.CIRun) {
			r.Jobs[0].Steps = append(r.Jobs[0].Steps, model.CIStep{Name: "go vet"})
		}},
		{"a job appearing", func(r *model.CIRun) { r.Jobs = append(r.Jobs, model.CIJob{JobID: "publish"}) }},
		{"a different run entirely", func(r *model.CIRun) { r.ID = "somebody else" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			before := changeRun()
			after := changeRun()
			c.change(&after)
			if job, step, ok := model.CIStepOutputChange(before, after); ok {
				t.Errorf("read as one step's output (job %d, step %d), which would lose the rest of it", job, step)
			}
		})
	}
}
