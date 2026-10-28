package model

import (
	"slices"
	"testing"
)

// The declarations are written by agents, in a pull request against the
// state repository, with no form to reject a bad one -- so what Validate
// and NormalizeValue accept is the whole of the guard, and it is checked
// here rather than left to the day somebody declares an enum with no
// options.

func TestCustomFieldValidate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field CustomField
		ok    bool
	}{
		{"a name alone is a text field", CustomField{Name: "customer"}, true},
		{"an enum with options", CustomField{
			Name: "priority", Type: CustomFieldEnum, Options: []string{"low", "high"}}, true},
		{"a default the type accepts", CustomField{
			Name: "due", Type: CustomFieldDate, Default: "2026-01-02"}, true},
		{"no name at all", CustomField{}, false},
		{"a name with spaces in it", CustomField{Name: "due date"}, false},
		{"a name with capitals in it", CustomField{Name: "DueDate"}, false},
		{"a type nobody has heard of", CustomField{Name: "x", Type: "colour"}, false},
		{"an enum with no options", CustomField{Name: "priority", Type: CustomFieldEnum}, false},
		{"an enum with the same option twice", CustomField{
			Name: "priority", Type: CustomFieldEnum, Options: []string{"low", "low"}}, false},
		{"options on something that is not an enum", CustomField{
			Name: "customer", Options: []string{"acme"}}, false},
		{"a default the type refuses", CustomField{
			Name: "due", Type: CustomFieldDate, Default: "next tuesday"}, false},
		// A declaration is written by hand, and a form hands the default
		// straight to its control: anything but the stored spelling of a
		// value opens the form on an empty box (or one a browser will
		// not submit), so the spelling is the declaration's job rather
		// than the reader's.
		{"a default that needs trimming", CustomField{
			Name: "customer", Default: " acme "}, false},
		{"a default that is nothing but space", CustomField{
			Name: "customer", Default: "   "}, false},
		{"a boolean default written as a person would say it", CustomField{
			Name: "escalated", Type: CustomFieldBool, Default: "yes"}, false},
		{"a boolean default written the way it is stored", CustomField{
			Name: "escalated", Type: CustomFieldBool, Default: "false"}, true},
		{"an enum default that is not one of the options", CustomField{
			Name: "priority", Type: CustomFieldEnum, Options: []string{"low", "high"},
			Default: " high"}, false},
		{"a number default a number input could not show", CustomField{
			Name: "estimate", Type: CustomFieldNumber, Default: "+3"}, false},
		{"a number default that is not finite", CustomField{
			Name: "estimate", Type: CustomFieldNumber, Default: "NaN"}, false},
		{"a date default in a year no date input has", CustomField{
			Name: "due", Type: CustomFieldDate, Default: "0000-01-02"}, false},
		// An option is offered exactly as it is spelled and a value is
		// trimmed before it is compared, so an option with space around
		// it is a choice nobody could ever store -- and two that differ
		// only by space are one choice shown twice.
		{"an option with space around it", CustomField{
			Name: "priority", Type: CustomFieldEnum, Options: []string{"low", " high"}}, false},
		{"two options that trim to the same choice", CustomField{
			Name: "priority", Type: CustomFieldEnum, Options: []string{"low", "low "}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.field.Validate()
			if tc.ok && err != nil {
				t.Fatalf("Validate() = %v, want it accepted", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("Validate() accepted a declaration grain could not offer")
			}
		})
	}
}

