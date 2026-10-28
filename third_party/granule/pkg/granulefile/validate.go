package granulefile

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Violation is one thing granule refuses in a file, and why.
type Violation struct {
	// Line is the line in the author's own file, 1-based, counting
	// comments, blanks and continuation lines (§10.2).
	Line int
	// Instruction is what was refused, named the way the file names it
	// so it can be searched for -- "CMD", "VM PACK", "# syntax=".
	// Empty for a problem that is the file's rather than a line's.
	Instruction string
	// Reason is a sentence or two: what granule does instead, or what to
	// write instead. A refusal that only says "unsupported" leaves the
	// reader to guess whether they have hit a policy or a gap.
	Reason string
	// Rule is what the refusal is made under -- "K3", "§5.5", "K9, §6" --
	// so a reader can look the rule up rather than take the message's
	// word for it. Empty where there is nothing to cite.
	Rule string
	// Level is set when this is a construct the implementation has not
	// built yet rather than one the language forbids (§10.3). The two
	// are different answers and read differently: a reader has to know
	// whether to change their file or to wait.
	Level Level
}

func (v Violation) String() string {
	if v.Instruction == "" {
		return v.Reason
	}
	return fmt.Sprintf("%s: %s", v.Instruction, v.Reason)
}

// Unimplemented reports whether this is a gap rather than a rule.
func (v Violation) Unimplemented() bool { return v.Level != 0 }

// unimplemented is the §10.3 form: a construct this implementation does
// not build yet, named with the level it belongs to, so a reader knows
// they are waiting rather than wrong.
func unimplemented(in Instruction, level Level, what string) Violation {
	return Violation{
		Line:        in.Line,
		Instruction: in.String(),
		Level:       level,
		Rule:        "§2.3",
		Reason: fmt.Sprintf("unimplemented: %s is %s, and this implementation implements %s. This is a gap rather than a rule -- the language allows what you wrote (%s), and granule cannot build it yet",
			in, levelPhrase(level), previousLevel(level), what),
	}
}

func levelPhrase(l Level) string { return "a " + l.String() + " construct" }

func previousLevel(l Level) Level {
	if l <= Level1 {
		return Level1
	}
	return l - 1
}

// Error is the refusal of a whole file: every violation in it, not the
// first.
//
// Reporting all of them is deliberate (§10.1). An author porting a
// container Dockerfile typically trips several at once -- a HEALTHCHECK,
// a STOPSIGNAL and an EXPOSE are one habit, not three -- and a validator
// that reports one per build turns a five-minute edit into five builds.
type Error struct {
	// Path is the file as the caller named it, so the message is
	// something an editor can be pointed at.
	Path       string
	Violations []Violation
}

func (e *Error) Error() string {
	var b strings.Builder
	noun := "problem"
	if len(e.Violations) != 1 {
		noun = "problems"
	}
	fmt.Fprintf(&b, "%s: %d %s in this file, and a build refuses a file with any of them:", e.Path, len(e.Violations), noun)
	for _, v := range e.Violations {
		fmt.Fprintf(&b, "\n  %s:%d: %s", e.Path, v.Line, v)
		if v.Rule != "" {
			fmt.Fprintf(&b, " (%s)", v.Rule)
		}
	}
	// The VM stage's vocabulary is what a reader who has just had three
	// instructions refused is asking for -- but only if that is what
	// they tripped over. A file whose problem is a stage name does not
	// need the list.
	b.WriteString("\n")
	for _, v := range e.Violations {
		if strings.HasSuffix(v.Rule, "§6") {
			fmt.Fprintf(&b, "\nWhat a granule VM stage may use: %s.", strings.Join(Supported, ", "))
			break
		}
	}
	b.WriteString("\nSee docs/granulefile.md: §6 for a VM stage, §7 for a container stage above a converter, §4.5 for the K rules cited above.")
	return b.String()
}

