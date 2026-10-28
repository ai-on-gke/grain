package guestdockerfile

import (
	"github.com/bwsalmon/granule/pkg/granulefile"
)

// Instruction is one instruction as it appeared in a Dockerfile: its
// name upper-cased, the rest of the line with any continuations folded
// into it, and the line its first line was on.
//
// It is granulefile's type rather than one of this package's own. The
// lexing a guest Dockerfile needs and the lexing a Granule needs are
// the same lexing -- comments, continuations, heredocs, parser
// directives -- and the two files are the same format, so there is one
// of each in this tree rather than two that can drift.
type Instruction = granulefile.Instruction

// Violation is one instruction granule refuses, and why (granulefile's
// type, for the same reason).
type Violation = granulefile.Violation

// Parse returns the instruction list of a Dockerfile's text.
//
// It parses as little as it can get away with: enough to know what
// instruction each line is, and nothing about what any of them mean --
// see granulefile.Lex, which is where it happens and where the three
// things that make it more than a line split are written down.
func Parse(src string) ([]Instruction, error) {
	return granulefile.Lex(src)
}
