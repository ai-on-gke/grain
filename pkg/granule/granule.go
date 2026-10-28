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

// Package granule drives the `granule` binary: creating and deleting the
// sandbox VMs bwsalmon/granule runs, plus the `docker inspect` a caller
// needs to tell a VM whose container died from one still on its way up
// and the `docker logs` that reads a guest's serial console.
//
// It never reads granule's own state files and never resolves an address
// for a VM, because nothing here needs one: a VM's guest is reached by
// exec'ing into its own container (mcp.DockerExecRunner), where the
// guest's address is already configured and directly reachable. This
// package used to carry a Port (granule's state file) and a PodIP/
// DockerPodIP (crictl or `docker inspect`) for the SSH-to-a-forwarded-
// port transport that preceded it; all three existed only to describe a
// route in from outside the VM's network namespace, and went away with
// it.
//
// What is left is a deliberately shallow dependency on granule: the
// `granule` subcommand names, the container names it derives from a VM
// name (PodName), the label it puts on them (VMLabel), and one `docker
// inspect` field. Never the `granule-shim`
// binary itself, which is a different program with a different job --
// the container-facing entrypoint that boots a single VM, sets up
// netshim networking, or execs into the guest (bwsalmon/granule's own
// cmd/granule-shim doc comment: "distinct from cmd/granule, which is
// the operator-facing CLI"). This package still never needs to agree
// with granule's own code on any Go type.
package granule

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultStateDir matches bwsalmon/granule's own default: pkg/cli's
// defaultStateDir, which every "granule vm" subcommand's "-state-dir"
// flag defaults to.
const DefaultStateDir = "/var/lib/granule/vms"

// DefaultCPUs, DefaultMemoryMB and DefaultDiskGB are grain's own default
// sandbox VM shape: the size a sandbox is built at when neither the task
// nor the deployment asked for one (model.Config, model.Task, both
// zero-means-unset). Every create passes all three verbatim
// (orchestrator.GranuleConfig.createArgs), so a sandbox's size is always a
// number this repo chose.
//
// They used to be granule's own defaults instead -- read off but never
// passed, so an unset dimension meant "whatever `granule vm create` does
// when the flag is missing" and disk had no default to name at all (an
// overlay is created exactly as large as the guest image behind it, a few
// hundred megabytes of slack for a build-heavy run). Naming the shape
// here instead makes the VM a run gets a property of grain rather than of
// whichever granule is vendored and whichever guest image a deployment
// happens to have built: 2 vCPU and 2048 MiB was a small VM for an agent
// that clones, builds and tests a repo, and nothing chose it for that
// job.
//
// 8 GiB of memory and 30 GiB of disk against 2 vCPU is deliberate: an
// agent run is bounded by what a build and a test suite need resident
// and on disk far more often than by how fast it gets there, and a
// deployment that wants a different trade sets it in Settings (or
// per task) rather than living with these.
//
// DefaultDiskGB is a floor, not a ceiling, and granule enforces that:
// `granule vm create -disk-size-mb` refuses a size below the guest
// image the overlay reads through to rather than truncating the guest's
// filesystem. 30 GiB is far above the image the repository's Granulefile
// produces, but a deployment booting a guest image larger than this has
// to raise the setting to match it -- which fails loudly at create time,
// naming both sizes, rather than building a smaller VM.
const (
	DefaultCPUs     = 2
	DefaultMemoryMB = 8192
	DefaultDiskGB   = 30
)

// BackendDocker is the value `granule vm create -backend` takes to run a VM
// directly against a local docker daemon instead of writing a static pod
// manifest for a standalone kubelet to pick up -- bwsalmon/granule's own
// dockervm backend, duplicated here for the same "read the shape, don't
// import the writer" reason PodName is (see the package doc comment). It
// needs neither containerd/CNI nor kubelet on the host.
const BackendDocker = "docker"

