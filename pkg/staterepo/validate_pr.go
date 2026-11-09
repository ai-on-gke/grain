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

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The pre-review validation of a config pull request
// (design/design/config-repo-settings.design.md, section 4.4): what a candidate
// grain/config tree has to pass before a person is asked to look at it,
// let alone merge it. Each stage answers one question:
//
//  1. Check -- does it load at all: the schema stamp, every file's
//     syntax with unknown fields refused, foreign keys on, and the
//     vocabulary warnings Check reports.
//  2. Capability catalogue consistency -- does settings/capabilities.yml
//     agree with the templates, schedules and defaults that name its
//     capabilities, with no plaintext secret in it (Check runs it, on
//     the tree as it is, before anything is imported).
//  3. CheckDispatchLiveness -- would merging it lock dispatch: no model
//     to run on, no director to talk to, no run allowed at all.
//  4. RehearseApply -- does the change apply to the deployment as it
//     runs now, the way a merge applies it, rather than to an empty
//     database.
//
// And one warning that is about the deployment rather than the tree: an
// edit to the content of one of grain's own schedules
// (systemschedules.go), which merges and is then undone by the next
// reconcile cycle. A warning rather than a refusal, so the card and the
// proposal's result say it before a person approves -- the same
// sentence the import writes afterwards, said while it can still change
// the decision.

// CandidateReport is what ValidateCandidateConfig found.
type CandidateReport struct {
	// Check is the load stage's report: rows per table, and the
	// warnings that do not refuse the change but a reviewer should read.
	Check CheckReport `json:"check"`
	// Liveness lists what would stop dispatch, empty when nothing would.
	Liveness []string `json:"liveness,omitempty"`
}

// ErrDispatchLocked is what ValidateCandidateConfig refuses a change
// with when merging it would leave dispatch unable to run anything.
var ErrDispatchLocked = errors.New("staterepo: the change would lock dispatch")

// ValidateCandidateConfig runs the whole pre-review suite over the
// candidate tree candidateDir, using liveDB -- the database the
// deployment is running on, which is never written -- for the schema to
// check against and the state to rehearse the apply on. Both run on
// snapshots of it taken here and removed afterwards.
func ValidateCandidateConfig(ctx context.Context, liveDB *sql.DB, candidateDir string) (CandidateReport, error) {
	var report CandidateReport
	scratch, err := os.MkdirTemp("", "grain-config-validate-")
	if err != nil {
		return report, err
	}
	defer os.RemoveAll(scratch)

	// 1 and 2. Check imports into what it is given, so it is given a
	// copy: replacing every row of that is what makes it a check.
	checkDB, err := snapshotDB(ctx, liveDB, filepath.Join(scratch, "check.db"))
	if err != nil {
		return report, err
	}
	defer checkDB.Close()
	report.Check, err = Check(ctx, checkDB, candidateDir, model.SchemaVersion)
	if err != nil {
		return report, fmt.Errorf("checking the candidate configuration: %w", err)
	}
	// Against liveDB, which only reads it: checkDB already holds the
	// candidate, so there would be nothing left to compare.
	edits, err := systemScheduleEditsFromApplying(ctx, liveDB, candidateDir)
	if err != nil {
		return report, err
	}
	for _, e := range edits {
		report.Check.Warnings = append(report.Check.Warnings, fmt.Sprintf(
			"%s edits content grain owns on its own schedule; those columns are a cache of the "+
				"template this build ships, so the next reconcile cycle writes the template back and "+
				"the change is undone rather than kept -- change the template in grain's source instead", e))
	}
	// 3.
	report.Liveness, err = CheckDispatchLiveness(ctx, checkDB)
	if err != nil {
		return report, err
	}
	if len(report.Liveness) > 0 {
		return report, fmt.Errorf("%w: %s", ErrDispatchLocked, strings.Join(report.Liveness, "; "))
	}
	// 4.
	if err := RehearseApply(ctx, liveDB, candidateDir); err != nil {
		return report, err
	}
	return report, nil
}

