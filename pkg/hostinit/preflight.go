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

package hostinit

import (
	"context"
	"fmt"
	"os"

	"github.com/ai-on-gke/grain/pkg/version"
)

// preflight checks every prerequisite platform depends on but will
// never install itself -- make, node/npm, /dev/kvm on PlatformLinuxKVM;
// a reachable cluster, RBAC, a pullable sandbox image, ReadWriteOnce
// storage on PlatformK8sContainer; a reachable cluster, RBAC, a
// privileged namespace and nested virtualization on PlatformK8sGranule
// -- before initFor does anything else. A bare host missing one of
// these used to be discovered only after the most expensive work Init
// does (building the granule CLI and its three images can take over ten
// minutes): the check sat at the bottom of initFor's step order, behind
// everything that could have been skipped instead. preflight runs first
// and reports every missing piece together, not merely the first one,
// so a single run tells an operator everything they have to fix rather
// than one prerequisite per run.
//
// Each Platform contributes its own ordered list of checks
// (checksFor): a "gate" check -- goos for the two host platforms,
// kubectl-reaches-the-cluster for the two Kubernetes platforms -- stops
// the list immediately on failure, since nothing after it is even
// checkable (there is no sense running `make --version` against a
// platform preflight has already refused outright, or checking RBAC
// against a cluster kubectl cannot even reach); an ordinary check adds
// its Step either way and lets the rest of the list run, which is how
// every missing piece is still reported together on a host or cluster
// this package actually brings up.
//
// ok is false if initFor must stop here, having made no change, started
// no install and built nothing: see this function's own entries in
// Result.Steps for what is missing and the exact command to fix it.
func preflight(ctx context.Context, sys System, r *Result, opts Options, goos string) (ok bool) {
	ok = true
	for _, c := range checksFor(opts, goos) {
		good, abort := c(ctx, sys, r)
		if !good {
			ok = false
		}
		if abort {
			break
		}
	}
	return ok
}

// platformCheck is one entry in a Platform's preflight list. It adds
// whatever Step(s) it wants to r itself (a binary-presence check always
// adds one; a gate check adds one only when it fails, so a host that
// passes it sees no extra line it wouldn't have seen before this check
// existed) and reports (ok, abort): ok is false if this check failed;
// abort is true if preflight must stop immediately rather than run the
// checks after this one.
type platformCheck func(ctx context.Context, sys System, r *Result) (ok, abort bool)

// linuxBuildToolchainChecks is the ordered preflight list every
// Linux-hosted platform here shares: the goos gate (checkGOOS), then
// make and node/npm, both needed by `make build` regardless of which
// sandbox backend the daemon ends up running. PlatformLinuxKVM appends
// its own /dev/kvm check after these three; PlatformLinuxContainer has
// nothing to add, since the one thing that distinguishes it --
// deliberately no /dev/kvm demanded -- is a check's absence, not a
// check of its own.
//
// With opts.UseBinary set nothing runs `make build`, so make and
// node/npm give way to a single check that the named binary is there
// and is a Go binary this host can execute (checkPrebuiltGrain).
func linuxBuildToolchainChecks(opts Options, goos string) []platformCheck {
	if opts.UseBinary != "" {
		return []platformCheck{checkGOOS(goos), checkPrebuiltGrain(opts.UseBinary)}
	}
	return []platformCheck{
		checkGOOS(goos),
		checkCommand("make",
			"`make build` -- the Makefile's own build and frontend targets -- needs make; grain init "+
				"installs docker, Go and the claude CLI but draws the line at a C toolchain",
			"sudo apt-get install -y make",
			"make", "--version"),
		checkCommand("node & npm (frontend build)",
			"`make build` runs `make frontend` before building the grain binary, since pkg/ui/server.go "+
				"go:embeds its output -- grain init does not pin or install a node/npm version the way it "+
				"does Go's",
			"sudo apt-get install -y nodejs npm",
			"npm", "--version"),
	}
}

