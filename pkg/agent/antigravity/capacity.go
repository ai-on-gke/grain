package antigravity

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
// What differs is the vocabulary. agy talks to the Gemini API, whose
// answer to an overloaded model is a 503 whose message is "The model is
// overloaded. Please try again later." and whose status is UNAVAILABLE.
// That status arrives in more than one rendering -- the API's own JSON
// body, the Go client's "Status: UNAVAILABLE", a gRPC "code =
// Unavailable" -- which is why several spellings of one fact are listed
// below rather than one.
//
// # None of which agy has been seen to say, and that is now measured
//
// Read this before adding a marker, because the markers are not what is
// wrong. Task 419 pointed a real agy 1.1.27 at a stub of Gemini's
// endpoint that refuses everything with exactly the body above, and
// watched what came out: agy writes `calling model: Error 503, Message:
// The model is overloaded. Please try again later., Status: UNAVAILABLE`
// to its own --log-file and puts `"error":"Agent execution terminated due
// to error."` on the stream -- the sentence every other kind of failure
// ends with. The error_message step carries no text, stderr is empty, and
// the subprocess error is a bare exit status 1.
//
// Framework.Run therefore gives every failed exec its own --log-file in
// the private HOME, bounds and redacts complete records from its tail,
// and passes only that exec's excerpt here alongside the stream and
// subprocess error. This keeps the match narrow without guessing that
// every generic exit 1 is the provider's fault. The raw log disappears
// with the HOME; diagnostics.go owns that deliberately small dependency
// on agy's undocumented log format and its credential-safety boundary.
//
// capacityFramework repeats usageLimitFramework's own word rather than
// borrowing it: both are this Framework's name as an operator reads it
// in a run's detail, and neither classifier should quietly change what
// the other one says.
const capacityFramework = "antigravity"

// capacityMarkers are the phrases that mean "no capacity" in agy's own
// report of a failed run, on top of agent's shared HTTP ones. Lowercase,
// matched against a lowercased haystack.
//
// Each one is tight for the reason usagelimit.go gives for excluding a
// bare "429". "unavailable" and "overloaded" are both words a
// repository's own text uses, and that text passes through this same
// stream, so neither appears here on its own: every marker carries the
// context that makes it the API talking rather than a file the agent
// happened to read.
//
// "internal" is deliberately absent even though a Gemini 500 INTERNAL is
// the same kind of transient refusal. There is no spelling of it tight
// enough to be safe -- "internal error" is a phrase half the world's
// test output contains -- and reading an ordinary failure as capacity
// is the direction that hides a real fault.
var capacityMarkers = []string{
	"model is overloaded",
	`"status": "unavailable"`,
	`"status":"unavailable"`,
	"status: unavailable",
	"code = unavailable",
}

// capacityFailure reads a failed run's own account of itself -- the
// terminal result event's text and whatever the subprocess reported --
// and returns the capacity refusal it describes, or nil when it
// describes something else, which is every ordinary failure.
//
// Consulted only after usageLimitFailure: a quota refusal that also
// mentions being overloaded is a quota refusal, and pausing for it is
// the response that keeps the rest of the queue off the same wall.
func capacityFailure(resultText string, runErr error) *agent.CapacityError {
	return agent.CapacityFailure(capacityFramework, capacityMarkers, resultText, runErr)
}
