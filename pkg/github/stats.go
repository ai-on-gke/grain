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

// stats.go is how a deployment finds out what it is spending.
//
// The rate-limit work of 2026-09-08 (docs/github-api-rate-limits.md)
// made two claims and could substantiate neither: that a throttle no
// longer gets worse, and that a poll of something unchanged now costs
// nothing. Both are true of the code and neither was visible from
// outside it -- there was no count of requests, no count of the 304s
// that are the whole saving, and nowhere an operator could see how much
// of the hour's budget was left. The one number anybody actually had was
// the moment GitHub started refusing.
//
// So every request through RealTransport is counted, by endpoint and by
// what it cost, and the newest rate-limit headers GitHub sent are kept
// beside them. That is enough to answer the three questions worth
// asking: are we near the limit, what is spending it, and is
// revalidation working.
//
// # Why endpoints and not paths
//
// A path carries an owner, a repository, a number and a sha, so counting
// paths would produce one row per pull request and answer nothing.
// endpointOf reduces a path to its shape --
// /repos/{owner}/{repo}/commits/{sha}/check-runs -- which is the unit an
// operator can act on: it names the *call site*, and a call site is a
// piece of code somebody can go and change.
//
// # What this deliberately does not do
//
// No timing, no histogram, no per-repository breakdown. Latency is not
// the problem this deployment had and a dimension nobody reads is a
// dimension that costs memory on a daemon that runs for weeks. The
// endpoint map is bounded by the number of call sites in this package,
// which is a compile-time quantity rather than a function of traffic.

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

// Stats is one transport's whole record of what it has spent.
type Stats struct {
	// Sent is requests that actually left this host.
	Sent int64 `json:"sent"`
	// Revalidated is those GitHub answered 304 -- sent, but free against
	// the primary rate limit, and served out of the ETag cache
	// (conditional.go). The ratio of this to Sent is whether
	// revalidation is working.
	Revalidated int64 `json:"revalidated"`
	// Declined is calls this process refused itself, without sending
	// anything, because GitHub had already said to stop (ratelimit.go).
	// Non-zero means the deployment is being throttled right now.
	Declined int64 `json:"declined"`
	// Failed is responses GitHub refused -- any 4xx or 5xx, including
	// the throttling ones, which is why this and Declined are separate:
	// the first refusal is Failed and everything behind it is Declined.
	Failed int64 `json:"failed"`
	// Retried is requests this transport sent a second time because the
	// first attempt got a 5xx rather than an answer (retry.go). A subset
	// of Sent, since the retry is itself a request: it is what the retry
	// costs, and the difference between it and the Failed 5xxs is what
	// the retry saved.
	Retried int64 `json:"retried"`
	// Endpoints is the same counts per call shape, which is what says
	// *what* is spending the budget.
	Endpoints map[string]EndpointStats `json:"endpoints,omitempty"`
	// Limit is the newest rate-limit headers seen, or the zero value on
	// a deployment that has made no call yet.
	Limit RateLimitSnapshot `json:"limit"`
	// Callers is the process-wide spend by caller label and, within
	// that, by kind of call (budget.go) -- every transport in this
	// process, not this one alone, since the rate limit belongs to the
	// credential.
	Callers map[string]CallerStats `json:"callers,omitempty"`
	// Budget is the tightest credential's budget, with how fast it is
	// falling and when it runs out at that rate.
	Budget BudgetSnapshot `json:"budget"`
	// Polls summarizes the adaptive poll schedule across tracked pull
	// requests.
	Polls PollStats `json:"polls"`
	// Cache is this transport's ETag cache occupancy and evictions.
	Cache CacheStats `json:"cache"`
}

// EndpointStats is one call shape's share.
type EndpointStats struct {
	Sent        int64 `json:"sent"`
	Revalidated int64 `json:"revalidated"`
	Failed      int64 `json:"failed"`
}

// RateLimitSnapshot is what GitHub last said about the budget.
//
// Read off whatever response carried the headers rather than by asking
// /rate_limit: that endpoint is free, but a number that arrives with the
// traffic cannot be stale in a way the traffic is not.
type RateLimitSnapshot struct {
	// Limit and Remaining are the hour's budget and what is left of it.
	// Zero for both means nothing has been seen yet, which is not the
	// same as a budget of zero -- Seen says which.
	Limit     int       `json:"limit"`
	Remaining int       `json:"remaining"`
	Resets    time.Time `json:"resets"`
	// Seen is when these headers arrived. Zero means no response has
	// carried any.
	Seen time.Time `json:"seen"`
}