// checksFor returns opts.Platform's own preflight list, in the order
// preflight runs it. Only the two Kubernetes platforms read anything
// from opts beyond Platform itself (K8sOptions/K8sGranuleOptions and
// DataDir); the two Linux host platforms still take goos as their own
// explicit parameter, the seam initFor's own tests use to exercise the
// non-Linux refusal without actually running on a non-Linux host.
func checksFor(opts Options, goos string) []platformCheck {
	platform := opts.Platform
	switch platform {
	case PlatformLinuxKVM:
		return append(linuxBuildToolchainChecks(opts, goos),
			checkCommand("/dev/kvm",
				"granule boots every sandbox as a cloud-hypervisor VM over KVM; without /dev/kvm every sandbox "+
					"start fails, and grain init has no safe way to turn on nested virtualization or load the "+
					"kvm module on your behalf. A host that cannot have KVM can run every sandbox as a plain docker "+
					"container instead: re-run with -platform=linux-container",
				"sudo modprobe kvm_intel || sudo modprobe kvm_amd    # or, with no KVM: re-run with -platform=linux-container",
				"test", "-e", "/dev/kvm"))
	case PlatformLinuxContainer:
		// No /dev/kvm check: its absence is the reason this platform
		// exists. No docker check either -- like PlatformLinuxKVM,
		// docker is installed by initFor's own ensureDocker step
		// (StepManual without root), not demanded by preflight.
		return linuxBuildToolchainChecks(opts, goos)
	case PlatformK8sContainer:
		return k8sContainerChecks(opts.K8s, opts.DataDir)
	case PlatformK8sGranule:
		return k8sGranuleChecks(opts.K8sGranule, opts.DataDir)
	case PlatformGKESandboxes:
		return gkeSandboxesChecks(opts)
	default:
		// Options.validate rejects any Platform not in knownPlatforms
		// before initFor ever calls preflight; reaching here would be
		// this package's own bug, not an operator's bad flag.
		return []platformCheck{func(ctx context.Context, sys System, r *Result) (bool, bool) {
			r.add(Step{Name: "platform", Status: StepRefused,
				Detail: fmt.Sprintf("grain init: unrecognized platform %q", platform)})
			return false, true
		}}
	}
}

// checkGOOS is PlatformLinuxKVM's (and, in time, any other Linux-only
// platform's) gate on running on Linux at all -- the refusal that used
// to sit in initFor itself, before any Options.Platform existed to make
// it one entry among others rather than the only thing checked. A host
// that passes it gets no Step for it: the gate only speaks up when it
// is the reason initFor stops.
func checkGOOS(goos string) platformCheck {
	return func(ctx context.Context, sys System, r *Result) (bool, bool) {
		if goos == "linux" {
			return true, false
		}
		r.add(Step{Name: "host platform", Status: StepRefused, Detail: fmt.Sprintf(
			"grain init brings a bare Linux host up to a running deployment; this is %s, where docker, "+
				"the granule CLI (it does not even compile for non-Linux) and the linux claude CLI release "+
				"this package installs would all either fail or do the wrong thing -- refusing rather than "+
				"print manual steps that cannot work on this platform", goos)})
		return false, true
	}
}

// checkCommand reports whether running args succeeds, the same test
// every PlatformLinuxKVM preflight check already used before platforms
// existed: StepSkipped ("already installed") on success, StepManual
// (why, with fixCmd as the exact command to run) on failure. Never
// aborts the rest of the list -- the property every check in this
// table is meant to keep is that a host missing two of three things
// hears about both in one run.
func checkCommand(name, why, fixCmd string, args ...string) platformCheck {
	return func(ctx context.Context, sys System, r *Result) (bool, bool) {
		if _, err := sys.Run(ctx, "", nil, args[0], args[1:]...); err == nil {
			r.add(Step{Name: name, Status: StepSkipped, Detail: "already installed"})
			return true, false
		}
		r.add(Step{Name: name, Status: StepManual, Detail: why, Command: fixCmd})
		return false, false
	}
}

// checkPrebuiltGrain stands in for the make and node/npm checks when
// -use-binary names a grain binary to run instead of building one: it
// passes (StepSkipped, naming the binary and its build stamp) if path is
// an executable regular file carrying Go build info, and fails with
// StepManual otherwise -- a missing or non-executable -use-binary would
// otherwise only surface when systemd failed to start the daemon.
func checkPrebuiltGrain(path string) platformCheck {
	const name = "prebuilt grain binary"
	return func(ctx context.Context, sys System, r *Result) (bool, bool) {
		fi, err := os.Stat(path)
		if err == nil && (!fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0) {
			err = fmt.Errorf("not an executable file (mode %s)", fi.Mode())
		}
		if err == nil {
			_, err = version.ReadFile(path)
		}
		if err != nil {
			r.add(Step{Name: name, Status: StepManual,
				Detail: fmt.Sprintf("-use-binary %s is not a grain binary this host can run: %v -- point it at one, "+
					"or drop -use-binary to build from -repo-root (which needs make and node/npm)", path, err),
				Command: "chmod +x " + path + "    # if it is the right file and only lacks the execute bit"})
			return false, false
		}
		detail := "-use-binary " + describeGrainBinary(path) + "; make and node/npm are not needed"
		if owner := nonRootOwner(fi); owner != "" {
			detail += fmt.Sprintf(" (owned by %s, not root -- that account can replace what grain-daemon.service runs; prefer a root-owned path such as /usr/local/bin/grain)", owner)
		}
		r.add(Step{Name: name, Status: StepSkipped, Detail: detail})
		return true, false
	}
}
