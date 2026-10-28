// Command granule is the container-facing binary for granule: it either
// boots a single cloud-hypervisor VM as PID 1 of a container ("run", the
// default), splices that VM onto the sandbox's own network segment as an
// optional Kubernetes init container ("netshim"), or blocks until signalled
// ("sleep", retained for older namespace-holder deployments). Normal "run"
// configures networking itself. It also runs a command in the VM over vsock
// ("exec", see pkg/guestexec), copies a file or directory in or out
// of that guest over the same session ("cp", see pkg/guestcp),
// live-resizes an already-running VM's memory and/or vCPU count via
// cloud-hypervisor's API ("resize", see pkg/hypervisor's
// APIClient.Resize/ResizeCPUs), or reports whether the guest is up yet
// and nothing else ("ready", see runReady and pkg/guestexec's
// Ready) -- the last four meant to be invoked as `kubectl exec`'s own
// command, so they reach the guest/VMM of the already-running container
// rather than this otherwise-empty one, and "ready" also as that
// container's own readiness probe.
//
// One mode does not run inside a VM container at all: "wrap" (see
// runWrap and pkg/guestwrap) turns an ordinary container image into
// a granule image that boots a VM and runs that container inside it. It
// produces an image rather than being part of one, so it runs wherever
// docker does -- a workstation or a CI job. It lives in this binary
// because the image it produces is built out of this binary's own image.
//
// All eight modes live in the same binary and the same OCI image -- which,
// since pkg/netshim talks to the kernel directly via netlink
// rather than exec'ing external CLIs, ships from "scratch" with no shell
// or coreutils of its own, so "sleep" exists here rather than relying on
// one being present in the image -- invoked with different args per role.
// See README.md for the environment variables each mode understands.
//
// The same binary also answers to "sh" and "bash": the Dockerfile's
// "final" stage symlinks /bin/sh and /bin/bash (and their /usr/bin
// equivalents) to it, so a plain `kubectl exec -it <pod> -- sh` (or
// "docker exec -it <container> bash") resolves to this binary too, and
// forwards into the guest the same way "exec" mode above does --
// docker/kubectl's own "exec" machinery always runs a command already
// present in the container by name, never through the mode dispatch
// below, so this is the only way for a plain sh/bash invocation (as
// opposed to one that already knows to run "granule exec") to end up
// reaching the guest at all. main tells these two apart from the eight
// real modes by argv[0] rather than an argument, since that's the only
// thing docker/kubectl's own exec machinery lets a container control
// about how it's invoked. See ShellCommandLine for exactly which
// "sh"/"bash" invocations this supports.
//
// This is distinct from cmd/granule, which is the operator-facing CLI
// that runs on the node itself (not inside a container) to manage those
// pods.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/bwsalmon/granule/pkg/config"
	"github.com/bwsalmon/granule/pkg/guestcp"
	"github.com/bwsalmon/granule/pkg/guestexec"
	"github.com/bwsalmon/granule/pkg/guestimage"
	"github.com/bwsalmon/granule/pkg/hypervisor"
	"github.com/bwsalmon/granule/pkg/memagent"
	"github.com/bwsalmon/granule/pkg/netshim"
	"github.com/bwsalmon/granule/pkg/workloadio"
)

func main() {
	log.SetFlags(0)

	argv0 := filepath.Base(os.Args[0])
	mode := "run"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	if argv0 == "sh" || argv0 == "bash" {
		mode = "shell"
	}

	switch mode {
	case "run":
		log.SetPrefix("granule: ")
		if err := runVM(); err != nil {
			log.Fatalf("%v", err)
		}
	case "netshim":
		log.SetPrefix("granule: netshim: ")
		if err := runNetshim(); err != nil {
			log.Fatalf("%v", err)
		}
	case "exec":
		log.SetPrefix("granule: exec: ")
		if err := runExec(os.Args[2:]); err != nil {
			log.Fatalf("%v", err)
		}
	case "cp":
		log.SetPrefix("granule: cp: ")
		if err := runCp(os.Args[2:]); err != nil {
			log.Fatalf("%v", err)
		}
	case "forward":
		log.SetPrefix("granule: forward: ")
		if err := runForward(os.Args[2:]); err != nil {
			log.Fatalf("%v", err)
		}
	case "resize":
		log.SetPrefix("granule: resize: ")
		if err := runResize(os.Args[2:]); err != nil {
			log.Fatalf("%v", err)
		}
	case "ready":
		log.SetPrefix("granule: ready: ")
		if err := runReady(os.Args[2:]); err != nil {
			// The one failure with an exit status of its own: the guest
			// answered and has not declared itself ready (see
			// guestexec.ExitNotDeclared). Everything a probe's caller
			// wants to say about that differs from "the VM is still
			// booting", so the two are not both 1.
			if errors.Is(err, guestexec.ErrNotDeclared) {
				log.Printf("%v", err)
				os.Exit(guestexec.ExitNotDeclared)
			}
			log.Fatalf("%v", err)
		}
	case "snapshot":
		log.SetPrefix("granule: snapshot: ")
		if err := runSnapshot(os.Args[2:]); err != nil {
			log.Fatalf("%v", err)
		}
	case "shell":
		log.SetPrefix("granule: " + argv0 + ": ")
		if err := runShell(os.Args[1:]); err != nil {
			log.Fatalf("%v", err)
		}
	case "wrap":
		log.SetPrefix("granule: wrap: ")
		if err := runWrap(os.Args[2:]); err != nil {
			log.Fatalf("%v", err)
		}
	case "sleep":
		// A bare "select {}" here would make Go's deadlock detector kill
		// the process immediately (nothing else could ever wake the sole
		// goroutine), so block on an explicit signal instead: "docker
		// stop" sends SIGTERM (then SIGKILL after its grace period if
		// this doesn't exit first), matching how "sleep infinity" would
		// have responded.
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		<-sigCh
	default:
		log.Fatalf("granule: unknown mode %q (want \"run\", \"netshim\", \"exec\", \"cp\", \"forward\", \"resize\", \"ready\", \"snapshot\", \"wrap\" or \"sleep\")", mode)
	}
}

