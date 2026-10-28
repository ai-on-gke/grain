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

package staterepo

// The settings export in the format people write settings in.
//
// Everything else in this package is one generic table-to-JSON dumper
// (dump.go): PRAGMA table_info, rows sorted by primary key, one file per
// table. That is the right shape for grain's own record of what it did
// -- nobody proposes a change to a transcript -- and the wrong shape for
// the three things a state repository exists to be reviewed for. A
// template is not one row: it is `template` plus template_read,
// template_write, template_grant, template_field and
// template_custom_field, which is six files to read and six files to
// edit in step for one object. A schedule's cadence is five columns with
// four of them NULL.
//
// So the three of them are exported here instead, as nested documents in
// the authoring format -- the same keys, hyphenated the same way, nested
// the same way, as the YAML a repository writes under `.grain`
// (pkg/repodecl, docs/declarations.md). docs/declaration-schema.md is
// the argument for which direction that convergence goes: the JSON is a
// dump, the YAML is a format people write, and it is the authoring
// format that wins.
//
// Two things about it are deliberate and are not the authoring format's:
//
//   - Identity stays the row's id. A settings id is not only a key, it
//     is a reference, and not all the references are settings --
//     task.review_template_id and the suite-run tables are history, which
//     a rename could not follow. It is also what protects the row grain
//     seeds for itself (`system:memory-agent`): a name-keyed export would
//     make renaming that key create a second schedule and leave the
//     seeder to re-create the first. So `id:` leads every entry, and the
//     file is ordered by it.
//   - Every key the settings context has is written, every time, even at
//     its default. That is not verbosity for its own sake: a settings
//     file that does not mention a column is a row that lands on the
//     schema's default when it is imported, which is a real loss that
//     resets.go reports and check.go warns about. A format that omitted
//     empty values would turn every one of those warnings on for every
//     row.
//
// What it does not carry is the four *_sequence tables. Those are id
// allocators (model.Store.NewTemplateID) rather than anything a template
// is made of -- a row of them is a number and a timestamp -- so they
// stay in tables/ with the rest of the dump.
//
// The shape is a mapping with one list in it, plus an escape hatch:
//
//	templates:
//	  - id: template-1
//	    name: nightly
//	    body: |
//	      Do the thing.
//	    grants: [memory]
//	orphaned-rows:
//	  template_read: [...]
//
// `orphaned-rows` is normally absent and is there because a database can
// hold rows it fits: until grain/task-380, Store.DeleteTemplate and
// Store.DeleteSchedule deleted the parent row and left the companion
// rows behind, so a deployment that deleted a template under an older
// build holds template_read rows no template claims. The current dump
// carries them because it dumps tables; this one would drop them, and a
// dump that drops rows is not a restore. They are written back out
// verbatim, in the flat shape the rest of the dump uses, and are the one
// part of these files nobody is meant to read.
//
// Nothing produces them any more: the store deletes a template's and a
// schedule's companion rows with their parent, and
// Store.DeleteOrphanedSettingsRows sweeps up whatever an older build
// accumulated when grain opens its store. Both halves of this stay
// anyway, and that is the deliberate choice rather than an oversight:
// the reader has to go on understanding the key, because a state
// repository written by an older build still holds one and importing it
// is exactly what a restore is; and the writer has to go on producing
// one, because "no orphan can exist" is a claim about a database this
// package is handed rather than one it can check -- and a dump that
// silently dropped rows would turn a wrong claim into lost data. What
// has changed is that both are now expected to be dead code on a
// deployment that has been running this build.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ai-on-gke/grain/pkg/declschema"
	"github.com/ai-on-gke/grain/pkg/model"
)

// SettingsDir holds the three nested settings documents, one per kind.
//
// Beside tables/ rather than inside it: everything in tables/ is a file
// named for a table, and the check that nothing else is there
// (TestTheDumpHoldsNoFileThatNamesNoTable, check.go's describeDump) is
// worth keeping exactly as strict as it is.
const SettingsDir = "settings"

// declKind is one of the three things, and the tables it is made of.
type declKind struct {
	schema declschema.Schema
	// file is the document's name inside SettingsDir.
	file string
	// list is the key the document's own list is under -- the plural of
	// the kind, since the file holds all of them.
	list string
	// parent is the column every companion table joins back on.
	parent string
	// fields is the kind's fields in the order the document writes them,
	// each resolved to the key it is written under and the columns or
	// companion table behind it.
	fields []declFieldShape
	// companions is every companion table, in SettingsTables' own order.
	companions []string
}

// Path is the document's path inside the repository, slash-separated the
// way the rest of this package reports one.
func (k declKind) Path() string { return SettingsDir + "/" + k.file }

// Tables is the kind's own table and its companions -- exactly the
// tables this document carries, and therefore exactly the files it
// replaces in tables/.
func (k declKind) Tables() []string {
	return append([]string{k.schema.Table}, k.companions...)
}

// shape says how one field crosses between columns and the document, for
// the handful that are not one column under one key.
type shape int

const (
	// shapeScalar is a column written as itself.
	shapeScalar shape = iota
	// shapeBool is an INTEGER column holding 0 or 1, written as a
	// boolean because that is what it means and what `.grain` writes.
	shapeBool
	// shapeTarget is target_owner and target_name, written "owner/name".
	shapeTarget
	// shapeChangeTarget is change_target_kind and change_target_spec,
	// written as the bare kind, or {kind, spec} when there is a spec.
	shapeChangeTarget
	// shapeRecurrence is the six cadence columns, written as one of the
	// four blocks a declared schedule uses.
	shapeRecurrence
	// shapeRepos is a companion table of (owner, name) -- `reads`.
	shapeRepos
	// shapeWrites is a companion table of (owner, name, base).
	shapeWrites
	// shapeGrants is a companion table of (capability, via, folder).
	shapeGrants
	// shapeInputs is template_field: the form a template asks for.
	shapeInputs
	// shapeValues is a *_custom_field table: name to value, or values.
	shapeValues
	// shapeItems is suite_item: the templates a suite runs, in order.
	shapeItems
)

// declFieldShape is one field of a document.
type declFieldShape struct {
	key     string
	columns []string
	table   string
	shape   shape
}

