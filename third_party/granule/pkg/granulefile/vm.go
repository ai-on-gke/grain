package granulefile

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// verbs is K1's closed set. The set being closed is what makes adding a
// verb later a compatible change rather than a silent reinterpretation
// of somebody's file (§11.4): a file written against a later version
// fails here by naming the construct it wanted.
var verbs = []string{"FROM", "WRAP", "PACK", "UNPACK", "SET", "SNAPSHOT"}

// settings is §5.5's table: the keys VM SET may record, and what a value
// of each has to look like. The key set is closed for the same reason
// the verb set is.
var settings = map[string]struct {
	env    string
	check  func(string) error
	syntax string
}{
	"cpus":             {env: "CHV_CPUS", check: positiveInt, syntax: "a positive whole number of vCPUs"},
	"vcpus":            {env: "CHV_VCPUS", check: positiveInt, syntax: "a positive whole number of vCPUs"},
	"cpus-max":         {env: "CHV_CPUS_MAX", check: positiveInt, syntax: "a positive whole number of vCPUs"},
	"memory-mb":        {env: "CHV_MEMORY_MB", check: positiveInt, syntax: "a positive whole number of MiB"},
	"memory-max-mb":    {env: "CHV_MEMORY_MAX_MB", check: positiveInt, syntax: "a positive whole number of MiB"},
	"memory-hotplug":   {env: "CHV_MEMORY_HOTPLUG", check: onOff, syntax: "on or off"},
	"kernel-cmdline":   {env: "CHV_CMDLINE", check: nonEmpty, syntax: "kernel command line arguments; quote it if it has a space in it"},
	"disk-mode":        {env: "CHV_DISK_MODE", check: oneOf("overlay", "persistent", "readonly"), syntax: "overlay, persistent or readonly"},
	"storage-backend":  {env: "CHV_STORAGE_BACKEND", check: oneOf("standard", "extents"), syntax: "standard or extents"},
	"disk-size-mb":     {env: "CHV_DISK_SIZE_MB", check: positiveInt, syntax: "a positive whole number of MiB"},
	"extent-size-mb":   {env: "CHV_EXTENT_SIZE_MB", check: positiveInt, syntax: "a positive whole number of MiB"},
	"disk-num-queues":  {env: "CHV_DISK_NUM_QUEUES", check: positiveInt, syntax: "a positive whole number of virtio-blk queues"},
	"num-queues":       {env: "CHV_DISK_NUM_QUEUES", check: positiveInt, syntax: "a positive whole number of virtio-blk queues"},
	"disk-iothreads":   {env: "CHV_DISK_IOTHREADS", check: onOff, syntax: "on or off"},
	"iothreads":        {env: "CHV_DISK_IOTHREADS", check: onOff, syntax: "on or off"},
	"disk-io-engine":   {env: "CHV_DISK_IO_ENGINE", check: oneOf("io_uring", "epoll", "sync"), syntax: "io_uring, epoll or sync"},
	"io-engine":        {env: "CHV_DISK_IO_ENGINE", check: oneOf("io_uring", "epoll", "sync"), syntax: "io_uring, epoll or sync"},
	"shutdown-timeout": {env: "CHV_SHUTDOWN_TIMEOUT", check: duration, syntax: "a duration with a unit, e.g. 30s or 2m"},
	"snapshot":         {env: "CHV_SNAPSHOT_PATH", check: onOff, syntax: "on or off; on is VM SNAPSHOT written as a setting (§5.6)"},
}

// SettingEnv is the runtime variable one VM SET key records itself as
// (§5.5). It is what a builder will write into the produced image's OCI
// config, so that nothing has to read a Granule to boot the result.
func SettingEnv(key string) (string, bool) {
	s, ok := settings[strings.ToLower(key)]
	if !ok {
		return "", false
	}
	return s.env, true
}

// wrapOptions and packOptions are §5.2's and §5.3's, with what a value
// of each has to look like. An unknown option is refused rather than
// ignored, and so is an option given twice (§5).
var wrapOptions = map[string]func(string) error{
	"name":        serviceName,
	"restart":     oneOf("always", "on-failure", "no"),
	"exit-action": oneOf("ignore", "poweroff"),
	"runtime":     nonEmpty,
}