// Unimplemented reports whether every violation is a gap in this
// implementation rather than something the language forbids: a file that
// is fine and that granule cannot build yet (§10.3).
func (e *Error) Unimplemented() bool {
	for _, v := range e.Violations {
		if !v.Unimplemented() {
			return false
		}
	}
	return len(e.Violations) > 0
}

// Options configures one validation.
type Options struct {
	// Level is the conformance level to validate against (§2.3). A
	// construct above it is reported as unimplemented, naming the level,
	// rather than as forbidden. Zero means Level3: check the file
	// against the whole language, which is what a bare --check does --
	// it builds nothing, so there is nothing it cannot build.
	Level Level

	// Target is the stage to build, if the caller named one (§4.4). It
	// decides which stage K10 requires instructions in, and it is
	// refused if the file has no such stage.
	Target string
}

func (o Options) withDefaults() Options {
	if o.Level == 0 {
		o.Level = Level3
	}
	return o
}

// Validate checks a parsed file against every rule in §4.5 and the
// per-kind instruction sets in §6 and §7, and returns an *Error naming
// the path, every line and every instruction it refuses.
//
// It validates the whole file, including stages the target does not
// depend on. §4.4 allows an implementation to validate only what it
// builds and requires it to say so; this one does not have to, because
// it builds nothing -- and a file whose errors appear one --target at a
// time is worse than one that reports them all.
func Validate(f *File, opts Options) error {
	opts = opts.withDefaults()

	var vs []Violation
	vs = append(vs, directiveViolations(f)...)
	vs = append(vs, globalViolations(f, opts)...)
	vs = append(vs, stageNameViolations(f)...)
	for _, s := range f.Stages {
		vs = append(vs, stageViolations(f, s, opts)...)
	}
	vs = append(vs, targetViolations(f, opts)...)

	sort.SliceStable(vs, func(i, j int) bool { return vs[i].Line < vs[j].Line })
	if len(vs) > 0 {
		return &Error{Path: f.Path, Violations: vs}
	}
	return nil
}

// directiveViolations is K8: a file that uses VM may not carry parser
// directives.
//
// A file that does not use VM is a Dockerfile, and its directives mean
// what they mean to docker and are passed through unchanged (§3.3).
func directiveViolations(f *File) []Violation {
	if !f.UsesVM() {
		return nil
	}
	out := Directives(f.Source)
	for i := range out {
		out[i].Rule = "K8"
	}
	return out
}

// Directives returns a violation for every parser directive in the
// file's leading comment block.
//
// A directive is only a directive at the very top of the file -- docker
// stops honouring them at the first line that is not one -- so only that
// leading run is examined. A `# syntax=` further down is an ordinary
// comment to docker as well, and refusing it would be an error about
// something that changes nothing either way.
//
// It is exported because every caller that rewrites the file before
// handing it to a container builder has the same problem with them, and
// the reasons should not be written down twice.
func Directives(src string) []Violation {
	var out []Violation
	for i, line := range SplitLines(src) {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") {
			break
		}
		if m := parserDirective.FindStringSubmatch(trimmed); m != nil {
			name := strings.ToLower(m[1])
			out = append(out, Violation{
				Line:        i + 1,
				Instruction: "# " + name + "=",
				Reason:      directiveReason(name),
			})
		}
	}
	return out
}

// directiveReason explains one refused parser directive.
//
// All three share a reason beyond their own, and it is mechanical rather
// than a matter of taste (§3.3): a file granule builds is rewritten --
// split into per-stage builds, with lines of granule's own supplied -- so
// a directive that was at the top of the author's file is not at the top
// of the file the container builder receives, and would be inert.
func directiveReason(name string) string {
	switch name {
	case "syntax":
		return "a syntax directive names the frontend that interprets this file, i.e. it replaces the instruction set granule just validated with whatever that image implements. granule builds with docker's built-in frontend and supplies lines of the file itself, so the directive would be inert as well as unchecked -- drop the line"
	case "escape":
		return "an escape directive changes the line-continuation character, which granule's own reading of this file does not follow -- so the file the builder is given and the file granule validated would not be the same file. Use the default backslash"
	default:
		return "a check directive configures docker's build-check warnings, about instructions these rules largely refuse anyway. What is valid in a Granule is decided here rather than by that lint"
	}
}

