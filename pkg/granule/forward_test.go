package granule

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestForwardArgsAndKubectlForwardArgs(t *testing.T) {
	t.Parallel()

	cfg := KubectlForwardConfig{
		Kubectl:     "/usr/local/bin/kubectl",
		Kubeconfig:  "/var/lib/grain/kubeconfig",
		Namespace:   "grain",
		Pod:         "grain-granule-7-1",
		Container:   "sandbox",
		RemotePorts: []int{9730, 9728, 9728},
		Locals: []ForwardSpec{
			{BindHost: "127.0.0.1", BindPort: 18080, TargetHost: "127.0.0.1", TargetPort: 8080},
		},
	}

	gotCLI := Local.ForwardArgs(cfg)
	wantCLI := []string{
		"forward", "-kube",
		"-kubectl", "/usr/local/bin/kubectl",
		"-kubeconfig", "/var/lib/grain/kubeconfig",
		"-n", "grain",
		"-c", "sandbox",
		"-L", "127.0.0.1:18080:127.0.0.1:8080",
		"-R", "127.0.0.1:9728:127.0.0.1:9728",
		"-R", "127.0.0.1:9730:127.0.0.1:9730",
		"grain-granule-7-1",
	}
	if !slices.Equal(gotCLI, wantCLI) {
		t.Errorf("ForwardArgs() = %v, want %v", gotCLI, wantCLI)
	}

	gotKubectl := KubectlForwardArgs(cfg)
	wantKubectl := []string{
		"--kubeconfig", "/var/lib/grain/kubeconfig",
		"-n", "grain",
		"exec", "-i",
		"-c", "sandbox",
		"grain-granule-7-1",
		"--",
		"granule", "forward",
		"-L", "127.0.0.1:18080:127.0.0.1:8080",
		"-R", "127.0.0.1:9728:127.0.0.1:9728",
		"-R", "127.0.0.1:9730:127.0.0.1:9730",
	}
	if !slices.Equal(gotKubectl, wantKubectl) {
		t.Errorf("KubectlForwardArgs() = %v, want %v", gotKubectl, wantKubectl)
	}
}

