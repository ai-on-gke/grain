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

// Package credhealth is the deployment's record of the operator's host
// credentials having lapsed, for whatever finds out about it outside a
// change provider (pkg/change keeps its own): a host tool an agent ran
// that failed on auth, or a proactive check a deployment layer registers
// (RegisterCheck) to warn before the credential expires rather than after
// runs have failed on it.
//
// Each failing source is recorded once, raises one warning notification
// (deduplicated by subject), and is cleared -- with its notification --
// by the next success from the same source. grain health reads Failures.
package credhealth

import (
	"context"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// CheckInterval is how often Watch runs the registered checks.
const CheckInterval = 5 * time.Minute

// Notifier raises and clears operator notifications: the daemon's store.
type Notifier interface {
	RaiseNotification(ctx context.Context, n model.Notification) (model.NotificationRaise, error)
	ClearNotification(ctx context.Context, subject, by string) (bool, error)
}

// raisedBy is the principal a credential notification is raised as.
const raisedBy = "automation:credential-health"

// Failure is one source's current spell of lapsed credentials.
type Failure struct {
	// Source names what saw it: "host-tool:<grant>" or "check:<name>".
	Source string `json:"source"`
	// Title and Detail are the notification's own: what lapsed, and
	// what to run where.
	Title  string `json:"title"`
	Detail string `json:"detail"`
	// Since and Last bound the spell; Count is how often it was seen.
	Since time.Time `json:"since"`
	Last  time.Time `json:"last"`
	Count int       `json:"count"`
}

// Subject is the notification subject source's failure is raised under.
func Subject(source string) string { return "credentials-expired." + source }

var (
	mu       sync.Mutex
	failures = map[string]Failure{}
	notifier Notifier
	checks   = map[string]Check{}
)

// Bind makes n the notifier failures are raised on, until the returned
// func is called. Before Bind, failures are recorded but raise nothing.
func Bind(n Notifier) (unbind func()) {
	mu.Lock()
	prev := notifier
	notifier = n
	mu.Unlock()
	return func() {
		mu.Lock()
		notifier = prev
		mu.Unlock()
	}
}

// Fail records source's credentials as lapsed and, the first time in a
// spell, raises a warning notification saying title and detail. It
// reports whether the spell is new.
func Fail(ctx context.Context, source, title, detail string, now time.Time) bool {
	mu.Lock()
	f, ok := failures[source]
	if ok {
		f.Title, f.Detail, f.Last = title, detail, now
		f.Count++
		failures[source] = f
		mu.Unlock()
		return false
	}
	failures[source] = Failure{Source: source, Title: title, Detail: detail, Since: now, Last: now, Count: 1}
	n := notifier
	mu.Unlock()
	if n != nil {
		if _, err := n.RaiseNotification(ctx, model.Notification{
			Subject: Subject(source), Severity: model.NotificationWarning, Title: title, Body: detail,
			RaisedBy: raisedBy, RaisedAt: now,
		}); err != nil {
			log.Printf("credhealth: raising %s: %v", Subject(source), err)
		}
	}
	return true
}

// Clear records source as healthy again and clears its notification,
// reporting whether it had been failing.
func Clear(ctx context.Context, source string) bool {
	mu.Lock()
	_, was := failures[source]
	delete(failures, source)
	n := notifier
	mu.Unlock()
	if was && n != nil {
		if _, err := n.ClearNotification(ctx, Subject(source), raisedBy); err != nil {
			log.Printf("credhealth: clearing %s: %v", Subject(source), err)
		}
	}
	return was
}

// Failures is every source currently failing, ordered by Source.
func Failures() []Failure {
	mu.Lock()
	defer mu.Unlock()
	out := make([]Failure, 0, len(failures))
	for _, f := range failures {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out
}

// Check is a cheap proactive credential check a deployment layer
// registers: a nil error is healthy, and an error's text is the detail
// the operator is shown (what is about to lapse, and what to run).
type Check struct {
	// Title is the notification's title while the check fails.
	Title string
	Run   func(ctx context.Context) error
}

// RegisterCheck adds c under name, run by Watch every CheckInterval,
// until the returned func is called.
func RegisterCheck(name string, c Check) (unregister func()) {
	mu.Lock()
	checks[name] = c
	mu.Unlock()
	return func() {
		mu.Lock()
		delete(checks, name)
		mu.Unlock()
		Clear(context.Background(), "check:"+name)
	}
}

// RunChecks runs every registered check once, recording each failure and
// clearing each pass.
func RunChecks(ctx context.Context, now time.Time) {
	mu.Lock()
	cs := make(map[string]Check, len(checks))
	names := make([]string, 0, len(checks))
	for name, c := range checks {
		cs[name] = c
		names = append(names, name)
	}
	mu.Unlock()
	sort.Strings(names)
	for _, name := range names {
		c := cs[name]
		if c.Run == nil {
			continue
		}
		if err := c.Run(ctx); err != nil {
			Fail(ctx, "check:"+name, c.Title, err.Error(), now)
		} else {
			Clear(ctx, "check:"+name)
		}
	}
}

// Watch binds n and runs the registered checks now and every
// CheckInterval until ctx ends.
func Watch(ctx context.Context, n Notifier) {
	defer Bind(n)()
	RunChecks(ctx, time.Now())
	t := time.NewTicker(CheckInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			RunChecks(ctx, time.Now())
		}
	}
}

// Reset forgets every failure and check, for tests.
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	failures = map[string]Failure{}
	checks = map[string]Check{}
	notifier = nil
}