// booleanKeys names the settings keys whose column is an INTEGER holding
// nothing but 0 and 1. There is no way to ask the schema -- SQLite has
// no boolean type and pkg/declschema describes fields rather than SQL --
// so it is a list, pinned by TestEverySettingsColumnHasAShape against
// what the documents actually round-trip.
var booleanKeys = map[string]bool{
	"auto-merge":             true,
	"auto-approve-proposals": true,
	"enabled":                true,
	"require-approval":       true,
	"interactive":            true,
}

// declKinds is the three of them, built from pkg/declschema so the keys,
// the columns behind them and the companion tables are the schema's
// answer rather than a second copy of it.
//
// Built once, at package initialisation: it is read per table by every
// caller that asks where a table's rows live, and the schema it is built
// from is a constant. A shape it cannot resolve panics here rather than
// on the first export, which is the point -- a column added to one of
// these three tables has to say what it looks like in the file before
// this package will start.
func declKinds() []declKind { return declKindsCache }

var declKindsCache = buildDeclKinds()

func buildDeclKinds() []declKind {
	var out []declKind
	for _, s := range declschema.Declarations() {
		k := declKind{schema: s, file: declFileName(s.Kind), list: declListKey(s.Kind), parent: declParent(s)}
		for _, f := range s.Fields {
			key, ok := f.KeyIn(declschema.Settings)
			if !ok {
				// A key no context accepts ("repo", "capabilities") and
				// the keys a repository writes instead of the settings
				// spelling. Neither is a field of this document.
				continue
			}
			sh, err := shapeOf(f, key)
			if err != nil {
				panic("staterepo: " + err.Error())
			}
			if f.Table != "" {
				k.companions = append(k.companions, f.Table)
			}
			k.fields = append(k.fields, declFieldShape{key: key, columns: f.Columns, table: f.Table, shape: sh})
		}
		out = append(out, k)
	}
	return out
}

// shapeOf picks how one schema field crosses, and refuses to guess. A
// column added to `schedule` has to be a pkg/declschema entry already
// (that package's own test says so); this is where it then has to say
// what it looks like in the file, rather than being written out under a
// shape that happens to compile.
func shapeOf(f declschema.Field, key string) (shape, error) {
	switch {
	case f.Table != "":
		switch {
		case strings.HasSuffix(f.Table, "_custom_field"):
			return shapeValues, nil
		case strings.HasSuffix(f.Table, "_read"):
			return shapeRepos, nil
		case strings.HasSuffix(f.Table, "_write"):
			return shapeWrites, nil
		case strings.HasSuffix(f.Table, "_grant"):
			return shapeGrants, nil
		case f.Table == "template_field":
			return shapeInputs, nil
		case f.Table == "suite_item":
			return shapeItems, nil
		}
		return 0, fmt.Errorf("no document shape for companion table %q", f.Table)
	case len(f.Columns) == 1:
		if booleanKeys[key] {
			return shapeBool, nil
		}
		return shapeScalar, nil
	case len(f.Columns) == 2 && f.Columns[0] == "target_owner":
		return shapeTarget, nil
	case len(f.Columns) == 2 && f.Columns[0] == "change_target_kind":
		return shapeChangeTarget, nil
	case len(f.Columns) == 6 && f.Columns[0] == "recurrence_kind":
		return shapeRecurrence, nil
	}
	return 0, fmt.Errorf("no document shape for key %q over columns %v", key, f.Columns)
}

func declFileName(kind model.DeclarationKind) string {
	return declListKey(kind) + ".yml"
}

func declListKey(kind model.DeclarationKind) string {
	switch kind {
	case model.DeclaredTemplate:
		return "templates"
	case model.DeclaredSuite:
		return "suites"
	case model.DeclaredSchedule:
		return "schedules"
	}
	panic(fmt.Sprintf("staterepo: no document for declaration kind %q", kind))
}

// declParent is the column a kind's companion tables join back on.
func declParent(s declschema.Schema) string { return s.Table + "_id" }

// declKindOf is the kind whose document carries table, if any.
func declKindOf(table string) (declKind, bool) {
	for _, k := range declKinds() {
		for _, t := range k.Tables() {
			if t == table {
				return k, true
			}
		}
	}
	return declKind{}, false
}

// declTables is every table the three documents carry, as a set -- the
// tables that are no longer written to tables/.
func declTables() map[string]bool {
	out := map[string]bool{}
	for _, k := range declKinds() {
		for _, t := range k.Tables() {
			out[t] = true
		}
	}
	return out
}

// DumpPath is where in the repository a table's rows are written: a
// settings document for the three kinds and their companions, and the
// table's own file for everything else. Slash-separated, because it is
// reported on pull requests as much as it is opened.
func DumpPath(table string) string {
	if k, ok := declKindOf(table); ok {
		return k.Path()
	}
	return TablesDir + "/" + table + ".json"
}

// dumpRowsOf reads the rows the dump holds for one table, whichever
// shape they are written in, and says whether the dump carries them at
// all -- which is the question every caller asks first, because a
// settings table the dump has no file for is one Apply empties.
//
// A document that is not there falls back to the table's own JSON file.
// That is not a transitional kindness, it is what a state repository
// written by any build before this one looks like, and Load's restore
// has to keep working on one.
func dumpRowsOf(dir, table string) ([]map[string]any, bool, error) {
	if k, ok := declKindOf(table); ok {
		doc, present, err := readDeclDocument(dir, k)
		if err != nil {
			return nil, false, err
		}
		if present {
			rows, err := doc.rows(k, table)
			return rows, true, err
		}
	}
	path := filepath.Join(dir, TablesDir, table+".json")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("staterepo: reading %s: %w", path, err)
	}
	rows, err := readRows(path)
	return rows, true, err
}

// declDocument is one settings file, parsed: the entries in the order
// they were written, and whatever orphaned rows the file carries.
type declDocument struct {
	entries []map[string]*yaml.Node
	orphans map[string][]map[string]any
	path    string
}

