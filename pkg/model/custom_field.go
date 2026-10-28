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

package model

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A custom field is a column this deployment adds to its own tasks:
// "priority", "customer", "due date" -- whatever the work here is
// actually sorted by, which grain cannot know and should not guess
// (grain/task-53).
//
// The declaration is settings, not task data. It lives in the state
// repository like every other setting (staterepo.SettingsTables names
// `custom_field`), which is what makes adding one an agent's job rather
// than an operator's: a pull request against tables/custom_field.json,
// reviewed and merged, is live on the next tick. Nothing in this
// package writes a declaration for itself, and there is deliberately no
// UI that does either -- the state repository is the one place the set
// of fields is decided, so that what a deployment carries is a diff
// somebody approved rather than a form somebody filled in and forgot.
//
// The *values* are ordinary task data (Task.CustomFields, the
// `task_custom_field` table): written by whoever files or edits the
// task, exported with the task, and meaningless to grain -- it never
// dispatches, orders or filters on one. They ride into a run's prompt
// (orchestrator.BuildPrompt), which is the whole point: a field says
// something about the work that the title and the body would otherwise
// have to say in prose, in a form the next task can be filed carrying
// too.
//
// "Whoever files the task" is not only a human at a form. A schedule
// and a template carry values of their own (Schedule.CustomFields,
// Template.CustomFields) and put them on everything they file, and an
// agent can ask for them on the work it proposes (propose_task's
// custom_fields, orchestrator.relayProposedTasks) -- grain/task-105,
// because most of what gets filed here is filed by grain itself, and a
// field only a human could ever write would be a column that is empty
// on exactly those tasks. Every one of those writers is normalized by
// this file and no other: NormalizeCustomFieldValues where there is
// somebody to hand an error to, FilterCustomFieldValues where there is
// not.

// CustomFieldType is the shape of one field's values -- what a form
// offers to enter it with, and what NormalizeValue will accept.
//
// TEXT in the schema rather than a constrained column, for the reason
// schema.go's own doc comment gives about every other enum here: a new
// type ships without a migration, and Validate is what rejects one this
// build has never heard of.
type CustomFieldType string

const (
	// CustomFieldText is a single line of free text, and the default:
	// a field whose type is left empty is one of these, so the smallest
	// declaration anybody can write ({"name": "customer"}) means the
	// obvious thing.
	CustomFieldText CustomFieldType = "text"
	// CustomFieldNumber is a decimal number, stored as the text it was
	// written as (see NormalizeValue) rather than as a float: "3.50" and
	// "3.5" are the same number and different answers, and nothing here
	// does arithmetic on one. What may be written is what a number input
	// will hold -- customFieldNumber, which is narrower than Go's own
	// idea of a float.
	CustomFieldNumber CustomFieldType = "number"
	// CustomFieldEnum is one of Options, which such a field must have at
	// least one of.
	CustomFieldEnum CustomFieldType = "enum"
	// CustomFieldDate is a calendar date, CustomFieldDateLayout --
	// deliberately a date and not a timestamp: a due date, a review
	// date, a launch. A field that wants a moment can say so in text
	// until something needs more than this.
	CustomFieldDate CustomFieldType = "date"
	// CustomFieldBool is a checkbox: "true" or "false" (see
	// NormalizeValue for what else is accepted on the way in).
	CustomFieldBool CustomFieldType = "boolean"
)

// CustomFieldDateLayout is how a CustomFieldDate value is written --
// ISO 8601's calendar date, which is what <input type="date"> both
// produces and reads, and what sorts correctly as a string.
const CustomFieldDateLayout = "2006-01-02"

// CustomFieldTypes is every type this build offers, in the order a
// picker should list them. The frontend renders an input per type and
// `grain state check` rejects a declaration naming anything else, so
// this list is the vocabulary in one place.
func CustomFieldTypes() []CustomFieldType {
	return []CustomFieldType{
		CustomFieldText,
		CustomFieldNumber,
		CustomFieldEnum,
		CustomFieldDate,
		CustomFieldBool,
	}
}

