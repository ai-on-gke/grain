package orchestrator_test

// reconcileDeclarations' behaviour, reached through RunCycle like every
// other reconciler here (schedule_test.go's own file comment): the rows
// a repository's `.grain` directory puts in the store, what a poll does
// when nothing has moved, and what happens when the switch goes off or
// the read fails (grain/task-60).

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// fakeDeclarations is a repository that says whatever a test tells it
// to, and counts what it was asked -- which is how "a poll that finds
// nothing new does not clone" is asserted at all.
type fakeDeclarations struct {
	commit   string
	declared model.RepoDeclarations
	err      error
	heads    int
	reads    int
}

func (f *fakeDeclarations) Head(context.Context, model.RepoRef) (string, error) {
	f.heads++
	if f.err != nil {
		return "", f.err
	}
	return f.commit, nil
}

func (f *fakeDeclarations) Read(context.Context, model.RepoRef) (model.RepoDeclarations, string, error) {
	f.reads++
	if f.err != nil {
		return model.RepoDeclarations{}, "", f.err
	}
	return f.declared, f.commit, nil
}

var declaringRepo = model.RepoRef{Owner: "acme", Name: "widgets"}

func nightlyDeclarations() model.RepoDeclarations {
	target := declaringRepo
	templateID := model.RepoDeclaredID(model.DeclaredTemplate, declaringRepo, "nightly")
	return model.RepoDeclarations{
		Templates: []model.DeclaredTemplateFile{{
			Path: model.TemplateDeclarationDir + "/nightly.yml",
			Template: model.Template{
				ID: templateID, Name: "nightly", Title: "Bump dependencies", Target: &target,
			},
		}},
		Schedules: []model.DeclaredScheduleFile{{
			Path: model.ScheduleDeclarationDir + "/nightly.yml",
			Schedule: model.Schedule{
				ID:         model.RepoDeclaredID(model.DeclaredSchedule, declaringRepo, "nightly"),
				Title:      "nightly",
				Target:     declaringRepo,
				TemplateID: &templateID,
				Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 24},
				Enabled:    true,
			},
		}},
	}
}

func declarationsOn(t *testing.T, store *model.Store, ctx context.Context, on bool) {
	t.Helper()
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: declaringRepo, Declarations: on}); err != nil {
		t.Fatalf("putting repo config: %v", err)
	}
}

func runDeclarations(t *testing.T, store *model.Store, source orchestrator.DeclarationSource, now time.Time) {
	t.Helper()
	deps := orchestrator.Deps{Store: store, Declarations: source}
	if err := orchestrator.RunCycle(t.Context(), deps, now); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
}

func TestACycleReadsADeclaringRepoAndStoresWhatItSays(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	declarationsOn(t, store, ctx, true)
	source := &fakeDeclarations{commit: "abc123", declared: nightlyDeclarations()}

	runDeclarations(t, store, source, baseTime)

	id := model.RepoDeclaredID(model.DeclaredSchedule, declaringRepo, "nightly")
	sched, err := store.GetSchedule(ctx, id)
	if err != nil || sched == nil {
		t.Fatalf("GetSchedule after a cycle = %v, %v, want the declared schedule", sched, err)
	}
	if sched.TemplateID == nil {
		t.Fatalf("schedule = %+v, want it firing the declared template", sched)
	}
	row, err := store.GetRepoDeclaration(ctx, id)
	if err != nil || row == nil {
		t.Fatalf("provenance = %v, %v", row, err)
	}
	if row.Path != model.ScheduleDeclarationDir+"/nightly.yml" {
		t.Errorf("provenance path = %q", row.Path)
	}
}

// A repository nobody switched on is a repository grain does not clone,
// does not poll, and knows nothing about.
func TestARepoWithoutTheSwitchIsNeverRead(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	declarationsOn(t, store, ctx, false)
	source := &fakeDeclarations{commit: "abc123", declared: nightlyDeclarations()}

	runDeclarations(t, store, source, baseTime)

	if source.heads != 0 || source.reads != 0 {
		t.Fatalf("looked at the repository %d/%d times, want none at all", source.heads, source.reads)
	}
	if sync, err := store.GetRepoDeclarationSync(ctx, declaringRepo); err != nil || sync != nil {
		t.Fatalf("sync row = %v, %v, want none", sync, err)
	}
}

// What makes polling cheap: a tip that has not moved is one commit check
// and no clone.
func TestAPollThatFindsNothingNewDoesNotRead(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	declarationsOn(t, store, ctx, true)
	source := &fakeDeclarations{commit: "abc123", declared: nightlyDeclarations()}

	runDeclarations(t, store, source, baseTime)
	// Far enough past the last check that the interval is no longer what
	// is holding the second poll back.
	runDeclarations(t, store, source, baseTime.Add(time.Hour))

	if source.reads != 1 {
		t.Fatalf("read the repository %d times, want 1 -- the second poll saw the same commit", source.reads)
	}
	if source.heads != 2 {
		t.Fatalf("checked the tip %d times, want 2", source.heads)
	}
}

