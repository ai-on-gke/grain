package postgres_test

// What syncing a repository's own declarations does to the store
// (model.Store.SyncRepoDeclarations, grain/task-60): the rows it writes,
// the ones it takes away when a file is deleted, and the two things it
// must not disturb -- a cadence already running and the declarations of
// a repository that could not be read this time.

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
)

var declaring = model.RepoRef{Owner: "bwsalmon", Name: "grain"}

func declaredTemplate(stem, title string) model.DeclaredTemplateFile {
	target := declaring
	return model.DeclaredTemplateFile{
		Path: model.TemplateDeclarationDir + "/" + stem + ".yml",
		Template: model.Template{
			ID:     model.RepoDeclaredID(model.DeclaredTemplate, declaring, stem),
			Name:   stem,
			Title:  title,
			Target: &target,
		},
	}
}

func declaredSchedule(stem, templateStem string, r model.Recurrence) model.DeclaredScheduleFile {
	id := model.RepoDeclaredID(model.DeclaredTemplate, declaring, templateStem)
	return model.DeclaredScheduleFile{
		Path: model.ScheduleDeclarationDir + "/" + stem + ".yml",
		Schedule: model.Schedule{
			ID:         model.RepoDeclaredID(model.DeclaredSchedule, declaring, stem),
			Title:      stem,
			Target:     declaring,
			TemplateID: &id,
			Recurrence: r,
			Enabled:    true,
		},
	}
}

func everySixHours() model.Recurrence {
	return model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 6}
}

func TestSyncWritesTheDeclaredRowsAndTheirProvenance(t *testing.T) {
	t.Parallel()
	store, _ := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)

	declared := model.RepoDeclarations{
		Templates: []model.DeclaredTemplateFile{declaredTemplate("nightly", "Bump dependencies")},
		Schedules: []model.DeclaredScheduleFile{declaredSchedule("nightly", "nightly", everySixHours())},
	}
	if _, err := store.SyncRepoDeclarations(ctx, declaring, "abc123", declared, now, time.UTC); err != nil {
		t.Fatalf("SyncRepoDeclarations: %v", err)
	}

	tmpl, err := store.GetTemplate(ctx, declared.Templates[0].Template.ID)
	if err != nil || tmpl == nil {
		t.Fatalf("GetTemplate: %v, %v", tmpl, err)
	}
	if tmpl.Title != "Bump dependencies" || tmpl.Target == nil || !tmpl.Target.Same(declaring) {
		t.Errorf("template = %+v", tmpl)
	}
	sched, err := store.GetSchedule(ctx, declared.Schedules[0].Schedule.ID)
	if err != nil || sched == nil {
		t.Fatalf("GetSchedule: %v, %v", sched, err)
	}
	if !sched.NextRunAt.After(now) {
		t.Errorf("nextRunAt = %v, want a first firing after the sync", sched.NextRunAt)
	}

	provenance, err := store.GetRepoDeclaration(ctx, tmpl.ID)
	if err != nil || provenance == nil {
		t.Fatalf("GetRepoDeclaration: %v, %v", provenance, err)
	}
	if provenance.Path != model.TemplateDeclarationDir+"/nightly.yml" || provenance.Commit != "abc123" {
		t.Errorf("provenance = %+v", provenance)
	}
	if !provenance.Repo.Same(declaring) || provenance.Kind != model.DeclaredTemplate {
		t.Errorf("provenance = %+v", provenance)
	}

	sync, err := store.GetRepoDeclarationSync(ctx, declaring)
	if err != nil || sync == nil {
		t.Fatalf("GetRepoDeclarationSync: %v, %v", sync, err)
	}
	if sync.Commit != "abc123" || sync.LastError != "" || sync.SyncedAt.IsZero() {
		t.Errorf("sync = %+v", sync)
	}
}

