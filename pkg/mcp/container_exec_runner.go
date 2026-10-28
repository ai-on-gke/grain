package mcp

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"

	"github.com/bwsalmon/grain/pkg/procgroup"
)

// ContainerExecRunner runs commands directly in a Docker container.
type ContainerExecRunner struct {
	Docker    string
	Container string
	// User is passed to `docker exec -u` when set. Empty runs as the
	// image's own USER, which is what every tool call wants; "0" is for
	// the controller's own writes into /etc, in an image that runs as an
	// unprivileged user and carries no sudo.
	User string
}

func (r *ContainerExecRunner) Run(ctx context.Context, argv []string, stdin string) (stdout, stderr string, exitCode int) {
	return r.RunStream(ctx, argv, stdin, nil)
}

// RunStream is Run with somebody watching: everything either stream
// produces is written to out as it arrives, as well as being collected
// into the strings Run returns. A nil out is Run, with nothing extra done.
func (r *ContainerExecRunner) RunStream(ctx context.Context, argv []string, stdin string, out io.Writer) (stdout, stderr string, exitCode int) {
	cmd := exec.CommandContext(ctx, r.binary(), r.execArgs(argv, stdin != "")...)
	procgroup.Prepare(cmd)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	outBuf, errBuf := newHeadTailBuffer(), newHeadTailBuffer()
	live := &liveStream{to: out}
	cmd.Stdout = live.writerFor(outBuf)
	cmd.Stderr = live.writerFor(errBuf)
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return outBuf.String(), errBuf.String(), exitErr.ExitCode()
		}
		return outBuf.String(), err.Error(), -1
	}
	return outBuf.String(), errBuf.String(), 0
}

func (r *ContainerExecRunner) binary() string {
	if r.Docker != "" {
		return r.Docker
	}
	return "docker"
}

// execArgs renders the `docker exec` arguments, split out of Run so that
// DialStream can build the same invocation -- and always interactively,
// since a connection is stdin as much as it is stdout.
func (r *ContainerExecRunner) execArgs(argv []string, interactive bool) []string {
	args := []string{"exec"}
	if interactive {
		args = append(args, "-i")
	}
	if r.User != "" {
		args = append(args, "-u", r.User)
	}
	args = append(args, r.Container)
	return append(args, argv...)
}
