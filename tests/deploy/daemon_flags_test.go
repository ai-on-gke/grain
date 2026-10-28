// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package deploy

// The agreement this file holds is the one whose absence took the
// deployment down on 2026-09-18 (grain/task-233): every flag the
// installer writes into grain-daemon.service has to be a flag `grain
// daemon` defines.
//
// It is a real failure mode rather than a theoretical one because the
// two sides move separately. scripts/setup.sh writes the unit once, at
// install time; every upgrade after that rewrites only the image ref
// file the unit interpolates (pkg/upgrade/image.go), so the flags are
// frozen at the installer of the day while the binary behind them keeps
// moving. Removing a flag from pkg/cli/daemon.go and from setup.sh in
// the same commit -- which is exactly what grain/task-193 did to
// -root-shell-control-dir -- therefore still leaves every already
// installed host passing it, and before this change that was a daemon
// that exited 2 on every start, 52 times in eight minutes.
//
// Two things now stand between that and an outage. The daemon ignores a
// flag it does not define and says so (pkg/cli/daemon_unknown_flags.go),
// and this test refuses a *newly written* unit that would need that
// tolerance -- because tolerance means the setting is silently not in
// force, which is the right answer for a host mid-upgrade and the wrong
// one for a host installed today.
//
// Text against text, like the rest of this package: the unit is the
// shell that writes it and the flag set is the Go that defines it, so
// neither side has to be built or run to be compared.

import (
	"regexp"
	"strings"
	"testing"
)

// valueFlagDef matches every `grain daemon` flag that takes an argument
// of its own -- fs.String, fs.Int, fs.Duration and the rest -- with the
// flag's name as its only capture.
var valueFlagDef = regexp.MustCompile(`fs\.(?:String|Int64|Int|Uint64|Uint|Float64|Duration)\("([a-z0-9-]+)"`)

// boolFlagDef is the same for the flags that take none, which is the
// distinction a command line cannot be read without: `-ci false` is a
// flag and a stray argument, while `-poll-interval 30s` is one flag.
var boolFlagDef = regexp.MustCompile(`fs\.Bool\("([a-z0-9-]+)"`)

// varFlagDef is fs.Var, whose name is the *second* argument -- the
// repeatable flags (-granule-create-arg, -reboot-cmd) and their
// stringSliceFlag. None of them is a boolean.
var varFlagDef = regexp.MustCompile(`fs\.Var\(&[A-Za-z0-9_]+, "([a-z0-9-]+)"`)

// daemonFlagFiles are the files beside daemon.go that register a flag on
// its FlagSet: sandboxnet.go's -k8s-sandbox-service.
var daemonFlagFiles = []string{"sandboxnet.go", "ghwebhook_flags.go"}

// daemonFlags is every flag pkg/cli/daemon.go defines, mapped to
// whether it takes a value -- with the ones registered for it from
// daemonFlagFiles, which daemon.go's own size limit (cmd/grain's
// TestCmdGrainFilesStaySmall) pushes some of them out into.
//
// The count floor below is what makes this safe to write as a regexp: a
// rename in daemon.go that this stopped matching would otherwise turn
// every assertion in this file into "the unit passes flags nothing
// defines", or -- far worse -- into an empty set that agrees with
// anything.
func daemonFlags(t *testing.T) map[string]bool {
	t.Helper()
	src := read(t, "pkg", "cli", "daemon.go")
	for _, file := range daemonFlagFiles {
		src += read(t, "pkg", "cli", file)
	}
	flags := map[string]bool{}
	for _, m := range boolFlagDef.FindAllStringSubmatch(src, -1) {
		flags[m[1]] = false
	}
	for _, re := range []*regexp.Regexp{valueFlagDef, varFlagDef} {
		for _, m := range re.FindAllStringSubmatch(src, -1) {
			flags[m[1]] = true
		}
	}
	if len(flags) < 80 {
		t.Fatalf("only %d `grain daemon` flags found in pkg/cli/daemon.go -- this file's regexps no longer "+
			"match how they are defined, so every assertion below is about nothing", len(flags))
	}
	for _, want := range []string{"data-dir", "ci", "granule-create-arg"} {
		if _, ok := flags[want]; !ok {
			t.Fatalf("-%s is not among the flags found, so one of the three definition forms is being missed", want)
		}
	}
	return flags
}

// daemonArgsBlock is one `daemon_args=(...)` or `daemon_args+=(...)` in
// write_systemd_units -- the whole of what the unit's ExecStart passes,
// spread over an initial assignment and a dozen conditional appends.
var daemonArgsBlock = regexp.MustCompile(`daemon_args\+?=\(`)