// NetModeFlat and NetModeNAT are the values `granule vm create -net`
// takes -- bwsalmon/granule's own netshim modes, duplicated here for the
// same "read the shape, don't import the writer" reason BackendDocker and
// PodName are.
//
// NAT mode puts every guest on a private subnet inside its network
// namespace and shares that namespace's single IP between them with DNAT
// and masquerade rules, so each VM needs an address and an external port
// assigned to it. Flat mode splices one guest straight onto the segment
// the container runtime already put the namespace on, where it takes over
// the address and MAC assigned to it -- so nothing has to assign either,
// and the guest reaches the network as an ordinary container does.
//
// Flat mode also gives the guest a second NIC: it now answers to the
// namespace's own address, so anything inside that namespace dialing it
// would reach the local stack instead, and "granule exec" (which is how
// this repo reaches a guest at all -- mcp.DockerExecRunner) goes over a
// private control link instead. The guest has to configure its end of
// that link, which granule's own guest overlay does in its
// granule-control-net service; a guest image not built from that overlay
// has no route in at all under this mode. The repository's Granulefile
// gets it from its `VM PACK`, which lays granule's guest-side pieces
// over the rootfs (scripts/granule/build-granule-image.sh).
const (
	NetModeFlat = "flat"
	NetModeNAT  = "nat"
)

// listRemote reads VM names out of `granule vm list`'s table.
//
// An empty state directory is "no VMs in <dir>", optionally followed by a
// parenthesised note, and neither is a VM; anything else is a NAME header
// and then one row per VM. A row's name is its first field, because the
// columns after it (CPUS, MEMORY_MB, DISK_MB, IMAGE) are tab-padded and a
// name never contains a space -- granule's vm create refuses one.
func (c Command) listRemote(ctx context.Context, stateDir string) ([]string, error) {
	cmd := c.command(ctx, "vm", "list", "-state-dir", stateDir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("granule: vm list via %s: %w: %s",
			strings.Join([]string(c), " "), err, strings.TrimSpace(stderr.String()))
	}
	var names []string
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "", strings.HasPrefix(line, "NAME"),
			strings.HasPrefix(line, "no VMs in "), strings.HasPrefix(line, "("):
			continue
		}
		names = append(names, strings.Fields(line)[0])
	}
	return names, nil
}

// PodName returns the container name granule gives a VM --
// bwsalmon/granule's vmspec.ContainerName, duplicated here rather
// than imported for the reason the package doc gives: this is a name
// granule derives on its own, not one this package is free to choose
// independently of it.
func PodName(vmName string) string {
	return "granule-vm-" + vmName
}

// Exists reports whether granule has state for VM name under stateDir --
// i.e. whether `granule vm create` has ever succeeded for it. granule
// writes one "<state-dir>/<name>.json" per VM (its own vmspec.Save)
// and removes it on delete, so the file's presence is the same signal a
// `granule vm list` would report, without this package having to agree
// with granule on anything inside it.
func Exists(stateDir, name string) bool {
	ok, _ := Local.Exists(context.Background(), stateDir, name)
	return ok
}

// Exists reports whether granule has state for VM name under stateDir,
// asked of whichever granule c names.
//
// Local reads the directory, as this package always has. A c that is not
// local cannot: granule's state directory belongs to the machine granule
// runs on, and for a grain on macOS driving a Linux VM that directory is
// inside the VM and this process has no path to it at all. So it asks
// granule instead -- "the same signal a `granule vm list` would report",
// which is what List's and this function's own doc comments have claimed
// all along.
//
// It returns an error rather than folding a failed question into "no",
// because the two have opposite consequences: Release skips the delete on
// a false, so a lost answer read as false leaks a VM and the address it
// holds.
func (c Command) Exists(ctx context.Context, stateDir, name string) (bool, error) {
	if c.IsLocal() {
		_, err := os.Stat(filepath.Join(stateDir, name+".json"))
		return err == nil, nil
	}
	names, err := c.List(ctx, stateDir)
	if err != nil {
		return false, err
	}
	for _, n := range names {
		if n == name {
			return true, nil
		}
	}
	return false, nil
}

// List is every VM name granule has state for under stateDir, in
// whatever order the directory yields. It reads the same "<name>.json"
// files Exists probes for one at a time -- granule's own vmspec.Save
// writes one per VM and removes it on delete -- so this is the same
// signal a `granule vm list` would report, without this package having
// to agree with granule on anything inside them.
//
// A missing state directory is not an error: a deployment that has never
// created a VM has no directory yet, and "no VMs" is the right answer
// rather than a failure a caller has to special-case.
func List(stateDir string) ([]string, error) {
	return Local.List(context.Background(), stateDir)
}