// A value is stored the way its type spells it, so that two tasks that
// say the same thing say it identically -- in the store, in a dump, and
// in the prompt a run is handed.
func TestCustomFieldNormalizeValue(t *testing.T) {
	number := CustomField{Name: "n", Type: CustomFieldNumber}
	enum := CustomField{Name: "p", Type: CustomFieldEnum, Options: []string{"low", "high"}}
	date := CustomField{Name: "d", Type: CustomFieldDate}
	boolean := CustomField{Name: "b", Type: CustomFieldBool}
	text := CustomField{Name: "t"}

	for _, tc := range []struct {
		field CustomField
		in    string
		want  string
		ok    bool
	}{
		{number, " 3.50 ", "3.50", true},
		{number, "seven", "", false},
		{number, "-2", "-2", true},
		{number, "1.5e3", "1.5e3", true},
		// What Go's own ParseFloat takes and a number input does not:
		// the non-finite spellings, a hexadecimal float, a leading plus,
		// Go's digit separators, and a number no float64 can hold. Every
		// one of them is a value the form would show as an empty box and
		// a filter would compare as nothing.
		{number, "NaN", "", false},
		{number, "Inf", "", false},
		{number, "-Infinity", "", false},
		{number, "0x1p-2", "", false},
		{number, "+3", "", false},
		{number, "1_000", "", false},
		{number, "1e999", "", false},
		{enum, "high", "high", true},
		{enum, "urgent", "", false},
		{date, "2026-01-02", "2026-01-02", true},
		{date, "02/01/2026", "", false},
		// Year zero is a date Go's calendar has and nobody else does:
		// <input type="date"> wants a year greater than zero and shows
		// an empty box for this one.
		{date, "0000-01-02", "", false},
		{boolean, "TRUE", "true", true},
		{boolean, "no", "false", true},
		{boolean, "maybe", "", false},
		{text, "  acme  ", "acme", true},
		// Empty is always allowed, whatever the type: there are no
		// required fields here, and a field declared today would
		// otherwise invalidate every task filed before it.
		{date, "   ", "", true},
	} {
		got, err := tc.field.NormalizeValue(tc.in)
		if tc.ok && err != nil {
			t.Errorf("%s.NormalizeValue(%q) = %v, want %q", tc.field.FieldType(), tc.in, err, tc.want)
			continue
		}
		if !tc.ok {
			if err == nil {
				t.Errorf("%s.NormalizeValue(%q) accepted a value its type cannot hold", tc.field.FieldType(), tc.in)
			}
			continue
		}
		if got != tc.want {
			t.Errorf("%s.NormalizeValue(%q) = %q, want %q", tc.field.FieldType(), tc.in, got, tc.want)
		}
	}
}

func TestNormalizeCustomFieldValues(t *testing.T) {
	fields := []CustomField{
		{Name: "customer"},
		{Name: "priority", Type: CustomFieldEnum, Options: []string{"low", "high"}},
	}

	got, err := NormalizeCustomFieldValues(fields, map[string][]string{
		"customer": {" acme "},
		"priority": {"high"},
	})
	if err != nil {
		t.Fatalf("normalizing: %v", err)
	}
	if !slices.Equal(got["customer"], []string{"acme"}) || !slices.Equal(got["priority"], []string{"high"}) {
		t.Errorf("values = %v, want them canonicalized", got)
	}

	// A cleared field leaves nothing behind, so "never set" and "set and
	// cleared" are one state rather than two that read differently in a
	// dump.
	got, err = NormalizeCustomFieldValues(fields, map[string][]string{"customer": {""}})
	if err != nil || got != nil {
		t.Errorf("clearing the only value = (%v, %v), want (nil, nil)", got, err)
	}

	if _, err := NormalizeCustomFieldValues(fields, map[string][]string{"nope": {"x"}}); err == nil {
		t.Error("a value for a field nobody declares was accepted, and would be shown nowhere")
	}
	if _, err := NormalizeCustomFieldValues(fields, map[string][]string{"priority": {"urgent"}}); err == nil {
		t.Error("a value outside the declared options was accepted")
	}
	// Clearing an undeclared one is the exception, and the only way a
	// value whose field has been deleted from the state repository can
	// ever be taken off a task.
	if _, err := NormalizeCustomFieldValues(fields, map[string][]string{"retired": {""}}); err != nil {
		t.Errorf("clearing a value for a deleted field = %v, want it allowed", err)
	}
	// A single-valued field handed two of them is refused rather than
	// given whichever came first (NormalizeValues).
	if _, err := NormalizeCustomFieldValues(fields, map[string][]string{"priority": {"low", "high"}}); err == nil {
		t.Error("a single-valued field took two values")
	}
}

