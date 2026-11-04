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
	"fmt"
	"log"
	"net"
	"strings"
)

// ContainerFenceMode is whether the container backend fences its
// sandboxes off from this host, its networks and link-local (the
// metadata server among them) with the same per-sandbox nftables chain
// an open-network granule VM gets (openFence).
type ContainerFenceMode string

const (
	// ContainerFenceAuto fences every container when this process can
	// run nft, and otherwise runs them unfenced and refuses external
	// capability grants. Which of the two is decided once, by the first
	// question asked (fenceEnabled), and holds for the process: once the
	// fence is on, a container whose chain cannot be installed is not
	// handed over, so a grant admitted against the fence never lands in
	// a sandbox without one. The empty mode is this one.
	ContainerFenceAuto ContainerFenceMode = "auto"
	// ContainerFenceRequire fences every container and refuses one
	// whose chain cannot be installed, nft missing included.
	ContainerFenceRequire ContainerFenceMode = "require"
	// ContainerFenceOff fences nothing, as this backend did before it
	// could, and refuses external capability grants.
	ContainerFenceOff ContainerFenceMode = "off"
)

// ParseContainerFenceMode reads -container-sandbox-fence. Empty is
// ContainerFenceAuto.
func ParseContainerFenceMode(s string) (ContainerFenceMode, error) {
	switch m := ContainerFenceMode(strings.TrimSpace(s)); m {
	case "", ContainerFenceAuto:
		return ContainerFenceAuto, nil
	case ContainerFenceRequire, ContainerFenceOff:
		return m, nil
	default:
		return "", fmt.Errorf("container sandbox fence %q is not one of auto, require or off", s)
	}
}

// unfencedOpenNetwork is implemented by a Sandboxes backend whose
// open-network sandboxes may be left able to reach this host and its
// networks: OpenNetworkUnfenced says why, or "" when they are fenced.
// mintCapabilityPass refuses external capability grants on such a
// backend, as it does on an unisolated one, since nothing then keeps a
// capability server's control plane out of a sandbox's reach (§3.2.4).
type unfencedOpenNetwork interface {
	OpenNetworkUnfenced(ctx context.Context) string
}

// openNetworkUnfenced is unfencedOpenNetwork's answer for sandboxes, ""
// for a backend that does not implement it.
func openNetworkUnfenced(ctx context.Context, sandboxes Sandboxes) string {
	u, ok := sandboxes.(unfencedOpenNetwork)
	if !ok {
		return ""
	}
	return u.OpenNetworkUnfenced(ctx)
}

// OpenNetworkUnfenced implements unfencedOpenNetwork.
func (c *ContainerSandboxes) OpenNetworkUnfenced(ctx context.Context) string {
	on, why := c.fenceEnabled(ctx)
	if on {
		return ""
	}
	return why
}

// fenceEnabled reports whether this backend fences its containers, and
// when it does not, why. Auto asks nft once whether it can be run at all
// (`nft list tables`, with runNFT's sudo fallback) and keeps the answer:
// a host that cannot run it is not fenced, and any other answer --
// including nft running and failing -- is, so a broken fence fails each
// Acquire rather than quietly opening every sandbox.
func (c *ContainerSandboxes) fenceEnabled(ctx context.Context) (bool, string) {
	switch c.cfg.Fence {
	case ContainerFenceRequire:
		return true, ""
	case ContainerFenceOff:
		return false, "this deployment runs its sandbox containers unfenced (-container-sandbox-fence=off), so they can reach grain's own host and its networks, capability servers' control planes included"
	}
	c.fenceMu.Lock()
	defer c.fenceMu.Unlock()
	if !c.fenceProbed {
		c.fenceProbed = true
		c.fenceOn, c.fenceWhy = true, ""
		if _, err := hostNFT(ctx, "", "list", "tables"); nftCannotRun(err) {
			c.fenceOn = false
			c.fenceWhy = "this deployment's sandbox containers are unfenced because grain cannot run nft on its host, so they can reach grain's own host and its networks, capability servers' control planes included"
			log.Printf("orchestrator: container sandboxes are not fenced off from this host, its networks or the metadata server, "+
				"and external capability grants are refused: nft cannot be run here (%v). Install nftables and give grain "+
				"CAP_NET_ADMIN or `sudo -n nft`, then restart, or set -container-sandbox-fence=require to refuse sandboxes instead", err)
		} else {
			log.Print("orchestrator: container sandboxes are fenced off from this host, its networks and link-local by a per-container nftables chain")
		}
	}
	return c.fenceOn, c.fenceWhy
}

