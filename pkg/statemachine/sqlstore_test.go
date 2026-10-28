package statemachine

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestSQLStorePersistsSnapshotsAndAppendOnlyLog(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "machines.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewSQLStore(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	instance := Instance{ID: "one", Definition: "sandbox@sha256:1", State: json.RawMessage(`{"nested":{"phase":"booting"}}`), Accepts: []string{"ready"}}
	if err := store.Create(ctx, instance); err != nil {
		t.Fatal(err)
	}
	tr := Transition{InstanceID: "one", Definition: instance.Definition, Sequence: 1,
		Event:   Event{SpecVersion: "1.0", ID: "e1", Source: "test://grain", Type: "ready"},
		State:   json.RawMessage(`{"nested":{"phase":"ready"}}`),
		Effects: []Effect{{Type: "agent.run", Data: json.RawMessage(`{"prompt":"work"}`)}},
		Accepts: []string{"finished"}}
	if err := store.Append(ctx, 0, tr); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, 0, tr); err == nil {
		t.Fatal("stale append succeeded")
	}
	reopened := NewSQLStore(db)
	got, err := reopened.Load(ctx, "one")
	if err != nil {
		t.Fatal(err)
	}
	if got.Sequence != 1 || got.Definition != instance.Definition || string(got.State) != string(tr.State) {
		t.Fatalf("instance = %#v", got)
	}
	log, err := reopened.Log(ctx, "one")
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 1 || log[0].Event.ID != "e1" || log[0].Effects[0].Type != "agent.run" {
		t.Fatalf("log = %#v", log)
	}
}

func TestSQLStorePersistsResultPublicFields(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "machines.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewSQLStore(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	instance := Instance{
		ID:         "inst-1",
		Definition: "acme/repo@sha256:v1",
		State:      json.RawMessage(`{"phase":"init"}`),
		Accepts:    []string{"start"},
		Lifecycle:  "active",
		Attention:  false,
		Presentation: Presentation{
			Label:    "Initializing",
			Tone:     "info",
			Activity: "working",
		},
		Inputs: []Input{
			{ID: "start_btn", Kind: "confirm", Label: "Start", Event: "start"},
		},
	}
	if err := store.Create(ctx, instance); err != nil {
		t.Fatalf("Create: %v", err)
	}

	loaded, err := store.Load(ctx, "inst-1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Lifecycle != "active" || loaded.Attention != false || loaded.Presentation.Label != "Initializing" || len(loaded.Inputs) != 1 || loaded.Inputs[0].ID != "start_btn" {
		t.Fatalf("loaded = %#v", loaded)
	}

	tr := Transition{
		InstanceID: "inst-1",
		Definition: instance.Definition,
		Sequence:   1,
		Event:      Event{SpecVersion: "1.0", ID: "e1", Source: "test://grain", Type: "start"},
		State:      json.RawMessage(`{"phase":"waiting_input"}`),
		Effects:    []Effect{{Type: "notify.user", Data: json.RawMessage(`{"msg":"needs input"}`)}},
		Accepts:    []string{"confirm_choice"},
		Lifecycle:  "active",
		Attention:  true,
		Presentation: Presentation{
			Label:       "Awaiting confirmation",
			Description: "Please confirm choice",
			Tone:        "warning",
			Activity:    "waiting",
		},
		Inputs: []Input{
			{ID: "choice_a", Kind: "choice", Label: "Option A", Event: "confirm_choice"},
		},
	}
	if err := store.Append(ctx, 0, tr); err != nil {
		t.Fatalf("Append: %v", err)
	}

	updated, err := store.Load(ctx, "inst-1")
	if err != nil {
		t.Fatalf("Load after Append: %v", err)
	}
	if updated.Lifecycle != "active" || !updated.Attention || updated.Presentation.Label != "Awaiting confirmation" || updated.Presentation.Tone != "warning" || len(updated.Inputs) != 1 || updated.Inputs[0].ID != "choice_a" {
		t.Fatalf("updated = %#v", updated)
	}

	log, err := store.Log(ctx, "inst-1")
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(log) != 1 {
		t.Fatalf("expected 1 transition, got %d", len(log))
	}
	if log[0].Lifecycle != "active" || !log[0].Attention || log[0].Presentation.Tone != "warning" || len(log[0].Inputs) != 1 || log[0].Inputs[0].ID != "choice_a" {
		t.Fatalf("log[0] = %#v", log[0])
	}
}

