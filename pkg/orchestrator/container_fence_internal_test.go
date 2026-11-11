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

package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// fenceDocker is a docker that does whatever it is asked, logging each
// call to the returned file.
func fenceDocker(t *testing.T) (docker, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "calls")
	docker = filepath.Join(dir, "docker")
	writeExecutableStub(t, docker, "#!/bin/sh\necho \"$*\" >> "+log+"\nexit 0\n")
	return docker, log
}

func nftScript(calls []string) string {
	for _, c := range calls {
		if strings.HasSuffix(c, "-f -") {
			return c
		}
	}
	return ""
}

// A container sandbox gets the open-network fence an open granule VM
// does, in a chain of its own: the gateway and git proxy only on the
// service ports, DNS anywhere, then this host, its networks and
// link-local (the metadata server) dropped by destination, jumped to by
// address and by bridge port. Release takes the chain away again.
func TestContainerFenceInstallsTheOpenNetworkChain(t *testing.T) {
	calls := withHostFirewall(t,
		func(ctx context.Context, stdin string, args ...string) ([]byte, error) { return nil, nil },
		fixedNetworks("172.20.0.2|16|172.20.0.1|||\n"))
	withConnectedPrefixes(t, "192.168.1.20/24", "172.20.0.1/16")
	docker, _ := fenceDocker(t)
	c := NewContainerSandboxes(ContainerConfig{Docker: docker, Image: "img", Workspace: "/w", Network: "mine",
		GitProxyHost:        "172.20.0.1",
		SandboxServicePorts: func() []SandboxServicePort { return []SandboxServicePort{{"tcp", 9728}} }})
	if why := c.OpenNetworkUnfenced(t.Context()); why != "" {
		t.Fatalf("OpenNetworkUnfenced = %q on a host that can run nft", why)
	}
	sb, err := c.Acquire(t.Context(), "1-1", Shape{})
	if err != nil {
		t.Fatal(err)
	}
	script := nftScript(*calls)
	for _, want := range []string{
		"add rule inet grain_network_none ct_grain_sandbox_1_1 ip daddr 172.20.0.1 tcp dport { 9728 } accept\n",
		"add rule inet grain_network_none ct_grain_sandbox_1_1 ip daddr 172.20.0.1 meta l4proto { tcp, udp } th dport 53 accept\n",
		"add rule inet grain_network_none ct_grain_sandbox_1_1 ip daddr { 0.0.0.0/8, 127.0.0.0/8, 172.20.0.0/16 } counter drop\n",
		"add rule inet grain_network_none ct_grain_sandbox_1_1 ip6 daddr { ::1/128, ::ffff:0.0.0.0/96 } counter drop\n",
		"add rule inet grain_network_none ct_grain_sandbox_1_1 meta l4proto { tcp, udp } th dport 53 accept\n",
		"add rule inet grain_network_none ct_grain_sandbox_1_1 ip daddr { 0.0.0.0/8, 127.0.0.0/8, 169.254.0.0/16, 172.20.0.0/16, 192.168.1.0/24 } counter drop\n",
		"add rule inet grain_network_none ct_grain_sandbox_1_1 ip6 daddr { ::1/128, ::ffff:0.0.0.0/96, fc00::/7, fe80::/10 } counter drop\n",
		"add rule bridge grain_network_none prerouting iifname \"veth1d9c4eb\" ether type != arp jump ct_grain_sandbox_1_1\n",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("ruleset is missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "ct_grain_sandbox_1_1 counter drop\n") {
		t.Errorf("an open container's chain drops everything:\n%s", script)
	}
	*calls = nil
	if err := sb.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(*calls, "\n"); !strings.Contains(got, "delete chain inet grain_network_none ct_grain_sandbox_1_1") {
		t.Errorf("Release left the chain behind; nft calls:\n%s", got)
	}
}

// A container whose fence cannot be installed is removed rather than
// handed over -- under auto once nft has been found to run, and under
// require even when it cannot.
func TestContainerFenceFailsClosed(t *testing.T) {
	cannotRun := errors.New("nft binary not found")
	for name, tc := range map[string]struct {
		mode ContainerFenceMode
		nft  nftRunner
		cfg  ContainerConfig
	}{
		"auto, the ruleset refused": {
			mode: ContainerFenceAuto,
			nft: func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
				if len(args) > 0 && args[0] == "-f" {
					return nil, errors.New("syntax error")
				}
				return nil, nil
			},
		},
		"auto, a git proxy that is not an address": {
			mode: ContainerFenceAuto,
			nft:  func(ctx context.Context, stdin string, args ...string) ([]byte, error) { return nil, nil },
			cfg:  ContainerConfig{GitProxyHost: "proxy.example"},
		},
		"require, no nft": {
			mode: ContainerFenceRequire,
			nft:  func(ctx context.Context, stdin string, args ...string) ([]byte, error) { return nil, cannotRun },
		},
	} {
		t.Run(name, func(t *testing.T) {
			withHostFirewall(t, tc.nft, fixedNetworks("172.20.0.2|16|172.20.0.1|||\n"))
			withConnectedPrefixes(t)
			docker, log := fenceDocker(t)
			cfg := tc.cfg
			cfg.Docker, cfg.Image, cfg.Workspace, cfg.Network, cfg.Fence = docker, "img", "/w", "mine", tc.mode
			c := NewContainerSandboxes(cfg)
			if _, err := c.Acquire(t.Context(), "1-1", Shape{}); err == nil || !strings.Contains(err.Error(), "fencing") {
				t.Fatalf("Acquire = %v, want a refusal naming the fence", err)
			}
			data, _ := os.ReadFile(log)
			calls := strings.Split(strings.TrimSpace(string(data)), "\n")
			if last := calls[len(calls)-1]; last != "rm -f grain-sandbox-1-1" {
				t.Errorf("docker calls %q: want the unfenced container removed", calls)
			}
			if c.SandboxCount() != 0 {
				t.Errorf("an unfenced container was handed over")
			}
		})
	}
}

