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

package github

// budget.go is who is spending the hour's budget, and how long it lasts.
//
// stats.go counts what one transport sent, by call shape. That answered
// "is revalidation working" and could not answer the question the
// 2026-10-26 exhaustion asked: *which part of grain* spent 5,000 calls
// in an hour. A call shape is shared by everybody -- a branch-head read
// is a merge-queue head check, a base-branch CI poll and a path trigger
// all at once -- and the transports are not one per process either: the
// daemon has its own, and every agent tool call builds another
// (pkg/daemon's PullRequestReader). The rate limit belongs to the
// credential, not to any of them.
//
// So this keeps a second, process-wide ledger beside the per-transport
// counts. Every request through any RealTransport is charged to the
// caller label it carried (CallerHeader, set by WithCaller) and, within
// that, to a coarse kind of read -- head read, PR read, checks, review
// comments -- that names what the caller was doing. Both dimensions are
// bounded by code rather than by traffic: callers are reconciler names
// and a handful of daemon paths, kinds are the table in kindOf.
//
// And it keeps the budget itself: the X-RateLimit headers of every
// response, per credential, sampled often enough to say how fast the
// remainder is falling and so when it will run out. That projection is
// what PollSchedule throttles against (Low), and the reason for it is
// the incident: GitHub's own cut-off is the first anybody heard of the
// budget, and by then every pull request in the merge queue had gone
// unreadable for a quarter of an hour.

