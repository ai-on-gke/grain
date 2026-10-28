package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/bwsalmon/granule/pkg/dockervm"
	"github.com/bwsalmon/granule/pkg/vmspec"
)

// defaultStatusTimeout bounds one "vm status": the probe behind it is a
// single attempt at reaching the guest, so anything longer than this is
// docker itself being slow rather than a guest taking its time.
const defaultStatusTimeout = 30 * time.Second

// runVMStatus answers "is this VM's guest up?" without waiting for the
// answer to be yes -- the question "vm create -wait" and "vm wait" can
// only answer by blocking until it is.
//
// It prints one fact per line -- the spec the VM was created from, what
// docker says about its container, and whether the guest answered a
// single readiness probe -- and reports through its exit status as well,
// so it is usable in a script's own condition ("granule vm status web
// >/dev/null || ..."): zero when the guest answered, non-zero when it did
// not. A VM that cannot be found, or a docker that cannot be reached, is
// an error instead -- the question could not be asked at all, which is a
// different thing from the answer being no.
func runVMStatus(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	name, rest, err := splitNameAndFlags(args, "vm status")
	if err != nil {
		return err
	}

	fs := flag.NewFlagSet("granule vm status "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", defaultStateDir(), "directory granule stores VM state in")
	timeout := fs.Duration("timeout", defaultStatusTimeout, "how long to give the check before reporting the guest as unreachable; this asks once rather than waiting for a boot -- \"granule vm wait\" is the one that waits")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	spec, err := vmspec.Load(*stateDir, name)
	if err != nil {
		return fmt.Errorf("VM %q not found in %s (\"granule vm list -state-dir %s\" shows what is)%s: %w", name, *stateDir, *stateDir, stateDirNoteSuffix(*stateDir), err)
	}
	probeCtx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	d := &dockervm.Docker{}
	st, err := dockervm.Inspect(probeCtx, d, name)
	if err != nil {
		return err
	}

	if err := writeStatus(stdout, spec, st, portsLines(probeCtx, d, spec, st)); err != nil {
		return err
	}

	// The facts are printed either way: a caller who ran this to find out
	// why a guest isn't up needs them most when the answer is "it isn't".
	if !st.Ready {
		return errGuestNotReady(name, st, *timeout, *stateDir)
	}
	return nil
}

// writeStatus prints one VM's status as fields rather than as a table
// row: there is one VM here, and the fields are worth reading whole --
// the spec is what the VM was created from, the container line is what a
// caller needs to reach for "docker logs", and the guest line is why it
// isn't ready yet.
func writeStatus(out io.Writer, spec vmspec.VMSpec, st dockervm.Status, ports []string) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "NAME\t%s\n", spec.Name)
	fmt.Fprintf(w, "IMAGE\t%s\n", spec.KonturImage)
	fmt.Fprintf(w, "CPUS\t%d\n", spec.CPUs)
	fmt.Fprintf(w, "MEMORY_MB\t%d\n", spec.MemoryMB)
	// The disk the VM boots, or the one baked into its granule image when
	// it was created without one of its own (see VMSpec.Validate). Spelt
	// out rather than left as the DISK_MB column's bare "image", which
	// would read as a path on this line.
	disk := spec.DiskImage
	if disk == "" {
		disk = "(none -- the granule image's own)"
	}
	fmt.Fprintf(w, "DISK\t%s\n", disk)
	fmt.Fprintf(w, "DISK_MODE\t%s\n", spec.DiskModeOrDerived())
	fmt.Fprintf(w, "DISK_MB\t%s\n", diskSizeLabel(spec))
	fmt.Fprintf(w, "CONTAINER\t%s\n", st.Container)
	fmt.Fprintf(w, "STATE\t%s\n", st.State)
	fmt.Fprintf(w, "GUEST\t%s\n", guestLine(st))
	// One line per published port, since a VM with several is the
	// ordinary case and a single line of them would be the one fact here
	// that has to be read apart again. The key is printed on the first
	// only, the way a field with several values reads.
	for i, port := range ports {
		key := ""
		if i == 0 {
			key = "PORTS"
		}
		fmt.Fprintf(w, "%s\t%s\n", key, port)
	}
	return w.Flush()
}