// rows is the flat rows of one table, taken out of the document -- the
// exact inverse of what writeDeclDocument put in, in the same value
// domain readRows returns so that every reader in this package is
// reading one kind of thing.
func (d declDocument) rows(k declKind, table string) ([]map[string]any, error) {
	main := table == k.schema.Table
	var out []map[string]any
	// The surrogate id suite_item carries is allocated here, across the
	// whole file, because the document does not carry one: it is a rowid
	// nothing in the schema refers to, and putSuite already throws every
	// one of them away and issues new ones on each edit of a suite.
	items := 0
	for i, entry := range d.entries {
		id, err := d.entryID(k, i, entry)
		if err != nil {
			return nil, err
		}
		row := map[string]any{"id": id}
		for _, f := range k.fields {
			if f.key == "id" {
				continue
			}
			node, ok := entry[f.key]
			if f.shape == shapeRecurrence {
				node, ok = recurrenceNodeOf(entry)
			}
			if !ok {
				continue
			}
			switch {
			case f.table == "" && main:
				if err := decodeIntoRow(d.path, id, f, node, row); err != nil {
					return nil, err
				}
			case f.table == table:
				rows, err := decodeCompanion(d.path, id, k.parent, f, node, &items)
				if err != nil {
					return nil, err
				}
				out = append(out, rows...)
			case f.table == "suite_item":
				// Counted even when another table is being read, so that
				// the ids a suite's items get do not depend on which
				// table the caller asked for.
				items += len(node.Content)
			}
		}
		if main {
			out = append(out, row)
		}
	}
	out = append(out, d.orphans[table]...)
	return out, nil
}

func (d declDocument) entryID(k declKind, i int, entry map[string]*yaml.Node) (string, error) {
	node, ok := entry["id"]
	if !ok {
		return "", fmt.Errorf("staterepo: %s entry %d has no `id`, and a settings row is its id", d.path, i)
	}
	if node.Kind != yaml.ScalarNode {
		return "", fmt.Errorf("staterepo: %s entry %d has an `id` that is not a name", d.path, i)
	}
	return node.Value, nil
}

// recurrenceNodeOf finds whichever of the cadence keys an entry carries.
func recurrenceNodeOf(entry map[string]*yaml.Node) (*yaml.Node, bool) {
	for _, key := range []string{"every", "daily", "weekly", "monthly", "recurrence"} {
		if node, ok := entry[key]; ok {
			node = &yaml.Node{Kind: yaml.MappingNode, Content: append([]*yaml.Node{
				{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
			}, node)}
			return node, true
		}
	}
	return nil, false
}

// readDeclDocument parses one kind's file, or reports that there is none.
func readDeclDocument(dir string, k declKind) (declDocument, bool, error) {
	path := filepath.Join(dir, SettingsDir, k.file)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return declDocument{}, false, nil
	}
	if err != nil {
		return declDocument{}, false, fmt.Errorf("staterepo: reading %s: %w", path, err)
	}
	doc := declDocument{path: k.Path(), orphans: map[string][]map[string]any{}}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return declDocument{}, false, fmt.Errorf("staterepo: parsing %s: %w", k.Path(), err)
	}
	if root.Kind == 0 || len(root.Content) == 0 {
		// An empty file is a kind with nothing in it, which is a real
		// state and not a malformed file.
		return doc, true, nil
	}
	top := root.Content[0]
	if top.Kind != yaml.MappingNode {
		return declDocument{}, false, fmt.Errorf("staterepo: %s does not hold a `%s:` list", k.Path(), k.list)
	}
	for i := 0; i+1 < len(top.Content); i += 2 {
		name, value := top.Content[i].Value, top.Content[i+1]
		switch name {
		case k.list:
			for j, entry := range value.Content {
				if entry.Kind != yaml.MappingNode {
					return declDocument{}, false, fmt.Errorf(
						"staterepo: %s entry %d is not a %s", k.Path(), j, strings.TrimSuffix(k.list, "s"))
				}
				fields := map[string]*yaml.Node{}
				for x := 0; x+1 < len(entry.Content); x += 2 {
					fields[entry.Content[x].Value] = entry.Content[x+1]
				}
				doc.entries = append(doc.entries, fields)
			}
		case orphanKey:
			for x := 0; x+1 < len(value.Content); x += 2 {
				table := value.Content[x].Value
				for _, row := range value.Content[x+1].Content {
					flat := map[string]any{}
					for y := 0; y+1 < len(row.Content); y += 2 {
						v, err := nodeValue(row.Content[y+1])
						if err != nil {
							return declDocument{}, false, fmt.Errorf("staterepo: %s: %w", k.Path(), err)
						}
						flat[row.Content[y].Value] = v
					}
					doc.orphans[table] = append(doc.orphans[table], flat)
				}
			}
		default:
			return declDocument{}, false, fmt.Errorf(
				"staterepo: %s has a top-level key %q; it holds `%s:` and, rarely, `%s:`",
				k.Path(), name, k.list, orphanKey)
		}
	}
	return doc, true, nil
}

// orphanKey is where rows whose parent is gone are kept.
const orphanKey = "orphaned-rows"

// exportDeclarations writes the three documents and reports which files
// under tables/ they stand in for.
func exportDeclarations(ctx context.Context, db querier, dir string) error {
	settingsDir := filepath.Join(dir, SettingsDir)
	if err := os.MkdirAll(settingsDir, 0o700); err != nil {
		return fmt.Errorf("staterepo: preparing %s: %w", settingsDir, err)
	}
	for _, k := range declKinds() {
		data, err := declDocumentFor(ctx, db, k)
		if err != nil {
			return err
		}
		if err := writeFileIfChanged(filepath.Join(settingsDir, k.file), data); err != nil {
			return err
		}
	}
	return nil
}

// declDocumentFor renders one kind's whole file.
func declDocumentFor(ctx context.Context, db querier, k declKind) ([]byte, error) {
	main, err := declTableRows(ctx, db, k.schema.Table)
	if err != nil {
		return nil, err
	}
	companions := map[string][]map[string]any{}
	claimed := map[string]bool{}
	for _, row := range main {
		id, _ := textOf(row["id"])
		claimed[id] = true
	}
	orphans := map[string][]map[string]any{}
	for _, table := range k.companions {
		rows, err := declTableRows(ctx, db, table)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			id, ok := textOf(row[k.parent])
			if ok && claimed[id] {
				companions[table] = append(companions[table], row)
				continue
			}
			orphans[table] = append(orphans[table], row)
		}
	}

	list := &yaml.Node{Kind: yaml.SequenceNode}
	for _, row := range main {
		entry, err := declEntry(k, row, companions)
		if err != nil {
			return nil, err
		}
		list.Content = append(list.Content, entry)
	}
	top := &yaml.Node{Kind: yaml.MappingNode}
	top.Content = append(top.Content, key(k.list), list)
	if len(orphans) > 0 {
		top.Content = append(top.Content, key(orphanKey), orphanNode(k, orphans))
	}
	top.Content[0].HeadComment = declHeader(k)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(top); err != nil {
		return nil, fmt.Errorf("staterepo: writing %s: %w", k.Path(), err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("staterepo: writing %s: %w", k.Path(), err)
	}
	return buf.Bytes(), nil
}

