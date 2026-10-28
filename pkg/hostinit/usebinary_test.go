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

package hostinit

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// prebuiltGrain go-builds a stand-in "release" grain binary (any Go
// binary carries the build info checkPrebuiltGrain reads) and returns
// its path.
func prebuiltGrain(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "grain-release")
	buildStampedRepo(t, path, false)
	return path
}

// TestPreflightUseBinarySkipsMakeAndNPM: with -use-binary there is no
// `make build`, so preflight must neither run nor demand make or npm --
// the whole point is a host with a release binary and no frontend
// toolchain getting through.
func TestPreflightUseBinarySkipsMakeAndNPM(t *testing.T) {
	t.Parallel()
	bin := prebuiltGrain(t)
	sys := newFakeSystem()
	sys.root = true
	// Nothing configured for make or npm: both would fail if asked.

	var r Result
	opts := Options{Platform: PlatformLinuxContainer, UseBinary: bin}
	if ok := preflight(context.Background(), sys, &r, opts, "linux"); !ok {
		t.Fatalf("preflight = false, want true: %+v", r.Steps)
	}
	if len(sys.calls) != 0 {
		t.Errorf("preflight ran commands with -use-binary set: %v", sys.calls)
	}
	s, found := stepStatus(r, "prebuilt grain binary")
	if !found || s.Status != StepSkipped || !strings.Contains(s.Detail, bin) {
		t.Errorf("prebuilt grain binary: got %+v, want StepSkipped naming %s", s, bin)
	}
	for _, name := range []string{"make", "node & npm (frontend build)"} {
		if _, found := stepStatus(r, name); found {
			t.Errorf("preflight reported a %q step with -use-binary set: %+v", name, r.Steps)
		}
	}
}

// TestPreflightUseBinaryStillDemandsKVM: -use-binary replaces only the
// build toolchain checks; PlatformLinuxKVM's /dev/kvm check stays.
func TestPreflightUseBinaryStillDemandsKVM(t *testing.T) {
	t.Parallel()
	bin := prebuiltGrain(t)
	sys := newFakeSystem()
	sys.root = true

	var r Result
	opts := Options{Platform: PlatformLinuxKVM, UseBinary: bin}
	if ok := preflight(context.Background(), sys, &r, opts, "linux"); ok {
		t.Fatalf("preflight = true with no /dev/kvm, want false")
	}
	if s, _ := stepStatus(r, "/dev/kvm"); s.Status != StepManual {
		t.Errorf("/dev/kvm: got %+v, want StepManual", s)
	}
	if sys.calledAny("make --version") || sys.calledAny("npm --version") {
		t.Errorf("preflight checked make/npm with -use-binary set: %v", sys.calls)
	}
}

func TestPreflightUseBinaryRejectsMissingAndNonExecutable(t *testing.T) {
	t.Parallel()
	notExec := filepath.Join(t.TempDir(), "grain")
	if err := os.WriteFile(notExec, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(t.TempDir(), "missing"), notExec} {
		var r Result
		opts := Options{Platform: PlatformLinuxContainer, UseBinary: path}
		if ok := preflight(context.Background(), newFakeSystem(), &r, opts, "linux"); ok {
			t.Errorf("%s: preflight = true, want false", path)
		}
		if s, _ := stepStatus(r, "prebuilt grain binary"); s.Status != StepManual {
			t.Errorf("%s: prebuilt grain binary: got %+v, want StepManual", path, s)
		}
	}
}

func TestValidateUseBinary(t *testing.T) {
	t.Parallel()
	base := baseOptions(t)

	ok := base
	ok.Platform = PlatformLinuxContainer
	ok.UseBinary = "/opt/grain/grain"
	if err := ok.withDefaults().validate(); err != nil {
		t.Errorf("-use-binary alone: %v", err)
	}
	if got := ok.withDefaults().GrainBinaryPath; got != "/opt/grain/grain" {
		t.Errorf("GrainBinaryPath = %q, want the -use-binary path", got)
	}

	same := ok
	same.GrainBinaryPath = "/opt/grain/grain"
	if err := same.withDefaults().validate(); err != nil {
		t.Errorf("-use-binary and an identical -grain-binary: %v", err)
	}

	differ := ok
	differ.GrainBinaryPath = "/usr/local/bin/grain"
	if err := differ.withDefaults().validate(); err == nil {
		t.Error("-use-binary and a different -grain-binary: validate accepted two binaries")
	}

	k8s := ok
	k8s.Platform = PlatformK8sContainer
	k8s.K8s.SandboxImage = "example/sandbox:1"
	if err := k8s.withDefaults().validate(); err == nil {
		t.Error("-use-binary with -platform=k8s-container: validate accepted a flag that platform never reads")
	}
}