// guestLine is the GUEST fact in words: whether the guest is ready, and
// -- when it isn't -- which of the two conditions readiness can have is
// the one outstanding, since that is the question this command exists to
// answer. A guest that never answered and a guest that answers but has
// not declared itself ready want completely different things done about
// them: the first is a boot to go and read the console for, the second
// is a workload inside a healthy VM.
func guestLine(st dockervm.Status) string {
	switch {
	case st.Ready && st.Fallback:
		// Ready, and said how: this VM's image has no "granule ready"
		// mode, so the answer came from the probe that predates it (see
		// dockervm.Status.Fallback). The same word plus what it is worth
		// -- a VM created before the mode existed still answers, and its
		// owner still gets told the guest is not the one reporting.
		return `ready (probed with "granule exec -- true": this VM's image predates "granule ready")`
	case st.Ready:
		return "ready"
	case st.Reachable:
		// Only reachable when this VM was created with a -ready-file:
		// without one the agent answering is the whole check, and a
		// guest that answered would be ready above.
		return "reachable, but the guest has not declared itself ready (" + st.Detail + ")"
	case !st.Running():
		// Nothing was probed, so there is no guest-side reason to give:
		// say that rather than imply the guest was asked.
		return "unreachable (no running container to reach it through)"
	case st.Detail != "":
		return "unreachable (" + st.Detail + ")"
	default:
		return "unreachable"
	}
}

// portsLines is the PORTS fact: which host ports reach this VM's guest.
// "vm create -publish-declared" prints that once, when it creates the
// VM, and until now nothing answered it afterwards -- the host side is
// docker's to choose, so it exists nowhere but on the container, and
// asking meant knowing granule's own container naming well enough to
// type "docker port granule-vm-<name>".
//
// It is read from docker rather than from the saved spec for the same
// reason the grant is saved as a grant (see VMSpec.PublishDeclaredPorts):
// the spec records that this VM's image was allowed whatever it
// declares, not what that resolved to on this machine at this boot.
//
// A VM that publishes nothing gets a line saying so, and that line says
// which of the two nothings it is: an image declaring no ports, or an
// image declaring some that nobody granted. The two look identical from
// out here and want completely different things done about them, which
// is the confusion the grant invites (see the README's "Publishing a
// guest's ports").
//
// Best effort: each branch is one more question asked of docker, and a
// docker that cannot answer one of them is worth a line saying so rather
// than a "vm status" that fails to report anything about the guest.
func portsLines(ctx context.Context, d *dockervm.Docker, spec vmspec.VMSpec, st dockervm.Status) []string {
	ports, err := dockervm.Published(ctx, d, spec.Name)
	if err != nil {
		return []string{fmt.Sprintf("unknown (%v)", err)}
	}
	if len(ports) == 0 {
		return []string{noPortsLine(ctx, d, spec, st)}
	}
	lines := make([]string, 0, len(ports))
	for _, p := range ports {
		lines = append(lines, p.Describe())
	}
	return lines
}

// noPortsLine says "nothing is published" together with what that means
// for this particular VM, which is the declaration its image carries and
// whether this VM was created with the grant that honours it.
func noPortsLine(ctx context.Context, d *dockervm.Docker, spec vmspec.VMSpec, st dockervm.Status) string {
	declared, err := dockervm.DeclaredPorts(ctx, d, spec.KonturImage)
	switch {
	case err != nil:
		// Commonly an image that is no longer on this machine. Nothing
		// is published either way -- that half was answered by docker
		// about the container; it is the declaration that couldn't be
		// checked, so the line says so rather than implying the image
		// asks for nothing.
		return fmt.Sprintf("none, and whether that is what its image asks for could not be checked (%v)", err)
	case len(declared) == 0 && spec.PublishDeclaredPorts:
		return fmt.Sprintf("none (created with -publish-declared, and %s declares no ports with EXPOSE)", spec.KonturImage)
	case len(declared) == 0:
		return fmt.Sprintf("none (%s declares no ports with EXPOSE)", spec.KonturImage)
	case !spec.PublishDeclaredPorts:
		return fmt.Sprintf("none: %s declares %s, and this VM was not granted them -- \"granule vm update %s -publish-declared\" re-creates it with them published, and \"-docker-run-opt -p HOST:GUEST\" publishes a port of your choosing instead",
			spec.KonturImage, strings.Join(declared, ", "), spec.Name)
	case !st.Running():
		// The grant is there and the container isn't: a publication
		// belongs to a running container, so there is nothing for
		// docker to hold the host side of. Getting the container back
		// is what the STATE line and this command's own error are
		// about, so this only says why the ports went with it.
		return fmt.Sprintf("none while the container is not running (%s declares %s, and this VM was created to publish them -- see STATE above)",
			spec.KonturImage, strings.Join(declared, ", "))
	default:
		// Running, granted, declaring -- and publishing none of it.
		// The declaration is read at each create, so the image has
		// gained these since this VM was created from it.
		return fmt.Sprintf("none, though this VM was created with -publish-declared and %s declares %s now: the declaration is read afresh at every create, so \"granule vm update %s\" re-creates this VM with what the image asks for today",
			spec.KonturImage, strings.Join(declared, ", "), spec.Name)
	}
}

