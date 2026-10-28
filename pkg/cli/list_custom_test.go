package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

// grain/task-99: the fields a deployment declares are attributes
// `grain list` can narrow and order by, under custom.<name> -- the ids
// the UI's own narrowing bar writes into a link, so the same question is
// askable in both places. These are that vocabulary against a listing;
// list_cli_test.go covers the wiring that fetches the declarations.

// declarations is one of every shape a deployment can declare: an enum
// with an order that is not alphabetical, a date, free text, a boolean,
// a number, and an enum holding a set.
func declarations() []ui.CustomFieldDeclaration {
	return []ui.CustomFieldDeclaration{
		{Name: "priority", Label: "Priority", Type: "enum", Options: []string{"low", "high", "critical"}},
		{Name: "due", Label: "Due date", Type: "date"},
		{Name: "customer", Type: "text"},
		{Name: "escalated", Type: "boolean"},
		{Name: "points", Type: "number"},
		{Name: "areas", Type: "enum", Options: []string{"ui", "daemon"}, Multiple: true},
	}
}

// filled is a listing with values for those fields: two tasks that say
// most things, one that says one thing, and one that says nothing at all
// -- which is the ordinary case, since no field is required.
func filled() []ui.Task {
	return []ui.Task{
		{
			ID: "1", Title: "Widget work", State: model.StateQueued, CreatedAt: at(3),
			CustomFields: map[string]ui.CustomFieldValue{
				"priority": {"high"},
				"due":      {"2026-09-10"},
				"customer": {"Acme"},
				"points":   {"3"},
				"areas":    {"ui", "daemon"},
			},
		},
		{
			ID: "2", Title: "Gadget work", State: model.StateRunning, CreatedAt: at(1),
			CustomFields: map[string]ui.CustomFieldValue{
				"priority":  {"critical"},
				"due":       {"2026-09-04"},
				"escalated": {"true"},
				"points":    {"10"},
				"areas":     {"daemon"},
			},
		},
		{
			ID: "3", Title: "Loose chat", State: model.StateProposed, CreatedAt: at(2),
			CustomFields: map[string]ui.CustomFieldValue{
				"priority": {"low"},
				"customer": {"Globex"},
			},
		},
		{ID: "4", Title: "Says nothing", State: model.StateQueued, CreatedAt: at(4)},
	}
}

// askedOf is `asked` over a deployment's own vocabulary rather than the
// built-in one.
func askedOf(t *testing.T, attrs attributeSet, zone *time.Location, filters ...string) taskQuery {
	t.Helper()
	q := taskQuery{now: time.Date(2026, 9, 4, 13, 0, 0, 0, time.UTC), zone: zone}
	for _, raw := range filters {
		cond, err := attrs.parseCondition(raw)
		if err != nil {
			t.Fatalf("parseCondition(%q): %v", raw, err)
		}
		q.filters = append(q.filters, cond)
	}
	return q
}

func TestCustomFieldConditionsNarrowTheListing(t *testing.T) {
	t.Parallel()
	attrs := attributesFor(declarations())
	for _, tc := range []struct {
		name    string
		filters []string
		want    []string
	}{
		{name: "one value of an enum", filters: []string{"custom.priority:any:critical"}, want: []string{"2"}},
		// The question a flag per field could never have asked: either
		// of two values, spelt as a link spells it.
		{name: "either of two", filters: []string{"custom.priority:any:high|critical"}, want: []string{"1", "2"}},
		{name: "all but one", filters: []string{"custom.priority:none:low"}, want: []string{"1", "2", "4"}},
		// Nobody filled it in, which is the ordinary answer for a field
		// and so a question worth asking.
		{name: "nobody filled it in", filters: []string{"custom.priority:empty"}, want: []string{"4"}},
		{name: "somebody did", filters: []string{"custom.priority:filled"}, want: []string{"1", "2", "3"}},
		{name: "none is the same question", filters: []string{"custom.priority:any:none"}, want: []string{"4"}},
		{name: "text a value contains", filters: []string{"custom.customer:has:glob"}, want: []string{"3"}},
		{name: "a boolean", filters: []string{"custom.escalated:any:true"}, want: []string{"2"}},
		{name: "a number", filters: []string{"custom.points:gt:5"}, want: []string{"2"}},
		{name: "a number, the other way", filters: []string{"custom.points:lt:5"}, want: []string{"1"}},
		{name: "a day", filters: []string{"custom.due:before:2026-09-05"}, want: []string{"2"}},
		{name: "a day, the other way", filters: []string{"custom.due:after:2026-09-05"}, want: []string{"1"}},
		// A field holding a set takes the comparisons Capability takes,
		// "has all of" among them.
		{name: "any of a set", filters: []string{"custom.areas:any:ui"}, want: []string{"1"}},
		{name: "all of a set", filters: []string{"custom.areas:all:ui,daemon"}, want: []string{"1"}},
		{name: "two fields, both of which have to hold",
			filters: []string{"custom.priority:any:high|critical", "custom.points:gt:5"}, want: []string{"2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := askedOf(t, attrs, time.UTC, tc.filters...)
			if got := q.apply(filled()); !equal(ids(got), tc.want) {
				t.Fatalf("apply(%v) = %v, want %v", tc.filters, ids(got), tc.want)
			}
		})
	}
}