// The reason polling is safe: a repository read again with the same
// cadence keeps the firing times grain is already counting down, and a
// row is not created afresh every couple of minutes.
func TestResyncingLeavesACadenceAlone(t *testing.T) {
	t.Parallel()
	store, _ := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)

	declared := model.RepoDeclarations{
		Templates: []model.DeclaredTemplateFile{declaredTemplate("nightly", "Bump dependencies")},
		Schedules: []model.DeclaredScheduleFile{declaredSchedule("nightly", "nightly", everySixHours())},
	}
	if _, err := store.SyncRepoDeclarations(ctx, declaring, "abc123", declared, now, time.UTC); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	first, err := store.GetSchedule(ctx, declared.Schedules[0].Schedule.ID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	firstTemplate, err := store.GetTemplate(ctx, declared.Templates[0].Template.ID)
	if err != nil {
		t.Fatalf("GetTemplate: %v", err)
	}

	// The same files, read again an hour later at a new commit because
	// something else in the repository moved.
	declared.Templates[0].Template.Title = "Bump dependencies (patch only)"
	later := now.Add(time.Hour)
	if _, err := store.SyncRepoDeclarations(ctx, declaring, "def456", declared, later, time.UTC); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	second, err := store.GetSchedule(ctx, declared.Schedules[0].Schedule.ID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if !second.NextRunAt.Equal(first.NextRunAt) {
		t.Errorf("nextRunAt moved from %v to %v on a re-read: a polled repository would never come due",
			first.NextRunAt, second.NextRunAt)
	}
	secondTemplate, err := store.GetTemplate(ctx, declared.Templates[0].Template.ID)
	if err != nil {
		t.Fatalf("GetTemplate: %v", err)
	}
	if secondTemplate.Title != "Bump dependencies (patch only)" {
		t.Errorf("title = %q, want the edited one", secondTemplate.Title)
	}
	if !secondTemplate.CreatedAt.Equal(firstTemplate.CreatedAt) {
		t.Errorf("createdAt moved from %v to %v: an edited declaration is not a new one",
			firstTemplate.CreatedAt, secondTemplate.CreatedAt)
	}
}

func TestEditingTheCadenceRetimesTheSchedule(t *testing.T) {
	t.Parallel()
	store, _ := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)

	declared := model.RepoDeclarations{
		Templates: []model.DeclaredTemplateFile{declaredTemplate("nightly", "Bump dependencies")},
		Schedules: []model.DeclaredScheduleFile{declaredSchedule("nightly", "nightly", everySixHours())},
	}
	if _, err := store.SyncRepoDeclarations(ctx, declaring, "abc123", declared, now, time.UTC); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	declared.Schedules[0].Schedule.Recurrence = model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 1}
	if _, err := store.SyncRepoDeclarations(ctx, declaring, "def456", declared, now, time.UTC); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	sched, err := store.GetSchedule(ctx, declared.Schedules[0].Schedule.ID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if want := now.Add(time.Hour); !sched.NextRunAt.Equal(want) {
		t.Fatalf("nextRunAt = %v, want %v -- a cadence the file changed is retimed", sched.NextRunAt, want)
	}
}

func TestADeletedFileTakesItsRowWithIt(t *testing.T) {
	t.Parallel()
	store, _ := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)

	declared := model.RepoDeclarations{
		Templates: []model.DeclaredTemplateFile{
			declaredTemplate("nightly", "Bump dependencies"),
			declaredTemplate("docs", "Sweep the docs"),
		},
	}
	if _, err := store.SyncRepoDeclarations(ctx, declaring, "abc123", declared, now, time.UTC); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	withdrawn := declared.Templates[1].Template.ID
	declared.Templates = declared.Templates[:1]
	if _, err := store.SyncRepoDeclarations(ctx, declaring, "def456", declared, now, time.UTC); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if tmpl, err := store.GetTemplate(ctx, withdrawn); err != nil || tmpl != nil {
		t.Fatalf("GetTemplate of a withdrawn declaration = %v, %v, want nothing", tmpl, err)
	}
	if row, err := store.GetRepoDeclaration(ctx, withdrawn); err != nil || row != nil {
		t.Fatalf("provenance of a withdrawn declaration = %v, %v, want nothing", row, err)
	}
	if kept, err := store.GetTemplate(ctx, declared.Templates[0].Template.ID); err != nil || kept == nil {
		t.Fatalf("the template still declared = %v, %v", kept, err)
	}
}