func declHeader(k declKind) string {
	one := strings.TrimSuffix(k.list, "s")
	return "grain's " + k.list + ", in the format a repository writes one in\n" +
		"(.grain/" + k.list + "/<name>.yml, docs/declarations.md), with the keys a\n" +
		"repository is not allowed to write as well. Edit it in a pull request:\n" +
		"a merged change is live on the next tick.\n" +
		"\n" +
		"`id` is the identity and is grain's to allocate -- it is what history\n" +
		"and the other settings refer this " + one + " by, so changing one is a\n" +
		"delete and a create rather than a rename. Everything else is yours."
}

// orphanNode writes the rows whose parent row is gone, verbatim.
func orphanNode(k declKind, orphans map[string][]map[string]any) *yaml.Node {
	tables := make([]string, 0, len(orphans))
	for table := range orphans {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	node := &yaml.Node{Kind: yaml.MappingNode}
	for _, table := range tables {
		rows := &yaml.Node{Kind: yaml.SequenceNode}
		for _, row := range orphans[table] {
			names := make([]string, 0, len(row))
			for name := range row {
				names = append(names, name)
			}
			sort.Strings(names)
			entry := &yaml.Node{Kind: yaml.MappingNode}
			for _, name := range names {
				entry.Content = append(entry.Content, key(name), valueNode(row[name]))
			}
			rows.Content = append(rows.Content, entry)
		}
		node.Content = append(node.Content, key(table), rows)
	}
	node.Content[0].HeadComment = "Rows whose " + strings.TrimSuffix(k.list, "s") +
		" is gone -- Store.Delete* leaves a deleted object's companion\n" +
		"rows behind, and a dump that dropped them would not be a restore. Nothing\n" +
		"reads them; deleting them here is safe and is the only way to be rid of them."
	return node
}

// declEntry renders one object.
func declEntry(k declKind, row map[string]any, companions map[string][]map[string]any) (*yaml.Node, error) {
	id, _ := textOf(row["id"])
	out := &yaml.Node{Kind: yaml.MappingNode}
	// Identity leads, where pkg/declschema puts it last: the schema's
	// order is what reads best in a file whose name is the identity, and
	// here the id is the one thing every entry is found by.
	out.Content = append(out.Content, key("id"), valueNode(row["id"]))
	for _, f := range k.fields {
		if f.key == "id" {
			continue
		}
		if f.table != "" {
			node, err := companionNode(f, mine(companions[f.table], k.parent, id))
			if err != nil {
				return nil, err
			}
			if node == nil {
				continue
			}
			out.Content = append(out.Content, key(f.key), node)
			continue
		}
		name, node, err := columnNode(f, row)
		if err != nil {
			return nil, err
		}
		if node == nil {
			continue
		}
		out.Content = append(out.Content, key(name), node)
	}
	return out, nil
}

// mine is the companion rows belonging to one parent.
func mine(rows []map[string]any, parent, id string) []map[string]any {
	var out []map[string]any
	for _, row := range rows {
		if got, ok := textOf(row[parent]); ok && got == id {
			out = append(out, row)
		}
	}
	return out
}

// columnNode renders one field's columns, and reports the key it is
// written under -- which is the field's own for every shape but the
// cadence, where the key is which of the four blocks it is.
func columnNode(f declFieldShape, row map[string]any) (string, *yaml.Node, error) {
	switch f.shape {
	case shapeTarget:
		owner, ownerOK := textOf(row["target_owner"])
		name, nameOK := textOf(row["target_name"])
		if !ownerOK || !nameOK || strings.Contains(owner, "/") {
			// Neither is a repository name, so neither is written as
			// one: the pair goes out as a pair and still round-trips.
			pair := &yaml.Node{Kind: yaml.MappingNode}
			pair.Content = append(pair.Content, key("owner"), valueNode(row["target_owner"]))
			pair.Content = append(pair.Content, key("name"), valueNode(row["target_name"]))
			return f.key, pair, nil
		}
		if owner == "" && name == "" {
			return f.key, str(""), nil
		}
		return f.key, str(owner + "/" + name), nil
	case shapeChangeTarget:
		kind, _ := textOf(row["change_target_kind"])
		spec, _ := textOf(row["change_target_spec"])
		if spec == "" {
			return f.key, str(kind), nil
		}
		var specNode yaml.Node
		if err := yaml.Unmarshal([]byte(spec), &specNode); err != nil || len(specNode.Content) != 1 {
			// Not JSON this build can read: kept as the string it is.
			pair := &yaml.Node{Kind: yaml.MappingNode}
			pair.Content = append(pair.Content, key("kind"), str(kind), key("spec"), str(spec))
			return f.key, pair, nil
		}
		pair := &yaml.Node{Kind: yaml.MappingNode}
		pair.Content = append(pair.Content, key("kind"), str(kind), key("spec"), specNode.Content[0])
		return f.key, pair, nil
	case shapeRecurrence:
		return recurrenceNode(row)
	case shapeBool:
		v := row[f.columns[0]]
		if n, ok := intOf(v); ok && (n == 0 || n == 1) {
			return f.key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: boolWord(n == 1)}, nil
		}
		return f.key, valueNode(v), nil
	default:
		return f.key, valueNode(row[f.columns[0]]), nil
	}
}

