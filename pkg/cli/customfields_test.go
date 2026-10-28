package cli

// grain/task-167: `grain create -field name=value` and `grain update
// <id> -field name=value` are the CLI's half of a deployment's own
// declared fields, which `grain list` could already narrow and sort by
// and nothing here could set. These are that flag against a real server,
// declarations and all: what it writes, what it refuses before the
// request, and -- the part an edit gets wrong if nobody pins it -- what
// it leaves alone.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"net/http/httptest"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/sqlite"
	"github.com/bwsalmon/grain/pkg/ui"
)

// declaredFields is one of every shape that matters to a value typed at
// a shell: an enum whose options are its vocabulary, free text a comma
// belongs inside, an enum holding a set, a date and a number.
func declaredFields() []model.CustomField {
	return []model.CustomField{
		{Name: "priority", Label: "Priority", Type: model.CustomFieldEnum, Options: []string{"low", "high", "critical"}},
		{Name: "customer", Type: model.CustomFieldText},
		{Name: "areas", Type: model.CustomFieldEnum, Options: []string{"ui", "daemon", "cli"}, Multiple: true},
		{Name: "due", Label: "Due date", Type: model.CustomFieldDate},
		{Name: "points", Type: model.CustomFieldNumber},
	}
}

// fieldTestServer is a deployment that declares those fields, with one
// task already filed, and the store beside it so a test can retire a
// declaration the way a merged pull request against tables/
// custom_field.json would.
func fieldTestServer(t *testing.T, fields []model.CustomField) (string, *model.Store) {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(t.TempDir(), "data")))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	for _, f := range fields {
		if err := store.PutCustomField(ctx, f); err != nil {
			t.Fatalf("declaring %s: %v", f.Name, err)
		}
	}
	client := ui.NewClient(ui.Config{Actor: ui.DefaultActor("tester"), Capabilities: ui.OfferedCapabilities()}, store)
	if _, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "Widget work", Repo: "acme/widgets"}); err != nil {
		t.Fatalf("creating the task: %v", err)
	}
	srv := httptest.NewServer(accessCodeServer(client))
	t.Cleanup(srv.Close)
	return srv.URL, store
}

// carries is one task's values as the server holds them, read back over
// the same API the command wrote them through.
func carries(t *testing.T, server, id string) map[string][]string {
	t.Helper()
	task, err := accessCodeClient(server).Task(context.Background(), id)
	if err != nil {
		t.Fatalf("reading task %s: %v", id, err)
	}
	out := map[string][]string{}
	for name, v := range task.CustomFields {
		out[name] = []string(v)
	}
	return out
}

func wantValues(t *testing.T, got map[string][]string, want map[string][]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("task carries %v, want %v", got, want)
	}
	for name, values := range want {
		if strings.Join(got[name], "|") != strings.Join(values, "|") {
			t.Errorf("task carries %s=%v, want %v", name, got[name], values)
		}
	}
}

func TestCreateSetsDeclaredFields(t *testing.T) {
	server, _ := fieldTestServer(t, declaredFields())
	out := captureStdout(t, func() {
		if err := runCLIWithAccessCode([]string{"-server", server, "create", "-title", "Filed from a shell", "-no-repo",
			"-field", "priority=high",
			// A set, both ways round: a comma-separated list and the
			// flag written twice.
			"-field", "areas=ui,daemon", "-field", "areas=cli",
			// A one-value text field keeps its comma, since only a
			// field declared to hold a set is cut on one.
			"-field", "customer=Acme, Inc.",
			"-field", "due=2026-09-10", "-field", "points=3"}); err != nil {
			t.Fatalf("grain create -field: %v", err)
		}
	})
	wantValues(t, carries(t, server, "2"), map[string][]string{
		"priority": {"high"},
		"areas":    {"cli", "daemon", "ui"}, // a set, so sorted (model.NormalizeValues)
		"customer": {"Acme, Inc."},
		"due":      {"2026-09-10"},
		"points":   {"3"},
	})
	// And the task it prints back says what it now carries, so the
	// values are confirmed where they were set rather than only in -json.
	if !strings.Contains(out, "priority=high") || !strings.Contains(out, "areas=cli,daemon,ui") {
		t.Errorf("grain create printed %q, want the fields it set", out)
	}
}

