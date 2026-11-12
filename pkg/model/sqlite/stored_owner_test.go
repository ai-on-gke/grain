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

package sqlite_test

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

var offHostWidgets = model.RepoRef{Host: "ghe.example.com", Owner: "acme", Name: "widgets"}

// A repository on another host has a qualification plan of its own, not
// the default host's same-named repository's, and removing it takes only
// its own (grain/task-4335).
func TestOffHostQualificationPlanIsItsOwn(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	home := testQualificationPlan()
	away := model.QualificationPlan{
		Repo: offHostWidgets, Configured: true, RequireApproval: true,
		Items: []model.QualificationItem{{TemplateID: "template-unit", Repeat: 3}},
	}
	for _, plan := range []model.QualificationPlan{home, away} {
		if err := store.PutQualificationPlan(ctx, plan); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []model.QualificationPlan{home, away} {
		got, err := store.GetQualificationPlan(ctx, want.Repo)
		if err != nil || got == nil || !reflect.DeepEqual(*got, want) {
			t.Fatalf("plan for %s = (%+v, %v), want %+v", want.Repo, got, err, want)
		}
	}
	using, err := store.QualificationPlansUsingTemplate(ctx, "template-unit")
	if err != nil {
		t.Fatal(err)
	}
	if want := []model.RepoRef{widgets, offHostWidgets}; !reflect.DeepEqual(using, want) {
		t.Fatalf("plans using template-unit = %v, want %v", using, want)
	}

	if err := store.ForgetRepo(ctx, offHostWidgets); err != nil {
		t.Fatal(err)
	}
	if got, err := store.GetQualificationPlan(ctx, offHostWidgets); err != nil || got != nil {
		t.Fatalf("plan for removed %s = (%+v, %v), want none", offHostWidgets, got, err)
	}
	if got, err := store.GetQualificationPlan(ctx, widgets); err != nil || got == nil || !reflect.DeepEqual(*got, home) {
		t.Fatalf("plan for %s after removing %s = (%+v, %v), want %+v", widgets, offHostWidgets, got, err, home)
	}
}

// A workflow schedule's firing reads back on the host it was written for,
// and removing that repository forgets it (grain/task-4335).
func TestOffHostScheduledWorkflowRunKeepsItsHost(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	for id, repo := range map[string]model.RepoRef{"sched-home": widgets, "sched-away": offHostWidgets} {
		if err := store.PutScheduledWorkflowRun(ctx, model.ScheduledWorkflowRun{
			ScheduleID: id, Stem: id, Repo: repo, Workflow: "ci", FiredAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.GetScheduledWorkflowRun(ctx, "sched-away")
	if err != nil || got == nil || got.Repo != offHostWidgets {
		t.Fatalf("firing for sched-away = (%+v, %v), want it in %s", got, err, offHostWidgets)
	}
	list, err := store.ListScheduledWorkflowRuns(ctx)
	if err != nil || len(list) != 2 || list[0].Repo != offHostWidgets || list[1].Repo != widgets {
		t.Fatalf("listed firings = (%+v, %v), want sched-away in %s and sched-home in %s",
			list, err, offHostWidgets, widgets)
	}

	if err := store.ForgetRepo(ctx, offHostWidgets); err != nil {
		t.Fatal(err)
	}
	if got, err := store.GetScheduledWorkflowRun(ctx, "sched-away"); err != nil || got != nil {
		t.Fatalf("firing for removed %s = (%+v, %v), want none", offHostWidgets, got, err)
	}
	if got, err := store.GetScheduledWorkflowRun(ctx, "sched-home"); err != nil || got == nil || got.Repo != widgets {
		t.Fatalf("firing for sched-home = (%+v, %v), want it kept in %s", got, err, widgets)
	}
}

// Migration 142 moves bare-owner qualification and workflow firing rows
// onto the host their schedule or the one target repo spelling them
// says, and leaves on the default host every row nothing settles.
func TestMigration142HostQualifiesQualificationAndWorkflowRunOwners(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	if err := store.PutConfig(ctx, testConfig()); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTargetRepos(ctx, []string{
		// Only off the default host: its rows move there.
		"ghe.example.com/acme/widgets",
		// On two hosts: ambiguous, so its rows stay.
		"acme/gadgets", "ghe.example.com/acme/gadgets",
		// Only on the default host: stays.
		"acme/tools",
	}); err != nil {
		t.Fatal(err)
	}
	// A workflow schedule in the default host's acme/widgets, which the
	// target list does not name: its firing stays where its schedule is,
	// even though the target list alone would move it.
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: "sched-home", Title: "Home", Body: "seed", Target: widgets,
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily}, Overlap: model.OverlapSkip,
		NextRunAt: now, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	// And one in another host's acme/tools, which the target list names
	// only on the default host: its schedule says otherwise.
	offHostTools := model.RepoRef{Host: "ghe.example.com", Owner: "acme", Name: "tools"}
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: "sched-away", Title: "Away", Body: "seed", Target: offHostTools,
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily}, Overlap: model.OverlapSkip,
		NextRunAt: now, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	// The rows a build before 142 wrote: bare owners throughout.
	for _, name := range []string{"widgets", "gadgets", "tools"} {
		for _, stmt := range []string{
			"INSERT INTO `qualification_config` (`owner`,`name`,`require_approval`,`auto_promote`) VALUES ('acme',?,1,0)",
			"INSERT INTO `qualification_item` (`owner`,`name`,`template_id`,`repeat_count`,`order_key`) VALUES ('acme',?,'template-unit',1,0)",
			"INSERT INTO `qualification_item_depends_on` (`owner`,`name`,`template_id`,`depends_on_template_id`) VALUES ('acme',?,'template-unit','template-build')",
		} {
			if _, err := db.ExecContext(ctx, stmt, name); err != nil {
				t.Fatal(err)
			}
		}
	}
	for id, name := range map[string]string{
		"sched-home": "widgets", "sched-away": "tools",
		// No schedule left: the target list decides.
		"sched-gone": "widgets", "sched-gone-gadgets": "gadgets",
	} {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO `scheduled_workflow_run` (`schedule_id`,`owner`,`name`,`fired_at`) VALUES (?,'acme',?,?)",
			id, name, now); err != nil {
			t.Fatal(err)
		}
	}

	migrate := func() {
		t.Helper()
		if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 141 WHERE `id` = 1"); err != nil {
			t.Fatal(err)
		}
		if err := model.New(db).Init(ctx); err != nil {
			t.Fatal(err)
		}
	}
	wantPlans := map[string][]string{
		"widgets": {"ghe.example.com/acme"},
		"gadgets": {"acme"},
		"tools":   {"acme"},
	}
	wantFirings := map[string]string{
		"sched-home":         "acme",
		"sched-away":         "ghe.example.com/acme",
		"sched-gone":         "ghe.example.com/acme",
		"sched-gone-gadgets": "acme",
	}
	verify := func(round string) {
		t.Helper()
		for _, table := range []string{"qualification_config", "qualification_item", "qualification_item_depends_on"} {
			for name, want := range wantPlans {
				if got := ownersOf(t, ctx, db, "SELECT `owner` FROM `"+table+"` WHERE `name` = ?", name); !reflect.DeepEqual(got, want) {
					t.Errorf("%s: `%s` owners for %s = %v, want %v", round, table, name, got, want)
				}
			}
		}
		for id, want := range wantFirings {
			got := ownersOf(t, ctx, db, "SELECT `owner` FROM `scheduled_workflow_run` WHERE `schedule_id` = ?", id)
			if !reflect.DeepEqual(got, []string{want}) {
				t.Errorf("%s: firing %s owner = %v, want %s", round, id, got, want)
			}
		}
	}
	migrate()
	verify("first run")
	if plan, err := store.GetQualificationPlan(ctx, offHostWidgets); err != nil || plan == nil {
		t.Errorf("plan for %s after migrating = (%+v, %v), want the moved one", offHostWidgets, plan, err)
	}
	if plan, err := store.GetQualificationPlan(ctx, widgets); err != nil || plan != nil {
		t.Errorf("plan for %s after migrating = (%+v, %v), want none: it was %s's", widgets, plan, err, offHostWidgets)
	}
	migrate()
	verify("second run")
}