// customFieldName is what a field may be called: lower-case, digits,
// dash and underscore, starting with a letter or a digit.
//
// It is narrow on purpose. The name is a key -- in the store, in the
// JSON on the wire, and in the query string of the link a task detail
// offers (ui/src/paths.js's `field.<name>=`) -- so it has to survive
// being written down in all three without quoting, and it should read
// the same in each. The label is where the spaces and the capital
// letters go.
var customFieldName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// customFieldNumber is what a CustomFieldNumber value may look like:
// HTML's own "valid floating-point number", which is what
// <input type="number"> will hold and hand back.
//
// A regexp rather than strconv.ParseFloat alone because ParseFloat is a
// good deal more generous than the control that has to render the
// result: it takes "NaN", "Inf", "+3", "1_000" and hexadecimal floats
// like "0x1p-2", none of which a number input can show -- its value
// sanitization blanks anything that is not a valid floating-point
// number, so such a value arrives in the form as an empty box, and a
// default spelled that way is a default nobody ever sees. The
// comparisons a filter makes on a stored number read it no more kindly
// (cmd/grain's matchesNumber, ui/src/taskFilters.js): "NaN" compares
// false against everything, including itself.
//
// ParseFloat still runs after this, for the one thing the shape cannot
// say: whether the number is in range ("1e999" is spelled correctly and
// is not a number float64 can hold).
var customFieldNumber = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?([eE][-+]?[0-9]+)?$`)

// CustomField is one declared field.
type CustomField struct {
	// Name is the key, unique across the deployment, and the primary key
	// of the row. Renaming one is deleting a field and declaring another:
	// the values already on tasks are keyed by this.
	Name string
	// Label is what a human reads -- "Due date" for `due-date`. Empty
	// means the name itself is good enough to show (DisplayName), which
	// it usually is.
	Label string
	// Type is the shape of the values. Empty means CustomFieldText, so
	// that the least anybody can declare is a name.
	Type CustomFieldType
	// Options is a CustomFieldEnum's choices, in the order a picker
	// offers them -- the declared order, not sorted, because "low,
	// medium, high" is not alphabetical and whoever wrote it down meant
	// that order. Ignored, and rejected by Validate, on every other
	// type.
	Options []string
	// Multiple makes this field hold a set of values rather than one:
	// "components", "affected customers", a deployment's own tags. It is
	// a flag on the existing types rather than a type of its own
	// (CustomFieldTypes gains nothing, and neither does a picker) because
	// how many values a field holds and what one value looks like are two
	// separate questions -- an enum of one option and an enum of several
	// offer the same choices, checked the same way, and only differ in
	// how many of them a task may pick.
	//
	// Text and enum only, which Validate is what enforces. A boolean that
	// held a set would be a set of at most two values nobody has a use
	// for, and neither a number nor a date has an obvious input or an
	// obvious meaning as a set -- a field that wants "the dates this
	// touches" can say so in multi-valued text until something needs
	// more than that.
	//
	// The values are a *set*, not a list: duplicates collapse, and the
	// order they were entered in is not kept (NormalizeValues, and
	// task_custom_field's own primary key). "Which tasks mention acme?"
	// is the question a multi-valued field is asked -- the frontend's
	// own comparisons for one are "is any of", "is none of" and "has all
	// of" (ui/src/taskAttributes.js's TYPE_TAGS) -- and none of those
	// have an opinion about order.
	Multiple bool
	// Description is the one line under the input saying what the field
	// is for. Optional, and worth writing: whoever fills it in next is
	// not whoever declared it.
	Description string
	// Default is the value a form offers before anybody types -- and
	// only that. It is never applied to a task by the store: a task
	// filed by an agent, by a schedule or through the API without naming
	// this field carries no value for it, which is the honest answer
	// ("nobody said") rather than a value nobody chose.
	//
	// One value even on a Multiple field, where it is the single value
	// the form starts holding: a default is what a form offers before
	// anybody types, and a whole set nobody chose is a good deal more
	// than that.
	Default string
	// Position orders the fields on a form and in a task's detail pane,
	// lowest first, with the name breaking ties -- so a declaration that
	// states no position at all still lists in a stable, obvious order.
	Position int
	// ShowOnRow says this field's value belongs on every row and card
	// that lists a task, not only in the task's own pane: the priority
	// beside the title, rather than one click away from it.
	//
	// It is the declaration's half of that question, and there is a
	// second half the frontend answers on its own -- a list *sorted* by a
	// field shows that field whether or not it is flagged, because an
	// order the reader cannot see is the one case where the value has to
	// be there (ui/src/components/CustomFields.jsx's rowFields). This
	// flag is for the field that belongs there always: the one this
	// deployment reads its backlog by.
	//
	// A flag rather than a count or an order of its own because the row
	// has a width budget and the deployment cannot see it from here: a
	// declaration says which fields are worth the space, and the
	// frontend, which knows how much space there is, shows as many of
	// them as fit (ROW_FIELD_LIMIT beside rowFields). So flagging six
	// fields is not an error -- it is a preference the row honours as
	// far as it can, in Position order, which is why nothing here
	// rejects it.
	//
	// Off by default, so every declaration written before this column
	// existed keeps the rows it already had.
	ShowOnRow bool
}

// DisplayName is what to show for this field: its Label, or its Name
// when it has none.
func (f CustomField) DisplayName() string {
	if strings.TrimSpace(f.Label) != "" {
		return f.Label
	}
	return f.Name
}

// FieldType is Type with the empty-means-text default applied, so
// callers switching on the type never have to spell that rule again.
func (f CustomField) FieldType() CustomFieldType {
	if f.Type == "" {
		return CustomFieldText
	}
	return f.Type
}

// Validate reports whether this declaration is one grain can actually
// offer -- which is what `grain state check` runs over a proposed
// tables/custom_field.json before it merges, so a field that would be
// unusable fails the pull request rather than the deployment.
func (f CustomField) Validate() error {
	if !customFieldName.MatchString(f.Name) {
		return fmt.Errorf("custom field name %q: must be lower-case letters, digits, %q or %q, "+
			"starting with a letter or a digit -- the label is where spaces and capitals go",
			f.Name, "-", "_")
	}
	known := false
	for _, t := range CustomFieldTypes() {
		if f.FieldType() == t {
			known = true
			break
		}
	}
	if !known {
		return fmt.Errorf("custom field %q: unknown type %q (one of %s)",
			f.Name, f.Type, customFieldTypeList())
	}
	if f.FieldType() == CustomFieldEnum {
		if len(f.Options) == 0 {
			return fmt.Errorf("custom field %q: an enum with no options is a field nobody can fill in", f.Name)
		}
		seen := map[string]bool{}
		for _, o := range f.Options {
			if strings.TrimSpace(o) == "" {
				return fmt.Errorf("custom field %q: an empty option is not a choice", f.Name)
			}
			// An option is offered by a picker exactly as it is spelled
			// here, and a value is trimmed on the way in
			// (NormalizeValue) -- so " high" is a choice that can be
			// shown and then never stored, and " high" beside "high" is
			// two choices that read identically and are one value.
			// Requiring the canonical spelling is what makes those two
			// failures impossible rather than confusing, and it is why
			// the duplicate check below can compare the option as
			// written.
			if o != strings.TrimSpace(o) {
				return fmt.Errorf("custom field %q: option %q is written with space around it, and a "+
					"value is trimmed before it is compared -- spell it %q",
					f.Name, o, strings.TrimSpace(o))
			}
			if seen[o] {
				return fmt.Errorf("custom field %q: option %q is listed twice", f.Name, o)
			}
			seen[o] = true
		}
	} else if len(f.Options) > 0 {
		return fmt.Errorf("custom field %q: options only mean something on an enum, not on a %s",
			f.Name, f.FieldType())
	}
	if f.Multiple && !multiValued[f.FieldType()] {
		return fmt.Errorf("custom field %q: a %s holds one value, not a set -- multiple is for %s and %s",
			f.Name, f.FieldType(), CustomFieldText, CustomFieldEnum)
	}
	// The default has to be the *stored* spelling of a value, not merely
	// something NormalizeValue could make one of. A form hands it
	// straight to the control (ui/src/components/CustomFields.jsx's
	// initialFieldValues), and a control only shows a value written the
	// way it writes one: a picker offers "high" and not " high", a date
	// input shows "2026-01-02" and blanks anything else, a boolean is
	// "true" or "false" and not "yes". A default the control cannot show
	// opens the form on an empty box, which is not a default at all --
	// and one a browser refuses is worse, because the form cannot be
	// submitted until somebody works out what is wrong with a field they
	// never touched.
	if f.Default != "" {
		canonical, err := f.NormalizeValue(f.Default)
		switch {
		case err != nil:
			return fmt.Errorf("custom field %q: its default is not a valid value: %w", f.Name, err)
		case canonical == "":
			return fmt.Errorf("custom field %q: its default %q is nothing but space -- "+
				"a field with no default says so by leaving it out", f.Name, f.Default)
		case canonical != f.Default:
			return fmt.Errorf("custom field %q: its default %q is not how a %s value is written -- "+
				"spell it %q, which is what the form would show and what would be stored",
				f.Name, f.Default, f.FieldType(), canonical)
		}
	}
	return nil
}

// multiValued is the types CustomField.Multiple may be set on -- see
// that field's own doc comment on why a boolean, a number and a date
// are not among them.
var multiValued = map[CustomFieldType]bool{
	CustomFieldText: true,
	CustomFieldEnum: true,
}

func customFieldTypeList() string {
	names := make([]string, 0, len(CustomFieldTypes()))
	for _, t := range CustomFieldTypes() {
		names = append(names, string(t))
	}
	return strings.Join(names, ", ")
}

// NormalizeValue checks one value against this field's type and returns
// what should be stored for it: the canonical spelling, so that "TRUE"
// and "true" are one value in the store and a date is always written
// the one way.
//
// What each type accepts is what the control that enters it accepts,
// deliberately and not by coincidence: a number is a number
// <input type="number"> will hold rather than anything Go can parse as
// a float, and a date is one <input type="date"> can show rather than
// any year Go's calendar has. A value this takes and a browser blanks
// is a value that can be stored, shown as an empty box on every form
// that edits the task, and silently dropped the next time anybody saves
// it.
//
// An empty value (after trimming) is "this task says nothing about this
// field" and is always allowed -- there is no required field here on
// purpose. A field an agent declared today would otherwise make every
// task filed before it invalid, and a good half of what grain files it
// files itself (a schedule firing, a review, a suite run) with no form
// in sight and nobody to ask.
func (f CustomField) NormalizeValue(value string) (string, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", nil
	}
	switch f.FieldType() {
	case CustomFieldNumber:
		if !customFieldNumber.MatchString(v) {
			return "", fmt.Errorf("%q is not a number: digits, optionally a decimal point and more "+
				"digits, optionally an exponent -- what a number input will hold", value)
		}
		if _, err := strconv.ParseFloat(v, 64); err != nil {
			return "", fmt.Errorf("%q is not a number this can store: it is outside the range of "+
				"the numbers a form and a filter can read back", value)
		}
		return v, nil
	case CustomFieldEnum:
		for _, o := range f.Options {
			if o == v {
				return o, nil
			}
		}
		return "", fmt.Errorf("%q is not one of %s", value, strings.Join(f.Options, ", "))
	case CustomFieldDate:
		day, err := time.Parse(CustomFieldDateLayout, v)
		if err != nil {
			return "", fmt.Errorf("%q is not a date of the form %s", value, CustomFieldDateLayout)
		}
		// Year zero parses here and is not a date anywhere else: HTML's
		// date string wants a year "greater than zero", so
		// <input type="date"> blanks "0000-01-02" rather than showing
		// it, and the calendar it stands in has no year 0 either.
		if day.Year() < 1 {
			return "", fmt.Errorf("%q is not a date of the form %s: the year has to be 0001 or later",
				value, CustomFieldDateLayout)
		}
		return v, nil
	case CustomFieldBool:
		switch strings.ToLower(v) {
		case "true", "yes", "on", "1":
			return "true", nil
		case "false", "no", "off", "0":
			return "false", nil
		}
		return "", fmt.Errorf("%q is not true or false", value)
	default:
		return v, nil
	}
}

// NormalizeValues is NormalizeValue over the whole of what one task says
// about one field: every value canonicalized, every empty one dropped,
// and -- on a Multiple field -- duplicates collapsed and the result
// sorted, because what such a field holds is a set (CustomField.Multiple)
// and a set has no order to preserve.
//
// A single-valued field handed more than one value is an error rather
// than a value quietly picked from among them: "priority: high, low" is
// a caller that has misunderstood the field, and answering it with
// whichever came first would put a value on the task that nobody asked
// for. One value, or none, is what such a field means.
//
// nil for a field a task says nothing about, so that "cleared" and
// "never set" stay the one state they are everywhere else here.
func (f CustomField) NormalizeValues(values []string) ([]string, error) {
	return f.NormalizeValuesFrom(values, nil)
}

// NormalizeValuesFrom is NormalizeValues for an *edit*: the same check,
// plus the one thing an edit may say that a filing may not -- "this is
// what was already here".
//
// had is what the thing being edited currently holds for this field. A
// value among those is accepted as it stands even when the declaration
// would no longer produce it, and a single-valued field is allowed to
// go on holding the set it was left with when its declaration stopped
// saying Multiple. That is the grandfather half of the policy this
// file's own doc comment states: a declaration that changed under a
// stored value must not make every other edit of that task fail, and
// must not make an edit that re-states it quietly throw it away.
//
// What it does not allow is *introducing* one. A legacy value is only
// ever grandfathered onto the task that already carries it, so an edit
// can keep one, clear one, or replace it with a value the declaration
// really accepts -- and a set that has grown, or a value nobody had,
// is refused exactly as it is on the way in.
func (f CustomField) NormalizeValuesFrom(values, had []string) ([]string, error) {
	legacy := map[string]bool{}
	for _, v := range had {
		if v := strings.TrimSpace(v); v != "" {
			legacy[v] = true
		}
	}
	var out []string
	seen := map[string]bool{}
	grandfathered := 0
	for _, value := range values {
		v, err := f.NormalizeValue(value)
		if err != nil {
			// Unchanged is not an error. The raw value is compared,
			// trimmed, because a value the declaration cannot normalize
			// is stored as whatever it was normalized to back when it
			// could be -- so it is already canonical for the
			// declaration that accepted it.
			if raw := strings.TrimSpace(value); legacy[raw] {
				v = raw
			} else {
				return nil, err
			}
		}
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		if legacy[v] {
			grandfathered++
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, nil
	}
	if !f.Multiple && len(out) > 1 {
		// A set left behind by a field that has stopped being Multiple
		// stays on the task while it is restated whole, and while it is
		// pared down: every step of "delete a chip, save" states a
		// subset of what was already there, so a pane can walk such a
		// task back to one value without a single request being
		// refused. Adding to it cannot, which is what the count checks
		// -- "priority: high, low" from a form that has misunderstood
		// the field is the error it always was.
		if grandfathered < len(out) {
			return nil, fmt.Errorf("takes a single value, and %d were given (%s)",
				len(out), strings.Join(out, ", "))
		}
		sort.Strings(out)
		return out, nil
	}
	if f.Multiple {
		sort.Strings(out)
	}
	return out, nil
}

// Offers says whether this declaration could still produce one value:
// whether NormalizeValue takes it and hands back exactly what it was
// given. That second half is what makes it a question about the
// declaration rather than about the store -- a boolean field takes
// "yes" and stores "true", so "yes" is a value this field accepts and
// not one it offers, and a task carrying it is carrying something no
// form on this deployment would produce today.
//
// An empty value is offered by every field: "nobody said" is what every
// field's absence means (NormalizeValue), so it is never a leftover.
//
// It is the Go half of ui/src/taskAttributes.js's isOffered, which is
// what a form asks so it can draw a value somebody picked differently
// from one its declaration has moved on from. Written against
// NormalizeValue rather than against a second copy of each type's rules
// so the two cannot answer differently about the same value.
func (f CustomField) Offers(value string) bool {
	v := strings.TrimSpace(value)
	if v == "" {
		return true
	}
	canonical, err := f.NormalizeValue(v)
	return err == nil && canonical == v
}

// LegacyValues is what one task's values for this field hold that the
// declaration has moved on from: the values it no longer offers, and --
// on a field that has stopped being Multiple -- the whole set, since
// each value may be fine while there being two of them is not.
//
// Empty for the ordinary case, which is what every caller checks: this
// answers "is there anything here a declaration has stranded", which is
// the question a form draws a notice from (LEGACY_NOTICE, ui/src/
// components/CustomFields.jsx, whose legacyValues this is the Go half
// of) and the question a narrowing asks per task (`grain list -filter
// custom.priority:legacy`).
//
// It is deliberately not the finer question "which of these values is
// the stranded one", which is what a report of what a proposed
// declaration would strand wants instead: on a field that has stopped
// holding a set, the answer to that is "none of them, there are simply
// two" (Store.LegacyCustomFieldUses splits the two apart).
func (f CustomField) LegacyValues(values []string) []string {
	held := make([]string, 0, len(values))
	for _, v := range values {
		if v := strings.TrimSpace(v); v != "" {
			held = append(held, v)
		}
	}
	if len(held) == 0 {
		return nil
	}
	if !f.Multiple && len(held) > 1 {
		return held
	}
	var out []string
	for _, v := range held {
		if !f.Offers(v) {
			out = append(out, v)
		}
	}
	return out
}