// List is every VM name granule has state for under stateDir, asked of
// whichever granule c names. See Exists for why a non-local c cannot read
// the directory itself.
//
// The remote answer is parsed out of `granule vm list`, whose first column
// is the VM name under a NAME header (granule's own pkg/cli vm list). That
// is a coupling to granule's output rather than to its types, and is the
// same kind this package already accepts for PodName: a name granule
// derives, which this package is not free to decide independently of it.
func (c Command) List(ctx context.Context, stateDir string) ([]string, error) {
	if !c.IsLocal() {
		return c.listRemote(ctx, stateDir)
	}
	entries, err := os.ReadDir(stateDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("granule: reading VM state directory %s: %w", stateDir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".json"))
	}
	return names, nil
}

// dockerInspect runs `docker inspect -f format name` and returns its
// trimmed stdout.
func dockerInspect(ctx context.Context, format, name string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", "inspect", "-f", format, name)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("granule: docker inspect %s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// DockerContainerStatus returns the docker State.Status (e.g. "running",
// "exited", "created", "dead") of vmName's own VM container under
// BackendDocker -- granule.PodName(vmName), not the "-netns" holder that
// merely owns the network namespace. "granule vm create" under this backend starts
// that container with a plain "docker run -d", which -- like any "docker run -d" --
// reports success the moment the container starts, not once whatever it execs
// has proven itself alive: a guest that fails before finishing boot
// (a bad disk/kernel path, cloud-hypervisor itself crashing, ...) still
// makes granule.Create return nil, and the container goes on to exit
// within seconds. GranuleSandboxes.waitForGuestReady polls this
// so that case fails fast with the container's own status instead of
// silently waiting out the full ReadyTimeout dialing a port nothing will
// ever answer.
func DockerContainerStatus(ctx context.Context, vmName string) (string, error) {
	return dockerInspect(ctx, `{{.State.Status}}`, PodName(vmName))
}

// DockerContainerLogTail returns the last lines lines of vmName's own VM
// container log under BackendDocker -- which, for a granule VM, is the
// guest's serial console: the container's entrypoint runs
// cloud-hypervisor with "--serial tty" and the guest boots with
// "console=ttyS0", so everything the guest's kernel and its early-boot
// units print on the console lands on the container's stdout and is
// readable from the host with `docker logs`.
//
// That makes it the one account of a guest's boot that needs nothing
// inside the guest to be working: no network, no vsock agent listening,
// no account to log in as and no journal to have permission for. The
// guest units that address a granule VM are declared
// StandardOutput=journal+console precisely so that a guest which fails
// to address itself says so somewhere readable from out here
// (bwsalmon/granule's overlay-debian/etc/systemd/system/granule-net-*.service).
//
// Both streams are collected into one buffer, in the order docker
// produced them, since a console is a transcript rather than a pair of
// streams. An error means docker itself refused -- no such container, a
// daemon that is not answering -- and carries whatever it said, so a
// best-effort caller can drop it and a diagnostic one can print it.
func DockerContainerLogTail(ctx context.Context, vmName string, lines int) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", "logs", "--tail", strconv.Itoa(lines), PodName(vmName))
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("granule: docker logs %s: %w: %s", PodName(vmName), err, strings.TrimSpace(output.String()))
	}
	return output.String(), nil
}

// Command is how the granule binary is invoked: its argv prefix. Local is
// the default and is what every deployment that runs granule beside grain
// uses.
//
// It exists because granule is not always on the same machine as grain.
// granule only builds for Linux -- pkg/netshim is netlink, tc and tuntap --
// so a grain running on macOS has no granule to exec and never will until
// granule's operator CLI is separable from its VMM half. What it does have
// is a local Linux VM, and a prefix is the whole of what it takes to reach
// one: Command{"limactl", "shell", "grain-sbx", "--", "granule"}, or an
// ssh, or a docker exec. The VM is "remote" only from grain's point of
// view; it is the same machine.
//
// A prefix rather than a path because reaching another machine always
// takes a wrapper and never just a different binary name.
type Command []string

// Local is granule on this machine's PATH: the default, and what the
// package-level functions below use.
var Local = Command{"granule"}

// IsLocal reports whether c runs granule on this machine, which is what
// decides whether granule's state directory is a directory this process
// can read (Exists, List).
func (c Command) IsLocal() bool { return len(c) == 0 || (len(c) == 1 && c[0] == "granule") }

