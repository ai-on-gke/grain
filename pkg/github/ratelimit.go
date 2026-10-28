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

// ratelimit.go is what this package does when GitHub says stop.
//
// Nothing here used to. Every call went out the moment its caller made
// it, every refusal came back as an ordinary *Error, and the next poll
// tick thirty seconds later made exactly the same calls again. That is
// the wrong answer to both of GitHub's limits and actively the wrong
// answer to one of them:
//
//   - The primary limit is a budget per hour per credential (5,000 for a
//     personal access token). Spending it is nobody's fault; carrying on
//     calling once it is spent buys nothing, because every one of those
//     calls is refused, and it hides the exhaustion behind a stream of
//     ordinary-looking errors from whichever reconciler happened to make
//     the call.
//   - The secondary limits are GitHub asking for a pause -- a burst too
//     fast, too many concurrent requests, too many writes in a minute --
//     and they come with a `Retry-After` naming how long. Ignoring that
//     header is what turns a pause into a longer pause: GitHub's own
//     documentation says a client that keeps calling through one may
//     have the limit extended or be banned outright.
//
// So a refusal now closes a gate, and the gate is asked before the
// request is built rather than after it has been sent. While it is shut,
// every call through this transport is refused here, at no cost to
// anybody's budget and without a packet leaving the host, with a
// *RateLimitError saying when it opens again. Callers need no change:
// they were already treating a refusal as an error to report and retry
// on the next cycle, which is exactly the right behaviour -- the only
// thing that was missing was grain not making the call in the meantime.
//
// The gate belongs to the transport rather than to the process, and is
// keyed by credential underneath that: a deployment whose ladder holds
// one token for its own repositories and another for somebody else's
// must not have the first one's exhaustion stop the second one working.
//
// What this deliberately does not do is wait. A reconcile cycle is a
// timer, not a queue, and blocking inside a GitHub call would hold up
// every other repository's merge queue and the tick's own dispatch to
// buy one read that the next tick would make anyway. The cycle is the
// retry; this only stops it spending calls it has been told will fail.
//
// docs/github-api-rate-limits.md is where the whole of it is written
// down, including the arithmetic of what grain spends per cycle.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// defaultThrottleWait is how long the gate stays shut when GitHub has
// refused a call for rate limiting but named no instant to come back at
// -- a secondary limit with no `Retry-After`, which is a shape GitHub
// does send.
//
// A minute, which is two poll intervals: long enough that the burst that
// earned the refusal is over, short enough that a merge waiting on the
// other side of it is not waiting on anything a person would notice.
const defaultThrottleWait = time.Minute

// maxThrottleWait bounds what any header can ask for. GitHub's primary
// limit resets on the hour and its secondary limits are measured in
// minutes, so nothing legitimate is longer than this -- and a header
// that says otherwise (a proxy in front of GitHub, a clock a long way
// out) must not be able to switch a deployment's GitHub access off for a
// day.
const maxThrottleWait = time.Hour

// RateLimitError is a call this process refused on GitHub's behalf,
// because GitHub had already refused one and said when to come back.
//
// It carries the instant rather than only a sentence so that a caller
// which wants to say "not until 14:32" can, and so that this reads as
// what it is in a log: grain declining to make a call, not GitHub
// declining to answer one.
type RateLimitError struct {
	// Until is when the gate opens again.
	Until time.Time
	// Reason is what GitHub said when it shut, kept so that the tenth
	// refused call still names the cause rather than only the clock.
	Reason string
}

func (e *RateLimitError) Error() string {
	wait := time.Until(e.Until).Round(time.Second)
	if wait < 0 {
		wait = 0
	}
	if e.Reason == "" {
		return fmt.Sprintf("GitHub is rate limiting this deployment; not calling again for %s", wait)
	}
	return fmt.Sprintf("GitHub is rate limiting this deployment (%s); not calling again for %s",
		e.Reason, wait)
}

