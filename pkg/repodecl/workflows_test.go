package repodecl_test

// A workflow's own `on: schedule` cron, read as one of this deployment's
// task schedules (grain/task-344). The cron-to-cadence translation itself
// is pkg/ci's (cron_test.go); what is asserted here is what comes out of a
// repository: whose namespace the schedule is in, which file it points at,
// what it fires, and what happens to it when the cron changes or goes away.

import (
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// nightlyWorkflow is the shape of the real case this exists for:
// bwsalmon/grain's own .grain/workflows/nightly.yml, a cron and a dispatch.
const nightlyWorkflow = `
name: nightly
on:
  schedule:
    - cron: "11 2 * * *"
  workflow_dispatch:
jobs:
  go-long:
    steps:
      - run: make test-long
`

func TestAWorkflowCronBecomesADeclaredDailySchedule(t *testing.T) {
	declared := load(t, map[string]string{".grain/workflows/nightly.yml": nightlyWorkflow})
	if len(declared.Schedules) != 1 {
		t.Fatalf("declared %d schedules, want 1", len(declared.Schedules))
	}
	file := declared.Schedules[0]
	if file.Path != ".grain/workflows/nightly.yml" {
		// The provenance row is built from this, and it is what the UI
		// names when it refuses an edit.
		t.Fatalf("declared in %q, want the workflow file", file.Path)
	}
	sched := file.Schedule
	if !model.IsRepoDeclaredID(sched.ID) {
		t.Fatalf("id %q is not in the declared namespace", sched.ID)
	}
	if sched.ID != model.RepoDeclaredID(model.DeclaredSchedule, repo, "workflow.nightly") {
		t.Fatalf("id %q", sched.ID)
	}
	if sched.Target != repo {
		t.Fatalf("target %v, want the declaring repo", sched.Target)
	}
	want := model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 2*60 + 11}
	if sched.Recurrence != want {
		t.Fatalf("recurrence %+v, want %+v", sched.Recurrence, want)
	}
	if !sched.Enabled || sched.Overlap != model.OverlapSkip {
		t.Fatalf("enabled %v overlap %q", sched.Enabled, sched.Overlap)
	}
	if !sched.AutoApproveProposals {
		t.Fatalf("autoApproveProposals = %v, want true", sched.AutoApproveProposals)
	}
	if sched.Base != "" {
		t.Fatalf("base %q, want empty -- a cron runs on the default branch", sched.Base)
	}
	if sched.TemplateID == nil || *sched.TemplateID != model.WorkflowReviewTemplateID("nightly") {
		t.Fatalf("template %v, want grain's own workflow review", sched.TemplateID)
	}
	// And that template has to be one this build can actually resolve at
	// firing time, or the schedule fails every firing.
	if model.SystemTemplate(*sched.TemplateID, model.RepoRef{}) == nil {
		t.Fatalf("no system template for %q", *sched.TemplateID)
	}
}

func TestAWorkflowCronWithExplicitTimeZone(t *testing.T) {
	wf := `
on:
  schedule:
    - cron: "0 9 * * 1"
      timezone: "America/Los_Angeles"
jobs:
  weekly:
    steps:
      - run: make test
`
	declared := load(t, map[string]string{".grain/workflows/weekly.yml": wf})
	if len(declared.Schedules) != 1 {
		t.Fatalf("declared %d schedules, want 1", len(declared.Schedules))
	}
	sched := declared.Schedules[0].Schedule
	if sched.TimeZone != "America/Los_Angeles" {
		t.Errorf("sched.TimeZone = %q, want America/Los_Angeles", sched.TimeZone)
	}
	if sched.Recurrence.TimeZone != "America/Los_Angeles" {
		t.Errorf("sched.Recurrence.TimeZone = %q, want America/Los_Angeles", sched.Recurrence.TimeZone)
	}
}

func TestAnInexpressibleWorkflowCronIsRefusedAndDeclaresNothing(t *testing.T) {
	err := loadErr(t, map[string]string{".grain/workflows/often.yml": `
on:
  schedule:
    - cron: "*/15 * * * *"
jobs:
  test:
    steps:
      - run: true
`})
	for _, want := range []string{"schedule:", "cron:", "*/15 * * * *", "daily", "weekly", "monthly", "every N hours"} {
		if !strings.Contains(err, want) {
			t.Fatalf("refusal does not mention %q: %s", want, err)
		}
	}
}

