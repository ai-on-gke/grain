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
	"encoding/base64"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/portforward"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/mcp"
)

// commandSandbox is a Sandbox that records every command run in it and
// answers each from answer (success by default).
type commandSandbox struct {
	mu     sync.Mutex
	runs   [][]string
	stdins []string
	answer func(argv []string) (string, string, int)
}

func (s *commandSandbox) Name() string                              { return "run-1" }
func (s *commandSandbox) Tools(context.Context) ([]mcp.Tool, error) { return nil, nil }
func (s *commandSandbox) ConfigureGitCredentials(context.Context, string, string, mcp.GitIdentity) error {
	return nil
}
func (s *commandSandbox) Release(context.Context) error { return nil }
func (s *commandSandbox) RunInSandbox(_ context.Context, argv []string, stdin string) (string, string, int) {
	s.mu.Lock()
	s.runs = append(s.runs, argv)
	s.stdins = append(s.stdins, stdin)
	s.mu.Unlock()
	if s.answer != nil {
		return s.answer(argv)
	}
	return "", "", 0
}

// describe names each recorded command by what it did, in order.
func (s *commandSandbox) describe() []string {
	var out []string
	for _, argv := range s.runs {
		switch {
		case argv[0] == "python3":
			out = append(out, "forward "+argv[2])
		case argv[0] == "sudo":
			out = append(out, "place-as-root "+argv[6]+" "+argv[8])
		case argv[2] == placeScript:
			out = append(out, "place "+argv[4])
		case argv[2] == setupScript:
			out = append(out, "setup "+argv[4])
		default:
			out = append(out, strings.Join(argv, " "))
		}
	}
	return out
}

func testPass(responses ...*protocol.MintResponse) *protocol.Pass {
	p := &protocol.Pass{}
	for i, r := range responses {
		r.Status = protocol.StatusOK
		p.Grants = append(p.Grants, protocol.MintedGrant{
			Grant:     protocol.Grant{Ref: protocol.Ref{CapabilityID: passTestCap, Method: "gcp-key"}},
			AttemptID: "grain-task-1-1-" + string(rune('0'+i)),
			Response:  r,
		})
	}
	return p
}

