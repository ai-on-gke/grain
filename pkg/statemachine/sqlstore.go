package statemachine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const maxWriteAttempts = 5

// SQLStore persists machines in any database/sql database that supports the
// SQLite-compatible statements used by Grain's model store.
type SQLStore struct{ db *sql.DB }

func NewSQLStore(db *sql.DB) *SQLStore { return &SQLStore{db: db} }

// Init creates the two append-only runtime tables. It is safe to call at every
// process start.
func (s *SQLStore) Init(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS machine_instance (
	id TEXT NOT NULL PRIMARY KEY,
	definition TEXT NOT NULL,
	sequence INTEGER NOT NULL,
	state BLOB NOT NULL,
	accepts BLOB NOT NULL,
	lifecycle TEXT NOT NULL DEFAULT '',
	attention INTEGER NOT NULL DEFAULT 0,
	presentation BLOB NOT NULL DEFAULT '',
	inputs BLOB NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS machine_transition (
	instance_id TEXT NOT NULL,
	sequence INTEGER NOT NULL,
	definition TEXT NOT NULL,
	event BLOB NOT NULL,
	state BLOB NOT NULL,
	effects BLOB NOT NULL,
	accepts BLOB NOT NULL,
	lifecycle TEXT NOT NULL DEFAULT '',
	attention INTEGER NOT NULL DEFAULT 0,
	presentation BLOB NOT NULL DEFAULT '',
	inputs BLOB NOT NULL DEFAULT '',
	event_id TEXT,
	PRIMARY KEY (instance_id, sequence),
	FOREIGN KEY (instance_id) REFERENCES machine_instance(id)
);`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("initializing machine store: %w", err)
	}
	for _, col := range []struct {
		table  string
		column string
		decl   string
	}{
		{"machine_instance", "lifecycle", "TEXT NOT NULL DEFAULT ''"},
		{"machine_instance", "attention", "INTEGER NOT NULL DEFAULT 0"},
		{"machine_instance", "presentation", "BLOB NOT NULL DEFAULT ''"},
		{"machine_instance", "inputs", "BLOB NOT NULL DEFAULT ''"},
		{"machine_transition", "lifecycle", "TEXT NOT NULL DEFAULT ''"},
		{"machine_transition", "attention", "INTEGER NOT NULL DEFAULT 0"},
		{"machine_transition", "presentation", "BLOB NOT NULL DEFAULT ''"},
		{"machine_transition", "inputs", "BLOB NOT NULL DEFAULT ''"},
		{"machine_transition", "event_id", "TEXT"},
	} {
		if err := s.ensureColumn(ctx, col.table, col.column, col.decl); err != nil {
			return fmt.Errorf("migrating %s.%s: %w", col.table, col.column, err)
		}
	}
	if err := s.backfillEventIDs(ctx); err != nil {
		return fmt.Errorf("migrating machine_transition.event_id: %w", err)
	}
	// One transition per event per instance: what makes resubmitting an
	// event Submit already committed a no-op rather than a second
	// transition, even from a process whose own Applied check raced it.
	if _, err := s.db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS machine_transition_event
		ON machine_transition (instance_id, event_id)`); err != nil {
		return fmt.Errorf("indexing machine_transition.event_id: %w", err)
	}
	return nil
}

// backfillEventIDs fills event_id on transitions logged before it was a
// column, from the event each one already carries.
func (s *SQLStore) backfillEventIDs(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT instance_id, sequence, event FROM machine_transition WHERE event_id IS NULL`)
	if err != nil {
		return err
	}
	type pending struct {
		instance string
		sequence uint64
		eventID  string
	}
	var fill []pending
	for rows.Next() {
		var p pending
		var raw []byte
		var event Event
		if err := rows.Scan(&p.instance, &p.sequence, &raw); err != nil {
			rows.Close()
			return err
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			rows.Close()
			return fmt.Errorf("decoding event of %s#%d: %w", p.instance, p.sequence, err)
		}
		p.eventID = event.ID
		fill = append(fill, p)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range fill {
		if _, err := s.db.ExecContext(ctx, `UPDATE machine_transition SET event_id = ? WHERE instance_id = ? AND sequence = ?`,
			p.eventID, p.instance, p.sequence); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLStore) ensureColumn(ctx context.Context, table, column, decl string) error {
	rows, err := s.db.QueryContext(ctx, "SELECT `"+column+"` FROM `"+table+"` WHERE 1 = 0")
	if err == nil {
		return rows.Close()
	}
	_, err = s.db.ExecContext(ctx, "ALTER TABLE `"+table+"` ADD COLUMN `"+column+"` "+decl)
	return err
}

func (s *SQLStore) Create(ctx context.Context, instance Instance) error {
	accepts, err := json.Marshal(instance.Accepts)
	if err != nil {
		return fmt.Errorf("encoding accepts: %w", err)
	}
	presentation, err := json.Marshal(instance.Presentation)
	if err != nil {
		return fmt.Errorf("encoding presentation: %w", err)
	}
	inputs, err := json.Marshal(instance.Inputs)
	if err != nil {
		return fmt.Errorf("encoding inputs: %w", err)
	}
	attention := 0
	if instance.Attention {
		attention = 1
	}
	var lastErr error
	for attempt := 0; attempt < maxWriteAttempts; attempt++ {
		_, err = s.db.ExecContext(ctx, `INSERT INTO machine_instance
			(id, definition, sequence, state, accepts, lifecycle, attention, presentation, inputs) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			instance.ID, instance.Definition, instance.Sequence, []byte(instance.State), accepts,
			instance.Lifecycle, attention, presentation, inputs)
		if err == nil {
			return nil
		}
		if !isSerializationFailure(err) {
			return fmt.Errorf("creating machine instance %q: %w", instance.ID, err)
		}
		lastErr = err
		time.Sleep(time.Duration(10*(attempt+1)) * time.Millisecond)
	}
	return fmt.Errorf("creating machine instance %q: %w", instance.ID, lastErr)
}