func TestServeForwardRoundTrip(t *testing.T) {
	t.Parallel()

	controllerLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer controllerLn.Close()
	controllerPort := controllerLn.Addr().(*net.TCPAddr).Port

	go func() {
		conn, err := controllerLn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	guestPort := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()

	remotes := []ForwardSpec{
		{
			BindHost:   "127.0.0.1",
			BindPort:   guestPort,
			TargetHost: "127.0.0.1",
			TargetPort: controllerPort,
		},
	}

	c2gReader, c2gWriter := io.Pipe()
	g2cReader, g2cWriter := io.Pipe()
	defer c2gReader.Close()
	defer c2gWriter.Close()
	defer g2cReader.Close()
	defer g2cWriter.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	readyCh := make(chan struct{})
	clientDone := make(chan error, 1)
	guestDone := make(chan error, 1)

	go func() {
		guestDone <- ServeForwardGuest(ctx, c2gReader, g2cWriter, remotes)
	}()
	go func() {
		clientDone <- ServeForwardClient(ctx, g2cReader, c2gWriter, remotes, nil, func() {
			close(readyCh)
		})
	}()

	select {
	case <-readyCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for forward ready")
	}

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", guestPort), 5*time.Second)
	if err != nil {
		t.Fatalf("dialing guest forwarded port: %v", err)
	}
	defer conn.Close()

	const payload = "ping-controller-service"
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != payload {
		t.Fatalf("got %q, want %q", buf, payload)
	}

	cancel()
	_ = c2gWriter.Close()
	_ = g2cWriter.Close()
	<-clientDone
	<-guestDone
}

// TestForwardKubectlRefusesOpenOutsideRemotePorts stands a fake kubectl in
// for the guest: it speaks the forward mux on its stdout and asks the
// controller to open a listener that is not in RemotePorts, the metadata
// server, and the one port that is. Only the last may be dialed.
func TestForwardKubectlRefusesOpenOutsideRemotePorts(t *testing.T) {
	t.Parallel()

	listen := func() (net.Listener, int) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		return ln, ln.Addr().(*net.TCPAddr).Port
	}
	allowedLn, allowedPort := listen()
	go func() {
		for {
			conn, err := allowedLn.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	forbiddenLn, forbiddenPort := listen()
	var forbiddenHits atomic.Int32
	go func() {
		for {
			conn, err := forbiddenLn.Accept()
			if err != nil {
				return
			}
			forbiddenHits.Add(1)
			_ = conn.Close()
		}
	}()

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	result := filepath.Join(dir, "result")
	targets := fmt.Sprintf("127.0.0.1:%d,169.254.169.254:80,127.0.0.1:%d", forbiddenPort, allowedPort)
	script := fmt.Sprintf("#!/bin/sh\n%s=%q %s=%q exec %q -test.run='^TestFakeKubectlForwardGuest$'\n",
		fakeKubectlTargetsEnv, targets, fakeKubectlResultEnv, result, exe)
	kubectl := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err = ForwardKubectl(ctx, KubectlForwardConfig{
		Kubectl:     kubectl,
		Pod:         "grain-granule-1-1",
		RemotePorts: []int{allowedPort},
	})
	if ctx.Err() != nil {
		t.Fatalf("ForwardKubectl did not finish: %v", err)
	}

	got, rerr := os.ReadFile(result)
	if rerr != nil {
		t.Fatalf("fake kubectl left no result (ForwardKubectl: %v): %v", err, rerr)
	}
	// frameOpenErr, frameOpenErr, frameOpenOK.
	if want := "4,4,3"; string(got) != want {
		t.Errorf("replies to the fake guest's opens = %q, want %q", got, want)
	}
	if n := forbiddenHits.Load(); n != 0 {
		t.Errorf("the listener outside RemotePorts was connected to %d times", n)
	}
}

const (
	fakeKubectlTargetsEnv = "GRAIN_TEST_FAKE_KUBECTL_TARGETS"
	fakeKubectlResultEnv  = "GRAIN_TEST_FAKE_KUBECTL_RESULT"
)

// TestFakeKubectlForwardGuest is not a test: it is the guest end of
// TestForwardKubectlRefusesOpenOutsideRemotePorts, run as a child process by
// the fake kubectl that test writes. It sends Ready, then an Open for each
// target in turn, and writes the type of the controller's reply to each to
// the result file.
func TestFakeKubectlForwardGuest(t *testing.T) {
	targets := os.Getenv(fakeKubectlTargetsEnv)
	if targets == "" {
		t.Skip("only run as the fake kubectl's guest")
	}
	writeFrame := func(typ byte, id uint32, payload string) {
		var hdr [9]byte
		hdr[0] = typ
		binary.BigEndian.PutUint32(hdr[1:5], id)
		binary.BigEndian.PutUint32(hdr[5:9], uint32(len(payload)))
		_, _ = os.Stdout.Write(append(hdr[:], payload...))
	}
	writeFrame(1, 0, "") // Ready
	var replies []string
	for i, target := range strings.Split(targets, ",") {
		id := uint32(2 * (i + 1))
		writeFrame(2, id, target) // Open
		for {
			var hdr [9]byte
			if _, err := io.ReadFull(os.Stdin, hdr[:]); err != nil {
				os.Exit(1)
			}
			payload := make([]byte, binary.BigEndian.Uint32(hdr[5:9]))
			if _, err := io.ReadFull(os.Stdin, payload); err != nil {
				os.Exit(1)
			}
			if binary.BigEndian.Uint32(hdr[1:5]) == id && (hdr[0] == 3 || hdr[0] == 4) {
				replies = append(replies, strconv.Itoa(int(hdr[0])))
				break
			}
		}
	}
	if err := os.WriteFile(os.Getenv(fakeKubectlResultEnv), []byte(strings.Join(replies, ",")), 0o644); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}
