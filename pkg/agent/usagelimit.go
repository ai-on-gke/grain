package agent

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

// UsageLimitError is a Framework's report that a run ended for a reason
// that has nothing to do with the task it was given: the account the
// agent runs as has no budget left in its current window -- Claude
// Code's own "Claude AI usage limit reached", an API 429, a Gemini
// quota. It is the one framework failure that says something about the
// *deployment* rather than about the run, which is why it needs a type
// of its own rather than another sentence in an error string.
//
// What reads it is orchestrator.RunDispatch, through UsageLimit below:
// a deployment that has run out of budget cannot fix that by dispatching
// the next task, so every run in flight is cancelled and dispatch is
// paused until the window resets (orchestrator.Pause). Retrying instead
// spends the rest of the queue on the same wall -- each task burning an
// attempt, a sandbox and its own place in the failure streak on a
// failure none of them caused.
//
// A Framework returns this instead of a plain error, never as well as
// one: it wraps whatever the CLI actually said (Err), so a caller that
// only wants the text still gets it from Error().
type UsageLimitError struct {
	// Framework names which implementation produced this ("claude",
	// "antigravity"), since the limit belongs to that framework's own
	// credential -- a deployment running both can be out of budget on
	// one and fine on the other. It is carried for the operator reading
	// the run's detail; nothing branches on it today.
	Framework string
	// Message is what the CLI said, trimmed to something a stored run
	// detail can carry (see detail limits in orchestrator.outcomeOf's
	// neighbours). Empty is allowed: the type itself is the fact.
	Message string
	// ResetAt is when the provider said the window reopens, in absolute
	// terms -- Claude Code reports it as a Unix epoch appended to its
	// own message ("...usage limit reached|1735689600"). Zero when the
	// provider named no time at all, which is the common case for a bare
	// 429.
	ResetAt time.Time
	// RetryAfter is the same fact expressed as a delay rather than an
	// instant, which is how the Gemini API reports it ("retryDelay":
	// "56s"). Zero when none was given. ResetAt wins over this where
	// both are set: an absolute instant survives however long the error
	// took to reach a reader, and a delay does not.
	RetryAfter time.Duration
	// Scope is how much of the credential the provider says it refused:
	// one model's quota (LimitScopeModel, and the zero value, which is
	// what a refusal saying nothing either way is read as) or the whole
	// account or key (LimitScopeAccount) -- credits or billing exhausted,
	// an organisation-wide quota, "all models". The orchestrator pauses
	// only the model the run was on unless this says the account
	// (orchestrator.Pause.Begin).
	Scope LimitScope
	// Model is the model the provider's own error names, where it names
	// one -- the concrete model a provider-chosen run ("auto") was
	// actually refused on. Empty when it named none; the pause is then
	// keyed on the model grain passed the CLI.
	Model string
	// Err is the underlying failure -- the CLI's own error, unmodified,
	// so errors.Is/As still reach whatever it carried.
	Err error
}

func (e *UsageLimitError) Error() string {
	parts := []string{"agent: usage limit reached"}
	if e.Framework != "" {
		parts[0] = e.Framework + ": usage limit reached"
	}
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	if !e.ResetAt.IsZero() {
		parts = append(parts, "resets at "+e.ResetAt.UTC().Format(time.RFC3339))
	} else if e.RetryAfter > 0 {
		parts = append(parts, "retry after "+e.RetryAfter.String())
	}
	return strings.Join(parts, "; ")
}

func (e *UsageLimitError) Unwrap() error { return e.Err }

// LimitScope is UsageLimitError.Scope: what a provider's refusal says it
// covers.
type LimitScope string

const (
	// LimitScopeModel is a refusal of one model's quota, and what a
	// refusal that says nothing about its reach is read as.
	LimitScopeModel LimitScope = ""
	// LimitScopeAccount is a refusal of the whole account or key: no
	// other model on the same credential would be served either.
	LimitScopeAccount LimitScope = "account"
)

// accountLimitMarkers are the phrases, lower-cased, in which providers
// say a refusal is about the account or key rather than one model: money
// (credits, billing, a plan), an organisation's or project's own quota,
// or every model at once. Shared by every framework's parser, the way
// TrimLimitMessage is, because the providers behind them overlap -- a
// codex connection and a custom command can both front OpenAI.
var accountLimitMarkers = []string{
	"insufficient_quota",
	"credit balance",
	"out of credits",
	"credits exhausted",
	"billing",
	"payment required",
	"plan limit",
	"all models",
	"organization's",
	"organisation's",
	"org quota",
	"organization quota",
	"project quota",
}

