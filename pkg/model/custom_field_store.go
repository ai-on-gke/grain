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
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
)

// The declarations, read and written. The values live on the task
// itself (Task.CustomFields, putTask and hydrate in store.go), because
// they are part of what a task *is* -- written in the same transaction,
// exported in the same tier, deleted with it.

// ListCustomFields returns every declared field in the order every
// listing of them should read (SortCustomFields): position, then name.
//
// The whole table, always: a deployment declares a handful of fields,
// every form that offers them offers all of them, and the alternative
// -- a filter nothing has asked for -- would only be a way for two
// readers to disagree about what this deployment has.
func (s *Store) ListCustomFields(ctx context.Context) ([]CustomField, error) {
	return listCustomFields(ctx, s.db)
}

// listCustomFields is that read against any querier, so the two callers
// that file tasks from inside a transaction of their own -- a suite pass
// and a qualification run, both of which put a template's values on
// every task they create -- read the declarations they are checking
// against in the same transaction as the write.
func listCustomFields(ctx context.Context, q querier) ([]CustomField, error) {
	var out []CustomField
	err := each(ctx, q,
		"SELECT "+customFieldColumns+" FROM `custom_field`", nil,
		func(rows *sql.Rows) error {
			f, err := scanCustomField(rows.Scan)
			if err != nil {
				return err
			}
			out = append(out, f)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing custom fields: %w", err)
	}
	return SortCustomFields(out), nil
}

// customFieldDeclarations is the declared fields for a caller that files
// several tasks in one transaction and wants them checked against what
// this deployment declares *now* -- a suite pass, a qualification run --
// without asking the same question once per task.
//
// Read on first use rather than up front: a template carrying no values
// of its own is every template on a deployment that declares no fields,
// and neither should pay for a query.
type customFieldDeclarations struct {
	q      querier
	loaded bool
	fields []CustomField
}

// apply is what a task filed from a template actually carries for this
// deployment's fields, given the template's own values: the ones still
// declared, canonicalized by their declaration, and nothing else. A
// value whose field has been retired since the template was written is
// dropped rather than failing the run that is filing the task --
// FilterCustomFieldValues, and orchestrator.firingCustomFields for the
// same rule on the schedule path.
func (d *customFieldDeclarations) apply(ctx context.Context, values map[string][]string) (map[string][]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if !d.loaded {
		fields, err := listCustomFields(ctx, d.q)
		if err != nil {
			return nil, err
		}
		d.fields, d.loaded = fields, true
	}
	kept, _ := FilterCustomFieldValues(d.fields, values)
	return kept, nil
}

// LegacyCustomFieldValue is one value tasks are still carrying that a
// declaration would no longer produce, and the tasks carrying it.
type LegacyCustomFieldValue struct {
	Value string
	// Tasks is every task holding this value, by id, most recently
	// filed last (Store.LegacyCustomFieldUses sorts them the way a
	// person reads a list of task numbers).
	Tasks []string
}

// LegacyCustomFieldUse is one field's worth of the answer to "which
// tasks did this declaration strand?" -- what somebody retiring an enum
// option, or taking `multiple` off a field, is deciding against.
//
// The two kinds are kept apart because they are two different
// decisions. Values is a value the declaration no longer produces: the
// retired option, the date a date field would now refuse. Sets is a
// task holding more than one value on a field that no longer holds a
// set, where no individual value is wrong and the multiplicity is. A
// task can be in both.
type LegacyCustomFieldUse struct {
	// Field is the declared name, and Label what to call it on screen
	// (CustomField.DisplayName) -- the field's own name, for one
	// nothing declares.
	Field string
	Label string
	// Declared is false for a field nothing in this set of declarations
	// names at all: every value it holds is stranded, because there is
	// nothing left to render, offer or explain one.
	Declared bool
	// Values is the stranded values, most-carried first and then
	// alphabetically -- the count being what the decision wants first.
	Values []LegacyCustomFieldValue
	// Sets is the tasks carrying more than one value on a field that is
	// no longer Multiple.
	Sets []string
	// Tasks is every task caught by either, counted once.
	Tasks []string
}

// LegacyCustomFieldUses is which tasks a set of declarations would
// strand: for every field, the values tasks carry that those
// declarations would not produce today, and how many tasks carry each.
//
// `fields` is the declarations to judge against, which is deliberately
// a parameter rather than this store's own: the question worth asking
// is usually about a declaration nobody has merged yet -- "if I retire
// this option, what happens to the backlog?" -- and that is a set of
// fields read from a proposed tables/custom_field.json rather than from
// here. Pass ListCustomFields' own answer to ask it about what is
// declared now.
//
// Every task is counted, whatever state it is in: a closed task
// carrying a retired option is still a task carrying it, and a report
// that hid them would answer "nothing to clear up" to somebody whose
// dump is full of them. Nothing here rewrites anything, for the reason
// NormalizeCustomFieldValuesFrom gives at length: the values stay until
// somebody edits them off, and this is how they are found.
//
// One entry per field with something to say, in declaration order
// (SortCustomFields) with the undeclared ones after them by name. A
// deployment nothing has stranded gets nil.
func (s *Store) LegacyCustomFieldUses(ctx context.Context, fields []CustomField) ([]LegacyCustomFieldUse, error) {
	// field -> task -> the values that task holds for it.
	held := map[string]map[string][]string{}
	err := each(ctx, s.db,
		"SELECT `name`,`task_id`,`value` FROM `task_custom_field` ORDER BY `name`,`task_id`,`value`", nil,
		func(rows *sql.Rows) error {
			var name, taskID, value string
			if err := rows.Scan(&name, &taskID, &value); err != nil {
				return err
			}
			if held[name] == nil {
				held[name] = map[string][]string{}
			}
			held[name][taskID] = append(held[name][taskID], value)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading the custom field values this deployment's tasks carry: %w", err)
	}
	if len(held) == 0 {
		return nil, nil
	}

	declared := make(map[string]CustomField, len(fields))
	var names []string
	for _, f := range SortCustomFields(append([]CustomField(nil), fields...)) {
		declared[f.Name] = f
		if held[f.Name] != nil {
			names = append(names, f.Name)
		}
	}
	for _, name := range sortedKeys(held) {
		if _, ok := declared[name]; !ok {
			names = append(names, name)
		}
	}

	var out []LegacyCustomFieldUse
	for _, name := range names {
		f, isDeclared := declared[name]
		use := LegacyCustomFieldUse{Field: name, Label: name, Declared: isDeclared}
		if isDeclared {
			use.Label = f.DisplayName()
		}
		tasksFor := map[string][]string{}
		for _, taskID := range sortedKeys(held[name]) {
			values := nonEmptyValues(held[name][taskID])
			if len(values) == 0 {
				continue
			}
			caught := false
			if isDeclared && !f.Multiple && len(values) > 1 {
				use.Sets = append(use.Sets, taskID)
				caught = true
			}
			for _, v := range values {
				// A value of an undeclared field is stranded whatever
				// it looks like: nothing is left to offer it.
				if isDeclared && f.Offers(v) {
					continue
				}
				tasksFor[v] = append(tasksFor[v], taskID)
				caught = true
			}
			if caught {
				use.Tasks = append(use.Tasks, taskID)
			}
		}
		if len(use.Tasks) == 0 {
			continue
		}
		for _, v := range sortedKeys(tasksFor) {
			use.Values = append(use.Values, LegacyCustomFieldValue{Value: v, Tasks: sortTaskIDs(tasksFor[v])})
		}
		// Most-carried first, since that is the number the decision
		// turns on; alphabetically within a count, so two values on the
		// same number of tasks report in the same order every time.
		sort.SliceStable(use.Values, func(i, j int) bool {
			return len(use.Values[i].Tasks) > len(use.Values[j].Tasks)
		})
		use.Sets = sortTaskIDs(use.Sets)
		use.Tasks = sortTaskIDs(use.Tasks)
		out = append(out, use)
	}
	return out, nil
}

// sortTaskIDs puts task ids in the order somebody reads a list of them:
// as the numbers they are, so 9 comes before 10, with anything that is
// not a number sorting as text after them. In place, and returned.
func sortTaskIDs(ids []string) []string {
	sort.SliceStable(ids, func(i, j int) bool {
		a, aErr := strconv.Atoi(ids[i])
		b, bErr := strconv.Atoi(ids[j])
		aNum, bNum := aErr == nil, bErr == nil
		switch {
		case aNum && bNum:
			return a < b
		case aNum != bNum:
			return aNum
		default:
			return ids[i] < ids[j]
		}
	})
	return ids
}

// GetCustomField reads one declaration by name, or nil (with no error)
// when nothing declares it -- the same "nobody has configured one" nil
// GetRepoConfig returns.
func (s *Store) GetCustomField(ctx context.Context, name string) (*CustomField, error) {
	f, err := scanCustomField(s.db.QueryRowContext(ctx,
		"SELECT "+customFieldColumns+" FROM `custom_field` WHERE `name` = ?", name).Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading custom field %s: %w", name, err)
	}
	return &f, nil
}

// PutCustomField inserts or replaces one declaration.
//
// It exists for the same reason PutTemplate does -- something has to be
// able to write the row that a dump is made of, and `grain state check`
// imports one to prove a proposed change loads -- rather than because a
// pane offers it: the set of fields is decided in the state repository
// (custom_field.go's own doc comment), and Import is what applies a
// merged change to it.
//
// Validated here rather than trusted, so a row that could never be
// rendered cannot be written at all.
func (s *Store) PutCustomField(ctx context.Context, f CustomField) error {
	if err := f.Validate(); err != nil {
		return err
	}
	options, err := json.Marshal(f.Options)
	if err != nil {
		return fmt.Errorf("writing custom field %s: %w", f.Name, err)
	}
	if f.Options == nil {
		// "[]" rather than "null" for a field with no options, so the
		// dump of a table nobody has changed is byte-identical whichever
		// way the slice happened to be built (staterepo.Export's own
		// stability property).
		options = []byte("[]")
	}
	return s.write(ctx, "put custom field "+f.Name, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"REPLACE INTO `custom_field` ("+customFieldColumns+") VALUES (?,?,?,?,?,?,?,?,?)",
			f.Name, f.Label, string(f.FieldType()), string(options), f.Description, f.Default, f.Position,
			f.Multiple, f.ShowOnRow)
		if err != nil {
			return fmt.Errorf("writing custom field %s: %w", f.Name, err)
		}
		return nil
	})
}

