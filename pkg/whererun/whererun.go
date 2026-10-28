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

// Package whererun answers one question for the rest of grain: does this
// daemon run on the bare machine, or inside a container?
//
// Every machine install runs the daemon in the release container
// (scripts/setup.sh; host mode, the daemon as a plain systemd service,
// was removed in task 5041), and Kubernetes runs it in a pod. A daemon on
// the bare machine is a development one: `grain demo`, a local `grain
// daemon`, the e2e harness -- or a host-mode install an older installer
// made, upgraded onto this build by its Upgrade button, until the
// installer moves it (pkg/daemon's installnotice.go, which says so).
//
// The answer is read off the process itself. A daemon counts as
// containerised only on evidence of a container: the variable grain's own
// image sets (GRAIN_IN_CONTAINER=1, Dockerfile), docker's /.dockerenv,
// podman's /run/.containerenv, or a container runtime's name in
// /proc/1/cgroup. GRAIN_IN_CONTAINER=0 says the bare machine outright.
package whererun

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// InContainerEnv is the variable grain's own deployment image sets
// (Dockerfile): the one signal that is about grain's container rather
// than about containers in general.
const InContainerEnv = "GRAIN_IN_CONTAINER"

// Detection is Detect's answer: where this process runs, and what it saw
// that says so.
type Detection struct {
	// Host is true on the host and false in a container.
	Host bool
	// Signal is the evidence, said so it can be quoted to an operator:
	// "/.dockerenv exists", "no container marker found".
	Signal string
}

// Probe is what Detect reads, so a test can describe a process without
// being one. The zero value is not useful; use System.
type Probe struct {
	Getenv   func(string) string
	ReadFile func(string) ([]byte, error)
	// Root is prepended to the marker paths (/.dockerenv,
	// /run/.containerenv, /proc/1/cgroup). Empty for the real ones.
	Root string
}

// System is the Probe of this process.
func System() Probe {
	return Probe{Getenv: os.Getenv, ReadFile: os.ReadFile}
}

// Detect reports where this process runs.
func Detect() Detection { return System().Detect() }

// OnHost is Detect().Host.
func OnHost() bool { return Detect().Host }

// cgroupMarkers are the names container runtimes leave in a process's
// cgroup path under cgroup v1, or under v2 without a private cgroup
// namespace. Matched as path components so that a host whose own slice
// names merely mention one (a user's "docker-compose@" unit) does not.
var cgroupMarkers = []string{"docker", "containerd", "kubepods", "libpod", "lxc", "crio"}

// Detect reports where p's process runs.
func (p Probe) Detect() Detection {
	switch v := strings.TrimSpace(p.Getenv(InContainerEnv)); v {
	case "":
	case "0", "false":
		// Said outright, as a test or a development daemon run inside
		// some other container may: the bare machine, whatever else.
		return Detection{Host: true, Signal: InContainerEnv + "=" + v}
	default:
		return Detection{Host: false, Signal: fmt.Sprintf("%s=%s, which grain's deployment image sets", InContainerEnv, v)}
	}
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := p.ReadFile(filepath.Join(p.Root, marker)); err == nil {
			return Detection{Host: false, Signal: marker + " exists"}
		}
	}
	if data, err := p.ReadFile(filepath.Join(p.Root, "/proc/1/cgroup")); err == nil {
		if marker, line := cgroupMarker(string(data)); marker != "" {
			return Detection{Host: false, Signal: fmt.Sprintf("/proc/1/cgroup names %s (%s)", marker, line)}
		}
	}
	return Detection{Host: true, Signal: "no container marker found"}
}

// cgroupMarker is the first container runtime /proc/1/cgroup's paths
// name, and the line it is on.
func cgroupMarker(cgroup string) (marker, line string) {
	for _, l := range strings.Split(cgroup, "\n") {
		// hierarchy-ID:controllers:path
		parts := strings.SplitN(strings.TrimSpace(l), ":", 3)
		if len(parts) != 3 {
			continue
		}
		for _, comp := range strings.Split(parts[2], "/") {
			for _, m := range cgroupMarkers {
				// "docker", "docker-<id>.scope", "cri-containerd-<id>.scope",
				// "kubepods.slice", "libpod-<id>.scope", "lxc.payload.<name>".
				name := strings.TrimSuffix(strings.TrimSuffix(comp, ".scope"), ".slice")
				if name == m || strings.HasPrefix(name, m+"-") || strings.HasPrefix(name, "cri-"+m+"-") ||
					(m == "lxc" && strings.HasPrefix(name, "lxc.payload")) {
					return m, strings.TrimSpace(l)
				}
			}
		}
	}
	return "", ""
}