// LegacyCustomFieldValues is CustomField.LegacyValues over everything
// one task carries: the values, keyed by field name, that no
// declaration in `fields` would produce today.
//
// A field nothing declares at all has every value it holds returned,
// which is the extreme of the same thing -- a declaration deleted from
// the state repository leaves the values it described exactly where
// they are (NormalizeCustomFieldValues on why nothing walks the tasks
// to strip them), and those are values nothing on this deployment can
// render, offer or explain.
//
// nil when the task carries nothing stranded, so a caller can ask
// "anything?" of the result itself.
func LegacyCustomFieldValues(fields []CustomField, values map[string][]string) map[string][]string {
	if len(values) == 0 {
		return nil
	}
	byName := make(map[string]CustomField, len(fields))
	for _, f := range SortCustomFields(slices.Clone(fields)) {
		if _, exists := byName[f.Name]; !exists {
			byName[f.Name] = f
		}
	}
	var out map[string][]string
	for _, name := range sortedKeys(values) {
		f, declared := byName[name]
		var legacy []string
		if declared {
			legacy = f.LegacyValues(values[name])
		} else {
			// Everything, since there is no declaration left to offer
			// any of it -- not CustomField.LegacyValues against an
			// invented declaration, which would read an undeclared
			// field as the text field a bare name means and hand back
			// nothing at all.
			legacy = nonEmptyValues(values[name])
		}
		if len(legacy) == 0 {
			continue
		}
		if out == nil {
			out = map[string][]string{}
		}
		out[name] = legacy
	}
	return out
}

