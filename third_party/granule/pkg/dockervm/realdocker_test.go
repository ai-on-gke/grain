package dockervm

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// realDocker returns a Docker that drives the real docker CLI, plus an
// image whose only process ignores SIGTERM, skipping the test when there
// is no docker daemon to talk to. The fakedocker-based tests pin down
// what dockervm sends; these pin down that it reads what a real docker
// (whichever release the host has) answers.
func realDocker(t *testing.T) (*Docker, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping real-docker test in -short mode")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker CLI on PATH")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("docker info: %v (no reachable docker daemon)", err)
	}

	bin := filepath.Join(t.TempDir(), "sleeper")
	build := exec.Command("go", "build", "-o", bin, "./testdata/sigtermsleeper")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building sigtermsleeper: %v\n%s", err, out)
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	var layer bytes.Buffer
	tw := tar.NewWriter(&layer)
	if err := tw.WriteHeader(&tar.Header{Name: "sleeper", Mode: 0o755, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	image := fmt.Sprintf("granule-dockervm-test-sleeper:%d", os.Getpid())
	imp := exec.Command("docker", "import", "--change", `ENTRYPOINT ["/sleeper"]`, "-", image)
	imp.Stdin = &layer
	if out, err := imp.CombinedOutput(); err != nil {
		t.Fatalf("docker import: %v\n%s", err, out)
	}
	t.Cleanup(func() { exec.Command("docker", "rmi", "-f", image).Run() })
	return &Docker{BinaryPath: "docker"}, image
}

// startVMContainer runs image under the container name Delete and Inspect
// use for VM name, labelled as granule labels its own.
func startVMContainer(t *testing.T, image, name string) {
	t.Helper()
	cname := vmContainerName(name)
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", cname).Run() })
	if out, err := exec.Command("docker", "run", "-d", "--name", cname, "--label", "granule.dev/vm="+name, image).CombinedOutput(); err != nil {
		t.Fatalf("docker run %s: %v\n%s", cname, err, out)
	}
	// An image that exits at once would leave nothing for Delete to race
	// against, and the test would pass without exercising anything.
	out, err := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", cname).CombinedOutput()
	if err != nil {
		t.Fatalf("docker inspect %s: %v\n%s", cname, err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "true" {
		logs, _ := exec.Command("docker", "logs", cname).CombinedOutput()
		t.Fatalf("container %s State.Running = %q after start, want \"true\"; logs:\n%s", cname, got, logs)
	}
}

func TestRealDocker_InspectReportsAMissingContainerAsMissing(t *testing.T) {
	d, _ := realDocker(t)
	name := fmt.Sprintf("missing-%d", os.Getpid())

	st, err := Inspect(context.Background(), d, name)
	if err != nil {
		t.Fatalf("Inspect error = %v, want ContainerMissing reported instead", err)
	}
	if st.State != ContainerMissing {
		t.Errorf("State = %q, want %q", st.State, ContainerMissing)
	}
}

// Delete racing other removals of the same container -- another Delete,
// or a "docker rm -f" from outside granule -- has to come back once the
// container is gone, however each docker call along the way lost the
// race: "removal ... is already in progress" from stop or rm, then
// inspect reporting the container missing once the other removal lands.
func TestRealDocker_ConcurrentDeletes(t *testing.T) {
	d, image := realDocker(t)

	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("race-%d-%d", os.Getpid(), i)
		t.Run(name, func(t *testing.T) {
			startVMContainer(t, image, name)

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			var wg sync.WaitGroup
			errs := make([]error, 3)
			// The outside removal starts first, as in a "docker rm -f &"
			// before "granule vm delete".
			wg.Add(1)
			go func() {
				defer wg.Done()
				if out, err := exec.Command("docker", "rm", "-f", vmContainerName(name)).CombinedOutput(); err != nil && !strings.Contains(strings.ToLower(string(out)), "already in progress") {
					t.Errorf("docker rm -f: %v\n%s", err, out)
				}
			}()
			time.Sleep(20 * time.Millisecond)
			for j := range errs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					errs[j] = Delete(ctx, d, name, 1, &strings.Builder{})
				}()
			}
			wg.Wait()
			for j, err := range errs {
				if err != nil {
					t.Errorf("Delete #%d error = %v, want nil", j, err)
				}
			}

			st, err := Inspect(context.Background(), d, name)
			if err != nil {
				t.Fatalf("Inspect after Delete: %v", err)
			}
			if st.State != ContainerMissing {
				t.Errorf("State after Delete = %q, want %q", st.State, ContainerMissing)
			}
		})
	}
}