// runVM boots a single cloud-hypervisor VM and forwards Kubernetes
// lifecycle signals to it, exactly as the standalone chv-runtime binary
// used to. Its own stdout/stderr become the VM's serial console output,
// so "kubectl logs" on the container is the VM's console.
func runVM() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}

	// FromEnv has already refused a guest whose contract this granule
	// cannot honour, so reaching here means the two agree. Say so anyway,
	// once, and loudest in the case that is going to matter later: a guest
	// carrying no declaration boots today as version 0 and is what a
	// future floor bump stops, so an operator should be able to find it in
	// a log they already have rather than after the upgrade that refuses
	// it. See pkg/guestimage's bootcontract.go.
	if cfg.GuestContract.State == guestimage.ContractUndeclared {
		log.Printf("%s; this granule implements version %d and accepts version %d, so it boots -- but a guest packed by this granule declares its version, and a later one may require it (see docs/guest-images.md, \"The guest contract\")",
			cfg.GuestContract.Describe(), guestimage.Contract, guestimage.MinimumBootContract)
	} else {
		log.Printf("%s; this granule implements version %d", cfg.GuestContract.Describe(), guestimage.Contract)
	}

	// Whether this container already has a VM in it is settled before
	// anything else: both sockets a boot has to clear (see
	// hypervisor.RemoveStaleSocket) belong to a VM that is still running
	// if anything answers on them, and so does the disk everything below
	// is about to write to. A second "granule run" -- including a bare
	// "granule", whose default mode this is -- stops here without having
	// touched either.
	if cfg.VsockSocket != "" {
		// cloud-hypervisor creates the vsock socket itself, but not the
		// directory it sits in, and refuses to start if that is missing
		// -- which reads as a hypervisor failure rather than a missing
		// mkdir.
		if err := os.MkdirAll(filepath.Dir(cfg.VsockSocket), 0o700); err != nil {
			return fmt.Errorf("preparing the directory for the guest's vsock socket: %w", err)
		}
		if err := hypervisor.RemoveStaleSocket(cfg.VsockSocket, "the guest's vsock socket"); err != nil {
			return err
		}
	}
	if err := hypervisor.RemoveStaleSocket(cfg.APISocket, "cloud-hypervisor's API socket"); err != nil {
		return err
	}

	// Before any disk or network is prepared for it: a VM this container
	// cannot open the hypervisor device for is not going to boot, and
	// cloud-hypervisor's own report of that names neither the device nor
	// the account it refused -- which in a rootless container is the
	// whole story (see hypervisor.CheckDevice).
	if err := hypervisor.CheckDevice(); err != nil {
		return err
	}

	// A resumed VM's disk has to be the one its snapshotted memory was
	// taken against, so the overlay saved inside the snapshot goes back
	// first -- before PrepareOverlay below, which then reuses it instead
	// of creating an empty overlay from the base image and resuming the
	// guest onto a disk that has silently rolled back underneath it.
	// See hypervisor.RestoreDiskOverlay.
	if hypervisor.SnapshotExists(cfg.SnapshotPath) && cfg.DiskMode == config.DiskModeOverlay {
		restored, err := hypervisor.RestoreDiskOverlay(cfg.SnapshotPath, cfg.OverlayPath)
		if err != nil {
			return err
		}
		if restored {
			log.Printf("restored the guest's disk overlay from the snapshot at %s", cfg.SnapshotPath)
		} else {
			log.Printf("warning: the snapshot at %s carries no disk overlay, so the guest resumes onto whatever disk this container has; if that is not the disk the snapshot was taken against, expect the guest to find files it believes in missing", cfg.SnapshotPath)
		}
	}

	// Before anything boots: in the default disk mode the guest writes
	// into an overlay of its own rather than into the image, and this is
	// what creates it, sizes it to CHV_DISK_SIZE_MB (growing one left
	// over from an earlier boot at a smaller size), and repoints the VM
	// at it. See config.PrepareOverlay.
	if err := cfg.PrepareOverlay(); err != nil {
		return err
	}
	if err := cfg.PrepareAuxiliaryDisks(); err != nil {
		return err
	}
	// After the auxiliary disks, so it is attached after them: see
	// config.PrepareContainerOverride.
	if err := cfg.PrepareContainerOverride(); err != nil {
		return err
	}
	if cfg.DiskSizeMB > 0 {
		log.Printf("writable disk sized to %d MiB; the guest grows its own filesystem into it", cfg.DiskSizeMB)
	}

	if err := configureNetwork(&cfg, netshim.Setup, netshim.Discover); err != nil {
		return err
	}

	// The memory agent's listener will not run without a token (see
	// pkg/memagent's "The token"), so one is made here if the operator
	// did not supply one. It reaches the guest over vsock once the guest
	// is up (startMemAgent), never on the kernel command line, which
	// every guest process can read.
	if cfg.MemAgent && cfg.MemAgentToken == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return fmt.Errorf("generating the memory agent's token: %w", err)
		}
		cfg.MemAgentToken = hex.EncodeToString(b)
	}

	// Before the VM boots, so that a workload which exits at once still
	// finds something to report its status to (see pkg/workloadio's
	// "Exit status"), and with any status a previous boot of this
	// container left gone: the file appearing is how a caller learns
	// this VM has stopped.
	if cfg.WorkloadExitFile != "" {
		if err := os.Remove(cfg.WorkloadExitFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("removing the previous boot's workload exit status: %w", err)
		}
	}
	var exits *workloadio.ExitCollector
	if cfg.VsockSocket != "" {
		exits, err = workloadio.ListenExit(cfg.VsockSocket)
		if err != nil {
			if cfg.WorkloadExitFile != "" {
				return err
			}
			log.Printf("%v; this container exits with cloud-hypervisor's status rather than its workload's", err)
		}
	}

	runner := hypervisor.New(cfg)
	if err := runner.Start(os.Stdout, os.Stderr); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		log.Printf("received %s, shutting down", sig)
		cancel()
		runner.Shutdown(context.Background())
	}()

	if cfg.MemAgent {
		startMemAgent(ctx, cfg)
	}

	// Its own context rather than ctx: the guest's last output is still
	// on its way when the VM stops, so this is let run past the
	// hypervisor's exit for a moment rather than cut off by it.
	streamCtx, stopStreams := context.WithCancel(context.Background())
	defer stopStreams()
	vmExited := make(chan struct{})
	streamsDone := forwardWorkloadOutput(streamCtx, vmExited, cfg)

	if cfg.SetupScript != "" {
		if runner.Restored() {
			log.Printf("resumed from suspended state at %s, skipping setup script", cfg.SnapshotPath)
		} else if err := runSetupScript(ctx, cfg, runner); err != nil {
			runner.Shutdown(context.Background())
			return err
		}
	}

	err = runner.Wait()
	// The guest hangs up once it has sent the workload's last output,
	// and cloud-hypervisor's exit closes whatever it had not. Either way
	// the forwarder is at the end of what it can read; this is only the
	// time to write it out.
	close(vmExited)
	select {
	case <-streamsDone:
	case <-time.After(2 * time.Second):
	}
	stopStreams()
	var observed workloadio.Observed
	if exits != nil {
		observed = exits.Close()
	}
	if code, ok := workloadExit(cfg, observed, err); ok {
		os.Exit(code)
	}
	if err == nil {
		log.Printf("cloud-hypervisor exited cleanly")
		return nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		log.Printf("cloud-hypervisor exited with status %d", exitErr.ExitCode())
		os.Exit(exitErr.ExitCode())
	}
	return err
}

