package statemachine

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

func TestEnginePersistsBeforeDispatchAndPinsDefinition(t *testing.T) {
	store := NewMemoryStore()
	var dispatched []EffectIntent
	engine := Engine{
		Store: store,
		Definitions: DefinitionFunc(func(_ context.Context, version string) (Reducer, error) {
			if version != "sandbox@sha256:one" {
				t.Fatalf("definition = %q", version)
			}
			return ReducerFunc(func(_ context.Context, state json.RawMessage, event Event) (Result, error) {
				if string(state) != `{"phase":"new"}` || event.Type != "sandbox.ready" {
					t.Fatalf("unexpected reducer input: %s %#v", state, event)
				}
				return Result{State: json.RawMessage(`{"phase":"running"}`),
					Effects: []Effect{{Type: "agent.run", Data: json.RawMessage(`{"prompt":"go"}`)}},
					Accepts: []string{"agent.finished"}}, nil
			}), nil
		}),
		Executor: ExecutorFunc(func(ctx context.Context, intent EffectIntent) error {
			current, err := store.Load(ctx, "one")
			if err != nil || current.Sequence != 1 {
				t.Fatalf("effect dispatched before commit: %#v, %v", current, err)
			}
			dispatched = append(dispatched, intent)
			return nil
		}),
	}
	_, err := engine.Create(context.Background(), "one", "sandbox@sha256:one", json.RawMessage(`{"phase":"new"}`), []string{"sandbox.ready"})
	if err != nil {
		t.Fatal(err)
	}
	next, err := engine.Submit(context.Background(), "one", Event{SpecVersion: "1.0", ID: "evt-1", Source: "test://grain", Type: "sandbox.ready"})
	if err != nil {
		t.Fatal(err)
	}
	if next.Definition != "sandbox@sha256:one" || next.Sequence != 1 || string(next.State) != `{"phase":"running"}` {
		t.Fatalf("next = %#v", next)
	}
	if len(dispatched) != 1 || dispatched[0].Key == "" || dispatched[0].Effect.Type != "agent.run" {
		t.Fatalf("intents = %#v", dispatched)
	}
	if got := store.Transitions("one"); len(got) != 1 || got[0].Event.ID != "evt-1" {
		t.Fatalf("transitions = %#v", got)
	}
}