// globalViolations covers what may appear before the first stage header
// (§4.1) and the half of K10 that is about the file having a stage at
// all.
func globalViolations(f *File, opts Options) []Violation {
	var out []Violation
	for _, in := range f.Globals {
		switch {
		case in.IsConverter():
			out = append(out, Violation{
				Line: in.Line, Instruction: in.String(), Rule: "K4",
				Reason: "a converter turns the stage above it into a machine, and there is no stage above this one: nothing has been built here to convert. Start a stage with FROM first, or -- if the machine is what you are starting from -- use VM FROM",
			})
		case in.Name == "ARG":
		default:
			out = append(out, Violation{
				Line: in.Line, Instruction: in.String(), Rule: "§4.1",
				Reason: fmt.Sprintf("only ARG may appear before the first stage header, and this is a %s. Everything else belongs to a stage, and a stage begins at a FROM or a VM FROM", in),
			})
		}
	}

	if len(f.Stages) == 0 {
		line := 1
		if len(f.Globals) > 0 {
			line = f.Globals[len(f.Globals)-1].Line
		}
		out = append(out, Violation{
			Line: line, Rule: "K10",
			Reason: "this file has no stage: a Granule is one or more stages, and a stage begins at a FROM (a container build) or a VM FROM (a machine to derive from)",
		})
	}
	return out
}

// stageNameViolations covers what §4.1 requires of a stage's names: an
// image to start from, and names that are unique, are not numbers and are
// spellable.
//
// A stage can carry two of them -- a header's and a converter's -- and
// both are in the one namespace, because both are what a `COPY --from=`
// and a `--target` are given.
func stageNameViolations(f *File) []Violation {
	var out []Violation
	seen := map[string]Instruction{}
	for _, s := range f.Stages {
		if s.Ref == "" {
			out = append(out, Violation{
				Line: s.Header.Line, Instruction: s.Header.String(), Rule: "§4.1",
				Reason: "a stage header names the image the stage starts from, and this one names nothing",
			})
			continue
		}
		if hasDanglingAS(s.Header) {
			out = append(out, Violation{
				Line: s.Header.Line, Instruction: s.Header.String(), Rule: "§4.1",
				Reason: "AS names the stage, and this one is followed by nothing. Give the stage a name, or drop the AS",
			})
			continue
		}

		for _, n := range s.Names() {
			v := func(reason string) Violation {
				return Violation{Line: n.In.Line, Instruction: n.In.String(), Rule: "§4.1", Reason: reason}
			}
			if !stageName.MatchString(n.Name) {
				reason := fmt.Sprintf("%q is not a stage name: a name is a letter followed by letters, digits, underscores, dots and dashes", n.Name)
				if _, err := strconv.Atoi(n.Name); err == nil {
					reason = fmt.Sprintf("%q is a number, and a stage may already be referred to by its index -- a numeric name would make `COPY --from=%s` two different things", n.Name, n.Name)
				}
				out = append(out, v(reason))
				continue
			}
			key := strings.ToLower(n.Name)
			if first, dup := seen[key]; dup {
				out = append(out, v(fmt.Sprintf("a stage called %q was already declared on line %d, and stage names are unique and case-insensitive: `COPY --from=%s` would not say which was meant", n.Name, first.Line, n.Name)))
				continue
			}
			seen[key] = n.In
		}
	}
	return out
}

