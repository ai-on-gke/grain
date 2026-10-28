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

package cli

// customfields.go is `-field`, the flag that puts a value on one of this
// deployment's own declared fields (model.CustomField) from a terminal:
// `grain create -field priority=high`, `grain update 12 -field
// areas=ui,daemon`.
//
// grain/task-167. Every other writer could already set one -- the
// new-task form and the detail pane, a schedule and a template's own
// values, `propose_task`'s custom_fields -- and `grain list` could
// narrow and sort by them (grain/task-99), but nothing typed at a shell
// could *write* one, so a task filed from a terminal, by a schedule's
// own prompt or by a scripted triage pass said nothing about the very
// columns a deployment reads its backlog by.
//
// The declarations themselves stay where they are: a pull request
// against the state repository's tables/custom_field.json (model.
// CustomField's own doc comment). This is only about the values a task
// carries.

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// customFieldFlag is what one -field is written as: name=value, the
// spelling `-write owner/name@branch` and `-filter attr:op:value`
// already give a flag carrying two halves -- one flag rather than a
// pair, because a CLI has no way to pair repeated flags.
//
// The "=" is required, and that is what makes clearing a value sayable
// without a flag of its own: `-field priority=` names the field and no
// value, which is the empty value the API already reads as "empty this
// one" (model.NormalizeCustomFieldValues), while a field no -field
// mentions is one this command says nothing about. A `-clear-field`
// would be a second spelling for the shorter of those two, and PATCH
// only has to tell them apart because a *form* sends every field at
// once; nothing typed here ever does.
//
// A field declared to hold a set (model.CustomField.Multiple) takes
// several values either way round: the flag repeated, or one flag
// carrying a comma-separated list. The split happens only on such a
// field, out of the declaration rather than out of the text, so that a
// comma in a one-value text field ("Acme, Inc.") stays part of the
// value rather than turning into a second one the field would then
// refuse. A set's own value that has to carry a comma is written %2C,
// the escaping a -filter condition already reads (unescapeValue).
type customFieldFlag struct {
	name  string
	value string
	// raw is the flag as it was typed, so an error can quote it back.
	raw string
}

// customFieldFlagUsage is the -field flag's own help line, worded the
// same on both commands that take it.
const customFieldFlagUsage = "name=value for one of the fields this deployment declares (repeatable; " +
	"\"name=\" clears it, and a field holding a set takes the flag more than once or a comma-separated list)"

// parseCustomFieldFlags cuts each -field into its two halves, keeping
// the order they were typed so that a command line with two mistakes in
// it is reported on the first of them every time.
func parseCustomFieldFlags(flags []string) ([]customFieldFlag, error) {
	out := make([]customFieldFlag, 0, len(flags))
	for _, raw := range flags {
		name, value, ok := strings.Cut(raw, "=")
		if !ok {
			return nil, fmt.Errorf("-field %s names no value: write it name=value, or name= to clear the field", raw)
		}
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("-field %s names no field: write it name=value", raw)
		}
		out = append(out, customFieldFlag{name: strings.TrimSpace(name), value: value, raw: raw})
	}
	return out, nil
}

