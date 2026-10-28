package statemachine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// dedupStores is every Store, fresh, so the resubmission tests hold for
// both rather than only for the one an engine test happens to use.
func dedupStores(t *testing.T) map[string]func() (Store, func(string) int) {
	return map[string]func() (Store, func(string) int){
		"memory": func() (Store, func(string) int) {
			store := NewMemoryStore()
			return store, func(id string) int { return len(store.Transitions(id)) }
		},
		"sql": func() (Store, func(string) int) {
			db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "machines.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			store := NewSQLStore(db)
			if err := store.Init(context.Background()); err != nil {
				t.Fatal(err)
			}
			return store, func(id string) int {
				log, err := store.Log(context.Background(), id)
				if err != nil {
					t.Fatal(err)
				}
				return len(log)
			}
		},
	}
}

// A caller that lost the answer to a Submit that committed sends the same
// event again. That must not apply it, or dispatch its effects, twice --
// even though the transition it caused no longer accepts its type.
func TestEngineSubmitIsIdempotentByEventID(t *testing.T) {
	for name, open := range dedupStores(t) {
		t.Run(name, func(t *testing.T) {
			store, transitions := open()
			reduces, dispatches := 0, 0
			engine := Engine{Store: store,
				Definitions: DefinitionFunc(func(context.Context, string) (Reducer, error) {
					return ReducerFunc(func(context.Context, json.RawMessage, Event) (Result, error) {
						reduces++
						return Result{State: json.RawMessage(`{"phase":"running"}`),
							Effects: []Effect{{Type: "agent.run"}}, Accepts: []string{"agent.finished"}}, nil
					}), nil
				}),
				Executor: ExecutorFunc(func(context.Context, EffectIntent) error { dispatches++; return nil }),
			}
			ctx := context.Background()
			if _, err := engine.Create(ctx, "one", "v1", json.RawMessage(`{}`), []string{"sandbox.ready"}); err != nil {
				t.Fatal(err)
			}
			event := Event{SpecVersion: "1.0", ID: "evt-1", Source: "test://grain", Type: "sandbox.ready"}
			first, err := engine.Submit(ctx, "one", event)
			if err != nil {
				t.Fatal(err)
			}
			again, err := engine.Submit(ctx, "one", event)
			if err != nil {
				t.Fatalf("resubmitting a committed event: %v", err)
			}
			if again.Sequence != first.Sequence || string(again.State) != string(first.State) {
				t.Fatalf("resubmission moved the instance: %#v, then %#v", first, again)
			}
			if n := transitions("one"); n != 1 {
				t.Errorf("transitions = %d, want 1", n)
			}
			if reduces != 1 || dispatches != 1 {
				t.Errorf("reduces = %d, dispatches = %d; want 1 and 1", reduces, dispatches)
			}
		})
	}
}

// An event its reducer could not handle is consumed by the reducer.failed
// it became; sending it again must not produce a second one.
func TestEngineSubmitDeduplicatesAnEventConsumedByReducerFailed(t *testing.T) {
	for name, open := range dedupStores(t) {
		t.Run(name, func(t *testing.T) {
			store, transitions := open()
			reduces := 0
			engine := Engine{Store: store, Definitions: DefinitionFunc(func(context.Context, string) (Reducer, error) {
				return ReducerFunc(func(_ context.Context, _ json.RawMessage, event Event) (Result, error) {
					reduces++
					if event.Type == "break" {
						return Result{}, errors.New("boom")
					}
					return Result{State: json.RawMessage(`{"failed":true}`), Accepts: []string{"break"}}, nil
				}), nil
			})}
			ctx := context.Background()
			if _, err := engine.Create(ctx, "one", "v1", json.RawMessage(`{}`), []string{"break"}); err != nil {
				t.Fatal(err)
			}
			event := Event{ID: "e", Source: "test://grain", Type: "break"}
			for i := 0; i < 2; i++ {
				if _, err := engine.Submit(ctx, "one", event); err != nil {
					t.Fatalf("submit %d: %v", i, err)
				}
			}
			if n := transitions("one"); n != 1 {
				t.Errorf("transitions = %d, want the one reducer.failed", n)
			}
			if reduces != 2 {
				t.Errorf("reduces = %d, want 2 (the event and its reducer.failed)", reduces)
			}
		})
	}
}

// The store refuses a second transition for one event even from a writer
// that never asked Applied -- another process that raced the check.
func TestStoreAppendRefusesAnAppliedEventID(t *testing.T) {
	for name, open := range dedupStores(t) {
		t.Run(name, func(t *testing.T) {
			store, transitions := open()
			ctx := context.Background()
			if err := store.Create(ctx, Instance{ID: "one", Definition: "v1", State: json.RawMessage(`{}`)}); err != nil {
				t.Fatal(err)
			}
			tr := func(seq uint64) Transition {
				return Transition{InstanceID: "one", Definition: "v1", Sequence: seq,
					Event: Event{ID: "evt-1", Source: "s", Type: "t"}, State: json.RawMessage(`{}`)}
			}
			if err := store.Append(ctx, 0, tr(1)); err != nil {
				t.Fatal(err)
			}
			if err := store.Append(ctx, 1, tr(2)); err == nil {
				t.Fatal("appended a second transition for evt-1")
			}
			if n := transitions("one"); n != 1 {
				t.Errorf("transitions = %d, want 1", n)
			}
			if applied, err := store.Applied(ctx, "one", "evt-1"); err != nil || !applied {
				t.Errorf("Applied(evt-1) = %v, %v", applied, err)
			}
			if applied, err := store.Applied(ctx, "other", "evt-1"); err != nil || applied {
				t.Errorf("Applied on another instance = %v, %v", applied, err)
			}
		})
	}
}

// A log written before event_id was a column is backfilled at Init, so
// dedup covers the events it already holds.
func TestSQLStoreInitBackfillsEventIDs(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "machines.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
CREATE TABLE machine_instance (id TEXT NOT NULL PRIMARY KEY, definition TEXT NOT NULL,
	sequence INTEGER NOT NULL, state BLOB NOT NULL, accepts BLOB NOT NULL);
CREATE TABLE machine_transition (instance_id TEXT NOT NULL, sequence INTEGER NOT NULL,
	definition TEXT NOT NULL, event BLOB NOT NULL, state BLOB NOT NULL, effects BLOB NOT NULL,
	accepts BLOB NOT NULL, PRIMARY KEY (instance_id, sequence));
INSERT INTO machine_instance VALUES ('one', 'v1', 1, '{}', '[]');
INSERT INTO machine_transition VALUES ('one', 1, 'v1', '{"id":"old-evt","source":"s","type":"t"}', '{}', '[]', '[]');`); err != nil {
		t.Fatal(err)
	}
	store := NewSQLStore(db)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if applied, err := store.Applied(ctx, "one", "old-evt"); err != nil || !applied {
		t.Fatalf("Applied(old-evt) after migration = %v, %v", applied, err)
	}
	if err := store.Init(ctx); err != nil {
		t.Fatalf("second Init: %v", err)
	}
}