// command builds the exec.Cmd that runs granule through c: the one place
// in this package that spawns it, so that what a granule invocation
// inherits is decided once rather than per subcommand.
//
// Env is deliberately left alone, which is this package's existing
// behaviour and is why it carries entries in
// .grain/baselines/exec-env-implicit.txt: granule reads a long list of
// its own variables and shells out to `docker`, so it needs that client's
// environment (DOCKER_HOST and the rest) to reach the daemon at all, and
// a wrapper reaching another machine needs HOME for its keys. Narrowing
// it is worth doing and is not a thing to do blind -- a missing variable
// here is a deployment that stops being able to start a VM.
func (c Command) command(ctx context.Context, args ...string) *exec.Cmd {
	full := c.argv(args...)
	return exec.CommandContext(ctx, full[0], full[1:]...)
}

// argv is c's prefix with args appended, or Local's when c is empty.
func (c Command) argv(args ...string) []string {
	prefix := []string(c)
	if len(prefix) == 0 {
		prefix = []string(Local)
	}
	return append(append([]string{}, prefix...), args...)
}

// vm runs `granule vm <subcommand> name -state-dir stateDir
// <extraArgs...>`, the shape every `granule vm` subcommand shares
// (DefaultStateDir's own doc comment). It returns granule's combined
// stdout+stderr on failure, folded into the error.
func (c Command) vm(ctx context.Context, subcommand, stateDir, name string, extraArgs ...string) error {
	cmd := c.command(ctx, append([]string{"vm", subcommand, name, "-state-dir", stateDir}, extraArgs...)...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("granule: vm %s %s: %w: %s", subcommand, name, err, strings.TrimSpace(output.String()))
	}
	return nil
}

// Create runs "granule vm create", bringing name up. extraArgs carries
// whatever else create needs beyond a name and -state-dir -- guest image,
// resource sizing, and so on.
func Create(ctx context.Context, stateDir, name string, extraArgs ...string) error {
	return Local.Create(ctx, stateDir, name, extraArgs...)
}

// Delete runs "granule vm delete", tearing name down.
func Delete(ctx context.Context, stateDir, name string) error {
	return Local.Delete(ctx, stateDir, name)
}

// Create runs "granule vm create" through c.
func (c Command) Create(ctx context.Context, stateDir, name string, extraArgs ...string) error {
	return c.vm(ctx, "create", stateDir, name, extraArgs...)
}

// Delete runs "granule vm delete" through c.
func (c Command) Delete(ctx context.Context, stateDir, name string) error {
	return c.vm(ctx, "delete", stateDir, name)
}

// VMLabel is the label granule's docker backend puts on every VM container
// it starts, valued with the VM's own name (bwsalmon/granule's
// dockervm.Create, "--label granule.dev/vm=<name>"). Duplicated here for
// the reason PodName is: a name granule chooses, not one this package is
// free to.
const VMLabel = "granule.dev/vm"

// dockerCommand builds a docker invocation carrying only the Docker
// client's own environment, so nothing the daemon holds -- model keys,
// GitHub tokens -- reaches a `docker rm` that has no use for it. The same
// set orchestrator's dockerCommand names, and for the same reason.
func dockerCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "docker", args...)
	for _, name := range []string{
		"PATH", "HOME", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG",
		"DOCKER_CERT_PATH", "DOCKER_TLS_VERIFY",
	} {
		if value, ok := os.LookupEnv(name); ok {
			cmd.Env = append(cmd.Env, name+"="+value)
		}
	}
	if cmd.Env == nil {
		cmd.Env = []string{}
	}
	return cmd
}

