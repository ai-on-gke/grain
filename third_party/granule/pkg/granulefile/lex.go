package granulefile

import (
	"fmt"
	"regexp"
	"strings"
)

// Instruction is one instruction as it appeared in a file: its name
// upper-cased, the verb of a VM instruction (§3.4) if it has one, the
// rest of the line with any continuations folded into it, and the line
// its first line was on.
//
// Line is what every refusal is reported against, so it counts lines in
// the author's own file -- comments, blanks and continuation lines
// included -- rather than instructions (§10.2).
type Instruction struct {
	Name string
	// Verb is the second token of a VM instruction, upper-cased: FROM,
	// WRAP, PACK, UNPACK, SET or SNAPSHOT (§4.5, K1). Empty for every
	// other instruction, and empty for a VM that was given no verb at
	// all.
	Verb string
	// From reports that the verb was followed by the word FROM, which is
	// what distinguishes the two ways a converter can be written (§4.3):
	// `VM PACK` converts the stage it sits in, and `VM PACK FROM <ref>`
	// starts a new stage from <ref>. The word itself is not left in Args.
	From bool
	Args string
	Line int
}

// String names the instruction the way a diagnostic has to: the whole
// name, which for VM includes its verb and the FROM that makes it a
// stage header.
func (in Instruction) String() string {
	name := in.Name
	if in.Verb != "" {
		name += " " + in.Verb
	}
	if in.From && in.Verb != "FROM" {
		name += " FROM"
	}
	return name
}

// IsVM reports whether this is the VM instruction, whatever its verb.
func (in Instruction) IsVM() bool { return in.Name == "VM" }

// IsHeader reports whether this instruction starts a stage (§4.1). Five
// things do: FROM, VM FROM, VM UNPACK FROM, VM PACK FROM and
// VM WRAP FROM.
func (in Instruction) IsHeader() bool {
	if in.Name == "FROM" {
		return true
	}
	if !in.IsVM() {
		return false
	}
	switch in.Verb {
	case "FROM":
		return true
	case "UNPACK", "PACK", "WRAP":
		return in.From
	}
	return false
}

// IsConverter reports whether this is an *in-stage* converter: a VM WRAP
// or VM PACK that converts the stage it appears in, rather than one of
// the FROM forms that starts a stage of its own (§4.3).
func (in Instruction) IsConverter() bool {
	return in.IsVM() && !in.From && (in.Verb == "WRAP" || in.Verb == "PACK")
}