// portsWord collapses what a VM publishes into the PORTS column "vm list
// -status" has room for: the mapping without its binding address
// ("32768->8080/tcp"), and one entry per guest port rather than the two
// lines docker prints for an IPv4 and an IPv6 binding of the same host
// port.
//
// It does not say anything about declarations that were never granted,
// which the single-VM status does: that costs another docker call per
// row, and it is an explanation rather than a fact about the VM. "vm
// status <name>" is where it belongs.
func portsWord(ports []dockervm.PublishedPort) string {
	if len(ports) == 0 {
		return "none"
	}
	seen := make(map[string]bool, len(ports))
	var mappings []string
	for _, p := range ports {
		mapping := p.HostPort() + "->" + p.Guest
		if seen[mapping] {
			continue
		}
		seen[mapping] = true
		mappings = append(mappings, mapping)
	}
	return strings.Join(mappings, ",")
}

// statusWord collapses a Status into the one word "vm list -status"
// prints per VM, using the same vocabulary as the STATE and GUEST lines
// above: "ready" only when the guest passed the whole check, and the
// container's own state when there was nothing to ask.
//
// "undeclared" is the third word, and only ever appears for a VM with a
// -ready-file: the guest answers and has not said its workload is up.
// One word cannot say more than that -- "vm status <name>" is where the
// path and the probe's own message are.
func statusWord(st dockervm.Status) string {
	switch {
	case !st.Running():
		return string(st.State)
	case st.Ready:
		return "ready"
	case st.Reachable:
		return "undeclared"
	default:
		return "unreachable"
	}
}

// errGuestNotReady turns "the guest isn't up" into granule's non-zero
// exit, and says what would be done about it -- which differs by how far
// short of ready the VM fell.
func errGuestNotReady(name string, st dockervm.Status, timeout time.Duration, stateDir string) error {
	switch st.State {
	case dockervm.ContainerMissing:
		return fmt.Errorf("VM %q is saved in %s but has no container %s: it was never started, or it has been removed (\"granule vm update %s -state-dir %s\" re-creates it from the saved spec, \"granule vm delete %s -state-dir %s\" forgets it)",
			name, stateDir, st.Container, name, stateDir, name, stateDir)
	case dockervm.ContainerExited:
		return fmt.Errorf("the VM container %s has exited, so the guest of %q is not reachable (see \"docker logs %s\")", st.Container, name, st.Container)
	default:
		// The VM is up and only the guest's own declaration is missing,
		// so nothing above ("it was never started", "read the console")
		// applies: what to do about it is inside the guest, which this
		// caller can still exec into to find out.
		if st.Reachable {
			return fmt.Errorf("the guest of %q is reachable but has not declared itself ready within %s: %s -- the VM booted and its agent answers, so this is the workload rather than the boot (\"granule vm exec %s -- ...\" to look, \"granule vm wait %s -state-dir %s\" to keep waiting)",
				name, timeout, st.Detail, name, name, stateDir)
		}
		return fmt.Errorf("the guest of %q did not answer within %s (see \"docker logs %s\"); if it is still booting, \"granule vm wait %s -state-dir %s\" blocks until it does",
			name, timeout, st.Container, name, stateDir)
	}
}