// A field declared multiple holds a set: duplicates collapse, order is
// not kept, and the values are still checked one by one against the
// type the declaration gives them.
func TestNormalizeMultipleCustomFieldValues(t *testing.T) {
	fields := []CustomField{
		{Name: "components", Multiple: true},
		{Name: "areas", Type: CustomFieldEnum, Options: []string{"ui", "daemon", "sandbox"}, Multiple: true},
	}

	got, err := NormalizeCustomFieldValues(fields, map[string][]string{
		"components": {" store ", "prompt", "store", ""},
		"areas":      {"sandbox", "daemon"},
	})
	if err != nil {
		t.Fatalf("normalizing: %v", err)
	}
	if !slices.Equal(got["components"], []string{"prompt", "store"}) {
		t.Errorf("components = %v, want the set, deduplicated and sorted", got["components"])
	}
	if !slices.Equal(got["areas"], []string{"daemon", "sandbox"}) {
		t.Errorf("areas = %v, want the set, deduplicated and sorted", got["areas"])
	}

	if _, err := NormalizeCustomFieldValues(fields, map[string][]string{"areas": {"ui", "nope"}}); err == nil {
		t.Error("a value outside the declared options was accepted inside a set")
	}
	// Every value blank is a clear, exactly as one blank value is on a
	// single-valued field.
	got, err = NormalizeCustomFieldValues(fields, map[string][]string{"components": {"", " "}})
	if err != nil || got != nil {
		t.Errorf("clearing a set = (%v, %v), want (nil, nil)", got, err)
	}
}

// A declaration that changes under values already stored grandfathers
// them: an edit may restate one as it stands, clear it, or replace it
// with a value the declaration accepts -- and may not introduce one
// onto something that was not already carrying it
// (NormalizeCustomFieldValuesFrom, and the three policies its doc
// comment weighs).
func TestNormalizeCustomFieldValuesFromGrandfathersWhatIsAlreadyThere(t *testing.T) {
	// "critical" has been dropped from the options, and `teams` has
	// stopped holding a set -- the two ways a merged pull request
	// against tables/custom_field.json can strand a value.
	fields := []CustomField{
		{Name: "priority", Type: CustomFieldEnum, Options: []string{"low", "high"}},
		{Name: "teams", Type: CustomFieldEnum, Options: []string{"infra", "ui"}},
	}
	had := map[string][]string{"priority": {"critical"}, "teams": {"infra", "ui"}}

	// The restatement a form makes when it is editing something else
	// entirely: every field as it stands, and nothing about it new.
	got, err := NormalizeCustomFieldValuesFrom(fields, map[string][]string{
		"priority": {"critical"},
		"teams":    {"infra", "ui"},
	}, had)
	if err != nil {
		t.Fatalf("restating what the task already carries: %v", err)
	}
	if !slices.Equal(got["priority"], []string{"critical"}) {
		t.Errorf("priority = %v, want the retired option kept", got["priority"])
	}
	if !slices.Equal(got["teams"], []string{"infra", "ui"}) {
		t.Errorf("teams = %v, want the whole set kept rather than one of it", got["teams"])
	}

	// Paring the set down is a subset of what was there, so every step
	// of "take one off, save" lands.
	got, err = NormalizeCustomFieldValuesFrom(fields, map[string][]string{"teams": {"ui"}}, had)
	if err != nil || !slices.Equal(got["teams"], []string{"ui"}) {
		t.Errorf("paring the set down = (%v, %v), want the one value that was kept", got, err)
	}
	// So is replacing a legacy value with one the declaration offers,
	// and so is clearing it.
	got, err = NormalizeCustomFieldValuesFrom(fields, map[string][]string{"priority": {"high"}}, had)
	if err != nil || !slices.Equal(got["priority"], []string{"high"}) {
		t.Errorf("replacing a legacy value = (%v, %v), want the declared one", got, err)
	}
	got, err = NormalizeCustomFieldValuesFrom(fields, map[string][]string{"priority": {""}}, had)
	if err != nil || got != nil {
		t.Errorf("clearing a legacy value = (%v, %v), want (nil, nil)", got, err)
	}

	// What none of that opens: a legacy value is grandfathered onto the
	// thing that holds it and nowhere else.
	if _, err := NormalizeCustomFieldValuesFrom(fields,
		map[string][]string{"priority": {"critical"}}, nil); err == nil {
		t.Error("a retired option was accepted onto a task that never carried it")
	}
	if _, err := NormalizeCustomFieldValuesFrom(fields,
		map[string][]string{"priority": {"critical"}}, map[string][]string{"priority": {"low"}}); err == nil {
		t.Error("a retired option was accepted over a value the declaration does offer")
	}
	// And a set is not allowed to grow just because it was already too
	// big for its declaration.
	if _, err := NormalizeCustomFieldValuesFrom(fields,
		map[string][]string{"teams": {"infra", "ui", "low"}}, had); err == nil {
		t.Error("a single-valued field took a value it was not already carrying")
	}
	// A field nothing declares at all is the same bargain: restating
	// what is stored says nothing new, setting something else is the
	// error it has always been.
	got, err = NormalizeCustomFieldValuesFrom(fields, map[string][]string{"retired": {"kept"}},
		map[string][]string{"retired": {"kept"}})
	if err != nil || !slices.Equal(got["retired"], []string{"kept"}) {
		t.Errorf("restating an undeclared field = (%v, %v), want it kept", got, err)
	}
	if _, err := NormalizeCustomFieldValuesFrom(fields, map[string][]string{"retired": {"new"}},
		map[string][]string{"retired": {"kept"}}); err == nil {
		t.Error("a new value was accepted for a field nothing declares")
	}
}

