// Command granule is the operator-facing CLI for a granule node: it
// creates, updates, reaches into and deletes VMs on the node's own docker
// daemon ("granule vm ..."), without needing a checkout of this repo on
// the node. A Kubernetes deployment applies a pod manifest instead (see
// deploy/k8s/gke.md) and needs none of this.
//
// This is distinct from cmd/granule-shim, which is the container-facing binary
// that actually runs inside a VM's containers.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/bwsalmon/granule/pkg/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	os.Exit(cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
