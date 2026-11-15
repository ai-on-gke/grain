// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
// (design/design/cli-run-location.design.md, "Host CLIs through grain hostd").
//
// Staging is LocalHost's for a hostd beside the daemon: the run's
// working directory, staging dir, CLI home and session store are on the
// daemon's bind mounts, which have the same paths on the host, so a file
// staged here is where the host-run CLI looks for it, and only Run
// crosses the socket. A hostd on another machine (Remote) stages over
// its session instead (hostd_remote.go). The binary is the allowlist
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

	// Remote is set for a hostd on another machine, one that dialed in
	// to the daemon (hostd.Hello.Remote): the run's files are staged
	// there over Client rather than here, and grain's endpoints are
	// carried to its loopback over Remote (hostd_remote.go). Nil is a
	// hostd beside the daemon, staging as LocalHost does.
	Remote HostdRemote
	// Grain and StageRoot are that hostd's grain binary and staging
	// directory, as its hello said them.
	Grain     string
	StageRoot string
	// LoginTargets maps each named CLI login on that hostd's machine to
	// its resolved regular-file target for Symlink
	// (hostd.Hello.LoginTargets, FeatureLoginsMap).
	LoginTargets map[string]string
	// HasLoginsMap reports whether that hostd advertised
	// hostd.FeatureLoginsMap, so LoginTargets is authoritative and
	// CodexAuth is not consulted.
	HasLoginsMap bool
	// CodexAuth is the codex login on that hostd's machine, for a hostd
	// that predates FeatureLoginsMap (hostd.Hello.CodexAuth).
	//
	// Deprecated: use HostLogin / LoginTargets.
	CodexAuth string
	// HookPolicy reports whether that hostd's hook subcommands accept
	// -policy and evaluate a staged per-run hook policy file
	// (hostd.Hello.Has(hostd.FeatureHookPolicy)).
	HookPolicy bool
}

// HostLogin returns the symlink target path for the named CLI login on a
// remote hostd's machine: from LoginTargets when the host advertises
// FeatureLoginsMap, or from CodexAuth for older hosts.
func (h HostdHost) HostLogin(name string) string {
	if h.HasLoginsMap {
		return h.LoginTargets[name]
	}
	if name == hostd.LoginCodex {
		return h.CodexAuth
	}
	return ""
}

func (h HostdHost) binary() string {
	if h.Path != "" {
		return h.Path
	}
	return h.Entry
}

// ErrHostdDraining is HostdHost.Run's error, wrapped, for a start grain
// hostd refused because it is draining for its own upgrade
// (hostd.IsDraining), or a running CLI cancelled when an explicit drain
// reached drain_cap. Nothing about the task earned it: the run goes back
// to the queue without a strike, and the next attempt starts on the hostd
// that replaces this one.
var ErrHostdDraining = errors.New("the host agent is updating (draining for its own upgrade); the run goes back to the queue")

// ErrHostdStopped is HostdHost.Run's (and runhost.Hostd.Attach's) error,
// wrapped, for a running CLI whose Exit has Draining set because the host
// agent itself stopped, restarted or crashed while the run was going
// (hostd.Exit.Stopped), with no drain in progress. Nothing about the task
// earned it: the run ends as interrupted, without a strike, and goes back
// to the queue.
var ErrHostdStopped = errors.New("the host agent stopped or restarted while the run was going; the run goes back to the queue")

// ErrHostdLost is HostdHost.Run's error, wrapped, for a CLI whose grain
// hostd dropped mid-run and did not reconnect within the reattach
// timeout (runhost.Hostd.ReattachTimeout, -hostd-reattach-timeout). It
// is the machine's doing, not the task's: the run ends as an
// infrastructure failure, without a strike, and goes back to the queue.
var ErrHostdLost = errors.New("the host agent disconnected mid-run and did not reconnect in time; the run goes back to the queue")

// ErrHostdWaiting is matched (errors.Is) by a framework build refused
// because the hostd a run is pinned or selected to is not connected
// (the daemon's HostdWaitError): nothing ran, and the run goes back to
// the queue, where its task waits for that hostd to reconnect.
var ErrHostdWaiting = errors.New("waiting for the host agent to reconnect")

// IsHostdStop reports whether err is a grain hostd infrastructure stop
// (ErrHostdDraining, ErrHostdStopped or ErrHostdLost): a start hostd
// refused because it is draining for its own upgrade, a host agent stop or
// crash while the CLI ran, or a mid-run disconnect that did not reconnect
// within the reattach timeout. None is a CLI crash or a refused resume
// flag, so a framework retry loop stops at once and hands err back for the
// run to return to the queue without a strike.
func IsHostdStop(err error) bool {
	return errors.Is(err, ErrHostdDraining) || errors.Is(err, ErrHostdStopped) || errors.Is(err, ErrHostdLost)
}

// Run implements Host with the same contract as LocalHost.Run: stdout is
// returned and teed as it arrives, a non-zero exit is an error carrying
// stderr, cancelling ctx kills the CLI's whole group, and a CLI whose
// descendants hold its output open past hostd.WaitDelay still returns.
func (h HostdHost) Run(ctx context.Context, c Command, tee io.Writer) (string, error) {
	c.Path = h.binary()
	h.logf("agent: running on the host through the host agent (entry %q): %s", h.Entry, CommandLine(c))
	var stdout, stderr bytes.Buffer
	var out io.Writer = &stdout
	if tee != nil {
		out = io.MultiWriter(&stdout, tee)
	}
	dir := c.Dir
	if h.remote() {
		dir = h.remoteDir(dir)
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
		Dir:  dir,
	}, strings.NewReader(c.Stdin), out, &stderr)
	if err == nil && exit.Stopped() {
		return stdout.String(), fmt.Errorf("%w: %s", ErrHostdStopped, exit.String())
	}
	if hostd.IsDraining(err) || exit.Draining {
		if err == nil {
			err = errors.New(exit.String())
		}
		return stdout.String(), fmt.Errorf("%w: %v", ErrHostdDraining, err)
	}
	if errors.Is(err, ErrDetached) || errors.Is(err, hostd.ErrDetached) {
		return stdout.String(), ErrDetached
	}
	if errors.Is(err, ErrHostdLost) {
		return stdout.String(), err
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
