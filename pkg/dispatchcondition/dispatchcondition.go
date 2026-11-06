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

// Package dispatchcondition records the reasons a deployment is not
// dispatching at all, as named conditions on the deployment rather than
// as log lines.
//
// A daemon that cannot dispatch usually knows it: the git proxy every
// run clones through failed to bind its port, the reconcile loop gave
// up on a panic. Before this package that knowledge went to the journal
// and to one boolean on GET /api/config (reconcilerDown), and nowhere
// else -- a queued task said "queued/ready" for as long as it sat
// there, and GET /api/sandboxes reported sandbox statistics for a
// deployment that would never build another sandbox. An agent reading
// the API could not learn what an operator with journalctl could.
//
// A Set holds the conditions in force, each with the error that raised
// it and when; what reads them is the health route (a failing check), a
// queued task's reason (why it is not being dispatched), the pipeline
// feed (a set and a clear are each an event) and the journal (one line
// for each). Setting and clearing are this package's only writes, and a
// clear is as loud as a set: a reader that saw dispatch blocked has to
// be able to see it unblocked.
package dispatchcondition

import (
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

// Condition is one named reason this deployment is not dispatching.
type Condition struct {
	// Name identifies the condition for a machine: "git-proxy",
	// "reconciler". One per cause; setting a name twice updates it.
	Name string `json:"name"`
	// Error is the underlying failure's text: its first line, with the
	// rest -- a panic's stack -- left to the journal (Block).
	Error string `json:"error"`
	// Since is when the condition was set.
	Since time.Time `json:"since"`
	// Remedy is what clears it, where the setter knows: "restart the
	// daemon once the port is free".
	Remedy string `json:"remedy,omitempty"`
}

// Sentence is the condition in one line, for a task's reason and a
// log: "git proxy failed to bind: listen tcp :7100: address already in
// use".
func (c Condition) Sentence() string {
	return strings.ReplaceAll(c.Name, "-", " ") + ": " + c.Error
}

// Event is a condition being set or cleared, in the order it happened.
type Event struct {
	At time.Time `json:"at"`
	// Set is true for the condition coming into force and false for it
	// clearing.
	Set       bool      `json:"set"`
	Condition Condition `json:"condition"`
}

// maxHistory bounds what History keeps: conditions are rare and each
// is one event, so this is generous, and it keeps a flapping setter
// from growing the process.
const maxHistory = 200

// Set is the conditions in force on one deployment, safe for use from
// every goroutine.
type Set struct {
	// Logf writes the journal line for each set and clear; nil is
	// log.Printf.
	Logf func(format string, args ...any)
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Redact, when set, is applied to a condition's text before it is
	// kept (Block). A condition's text is served far beyond the
	// journal -- to every queued task's reason, which an agent reads
	// for its own task from inside its sandbox, and to the health,
	// config and pipeline-feed routes -- so the deployment wires its
	// own secret redaction here, the same list its journal is read
	// through, rather than trusting that no error a daemon fails on
	// ever quotes a credential. nil keeps the text as it is.
	Redact func(text string) string

	mu      sync.Mutex
	active  map[string]Condition
	history []Event
	// version counts sets and clears, and a re-report that changed a
	// condition's text, for a reader that caches what it derived from
	// Active and has to know when that went stale (pkg/ui's task
	// caches, keyed on the store's own change token, which nothing here
	// moves).
	version uint64
}

func (s *Set) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Set) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// Block sets the condition name with err's text, logging it. A name
// already in force keeps its Since and takes the new error text, so a
// setter that re-reports the same failure every cycle neither moves
// the clock nor writes a line each time.
//
// Only the error's first line is kept (errorLine), passed through
// Redact where one is set. A condition is read
// far outside the journal -- by every queued task's reason, which any
// agent reads for its own task from its sandbox, by GET /api/config,
// and by the health route and the pipeline feed -- and the one error
// that arrives with more than a line is runDaemon's recovered panic,
// whose text is the panic value followed by a goroutine stack: the
// build's file paths, every frame and its pointers. That is for the
// journal, where the daemon logs the whole error beside this
// condition; the condition carries what the operator reads first.
func (s *Set) Block(name string, err error, remedy string) {
	text := "unknown error"
	if err != nil {
		text = errorLine(err)
	}
	// Outside the lock: the deployment's redactor reads its secret
	// store, and a reader of Active should not wait on that.
	if s.Redact != nil {
		text = s.Redact(text)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil {
		s.active = map[string]Condition{}
	}
	if c, ok := s.active[name]; ok {
		if c.Error != text || c.Remedy != remedy {
			// New words for the same condition: a cache keyed on
			// Version has to re-read them.
			s.version++
		}
		c.Error, c.Remedy = text, remedy
		s.active[name] = c
		return
	}
	c := Condition{Name: name, Error: text, Since: s.now(), Remedy: remedy}
	s.active[name] = c
	s.record(Event{At: c.Since, Set: true, Condition: c})
	s.logf("grain daemon: dispatch blocked [%s]: %s -- no task is dispatched until this clears", name, text)
}

// errorLine is err's first line, trimmed, and "unknown error" when that
// is empty: the condition's text (Block). A multi-line error -- a
// recovered panic with its stack -- keeps its first line and says the
// rest is in the journal.
func errorLine(err error) string {
	text := err.Error()
	first, rest, more := strings.Cut(text, "\n")
	first = strings.TrimSpace(first)
	if first == "" {
		return "unknown error"
	}
	if more && strings.TrimSpace(rest) != "" {
		first += " (the rest is in the daemon's journal)"
	}
	return first
}

// Clear lifts the condition name, logging it; a name not in force is
// nothing to do.
func (s *Set) Clear(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.active[name]
	if !ok {
		return
	}
	delete(s.active, name)
	at := s.now()
	s.record(Event{At: at, Set: false, Condition: c})
	s.logf("grain daemon: dispatch unblocked [%s] after %s", name, at.Sub(c.Since).Round(time.Second))
}

func (s *Set) record(e Event) {
	s.version++
	s.history = append(s.history, e)
	if len(s.history) > maxHistory {
		s.history = s.history[len(s.history)-maxHistory:]
	}
}

// Active is every condition in force, oldest first. nil when dispatch
// is not blocked.
func (s *Set) Active() []Condition {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.active) == 0 {
		return nil
	}
	out := make([]Condition, 0, len(s.active))
	for _, c := range s.active {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Since.Equal(out[j].Since) {
			return out[i].Since.Before(out[j].Since)
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Blocked reports whether any condition is in force.
func (s *Set) Blocked() bool { return len(s.Active()) > 0 }

// Why is the conditions in force as one line -- the oldest's sentence,
// and how many more there are -- or "" when there are none. It is what
// a queued task's reason carries.
func (s *Set) Why() string {
	active := s.Active()
	switch len(active) {
	case 0:
		return ""
	case 1:
		return active[0].Sentence()
	}
	return active[0].Sentence() + " (and " + strings.Join(names(active[1:]), ", ") + ")"
}

func names(cs []Condition) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Name
	}
	return out
}

// Version is a counter that moves on every set and clear, and on a
// re-report that changed a condition's text: equal between two reads,
// nothing about Active or History changed between them. Zero on a Set
// nothing has touched, and on a nil one.
func (s *Set) Version() uint64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version
}

// History is every set and clear since the process started, oldest
// first, bounded to the most recent maxHistory.
func (s *Set) History() []Event {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.history...)
}