var packOptions = map[string]func(string) error{
	"variant":       oneOf("debian", "alpine", "auto"),
	"disk-extra-mb": wholeInt,
}

// vmViolations decides one VM instruction that is not a stage header:
// its verb (K1), where it is allowed to be (K2-K5, K11) and its own
// options and settings (§5). The headers are headerViolations'.
func vmViolations(f *File, s *Stage, in Instruction, opts Options) []Violation {
	v := func(rule, reason string) Violation {
		return Violation{Line: in.Line, Instruction: in.String(), Rule: rule, Reason: reason}
	}

	switch in.Verb {
	case "":
		return []Violation{v("K1", "VM takes a verb, and this line gives it none. The verbs are "+strings.Join(verbs, ", ")+": VM FROM starts a stage from a machine, VM WRAP and VM PACK turn the container stage above them into one (or start a stage of their own, written VM PACK FROM), VM UNPACK FROM starts a container stage from a machine's disk, VM SET records a default for the machine that boots, and VM SNAPSHOT saves its running state into the image")}

	case "FROM":
		// A VM FROM is a stage header, so it is never reached here: the
		// parser starts a stage on it. Reaching this means something in
		// the grouping changed, and saying so is better than silence.
		return []Violation{v("§6", "VM FROM starts a stage, so it cannot appear inside one")}

	case "UNPACK":
		// A VM UNPACK FROM is a header too, so what reaches here is an
		// UNPACK with no FROM after it: not a form the language has.
		return []Violation{v("K11", "VM UNPACK is always written VM UNPACK FROM <ref>, and it starts a container stage whose root filesystem is extracted from that machine's disk (§5.4). It is a stage header, so it cannot appear inside a stage, and it is not an instruction on its own")}

	case "WRAP", "PACK":
		return converterViolations(f, s, in, opts)

	case "SET":
		return setViolations(s, in, opts)

	case "SNAPSHOT":
		return snapshotViolations(s, in, opts)
	}

	return []Violation{v("K1", fmt.Sprintf("%q is not a VM verb. The verbs are a closed set -- %s -- so a file written against a later version of the language fails here by naming what it wanted, rather than building something else", in.Verb, strings.Join(verbs, ", ")))}
}

// converterViolations decides a VM WRAP or VM PACK in either of its two
// forms (§4.3): where it is (K2, K3, K6), whether this implementation
// builds one (§2.3) and its options (§5.2, §5.3).
//
// The header form (`VM PACK FROM <ref>`) starts a stage of its own, so
// the placement rules do not apply to it -- there is no stage above it to
// convert twice or to have started as a machine -- but everything about
// its options, its service name and what it may not pack is the same.
func converterViolations(f *File, s *Stage, in Instruction, opts Options) []Violation {
	v := func(rule, reason string) Violation {
		return Violation{Line: in.Line, Instruction: in.String(), Rule: rule, Reason: reason}
	}
	var out []Violation

	if !in.From {
		switch {
		case s.StartsAsVM():
			out = append(out, v("K3", fmt.Sprintf("this stage started with %s, so it is already a machine and there is nothing to convert. A converter takes the container image a stage has built and makes a machine of it; below a machine header there is no container image, and the instructions here are already running against the guest", s.Header)))
		case s.Converter() != nil && s.Converter().Line != in.Line:
			out = append(out, v("K2", fmt.Sprintf("this stage already converted at line %d, and a stage holds at most one in-stage converter. What follows a converter is a machine, so a second one would be asking to convert the machine it just produced -- start a new stage (FROM, VM FROM, or a header converter written VM %s FROM <ref>) if what you meant was a second build", s.Converter().Line, in.Verb)))
		}
	}

	if in.Verb == "WRAP" {
		if from, scratch := wrapsScratch(f, s); scratch {
			out = append(out, v("K6", fmt.Sprintf("a wrapped workload is run by a container runtime inside the guest, and %s has no filesystem for it to run. Build the workload on a base that has one, or use VM PACK if what you meant was that this filesystem is the machine's root", from)))
		}
	}

	if opts.Level < Level2 {
		what := "converting a container stage into a machine"
		if in.From {
			what = "starting a VM stage by converting an image or an earlier stage"
		}
		out = append(out, unimplemented(in, Level2, what))
	}

	table, what := wrapOptions, "§5.2"
	if in.Verb == "PACK" {
		table, what = packOptions, "§5.3"
	}

	tail := converterTail(in)
	// A FROM anywhere but directly after the verb is one mistake, not two:
	// what follows it was meant to be a reference, so nothing past it is
	// read as an option or a name.
	for i, arg := range tail {
		if strings.EqualFold(arg, "FROM") {
			out = append(out, Violation{
				Line: in.Line, Instruction: in.String(), Rule: what,
				Reason: fmt.Sprintf("a FROM in a %s comes directly after the verb -- `%s FROM <ref>` is the stage-header form of the converter (§4.1), and it starts a stage of its own -- and here it is an argument among others, which is not a form the language has", in, in),
			})
			tail = tail[:i]
			break
		}
	}

	parsed, positional, bad := parseOptions(in, tail, table, what)
	out = append(out, bad...)

	if in.Verb == "PACK" && len(positional) > 0 {
		out = append(out, v("§5.3", fmt.Sprintf("VM PACK takes no positional arguments, and was given %q. What it packs is the stage above it, or the ref of a VM PACK FROM; the options it does take are --variant= and --disk-extra-mb=, and a name for what it produces is written AS <name>", strings.Join(positional, " "))))
	}

	if in.Verb == "WRAP" {
		out = append(out, wrapNameViolations(s, in, parsed, positional)...)
	}
	if hasDanglingAS(in) {
		out = append(out, v("§4.1", "AS names the VM stage this converter produces, and this one is followed by nothing. Give it a name, or drop the AS"))
	}
	return out
}