// A file that stops parsing is not a repository asking for its standing
// work to be deleted.
func TestAFailedReadLeavesTheLastGoodDeclarationsRunning(t *testing.T) {
	t.Parallel()
	store, _ := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)

	declared := model.RepoDeclarations{
		Templates: []model.DeclaredTemplateFile{declaredTemplate("nightly", "Bump dependencies")},
	}
	if _, err := store.SyncRepoDeclarations(ctx, declaring, "abc123", declared, now, time.UTC); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if err := store.RecordDeclarationFailure(ctx, declaring, "nightly.yml: line 3: did not find expected key", now); err != nil {
		t.Fatalf("RecordDeclarationFailure: %v", err)
	}
	if tmpl, err := store.GetTemplate(ctx, declared.Templates[0].Template.ID); err != nil || tmpl == nil {
		t.Fatalf("GetTemplate after a failed read = %v, %v, want the last good one", tmpl, err)
	}
	sync, err := store.GetRepoDeclarationSync(ctx, declaring)
	if err != nil || sync == nil {
		t.Fatalf("GetRepoDeclarationSync: %v, %v", sync, err)
	}
	if sync.LastError == "" {
		t.Error("lastError is empty: a repository whose declarations stopped updating has to say so")
	}
	if sync.Commit != "abc123" {
		t.Errorf("commit = %q, want the last good one so the next poll tries again", sync.Commit)
	}
}

func TestForgettingARepoTakesEverythingItDeclared(t *testing.T) {
	t.Parallel()
	store, _ := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)

	declared := model.RepoDeclarations{
		Templates: []model.DeclaredTemplateFile{declaredTemplate("nightly", "Bump dependencies")},
		Schedules: []model.DeclaredScheduleFile{declaredSchedule("nightly", "nightly", everySixHours())},
	}
	if _, err := store.SyncRepoDeclarations(ctx, declaring, "abc123", declared, now, time.UTC); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if err := store.ForgetRepoDeclarations(ctx, declaring); err != nil {
		t.Fatalf("ForgetRepoDeclarations: %v", err)
	}
	if sched, err := store.GetSchedule(ctx, declared.Schedules[0].Schedule.ID); err != nil || sched != nil {
		t.Fatalf("schedule after forgetting = %v, %v, want nothing -- the switch has to actually stop it", sched, err)
	}
	if tmpl, err := store.GetTemplate(ctx, declared.Templates[0].Template.ID); err != nil || tmpl != nil {
		t.Fatalf("template after forgetting = %v, %v, want nothing", tmpl, err)
	}
	repos, err := store.DeclaringRepos(ctx)
	if err != nil {
		t.Fatalf("DeclaringRepos: %v", err)
	}
	if len(repos) != 0 {
		t.Fatalf("DeclaringRepos = %v, want none left to poll", repos)
	}
}

// A declared suite is an ordinary suite: a run of one snapshots its
// items the way every other run does.
func TestADeclaredSuiteIsAnOrdinarySuite(t *testing.T) {
	t.Parallel()
	store, _ := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)

	template := declaredTemplate("nightly", "Bump dependencies")
	suite := model.DeclaredSuiteFile{
		Path: model.SuiteDeclarationDir + "/sweep.yml",
		Suite: model.Suite{
			ID:        model.RepoDeclaredID(model.DeclaredSuite, declaring, "sweep"),
			Name:      "Nightly sweep",
			Items:     []model.SuiteItem{{TemplateID: template.Template.ID}},
			Mode:      model.SuiteCount,
			Count:     2,
			AutoMerge: true,
		},
	}
	if _, err := store.SyncRepoDeclarations(ctx, declaring, "abc123", model.RepoDeclarations{
		Templates: []model.DeclaredTemplateFile{template},
		Suites:    []model.DeclaredSuiteFile{suite},
	}, now, time.UTC); err != nil {
		t.Fatalf("sync: %v", err)
	}
	stored, err := store.GetSuite(ctx, suite.Suite.ID)
	if err != nil || stored == nil {
		t.Fatalf("GetSuite: %v, %v", stored, err)
	}
	if len(stored.Items) != 1 || stored.Items[0].TemplateID != template.Template.ID {
		t.Fatalf("suite items = %v", stored.Items)
	}
	run, err := store.CreateSuiteRun(ctx, *stored, declaring, "main", now)
	if err != nil {
		t.Fatalf("CreateSuiteRun: %v", err)
	}
	if len(run.Tasks) != 1 {
		t.Fatalf("a pass of the declared suite filed %d task(s), want 1", len(run.Tasks))
	}
}

