package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ai-on-gke/grain/pkg/authfix"
	"github.com/ai-on-gke/grain/pkg/hostd"
)

// HostdHost runs one allowlisted agent CLI on the bare machine through
// grain hostd, for a daemon that itself runs in the release container
// (docs/design/cli-run-location.md, "Host CLIs through grain hostd").
//
// Staging is LocalHost's: the run's working directory, staging dir, CLI
// home and session store are on the daemon's bind mounts, which have the
// same paths on the host, so a file staged here is where the host-run CLI
// looks for it. Only Run crosses the socket. The binary is the allowlist
// entry's, not Command.Path: the daemon names an entry and hostd decides
// what that runs.
type HostdHost struct {
	LocalHost
	Client hostd.Client
	// Entry is the allowlist name of the CLI, e.g. "agy".
	Entry string
	// Path is the host binary the allowlist entry runs, as hostd's Offers
	// reported it; Entry when unset.
	Path string
	// Start, when set, starts req in Client's place: a run's CLI goes
	// through runhost.Hostd, which starts it detached so it outlives a
	// daemon upgrade. Nil is Client.Run.
	Start func(ctx context.Context, req hostd.Request, stdin io.Reader, stdout, stderr io.Writer) (hostd.Exit, error)
}

func (h HostdHost) binary() string {
	if h.Path != "" {
		return h.Path
	}
	return h.Entry
}

// ErrHostdDraining is HostdHost.Run's error, wrapped, for a start grain
// hostd refused because it is draining for its own upgrade
// (hostd.IsDraining). Nothing ran, and nothing about the task earned it:
// the run goes back to the queue without a strike, and the next attempt
// starts on the hostd that replaces this one.
var ErrHostdDraining = errors.New("grain hostd is updating (draining for its own upgrade); the run goes back to the queue")

// Run implements Host with the same contract as LocalHost.Run: stdout is
// returned and teed as it arrives, a non-zero exit is an error carrying
// stderr, cancelling ctx kills the CLI's whole group, and a CLI whose
// descendants hold its output open past hostd.WaitDelay still returns.
func (h HostdHost) Run(ctx context.Context, c Command, tee io.Writer) (string, error) {
	c.Path = h.binary()
	h.logf("agent: running on the host through grain hostd (entry %q): %s", h.Entry, CommandLine(c))
	var stdout, stderr bytes.Buffer
	var out io.Writer = &stdout
	if tee != nil {
		out = io.MultiWriter(&stdout, tee)
	}
	start := h.Start
	if start == nil {
		start = h.Client.Run
	}
	exit, err := start(ctx, hostd.Request{
		Kind: hostd.KindCLI,
		Name: h.Entry,
		Args: c.Args,
		Env:  c.Env,
		Dir:  c.Dir,
	}, strings.NewReader(c.Stdin), out, &stderr)
	if hostd.IsDraining(err) {
		return "", fmt.Errorf("%w: %v", ErrHostdDraining, err)
	}
	if errors.Is(err, ErrDetached) || errors.Is(err, hostd.ErrDetached) {
		return stdout.String(), ErrDetached
	}
	if exit.WaitDelay {
		h.logf("agent: %s exited on the host, but something it started still held its output open after %s; stopped waiting for it", h.Entry, hostd.WaitDelay)
	}
	if err == nil && exit.Code != 0 {
		err = errors.New(exit.String())
	}
	if err != nil {
		err = authfix.Wrap(err, stderr.String(), c.Path)
		return stdout.String(), fmt.Errorf("%w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