func boolWord(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// recurrenceNode writes the six cadence columns as the block the
// schedule's kind calls for -- `daily: {at: "09:00"}` rather than
// recurrence_kind, every_n_hours, every_n_minutes, time_of_day_minutes,
// weekday and day_of_month with five of them NULL. Both interval kinds
// share `every`, as `every: {hours: N}` and `every: {minutes: N}`.
//
// Total rather than only correct for the rows grain writes: a row whose
// columns do not fit its kind (an old dump edited by hand, a kind a
// later build adds) goes out under `recurrence:` with all six columns
// spelled out, so there is no shape of the row this cannot carry.
func recurrenceNode(row map[string]any) (string, *yaml.Node, error) {
	kind, _ := textOf(row["recurrence_kind"])
	hours, hoursSet := intOf(row["every_n_hours"])
	minutes, minutesSet := intOf(row["every_n_minutes"])
	at, atSet := intOf(row["time_of_day_minutes"])
	weekday, weekdaySet := intOf(row["weekday"])
	day, daySet := intOf(row["day_of_month"])
	block := func(content ...*yaml.Node) *yaml.Node {
		return &yaml.Node{Kind: yaml.MappingNode, Content: content}
	}
	switch model.RecurrenceKind(kind) {
	case model.RecurrenceEveryNHours:
		if hoursSet && !minutesSet && !atSet && !weekdaySet && !daySet {
			return "every", block(key("hours"), number(hours)), nil
		}
	case model.RecurrenceEveryNMinutes:
		if minutesSet && !hoursSet && !atSet && !weekdaySet && !daySet {
			return "every", block(key("minutes"), number(minutes)), nil
		}
	case model.RecurrenceDaily:
		if !hoursSet && !minutesSet && atSet && !weekdaySet && !daySet && timeOfDay(at) != "" {
			return "daily", block(key("at"), clock(at)), nil
		}
	case model.RecurrenceWeekly:
		if !hoursSet && !minutesSet && atSet && weekdaySet && !daySet && timeOfDay(at) != "" && weekdayName(weekday) != "" {
			return "weekly", block(key("day"), str(weekdayName(weekday)), key("at"), clock(at)), nil
		}
	case model.RecurrenceMonthly:
		if !hoursSet && !minutesSet && atSet && !weekdaySet && daySet && timeOfDay(at) != "" {
			return "monthly", block(key("day"), number(day), key("at"), clock(at)), nil
		}
	}
	out := block()
	for _, c := range []string{
		"recurrence_kind", "every_n_hours", "every_n_minutes", "time_of_day_minutes", "weekday", "day_of_month"} {
		out.Content = append(out.Content, key(strings.ReplaceAll(c, "_", "-")), valueNode(row[c]))
	}
	return "recurrence", out, nil
}

// clock is a time of day as a quoted scalar. Quoted deliberately: YAML
// reads a bare 09:00 as a string here and a bare 9:00 as sixty times
// nine, and `at: "09:00"` is how docs/declarations.md spells it and how
// anybody editing one of these files will copy it.
func clock(minutes int64) *yaml.Node {
	return &yaml.Node{
		Kind: yaml.ScalarNode, Tag: "!!str", Style: yaml.DoubleQuotedStyle, Value: timeOfDay(minutes),
	}
}

// timeOfDay renders minutes since midnight as "09:00", or "" for a value
// that is not one.
func timeOfDay(minutes int64) string {
	if minutes < 0 || minutes > 23*60+59 {
		return ""
	}
	return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60)
}

var weekdayNames = []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}

func weekdayName(n int64) string {
	if n < 0 || int(n) >= len(weekdayNames) {
		return ""
	}
	return weekdayNames[n]
}

func weekdayNumber(name string) (int64, bool) {
	for i, w := range weekdayNames {
		if w == name {
			return int64(i), true
		}
	}
	return 0, false
}

// companionNode renders one relation as the nested list it is in the
// authoring format, or nil for a relation with nothing in it -- an empty
// list adds a key to every entry to say nothing.
func companionNode(f declFieldShape, rows []map[string]any) (*yaml.Node, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	switch f.shape {
	case shapeRepos:
		out := &yaml.Node{Kind: yaml.SequenceNode}
		for _, row := range rows {
			out.Content = append(out.Content, repoNode(row, "owner", "name"))
		}
		return out, nil
	case shapeWrites:
		out := &yaml.Node{Kind: yaml.SequenceNode}
		for _, row := range rows {
			base, ok := textOf(row["base"])
			repo := repoNode(row, "owner", "name")
			if ok && base == "" && repo.Kind == yaml.ScalarNode {
				out.Content = append(out.Content, repo)
				continue
			}
			entry := &yaml.Node{Kind: yaml.MappingNode}
			entry.Content = append(entry.Content, key("repo"), repo, key("base"), valueNode(row["base"]))
			out.Content = append(out.Content, entry)
		}
		return out, nil
	case shapeGrants:
		out := &yaml.Node{Kind: yaml.SequenceNode}
		for _, row := range rows {
			via, viaOK := textOf(row["via"])
			capability, capOK := textOf(row["capability"])
			if viaOK && capOK && via == string(model.GrantByLabel) && row["folder"] == nil {
				out.Content = append(out.Content, str(capability))
				continue
			}
			entry := &yaml.Node{Kind: yaml.MappingNode}
			entry.Content = append(entry.Content,
				key("capability"), valueNode(row["capability"]),
				key("via"), valueNode(row["via"]),
				key("folder"), valueNode(row["folder"]))
			out.Content = append(out.Content, entry)
		}
		return out, nil
	case shapeInputs:
		sort.SliceStable(rows, func(i, j int) bool {
			a, _ := intOf(rows[i]["position"])
			b, _ := intOf(rows[j]["position"])
			if a != b {
				return a < b
			}
			x, _ := textOf(rows[i]["name"])
			y, _ := textOf(rows[j]["name"])
			return x < y
		})
		out := &yaml.Node{Kind: yaml.SequenceNode}
		for _, row := range rows {
			entry := &yaml.Node{Kind: yaml.MappingNode}
			entry.Content = append(entry.Content, key("name"), valueNode(row["name"]))
			entry.Content = append(entry.Content, key("label"), valueNode(row["label"]))
			entry.Content = append(entry.Content, key("options"), optionsNode(row["options"]))
			entry.Content = append(entry.Content, key("default"), valueNode(row["default_value"]))
			entry.Content = append(entry.Content, key("required"), boolNode(row["required"]))
			out.Content = append(out.Content, entry)
		}
		return out, nil
	case shapeValues:
		byName := map[string][]*yaml.Node{}
		var names []string
		for _, row := range rows {
			name, _ := textOf(row["name"])
			if _, seen := byName[name]; !seen {
				names = append(names, name)
			}
			byName[name] = append(byName[name], valueNode(row["value"]))
		}
		sort.Strings(names)
		out := &yaml.Node{Kind: yaml.MappingNode}
		for _, name := range names {
			values := byName[name]
			if len(values) == 1 {
				out.Content = append(out.Content, key(name), values[0])
				continue
			}
			out.Content = append(out.Content, key(name), &yaml.Node{Kind: yaml.SequenceNode, Content: values})
		}
		return out, nil
	case shapeItems:
		sort.SliceStable(rows, func(i, j int) bool {
			a, _ := floatOf(rows[i]["order_key"])
			b, _ := floatOf(rows[j]["order_key"])
			if a != b {
				return a < b
			}
			x, _ := intOf(rows[i]["id"])
			y, _ := intOf(rows[j]["id"])
			return x < y
		})
		out := &yaml.Node{Kind: yaml.SequenceNode}
		for _, row := range rows {
			out.Content = append(out.Content, valueNode(row["template_id"]))
		}
		return out, nil
	}
	return nil, fmt.Errorf("staterepo: no renderer for %q", f.key)
}