func TestSyncWritesDeclaredSettingsAndUpdatesRepoConfig(t *testing.T) {
	t.Parallel()
	store, _ := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)

	// The row as an operator left it: the switch that lets grain read
	// this repository at all, the capabilities it hands tasks here, and
	// standing instructions somebody typed into the repos pane.
	operators := model.RepoConfig{
		Repo:                declaring,
		Declarations:        true,
		DefaultCapabilities: []string{"grain-ci"},
		CICapabilities:      []string{"package-publish"},
		ReleaseSecrets:      []string{"gcp-key"},
		IgnoreGitHubCI:      true,
		PromptExtension:     "Read docs/x.md first.",
		SetupCommand:        "make deps",
	}
	if err := store.PutRepoConfig(ctx, operators); err != nil {
		t.Fatalf("PutRepoConfig: %v", err)
	}

	declared := model.RepoDeclarations{
		Settings: &model.DeclaredSettingsFile{
			Path: ".grain/settings.yml",
			Settings: model.RepoSettings{
				PromptExtension: "Do not fail.",
				SetupCommand:    "npm ci",
			},
		},
	}
	if _, err := store.SyncRepoDeclarations(ctx, declaring, "abc123", declared, now, time.UTC); err != nil {
		t.Fatalf("SyncRepoDeclarations: %v", err)
	}

	cfg, err := store.GetRepoConfig(ctx, declaring)
	if err != nil || cfg == nil {
		t.Fatalf("GetRepoConfig: %v, %v", cfg, err)
	}
	// What the declaration wrote: its own two columns, and nothing that
	// belongs to the operator (grain/task-340).
	if cfg.DeclaredPromptExtension != "Do not fail." || cfg.DeclaredSetupCommand != "npm ci" {
		t.Errorf("declared layer = %q / %q", cfg.DeclaredPromptExtension, cfg.DeclaredSetupCommand)
	}
	if cfg.PromptExtension != operators.PromptExtension || cfg.SetupCommand != operators.SetupCommand {
		t.Errorf("a declaration overwrote the operator's own text: %+v", cfg)
	}
	if !cfg.IgnoreGitHubCI || len(cfg.DefaultCapabilities) != 1 || len(cfg.ReleaseSecrets) != 1 {
		t.Errorf("a declaration changed what the repository is allowed to do: %+v", cfg)
	}
	// Both layers, the operator's first, is what a run is actually told.
	if got, want := cfg.RepoPromptExtension(), "Read docs/x.md first.\n\nDo not fail."; got != want {
		t.Errorf("RepoPromptExtension() = %q, want %q", got, want)
	}
	if got, want := cfg.SetupScript(), "make deps\nnpm ci"; got != want {
		t.Errorf("SetupScript() = %q, want %q", got, want)
	}

	provenance, err := store.GetRepoDeclaration(ctx, model.RepoDeclaredID(model.DeclaredSettings, declaring, "settings"))
	if err != nil || provenance == nil {
		t.Fatalf("GetRepoDeclaration: %v, %v", provenance, err)
	}
	if provenance.Path != ".grain/settings.yml" || provenance.Commit != "abc123" {
		t.Errorf("provenance = %+v", provenance)
	}

	// The file is deleted on the next commit. That takes back the layer
	// it added and leaves everything an operator wrote exactly as it
	// was -- deleting `.grain/settings.yml` is not a way to clear a
	// repository's capabilities or its release secrets.
	if _, err := store.SyncRepoDeclarations(ctx, declaring, "def456", model.RepoDeclarations{}, now, time.UTC); err != nil {
		t.Fatalf("SyncRepoDeclarations empty: %v", err)
	}
	after, err := store.GetRepoConfig(ctx, declaring)
	if err != nil || after == nil {
		t.Fatalf("GetRepoConfig after withdrawal: %v, %v", after, err)
	}
	if after.DeclaredPromptExtension != "" || after.DeclaredSetupCommand != "" {
		t.Errorf("a withdrawn settings file left its layer behind: %+v", after)
	}
	operators.Repo = after.Repo
	if !reflect.DeepEqual(*after, operators) {
		t.Errorf("the row after withdrawal is not the one an operator wrote:\n got %+v\nwant %+v",
			*after, operators)
	}
}

