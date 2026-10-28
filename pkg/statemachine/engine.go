package statemachine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
)

var (
	ErrInstanceNotFound = errors.New("machine instance not found")
	ErrEventNotAccepted = errors.New("event is not accepted in the current state")

	// ErrReducerUnavailable is a reducer grain could not reach, as
	// opposed to one that answered wrongly.
	//
	// This is the distinction a subprocess reducer could not draw and a
	// reducer served over a link has to: "the definition said something
	// wrong" is the definition's problem and belongs in reducer.failed,
	// where a definition can react to its own bug, while "the machine
	// went away" is grain's problem and must not consume the event or
	// move the snapshot. An unavailable reducer leaves the instance
	// exactly as it was, for whoever is retrying to submit the same event
	// again -- which Submit's deduplication by event id makes safe: a
	// retry of an event that did commit after all is a no-op.
	ErrReducerUnavailable = errors.New("reducer is unavailable")
)

// Store is the durable single-writer boundary. Append atomically rejects a
// stale expectedSequence and persists both transition and latest snapshot,
// and rejects a transition whose event id the instance has already logged.
// Applied reports whether it has: the lookup Submit's deduplication rests on.
type Store interface {
	Load(context.Context, string) (Instance, error)
	Create(context.Context, Instance) error
	Append(context.Context, uint64, Transition) error
	Applied(ctx context.Context, instance, eventID string) (bool, error)
}

type Definitions interface {
	Reducer(context.Context, string) (Reducer, error)
}
type DefinitionFunc func(context.Context, string) (Reducer, error)

func (f DefinitionFunc) Reducer(ctx context.Context, version string) (Reducer, error) {
	return f(ctx, version)
}

type Executor interface {
	Dispatch(context.Context, EffectIntent) error
}
type ExecutorFunc func(context.Context, EffectIntent) error

func (f ExecutorFunc) Dispatch(ctx context.Context, intent EffectIntent) error {
	return f(ctx, intent)
}

// Engine owns durability and effect dispatch. Local locks avoid wasted calls;
// Store.Append remains the cross-process single-writer authority.
type Engine struct {
	Store       Store
	Definitions Definitions
	Executor    Executor
	locks       sync.Map
}

func (e *Engine) Create(ctx context.Context, id, definition string, state json.RawMessage, accepts []string) (Instance, error) {
	if id == "" || definition == "" {
		return Instance{}, errors.New("instance id and definition are required")
	}
	if !json.Valid(state) {
		return Instance{}, errors.New("initial state is not valid JSON")
	}
	if _, err := e.Definitions.Reducer(ctx, definition); err != nil {
		return Instance{}, fmt.Errorf("resolving reducer %q: %w", definition, err)
	}
	instance := Instance{ID: id, Definition: definition, State: cloneJSON(state), Accepts: slices.Clone(accepts)}
	if err := e.Store.Create(ctx, instance); err != nil {
		return Instance{}, err
	}
	return instance, nil
}

func (e *Engine) Submit(ctx context.Context, id string, event Event) (Instance, error) {
	lock := e.instanceLock(id)
	lock.Lock()
	defer lock.Unlock()
	instance, err := e.Store.Load(ctx, id)
	if err != nil {
		return Instance{}, err
	}
	if event.ID == "" || event.Source == "" || event.Type == "" {
		return instance, errors.New("event id, source, and type are required")
	}
	// An event this instance has already consumed -- as itself, or as the
	// reducer.failed it turned into -- is a resubmission, typically by a
	// caller whose first Submit committed but whose answer it never saw.
	// It succeeds without reducing or dispatching anything a second time,
	// and before the accepts check, since the transition it caused may
	// well have stopped accepting its type. The instance lock makes the
	// check and the commit one step in this process; across processes the
	// stale-sequence check in Append, and the store's own refusal of a
	// logged event id, keep the loser from committing.
	for _, id := range []string{event.ID, failedEventID(event.ID)} {
		applied, err := e.Store.Applied(ctx, instance.ID, id)
		if err != nil {
			return instance, fmt.Errorf("checking whether event %q was applied: %w", event.ID, err)
		}
		if applied {
			return instance, nil
		}
	}
	if !slices.Contains(instance.Accepts, event.Type) {
		return instance, fmt.Errorf("%w: %s", ErrEventNotAccepted, event.Type)
	}
	reducer, err := e.Definitions.Reducer(ctx, instance.Definition)
	if err != nil {
		return instance, fmt.Errorf("resolving reducer %q: %w", instance.Definition, err)
	}
	result, err := e.reduce(ctx, reducer, instance, event)
	if err == nil {
		err = validateResult(result)
	}
	if errors.Is(err, ErrReducerUnavailable) {
		// Not charged to the event: no transition, no reducer.failed, and
		// the snapshot where it was. Whoever submitted this may submit it
		// again once there is something to submit it to.
		return instance, fmt.Errorf("reducing %s for instance %s: %w", event.Type, instance.ID, err)
	}
	if err != nil {
		return e.reducerFailed(ctx, instance, reducer, event, err)
	}
	return e.commitAndDispatch(ctx, instance, event, result)
}

// reduce is every call into a definition, so that what the engine knows
// about the call and the definition does not travel with all of them.
func (e *Engine) reduce(ctx context.Context, reducer Reducer, instance Instance, event Event) (Result, error) {
	ctx = WithReduceContext(ctx, ReduceContext{Instance: instance.ID, Sequence: instance.Sequence})
	return reducer.Reduce(ctx, cloneJSON(instance.State), event)
}

