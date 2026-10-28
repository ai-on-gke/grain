package granulefile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"strconv"
	"strings"

	"github.com/bwsalmon/granule/pkg/dockercli"
	"github.com/bwsalmon/granule/pkg/guestboot"
	"github.com/bwsalmon/granule/pkg/guestbuild"
)

// BootPhase is the part of a VM stage that only a booted guest can do
// (§6, §5.6): the stage's `RUN`s, which execute inside the machine, and
// the snapshot `VM SNAPSHOT` takes of it once they have.
//
// It is why a VM stage with a `RUN` in it cannot be one Dockerfile
// handed to docker: a `docker build` cannot reach /dev/kvm, so the
// build packs a machine first, boots it, runs these, and commits the
// running guest as the stage's result. PlanBuild produces the plan;
// Build is what boots anything.
type BootPhase struct {
	// Stage is the stage's label, and Path the file it came out of,
	// for the lines this phase prints and the errors it returns: a
	// step's output is headed by the file and line of the instruction
	// producing it, the way a container build's is (§10.2, §10.4).
	Stage string
	Path  string

	// Base is what the stage started from -- the machine a `VM FROM`
	// inherited, or the image a `VM PACK` packed -- recorded on the
	// result as its provenance. The image this phase actually boots is
	// an intermediate the build made and deletes on the way out, and a
	// label pointing at that would name a tag nobody can pull.
	Base string

	// Steps are the stage's instructions, in the order the file wrote
	// them.
	Steps []BootStep

	// Snapshot is VM SNAPSHOT, or the `VM SET snapshot=on` that §5.6
	// says is the same thing: snapshot the guest after the last step
	// and package the snapshot into the image, so that running the
	// result resumes instead of cold-booting.
	Snapshot bool

	// MemoryMB and MemoryMaxMB are the stage's `VM SET memory-mb` and
	// `memory-max-mb`, if it set them, so that the guest this boots is
	// the size the file says the machine is.
	//
	// It matters most for a snapshot. A restore replays the snapshot's
	// own memory size rather than the image's (see
	// pkg/hypervisor.BuildArgs, which passes cloud-hypervisor nothing
	// but `--restore`), so the size the build boots at is the size the
	// machine has forever -- and a build that ignored the setting would
	// record a `CHV_MEMORY_MB` the machine could never honour, which is
	// an instruction dropped rather than built (§10.1).
	MemoryMB    int
	MemoryMaxMB int
}

// BootStep is one instruction executed inside the booted guest.
type BootStep struct {
	// Instruction and Line are the instruction as the file wrote it and
	// the line it is on, so that a step's output and a step's failure
	// name the line a reader has to go and look at (§10.2).
	Instruction string
	Line        int

	// Argv is the command. A shell-form `RUN` has the stage's `SHELL`
	// (default `/bin/sh -c`) in front of it; an exec-form one is the
	// array the file wrote.
	Argv []string

	// User, Dir and Env are the stage's `USER`, `WORKDIR`, `ARG` and
	// `ENV` as they stood at this instruction -- build-time state that
	// applies to the `RUN`s below it and to nothing else (§6). None of
	// them persists into the booted machine.
	User string
	Dir  string
	Env  []string
}

// vmStageParts is one VM stage's instructions, sorted into what each
// half of the build does with them.
type vmStageParts struct {
	// offline are the instructions applied to the guest root filesystem
	// without booting anything: the copies §6 says need no boot, and
	// the WORKDIR that creates a directory in it and decides what a
	// relative copy destination means.
	offline []Instruction

	// labels are recorded on the image the stage produces.
	labels []Instruction

	// settings are the VM SETs, as the runtime environment variables
	// SettingEnv names.
	settings    map[string]string
	extraDiskMB int

	// volumes are the stage's VOLUME mount points, recorded in the
	// produced image's metadata (§6).
	volumes []string

	// boot is nil unless the stage needs a guest booted during the
	// build.
	boot *BootPhase
}