// listenerOfThisProcess is a loopback listener owned by the test process,
// standing in for a capability server's data-plane listener.
func listenerOfThisProcess(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			// Held open, as a real data-plane server holds it: the
			// ownership check reads the socket's far end.
			t.Cleanup(func() { c.Close() })
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestMaterializePassRunsPortsThenPlacementsThenSetup(t *testing.T) {
	if _, err := os.Stat("/proc/net/tcp"); err != nil {
		t.Skip("socket ownership is read from /proc")
	}
	hostPort := listenerOfThisProcess(t)
	pass := testPass(
		&protocol.MintResponse{
			Placements: []protocol.Placement{{Path: "/home/debian/.config/a", Content: "secret-a"}},
			Ports:      []protocol.Port{{HostPort: hostPort, SandboxPort: 9728}},
			Setup:      "/home/debian/.a/setup",
		},
		&protocol.MintResponse{
			Placements: []protocol.Placement{{Path: "/etc/b.conf", Mode: "0644", Owner: "root", Content: "b"}},
			SetupRoot:  "/home/debian/.b/setup-root",
		},
	)
	relay := portforward.NewRelay()
	cfg := Config{CapabilityPorts: relay, CapabilityPortsURL: "http://172.17.0.1:40003"}
	sb := &commandSandbox{}
	self := func(string) (portforward.Service, error) { return portforward.Service{PID: os.Getpid()}, nil }
	m, err := materializePass(context.Background(), cfg, sb, pass, time.Now().Add(time.Hour), self)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	want := []string{
		"place " + capabilityPortsDir + "/forwarder.py",
		"place " + capabilityPortsDir + "/9728.token",
		"forward 9728",
		"place /home/debian/.config/a",
		"place-as-root /etc/b.conf root",
		"setup /home/debian/.b/setup-root",
		"setup /home/debian/.a/setup",
	}
	if got := sb.describe(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if forward := sb.runs[2]; forward[3] != "172.17.0.1" || forward[4] != "40003" {
		t.Fatalf("the guest forwarder dials %v, not the relay", forward)
	}
	if sb.stdins[3] != base64.StdEncoding.EncodeToString([]byte("secret-a")) {
		t.Fatalf("placement content was not handed over: %q", sb.stdins[3])
	}

	// recreate_sandbox applies the same pass to the new VM, minting
	// nothing and reusing the forwards it already holds.
	again := &commandSandbox{}
	if err := m.apply(context.Background(), again); err != nil {
		t.Fatal(err)
	}
	if got := again.describe(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("re-applied commands:\n%s", strings.Join(got, "\n"))
	}
	if again.stdins[1] != sb.stdins[1] {
		t.Fatal("a recreated sandbox was given a different forward token")
	}
}

func TestMaterializePassRefusesPortsItCannotForward(t *testing.T) {
	pass := testPass(&protocol.MintResponse{Ports: []protocol.Port{{HostPort: 9728, SandboxPort: 9728}}})
	_, err := materializePass(context.Background(), Config{}, &commandSandbox{}, pass, time.Time{},
		func(string) (portforward.Service, error) { return portforward.Service{}, nil })
	var refused *protocol.RefusedError
	if !errors.As(err, &refused) || !strings.Contains(refused.Reason, "no capability port relay") {
		t.Fatalf("ports with no relay = %v", err)
	}
}

func TestMaterializePassStopsAtTheFirstFailingSetup(t *testing.T) {
	pass := testPass(
		&protocol.MintResponse{Setup: "/home/debian/.a/setup"},
		&protocol.MintResponse{Setup: "/home/debian/.b/setup"},
	)
	sb := &commandSandbox{answer: func(argv []string) (string, string, int) {
		if argv[2] == setupScript && argv[4] == "/home/debian/.a/setup" {
			return "cloning...\nfatal: no such repo\n", "", 3
		}
		return "", "", 0
	}}
	_, err := materializePass(context.Background(), Config{}, sb, pass, time.Time{}, nil)
	var setup *CapabilitySetupError
	if !errors.As(err, &setup) || setup.ExitCode != 3 || setup.AttemptID != "grain-task-1-1-0" {
		t.Fatalf("setup failure = %#v", err)
	}
	if len(sb.runs) != 1 {
		t.Fatalf("setup went on after a failure: %v", sb.describe())
	}
	body, source := capabilitySetupComment(err)
	for _, want := range []string{"/home/debian/.a/setup", "exited with status 3", "fatal: no such repo", "untrusted text"} {
		if !strings.Contains(body, want) {
			t.Errorf("the task's report does not say %q:\n%s", want, body)
		}
	}
	if !strings.HasPrefix(source, "capability:") {
		t.Errorf("the report is attributed to %q, not the capability", source)
	}
}

func TestSetupCommandsAreBoundedByTheGrantsDeadline(t *testing.T) {
	pass := testPass(&protocol.MintResponse{Setup: "/home/debian/.a/setup"})
	sb := &commandSandbox{}
	now := time.Now()
	m := &passMaterialization{pass: pass, deadline: now.Add(90 * time.Second), timeout: DefaultCapabilitySetupTimeout,
		now: func() time.Time { return now }}
	if err := m.runSetup(context.Background(), sb); err != nil {
		t.Fatal(err)
	}
	if secs := sb.runs[0][5]; secs != "90" {
		t.Fatalf("setup timeout = %ss, want the 90s left before the deadline", secs)
	}
	m.deadline = now
	if err := m.runSetup(context.Background(), sb); err == nil {
		t.Fatal("a setup command ran past the grants' deadline")
	}
}

// The two scripts are run for real, against a temporary directory.
func TestPlaceScriptWritesAtomicallyWithItsMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "key.json")
	cmd := exec.Command("sh", "-c", placeScript, "sh", path, "0640", "")
	cmd.Stdin = strings.NewReader(base64.StdEncoding.EncodeToString([]byte("{\"k\":1}")))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "{\"k\":1}" {
		t.Fatalf("placed %q, %v", data, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".grain-placement.*")); len(left) != 0 {
		t.Fatalf("temporary files left behind: %v", left)
	}
}

