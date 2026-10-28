package hostport

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
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

func TestForwarderForwardsAndRepoints(t *testing.T) {
	f := New("127.0.0.1")
	t.Cleanup(f.Close)
	port := freePort(t)
	if errs := f.Sync(map[int]func(ctx context.Context) (net.Conn, error){port: dialTo(echoServer(t, "one"))}); len(errs) != 0 {
		t.Fatal(errs)
	}
	if got, err := roundTrip(t, port); err != nil || got != "one:hi\n" {
		t.Fatalf("first = %q, %v", got, err)
	}
	// A restarted sandbox is a new dial target behind the same listener.
	f.Sync(map[int]func(ctx context.Context) (net.Conn, error){port: dialTo(echoServer(t, "two"))})
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