// DeleteCustomField drops one declaration. The values tasks already
// carry for it are left exactly where they are -- see
// NormalizeCustomFieldValues on why a deleted field does not
// retroactively invalidate the tasks that used it.
func (s *Store) DeleteCustomField(ctx context.Context, name string) error {
	return s.write(ctx, "delete custom field "+name, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM `custom_field` WHERE `name` = ?", name)
		return err
	})
}

const customFieldColumns = "`name`,`label`,`type`,`options`,`description`,`default_value`,`position`,`multiple`,`show_on_row`"

func scanCustomField(scan func(...any) error) (CustomField, error) {
	var f CustomField
	var fieldType, options string
	if err := scan(&f.Name, &f.Label, &fieldType, &options,
		&f.Description, &f.Default, &f.Position, &f.Multiple, &f.ShowOnRow); err != nil {
		return CustomField{}, err
	}
	f.Type = CustomFieldType(fieldType)
	// An unreadable options column is read as no options rather than as
	// an error: the column is JSON grain wrote itself, and a field whose
	// choices cannot be read is still worth listing (as a broken enum a
	// human can see and fix in the repository) rather than making every
	// listing of every field fail.
	if options != "" {
		var parsed []string
		if err := json.Unmarshal([]byte(options), &parsed); err == nil {
			f.Options = parsed
		}
	}
	return f, nil
}