// unitFlags is every flag scripts/setup.sh writes into
// grain-daemon.service, in the order the ExecStart passes them.
//
// The shell is read the way the daemon reads the line it produces: a
// token starting with "-" is a flag, and a flag that takes a value takes
// the token after it *whatever that token looks like*. That second rule
// is not a nicety -- `-granule-create-arg -disk-mode=overlay` and
// `-reboot-cmd touch -reboot-cmd $CONTROL_DIR/reboot` both pass
// flag-shaped things as values, and a reader that did not know which
// flags take values would report granulectl's own arguments as grain
// flags that do not exist.
func unitFlags(t *testing.T, takesValue map[string]bool) []string {
	t.Helper()
	// Comments stripped for the usual reason (setupCode): this file is
	// more commentary than code, and a flag named in a paragraph about
	// the flag that replaced it is not a flag the unit passes.
	units := body(t, setupCode(t), "write_systemd_units() {")

	var tokens []string
	for _, at := range daemonArgsBlock.FindAllStringIndex(units, -1) {
		depth, i := 1, at[1]
		start := i
		for ; i < len(units) && depth > 0; i++ {
			switch units[i] {
			case '(':
				depth++
			case ')':
				depth--
			}
		}
		if depth != 0 {
			t.Fatalf("a daemon_args block opening at offset %d is never closed", at[0])
		}
		tokens = append(tokens, strings.Fields(units[start:i-1])...)
	}
	if len(tokens) == 0 {
		t.Fatal("no daemon_args block found in write_systemd_units -- this test is reading the wrong function")
	}

	var flags []string
	for i := 0; i < len(tokens); i++ {
		tok := strings.Trim(tokens[i], `"'`)
		if !strings.HasPrefix(tok, "-") {
			continue
		}
		name := strings.TrimPrefix(strings.TrimPrefix(tok, "-"), "-")
		attached := false
		if eq := strings.Index(name, "="); eq >= 0 {
			name, attached = name[:eq], true
		}
		flags = append(flags, name)
		if takesValue[name] && !attached {
			i++ // its value, which is not a flag however it is spelled
		}
	}
	return flags
}

// The assertion that would have caught grain/task-193's removal before
// it shipped, and the cheapest one in this package: parse the unit the
// installer writes, ask the binary which flags it defines, compare.
func TestTheInstallerWritesNoFlagTheDaemonDoesNotDefine(t *testing.T) {
	defined := daemonFlags(t)
	passed := unitFlags(t, defined)
	if len(passed) < 20 {
		t.Fatalf("only %d flags read out of write_systemd_units (%v) -- the unit passes far more than that, "+
			"so this test is no longer reading it", len(passed), passed)
	}
	for _, name := range passed {
		if _, ok := defined[name]; !ok {
			t.Errorf("grain-daemon.service passes -%s and pkg/cli/daemon.go defines no such flag. "+
				"Every host this installer touches would start a daemon that ignores it (and every host "+
				"installed *before* a removal keeps passing it until setup.sh is re-run) -- remove it from "+
				"write_systemd_units, or keep the flag defined and deprecated the way -max-ci-jobs is", name)
		}
	}
}

// The other deployment shape with flags written down beside an image:
// deploy/kubernetes/grain.yaml's own args list, which is a pod spec an
// operator applies once and a rollout repoints (upgrade.ModeKube). The
// same drift, by the same route, with no systemd in it.
func TestTheKubernetesManifestPassesNoFlagTheDaemonDoesNotDefine(t *testing.T) {
	defined := daemonFlags(t)
	manifest := read(t, "deploy", "kubernetes", "grain.yaml")
	args := between(t, manifest, "          args:\n", "          env:")
	found := 0
	for _, line := range strings.Split(args, "\n") {
		line = strings.TrimSpace(line)
		// Commented-out arguments are the manifest's own documentation
		// of what an operator may want to uncomment, and they are one
		// edit away from being real, so they are checked too.
		line = strings.TrimSpace(strings.TrimPrefix(line, "#"))
		arg, ok := strings.CutPrefix(line, "- ")
		if !ok {
			continue
		}
		arg = strings.TrimSpace(arg)
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		name := strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-")
		name, _, _ = strings.Cut(name, "=")
		found++
		if _, ok := defined[name]; !ok {
			t.Errorf("deploy/kubernetes/grain.yaml passes -%s and pkg/cli/daemon.go defines no such flag", name)
		}
	}
	if found < 5 {
		t.Fatalf("only %d flags read out of the manifest's args list -- this test is reading the wrong block", found)
	}
}

