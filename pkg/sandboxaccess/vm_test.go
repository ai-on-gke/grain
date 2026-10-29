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

package sandboxaccess

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/granule/pkg/sandbox"
)

// TestVM_GuestCannotReachAcceptor is the acceptor end of
// scripts/sandbox-access-guest-probe.sh, which boots a real guest under
// granule-shim with CHV_NET_MODE=inline and its wrapper dialling out to
// this acceptor, in network namespaces laid out like a pod and grain's
// pod. It is skipped anywhere else.
//
// It mints the sandbox's credentials into SANDBOXACCESS_VM_DIR/access
// for granule-shim to start with, waits for the wrapper to connect, and
// then from inside the guest -- which owns the pod's address and has
// open egress -- connects to the acceptor, which must fail, and to a
// control listener on a sibling address and the same port, which must
// succeed: the guest has a route, and only the acceptor's address is
// dropped (granule's netshim controllerDropRules).
//
//	SANDBOXACCESS_VM_DIR      work directory shared with the script
//	SANDBOXACCESS_VM_LISTEN   the acceptor's address (ip:port)
//	SANDBOXACCESS_VM_CONTROL  the control listener's address (ip:port)
func TestVM_GuestCannotReachAcceptor(t *testing.T) {
	dir := os.Getenv("SANDBOXACCESS_VM_DIR")
	if dir == "" {
		t.Skip("SANDBOXACCESS_VM_DIR unset; run scripts/sandbox-access-guest-probe.sh")
	}
	listen := os.Getenv("SANDBOXACCESS_VM_LISTEN")
	control := os.Getenv("SANDBOXACCESS_VM_CONTROL")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	ctl, err := net.Listen("tcp", control)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	go func() {
		for {
			c, err := ctl.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("control\n"))
			_ = c.Close()
		}
	}()

	ca, err := NewCA(testDeployment, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	host, _, _ := net.SplitHostPort(listen)
	a, err := Listen(Config{Deployment: testDeployment, CA: ca, ServerNames: []string{host}}, listen)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = a.Serve(ctx) }()
	defer a.Close()

	const name = "sandbox-vm"
	creds, err := a.Mint(name, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	access := filepath.Join(dir, "access")
	if err := os.MkdirAll(access, 0o700); err != nil {
		t.Fatal(err)
	}
	for file, data := range creds.SecretData() {
		if err := os.WriteFile(filepath.Join(access, file), data, 0o400); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "minted"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.WriteFile(filepath.Join(dir, "done"), nil, 0o644)

	cli, err := a.WaitClient(ctx, name)
	if err != nil {
		t.Fatalf("the wrapper never connected: %v", err)
	}
	t.Logf("wrapper connected; channels %v", a.Connected())

	probe := func(addr string) string {
		h, p, _ := net.SplitHostPort(addr)
		line := fmt.Sprintf(`if ! command -v nc >/dev/null; then echo NO-NC; elif echo | nc -w 5 %s %s >/dev/null 2>&1; then echo CONNECTED; else echo FAILED; fi`, h, p)
		var out bytes.Buffer
		if _, err := cli.Exec(ctx, sandbox.ExecRequest{Line: line}, nil, &out, io.Discard); err != nil {
			t.Fatalf("guest probe of %s: %v", addr, err)
		}
		return strings.TrimSpace(out.String())
	}
	if got := probe(control); got != "CONNECTED" {
		t.Fatalf("guest connection to the control listener %s = %q, want CONNECTED (the guest needs a route for the next check to mean anything)", control, got)
	}
	if got := probe(listen); got != "FAILED" {
		t.Fatalf("guest connection to the acceptor %s = %q, want FAILED", listen, got)
	}
	t.Logf("guest reached the control listener %s and not the acceptor %s", control, listen)

	// And the wrapper's own channel is untouched by the drop.
	var out bytes.Buffer
	if code, err := cli.Exec(ctx, sandbox.ExecRequest{Argv: []string{"echo", "still-here"}}, nil, &out, io.Discard); err != nil || code != 0 || strings.TrimSpace(out.String()) != "still-here" {
		t.Fatalf("exec after the probes = %d, %v, %q", code, err, out.String())
	}
}