// containerChain is the nftables chain a sandbox container is fenced by.
func containerChain(container string) string {
	return "ct_" + sanitizeNFTName(container)
}

// fence installs container's open-network chain when this backend
// fences at all: granuleIsolationRuleset with an openFence tail, as an
// open granule VM gets, accepting the gateway and git proxy only on the
// sandbox service ports, DNS anywhere, then dropping link-local,
// loopback, every prefix the host has an address in and the container's
// own segment by destination, jumped to by source address and by the
// container's bridge port alike. Every failure is returned, and the
// caller does not hand the container over.
func (c *ContainerSandboxes) fence(ctx context.Context, container string) error {
	if on, _ := c.fenceEnabled(ctx); !on {
		return nil
	}
	var gitProxy net.IP
	if c.cfg.GitProxyHost != "" {
		if gitProxy = net.ParseIP(c.cfg.GitProxyHost); gitProxy == nil {
			return fmt.Errorf("orchestrator: fencing sandbox container %q: the git proxy host %q is not an IP address, so no host firewall rule can allow it (-container-sandbox-fence=off runs containers unfenced)", container, c.cfg.GitProxyHost)
		}
	}
	nets, err := hostGuestNetworks(ctx, container)
	if err != nil {
		return fmt.Errorf("orchestrator: fencing sandbox container %q: finding the addresses to firewall: %w", container, err)
	}
	ports, err := hostGuestPorts(ctx, container)
	if err != nil {
		return fmt.Errorf("orchestrator: fencing sandbox container %q: finding the bridge ports to firewall: %w", container, err)
	}
	connected, err := hostConnectedPrefixes()
	if err != nil {
		return fmt.Errorf("orchestrator: fencing sandbox container %q: finding the networks this host is on: %w", container, err)
	}
	var services []SandboxServicePort
	allPorts := c.cfg.SandboxServicePorts == nil
	if !allPorts {
		services = c.cfg.SandboxServicePorts()
	}
	chain := containerChain(container)
	ips := make([]string, 0, len(nets))
	for _, n := range nets {
		ips = append(ips, n.IP.String())
	}

	nftLock.Lock()
	defer nftLock.Unlock()
	// Whatever last held this name, these addresses or these ports --
	// a container this one replaces, or one a crashed daemon left a
	// chain behind for -- has its jumps removed first.
	cleanupGranuleHostIsolationLocked(ctx, chain, ips, ports)
	script := granuleIsolationRuleset(chain, nets, ports, gitProxy, services, allPorts, &openFence{Connected: connected})
	if _, err := hostNFT(ctx, script, "-f", "-"); err != nil {
		return fmt.Errorf("orchestrator: fencing sandbox container %q: installing nftables chain %s: %w", container, chain, err)
	}
	return nil
}

// unfence removes container's chain, if this backend fences at all.
// Best-effort, like the granule backend's: a chain left behind holds
// jumps from addresses and ports the next fence clears before it
// installs its own.
func (c *ContainerSandboxes) unfence(ctx context.Context, container string) {
	if on, _ := c.fenceEnabled(ctx); !on {
		return
	}
	nftLock.Lock()
	defer nftLock.Unlock()
	cleanupGranuleHostIsolationLocked(ctx, containerChain(container), nil, nil)
}
