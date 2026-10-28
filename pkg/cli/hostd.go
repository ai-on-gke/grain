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

package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/intree"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/version"
)

// hostdCmd is "grain hostd": the host-side process server a
// container-mode daemon starts allowlisted host CLIs and capability
// servers through (pkg/hostd). It runs on the bare machine as the user
// grain was installed for, never as root, and opens no store and no
// network port.
func hostdCmd(args []string) {
	fs := flag.NewFlagSet("hostd", flag.ExitOnError)
	socket := fs.String("socket", hostd.DefaultSocketPath, "the unix socket to listen on (mode 0600); bind mount it into the daemon's container at the same path")
	allowlist := fs.String("allowlist", hostd.DefaultAllowlistPath, "the allowlist of what this host will run; must be root-owned and not group- or world-writable")
	stateDir := fs.String("state-dir", defaultHostdStateDir(), "where hostd records the process groups it started, so a restarted hostd reaps what the last one left")
	grace := fs.Duration("disconnect-grace", hostd.DefaultDisconnectGrace, "how long a process may outlive the daemon connection that started it")
	check := fs.Bool("check", false, "check the allowlist, print what it offers, and exit")
	drain := fs.Bool("drain", false, "ask the hostd serving -socket to drain for an upgrade, and exit: it refuses new starts, "+
		"waits for what it runs (up to the allowlist's drain_cap), then exits for systemd to restart it on the new binary. "+
		"A hostd speaking this build's protocol is asked for a cutover instead: it keeps taking starts and exits the first "+
		"moment nothing runs, draining only if no such moment comes within drain_cap. SIGUSR1 to hostd drains")
	status := fs.Bool("status", false, "ask the hostd serving -socket whether it answers and what it offers, print that on one line, "+
		"then one line per process it holds (a detached run's id, run, task and pid), and exit: 1 when it does not answer. "+
		"scripts/setup.sh's readiness report runs it")
	ensure := fs.Bool("ensure-builtins", false, "add to -allowlist the capabilities entries grain's own servers need in this build (host-git, grain-gcp, ... and the edition's, "+
		"registered by the build's edition) that it has no entry of that name for, print each one added, and exit. An entry already there is never changed, "+
		"only reported when it differs from the default. Run as root on the host, by grain-hostd.service before each start and by the upgrade step; "+
		"hostd re-reads the allowlist on every request, so nothing needs restarting")
	command := fs.String("command", hostd.DefaultGrainCommand, "with -ensure-builtins: the grain binary on the host the added entries run")
	build := fs.Bool("build", false, "print this binary's build and exit: what a hostd asks of the version its current link names")
	_ = fs.Parse(args)

	if *build {
		fmt.Println(version.Get().Release)
		return
	}

	if *ensure {
		if err := ensureHostdBuiltins(os.Stdout, *allowlist, *command); err != nil {
			fmt.Fprintln(os.Stderr, "grain hostd: "+err.Error())
			os.Exit(1)
		}
		return
	}

	if *status {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c := hostd.Client{Socket: *socket, Build: version.Get().Release}
		line, err := hostdStatusLine(ctx, c)
		if err != nil {
			fmt.Fprintln(os.Stderr, "grain hostd: "+err.Error())
			os.Exit(1)
		}
		fmt.Println(line)
		// A hostd too old to list its processes still answered above.
		if procs, err := c.Processes(ctx); err == nil {
			writeHostdProcesses(os.Stdout, procs, time.Now())
		}
		return
	}

	if *drain {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c := hostd.Client{Socket: *socket, Build: version.Get().Release}
		// Only a hostd this binary can stand in for without a daemon
		// noticing is cut over; any other is drained, so that its runs end
		// on the build they started on and the next start meets the new
		// one.
		do := c.Drain
		if h, err := c.Hello(ctx); err == nil && h.Protocol == hostd.ProtocolVersion {
			do = c.Cutover
		}
		if err := do(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "grain hostd: "+err.Error())
			os.Exit(1)
		}
		return
	}

	if *check {
		a, err := hostd.LoadAllowlist(*allowlist, []uint32{0})
		if err != nil {
			fmt.Fprintln(os.Stderr, "grain hostd: "+err.Error())
			os.Exit(1)
		}
		for _, o := range a.Offers() {
			fmt.Printf("%s\t%s\t%s\n", o.Kind, o.Name, o.Path)
		}
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	srv := &hostd.Server{
		Socket:          *socket,
		Allowlist:       *allowlist,
		StateDir:        *stateDir,
		DisconnectGrace: *grace,
		Build:           version.Get().Release,
		CurrentLink:     hostd.CurrentLinkFor(os.Args[0]),
	}
	usr1 := make(chan os.Signal, 1)
	signal.Notify(usr1, syscall.SIGUSR1)
	go func() {
		for range usr1 {
			srv.Drain()
		}
	}()
	if err := srv.Serve(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "grain hostd: "+err.Error())
		os.Exit(1)
	}
}

