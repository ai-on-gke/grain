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

package whererun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// probe is a Probe of a made-up process: env is its environment, and
// files the marker files under a root of its own.
func probe(t *testing.T, env map[string]string, files map[string]string) Probe {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return Probe{
		Getenv:   func(k string) string { return env[k] },
		ReadFile: os.ReadFile,
		Root:     root,
	}
}

func TestDetect(t *testing.T) {
	const hostCgroup = "0::/init.scope\n"
	cases := []struct {
		name       string
		env        map[string]string
		files      map[string]string
		wantHost   bool
		wantSignal string
	}{
		{
			// A unit written before d497083a9: no variable, and nothing
			// about the process says container.
			name:       "pre-d497083 host unit",
			files:      map[string]string{"proc/1/cgroup": hostCgroup},
			wantHost:   true,
			wantSignal: "no container marker found",
		},
		{
			name:       "nothing readable at all",
			wantHost:   true,
			wantSignal: "no container marker found",
		},
		{
			// Host mode's own variable, which a unit an older installer
			// wrote still sets, is no evidence either way any more: what
			// the process itself shows decides.
			name:       "GRAIN_INSTALL_MODE=host is no evidence",
			env:        map[string]string{"GRAIN_INSTALL_MODE": "host", InContainerEnv: "1"},
			wantHost:   false,
			wantSignal: "GRAIN_IN_CONTAINER=1",
		},
		{
			name:       "grain's own image",
			env:        map[string]string{InContainerEnv: "1"},
			wantHost:   false,
			wantSignal: "GRAIN_IN_CONTAINER=1",
		},
		{
			name:       "GRAIN_IN_CONTAINER=0 says the bare machine, whatever else",
			env:        map[string]string{InContainerEnv: "0"},
			files:      map[string]string{".dockerenv": ""},
			wantHost:   true,
			wantSignal: "GRAIN_IN_CONTAINER=0",
		},
		{
			name:       "docker",
			files:      map[string]string{".dockerenv": ""},
			wantHost:   false,
			wantSignal: "/.dockerenv exists",
		},
		{
			name:       "podman",
			files:      map[string]string{"run/.containerenv": ""},
			wantHost:   false,
			wantSignal: "/run/.containerenv exists",
		},
		{
			name:       "cgroup v1 docker",
			files:      map[string]string{"proc/1/cgroup": "12:pids:/docker/0123abcd\n11:memory:/docker/0123abcd\n"},
			wantHost:   false,
			wantSignal: "/proc/1/cgroup names docker",
		},
		{
			name:       "kubernetes",
			files:      map[string]string{"proc/1/cgroup": "0::/kubepods.slice/kubepods-burstable.slice/cri-containerd-ab.scope\n"},
			wantHost:   false,
			wantSignal: "/proc/1/cgroup names kubepods",
		},
		{
			name:       "a host slice that merely mentions docker is a host",
			files:      map[string]string{"proc/1/cgroup": "0::/system.slice/mydockerthing.service\n"},
			wantHost:   true,
			wantSignal: "no container marker found",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := probe(t, tc.env, tc.files).Detect()
			if d.Host != tc.wantHost || !strings.Contains(d.Signal, tc.wantSignal) {
				t.Fatalf("Detect() = %+v, want Host=%v with a signal naming %q", d, tc.wantHost, tc.wantSignal)
			}
		})
	}
}
