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

package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContainerRunArgsAreHardened(t *testing.T) {
	c := NewContainerSandboxes(ContainerConfig{Image: "img", Workspace: "/home/agent"})
	args := strings.Join(c.runArgs("grain-sandbox-x", Shape{CPUs: 2, MemoryMB: 1024}, ""), " ")
	for _, want := range []string{
		"--cap-drop ALL",
		"--security-opt no-new-privileges",
		"--network " + DefaultContainerNetwork,
		"--pids-limit 4096",
		"--memory 1024m --memory-swap 1024m",
		"--oom-score-adj 500",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("docker run args %q lack %q", args, want)
		}
	}
	if strings.Contains(args, "--cap-add") {
		t.Errorf("docker run args %q add a capability back with none configured", args)
	}
	if !strings.HasSuffix(args, "--workdir /home/agent img sleep infinity") {
		t.Errorf("docker run args %q: the image and its command must come last", args)
	}
}

func TestContainerRunArgsHonourOverrides(t *testing.T) {
	c := NewContainerSandboxes(ContainerConfig{Image: "img", Workspace: "/w",
		Network: "bridge", CapAdd: []string{" CHOWN", "", "SETUID "}, PidsLimit: -1})
	args := strings.Join(c.runArgs("n", Shape{CPUs: 1, MemoryMB: 512}, ""), " ")
	for _, want := range []string{"--network bridge", "--cap-add CHOWN", "--cap-add SETUID", "--cap-drop ALL"} {
		if !strings.Contains(args, want) {
			t.Errorf("docker run args %q lack %q", args, want)
		}
	}
	if strings.Contains(args, "--pids-limit") {
		t.Errorf("docker run args %q set a pids limit though PidsLimit is negative", args)
	}
}

// A network the operator named is theirs: nothing is created, so
// ensureNetwork succeeds without running docker at all.
func TestContainerNamedNetworkIsNotCreated(t *testing.T) {
	c := NewContainerSandboxes(ContainerConfig{Docker: "/nonexistent/docker", Network: "mine"})
	if err := c.ensureNetwork(t.Context()); err != nil {
		t.Fatalf("ensureNetwork with a named network: %v", err)
	}
}

// A create that fails still removes whatever `docker run` left behind,
// on a context the caller's cancellation does not reach, rather than
// leaving a container no handle can release until the next restart.
func TestContainerAcquireRemovesContainerOnFailedCreate(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls")
	docker := filepath.Join(dir, "docker")
	script := "#!/bin/sh\necho \"$*\" >> " + logPath + "\n[ \"$1\" = run ] && exit 1\nexit 0\n"
	if err := os.WriteFile(docker, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	c := NewContainerSandboxes(ContainerConfig{Docker: docker, Image: "img", Workspace: "/w", Network: "mine"})
	if _, err := c.Acquire(t.Context(), "1-1", Shape{}); err == nil {
		t.Fatal("Acquire succeeded though docker run failed")
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Split(strings.TrimSpace(string(data)), "\n")
	last := calls[len(calls)-1]
	if !strings.HasPrefix(calls[len(calls)-2], "run ") || last != "rm -f grain-sandbox-1-1" {
		t.Errorf("docker calls %q: want the failed run followed by rm -f grain-sandbox-1-1", calls)
	}
	if c.SandboxCount() != 0 {
		t.Errorf("a failed Acquire left %d live sandboxes", c.SandboxCount())
	}
}

// A container sandbox is handed nothing that leads to the daemon's
// access code (grain/task-4343): no environment from the daemon -- not
// GRAIN_LOCAL_API_TOKEN_FILE, which the daemon sets in its own -- and
// no mount, so neither the token file nor the data directory holding it
// is a path inside the container.
func TestContainerSandboxesGetNoAccessCode(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("GRAIN_LOCAL_API_TOKEN_FILE", filepath.Join(dataDir, "local-api-token"))
	t.Setenv("GRAIN_DATA_DIR", dataDir)
	c := NewContainerSandboxes(ContainerConfig{Image: "img", Workspace: "/home/agent"})
	args := c.runArgs("grain-sandbox-x", Shape{CPUs: 2, MemoryMB: 1024}, "")
	for i, a := range args {
		if a == "-e" || a == "--env" || a == "--env-file" || a == "-v" || a == "--volume" || a == "--mount" {
			t.Errorf("container sandbox run args pass %s %q: a sandbox gets no environment or mount from the daemon", a, args[i+1])
		}
		if strings.Contains(a, "local-api-token") || strings.Contains(a, "GRAIN_LOCAL_API_TOKEN_FILE") || strings.Contains(a, dataDir) {
			t.Errorf("container sandbox run args mention the access code's file: %q", a)
		}
	}
}

func TestSetImageReplacesTheContainerImageForTheNextCreate(t *testing.T) {
	c := NewContainerSandboxes(ContainerConfig{Image: "ghcr.io/bwsalmon/grain/k8s-sandbox:sha-1", Workspace: "/home/agent"})
	c.SetImage("")
	if got := c.CurrentImage(); got != "ghcr.io/bwsalmon/grain/k8s-sandbox:sha-1" {
		t.Fatalf("SetImage(\"\") left %q, want the configured image kept", got)
	}
	c.SetImage("registry.example/team/sandbox:v2")
	args := c.runArgs("grain-run", DefaultShape(), "")
	if got := args[len(args)-3]; got != "registry.example/team/sandbox:v2" {
		t.Fatalf("docker run image = %q, want the image SetImage named (%v)", got, args)
	}
}