func (e *Engine) reducerFailed(ctx context.Context, instance Instance, reducer Reducer, original Event, cause error) (Instance, error) {
	data, _ := json.Marshal(struct {
		EventID string `json:"event_id"`
		Error   string `json:"error"`
	}{original.ID, cause.Error()})
	failure := Event{SpecVersion: "1.0", ID: failedEventID(original.ID), Source: "grain://engine",
		Type: "reducer.failed", Subject: instance.ID, DataContentType: "application/json", Data: data}
	result, err := e.reduce(ctx, reducer, instance, failure)
	if err == nil {
		err = validateResult(result)
	}
	if err != nil {
		return instance, fmt.Errorf("reducer failed for event %q and reducer.failed: %w (original: %v)", original.Type, err, cause)
	}
	return e.commitAndDispatch(ctx, instance, failure, result)
}

func validateResult(result Result) error {
	if !json.Valid(result.State) {
		return errors.New("reducer returned invalid JSON state")
	}
	for _, effect := range result.Effects {
		if effect.Type == "" {
			return errors.New("reducer returned an effect without a type")
		}
		if len(effect.Data) > 0 && !json.Valid(effect.Data) {
			return fmt.Errorf("reducer returned invalid JSON data for effect %q", effect.Type)
		}
	}
	if err := fromVocabulary("lifecycle", result.Lifecycle, LifecycleVocabulary); err != nil {
		return err
	}
	if err := fromVocabulary("presentation.tone", result.Presentation.Tone, ToneVocabulary); err != nil {
		return err
	}
	if err := fromVocabulary("presentation.activity", result.Presentation.Activity, ActivityVocabulary); err != nil {
		return err
	}
	ids := map[string]bool{}
	for _, input := range result.Inputs {
		switch {
		case input.ID == "":
			return errors.New("reducer returned an input without an id")
		case ids[input.ID]:
			return fmt.Errorf("reducer returned two inputs with the id %q", input.ID)
		case input.Label == "":
			return fmt.Errorf("reducer returned input %q with no label", input.ID)
		case input.Event == "":
			return fmt.Errorf("reducer returned input %q with no event", input.ID)
		}
		ids[input.ID] = true
		if err := fromVocabulary(fmt.Sprintf("input %q kind", input.ID), input.Kind, InputKindVocabulary); err != nil {
			return err
		}
		// The one refusal worth making here rather than at the button: an
		// input offering an event the machine would refuse is a control
		// that does nothing, and finding that out when somebody presses
		// it is finding out too late.
		if !slices.Contains(result.Accepts, input.Event) {
			return fmt.Errorf("reducer offered input %q sending %q, which is not in accepts %v",
				input.ID, input.Event, result.Accepts)
		}
	}
	return nil
}

// fromVocabulary holds one field to its closed set. Empty is always
// allowed: a definition that says nothing about tone has not said
// something wrong about it.
func fromVocabulary(field, value string, vocabulary []string) error {
	if value == "" || slices.Contains(vocabulary, value) {
		return nil
	}
	return fmt.Errorf("reducer returned %s %q, which is not one of %v", field, value, vocabulary)
}

func (e *Engine) commitAndDispatch(ctx context.Context, current Instance, event Event, result Result) (Instance, error) {
	lifecycle := result.Lifecycle
	if lifecycle == "" {
		lifecycle = current.Lifecycle
	}
	tr := Transition{
		InstanceID:   current.ID,
		Definition:   current.Definition,
		Sequence:     current.Sequence + 1,
		Event:        event,
		State:        cloneJSON(result.State),
		Effects:      cloneEffects(result.Effects),
		Accepts:      slices.Clone(result.Accepts),
		Lifecycle:    lifecycle,
		Attention:    result.Attention,
		Presentation: result.Presentation,
		Inputs:       cloneInputs(result.Inputs),
	}
	if err := e.Store.Append(ctx, current.Sequence, tr); err != nil {
		return current, fmt.Errorf("persisting transition: %w", err)
	}
	next := Instance{
		ID:           current.ID,
		Definition:   current.Definition,
		Sequence:     tr.Sequence,
		State:        cloneJSON(tr.State),
		Accepts:      slices.Clone(tr.Accepts),
		Lifecycle:    tr.Lifecycle,
		Attention:    tr.Attention,
		Presentation: tr.Presentation,
		Inputs:       cloneInputs(tr.Inputs),
	}
	if e.Executor == nil && len(tr.Effects) > 0 {
		return next, errors.New("effects persisted without an executor")
	}
	for i, effect := range tr.Effects {
		intent := EffectIntent{Key: intentKey(current.ID, tr.Sequence, i), InstanceID: current.ID,
			Sequence: tr.Sequence, Index: i, Effect: effect}
		if err := e.Executor.Dispatch(ctx, intent); err != nil {
			return next, fmt.Errorf("dispatching persisted effect %s: %w", intent.Key, err)
		}
	}
	return next, nil
}

// failedEventID is the id of the reducer.failed event that stands in for
// an event its reducer could not handle.
func failedEventID(original string) string { return original + ":reducer.failed" }

func (e *Engine) instanceLock(id string) *sync.Mutex {
	value, _ := e.locks.LoadOrStore(id, new(sync.Mutex))
	return value.(*sync.Mutex)
}
func intentKey(instance string, sequence uint64, index int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d", instance, sequence, index)))
	return hex.EncodeToString(sum[:16])
}
func cloneJSON(value json.RawMessage) json.RawMessage { return slices.Clone(value) }
func cloneEffects(effects []Effect) []Effect {
	result := make([]Effect, len(effects))
	for i, effect := range effects {
		result[i] = Effect{Type: effect.Type, Data: cloneJSON(effect.Data)}
	}
	return result
}