// The first sync of a repository an operator has already configured is
// where the old whole-row overwrite bit: the file was written without
// the row in front of whoever wrote it, and everything it left out went
// at once -- which is what happened to this repository's own row at
// 18:52 on 20 Sep 2026 (grain/task-460).
//
// grain/task-340 answered that at the format rather than at the sync. A
// settings file cannot name a capability, a release secret, the
// publishing switch or any other field an operator wrote, so there is
// nothing left for a sync to compare against the row: what used to need
// a guard (RepoSettings.SilentLoss, removed with the reach that made it
// necessary) is now simply inexpressible. This is that property, held
// where the incident happened -- a first sync against a fully configured
// row.
func TestSyncCannotTakeBackWhatTheRowHolds(t *testing.T) {
	t.Parallel()
	store, _ := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)

	row := model.RepoConfig{
		Repo:                         declaring,
		Declarations:                 true,
		CloneURL:                     "http://git.example/bwsalmon/grain",
		DefaultCapabilities:          []string{"grain-ci"},
		CICapabilities:               []string{"gemini-key", "package-publish"},
		ReleaseSecrets:               []string{"gcp-key"},
		Work:                         model.ItemOwnership(model.ReleaseItem, model.OwnershipOff),
		BaseBranchCIJobs:             []string{"go", "lint"},
		PostMergeDispatchJobs:        []string{".grain/workflows/image.yml:guest-cache"},
		WatchDefaultBranch:           true,
		MergeOnGreen:                 true,
		BaseRepairBypassesHumanGates: true,
		RequireBranchUpToDate:        true,
		IgnoreGitHubCI:               true,
		UnfixableChecks:              []string{"cla/google"},
		PromptExtension:              "Read docs/x.md first.",
		SetupCommand:                 "make deps",
	}
	if err := store.PutRepoConfig(ctx, row); err != nil {
		t.Fatalf("PutRepoConfig: %v", err)
	}

	declared := model.RepoDeclarations{
		Settings: &model.DeclaredSettingsFile{
			Path:     ".grain/settings.yml",
			Settings: model.RepoSettings{PromptExtension: "and mind the generated client"},
		},
		Templates: []model.DeclaredTemplateFile{declaredTemplate("nightly", "Nightly sweep")},
	}
	if _, err := store.SyncRepoDeclarations(ctx, declaring, "abc123", declared, now, time.UTC); err != nil {
		t.Fatalf("SyncRepoDeclarations: %v", err)
	}

	cfg, err := store.GetRepoConfig(ctx, declaring)
	if err != nil || cfg == nil {
		t.Fatalf("GetRepoConfig: %v, %v", cfg, err)
	}
	// Every field an operator wrote, still exactly as they wrote it --
	// including the two the declaration has a layer of its own for.
	want := row
	want.DeclaredPromptExtension = "and mind the generated client"
	if !reflect.DeepEqual(*cfg, want) {
		t.Errorf("a settings declaration reached the operator's row:\n got %+v\nwant %+v", *cfg, want)
	}
	// And what a run is told is both layers, the operator's first.
	if got, want := cfg.RepoPromptExtension(),
		"Read docs/x.md first.\n\nand mind the generated client"; got != want {
		t.Errorf("RepoPromptExtension() = %q, want %q", got, want)
	}
}

