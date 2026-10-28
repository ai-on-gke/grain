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

package portforward

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestParseHexAddr(t *testing.T) {
	got, err := parseHexAddr("0100007F:1F90")
	if err != nil {
		t.Fatal(err)
	}
	if want := netip.MustParseAddrPort("127.0.0.1:8080"); got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
	got, err = parseHexAddr("0000000000000000FFFF00000100007F:0050")
	if err != nil {
		t.Fatal(err)
	}
	if want := netip.MustParseAddrPort("127.0.0.1:80"); got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func needProc(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("socket ownership is read from /proc")
	}
}

// echoListener serves an echo on a loopback port of this very process.
func echoListener(t *testing.T) int {
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
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestOwnershipIsCheckedOnTheEstablishedConnection(t *testing.T) {
	needProc(t)
	port := echoListener(t)
	if pid, err := ListenerOwner(port); err != nil || pid != os.Getpid() {
		t.Fatalf("ListenerOwner(%d) = %d, %v; want this process %d", port, pid, err, os.Getpid())
	}
	r := NewRelay()
	if _, err := r.Open(context.Background(), port, Service{PID: os.Getpid()}); err != nil {
		t.Fatalf("a listener of the service itself was refused: %v", err)
	}

	// A process outside the service: a child that is not this one's
	// ancestor, so this process's listener is not one of its.
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Skip("no sleep to stand in for another service:", err)
	}
	t.Cleanup(func() { child.Process.Kill(); child.Wait() })
	_, err := r.Open(context.Background(), port, Service{PID: child.Process.Pid})
	if err == nil || !strings.Contains(err.Error(), "not part of the capability server's own service") {
		t.Fatalf("a listener outside the service was not refused: %v", err)
	}
	if _, err := r.Open(context.Background(), 1, Service{PID: os.Getpid()}); err == nil {
		t.Fatal("a host_port with no listener was not refused")
	}
}

func TestRelayRefusesUnknownTokens(t *testing.T) {
	needProc(t)
	srv := httptest.NewServer(NewRelay())
	defer srv.Close()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "GET "+Path+" HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer nope\r\nConnection: Upgrade\r\nUpgrade: "+UpgradeProtocol+"\r\n\r\n")
	line, _ := bufio.NewReader(conn).ReadString('\n')
	if !strings.Contains(line, "403") {
		t.Fatalf("unknown token answered %q", line)
	}
}

// TestGuestForwarderEndToEnd runs the real guest script locally: a
// connection to its sandbox port reaches the host_port through the relay,
// and closing the forward cuts the connection.
func TestGuestForwarderEndToEnd(t *testing.T) {
	needProc(t)
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3 to run the guest forwarder")
	}
	hostPort := echoListener(t)
	relay := NewRelay()
	srv := httptest.NewServer(relay)
	defer srv.Close()
	f, err := relay.Open(context.Background(), hostPort, Service{PID: os.Getpid()})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "forwarder.py")
	tokenFile := filepath.Join(dir, "token")
	os.WriteFile(script, []byte(GuestForwarder), 0o600)
	os.WriteFile(tokenFile, []byte(f.Token()+"\n"), 0o600)
	host, rport, err := RelayAddress(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	sandboxPort := freePort(t)
	out, err := exec.Command("python3", script, strconv.Itoa(sandboxPort), host, strconv.Itoa(rport), tokenFile).CombinedOutput()
	if err != nil {
		t.Fatalf("forwarder did not start: %v: %s", err, out)
	}
	t.Cleanup(func() {
		if pid, err := ListenerOwner(sandboxPort); err == nil && pid != os.Getpid() {
			syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(sandboxPort)), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	io.WriteString(conn, "hello through the relay\n")
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || line != "hello through the relay\n" {
		t.Fatalf("echo through the forward = %q, %v", line, err)
	}

	f.Close()
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("a connection survived its forward being closed")
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// TestRelayFallsBackToInodeOwnerWhenProcFDsAreUnreadable covers a run: host
// capability server started by grain hostd under another uid: the daemon can
// read /proc/net/tcp and /proc/<pid>/status, but not /proc/<pid>/fd, so the
// relay asks InodeOwner (hostd's OpSocketOwner) and still checks Contains
// against the server's service, failing closed whenever InodeOwner refuses
// or names a process outside the service.
func TestRelayFallsBackToInodeOwnerWhenProcFDsAreUnreadable(t *testing.T) {
	needProc(t)
	// Point procRoot at a view of /proc that exposes /proc/net and
	// /proc/<pid>/status (world-readable on the host) without
	// /proc/<pid>/fd, matching what the daemon sees for another user's
	// process.
	fakeProc := t.TempDir()
	if err := os.Symlink("/proc/net", filepath.Join(fakeProc, "net")); err != nil {
		t.Fatal(err)
	}
	myPID := strconv.Itoa(os.Getpid())
	if err := os.Mkdir(filepath.Join(fakeProc, myPID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("/proc", myPID, "status"), filepath.Join(fakeProc, myPID, "status")); err != nil {
		t.Fatal(err)
	}
	saved := procRoot
	procRoot = fakeProc
	defer func() { procRoot = saved }()

	port := echoListener(t)
	svc := Service{PID: os.Getpid()}

	// With no InodeOwner, local /proc/<pid>/fd is unreadable and the
	// relay fails closed.
	r := NewRelay()
	if _, err := r.Open(context.Background(), port, svc); err == nil ||
		!strings.Contains(err.Error(), "could not be attributed to a process") {
		t.Fatalf("without InodeOwner, Open = %v; want attribution failure", err)
	}

	// An older or unreachable hostd fails closed.
	r.InodeOwner = func(context.Context, uint64) (int, error) {
		return 0, errors.New("grain hostd is on an older protocol that cannot check socket ownership")
	}
	if _, err := r.Open(context.Background(), port, svc); err == nil ||
		!strings.Contains(err.Error(), "older protocol") {
		t.Fatalf("with failing InodeOwner, Open = %v; want the hostd error", err)
	}

	// A socket hostd attributes to a process outside the service (here,
	// this process, which is child's parent rather than its descendant)
	// is still refused by Service.Contains.
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Skip("no sleep:", err)
	}
	t.Cleanup(func() { child.Process.Kill(); child.Wait() })
	r.InodeOwner = func(context.Context, uint64) (int, error) {
		return os.Getpid(), nil
	}
	if _, err := r.Open(context.Background(), port, Service{PID: child.Process.Pid}); err == nil ||
		!strings.Contains(err.Error(), "not part of the capability server's own service") {
		t.Fatalf("with another service's pid from InodeOwner, Open = %v; want service refusal", err)
	}

	// When InodeOwner resolves the socket inode to a process in svc,
	// both Open and a forwarded connection succeed.
	var asked uint64
	r.InodeOwner = func(_ context.Context, inode uint64) (int, error) {
		asked = inode
		return os.Getpid(), nil
	}
	f, err := r.Open(context.Background(), port, svc)
	if err != nil {
		t.Fatalf("with InodeOwner returning the service's pid, Open failed: %v", err)
	}
	defer f.Close()
	if asked == 0 {
		t.Fatal("InodeOwner was not handed the socket's inode from /proc/net/tcp")
	}
}
