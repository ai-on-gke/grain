package guestforward

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestDockerExecTransport runs the forwarder over a real `docker exec -i
// <container> granule forward-stdio`, into a container started with
// --network none, in both directions: a service on this side reached
// from a loopback port inside the container, and a service inside the
// container reached from a port on this side. Many connections at once,
// a large transfer, and a reconnect after the exec stream is killed out
// from under it.
//
// The container's granule is internal/fakeshim, which serves the agent
// end on its own stdio instead of relaying to a guest over vsock (that
// hop is TestForwardStdioRelayToAgent's, in cmd/granule-shim). A real
// VM end to end is test/forward-e2e.sh.
//
// Skipped without a docker daemon, and under -short.
func TestDockerExecTransport(t *testing.T) {
	if testing.Short() {
		t.Skip("-short")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("no docker daemon")
	}

	dir := t.TempDir()
	bin := filepath.Join(dir, "granule")
	build := exec.Command("go", "build", "-o", bin, "./internal/fakeshim")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building fakeshim: %v\n%s", err, out)
	}

	// An image of nothing but that binary, imported rather than built or
	// pulled, so the test needs no registry and no base image.
	image := fmt.Sprintf("granule-forward-test:%d", time.Now().UnixNano())
	var tarball bytes.Buffer
	tw := tar.NewWriter(&tarball)
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	_ = tw.WriteHeader(&tar.Header{Name: "usr/local/bin/granule", Mode: 0o755, Size: int64(len(data))})
	_, _ = tw.Write(data)
	_ = tw.Close()
	imp := exec.Command("docker", "import", "-", image)
	imp.Stdin = &tarball
	if out, err := imp.CombinedOutput(); err != nil {
		t.Fatalf("docker import: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", image).Run() })

	name := strings.ReplaceAll(image, ":", "-")
	if out, err := exec.Command("docker", "run", "-d", "--rm", "--network", "none", "--name", name, image, "/usr/local/bin/granule", "sleep").CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })

	// The error a caller sees for a container that is not there names
	// docker's own reason.
	if _, err := Start(context.Background(), Options{
		Transport:    DockerExec("", name+"-missing"),
		ReadyTimeout: 20 * time.Second,
		Local:        []LocalForward{{Listen: "127.0.0.1:0", Guest: "80"}},
	}); err == nil || !strings.Contains(err.Error(), "No such container") {
		t.Fatalf("Start against a missing container = %v", err)
	}

	callerSvc := echoListener(t)
	f, err := Start(context.Background(), Options{
		Transport:    DockerExec("", name),
		Reconnect:    true,
		MinBackoff:   50 * time.Millisecond,
		ReadyTimeout: 20 * time.Second,
		Logf:         t.Logf,
		Remote: []RemoteForward{
			// Inside the container: a caller-side echo service.
			{Guest: "127.0.0.1:9728", Target: callerSvc},
		},
		Local: []LocalForward{
			// Here: the in-container end of the remote mapping above,
			// so every byte crosses the exec stream twice -- out
			// through -L and back through -R.
			{Listen: "127.0.0.1:0", Guest: "127.0.0.1:9728"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	local := binding(t, f, "local")

	t.Run("concurrent", func(t *testing.T) {
		var wg sync.WaitGroup
		errs := make(chan error, 50)
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				msg := []byte(fmt.Sprintf("connection %d %s", i, strings.Repeat("y", i*1000)))
				got, err := roundTrip(local, msg)
				if err != nil {
					errs <- err
				} else if !bytes.Equal(got, msg) {
					errs <- fmt.Errorf("connection %d: echo mismatch", i)
				}
			}(i)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Error(err)
		}
	})

	t.Run("large", func(t *testing.T) {
		data := make([]byte, 16<<20)
		_, _ = rand.Read(data)
		start := time.Now()
		got, err := roundTrip(local, data)
		if err != nil {
			t.Fatal(err)
		}
		if sha256.Sum256(got) != sha256.Sum256(data) {
			t.Fatalf("got %d bytes back, want %d identical", len(got), len(data))
		}
		t.Logf("16 MiB each way through docker exec twice in %s", time.Since(start).Round(time.Millisecond))
	})

	t.Run("reconnect", func(t *testing.T) {
		// Kill the docker exec carrying the session, as a dropped
		// kubelet or API-server stream would end it.
		killExecs(t)
		deadline := time.Now().Add(20 * time.Second)
		for {
			got, err := roundTrip(local, []byte("after reconnect"))
			if err == nil && string(got) == "after reconnect" {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("no reconnect: %q, %v", got, err)
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
}

// killExecs kills every docker exec client this test process started,
// which ends their exec streams.
func killExecs(t *testing.T) {
	out, err := exec.Command("pgrep", "-f", "-P", fmt.Sprint(os.Getpid()), "docker exec -i").Output()
	if err != nil {
		t.Fatalf("finding the docker exec to kill: %v", err)
	}
	for _, pid := range strings.Fields(string(out)) {
		_ = exec.Command("kill", "-9", pid).Run()
	}
}