import (
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// CallerHeader carries a request's caller label from RESTClient to the
// transport. It never leaves the host: RealTransport strips it before
// sending, and it is not part of the ETag cache's key, so two callers
// reading the same resource share one cached answer.
const CallerHeader = "X-Grain-Caller"

// RepoHeader carries the owner/repo a RESTClient call is about to the
// transport, so a router underneath (pkg/daemon's forgeRouter) still
// knows which repository a follow-up request whose path does not begin
// with /repos/ (such as a Link-header page under /repositories/<id>/...)
// belongs to. Like CallerHeader, RealTransport strips it before sending.
const RepoHeader = "X-Grain-Repo"

// Unattributed is the label a request with none is charged to. A row of
// it in the metrics is a call site somebody has not labelled yet.
const Unattributed = "unattributed"

// LowBudgetFraction is the share of the hour's budget below which the
// budget counts as low: PollSchedule stops polling what can wait, so
// what is left goes to the merge queue's head and base-branch CI rather
// than to GitHub's own cut-off.
const LowBudgetFraction = 0.15

// WithCaller is c with every request it makes charged to caller.
//
// A client that is not a RESTClient -- a test's fake, a wrapper -- is
// returned unchanged: there is no transport under it to charge, and
// returning a different type would break the optional-interface
// assertions callers make of a client (reviewThreadLister and the like).
func WithCaller(c Client, caller string) Client {
	switch c := c.(type) {
	case *RESTClient:
		if c != nil {
			return c.As(caller)
		}
	case CallerLabeler:
		return c.WithCaller(caller)
	}
	return c
}

// CallerLabeler is a wrapper around a Client that can pass a caller
// label through to the RESTClient underneath it, keeping its own type.
type CallerLabeler interface {
	WithCaller(caller string) Client
}

// As is a copy of c whose requests are charged to caller. The copy
// shares c's transport, and with it the ETag cache and the throttle.
func (c *RESTClient) As(caller string) *RESTClient {
	out := *c
	out.Caller = caller
	return &out
}

// CallerStats is one caller's share of the spend.
type CallerStats struct {
	// Sent is requests that left the host for this caller; Revalidated
	// the subset answered 304, which are free. Sent - Revalidated is
	// what it cost against the rate limit.
	Sent        int64 `json:"sent"`
	Revalidated int64 `json:"revalidated"`
	Failed      int64 `json:"failed"`
	// Declined is calls refused without sending, by the throttle.
	Declined int64 `json:"declined"`
	// Kinds is the same per kind of read (kindOf).
	Kinds map[string]KindStats `json:"kinds,omitempty"`
}

// KindStats is one kind of call's share of a caller.
type KindStats struct {
	Sent        int64 `json:"sent"`
	Revalidated int64 `json:"revalidated"`
}

// BudgetSnapshot is the hour's budget for the credential closest to
// running out, and how long it will last at the rate it is going.
type BudgetSnapshot struct {
	RateLimitSnapshot
	// Credentials is how many distinct credentials have reported a
	// budget recently; the snapshot is the tightest of them.
	Credentials int `json:"credentials"`
	// SpendPerHour is how fast Remaining has been falling over the last
	// budgetWindow, as an hourly rate. Zero when there is not enough
	// history in the current hour to say.
	SpendPerHour float64 `json:"spendPerHour"`
	// ExhaustedAt is when Remaining reaches zero at SpendPerHour, or the
	// zero time when it lasts until Resets.
	ExhaustedAt time.Time `json:"exhaustedAt,omitempty"`
	// Low is Remaining under LowBudgetFraction of Limit: non-critical
	// polling is cut back while it holds.
	Low bool `json:"low"`
	// Polls summarizes the adaptive poll schedule across tracked pull
	// requests (tracked count, backed-off count, median and max interval).
	Polls PollStats `json:"polls"`
}

// budgetWindow is how far back the spend rate is measured. Long enough
// to smooth over a reconcile cycle's burst, short enough to follow a
// change in what the deployment is doing.
const budgetWindow = 10 * time.Minute

// budgetStale is how long a credential's budget is believed without a
// fresh response. A per-run credential nothing has used for this long
// is not what the deployment is spending.
const budgetStale = 15 * time.Minute

type budgetSample struct {
	at        time.Time
	remaining int
}

type credentialBudget struct {
	limit    RateLimitSnapshot
	samples  []budgetSample // oldest first, within budgetWindow, all in limit.Resets' hour
	low      bool
	credName string // a short hash, for the log line
}

// callerLedger is the process-wide record. One of them (ledger) serves
// every transport; tests build their own.
type callerLedger struct {
	mu      sync.Mutex
	callers map[string]*CallerStats
	budgets map[string]*credentialBudget
	// logf is where a change in the low-budget state is said, once per
	// change. nil is log.Printf.
	logf func(string, ...any)
}

var ledger = &callerLedger{}

func (l *callerLedger) caller(name string) *CallerStats {
	if name == "" {
		name = Unattributed
	}
	if l.callers == nil {
		l.callers = map[string]*CallerStats{}
	}
	c := l.callers[name]
	if c == nil {
		c = &CallerStats{Kinds: map[string]KindStats{}}
		l.callers[name] = c
	}
	return c
}

func (l *callerLedger) record(caller, method, path string, revalidated, failed bool) {
	kind := kindOf(method, endpointOf(path))
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.caller(caller)
	c.Sent++
	k := c.Kinds[kind]
	k.Sent++
	if revalidated {
		c.Revalidated++
		k.Revalidated++
	}
	if failed {
		c.Failed++
	}
	c.Kinds[kind] = k
}

func (l *callerLedger) recordDeclined(caller string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.caller(caller).Declined++
}

// noteLimit takes one response's rate-limit headers for the credential
// that sent it.
func (l *callerLedger) noteLimit(authorization string, headers map[string]string, now time.Time) {
	limit, hasLimit := atoi(headerValue(headers, "X-RateLimit-Limit"))
	remaining, hasRemaining := atoi(headerValue(headers, "X-RateLimit-Remaining"))
	if !hasLimit || !hasRemaining || limit <= 0 {
		return
	}
	// Only the core REST budget: GraphQL and search have budgets of
	// their own, and mixing their remainders in would make the core one
	// look like it jumps about.
	if res := headerValue(headers, "X-RateLimit-Resource"); res != "" && res != "core" {
		return
	}
	snap := RateLimitSnapshot{Limit: limit, Remaining: remaining, Seen: now}
	if reset, ok := unixSeconds(headerValue(headers, "X-RateLimit-Reset")); ok {
		snap.Resets = reset
	}
	sum := sha256.Sum256([]byte(authorization))
	key := hex.EncodeToString(sum[:4])

	l.mu.Lock()
	if l.budgets == nil {
		l.budgets = map[string]*credentialBudget{}
	}
	b := l.budgets[key]
	if b == nil {
		b = &credentialBudget{credName: key}
		l.budgets[key] = b
	}
	// A new hour (a different reset time) starts a new history: the
	// remainder jumped back up, and that is not negative spend.
	if !snap.Resets.Equal(b.limit.Resets) {
		b.samples = b.samples[:0]
	}
	b.limit = snap
	if n := len(b.samples); n == 0 || now.Sub(b.samples[n-1].at) >= 5*time.Second {
		b.samples = append(b.samples, budgetSample{at: now, remaining: remaining})
	}
	cut := 0
	for cut < len(b.samples)-1 && now.Sub(b.samples[cut].at) > budgetWindow {
		cut++
	}
	b.samples = b.samples[cut:]
	low := float64(remaining) < LowBudgetFraction*float64(limit)
	changed := low != b.low
	b.low = low
	logf := l.logf
	l.mu.Unlock()

	if changed {
		if logf == nil {
			logf = log.Printf
		}
		if low {
			logf("github: credential %s has %d of %d calls left this hour (under %.0f%%); "+
				"polling idle pull requests, stale branches and path triggers is cut back until it recovers",
				key, remaining, limit, LowBudgetFraction*100)
		} else {
			logf("github: credential %s has %d of %d calls left this hour; polling is back to normal",
				key, remaining, limit)
		}
	}
}

// low is whether any credential heard from recently is low.
func (l *callerLedger) low(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, b := range l.budgets {
		if b.low && now.Sub(b.limit.Seen) < budgetStale {
			return true
		}
	}
	return false
}

// budget is the tightest recent credential's budget and its projection.
func (l *callerLedger) budget(now time.Time) BudgetSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	var (
		out      BudgetSnapshot
		tightest *credentialBudget
		frac     float64
	)
	for _, b := range l.budgets {
		if now.Sub(b.limit.Seen) >= budgetStale {
			continue
		}
		out.Credentials++
		f := float64(b.limit.Remaining) / float64(b.limit.Limit)
		if tightest == nil || f < frac {
			tightest, frac = b, f
		}
	}
	if tightest == nil {
		return out
	}
	out.RateLimitSnapshot = tightest.limit
	out.Low = tightest.low
	if n := len(tightest.samples); n >= 2 {
		first, last := tightest.samples[0], tightest.samples[n-1]
		elapsed := last.at.Sub(first.at)
		if elapsed >= 30*time.Second && first.remaining > last.remaining {
			out.SpendPerHour = float64(first.remaining-last.remaining) / elapsed.Hours()
		}
	}
	if out.SpendPerHour > 0 {
		at := out.Seen.Add(time.Duration(float64(out.Remaining) / out.SpendPerHour * float64(time.Hour)))
		if out.Resets.IsZero() || at.Before(out.Resets) {
			out.ExhaustedAt = at
		}
	}
	return out
}