// workloadExit is the exit code of a VM container that runs a wrapped
// workload, once the VM has stopped: the status granule-container
// reported, or workloadio.ExitUnreported when it reported none (see
// workloadio.Observed.Status). It also writes that to
// cfg.WorkloadExitFile, if one was asked for. ok is false for a VM that
// runs no wrapped workload, whose exit code is still cloud-hypervisor's.
func workloadExit(cfg config.Config, observed workloadio.Observed, vmErr error) (code int, ok bool) {
	status, ok := observed.Status(cfg.WorkloadExitFile != "")
	if !ok {
		return 0, false
	}
	vm := "exited cleanly"
	var exitErr *exec.ExitError
	if errors.As(vmErr, &exitErr) {
		vm = fmt.Sprintf("exited with status %d", exitErr.ExitCode())
	} else if vmErr != nil {
		vm = fmt.Sprintf("failed: %v", vmErr)
	}
	if status.Reported {
		log.Printf("cloud-hypervisor %s; exiting with the wrapped container's status, %d", vm, status.ExitCode)
	} else {
		log.Printf("cloud-hypervisor %s, and the wrapped container reported no exit status (%s); exiting with %d", vm, status.Reason, status.ExitCode)
	}
	if cfg.WorkloadExitFile != "" {
		if err := workloadio.WriteExitFile(cfg.WorkloadExitFile, status); err != nil {
			log.Printf("%v", err)
		}
	}
	return status.ExitCode, true
}