// A host that cannot run nft at all keeps its container sandboxes under
// auto, unfenced -- and says so, which is what admission refuses
// external capability grants on. off does the same without asking nft.
func TestContainerFenceUnfencedHosts(t *testing.T) {
	for name, tc := range map[string]struct {
		mode ContainerFenceMode
		why  string
	}{
		"auto, no nft": {ContainerFenceAuto, "cannot run nft"},
		"off":          {ContainerFenceOff, "-container-sandbox-fence=off"},
	} {
		t.Run(name, func(t *testing.T) {
			calls := withHostFirewall(t,
				func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
					return nil, errors.New("sudo: a password is required")
				},
				fixedNetworks("172.20.0.2|16|172.20.0.1|||\n"))
			docker, _ := fenceDocker(t)
			c := NewContainerSandboxes(ContainerConfig{Docker: docker, Image: "img", Workspace: "/w", Network: "mine", Fence: tc.mode})
			sb, err := c.Acquire(t.Context(), "1-1", Shape{NetworkAccess: model.NetworkOpen})
			if err != nil {
				t.Fatalf("Acquire = %v, want an unfenced container on a host with no fence", err)
			}
			if script := nftScript(*calls); script != "" {
				t.Errorf("an unfenced host was sent a ruleset:\n%s", script)
			}
			if why := c.OpenNetworkUnfenced(t.Context()); !strings.Contains(why, tc.why) {
				t.Errorf("OpenNetworkUnfenced = %q, want it to say %q", why, tc.why)
			}
			_ = sb.Release(t.Context())
			if tc.mode == ContainerFenceOff && len(*calls) != 0 {
				t.Errorf("off ran nft: %q", *calls)
			}
		})
	}
}

func TestParseContainerFenceMode(t *testing.T) {
	for in, want := range map[string]ContainerFenceMode{"": ContainerFenceAuto, "auto": ContainerFenceAuto,
		"require": ContainerFenceRequire, " off ": ContainerFenceOff} {
		if got, err := ParseContainerFenceMode(in); err != nil || got != want {
			t.Errorf("ParseContainerFenceMode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseContainerFenceMode("on"); err == nil {
		t.Error(`ParseContainerFenceMode("on") succeeded`)
	}
}
