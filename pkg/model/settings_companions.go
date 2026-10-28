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

// What a template, a suite and a schedule are made of besides their own
// row, and the one sweep that cleans up after the builds that forgot it.
//
// None of these tables carries a foreign key onto its parent -- nothing
// in this schema does (model/schema.go) -- so "delete the children with
// the parent" is a rule the store keeps by hand, in the same transaction
// as the parent's own delete. It was kept for a suite and not for a
// template or a schedule (grain/task-380), which left every
// template_read, template_write, template_grant, template_field,
// template_custom_field and schedule_* row of everything anybody ever
// deleted in the database for good. Nothing reads them -- every read
// joins from the parent -- so the cost was a database and a state
// repository dump that grew a little with every deletion, and the
// `orphaned-rows` escape hatch the settings export needs to stay a
// complete restore (pkg/staterepo/decl.go).
//
// The lists live here rather than inline at each use because there are
// two uses of each: putTemplate/putSchedule delete the companion rows
// before rewriting them, and DeleteTemplate/DeleteSchedule delete them
// for good. A table added to one and not the other is exactly the bug
// this file exists to have fixed, so both read the same list.

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// settingsCompanions is one kind's companion tables: the tables keyed by
// idColumn whose rows belong to a row of parent.
type settingsCompanions struct {
	// parent is the owning table, and idColumn the column its companions
	// name a row of it by.
	parent   string
	idColumn string
	// tables is every companion, in the order a delete walks them --
	// which is unordered in effect, nothing here referring to anything
	// else here.
	tables []string
}

// templateCompanions, suiteCompanions and scheduleCompanions are the
// three, in pkg/declschema's own order (template, suite, schedule).
//
// The *_sequence tables are deliberately absent: an id allocator is a
// number and a timestamp, not a row belonging to any one template
// (staterepo/decl.go makes the same exclusion for the same reason).
var (
	templateCompanions = settingsCompanions{
		parent:   "template",
		idColumn: "template_id",
		tables: []string{
			"template_read", "template_write", "template_grant",
			"template_field", "template_custom_field",
		},
	}
	suiteCompanions = settingsCompanions{
		parent:   "suite",
		idColumn: "suite_id",
		tables:   []string{"suite_item"},
	}
	scheduleCompanions = settingsCompanions{
		parent:   "schedule",
		idColumn: "schedule_id",
		tables: []string{
			"schedule_read", "schedule_write", "schedule_grant",
			"schedule_custom_field",
		},
	}

	// allSettingsCompanions is what the sweep walks.
	allSettingsCompanions = []settingsCompanions{
		templateCompanions, suiteCompanions, scheduleCompanions,
	}
)

// deleteCompanionRows clears one owner's rows out of every companion
// table: the delete half of putTemplate/putSchedule's rewrite, and the
// whole of what DeleteTemplate/DeleteSchedule do before dropping the
// parent row.
func deleteCompanionRows(ctx context.Context, tx *sql.Tx, c settingsCompanions, id string) error {
	for _, table := range c.tables {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM `"+table+"` WHERE `"+c.idColumn+"` = ?", id); err != nil {
			return fmt.Errorf("deleting %s rows of %s %s: %w", table, c.parent, id, err)
		}
	}
	return nil
}

// OrphanedSettingsRows is what one sweep found: how many rows of each
// companion table named a parent that is gone. A table with nothing to
// sweep is absent rather than present at zero, so the map is empty for
// the database this is true of -- which, once every build deletes
// companion rows with their parent, is every database that has not been
// carried across from an older one.
type OrphanedSettingsRows map[string]int

// Total is how many rows there were altogether.
func (o OrphanedSettingsRows) Total() int {
	n := 0
	for _, count := range o {
		n += count
	}
	return n
}

// String names the tables and their counts, table order, for the line
// whoever ran the sweep puts in the journal: "template_grant 3,
// schedule_read 1".
func (o OrphanedSettingsRows) String() string {
	tables := make([]string, 0, len(o))
	for table := range o {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	parts := make([]string, 0, len(tables))
	for _, table := range tables {
		parts = append(parts, fmt.Sprintf("%s %d", table, o[table]))
	}
	return strings.Join(parts, ", ")
}

// DeleteOrphanedSettingsRows deletes every companion row whose parent
// row is gone, and reports how many of each it deleted.
//
// This is the cleanup for what older builds accumulated: until
// grain/task-380 Store.DeleteTemplate and Store.DeleteSchedule deleted
// only the parent row, so a deployment that has ever deleted a template
// or a schedule holds its companion rows still. Nothing since deletes
// them -- there is no second path that revisits a companion table of a
// parent that no longer exists -- so they are swept once, here, rather
// than left for a dump to carry forever.
//
// It is a delete, and a delete run without anybody asking for one, which
// is why it is written to be as conservative as a delete can be:
//
//   - It counts before it deletes, and issues no DELETE at all for a
//     table with nothing orphaned. A healthy database is therefore swept
//     by a handful of counting SELECTs that dirty no page and change no
//     row, and the sweep has nothing to say about it.
//   - It returns the counts rather than logging them itself (pkg/model
//     logs nothing, deliberately), so that the one caller that runs it
//     says in the journal what it deleted -- rows nobody reads are still
//     rows somebody may want to know went.
//   - It is one transaction, so the sweep is all-or-nothing, and it is
//     keyed on nothing but "the id in this row is not an id in the parent
//     table". A row whose parent exists is untouched however old,
//     unreferenced or odd it looks.
func (s *Store) DeleteOrphanedSettingsRows(ctx context.Context) (OrphanedSettingsRows, error) {
	swept := OrphanedSettingsRows{}
	err := s.write(ctx, "delete settings rows whose owner is gone", func(tx *sql.Tx) error {
		clear(swept)
		for _, c := range allSettingsCompanions {
			for _, table := range c.tables {
				where := "WHERE `" + c.idColumn + "` NOT IN (SELECT `id` FROM `" + c.parent + "`)"
				var orphaned int
				if err := tx.QueryRowContext(ctx,
					"SELECT COUNT(*) FROM `"+table+"` "+where).Scan(&orphaned); err != nil {
					return fmt.Errorf("counting %s rows whose %s is gone: %w", table, c.parent, err)
				}
				if orphaned == 0 {
					continue
				}
				if _, err := tx.ExecContext(ctx, "DELETE FROM `"+table+"` "+where); err != nil {
					return fmt.Errorf("deleting %s rows whose %s is gone: %w", table, c.parent, err)
				}
				swept[table] = orphaned
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return swept, nil
}