// planVMStage sorts a VM stage's instructions into vmStageParts, and
// refuses every instruction it has nowhere to put.
//
// The refusal is the point of it. §6 and §10.1 are explicit: an
// instruction an implementation does not build MUST be refused rather
// than dropped, because a dropped instruction is a machine that comes up
// without the configuration its file specified, discovered in production
// rather than at build time.
//
// canBoot is whether this stage may have a boot phase at all, which
// today means whether it is the stage being built. A `RUN` in an earlier
// VM stage is refused rather than dropped for the same reason: see
// bootInLaterStage.
//
// vars is the build's own scope (see buildVars) and overriddenArgs the
// names the caller's build arguments supplied; both are read here and
// neither is written, a stage's ARGs being the stage's (§6).
func planVMStage(f *File, stage *Stage, instrs []Instruction, vars map[string]string, overriddenArgs map[string]bool, canBoot bool) (vmStageParts, error) {
	parts := vmStageParts{settings: make(map[string]string)}
	boot := &BootPhase{Stage: stage.Label(), Path: f.Path}
	var refused []Violation

	// The state a VM stage's RUNs run with (§6). All of it is
	// build-time only: it decides what the instructions below it do
	// and is not written into the machine.
	//
	// ENV is deliberately not emitted into the container stage the
	// copies go to: §6 says a VM stage's variables do not persist into
	// the booted machine, and an ENV in a docker stage is image config
	// that would.
	var user, workdir string
	var env, argEnv []string
	shell := []string{"/bin/sh", "-c"}

	// This stage's expansion scope (§3.5): the file's globals, with the
	// stage's own ARGs written over them. It is a copy, because those
	// ARGs are in scope for the rest of *this* stage and writing them
	// into the build's map would put them in scope for every stage
	// after it as well.
	local := make(map[string]string, len(vars))
	maps.Copy(local, vars)

	refuse := func(in Instruction) { refused = append(refused, unbuiltInVMStage(in)) }

	for _, in := range instrs {
		if in.IsConverter() {
			continue
		}
		switch {
		case strings.EqualFold(in.Name, "COPY"), strings.EqualFold(in.Name, "ADD"):
			parts.offline = append(parts.offline, in)

		case strings.EqualFold(in.Name, "LABEL"):
			parts.labels = append(parts.labels, in)

		case strings.EqualFold(in.Name, "ARG"):
			// §6: as in a container stage -- a build-time variable,
			// with a default, in scope for the rest of the stage. It
			// needs no boot: it decides what the instructions below it
			// expand to and what environment the RUNs below them carry,
			// and like the rest of this state it is not written into the
			// machine.
			for _, a := range fields(expand(in.Args, local)) {
				name, def, hasDefault := strings.Cut(a, "=")
				switch {
				case overriddenArgs[name]:
					// A `-build-arg` the caller gave wins over the
					// file's default, here as over a global's (§3.5),
					// and their value is in the scope already.
				case hasDefault:
					local[name] = unquote(def)
				default:
					// `ARG NAME` with no default declares it and
					// supplies nothing, so it keeps whatever a global
					// ARG of that name holds -- which is how docker
					// brings a global into a stage -- and is worth
					// nothing when there is none.
				}
				if v := local[name]; v != "" {
					argEnv = mergeEnv(argEnv, []string{name + "=" + v})
				}
			}
			// The declaration also has to reach the container stage the
			// copies go to, since docker expands a COPY there for
			// itself: the same both-halves treatment WORKDIR gets below,
			// and unlike ENV it records nothing on the image -- an ARG
			// is build-time to docker too.
			parts.offline = append(parts.offline, in)

		case strings.EqualFold(in.Name, "VOLUME"):
			parts.volumes = append(parts.volumes, parseVolumeArgs(expand(in.Args, local))...)

		case strings.EqualFold(in.Name, "WORKDIR"):
			// Both halves of §6's WORKDIR, and both offline: the
			// instruction goes into the container stage that becomes
			// the guest root filesystem, where it creates the directory
			// and becomes the target a relative COPY destination below
			// it resolves against, and the same path is where the RUNs
			// below it run.
			workdir = strings.TrimSpace(expand(in.Args, local))
			parts.offline = append(parts.offline, in)

		case strings.EqualFold(in.Name, "USER"):
			user = strings.TrimSpace(expand(in.Args, local))

		case strings.EqualFold(in.Name, "ENV"):
			env = mergeEnv(env, parseEnvArgs(expand(in.Args, local)))

		case strings.EqualFold(in.Name, "SHELL"):
			parsed, ok := parseShellArray(in.Args)
			if !ok {
				return vmStageParts{}, &Error{Path: f.Path, Violations: []Violation{{
					Line:        in.Line,
					Instruction: in.String(),
					Rule:        "§6",
					Reason:      `SHELL takes a JSON array -- SHELL ["/bin/bash", "-o", "pipefail", "-c"] -- and this one is not one, so there is nothing to hand a shell-form RUN to`,
				}}}
			}
			shell = parsed

		case strings.EqualFold(in.Name, "RUN"):
			if !canBoot {
				refused = append(refused, bootInLaterStage(stage, in))
				continue
			}
			boot.Steps = append(boot.Steps, BootStep{
				Instruction: formatInstruction(in),
				Line:        in.Line,
				Argv:        runArgv(in.Args, shell),
				User:        user,
				Dir:         workdir,
				// The stage's ARGs are in the environment its RUNs get,
				// as they are in a container build -- and an ENV of the
				// same name overrides one wherever it was written, which
				// is docker's rule for the pair.
				Env: mergeEnv(argEnv, env),
			})

		case in.IsVM() && in.Verb == "SNAPSHOT":
			if !canBoot {
				refused = append(refused, bootInLaterStage(stage, in))
				continue
			}
			boot.Snapshot = true

		case in.IsVM() && in.Verb == "SET":
			for _, arg := range fields(expand(in.Args, local)) {
				k, v, ok := strings.Cut(arg, "=")
				if !ok {
					continue
				}
				if k == "disk-extra-mb" || k == "extra-disk-mb" {
					mb, err := strconv.Atoi(v)
					if err != nil {
						return vmStageParts{}, fmt.Errorf("invalid %s value %q: %w", k, v, err)
					}
					parts.extraDiskMB += mb
					continue
				}
				if k == "snapshot" {
					// §5.6: `VM SET snapshot=on` is `VM SNAPSHOT`
					// written as a setting. It is the build that takes
					// the snapshot, and the runtime finds the bundle
					// where the build put it, so this records a boot
					// phase rather than an environment variable -- an
					// image carrying CHV_SNAPSHOT_PATH=on would refuse
					// to boot, that variable being a path.
					if v == "on" {
						if !canBoot {
							refused = append(refused, bootInLaterStage(stage, in))
							continue
						}
						boot.Snapshot = true
					}
					continue
				}
				if envVar, ok := SettingEnv(k); ok {
					parts.settings[envVar] = v
				}
			}

		default:
			refuse(in)
		}
	}

	if len(refused) > 0 {
		return vmStageParts{}, &Error{Path: f.Path, Violations: refused}
	}
	if len(boot.Steps) > 0 || boot.Snapshot {
		// The machine's own size, for the guest this is about to boot:
		// the settings are read above into what the image records, and
		// a boot that used a different figure would be running the
		// stage's instructions in a machine the file did not describe.
		boot.MemoryMB = settingInt(parts.settings, "CHV_MEMORY_MB")
		boot.MemoryMaxMB = settingInt(parts.settings, "CHV_MEMORY_MAX_MB")
		parts.boot = boot
	}
	return parts, nil
}

