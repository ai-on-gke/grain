// Package cli implements the granule command-line tool: creating,
// updating, reaching into and deleting granule VMs on a local docker
// daemon, all from a single binary. See cmd/granule/main.go for the
// entrypoint.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/bwsalmon/granule/pkg/dockercli"
)

const usage = `granule manages granule VMs as containers on a local docker daemon. On
Kubernetes, apply a pod manifest instead -- see deploy/k8s/gke.md.

Usage:
  granule [-docker <cli>] <command> ...

  -docker <cli>  the container CLI to drive instead of "docker": a path, or
                 a name looked up on PATH. Same as setting $GRANULE_DOCKER.

  granule doctor [-only <check>,...]          check this host has what a VM needs, and say what is missing
  granule vm create <name> [flags]            start a new VM
  granule vm run <name> [flags] -- <command>  create a VM, wait for it, run one command, delete it
  granule vm exec <name> [flags] -- <command> run a command in a running VM's guest
  granule vm shell <name> [flags]             open an interactive shell in a running VM's guest
  granule vm wait <name> [flags]              block until a VM's guest answers a command
  granule vm status <name> [flags]            report whether a VM's guest is up, without waiting
  granule vm update <name> [flags]            change a VM's settings and re-submit it
  granule vm delete <name>                    remove a VM
  granule vm list [-status]                   list VMs granule knows about
  granule forward [-L spec] [-R spec] <name>  forward TCP ports between this host and a VM guest
  granule guest build [flags]                 build a bootable guest from a Dockerfile (-setup: boot one and provision it instead)
  granule guest pack [flags]                  pack a guest rootfs image into a bootable granule image
  granule guest rebase [flags]                make an existing guest image's granule half current
  granule guest verify [flags]                boot a guest image, run checks in it, print a verdict
  granule guest flatten [flags]               replay qcow2 layers against a flat file and generate a single layer
  granule granulefile check [flags]            validate a Granule and report, building nothing
  granule granulefile build [flags]            build a Granule into a container or VM image

Run "granule <command> -h" for a command's flags.
`

// Run executes granule with args (os.Args[1:]) and returns the process
// exit code.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	args, err := globalFlags(args)
	if err != nil {
		fmt.Fprintf(stderr, "granule: %v\n\n", err)
		fmt.Fprint(stderr, usage)
		return 2
	}
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "vm":
		err = runVM(ctx, rest, stdin, stdout, stderr)
	case "forward":
		err = runForward(ctx, rest, stderr)
	case "guest":
		err = runGuest(ctx, rest, stdout, stderr)
	case "granulefile":
		err = runGranule(ctx, rest, stdin, stdout, stderr)
	case "doctor":
		err = runDoctor(ctx, rest, stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "granule: unknown command %q\n\n", cmd)
		fmt.Fprint(stderr, usage)
		return 2
	}

	// A subcommand's own "-h" asked a question and has already been
	// answered: the flag package printed that command's flags on the way
	// out. Adding "granule: flag: help requested" underneath them and
	// exiting non-zero makes a satisfied request read as a mistake, and
	// makes "granule vm create -h" unusable in any script that checks
	// statuses. (cmd/granule-shim answers "granule cp -h" the same way.)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}

	// A command run in a guest ("vm exec"/"vm shell"/"vm run") that exited
	// non-zero is reported as its own status and nothing else: granule
	// is a pipe here, and the command's stderr has already reached the
	// caller's. Printing "granule: ..." over the top would make every
	// failing guest command look like a granule failure.
	var status *exitStatusError
	if errors.As(err, &status) {
		return status.code
	}
	if err != nil {
		fmt.Fprintf(stderr, "granule: %v\n", err)
		return 1
	}
	return 0
}

// globalFlags consumes the flags that come before the command and apply
// to all of them -- today only -docker -- and returns what is left.
//
// -docker is passed on as $GRANULE_DOCKER rather than threaded through
// every command's options, so that dockercli.Binary() stays the one
// place the choice is made, and the flag and the variable cannot
// disagree about what a command or anything it starts will exec.
func globalFlags(args []string) ([]string, error) {
	for len(args) > 0 {
		name, value, hasValue := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(args[0], "-"), "-"), "=")
		if !strings.HasPrefix(args[0], "-") || name != "docker" {
			return args, nil
		}
		args = args[1:]
		if !hasValue {
			if len(args) == 0 {
				return nil, errors.New("flag needs an argument: -docker")
			}
			value, args = args[0], args[1:]
		}
		if strings.TrimSpace(value) == "" {
			return nil, errors.New("-docker: empty container CLI")
		}
		if err := os.Setenv(dockercli.EnvBinary, value); err != nil {
			return nil, err
		}
	}
	return args, nil
}