// stats is the live counter behind Stats, guarded because a reconcile
// cycle's dispatch goroutines and a UI request share one transport.
type stats struct {
	mu          sync.Mutex
	sent        int64
	revalidated int64
	declined    int64
	failed      int64
	retried     int64
	endpoints   map[string]EndpointStats
	limit       RateLimitSnapshot
}

func (s *stats) record(endpoint string, revalidated, failed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent++
	if s.endpoints == nil {
		s.endpoints = map[string]EndpointStats{}
	}
	e := s.endpoints[endpoint]
	e.Sent++
	if revalidated {
		s.revalidated++
		e.Revalidated++
	}
	if failed {
		s.failed++
		e.Failed++
	}
	s.endpoints[endpoint] = e
}

func (s *stats) recordDeclined() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.declined++
}

// recordRetried is called when a 5xx is about to be asked again. Counted
// where the decision is made rather than where the second request is
// sent, so it counts retries taken and not requests that happen to be
// second attempts.
func (s *stats) recordRetried() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retried++
}

// noteLimit keeps the newest headers. Newest by arrival rather than by
// Remaining: a lower number is not necessarily later, since concurrent
// calls come back out of order, and what an operator wants is the last
// thing GitHub actually said.
func (s *stats) noteLimit(headers map[string]string, now time.Time) {
	limit, hasLimit := atoi(headerValue(headers, "X-RateLimit-Limit"))
	remaining, hasRemaining := atoi(headerValue(headers, "X-RateLimit-Remaining"))
	if !hasLimit && !hasRemaining {
		return
	}
	snapshot := RateLimitSnapshot{Limit: limit, Remaining: remaining, Seen: now}
	if reset, ok := unixSeconds(headerValue(headers, "X-RateLimit-Reset")); ok {
		snapshot.Resets = reset
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limit = snapshot
}

func (s *stats) snapshot() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Stats{
		Sent: s.sent, Revalidated: s.revalidated, Declined: s.declined, Failed: s.failed,
		Retried: s.retried, Limit: s.limit,
	}
	if len(s.endpoints) > 0 {
		out.Endpoints = make(map[string]EndpointStats, len(s.endpoints))
		for k, v := range s.endpoints {
			out.Endpoints[k] = v
		}
	}
	return out
}

func atoi(s string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, false
	}
	return n, true
}

// Stats returns what this transport has spent so far.
//
// A snapshot rather than a live view: a caller rendering it should not
// be able to see the counts move underneath it, and a caller holding it
// should not be able to write into the transport's own map.
func (t *RealTransport) Stats() Stats {
	t.init()
	now := time.Now()
	out := t.counts.snapshot()
	out.Callers = t.ledgerOf().callerStats()
	out.Budget = t.ledgerOf().budget(now)
	out.Polls = polls.Stats(now)
	if out.Budget.Polls.Tracked == 0 {
		out.Budget.Polls = out.Polls
	}
	out.Cache = t.cache.stats()
	return out
}

// endpointOf reduces a request path to the call shape it belongs to, so
// the counts name a piece of code rather than a pull request.
//
// The rule is positional and deliberately dumb: after /repos, the next
// two segments are an owner and a repository whatever they look like,
// and after that anything that could be an identifier -- a number, a
// sha, anything long and hex -- is one. A segment this cannot recognise
// is left alone, which keeps a path this package grows later readable in
// the counts without anybody having to update a table here.
//
// Query strings go entirely: per_page and page are pagination rather
// than call shape, and head_sha is an identifier by another route.
func endpointOf(path string) string {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	segments := strings.Split(strings.Trim(path, "/"), "/")
	for i, seg := range segments {
		switch {
		case i == 1 && len(segments) > 2 && segments[0] == "repos":
			segments[i] = "{owner}"
		case i == 2 && len(segments) > 2 && segments[0] == "repos":
			segments[i] = "{repo}"
		case isNumber(seg):
			segments[i] = "{n}"
		case looksLikeSHA(seg):
			segments[i] = "{sha}"
		}
	}
	return "/" + strings.Join(segments, "/")
}

func isNumber(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// looksLikeSHA is deliberately loose: seven hex characters or more,
// which catches an abbreviated sha as well as a full one. A branch name
// that happens to be all hex and that long is misfiled as {sha}, which
// costs one mislabelled row in a diagnostic and is worth less than the
// table of exact positions the alternative would need.
func looksLikeSHA(s string) bool {
	if len(s) < 7 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}
