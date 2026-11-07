package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gke-labs/granule/pkg/dockercli"
	"github.com/gke-labs/granule/pkg/guestforward"
	"github.com/gke-labs/granule/pkg/vmspec"
)

const forwardUsage = `usage: granule forward <target> [flags]

Forwards TCP ports into and out of a VM's guest over the same channel
"granule vm exec" uses -- docker/kubectl exec into the VM's container,
then vsock to the guest's agent -- never over the guest's network. A
forwarded port inside the guest is a loopback listener the agent holds,
so it works for a guest with no network at all, and nothing here
touches the guest's interfaces or routes. Runs until interrupted.

Targets:
  <vm>                a VM granule created on the local docker daemon
  container/<name>    any docker container running a granule VM
  pod/<name>          a granule VM pod on Kubernetes, reached with kubectl
                      (-n, -c, -context and -kubeconfig pick which)

Mappings (each repeatable, at least one required):
  -remote GUEST=CALLER   a listener inside the guest whose connections reach
  (-R)                   CALLER on this machine; ssh -R's direction
  -local  CALLER=GUEST   a listener on this machine whose connections reach
  (-L)                   GUEST, dialled from inside the guest; ssh -L's

An address is host:port, a bare port (meaning 127.0.0.1), or unix:/path.
Port 0 picks a free port; the bound ports are printed once everything is
up (-json for one machine-readable line).

Examples:
  granule forward web -R 9728=9728
  granule forward pod/sandbox-1 -n sandboxes -L 127.0.0.1:0=7000 -json -reconnect

Flags:`

// repeatedMapping collects one kind of GUEST=CALLER / CALLER=GUEST flag.
type repeatedMapping [][2]string

func (r *repeatedMapping) String() string {
	var parts []string
	for _, m := range *r {
		parts = append(parts, m[0]+"="+m[1])
	}
	return strings.Join(parts, " ")
}

func (r *repeatedMapping) Set(v string) error {
	listen, target, ok := strings.Cut(v, "=")
	if !ok || listen == "" || target == "" {
		return fmt.Errorf("%q: want LISTEN=TARGET, e.g. 9728=127.0.0.1:9728", v)
	}
	for _, a := range []string{listen, target} {
		if _, _, err := guestforward.ParseAddr(a); err != nil {
			return err
		}
	}
	*r = append(*r, [2]string{listen, target})
	return nil
}

// forwardTarget is what <target> resolved to.
type forwardTarget struct {
	transport guestforward.Transport
	describe  string
}