// Multiple is a flag on the types that can hold a set, and rejected on
// the ones that cannot -- what a declaration in the state repository is
// checked against by `grain state check` before it merges.
func TestValidateMultiple(t *testing.T) {
	for _, f := range []CustomField{
		{Name: "components", Multiple: true},
		{Name: "areas", Type: CustomFieldEnum, Options: []string{"ui"}, Multiple: true},
	} {
		if err := f.Validate(); err != nil {
			t.Errorf("%s: %v, want a multi-valued %s accepted", f.Name, err, f.FieldType())
		}
	}
	for _, f := range []CustomField{
		{Name: "escalated", Type: CustomFieldBool, Multiple: true},
		{Name: "estimate", Type: CustomFieldNumber, Multiple: true},
		{Name: "due", Type: CustomFieldDate, Multiple: true},
	} {
		if err := f.Validate(); err == nil {
			t.Errorf("%s: a multi-valued %s was accepted", f.Name, f.FieldType())
		}
	}
}

// FilterCustomFieldValues is the same normalization for a caller with
// nobody to hand an error to -- a schedule firing at three in the
// morning, a template's values on a suite pass, an agent's proposal
// relayed after its run is over (grain/task-105). The canonical value it
// keeps is the one NormalizeCustomFieldValues would have stored; the
// difference is that what fails is dropped and named rather than failing
// the whole call.
func TestFilterCustomFieldValues(t *testing.T) {
	fields := []CustomField{
		{Name: "customer"},
		{Name: "priority", Type: CustomFieldEnum, Options: []string{"low", "high"}},
		{Name: "escalated", Type: CustomFieldBool},
	}

	kept, dropped := FilterCustomFieldValues(fields, map[string][]string{
		"customer":  {" acme "},
		"escalated": {"YES"},
	})
	if !slices.Equal(kept["customer"], []string{"acme"}) ||
		!slices.Equal(kept["escalated"], []string{"true"}) {
		t.Errorf("values = %v, want them canonicalized exactly as a human's own filing is", kept)
	}
	if len(dropped) != 0 {
		t.Errorf("dropped = %v, want nothing", dropped)
	}

	// The two failures a firing has to survive: a field retired from the
	// state repository since the schedule was written, and a value its
	// declaration does not allow. Both named, in one predictable order,
	// so a caller that has somewhere to say so can.
	kept, dropped = FilterCustomFieldValues(fields, map[string][]string{
		"retired":  {"whatever"},
		"priority": {"urgent"},
		"customer": {"acme"},
	})
	if !slices.Equal(kept["customer"], []string{"acme"}) || len(kept) != 1 {
		t.Errorf("values = %v, want only the one that still holds", kept)
	}
	if len(dropped) != 2 || dropped[0] != "priority" || dropped[1] != "retired" {
		t.Errorf("dropped = %v, want both, by name", dropped)
	}

	// An empty value for a field nobody declares is not a dropped value:
	// it is a caller saying nothing, which is what an empty value means
	// everywhere else too.
	if kept, dropped := FilterCustomFieldValues(fields, map[string][]string{"retired": {""}}); kept != nil || dropped != nil {
		t.Errorf("clearing a retired field = (%v, %v), want (nil, nil)", kept, dropped)
	}

	// A single-valued field is refused a set of two here exactly as it
	// is at the API -- the same NormalizeValues, and the same reading of
	// what a declaration promises -- and the difference is only that the
	// name is dropped and reported rather than failing the firing.
	if kept, dropped := FilterCustomFieldValues(fields,
		map[string][]string{"customer": {"acme", "globex"}}); len(kept) != 0 ||
		len(dropped) != 1 || dropped[0] != "customer" {
		t.Errorf("two values for a single-valued field = (%v, %v), want it dropped by name", kept, dropped)
	}

	// And a field that was declared to hold a set keeps all of them,
	// canonicalized and sorted, the way NormalizeValues stores one.
	multi := []CustomField{{Name: "components", Multiple: true}}
	if kept, dropped := FilterCustomFieldValues(multi,
		map[string][]string{"components": {" api ", "ui", "api"}}); !slices.Equal(
		kept["components"], []string{"api", "ui"}) || len(dropped) != 0 {
		t.Errorf("a multi-valued field = (%v, %v), want the set it was given", kept, dropped)
	}
}

