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

// Package change is core grain's seam to a task's change when that change
// is not a GitHub pull request -- a changelist CL, today
// (docs/design/changelists.md#G3-2, §2.5).
//
// The orchestrator, pkg/mcp, the UI and metrics live in core and must
// not import google/. An edition registers a Provider here for the link
// kind its changes are recorded under (model.LinkChangelist), and an
// Opener for the change-target kind whose open_pull_request it answers
// ("changelist"). Core asks this registry about non-PR change links and
// never names the edition; code for pull requests is untouched.
//
// Providers do not reach their forge themselves. They call a capability
// server's daemon-only control tools (the capability server's observe and
// open_change) through the Control the daemon binds at startup, so the
// capability server stays the one place that touches the operator's
// tools, enforces confinement and writes the audit.
package change

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Health is a change's forge-neutral health, the shape forge.Health has
// for a pull request.
type Health string

const (
	HealthOK         Health = "ok"
	HealthPending    Health = "pending"
	HealthFailing    Health = "failing"
	HealthConflicted Health = "conflicted"
	HealthBehind     Health = "behind"
	HealthUnknown    Health = "unknown"
)

// Status is where a change is in its life, as a work tree shows it.
type Status string

const (
	StatusPending   Status = "pending"
	StatusMailed    Status = "mailed"
	StatusLGTM      Status = "lgtm"
	StatusSubmitted Status = "submitted"
	StatusAbandoned Status = "abandoned"
)

// Check is one check on a change's current snapshot.
type Check struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

// Observation is what a provider learned about one change.
type Observation struct {
	// Target is the link target observed, "cl/<number>".
	Target    string  `json:"target"`
	Status    Status  `json:"status"`
	Health    Health  `json:"health"`
	Snapshot  int     `json:"snapshot,omitempty"`
	Presubmit string  `json:"presubmit,omitempty"`
	Checks    []Check `json:"checks,omitempty"`
	URL       string  `json:"url,omitempty"`
}

// Thread is one review thread on a change.
type Thread struct {
	ID       string `json:"id"`
	Author   string `json:"author"`
	Body     string `json:"body"`
	Resolved bool   `json:"resolved"`
}

// Person is who asked for a Submit.
type Person struct{ Name string }

// Result is a Submit's outcome.
type Result struct {
	Target  string
	Message string
}

// SyncResult is a Sync's outcome.
type SyncResult struct {
	To      string
	Message string
}

// Provider answers for one kind of non-PR change link. Only Observe is
// used before stage S3; the rest answer ErrNotSupported until then.
type Provider interface {
	Kind() model.LinkKind
	Observe(ctx context.Context, task model.Task, target string) (Observation, error)
	Threads(ctx context.Context, task model.Task, target string) ([]Thread, error)
	Submit(ctx context.Context, task model.Task, target string, by Person) (Result, error)
	Sync(ctx context.Context, task model.Task, to string) (SyncResult, error)
}

// OpenRequest is what open_pull_request hands an Opener: the change
// description the agent wrote, and the deployment's external link to the
// task ("" when no external UI base is configured).
type OpenRequest struct {
	Summary string
	Body    string
	// Trailers are the task's lineage trailers ("Origin: task 831",
	// "Filed-by: task 1401").
	Trailers []string
	TaskURL  string
}

// OpenResult is one open_pull_request on a non-git task: the change now
// holding the task's work and the state of its checks.
type OpenResult struct {
	// Target is the link target, "cl/<number>".
	Target    string
	URL       string
	Created   bool
	Snapshot  int
	Presubmit string
	Note      string
}

// Opener is a Provider's answer to open_pull_request on a task whose
// change target is TargetKind.
type Opener interface {
	TargetKind() string
	Open(ctx context.Context, task model.Task, req OpenRequest) (OpenResult, error)
}

// ErrNotSupported is a Provider method its stage has not built yet.
var ErrNotSupported = errors.New("not supported for this change kind yet")

// CredentialsExpiredError is a provider's forge refusing because the
// operator's credentials have expired (changelist: workstation credentials, §8.3). It is
// never read as a change's state: the deployment's health for Kind is
// blocked on credentials until a Check passes again.
type CredentialsExpiredError struct {
	Kind   string
	Detail string
}