func TestEveryCronShapeATaskScheduleCanExpress(t *testing.T) {
	for _, tc := range []struct {
		cron string
		want model.Recurrence
	}{
		{"11 2 * * *", model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 131}},
		{"0 9 * * mon", model.Recurrence{Kind: model.RecurrenceWeekly, TimeOfDay: 540, Weekday: time.Monday}},
		{"30 6 * * 0", model.Recurrence{Kind: model.RecurrenceWeekly, TimeOfDay: 390, Weekday: time.Sunday}},
		{"0 4 1 * *", model.Recurrence{Kind: model.RecurrenceMonthly, TimeOfDay: 240, DayOfMonth: 1}},
	} {
		declared := load(t, map[string]string{".grain/workflows/w.yml": "on:\n  schedule:\n    - cron: \"" +
			tc.cron + "\"\njobs:\n  test:\n    steps:\n      - run: true\n"})
		if len(declared.Schedules) != 1 {
			t.Fatalf("%s: declared %d schedules", tc.cron, len(declared.Schedules))
		}
		if got := declared.Schedules[0].Schedule.Recurrence; got != tc.want {
			t.Fatalf("%s: %+v, want %+v", tc.cron, got, tc.want)
		}
	}
}

func TestAWorkflowWithNoCronDeclaresNoSchedule(t *testing.T) {
	declared := load(t, map[string]string{".grain/workflows/tests.yml": `
on: push
jobs:
  test:
    steps:
      - run: go test ./...
`})
	if len(declared.Schedules) != 0 {
		t.Fatalf("declared %d schedules from a workflow with no cron", len(declared.Schedules))
	}
}

// GitHub's own scheduler already runs a `.github/workflows` cron, so
// translating it as well would run every nightly twice.
func TestAGitHubWorkflowCronIsLeftToGitHub(t *testing.T) {
	declared := load(t, map[string]string{".github/workflows/nightly.yml": `
on:
  schedule:
    - cron: "11 2 * * *"
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - run: true
`})
	if len(declared.Schedules) != 0 {
		t.Fatalf("declared %d schedules from a GitHub Actions cron", len(declared.Schedules))
	}
}

// Two crons in one file are two cadences, so they are two schedules --
// numbered, since one stem cannot identify both.
func TestSeveralCronsInOneWorkflowAreSeveralSchedules(t *testing.T) {
	declared := load(t, map[string]string{".grain/workflows/sweep.yml": `
on:
  schedule:
    - cron: "0 9 * * mon"
    - cron: "0 9 * * thu"
jobs:
  test:
    steps:
      - run: true
`})
	if len(declared.Schedules) != 2 {
		t.Fatalf("declared %d schedules, want 2", len(declared.Schedules))
	}
	ids := map[string]bool{}
	for _, file := range declared.Schedules {
		if file.Path != ".grain/workflows/sweep.yml" {
			t.Fatalf("declared in %q", file.Path)
		}
		ids[file.Schedule.ID] = true
	}
	for _, stem := range []string{"workflow.sweep.1", "workflow.sweep.2"} {
		if !ids[model.RepoDeclaredID(model.DeclaredSchedule, repo, stem)] {
			t.Fatalf("no schedule for %q: %v", stem, ids)
		}
	}
}

// A repository declaring a schedule by hand and a workflow cron whose stem
// collides with it is one row written twice, with whichever was read last
// winning. It is an error naming both files instead.
func TestAHandWrittenScheduleAndAWorkflowCronCannotShareAnID(t *testing.T) {
	err := loadErr(t, map[string]string{
		".grain/templates/nightly.yml": "title: Nightly\n",
		".grain/schedules/workflow.nightly.yml": `
template: nightly
daily:
  at: "12:00"
`,
		".grain/workflows/nightly.yml": nightlyWorkflow,
	})
	if !strings.Contains(err, ".grain/workflows/nightly.yml") ||
		!strings.Contains(err, ".grain/schedules/workflow.nightly.yml") {
		t.Fatalf("collision does not name both files: %s", err)
	}
}

// A workflow that does not parse takes the whole read down, exactly as a
// malformed declaration file does: a repository keeps the declarations it
// already had rather than having half of them replaced
// (orchestrator.syncDeclarations).
func TestABrokenWorkflowFailsTheWholeRead(t *testing.T) {
	err := loadErr(t, map[string]string{
		".grain/workflows/nightly.yml": nightlyWorkflow,
		".grain/workflows/broken.yml":  "on: push\njobs:\n  test:\n    uses: actions/checkout@v4\n",
	})
	if !strings.Contains(err, "broken.yml") {
		t.Fatalf("error does not name the broken workflow: %s", err)
	}
}