// settingInt reads one recorded VM SET back as a number, or 0 when the
// stage did not set it. The value has already been validated (§5.5), so
// anything unreadable here means "not set" rather than an error to
// report a second time.
func settingInt(settings map[string]string, key string) int {
	n, err := strconv.Atoi(settings[key])
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// runArgv is what a VM-stage RUN executes inside the guest: the array an
// exec-form RUN wrote, or the stage's SHELL with the shell-form
// command string as its last argument.
//
// The command string travels to the guest unexpanded, and is expanded
// there by the shell running it. That is where a `RUN echo $HOME` gets
// the guest's own HOME rather than the empty string this build would
// substitute, the build's variables being a different scope from the
// booted machine's.
func runArgv(args string, shell []string) []string {
	trimmed := strings.TrimSpace(args)
	if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
		var arr []string
		if err := json.Unmarshal([]byte(trimmed), &arr); err == nil && len(arr) > 0 {
			return arr
		}
	}
	return append(append([]string{}, shell...), trimmed)
}

// parseShellArray reads SHELL's JSON array, which is the only form it
// has: unlike ENTRYPOINT and CMD, SHELL has no shell form, since a shell
// form would have to be interpreted by the very thing it is setting.
func parseShellArray(args string) ([]string, bool) {
	var arr []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(args)), &arr); err != nil || len(arr) == 0 {
		return nil, false
	}
	return arr, true
}

// bootInLaterStage is the §10.3 diagnostic for a boot in a stage that is
// not the one being built.
//
// A stage whose guest has to be booted is built in three parts -- pack,
// boot, commit -- and what comes out is an image rather than a stage the
// rest of one Dockerfile can go on referring to. Building a file where
// such a stage is followed by others means carrying that image back into
// a second build, and this tree does not do it yet. Refused rather than
// dropped, as ever: a machine built without its RUNs is the file
// quietly building something else.
func bootInLaterStage(stage *Stage, in Instruction) Violation {
	// The name to build the stage on its own by, which is a name the
	// stage answers to and not its label: a converted stage's machine
	// is the name a -target resolves (§4.4).
	target := fmt.Sprintf("%d", stage.Index)
	if names := stage.Names(); len(names) > 0 {
		target = names[len(names)-1].Name
	}
	return Violation{
		Line:        in.Line,
		Instruction: in.String(),
		Level:       Level3,
		Rule:        "§10.3",
		Reason: fmt.Sprintf("unimplemented: %s boots the guest during the build, and granule boots one only for the stage it is building -- %s is followed by further stages in this build. Build this stage on its own (`-target %s`) and start the later stages from the machine it produces, or move the instruction into the stage being built",
			strings.ToUpper(in.String()), stage.Label(), target),
	}
}

