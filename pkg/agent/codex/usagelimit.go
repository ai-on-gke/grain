package codex

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// agent/claude's usagelimit.go has the reasoning all three frameworks
// share: a run that ends because the credential it runs as has no budget
// left in its current window is not a run that failed on its own terms,
// and retrying it -- or dispatching the next task -- spends the whole
// queue on the same refusal. agent.UsageLimitError is how that is said
// to orchestrator.RunDispatch, which pauses dispatch until the window
// resets instead.
//
// What differs is the vocabulary. codex reports a plan's own ceiling in
// words ("you've hit your usage limit", "try again in 4h 12m") and the
// API's in the error body it passes through (a 429's
// rate_limit_exceeded, an insufficient_quota). Both end up in the same
// agent.UsageLimitError; the relative "try again in" becomes its
// RetryAfter, the same field agent/antigravity fills from the Gemini
// API's own retryDelay.
const usageLimitFramework = "codex"

// usageLimitMarkers are the phrases that mean "out of budget" in codex's
// own report of a failed run. Lowercase, matched against a lowercased
// haystack.
//
// A bare "429" is deliberately absent, for the reason both other
// frameworks give: it is three digits that a repository's own files and
// test output are full of, and some of that text passes through this
// same stream.
var usageLimitMarkers = []string{
	"usage limit reached",
	"usage limit",
	"rate limit exceeded",
	"rate_limit_exceeded",
	"quota exceeded",
	"insufficient_quota",
	"exceeded your current quota",
}

// retryAfterPattern is codex's own "come back in", as it words a plan
// limit: "try again in 4h 12m", "try again in 45m", "try again in 30s".
// Each unit is optional and they appear largest-first, which is what the
// three separate optional groups say.
var retryAfterPattern = regexp.MustCompile(`(?i)try again in\s+(?:(\d+)\s*h)?\s*(?:(\d+)\s*m(?:in)?)?\s*(?:(\d+)\s*s)?`)

// usageLimitFailure reads a failed run's own account of itself -- the
// stream's terminal failure text and whatever the subprocess reported --
// and returns the limit it describes, or nil when it describes something
// else entirely (which is every ordinary failure).
//
// Like agent/antigravity and unlike agent/claude there is no second,
// stricter reading for a *successful* run: codex reports a limit by
// failing the turn, never as the run's final answer, so nothing here has
// to tell the model quoting the phrase apart from the CLI reporting it.
func usageLimitFailure(resultText string, runErr error) *agent.UsageLimitError {
	text := resultText
	if runErr != nil {
		text = strings.TrimSpace(text + "\n" + runErr.Error())
	}
	if !mentionsUsageLimit(text) {
		return nil
	}
	limit := &agent.UsageLimitError{
		Framework: usageLimitFramework,
		Message:   agent.TrimLimitMessage(text),
		Err:       runErr,
	}
	limit.Classify(text)
	if d, ok := retryAfter(text); ok {
		limit.RetryAfter = d
	}
	return limit
}

// mentionsUsageLimit reports whether text carries any of the markers.
func mentionsUsageLimit(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range usageLimitMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// retryAfter reads codex's own "try again in ..." out of text, when it
// named one. A delay of zero reads as "none given" rather than "come
// back immediately": the caller's own floor is the safer reading of a
// provider that has just refused a request (agent.UsageLimitError.
// ResumeAt makes the same argument for a reset already in the past).
func retryAfter(text string) (time.Duration, bool) {
	m := retryAfterPattern.FindStringSubmatch(text)
	if m == nil {
		return 0, false
	}
	total := time.Duration(atoi(m[1]))*time.Hour +
		time.Duration(atoi(m[2]))*time.Minute +
		time.Duration(atoi(m[3]))*time.Second
	if total <= 0 {
		return 0, false
	}
	return total, true
}

// atoi is strconv.Atoi with "no digits at all" -- an optional group the
// pattern above did not match -- reading as zero, which is what a unit
// the provider did not name contributes to the total.
func atoi(s string) int {
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}