// forwardWorkloadOutput writes the wrapped container's own stdout and
// stderr to cfg.WorkloadStdout and cfg.WorkloadStderr, apart from the
// console, until ctx is cancelled or, once vmExited is closed, the guest
// has nothing more to send (see pkg/workloadio). The returned
// channel is closed when it stops; with neither set, it does nothing and
// the channel is already closed.
//
// A destination whose reader goes away -- a FIFO's -- is opened again,
// which waits for the next reader; the guest keeps what the workload
// writes in the meantime. Anything else that goes wrong is logged and
// ends the forwarding, never the VM: the console still has the output.
//
// The container's input, if asked for (cfg.WorkloadStdin and
// cfg.WorkloadResize), goes the other way on the same connection: see
// workloadInput.
func forwardWorkloadOutput(ctx context.Context, vmExited <-chan struct{}, cfg config.Config) <-chan struct{} {
	done := make(chan struct{})
	if cfg.WorkloadStdout == "" && cfg.WorkloadStderr == "" && cfg.WorkloadStdin == "" && cfg.WorkloadResize == "" {
		close(done)
		return done
	}
	in := workloadInput(ctx, cfg)
	go func() {
		defer close(done)
		for {
			stdout, stderr, closeOutputs, err := workloadio.OpenOutputs(ctx, cfg.WorkloadStdout, cfg.WorkloadStderr)
			if err != nil {
				if ctx.Err() == nil {
					log.Printf("cannot open the workload's output: %v", err)
				}
				return
			}
			err = workloadio.Forward(ctx, vmExited, cfg.VsockSocket, stdout, stderr, in)
			closeOutputs()
			if err == nil || ctx.Err() != nil {
				return
			}
			if !workloadio.IsBrokenReader(err) {
				log.Printf("stopped forwarding the workload's output: %v", err)
				return
			}
		}
	}()
	return done
}

// workloadInput reads the wrapped container's stdin from cfg.WorkloadStdin
// and its terminal's sizes from cfg.WorkloadResize, for Forward to send
// to the guest, until ctx is cancelled. It is nil when neither is set.
//
// The stdin is read once, to its end: a FIFO is waited on for its writer,
// and that writer closing it is the container's end-of-file. The resize
// FIFO is opened again each time its writer closes it, so that a caller
// may open it for each size; a regular file is read once.
func workloadInput(ctx context.Context, cfg config.Config) *workloadio.HostInput {
	if cfg.WorkloadStdin == "" && cfg.WorkloadResize == "" {
		return nil
	}
	in := workloadio.NewHostInput()
	if cfg.WorkloadStdin != "" {
		go func() {
			f, err := openInput(ctx, cfg.WorkloadStdin)
			if err != nil {
				if ctx.Err() == nil {
					log.Printf("cannot open the workload's stdin: %v", err)
				}
				return
			}
			defer f.Close()
			if err := in.CopyStdin(ctx.Done(), f); err != nil {
				log.Printf("reading the workload's stdin: %v; ending it", err)
			}
		}()
	}
	if cfg.WorkloadResize != "" {
		go func() {
			for {
				f, err := openInput(ctx, cfg.WorkloadResize)
				if err != nil {
					if ctx.Err() == nil {
						log.Printf("cannot open the workload's terminal sizes: %v", err)
					}
					return
				}
				fifo := false
				if st, err := f.Stat(); err == nil {
					fifo = st.Mode()&os.ModeNamedPipe != 0
				}
				readSizes(f, in)
				f.Close()
				if !fifo || ctx.Err() != nil {
					return
				}
			}
		}()
	}
	return in
}

// readSizes applies each "COLS ROWS" line r has to in.
func readSizes(r io.Reader, in *workloadio.HostInput) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		cols, rows, err := workloadio.ParseWinsize(sc.Text())
		if err != nil {
			log.Printf("%v", err)
			continue
		}
		in.Resize(cols, rows)
	}
}

// openInput opens path for reading, which for a FIFO waits for a writer,
// giving up when ctx is cancelled. The open still blocked then is
// abandoned; it lasts no longer than this process does.
func openInput(ctx context.Context, path string) (*os.File, error) {
	type result struct {
		f   *os.File
		err error
	}
	ch := make(chan result, 1)
	go func() {
		f, err := os.Open(path)
		ch <- result{f, err}
	}()
	select {
	case r := <-ch:
		return r.f, r.err
	case <-ctx.Done():
		go func() {
			if r := <-ch; r.f != nil {
				r.f.Close()
			}
		}()
		return nil, fmt.Errorf("waiting for a writer on %s: %w", path, ctx.Err())
	}
}

