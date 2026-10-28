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

package statemachine

// What a definition is allowed to answer, and what the engine does with a
// definition it could not reach at all.
//
// Both are contract rather than mechanism: nothing here starts a process
// or opens a connection, and every reducer is a closure. What is being
// pinned is which answers are refused and -- the sharper one -- which
// failures are the definition's to hear about.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

var testEvent = Event{
	SpecVersion: "1.0", ID: "observation-12", Source: "grain://task-observer",
	Type: "task.observed", Subject: "123", DataContentType: "application/json",
	Data: json.RawMessage(`{"approved":true}`),
}

// A result says nothing about tone unless it says something about tone.
// Every field here is optional, and leaving one out is not a definition
// saying something wrong about it.
func TestAResultMaySayNothingAboutHowItReads(t *testing.T) {
	if err := validateResult(Result{State: json.RawMessage(`{}`), Accepts: []string{"task.observed"}}); err != nil {
		t.Fatalf("validateResult: %v", err)
	}
}

// Closed vocabularies, because every one of them is rendered: a tone
// becomes a colour and an activity becomes a spinner or the absence of
// one, so a definition inventing a seventh tone is a definition producing
// a state nothing knows how to draw.
func TestAResultIsHeldToTheVocabulariesItIsRenderedThrough(t *testing.T) {
	for field, result := range map[string]Result{
		"lifecycle":             {State: json.RawMessage(`{}`), Lifecycle: "in_progress"},
		"presentation.tone":     {State: json.RawMessage(`{}`), Presentation: Presentation{Tone: "chartreuse"}},
		"presentation.activity": {State: json.RawMessage(`{}`), Presentation: Presentation{Activity: "pondering"}},
	} {
		err := validateResult(result)
		if err == nil {
			t.Errorf("%s: accepted a value nothing can draw", field)
			continue
		}
		if !strings.Contains(err.Error(), field) {
			t.Errorf("%s: %v does not name the field", field, err)
		}
	}
	ok := Result{
		State:        json.RawMessage(`{}`),
		Lifecycle:    "active",
		Presentation: Presentation{Label: "Ready to submit", Tone: "success", Activity: "idle"},
	}
	if err := validateResult(ok); err != nil {
		t.Errorf("validateResult: %v", err)
	}
}

// The one refusal worth making here rather than at the button: an input
// offering an event the machine would refuse is a control that does
// nothing, and finding that out when somebody presses it is finding out
// too late.
func TestAnInputMustSendAnEventTheMachineWouldAccept(t *testing.T) {
	err := validateResult(Result{
		State:   json.RawMessage(`{}`),
		Accepts: []string{"task.observed"},
		Inputs: []Input{{ID: "cancel_submission", Kind: "confirm", Label: "Cancel submission",
			Event: "submission.cancelled"}},
	})
	if err == nil {
		t.Fatal("a button that does nothing was accepted")
	}
	if !strings.Contains(err.Error(), "not in accepts") || !strings.Contains(err.Error(), "cancel_submission") {
		t.Errorf("%v does not say which input, or why", err)
	}
}