// CheckDispatchLiveness reports what in db's configuration would stop
// dispatch running anything: the default model or the director's served
// by no enabled connection, the director session gone, or no run allowed
// at all. db holds the configuration to judge -- a candidate tree
// imported into a throwaway database, as ValidateCandidateConfig does.
//
// A model is judged only against connections that say which models they
// serve: a discovering connection whose provider has not been asked yet
// advertises nothing, and is not evidence that anything is missing.
func CheckDispatchLiveness(ctx context.Context, db *sql.DB) ([]string, error) {
	cfg, err := model.New(db).GetConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("staterepo: reading the candidate settings: %w", err)
	}
	// No row is a deployment the daemon seeds at its first start.
	if cfg == nil {
		return nil, nil
	}
	var out []string
	if cfg.MaxWorkers < 1 {
		out = append(out, fmt.Sprintf("max_parallel is %d, which lets no run start", cfg.MaxWorkers))
	}
	var enabled []model.AgentBackend
	for _, b := range cfg.AgentBackends {
		if !b.Disabled {
			enabled = append(enabled, b)
		}
	}
	if len(cfg.AgentBackends) > 0 && len(enabled) == 0 {
		out = append(out, "every model connection is disabled, so no run has anything to run on")
	}
	if msg := unservedModels("default_model", cfg.DefaultModels(), enabled); msg != "" {
		out = append(out, msg)
	}
	sessions := model.ConsoleSessionsOr(cfg.ConsoleSessions)
	var director *model.ConsoleSession
	for i := range sessions {
		if sessions[i].ID == model.DirectorSessionID {
			director = &sessions[i]
		}
	}
	switch {
	case cfg.ConsoleSessions != nil && director == nil:
		out = append(out, fmt.Sprintf("console_sessions no longer has the %q session", model.DirectorSessionID))
	case director != nil && strings.TrimSpace(director.Model) != "":
		if msg := unservedModels("director_model", []string{director.Model}, enabled); msg != "" {
			out = append(out, msg)
		}
	}
	// A director session naming a grant grain's built-in Tier 3 floor
	// refuses would be held at dispatch (orchestrator.catalogueHold)
	// whatever settings/capabilities.yml says.
	if director != nil {
		for _, capID := range director.Capabilities {
			if r := model.DirectorFloorRefusal(capID, model.CatalogueKindDirector); r != nil {
				out = append(out, fmt.Sprintf("console_sessions.%s: %s, so dispatch would hold the director", model.DirectorSessionID, r.Reason))
			}
		}
	}
	return out, nil
}

// unservedModels says that none of models is served by an enabled
// connection, or is empty when one is or when no connection says what
// it serves.
func unservedModels(setting string, models []string, enabled []model.AgentBackend) string {
	if len(models) == 0 {
		return ""
	}
	known := false
	for _, b := range enabled {
		if !b.HasModels() {
			continue
		}
		known = true
		for _, m := range models {
			if b.SupportsModel(m) {
				return ""
			}
		}
	}
	if !known {
		return ""
	}
	return fmt.Sprintf("%s %s is served by no enabled connection", setting, strings.Join(models, ", "))
}

// RehearseApply applies candidateDir the way a merged config change is
// applied to a running deployment (ImportTables over SettingsTables and
// the memory tables, as Apply does) -- to a snapshot of liveDB, thrown
// away afterwards, so the deployment's own database is never touched.
// It fails where the real apply would.
func RehearseApply(ctx context.Context, liveDB *sql.DB, candidateDir string) error {
	scratch, err := os.MkdirTemp("", "grain-config-rehearse-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	db, err := snapshotDB(ctx, liveDB, filepath.Join(scratch, "apply.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return fmt.Errorf("staterepo: enabling foreign keys for the rehearsed apply: %w", err)
	}
	tables := append(append([]string{}, SettingsTables...), memoryTables...)
	if err := ImportTables(ctx, db, candidateDir, tables); err != nil {
		return fmt.Errorf("rehearsing the apply of the candidate configuration: %w", err)
	}
	return nil
}

// snapshotDB copies liveDB to path (SQLite's VACUUM INTO, which reads
// it in one transaction and writes nothing to it) and opens the copy
// with liveDB's own driver.
func snapshotDB(ctx context.Context, liveDB *sql.DB, path string) (*sql.DB, error) {
	if _, err := liveDB.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return nil, fmt.Errorf("staterepo: snapshotting the database to validate against: %w", err)
	}
	drv := liveDB.Driver()
	if dc, ok := drv.(driver.DriverContext); ok {
		conn, err := dc.OpenConnector(path)
		if err != nil {
			return nil, fmt.Errorf("staterepo: opening the snapshot: %w", err)
		}
		return sql.OpenDB(conn), nil
	}
	return sql.OpenDB(dsnConnector{drv: drv, dsn: path}), nil
}

type dsnConnector struct {
	drv driver.Driver
	dsn string
}

func (c dsnConnector) Connect(context.Context) (driver.Conn, error) { return c.drv.Open(c.dsn) }
func (c dsnConnector) Driver() driver.Driver                        { return c.drv }