// The two columns a settings declaration does write are handed back by
// the sync so the orchestrator can log them (grain/task-476). A
// repository's standing prompt and its setup command change by merging
// a file and then show up as an agent behaving differently somewhere
// else, so the sync that did it is worth a line where it happened.
func TestSyncReportsTheSettingsFieldsItWrote(t *testing.T) {
	t.Parallel()
	store, _ := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)

	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:                    declaring,
		Declarations:            true,
		SetupCommand:            "make operator-deps",
		DeclaredPromptExtension: "Mind the generated client.",
		DeclaredSetupCommand:    "make deps",
	}); err != nil {
		t.Fatalf("PutRepoConfig: %v", err)
	}

	declared := model.RepoDeclarations{
		Settings: &model.DeclaredSettingsFile{
			Path:     ".grain/settings.yml",
			Settings: model.RepoSettings{PromptExtension: "Mind the generated client, and the fixtures."},
		},
	}
	changed, err := store.SyncRepoDeclarations(ctx, declaring, "abc123", declared, now, time.UTC)
	if err != nil {
		t.Fatalf("SyncRepoDeclarations: %v", err)
	}
	described := model.DescribeSettingsChanges(changed)
	for _, want := range []string{
		`prompt-extension: "Mind the generated client." -> "Mind the generated client, and the fixtures."`,
		`setup-command: "make deps" -> (blank)`,
	} {
		if !strings.Contains(described, want) {
			t.Errorf("the sync did not report %q:\n%s", want, described)
		}
	}
	// The operator's own setup command is not a declaration's to write
	// and is not reported as one.
	if strings.Contains(described, "make operator-deps") {
		t.Errorf("the sync reported a column the declaration cannot write:\n%s", described)
	}

	// The second sync of the same file changes nothing, so it reports
	// nothing: this is meant to be one line when a row moves, not a line
	// every couple of minutes for as long as the repository declares.
	again, err := store.SyncRepoDeclarations(ctx, declaring, "def456", declared, now.Add(time.Hour), time.UTC)
	if err != nil {
		t.Fatalf("SyncRepoDeclarations again: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("re-syncing an unchanged file reported %d change(s):\n%s", len(again), model.DescribeSettingsChanges(again))
	}

	// And withdrawing the file takes the declared layer back to blank,
	// which is reported too.
	withdrawn, err := store.SyncRepoDeclarations(ctx, declaring, "789abc", model.RepoDeclarations{}, now.Add(2*time.Hour), time.UTC)
	if err != nil {
		t.Fatalf("SyncRepoDeclarations withdrawing settings: %v", err)
	}
	if got := model.DescribeSettingsChanges(withdrawn); !strings.Contains(got,
		`prompt-extension: "Mind the generated client, and the fixtures." -> (blank)`) {
		t.Errorf("withdrawing the settings file did not report the declared layer going:\n%s", got)
	}
}

func TestForgetRepoRemovesAllRepoRecords(t *testing.T) {
	t.Parallel()
	store, _ := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)

	// 1. Repo config
	cfg := model.RepoConfig{
		Repo:                declaring,
		Declarations:        true,
		DefaultCapabilities: []string{"grain-ci"},
		CICapabilities:      []string{"package-publish"},
		Work:                model.ItemOwnership(model.ReleaseItem, model.OwnershipOff),
	}
	if err := store.PutRepoConfig(ctx, cfg); err != nil {
		t.Fatalf("PutRepoConfig: %v", err)
	}

	// 2. Repo workflows
	if err := store.PutRepoWorkflows(ctx, declaring, []model.RepoWorkflow{
		{Repo: declaring, Path: ".grain/workflows/ci.yml", Text: "name: CI"},
	}); err != nil {
		t.Fatalf("PutRepoWorkflows: %v", err)
	}

	// 3. Base branch CI / release / dispatch
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: declaring, Branch: "main", Commit: "abc123",
	}); err != nil {
		t.Fatalf("PutBaseBranchCI: %v", err)
	}
	if err := store.PutBaseBranchRelease(ctx, model.BaseBranchRelease{
		Repo: declaring, Branch: "main", Commit: "abc123",
	}); err != nil {
		t.Fatalf("PutBaseBranchRelease: %v", err)
	}
	if err := store.PutBaseBranchDispatch(ctx, model.BaseBranchDispatch{
		Repo: declaring, Branch: "main", Jobs: []string{"deploy"}, Commit: "abc123",
	}); err != nil {
		t.Fatalf("PutBaseBranchDispatch: %v", err)
	}

	// 4. Declarations
	declared := model.RepoDeclarations{
		Templates: []model.DeclaredTemplateFile{declaredTemplate("nightly", "Bump dependencies")},
		Schedules: []model.DeclaredScheduleFile{declaredSchedule("nightly", "nightly", everySixHours())},
	}
	if _, err := store.SyncRepoDeclarations(ctx, declaring, "abc123", declared, now, time.UTC); err != nil {
		t.Fatalf("SyncRepoDeclarations: %v", err)
	}

	// 5. ForgetRepo
	if err := store.ForgetRepo(ctx, declaring); err != nil {
		t.Fatalf("ForgetRepo: %v", err)
	}

	// Verify all cleaned up
	gotCfg, err := store.GetRepoConfig(ctx, declaring)
	if err != nil || gotCfg != nil {
		t.Errorf("GetRepoConfig after ForgetRepo = %v, %v, want nil", gotCfg, err)
	}
	wfs, err := store.GetRepoWorkflows(ctx, declaring)
	if err != nil || len(wfs) != 0 {
		t.Errorf("GetRepoWorkflows after ForgetRepo = %v, %v, want empty", wfs, err)
	}
	cis, err := store.ListBaseBranchCI(ctx)
	if err != nil || len(cis) != 0 {
		t.Errorf("ListBaseBranchCI after ForgetRepo = %v, %v, want empty", cis, err)
	}
	rels, err := store.ListBaseBranchRelease(ctx)
	if err != nil || len(rels) != 0 {
		t.Errorf("ListBaseBranchRelease after ForgetRepo = %v, %v, want empty", rels, err)
	}
	disps, err := store.ListBaseBranchDispatch(ctx)
	if err != nil || len(disps) != 0 {
		t.Errorf("ListBaseBranchDispatch after ForgetRepo = %v, %v, want empty", disps, err)
	}
	sched, err := store.GetSchedule(ctx, declared.Schedules[0].Schedule.ID)
	if err != nil || sched != nil {
		t.Errorf("GetSchedule after ForgetRepo = %v, %v, want nil", sched, err)
	}
	tmpl, err := store.GetTemplate(ctx, declared.Templates[0].Template.ID)
	if err != nil || tmpl != nil {
		t.Errorf("GetTemplate after ForgetRepo = %v, %v, want nil", tmpl, err)
	}
	declaringRepos, err := store.DeclaringRepos(ctx)
	if err != nil || len(declaringRepos) != 0 {
		t.Errorf("DeclaringRepos after ForgetRepo = %v, %v, want empty", declaringRepos, err)
	}
}

