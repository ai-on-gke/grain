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

package granule

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/gke-labs/granule/pkg/guestforward"
)

// ForwardSpec describes a single TCP port forward between the host/controller
// and a sandbox guest.
type ForwardSpec struct {
	BindHost   string
	BindPort   int
	TargetHost string
	TargetPort int
}

// String formats s as "bindHost:bindPort:targetHost:targetPort".
func (s ForwardSpec) String() string {
	bindHost := s.BindHost
	if bindHost == "" {
		bindHost = "127.0.0.1"
	}
	targetHost := s.TargetHost
	if targetHost == "" {
		targetHost = "127.0.0.1"
	}
	return fmt.Sprintf("%s:%d:%s:%d", bindHost, s.BindPort, targetHost, s.TargetPort)
}

// addr renders host:port, defaulting an empty host to loopback.
func addr(host string, port int) string {
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// remoteForward maps s, a guest-to-controller (-R) spec, onto
// guestforward's shape: the agent listens on the bind address inside the
// guest and each connection to it is dialed to the target here. The guest
// only ever names which of these listeners took a connection, never where
// to dial, so it cannot reach anything on the controller beyond the
// targets listed here.
func (s ForwardSpec) remoteForward() guestforward.RemoteForward {
	return guestforward.RemoteForward{Guest: addr(s.BindHost, s.BindPort), Target: addr(s.TargetHost, s.TargetPort)}
}

// localForward maps s, a controller-to-guest (-L) spec, onto
// guestforward's shape.
func (s ForwardSpec) localForward() guestforward.LocalForward {
	return guestforward.LocalForward{Listen: addr(s.BindHost, s.BindPort), Guest: addr(s.TargetHost, s.TargetPort)}
}

// LoopbackRemoteSpecs builds deduplicated, sorted guest-to-controller (-R)
// loopback forward specifications mapping 127.0.0.1:<port> inside the guest to
// 127.0.0.1:<port> on the controller for each positive port in ports.
func LoopbackRemoteSpecs(ports []int) []ForwardSpec {
	seen := make(map[int]bool, len(ports))
	var sorted []int
	for _, p := range ports {
		if p <= 0 || seen[p] {
			continue
		}
		seen[p] = true
		sorted = append(sorted, p)
	}
	slices.Sort(sorted)
	specs := make([]ForwardSpec, 0, len(sorted))
	for _, p := range sorted {
		specs = append(specs, ForwardSpec{
			BindHost:   "127.0.0.1",
			BindPort:   p,
			TargetHost: "127.0.0.1",
			TargetPort: p,
		})
	}
	return specs
}

// KubectlForwardConfig configures a `granule forward` session into a
// Kubernetes sandbox pod over `kubectl exec -i`.
type KubectlForwardConfig struct {
	// Kubectl is the kubectl binary to execute ("kubectl" when empty).
	Kubectl string
	// Kubeconfig is an optional kubeconfig file path passed as --kubeconfig.
	Kubeconfig string
	// Namespace is the pod's Kubernetes namespace ("default" when empty).
	Namespace string
	// Pod is the sandbox pod name.
	Pod string
	// Container is the container within the pod ("sandbox" when empty).
	Container string
	// RemotePorts is shorthand for LoopbackRemoteSpecs(RemotePorts): each port
	// is forwarded from guest 127.0.0.1:<port> to controller 127.0.0.1:<port>.
	RemotePorts []int
	// Remotes are explicit guest-to-controller (-R) port forwards.
	Remotes []ForwardSpec
	// Locals are explicit controller-to-guest (-L) port forwards.
	Locals []ForwardSpec
	// OnReady is called once when the guest side of the forward session is
	// bound and ready to accept connections.
	OnReady func()
}

func (cfg KubectlForwardConfig) resolvedRemotes() []ForwardSpec {
	seen := make(map[string]bool)
	var out []ForwardSpec
	for _, s := range LoopbackRemoteSpecs(cfg.RemotePorts) {
		k := s.String()
		if !seen[k] {
			seen[k] = true
			out = append(out, s)
		}
	}
	for _, s := range cfg.Remotes {
		if s.BindPort <= 0 || s.TargetPort <= 0 {
			continue
		}
		k := s.String()
		if !seen[k] {
			seen[k] = true
			out = append(out, s)
		}
	}
	return out
}

func (cfg KubectlForwardConfig) resolvedLocals() []ForwardSpec {
	var out []ForwardSpec
	for _, s := range cfg.Locals {
		if s.BindPort > 0 && s.TargetPort > 0 {
			out = append(out, s)
		}
	}
	return out
}

// ForwardArgs returns the arguments to pass to the operator `granule` CLI for
// `granule forward pod/<pod> ...`. That CLI runs kubectl from PATH, so
// cfg.Kubectl is not passed on.
func (c Command) ForwardArgs(cfg KubectlForwardConfig) []string {
	args := []string{"forward", "pod/" + cfg.Pod}
	if cfg.Kubeconfig != "" {
		args = append(args, "-kubeconfig", cfg.Kubeconfig)
	}
	args = append(args, "-n", cfg.namespace(), "-c", cfg.container())
	for _, s := range cfg.resolvedLocals() {
		l := s.localForward()
		args = append(args, "-L", l.Listen+"="+l.Guest)
	}
	for _, s := range cfg.resolvedRemotes() {
		r := s.remoteForward()
		args = append(args, "-R", r.Guest+"="+r.Target)
	}
	return args
}

func (cfg KubectlForwardConfig) namespace() string {
	if cfg.Namespace == "" {
		return "default"
	}
	return cfg.Namespace
}

func (cfg KubectlForwardConfig) container() string {
	if cfg.Container == "" {
		return "sandbox"
	}
	return cfg.Container
}

// Forward runs `granule forward pod/<pod>` through c.
func (c Command) Forward(ctx context.Context, cfg KubectlForwardConfig) error {
	cmd := c.command(ctx, c.ForwardArgs(cfg)...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("granule: forward %s: %w: %s", cfg.Pod, err, strings.TrimSpace(output.String()))
	}
	return nil
}

// KubectlForwardArgs renders the `kubectl` command-line arguments that reach
// the guest agent's forwarding session inside a Kubernetes sandbox pod: the
// shim's `granule forward-stdio` mode over `kubectl exec -i`. The mappings
// themselves travel inside the session rather than on this command line.
func KubectlForwardArgs(cfg KubectlForwardConfig) []string {
	var argv []string
	if cfg.Kubeconfig != "" {
		argv = append(argv, "--kubeconfig", cfg.Kubeconfig)
	}
	return append(argv, "-n", cfg.namespace(), "exec", "-i",
		"-c", cfg.container(), cfg.Pod, "--", "granule", guestforward.ShimMode)
}

func kubectlForwardCommand(bin string, args ...string) *exec.Cmd {
	if bin == "" {
		bin = "kubectl"
	}
	cmd := exec.Command(bin, args...)
	for _, name := range []string{
		"PATH", "HOME", "KUBECONFIG",
		"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy",
		"USER", "LOGNAME",
		"GOOGLE_APPLICATION_CREDENTIALS", "CLOUDSDK_CONFIG",
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

// ForwardKubectl starts `kubectl exec -i ... -- granule forward-stdio`
// against cfg.Pod and serves the controller side of the guestforward session
// until ctx is cancelled or the stream closes.
func ForwardKubectl(ctx context.Context, cfg KubectlForwardConfig) error {
	remotes, locals := cfg.resolvedRemotes(), cfg.resolvedLocals()
	if len(remotes)+len(locals) == 0 {
		return nil
	}
	argv := KubectlForwardArgs(cfg)
	transport := func(context.Context) (io.ReadWriteCloser, error) {
		return startCommandConn(kubectlForwardCommand(cfg.Kubectl, argv...))
	}
	if err := serveForward(ctx, transport, remotes, locals, cfg.OnReady); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("granule: forward %s: %w", cfg.Pod, err)
	}
	return ctx.Err()
}

// serveForward runs one guestforward session over transport, calls onReady
// once every guest listener is bound, and returns when the session ends.
// It does not reconnect: callers supervise and restart it themselves.
func serveForward(ctx context.Context, transport guestforward.Transport, remotes, locals []ForwardSpec, onReady func()) error {
	opts := guestforward.Options{Transport: transport}
	for _, s := range remotes {
		opts.Remote = append(opts.Remote, s.remoteForward())
	}
	for _, s := range locals {
		opts.Local = append(opts.Local, s.localForward())
	}
	f, err := guestforward.Start(ctx, opts)
	if err != nil {
		return err
	}
	if onReady != nil {
		onReady()
	}
	return f.Wait()
}

// commandConn is a running command's stdout and stdin as one stream.
// Closing it kills the command; once its stdout ends, a read reports the
// command's exit status and the tail of its stderr, which is how "pod not
// found" or "the guest is not reachable" reaches the caller rather than a
// bare EOF.
type commandConn struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr bytes.Buffer

	waitOnce sync.Once
	waitErr  error
}

func startCommandConn(cmd *exec.Cmd) (*commandConn, error) {
	c := &commandConn{cmd: cmd}
	var err error
	if c.stdin, err = cmd.StdinPipe(); err != nil {
		return nil, err
	}
	if c.stdout, err = cmd.StdoutPipe(); err != nil {
		return nil, err
	}
	cmd.Stderr = &c.stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", cmd.Path, err)
	}
	return c, nil
}