// The values a task carries are canonicalised by the declaration, not
// stored as typed: "TRUE" is "true", and an enum option is the option.
func TestFieldValuesAreNormalizedBeforeTheyAreSent(t *testing.T) {
	t.Parallel()
	server, _ := fieldTestServer(t, []model.CustomField{{Name: "escalated", Type: model.CustomFieldBool}})
	if err := runCLIWithAccessCode([]string{"-server", server, "create", "-title", "Escalated", "-no-repo", "-field", "escalated=YES"}); err != nil {
		t.Fatalf("grain create -field escalated=YES: %v", err)
	}
	wantValues(t, carries(t, server, "2"), map[string][]string{"escalated": {"true"}})
}

// An edit names the one field it means. PATCH replaces the whole set
// (ui.UpdateTaskRequest.CustomFields), so the command has to re-state
// the rest -- and this is the test that fails if it ever stops.
func TestUpdateLeavesTheFieldsItDoesNotName(t *testing.T) {
	t.Parallel()
	server, _ := fieldTestServer(t, declaredFields())
	if err := runCLIWithAccessCode([]string{"-server", server, "update", "1",
		"-field", "priority=high", "-field", "customer=Acme", "-field", "areas=ui,cli"}); err != nil {
		t.Fatalf("grain update -field: %v", err)
	}
	if err := runCLIWithAccessCode([]string{"-server", server, "update", "1", "-field", "priority=critical"}); err != nil {
		t.Fatalf("grain update -field priority=critical: %v", err)
	}
	wantValues(t, carries(t, server, "1"), map[string][]string{
		"priority": {"critical"},
		"customer": {"Acme"},
		"areas":    {"cli", "ui"},
	})
	// An edit that names no field at all says nothing about them, which
	// is not the same as saying they are empty.
	if err := runCLIWithAccessCode([]string{"-server", server, "update", "1", "-title", "Renamed"}); err != nil {
		t.Fatalf("grain update -title: %v", err)
	}
	wantValues(t, carries(t, server, "1"), map[string][]string{
		"priority": {"critical"},
		"customer": {"Acme"},
		"areas":    {"cli", "ui"},
	})
}

// "name=" is how a value is cleared -- the empty value the API already
// reads as one, rather than a -clear-field of its own.
func TestUpdateClearsAFieldWithAnEmptyValue(t *testing.T) {
	t.Parallel()
	server, _ := fieldTestServer(t, declaredFields())
	if err := runCLIWithAccessCode([]string{"-server", server, "update", "1",
		"-field", "priority=high", "-field", "customer=Acme"}); err != nil {
		t.Fatalf("grain update -field: %v", err)
	}
	if err := runCLIWithAccessCode([]string{"-server", server, "update", "1", "-field", "priority="}); err != nil {
		t.Fatalf("grain update -field priority=: %v", err)
	}
	wantValues(t, carries(t, server, "1"), map[string][]string{"customer": {"Acme"}})
}

