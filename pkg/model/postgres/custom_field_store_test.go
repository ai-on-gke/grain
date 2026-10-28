package postgres_test

// The custom field declarations and the values tasks carry for them
// (grain/task-53), against a real embedded SQLite database --
// store_test.go's own doc comment gives the reasoning for that again
// here.

import (
	"slices"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

func TestCustomFieldRoundTrips(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	want := model.CustomField{
		Name:        "priority",
		Label:       "Priority",
		Type:        model.CustomFieldEnum,
		Options:     []string{"low", "high"},
		Description: "how far up the pile this belongs",
		Default:     "low",
		Position:    2,
	}
	if err := store.PutCustomField(ctx, want); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := store.GetCustomField(ctx, "priority")
	if err != nil || got == nil {
		t.Fatalf("get: %v (nil=%v)", err, got == nil)
	}
	if got.Label != want.Label || got.Type != want.Type || got.Description != want.Description ||
		got.Default != want.Default || got.Position != want.Position {
		t.Errorf("declaration did not survive: %+v", *got)
	}
	// The options are the reason this column is JSON rather than a
	// comma-separated list: order is meaningful ("low, high" is not
	// alphabetical) and a label may contain the separator.
	if len(got.Options) != 2 || got.Options[0] != "low" || got.Options[1] != "high" {
		t.Errorf("options = %v, want [low high] in that order", got.Options)
	}

	missing, err := store.GetCustomField(ctx, "nothing-declares-this")
	if err != nil || missing != nil {
		t.Errorf("get of an undeclared field = (%v, %v), want (nil, nil)", missing, err)
	}
}

// A declaration grain could not offer is refused where it is written,
// rather than stored and rendered as a field nobody can fill in.
func TestPutCustomFieldRefusesWhatCannotBeOffered(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutCustomField(ctx, model.CustomField{
		Name: "priority", Type: model.CustomFieldEnum,
	}); err == nil {
		t.Fatal("an enum with no options was stored")
	}
}

func TestListCustomFieldsIsInDeclaredOrder(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	for _, f := range []model.CustomField{
		{Name: "zebra"},
		{Name: "apple"},
		{Name: "first", Position: -1},
	} {
		if err := store.PutCustomField(ctx, f); err != nil {
			t.Fatalf("put %s: %v", f.Name, err)
		}
	}
	fields, err := store.ListCustomFields(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var names []string
	for _, f := range fields {
		names = append(names, f.Name)
	}
	want := []string{"first", "apple", "zebra"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("order = %v, want %v (position, then name)", names, want)
		}
	}

	if err := store.DeleteCustomField(ctx, "apple"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	fields, err = store.ListCustomFields(ctx)
	if err != nil || len(fields) != 2 {
		t.Fatalf("after delete: %d fields, %v", len(fields), err)
	}
}

// The values ride with the task itself -- written in the same
// transaction, read back by the same hydration, and gone when the task
// is replaced with one that says something else.
func TestTaskCustomFieldValuesRoundTrip(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	task := model.Task{
		ID:     "1",
		Intent: model.IntentImplement,
		Title:  "Fix the thing",
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: human},
			Reason:      model.ReasonDirect,
		},
		Binding:      model.BindingDirective,
		CustomFields: map[string][]string{"customer": {"acme"}, "priority": {"high"}, "blank": {""}},
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := store.GetTask(ctx, "1")
	if err != nil || got == nil {
		t.Fatalf("get: %v (nil=%v)", err, got == nil)
	}
	if !slices.Equal(got.CustomFields["customer"], []string{"acme"}) ||
		!slices.Equal(got.CustomFields["priority"], []string{"high"}) {
		t.Errorf("values = %v, want the two that were set", got.CustomFields)
	}
	// An empty value is not a value: dropped on the way in, so a cleared
	// field leaves no row to be exported, diffed or wondered about.
	if _, ok := got.CustomFields["blank"]; ok {
		t.Errorf("an empty value was stored: %v", got.CustomFields)
	}

	task.CustomFields = nil
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("re-put: %v", err)
	}
	got, err = store.GetTask(ctx, "1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.CustomFields != nil {
		t.Errorf("values = %v, want nil once the task carries none", got.CustomFields)
	}
}

// A field declared multiple carries a set of values on a task -- a row
// per value, which is what the widened primary key is for
// (model.CustomField.Multiple).
func TestTaskMultiValuedCustomFieldRoundTrips(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutCustomField(ctx, model.CustomField{Name: "components", Multiple: true}); err != nil {
		t.Fatalf("declaring: %v", err)
	}
	declared, err := store.GetCustomField(ctx, "components")
	if err != nil || declared == nil {
		t.Fatalf("get declaration: %v (nil=%v)", err, declared == nil)
	}
	if !declared.Multiple {
		t.Errorf("declaration = %+v, want multiple to survive the round trip", *declared)
	}

	task := model.Task{
		ID:     "1",
		Intent: model.IntentImplement,
		Title:  "Fix the thing",
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: human},
			Reason:      model.ReasonDirect,
		},
		Binding: model.BindingDirective,
		// "store" twice: what the field holds is a set, so a duplicate is
		// dropped rather than left to fail against the primary key.
		CustomFields: map[string][]string{"components": {"store", "prompt", "store"}},
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := store.GetTask(ctx, "1")
	if err != nil || got == nil {
		t.Fatalf("get: %v (nil=%v)", err, got == nil)
	}
	// Read back in value order, so that two exports of a task nobody
	// touched are the same bytes (store.go's hydrate).
	if !slices.Equal(got.CustomFields["components"], []string{"prompt", "store"}) {
		t.Errorf("values = %v, want both, once each, in value order", got.CustomFields["components"])
	}
}