// limitModelPattern finds the model a provider's refusal names: Gemini's
// quota dimensions ("model": "gemini-2.5-pro"), and the prose forms
// "for model X", "model: X" and OpenAI's "Rate limit reached for X in
// organization ...".
var limitModelPattern = regexp.MustCompile(`(?i)(?:"model"\s*:\s*"|\bfor model\s+[` + "`" + `'"]?|\bmodel[:=]\s*[` + "`" + `'"]?|\blimit reached for\s+)([a-z][a-z0-9._:/-]*[a-z0-9])`)

// ClassifyLimit reads a provider's refusal text for the two hints a
// UsageLimitError carries beyond the fact of the limit: whether the
// refusal is account-wide (Scope) and which model it names (Model). A
// Framework's parser calls it on the same text it detected the limit in.
func ClassifyLimit(text string) (LimitScope, string) {
	lower := strings.ToLower(text)
	scope := LimitScopeModel
	for _, marker := range accountLimitMarkers {
		if strings.Contains(lower, marker) {
			scope = LimitScopeAccount
			break
		}
	}
	var named string
	if m := limitModelPattern.FindStringSubmatch(text); m != nil {
		named = strings.ToLower(m[1])
	}
	return scope, named
}

// Classify fills e's Scope and Model from text (ClassifyLimit), leaving
// either alone where the text has nothing to say, and returns e.
func (e *UsageLimitError) Classify(text string) *UsageLimitError {
	scope, named := ClassifyLimit(text)
	if scope != LimitScopeModel {
		e.Scope = scope
	}
	if named != "" && e.Model == "" {
		e.Model = named
	}
	return e
}

// ResumeAt is when the provider itself says this limit lifts, given the
// moment now that a caller is asking at -- false when it named neither
// an instant nor a delay, which leaves the wait a policy decision for
// the caller rather than a fact from the provider (orchestrator's own
// defaultUsageLimitPause).
//
// A ResetAt already in the past reads as "no time given" rather than as
// "resume immediately": a stale instant is either a clock the two ends
// disagree about or a limit whose window has genuinely reopened, and
// treating it as the second would have the deployment walk straight back
// into whichever it was. The caller's own floor is the safer answer for
// both.
func (e *UsageLimitError) ResumeAt(now time.Time) (time.Time, bool) {
	if !e.ResetAt.IsZero() && e.ResetAt.After(now) {
		return e.ResetAt.UTC(), true
	}
	if e.RetryAfter > 0 {
		return now.Add(e.RetryAfter).UTC(), true
	}
	return time.Time{}, false
}

// UsageLimit reports whether err is (or wraps) a UsageLimitError, and
// hands back the one it found. It is errors.As with the type spelled
// out once here rather than at every call site, the same convenience
// mcp.BareToolName is for its own convention.
func UsageLimit(err error) (*UsageLimitError, bool) {
	var limit *UsageLimitError
	if errors.As(err, &limit) {
		return limit, true
	}
	return nil, false
}

// TrimLimitMessage bounds what a Framework puts in UsageLimitError.
// Message -- and in CapacityError.Message, which is the same text from
// the same streams: one line, no more than maxLimitMessage bytes. What a
// provider says when it refuses a request ranges from a sentence to a
// whole JSON error body, and this text lands in task_run.detail, which
// `grain get` and the UI's attempt timeline print inline.
//
// Exported because both Framework implementations need it and neither
// should own the bound: two frameworks trimming to two different lengths
// would make a run's detail read differently for a reason that has
// nothing to do with the run.
func TrimLimitMessage(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxLimitMessage {
		s = headAtRune(s, maxLimitMessage) + "..."
	}
	return s
}

// maxLimitMessage is TrimLimitMessage's own ceiling -- generous enough
// for a provider's whole sentence ("Claude AI usage limit reached, your
// limit resets at ...") and short enough that a JSON error body does not
// take a run listing over with it.
const maxLimitMessage = 240

// ErrUsageLimit is the sentinel a caller that only wants to ask "was
// this a usage limit?" can compare against with errors.Is, without
// naming the struct. Every UsageLimitError matches it.
var ErrUsageLimit = errors.New("agent: usage limit reached")

// Is makes errors.Is(err, ErrUsageLimit) answer true for any
// UsageLimitError, whatever its own fields say -- the shape errors.Is
// documents for a type that wants to match a sentinel it does not wrap.
func (e *UsageLimitError) Is(target error) bool { return target == ErrUsageLimit }