// stageViolations validates one stage, header and body.
//
// The whole stage is read before any of it is judged, because what the
// container part of it may use depends on which converter ends it (§7):
// refusing an EXPOSE in a stage whose last line turns out to be VM WRAP
// would be wrong, and accepting one in a stage that ends in VM PACK would
// be worse.
func stageViolations(f *File, s *Stage, opts Options) []Violation {
	var out []Violation
	out = append(out, headerRefViolations(f, s)...)
	out = append(out, headerViolations(f, s, opts)...)

	rules := containerRules(s)
	for i, in := range s.Instructions {
		if in.IsVM() {
			out = append(out, vmViolations(f, s, in, opts)...)
			continue
		}

		at := rules
		if s.KindAt(i) == VM {
			at = RulesVMStage
		}
		if v, refused := Refuse(in, at); refused {
			out = append(out, v)
			continue
		}
		out = append(out, volumeViolations(in)...)
		out = append(out, levelViolations(in, s.KindAt(i), opts)...)
		out = append(out, copyViolations(f, s, in, opts)...)
	}
	return out
}

// containerRules is §7: what the part of a stage above its in-stage
// converter may use, which the converter decides.
func containerRules(s *Stage) Rules {
	if c := s.Converter(); c != nil && c.Verb == "PACK" {
		return RulesAbovePack
	}
	return RulesContainer
}

// headerViolations decides one VM stage header: the base each of the four
// may not be started from (§5.1, §5.3, K6, K11), what a header
// converter's own options say (§5.2, §5.3), and whether this
// implementation starts a stage that way at all (§2.3). Where the base is
// another stage in this file, headerRefViolations has more to say about
// it than this does.
func headerViolations(f *File, s *Stage, opts Options) []Violation {
	in := s.Header
	v := func(rule, reason string) Violation {
		return Violation{Line: in.Line, Instruction: in.String(), Rule: rule, Reason: reason}
	}
	if !in.IsVM() {
		// An ordinary FROM. What else it names is docker's business.
		return nil
	}
	scratch := strings.EqualFold(s.Ref, "scratch")

	var out []Violation
	switch in.Verb {
	case "FROM":
		if scratch {
			out = append(out, v("§5.1", "there is no empty machine: a guest is a disk with an init on it, and VM FROM inherits one. Build a root filesystem in a container stage and convert it with VM PACK if what you want is a machine of your own making"))
		}
		if opts.Level < Level3 {
			out = append(out, unimplemented(in, Level3, "deriving a machine from an existing granule VM image"))
		}
		return out

	case "UNPACK":
		if scratch {
			out = append(out, v("K11", "VM UNPACK FROM extracts a container stage's root filesystem out of an existing machine's guest disk, and scratch is not a machine: it has no disk to extract and no filesystem to extract it into. Start from a granule VM image, or from an ordinary FROM if an empty stage is what you wanted"))
		}
		if opts.Level < Level2 {
			out = append(out, unimplemented(in, Level2, "extracting a machine's guest root filesystem back into a container stage"))
		}
		return out

	case "PACK", "WRAP":
		// A header converter is a converter: everything about its
		// options, its service name and what it may not convert is the
		// in-stage form's, and the only difference is that the placement
		// rules have nothing to say about it.
		out = append(out, converterViolations(f, s, in, opts)...)
		if in.Verb == "PACK" && scratch {
			out = append(out, v("§5.3", "VM PACK FROM packs a guest rootfs -- a filesystem carrying an init the packed disk can boot -- and scratch carries nothing at all. Pack ghcr.io/gke-labs/granule/debian.rootfs, a stage built from it, or a VM UNPACK FROM of an existing machine"))
		}
		return out
	}
	return out
}

