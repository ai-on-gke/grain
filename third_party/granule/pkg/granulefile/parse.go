package granulefile

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// File is a parsed Granule: its global ARGs and its stages, in the
// order they appear.
//
// Parsing groups instructions; it decides nothing about whether they are
// allowed. Everything a file can be refused for is a Violation from
// Validate, so that a file with several problems is refused once with
// all of them rather than once per problem (§10.1).
type File struct {
	// Path is the file as the caller named it, so a diagnostic is
	// something an editor can be pointed at.
	Path string
	// Source is the file's text, kept because the parser directives
	// (§3.3) are comments and so are not instructions.
	Source string
	// Globals are the instructions before the first stage header. They
	// are meant to be ARGs and comments; anything else there is refused
	// (§4.1).
	Globals []Instruction
	Stages  []*Stage
}

// UsesVM reports whether the file uses the VM instruction anywhere,
// which is what decides whether it is a Dockerfile (§1.2) and whether
// parser directives are refused (K8).
func (f *File) UsesVM() bool {
	for _, in := range f.Globals {
		if in.IsVM() {
			return true
		}
	}
	for _, s := range f.Stages {
		if s.Header.IsVM() {
			return true
		}
		for _, in := range s.Instructions {
			if in.IsVM() {
				return true
			}
		}
	}
	return false
}

// Stage looks a stage up the way COPY --from= and --target do: by name,
// case-insensitively, or by zero-based index (§4.1).
//
// The kind it returns is what the name refers to, which is not always
// what the stage ends up being. A stage that starts `FROM x AS rootfs`
// and converts with `VM PACK AS machine` carries two names: `rootfs` is
// the container filesystem as it stood before the pack, and `machine` is
// the machine the pack made of it (§4.1). What a `COPY --from=` reads
// depends on which of the two it named (§8), so a caller is told.
func (f *File) Stage(ref string) (*Stage, Kind, bool) {
	for _, s := range f.Stages {
		if s.ConvertedName != "" && strings.EqualFold(s.ConvertedName, ref) {
			return s, VM, true
		}
		if s.Name != "" && strings.EqualFold(s.Name, ref) {
			// With a name on the converter too, the header's name is the
			// container half and only that; without one, the header's
			// name is the whole stage and means what the stage produces.
			if s.ConvertedName != "" {
				return s, s.HeaderKind(), true
			}
			return s, s.Kind(), true
		}
	}
	// An index addresses the stage rather than a half of it, so it means
	// what the stage produces.
	if i, err := strconv.Atoi(ref); err == nil && i >= 0 && i < len(f.Stages) {
		return f.Stages[i], f.Stages[i].Kind(), true
	}
	return nil, Container, false
}

// Target is the stage a build produces, and what it produces: the stage
// named, or the last in the file (§4.4).
func (f *File) Target(name string) (*Stage, Kind, bool) {
	if name != "" {
		return f.Stage(name)
	}
	if len(f.Stages) == 0 {
		return nil, Container, false
	}
	last := f.Stages[len(f.Stages)-1]
	return last, last.Kind(), true
}

// Stage is one stage: its header, and the instructions under it up to
// the next header or the end of the file.
type Stage struct {
	Index  int
	Header Instruction
	// Ref is the image reference in the header, as written.
	Ref string
	// Name is the header's AS name as written, or empty. On a container
	// header that is later converted by a named converter it is the
	// container filesystem's name and not the machine's (§4.1).
	Name string
	// ConvertedName is the AS name an in-stage converter gives the
	// machine it makes of this stage, or empty. A stage can carry both
	// names, and they mean different things to a COPY --from=.
	ConvertedName string
	// Instructions is the body, not including the header.
	Instructions []Instruction
}

// HeaderKind is the stage's kind at its header (§4.2's table): a machine
// from the first line for VM FROM, VM PACK FROM and VM WRAP FROM, and a
// container build for FROM and VM UNPACK FROM.
func (s *Stage) HeaderKind() Kind {
	if s.Header.IsVM() && s.Header.Verb != "UNPACK" {
		return VM
	}
	return Container
}

// StartsAsVM reports whether the stage was a machine from its header,
// which is what makes an in-stage converter in it meaningless (K3).
func (s *Stage) StartsAsVM() bool { return s.HeaderKind() == VM }

// Converter is the stage's in-stage converter (§4.3), or nil. A stage
// with more than one is refused (K2), and this is the first.
func (s *Stage) Converter() *Instruction {
	for i := range s.Instructions {
		if s.Instructions[i].IsConverter() {
			return &s.Instructions[i]
		}
	}
	return nil
}

// Kind is what the stage is building by the end of it, which is what
// decides the build's result when it is the target (§4.4).
func (s *Stage) Kind() Kind {
	if s.StartsAsVM() || s.Converter() != nil {
		return VM
	}
	return Container
}

// KindAt is the kind of the stage at the i'th instruction of its body:
// container above an in-stage converter, VM at it and below it (§4.2).
// The converter itself is reported as a VM-stage instruction, since that
// is the line the change happens on.
func (s *Stage) KindAt(i int) Kind {
	if s.StartsAsVM() {
		return VM
	}
	for j := 0; j <= i && j < len(s.Instructions); j++ {
		if s.Instructions[j].IsConverter() {
			return VM
		}
	}
	return Container
}