// ensureHostdBuiltins is -ensure-builtins: hostd.EnsureBuiltins with this
// build's entries, saying what it added and what it left as the operator
// has it. Its "added" lines are what scripts/setup.sh's upgrade step and
// the journal report, so keep their first words as they are.
func ensureHostdBuiltins(w io.Writer, allowlist, command string) error {
	res, err := hostd.EnsureBuiltins(allowlist, command, intree.HostdBuiltins())
	for _, name := range res.Added {
		fmt.Fprintf(w, "added %s: grain's own capabilities entry %q to %s (version %s)\n", name, name, allowlist, version.Get().Release)
	}
	for _, d := range res.Differs {
		fmt.Fprintf(w, "kept %s as you have it; it differs from grain's default\n", d)
	}
	if res.Manual != "" {
		fmt.Fprintf(w, "manual: %s's capabilities are not in a shape grain edits (a flow mapping); add these under capabilities: by hand:\n%s", allowlist, res.Manual)
	}
	return err
}

// writeHostdProcesses is the rest of -status's output: one "process"
// line per process hostd holds. scripts/setup.sh's readiness summary and
// its drain decision (hostd_processes) read these lines, so keep their
// first words as they are.
func writeHostdProcesses(w io.Writer, procs []hostd.Process, now time.Time) {
	for _, p := range procs {
		line := fmt.Sprintf("process %s: %s %q, pid %d", p.ID, p.Kind, p.Name, p.PID)
		if p.Run != "" {
			line += fmt.Sprintf(", run %s of task %s", p.Run, p.Task)
		}
		switch {
		case p.Exited && p.Exit != nil:
			line += fmt.Sprintf(", ended (code %d)", p.Exit.Code)
		case p.Exited:
			line += ", ended"
		default:
			line += fmt.Sprintf(", running for %s", now.Sub(p.Started).Round(time.Second))
		}
		if p.Detached {
			if p.Attached {
				line += ", detached, a daemon attached"
			} else {
				line += ", detached, no daemon attached"
			}
		}
		fmt.Fprintln(w, line)
	}
}

// defaultHostdStateDir is $XDG_STATE_HOME/grain-hostd, or
// ~/.local/state/grain-hostd: the installing user's own, outside every
// directory the daemon's container mounts.
func defaultHostdStateDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "grain-hostd")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", "grain-hostd")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("grain-hostd-%d-%d", os.Getuid(), time.Now().Unix()))
}

// hostdStatusLine is what the hostd c dials says about itself, as one
// line: "answering on <socket> (build v2.1.0), offering cli agy
// (/opt/agy)". An error when it does not answer at all.
func hostdStatusLine(ctx context.Context, c hostd.Client) (string, error) {
	hello, err := c.Hello(ctx)
	if err != nil {
		return "", err
	}
	offers, err := c.Offers(ctx)
	if err != nil {
		return "", err
	}
	return formatHostdStatus(c.Socket, hello, offers), nil
}

func formatHostdStatus(socket string, hello hostd.Hello, offers []hostd.Offer) string {
	build := hello.Build
	if build == "" {
		build = "an unknown build"
	}
	line := fmt.Sprintf("answering on %s (%s, protocol %d)", socket, build, hello.Protocol)
	if hello.Cutover {
		line += ", cutting over to a new build once nothing runs"
	}
	if hello.Draining {
		line += ", draining for an upgrade"
	}
	if len(offers) == 0 {
		return line + ", offering nothing"
	}
	var each []string
	for _, o := range offers {
		each = append(each, fmt.Sprintf("%s %s (%s)", o.Kind, o.Name, o.Path))
	}
	return line + ", offering " + strings.Join(each, ", ")
}