// The half a filter cannot stand in for: "the urgent ones" is a
// narrowing and "the backlog, most urgent first" is an order. An enum
// ranks by its declared options rather than alphabetically, which is
// what declaring them in an order is for, and the tasks nobody filled
// the field in on go last whichever way it points.
func TestCustomFieldsAreSortKeys(t *testing.T) {
	t.Parallel()
	attrs := attributesFor(declarations())
	for _, tc := range []struct {
		name string
		sort string
		want []string
	}{
		{name: "an enum, in the order it was declared", sort: "custom.priority:asc", want: []string{"3", "1", "2"}},
		{name: "backwards", sort: "custom.priority:desc", want: []string{"2", "1", "3"}},
		{name: "a number, as a number", sort: "custom.points:desc", want: []string{"2", "1"}},
		{name: "a date, by the day it names", sort: "custom.due:asc", want: []string{"2", "1"}},
		{name: "text", sort: "custom.customer:asc", want: []string{"1", "3"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			specs, err := attrs.parseSort(tc.sort)
			if err != nil {
				t.Fatalf("parseSort(%q): %v", tc.sort, err)
			}
			q := taskQuery{sort: specs, zone: time.UTC}
			got := ids(q.apply(filled()))
			// Whatever the order, the tasks with no value for the field
			// are after the ones that have one.
			if !equal(got[:len(tc.want)], tc.want) {
				t.Fatalf("apply(-sort %s) = %v, want it to start %v", tc.sort, got, tc.want)
			}
		})
	}
}

// A custom date field holds a calendar day (model.CustomFieldDateLayout),
// so when that day *begins* is a question about the deployment's own
// clock -- read as UTC, a task due on the 4th would answer "no" to "is
// after the 4th" on every deployment west of it. taskFilters.js reads one
// the same way, which is the reason this side has to.
func TestACustomDayIsReadInTheDeploymentsZone(t *testing.T) {
	t.Parallel()
	attrs := attributesFor(declarations())
	pacific, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatalf("loading the zone: %v", err)
	}
	// The 4th begins at 07:00 UTC there, so the task due that day is
	// after 03:00 UTC on the 4th -- and would not be, read as UTC.
	const after = "custom.due:after:2026-09-04T03:00:00Z"
	if got := ids(askedOf(t, attrs, pacific, after).apply(filled())); !equal(got, []string{"1", "2"}) {
		t.Fatalf("apply(%s) = %v, want the task due that day as well as the later one", after, got)
	}
	if got := ids(askedOf(t, attrs, time.UTC, after).apply(filled())); !equal(got, []string{"1"}) {
		t.Fatalf("the same condition read in UTC = %v, want only the later task -- which is what proves the zone is doing the work", got)
	}
}

// Which is why a listing that names a custom date attribute asks the
// deployment what zone it keeps, even when the condition itself names an
// instant: it is the *task's* value that is a bare day.
func TestACustomDateNeedsTheDeploymentsZone(t *testing.T) {
	t.Parallel()
	attrs := attributesFor(declarations())
	for _, tc := range []struct {
		flags listFlags
		want  bool
	}{
		{flags: listFlags{filters: conditionList{"custom.due:after:2026-09-04T03:00:00Z"}}, want: true},
		{flags: listFlags{filters: conditionList{"custom.due:within:7"}}, want: true},
		{flags: listFlags{sortBy: "custom.due:asc"}, want: true},
		// The built-in date is a timestamp, so only a bare day written
		// in the condition needs a zone.
		{flags: listFlags{filters: conditionList{"created:after:2026-09-04T03:00:00Z"}}, want: false},
		{flags: listFlags{filters: conditionList{"created:after:2026-09-04"}}, want: true},
		{flags: listFlags{sortBy: "created:asc"}, want: false},
		{flags: listFlags{filters: conditionList{"custom.priority:any:low"}}, want: false},
	} {
		q, err := tc.flags.query(attrs)
		if err != nil {
			t.Fatalf("query(%+v): %v", tc.flags, err)
		}
		if got := q.needsZone(); got != tc.want {
			t.Errorf("needsZone(%+v) = %v, want %v", tc.flags, got, tc.want)
		}
	}
}

