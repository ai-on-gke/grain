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
	"fmt"
)

// migrateScheduleOwnSettings is migration 143: it takes the copies of a
// template's admission, hosting and grants off every schedule that fires
// that template, leaving the schedule's own raise and grants where they
// were.
//
// Before it, every firing of a template-backed schedule copied its
// template's admission, hosting and grants over the schedule's own
// (Schedule.syncTemplateCache), and attaching a schedule to a template
// did the same (ui.scheduleContentFromTemplate). Those three are what
// the schedule adds to its template (Schedule.Grants), which
// ComposeScheduleTask merges with the template's at every firing, so a
// copy left on the row would go on raising every firing to what the
// template said when it was copied, after the template stopped saying
// it. This is the one place a copy is told apart from the schedule's
// own, by the only thing that can tell them apart: a copy says no more
// than the template does.
//
//   - An admission or hosting no higher than the template's is cleared:
//     it raises nothing, whether it was a copy or the schedule's own.
//   - A grant the template also grants is deleted, unless a repository's
//     schedule declaration gave it (its Via is the file): that one is
//     the schedule's own, and the declaration sync writes it again on
//     its next commit anyway. One of grain's own schedules keeps its
//     grants, which its reconciler keeps a copy of the template's on
//     purpose (orchestrator.applySystemContent).
//
// A schedule whose template is gone is left as it is: there is nothing
// to tell a copy against, and it cannot fire until somebody points it at
// another template.
//
// Only the columns schema 143 has are read, so this runs as written from
// any earlier store. Idempotent: a second pass finds nothing left that
// says no more than its template.
func migrateScheduleOwnSettings(ctx context.Context, tx *sql.Tx) error {
	type row struct {
		id, template string
		admission    Admission
		hosting      Hosting
	}
	var scheds []row
	if err := each(ctx, tx,
		"SELECT `id`,`template_id`,`admission`,`hosting` FROM `schedule` "+
			"WHERE `template_id` IS NOT NULL AND `template_id` <> '' ORDER BY `id`",
		nil, func(rows *sql.Rows) error {
			var r row
			var adm, host string
			if err := rows.Scan(&r.id, &r.template, &adm, &host); err != nil {
				return err
			}
			r.admission, r.hosting = Admission(adm), Hosting(host)
			scheds = append(scheds, r)
			return nil
		}); err != nil {
		return fmt.Errorf("reading template-backed schedules: %w", err)
	}
	for _, r := range scheds {
		var tmplAdmission Admission
		var tmplHosting Hosting
		system := IsSystemTemplateID(r.template)
		if system {
			tmpl := SystemTemplate(r.template, RepoRef{})
			if tmpl == nil {
				continue
			}
			tmplAdmission, tmplHosting = tmpl.Admission, tmpl.Hosting
		} else {
			var adm, host string
			err := tx.QueryRowContext(ctx,
				"SELECT `admission`,`hosting` FROM `template` WHERE `id` = ?", r.template).Scan(&adm, &host)
			if err == sql.ErrNoRows {
				continue
			}
			if err != nil {
				return fmt.Errorf("reading template %s of schedule %s: %w", r.template, r.id, err)
			}
			tmplAdmission, tmplHosting = Admission(adm), Hosting(host)
		}
		admission, hosting := r.admission, r.hosting
		if rank := admission.Rank(); rank >= 0 && rank <= tmplAdmission.Rank() {
			admission = ""
		}
		if rank := hosting.Rank(); rank >= 0 && rank <= tmplHosting.Rank() {
			hosting = ""
		}
		if admission != r.admission || hosting != r.hosting {
			if _, err := tx.ExecContext(ctx,
				"UPDATE `schedule` SET `admission` = ?, `hosting` = ? WHERE `id` = ?",
				string(admission), string(hosting), r.id); err != nil {
				return fmt.Errorf("clearing schedule %s's copy of its template's admission and hosting: %w", r.id, err)
			}
		}
		if system {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM `schedule_grant` WHERE `schedule_id` = ? AND `via` NOT LIKE '.grain/schedules/%' "+
				"AND `capability` IN (SELECT `capability` FROM `template_grant` WHERE `template_id` = ?)",
			r.id, r.template); err != nil {
			return fmt.Errorf("deleting schedule %s's copy of its template's grants: %w", r.id, err)
		}
	}
	return nil
}
