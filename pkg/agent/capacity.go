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

package agent

// A provider with no capacity right now is not a provider a deployment
// has run out of budget with, and it is not a run that failed on its own
// terms either. Gemini answers an overloaded model with a 503 and "the
// model is overloaded"; Anthropic answers with a 529 overloaded_error;
// OpenAI answers with a 503 of its own. None of that says anything about
// the task the run was given, and all of it is over in seconds.
//
// Read as an ordinary failure -- which is what it was, since it matches
// none of usagelimit.go's markers -- it costs the task far more than the
// run: task_streak counts it (model.PausedOutcome's own doc comment on
// why that matters), dispatch backs the task off, and five of them in a
// row put the task in state 'failed' and stop it being dispatched at
// all. A provider outage lasting a minute therefore retires tasks that
// were never wrong, and the damage outlives the outage.
//
// CapacityError is how a Framework says "the provider, not the task" so
// that orchestrator.RunDispatch can record the run under model.
// CapacityOutcome, which no failure streak counts.
//
// Deliberately *not* a UsageLimitError, though the two are cousins. A
// usage limit begins an orchestrator.Pause: dispatch onto the credential
// stops and every other run in flight on it is cancelled, which is the right
// response to a window that reopens in hours and the wrong one to an
// overload that is over before the pause is read. A capacity refusal
// starts nothing; dispatch's own retry backoff carries the retry, which
// is why this type has no ResetAt or RetryAfter to pause on. What it
// buys the task is only that the attempt it just lost is not held
// against it.

import (
	"errors"
	"strings"
)

// CapacityError is a Framework's report that the provider refused this
// run for want of capacity -- overloaded, unavailable, a gateway between
// the CLI and the model that gave up -- rather than because of anything
// about the run.
//
// A Framework returns this instead of a plain error, never as well as
// one: it wraps whatever the CLI actually said (Err), so a caller that
// only wants the text still gets it from Error().
type CapacityError struct {
	// Framework names which implementation produced this ("claude",
	// "antigravity", "codex"), since capacity belongs to that
	// framework's own provider -- a deployment running two can be
	// refused by one and served by the other. Carried for the operator
	// reading the run's detail; nothing branches on it today.
	Framework string
	// Message is what the CLI said, trimmed by TrimLimitMessage to
	// something a stored run detail can carry. Empty is allowed: the
	// type itself is the fact.
	Message string
	// Err is the underlying failure -- the CLI's own error, unmodified,
	// so errors.Is/As still reach whatever it carried.
	Err error
}

func (e *CapacityError) Error() string {
	parts := []string{"agent: the provider had no capacity for this run"}
	if e.Framework != "" {
		parts[0] = e.Framework + ": the provider had no capacity for this run"
	}
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	return strings.Join(parts, "; ")
}

func (e *CapacityError) Unwrap() error { return e.Err }

// Capacity reports whether err is (or wraps) a CapacityError, and hands
// back the one it found -- errors.As with the type spelled out once here
// rather than at every call site, exactly as UsageLimit is.
func Capacity(err error) (*CapacityError, bool) {
	var capacity *CapacityError
	if errors.As(err, &capacity) {
		return capacity, true
	}
	return nil, false
}

// ErrCapacity is the sentinel a caller that only wants to ask "was this
// the provider being overloaded?" can compare against with errors.Is,
// without naming the struct. Every CapacityError matches it.
var ErrCapacity = errors.New("agent: the provider had no capacity for this run")

// Is makes errors.Is(err, ErrCapacity) answer true for any
// CapacityError, whatever its own fields say -- the shape errors.Is
// documents for a type that wants to match a sentinel it does not wrap.
func (e *CapacityError) Is(target error) bool { return target == ErrCapacity }

// sharedCapacityMarkers are the phrases that mean "no capacity" whoever
// is being asked: HTTP statuses a provider's own edge writes, in the
// spelling a client library renders them in. Lowercase, matched against
// a lowercased haystack.
//
// Every marker here carries its status code *and* its reason phrase.
// That is the same discipline usagelimit.go applies when it refuses to
// match a bare "429": the text this reads is a run's own transcript,
// which for an hour has been full of a repository's files, diffs and
// test output, and "503" and "unavailable" both appear in those on their
// own. "503 service unavailable" is a phrase a provider writes and a
// repository does not.
var sharedCapacityMarkers = []string{
	"503 service unavailable",
	"502 bad gateway",
	"504 gateway timeout",
}

// CapacityFailure reads a failed run's own account of itself -- the text
// its stream produced plus whatever the subprocess reported -- and
// returns the capacity refusal it describes, or nil when it describes
// something else entirely, which is every ordinary failure.
//
// markers is the framework's own vocabulary, consulted alongside the
// shared list above: each provider words an overload its own way, and
// the CLI in front of it words it again. Per-framework lists rather than
// one union so that a phrase only one provider writes cannot be matched
// in a stream belonging to another, where it would be a phrase somebody
// merely typed.
//
// Only ever called for a run that has already failed. That is what makes
// substring matching safe at all: the alternative reading is not "the
// agent said something" but "the run broke for reasons we cannot name",
// which is precisely the reading that costs the task its retry budget.
func CapacityFailure(framework string, markers []string, resultText string, runErr error) *CapacityError {
	text := resultText
	if runErr != nil {
		text = strings.TrimSpace(text + "\n" + runErr.Error())
	}
	if !mentionsCapacity(text, markers) {
		return nil
	}
	return &CapacityError{
		Framework: framework,
		Message:   TrimLimitMessage(text),
		Err:       runErr,
	}
}

// mentionsCapacity reports whether text carries any of the shared
// markers or any of the framework's own.
func mentionsCapacity(text string, markers []string) bool {
	lower := strings.ToLower(text)
	for _, marker := range sharedCapacityMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
