package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/bwsalmon/granule/pkg/dockervm"
	"github.com/bwsalmon/granule/pkg/vmspec"
)

// defaultReadyTimeout is how long "vm create -wait", "vm wait" and "vm
// run" give a guest to answer before giving up. Generous on purpose: a
// cold boot of a stock guest is seconds, but a guest that runs a setup
// script or grows its filesystem onto a larger -disk-size-mb can take
// considerably longer, and the wait ends early anyway if the VM's
// container exits.
const defaultReadyTimeout = 3 * time.Minute

// runVMWait blocks until a VM's guest answers a command, so a script can
// wait for a VM created earlier without inventing its own poll loop.
//
// "vm create" returns once the containers are started, which is before
// the guest has booted; "vm create -wait" folds this in for the VM it
// makes, and this is the same wait for a VM that already exists -- one
// started without -wait, one being restarted, or one a different script
// created.
func runVMWait(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	name, rest, err := splitNameAndFlags(args, "vm wait")
	if err != nil {
		return err
	}

	fs := flag.NewFlagSet("granule vm wait "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", defaultStateDir(), "directory granule stores VM state in")
	timeout := fs.Duration("timeout", defaultReadyTimeout, "how long to wait for the guest to answer before giving up")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	if _, err := vmspec.Load(*stateDir, name); err != nil {
		return fmt.Errorf("VM %q not found in %s (\"granule vm list -state-dir %s\" shows what is)%s: %w", name, *stateDir, *stateDir, stateDirNoteSuffix(*stateDir), err)
	}
	return waitForGuest(ctx, name, *timeout, stdout)
}

// waitForGuest is the readiness wait itself, shared by "vm wait" and "vm
// create -wait": it says what it is waiting for, waits, and says when the
// guest answered.
//
// Progress goes to the writer the caller passes rather than always to
// stderr, because who owns stdout differs between them -- "vm create"'s
// own output is progress, "vm run"'s stdout belongs to the guest command.
func waitForGuest(ctx context.Context, name string, timeout time.Duration, progress io.Writer) error {
	fmt.Fprintf(progress, "waiting for the guest of %q (up to %s)\n", name, timeout)
	if err := dockervm.WaitReady(ctx, &dockervm.Docker{}, name, timeout); err != nil {
		return err
	}
	fmt.Fprintf(progress, "VM %q is ready\n", name)
	return nil
}
