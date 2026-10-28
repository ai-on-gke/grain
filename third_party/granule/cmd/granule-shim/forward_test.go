package main

import (
	"bufio"
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/granule/pkg/agent"
	"github.com/bwsalmon/granule/pkg/guestforward"
)

// The caller-side transports run "granule <ShimMode>"; this binary has
// to answer to that name.
func TestShimModeMatchesTransport(t *testing.T) {
	if guestforward.ShimMode != "forward-stdio" {
		t.Fatalf("guestforward.ShimMode = %q, but the shim's mode is forward-stdio", guestforward.ShimMode)
	}
}

// stdioPair is the shim's stdin/stdout as the caller sees them: what the
// caller writes arrives on the shim's stdin, and the shim's stdout is
// what the caller reads.
type stdioPair struct {
	io.Reader
	io.WriteCloser
	closeRead func() error
}

func (p stdioPair) Close() error {
	_ = p.WriteCloser.Close()
	return p.closeRead()
}

// The whole chain short of docker and vsock: a Forwarder whose transport
// is the shim's stdio, relay copying it to a connection, and the real
// agent's Serve answering the Forward request on the other end of that
// connection -- in both directions.
func TestForwardStdioRelayToAgent(t *testing.T) {
	svc, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	go func() {
		for {
			c, err := svc.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()

	transport := func(ctx context.Context) (io.ReadWriteCloser, error) {
		guestSide, shimSide := net.Pipe()
		go func() { _ = agent.Serve(context.Background(), guestSide) }()

		stdinR, stdinW := io.Pipe()
		stdoutR, stdoutW := io.Pipe()
		go func() {
			defer shimSide.Close()
			defer stdoutW.Close()
			br, err := openForward(shimSide)
			if err != nil {
				stdoutW.CloseWithError(err)
				return
			}
			_ = relay(context.Background(), stdinR, stdoutW, br, shimSide)
		}()
		return stdioPair{Reader: stdoutR, WriteCloser: stdinW, closeRead: stdoutR.Close}, nil
	}

	f, err := guestforward.Start(context.Background(), guestforward.Options{
		Transport:    transport,
		ReadyTimeout: 5 * time.Second,
		Remote:       []guestforward.RemoteForward{{Guest: "127.0.0.1:0", Target: svc.Addr().String()}},
		Local:        []guestforward.LocalForward{{Listen: "127.0.0.1:0", Guest: svc.Addr().String()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	for _, b := range f.Bindings() {
		c, err := net.Dial("tcp", b.Listen)
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		msg := "through the " + b.Direction + " mapping"
		if _, err := c.Write([]byte(msg)); err != nil {
			t.Fatal(err)
		}
		_ = c.(*net.TCPConn).CloseWrite()
		got, err := io.ReadAll(c)
		c.Close()
		if err != nil || string(got) != msg {
			t.Fatalf("%s: got %q, %v", b.Direction, got, err)
		}
	}
}

// An agent that refuses the session -- here, a forward request carrying
// a command too -- is reported, not relayed into.
func TestOpenForwardReportsRefusal(t *testing.T) {
	guestSide, shimSide := net.Pipe()
	defer shimSide.Close()
	go func() {
		// An agent too old to know the field answers this way (see
		// execwire.ReadRequest); the shim has to pass the reason on.
		if _, err := bufio.NewReader(guestSide).ReadString('\n'); err != nil {
			return
		}
		_, _ = guestSide.Write([]byte(`{"ok":false,"error":"execwire: the request asks for something this agent does not understand: forward"}` + "\n"))
		_, _ = io.Copy(io.Discard, guestSide)
	}()
	_, err := openForward(shimSide)
	if err == nil || !strings.Contains(err.Error(), "refused a forwarding session") || !strings.Contains(err.Error(), "forward") {
		t.Fatalf("openForward = %v", err)
	}
}