// repoNode writes a repository as "owner/name" -- or "host/owner/name"
// for one on a git host other than the default, whose owner column holds
// host/owner (model.RepoRef.StoredOwner) -- or as a pair for the values
// that are not one, so nothing here can lose a byte.
func repoNode(row map[string]any, ownerCol, nameCol string) *yaml.Node {
	owner, ownerOK := textOf(row[ownerCol])
	name, nameOK := textOf(row[nameCol])
	if ownerOK && nameOK && owner != "" && name != "" && strings.Count(owner, "/") <= 1 &&
		!strings.HasPrefix(owner, "/") && !strings.HasSuffix(owner, "/") && !strings.Contains(name, "/") {
		return str(owner + "/" + name)
	}
	pair := &yaml.Node{Kind: yaml.MappingNode}
	pair.Content = append(pair.Content, key("owner"), valueNode(row[ownerCol]), key("name"), valueNode(row[nameCol]))
	return pair
}

// optionsNode writes template_field.options -- a JSON array in the
// column -- as the YAML list it means, and verbatim for anything that is
// not one, so a hand-written column survives the round trip too.
func optionsNode(v any) *yaml.Node {
	text, ok := textOf(v)
	if !ok {
		return valueNode(v)
	}
	var options []string
	if err := json.Unmarshal([]byte(text), &options); err != nil {
		return valueNode(v)
	}
	if back, err := json.Marshal(options); err != nil || string(back) != text {
		// The column does not render back to the bytes it holds --
		// whitespace, an escape spelled another way -- so it goes out as
		// the string it is.
		return valueNode(v)
	}
	out := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
	for _, o := range options {
		out.Content = append(out.Content, str(o))
	}
	return out
}

func boolNode(v any) *yaml.Node {
	if n, ok := intOf(v); ok && (n == 0 || n == 1) {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: boolWord(n == 1)}
	}
	return valueNode(v)
}

// key is a mapping key, which is always a plain string here.
func key(name string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}
}

func str(s string) *yaml.Node { return valueNode(s) }

func number(n int64) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatInt(n, 10)}
}

