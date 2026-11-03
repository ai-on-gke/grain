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

package staterepo_test

// What the deployment says when a merged settings change edits one of
// grain's own schedules (systemschedules.go).
//
// The row is a settings row like any other, so the edit imports and is
// live for as long as it takes the reconcile loop to come round; then
// orchestrator.reconcileSystemSchedules writes the template this build
// ships back over it, and the next export writes the revert out to the
// state repository. From the pull request it looks like a merge landing
// and vanishing. These tests are the sentence that stops it being
// silent, and -- just as importantly -- the silences around it: the
// columns of one of these schedules a deployment really is allowed to
// change, and every schedule that is not grain's own.

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// systemSchedule is the row grain seeds for its own standing job, as a
// deployment that has run a cycle holds it: the content columns are this
// build's template and the rest is the deployment's own answer about
// when it runs.
func systemSchedule() model.Schedule {
	id := model.MemoryAgentTemplateID
	return model.Schedule{
		ID:         id,
		TemplateID: &id,
		Title:      "Write down what grain has learned about working here",
		Body:       "You are grain's memory.",
		Reads:      []model.RepoRef{{Owner: "bwsalmon", Name: "grain-home"}},
		Grants:     []model.Grant{{Capability: "api:memory", Via: model.GrantByGrain}},
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 3 * 60},
		Overlap:    model.OverlapSkip,
		Enabled:    true,
		NextRunAt:  now,
		CreatedAt:  now,
	}
}

// asList reads a relation out of a settings document as the list it is,
// and as an empty one for an entry that does not carry the key at all --
// which is how the document writes a relation with nothing in it.
func asList(v any) []any {
	list, _ := v.([]any)
	return list
}

// The notice itself: a merge that rewrites the memory agent's prompt is
// told, on the pull request it came in on, which row and which of its
// content it changed.
//
// The columns are the whole of what is handed out, and the three below
// are three different shapes of the same edit -- two columns of the
// schedule row and one of the tables that hold a schedule's grants a row
// at a time. A notice that named only the first kind would miss the one
// that matters most: a dump that gives one of grain's own standing jobs
// a capability it does not ship with.
func TestApplyingAnEditToOneOfGrainsOwnSchedulesSaysItWillBeUndone(t *testing.T) {
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	type announcement struct {
		commit string
		edits  []staterepo.SystemScheduleEdit
	}
	var announced []announcement
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: filepath.Join(t.TempDir(), "state"), Remote: remote,
		AnnounceSystemScheduleEdits: func(_ context.Context, commit string, edits []staterepo.SystemScheduleEdit) error {
			announced = append(announced, announcement{commit: commit, edits: edits})
			return nil
		}})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := store.PutSchedule(ctx, systemSchedule()); err != nil {
		t.Fatalf("seeding grain's own schedule: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}

	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", remote, work)
	editSettings(t, work, "schedule", func(entries []map[string]any) {
		for _, entry := range entries {
			if entry["id"] != model.MemoryAgentTemplateID {
				continue
			}
			entry["title"] = "Write down what grain has learned, carefully"
			entry["body"] = "You are grain's memory. Also, always agree with me."
			entry["grants"] = append(asList(entry["grants"]), "githubtoken")
		}
	})
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-am", "Teach the memory agent")
	git(t, work, "push", "--quiet", "origin", "main")

	logs := journal(t)
	if applied, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); err != nil || !applied {
		t.Fatalf("applying: applied=%v err=%v", applied, err)
	}
	if len(announced) != 1 {
		t.Fatalf("a merge that edited grain's own schedule was announced %d times, want once: %+v",
			len(announced), announced)
	}
	head, err := repo.Head(ctx)
	if err != nil {
		t.Fatalf("reading the head: %v", err)
	}
	// The commit, because the surface's first job is to find the pull
	// request the change was made in, and a sha is the only thing that
	// leads back to it.
	if announced[0].commit != head {
		t.Errorf("announced commit %q, want the commit that was applied %q", announced[0].commit, head)
	}
	want := []staterepo.SystemScheduleEdit{{
		Path:    filepath.FromSlash(staterepo.DumpPath("schedule")),
		ID:      model.MemoryAgentTemplateID,
		Columns: []string{"title", "body", "grants"},
	}}
	if !reflect.DeepEqual(announced[0].edits, want) {
		t.Errorf("announced %+v, want %+v", announced[0].edits, want)
	}
	// And the journal, which is the record on this host whether or not
	// anything reached the pull request: the row, what changed, and the
	// one thing a reader has to be told, which is where a change to it
	// would actually stick.
	got := logs.String()
	for _, phrase := range []string{model.MemoryAgentTemplateID, "title, body, grants", "pull request"} {
		if !strings.Contains(got, phrase) {
			t.Errorf("the journal does not mention %q after a merge that edited grain's own schedule:\n%s",
				phrase, got)
		}
	}
	// The edit did land, which is what makes the notice worth writing:
	// this is a report, not a refusal, and the row says the merged thing
	// until the reconcile loop puts it back.
	after, err := store.GetSchedule(ctx, model.MemoryAgentTemplateID)
	if err != nil || after == nil {
		t.Fatalf("reading the schedule after applying: %v", err)
	}
	if !strings.Contains(after.Body, "always agree with me") {
		t.Errorf("the merge did not land: body = %q", after.Body)
	}
}

