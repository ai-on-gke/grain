package dockervm

import (
	"bytes"
	"context"
	"strings"
)

// ContainerState is what docker says about a VM's own container, as far as
// anything reaching for the guest cares: it is there and running, it is
// there and isn't, or there is no such container at all. The three are
// kept apart because they want different things done about them -- wait,
// read the logs, or create the VM again.
type ContainerState string

const (
	ContainerRunning ContainerState = "running"
	ContainerExited  ContainerState = "exited"
	ContainerMissing ContainerState = "missing"
)

// containerState asks docker what state a container is in, reporting a
// container that isn't there at all as ContainerMissing rather than as an
// error: to a caller reporting on a VM that is a fact about the VM, not a
// failure to find out.
func (d *Docker) containerState(ctx context.Context, container string) (ContainerState, error) {
	var out bytes.Buffer
	if err := d.run(ctx, &out, "inspect", "-f", "{{.State.Running}}", container); err != nil {
		if isNotFound(err) {
			return ContainerMissing, nil
		}
		return "", err
	}
	if strings.TrimSpace(out.String()) == "true" {
		return ContainerRunning, nil
	}
	return ContainerExited, nil
}

// State answers "is this VM's container there?" and nothing else: it is
// Inspect without the guest probe, for a caller that only needs to know
// whether the VM exists at all.
//
// Inspect is the answer worth having when the question is "can I reach
// the guest?", but it execs into a running container to find out, which
// is a cost (and a wait) nothing gets from a caller that is about to tear
// the container down anyway -- see pkg/cli's deleteVM, which asks
// this to tell a VM that is genuinely gone from one whose spec was saved
// under a different state directory.
func State(ctx context.Context, d *Docker, name string) (ContainerState, error) {
	return d.containerState(ctx, vmContainerName(name))
}

// containerEnv reads one variable out of a container's own environment,
// returning "" for a variable that isn't set.
//
// This is how the fallback readiness probe finds the guest's declared
// ready file (see probeLegacy): the path lives on the VM container as
// GRANULE_READY_FILE, where "granule ready" reads it from the inside, and
// an image predating that mode has to be told the path from out here
// instead. Asking docker rather than the saved VM spec keeps the two
// probes asking the same question of the same container -- a VM whose
// spec has since been edited but not re-created included.
func (d *Docker) containerEnv(ctx context.Context, container, key string) (string, error) {
	var out bytes.Buffer
	if err := d.run(ctx, &out, "inspect", "-f", "{{range .Config.Env}}{{println .}}{{end}}", container); err != nil {
		return "", err
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), key+"="); ok {
			return v, nil
		}
	}
	return "", nil
}