// headerRefViolations decides what a stage header may point *at*, for the
// one case where this package can know: a reference that names another
// stage in the same file, whose kind it has already worked out.
//
// An external reference is a registry question and is left alone -- what
// is at the other end of `VM FROM ghcr.io/example/vm:1` is not knowable
// here, and §5.1 puts that check in the builder. A stage in the file is
// different: the file says what kind it is, so pointing a machine
// instruction at a container stage, or a pack at a machine, is a mistake
// this can name before anything is pulled.
func headerRefViolations(f *File, s *Stage) []Violation {
	in := s.Header
	v := func(rule, reason string) Violation {
		return Violation{Line: in.Line, Instruction: in.String(), Rule: rule, Reason: reason}
	}
	if s.Ref == "" || strings.EqualFold(s.Ref, "scratch") {
		return nil
	}
	source, kind, ok := f.Stage(s.Ref)
	if !ok {
		return nil
	}

	// Stages are built in the order they appear, so a header may only
	// name one above it -- which is what keeps the stage graph acyclic
	// (K7) without a graph walk.
	if source.Index >= s.Index {
		if source == s {
			return []Violation{v("K7", fmt.Sprintf("this stage starts from %q, which is its own name: a stage cannot be built from itself. Name an earlier stage, or an image", s.Ref))}
		}
		return []Violation{v("K7", fmt.Sprintf("%q names a stage declared below this one, and stages are built in the order they appear: at this line that stage has not been built. Move it above, or start from an image", s.Ref))}
	}

	switch {
	case in.Verb == "FROM" && kind != VM:
		return []Violation{v("§5.1", fmt.Sprintf("VM FROM inherits a machine, and %q is a container stage rather than one: it has no guest disk to boot. VM PACK FROM %s is what packs that filesystem into a machine, and is almost certainly what was meant", s.Ref, s.Ref))}
	case in.Verb == "UNPACK" && kind != VM:
		return []Violation{v("K11", fmt.Sprintf("VM UNPACK FROM extracts a root filesystem out of a machine's guest disk, and %q is a container stage: its filesystem is already one, so an ordinary FROM %s is what reads it", s.Ref, s.Ref))}
	case (in.Verb == "PACK" || in.Verb == "WRAP") && kind == VM:
		section := "§5.3"
		what := "VM PACK FROM packs a container filesystem into a bootable disk"
		if in.Verb == "WRAP" {
			section = "§5.2"
			what = "VM WRAP FROM runs a container image as a workload inside a guest"
		}
		return []Violation{v(section, fmt.Sprintf("%s, and %q is already a machine: what it produces is a guest disk rather than a filesystem to convert. VM FROM %s derives from a machine; COPY --from=%s reads files out of one", what, s.Ref, s.Ref, s.Ref))}
	}
	return nil
}

// levelViolations reports the VM-stage instructions this implementation
// does not execute yet (§2.3, §10.3).
func levelViolations(in Instruction, kind Kind, opts Options) []Violation {
	if kind != VM || opts.Level >= Level3 {
		return nil
	}
	switch in.Name {
	case "RUN":
		return []Violation{unimplemented(in, Level3, "running a command inside a booted guest -- the expensive instruction in the language, and the one that needs /dev/kvm")}
	case "VOLUME":
		return []Violation{unimplemented(in, Level3, "recording an in-guest mount point, and mounting something at it when the machine runs")}
	case "SHELL":
		return []Violation{unimplemented(in, Level3, "choosing the interpreter a shell-form RUN is handed to inside the guest, which is only meaningful once a RUN in a VM stage runs at all")}
	case "ENTRYPOINT", "CMD":
		return []Violation{unimplemented(in, Level3, "recording the workload the guest boots into, so that a machine runs what the file says without a unit file of its own")}
	}
	return nil
}

