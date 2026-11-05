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

package daemon

// highwater.go keeps a record, outside the database, of how far the
// store this data directory's daemon writes to has got: the last task id
// it handed out and the last run it started. It only ever moves forward.
//
// It is what lets a start notice that the store it opened is older than
// one a previous daemon wrote to. On 2026-11-04 a daemon restarted onto
// a copy of its store taken twelve hours earlier; nothing objected, and
// it handed out again task ids that pull requests, branches and messages
// already named. With this file, that start refuses (CheckStoreHighWater)
// and says which store it opened, how far behind it is and which other
// stores the host holds; and a start an operator lets through anyway
// (GRAIN_ACCEPT_STORE_BEHIND) seeds the task id sequence past the mark
// (seedTaskSequence), so no id is ever issued twice.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// StoreHighWaterFile is the mark's file under the data directory.
const StoreHighWaterFile = "store-highwater.json"

// AcceptStoreBehindEnv, set to 1, lets a daemon start on a store behind
// the mark. Task ids still continue from the mark.
const AcceptStoreBehindEnv = "GRAIN_ACCEPT_STORE_BEHIND"

// storeHighWaterInterval is how often a running daemon reads its store's
// marks and moves the file forward.
const storeHighWaterInterval = 5 * time.Second

// runTimeSlack absorbs the precision a timestamp loses being copied
// between drivers (SQLite keeps nanoseconds, PostgreSQL microseconds).
const runTimeSlack = time.Second

// StoreHighWater is the mark.
type StoreHighWater struct {
	// LastTaskID is the highest task id the store had handed out.
	LastTaskID int64 `json:"lastTaskId"`
	// LastRunID and LastRunAt name the run most recently started.
	LastRunID string    `json:"lastRunId,omitempty"`
	LastRunAt time.Time `json:"lastRunAt,omitempty"`
	// LastWriteAt is when the mark last moved forward: about when the
	// daemon last allocated a task or started a run.
	LastWriteAt time.Time `json:"lastWriteAt,omitempty"`
	// Store names the store the mark was last moved by (StoreDescription).
	Store string `json:"store,omitempty"`
}

// ReadStoreHighWater reads dataDir's mark; nil when there is none yet.
func ReadStoreHighWater(dataDir string) (*StoreHighWater, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, StoreHighWaterFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var hw StoreHighWater
	if err := json.Unmarshal(raw, &hw); err != nil {
		return nil, fmt.Errorf("reading %s: %w", filepath.Join(dataDir, StoreHighWaterFile), err)
	}
	return &hw, nil
}