func TestSyncRepoDeclarationsWithPushRules(t *testing.T) {
	t.Parallel()
	store, _ := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)

	tmpl := declaredTemplate("with-push", "Template with push rules")
	tmpl.Push = model.RefPushSet{
		{Pattern: "refs/heads/main", Force: true, Delete: true},
		{Pattern: "refs/tags/v*", Force: false, Delete: false},
	}
	declared := model.RepoDeclarations{
		Templates: []model.DeclaredTemplateFile{tmpl},
	}
	if _, err := store.SyncRepoDeclarations(ctx, declaring, "abc123", declared, now, time.UTC); err != nil {
		t.Fatalf("SyncRepoDeclarations: %v", err)
	}

	provenance, err := store.GetRepoDeclaration(ctx, tmpl.Template.ID)
	if err != nil || provenance == nil {
		t.Fatalf("GetRepoDeclaration: %v, %v", provenance, err)
	}
	if len(provenance.Push) != 2 {
		t.Fatalf("provenance.Push = %+v, want 2 rules", provenance.Push)
	}
	if provenance.Push[0].Pattern != "refs/heads/main" || !provenance.Push[0].Force || !provenance.Push[0].Delete {
		t.Errorf("provenance.Push[0] = %+v, want main with force and delete", provenance.Push[0])
	}
	if provenance.Push[1].Pattern != "refs/tags/v*" || provenance.Push[1].Force || provenance.Push[1].Delete {
		t.Errorf("provenance.Push[1] = %+v, want tags without force or delete", provenance.Push[1])
	}
}

