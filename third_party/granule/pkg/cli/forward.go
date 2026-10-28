package cli

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/bwsalmon/granule/pkg/dockercli"
	"github.com/bwsalmon/granule/pkg/forward"
)

type forwardArgs struct {
	Kube       bool
	Kubectl    string
	Kubeconfig string
	Namespace  string
	Container  string
	Locals     []forward.Spec
	Remotes    []forward.Spec
	Target     string
}

func parseForwardCLIArgs(args []string, stderr io.Writer) (forwardArgs, error) {
	fs := flag.NewFlagSet("granule forward", flag.ContinueOnError)
	fs.SetOutput(stderr)
	kube := fs.Bool("kube", false, "reach a Kubernetes pod via kubectl exec instead of a local Docker VM")
	kubectl := fs.String("kubectl", "kubectl", "kubectl binary to run when -kube is set")
	kubeconfig := fs.String("kubeconfig", "", "kubeconfig path passed to kubectl when -kube is set")
	namespace := fs.String("n", "default", "Kubernetes namespace when -kube is set")
	container := fs.String("c", "sandbox", "Kubernetes container name when -kube is set")
	var rawLocals, rawRemotes stringList
	fs.Var(&rawLocals, "L", "forward local port to guest port ([bind_host:]port:target_host:target_port)")
	fs.Var(&rawRemotes, "R", "forward guest port to local port ([bind_host:]port:target_host:target_port)")

	var flags []string
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if !strings.Contains(name, "=") && name != "kube" && name != "h" && name != "help" && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positional = append(positional, a)
	}
	if err := fs.Parse(flags); err != nil {
		return forwardArgs{}, err
	}
	if len(positional) != 1 || strings.TrimSpace(positional[0]) == "" {
		return forwardArgs{}, fmt.Errorf("usage: granule forward [-kube] [-L spec]... [-R spec]... <name>")
	}

	out := forwardArgs{
		Kube:       *kube,
		Kubectl:    *kubectl,
		Kubeconfig: *kubeconfig,
		Namespace:  *namespace,
		Container:  *container,
		Target:     positional[0],
	}
	for _, raw := range rawLocals.values {
		spec, err := forward.ParseSpec(forward.LocalToRemote, raw)
		if err != nil {
			return forwardArgs{}, fmt.Errorf("-L %s: %w", raw, err)
		}
		out.Locals = append(out.Locals, spec)
	}
	for _, raw := range rawRemotes.values {
		spec, err := forward.ParseSpec(forward.RemoteToLocal, raw)
		if err != nil {
			return forwardArgs{}, fmt.Errorf("-R %s: %w", raw, err)
		}
		out.Remotes = append(out.Remotes, spec)
	}
	if len(out.Locals) == 0 && len(out.Remotes) == 0 {
		return forwardArgs{}, fmt.Errorf("forward: at least one -L or -R specification is required")
	}
	return out, nil
}

func (f forwardArgs) execCommand() (string, []string) {
	var shimArgs []string
	shimArgs = append(shimArgs, "granule", "forward")
	for _, s := range f.Locals {
		shimArgs = append(shimArgs, "-L", s.String())
	}
	for _, s := range f.Remotes {
		shimArgs = append(shimArgs, "-R", s.String())
	}
	if f.Kube {
		bin := f.Kubectl
		if bin == "" {
			bin = "kubectl"
		}
		var argv []string
		if f.Kubeconfig != "" {
			argv = append(argv, "--kubeconfig", f.Kubeconfig)
		}
		ns := f.Namespace
		if ns == "" {
			ns = "default"
		}
		argv = append(argv, "-n", ns, "exec", "-i")
		if f.Container != "" {
			argv = append(argv, "-c", f.Container)
		}
		argv = append(argv, f.Target, "--")
		argv = append(argv, shimArgs...)
		return bin, argv
	}
	argv := []string{"exec", "-i", "granule-vm-" + f.Target}
	argv = append(argv, shimArgs...)
	return dockercli.Binary(), argv
}

func runForward(ctx context.Context, args []string, stderr io.Writer) error {
	parsed, err := parseForwardCLIArgs(args, stderr)
	if err != nil {
		return err
	}
	bin, argv := parsed.execCommand()
	cmd := exec.CommandContext(ctx, bin, argv...)
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", bin, err)
	}

	var allSpecs []forward.Spec
	allSpecs = append(allSpecs, parsed.Locals...)
	allSpecs = append(allSpecs, parsed.Remotes...)

	runErr := forward.RunClient(ctx, stdoutPipe, stdinPipe, allSpecs, nil)
	_ = stdinPipe.Close()
	waitErr := cmd.Wait()
	if runErr != nil {
		return runErr
	}
	if waitErr != nil && ctx.Err() == nil {
		if msg := strings.TrimSpace(errBuf.String()); msg != "" {
			return fmt.Errorf("%s: %w", msg, waitErr)
		}
		return waitErr
	}
	return nil
}
