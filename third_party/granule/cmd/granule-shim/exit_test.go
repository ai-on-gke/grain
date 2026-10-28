package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/bwsalmon/granule/pkg/config"
	"github.com/bwsalmon/granule/pkg/workloadio"
)

// chvExit is a cloud-hypervisor that exited with status code.
func chvExit(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit "+strconv.Itoa(code)).Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != code {
		t.Fatalf("could not make an exit status %d: %v", code, err)
	}
	return err
}

func readExitFile(t *testing.T, path string) workloadio.ExitStatus {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var status workloadio.ExitStatus
	if err := json.Unmarshal(data, &status); err != nil {
		t.Fatalf("exit file %q: %v", data, err)
	}
	return status
}

func TestWorkloadExit(t *testing.T) {
	// The workload's reported status wins over cloud-hypervisor's.
	exitFile := filepath.Join(t.TempDir(), "exit")
	cfg := config.Config{WorkloadExitFile: exitFile}
	code, ok := workloadExit(cfg, workloadio.Observed{Started: true, Exited: true, Code: 42}, nil)
	if !ok || code != 42 {
		t.Errorf("reported exit: got %d, %v; want 42", code, ok)
	}
	if got := readExitFile(t, exitFile); got != (workloadio.ExitStatus{ExitCode: 42, Reported: true}) {
		t.Errorf("exit file holds %+v", got)
	}

	// Started and never reported exiting: the VM died under it.
	code, ok = workloadExit(cfg, workloadio.Observed{Started: true}, chvExit(t, 1))
	if !ok || code != workloadio.ExitUnreported {
		t.Errorf("unreported exit: got %d, %v; want %d", code, ok, workloadio.ExitUnreported)
	}
	if got := readExitFile(t, exitFile); got.Reported || got.ExitCode != workloadio.ExitUnreported || got.Reason == "" {
		t.Errorf("exit file holds %+v", got)
	}

	// Declared wrapped by the exit file, and nothing reported at all.
	code, ok = workloadExit(cfg, workloadio.Observed{}, nil)
	if !ok || code != workloadio.ExitUnreported {
		t.Errorf("declared, nothing reported: got %d, %v; want %d", code, ok, workloadio.ExitUnreported)
	}

	// Without an exit file the shim still exits with the workload's
	// status -- a plain `docker run` of a wrapped image -- and writes
	// nothing.
	code, ok = workloadExit(config.Config{}, workloadio.Observed{Started: true, Exited: true, Code: 3}, nil)
	if !ok || code != 3 {
		t.Errorf("no exit file: got %d, %v; want 3", code, ok)
	}

	// A VM that never ran a wrapped workload keeps cloud-hypervisor's.
	if _, ok := workloadExit(config.Config{}, workloadio.Observed{}, chvExit(t, 1)); ok {
		t.Error("an unwrapped VM's exit code was taken over")
	}
}