// wrapsScratch reports whether what a VM WRAP would wrap is an image with
// no filesystem (K6), and says which reference said so.
//
// One hop of resolution is worth doing: `VM WRAP FROM empty` where
// `empty` is an earlier `FROM scratch` stage is the same mistake written
// one line further away, and a rule that only read the literal word
// would miss it.
func wrapsScratch(f *File, s *Stage) (string, bool) {
	ref := s.Ref
	if strings.EqualFold(ref, "scratch") {
		return "an image built FROM scratch", true
	}
	if source, _, ok := f.Stage(ref); ok && source.Index < s.Index && strings.EqualFold(source.Ref, "scratch") {
		return fmt.Sprintf("%s, which is built FROM scratch on line %d,", source.Label(), source.Header.Line), true
	}
	return "", false
}

// converterTail is a converter's arguments with the parts that are not
// options taken off: the ref of a header form, which §4.1 puts directly
// after the FROM, and the AS name either form may end with.
func converterTail(in Instruction) []string {
	args := fields(in.Args)
	if in.From && len(args) > 0 {
		args = args[1:]
	}
	switch n := len(args); {
	case n >= 2 && strings.EqualFold(args[n-2], "AS"):
		args = args[:n-2]
	case n >= 1 && strings.EqualFold(args[n-1], "AS"):
		// An AS with nothing after it, which hasDanglingAS reports on its
		// own: it is not an option and it is not a service name.
		args = args[:n-1]
	}
	return args
}

// snapshotViolations decides a VM SNAPSHOT: where it is (K5), whether
// this implementation takes one (§2.3) and that it takes no arguments
// (§5.6).
func snapshotViolations(s *Stage, in Instruction, opts Options) []Violation {
	v := func(rule, reason string) Violation {
		return Violation{Line: in.Line, Instruction: in.String(), Rule: rule, Reason: reason}
	}
	var out []Violation

	if !becomesVM(s) {
		out = append(out, v("K5", "this stage never becomes a machine, so there is no running VM to snapshot: what this stage produces is a container image, and a container image has no memory or device state to save. Convert the stage (VM WRAP or VM PACK) if it was meant to be a machine, or move the VM SNAPSHOT into the stage that is one"))
	}
	if opts.Level < Level3 {
		out = append(out, unimplemented(in, Level3, "booting the stage's machine, pausing it and saving its memory and device state into the image"))
	}
	if args := fields(in.Args); len(args) > 0 {
		out = append(out, v("§5.6", fmt.Sprintf("VM SNAPSHOT takes no arguments, and was given %q. It says that this stage's machine is snapshotted on its last instruction and nothing else; the machine's own defaults are VM SET's, and `VM SET snapshot=on` is this instruction written as one", strings.Join(args, " "))))
	}
	return out
}