// startMemAgent starts pkg/memagent's listener in the background, and
// delivers its token to the guest, so
// the guest-side agent baked into the disk image (see
// deploy/guest-image's granule-mem-agent) can ask this already-running VM
// to grow its own memory when it observes pressure -- the automatic
// counterpart to an operator's manual "granule resize". It talks to
// cloud-hypervisor over its own APIClient rather than reusing runner's
// (unexported) one: both just dial the same api socket, so there's no
// shared state to coordinate. Runs until ctx is cancelled (i.e. until
// runVM's own shutdown signal handling fires); errors are logged rather
// than fatal, since a guest that can't ask for more memory should still
// boot and run at whatever it already has.
func startMemAgent(ctx context.Context, cfg config.Config) {
	server := memagent.New(memagent.Config{
		Addr:              cfg.MemAgentAddr,
		Token:             cfg.MemAgentToken,
		MinMB:             cfg.MemoryMB,
		MaxMB:             cfg.MemoryMaxMB,
		StepMB:            cfg.MemAgentStepMB,
		Cooldown:          cfg.MemAgentCooldown,
		ScaleDownCooldown: cfg.MemAgentScaleDownCooldown,
	}, hypervisor.NewAPIClient(cfg.APISocket))

	go func() {
		if err := server.Serve(ctx); err != nil && ctx.Err() == nil {
			log.Printf("mem-agent listener: %v", err)
		}
	}()

	// On every start, a resume included: a resumed guest holds whatever
	// token was current when it was suspended, and an unset
	// CHV_MEM_AGENT_TOKEN means this run made a new one.
	gcfg, err := memAgentExecConfig(cfg)
	if err != nil {
		log.Printf("mem-agent: cannot deliver the token to the guest, so it will not signal: %v", err)
		return
	}
	run := func(ctx context.Context, line string, stdin io.Reader, stderr io.Writer) (int, error) {
		return guestexec.RunLine(ctx, gcfg, line, stdin, io.Discard, stderr)
	}
	go func() {
		if err := memagent.DeliverToken(ctx, run, cfg.MemAgentToken, 5*time.Second); err != nil {
			if ctx.Err() == nil {
				log.Printf("mem-agent: delivering the token to the guest: %v", err)
			}
			return
		}
		log.Printf("mem-agent: token delivered to the guest's %s", memagent.TokenFile)
	}()
}

// memAgentExecConfig is the guest exec session the memory agent's token
// is delivered over: the VM's own vsock socket, as root whatever account
// GRANULE_EXEC_USER names for everyone else (the token file is root's,
// and so is the agent reading it), in no particular directory and with
// no extra environment.
func memAgentExecConfig(cfg config.Config) (guestexec.Config, error) {
	gcfg, err := guestexec.FromEnv()
	if err != nil {
		return guestexec.Config{}, err
	}
	if cfg.VsockSocket == "" {
		return guestexec.Config{}, errors.New("this VM has no vsock device (CHV_VSOCK_SOCKET is empty)")
	}
	gcfg.Socket = cfg.VsockSocket
	gcfg.User = "root"
	gcfg.Dir = ""
	gcfg.Env = nil
	return gcfg, nil
}

