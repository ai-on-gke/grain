package guestdockerfile

import (
	"strings"
	"testing"
)

// The lexing itself is granulefile's, and is tested there: a guest
// Dockerfile and a Granule are the same format, and the heredoc,
// continuation and comment cases are the same cases. What this asserts
// is the part that is this package's -- that Parse is that lexer, and
// that a file it refuses is refused naming the file.
func TestParse(t *testing.T) {
	got, err := Parse("RUN apt-get update && \\\n    apt-get install -y nginx\n# a comment\nCOPY a /a\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []Instruction{
		{Name: "RUN", Args: "apt-get update && apt-get install -y nginx", Line: 1},
		{Name: "COPY", Args: "a /a", Line: 4},
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %d instructions, want %d: %+v", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("instruction %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

// An unterminated heredoc is the one parse failure worth refusing over:
// everything below it is its body, so carrying on would validate lines
// that are not instructions and skip the ones that are.
func TestParseUnterminatedHeredoc(t *testing.T) {
	src := "RUN echo one\nRUN <<EOF\napt-get update\nCMD [\"nope\"]\n"
	if _, err := Parse(src); err == nil || !strings.Contains(err.Error(), "line 2") || !strings.Contains(err.Error(), "EOF") {
		t.Fatalf("Parse = %v, want a complaint naming line 2 and EOF", err)
	}
	if _, err := Validate("Dockerfile", src); err == nil || !strings.Contains(err.Error(), "Dockerfile: line 2") {
		t.Fatalf("Validate = %v, want the same complaint, naming the file", err)
	}
}