// RemoveContainers force-removes vmName's containers directly with
// `docker rm -f`: its own VM container (PodName) and the "-netns" holder
// an older granule split the network namespace into, which granule's own
// delete still removes for the same reason.
//
// It exists for a VM granule has no state for. `granule vm create` starts
// the container before it saves "<state-dir>/<name>.json"
// (bwsalmon/granule's submitVM), so a create killed between the two --
// a cancelled Acquire, a daemon stopping, a failed save -- leaves a
// running VM that `granule vm delete` cannot reach: without state it
// falls back to the static-pod backend and never touches docker. Removing
// the containers by name is the whole of what the docker backend's own
// delete would have done.
//
// A container that is already gone is success, the way granule's own
// remove treats it: docker reports "No such container" for each missing
// name and exits non-zero even when it removed the rest, so the error is
// only returned when its stderr said something else as well.
func RemoveContainers(ctx context.Context, vmName string) error {
	pod := PodName(vmName)
	cmd := dockerCommand(ctx, "rm", "-f", pod, pod+"-netns")
	// stdout is the names docker did remove; only stderr says what failed.
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return nil
	}
	said := strings.TrimSpace(stderr.String())
	if said == "" {
		return fmt.Errorf("granule: docker rm -f %s: %w", pod, err)
	}
	for _, line := range strings.Split(said, "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.Contains(line, "No such container") {
			return fmt.Errorf("granule: docker rm -f %s: %w: %s", pod, err, said)
		}
	}
	return nil
}

// ListLabeledVMs returns the VM name (VMLabel's value) of every container,
// running or not, that carries both VMLabel and the given extra label --
// "key=value", as docker's own --filter label= takes it. An empty extra
// lists every granule VM container on the docker host.
//
// This is how a VM with no state file is found at all: granule.List reads
// the state directory, and a create interrupted before its save left
// nothing there. Each name appears once, however many containers carry it.
func ListLabeledVMs(ctx context.Context, extra string) ([]string, error) {
	args := []string{"ps", "-a", "--filter", "label=" + VMLabel}
	if extra != "" {
		args = append(args, "--filter", "label="+extra)
	}
	args = append(args, "--format", `{{.Label "`+VMLabel+`"}}`)
	cmd := dockerCommand(ctx, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("granule: docker ps listing VM containers: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	seen := map[string]bool{}
	var names []string
	for _, line := range strings.Split(stdout.String(), "\n") {
		name := strings.TrimSpace(line)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names, nil
}

// ContainerExit is what docker says about how vmName's own VM container
// stopped, or that it has not: State.Status, State.ExitCode and
// State.OOMKilled. The container's entrypoint is the VM process
// (cloud-hypervisor), so this is the host's account of that process.
type ContainerExit struct {
	Status    string
	ExitCode  int
	OOMKilled bool
}

// containerExitFormat is the docker inspect template ParseContainerExit
// reads.
const containerExitFormat = `{{.State.Status}} {{.State.ExitCode}} {{.State.OOMKilled}}`

// DockerContainerExit inspects vmName's own VM container under
// BackendDocker -- see DockerContainerStatus -- for how it stopped.
func DockerContainerExit(ctx context.Context, vmName string) (ContainerExit, error) {
	out, err := dockerInspect(ctx, containerExitFormat, PodName(vmName))
	if err != nil {
		return ContainerExit{}, err
	}
	return ParseContainerExit(out)
}

// ParseContainerExit reads containerExitFormat's output.
func ParseContainerExit(out string) (ContainerExit, error) {
	fields := strings.Fields(out)
	if len(fields) != 3 {
		return ContainerExit{}, fmt.Errorf("granule: unexpected docker inspect state %q", out)
	}
	code, err := strconv.Atoi(fields[1])
	if err != nil {
		return ContainerExit{}, fmt.Errorf("granule: docker inspect exit code %q: %w", fields[1], err)
	}
	return ContainerExit{Status: fields[0], ExitCode: code, OOMKilled: fields[2] == "true"}, nil
}

// guestOOMMarkers are what a Linux kernel prints when its OOM killer
// runs: "<proc> invoked oom-killer: ...", the "oom-kill:constraint=..."
// summary, and "Out of memory: Killed process N (name) ...". Matched in
// lower case.
var guestOOMMarkers = []string{"invoked oom-killer", "oom-kill:", "out of memory: killed process", "memory cgroup out of memory"}

// GuestOOMLine returns the last line of a guest's serial console (see
// DockerContainerLogTail) in which its kernel reported an OOM kill, or ""
// when it reported none. The "Killed process" line is preferred, since it
// names what died.
func GuestOOMLine(console string) string {
	var found, killed string
	for _, line := range strings.Split(console, "\n") {
		lower := strings.ToLower(line)
		for _, m := range guestOOMMarkers {
			if strings.Contains(lower, m) {
				found = strings.TrimSpace(line)
				if strings.Contains(lower, "killed process") {
					killed = found
				}
				break
			}
		}
	}
	if killed != "" {
		return killed
	}
	return found
}