func (e *CredentialsExpiredError) Error() string {
	msg := "credentials_expired: the operator's corp credentials have expired"
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// IsCredentialsExpired reports whether err is, or wraps, a
// CredentialsExpiredError, or carries the credentials_expired error code
// a capability server's tool answer starts with.
func IsCredentialsExpired(err error) bool {
	if err == nil {
		return false
	}
	var ce *CredentialsExpiredError
	return errors.As(err, &ce) || strings.Contains(err.Error(), CredentialsExpiredCode)
}

// CredentialsExpiredCode is the typed error code a capability server
// answers with when its operator credentials have expired.
const CredentialsExpiredCode = "credentials_expired"

// Control is how a provider reaches capability servers' daemon-only
// control tools: the first attached server offering tool answers, with
// its data-plane token added as the "token" argument.
type Control interface {
	CallControl(ctx context.Context, tool string, args map[string]any) ([]byte, error)
}

var (
	mu        sync.RWMutex
	providers = map[model.LinkKind]Provider{}
	openers   = map[string]Opener{}
	control   Control
)

// Register adds p for its Kind, and as an Opener for its TargetKind when
// it is one.
func Register(p Provider) {
	mu.Lock()
	defer mu.Unlock()
	if p.Kind() == "" {
		panic("change.Register: empty Kind")
	}
	providers[p.Kind()] = p
	if o, ok := p.(Opener); ok && o.TargetKind() != "" {
		openers[o.TargetKind()] = o
	}
}

// Unregister removes the provider for kind, and its Opener. For tests,
// which register fakes into this process-wide registry.
func Unregister(kind model.LinkKind) {
	mu.Lock()
	defer mu.Unlock()
	if p, ok := providers[kind]; ok {
		if o, ok := p.(Opener); ok {
			delete(openers, o.TargetKind())
		}
	}
	delete(providers, kind)
}

// Lookup is the provider registered for kind.
func Lookup(kind model.LinkKind) (Provider, bool) {
	mu.RLock()
	defer mu.RUnlock()
	p, ok := providers[kind]
	return p, ok
}

// OpenerFor is the Opener for a change-target kind.
func OpenerFor(targetKind string) (Opener, bool) {
	mu.RLock()
	defer mu.RUnlock()
	o, ok := openers[targetKind]
	return o, ok
}

// Kinds is every registered provider's link kind, sorted.
func Kinds() []model.LinkKind {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]model.LinkKind, 0, len(providers))
	for k := range providers {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Bind sets the Control providers reach capability servers through.
func Bind(c Control) {
	mu.Lock()
	defer mu.Unlock()
	control = c
}

// CallControl calls tool through the bound Control.
func CallControl(ctx context.Context, tool string, args map[string]any) ([]byte, error) {
	mu.RLock()
	c := control
	mu.RUnlock()
	if c == nil {
		return nil, fmt.Errorf("no capability server is reachable from here to answer %s", tool)
	}
	return c.CallControl(ctx, tool, args)
}

// Changes is task's change links a registered provider answers for, in
// the order they were linked: the first is the task's primary change.
func Changes(task model.Task) []model.Link {
	var out []model.Link
	for _, l := range task.Links {
		if _, ok := Lookup(l.Kind); ok || l.Kind == model.LinkChangelist {
			out = append(out, l)
		}
	}
	return out
}

// Primary is task's primary non-PR change -- its first changelist --
// or "" when it has none.
func Primary(task model.Task) string {
	for _, l := range task.Links {
		if l.Kind == model.LinkChangelist {
			return l.Target
		}
	}
	return ""
}

// HealthState is a deployment's health for one change kind: blocked on
// the operator's credentials, or not.
type HealthState struct {
	Blocked string    `json:"blocked,omitempty"` // "credentials"
	Detail  string    `json:"detail,omitempty"`
	Since   time.Time `json:"since,omitempty"`
}

// BlockedCredentials is HealthState.Blocked for expired credentials.
const BlockedCredentials = "credentials"

var (
	healthMu sync.Mutex
	health   = map[string]HealthState{}
)

// MarkBlocked records kind's deployment health as blocked for reason,
// keeping the first moment it was seen. It reports whether this is new.
func MarkBlocked(kind, reason, detail string, now time.Time) bool {
	healthMu.Lock()
	defer healthMu.Unlock()
	if h, ok := health[kind]; ok && h.Blocked == reason {
		h.Detail = detail
		health[kind] = h
		return false
	}
	health[kind] = HealthState{Blocked: reason, Detail: detail, Since: now}
	return true
}

// ClearBlocked records kind as healthy again, reporting whether it had
// been blocked.
func ClearBlocked(kind string) bool {
	healthMu.Lock()
	defer healthMu.Unlock()
	_, was := health[kind]
	delete(health, kind)
	return was
}

// HealthOf is kind's recorded health; the zero HealthState is healthy.
func HealthOf(kind string) HealthState {
	healthMu.Lock()
	defer healthMu.Unlock()
	return health[kind]
}
