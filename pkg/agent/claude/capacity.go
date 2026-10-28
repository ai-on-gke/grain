package claude

import "github.com/bwsalmon/grain/pkg/agent"

// agent/capacity.go has the reasoning all three frameworks share: a
// provider that is overloaded right now refused this run for reasons
// that have nothing to do with the task it was given, and reading that
// as an ordinary failure spends the task's own retry budget -- five in a
// row retire it -- on an outage that was over in seconds.
// agent.CapacityError is how that is said to orchestrator.RunDispatch,
// which records it under model.CapacityOutcome and lets dispatch's
// ordinary backoff carry the retry.
//
// What differs is the vocabulary. The Anthropic API answers an
// overloaded model with a 529 whose error type is overloaded_error, and
// the CLI passes that body through in the text of the failed turn.
const capacityFramework = "claude"

// capacityMarkers are the phrases that mean "no capacity" in claude's
// own report of a failed run, on top of agent's shared HTTP ones.
// Lowercase, matched against a lowercased haystack.
//
// Tight, for the reason usagelimit.go gives for excluding a bare "429":
// a bare "529" is three digits, and "overloaded" on its own is a word,
// and the stream this reads has been carrying a repository's files and
// test output for an hour. "overloaded_error" is the API's own error
// type, which is a token a provider writes and a repository does not.
var capacityMarkers = []string{
	"overloaded_error",
	"api error: 529",
}

// capacityFailure reads a failed run's own account of itself -- the
// terminal result event's text and whatever the subprocess reported --
// and returns the capacity refusal it describes, or nil when it
// describes something else, which is every ordinary failure.
//
// Consulted only after usageLimitFromFailure: an account with no budget
// left is a usage limit even if the same body mentions load, and pausing
// for it is what keeps the rest of the queue off the same wall.
//
// Unlike usageLimitFromResult there is no reading of a *successful* run
// here. A refusal the CLI delivers as the run's own final answer is a
// sentence the model wrote, and a model that says "the server is
// overloaded" mid-answer has not been refused anything.
func capacityFailure(resultText string, runErr error) *agent.CapacityError {
	return agent.CapacityFailure(capacityFramework, capacityMarkers, resultText, runErr)
}
