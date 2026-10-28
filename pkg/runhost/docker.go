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

package runhost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
)

// Driver creates, lists and destroys run hosts. It never waits on one and
// never calls into one: liveness is the listing, output is the spool.
type Driver interface {
	Create(ctx context.Context, spec Spec) error
	// List returns every run host of deployment, running or exited.
	List(ctx context.Context, deployment string) ([]Info, error)
	// Destroy removes a run host, killing its process if it still runs.
	// A name that is already gone is not an error.
	Destroy(ctx context.Context, name string) error
}

// Docker is the single-machine Driver: one container per run, a sibling
// of grain-daemon started through the docker socket the daemon already
// has. It is a sibling rather than a process inside grain-daemon because
// grain-daemon.service's `docker rm -f grain-daemon` takes everything
// inside that container with it on every restart; on a host-binary
// install it is equally outside the daemon unit's cgroup, so the unit's
// KillMode never reaches it.
type Docker struct {
	// Bin is the docker CLI, "docker" when empty.
	Bin string
	// Network is the network mode, "host" when empty: the run host's
	// mcpserver reaches the daemon at the address it does today.
	Network string
	// Env is set on the container: HOME, chiefly, which a uid with no
	// passwd entry in the image otherwise gets as "/", where a CLI cannot
	// write its config or its session store. Never a secret: `docker
	// inspect` shows it, and a run's own environment travels in the
	// spool's env file instead.
	Env map[string]string
	// run is the exec seam a test replaces.
	run func(ctx context.Context, args ...string) ([]byte, error)
}

func (d Docker) exec(ctx context.Context, args ...string) ([]byte, error) {
	if d.run != nil {
		return d.run(ctx, args...)
	}
	bin := d.Bin
	if bin == "" {
		bin = "docker"
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = dockerEnv(os.Environ())
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("docker %s: %w (%s)", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// dockerEnv is the part of the daemon's environment the docker CLI is
// given: what finds the binary's own helpers and its config, and the
// DOCKER_* variables that name the daemon to talk to. Nothing else -- the
// daemon's API keys and tokens have no business in a docker client, and a
// run's own environment reaches its run host through the spool's env
// file instead (WriteSpool), never through `docker run -e`.
func dockerEnv(environ []string) []string {
	var env []string
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		if k == "PATH" || k == "HOME" || strings.HasPrefix(k, "DOCKER_") || k == "XDG_RUNTIME_DIR" {
			env = append(env, kv)
		}
	}
	return env
}

// createArgs is the `docker run` argv for spec.
func (d Docker) createArgs(spec Spec) []string {
	network := d.Network
	if network == "" {
		network = "host"
	}
	args := []string{"run", "--detach", "--name", spec.Name, "--network", network,
		// The run host's own process is the entrypoint's; tini would be
		// the image's choice, and the image's ENTRYPOINT is grain itself.
		"--entrypoint", spec.Command[0],
		// Never restarted: a run host whose CLI exited is finished, and
		// its exit.json is the record of it.
		"--restart", "no"}
	if spec.User != "" {
		args = append(args, "--user", spec.User)
	}
	labels := spec.Labels.Map()
	for _, k := range slices.Sorted(maps.Keys(labels)) {
		args = append(args, "--label", k+"="+labels[k])
	}
	for _, k := range slices.Sorted(maps.Keys(d.Env)) {
		args = append(args, "--env", k+"="+d.Env[k])
	}
	for _, m := range spec.Mounts {
		v := m.Path + ":" + m.Path
		if m.ReadOnly {
			v += ":ro"
		}
		args = append(args, "--volume", v)
	}
	args = append(args, spec.Image)
	return append(args, spec.Command[1:]...)
}

// SharesHostLoopback reports whether a container on d's network reaches
// the daemon's loopback: on the host network, which is the default, it
// does.
func (d Docker) SharesHostLoopback() bool { return d.Network == "" || d.Network == "host" }

// Create implements Driver.
func (d Docker) Create(ctx context.Context, spec Spec) error {
	if len(spec.Command) == 0 {
		return fmt.Errorf("runhost: spec %s has no command", spec.Name)
	}
	_, err := d.exec(ctx, d.createArgs(spec)...)
	return err
}

// inspected is the part of `docker inspect` List reads.
type inspected struct {
	Name   string `json:"Name"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	State struct {
		Running bool `json:"Running"`
	} `json:"State"`
}

// List implements Driver.
func (d Docker) List(ctx context.Context, deployment string) ([]Info, error) {
	out, err := d.exec(ctx, "ps", "--all", "--quiet", "--no-trunc",
		"--filter", "label="+LabelDeployment+"="+deployment)
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return nil, nil
	}
	out, err = d.exec(ctx, append([]string{"inspect"}, ids...)...)
	if err != nil {
		return nil, err
	}
	var all []inspected
	if err := json.Unmarshal(out, &all); err != nil {
		return nil, fmt.Errorf("runhost: reading docker inspect: %w", err)
	}
	var infos []Info
	for _, c := range all {
		l, err := ParseLabels(c.Config.Labels)
		if err != nil || l.Deployment != deployment {
			continue
		}
		infos = append(infos, Info{Name: strings.TrimPrefix(c.Name, "/"), Labels: l, Running: c.State.Running})
	}
	return infos, nil
}

// Destroy implements Driver.
func (d Docker) Destroy(ctx context.Context, name string) error {
	_, err := d.exec(ctx, "rm", "--force", name)
	if err != nil && strings.Contains(err.Error(), "No such container") {
		return nil
	}
	return err
}
