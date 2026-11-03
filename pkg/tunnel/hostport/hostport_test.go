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

package hostport

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"
	"time"
)

func echoServer(t *testing.T, tag string) string {
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
			go func() {
				defer c.Close()
				line, _ := bufio.NewReader(c).ReadString('\n')
				fmt.Fprint(c, tag+":"+line)
			}()
		}
	}()
	return ln.Addr().String()
}

func freePort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func roundTrip(t *testing.T, port int) (string, error) {
	t.Helper()
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 5*time.Second)
	if err != nil {
		return "", err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprint(c, "hi\n")
	return bufio.NewReader(c).ReadString('\n')
}

func dialTo(addr string) func(ctx context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}
}

// syncOnFreePort has f forward a port nothing else holds to dial, and
// returns it. freePort's port is free only until it returns, so another
// listener -- this package's echo servers, or any other test process --
// can take it before Sync binds it; that is retried on a fresh port
// rather than reported as the forwarder's failure.
func syncOnFreePort(t *testing.T, f *Forwarder, dial func(ctx context.Context) (net.Conn, error)) int {
	t.Helper()
	var errs map[int]error
	for range 10 {
		port := freePort(t)
		if errs = f.Sync(map[int]func(ctx context.Context) (net.Conn, error){port: dial}); len(errs) == 0 {
			return port
		}
		if !errors.Is(errs[port], syscall.EADDRINUSE) {
			break
		}
	}
	t.Fatal(errs)
	return 0
}

func TestForwarderForwardsAndRepoints(t *testing.T) {
	f := New("127.0.0.1")
	t.Cleanup(f.Close)
	// Both dial targets listen before a port is picked for the forwarder,
	// so neither can be handed the port it is about to bind.
	one, two := echoServer(t, "one"), echoServer(t, "two")
	port := syncOnFreePort(t, f, dialTo(one))
	if got, err := roundTrip(t, port); err != nil || got != "one:hi\n" {
		t.Fatalf("first = %q, %v", got, err)
	}
	// A restarted sandbox is a new dial target behind the same listener.
	f.Sync(map[int]func(ctx context.Context) (net.Conn, error){port: dialTo(two)})
	if got, err := roundTrip(t, port); err != nil || got != "two:hi\n" {
		t.Fatalf("after repointing = %q, %v", got, err)
	}
	// A sandbox that is down refuses the connection rather than hanging.
	f.Sync(map[int]func(ctx context.Context) (net.Conn, error){port: func(context.Context) (net.Conn, error) {
		return nil, errors.New("restarting")
	}})
	if got, err := roundTrip(t, port); err == nil && got != "" {
		t.Fatalf("a down sandbox answered %q", got)
	}
	f.Sync(nil)
	if f.Addr(port) != nil {
		t.Fatal("listener kept after it was no longer wanted")
	}
}

func TestForwarderReportsAPortInUse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	f := New("127.0.0.1")
	t.Cleanup(f.Close)
	if errs := f.Sync(map[int]func(ctx context.Context) (net.Conn, error){port: dialTo("127.0.0.1:1")}); errs[port] == nil {
		t.Fatal("binding a taken port reported no error")
	}
}