// customFieldValues is what a -field-carrying command sends: the values
// the flags state, checked against what this deployment declares before
// the request is made rather than after it is refused.
//
// The declarations are fetched once, and only when there is a -field to
// check -- the same bargain `grain list` strikes with them (list.go's
// own fetch): an ordinary create or update costs no extra round trip,
// and a command that does name a field is answered with the names and
// the values that deployment really has rather than with a 400 from the
// far end. They come back alongside the values because an *edit* needs
// them for a second reason (mergedCustomFields).
//
// Checking here is not a second, looser gate: the values go through
// model.CustomField's own NormalizeValues, which is what the server
// runs too, so an enum, a date, a number and a single-valued field
// handed two values are refused in the same words on either side of the
// wire.
func customFieldValues(ctx context.Context, c *ui.HTTPClient, flags []string) (
	map[string]ui.CustomFieldValue, []ui.CustomFieldDeclaration, error) {
	if len(flags) == 0 {
		return nil, nil, nil
	}
	stated, err := parseCustomFieldFlags(flags)
	if err != nil {
		return nil, nil, err
	}
	declared, err := c.CustomFields(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("asking which fields this deployment declares: %w", err)
	}
	byName := make(map[string]ui.CustomFieldDeclaration, len(declared))
	for _, f := range declared {
		byName[f.Name] = f
	}
	// Gathered per field first, so a field named twice is one field
	// holding two values rather than the second flag overwriting the
	// first -- which is how a set is given without a comma in it.
	written := map[string][]string{}
	var order []string
	values := map[string]ui.CustomFieldValue{}
	for _, f := range stated {
		declaration, ok := byName[f.name]
		if !ok {
			// A field nothing declares is refused -- unless this is a
			// clear, which stays sayable about a declaration that has
			// been deleted from the state repository: the values it
			// left on tasks are the one thing here that nothing can
			// describe and nobody could otherwise remove
			// (model.NormalizeCustomFieldValues' own exception).
			if strings.TrimSpace(f.value) == "" {
				values[f.name] = ui.CustomFieldValue{}
				continue
			}
			return nil, nil, fmt.Errorf("-field %s: %w", f.raw, undeclaredFieldError(f.name, declared))
		}
		if _, seen := written[f.name]; !seen {
			order = append(order, f.name)
		}
		written[f.name] = append(written[f.name], customFieldWritten(declaration, f.value)...)
	}
	for _, name := range order {
		v, err := declaredField(byName[name]).NormalizeValues(written[name])
		if err != nil {
			return nil, nil, fmt.Errorf("-field %s: %w", name, err)
		}
		// Nothing left after normalization is a clear: `-field
		// priority=` and `-field priority="  "` say the same thing, the
		// way an empty value does everywhere else here.
		values[name] = ui.CustomFieldValue(v)
	}
	return values, declared, nil
}

// customFieldWritten is one -field's value as the values it holds: one
// value for an ordinary field, and for a field declared to hold a set,
// however many the comma-separated list carries -- see customFieldFlag's
// own doc comment on why the declaration decides that and not the text.
func customFieldWritten(f ui.CustomFieldDeclaration, value string) []string {
	if !f.Multiple {
		return []string{value}
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		out = append(out, unescapeValue(part))
	}
	return out
}

// declaredField is a declaration as the model type that knows how to
// check a value against it. Only the half that decides what a value may
// be is carried over: the type, its options and whether it holds a set,
// because those are what NormalizeValues reads, and the label, because
// it is what an error calls the field.
func declaredField(f ui.CustomFieldDeclaration) model.CustomField {
	return model.CustomField{
		Name:     f.Name,
		Label:    f.Label,
		Type:     model.CustomFieldType(f.Type),
		Options:  f.Options,
		Multiple: f.Multiple,
	}
}

// undeclaredFieldError is what a name nothing declares is refused with:
// the names that would have worked, since a terminal has nowhere else
// to look them up, and where they come from when there are none at all
// -- a deployment that has never declared one is not somebody's typo,
// it is somebody who needs a pull request against its state repository.
func undeclaredFieldError(name string, declared []ui.CustomFieldDeclaration) error {
	if len(declared) == 0 {
		return fmt.Errorf("this deployment declares no custom fields at all, so there is no %q to set -- "+
			"they are declared in its state repository, in tables/custom_field.json, not from here", name)
	}
	names := make([]string, 0, len(declared))
	for _, f := range declared {
		names = append(names, f.Name)
	}
	return fmt.Errorf("this deployment declares no field %q: one of %s", name, strings.Join(names, ", "))
}

