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
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"
)

// DefaultContainerNetworkCreateArgs is the docker command line (after
// "docker") that creates DefaultContainerNetwork, shared by
// ContainerSandboxes.ensureNetwork and DefaultContainerNetworkGateway so
// both make the same network whichever gets there first.
func DefaultContainerNetworkCreateArgs() []string {
	return []string{"network", "create", "--driver", "bridge",
		"--label", "grain.sandbox=true",
		"--opt", "com.docker.network.bridge.enable_icc=false",
		DefaultContainerNetwork}
}

// DefaultContainerNetworkGateway is the IPv4 gateway of
// DefaultContainerNetwork, created first through docker (which runs one
// docker command and returns its stdout) if it does not exist yet. It is
// the address a sandbox container reaches this host at, so the one a
// daemon's -container-git-proxy-host has to be: `grain init workstation`
// writes it into daemon.sh, which without it exited at once
// ("-container-git-proxy-host is required"). It never guesses: a gateway
// docker does not report is an error.
func DefaultContainerNetworkGateway(ctx context.Context, docker func(ctx context.Context, args ...string) (string, error)) (string, error) {
	name := DefaultContainerNetwork
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	inspect := func() (string, error) {
		return docker(ctx, "network", "inspect", name, "-f", "{{json .IPAM.Config}}")
	}
	out, err := inspect()
	if err != nil {
		if _, createErr := docker(ctx, DefaultContainerNetworkCreateArgs()...); createErr != nil {
			return "", fmt.Errorf("creating docker network %s: %w", name, createErr)
		}
		if out, err = inspect(); err != nil {
			return "", fmt.Errorf("inspecting docker network %s: %w", name, err)
		}
	}
	var configs []struct {
		Gateway string `json:"Gateway"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &configs); err != nil {
		return "", fmt.Errorf("reading docker network %s's IPAM config: %w", name, err)
	}
	for _, c := range configs {
		if ip := net.ParseIP(c.Gateway); ip != nil && ip.To4() != nil {
			return c.Gateway, nil
		}
	}
	return "", fmt.Errorf("docker reports no IPv4 gateway for network %s", name)
}