func TestEnsureGrainBinaryUseBinarySkipsAndNamesIt(t *testing.T) {
	t.Parallel()
	bin := prebuiltGrain(t)
	opts := baseOptions(t)
	opts.UseBinary = bin
	opts = opts.withDefaults()
	sys := newFakeSystem()

	var r Result
	if !ensureGrainBinary(context.Background(), sys, opts, &r, nil) {
		t.Fatal("ensureGrainBinary reported failure for a prebuilt binary")
	}
	s, _ := stepStatus(r, grainBinaryStepName)
	if s.Status != StepSkipped || !strings.Contains(s.Detail, bin) {
		t.Errorf("%s: got %+v, want StepSkipped naming %s", grainBinaryStepName, s, bin)
	}
	if len(sys.calls) != 0 {
		t.Errorf("ensureGrainBinary ran commands with -use-binary set: %v", sys.calls)
	}
}

// TestInitLinuxContainerUseBinaryNeedsNoBuildToolchain is the flow the
// install walkthrough could not get through: a linux-container host
// with a release grain binary and no make, node/npm or Go reaches a
// running daemon, never runs any of the three, and the unit runs the
// binary it was handed.
func TestInitLinuxContainerUseBinaryNeedsNoBuildToolchain(t *testing.T) {
	// Not t.Parallel(): redirects the shared systemdUnitPath package var.
	unitPath := withTempUnitPath(t)
	bin := prebuiltGrain(t)
	opts := baseOptions(t)
	opts.Platform = PlatformLinuxContainer
	opts.UseBinary = bin
	opts = opts.withDefaults()

	sys := newFakeSystem()
	sys.root = true
	sys.on("git version 2.40.0", nil, "git", "--version")
	sys.on("installed", nil, "dpkg", "-s", "docker-ce")
	sys.on("grain-sandboxes", nil, "docker", "network", "ls", "--filter", "name=^grain-sandboxes$", "--format", "{{.Name}}")
	sys.on(`[{"Subnet":"10.241.0.0/24","Gateway":"10.241.0.1"}]`, nil,
		"docker", "network", "inspect", "grain-sandboxes", "-f", "{{json .IPAM.Config}}")
	sys.on("", fmt.Errorf("no such user"), "id", "-u", DefaultDaemonUser)
	sys.on("", nil, "useradd", "--system", "--no-create-home", "--shell", "/usr/sbin/nologin", "--user-group", DefaultDaemonUser)
	sys.on("", fmt.Errorf("no such user"), "id", "-nG", DefaultDaemonUser)
	sys.on("", nil, "usermod", "-aG", "docker", DefaultDaemonUser)
	sys.on("", fmt.Errorf("no such file"), "stat", "-c", "%U:%G", opts.DataDir)
	sys.on("", nil, "chown", "-R", DefaultDaemonUser+":"+DefaultDaemonUser, opts.DataDir)
	sys.on("cap_net_bind_service=ep", nil, "getcap", bin)
	sys.on("claude "+DefaultClaudeCLIVersion, nil, "claude", "--version")
	sys.on("inactive", nil, "systemctl", "is-active", "grain-daemon.service")
	sys.on("", fmt.Errorf("no process found"), "pgrep", "-f", "grain daemon")
	sys.on("", nil, "systemctl", "daemon-reload")
	sys.on("", nil, "systemctl", "enable", "--now", "grain-daemon.service")
	sys.onHTTPRequest(http.MethodGet, "http://"+DefaultUIAddr+"/api/config", http.StatusOK, nil, nil)

	result, err := initFor(context.Background(), sys, opts, "linux")
	if err != nil {
		t.Fatalf("initFor: %v", err)
	}

	if s, _ := stepStatus(result, "go toolchain"); s.Status != StepSkipped || !strings.Contains(s.Detail, "-use-binary") {
		t.Errorf("go toolchain: got %+v, want StepSkipped naming -use-binary", s)
	}
	if s, _ := stepStatus(result, grainBinaryStepName); s.Status != StepSkipped || !strings.Contains(s.Detail, bin) {
		t.Errorf("%s: got %+v, want StepSkipped naming %s", grainBinaryStepName, s, bin)
	}
	if s, _ := stepStatus(result, "start the daemon"); s.Status != StepDone {
		t.Fatalf("start the daemon: got %+v, want StepDone", s)
	}
	for _, call := range sys.calls {
		for _, banned := range []string{"make ", "npm ", "go "} {
			if strings.HasPrefix(call, banned) {
				t.Errorf("Init ran %q with -use-binary on -platform=linux-container", call)
			}
		}
	}

	unit, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("reading the unit ensureDaemon should have written: %v", err)
	}
	if !strings.Contains(string(unit), "ExecStart="+bin+" ") {
		t.Errorf("unit does not run the -use-binary binary %s:\n%s", bin, unit)
	}
}
