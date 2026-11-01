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

package codex

import "github.com/ai-on-gke/grain/pkg/agent"

// agent/capacity.go has the reasoning all three frameworks share: a
// provider that is overloaded right now refused this run for reasons
// that have nothing to do with the task it was given, and reading that
// as an ordinary failure spends the task's own retry budget -- five in a
// row retire it -- on an outage that was over in seconds.
// agent.CapacityError is how that is said to orchestrator.RunDispatch,
// which records it under model.CapacityOutcome and lets dispatch's
// ordinary backoff carry the retry.
//
// What differs is the vocabulary. The OpenAI API answers an overloaded
// model in words rather than with a type of its own -- "the server is
// overloaded or not ready yet", "the server had an error while
// processing your request" -- and codex passes that sentence through as
// the turn's failure.
//
// # A 429 arrives as codex's own sentence, with the provider's body gone
//
// The exception, and the reason this list carries a status code as well
// as sentences. Measured on codex-cli 0.153.0 against a stub of the
// responses API (the 2026-09-08 codex-retry hand run §7): codex
// does not retry an HTTP 429 at all -- one request, about 1.2 seconds --
// and collapses it to
//
//	exceeded retry limit, last status: 429 Too Many Requests
//
// dropping the provider's body on the way. Identical with a Retry-After
// header, identical without one, and identical with a body worded
// exactly the way OpenAI words a rate limit (`"code":
// "rate_limit_exceeded"`, "Please try again in 20s"): none of that
// survives into anything grain can read.
//
// So the marker is here rather than in usagelimit.go, and that is a
// decision rather than a filing accident. codex's sentence does not say
// which kind of 429 it was -- a per-minute rate limit that clears in
// seconds, or a plan whose ceiling is hours away -- so the question is
// which way round it is safer to be wrong:
//
//   - Read as capacity when it was a plan's ceiling: the run retries
//     twice in place (retry.go), the task returns to the queue under
//     dispatch's own backoff, and its failure streak is untouched. The
//     deployment walks into the same wall a few more times, and pauses
//     nothing.
//   - Read as a usage limit when it was a per-minute rate limit: every
//     run in flight is cancelled and dispatch stops deployment-wide for
//     at least a minute and by default a quarter of an hour
//     (orchestrator's minUsageLimitPause and defaultUsageLimitPause),
//     because a single busy minute happened to land on one task.
//
// The first is much the cheaper mistake. It is also the likelier case:
// a 429 from the responses API is ordinarily an RPM or TPM limit, while
// an exhausted plan is `insufficient_quota` -- a body codex would have
// to pass through for anyone to see it. Where it does pass a body
// through, usagelimit.go's markers still match first and still pause;
// that ordering is unchanged, and this only covers the case where codex
// left nothing else to go on.
//
// Being retryable is the other half of what this buys. codex retries a
// 429 zero times, so until this marker existed nothing retried it at
// all: not codex, and not grain, which read it as an ordinary failed run
// and spent one of the five failures that retire a task. A 30-second
// backoff and a second exec in the same sandbox
// (agent.BaseTransientBackoff) is very nearly the ideal answer to a
// per-minute limit, and it is what a capacity refusal already gets.
const capacityFramework = "codex"

// capacityMarkers are the phrases that mean "no capacity" in codex's own
// report of a failed run, on top of agent's shared HTTP ones. Lowercase,
// matched against a lowercased haystack.
//
// Whole sentences rather than the words in them, for the reason
// usagelimit.go gives for excluding a bare "429": "overloaded" and
// "server error" are both things a repository's own text says, and that
// text passes through this same stream. What is matched here is the
// provider apologising, which nothing in a repository does.
var capacityMarkers = []string{
	"server is overloaded",
	"engine is currently overloaded",
	"server had an error while processing your request",
	// codex's own sentence for a 429 (the section above), with the
	// status kept and the reason phrase left off, so a codex that
	// recapitalises "Too Many Requests" still matches.
	"exceeded retry limit, last status: 429",
	// And the status with its reason phrase, on the discipline
	// agent.sharedCapacityMarkers applies to the 5xx ones: cover for a
	// codex that rewords the sentence in front of the status, which it
	// has reworded its event vocabulary once already. Still not the bare
	// "429" both classifiers refuse -- the reason phrase is what makes
	// it the provider's edge talking -- and what it is matched against
	// is only codex's own terminal failure text, never the run's whole
	// transcript (retry.go's retryable, and Framework.Run).
	"429 too many requests",
}

// capacityFailure reads a failed run's own account of itself -- the
// stream's terminal failure text and whatever the subprocess reported --
// and returns the capacity refusal it describes, or nil when it
// describes something else, which is every ordinary failure.
//
// Consulted only after usageLimitFailure: a plan that has run out of
// budget is a usage limit even where the same body mentions load, and
// pausing for it is what keeps the rest of the queue off the same wall.
func capacityFailure(resultText string, runErr error) *agent.CapacityError {
	return agent.CapacityFailure(capacityFramework, capacityMarkers, resultText, runErr)
}