// mergedCustomFields is what an *edit* has to send: PATCH /api/tasks
// replaces a task's values for every declared field rather than merging
// into them (ui.UpdateTaskRequest.CustomFields), because the pane that
// edits them renders every field at once and one it left blank is one
// somebody cleared. A command line is the opposite -- it names the one
// field it means and nothing else -- so this re-states the rest, out of
// the task as it stands, and the request is the whole answer either way.
//
// What the task carries for a field nothing declares any more is left
// out rather than re-stated: the server keeps such a value untouched
// precisely because nothing can re-state it (ui.mergeCustomFields), and
// sending one back would be refused as a name nobody declares.
//
// A value the task carries that its own declaration would no longer
// accept -- an enum option dropped from tables/custom_field.json since
// somebody chose it, or a set left behind by a field that has stopped
// holding one -- is re-stated with everything else, unchanged and
// whole. The server takes a restatement of what a task already holds as
// saying nothing new about it (model.NormalizeCustomFieldValuesFrom),
// so `grain update 12 -field priority=high` neither fails over a
// retired customer value nor throws it away. Changing one is still said
// the way anything else here is said: -field name=value to replace it,
// -field name= to clear it.
func mergedCustomFields(task ui.Task, stated map[string]ui.CustomFieldValue,
	declared []ui.CustomFieldDeclaration) map[string]ui.CustomFieldValue {
	byName := make(map[string]ui.CustomFieldDeclaration, len(declared))
	for _, f := range declared {
		byName[f.Name] = f
	}
	merged := make(map[string]ui.CustomFieldValue, len(task.CustomFields)+len(stated))
	// Sorted for no reason the request can see: a map would do, and a
	// stable order makes the body this builds the same on every run,
	// which is one less thing to explain when two of them differ.
	for _, name := range slices.Sorted(maps.Keys(task.CustomFields)) {
		if _, said := stated[name]; said {
			continue
		}
		if _, ok := byName[name]; !ok {
			continue
		}
		merged[name] = task.CustomFields[name]
	}
	maps.Copy(merged, stated)
	return merged
}

// updatedCustomFields is `grain update`'s whole side of this: the values
// its -field flags state, merged into the ones the task already carries,
// or nil for an edit that names no field at all -- which is an edit that
// must say nothing about them, since saying an empty map would clear
// every one.
//
// It reads the task before it edits it, which is one extra request and
// only on an edit that names a field. There is no PATCH that touches one
// field and leaves the others (that field's own doc comment on why the
// API replaces the set), so the alternative is not a cheaper request but
// a wrong one: `grain update 12 -field priority=high` would silently
// clear the customer somebody had recorded on task 12.
func updatedCustomFields(ctx context.Context, c *ui.HTTPClient, id string, flags []string) (
	map[string]ui.CustomFieldValue, error) {
	stated, declared, err := customFieldValues(ctx, c, flags)
	if err != nil {
		return nil, err
	}
	if stated == nil {
		return nil, nil
	}
	task, err := c.Task(ctx, id)
	if err != nil {
		return nil, err
	}
	return mergedCustomFields(task, stated, declared), nil
}

// declaredFieldHelp is the half of -field only the deployment knows: the
// fields it declares, what each of them holds, and what a human calls
// it. Printed after the built-in usage by `grain create -h` and `grain
// update -h`, exactly as `grain list -h` prints the same declarations as
// the attributes they are (list.go's declaredHelp) -- a terminal has
// nowhere else to find out that this deployment has a priority field, or
// which options it offers.
//
// Empty when the deployment declares none, and empty when it cannot be
// asked: help is not worth failing over, and every flag it describes
// goes on working.
func declaredFieldHelp(ctx context.Context, c *ui.HTTPClient) string {
	fields, err := c.CustomFields(ctx)
	if err != nil || len(fields) == 0 {
		return ""
	}
	width := 0
	for _, f := range fields {
		if len(f.Name) > width {
			width = len(f.Name)
		}
	}
	var b strings.Builder
	b.WriteString("\nFields this deployment declares, which -field takes name=value for:\n")
	for _, f := range fields {
		fmt.Fprintf(&b, "  %-*s  %s\n", width, f.Name, declaredFieldSummary(f))
	}
	return b.String()
}

// declaredFieldSummary is one declaration on one line: what it holds --
// an enum's options rather than the word "enum", since the options are
// the part somebody about to type a value needs -- and what the
// deployment calls it, when that is not the name itself.
func declaredFieldSummary(f ui.CustomFieldDeclaration) string {
	held := f.Type
	if held == "" {
		held = string(model.CustomFieldText)
	}
	if len(f.Options) > 0 {
		held = strings.Join(f.Options, " | ")
	}
	if f.Multiple {
		held = "any of: " + held
	}
	if f.Label != "" && f.Label != f.Name {
		held += "  (" + f.Label + ")"
	}
	return held
}
