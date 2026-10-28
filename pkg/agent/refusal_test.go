package agent_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bwsalmon/grain/pkg/agent"
)

// The bound is the point: this is a provider's own output, and a
// provider's own output is whatever length it likes. A tail that kept it
// all would put a run's whole error body in a row an operator reads a
// week of at once.
func TestUnmatchedFailureTailIsBounded(t *testing.T) {
	long := strings.Repeat("x", 4000) + " api error: 529"
	got := agent.UnmatchedFailureTail(long)
	// The ellipsis is on top of the bound, which is what the bound is
	// about: the kept text itself never exceeds it.
	if kept := strings.TrimPrefix(got, "..."); len(kept) > agent.MaxUnmatchedFailureText {
		t.Errorf("UnmatchedFailureTail kept %d bytes of a %d-byte failure, want at most %d",
			len(kept), len(long), agent.MaxUnmatchedFailureText)
	}
	if !strings.HasPrefix(got, "...") {
		t.Errorf("UnmatchedFailureTail = %q, want a truncated tail to say it is one", got)
	}
}

// The tail rather than the head, because a provider that refuses a run
// mid-flight says so last: the beginning of a long failure is whatever
// the run was doing when it was refused.
func TestUnmatchedFailureTailKeepsTheEnd(t *testing.T) {
	text := strings.Repeat("the agent was editing files. ", 200) +
		"then: overloaded_error, please retry"
	got := agent.UnmatchedFailureTail(text)
	if !strings.HasSuffix(got, "then: overloaded_error, please retry") {
		t.Errorf("UnmatchedFailureTail = %q, want it to end with what the provider said last", got)
	}
}

// A cut taken at a byte offset can land inside a multi-byte character,
// and what is stored has to render -- in a JSON body, in a terminal, in
// a row somebody pastes into an issue.
func TestUnmatchedFailureTailStaysValidUTF8(t *testing.T) {
	// Padded to a length that puts the bound somewhere inside the run of
	// three-byte characters, whichever byte that turns out to be.
	for pad := 0; pad < 3; pad++ {
		text := strings.Repeat("a", pad) + strings.Repeat("☃", agent.MaxUnmatchedFailureText)
		got := agent.UnmatchedFailureTail(text)
		if !utf8.ValidString(got) {
			t.Errorf("UnmatchedFailureTail(pad=%d) is not valid UTF-8: %q", pad, got)
		}
	}
}

// One line, because this lands in a report printed a row at a time and
// in a JSON field read by eye -- the same folding TrimLimitMessage does
// for the run detail next to it.
func TestUnmatchedFailureTailIsOneLine(t *testing.T) {
	got := agent.UnmatchedFailureTail("  api error: 500\n  {\"type\":\"error\"}\n")
	if want := `api error: 500 {"type":"error"}`; got != want {
		t.Errorf("UnmatchedFailureTail = %q, want %q", got, want)
	}
}

// The loose read, whose whole job is to catch what the tight ones in
// capacity.go and usagelimit.go would miss after a rewording. Every one
// of these is a phrase those two deliberately refuse to match, and
// matching them here costs nothing but a mark against a run that has
// already failed.
func TestResemblesRefusalCatchesWhatTheTightMarkersWouldNot(t *testing.T) {
	for _, text := range []string{
		"the model is currently experiencing heavy load, please try again",
		"unusually heavy demand right now; please retry shortly",
		"Error: 529",
		"we are at capacity right now",
		"upstream connect error",
		"the service is temporarily not accepting requests",
		"you have exceeded your quota for this model",
	} {
		if !agent.ResemblesRefusal(text) {
			t.Errorf("ResemblesRefusal(%q) = false, want it flagged for a human to read", text)
		}
	}
}

// And it still has to be quiet about the ordinary failure, or the report
// it feeds is a list of everything and says nothing.
func TestResemblesRefusalIgnoresAnOrdinaryFailure(t *testing.T) {
	for _, text := range []string{
		"claude: exceeded max turns (100) without a final answer",
		"the agent made no tool calls at all",
		"claude: running claude: exec: \"claude\": executable file not found in $PATH",
		"context deadline exceeded",
	} {
		if agent.ResemblesRefusal(text) {
			t.Errorf("ResemblesRefusal(%q) = true, want an ordinary failure left alone", text)
		}
	}
}

// The two halves of the instrument have to agree with each other: a
// refusal grain *does* recognise never reaches this file at all, but the
// text of one that did would read as a refusal here too. This is what
// makes the report's own reading honest -- an operator seeing a run of
// these knows they are looking at the shape of a refusal grain missed.
func TestResemblesRefusalWouldRecogniseTheRefusalsGrainAlreadyKnows(t *testing.T) {
	for _, text := range []string{
		"api error: 529 {\"type\":\"overloaded_error\"}",
		"[503 Service Unavailable] The model is overloaded. Please try again later.",
		"api error: 429 rate limit exceeded",
	} {
		if !agent.ResemblesRefusal(agent.UnmatchedFailureTail(text)) {
			t.Errorf("ResemblesRefusal(%q) = false, want the shape of a known refusal to read as one", text)
		}
	}
}