// nonEmptyValues is the values that say something, trimmed -- the same
// reading of "a blank is nothing" every normalizer here starts with.
func nonEmptyValues(values []string) []string {
	var out []string
	for _, v := range values {
		if v := strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// SortCustomFields orders a set of declarations the way every listing of
// them should read: Position first, then Name. In place, and returned
// for convenience.
func SortCustomFields(fields []CustomField) []CustomField {
	sort.SliceStable(fields, func(i, j int) bool {
		if fields[i].Position != fields[j].Position {
			return fields[i].Position < fields[j].Position
		}
		return fields[i].Name < fields[j].Name
	})
	return fields
}

// NormalizeCustomFieldValues checks a whole set of values against the
// fields this deployment declares, and returns what to store: every
// value canonicalized by its own field, and every empty one dropped, so
// that "cleared" and "never set" are one state in the store rather than
// two that look different in a dump and mean the same thing.
//
// The values for one field are a slice because a declaration may say a
// field holds a set of them (CustomField.Multiple). A single-valued
// field -- which is nearly all of them -- takes a slice of one, and is
// refused a slice of more (NormalizeValues).
//
// A value naming a field nobody declared is an error rather than
// something quietly kept: a typo'd key would otherwise sit on the task
// for ever, shown nowhere -- every reader renders by declaration -- and
// found by nobody. Naming an undeclared field with an *empty* value is
// the exception, and it is how a stale value is cleared: a field
// deleted from the state repository leaves its values where they are
// (nothing walks the tasks to strip them), so "remove this" has to stay
// sayable about a field this build can no longer describe.
//
// This is the filing gate: nothing is already stored, so nothing is
// grandfathered. An *edit* has values in front of it that were written
// under whatever the declarations said at the time, and asks
// NormalizeCustomFieldValuesFrom below instead.
func NormalizeCustomFieldValues(fields []CustomField, values map[string][]string) (map[string][]string, error) {
	return NormalizeCustomFieldValuesFrom(fields, values, nil)
}

// NormalizeCustomFieldValuesFrom is NormalizeCustomFieldValues for an
// *edit* of something that already has values: had is what it holds
// now, keyed the same way, and a value already in there is one this
// edit may restate whatever the declarations have done since
// (CustomField.NormalizeValuesFrom).
//
// This is the whole of grain's answer to a declaration that changes
// under stored values, and it is worth stating in one place because
// there are three things it could have been:
//
//   - Walk the tasks when a declaration changes, rewriting or stripping
//     what no longer fits. Nothing here does that, and nothing should:
//     a merged pull request against tables/custom_field.json would
//     silently rewrite task data, an option removed by mistake would
//     take every value of it with it, and putting the option back would
//     not bring them back.
//   - Refuse the values, which is what the store did before
//     grain/task-64 and is what a task carrying one felt like: a
//     retired enum option made an unrelated edit -- a title, a repo, a
//     priority on a *different* field -- fail on a value nobody had
//     touched, because a form that renders every field restates every
//     field.
//   - Grandfather them, which is this. The values a task already holds
//     are things somebody recorded about it, and they stay recorded and
//     stay editable: an edit may restate one unchanged, may clear it
//     (the empty value, which has always meant "nobody said"), or may
//     replace it with a value the declaration does accept. Only
//     *introducing* one is refused, so a legacy value can never spread
//     from the task that has it to a task that does not -- and a
//     deployment that wants them gone clears them, one task or one bulk
//     edit at a time, rather than having them rewritten underneath it.
//
// The migration path that falls out of the third is the one a pane can
// actually offer: show the legacy value rather than an empty box
// (ui/src/components/CustomFields.jsx), let it be picked off, and let
// everything else about the task go on being edited meanwhile.
func NormalizeCustomFieldValuesFrom(fields []CustomField, values, had map[string][]string) (map[string][]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	byName := make(map[string]CustomField, len(fields))
	for _, f := range SortCustomFields(slices.Clone(fields)) {
		if _, exists := byName[f.Name]; !exists {
			byName[f.Name] = f
		}
	}
	out := map[string][]string{}
	// Sorted so a request naming two undeclared fields fails on the same
	// one every time, rather than on whichever the map handed over first.
	for _, name := range sortedKeys(values) {
		f, ok := byName[name]
		if !ok {
			// Clearing one is always allowed, declared or not: a value
			// whose field was deleted from the state repository is the
			// one thing a task can carry that nothing can describe, and
			// "remove it" has to stay sayable or it is on that task for
			// ever. Setting one is not -- see the doc comment above.
			if !anyNonEmpty(values[name]) {
				continue
			}
			// Restating what is already there is not setting one: a
			// caller that read the task, changed one field and sent the
			// map back is saying nothing new about this one, and
			// refusing it would be the same "an edit fails over a field
			// it never touched" this whole function exists to stop.
			// Undeclared is where that bites hardest, since nothing can
			// even describe the value being objected to.
			if kept := restated(values[name], had[name]); kept != nil {
				out[name] = kept
				continue
			}
			return nil, fmt.Errorf("no custom field %q is declared on this deployment", name)
		}
		v, err := f.NormalizeValuesFrom(values[name], had[name])
		if err != nil {
			return nil, fmt.Errorf("custom field %q: %w", f.DisplayName(), err)
		}
		if len(v) == 0 {
			continue
		}
		out[name] = v
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// restated is the values for an undeclared field when the edit says
// exactly what is already stored, and nil when it says anything else --
// including when nothing is stored, which is the ordinary "no such
// field" a filing gets. Order and duplicates are not compared, since
// nothing about a stored set keeps either (store.go reads them
// `ORDER BY name, value`); what is compared is which values are there.
func restated(values, had []string) []string {
	if len(had) == 0 {
		return nil
	}
	holds := make(map[string]bool, len(had))
	for _, v := range had {
		if v := strings.TrimSpace(v); v != "" {
			holds[v] = true
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, value := range values {
		v := strings.TrimSpace(value)
		if v == "" || seen[v] {
			continue
		}
		if !holds[v] {
			return nil
		}
		seen[v] = true
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

// FilterCustomFieldValues is NormalizeCustomFieldValues for a writer
// with nobody to tell: it returns the values that can still be set,
// canonicalized by exactly the same per-field NormalizeValues,
// alongside the names of the ones that cannot.
//
// Normalization is deliberately not a second, looser gate -- an agent's
// "TRUE" and a schedule's "TRUE" are stored as the human's "true", a
// number, a date or an enum choice is checked against the declaration
// the same way for all three, and a single-valued field refuses a set
// of two here exactly as it does at the API. The only difference is
// what happens to a value that fails: NormalizeCustomFieldValues
// returns an error, which
// is the right answer for an API request a human is waiting on and can
// fix, and this drops it, which is the right answer for a caller whose
// values were written down somewhere else and whose failure would
// otherwise take something bigger with it.
//
// Those callers are the ones with no form in sight. A schedule's values
// (Schedule.CustomFields) are applied afresh at every firing, and a
// field deleted from the state repository weeks after the schedule was
// written must not stop the chore from filing -- the schedule says
// nothing about that field any more, which is exactly what an empty
// value already means everywhere else. The same holds for a template's
// values on every firing of it, and for a value an agent asked for on a
// proposed task after its run is over.
//
// dropped is returned rather than swallowed so a caller that has
// somewhere to say it can (orchestrator.relayProposedTasks writes it
// into the proposal's body, beside its note about a depends_on that
// resolved to nothing). A firing has nowhere to put such a note -- a
// recurring task carrying the same apology every week is worse than the
// value being gone -- so it drops them, as the doc comments there say.
func FilterCustomFieldValues(fields []CustomField, values map[string][]string) (map[string][]string, []string) {
	if len(values) == 0 {
		return nil, nil
	}
	byName := make(map[string]CustomField, len(fields))
	for _, f := range SortCustomFields(slices.Clone(fields)) {
		if _, exists := byName[f.Name]; !exists {
			byName[f.Name] = f
		}
	}
	var kept map[string][]string
	var dropped []string
	// Sorted for the reason NormalizeCustomFieldValues sorts: two calls
	// on the same values should report the same names in the same order.
	for _, name := range sortedKeys(values) {
		f, ok := byName[name]
		if !ok {
			// An empty value for a field nobody declares is not a
			// dropped value, it is a caller saying nothing -- the same
			// reading NormalizeCustomFieldValues gives it.
			if anyNonEmpty(values[name]) {
				dropped = append(dropped, name)
			}
			continue
		}
		v, err := f.NormalizeValues(values[name])
		if err != nil {
			dropped = append(dropped, name)
			continue
		}
		if len(v) == 0 {
			continue
		}
		if kept == nil {
			kept = map[string][]string{}
		}
		kept[name] = v
	}
	return kept, dropped
}

// anyNonEmpty reports whether a set of values says anything at all --
// the "is this a clear?" question NormalizeCustomFieldValues asks about
// a field nothing declares any more. A set of nothing but blanks is a
// clear, exactly as a single blank was before a field could hold more
// than one value.
func anyNonEmpty(values []string) bool {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