// A declaration deleted from the state repository leaves its values on
// the tasks that carried them: nothing walks the store to strip them,
// and nothing renders them either. Clearing one has to stay sayable, so
// an *empty* value is the one thing accepted for a field nobody declares
// (model.NormalizeCustomFieldValues' own exception).
func TestUpdateClearsAValueWhoseFieldIsGone(t *testing.T) {
	t.Parallel()
	server, store := fieldTestServer(t, declaredFields())
	if err := runCLIWithAccessCode([]string{"-server", server, "update", "1",
		"-field", "priority=high", "-field", "customer=Acme"}); err != nil {
		t.Fatalf("grain update -field: %v", err)
	}
	if err := store.DeleteCustomField(context.Background(), "customer"); err != nil {
		t.Fatalf("retiring the customer declaration: %v", err)
	}
	if err := runCLIWithAccessCode([]string{"-server", server, "update", "1", "-field", "customer="}); err != nil {
		t.Fatalf("grain update -field customer= after the declaration went: %v", err)
	}
	wantValues(t, carries(t, server, "1"), map[string][]string{"priority": {"high"}})
}

// The other half of a declaration that changed under a task: a value an
// edit has to re-state and the declaration would no longer produce. It
// is re-stated as it stands and kept, rather than failing an edit that
// named another field entirely or being thrown away to get that edit
// through (model.NormalizeCustomFieldValuesFrom).
func TestUpdateRestatesAValueTheDeclarationNoLongerAccepts(t *testing.T) {
	t.Parallel()
	server, store := fieldTestServer(t, declaredFields())
	if err := runCLIWithAccessCode([]string{"-server", server, "update", "1", "-field", "priority=critical"}); err != nil {
		t.Fatalf("grain update -field priority=critical: %v", err)
	}
	// "critical" is dropped from the options, the way a merged pull
	// request against tables/custom_field.json would drop it.
	narrowed := model.CustomField{Name: "priority", Label: "Priority", Type: model.CustomFieldEnum,
		Options: []string{"low", "high"}}
	if err := store.PutCustomField(context.Background(), narrowed); err != nil {
		t.Fatalf("narrowing the priority declaration: %v", err)
	}
	if err := runCLIWithAccessCode([]string{"-server", server, "update", "1", "-field", "customer=Acme"}); err != nil {
		t.Fatalf("grain update -field customer=Acme over a legacy priority: %v", err)
	}
	wantValues(t, carries(t, server, "1"),
		map[string][]string{"priority": {"critical"}, "customer": {"Acme"}})
	// Grandfathered, not re-opened: the option nobody else holds still
	// cannot be typed onto this task or any other.
	err := runCLIWithAccessCode([]string{"-server", server, "update", "1", "-field", "priority=critical"})
	if err == nil {
		t.Fatal("grain update -field priority=critical succeeded, want the retired option still refused")
	}
	if !strings.Contains(err.Error(), "is not one of") {
		t.Errorf("error %q, want it to say the option is not offered", err)
	}
	// And it can still be taken off, which is the way out of carrying
	// one: nothing here is stuck with it.
	if err := runCLIWithAccessCode([]string{"-server", server, "update", "1", "-field", "priority="}); err != nil {
		t.Fatalf("grain update -field priority= clearing a legacy value: %v", err)
	}
	wantValues(t, carries(t, server, "1"), map[string][]string{"customer": {"Acme"}})
}