func TestEngineRejectsAnEventOutsideAccepts(t *testing.T) {
	called := false
	engine := Engine{Store: NewMemoryStore(), Definitions: DefinitionFunc(func(context.Context, string) (Reducer, error) {
		return ReducerFunc(func(context.Context, json.RawMessage, Event) (Result, error) { called = true; return Result{}, nil }), nil
	})}
	_, err := engine.Create(context.Background(), "one", "v1", json.RawMessage(`{}`), []string{"approved"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.Submit(context.Background(), "one", Event{ID: "e", Source: "test://grain", Type: "denied"})
	if !errors.Is(err, ErrEventNotAccepted) {
		t.Fatalf("error = %v", err)
	}
	if called {
		t.Fatal("reducer called for rejected event")
	}
}

func TestReducerFailureBecomesAnEventTheMachineHandles(t *testing.T) {
	store := NewMemoryStore()
	engine := Engine{Store: store, Definitions: DefinitionFunc(func(context.Context, string) (Reducer, error) {
		return ReducerFunc(func(_ context.Context, _ json.RawMessage, event Event) (Result, error) {
			if event.Type == "break" {
				return Result{}, errors.New("bad output")
			}
			if event.Type != "reducer.failed" {
				t.Fatalf("fallback event = %q", event.Type)
			}
			return Result{State: json.RawMessage(`{"phase":"failed"}`), Accepts: []string{"retry"}}, nil
		}), nil
	})}
	_, _ = engine.Create(context.Background(), "one", "v1", json.RawMessage(`{}`), []string{"break"})
	next, err := engine.Submit(context.Background(), "one", Event{ID: "e", Source: "test://grain", Type: "break"})
	if err != nil {
		t.Fatal(err)
	}
	if string(next.State) != `{"phase":"failed"}` {
		t.Fatalf("state = %s", next.State)
	}
	got := store.Transitions("one")
	if len(got) != 1 || got[0].Event.Type != "reducer.failed" {
		t.Fatalf("transitions = %#v", got)
	}
}

func TestEngineSerializesEventsForOneInstance(t *testing.T) {
	store := NewMemoryStore()
	var mu sync.Mutex
	var seen []string
	engine := Engine{Store: store, Definitions: DefinitionFunc(func(context.Context, string) (Reducer, error) {
		return ReducerFunc(func(_ context.Context, state json.RawMessage, event Event) (Result, error) {
			mu.Lock()
			seen = append(seen, string(state))
			mu.Unlock()
			return Result{State: json.RawMessage(event.Data), Accepts: []string{"set"}}, nil
		}), nil
	})}
	_, _ = engine.Create(context.Background(), "one", "v1", json.RawMessage(`0`), []string{"set"})
	var wg sync.WaitGroup
	for _, value := range []string{"1", "2"} {
		wg.Add(1)
		go func(value string) {
			defer wg.Done()
			_, _ = engine.Submit(context.Background(), "one", Event{ID: value, Source: "test://grain", Type: "set", Data: json.RawMessage(value)})
		}(value)
	}
	wg.Wait()
	if len(seen) != 2 || seen[0] == seen[1] {
		t.Fatalf("reducers did not observe successive snapshots: %v", seen)
	}
	if got := store.Transitions("one"); len(got) != 2 {
		t.Fatalf("transitions = %d", len(got))
	}
}

func TestEngineCreateRejectsInvalidInputs(t *testing.T) {
	engine := Engine{Store: NewMemoryStore(), Definitions: DefinitionFunc(func(context.Context, string) (Reducer, error) {
		return nil, errors.New("not found")
	})}

	if _, err := engine.Create(context.Background(), "", "v1", json.RawMessage(`{}`), nil); err == nil {
		t.Error("Create missing ID should fail")
	}
	if _, err := engine.Create(context.Background(), "one", "v1", json.RawMessage(`bad json`), nil); err == nil {
		t.Error("Create with bad JSON should fail")
	}
	if _, err := engine.Create(context.Background(), "one", "v1", json.RawMessage(`{}`), nil); err == nil {
		t.Error("Create with missing reducer should fail")
	}
}

func TestEngineSubmitRejectsInvalidInputs(t *testing.T) {
	engine := Engine{Store: NewMemoryStore(), Definitions: DefinitionFunc(func(context.Context, string) (Reducer, error) {
		return ReducerFunc(func(context.Context, json.RawMessage, Event) (Result, error) { return Result{}, nil }), nil
	})}
	_, _ = engine.Create(context.Background(), "one", "v1", json.RawMessage(`{}`), []string{"ok"})

	if _, err := engine.Submit(context.Background(), "missing", Event{ID: "e", Source: "s", Type: "ok"}); err == nil {
		t.Error("Submit for missing instance should fail")
	}
	if _, err := engine.Submit(context.Background(), "one", Event{Source: "s", Type: "ok"}); err == nil {
		t.Error("Submit with missing event ID should fail")
	}
}

func TestEngineValidateResultRejectsInvalidOutput(t *testing.T) {
	engine := Engine{Store: NewMemoryStore(), Definitions: DefinitionFunc(func(context.Context, string) (Reducer, error) {
		return ReducerFunc(func(context.Context, json.RawMessage, Event) (Result, error) {
			return Result{State: json.RawMessage(`bad json`)}, nil
		}), nil
	})}
	_, _ = engine.Create(context.Background(), "one", "v1", json.RawMessage(`{}`), []string{"break"})
	_, err := engine.Submit(context.Background(), "one", Event{ID: "e", Source: "test", Type: "break"})
	if err == nil {
		t.Error("Submit with invalid JSON output should fail")
	}
}

func TestEnginePersistsResultPublicFieldsAndReturnsCommittedResult(t *testing.T) {
	store := NewMemoryStore()
	expectedInputs := []Input{
		{ID: "approve", Kind: "confirm", Label: "Approve PR", Event: "approve"},
	}
	expectedPres := Presentation{
		Label:       "Waiting for approval",
		Description: "PR review required",
		Tone:        "warning",
		Activity:    "waiting",
	}

	engine := Engine{
		Store: store,
		Definitions: DefinitionFunc(func(context.Context, string) (Reducer, error) {
			return ReducerFunc(func(ctx context.Context, state json.RawMessage, event Event) (Result, error) {
				return Result{
					State:        json.RawMessage(`{"status":"needs_approval"}`),
					Accepts:      []string{"approve"},
					Lifecycle:    "active",
					Attention:    true,
					Presentation: expectedPres,
					Inputs:       expectedInputs,
				}, nil
			}), nil
		}),
	}

	_, err := engine.Create(context.Background(), "inst-1", "v1", json.RawMessage(`{"status":"init"}`), []string{"run"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	resInst, err := engine.Submit(context.Background(), "inst-1", Event{
		ID:     "evt-1",
		Source: "test://grain",
		Type:   "run",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Verify Submit returns the committed Result fields
	if resInst.Lifecycle != "active" {
		t.Errorf("resInst.Lifecycle = %q, want active", resInst.Lifecycle)
	}
	if !resInst.Attention {
		t.Errorf("resInst.Attention = %v, want true", resInst.Attention)
	}
	if resInst.Presentation != expectedPres {
		t.Errorf("resInst.Presentation = %+v, want %+v", resInst.Presentation, expectedPres)
	}
	if len(resInst.Inputs) != 1 || resInst.Inputs[0].ID != "approve" {
		t.Errorf("resInst.Inputs = %+v, want %+v", resInst.Inputs, expectedInputs)
	}

	// Verify store has the updated snapshot
	loaded, err := store.Load(context.Background(), "inst-1")
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	if loaded.Lifecycle != "active" || !loaded.Attention || loaded.Presentation != expectedPres || len(loaded.Inputs) != 1 {
		t.Errorf("loaded = %+v", loaded)
	}

	// Verify transitions have the fields
	trs := store.Transitions("inst-1")
	if len(trs) != 1 {
		t.Fatalf("len(trs) = %d, want 1", len(trs))
	}
	if trs[0].Lifecycle != "active" || !trs[0].Attention || trs[0].Presentation != expectedPres || len(trs[0].Inputs) != 1 {
		t.Errorf("trs[0] = %+v", trs[0])
	}
}

func TestEnginePreservesLifecycleWhenEmpty(t *testing.T) {
	store := NewMemoryStore()
	step := 0
	engine := Engine{
		Store: store,
		Definitions: DefinitionFunc(func(context.Context, string) (Reducer, error) {
			return ReducerFunc(func(ctx context.Context, state json.RawMessage, event Event) (Result, error) {
				step++
				if step == 1 {
					return Result{
						State:     json.RawMessage(`{"step":1}`),
						Accepts:   []string{"step2"},
						Lifecycle: "active",
						Attention: true,
					}, nil
				}
				// Second step returns empty Lifecycle: should preserve "active"
				return Result{
					State:     json.RawMessage(`{"step":2}`),
					Accepts:   []string{"finish"},
					Lifecycle: "",
					Attention: false,
				}, nil
			}), nil
		}),
	}

	_, err := engine.Create(context.Background(), "inst-1", "v1", json.RawMessage(`{}`), []string{"step1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	inst1, err := engine.Submit(context.Background(), "inst-1", Event{ID: "e1", Source: "s", Type: "step1"})
	if err != nil {
		t.Fatalf("Submit 1: %v", err)
	}
	if inst1.Lifecycle != "active" {
		t.Fatalf("inst1.Lifecycle = %q, want active", inst1.Lifecycle)
	}

	inst2, err := engine.Submit(context.Background(), "inst-1", Event{ID: "e2", Source: "s", Type: "step2"})
	if err != nil {
		t.Fatalf("Submit 2: %v", err)
	}
	if inst2.Lifecycle != "active" {
		t.Fatalf("inst2.Lifecycle = %q, want active (preserved)", inst2.Lifecycle)
	}
	if inst2.Attention {
		t.Fatalf("inst2.Attention = %v, want false", inst2.Attention)
	}
}