// And what keeps the poll from being every tick: a repository looked at
// a moment ago is left alone.
func TestARepoLookedAtAMomentAgoIsLeftAlone(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	declarationsOn(t, store, ctx, true)
	source := &fakeDeclarations{commit: "abc123", declared: nightlyDeclarations()}

	runDeclarations(t, store, source, baseTime)
	runDeclarations(t, store, source, baseTime.Add(5*time.Second))

	if source.heads != 1 {
		t.Fatalf("checked the tip %d times in five seconds, want 1", source.heads)
	}
}

func TestAMovedRepoIsReadAgain(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	declarationsOn(t, store, ctx, true)
	source := &fakeDeclarations{commit: "abc123", declared: nightlyDeclarations()}
	runDeclarations(t, store, source, baseTime)

	moved := nightlyDeclarations()
	moved.Templates[0].Template.Title = "Bump dependencies (patch only)"
	source.commit, source.declared = "def456", moved
	runDeclarations(t, store, source, baseTime.Add(time.Hour))

	tmpl, err := store.GetTemplate(ctx, model.RepoDeclaredID(model.DeclaredTemplate, declaringRepo, "nightly"))
	if err != nil || tmpl == nil {
		t.Fatalf("GetTemplate = %v, %v", tmpl, err)
	}
	if tmpl.Title != "Bump dependencies (patch only)" {
		t.Fatalf("title = %q, want what the repository says now", tmpl.Title)
	}
}

// The switch has to actually stop it: a schedule that fired once more
// after an operator turned declarations off would make a liar of the
// switch, which is why the removal runs before anything can fire.
func TestTurningTheSwitchOffRemovesWhatTheRepoDeclared(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	declarationsOn(t, store, ctx, true)
	source := &fakeDeclarations{commit: "abc123", declared: nightlyDeclarations()}
	runDeclarations(t, store, source, baseTime)

	declarationsOn(t, store, ctx, false)
	runDeclarations(t, store, source, baseTime.Add(time.Hour))

	id := model.RepoDeclaredID(model.DeclaredSchedule, declaringRepo, "nightly")
	if sched, err := store.GetSchedule(ctx, id); err != nil || sched != nil {
		t.Fatalf("schedule after the switch went off = %v, %v, want nothing", sched, err)
	}
	if tmpl, err := store.GetTemplate(ctx,
		model.RepoDeclaredID(model.DeclaredTemplate, declaringRepo, "nightly")); err != nil || tmpl != nil {
		t.Fatalf("template after the switch went off = %v, %v, want nothing", tmpl, err)
	}
}

// A repository that cannot be read keeps what it last declared, and says
// why -- switching off a deployment's standing work over a syntax error
// would be the worse failure.
func TestAnUnreadableRepoKeepsItsDeclarationsAndRecordsTheReason(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	declarationsOn(t, store, ctx, true)
	source := &fakeDeclarations{commit: "abc123", declared: nightlyDeclarations()}
	runDeclarations(t, store, source, baseTime)

	source.err = fmt.Errorf("nightly.yml: line 3: did not find expected key")
	deps := orchestrator.Deps{Store: store, Declarations: source}
	if err := orchestrator.RunCycle(t.Context(), deps, baseTime.Add(time.Hour)); err == nil {
		t.Fatal("RunCycle: no error, want the failed read reported")
	}

	id := model.RepoDeclaredID(model.DeclaredSchedule, declaringRepo, "nightly")
	if sched, err := store.GetSchedule(ctx, id); err != nil || sched == nil {
		t.Fatalf("schedule after a failed read = %v, %v, want the last good one", sched, err)
	}
	sync, err := store.GetRepoDeclarationSync(ctx, declaringRepo)
	if err != nil || sync == nil {
		t.Fatalf("sync row = %v, %v", sync, err)
	}
	if sync.LastError == "" || sync.Commit != "abc123" {
		t.Fatalf("sync = %+v, want the reason recorded against the last good commit", sync)
	}
}

// A deployment with nothing switched on spends nothing here, and one
// with no source wired at all does not fail its cycle.
func TestNoSourceIsANoOp(t *testing.T) {
	t.Parallel()
	store, _ := openStore(t)
	deps := orchestrator.Deps{Store: store}
	if err := orchestrator.RunCycle(t.Context(), deps, baseTime); err != nil {
		t.Fatalf("RunCycle with no declaration source: %v", err)
	}
}

// An empty repository -- a default branch with no commit on it -- reads
// as declaring nothing rather than as a failure, and is not cloned for
// the privilege.
func TestAnEmptyRepoDeclaresNothingWithoutBeingCloned(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	declarationsOn(t, store, ctx, true)
	source := &fakeDeclarations{commit: ""}

	runDeclarations(t, store, source, baseTime)
	runDeclarations(t, store, source, baseTime.Add(time.Hour))

	if source.reads != 0 {
		t.Fatalf("cloned an empty repository %d times, want none", source.reads)
	}
	sync, err := store.GetRepoDeclarationSync(ctx, declaringRepo)
	if err != nil || sync == nil {
		t.Fatalf("sync row = %v, %v", sync, err)
	}
	if sync.LastError != "" {
		t.Fatalf("lastError = %q, want an empty repository to read as nothing rather than as a failure", sync.LastError)
	}
}