// IsRateLimited reports whether err is this deployment being throttled --
// either GitHub's own refusal, or this package declining to make a call
// while a refusal it already had is still in force.
//
// It is the predicate IsPermissionDenied is careful *not* to be. That
// one answers "a permission this credential can never hold" and latches
// auto-merge off on the strength of it, so it excludes rate limiting by
// name (see its own doc comment). This is the other side of the same
// split: a caller that wants to say "this will work again shortly"
// rather than "this will never work" asks here.
//
// Message-sniffing for GitHub's own refusals, because the status alone
// says nothing: 403 is also SAML enforcement, an IP allow list and a
// missing permission, and 429 is not what the primary limit answers with
// at all. GitHub's wording is stable and specific -- "API rate limit
// exceeded", "You have exceeded a secondary rate limit", and the older
// "abuse detection mechanism" -- and anything this cannot positively
// identify stays an ordinary error, which is the safe direction: a
// missed case costs the behaviour this package had before it existed.
func IsRateLimited(err error) bool {
	var refused *RateLimitError
	if errors.As(err, &refused) {
		return true
	}
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	return rateLimitStatus(e.Status) && rateLimitWording(errorMessage(e.Body))
}

// rateLimitStatus is the two statuses GitHub throttles with. 403 is what
// both the primary limit and (usually) the secondary limits answer;
// 429 is what some secondary limits answer instead, and GitHub's own
// documentation names both.
func rateLimitStatus(status int) bool { return status == 403 || status == 429 }

// rateLimitWording reports whether GitHub's own sentence is one of the
// throttling ones.
func rateLimitWording(said string) bool {
	said = strings.ToLower(said)
	return strings.Contains(said, "rate limit") ||
		strings.Contains(said, "abuse detection") ||
		strings.Contains(said, "exceeded a secondary")
}

// throttle is one credential's "do not call until", and the whole of the
// state this file keeps.
//
// Zero value is open, so a RealTransport built as a struct literal --
// which every test in this package and the live-test seams do -- behaves
// exactly as it did before this existed until GitHub actually refuses
// something.
type throttle struct {
	mu     sync.Mutex
	until  time.Time
	reason string
	// announced is whether the journal already carries the line for this
	// closure. TokenFor-style: the gate is asked once per call on every
	// goroutine that makes one, so a refusal lasting an hour must be one
	// line rather than one per call for an hour.
	announced bool
}

// throttles is one transport's gates, one per credential.
//
// Keyed by a digest of the Authorization header rather than the header
// itself, so the map holds no token, and built lazily so a credential
// that is never refused costs nothing. Requests with no Authorization
// header share the gate keyed by the empty string's digest.
type throttles struct {
	mu    sync.Mutex
	gates map[string]*throttle
}

// forCredential returns the gate for the credential that authorization
// (an Authorization header's value) carries, creating it open.
func (g *throttles) forCredential(authorization string) *throttle {
	sum := sha256.Sum256([]byte(authorization))
	key := hex.EncodeToString(sum[:8])
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.gates == nil {
		g.gates = map[string]*throttle{}
	}
	gate, ok := g.gates[key]
	if !ok {
		gate = &throttle{}
		g.gates[key] = gate
	}
	return gate
}

// blocked reports whether calls are being refused, and until when.
func (t *throttle) blocked(now time.Time) (time.Time, string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.until.IsZero() || !now.Before(t.until) {
		return time.Time{}, "", false
	}
	return t.until, t.reason, true
}