// valueNode renders one column value, through the same encodeValue every
// row in the dump goes through: a timestamp is RFC3339 in UTC, and bytes
// that are not text are base64 in a one-key mapping, which is the one
// shape that cannot collide with a string a column legitimately holds.
func valueNode(v any) *yaml.Node {
	switch t := encodeValue(v).(type) {
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: boolWord(t)}
	case int64:
		return number(t)
	case int:
		return number(int64(t))
	case float64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: strconv.FormatFloat(t, 'g', -1, 64)}
	case map[string]string:
		out := &yaml.Node{Kind: yaml.MappingNode}
		for _, name := range sortedKeys(t) {
			out.Content = append(out.Content, key(name), str(t[name]))
		}
		return out
	case string:
		node := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: t}
		if blockSafe(t) {
			// The whole point of the exercise, for a template's body: a
			// prompt somebody wrote reads as the prose it is rather than
			// as one line with \n in it.
			node.Style = yaml.LiteralStyle
		}
		return node
	default:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: fmt.Sprint(t)}
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// blockSafe reports whether a string can go out as a `|` literal and
// come back the same. YAML's literal style cannot carry a line with
// trailing whitespace on it, or a carriage return, or anything that is
// not printable; the emitter quietly falls back for some of those and
// not for others, so the answer is decided here rather than discovered.
func blockSafe(s string) bool {
	if !strings.Contains(s, "\n") || strings.Contains(s, "\r") {
		return false
	}
	for _, line := range strings.Split(s, "\n") {
		if strings.HasSuffix(line, " ") || strings.HasSuffix(line, "\t") {
			return false
		}
	}
	for _, r := range s {
		if r == '\n' || r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// nodeValue reads one scalar back, in the value domain readRows returns:
// a number is a json.Number so that a 64-bit id does not go through a
// float, and a timestamp is the string decodeValue will parse.
func nodeValue(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.ScalarNode:
		switch n.Tag {
		case "!!null":
			return nil, nil
		case "!!bool":
			return n.Value == "true" || n.Value == "yes" || n.Value == "on" ||
				n.Value == "True" || n.Value == "TRUE", nil
		case "!!int", "!!float":
			return json.Number(n.Value), nil
		default:
			return n.Value, nil
		}
	case yaml.MappingNode:
		out := map[string]any{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			v, err := nodeValue(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			out[n.Content[i].Value] = v
		}
		return out, nil
	}
	return nil, fmt.Errorf("a value here is a scalar or a base64 object, not a list")
}

// decodeIntoRow is columnNode's inverse for one field of the kind's own
// table.
func decodeIntoRow(path, id string, f declFieldShape, node *yaml.Node, row map[string]any) error {
	switch f.shape {
	case shapeTarget:
		return decodeRepoInto(path, id, node, row, "target_owner", "target_name")
	case shapeChangeTarget:
		return decodeChangeTargetInto(path, id, f.key, node, row)
	case shapeRecurrence:
		return decodeRecurrence(path, id, node, row)
	case shapeBool:
		v, err := nodeValue(node)
		if err != nil {
			return fmt.Errorf("staterepo: %s, %s: %s: %w", path, id, f.key, err)
		}
		row[f.columns[0]] = fromBool(v)
		return nil
	default:
		v, err := nodeValue(node)
		if err != nil {
			return fmt.Errorf("staterepo: %s, %s: %s: %w", path, id, f.key, err)
		}
		row[f.columns[0]] = v
		return nil
	}
}

// fromBool turns the booleans these files are written with back into the
// 0 and 1 an INTEGER column holds, and leaves anything else alone.
func fromBool(v any) any {
	if b, ok := v.(bool); ok {
		if b {
			return json.Number("1")
		}
		return json.Number("0")
	}
	return v
}

// decodeRepoInto reads "owner/name", or the pair form, into two columns.
func decodeRepoInto(path, id string, node *yaml.Node, row map[string]any, ownerCol, nameCol string) error {
	if node.Kind == yaml.MappingNode {
		v, err := nodeValue(node)
		if err != nil {
			return fmt.Errorf("staterepo: %s, %s: %w", path, id, err)
		}
		pair, _ := v.(map[string]any)
		owner, hasOwner := pair["owner"]
		name, hasName := pair["name"]
		if !hasOwner || !hasName {
			return fmt.Errorf("staterepo: %s, %s: a repository is \"owner/name\" or {owner, name}", path, id)
		}
		row[ownerCol], row[nameCol] = owner, name
		return nil
	}
	if node.Tag == "!!null" {
		row[ownerCol], row[nameCol] = nil, nil
		return nil
	}
	// The name is everything after the last slash, so "host/owner/name"
	// keeps host/owner in the owner column (model.RepoRef.StoredOwner).
	owner, name := node.Value, ""
	if i := strings.LastIndex(node.Value, "/"); i >= 0 {
		owner, name = node.Value[:i], node.Value[i+1:]
	}
	row[ownerCol], row[nameCol] = owner, name
	return nil
}

// decodeRecurrence is recurrenceNode's inverse: the block name says the
// kind, and what is under it says the columns.
func decodeRecurrence(path, id string, node *yaml.Node, row map[string]any) error {
	fail := func(why string) error {
		return fmt.Errorf("staterepo: %s, %s: %s", path, id, why)
	}
	if node.Kind != yaml.MappingNode || len(node.Content) != 2 {
		return fail("a cadence is one of `every`, `daily`, `weekly`, `monthly` or `recurrence`")
	}
	block := node.Content[0].Value
	body := node.Content[1]
	inside := map[string]*yaml.Node{}
	for i := 0; i+1 < len(body.Content); i += 2 {
		inside[body.Content[i].Value] = body.Content[i+1]
	}
	for _, c := range []string{
		"recurrence_kind", "every_n_hours", "every_n_minutes", "time_of_day_minutes", "weekday", "day_of_month"} {
		row[c] = nil
	}
	switch block {
	case "recurrence":
		for _, c := range []string{
			"recurrence_kind", "every_n_hours", "every_n_minutes", "time_of_day_minutes", "weekday", "day_of_month"} {
			n, ok := inside[strings.ReplaceAll(c, "_", "-")]
			if !ok {
				delete(row, c)
				continue
			}
			v, err := nodeValue(n)
			if err != nil {
				return fail(err.Error())
			}
			row[c] = v
		}
		return nil
	case "every":
		hours, hoursOK := inside["hours"]
		minutes, minutesOK := inside["minutes"]
		if hoursOK == minutesOK {
			return fail("`every` needs one of `hours` or `minutes`")
		}
		kind, column, n := model.RecurrenceEveryNHours, "every_n_hours", hours
		if minutesOK {
			kind, column, n = model.RecurrenceEveryNMinutes, "every_n_minutes", minutes
		}
		v, err := nodeValue(n)
		if err != nil {
			return fail(err.Error())
		}
		row["recurrence_kind"] = string(kind)
		row[column] = v
		return nil
	case "daily", "weekly", "monthly":
		at, ok := inside["at"]
		if !ok {
			return fail("`" + block + "` needs a time of day, as `at: \"09:00\"`")
		}
		minutes, err := minutesOfDay(at.Value)
		if err != nil {
			return fail(err.Error())
		}
		row["recurrence_kind"] = block
		row["time_of_day_minutes"] = json.Number(strconv.FormatInt(minutes, 10))
		if block == "weekly" {
			day, ok := inside["day"]
			if !ok {
				return fail("`weekly` needs a `day`")
			}
			n, ok := weekdayNumber(strings.ToLower(strings.TrimSpace(day.Value)))
			if !ok {
				return fail(strconv.Quote(day.Value) + " is not a day of the week")
			}
			row["weekday"] = json.Number(strconv.FormatInt(n, 10))
		}
		if block == "monthly" {
			day, ok := inside["day"]
			if !ok {
				return fail("`monthly` needs a `day`")
			}
			v, err := nodeValue(day)
			if err != nil {
				return fail(err.Error())
			}
			row["day_of_month"] = v
		}
		return nil
	}
	return fail("a cadence is one of `every`, `daily`, `weekly`, `monthly` or `recurrence`")
}

func minutesOfDay(at string) (int64, error) {
	hour, minute, ok := strings.Cut(strings.TrimSpace(at), ":")
	if !ok {
		return 0, fmt.Errorf("%q is not a time of day -- write it as \"09:00\"", at)
	}
	h, herr := strconv.Atoi(strings.TrimSpace(hour))
	m, merr := strconv.Atoi(strings.TrimSpace(minute))
	if herr != nil || merr != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("%q is not a time of day -- write it as \"09:00\"", at)
	}
	return int64(h*60 + m), nil
}

// decodeCompanion is companionNode's inverse: one nested list back into
// the rows of one companion table.
//
// items is the running count of suite_item rows, which is where that
// table's surrogate id comes from -- see the note in this package's own
// tests on why the list's position is the whole of what it means.
func decodeCompanion(path, id, parent string, f declFieldShape, node *yaml.Node, items *int) ([]map[string]any, error) {
	fail := func(why string) error { return fmt.Errorf("staterepo: %s, %s: %s: %s", path, id, f.key, why) }
	var out []map[string]any
	switch f.shape {
	case shapeRepos:
		for _, entry := range node.Content {
			row := map[string]any{parent: id}
			if err := decodeRepoInto(path, id, entry, row, "owner", "name"); err != nil {
				return nil, err
			}
			out = append(out, row)
		}
	case shapeWrites:
		for _, entry := range node.Content {
			row := map[string]any{parent: id, "base": ""}
			target := entry
			if entry.Kind == yaml.MappingNode && len(entry.Content) > 0 && entry.Content[0].Value == "repo" {
				fields := map[string]*yaml.Node{}
				for i := 0; i+1 < len(entry.Content); i += 2 {
					fields[entry.Content[i].Value] = entry.Content[i+1]
				}
				target = fields["repo"]
				if base, ok := fields["base"]; ok {
					v, err := nodeValue(base)
					if err != nil {
						return nil, fail(err.Error())
					}
					row["base"] = v
				}
			}
			if err := decodeRepoInto(path, id, target, row, "owner", "name"); err != nil {
				return nil, err
			}
			out = append(out, row)
		}
	case shapeGrants:
		for _, entry := range node.Content {
			row := map[string]any{parent: id, "via": string(model.GrantByLabel), "folder": nil}
			if entry.Kind == yaml.ScalarNode {
				row["capability"] = entry.Value
				out = append(out, row)
				continue
			}
			v, err := nodeValue(entry)
			if err != nil {
				return nil, fail(err.Error())
			}
			named, _ := v.(map[string]any)
			capability, ok := named["capability"]
			if !ok {
				return nil, fail("a grant is a capability, or {capability, via, folder}")
			}
			row["capability"] = capability
			if via, ok := named["via"]; ok {
				row["via"] = via
			}
			if folder, ok := named["folder"]; ok {
				row["folder"] = folder
			}
			out = append(out, row)
		}
	case shapeInputs:
		for i, entry := range node.Content {
			fields := map[string]*yaml.Node{}
			for x := 0; x+1 < len(entry.Content); x += 2 {
				fields[entry.Content[x].Value] = entry.Content[x+1]
			}
			row := map[string]any{
				parent:     id,
				"position": json.Number(strconv.Itoa(i)),
				"label":    "",
				"options":  "[]",
				// default_value and required carry the column's own
				// default for a field that leaves them out, the way
				// pkg/repodecl's own reader does.
				"default_value": "",
				"required":      json.Number("0"),
			}
			name, ok := fields["name"]
			if !ok {
				return nil, fail("an input needs a `name`")
			}
			row["name"] = name.Value
			if label, ok := fields["label"]; ok {
				v, err := nodeValue(label)
				if err != nil {
					return nil, fail(err.Error())
				}
				row["label"] = v
			}
			if options, ok := fields["options"]; ok {
				v, err := optionsColumn(options)
				if err != nil {
					return nil, fail(err.Error())
				}
				row["options"] = v
			}
			if def, ok := fields["default"]; ok {
				v, err := nodeValue(def)
				if err != nil {
					return nil, fail(err.Error())
				}
				row["default_value"] = v
			}
			if required, ok := fields["required"]; ok {
				v, err := nodeValue(required)
				if err != nil {
					return nil, fail(err.Error())
				}
				row["required"] = fromBool(v)
			}
			out = append(out, row)
		}
	case shapeValues:
		for i := 0; i+1 < len(node.Content); i += 2 {
			name, values := node.Content[i].Value, node.Content[i+1]
			carried := []*yaml.Node{values}
			if values.Kind == yaml.SequenceNode {
				carried = values.Content
			}
			for _, v := range carried {
				value, err := nodeValue(v)
				if err != nil {
					return nil, fail(err.Error())
				}
				out = append(out, map[string]any{parent: id, "name": name, "value": value})
			}
		}
	case shapeItems:
		for i, entry := range node.Content {
			*items++
			out = append(out, map[string]any{
				"id":          json.Number(strconv.Itoa(*items)),
				parent:        id,
				"template_id": entry.Value,
				"order_key":   json.Number(strconv.Itoa(i)),
			})
		}
	default:
		return nil, fail("no reader for this relation")
	}
	return out, nil
}

// optionsColumn is optionsNode's inverse: a YAML list becomes the JSON
// array the column holds, and a scalar is the column verbatim.
func optionsColumn(n *yaml.Node) (any, error) {
	if n.Kind != yaml.SequenceNode {
		return nodeValue(n)
	}
	options := []string{}
	for _, o := range n.Content {
		options = append(options, o.Value)
	}
	data, err := json.Marshal(options)
	if err != nil {
		return nil, err
	}
	return string(data), nil
}

// declTableRows reads one whole table as encodeValue'd values in the
// export's own row order -- storedRows with the dump's own encoding on
// it, which is what these documents are written out of.
func declTableRows(ctx context.Context, db querier, table string) ([]map[string]any, error) {
	cols, err := columns(ctx, db, table)
	if err != nil {
		return nil, err
	}
	rows, err := storedRows(ctx, db, table, cols)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		for name, v := range row {
			row[name] = encodeValue(v)
		}
	}
	return rows, nil
}