// becomesVM reports whether the stage is a machine by the end of it,
// which is what K5 asks of a VM SET and a VM SNAPSHOT: they may sit above
// the converter that makes the stage a machine, but not in a stage that
// never becomes one, because nothing would read them.
func becomesVM(s *Stage) bool { return s.Kind() == VM }

// wrapNameViolations decides the service name a VM WRAP runs its
// workload under (§5.2): the positional argument, or --name=, or the
// stage's name, or "app".
func wrapNameViolations(s *Stage, in Instruction, parsed map[string]string, positional []string) []Violation {
	v := func(rule, reason string) Violation {
		return Violation{Line: in.Line, Instruction: in.String(), Rule: rule, Reason: reason}
	}
	var out []Violation

	if len(positional) > 1 {
		out = append(out, v("§5.2", fmt.Sprintf("VM WRAP takes one positional argument -- the service name the wrapped workload runs under -- and was given %d (%s)", len(positional), strings.Join(positional, " "))))
	}
	named, hasNamed := parsed["name"]
	if hasNamed && len(positional) > 0 {
		out = append(out, v("§5.2", fmt.Sprintf("the service name is given twice, as --name=%s and as %q. Give it once: the positional argument and --name= are the same thing", named, positional[0])))
	}

	name := ""
	switch {
	case len(positional) > 0:
		name = positional[0]
	case hasNamed:
		// Already checked by the option table.
	case s.ConvertedName != "":
		name = s.ConvertedName
	case s.Name != "":
		name = s.Name
	}
	if name != "" {
		if err := serviceName(name); err != nil {
			out = append(out, v("§5.2", fmt.Sprintf("the service name %q %s: it names the generated granule-wrap@ unit and the OCI layout directory in the guest (§9.2), so it has to be a name both of those can carry -- give one with the positional argument or --name=", name, err)))
		}
	}
	return out
}

// setViolations decides a VM SET: where it is (K5), whether this
// implementation records one (§2.3) and its settings (§5.5).
func setViolations(s *Stage, in Instruction, opts Options) []Violation {
	v := func(rule, reason string) Violation {
		return Violation{Line: in.Line, Instruction: in.String(), Rule: rule, Reason: reason}
	}
	var out []Violation

	if !becomesVM(s) {
		out = append(out, v("K5", "this stage never becomes a machine, so nothing would ever read these settings: they are the defaults of a VM, and what this stage produces is a container image. Convert the stage (VM WRAP or VM PACK) if it was meant to be a machine, or move the VM SET into the stage that is one"))
	}
	if opts.Level < Level2 {
		out = append(out, unimplemented(in, Level2, "recording a machine's defaults"))
	}

	args := fields(in.Args)
	if len(args) == 0 {
		return append(out, v("§5.5", "VM SET takes at least one key=value setting. The keys are "+strings.Join(settingKeys(), ", ")))
	}

	seen := map[string]bool{}
	for _, arg := range args {
		key, value, ok := strings.Cut(arg, "=")
		if !ok {
			out = append(out, v("§5.5", fmt.Sprintf("%q is not a setting: VM SET takes key=value pairs, and the keys are %s", arg, strings.Join(settingKeys(), ", "))))
			continue
		}
		key = strings.ToLower(key)
		def, known := settings[key]
		if !known {
			out = append(out, v("§5.5", fmt.Sprintf("%q is not a setting granule records. The keys are a closed set -- %s -- so an unknown one is refused rather than recorded and ignored", key, strings.Join(settingKeys(), ", "))))
			continue
		}
		if _, dup := seen[key]; dup {
			out = append(out, v("§5.5", fmt.Sprintf("%s is set twice on this line. A later VM SET of the same key overrides an earlier one, but giving it twice in one instruction says nothing about which was meant", key)))
			continue
		}
		seen[key] = true
		if err := def.check(value); err != nil {
			out = append(out, v("§5.5", fmt.Sprintf("%s=%s %s: it wants %s. Settings are validated here rather than at boot, where a bad one would be a machine that does not come up (%s is what this records)", key, value, err, def.syntax, def.env)))
			continue
		}
		// snapshot=on is VM SNAPSHOT written as a setting (§5.6), so it
		// costs what VM SNAPSHOT costs: a booted guest, which is level 3
		// rather than the level 2 the rest of this table is.
		if key == "snapshot" && on(value) && opts.Level < Level3 {
			out = append(out, unimplemented(in, Level3, "snapshotting the stage's running machine -- snapshot=on is VM SNAPSHOT written as a setting (§5.6), and taking one boots the guest"))
		}
	}
	return out
}