func TestSetupScriptRunsInTheRootWithACleanEnvironment(t *testing.T) {
	if _, err := exec.LookPath("timeout"); err != nil {
		t.Skip("no timeout(1)")
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	script := filepath.Join(dir, "setup")
	os.WriteFile(script, []byte("#!/bin/sh\npwd\necho \"token=${GRAIN_TOKEN:-none}\"\nread x && echo stdin-open\nexit 7\n"), 0o700)
	cmd := exec.Command("sh", "-c", setupScript, "sh", script, "30", root, "65536")
	cmd.Env = append(os.Environ(), "GRAIN_TOKEN=leaked")
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("exit = %v: %s", err, out)
	}
	for _, want := range []string{root + "\n", "token=none\n"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("output %q does not carry %q", out, want)
		}
	}
	if strings.Contains(string(out), "stdin-open") {
		t.Error("the setup command's stdin was not closed")
	}

	os.WriteFile(script, []byte("#!/bin/sh\nsleep 30\n"), 0o700)
	out, err = exec.Command("sh", "-c", setupScript, "sh", script, "1", root, "65536").CombinedOutput()
	if !errors.As(err, &exit) || exit.ExitCode() != 124 {
		t.Fatalf("a command past its timeout exited %v: %s", err, out)
	}

	os.Chmod(script, 0o600)
	out, err = exec.Command("sh", "-c", setupScript, "sh", script, "1", root, "65536").CombinedOutput()
	if !errors.As(err, &exit) || exit.ExitCode() != 126 || !strings.Contains(string(out), "not an executable") {
		t.Fatalf("a non-executable command exited %v: %s", err, out)
	}
}

// channelSandbox is a commandSandbox whose capability ports run over a
// dial-out access channel (k8sGranuleSandbox in dialout mode).
type channelSandbox struct {
	commandSandbox
	ports  []int
	serves []func(context.Context, net.Conn)
}

func (s *channelSandbox) UsesChannelPortForward() bool { return true }
func (s *channelSandbox) ForwardCapabilityPort(_ context.Context, sandboxPort int, serve func(context.Context, net.Conn)) error {
	s.ports = append(s.ports, sandboxPort)
	s.serves = append(s.serves, serve)
	return nil
}

// A port forward carried over the dial-out channel goes through the
// relay's forward rather than straight to host_port: no guest forwarder or
// token is placed, and the grant's Close cuts a connection the channel is
// carrying, as it does a guest forwarder's (docs/capabilities-spec.md
// §5.3.4, docs/design/grain-on-gke.md#GKE-3).
func TestMaterializePassForwardsChannelPortsThroughTheRelaysForward(t *testing.T) {
	if _, err := os.Stat("/proc/net/tcp"); err != nil {
		t.Skip("socket ownership is read from /proc")
	}
	hostPort := listenerOfThisProcess(t)
	pass := testPass(&protocol.MintResponse{Ports: []protocol.Port{{HostPort: hostPort, SandboxPort: 9728}}})
	cfg := Config{CapabilityPorts: portforward.NewRelay(), CapabilityPortsURL: "http://172.17.0.1:40003"}
	sb := &channelSandbox{}
	self := func(string) (portforward.Service, error) { return portforward.Service{PID: os.Getpid()}, nil }
	m, err := materializePass(context.Background(), cfg, sb, pass, time.Now().Add(time.Hour), self)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if got := sb.describe(); len(got) != 0 {
		t.Fatalf("a channel forward ran guest commands: %v", got)
	}
	if len(sb.ports) != 1 || sb.ports[0] != 9728 {
		t.Fatalf("forwarded sandbox ports %v, want [9728]", sb.ports)
	}

	guest, inner := net.Pipe()
	defer guest.Close()
	go sb.serves[0](context.Background(), inner)
	guest.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(guest, "x"); err != nil {
		t.Fatalf("the channel connection was not bridged to host_port: %v", err)
	}
	m.Close()
	if _, err := guest.Read(make([]byte, 1)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("a channel connection survived its grant being closed: %v", err)
	}
}