func TestSQLStoreInitMigratesOlderSchema(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy_machines.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	// Create legacy schema without lifecycle, attention, presentation, inputs
	const legacySchema = `
CREATE TABLE machine_instance (
	id TEXT NOT NULL PRIMARY KEY,
	definition TEXT NOT NULL,
	sequence INTEGER NOT NULL,
	state BLOB NOT NULL,
	accepts BLOB NOT NULL
);
CREATE TABLE machine_transition (
	instance_id TEXT NOT NULL,
	sequence INTEGER NOT NULL,
	definition TEXT NOT NULL,
	event BLOB NOT NULL,
	state BLOB NOT NULL,
	effects BLOB NOT NULL,
	accepts BLOB NOT NULL,
	PRIMARY KEY (instance_id, sequence),
	FOREIGN KEY (instance_id) REFERENCES machine_instance(id)
);`
	if _, err := db.ExecContext(ctx, legacySchema); err != nil {
		t.Fatalf("creating legacy schema: %v", err)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO machine_instance
		(id, definition, sequence, state, accepts) VALUES (?, ?, ?, ?, ?)`,
		"legacy-1", "acme/repo@sha256:v1", 0, []byte(`{"phase":"old"}`), []byte(`["step"]`)); err != nil {
		t.Fatalf("inserting legacy instance: %v", err)
	}

	store := NewSQLStore(db)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("Init migration failed: %v", err)
	}

	// Idempotent Init call
	if err := store.Init(ctx); err != nil {
		t.Fatalf("second Init call failed: %v", err)
	}

	inst, err := store.Load(ctx, "legacy-1")
	if err != nil {
		t.Fatalf("Load migrated instance: %v", err)
	}
	if inst.ID != "legacy-1" || inst.Lifecycle != "" || inst.Attention != false || inst.Presentation.Label != "" || len(inst.Inputs) != 0 {
		t.Fatalf("migrated instance unexpected values: %#v", inst)
	}

	// Append a transition with new fields to the migrated table
	tr := Transition{
		InstanceID:   "legacy-1",
		Definition:   "acme/repo@sha256:v1",
		Sequence:     1,
		Event:        Event{SpecVersion: "1.0", ID: "e1", Source: "test://grain", Type: "step"},
		State:        json.RawMessage(`{"phase":"new"}`),
		Accepts:      []string{"finish"},
		Lifecycle:    "active",
		Attention:    true,
		Presentation: Presentation{Label: "Step 1", Tone: "info"},
		Inputs:       []Input{{ID: "in1", Kind: "confirm", Label: "Finish", Event: "finish"}},
	}
	if err := store.Append(ctx, 0, tr); err != nil {
		t.Fatalf("Append to migrated store: %v", err)
	}

	updated, err := store.Load(ctx, "legacy-1")
	if err != nil {
		t.Fatalf("Load updated migrated instance: %v", err)
	}
	if updated.Lifecycle != "active" || !updated.Attention || updated.Presentation.Label != "Step 1" || len(updated.Inputs) != 1 {
		t.Fatalf("updated migrated instance: %#v", updated)
	}
}

func TestSQLStorePinsCountingQuery(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "pins.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewSQLStore(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}

	// Setup instances:
	// 1. acme/repo @ v2, active, attention=true -> matches Here and Attention
	// 2. acme/repo @ v2, active, attention=false -> matches Here
	// 3. acme/repo @ v2, succeeded, attention=true -> matches Here (not Attention because not active)
	// 4. acme/repo @ v1, active, attention=true -> matches Older
	// 5. acme/repo @ v1, succeeded, attention=false -> matches Older
	// 6. other/repo @ v2, active, attention=true -> unrelated repo
	instances := []Instance{
		{ID: "i1", Definition: "acme/repo@sha256:v2", State: json.RawMessage(`{}`), Accepts: []string{"a"}, Lifecycle: "active", Attention: true},
		{ID: "i2", Definition: "acme/repo@sha256:v2", State: json.RawMessage(`{}`), Accepts: []string{"a"}, Lifecycle: "active", Attention: false},
		{ID: "i3", Definition: "acme/repo@sha256:v2", State: json.RawMessage(`{}`), Accepts: []string{"a"}, Lifecycle: "succeeded", Attention: true},
		{ID: "i4", Definition: "acme/repo@sha256:v1", State: json.RawMessage(`{}`), Accepts: []string{"a"}, Lifecycle: "active", Attention: true},
		{ID: "i5", Definition: "acme/repo@sha256:v1", State: json.RawMessage(`{}`), Accepts: []string{"a"}, Lifecycle: "succeeded", Attention: false},
		{ID: "i6", Definition: "other/repo@sha256:v2", State: json.RawMessage(`{}`), Accepts: []string{"a"}, Lifecycle: "active", Attention: true},
	}
	for _, inst := range instances {
		if err := store.Create(ctx, inst); err != nil {
			t.Fatalf("Create %s: %v", inst.ID, err)
		}
	}

	pins, err := store.Pins(ctx, "acme/repo", "acme/repo@sha256:v2")
	if err != nil {
		t.Fatalf("Pins: %v", err)
	}
	if pins.Here != 3 {
		t.Errorf("pins.Here = %d, want 3", pins.Here)
	}
	if pins.Older != 2 {
		t.Errorf("pins.Older = %d, want 2", pins.Older)
	}
	if pins.Attention != 1 {
		t.Errorf("pins.Attention = %d, want 1", pins.Attention)
	}

	// Query for older version
	pinsOld, err := store.Pins(ctx, "acme/repo", "acme/repo@sha256:v1")
	if err != nil {
		t.Fatalf("Pins v1: %v", err)
	}
	if pinsOld.Here != 2 {
		t.Errorf("pinsOld.Here = %d, want 2", pinsOld.Here)
	}
	if pinsOld.Older != 3 {
		t.Errorf("pinsOld.Older = %d, want 3", pinsOld.Older)
	}
	if pinsOld.Attention != 1 {
		t.Errorf("pinsOld.Attention = %d, want 1", pinsOld.Attention)
	}
}
