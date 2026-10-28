package statecheck

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
)

// grain/task-105: a schedule and a template carry values for this
// deployment's declared fields, and those tables are hand-edited in a
// pull request like every other settings table -- so the check that pull
// request already runs says which of those values nothing in the dump
// can hold.
//
// A warning, not a failure, and deliberately: it is the same question a
// firing answers, and it gives the same answer (the value is dropped and
// the task is filed). Failing would make retiring a declaration
// impossible without editing every schedule that ever named it in the
// same pull request.
func TestWarnsAboutCustomFieldValuesNothingDeclares(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, cleanup, err := ScratchStore()
	if err != nil {
		t.Fatalf("opening a store: %v", err)
	}
	defer cleanup()
	store := model.New(db)
	if err := store.PutCustomField(ctx, model.CustomField{
		Name: "priority", Type: model.CustomFieldEnum, Options: []string{"low", "high"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: "sched-1", Title: "Nightly dependency bump",
		Target:       model.RepoRef{Owner: "acme", Name: "widgets"},
		Recurrence:   model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 24},
		CustomFields: map[string][]string{"priority": {"high"}, "team": {"infra"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTemplate(ctx, model.Template{
		ID: "template-1", Name: "Bump", Title: "Bump dependencies",
		CustomFields: map[string][]string{"priority": {"urgent"}},
	}); err != nil {
		t.Fatal(err)
	}

	warnings, err := UnusableCustomFieldValues(ctx, db)
	if err != nil {
		t.Fatalf("checking: %v", err)
	}
	if len(warnings) != 2 {
		t.Fatalf("warnings = %v, want one per row carrying a value nothing can hold", warnings)
	}
	// The name nothing declares, not the value that is still fine beside
	// it.
	if !strings.Contains(warnings[0], "sched-1") || !strings.Contains(warnings[0], "team") ||
		strings.Contains(warnings[0], "priority") {
		t.Errorf("schedule warning = %q, want it naming only the undeclared field", warnings[0])
	}
	// And a value its own declaration would refuse, which is just as
	// invisible at firing time.
	if !strings.Contains(warnings[1], "template-1") || !strings.Contains(warnings[1], "priority") {
		t.Errorf("template warning = %q, want it naming the value the enum refuses", warnings[1])
	}
}

// grain/task-316: grain_config is a settings table like every other one
// here, so a pull request against the state repository can store a poll
// interval of 0 -- and "ok: grain can load this" was a true answer about
// a row that leaves the deployment reading it pacing its reconcile loop
// by a number nobody chose. Loading it was never the part that went
// wrong.
//
// A warning rather than a failure, for UnusableCustomFieldValues' own
// reason: the daemon substitutes its own -poll-interval now
// (daemon.Config.RunnableStoredConfig), so the deployment survives the row -- what a
// reviewer needs is to know that the number in the diff is not the
// number that will be in force.
func TestWarnsAboutSettingsGrainCannotRunOn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, cleanup, err := ScratchStore()
	if err != nil {
		t.Fatalf("opening a store: %v", err)
	}
	defer cleanup()
	store := model.New(db)

	// A row nobody would write by hand and every "nothing chosen" path
	// writes by accident (model.DefaultConfig leaves both at zero).
	if err := store.PutConfig(ctx, model.Config{PollInterval: 0, MaxWorkers: 0}); err != nil {
		t.Fatalf("putting a settings row: %v", err)
	}
	warnings, err := UnrunnableSettings(ctx, db)
	if err != nil {
		t.Fatalf("checking: %v", err)
	}
	if len(warnings) != 2 {
		t.Fatalf("warnings = %v, want one for the poll interval and one for the worker count", warnings)
	}
	if !strings.Contains(warnings[0], "poll interval") || !strings.Contains(warnings[0], "-poll-interval") {
		t.Errorf("poll interval warning = %q, want it naming the setting and what will be used instead", warnings[0])
	}
	if !strings.Contains(warnings[1], "max workers") {
		t.Errorf("max workers warning = %q, want it naming the setting", warnings[1])
	}

	// And a row a daemon can run on is not worth a word.
	if err := store.PutConfig(ctx, model.Config{PollInterval: 30 * time.Second, MaxWorkers: 1}); err != nil {
		t.Fatalf("putting a settings row: %v", err)
	}
	warnings, err = UnrunnableSettings(ctx, db)
	if err != nil {
		t.Fatalf("checking: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none for settings grain can run on", warnings)
	}

	if err := store.PutConfig(ctx, model.Config{
		PollInterval:    30 * time.Second,
		MaxWorkers:      1,
		K8sNodeSelector: []string{"pool=kvm", "bad-selector"},
	}); err != nil {
		t.Fatalf("putting a settings row with bad k8s_node_selector: %v", err)
	}
	warnings, err = UnrunnableSettings(ctx, db)
	if err != nil {
		t.Fatalf("checking: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "k8s_node_selector") || !strings.Contains(warnings[0], "bad-selector") {
		t.Errorf("k8s_node_selector warnings = %v, want one naming k8s_node_selector and bad-selector", warnings)
	}

	// Capability servers: an entry the daemon would refuse, and a
	// credential committed in cleartext, each warned about without the
	// value being repeated.
	if err := store.PutConfig(ctx, model.Config{
		PollInterval: 30 * time.Second,
		MaxWorkers:   1,
		MCPServers: map[string]protocol.ServerConfig{
			"https://example.com/caps/ok":    {ServerURL: "https://cap.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer $secret:tok"}},
			"https://example.com/caps/plain": {ServerURL: "https://cap.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer cleartext-value"}},
			"https://example.com/caps/bad":   {Command: "../escape"},
		},
	}); err != nil {
		t.Fatalf("putting a settings row with capability servers: %v", err)
	}
	warnings, err = UnrunnableSettings(ctx, db)
	if err != nil {
		t.Fatalf("checking: %v", err)
	}
	if len(warnings) != 2 || !strings.Contains(warnings[0], "caps/bad") || !strings.Contains(warnings[1], "caps/plain") ||
		strings.Contains(strings.Join(warnings, " "), "cleartext-value") {
		t.Errorf("capability server warnings = %v", warnings)
	}
}

// A dump from a deployment that has never been configured carries no
// settings row at all, which is not a fault: the daemon seeds one from
// its own flags the first time it starts.
func TestSaysNothingAboutADumpWithNoSettingsRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, cleanup, err := ScratchStore()
	if err != nil {
		t.Fatalf("opening a store: %v", err)
	}
	defer cleanup()
	warnings, err := UnrunnableSettings(ctx, db)
	if err != nil {
		t.Fatalf("checking: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none: an unconfigured deployment has chosen nothing yet", warnings)
	}
}

// grain/task-201: the same pull request, asked the other question. A
// change to tables/custom_field.json that retires an enum option, or
// takes `multiple` off a field, leaves the tasks carrying the old value
// exactly as they are -- so what the review of that diff wants to know
// is how many of them there are, which nothing said until this.
func TestCountsTheTasksADeclarationWouldStrand(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, cleanup, err := ScratchStore()
	if err != nil {
		t.Fatalf("opening a store: %v", err)
	}
	defer cleanup()
	store := model.New(db)
	// The declarations as the proposed change leaves them: "critical"
	// retired from the options, and `teams` no longer holding a set.
	for _, f := range []model.CustomField{
		{Name: "priority", Type: model.CustomFieldEnum, Options: []string{"low", "high"}},
		{Name: "teams", Type: model.CustomFieldEnum, Options: []string{"infra", "ui"}},
	} {
		if err := store.PutCustomField(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	file := func(id string, values map[string][]string) {
		t.Helper()
		if err := store.PutTask(ctx, model.Task{
			ID: id, Intent: model.IntentImplement, Title: "task " + id,
			Origin: model.Origin{
				Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "bwsalmon"}},
				Reason:      model.ReasonDirect,
			},
			CustomFields: values,
		}); err != nil {
			t.Fatalf("filing %s: %v", id, err)
		}
	}
	file("2", map[string][]string{"priority": {"critical"}})
	file("9", map[string][]string{"priority": {"critical"}})
	file("10", map[string][]string{"priority": {"high"}})
	file("11", map[string][]string{"teams": {"infra", "ui"}})
	file("12", map[string][]string{"gone": {"whatever"}})

	warnings, err := StrandedCustomFieldValues(ctx, db)
	if err != nil {
		t.Fatalf("checking: %v", err)
	}
	if len(warnings) != 3 {
		t.Fatalf("warnings = %v, want one each for priority, teams and the field nothing declares", warnings)
	}
	// The count first, because the count is what the decision turns on,
	// and then the tasks themselves so somebody can go and look.
	for _, want := range []string{`"priority"`, "2 tasks carry", `"critical" on 2 tasks (2, 9)`} {
		if !strings.Contains(warnings[0], want) {
			t.Errorf("priority warning = %q, want it to say %s", warnings[0], want)
		}
	}
	if strings.Contains(warnings[0], "10") {
		t.Errorf("priority warning = %q, and task 10 carries a value the declaration still offers", warnings[0])
	}
	// A set left by a field that has stopped holding one is reported as
	// that, not as two retired values: both are still offered.
	if !strings.Contains(warnings[1], `1 task carries more than one "teams" value`) ||
		!strings.Contains(warnings[1], "11") {
		t.Errorf("teams warning = %q, want the set named as a set", warnings[1])
	}
	if !strings.Contains(warnings[2], `nothing in this dump declares`) ||
		!strings.Contains(warnings[2], `"gone"`) {
		t.Errorf("undeclared warning = %q, want it naming the field nothing declares", warnings[2])
	}

	// And a dump nothing has stranded says nothing at all -- this is a
	// question about a declaration, so putting the option back answers
	// it differently about the same rows.
	if err := store.PutCustomField(ctx, model.CustomField{
		Name: "priority", Type: model.CustomFieldEnum, Options: []string{"low", "high", "critical"},
	}); err != nil {
		t.Fatal(err)
	}
	warnings, err = StrandedCustomFieldValues(ctx, db)
	if err != nil {
		t.Fatalf("checking: %v", err)
	}
	for _, w := range warnings {
		if strings.Contains(w, "priority") {
			t.Errorf("warning = %q, and the declaration offers every value stored for it", w)
		}
	}
}