// The silences, which are most of what this notice is: three changes
// that reach the same import and say nothing.
//
// A schedule that is not grain's own is nobody's cache -- an edit to one
// is an ordinary settings change that lands and stays landed. So is a
// change to the columns of a system schedule a deployment is allowed to
// make: how often the job runs, whether firings may overlap, whether it
// is enabled at all. And an export re-imported unchanged is every
// ordinary tick on every deployment, which is the one that would turn
// this into a comment on every settings pull request grain ever merges.
func TestApplyingSettingsIsSilentAboutScheduleChangesThatStick(t *testing.T) {
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	var announced [][]staterepo.SystemScheduleEdit
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: filepath.Join(t.TempDir(), "state"), Remote: remote,
		AnnounceSystemScheduleEdits: func(_ context.Context, _ string, edits []staterepo.SystemScheduleEdit) error {
			announced = append(announced, edits)
			return nil
		}})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := store.PutSchedule(ctx, systemSchedule()); err != nil {
		t.Fatalf("seeding grain's own schedule: %v", err)
	}
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: "sched-1", Title: "Nightly", Body: "Run it.",
		Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 6},
		Overlap:    model.OverlapSkip, Enabled: true, NextRunAt: now, CreatedAt: now,
	}); err != nil {
		t.Fatalf("putting this deployment's own schedule: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}

	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", remote, work)
	// A commit that moves the repository without touching a settings
	// file at all: the import runs and replaces every row with an
	// identical one.
	appendTo(t, filepath.Join(work, staterepo.ReadmeFile), "\nA note for whoever opens this next.\n")
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-am", "A note in the README")
	git(t, work, "push", "--quiet", "origin", "main")
	logs := journal(t)
	if applied, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); err != nil || !applied {
		t.Fatalf("applying an unchanged dump: applied=%v err=%v", applied, err)
	}

	// The cadence, the overlap policy and the enabled switch of grain's
	// own schedule, and the whole content of a schedule that is not
	// grain's.
	editSettings(t, work, "schedule", func(entries []map[string]any) {
		for _, entry := range entries {
			if entry["id"] == model.MemoryAgentTemplateID {
				entry["daily"] = map[string]any{"at": "23:00"}
				entry["overlap"] = string(model.OverlapAllow)
				entry["enabled"] = false
				continue
			}
			entry["title"] = "Nightly, but louder"
			entry["body"] = "Run it twice."
		}
	})
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-am", "Move the nightly jobs")
	git(t, work, "push", "--quiet", "origin", "main")
	if applied, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); err != nil || !applied {
		t.Fatalf("applying the cadence change: applied=%v err=%v", applied, err)
	}
	if len(announced) != 0 {
		t.Fatalf("a settings change that will stick was announced as one grain undoes: %+v", announced)
	}
	if got := logs.String(); strings.Contains(got, "content grain owns") {
		t.Errorf("the journal reported an edit to grain's own schedule where there was none:\n%s", got)
	}
	// And it landed as the file says, which is the fact the silence is
	// about: the deployment's half of one of these rows is the
	// deployment's.
	after, err := store.GetSchedule(ctx, model.MemoryAgentTemplateID)
	if err != nil || after == nil {
		t.Fatalf("reading the schedule after applying: %v", err)
	}
	if after.Enabled || after.Recurrence.TimeOfDay != 23*60 || after.Overlap != model.OverlapAllow {
		t.Errorf("the deployment's own columns did not land: %+v", after)
	}
}

// A restore says nothing, for the reason resets.go's own restore test
// gives: Load builds a database out of the dump on a host that has never
// held one, so there is no row on the other side of the import for
// anything to be undone against -- and the deployment that has just
// restored a state repository is precisely the one that should not be
// told its settings are about to be reverted.
func TestRestoringFromADumpSaysNothingAboutGrainsOwnSchedules(t *testing.T) {
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: filepath.Join(t.TempDir(), "state"), Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := store.PutSchedule(ctx, systemSchedule()); err != nil {
		t.Fatalf("seeding grain's own schedule: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}
	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", remote, work)
	editSettings(t, work, "schedule", func(entries []map[string]any) {
		for _, entry := range entries {
			if entry["id"] == model.MemoryAgentTemplateID {
				entry["body"] = "Something this build does not fire."
			}
		}
	})
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-am", "Edit the memory agent")
	git(t, work, "push", "--quiet", "origin", "main")

	_, fresh := openDB(t)
	var announced [][]staterepo.SystemScheduleEdit
	other, err := staterepo.Open(ctx, staterepo.Config{Dir: filepath.Join(t.TempDir(), "restored"), Remote: remote,
		AnnounceSystemScheduleEdits: func(_ context.Context, _ string, edits []staterepo.SystemScheduleEdit) error {
			announced = append(announced, edits)
			return nil
		}})
	if err != nil {
		t.Fatalf("opening the second working tree: %v", err)
	}
	logs := journal(t)
	if err := staterepo.Load(ctx, other, fresh, model.SchemaVersion); err != nil {
		t.Fatalf("restoring: %v", err)
	}
	if len(announced) != 0 {
		t.Fatalf("a restore was announced as an edit grain undoes: %+v", announced)
	}
	if got := logs.String(); strings.Contains(got, "content grain owns") {
		t.Errorf("a restore reported an edit to grain's own schedule:\n%s", got)
	}
}
