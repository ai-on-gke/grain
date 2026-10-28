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

// poll.go is how often grain asks about something that is not changing.
//
// The ETag cache (conditional.go) makes an unchanged read free, but only
// a read GitHub answers 304; a read that misses the cache, an endpoint
// with no ETag and a burst all still cost, and a reconcile cycle every
// thirty seconds across a few dozen pull requests and branches spends
// the hour's budget on things nobody has touched. PollSchedule is the
// other half: a thing that has stopped changing is asked about less and
// less often, up to a cap, and goes straight back to the base cadence the
// moment anything moves.
//
// The rules, in the order they apply:
//
//   - Something active -- CI running, recently pushed, in a merge batch,
//     at the head of the queue -- is due every time it is asked about.
//     That is the behaviour before this file existed, unchanged for
//     exactly the pull requests whose answers are moving.
//   - Something idle is due when its interval has run out. Each poll
//     that observes no change doubles the interval, from PollBase to
//     PollCap; a poll that does observe one resets it to PollBase.
//   - A write grain makes itself (a push, a merge, a comment -- any
//     non-GET through a RESTClient) resets everything about the pull
//     request or branch it wrote to, and a merge resets the whole
//     repository, since every other pull request's mergeability has just
//     moved with its base. Reset does the same for a caller with its own
//     reason, such as an agent waiting on its checks.
//   - While the budget is low (BudgetLow), anything not PollCritical is
//     polled at PollCap whether or not it is active, and PollBackground
//     is not polled at all until the budget recovers. PollCritical --
//     the merge queue's head, base-branch CI -- is never held back: that
//     is what the remaining budget is being kept for.

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

// PollClass is how much a poll is worth when the budget is short.
type PollClass int

const (
	// PollNormal is a pull request or branch read: backed off while
	// idle, and held at PollCap while the budget is low.
	PollNormal PollClass = iota
	// PollCritical is never backed off or held back: the merge queue's
	// head and base-branch CI.
	PollCritical
	// PollBackground is the first thing cut: path triggers, stale
	// branches. Not polled at all while the budget is low.
	PollBackground
)

// The cadence. PollBase is about one reconcile cycle; PollCap is the
// longest anything idle goes unread, which bounds how late grain notices
// a change nobody told it about.
const (
	PollBase = 30 * time.Second
	PollCap  = 8 * time.Minute
)

// pollForget is how long an entry nobody has asked about is kept. Past
// it the pull request has closed or the branch has gone, and the entry
// is dropped so the map is bounded by what is open now.
const pollForget = time.Hour

// PollKey names one thing polled: Subject is the pull request
// ("owner/repo#N") or branch ("owner/repo@branch") it is about, which is
// what a write resets, and Aspect is what about it is read ("review
// comments", "path triggers"). Scope keeps two stores in one process --
// parallel tests -- apart.
type PollKey struct {
	Scope   string
	Subject string
	Aspect  string
}

// PullRequestSubject and BranchSubject are the two Subject spellings.
func PullRequestSubject(owner, repo string, number int) string {
	return strings.ToLower(owner+"/"+repo) + "#" + strconv.Itoa(number)
}

func BranchSubject(owner, repo, branch string) string {
	return strings.ToLower(owner+"/"+repo) + "@" + branch
}

type pollEntry struct {
	interval    time.Duration
	next        time.Time
	fingerprint string
	observed    bool
	asked       time.Time
}

// PollSchedule decides which polls are due. The zero value is ready to
// use; one process-wide schedule (Polls) is what the orchestrator and
// RESTClient's writes share.
type PollSchedule struct {
	mu      sync.Mutex
	entries map[PollKey]*pollEntry
	// low answers whether the budget is low; nil is BudgetLow.
	low func(time.Time) bool
}

var polls = &PollSchedule{}

// Polls is the process-wide schedule.
func Polls() *PollSchedule { return polls }

func (p *PollSchedule) budgetLow(now time.Time) bool {
	p.mu.Lock()
	low := p.low
	p.mu.Unlock()
	if low != nil {
		return low(now)
	}
	return BudgetLow(now)
}

// SetPollBudgetLow makes the process-wide schedule judge the budget by
// low rather than by BudgetLow, and returns what puts it back. It is for
// another package's tests, which cannot fake a credential's rate-limit
// headers without every parallel test seeing the same low budget; a test
// using it must not run in parallel.
func SetPollBudgetLow(low func(time.Time) bool) (restore func()) {
	polls.mu.Lock()
	prev := polls.low
	polls.low = low
	polls.mu.Unlock()
	return func() {
		polls.mu.Lock()
		polls.low = prev
		polls.mu.Unlock()
	}
}