func TestAnInputNeedsAnIdALabelAnEventAndAKnownKind(t *testing.T) {
	accepts := []string{"submission.cancelled"}
	full := Input{ID: "cancel", Kind: "confirm", Label: "Cancel", Event: "submission.cancelled"}
	for name, input := range map[string]Input{
		"no id":    {Kind: "confirm", Label: "Cancel", Event: "submission.cancelled"},
		"no label": {ID: "cancel", Kind: "confirm", Event: "submission.cancelled"},
		"no event": {ID: "cancel", Kind: "confirm", Label: "Cancel"},
		"a kind nothing renders": {ID: "cancel", Kind: "interpretive-dance",
			Label: "Cancel", Event: "submission.cancelled"},
	} {
		if err := validateResult(Result{State: json.RawMessage(`{}`), Accepts: accepts,
			Inputs: []Input{input}}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Two controls with one id is one control written twice, with
	// whichever arrived last deciding what the other one does.
	if err := validateResult(Result{State: json.RawMessage(`{}`), Accepts: accepts,
		Inputs: []Input{full, full}}); err == nil || !strings.Contains(err.Error(), "two inputs") {
		t.Errorf("duplicate ids: %v", err)
	}
	if err := validateResult(Result{State: json.RawMessage(`{}`), Accepts: accepts,
		Inputs: []Input{full}}); err != nil {
		t.Errorf("a well-formed input: %v", err)
	}
}

// The distinction a subprocess reducer could not draw. "The definition
// said something wrong" is the definition's problem and belongs in
// reducer.failed, where a definition can react to its own bug; "the
// machine went away" is grain's problem and must not consume the event or
// move the snapshot.
func TestAnUnreachableReducerIsNotChargedToTheEvent(t *testing.T) {
	store := NewMemoryStore()
	var calls int
	engine := Engine{
		Store: store,
		Definitions: DefinitionFunc(func(context.Context, string) (Reducer, error) {
			return ReducerFunc(func(context.Context, json.RawMessage, Event) (Result, error) {
				calls++
				return Result{}, fmt.Errorf("%w: the exec died", ErrReducerUnavailable)
			}), nil
		}),
	}
	if _, err := engine.Create(context.Background(), "one", "sha256:one",
		json.RawMessage(`{"phase":"new"}`), []string{"task.observed"}); err != nil {
		t.Fatal(err)
	}
	instance, err := engine.Submit(context.Background(), "one", testEvent)
	if !errors.Is(err, ErrReducerUnavailable) {
		t.Fatalf("error = %v", err)
	}
	if calls != 1 {
		t.Errorf("the reducer was asked %d times -- an unreachable machine gets no reducer.failed", calls)
	}
	if instance.Sequence != 0 || string(instance.State) != `{"phase":"new"}` {
		t.Errorf("the snapshot moved: %+v", instance)
	}
	if got := store.Transitions("one"); len(got) != 0 {
		t.Errorf("transitions = %+v", got)
	}
}

// A definition that answers wrongly still gets exactly one reducer.failed
// against the last committed state, which is the behaviour the sentinel
// above has to leave alone.
func TestADefinitionThatAnswersWronglyStillHearsAboutIt(t *testing.T) {
	var seen []string
	engine := Engine{
		Store: NewMemoryStore(),
		Definitions: DefinitionFunc(func(context.Context, string) (Reducer, error) {
			return ReducerFunc(func(_ context.Context, _ json.RawMessage, event Event) (Result, error) {
				seen = append(seen, event.Type)
				if event.Type == "reducer.failed" {
					return Result{State: json.RawMessage(`{"phase":"broken"}`), Accepts: []string{"task.observed"}}, nil
				}
				return Result{State: json.RawMessage(`not json`)}, nil
			}), nil
		}),
	}
	if _, err := engine.Create(context.Background(), "one", "sha256:one",
		json.RawMessage(`{}`), []string{"task.observed"}); err != nil {
		t.Fatal(err)
	}
	next, err := engine.Submit(context.Background(), "one", testEvent)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if len(seen) != 2 || seen[1] != "reducer.failed" {
		t.Fatalf("the definition was asked %v", seen)
	}
	if string(next.State) != `{"phase":"broken"}` {
		t.Errorf("state = %s", next.State)
	}
}

// Reduce's signature is state and event and stays that way -- it is the
// whole of what a definition is a function of. What the engine knows that
// the signature does not carry travels beside the call.
func TestTheEnginePassesTheReduceContextBesideTheCall(t *testing.T) {
	var seen ReduceContext
	var present bool
	engine := Engine{
		Store: NewMemoryStore(),
		Definitions: DefinitionFunc(func(context.Context, string) (Reducer, error) {
			return ReducerFunc(func(ctx context.Context, _ json.RawMessage, _ Event) (Result, error) {
				seen, present = ReduceContextFrom(ctx)
				return Result{State: json.RawMessage(`{}`), Accepts: []string{"task.observed"}}, nil
			}), nil
		}),
	}
	if _, err := engine.Create(context.Background(), "abc", "sha256:one",
		json.RawMessage(`{}`), []string{"task.observed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Submit(context.Background(), "abc", testEvent); err != nil {
		t.Fatal(err)
	}
	if !present || seen.Instance != "abc" || seen.Sequence != 0 {
		t.Fatalf("reduce context = %+v, present = %v", seen, present)
	}
	// A reducer called directly by a test gets the zero value and false,
	// rather than a context that claims to be an engine's.
	if _, ok := ReduceContextFrom(context.Background()); ok {
		t.Error("a bare context reported a reduce context")
	}
}

// The key has to be able to travel in an HTTP header, which the engine's
// own NUL-separated triple cannot, and has to distinguish every reduce.
func TestTheIdempotencyKeyIsHeaderSafeAndDistinct(t *testing.T) {
	rc := ReduceContext{Instance: "abc", Sequence: 11}
	key := rc.IdempotencyKey("observation-12")
	if len(key) != 64 || strings.ContainsAny(key, "\x00 \r\n") {
		t.Errorf("key %q cannot travel in a header", key)
	}
	for name, other := range map[string]string{
		"another sequence": ReduceContext{Instance: "abc", Sequence: 12}.IdempotencyKey("observation-12"),
		"another instance": ReduceContext{Instance: "xyz", Sequence: 11}.IdempotencyKey("observation-12"),
		"another event":    rc.IdempotencyKey("observation-13"),
	} {
		if key == other {
			t.Errorf("%s produced the same key", name)
		}
	}
	if key != rc.IdempotencyKey("observation-12") {
		t.Error("the same reduce produced two keys")
	}
}