func TestACycleReadsDeclaredRepoSettings(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	declarationsOn(t, store, ctx, true)
	decl := nightlyDeclarations()
	decl.Settings = &model.DeclaredSettingsFile{
		Path: ".grain/settings.yml",
		Settings: model.RepoSettings{
			PromptExtension: "Read docs/x.md first.",
			SetupCommand:    "npm ci",
		},
	}
	source := &fakeDeclarations{commit: "abc123", declared: decl}

	runDeclarations(t, store, source, baseTime)

	cfg, err := store.GetRepoConfig(ctx, declaringRepo)
	if err != nil || cfg == nil {
		t.Fatalf("GetRepoConfig: %v, %v", cfg, err)
	}
	if cfg.DeclaredPromptExtension != "Read docs/x.md first." || cfg.DeclaredSetupCommand != "npm ci" {
		t.Errorf("repo config = %+v, want the declared layer applied", cfg)
	}
}

func TestDeclarationsIgnoreOffAllowlistRepo(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	declarationsOn(t, store, ctx, true)
	source := &fakeDeclarations{commit: "abc123", declared: nightlyDeclarations()}

	// First cycle with open allowlist syncs declarations
	runDeclarations(t, store, source, baseTime)
	id := model.RepoDeclaredID(model.DeclaredSchedule, declaringRepo, "nightly")
	sched, err := store.GetSchedule(ctx, id)
	if err != nil || sched == nil {
		t.Fatalf("schedule before restriction = %v, %v", sched, err)
	}

	// Now restrict TargetRepos to another repo
	store.SetTargetRepos(ctx, []string{"other/repo"})
	deps := orchestrator.Deps{
		Store:        store,
		Declarations: source,
		Config:       orchestrator.Config{},
	}
	if err := orchestrator.RunCycle(t.Context(), deps, baseTime.Add(time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	// Declarations for declaringRepo should now be forgotten
	sched, err = store.GetSchedule(ctx, id)
	if err != nil || sched != nil {
		t.Fatalf("schedule after allowlist restriction = %v, %v, want nil", sched, err)
	}
}

func TestOffDefaultHostRepoDeclarationFailureIsThrottledAndNotForgotten(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	offHost := model.RepoRef{Host: "depot.code.forge.test", Owner: "experimental", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: offHost, Declarations: true}); err != nil {
		t.Fatalf("putting repo config: %v", err)
	}

	target := offHost
	tmplID := model.RepoDeclaredID(model.DeclaredTemplate, offHost, "nightly")
	schedID := model.RepoDeclaredID(model.DeclaredSchedule, offHost, "nightly")
	declared := model.RepoDeclarations{
		Templates: []model.DeclaredTemplateFile{{
			Path:     model.TemplateDeclarationDir + "/nightly.yml",
			Template: model.Template{ID: tmplID, Name: "nightly", Title: "Nightly", Target: &target},
		}},
		Schedules: []model.DeclaredScheduleFile{{
			Path: model.ScheduleDeclarationDir + "/nightly.yml",
			Schedule: model.Schedule{
				ID: schedID, Title: "nightly", Target: offHost, TemplateID: &tmplID,
				Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 24},
				Enabled:    true,
			},
		}},
	}
	source := &fakeDeclarations{commit: "abc123", declared: declared}
	runDeclarations(t, store, source, baseTime)

	// Next poll after poll interval fails (e.g. the forge login expired).
	t1 := baseTime.Add(time.Hour)
	source.err = fmt.Errorf("forge login needed: run renew-host-creds")
	deps := orchestrator.Deps{Store: store, Declarations: source}
	if err := orchestrator.RunCycle(t.Context(), deps, t1); err == nil {
		t.Fatal("RunCycle: expected error from failed declaration poll")
	}
	if source.heads != 2 {
		t.Fatalf("source.heads = %d, want 2", source.heads)
	}

	// 10 seconds later (next reconcile tick), the off-default-host repo must
	// still match enabled[offHost], keep its last good declarations and sync
	// row, and be throttled by declarationPollInterval rather than polled on
	// every 10s tick.
	if err := orchestrator.RunCycle(t.Context(), deps, t1.Add(10*time.Second)); err != nil {
		t.Fatalf("RunCycle 10s after failure: unexpected error %v", err)
	}
	if source.heads != 2 {
		t.Fatalf("source.heads 10s after failure = %d, want 2 (throttled)", source.heads)
	}
	sched, err := store.GetSchedule(ctx, schedID)
	if err != nil || sched == nil {
		t.Fatalf("GetSchedule 10s after failure = %v, %v, want preserved schedule", sched, err)
	}
	sync, err := store.GetRepoDeclarationSync(ctx, offHost)
	if err != nil || sync == nil || sync.Repo != offHost || sync.LastError == "" {
		t.Fatalf("GetRepoDeclarationSync 10s after failure = %+v, %v, want preserved sync row for %+v", sync, err, offHost)
	}
}