// intOf reads a value that came out of encodeValue as the integer it is,
// and false for anything that is not one.
func intOf(v any) (int64, bool) {
	switch t := v.(type) {
	case int64:
		return t, true
	case int:
		return int64(t), true
	case float64:
		if t == float64(int64(t)) {
			return int64(t), true
		}
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return n, true
		}
	}
	return 0, false
}

func floatOf(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int64:
		return float64(t), true
	case json.Number:
		if f, err := t.Float64(); err == nil {
			return f, true
		}
	}
	return 0, false
}

// decodeChangeTargetInto is shapeChangeTarget's columnNode inverse: a
// bare kind, or {kind, spec} with spec a mapping (re-encoded as the JSON
// the column holds) or that JSON as a string.
func decodeChangeTargetInto(path, id, name string, node *yaml.Node, row map[string]any) error {
	if node.Kind == yaml.ScalarNode {
		row["change_target_kind"], row["change_target_spec"] = strings.TrimSpace(node.Value), ""
		return nil
	}
	var ct model.ChangeTarget
	if err := node.Decode(&ct); err != nil {
		var raw struct {
			Kind string `yaml:"kind"`
			Spec string `yaml:"spec"`
		}
		if err2 := node.Decode(&raw); err2 != nil {
			return fmt.Errorf("staterepo: %s, %s: %s: %w", path, id, name, err)
		}
		row["change_target_kind"], row["change_target_spec"] = strings.TrimSpace(raw.Kind), raw.Spec
		return nil
	}
	row["change_target_kind"], row["change_target_spec"] = ct.Kind, ct.SpecString()
	return nil
}