// grain/task-201: which tasks a declaration stranded, which is the
// question nothing server-side could answer before -- a retired enum
// option was findable only by already knowing which value to ask about.
func TestLegacyCustomFieldUsesFindsWhatADeclarationStranded(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	file := func(id string, values map[string][]string) {
		t.Helper()
		if err := store.PutTask(ctx, model.Task{
			ID:     id,
			Intent: model.IntentImplement,
			Title:  "task " + id,
			Origin: model.Origin{
				Attribution: model.Attribution{Actor: human},
				Reason:      model.ReasonDirect,
			},
			Binding:      model.BindingDirective,
			CustomFields: values,
		}); err != nil {
			t.Fatalf("filing %s: %v", id, err)
		}
	}
	file("2", map[string][]string{"priority": {"critical"}, "customer": {"acme"}})
	file("10", map[string][]string{"priority": {"critical"}})
	file("9", map[string][]string{"priority": {"urgent"}})
	file("11", map[string][]string{"priority": {"high"}})
	// A task left holding a set by a field that has stopped holding one,
	// where no value is wrong and the multiplicity is.
	file("12", map[string][]string{"teams": {"infra", "ui"}})
	// And a field the state repository no longer declares at all.
	file("13", map[string][]string{"retired": {"whatever"}})

	// The declarations as they would be *after* the change being
	// weighed: "critical" and "urgent" retired, `teams` no longer
	// holding a set.
	fields := []model.CustomField{
		{Name: "priority", Label: "Priority", Type: model.CustomFieldEnum,
			Options: []string{"low", "high"}, Position: 1},
		{Name: "teams", Type: model.CustomFieldEnum, Options: []string{"infra", "ui"}, Position: 2},
		{Name: "customer", Position: 3},
	}
	uses, err := store.LegacyCustomFieldUses(ctx, fields)
	if err != nil {
		t.Fatalf("reading what is stranded: %v", err)
	}
	if len(uses) != 3 {
		t.Fatalf("got %d fields with something stranded, want priority, teams and retired: %+v", len(uses), uses)
	}

	// Declaration order first (position, then name), undeclared after.
	if uses[0].Field != "priority" || uses[1].Field != "teams" || uses[2].Field != "retired" {
		t.Errorf("fields = %s, %s, %s; want priority, teams, retired",
			uses[0].Field, uses[1].Field, uses[2].Field)
	}
	if uses[0].Label != "Priority" || !uses[0].Declared {
		t.Errorf("priority reads as %q, declared=%v", uses[0].Label, uses[0].Declared)
	}
	// Most-carried value first, since the count is what the decision
	// turns on, and task ids as the numbers they are: 9 before 10.
	if len(uses[0].Values) != 2 ||
		uses[0].Values[0].Value != "critical" || !slices.Equal(uses[0].Values[0].Tasks, []string{"2", "10"}) ||
		uses[0].Values[1].Value != "urgent" || !slices.Equal(uses[0].Values[1].Tasks, []string{"9"}) {
		t.Errorf("priority's stranded values = %+v, want critical on 2 and 10, then urgent on 9", uses[0].Values)
	}
	if !slices.Equal(uses[0].Tasks, []string{"2", "9", "10"}) {
		t.Errorf("priority's tasks = %v, want 2, 9 and 10 -- and not 11, which carries a value it still offers", uses[0].Tasks)
	}
	if len(uses[0].Sets) != 0 {
		t.Errorf("priority reports sets %v, and no task carries one", uses[0].Sets)
	}

	// The set is reported as a set rather than as two stranded values:
	// "infra" and "ui" are both offered, and clearing either is what
	// fixes the task.
	if !slices.Equal(uses[1].Sets, []string{"12"}) || len(uses[1].Values) != 0 {
		t.Errorf("teams = %+v, want task 12 under Sets and no stranded value", uses[1])
	}

	if uses[2].Declared || !slices.Equal(uses[2].Tasks, []string{"13"}) ||
		len(uses[2].Values) != 1 || uses[2].Values[0].Value != "whatever" {
		t.Errorf("an undeclared field = %+v, want everything task 13 holds for it", uses[2])
	}

	// Asked about the declarations that would keep every value, there is
	// nothing to report: this is a question about a declaration, not a
	// property of the rows.
	kept := []model.CustomField{
		{Name: "priority", Type: model.CustomFieldEnum, Options: []string{"low", "high", "critical", "urgent"}},
		{Name: "teams", Type: model.CustomFieldEnum, Options: []string{"infra", "ui"}, Multiple: true},
		{Name: "customer"},
		{Name: "retired"},
	}
	uses, err = store.LegacyCustomFieldUses(ctx, kept)
	if err != nil {
		t.Fatalf("reading against the declarations that hold everything: %v", err)
	}
	if len(uses) != 0 {
		t.Errorf("declarations that offer every stored value stranded %+v", uses)
	}
}