// Lex returns the instruction list of a file's text.
//
// It parses as little as it can get away with: enough to know what
// instruction each line is, and nothing about what any of them mean.
// That is exactly the amount §3.2 asks for -- what an instruction
// *means* is the builder's business, and what is allowed to *appear* is
// this package's. Four things force it to be more than a line split, and
// the first three are cases where splitting on newlines would read
// something as an instruction that docker does not:
//
//   - Comments and blank lines, which are not instructions.
//   - Line continuations, whose second and subsequent lines belong to
//     the instruction above rather than being instructions themselves.
//   - Heredocs (`RUN <<EOF`), whose body is shell -- and a body line
//     reading `apt-get update` is not an APT-GET instruction. The
//     detection here mirrors BuildKit's own, which tests each token of
//     the line rather than searching the line: the `<<` in a
//     `$((1<<SHIFT))` inside a RUN is arithmetic, not a redirect.
//   - VM's verb, which is part of its name (§3.4) rather than an
//     argument: VM is the one instruction whose second token says what
//     it is.
//
// The only thing it refuses outright is a heredoc that is never closed,
// because everything below one is its body: parsing on would validate
// lines that are not instructions and skip the ones that are.
func Lex(src string) ([]Instruction, error) {
	lines := SplitLines(src)
	var instrs []Instruction

	for i := 0; i < len(lines); {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			i++
			continue
		}

		start := i
		// Fold continuations. A comment or blank line inside one is
		// skipped rather than ending it, which is what docker does with
		// it too.
		var folded string
		for {
			part := strings.TrimSpace(lines[i])
			continued := strings.HasSuffix(part, `\`)
			part = strings.TrimRight(strings.TrimSuffix(part, `\`), " \t")
			switch {
			case folded == "":
				folded = part
			case part != "":
				folded += " " + part
			}
			i++
			if !continued {
				break
			}
			for i < len(lines) {
				next := strings.TrimSpace(lines[i])
				if next != "" && !strings.HasPrefix(next, "#") {
					break
				}
				i++
			}
			if i >= len(lines) {
				break
			}
		}

		// Consume the body of any heredoc the instruction opened, so its
		// contents are never read as instructions.
		for _, delim := range heredocDelimiters(folded) {
			end, err := consumeHeredoc(lines, i, delim)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", start+1, err)
			}
			i = end
		}

		name, args, _ := strings.Cut(folded, " ")
		in := Instruction{
			Name: strings.ToUpper(strings.TrimSpace(name)),
			Args: strings.TrimSpace(args),
			Line: start + 1,
		}
		if in.IsVM() {
			verb, rest, _ := strings.Cut(in.Args, " ")
			in.Verb = strings.ToUpper(strings.TrimSpace(verb))
			in.Args = strings.TrimSpace(rest)
			// A FROM directly after the verb belongs to the name rather
			// than to the arguments: it is what makes WRAP, PACK and
			// UNPACK stage headers (§4.1). Anywhere else in the line it
			// is an argument, and is refused as one.
			switch in.Verb {
			case "UNPACK", "PACK", "WRAP":
				if word, rest, _ := strings.Cut(in.Args, " "); strings.EqualFold(strings.TrimSpace(word), "FROM") {
					in.From = true
					in.Args = strings.TrimSpace(rest)
				}
			}
		}
		instrs = append(instrs, in)
	}

	return instrs, nil
}

// SplitLines splits a file into lines, dropping the CR of a CRLF so that
// a file written on Windows parses identically (§3.1).
func SplitLines(src string) []string {
	lines := strings.Split(src, "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	return lines
}

// parserDirective matches the comment forms docker treats as directives
// rather than as comments.
var parserDirective = regexp.MustCompile(`^#\s*(syntax|escape|check)\s*=`)

// heredoc matches one token of an instruction that opens a heredoc: an
// optional file descriptor, "<<", an optional "-" for tab stripping, and
// the delimiter, optionally quoted. Anchored at both ends and applied
// per token, as BuildKit's own parser does it.
var heredoc = regexp.MustCompile(`^\d*<<(-?)(["']?)([A-Za-z_][A-Za-z0-9_.-]*)(["']?)$`)

// heredocDelimiter is one heredoc an instruction opened.
type heredocDelimiter struct {
	word      string
	stripTabs bool
}

func heredocDelimiters(folded string) []heredocDelimiter {
	var out []heredocDelimiter
	for _, token := range strings.Fields(folded) {
		m := heredoc.FindStringSubmatch(token)
		if m == nil || m[2] != m[4] {
			continue
		}
		out = append(out, heredocDelimiter{word: m[3], stripTabs: m[1] == "-"})
	}
	return out
}

// consumeHeredoc returns the index just past the terminator of the
// heredoc whose body starts at lines[from].
func consumeHeredoc(lines []string, from int, delim heredocDelimiter) (int, error) {
	for i := from; i < len(lines); i++ {
		candidate := lines[i]
		if delim.stripTabs {
			candidate = strings.TrimLeft(candidate, "\t")
		}
		if strings.TrimRight(candidate, " \t") == delim.word {
			return i + 1, nil
		}
	}
	return len(lines), fmt.Errorf("the heredoc opened here is never closed by a %q line, so everything below it is its body", delim.word)
}

// fields splits an instruction's arguments on whitespace, keeping a
// quoted run together and stripping the quotes that held it.
//
// VM's own arguments need this and Dockerfile's lexing does not give it
// to us: `VM SET kernel-cmdline="console=ttyS0 quiet"` is one setting
// whose value has a space in it, and strings.Fields would read the
// second half as a second setting with an unknown key (§5.5).
func fields(args string) []string {
	var (
		out   []string
		cur   strings.Builder
		quote rune
		have  bool
	)
	flush := func() {
		if have {
			out = append(out, cur.String())
			cur.Reset()
			have = false
		}
	}
	for _, r := range args {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			cur.WriteRune(r)
		case r == '"' || r == '\'':
			quote = r
			have = true
		case r == ' ' || r == '\t':
			flush()
		default:
			cur.WriteRune(r)
			have = true
		}
	}
	flush()
	return out
}
