package orchestrator_test

// GranuleConfig.SandboxImage: the deployment that derives its own guest
// image from the stock one (pkg/sandboximage) decides which image a VM
// is built from at the moment it is built, not when this process
// started -- so an edited setup script, or an upgrade that moved the
// stock image, reaches the next sandbox rather than the next restart.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// createLine is the "vm create" the fake granule recorded, which is
// the argument list `granule vm create` was really given.
func createLine(t *testing.T, argvLog string) string {
	t.Helper()
	data, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range splitNonEmptyLines(string(data)) {
		if strings.HasPrefix(line, "vm create ") {
			return line
		}
	}
	t.Fatalf("no `vm create` in %q", data)
	return ""
}

// The resolver's answer is what the VM is created from, replacing the
// image the deployment's own create args name.
func TestSandboxImageReplacesTheImageInTheCreateArgs(t *testing.T) {
	stateDir := t.TempDir()
	argvLog := filepath.Join(t.TempDir(), "granule-argv.log")
	writeFakeGranule(t, argvLog, 30080)
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 0, t.TempDir())

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:   stateDir,
		CreateArgs: []string{"-disk-mode=overlay", "-kontur-image", "ghcr.io/bwsalmon/grain/guest:stock"},
		SandboxImage: func(context.Context) (string, error) {
			return "grain-sandbox-custom:abc123", nil
		},
	})

	sb, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sb.Release(context.Background()) }()

	line := createLine(t, argvLog)
	if !strings.Contains(line, "-kontur-image grain-sandbox-custom:abc123") {
		t.Errorf("create args = %q, want the VM created from the derived image", line)
	}
	if strings.Contains(line, "guest:stock") {
		t.Errorf("create args = %q, want the stock image replaced rather than left beside the derived one", line)
	}
}

// A deployment whose create args name no image at all -- everything
// after cmd/grain fills its own stamp in, and every test above -- still
// gets the derived one.
func TestSandboxImageIsAppendedWhenTheCreateArgsNameNone(t *testing.T) {
	stateDir := t.TempDir()
	argvLog := filepath.Join(t.TempDir(), "granule-argv.log")
	writeFakeGranule(t, argvLog, 30080)
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 0, t.TempDir())

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:     stateDir,
		SandboxImage: func(context.Context) (string, error) { return "grain-sandbox-custom:abc123", nil },
	})

	sb, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sb.Release(context.Background()) }()

	if line := createLine(t, argvLog); !strings.Contains(line, "-kontur-image grain-sandbox-custom:abc123") {
		t.Errorf("create args = %q, want the derived image named", line)
	}
}

// An image that cannot be resolved fails the Acquire, and nothing is
// created: a guest build that failed means this deployment's sandboxes
// are missing exactly what it said they need, and running the task
// anyway in the stock guest is the outcome worth refusing.
func TestSandboxImageFailureFailsTheAcquireWithoutCreatingAVM(t *testing.T) {
	stateDir := t.TempDir()
	argvLog := filepath.Join(t.TempDir(), "granule-argv.log")
	writeFakeGranule(t, argvLog, 30080)

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir: stateDir,
		SandboxImage: func(context.Context) (string, error) {
			return "", errors.New("the setup script exited 1")
		},
	})

	_, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{})
	if err == nil {
		t.Fatal("Acquire succeeded with no sandbox image to create the VM from")
	}
	if !strings.Contains(err.Error(), "the setup script exited 1") {
		t.Errorf("Acquire error = %v, want it to carry what the resolver said", err)
	}
	if _, statErr := os.Stat(argvLog); statErr == nil {
		t.Errorf("granule ran despite there being no image to create from: %s", createLine(t, argvLog))
	}
	if _, statErr := os.Stat(filepath.Join(stateDir, "g-t1-1.json")); statErr == nil {
		t.Error("VM state exists after a failed image resolution, want nothing created")
	}
}