// Migration 145 moves a bare-owner repository's releases onto the host
// the one target repo spelling it says, its candidates and qualification
// runs with them, and leaves on the default host every repository
// nothing settles.
func TestMigration145HostQualifiesReleaseOwners(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	if err := store.PutConfig(ctx, testConfig()); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTargetRepos(ctx, []string{
		// Only off the default host: its releases move there.
		"ghe.example.com/acme/widgets",
		// On two hosts: ambiguous, so its releases stay.
		"acme/gadgets", "ghe.example.com/acme/gadgets",
		// Only on the default host: stays.
		"acme/tools",
	}); err != nil {
		t.Fatal(err)
	}

	// The rows a build before 145 wrote: bare owners throughout, one
	// release, candidate and qualification run per repository.
	for i, name := range []string{"widgets", "gadgets", "tools"} {
		id := int64(i + 1)
		for _, stmt := range []struct {
			query string
			args  []any
		}{
			{"INSERT INTO `release` (`id`,`owner`,`repo`,`name`,`status`,`created_at`) VALUES (?,'acme',?,'myfeat','active',?)",
				[]any{id, name, now}},
			{"INSERT INTO `release_candidate` (`id`,`release_id`,`owner`,`repo`,`number`,`branch`,`status`,`created_at`) " +
				"VALUES (?,?,'acme',?,1,'release/myfeat-rc.1','active',?)", []any{id, id, name, now}},
			{"INSERT INTO `qualification_run` (`id`,`owner`,`name`,`candidate_id`,`created_at`) VALUES (?,'acme',?,?,?)",
				[]any{id, name, id, now}},
		} {
			if _, err := db.ExecContext(ctx, stmt.query, stmt.args...); err != nil {
				t.Fatal(err)
			}
		}
	}

	migrate := func() {
		t.Helper()
		if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 144 WHERE `id` = 1"); err != nil {
			t.Fatal(err)
		}
		if err := model.New(db).Init(ctx); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string][]string{
		"widgets": {"ghe.example.com/acme"},
		"gadgets": {"acme"},
		"tools":   {"acme"},
	}
	verify := func(round string) {
		t.Helper()
		for _, q := range []struct{ table, name string }{
			{"release", "repo"}, {"release_candidate", "repo"}, {"qualification_run", "name"},
		} {
			for name, want := range want {
				if got := ownersOf(t, ctx, db, "SELECT `owner` FROM `"+q.table+"` WHERE `"+q.name+"` = ?", name); !reflect.DeepEqual(got, want) {
					t.Errorf("%s: `%s` owners for %s = %v, want %v", round, q.table, name, got, want)
				}
			}
		}
	}
	migrate()
	verify("first run")
	release, err := store.GetRelease(ctx, offHostWidgets, "myfeat")
	if err != nil || release == nil || release.Repo != offHostWidgets {
		t.Fatalf("release myfeat of %s after migrating = (%+v, %v), want the moved one", offHostWidgets, release, err)
	}
	if got, err := store.GetRelease(ctx, widgets, "myfeat"); err != nil || got != nil {
		t.Errorf("release myfeat of %s after migrating = (%+v, %v), want none: it was %s's", widgets, got, err, offHostWidgets)
	}
	candidate, err := store.CurrentCandidateForRelease(ctx, release.ID)
	if err != nil || candidate == nil || candidate.Repo != offHostWidgets {
		t.Fatalf("candidate of the moved release = (%+v, %v), want it in %s", candidate, err, offHostWidgets)
	}
	if run, err := store.CandidateQualificationRun(ctx, candidate.ID); err != nil || run == nil || run.Repo != offHostWidgets {
		t.Errorf("qualification run of the moved candidate = (%+v, %v), want it in %s", run, err, offHostWidgets)
	}
	migrate()
	verify("second run")
}

func ownersOf(t *testing.T, ctx context.Context, db *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var owner string
		if err := rows.Scan(&owner); err != nil {
			t.Fatal(err)
		}
		out = append(out, owner)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
