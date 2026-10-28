package granulefile

import (
	"strings"
	"testing"
)

// TestLex covers the four things that make this more than a line split,
// from both sides: what has to be read as an instruction, and what must
// not be -- because a continuation line or a heredoc body read as an
// instruction is a refusal of something the author never wrote.
func TestLex(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want []Instruction
	}{{
		name: "comments, blank lines and indentation",
		src: `
# install the thing
   RUN apt-get update

	COPY app /srv/app
`,
		want: []Instruction{
			{Name: "RUN", Args: "apt-get update", Line: 3},
			{Name: "COPY", Args: "app /srv/app", Line: 5},
		},
	}, {
		name: "a continuation is one instruction, not three",
		src: `RUN apt-get update && \
    apt-get install -y \
    nginx
COPY a /a
`,
		want: []Instruction{
			{Name: "RUN", Args: "apt-get update && apt-get install -y nginx", Line: 1},
			{Name: "COPY", Args: "a /a", Line: 4},
		},
	}, {
		name: "a comment inside a continuation is skipped, as docker skips it",
		src: `RUN apt-get update && \
# and then
    apt-get install -y nginx
`,
		want: []Instruction{{Name: "RUN", Args: "apt-get update && apt-get install -y nginx", Line: 1}},
	}, {
		name: "a heredoc body is shell, not instructions",
		src: `RUN <<EOF
apt-get update
cmd_that_is_not_CMD
EOF
COPY a /a
`,
		want: []Instruction{
			{Name: "RUN", Args: "<<EOF", Line: 1},
			{Name: "COPY", Args: "a /a", Line: 5},
		},
	}, {
		name: "a quoted, tab-stripped heredoc, and a second one on the same line",
		src:  "COPY <<-\"one\" <<two /etc/both\n\tRUN not an instruction\n\tone\nENV nor this\ntwo\nUSER app\n",
		want: []Instruction{
			{Name: "COPY", Args: `<<-"one" <<two /etc/both`, Line: 1},
			{Name: "USER", Args: "app", Line: 6},
		},
	}, {
		// BuildKit matches heredocs per token: a "<<" inside an argument
		// is an argument, and reading it as a redirect would swallow the
		// rest of the file.
		name: "a << that is not a heredoc",
		src: `RUN echo $((1<<20)) > /etc/shift
LABEL note="a<<b"
USER app
`,
		want: []Instruction{
			{Name: "RUN", Args: "echo $((1<<20)) > /etc/shift", Line: 1},
			{Name: "LABEL", Args: `note="a<<b"`, Line: 2},
			{Name: "USER", Args: "app", Line: 3},
		},
	}, {
		name: "CRLF line endings",
		src:  "RUN apt-get update\r\nUSER app\r\n",
		want: []Instruction{
			{Name: "RUN", Args: "apt-get update", Line: 1},
			{Name: "USER", Args: "app", Line: 2},
		},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Lex(tc.src)
			if err != nil {
				t.Fatalf("Lex: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("parsed %d instructions, want %d: %+v", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("instruction %d: got %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// An unterminated heredoc is the one lexing failure worth refusing over:
// everything below it is its body, so carrying on would validate lines
// that are not instructions and skip the ones that are.
func TestLexUnterminatedHeredoc(t *testing.T) {
	src := "RUN echo one\nRUN <<EOF\napt-get update\nCMD [\"nope\"]\n"
	if _, err := Lex(src); err == nil || !strings.Contains(err.Error(), "line 2") || !strings.Contains(err.Error(), "EOF") {
		t.Fatalf("Lex = %v, want a complaint naming line 2 and EOF", err)
	}
	if _, err := Parse("Granule", src); err == nil || !strings.Contains(err.Error(), "Granule: line 2") {
		t.Fatalf("Parse = %v, want the same complaint, naming the file", err)
	}
}

// VM's verb is part of its name (§3.4), so the lexer splits it off: it
// is the one instruction whose second token says what it is, and every
// rule about where a converter may appear needs to know which verb this
// was before anything else can be decided.
// The FROM after PACK, WRAP or UNPACK is part of the name too, for the
// same reason: `VM PACK` converts the stage it sits in and
// `VM PACK FROM <ref>` starts a stage of its own (§4.1, §4.3), and which
// of the two this is has to be settled before any rule can look at it.
func TestLexVMVerb(t *testing.T) {
	got, err := Lex(`VM FROM ghcr.io/example/vm:1 AS m
vm pack --variant=debian
VM
VM SET memory-mb=2048
VM SNAPSHOT
VM unpack from ghcr.io/example/my-vm:latest AS unpacked
VM PACK FROM rootfs AS machine
VM WRAP FROM alpine:latest --restart=no redis AS wrapped
VM PACK --variant=debian FROM rootfs
`)
	if err != nil {
		t.Fatalf("Lex: %v", err)
	}
	want := []Instruction{
		{Name: "VM", Verb: "FROM", Args: "ghcr.io/example/vm:1 AS m", Line: 1},
		{Name: "VM", Verb: "PACK", Args: "--variant=debian", Line: 2},
		{Name: "VM", Verb: "", Args: "", Line: 3},
		{Name: "VM", Verb: "SET", Args: "memory-mb=2048", Line: 4},
		{Name: "VM", Verb: "SNAPSHOT", Args: "", Line: 5},
		{Name: "VM", Verb: "UNPACK", From: true, Args: "ghcr.io/example/my-vm:latest AS unpacked", Line: 6},
		{Name: "VM", Verb: "PACK", From: true, Args: "rootfs AS machine", Line: 7},
		{Name: "VM", Verb: "WRAP", From: true, Args: "alpine:latest --restart=no redis AS wrapped", Line: 8},
		// A FROM that is not directly after the verb is an argument, and
		// this is an in-stage converter with a stray word in it -- which
		// §5.3 refuses rather than reading as a header.
		{Name: "VM", Verb: "PACK", Args: "--variant=debian FROM rootfs", Line: 9},
	}
	if len(got) != len(want) {
		t.Fatalf("lexed %d instructions, want %d: %+v", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("instruction %d: got %+v, want %+v", i, got[i], want[i])
		}
	}

	// A diagnostic names the whole instruction, FROM included, so that
	// what it says can be searched for in the file (§10.2).
	for i, want := range []string{"VM FROM", "VM PACK", "VM", "VM SET", "VM SNAPSHOT", "VM UNPACK FROM", "VM PACK FROM", "VM WRAP FROM", "VM PACK"} {
		if got := got[i].String(); got != want {
			t.Errorf("instruction %d names itself %q, want %q", i, got, want)
		}
	}
	// And which of them start a stage is §4.1's list of five. An in-stage
	// converter is not one, which is the distinction the FROM carries.
	for i, header := range []bool{true, false, false, false, false, true, true, true, false} {
		if got[i].IsHeader() != header {
			t.Errorf("%s (line %d): IsHeader() = %v, want %v", got[i], got[i].Line, got[i].IsHeader(), header)
		}
		if got[i].IsConverter() == header && (got[i].Verb == "PACK" || got[i].Verb == "WRAP") {
			t.Errorf("%s (line %d) is both a header and an in-stage converter, and it is one or the other", got[i], got[i].Line)
		}
	}
}