func writeStoreHighWater(dataDir string, hw StoreHighWater) error {
	raw, err := json.MarshalIndent(hw, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dataDir, StoreHighWaterFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readStoreMarks reads the store's own answer to the mark's questions.
func readStoreMarks(ctx context.Context, db *sql.DB) (StoreHighWater, error) {
	var hw StoreHighWater
	var maxTask sql.NullInt64
	if err := db.QueryRowContext(ctx, "SELECT MAX(`number`) FROM `task_sequence`").Scan(&maxTask); err != nil {
		return hw, fmt.Errorf("reading the last task id: %w", err)
	}
	hw.LastTaskID = maxTask.Int64
	var (
		runID string
		runAt time.Time
	)
	err := db.QueryRowContext(ctx, "SELECT `id`, `started_at` FROM `task_run` ORDER BY `started_at` DESC LIMIT 1").Scan(&runID, &runAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return hw, fmt.Errorf("reading the last run: %w", err)
	default:
		hw.LastRunID, hw.LastRunAt = runID, runAt.UTC()
	}
	return hw, nil
}

// behind says how store falls short of mark, or nothing when it does not.
func (mark StoreHighWater) behind(store StoreHighWater) []string {
	var out []string
	if store.LastTaskID < mark.LastTaskID {
		out = append(out, fmt.Sprintf("its last task is #%d, but task ids up to #%d were handed out (%d task(s) missing)",
			store.LastTaskID, mark.LastTaskID, mark.LastTaskID-store.LastTaskID))
	}
	if !mark.LastRunAt.IsZero() && store.LastRunAt.Add(runTimeSlack).Before(mark.LastRunAt) {
		last := "no run at all"
		if !store.LastRunAt.IsZero() {
			last = "its last run started " + store.LastRunAt.UTC().Format(time.RFC3339)
		}
		out = append(out, fmt.Sprintf("%s, but run %s started %s (%s of runs missing)",
			last, mark.LastRunID, mark.LastRunAt.UTC().Format(time.RFC3339),
			mark.LastRunAt.Sub(store.LastRunAt).Round(time.Second)))
	}
	return out
}

// StoreDescription names a store for a person: the driver and where it is.
func StoreDescription(driver, dsn, dataDir string) string {
	switch driver {
	case "postgres":
		if dsn == "" {
			return "PostgreSQL (the embedded server under " + dataDir + ")"
		}
		return "PostgreSQL at " + sanitizedDSN(dsn)
	default:
		path := dsn
		if path == "" {
			path = filepath.Join(dataDir, "store")
		}
		return "SQLite at " + path
	}
}

// otherStores lists the stores present under dataDir besides the one
// described by driver/dsn: the SQLite store and every PostgreSQL data
// directory (postgres, and the postgres.* an operator set aside).
func otherStores(driver, dsn, dataDir string) []string {
	var out []string
	if driver == "postgres" || (dsn != "" && dsn != filepath.Join(dataDir, "store")) {
		if HasSQLiteStore(dataDir) {
			out = append(out, "SQLite at "+filepath.Join(dataDir, "store"))
		}
	}
	matches, _ := filepath.Glob(filepath.Join(dataDir, "postgres*"))
	for _, m := range matches {
		if _, err := os.Stat(filepath.Join(m, "PG_VERSION")); err != nil {
			continue
		}
		out = append(out, "a PostgreSQL data directory at "+m)
	}
	return out
}

// CheckStoreHighWater compares the store db holds with dataDir's mark.
// A store behind it is refused, unless AcceptStoreBehindEnv is set; then
// the task id sequence is seeded past the mark instead. Either way, a
// store at or past the mark has the mark moved up to it.
func CheckStoreHighWater(ctx context.Context, db *sql.DB, driver, dsn, dataDir string) error {
	if dataDir == "" {
		return nil
	}
	mark, err := ReadStoreHighWater(dataDir)
	if err != nil {
		return err
	}
	store, err := readStoreMarks(ctx, db)
	if err != nil {
		return fmt.Errorf("checking the store against %s: %w", StoreHighWaterFile, err)
	}
	desc := StoreDescription(driver, dsn, dataDir)
	if mark == nil {
		store.Store, store.LastWriteAt = desc, time.Now().UTC()
		return writeStoreHighWater(dataDir, store)
	}
	if reasons := mark.behind(store); len(reasons) > 0 {
		msg := storeBehindMessage(desc, *mark, reasons, otherStores(driver, dsn, dataDir), dataDir)
		if os.Getenv(AcceptStoreBehindEnv) != "1" {
			return errors.New("refusing to start: " + msg)
		}
		log.Printf("grain daemon: ERROR: %s -- starting anyway because %s=1; task ids continue from #%d",
			msg, AcceptStoreBehindEnv, mark.LastTaskID+1)
		if err := seedTaskSequence(ctx, db, driver, mark.LastTaskID); err != nil {
			return err
		}
		// The run half is accepted as lost; the task id half stays, so
		// no id the lost store issued is issued again.
		mark.LastRunID, mark.LastRunAt = store.LastRunID, store.LastRunAt
	}
	return advanceStoreHighWater(dataDir, *mark, store, desc)
}

func storeBehindMessage(desc string, mark StoreHighWater, reasons, others []string, dataDir string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "the store this daemon opens, %s, is behind the writes a previous daemon on %s made (%s, last moved %s by %s): %s.",
		desc, dataDir, StoreHighWaterFile, mark.LastWriteAt.UTC().Format(time.RFC3339), orUnknown(mark.Store), strings.Join(reasons, "; "))
	if len(others) > 0 {
		fmt.Fprintf(&b, " Other stores on this host: %s.", strings.Join(others, "; "))
	} else {
		b.WriteString(" No other store was found under the data directory.")
	}
	fmt.Fprintf(&b, " Starting here would lose those writes and hand their task ids out again. To recover, point the daemon at the newer store "+
		"(db-config.json or GRAIN_DB_DRIVER/GRAIN_DB_DSN), or copy the newer store into this one with the daemon stopped "+
		"(docs/database.md, \"Moving a store\"). If the newer store is gone for good, start once with %s=1: task ids then continue from #%d.",
		AcceptStoreBehindEnv, mark.LastTaskID+1)
	return b.String()
}

func orUnknown(s string) string {
	if s == "" {
		return "an unnamed store"
	}
	return s
}

// advanceStoreHighWater writes the larger of mark and store, field by
// field, when that moves the mark at all.
func advanceStoreHighWater(dataDir string, mark, store StoreHighWater, desc string) error {
	next, moved := mark, false
	if store.LastTaskID > next.LastTaskID {
		next.LastTaskID, moved = store.LastTaskID, true
	}
	if store.LastRunAt.After(next.LastRunAt) {
		next.LastRunID, next.LastRunAt, moved = store.LastRunID, store.LastRunAt, true
	}
	if next.Store != desc {
		next.Store, moved = desc, true
	}
	if !moved {
		return nil
	}
	next.LastWriteAt = time.Now().UTC()
	return writeStoreHighWater(dataDir, next)
}

// seedTaskSequence makes the store's next task id greater than floor.
// A row is written at floor itself, which moves SQLite's AUTOINCREMENT;
// PostgreSQL's sequence is not moved by an explicit value, so it is set
// as well.
func seedTaskSequence(ctx context.Context, db *sql.DB, driver string, floor int64) error {
	if floor <= 0 {
		return nil
	}
	var maxTask sql.NullInt64
	if err := db.QueryRowContext(ctx, "SELECT MAX(`number`) FROM `task_sequence`").Scan(&maxTask); err != nil {
		return fmt.Errorf("reading the task id sequence: %w", err)
	}
	if maxTask.Int64 >= floor {
		return nil
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO `task_sequence` (`number`, `issued_at`) VALUES (?, ?)", floor, time.Now().UTC()); err != nil {
		return fmt.Errorf("seeding the task id sequence past #%d: %w", floor, err)
	}
	if driver == "postgres" {
		if _, err := db.ExecContext(ctx, "SELECT setval(pg_get_serial_sequence('task_sequence', 'number'), "+strconv.FormatInt(floor, 10)+")"); err != nil {
			return fmt.Errorf("seeding the task id sequence past #%d: %w", floor, err)
		}
	}
	log.Printf("grain daemon: task ids continue from #%d, past the last one %s records", floor+1, StoreHighWaterFile)
	return nil
}

// watchStoreHighWater moves the mark forward as the daemon writes, until
// ctx ends, and once more on the way out.
func watchStoreHighWater(ctx context.Context, db *sql.DB, desc, dataDir string) {
	if dataDir == "" {
		return
	}
	update := func(ctx context.Context) {
		store, err := readStoreMarks(ctx, db)
		if err != nil {
			return
		}
		mark, err := ReadStoreHighWater(dataDir)
		if err != nil {
			log.Printf("grain daemon: reading %s: %v", StoreHighWaterFile, err)
			return
		}
		if mark == nil {
			mark = &StoreHighWater{}
		}
		if err := advanceStoreHighWater(dataDir, *mark, store, desc); err != nil {
			log.Printf("grain daemon: moving %s forward: %v", StoreHighWaterFile, err)
		}
	}
	t := time.NewTicker(storeHighWaterInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			final, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			update(final)
			cancel()
			return
		case <-t.C:
			update(ctx)
		}
	}
}