func settingKeys() []string {
	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// parseOptions reads the Dockerfile-style options of a VM instruction:
// --name=value, before any positional argument (§5). args is the part of
// the line the options are in, which is not always the whole of it.
func parseOptions(in Instruction, args []string, table map[string]func(string) error, section string) (map[string]string, []string, []Violation) {
	v := func(reason string) Violation {
		return Violation{Line: in.Line, Instruction: in.String(), Rule: section, Reason: reason}
	}

	parsed := map[string]string{}
	var positional []string
	var out []Violation

	for _, arg := range args {
		if !strings.HasPrefix(arg, "--") {
			positional = append(positional, arg)
			continue
		}
		if len(positional) > 0 {
			out = append(out, v(fmt.Sprintf("%s comes after a positional argument. Options are written in Dockerfile's own flag style, --name=value and before the positional arguments, the way COPY --from= and RUN --mount= are", arg)))
		}

		name, value, ok := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		name = strings.ToLower(name)
		if !ok {
			out = append(out, v(fmt.Sprintf("--%s takes a value, written --%s=value. granule refuses an option it cannot read rather than guessing at what was meant", name, name)))
			continue
		}
		check, known := table[name]
		if !known {
			out = append(out, v(fmt.Sprintf("--%s is not an option of %s. The options are %s, and an unknown one is refused rather than ignored: a misspelt option that built anyway is a machine missing the thing the file asked for", name, in, optionList(table))))
			continue
		}
		if _, dup := parsed[name]; dup {
			out = append(out, v(fmt.Sprintf("--%s is given twice, and this says nothing about which value was meant", name)))
			continue
		}
		parsed[name] = value
		if err := check(value); err != nil {
			out = append(out, v(fmt.Sprintf("--%s=%s %s", name, value, err)))
		}
	}
	return parsed, positional, out
}

func optionList(table map[string]func(string) error) string {
	names := make([]string, 0, len(table))
	for k := range table {
		names = append(names, "--"+k+"=")
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// The value checks. Each returns a phrase that reads after the value it
// was given: "--restart=sometimes is not one of always, on-failure, no".

func oneOf(allowed ...string) func(string) error {
	return func(value string) error {
		for _, a := range allowed {
			if value == a {
				return nil
			}
		}
		return fmt.Errorf("is not one of %s", strings.Join(allowed, ", "))
	}
}

func positiveInt(value string) error {
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return fmt.Errorf("is not a positive whole number")
	}
	return nil
}

func wholeInt(value string) error {
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 {
		return fmt.Errorf("is not a whole number of MiB")
	}
	return nil
}

func nonEmpty(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("is empty")
	}
	return nil
}

func duration(value string) error {
	if _, err := time.ParseDuration(value); err != nil {
		return fmt.Errorf("is not a duration")
	}
	return nil
}

// onOff takes the specification's own spelling and the spellings the
// runtime variable behind it already parses, so that a file written
// either way means the same thing to the machine it boots.
func onOff(value string) error {
	switch strings.ToLower(value) {
	case "on", "off", "true", "false", "1", "0":
		return nil
	}
	return fmt.Errorf("is not a boolean")
}

// on reads an already-validated on/off value.
func on(value string) bool {
	switch strings.ToLower(value) {
	case "on", "true", "1":
		return true
	}
	return false
}

// serviceNamePattern is what a wrapped workload's service name has to
// match (§5.2): it names a unit and a directory in the guest.
var serviceNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)

func serviceName(value string) error {
	if !serviceNamePattern.MatchString(value) {
		return fmt.Errorf("does not match %s", serviceNamePattern)
	}
	return nil
}
