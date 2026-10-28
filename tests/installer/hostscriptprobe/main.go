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

// Command hostscriptprobe drives an installed grain hostd's host-script
// entry for tests/installer's hostd test, run as the installing user, who
// alone may open hostd's socket. It attaches the entry as a run: host
// capability server, mints one grant of a script that reports `id -u`,
// checks the grant's port is owned by the process hostd reported starting
// -- the check grain's port relay makes before it forwards a port
// (portforward.ServiceFor, Service.Contains) -- calls it the way
// `grain host-script whoami` does, releases the grant and calls it again. It prints what
// it saw as JSON on stdout.
//
//	hostscriptprobe <hostd socket> <allowlist entry> <scratch directory>
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/portforward"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/hostscriptcli"
	"github.com/ai-on-gke/grain/pkg/model"
)

// ID is the capability id the probe attaches the entry under.
const ID = "https://github.com/ai-on-gke/grain/capabilities/host-script"

// Report is what the probe prints.
type Report struct {
	ServicePID int `json:"servicePid"`
	// ListenerOwner is the process owning the grant's port, and
	// PortForwardable whether it belongs to the service ServicePID is.
	ListenerOwner   int    `json:"listenerOwner"`
	PortForwardable bool   `json:"portForwardable"`
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	Exit            int    `json:"exit"`
	ExitAfter       int    `json:"exitAfterRelease"`
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: hostscriptprobe <hostd socket> <allowlist entry> <scratch directory>")
		os.Exit(2)
	}
	r, err := probe(os.Args[1], os.Args[2], os.Args[3])
	if err != nil {
		fmt.Fprintln(os.Stderr, "hostscriptprobe:", err)
		os.Exit(1)
	}
	_ = json.NewEncoder(os.Stdout).Encode(r)
}

func probe(socket, entry, scratch string) (Report, error) {
	var r Report
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := hostd.Client{Socket: socket}
	srv, err := protocol.Attach(ctx, ID, protocol.ServerConfig{
		Command: entry, Run: protocol.RunHost,
		Config: map[string]any{"scripts": []any{map[string]any{
			"name": "whoami", "description": "reports who it runs as",
			"script": `id -u; printf '%s\n' "$1"`, "timeout": 10,
		}}},
	}, func(string) (string, bool) { return "", false }, protocol.DialOptions{Hostd: &client, Stderr: os.Stderr})
	if err != nil {
		return r, fmt.Errorf("attaching %s through hostd: %w", entry, err)
	}
	defer srv.Close()
	lookup := func(string) (*protocol.Server, error) { return srv, nil }

	grants, err := model.CapabilityGrants([]model.Grant{{Capability: ID + `#host-script={"script":"whoami"}`}}, lookup)
	if err != nil {
		return r, err
	}
	pass, err := protocol.MintPass(ctx, grants, func(string) (protocol.Minter, error) { return srv, nil },
		protocol.PassOptions{Deployment: "installer", Task: "1", Attempt: 1, Deadline: time.Now().Add(time.Hour)})
	if err != nil {
		return r, fmt.Errorf("minting: %w", err)
	}
	released := false
	defer func() {
		if !released {
			_, _ = pass.Release(context.Background())
		}
	}()
	ports := pass.Ports()
	if len(ports) != 1 {
		return r, fmt.Errorf("the grant has %d ports, want 1", len(ports))
	}

	// What the relay checks before it forwards a grant's port: both direct
	// /proc/<pid>/fd inspection (possible because grain restores
	// PR_SET_DUMPABLE) and asking hostd (OpSocketOwner, which the daemon
	// uses when hostd runs under another uid), plus opening the forward on
	// a relay wired to hostd.
	pid, url := srv.ServiceProcess()
	r.ServicePID = pid
	svc, err := portforward.ServiceFor(pid, url)
	if err != nil {
		return r, fmt.Errorf("the server's service: %w", err)
	}
	if r.ListenerOwner, err = portforward.ListenerOwner(ports[0].HostPort); err != nil {
		return r, fmt.Errorf("the owner of port %d: %w", ports[0].HostPort, err)
	}
	inode, err := portforward.ListenerInode(ports[0].HostPort)
	if err != nil {
		return r, fmt.Errorf("the socket inode of port %d: %w", ports[0].HostPort, err)
	}
	hostdOwner, err := client.SocketOwner(ctx, inode)
	if err != nil {
		return r, fmt.Errorf("hostd socket owner of port %d (inode %d): %w", ports[0].HostPort, inode, err)
	}
	relay := portforward.NewRelay()
	relay.InodeOwner = client.SocketOwner
	fwd, err := relay.Open(ctx, ports[0].HostPort, svc)
	if err != nil {
		return r, fmt.Errorf("opening relay forward for port %d: %w", ports[0].HostPort, err)
	}
	fwd.Close()
	r.PortForwardable = svc.Contains(r.ListenerOwner) && svc.Contains(hostdOwner)

	// `grain host-script whoami`, run here rather than in a sandbox: the
	// host is where the port it calls is. The grant's descriptor goes in
	// scratch, standing in for the sandbox's hostscriptcli.Dir.
	placed := false
	for _, pl := range pass.Placements() {
		if pl.Path == hostscriptcli.Dir+"/whoami.json" {
			if err := os.WriteFile(filepath.Join(scratch, "whoami.json"), []byte(pl.Content), 0o600); err != nil {
				return r, err
			}
			placed = true
		}
	}
	if !placed {
		return r, errors.New("the grant placed no descriptor for whoami")
	}
	var stdout, stderr bytes.Buffer
	r.Exit = hostscriptcli.Run(scratch, []string{"whoami", "$(id -u) literal"}, &stdout, &stderr)
	r.Stdout, r.Stderr = stdout.String(), stderr.String()

	if _, err := pass.Release(ctx); err != nil {
		return r, fmt.Errorf("releasing: %w", err)
	}
	released = true
	r.ExitAfter = hostscriptcli.Run(scratch, []string{"whoami"}, &bytes.Buffer{}, &bytes.Buffer{})
	return r, nil
}