func TestOffDefaultHostRepoDeclarationsKeepTheirHost(t *testing.T) {
	t.Parallel()
	store, _ := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
	offHost := model.RepoRef{Host: "depot.code.forge.test", Owner: "experimental", Name: "widgets"}

	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: offHost, Declarations: true}); err != nil {
		t.Fatalf("PutRepoConfig: %v", err)
	}

	// Recording a failure before any sync preserves Host on the sync row.
	if err := store.RecordDeclarationFailure(ctx, offHost, "forge login needed: run renew-host-creds", now); err != nil {
		t.Fatalf("RecordDeclarationFailure: %v", err)
	}
	sync, err := store.GetRepoDeclarationSync(ctx, offHost)
	if err != nil || sync == nil {
		t.Fatalf("GetRepoDeclarationSync = %v, %v, want sync row", sync, err)
	}
	if sync.Repo != offHost || sync.LastError != "forge login needed: run renew-host-creds" {
		t.Fatalf("sync = %+v, want repo %+v with recorded error", sync, offHost)
	}
	repos, err := store.DeclaringRepos(ctx)
	if err != nil || len(repos) != 1 || repos[0] != offHost {
		t.Fatalf("DeclaringRepos after failure = %+v, %v, want [%+v]", repos, err, offHost)
	}

	// Syncing declarations, triggers, and merge rules preserves Host across all tables.
	target := offHost
	tmplID := model.RepoDeclaredID(model.DeclaredTemplate, offHost, "nightly")
	schedID := model.RepoDeclaredID(model.DeclaredSchedule, offHost, "nightly")
	declared := model.RepoDeclarations{
		Settings: &model.DeclaredSettingsFile{
			Path:     ".grain/settings.yml",
			Settings: model.RepoSettings{PromptExtension: "Off-host prompt."},
		},
		Templates: []model.DeclaredTemplateFile{{
			Path:     model.TemplateDeclarationDir + "/nightly.yml",
			Template: model.Template{ID: tmplID, Name: "nightly", Title: "Nightly", Target: &target},
		}},
		Schedules: []model.DeclaredScheduleFile{{
			Path: model.ScheduleDeclarationDir + "/nightly.yml",
			Schedule: model.Schedule{
				ID: schedID, Title: "nightly", Target: offHost, TemplateID: &tmplID,
				Recurrence: everySixHours(), Enabled: true,
			},
		}},
		Triggers: []model.PathTrigger{{
			Name: "docs", Path: ".grain/triggers/docs.yml", On: model.TriggerOnMerge,
			Paths: []string{"docs/**"}, Template: tmplID, Per: model.TriggerPerChange,
		}},
		MergeRules: &model.MergeRules{
			Path: model.MergeRulesPath,
			Rules: []model.MergeRule{{
				ID: "docs", Paths: []string{"docs/**"},
				MergePolicy: model.MergePolicy{AutoMerge: model.AutoMergeNever},
			}},
		},
	}
	if _, err := store.SyncRepoDeclarations(ctx, offHost, "abc123", declared, now.Add(time.Minute), time.UTC); err != nil {
		t.Fatalf("SyncRepoDeclarations: %v", err)
	}
	if err := store.RecordDeclarationCheck(ctx, offHost, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("RecordDeclarationCheck: %v", err)
	}

	declRow, err := store.GetRepoDeclaration(ctx, tmplID)
	if err != nil || declRow == nil || declRow.Repo != offHost {
		t.Fatalf("GetRepoDeclaration = %+v, %v, want Repo %+v", declRow, err, offHost)
	}
	allDecls, err := store.ListRepoDeclarations(ctx)
	if err != nil || len(allDecls) != 3 {
		t.Fatalf("ListRepoDeclarations = %+v, %v, want 3 rows", allDecls, err)
	}
	for _, d := range allDecls {
		if d.Repo != offHost {
			t.Errorf("ListRepoDeclarations row %s Repo = %+v, want %+v", d.ID, d.Repo, offHost)
		}
	}
	syncs, err := store.ListRepoDeclarationSyncs(ctx)
	if err != nil || len(syncs) != 1 || syncs[0].Repo != offHost || syncs[0].LastError != "" {
		t.Fatalf("ListRepoDeclarationSyncs = %+v, %v, want [%+v] with cleared error", syncs, err, offHost)
	}
	triggers, err := store.PathTriggersOf(ctx, offHost)
	if err != nil || len(triggers) != 1 || triggers[0].Repo != offHost {
		t.Fatalf("PathTriggersOf = %+v, %v, want 1 trigger for %+v", triggers, err, offHost)
	}
	rules, err := store.MergeRulesOf(ctx, offHost)
	if err != nil || rules == nil || rules.Commit != "abc123" {
		t.Fatalf("MergeRulesOf = %+v, %v, want rules at abc123", rules, err)
	}

	// ForgetRepo cleans up off-default-host records.
	if err := store.ForgetRepo(ctx, offHost); err != nil {
		t.Fatalf("ForgetRepo: %v", err)
	}
	if gotCfg, err := store.GetRepoConfig(ctx, offHost); err != nil || gotCfg != nil {
		t.Errorf("GetRepoConfig after ForgetRepo = %+v, %v, want nil", gotCfg, err)
	}
	if repos, err := store.DeclaringRepos(ctx); err != nil || len(repos) != 0 {
		t.Errorf("DeclaringRepos after ForgetRepo = %+v, %v, want empty", repos, err)
	}
}
