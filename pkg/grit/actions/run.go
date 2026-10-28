package actions

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// RunRequest is one job for Run: the repository to copy into the VM, the
// workflow file and job id in it, the event act simulates, the runs-on
// label and its image mapping, and the VM name (generated when empty).
type RunRequest struct {
	Repo, Workflow, Job, Event, Runner, Name string
	Images                                   RunnerImages
}

// Run creates a Granule VM, copies the input repository into it, runs one
// Actions job, and deletes the VM.
func Run(ctx context.Context, req RunRequest, stdout, stderr io.Writer) error {
	repo, err := filepath.Abs(req.Repo)
	if err != nil {
		return err
	}
	if req.Runner == "" {
		req.Runner = DefaultRunner
	}
	image, err := req.Images.Resolve(req.Runner)
	if err != nil {
		return err
	}
	if req.Name == "" {
		req.Name = fmt.Sprintf("grit-%d", os.Getpid())
	}
	if req.Workflow == "" || req.Job == "" {
		return fmt.Errorf("grit: workflow and job are required")
	}
	if err := command(ctx, stdout, stderr, nil, granuleCommand, "vm", "create", req.Name, "-wait", "-kontur-image", image); err != nil {
		return err
	}
	defer command(context.Background(), stdout, stderr, nil, granuleCommand, "vm", "delete", req.Name)

	tar := exec.CommandContext(ctx, "tar", "-cf", "-", "-C", repo, ".")
	cp := exec.CommandContext(ctx, dockerCommand, "exec", "-i", "granule-vm-"+req.Name, "granule", "cp", "-tar", "-", "/workspace")
	pipe, err := tar.StdoutPipe()
	if err != nil {
		return err
	}
	cp.Stdin, tar.Stderr, cp.Stdout, cp.Stderr = pipe, stderr, stdout, stderr
	if err := cp.Start(); err != nil {
		pipe.Close()
		return err
	}
	// cp holds its own copy of the read end now. Keeping this one open
	// would leave tar blocked on a full pipe if cp died early, instead of
	// getting EPIPE.
	pipe.Close()
	// cp is waited on whatever tar did, so a failed tar leaves neither a
	// zombie nor cp's copying goroutines behind.
	tarErr := tar.Run()
	if err := errors.Join(tarErr, cp.Wait()); err != nil {
		return fmt.Errorf("grit: copy repository: %w", err)
	}
	script := "cd /workspace\n" + ActScript(req.Workflow, req.Job, req.Event)
	return command(ctx, stdout, stderr, nil, granuleCommand, "vm", "exec", req.Name, "--", "bash", "-lc", script)
}

// granuleCommand and dockerCommand are the binaries Run drives, held in
// variables so a test can stand in for them.
var granuleCommand, dockerCommand = "granule", "docker"

func command(ctx context.Context, stdout, stderr io.Writer, stdin io.Reader, name string, args ...string) error {
	c := exec.CommandContext(ctx, name, args...)
	c.Stdin, c.Stdout, c.Stderr = stdin, stdout, stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("grit: %s: %w", strings.Join(append([]string{name}, args...), " "), err)
	}
	return nil
}