// shut closes the gate until at least `until`, and says so once.
//
// At least: a second refusal arriving while the gate is already shut for
// longer leaves the longer instant standing. GitHub answers every call
// made during a primary-limit window with the same reset time, so the
// two are usually equal; where they are not, the later one is the one
// that was actually asked for.
func (t *throttle) shut(now, until time.Time, reason string) {
	if until.After(now.Add(maxThrottleWait)) {
		until = now.Add(maxThrottleWait)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if until.After(t.until) {
		t.until = until
	}
	if t.reason == "" || reason != "" {
		t.reason = reason
	}
	if t.announced {
		return
	}
	t.announced = true
	log.Printf("github: GitHub is rate limiting this deployment (%s), so grain is not calling it "+
		"again until %s. Everything that reads or writes GitHub -- the merge queue, CI health, "+
		"opening pull requests -- is paused until then and resumes on its own.",
		reason, t.until.UTC().Format(time.RFC3339))
}

// opened is called after a call GitHub actually answered, and is what
// lets the next closure be announced. It does not reopen the gate --
// nothing gets through it to answer while it is shut -- it only forgets
// that this closure was already reported.
func (t *throttle) opened(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.announced {
		return
	}
	if !t.until.IsZero() && now.Before(t.until) {
		return
	}
	t.announced = false
	t.until, t.reason = time.Time{}, ""
	log.Printf("github: GitHub is answering this deployment again.")
}

// throttledUntil reads a response for GitHub asking to be left alone,
// and answers with the instant to come back at.
//
// Three signals, in the order they are trusted:
//
//   - `Retry-After`, which is GitHub naming the pause outright and is
//     the one that carries an obligation. It arrives on the secondary
//     limits.
//   - `x-ratelimit-remaining: 0` with `x-ratelimit-reset`, which is the
//     primary limit and names the instant the budget refills. A refusal
//     with budget still on the clock is not this -- it is one of 403's
//     other meanings -- so both halves are required.
//   - GitHub's own wording, for a refusal that carries neither header.
//     No instant to read there, so defaultThrottleWait stands in.
//
// A response that is none of those is not throttling and is left to the
// caller exactly as it always was.
func throttledUntil(now time.Time, resp ApiResponse) (time.Time, string, bool) {
	if !rateLimitStatus(resp.Status) {
		return time.Time{}, "", false
	}
	said := errorMessage(resp.Body)
	if said == "" {
		said = fmt.Sprintf("GitHub answered %d", resp.Status)
	}
	if d, ok := retryAfter(headerValue(resp.Headers, "Retry-After")); ok {
		return now.Add(d), said, true
	}
	if headerValue(resp.Headers, "X-RateLimit-Remaining") == "0" {
		if reset, ok := unixSeconds(headerValue(resp.Headers, "X-RateLimit-Reset")); ok {
			return reset, said, true
		}
	}
	if rateLimitWording(said) {
		return now.Add(defaultThrottleWait), said, true
	}
	return time.Time{}, "", false
}

// retryAfter reads the header in either of the two forms RFC 9110 allows
// -- a count of seconds, or an HTTP date. GitHub sends seconds; the date
// form is read anyway because it costs one branch and a proxy in front
// of GitHub may rewrite it.
//
// A non-positive or unreadable value is no answer rather than "come back
// now": a header that parsed to zero would make this gate a no-op at
// exactly the moment it is needed.
func retryAfter(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(value); err == nil {
		if secs <= 0 {
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	d := time.Until(when)
	if d <= 0 {
		return 0, false
	}
	return d, true
}

// unixSeconds reads an epoch-seconds header (`x-ratelimit-reset`).
func unixSeconds(value string) (time.Time, bool) {
	secs, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || secs <= 0 {
		return time.Time{}, false
	}
	return time.Unix(secs, 0), true
}

// headerValue reads one header out of an ApiResponse's map, which is not
// an http.Header and so has no canonicalisation of its own.
//
// RealTransport fills it from http.Header's own keys, which are already
// canonical ("X-Ratelimit-Reset", note the lower-case l -- Go's
// canonicalisation title-cases each dash-separated word, so it does not
// match GitHub's own "X-RateLimit-Reset" spelling). A FakeTransport or a
// githubsim writes whatever the test typed. So this tries the name as
// given, then Go's canonical form of it, then a case-insensitive walk --
// the walk being what makes a header spelled any of the three ways
// readable from any of the three sources.
func headerValue(headers map[string]string, name string) string {
	if headers == nil {
		return ""
	}
	if v, ok := headers[name]; ok {
		return v
	}
	if v, ok := headers[http.CanonicalHeaderKey(name)]; ok {
		return v
	}
	for k, v := range headers {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}