func (l *callerLedger) callerStats() map[string]CallerStats {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.callers) == 0 {
		return nil
	}
	out := make(map[string]CallerStats, len(l.callers))
	for name, c := range l.callers {
		cp := *c
		cp.Kinds = make(map[string]KindStats, len(c.Kinds))
		for k, v := range c.Kinds {
			cp.Kinds[k] = v
		}
		out[name] = cp
	}
	return out
}

// Callers is the process-wide spend by caller label.
func Callers() map[string]CallerStats { return ledger.callerStats() }

// Budget is the hour's budget for the tightest credential this process
// has heard about recently, with its projection.
func Budget(now time.Time) BudgetSnapshot { return ledger.budget(now) }

// BudgetLow is whether the budget is under LowBudgetFraction for any
// credential heard from recently.
func BudgetLow(now time.Time) bool { return ledger.low(now) }

// kindOf names what a call is doing, in the words an operator thinks
// in, from its method and call shape (endpointOf). A shape this does
// not know is "other" rather than a new row, so the table stays bounded
// by this function rather than by the paths a future call site sends.
//
// GraphQL is named before the method is looked at: every GraphQL call is
// a POST, reads included, and it is charged to a budget of its own
// (X-RateLimit-Resource: graphql), so it is a row of its own rather than
// a "write". Both places a host serves it are recognised -- /graphql on
// api.github.com and /api/graphql on a GitHub Enterprise Server -- for
// whichever of them a transport records.
func kindOf(method, endpoint string) string {
	if endpoint == "/graphql" || endpoint == "/api/graphql" {
		return "graphql"
	}
	if method != "" && method != http.MethodGet {
		return "write"
	}
	has := func(s string) bool { return strings.Contains(endpoint, s) }
	switch {
	case has("/check-runs"), has("/check-suites"), has("/status"), has("/actions/"):
		return "checks"
	case has("/pulls/{n}/comments"), has("/pulls/{n}/reviews"):
		return "review comments"
	case has("/issues/{n}/comments"), has("/issues/comments"):
		return "comments"
	case has("/pulls/{n}/files"), has("/pulls/{n}/commits"):
		return "PR files"
	case strings.HasSuffix(endpoint, "/pulls/{n}"):
		return "PR read"
	case has("/pulls"):
		return "PR list"
	case has("/branches/"), has("/git/ref"):
		return "head read"
	case strings.HasSuffix(endpoint, "/branches"):
		return "branch list"
	case has("/compare/"):
		return "compare"
	case has("/commits"):
		return "commits"
	case has("/contents/"), has("/git/trees"), has("/git/blobs"):
		return "contents"
	case has("/issues"):
		return "issues"
	case strings.Count(endpoint, "/") == 3 && strings.HasPrefix(endpoint, "/repos/"):
		return "repo read"
	}
	return "other"
}