// And the same question asked of the *upgrade*, on both shapes that
// have a command line written down beside an image (grain/task-244).
//
// This is the direction the two tests above cannot see: the args are
// fine against the daemon.go in this tree, and the build being rolled
// out is some other commit's. Only the running deployment can ask -- it
// is the one thing that knows both what it was started with and what it
// is about to start -- so both upgrade paths carry the flags this
// process saw into their config and ask the candidate build for its own
// list before cutting over.
func TestBothUpgradePathsCheckTheirFlagsAgainstTheBuildTheyRollOut(t *testing.T) {
	daemon := read(t, "pkg", "daemon", "upgrade.go")
	// Each config literal closes with a `},` at its own indentation; the
	// `[]string{...}` values inside it do not.
	for _, literal := range []string{"Kube: &upgrade.KubeConfig{", "Image: &upgrade.ImageConfig{"} {
		t.Run(literal, func(t *testing.T) {
			cfg := between(t, daemon, literal, "\n\t\t\t},")
			// The flags this very process was started with, which under
			// systemd is the unit's ExecStart and under Kubernetes is the
			// Deployment's args -- neither of which an upgrade rewrites.
			// Matched loosely on purpose: what has to hold is that both
			// values reach the config, not how gofmt happens to align
			// the field names in front of them.
			contains(t, cfg, "UnitFlags:")
			contains(t, cfg, "cfg.UnitFlags")
			contains(t, cfg, `FlagNamesArgs:`)
			contains(t, cfg, `[]string{"daemon", "-flag-names"}`)
		})
	}
}

// The other direction of the same drift, which no test of this
// repository's own files can catch: a unit *ahead* of the image it is
// written for -- this script writing today's flags against a pinned
// GRAIN_IMAGE, or a host reinstalled onto an older build. The flags are
// all defined in pkg/cli/daemon.go at that commit, so the assertion
// above is satisfied and the deployment is still misconfigured.
//
// So the installer asks the build itself, at install time, and says
// which flags it is about to write that the build does not take.
func TestTheInstallerChecksItsFlagsAgainstTheBuildItWritesTheUnitFor(t *testing.T) {
	code := setupCode(t)

	// Asked of the build that will run: the image this unit starts.
	spec := body(t, code, "daemon_flag_spec() {")
	contains(t, spec, "image_run -- daemon -flag-names")
	// A build too old to answer is "not checked", not "accepts nothing":
	// reporting every flag in the unit as unknown would be a false alarm
	// on the one check nobody can afford to learn to ignore.
	contains(t, spec, "|| true")
	check := body(t, code, "verify_daemon_flags() {")
	contains(t, check, "are not checked against it")

	// And it runs before either unit is written, rather than after --
	// the point is to say so while an operator is still watching the
	// install they asked for.
	contains(t, code, `verify_daemon_flags "${daemon_args[@]}"`)
	before(t, code, `verify_daemon_flags "${daemon_args[@]}"`, "cat > /etc/systemd/system/grain-daemon.service",
		"the container-mode unit is written before its flags are checked against the image")

	// Reported, not enforced, like verify_agent_cli beside it: an
	// operator pinning an older image usually means it.
	absent(t, check, "exit 1")
}

// The run-location flags an older installer wrote into a unit still pass
// verify_daemon_flags against this build: v2.3.0 removed the per-run
// docker launcher -cli-image, -cli-image-file and -cli-container-engine
// configured, and "auto" as a location, but keeps the flags defined as
// no-ops for a release so those units start and are not reported as
// drifted.
func TestAnOlderUnitsRunLocationFlagsStillPassVerifyDaemonFlags(t *testing.T) {
	var spec strings.Builder
	for name, takesValue := range daemonFlags(t) {
		kind := "bool"
		if takesValue {
			kind = "value"
		}
		spec.WriteString(name + " " + kind + "\n")
	}
	older := ` daemon -claude-run auto -codex-run container -agy-run host -cli-image ghcr.io/bwsalmon/grain/google-grain:v2.1.0` +
		` -cli-image-file /etc/grain/image.env -cli-container-engine docker`
	env := []string{"DAEMON_FLAG_SPEC_READ=1", "DAEMON_FLAG_SPEC=" + spec.String(), "GRAIN_IMAGE_REF=ghcr.io/bwsalmon/grain/grain:v2.3.0"}
	out := runBash(t, logFn+shellFunction(t, "verify_daemon_flags")+"verify_daemon_flags"+older, env...)
	absent(t, out, "WARNING")
	// And the check is live: a flag the build does not define is named.
	out = runBash(t, logFn+shellFunction(t, "verify_daemon_flags")+"verify_daemon_flags"+older+" -no-such-flag x", env...)
	contains(t, out, "-no-such-flag")
}