func runForward(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	var target string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		target, args = args[0], args[1:]
	}

	fs := flag.NewFlagSet("granule forward", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), forwardUsage)
		fs.PrintDefaults()
	}
	var remote, local repeatedMapping
	fs.Var(&remote, "remote", "GUEST=CALLER: listen on GUEST inside the guest, connect to CALLER here (repeatable)")
	fs.Var(&remote, "R", "short for -remote")
	fs.Var(&local, "local", "CALLER=GUEST: listen on CALLER here, connect to GUEST inside the guest (repeatable)")
	fs.Var(&local, "L", "short for -local")
	anyAddr := fs.Bool("remote-any-addr", false, "let -remote listen on a guest address other than loopback, reachable from the guest's network")
	asJSON := fs.Bool("json", false, "print the bound addresses as one JSON line once everything is up")
	reconnect := fs.Bool("reconnect", false, "when the exec stream drops, reconnect with backoff and re-establish every mapping instead of exiting")
	readyTimeout := fs.Duration("ready-timeout", 60*time.Second, "how long to wait for the guest's agent to answer, on start and on each reconnect")
	stateDir := fs.String("state-dir", defaultStateDir(), "directory granule stores VM state in (<vm> targets)")
	namespace := fs.String("n", "", "Kubernetes namespace (pod/ targets)")
	container := fs.String("c", "", "the pod's VM container (pod/ targets; default: the pod's default container)")
	kubeContext := fs.String("context", "", "kubeconfig context (pod/ targets)")
	kubeconfig := fs.String("kubeconfig", "", "kubeconfig file (pod/ targets)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if target == "" {
		if fs.NArg() > 0 {
			return fmt.Errorf("forward: the target comes before the flags (granule forward <target> [flags]), and %q came after them", fs.Arg(0))
		}
		return errors.New("forward: a target is required (granule forward <vm|container/name|pod/name> [flags]; \"granule forward -h\" for more)")
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("forward: unexpected arguments %q", strings.Join(fs.Args(), " "))
	}
	if len(remote) == 0 && len(local) == 0 {
		return errors.New("forward: nothing to forward; give at least one -remote GUEST=CALLER or -local CALLER=GUEST")
	}

	t, err := resolveForwardTarget(target, *stateDir, guestforward.Kubectl{
		Namespace:  *namespace,
		Container:  *container,
		Context:    *kubeContext,
		Kubeconfig: *kubeconfig,
	})
	if err != nil {
		return err
	}
	opts := guestforward.Options{
		Transport:    t.transport,
		Reconnect:    *reconnect,
		ReadyTimeout: *readyTimeout,
		Logf: func(format string, args ...any) {
			fmt.Fprintf(stderr, "granule: forward: "+format+"\n", args...)
		},
	}
	for _, m := range remote {
		opts.Remote = append(opts.Remote, guestforward.RemoteForward{Guest: m[0], Target: m[1], AnyAddr: *anyAddr})
	}
	for _, m := range local {
		opts.Local = append(opts.Local, guestforward.LocalForward{Listen: m[0], Guest: m[1]})
	}

	f, err := guestforward.Start(ctx, opts)
	if err != nil {
		return fmt.Errorf("forward to %s: %w", t.describe, err)
	}
	defer f.Close()
	if err := writeBindings(stdout, f.Bindings(), *asJSON); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return nil
	case <-f.Done():
		if err := f.Wait(); err != nil {
			return fmt.Errorf("forward to %s: %w", t.describe, err)
		}
		return nil
	}
}

func resolveForwardTarget(target, stateDir string, k guestforward.Kubectl) (forwardTarget, error) {
	if pod, ok := strings.CutPrefix(target, "pod/"); ok {
		if pod == "" {
			return forwardTarget{}, errors.New("forward: pod/ needs a pod name")
		}
		k.Pod = pod
		return forwardTarget{transport: guestforward.KubectlExec(k), describe: target}, nil
	}
	if k != (guestforward.Kubectl{}) {
		return forwardTarget{}, fmt.Errorf("forward: -n, -c, -context and -kubeconfig only apply to pod/ targets, and %q is not one", target)
	}
	if name, ok := strings.CutPrefix(target, "container/"); ok {
		if name == "" {
			return forwardTarget{}, errors.New("forward: container/ needs a container name")
		}
		return forwardTarget{transport: guestforward.DockerExec(dockercli.Binary(), name), describe: target}, nil
	}
	if err := vmspec.ValidateName(target); err != nil {
		return forwardTarget{}, fmt.Errorf("forward: %w (or name container/<name> or pod/<name>)", err)
	}
	if _, err := vmspec.Load(stateDir, target); err != nil {
		return forwardTarget{}, fmt.Errorf("VM %q not found in %s (\"granule vm list -state-dir %s\" shows what is; container/<name> or pod/<name> reach a VM granule did not create)%s: %w", target, stateDir, stateDir, stateDirNoteSuffix(stateDir), err)
	}
	return forwardTarget{
		transport: guestforward.DockerExec(dockercli.Binary(), vmspec.ContainerName(target)),
		describe:  fmt.Sprintf("VM %q", target),
	}, nil
}

func writeBindings(w io.Writer, bindings []guestforward.Binding, asJSON bool) error {
	if asJSON {
		b, err := json.Marshal(struct {
			Bindings []guestforward.Binding `json:"bindings"`
		}{bindings})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "%s\n", b)
		return err
	}
	for _, b := range bindings {
		var err error
		if b.Direction == "local" {
			_, err = fmt.Fprintf(w, "forwarding %s (here) -> %s (in the guest)\n", b.Listen, b.Target)
		} else {
			_, err = fmt.Fprintf(w, "forwarding %s (in the guest) -> %s (here)\n", b.Listen, b.Target)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