func (s *SQLStore) Load(ctx context.Context, id string) (Instance, error) {
	var instance Instance
	var state, accepts, presentation, inputs []byte
	var attention int
	var lastErr error
	for attempt := 0; attempt < maxWriteAttempts; attempt++ {
		err := s.db.QueryRowContext(ctx, `SELECT id, definition, sequence, state, accepts, lifecycle, attention, presentation, inputs
			FROM machine_instance WHERE id = ?`, id).Scan(
			&instance.ID, &instance.Definition, &instance.Sequence, &state, &accepts,
			&instance.Lifecycle, &attention, &presentation, &inputs)
		if errors.Is(err, sql.ErrNoRows) {
			return Instance{}, ErrInstanceNotFound
		}
		if err == nil {
			instance.State = json.RawMessage(state)
			instance.Attention = attention != 0
			if err := json.Unmarshal(accepts, &instance.Accepts); err != nil {
				return Instance{}, fmt.Errorf("decoding accepts for machine instance %q: %w", id, err)
			}
			if len(presentation) > 0 && string(presentation) != "null" {
				if err := json.Unmarshal(presentation, &instance.Presentation); err != nil {
					return Instance{}, fmt.Errorf("decoding presentation for machine instance %q: %w", id, err)
				}
			}
			if len(inputs) > 0 && string(inputs) != "null" {
				if err := json.Unmarshal(inputs, &instance.Inputs); err != nil {
					return Instance{}, fmt.Errorf("decoding inputs for machine instance %q: %w", id, err)
				}
			}
			return instance, nil
		}
		if !isSerializationFailure(err) {
			return Instance{}, fmt.Errorf("loading machine instance %q: %w", id, err)
		}
		lastErr = err
		time.Sleep(time.Duration(10*(attempt+1)) * time.Millisecond)
	}
	return Instance{}, fmt.Errorf("loading machine instance %q: %w", id, lastErr)
}

