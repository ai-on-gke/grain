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
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/bwsalmon/granule/pkg/forward"
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

func (s ForwardSpec) toInternal(dir forward.Direction) forward.Spec {
	bindHost := s.BindHost
	if bindHost == "" {
		bindHost = "127.0.0.1"
	}
	targetHost := s.TargetHost
	if targetHost == "" {
		targetHost = "127.0.0.1"
	}
	return forward.Spec{
		Direction:  dir,
		BindHost:   bindHost,
		BindPort:   s.BindPort,
		TargetHost: targetHost,
		TargetPort: s.TargetPort,
	}
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

func (cfg KubectlForwardConfig) internalSpecs() []forward.Spec {
	remotes := cfg.resolvedRemotes()
	specs := make([]forward.Spec, 0, len(cfg.Locals)+len(remotes))
	for _, s := range cfg.Locals {
		if s.BindPort > 0 && s.TargetPort > 0 {
			specs = append(specs, s.toInternal(forward.LocalToRemote))
		}
	}
	for _, s := range remotes {
		specs = append(specs, s.toInternal(forward.RemoteToLocal))
	}
	return specs
}

// ForwardArgs returns the arguments to pass to the operator `granule` CLI for
// `granule forward -kube ...`.
func (c Command) ForwardArgs(cfg KubectlForwardConfig) []string {
	args := []string{"forward", "-kube"}
	if cfg.Kubectl != "" {
		args = append(args, "-kubectl", cfg.Kubectl)
	}
	if cfg.Kubeconfig != "" {
		args = append(args, "-kubeconfig", cfg.Kubeconfig)
	}
	ns := cfg.Namespace
	if ns == "" {
		ns = "default"
	}
	args = append(args, "-n", ns)
	container := cfg.Container
	if container == "" {
		container = "sandbox"
	}
	args = append(args, "-c", container)
	for _, s := range cfg.Locals {
		if s.BindPort > 0 && s.TargetPort > 0 {
			args = append(args, "-L", s.String())
		}
	}
	for _, s := range cfg.resolvedRemotes() {
		args = append(args, "-R", s.String())
	}
	args = append(args, cfg.Pod)
	return args
}

// Forward runs `granule forward -kube` through c.
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

// Forward runs `granule forward -kube` through Local.
func Forward(ctx context.Context, cfg KubectlForwardConfig) error {
	return Local.Forward(ctx, cfg)
}

// KubectlForwardArgs renders the `kubectl` command-line arguments that invoke
// `granule forward` inside a Kubernetes sandbox pod over `kubectl exec -i`.
func KubectlForwardArgs(cfg KubectlForwardConfig) []string {
	var argv []string
	if cfg.Kubeconfig != "" {
		argv = append(argv, "--kubeconfig", cfg.Kubeconfig)
	}
	ns := cfg.Namespace
	if ns == "" {
		ns = "default"
	}
	argv = append(argv, "-n", ns, "exec", "-i")
	container := cfg.Container
	if container == "" {
		container = "sandbox"
	}
	argv = append(argv, "-c", container, cfg.Pod, "--", "granule", "forward")
	for _, s := range cfg.Locals {
		if s.BindPort > 0 && s.TargetPort > 0 {
			argv = append(argv, "-L", s.String())
		}
	}
	for _, s := range cfg.resolvedRemotes() {
		argv = append(argv, "-R", s.String())
	}
	return argv
}

func kubectlForwardCommand(ctx context.Context, bin string, args ...string) *exec.Cmd {
	if bin == "" {
		bin = "kubectl"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
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

// ForwardKubectl starts `kubectl exec -i ... -- granule forward` against
// cfg.Pod and serves the controller side of the multiplexed port-forward
// session until ctx is cancelled or the stream closes.
func ForwardKubectl(ctx context.Context, cfg KubectlForwardConfig) error {
	specs := cfg.internalSpecs()
	if len(specs) == 0 {
		return nil
	}
	argv := KubectlForwardArgs(cfg)
	cmd := kubectlForwardCommand(ctx, cfg.Kubectl, argv...)
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("granule: forward stdin pipe for %s: %w", cfg.Pod, err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("granule: forward stdout pipe for %s: %w", cfg.Pod, err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("granule: starting kubectl exec forward for %s: %w", cfg.Pod, err)
	}

	runErr := forward.RunClient(ctx, stdoutPipe, stdinPipe, specs, cfg.OnReady)
	_ = stdinPipe.Close()
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if runErr != nil {
		if said := strings.TrimSpace(stderr.String()); said != "" {
			return fmt.Errorf("granule: forward %s: %w: %s", cfg.Pod, runErr, said)
		}
		return fmt.Errorf("granule: forward %s: %w", cfg.Pod, runErr)
	}
	if waitErr != nil {
		if said := strings.TrimSpace(stderr.String()); said != "" {
			return fmt.Errorf("granule: forward %s: %w: %s", cfg.Pod, waitErr, said)
		}
		return fmt.Errorf("granule: forward %s: %w", cfg.Pod, waitErr)
	}
	return nil
}

// ForwardStream runs the controller side of a multiplexed port-forward session
// over a stream opened by openStream (for example, GranuleSandboxRunner.OpenForwardStream)
// until ctx is cancelled or the stream closes.
func ForwardStream(ctx context.Context, openStream func(context.Context) (io.ReadWriteCloser, error), cfg KubectlForwardConfig) error {
	specs := cfg.internalSpecs()
	if len(specs) == 0 {
		return nil
	}
	st, err := openStream(ctx)
	if err != nil {
		return fmt.Errorf("granule: opening forward stream for %s: %w", cfg.Pod, err)
	}
	defer st.Close()
	if err := forward.RunClient(ctx, st, st, specs, cfg.OnReady); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("granule: forward %s: %w", cfg.Pod, err)
	}
	return nil
}

// ServeForwardClient runs the controller side of a port-forward session over r
// and w.
func ServeForwardClient(ctx context.Context, r io.Reader, w io.Writer, remotes, locals []ForwardSpec, onReady func()) error {
	specs := make([]forward.Spec, 0, len(locals)+len(remotes))
	for _, s := range locals {
		specs = append(specs, s.toInternal(forward.LocalToRemote))
	}
	for _, s := range remotes {
		specs = append(specs, s.toInternal(forward.RemoteToLocal))
	}
	return forward.RunClient(ctx, r, w, specs, onReady)
}

// ServeForwardGuest binds remotes inside the guest and serves the guest side of
// a port-forward session over r and w.
func ServeForwardGuest(ctx context.Context, r io.Reader, w io.Writer, remotes []ForwardSpec) error {
	specs := make([]forward.Spec, 0, len(remotes))
	for _, s := range remotes {
		specs = append(specs, s.toInternal(forward.RemoteToLocal))
	}
	return forward.ServeGuest(ctx, r, w, specs)
}