// A value the field does not declare is refused with the ones it does,
// the same way a misspelt -state is -- and an attribute nothing declares
// is refused with the attributes this deployment actually has, its own
// among them, which is the whole of what tells somebody custom.priority
// exists and custom.prio does not.
func TestCustomFieldsRefuseWhatNobodyCanActOn(t *testing.T) {
	t.Parallel()
	attrs := attributesFor(declarations())
	for _, tc := range []struct {
		raw  string
		says string
	}{
		{raw: "custom.priority:any:urgent", says: "low, high, critical"},
		{raw: "custom.escalated:any:maybe", says: "true, false"},
		{raw: "custom.prio:any:high", says: "custom.priority"},
		{raw: "custom.points:has:3", says: "takes no comparison"},
	} {
		_, err := attrs.parseCondition(tc.raw)
		if err == nil {
			t.Fatalf("parseCondition(%q) succeeded, want an error naming the words that work", tc.raw)
		}
		if !strings.Contains(err.Error(), tc.says) {
			t.Errorf("parseCondition(%q) = %v, want it to say %q", tc.raw, err, tc.says)
		}
	}
}

// The namespace earns its keep here: a deployment that declares a field
// called "repo" gets an attribute of its own rather than one that
// quietly replaces the built-in everything else already names.
func TestADeclaredFieldCannotShadowABuiltIn(t *testing.T) {
	t.Parallel()
	attrs := attributesFor([]ui.CustomFieldDeclaration{{Name: "repo", Type: "text"}})
	builtin := attrs.byID("repo")
	if builtin == nil || builtin.custom {
		t.Fatalf(`byID("repo") = %+v, want the built-in repo attribute`, builtin)
	}
	declared := attrs.byID("custom.repo")
	if declared == nil || !declared.custom {
		t.Fatalf(`byID("custom.repo") = %+v, want the declared field`, declared)
	}
}

// A type this build has never heard of -- a newer daemon declaring one
// -- is compared as text rather than dropped out of the vocabulary, the
// reading taskAttributes.js gives it too.
func TestAnUnknownFieldTypeIsText(t *testing.T) {
	t.Parallel()
	attrs := attributesFor([]ui.CustomFieldDeclaration{{Name: "colour", Type: "swatch"}})
	a := attrs.byID("custom.colour")
	if a == nil || a.typ != typeText {
		t.Fatalf(`custom.colour is %+v, want a text attribute`, a)
	}
}

// A deployment that declares nothing is the built-in vocabulary itself,
// which is what keeps the ordinary listing exactly what it was.
func TestNoDeclarationsIsTheBuiltInVocabulary(t *testing.T) {
	t.Parallel()
	if got := attributesFor(nil); len(got) != len(builtinAttributes) {
		t.Fatalf("attributesFor(nil) has %d attributes, want the %d built-in ones", len(got), len(builtinAttributes))
	}
}

// grain/task-201: the tasks a changed declaration stranded. A value the
// declaration has moved on from stays on the task until somebody edits
// it off (model.NormalizeCustomFieldValuesFrom), and asking about it by
// name -- `-filter custom.priority:any:critical` -- only works if you
// already know which value to ask about. This is the question that does
// not need to know.
func TestLegacyNarrowsToTheTasksADeclarationStranded(t *testing.T) {
	t.Parallel()
	// The declarations *after* the change: "critical" retired from the
	// options, and `areas` no longer holding a set.
	retired := []ui.CustomFieldDeclaration{
		{Name: "priority", Label: "Priority", Type: "enum", Options: []string{"low", "high"}},
		{Name: "customer", Type: "text"},
		{Name: "areas", Type: "enum", Options: []string{"ui", "daemon"}},
	}
	attrs := attributesFor(retired)

	q := askedOf(t, attrs, time.UTC, "custom.priority:legacy")
	if got := ids(q.apply(filled())); !equal(got, []string{"2"}) {
		t.Errorf("tasks carrying a retired option = %v, want only the one that has it", got)
	}
	// The set is stranded whole -- both of its values are still offered,
	// and there being two of them is what the declaration no longer
	// allows.
	q = askedOf(t, attrs, time.UTC, "custom.areas:legacy")
	if got := ids(q.apply(filled())); !equal(got, []string{"1"}) {
		t.Errorf("tasks left holding a set = %v, want the one with two values", got)
	}
	// Free text strands nothing: a text field takes whatever it is
	// given, so this is not a way to ask "which tasks have a value at
	// all" by another name.
	q = askedOf(t, attrs, time.UTC, "custom.customer:legacy")
	if got := ids(q.apply(filled())); len(got) != 0 {
		t.Errorf("text stranded %v, and text accepts everything", got)
	}
	// Asked against the declarations that still offer every value, it
	// finds nothing: this is a question about a declaration, not a
	// property of the task.
	q = askedOf(t, attributesFor(declarations()), time.UTC, "custom.priority:legacy")
	if got := ids(q.apply(filled())); len(got) != 0 {
		t.Errorf("the declaration that offers every stored value stranded %v", got)
	}

	// No built-in takes the comparison, and a typo is answered with the
	// comparisons that do -- the rule every -filter clause has here.
	if _, err := attrs.parseCondition("state:legacy"); err == nil {
		t.Error("a built-in attribute took \"legacy\", which only a deployment's own declaration can answer")
	}
	if _, err := attrs.parseCondition("custom.priority:legacy:critical"); err == nil {
		t.Error("\"legacy\" took a value, and it compares against the declaration rather than against one")
	}
}
