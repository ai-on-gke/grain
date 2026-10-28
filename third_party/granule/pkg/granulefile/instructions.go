package granulefile

import (
	"fmt"
	"regexp"
	"strings"
)

// Rules is the instruction set one stage may use. Which one applies is
// decided by the stage's kind at the line, and -- above a converter --
// by which converter ends the stage (§7), which is why a whole stage has
// to be parsed before any of it is validated.
type Rules int

const (
	// RulesContainer is an ordinary container stage: a Dockerfile, with
	// the full instruction set. A stage that is never converted, and the
	// part of a stage above a VM WRAP, are both this: what they build is
	// a container image, healthcheck and all.
	RulesContainer Rules = iota

	// RulesAbovePack is a container stage that ends in VM PACK (§7).
	// What it builds is a root filesystem, so the few instructions that
	// describe a *container being run by a container runtime* are refused
	// with §6's reasons: they would be recorded in an image config that
	// nothing reads, since what is built from this stage is a disk. What
	// the machine itself has a use for -- its workload, its mount points,
	// its shell -- is not among them.
	RulesAbovePack

	// RulesVMStage is a machine under construction (§6): from an
	// in-stage converter, or from a VM FROM / VM PACK FROM / VM WRAP
	// FROM header, to the end of the stage.
	RulesVMStage
)

// Supported lists what a VM stage may use (§6), in the order the
// specification documents them. They mean exactly what they mean to
// docker: this is a subset of Dockerfile, not a dialect of it.
var Supported = []string{"RUN", "COPY", "ADD", "ENV", "WORKDIR", "USER", "SHELL", "VOLUME", "ENTRYPOINT", "CMD", "ARG", "LABEL", "VM SET", "VM SNAPSHOT"}

// dockerfileInstructions is every instruction the Dockerfile format has.
// It is what separates "forbidden" from "not a thing at all" (§10.3): an
// instruction in here that a stage may not use is refused with a reason,
// and a name that is not in here is not an instruction anybody has.
var dockerfileInstructions = map[string]bool{
	"ADD": true, "ARG": true, "CMD": true, "COPY": true, "ENTRYPOINT": true,
	"ENV": true, "EXPOSE": true, "FROM": true, "HEALTHCHECK": true,
	"LABEL": true, "MAINTAINER": true, "ONBUILD": true, "RUN": true,
	"SHELL": true, "STOPSIGNAL": true, "USER": true, "VOLUME": true,
	"WORKDIR": true,
}

// vmStageAllows is §6's table: what a VM stage may use. VM SET and VM
// SNAPSHOT are allowed too, and are handled with the rest of the VM
// instruction.
//
// SHELL, VOLUME, ENTRYPOINT and CMD are in here because §6 gives each of
// them a meaning a machine has: SHELL is the interpreter a shell-form RUN
// is handed to inside the guest, VOLUME declares an in-guest mount point
// recorded in the image's metadata, and ENTRYPOINT and CMD are the
// workload the guest boots into -- the way to say "run this" without
// writing a unit file for it. None of the four is honoured by anything in
// this tree yet, which is a level rather than a rule (§2.3) and is
// reported as one.
var vmStageAllows = map[string]bool{
	"RUN": true, "COPY": true, "ADD": true, "ENV": true, "WORKDIR": true,
	"USER": true, "SHELL": true, "VOLUME": true, "ENTRYPOINT": true,
	"CMD": true, "ARG": true, "LABEL": true,
}

// containerRuntimeInstructions is §7's list: the instructions from §6's
// refusal table that describe a container's runtime, which a stage ending
// in VM PACK MUST be refused for the same reasons.
//
// It is what is left of that table once the instructions a machine has a
// use for are taken out of it: VOLUME is a mount point the packed machine
// keeps, ENTRYPOINT and CMD are the workload it boots into, and MAINTAINER
// is a label rather than a runtime. What remains describes a container
// being run by a container runtime, which is not what a packed disk is.
var containerRuntimeInstructions = []string{
	"EXPOSE", "HEALTHCHECK", "STOPSIGNAL", "ONBUILD",
}