// A schedule file may run one of the repository's own workflows on a
// branch of its choosing (grain/task-3159): the cron in the workflow is
// the default branch's, and this is how the same suite also runs on a
// release candidate. What comes out has to be exactly the schedule a cron
// is translated into, base aside -- grain's review template as the thing
// it fires is what sends it down the scheduled-workflow path rather than
// filing an agent task -- or the two runs are not the same thing.
func TestAScheduleFileRunsAWorkflowOnItsBase(t *testing.T) {
	for _, name := range []string{"nightly", "nightly.yml", ".grain/workflows/nightly.yml"} {
		t.Run(name, func(t *testing.T) {
			declared := load(t, map[string]string{
				".grain/workflows/nightly.yml": nightlyWorkflow,
				".grain/schedules/nightly-candidate.yml": `
workflow: ` + name + `
base: candidate
daily:
  at: "14:11"
`,
			})
			var sched *model.Schedule
			var cron *model.Schedule
			for i, file := range declared.Schedules {
				switch file.Path {
				case ".grain/schedules/nightly-candidate.yml":
					sched = &declared.Schedules[i].Schedule
				case ".grain/workflows/nightly.yml":
					cron = &declared.Schedules[i].Schedule
				}
			}
			if sched == nil || cron == nil {
				t.Fatalf("declared %+v, want the cron's schedule and the file's", declared.Schedules)
			}
			if sched.ID != model.RepoDeclaredID(model.DeclaredSchedule, repo, "nightly-candidate") {
				t.Fatalf("id %q, want the file's own", sched.ID)
			}
			if sched.TemplateID == nil || *sched.TemplateID != model.WorkflowReviewTemplateID("nightly") {
				t.Fatalf("template %v, want grain's own workflow review", sched.TemplateID)
			}
			if *sched.TemplateID != *cron.TemplateID {
				t.Fatalf("fires %q where the cron fires %q -- one workflow, one way of running it",
					*sched.TemplateID, *cron.TemplateID)
			}
			if sched.Base != "candidate" {
				t.Fatalf("base %q, want candidate", sched.Base)
			}
			if sched.Overlap != model.OverlapSkip || !sched.AutoApproveProposals || !sched.Enabled {
				t.Fatalf("overlap %q, auto-approve %v, enabled %v -- want what a cron gets",
					sched.Overlap, sched.AutoApproveProposals, sched.Enabled)
			}
			if want := `Review last night's "nightly" workflow run on candidate`; sched.Title != want {
				t.Fatalf("title %q, want %q", sched.Title, want)
			}
			if sched.SuiteID != nil {
				t.Fatalf("suite %v, want none", *sched.SuiteID)
			}
		})
	}
}

func TestAWorkflowScheduleKeepsTheNameItIsGiven(t *testing.T) {
	declared := load(t, map[string]string{
		".grain/workflows/nightly.yml": nightlyWorkflow,
		".grain/schedules/nightly-candidate.yml": `
name: Candidate nightly
workflow: nightly
base: candidate
auto-approve-proposals: false
overlap: skip
daily:
  at: "14:11"
`,
	})
	for _, file := range declared.Schedules {
		if file.Path != ".grain/schedules/nightly-candidate.yml" {
			continue
		}
		if file.Schedule.Title != "Candidate nightly" {
			t.Fatalf("title %q", file.Schedule.Title)
		}
		if file.Schedule.AutoApproveProposals {
			t.Fatalf("auto-approve-proposals: false was not kept")
		}
		return
	}
	t.Fatalf("no schedule from the file in %+v", declared.Schedules)
}

func TestAWorkflowScheduleIsRefusedWhenItCannotRun(t *testing.T) {
	unscheduled := `
on:
  workflow_dispatch:
jobs:
  go-long:
    steps:
      - run: make test-long
`
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{
			name: "no such workflow",
			files: map[string]string{".grain/schedules/x.yml": `
workflow: nightly
daily:
  at: "14:11"
`},
			want: `no workflow "nightly"`,
		},
		{
			name: "a template as well",
			files: map[string]string{
				".grain/workflows/nightly.yml": nightlyWorkflow,
				".grain/templates/t.yml":       "title: T\nbody: b\n",
				".grain/schedules/x.yml": `
workflow: nightly
template: t
daily:
  at: "14:11"
`},
			want: "not several",
		},
		{
			name: "a workflow no schedule event runs",
			files: map[string]string{
				".grain/workflows/nightly.yml": unscheduled,
				".grain/schedules/x.yml": `
workflow: nightly
daily:
  at: "14:11"
`},
			want: "has no `on: schedule`",
		},
		{
			name: "overlapping runs",
			files: map[string]string{
				".grain/workflows/nightly.yml": nightlyWorkflow,
				".grain/schedules/x.yml": `
workflow: nightly
overlap: allow
daily:
  at: "14:11"
`},
			want: "cannot overlap",
		},
		{
			name: "a .yaml workflow",
			files: map[string]string{
				".grain/workflows/nightly.yaml": nightlyWorkflow,
				".grain/schedules/x.yml": `
workflow: nightly
daily:
  at: "14:11"
`},
			want: "rename it",
		},
		{
			name: "a review policy grain does not know",
			files: map[string]string{
				".grain/workflows/nightly.yml": nightlyWorkflow,
				".grain/schedules/x.yml": `
workflow: nightly
review: sometimes
daily:
  at: "14:11"
`},
			want: `got "sometimes"`,
		},
		{
			name: "a review policy on a schedule that runs no workflow",
			files: map[string]string{
				".grain/templates/t.yml": "title: T\nbody: b\n",
				".grain/schedules/x.yml": `
template: t
review: always
daily:
  at: "14:11"
`},
			want: "runs no `workflow:`",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if msg := loadErr(t, tc.files); !strings.Contains(msg, tc.want) {
				t.Fatalf("error %q, want it to say %q", msg, tc.want)
			}
		})
	}
}