func TestSortCustomFields(t *testing.T) {
	got := SortCustomFields([]CustomField{
		{Name: "zebra"},
		{Name: "apple"},
		{Name: "first", Position: -1},
		{Name: "last", Position: 9},
	})
	var names []string
	for _, f := range got {
		names = append(names, f.Name)
	}
	want := []string{"first", "apple", "zebra", "last"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("order = %v, want %v", names, want)
		}
	}
}

// grain/task-201: the other half of grandfathering a value is being
// able to find the tasks carrying one. Offers and LegacyValues are what
// everything that asks reads it off -- a narrowing, a report of what a
// proposed declaration would strand -- and they have to answer the same
// way the form does (ui/src/taskAttributes.js's isOffered, whose cases
// these mirror).
func TestOffersIsWhatThisDeclarationWouldStillProduce(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		what  string
		field CustomField
		value string
		want  bool
	}{
		{"a declared option", CustomField{Name: "priority", Type: CustomFieldEnum,
			Options: []string{"low", "high"}}, "high", true},
		{"a retired option", CustomField{Name: "priority", Type: CustomFieldEnum,
			Options: []string{"low", "high"}}, "critical", false},
		{"text takes anything", CustomField{Name: "customer"}, "Acme, Inc.", true},
		{"no value at all is every field's own answer", CustomField{Name: "priority",
			Type: CustomFieldEnum, Options: []string{"low"}}, "  ", true},
		{"a number a number input will hold", CustomField{Name: "points",
			Type: CustomFieldNumber}, "3.5", true},
		{"a number it would blank", CustomField{Name: "points",
			Type: CustomFieldNumber}, "+3", false},
		{"a date", CustomField{Name: "due", Type: CustomFieldDate}, "2026-03-04", true},
		{"year zero, which a date input blanks", CustomField{Name: "due",
			Type: CustomFieldDate}, "0000-01-02", false},
		{"what a boolean stores", CustomField{Name: "urgent", Type: CustomFieldBool}, "true", true},
		// Accepted on the way in and canonicalized to "true", so a task
		// carrying it literally is carrying something no form here
		// would produce.
		{"what a boolean takes but does not store", CustomField{Name: "urgent",
			Type: CustomFieldBool}, "yes", false},
		{"a type this build has never heard of assumes least",
			CustomField{Name: "colour", Type: CustomFieldType("swatch")}, "puce", true},
	} {
		if got := tc.field.Offers(tc.value); got != tc.want {
			t.Errorf("%s: Offers(%q) = %v, want %v", tc.what, tc.value, got, tc.want)
		}
	}
}

