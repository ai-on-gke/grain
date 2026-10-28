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

// retry.go is what this package does when GitHub fails to answer at all.
//
// It is the other half of ratelimit.go, and the two are opposites that
// share a premise. A throttle is GitHub having answered "no, and not for
// a while": the right response is to stop calling, which is what the gate
// next door does. A 5xx is GitHub having answered nothing at all -- a 502
// from whatever sits in front of the API, an Envoy "upstream connect
// error" on a 503, an empty 500 -- and the right response to that is to
// ask the same question again, because the question was never heard. The
// premise both share is that a caller should not have to know which it
// met.
//
// Nothing here used to retry. Task 211 established that: RealTransport
// built one request, called client.Do once, and handed back whatever came
// of it, and a 502 reached the caller as a failed operation on the first
// try. For the merge queue that was fine and remains fine -- it
// reconciles every ten seconds, so the cycle *is* the retry, and 211
// deliberately made the queue patient rather than adding a second layer
// underneath it. But the queue is not the only caller of this package.
// EnsurePullRequest, the finish path, the MCP tools an agent calls and
// cmd/ghcr-prune have no cycle behind them: for those a 502 is not a
// deferred read, it is the operation failing, and this deployment's own
// daemon log carries one every few hours.
//
// So one retry, and the whole design is in what it refuses to do:
//
//   - **Idempotent methods only, by construction.** GET and HEAD are
//     retried; every other method is not, and the decision is made here
//     from the method of the request rather than at any call site.
//     MergePullRequest, CreateIssue, the label moves and every other
//     POST/PATCH/PUT/DELETE are therefore excluded by the shape of the
//     code and not by anybody remembering -- a write that GitHub answered
//     502 may well have happened, and sending it again could merge a pull
//     request twice or file two copies of the same issue. A new write
//     added to this package inherits "not retried" without its author
//     doing anything, which is the only safe default.
//   - **One extra attempt.** During a real GitHub outage every call costs
//     attempts x calls, and a daemon whose reconcile cycle is stuck
//     inside a retry ladder looks hung from outside. Two attempts and a
//     quarter-second is a bound worth paying with nobody watching; an
//     exponential ladder is not, and anything that has not cleared by the
//     second attempt is what the caller's own next cycle is for.
//   - **Status only, not transport failures.** A refused connection, a
//     DNS failure or a timeout is not a *Error and is not retried here,
//     for the reason IsServerError itself excludes them: a timeout is the
//     one failure whose retry doubles the wait a caller is already
//     complaining about, and a connection that was refused was refused
//     for a reason this package cannot see. Deliberately narrow; a caller
//     that wants more asks for it.
//   - **Never through a shut gate.** The retry is a second trip through
//     the whole of Request's preamble, so the rate-limit gate is asked
//     again before the second call is built. A 5xx followed by a throttle
//     from a concurrent call ends as the throttle, not as a call sent
//     into a closure GitHub has already declared.
//
// What a caller sees is unchanged in every case but one: a 5xx that the
// second attempt answered normally now arrives as the answer instead of
// as an error. A 5xx that persists arrives exactly as it always did, a
// few hundred milliseconds later, and is still IsServerError to every
// predicate that reads it.
//
// Stats.Retried counts the extra requests, so the claim this file makes
// is one an operator can check rather than one they have to believe --
// stats.go's own reason for existing.

import (
	"net/http"
	"strings"
	"time"
)

// maxRequestAttempts is how many times one call may be sent: the first
// attempt and one retry.
//
// Not a knob. A number a deployment can raise is a number somebody raises
// during an outage, which is exactly when multiplying grain's call volume
// is the wrong thing to do.
const maxRequestAttempts = 2

// retryBackoff is the pause between the two attempts.
//
// Long enough that a load balancer swapping a backend out has a moment to
// finish doing it, short enough to be invisible to the human waiting on
// whatever made the call. It is a flat pause rather than the first rung
// of a ladder because there is no second rung.
const retryBackoff = 250 * time.Millisecond

// serverErrorStatus is GitHub having failed to answer rather than having
// answered "no" -- any 5xx.
//
// The one place that reads the status, so that what IsServerError tells a
// caller and what this file retries cannot drift apart: the predicate is
// this function over an *Error, and the retry is this function over the
// response that would become one.
func serverErrorStatus(status int) bool { return status >= 500 && status <= 599 }

// idempotentMethod reports whether sending this request a second time is
// safe when the first attempt produced no answer.
//
// The allow list is the point. Everything not named here is a write until
// proven otherwise, so a method this package starts using tomorrow is not
// retried until somebody decides in this function that it may be.
//
// GET and HEAD only. PUT and DELETE are idempotent in HTTP's own sense --
// sending one twice leaves the same state behind -- but not in the sense
// that matters to a caller: GitHub's PUT /merge answers 409 to the second
// one, and a DELETE of a branch a retry has already deleted answers 422,
// which would turn an operation that actually succeeded into an error
// report. Neither belongs here.
func idempotentMethod(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodGet, http.MethodHead:
		return true
	}
	return false
}

// worthRetrying reports whether this attempt's outcome should be asked
// again, given which attempt it was.
func worthRetrying(method string, resp ApiResponse, attempt int) bool {
	return attempt < maxRequestAttempts && idempotentMethod(method) && serverErrorStatus(resp.Status)
}

// pause waits between attempts.
//
// Through a field so a test can assert the backoff happened without
// spending it; nil is the live path and calls time.Sleep. Blocking the
// calling goroutine for a quarter second is the whole cost of this file,
// and it is paid only on a 5xx, which is rare enough in the log to
// measure in hours.
func (t *RealTransport) pause(d time.Duration) {
	if t.sleep != nil {
		t.sleep(d)
		return
	}
	time.Sleep(d)
}
