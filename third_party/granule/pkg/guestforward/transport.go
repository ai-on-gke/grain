package guestforward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// DockerExec is the transport for a VM container on a docker daemon:
// `docker exec -i <container> granule forward-stdio`. docker is the CLI
// to run, empty meaning "docker" on PATH.
func DockerExec(docker, container string) Transport {
	if docker == "" {
		docker = "docker"
	}
	return CommandTransport(docker, "exec", "-i", container, "granule", ShimMode)
}

// Kubectl names a VM pod's container for KubectlExec. Only Pod is
// required.
type Kubectl struct {
	// Kubectl is the CLI to run, empty meaning "kubectl" on PATH.
	Kubectl    string
	Kubeconfig string
	Context    string
	Namespace  string
	Pod        string
	// Container is the pod's VM container. Empty leaves kubectl to pick
	// the pod's default container, which for granule's manifests is the
	// VM's (see deploy/k8s).
	Container string
}

// Args is the kubectl command line KubectlExec runs, without the binary.
func (k Kubectl) Args() []string {
	var args []string
	if k.Kubeconfig != "" {
		args = append(args, "--kubeconfig", k.Kubeconfig)
	}
	if k.Context != "" {
		args = append(args, "--context", k.Context)
	}
	args = append(args, "exec", "-i")
	if k.Namespace != "" {
		args = append(args, "-n", k.Namespace)
	}
	args = append(args, k.Pod)
	if k.Container != "" {
		args = append(args, "-c", k.Container)
	}
	return append(args, "--", "granule", ShimMode)
}

// KubectlExec is the transport for a VM pod on Kubernetes:
// `kubectl exec -i <pod> -c <container> -- granule forward-stdio`, which
// needs nothing of the caller's RBAC beyond pods/exec on that pod.
func KubectlExec(k Kubectl) Transport {
	bin := k.Kubectl
	if bin == "" {
		bin = "kubectl"
	}
	return CommandTransport(bin, k.Args()...)
}

// CommandTransport runs name with args for every connection and uses its
// stdin and stdout as the stream. Its stderr is kept, and the last of it
// is what the stream's read error says once the command exits -- which
// is how "the VM is not running" or "pod not found" reaches the caller
// rather than a bare EOF. Closing the stream kills the command.
func CommandTransport(name string, args ...string) Transport {
	return func(ctx context.Context) (io.ReadWriteCloser, error) {
		return startCommand(name, args)
	}
}

type commandConn struct {
	desc   string
	cmd    *exec.Cmd
	stdin  *os.File
	stdout *os.File
	stderr *tailBuffer

	waited  chan struct{}
	waitErr error

	closeOnce sync.Once
}

func startCommand(name string, args []string) (*commandConn, error) {
	// Pipes made here rather than by os/exec, so that Wait -- run as
	// soon as the command starts, to notice it exiting -- does not close
	// the reading end out from under a Read still draining it.
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, err
	}
	c := &commandConn{
		desc:   strings.Join(append([]string{name}, args...), " "),
		stdin:  inW,
		stdout: outR,
		stderr: &tailBuffer{max: 4 << 10},
		waited: make(chan struct{}),
	}
	c.cmd = exec.Command(name, args...)
	c.cmd.Stdin = inR
	c.cmd.Stdout = outW
	c.cmd.Stderr = c.stderr
	err = c.cmd.Start()
	inR.Close()
	outW.Close()
	if err != nil {
		inW.Close()
		outR.Close()
		return nil, fmt.Errorf("starting %s: %w", c.desc, err)
	}
	go func() {
		c.waitErr = c.cmd.Wait()
		close(c.waited)
	}()
	return c, nil
}

func (c *commandConn) Read(p []byte) (int, error) {
	n, err := c.stdout.Read(p)
	if errors.Is(err, io.EOF) {
		// The command closed its stdout, which in practice means it
		// exited; give it a moment to, so the error can say how.
		select {
		case <-c.waited:
		case <-time.After(5 * time.Second):
		}
		return n, c.exitError()
	}
	return n, err
}

func (c *commandConn) exitError() error {
	msg := strings.TrimSpace(c.stderr.String())
	select {
	case <-c.waited:
		status := "exited"
		if c.waitErr != nil {
			status = c.waitErr.Error()
		}
		if msg != "" {
			return fmt.Errorf("%s: %s: %s: %w", c.desc, status, msg, io.EOF)
		}
		return fmt.Errorf("%s: %s: %w", c.desc, status, io.EOF)
	default:
		if msg != "" {
			return fmt.Errorf("%s closed its output: %s: %w", c.desc, msg, io.EOF)
		}
		return fmt.Errorf("%s closed its output: %w", c.desc, io.EOF)
	}
}

func (c *commandConn) Write(p []byte) (int, error) { return c.stdin.Write(p) }

func (c *commandConn) Close() error {
	c.closeOnce.Do(func() {
		_ = c.stdin.Close()
		select {
		case <-c.waited:
		case <-time.After(2 * time.Second):
			_ = c.cmd.Process.Kill()
			<-c.waited
		}
		_ = c.stdout.Close()
	})
	return nil
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