func (c *commandConn) wait() error {
	c.waitOnce.Do(func() { c.waitErr = c.cmd.Wait() })
	return c.waitErr
}

func (c *commandConn) Read(p []byte) (int, error) {
	n, err := c.stdout.Read(p)
	if err == nil {
		return n, nil
	}
	werr := c.wait()
	said := c.stderr.String()
	if len(said) > 4<<10 {
		said = said[len(said)-4<<10:]
	}
	if said = strings.TrimSpace(said); said != "" {
		if werr == nil {
			werr = err
		}
		return n, fmt.Errorf("%w: %s", werr, said)
	}
	if werr != nil {
		return n, werr
	}
	return n, err
}

func (c *commandConn) Write(p []byte) (int, error) { return c.stdin.Write(p) }

func (c *commandConn) Close() error {
	_ = c.stdin.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	_ = c.wait()
	return nil
}

// pipeConn joins a reader and a writer into one stream.
type pipeConn struct {
	io.Reader
	io.Writer
}

func (p pipeConn) Close() error {
	if c, ok := p.Reader.(io.Closer); ok {
		_ = c.Close()
	}
	if c, ok := p.Writer.(io.Closer); ok {
		_ = c.Close()
	}
	return nil
}

// ServeForwardClient runs the controller side of a port-forward session over r
// and w, asking the guest end to bind remotes and listening here on locals.
func ServeForwardClient(ctx context.Context, r io.Reader, w io.Writer, remotes, locals []ForwardSpec, onReady func()) error {
	used := false
	transport := func(context.Context) (io.ReadWriteCloser, error) {
		if used {
			return nil, errors.New("granule: forward stream already used")
		}
		used = true
		return pipeConn{r, w}, nil
	}
	return serveForward(ctx, transport, remotes, locals, onReady)
}

// ServeForwardGuest serves the guest side of a port-forward session over r
// and w, binding the listeners the controller asks for. remotes is kept for
// compatibility: the controller end names its own listeners.
func ServeForwardGuest(ctx context.Context, r io.Reader, w io.Writer, remotes []ForwardSpec) error {
	return guestforward.ServeAgent(ctx, pipeConn{r, w}, guestforward.AgentOptions{})
}