// bootTag is the image the guest phase boots: the machine the docker
// build produces before anything has run inside it.
//
// It is deleted on the way out, and named after the output tag so that
// one left behind by a build that died says which build left it.
func bootTag(tag string) string {
	// The tag is what follows the last colon, and only if that colon is
	// after the last slash: "localhost:5000/vm" is a registry with a
	// port on it and no tag at all, and cutting at the first colon
	// would have named an image on no registry.
	name := tag
	if i := strings.LastIndexByte(tag, ':'); i > strings.LastIndexByte(tag, '/') {
		name = tag[:i]
	}
	return name + ":granule-preboot"
}

// Describe is the guest phase written out for a reader, for the tail of
// what `granule granulefile build -print` prints.
//
// Without it that command answers a file with a `RUN` in a VM stage
// with a Dockerfile that packs a machine and runs nothing -- true as far
// as it goes, and exactly the half that makes the other half look like
// it was dropped.
func (b *BootPhase) Describe() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Then, inside the machine the build above produces (§6), before it\n")
	fmt.Fprintf(&sb, "# is committed as this build's output:\n")
	for _, s := range b.Steps {
		fmt.Fprintf(&sb, "#   %s:%d: %s\n", b.Path, s.Line, s.Instruction)
		fmt.Fprintf(&sb, "#     %s\n", strings.Join(s.Argv, " "))
		if s.User != "" {
			fmt.Fprintf(&sb, "#     as %s\n", s.User)
		}
		if s.Dir != "" {
			fmt.Fprintf(&sb, "#     in %s\n", s.Dir)
		}
		for _, e := range s.Env {
			fmt.Fprintf(&sb, "#     with %s\n", e)
		}
	}
	if b.Snapshot {
		fmt.Fprintf(&sb, "# and the guest is snapshotted into the image, so that running it\n")
		fmt.Fprintf(&sb, "# resumes rather than boots (§5.6).\n")
	}
	return sb.String()
}

// runBootPhase boots the machine the build has just packed, runs the
// stage's instructions inside it, and commits the result as the build's
// output.
//
// This is `granule guest build -setup` per instruction: the same boot,
// the same scrub of per-boot identity, the same commit -- which is what
// the refusal this replaces pointed at. The steps differ from a setup
// script only in being separate commands, so that each carries the
// account, directory and environment its own instruction asked for and a
// failure names the line.
func runBootPhase(ctx context.Context, phase *BootPhase, from string, opts BuildOptions) error {
	steps := make([]guestbuild.Step, 0, len(phase.Steps))
	for _, s := range phase.Steps {
		steps = append(steps, guestbuild.Step{
			Description: fmt.Sprintf("%s:%d: %s", phase.Path, s.Line, s.Instruction),
			Command: guestboot.Command{
				Argv: s.Argv,
				User: s.User,
				Dir:  s.Dir,
				Env:  s.Env,
			},
		})
	}

	fmt.Fprintf(opts.Stdout, "booting %s to run %s inside it\n", from, plural(len(phase.Steps), "instruction"))
	err := guestbuild.Build(ctx, guestbuild.Options{
		From:  from,
		Tag:   opts.Tag,
		Steps: steps,
		// The image booted here is the intermediate this build made and
		// is about to delete, so a provenance label pointing at it
		// would name a tag that no longer exists. What the machine
		// actually came from is the stage's own base.
		BaseLabel:    phase.Base,
		Snapshot:     phase.Snapshot,
		MemoryMB:     phase.MemoryMB,
		MemoryMaxMB:  phase.MemoryMaxMB,
		DockerBinary: opts.DockerBinary,
		Stdout:       opts.Stdout,
		Stderr:       opts.Stderr,
	})
	if err != nil {
		return fmt.Errorf("%s: %w", phase.Stage, err)
	}
	return nil
}

// removeImage drops an intermediate image, saying nothing when it
// cannot: it is cleanup, and a build that worked should not fail over
// it.
func removeImage(ctx context.Context, opts BuildOptions, tag string) {
	d := &dockercli.CLI{Binary: opts.DockerBinary}
	_ = d.Run(ctx, io.Discard, io.Discard, "image", "rm", "-f", tag)
}

func plural(n int, what string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", what)
	}
	return fmt.Sprintf("%d %ss", n, what)
}
