// Package statemachine provides Grain's reducer-driven workflow runtime.
//
// A definition is an opaque function from state and event to state, effects,
// and accepted event types. The engine persists its answer before dispatching.
package statemachine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

type Event struct {
	SpecVersion     string          `json:"specversion"`
	ID              string          `json:"id"`
	Source          string          `json:"source"`
	Type            string          `json:"type"`
	Subject         string          `json:"subject,omitempty"`
	Time            *time.Time      `json:"time,omitempty"`
	DataContentType string          `json:"datacontenttype,omitempty"`
	Data            json.RawMessage `json:"data,omitempty"`
}

type Effect struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// Result is a definition's whole answer to one event: the next state, the
// effects grain should perform, what may be sent next, and how the thing
// should read to a person.
//
// A definition requests and never acts. Every field here is either
// grain's to persist or grain's to carry out; none of it is something the
// definition did.
type Result struct {
	State   json.RawMessage `json:"state"`
	Effects []Effect        `json:"effects"`
	Accepts []string        `json:"accepts"`

	// Lifecycle is where the instance is as a whole, from
	// LifecycleVocabulary. Empty leaves it unchanged.
	Lifecycle string `json:"lifecycle,omitempty"`
	// Attention is whether a person is being waited on. It is what holds
	// a serving sandbox warm, because it is precisely the state somebody
	// is about to act on.
	Attention bool `json:"attention,omitempty"`
	// Presentation is how this state reads. Public by construction:
	// State is private to the definition and never rendered, so anything
	// a person should see has to be said here.
	Presentation Presentation `json:"presentation,omitzero"`
	// Inputs is what a person may send from here. Every input's Event
	// must be in Accepts -- an input whose event the machine would refuse
	// is a button that does nothing, and is refused when the result is
	// validated rather than when somebody presses it.
	Inputs []Input `json:"inputs,omitempty"`
}

// Presentation is the public face of one state.
type Presentation struct {
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
	Tone        string `json:"tone,omitempty"`
	Activity    string `json:"activity,omitempty"`
}

// Input is one thing a person may send, as offered by the definition and
// accepted through POST /api/tasks/{task_id}/inputs/{input_id}.
type Input struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
	Event string `json:"event"`
}

// The closed vocabularies a Result is held to.
//
// Closed rather than free-form because every one of them is rendered:
// a tone becomes a colour and an activity becomes a spinner or the
// absence of one, so a definition inventing a seventh tone would be a
// definition producing a state nothing knows how to draw. Widening one
// is a change here, and a change to whatever draws it, together.
var (
	LifecycleVocabulary = []string{"active", "succeeded", "failed", "cancelled"}
	ToneVocabulary      = []string{"neutral", "info", "success", "warning", "danger"}
	ActivityVocabulary  = []string{"idle", "working", "waiting", "blocked"}
	InputKindVocabulary = []string{"confirm", "text", "choice"}
)

// Reducer must be stateless: state is the entire durable machine state.
type Reducer interface {
	Reduce(context.Context, json.RawMessage, Event) (Result, error)
}

type ReducerFunc func(context.Context, json.RawMessage, Event) (Result, error)

func (f ReducerFunc) Reduce(ctx context.Context, state json.RawMessage, event Event) (Result, error) {
	return f(ctx, state, event)
}

// Instance pins an immutable definition version and its latest snapshot.
type Instance struct {
	ID           string          `json:"id"`
	Definition   string          `json:"definition"`
	Sequence     uint64          `json:"sequence"`
	State        json.RawMessage `json:"state"`
	Accepts      []string        `json:"accepts"`
	Lifecycle    string          `json:"lifecycle,omitempty"`
	Attention    bool            `json:"attention,omitempty"`
	Presentation Presentation    `json:"presentation,omitzero"`
	Inputs       []Input         `json:"inputs,omitempty"`
}

// Transition is append-only and carries resulting state, avoiding replay.
type Transition struct {
	InstanceID   string          `json:"instance_id"`
	Definition   string          `json:"definition"`
	Sequence     uint64          `json:"sequence"`
	Event        Event           `json:"event"`
	State        json.RawMessage `json:"state"`
	Effects      []Effect        `json:"effects"`
	Accepts      []string        `json:"accepts"`
	Lifecycle    string          `json:"lifecycle,omitempty"`
	Attention    bool            `json:"attention,omitempty"`
	Presentation Presentation    `json:"presentation,omitzero"`
	Inputs       []Input         `json:"inputs,omitempty"`
}

// Pins counts one repository's instances against one version.
type Pins struct {
	// Here is instances pinned to the version asked about.
	Here int
	// Older is instances pinned to any other version -- what an operator
	// is watching drain after a release.
	Older int
	// Attention is instances on the version asked about that are active
	// with attention set, which is precisely the state a person is about
	// to act on.
	Attention int
}

func cloneInputs(inputs []Input) []Input {
	if inputs == nil {
		return nil
	}
	return slices.Clone(inputs)
}

// EffectIntent gives at-least-once delivery a stable idempotency key.
type EffectIntent struct {
	Key        string `json:"key"`
	InstanceID string `json:"instance_id"`
	Sequence   uint64 `json:"sequence"`
	Index      int    `json:"index"`
	Effect     Effect `json:"effect"`
}

// ReduceContext is what the engine knows about a call that the Reducer
// interface does not carry.
//
// Reduce's signature is state and event, and stays that way: it is the
// whole of what a definition is a function of, and widening it would
// invite a definition to be a function of something else. But a reducer
// that is reached over a network needs to be able to say which instance
// and which sequence a request is for -- to route it, to log it, and to
// recognise the same request arriving twice after a crash between the
// answer and the commit -- and none of that is input to the function.
//
// So it travels beside the call rather than in it. HTTPReducer puts it in
// the request's headers and its envelope; a definition that reads it to
// decide anything is a definition that is no longer pure, which is its
// own bug and not one this seam can prevent.
type ReduceContext struct {
	Instance string
	Sequence uint64
}

// IdempotencyKey is the engine's own name for one reduce: this instance,
// at this sequence, for this event. The same key arriving twice must
// produce the same answer, which for a pure function comes free.
//
// Hashed rather than carried literally, because the engine's triple is
// NUL-separated and this key's job is to travel in an HTTP header, where
// a NUL cannot go. The same construction the effect outbox uses for its
// own keys (intentKey), for the same reason it uses it: what a key has to
// be is stable and distinct, not readable.
func (c ReduceContext) IdempotencyKey(eventID string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s", c.Instance, c.Sequence, eventID)))
	return hex.EncodeToString(sum[:])
}

type reduceContextKey struct{}

// WithReduceContext is how the engine passes it; nothing else should.
func WithReduceContext(ctx context.Context, rc ReduceContext) context.Context {
	return context.WithValue(ctx, reduceContextKey{}, rc)
}

// ReduceContextFrom returns what the engine said about this call, and
// whether it said anything. A reducer called directly by a test gets the
// zero value and false.
func ReduceContextFrom(ctx context.Context) (ReduceContext, bool) {
	rc, ok := ctx.Value(reduceContextKey{}).(ReduceContext)
	return rc, ok
}