// Everything refused before the request is made, with the names and the
// values that would have worked -- a terminal has nowhere else to look
// them up.
func TestFieldFlagRefusesWhatTheDeploymentDoesNotDeclare(t *testing.T) {
	t.Parallel()
	server, _ := fieldTestServer(t, declaredFields())
	for _, tc := range []struct {
		name    string
		args    []string
		mention []string
	}{
		{
			name:    "a field nothing declares",
			args:    []string{"-field", "colour=blue"},
			mention: []string{`no field "colour"`, "priority", "customer"},
		},
		{
			name:    "a value an enum does not offer",
			args:    []string{"-field", "priority=urgent"},
			mention: []string{`"urgent" is not one of`, "low, high, critical"},
		},
		{
			name:    "a date that is not one",
			args:    []string{"-field", "due=next tuesday"},
			mention: []string{"2006-01-02"},
		},
		{
			name:    "a number that is not one",
			args:    []string{"-field", "points=several"},
			mention: []string{"is not a number"},
		},
		{
			name:    "two values for a field that holds one",
			args:    []string{"-field", "priority=high", "-field", "priority=low"},
			mention: []string{"takes a single value"},
		},
		{
			name:    "a flag with no value at all",
			args:    []string{"-field", "priority"},
			mention: []string{"name=value"},
		},
		{
			name:    "a flag with no field",
			args:    []string{"-field", "=high"},
			mention: []string{"name=value"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, command := range [][]string{
				append([]string{"create", "-title", "Filed from a shell", "-no-repo"}, tc.args...),
				append([]string{"update", "1"}, tc.args...),
			} {
				err := runCLIWithAccessCode(append([]string{"-server", server}, command...))
				if err == nil {
					t.Fatalf("grain %s returned no error, want one naming what works", strings.Join(command, " "))
				}
				for _, want := range tc.mention {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("grain %s failed with %q, want it to mention %q", strings.Join(command, " "), err, want)
					}
				}
			}
			// Nothing was written by any of it: the task the update
			// named still says nothing about a field, and no second task
			// was filed.
			wantValues(t, carries(t, server, "1"), map[string][]string{})
		})
	}
}

// A deployment that declares none at all is not somebody's typo, and
// saying "one of: " with nothing after it would be no help.
func TestFieldFlagOnADeploymentWithNoDeclarations(t *testing.T) {
	t.Parallel()
	server, _ := fieldTestServer(t, nil)
	err := runCLIWithAccessCode([]string{"-server", server, "create", "-title", "Filed from a shell", "-no-repo", "-field", "priority=high"})
	if err == nil {
		t.Fatal("grain create -field priority=high succeeded, want it refused")
	}
	if !strings.Contains(err.Error(), "tables/custom_field.json") {
		t.Errorf("error %q, want it to say where fields are declared", err)
	}
}

// declaredFieldHelp is what `grain create -h` prints under the flags,
// since the flag's own usage line cannot name a field this build has
// never heard of.
func TestDeclaredFieldHelpListsWhatTheDeploymentDeclares(t *testing.T) {
	t.Parallel()
	server, _ := fieldTestServer(t, declaredFields())
	help := declaredFieldHelp(context.Background(), accessCodeClient(server))
	for _, want := range []string{"priority", "low | high | critical", "Due date", "any of: ui | daemon | cli"} {
		if !strings.Contains(help, want) {
			t.Errorf("declaredFieldHelp() = %q, want it to mention %q", help, want)
		}
	}
	// A deployment that declares none says nothing rather than printing
	// an empty heading, and one that cannot be reached says nothing
	// either -- help that fails is worse than help that is short.
	empty, _ := fieldTestServer(t, nil)
	if got := declaredFieldHelp(context.Background(), accessCodeClient(empty)); got != "" {
		t.Errorf("declaredFieldHelp() over a deployment declaring none = %q, want nothing", got)
	}
	// Bounded: the client retries a refused connection with backoff,
	// which left this one call holding the test for nearly two minutes.
	unreachable, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if got := declaredFieldHelp(unreachable, accessCodeClient("http://127.0.0.1:1")); got != "" {
		t.Errorf("declaredFieldHelp() over an unreachable deployment = %q, want nothing", got)
	}
}

// A value in a set that has to carry a comma is written the way a
// -filter condition writes one.
func TestASetValueCanCarryAComma(t *testing.T) {
	t.Parallel()
	server, _ := fieldTestServer(t, []model.CustomField{
		{Name: "customers", Type: model.CustomFieldText, Multiple: true},
	})
	if err := runCLIWithAccessCode([]string{"-server", server, "create", "-title", "Two customers", "-no-repo",
		"-field", "customers=Acme%2C Inc.,Globex"}); err != nil {
		t.Fatalf("grain create -field customers=...: %v", err)
	}
	wantValues(t, carries(t, server, "2"), map[string][]string{"customers": {"Acme, Inc.", "Globex"}})
}