func (s *SQLStore) Append(ctx context.Context, expected uint64, tr Transition) error {
	if tr.Sequence != expected+1 {
		return errors.New("transition sequence is not the next sequence")
	}
	event, err := json.Marshal(tr.Event)
	if err != nil {
		return fmt.Errorf("encoding event: %w", err)
	}
	effects, err := json.Marshal(tr.Effects)
	if err != nil {
		return fmt.Errorf("encoding effects: %w", err)
	}
	accepts, err := json.Marshal(tr.Accepts)
	if err != nil {
		return fmt.Errorf("encoding accepts: %w", err)
	}
	presentation, err := json.Marshal(tr.Presentation)
	if err != nil {
		return fmt.Errorf("encoding presentation: %w", err)
	}
	inputs, err := json.Marshal(tr.Inputs)
	if err != nil {
		return fmt.Errorf("encoding inputs: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt < maxWriteAttempts; attempt++ {
		err := s.appendOnce(ctx, expected, tr, event, effects, accepts, presentation, inputs)
		if err == nil {
			return nil
		}
		if !isSerializationFailure(err) {
			return err
		}
		lastErr = err
		time.Sleep(time.Duration(10*(attempt+1)) * time.Millisecond)
	}
	return fmt.Errorf("appending machine transition: %w", lastErr)
}

func (s *SQLStore) appendOnce(ctx context.Context, expected uint64, tr Transition, event, effects, accepts, presentation, inputs []byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning machine transition: %w", err)
	}
	defer tx.Rollback()
	attention := 0
	if tr.Attention {
		attention = 1
	}
	result, err := tx.ExecContext(ctx, `UPDATE machine_instance
		SET sequence = ?, state = ?, accepts = ?, lifecycle = ?, attention = ?, presentation = ?, inputs = ?
		WHERE id = ? AND definition = ? AND sequence = ?`,
		tr.Sequence, []byte(tr.State), accepts, tr.Lifecycle, attention, presentation, inputs,
		tr.InstanceID, tr.Definition, expected)
	if err != nil {
		return fmt.Errorf("updating machine snapshot: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking machine snapshot update: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("stale machine sequence or definition for %q", tr.InstanceID)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO machine_transition
		(instance_id, sequence, definition, event, state, effects, accepts, lifecycle, attention, presentation, inputs, event_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, tr.InstanceID, tr.Sequence, tr.Definition,
		event, []byte(tr.State), effects, accepts, tr.Lifecycle, attention, presentation, inputs, tr.Event.ID); err != nil {
		return fmt.Errorf("appending machine transition: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing machine transition: %w", err)
	}
	return nil
}

// Applied reports whether the instance's log already holds a transition
// for the event id.
func (s *SQLStore) Applied(ctx context.Context, instance, eventID string) (bool, error) {
	var lastErr error
	for attempt := 0; attempt < maxWriteAttempts; attempt++ {
		var one int
		err := s.db.QueryRowContext(ctx, `SELECT 1 FROM machine_transition WHERE instance_id = ? AND event_id = ?`,
			instance, eventID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err == nil {
			return true, nil
		}
		if !isSerializationFailure(err) {
			return false, fmt.Errorf("looking up event %q for machine instance %q: %w", eventID, instance, err)
		}
		lastErr = err
		time.Sleep(time.Duration(10*(attempt+1)) * time.Millisecond)
	}
	return false, fmt.Errorf("looking up event %q for machine instance %q: %w", eventID, instance, lastErr)
}

// Log returns an instance's complete transition history in sequence order.
func (s *SQLStore) Log(ctx context.Context, id string) ([]Transition, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT sequence, definition, event, state, effects, accepts, lifecycle, attention, presentation, inputs
		FROM machine_transition WHERE instance_id = ? ORDER BY sequence`, id)
	if err != nil {
		return nil, fmt.Errorf("reading machine log: %w", err)
	}
	defer rows.Close()
	var result []Transition
	for rows.Next() {
		var tr Transition
		var event, state, effects, accepts, presentation, inputs []byte
		var attention int
		tr.InstanceID = id
		if err := rows.Scan(&tr.Sequence, &tr.Definition, &event, &state, &effects, &accepts,
			&tr.Lifecycle, &attention, &presentation, &inputs); err != nil {
			return nil, fmt.Errorf("scanning machine log: %w", err)
		}
		tr.State = json.RawMessage(state)
		tr.Attention = attention != 0
		if err := json.Unmarshal(event, &tr.Event); err != nil {
			return nil, fmt.Errorf("decoding logged event: %w", err)
		}
		if err := json.Unmarshal(effects, &tr.Effects); err != nil {
			return nil, fmt.Errorf("decoding logged effects: %w", err)
		}
		if err := json.Unmarshal(accepts, &tr.Accepts); err != nil {
			return nil, fmt.Errorf("decoding logged accepts: %w", err)
		}
		if len(presentation) > 0 && string(presentation) != "null" {
			if err := json.Unmarshal(presentation, &tr.Presentation); err != nil {
				return nil, fmt.Errorf("decoding logged presentation: %w", err)
			}
		}
		if len(inputs) > 0 && string(inputs) != "null" {
			if err := json.Unmarshal(inputs, &tr.Inputs); err != nil {
				return nil, fmt.Errorf("decoding logged inputs: %w", err)
			}
		}
		result = append(result, tr)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading machine log: %w", err)
	}
	return result, nil
}

// Pins counts one repository's instances against one version.
func (s *SQLStore) Pins(ctx context.Context, repo, version string) (Pins, error) {
	var pins Pins
	repoPrefix := repo + "@%"
	var lastErr error
	for attempt := 0; attempt < maxWriteAttempts; attempt++ {
		err := s.db.QueryRowContext(ctx, `SELECT
			COUNT(CASE WHEN definition = ? THEN 1 END),
			COUNT(CASE WHEN definition != ? AND definition LIKE ? THEN 1 END),
			COUNT(CASE WHEN definition = ? AND lifecycle = 'active' AND attention != 0 THEN 1 END)
			FROM machine_instance`,
			version, version, repoPrefix, version).Scan(&pins.Here, &pins.Older, &pins.Attention)
		if err == nil {
			return pins, nil
		}
		if !isSerializationFailure(err) {
			return Pins{}, fmt.Errorf("counting pins for repo %q version %q: %w", repo, version, err)
		}
		lastErr = err
		time.Sleep(time.Duration(10*(attempt+1)) * time.Millisecond)
	}
	return Pins{}, fmt.Errorf("counting pins for repo %q version %q: %w", repo, version, lastErr)
}

func isSerializationFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, marker := range []string{
		"database is locked",
		"SQLITE_BUSY",
		"40001",
		"40P01",
		"55P03",
		"could not serialize access",
		"deadlock detected",
		"lock timeout",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