// StoreCheck is CheckStore's answer.
type StoreCheck struct {
	// Store names the store dataDir selects, and Marks is what it holds.
	Store string
	Marks StoreHighWater
	// Mark is dataDir's high-water mark, nil when there is none yet.
	Mark *StoreHighWater
	// Others are the other stores under dataDir.
	Others []string
	// Behind is empty when the store is at or past the mark; otherwise
	// Message says how far behind it is and how to recover.
	Behind  []string
	Message string
}

// storeTableExists reports whether db already holds task_sequence, so
// CheckStore (which opens db raw, without Init) reads an uninitialized
// database as empty rather than failing on a missing table.
func storeTableExists(ctx context.Context, db *sql.DB, driver string) (bool, error) {
	if driver == "postgres" {
		var exists bool
		err := db.QueryRowContext(ctx,
			"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'task_sequence')").
			Scan(&exists)
		return exists, err
	}
	var count int
	err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'task_sequence'").
		Scan(&count)
	return count > 0, err
}

// CheckStore is CheckStoreHighWater without the writes: what `grain db
// check` and setup.sh ask before a daemon is started on db.
func CheckStore(ctx context.Context, db *sql.DB, driver, dsn, dataDir string) (*StoreCheck, error) {
	mark, err := ReadStoreHighWater(dataDir)
	if err != nil {
		return nil, err
	}
	var store StoreHighWater
	hasTable, err := storeTableExists(ctx, db, driver)
	if err != nil {
		return nil, err
	}
	if hasTable {
		store, err = readStoreMarks(ctx, db)
		if err != nil {
			return nil, err
		}
	}
	out := &StoreCheck{Store: StoreDescription(driver, dsn, dataDir), Marks: store, Mark: mark, Others: otherStores(driver, dsn, dataDir)}
	if mark != nil {
		out.Behind = mark.behind(store)
		if len(out.Behind) > 0 {
			out.Message = storeBehindMessage(out.Store, *mark, out.Behind, out.Others, dataDir)
		}
	}
	return out, nil
}
