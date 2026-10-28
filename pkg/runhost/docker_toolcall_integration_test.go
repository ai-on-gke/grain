package runhost

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// TestRunHostCannotReachAContainerSandbox makes a real sandbox tool call
// from a docker run host, against a real local container sandbox: the
// `grain mcpserver -container grain-sandbox-<run>` a claude run forks
// under -container-sandboxes, answering a run_command.
//
// From the daemon's own machine the call reaches the sandbox. From a run
// host it cannot: the mcpserver runs every tool as `docker exec`, and a
// run host has no docker socket (nor, in the deployment image, a docker
// CLI). That is why the daemon refuses -run-host-image together with
// -container-sandboxes (daemon.CheckRunHostBackend), and this is the
// test that says so with a real call rather than a reading of the code
// (docs/test-runs/2026-10-14-run-hosts-daemon.md, finding 6). If a run
// host is ever given a way to reach a container sandbox, this fails, and
// the refusal is what to lift with it.
//
// It needs a docker daemon and builds grain, so it runs only with
// GRAIN_RUNHOST_DOCKER=1 (the "run host" job in .grain/workflows/tests.yml).
// GRAIN_RUNHOST_IMAGE names the image, debian:bookworm-slim by default,
// for both the run host and the sandbox.
func TestRunHostCannotReachAContainerSandbox(t *testing.T) {
	if os.Getenv("GRAIN_RUNHOST_DOCKER") != "1" {
		t.Skip("set GRAIN_RUNHOST_DOCKER=1 to run against a real docker daemon")
	}
	image := os.Getenv("GRAIN_RUNHOST_IMAGE")
	if image == "" {
		image = "debian:bookworm-slim"
	}
	bin := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(bin, "grain"), "github.com/ai-on-gke/grain/cmd/grain")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building grain: %v\n%s", err, out)
	}

	// The sandbox, named and laid out as orchestrator.ContainerSandboxes
	// names and lays out its own: the tools work in /home/agent.
	nonce := time.Now().UnixNano()
	sandbox := fmt.Sprintf("grain-sandbox-runhost-tool-test-%d", nonce)
	if out, err := exec.Command("docker", "run", "-d", "--name", sandbox, image, "sleep", "infinity").CombinedOutput(); err != nil {
		t.Fatalf("starting the sandbox container: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", sandbox).Run() })
	if out, err := exec.Command("docker", "exec", sandbox, "mkdir", "-p", "/home/agent").CombinedOutput(); err != nil {
		t.Fatalf("making the sandbox workspace: %v\n%s", err, out)
	}

	const marker = "reached-the-sandbox"
	stdin := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"runhost-test","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"run_command","arguments":{"command":"echo ` + marker + `","description":"probing the sandbox"}}}`,
	}, "\n") + "\n"
	command := agent.Command{Path: filepath.Join(bin, "grain"), Args: []string{"mcpserver", "-container", sandbox}, Stdin: stdin}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// From the daemon's machine: the same argv, the same requests.
	local, err := agent.LocalHost{Logf: t.Logf}.Run(ctx, command, nil)
	if err != nil {
		t.Fatalf("mcpserver on the daemon's machine: %v\n%s", err, local)
	}
	if !strings.Contains(local, marker) || !strings.Contains(local, `"isError":false`) {
		t.Fatalf("run_command from the daemon's machine did not reach the sandbox:\n%s", local)
	}

	// From a run host.
	root := t.TempDir()
	deployment := fmt.Sprintf("runhost-tool-test-%d", nonce)
	driver := Docker{}
	t.Cleanup(func() {
		infos, _ := driver.List(context.Background(), deployment)
		for _, i := range infos {
			_ = driver.Destroy(context.Background(), i.Name)
		}
	})
	host := &Host{
		LocalHost:   agent.LocalHost{Logf: t.Logf},
		Driver:      driver,
		Deployment:  deployment,
		Generation:  "test",
		Image:       image,
		Grain:       filepath.Join(bin, "grain"),
		User:        fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		SpoolRoot:   filepath.Join(root, "spool"),
		StagingRoot: filepath.Join(root, "staging"),
		Mounts:      []Mount{{Path: bin, ReadOnly: true}},
		Poll:        50 * time.Millisecond,
	}
	runCtx := WithRun(ctx, Run{Task: "9", Run: "9-1", Deadline: time.Now().Add(10 * time.Minute)})
	remote, err := host.Run(runCtx, command, nil)
	if err != nil {
		t.Fatalf("mcpserver in a run host: %v\n%s", err, remote)
	}
	if !strings.Contains(remote, `"id":2`) {
		t.Fatalf("mcpserver in a run host never answered the tool call:\n%s", remote)
	}
	if strings.Contains(remote, marker+"\\n") || !strings.Contains(remote, `"isError":true`) {
		t.Fatalf("run_command from a run host reached the container sandbox, which "+
			"daemon.CheckRunHostBackend refuses to start on the grounds that it cannot -- lift that refusal:\n%s", remote)
	}
	t.Logf("run_command from a run host, as the refusal expects: %s", remote)
}