// Label names the stage the way a diagnostic should: its name if it has
// one -- the machine's, where the two differ -- and its index if it has
// none.
func (s *Stage) Label() string {
	switch {
	case s.ConvertedName != "":
		return s.ConvertedName
	case s.Name != "":
		return s.Name
	default:
		return fmt.Sprintf("stage %d", s.Index)
	}
}

// StageName is one name a stage answers to, and the instruction that
// gave it -- a header's AS, or a converter's.
type StageName struct {
	Name string
	In   Instruction
}

// Names are every name this stage answers to, in the order they are
// written: what §4.1 requires to be unique across a file.
func (s *Stage) Names() []StageName {
	var out []StageName
	if s.Name != "" {
		out = append(out, StageName{s.Name, s.Header})
	}
	if c := s.Converter(); c != nil && s.ConvertedName != "" {
		out = append(out, StageName{s.ConvertedName, *c})
	}
	return out
}

// Parse reads a file into stages. The only thing it can fail on is the
// lexing (§3.2's unterminated heredoc); everything else a file can be
// wrong about is Validate's, because a parse that stopped at the first
// problem could not report the rest of them.
func Parse(path, src string) (*File, error) {
	instrs, err := Lex(src)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	f := &File{Path: path, Source: src}
	var current *Stage
	for _, in := range instrs {
		if in.IsHeader() {
			ref, name := headerParts(in)
			current = &Stage{Index: len(f.Stages), Header: in, Ref: ref, Name: name}
			f.Stages = append(f.Stages, current)
			continue
		}
		if current == nil {
			f.Globals = append(f.Globals, in)
			continue
		}
		current.Instructions = append(current.Instructions, in)
		// An in-stage converter may name the machine it makes of the
		// stage, which is a second name for the same stage (§4.1). The
		// first converter's is the one that counts; a stage with two is
		// refused (K2).
		if in.IsConverter() && current.ConvertedName == "" {
			current.ConvertedName = asName(fields(in.Args))
		}
	}
	return f, nil
}

// Check parses and validates in one call: what a --check wants, and what
// a builder would call before building anything.
func Check(path, src string, opts Options) (*File, error) {
	f, err := Parse(path, src)
	if err != nil {
		return nil, err
	}
	if err := Validate(f, opts); err != nil {
		return f, err
	}
	return f, nil
}

// headerParts pulls the reference and the AS name out of a stage header,
// in any of its five shapes (§4.1).
//
// Flags are skipped rather than read: a `FROM --platform=linux/amd64` is
// the container builder's business, and a converter header's own options
// are read by §5's own checks. The only things this needs off the line
// are which image and which name -- and the name is the last two tokens,
// which is where every shape puts it, `VM WRAP FROM ref --restart=no
// svc AS machine` included.
func headerParts(in Instruction) (ref, name string) {
	args := fields(in.Args)
	for len(args) > 0 && strings.HasPrefix(args[0], "--") {
		args = args[1:]
	}
	if len(args) == 0 {
		return "", ""
	}
	return args[0], asName(args[1:])
}

// asName is the "AS name" at the end of a header or a converter, or "".
func asName(args []string) string {
	if n := len(args); n >= 2 && strings.EqualFold(args[n-2], "AS") {
		return args[n-1]
	}
	return ""
}

// hasDanglingAS reports a header or converter whose AS names nothing,
// which is worth saying rather than silently leaving the stage unnamed.
func hasDanglingAS(in Instruction) bool {
	args := fields(in.Args)
	return len(args) >= 1 && strings.EqualFold(args[len(args)-1], "AS")
}

// stageName is what §4.1 requires of an AS name: it may not be a number
// (a stage may be referred to by index, and a numeric name would make
// "COPY --from=2" ambiguous), and it is otherwise a letter followed by
// letters, digits, underscores, dots and dashes.
var stageName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]*$`)

// ParseVolumeArgs parses the in-guest mount paths from a VOLUME instruction.
// Supports exec form `VOLUME ["/path1", "/path2"]` and shell form `VOLUME /path1 /path2`.
func ParseVolumeArgs(args string) ([]string, error) {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" {
		return nil, fmt.Errorf("VOLUME requires at least one path argument")
	}
	if strings.HasPrefix(trimmed, "[") {
		var paths []string
		if err := json.Unmarshal([]byte(trimmed), &paths); err != nil {
			return nil, fmt.Errorf("invalid VOLUME JSON array: %w", err)
		}
		if len(paths) == 0 {
			return nil, fmt.Errorf("VOLUME JSON array must contain at least one path")
		}
		return paths, nil
	}
	paths := fields(trimmed)
	if len(paths) == 0 {
		return nil, fmt.Errorf("VOLUME requires at least one path argument")
	}
	return paths, nil
}

// Volumes returns all volume mount points declared in this stage via VOLUME instructions.
func (s *Stage) Volumes() ([]string, error) {
	var out []string
	for _, in := range s.Instructions {
		if strings.EqualFold(in.Name, "VOLUME") {
			paths, err := ParseVolumeArgs(in.Args)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", in.Line, err)
			}
			out = append(out, paths...)
		}
	}
	return out, nil
}