// copyViolations is K7 and the level rule for §8's cross-stage copies: a
// --from may only name an earlier stage or an external image.
func copyViolations(f *File, s *Stage, in Instruction, opts Options) []Violation {
	if in.Name != "COPY" && in.Name != "ADD" {
		return nil
	}
	ref := copyFrom(in.Args)
	if ref == "" {
		return nil
	}

	v := func(rule, reason string) Violation {
		return Violation{Line: in.Line, Instruction: in.String(), Rule: rule, Reason: reason}
	}

	// A stage cannot copy out of itself -- not by its index, not by the
	// name of its container half, and not by the name its own converter
	// gave the machine it produces.
	if ref == strconv.Itoa(s.Index) ||
		(s.Name != "" && strings.EqualFold(ref, s.Name)) ||
		(s.ConvertedName != "" && strings.EqualFold(ref, s.ConvertedName)) {
		return []Violation{v("K7", fmt.Sprintf("--from=%s names the stage this copy is in, and a stage cannot copy out of itself: the source would be the filesystem the copy is writing into, half built. Name an earlier stage, or an image", ref))}
	}

	source, sourceKind, ok := f.Stage(ref)
	if !ok {
		if i, err := strconv.Atoi(ref); err == nil {
			return []Violation{v("K7", fmt.Sprintf("--from=%d names stage %d and this file has %d, so there is no such stage to copy out of. A --from is a stage name, a stage index, or an image reference -- and a number is read as an index", i, i, len(f.Stages)))}
		}
		// Not a stage: an external image reference, which is ordinary
		// and which this package cannot check without a registry.
		return nil
	}
	if source.Index > s.Index {
		return []Violation{v("K7", fmt.Sprintf("--from=%s names a stage declared below this one, and stages are built in the order they appear: at this line that stage has not been built. Move it above, or copy from an image", ref))}
	}

	// What a copy out of a VM stage reads is that stage's guest root
	// filesystem (§8), which is a different operation from reading a
	// container stage's -- and a level 3 one.
	if sourceKind == VM && opts.Level < Level3 {
		return []Violation{unimplemented(in, Level3, "copying out of a VM stage's guest root filesystem")}
	}
	return nil
}

// copyFrom returns the --from= of a COPY or ADD, or "".
func copyFrom(args string) string {
	for _, f := range fields(args) {
		if !strings.HasPrefix(f, "--") {
			break
		}
		if value, ok := strings.CutPrefix(f, "--from="); ok {
			return value
		}
	}
	return ""
}

// targetViolations is the rest of K10, and the refusal of a --target
// naming a stage the file does not have.
func targetViolations(f *File, opts Options) []Violation {
	if len(f.Stages) == 0 {
		return nil
	}
	target, _, ok := f.Target(opts.Target)
	if !ok {
		last := f.Stages[len(f.Stages)-1]
		return []Violation{{
			Line: last.Header.Line, Rule: "§4.4",
			Reason: fmt.Sprintf("this file has no stage called %q to build: it has %s", opts.Target, strings.Join(stageLabels(f), ", ")),
		}}
	}

	// A stage that exists only to be copied out of may be empty, and so
	// may a target somebody named: both were asked for by name. The last
	// stage of a file, reached because it is last, may not -- building it
	// would publish a copy of its own base under a new name.
	if opts.Target == "" && len(target.Instructions) == 0 {
		return []Violation{{
			Line: target.Header.Line, Instruction: target.Header.String(), Rule: "K10",
			Reason: "this is the stage the build would produce, and it does nothing: what it published would be a copy of its own base under a new name. Give it something to do, or name the stage you meant to build",
		}}
	}
	return nil
}

func stageLabels(f *File) []string {
	out := make([]string, 0, len(f.Stages))
	for _, s := range f.Stages {
		out = append(out, s.Label())
	}
	return out
}

func volumeViolations(in Instruction) []Violation {
	if !strings.EqualFold(in.Name, "VOLUME") {
		return nil
	}
	paths, err := ParseVolumeArgs(in.Args)
	if err != nil {
		return []Violation{{
			Line:        in.Line,
			Instruction: in.String(),
			Rule:        "§6",
			Reason:      err.Error(),
		}}
	}
	for _, p := range paths {
		if !strings.HasPrefix(p, "/") {
			return []Violation{{
				Line:        in.Line,
				Instruction: in.String(),
				Rule:        "§6",
				Reason:      fmt.Sprintf("%q is not an absolute path: guest volume mount points must start with '/'", p),
			}}
		}
	}
	return nil
}