// Refuse decides one non-VM instruction against a stage's rules, and is
// the single place the language's refusal reasons are written down.
//
// A VM instruction is not decided here: its verb decides what it is, and
// the rules it is checked against (K1-K6, §5) are the file's structure
// rather than one stage's vocabulary. Callers validate those separately
// and this returns false for them.
func Refuse(in Instruction, rules Rules) (Violation, bool) {
	if in.IsVM() {
		return Violation{}, false
	}

	v := Violation{Line: in.Line, Instruction: in.String()}

	if !dockerfileInstructions[in.Name] {
		v.Reason = fmt.Sprintf("%s is not an instruction: the Dockerfile format has no such name, so the container builder this is handed to would refuse the line as well. If it is an instruction a builder you use does have, that is a gap in granule rather than a rule of the language -- report it rather than working around it", in.Name)
		return v, true
	}

	if refused, reason := refusal(in.Name, rules); refused {
		v.Reason = reason
		v.Rule = citation(rules)
		return v, true
	}

	// ADD of a local path is COPY with tar extraction, and is a thing
	// people write on purpose; ADD of a remote source is a fetch, and is
	// refused wherever granule is the one doing the fetching.
	if in.Name == "ADD" && rules != RulesContainer && rules != RulesAbovePack {
		if remote := remoteSources(in.Args); len(remote) > 0 {
			v.Reason = fmt.Sprintf("granule refuses ADD of a remote source (%s): it fetches at build time from a host outside the build context, with the network of whatever machine ran the build and nothing recording what came back. COPY it out of the build context, or fetch it in a RUN where the checksum is yours to check. ADD of a local path is supported, and is COPY with tar extraction", strings.Join(remote, ", "))
			v.Rule = citation(rules)
			return v, true
		}
	}

	return Violation{}, false
}

// refusal answers whether one instruction is refused under one ruleset,
// and why.
func refusal(name string, rules Rules) (bool, string) {
	switch rules {
	case RulesContainer:
		// A Dockerfile stage is a Dockerfile stage. Nothing granule adds
		// applies to it, because nothing it builds is a machine.
		return false, ""

	case RulesVMStage:
		if vmStageAllows[name] {
			return false, ""
		}
		return true, vmStageReason(name)

	case RulesAbovePack:
		for _, refused := range containerRuntimeInstructions {
			if name == refused {
				return true, vmStageReason(name) + packSuffix
			}
		}
		return false, ""
	}
	return false, ""
}

// packSuffix is why §7 refuses a container-runtime instruction in a
// stage that is still a container build: the reason is §6's, and the
// stage's own result is what makes it apply here too.
const packSuffix = ". This stage is still a container build, but what it builds is the machine's root filesystem: the declaration would be recorded in an image config that nothing reads, because what comes out of the stage is a disk"

// vmStageReason is §6's refusal table, in one place. Each reason says
// what granule does instead, or what to write instead: a refusal that
// only says "unsupported" leaves the reader to guess whether they have
// hit a policy or a gap (§10.2).
func vmStageReason(name string) string {
	switch name {
	case "EXPOSE":
		return "port metadata belongs to the container that publishes the ports, which is the granule image around this guest and not this guest. A guest's own listening ports are reached through that container's network (see netshim in the top-level README), and nothing consults an EXPOSE recorded in here"

	case "HEALTHCHECK":
		return "docker health checks probe a container's main process, and this guest is not run as one. Health of a guest is a systemd unit's business inside it, or `granule vm status`/`vm wait` from outside"

	case "STOPSIGNAL":
		return "a granule guest is stopped by an ACPI power button press, which acpid turns into an ordinary shutdown (see deploy/guest-image/README.md's \"Graceful shutdown\"). There is no process in here to signal, so this would change nothing about how the machine goes down"

	case "ONBUILD":
		return "ONBUILD defers instructions to whoever builds FROM the result, where none of them is validated against these rules and where what is built is a disk rather than something to build on"

	case "MAINTAINER":
		return "docker deprecated MAINTAINER long ago and granule does not carry it forward. `LABEL org.opencontainers.image.authors=\"...\"` is the supported spelling and lands in the image's metadata"

	case "FROM":
		return "FROM starts a stage, so it cannot appear inside one: everything from here to the next stage header would be a new stage's instructions rather than this one's. The headers are FROM, VM FROM, VM UNPACK FROM, VM PACK FROM and VM WRAP FROM"

	default:
		return name + " has no meaning for a machine under construction, and granule refuses it rather than recording something nothing will read. What a VM stage may use is " + strings.Join(Supported, ", ")
	}
}

// citation is the rule a refusal is made under, for the diagnostic to
// cite (§4.5).
func citation(rules Rules) string {
	switch rules {
	case RulesVMStage:
		return "K9, §6"
	case RulesAbovePack:
		return "K9, §7"
	default:
		return ""
	}
}

// remoteSources returns the sources of an ADD that are not local paths.
//
// Everything but the last field is a source, and a field beginning with
// "--" is one of ADD's own flags (--chown, --chmod, --checksum, --link,
// --keep-git-dir, --exclude) rather than either.
func remoteSources(args string) []string {
	fields := strings.Fields(args)
	var sources []string
	for i, f := range fields {
		if strings.HasPrefix(f, "--") {
			continue
		}
		if i == len(fields)-1 {
			break
		}
		sources = append(sources, f)
	}

	var remote []string
	for _, s := range sources {
		if remoteSource.MatchString(s) {
			remote = append(remote, s)
		}
	}
	return remote
}

// remoteSource matches the source forms ADD fetches over the network: a
// URL, and git's scp-style remote syntax.
var remoteSource = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.-]*://|[A-Za-z0-9_.-]+@[A-Za-z0-9_.-]+:)`)