// Due is whether key should be read now. active says the thing is
// moving (CI running, just pushed, in a batch); see the file comment for
// what class does.
func (p *PollSchedule) Due(key PollKey, class PollClass, active bool, now time.Time) bool {
	low := class != PollCritical && p.budgetLow(now)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sweep(now)
	e := p.entry(key)
	e.asked = now
	switch {
	case class == PollCritical:
		return true
	case low && class == PollBackground:
		return false
	case low:
		return !e.observed || !now.Before(e.lastRead().Add(PollCap))
	case active:
		return true
	}
	return !e.observed || !now.Before(e.next)
}

// Observe records what a read of key saw. A fingerprint that differs
// from the last one -- or the first one -- resets the interval to
// PollBase; the same one doubles it, up to PollCap.
func (p *PollSchedule) Observe(key PollKey, fingerprint string, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.entry(key)
	switch {
	case !e.observed || fingerprint != e.fingerprint:
		e.interval = PollBase
	case e.interval < PollCap:
		e.interval *= 2
		if e.interval > PollCap {
			e.interval = PollCap
		}
	}
	e.fingerprint, e.observed, e.asked = fingerprint, true, now
	e.next = now.Add(e.interval)
}

// Interval is key's current backoff, PollBase for anything not yet seen.
func (p *PollSchedule) Interval(key PollKey) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.entries[key]; ok && e.interval > 0 {
		return e.interval
	}
	return PollBase
}

// Reset makes everything about subject due on its next poll, in every
// scope: grain has just acted on it, or somebody asked.
func (p *PollSchedule) Reset(subject string) {
	p.resetWhere(func(k PollKey) bool { return k.Subject == subject })
}

// ResetRepo is Reset for every pull request and branch of a repository.
func (p *PollSchedule) ResetRepo(owner, repo string) {
	prefix := strings.ToLower(owner + "/" + repo)
	p.resetWhere(func(k PollKey) bool {
		return strings.HasPrefix(k.Subject, prefix+"#") || strings.HasPrefix(k.Subject, prefix+"@")
	})
}

func (p *PollSchedule) resetWhere(match func(PollKey) bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, e := range p.entries {
		if match(k) {
			e.interval, e.next = PollBase, time.Time{}
		}
	}
}

func (p *PollSchedule) entry(key PollKey) *pollEntry {
	if p.entries == nil {
		p.entries = map[PollKey]*pollEntry{}
	}
	e := p.entries[key]
	if e == nil {
		e = &pollEntry{interval: PollBase}
		p.entries[key] = e
	}
	return e
}

// lastRead is when the entry was last observed: next less its interval.
func (e *pollEntry) lastRead() time.Time { return e.next.Add(-e.interval) }

func (p *PollSchedule) sweep(now time.Time) {
	for k, e := range p.entries {
		if !e.asked.IsZero() && now.Sub(e.asked) > pollForget {
			delete(p.entries, k)
		}
	}
}

// noteWrite is what a RESTClient write tells the schedule: the subject
// path names is reset, and a merge resets its whole repository.
func noteWrite(method, path string) {
	if method == "" || method == "GET" {
		return
	}
	owner, repo, rest, ok := splitRepoPath(path)
	if !ok {
		return
	}
	segs := strings.Split(rest, "/")
	switch {
	case rest == "merges", len(segs) >= 3 && segs[0] == "pulls" && segs[2] == "merge":
		polls.ResetRepo(owner, repo)
		return
	case len(segs) >= 2 && (segs[0] == "pulls" || segs[0] == "issues"):
		if n, ok := atoi(segs[1]); ok {
			polls.Reset(PullRequestSubject(owner, repo, n))
		}
	case len(segs) >= 4 && segs[0] == "git" && segs[1] == "refs" && segs[2] == "heads":
		polls.Reset(BranchSubject(owner, repo, strings.Join(segs[3:], "/")))
	}
}

// splitRepoPath is /repos/{owner}/{repo}/{rest}, query dropped.
func splitRepoPath(path string) (owner, repo, rest string, ok bool) {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	parts := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 4)
	if len(parts) < 4 || parts[0] != "repos" {
		return "", "", "", false
	}
	return parts[1], parts[2], parts[3], true
}