// runSetupScript runs cfg.SetupScript once inside the guest over its
// vsock device (see pkg/guestexec, the same machinery "granule exec"
// uses below).
// If cfg.SnapshotPath is set, it then -- once the script exits zero --
// suspends the VM to it (see hypervisor.Runner.Suspend) so a future
// granule run resumes this exact post-setup state instead of booting
// fresh and re-running the script; left unset, the script will simply
// run again on the next fresh boot. Only called for a freshly booted
// VM: runVM skips this when runner.Restored() is already true.
func runSetupScript(ctx context.Context, cfg config.Config, runner *hypervisor.Runner) error {
	gcfg, err := guestexec.FromEnv()
	if err != nil {
		return fmt.Errorf("configuring guest exec for the setup script: %w", err)
	}

	log.Printf("running setup script in guest")
	code, err := guestexec.RunLine(ctx, gcfg, cfg.SetupScript, nil, os.Stdout, os.Stderr)
	if err != nil {
		return fmt.Errorf("running setup script: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("setup script exited with status %d", code)
	}

	if cfg.SnapshotPath == "" {
		log.Printf("setup script complete")
		return nil
	}

	log.Printf("setup script complete, suspending to %s", cfg.SnapshotPath)
	if err := runner.Suspend(ctx); err != nil {
		return fmt.Errorf("suspending vm: %w", err)
	}
	log.Printf("suspended: future runs will resume from this state instead of rerunning the setup script")
	return nil
}

// runExec connects to the VM guest and runs args as a command there (or,
// given none, an interactive login shell), proxying stdin/stdout/stderr
// and, for a TTY, window resizes -- see pkg/guestexec. Meant to be
// invoked as `kubectl exec`'s own command (e.g. `kubectl exec -it <pod>
// -c <container> -- granule exec -- <command>`), since the container
// itself ships no shell of its own to exec into (see the "final" stage
// of the Dockerfile).
//
// -w and -e (see guestexec.ParseArgs) are the two things about the
// command that used to have to be written into the command line itself:
// a working directory as a "cd ... &&" prefix, a variable as an "export"
// in front of it, both quoted correctly by the caller.
func runExec(args []string) error {
	opts, err := guestexec.ParseArgs(args)
	if errors.Is(err, flag.ErrHelp) {
		// "granule exec -h" asked a question rather than got something
		// wrong, so it answers on stdout and exits zero -- the same as
		// "granule cp -h".
		fmt.Println(guestexec.Usage)
		return nil
	}
	if err != nil {
		return err
	}

	return runGuestSession(func(ctx context.Context, cfg guestexec.Config) (int, error) {
		// Per command rather than per container, which is why these come
		// from flags and not from cfg's own environment variables. The
		// account is the one exception that has both spellings: a
		// container's GRANULE_EXEC_USER is the default for every session
		// into it, and -u overrides it for this one command.
		cfg.Dir, cfg.Env, cfg.Container = opts.Workdir, opts.Env, opts.Container
		if opts.User != "" || opts.Container {
			// GRANULE_EXEC_USER names a guest account. Inside the
			// wrapped container it means nothing, and the default there
			// is the container's own USER, which an empty user asks for.
			cfg.User = opts.User
		}
		return guestexec.Run(ctx, cfg, opts.Command, os.Stdin, os.Stdout, os.Stderr)
	})
}

// runForward bridges a multiplexed TCP port-forwarding session between this
// container's stdin/stdout and the guest agent over vsock.
func runForward(args []string) error {
	opts, err := guestexec.ParseForwardArgs(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Println(guestexec.ForwardUsage)
		return nil
	}
	if err != nil {
		return err
	}
	cfg, err := guestexec.FromEnv()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return guestexec.Forward(ctx, cfg, opts.Remotes, os.Stdin, os.Stdout)
}

// runReady answers one question about this container's own VM -- is the
// guest up? -- with an exit status and nothing else, so that a caller
// stops needing a poll loop around "granule exec -- true" of its own.
//
// Being a mode of the same binary in the same container, it is usable as
// a container readiness probe with nothing extra in the image:
//
//	readinessProbe:
//	  exec:
//	    command: ["granule", "ready", "-timeout", "0"]
//
// which is what makes "kubectl wait --for=condition=Ready" report on the
// guest rather than on the container wrapping it (see the pod manifests
// under deploy/k8s/, which set exactly that). The
// "-timeout 0" is a single attempt: the kubelet retries on its own
// schedule, and a probe that did its own retrying would only be racing
// it.
//
// -timeout defaults to whatever GRANULE_EXEC_CONNECT_TIMEOUT would give a
// session (30s if unset), so a "granule ready" with no flags waits about
// as long as a "granule exec" into a booting guest would.
//
// What being ready means is guestexec.Ready's doc comment: granule-agent
// answering, plus -- only when one is configured -- a file the guest
// creates for itself when its own workload is up. -file is where that
// path comes from on a command line; GRANULE_READY_FILE is where it comes
// from for the probe in the manifest, whose command line is fixed. With
// neither set this asks exactly what it asked before either existed, so
// a guest image that declares nothing is unaffected.
func runReady(args []string) error {
	cfg, err := guestexec.FromEnv()
	if err != nil {
		return err
	}

	fs := flag.NewFlagSet("granule ready", flag.ContinueOnError)
	timeout := fs.Duration("timeout", cfg.ConnectTimeout, "how long to keep probing the guest before giving up; 0 makes it a single attempt, which is what a readiness probe wants")
	readyFile := fs.String("file", cfg.ReadyFile, "absolute path inside the guest that must exist as well, the guest's own declaration that its workload is up (default: GRANULE_READY_FILE, unset meaning the agent answering is the whole check)")
	if err := fs.Parse(args); err != nil {
		// "granule ready -h" asked a question and Parse has already
		// answered it with the usage above, so it exits zero -- the same
		// as "granule cp -h" and "granule resize -h".
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *timeout < 0 {
		return fmt.Errorf("-timeout must not be negative, got %s", *timeout)
	}
	if *readyFile != "" && !strings.HasPrefix(*readyFile, "/") {
		// Refused rather than resolved: the path is checked in a session
		// that starts in the account's home directory, so a relative one
		// would quietly mean a different file per account -- and one
		// inside the guest, where nothing out here can see that it is
		// the wrong file.
		return fmt.Errorf("-file %q: the guest path must be absolute", *readyFile)
	}
	cfg.ConnectTimeout = *timeout
	cfg.ReadyFile = *readyFile

	ctx, cancel := signalledContext()
	defer cancel()
	// Silent on success: the exit status is the whole answer, and a probe
	// that printed on every success would fill a kubelet's logs with it.
	return guestexec.Ready(ctx, cfg)
}

// runCp copies a file or directory between this container's own
// stdin/stdout and the guest's filesystem, over the same session "exec"
// above uses -- see pkg/guestcp. Meant to be invoked the same way,
// with the caller's own redirection supplying or receiving the stream:
//
//	docker exec -i granule-vm-web granule cp - /srv/app.tar < app.tar
//	docker exec    granule-vm-web granule cp /srv/app.tar - > app.tar
//
// Unlike runExec and runShell this does not exit with the guest command's
// status: a copy either happened or it did not, so what the guest's shell
// exited with is part of the error message rather than something to hand
// back as this process's own answer.
func runCp(args []string) error {
	opts, err := guestcp.ParseArgs(args)
	if errors.Is(err, flag.ErrHelp) {
		// "granule cp -h" asked a question rather than got something
		// wrong, so it answers on stdout and exits zero instead of
		// arriving as a log line prefixed "granule: cp:".
		fmt.Println(guestcp.Usage)
		return nil
	}
	if err != nil {
		return err
	}
	cfg, err := guestexec.FromEnv()
	if err != nil {
		return err
	}

	ctx, cancel := signalledContext()
	defer cancel()
	return guestcp.Copy(ctx, cfg, opts, os.Stdin, os.Stdout, os.Stderr)
}

// runResize live-resizes this container's own already-running VM's
// guest memory and/or vCPU count via the cloud-hypervisor API (see
// pkg/hypervisor's APIClient.Resize/ResizeCPUs), within the ranges
// CHV_MEMORY_MB..CHV_MEMORY_MAX_MB and CHV_CPUS..CHV_CPUS_MAX that were
// configured at boot (see pkg/config) -- it does not, and cannot,
// change those ranges itself. Meant to be invoked the same way "exec"
// is, e.g. `kubectl exec <pod> -c <container> -- granule resize
// -memory-mb=1024` or `-cpus=4` (either or both may be given in one
// call), since reaching this container's API socket from outside it has
// no other path. Requires CHV_MEMORY_HOTPLUG (for -memory-mb) or a
// CHV_CPUS_MAX above CHV_CPUS (for -cpus) to have been set at boot;
// cloud-hypervisor rejects the corresponding resize otherwise.
//
// Asking for the size the guest already has is not an error in either
// half: the current size is read back from cloud-hypervisor first (see
// APIClient.Info) and a request that would change nothing is skipped
// with a line saying so, rather than sent and rejected. That makes a
// script that pins a VM to a fixed size safe to run twice -- without
// it, cloud-hypervisor's virtio-mem device fails the second -memory-mb
// with a 500 while the second -cpus quietly succeeds. A -cpus
// value below the current count asks cloud-hypervisor to remove vCPUs,
// which only completes once the guest acknowledges the removal -- a
// second -cpus call made before that finishes fails with a 429 from
// cloud-hypervisor ("a cpu removal is still pending"); see
// APIClient.ResizeCPUs and the README's "CPU hotplug" section.
func runResize(args []string) error {
	fs := flag.NewFlagSet("granule resize", flag.ContinueOnError)
	memoryMB := fs.Int("memory-mb", 0, "desired guest memory size, in MiB")
	cpus := fs.Int("cpus", 0, "desired vCPU count")
	timeout := fs.Duration("timeout", 10*time.Second, "how long to wait for cloud-hypervisor's API to respond")
	if err := fs.Parse(args); err != nil {
		// "granule resize -h" asked a question and Parse has already
		// answered it with the usage above, so it exits zero rather
		// than adding a "flag: help requested" line to it -- the same
		// as "granule cp -h".
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	// A negative size is a typo, not a request to leave that half alone:
	// saying "required" to someone who did pass -memory-mb sends them
	// looking in the wrong place.
	if *memoryMB < 0 || *cpus < 0 {
		return fmt.Errorf("-memory-mb and -cpus must be positive, got -memory-mb=%d -cpus=%d", *memoryMB, *cpus)
	}
	if *memoryMB == 0 && *cpus == 0 {
		return fmt.Errorf("at least one of -memory-mb or -cpus is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	api := hypervisor.NewAPIClient(config.APISocket())
	if *memoryMB > 0 {
		resized, err := api.ResizeIfNeeded(ctx, uint64(*memoryMB)*1024*1024)
		if err != nil {
			return fmt.Errorf("resizing memory: %w", err)
		}
		if resized {
			log.Printf("requested resize to %d MiB", *memoryMB)
		} else {
			log.Printf("already at %d MiB, leaving memory alone", *memoryMB)
		}
	}
	if *cpus > 0 {
		resized, err := api.ResizeCPUsIfNeeded(ctx, uint32(*cpus))
		if err != nil {
			return fmt.Errorf("resizing cpus: %w", err)
		}
		if resized {
			log.Printf("requested resize to %d vCPU(s)", *cpus)
		} else {
			log.Printf("already at %d vCPU(s), leaving cpus alone", *cpus)
		}
	}
	return nil
}

// runShell implements the "sh"/"bash" shim described in this file's own
// doc comment: args is os.Args[1:] exactly as a real sh/bash invoked the
// same way would have received it. See guestexec.ShellCommandLine for
// which invocations are actually supported.
func runShell(args []string) error {
	line, err := guestexec.ShellCommandLine(args)
	if err != nil {
		return err
	}

	return runGuestSession(func(ctx context.Context, cfg guestexec.Config) (int, error) {
		return guestexec.RunLine(ctx, cfg, line, os.Stdin, os.Stdout, os.Stderr)
	})
}

// runGuestSession is the shared machinery behind runExec and runShell:
// it loads guestexec's config, wires SIGTERM/SIGINT into cancelling the
// session the same way runVM turns them into a VM shutdown, runs the
// guest session via runSession, and exits directly with its exit code on
// success (mirroring runVM's own handling of cloud-hypervisor's exit
// code) rather than returning nil, since 0 is itself a meaningful,
// common outcome the caller (main) would otherwise have no way to
// distinguish from "never ran".
func runGuestSession(runSession func(ctx context.Context, cfg guestexec.Config) (int, error)) error {
	cfg, err := guestexec.FromEnv()
	if err != nil {
		return err
	}

	ctx, cancel := signalledContext()
	defer cancel()

	code, err := runSession(ctx, cfg)
	if err != nil {
		return err
	}
	os.Exit(code)
	return nil
}

// signalledContext returns a context that SIGTERM/SIGINT cancels, which
// is what tears a guest session down the same way those signals turn
// into a VM shutdown in runVM. Shared by every mode that holds a session
// open (exec, the sh/bash shims, cp), since a copy interrupted with ^C
// has to end the guest's own command too rather than leave it writing
// into a stream nobody is reading.
func signalledContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		if sig, ok := <-sigCh; ok {
			log.Printf("received %s, closing guest session", sig)
			cancel()
		}
	}()
	return ctx, cancel
}

// runNetshim splices this namespace's single VM onto the segment the
// container runtime put the sandbox on, for the VM container that
// follows it, exactly as the standalone netshim binary used to.
func runNetshim() error {
	cfg, err := netshim.FromEnv()
	if err != nil {
		return err
	}
	// Resolved here only so the log line below names the interface;
	// Setup would resolve an "auto" the same way on its own.
	if cfg, err = cfg.ResolveExternalIface(); err != nil {
		return err
	}

	if err := netshim.Setup(cfg); err != nil {
		return err
	}

	log.Printf("network shim ready: %s spliced onto %s", cfg.TapName(), cfg.ExternalIface)
	return nil
}

// applyNetshimNet fills in the guest configuration the VM cannot be told
// in advance: the address, MAC and MTU the container runtime chose for
// this network namespace, which are only knowable once the sandbox
// exists.
//
// It is deliberately done here, from inside the namespace, rather than
// computed by whoever created the sandbox and passed down as environment.
// Reading them back off the external interface works identically whether
// the sandbox came from "docker run", a kubelet, or something else, so
// the same image behaves the same way under all of them -- and there is
// no second copy of the identity to drift out of date.
//
// This relies on netshim having left that interface addressed, which
// netshim.Setup documents it does.
func applyNetshimNet(cfg *config.Config, ncfg netshim.Config, id netshim.Identity) {
	guest := netshim.GuestConfig(ncfg, id)
	cfg.Nets = guest.Nets

	cmdline := netshim.WithGuestParams(cfg.Cmdline, guest)
	applied := cmdline != cfg.Cmdline
	if !applied {
		log.Printf("CHV_CMDLINE configures an interface itself, so the identity discovered on %s is not applied to it", id.Iface)
	}
	cfg.Cmdline = cmdline

	log.Printf("guest takes over %s as %s (mac %s, mtu %d)",
		id.Iface, id.Addresses(), id.MAC, id.MTU)
	if warning := ipv6ContractWarning(cfg.GuestContract, id); warning != "" && applied {
		log.Print(warning)
	}
	// The routes are what an address and a netmask cannot say: on a
	// point-to-point CNI the subnet is deliberately not on-link, and a
	// guest that never hears about it can reach its gateway and nothing
	// else on the segment. Logged in full because it is the one part of
	// the identity whose absence looks like a working guest.
	if guest.RouteParam != "" {
		log.Printf("guest also gets the %d route(s) %s carries: %s",
			len(id.Routes), id.Iface, guest.RouteParam)
	}
	if guest.Route6Param != "" {
		log.Printf("guest also gets the %d IPv6 route(s) %s carries: %s",
			len(id.Routes6), id.Iface, guest.Route6Param)
	}
	if len(ncfg.DNS) > 0 {
		log.Printf("guest resolves through %s",
			strings.Join(strings.Fields(netshim.JoinIPs(ncfg.DNS)+" "+guest.DNSSearchParam+" "+guest.DNSOptionsParam), " "))
	}
	if guest.ControlIP != "" {
		log.Printf("control link expects the guest at %s on its second NIC", guest.ControlIP)
	}
}

// runSnapshot captures a full snapshot of this container's already-running VM
// (memory, device registers, and writable disk overlay) via cloud-hypervisor's
// API.
func runSnapshot(args []string) error {
	fs := flag.NewFlagSet("granule snapshot", flag.ContinueOnError)
	path := fs.String("path", guestimage.SnapshotDir, "destination directory to save the snapshot bundle")
	overlay := fs.String("overlay", guestimage.Overlay, "path to the writable disk overlay to include in the snapshot")
	resume := fs.Bool("resume", false, "resume the VM after snapshotting instead of leaving it paused")
	timeout := fs.Duration("timeout", 60*time.Second, "how long to wait for the snapshot operation")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	api := hypervisor.NewAPIClient(config.APISocket())
	if err := api.WaitReady(ctx); err != nil {
		return fmt.Errorf("waiting for cloud-hypervisor API socket: %w", err)
	}

	if err := hypervisor.TakeSnapshot(ctx, api, *path, *overlay, *resume); err != nil {
		return fmt.Errorf("snapshotting VM: %w", err)
	}
	log.Printf("captured VM snapshot to %s", *path)
	return nil
}