func TestLegacyValuesAreWhatTheDeclarationHasMovedOnFrom(t *testing.T) {
	t.Parallel()
	priority := CustomField{Name: "priority", Type: CustomFieldEnum, Options: []string{"low", "high"}}
	if got := priority.LegacyValues([]string{"high"}); len(got) != 0 {
		t.Errorf("a value the declaration offers = %v, want nothing", got)
	}
	if got := priority.LegacyValues([]string{"critical"}); !slices.Equal(got, []string{"critical"}) {
		t.Errorf("a retired option = %v, want it named", got)
	}
	if got := priority.LegacyValues(nil); got != nil {
		t.Errorf("a task saying nothing = %v, want nil", got)
	}
	// A field that has stopped holding a set strands the whole set,
	// since each value is fine and there being two of them is not --
	// which is the notice a form draws over all of them
	// (LEGACY_SET_NOTICE).
	if got := priority.LegacyValues([]string{"low", "high"}); !slices.Equal(got, []string{"low", "high"}) {
		t.Errorf("a set on a single-valued field = %v, want the whole set", got)
	}
	multi := CustomField{Name: "areas", Type: CustomFieldEnum, Options: []string{"ui", "daemon"}, Multiple: true}
	if got := multi.LegacyValues([]string{"ui", "daemon"}); len(got) != 0 {
		t.Errorf("a set on a field that holds one = %v, want nothing", got)
	}
	if got := multi.LegacyValues([]string{"ui", "sandbox"}); !slices.Equal(got, []string{"sandbox"}) {
		t.Errorf("one retired option inside a set = %v, want only that one", got)
	}
}

func TestLegacyCustomFieldValuesReadsAWholeTask(t *testing.T) {
	t.Parallel()
	fields := []CustomField{
		{Name: "priority", Type: CustomFieldEnum, Options: []string{"low", "high"}},
		{Name: "customer"},
	}
	got := LegacyCustomFieldValues(fields, map[string][]string{
		"priority": {"critical"},
		"customer": {"acme"},
		// A field nothing declares any more: every value it holds is
		// stranded, because nothing is left to render or offer one.
		"retired": {"whatever"},
	})
	if !slices.Equal(got["priority"], []string{"critical"}) {
		t.Errorf("priority = %v, want the retired option", got["priority"])
	}
	if len(got["customer"]) != 0 {
		t.Errorf("customer = %v, want nothing: text takes anything", got["customer"])
	}
	if !slices.Equal(got["retired"], []string{"whatever"}) {
		t.Errorf("an undeclared field = %v, want everything it holds", got["retired"])
	}
	if got := LegacyCustomFieldValues(fields, map[string][]string{"priority": {"low"}}); got != nil {
		t.Errorf("a task carrying nothing stranded = %v, want nil", got)
	}
}
