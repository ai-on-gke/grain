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

package sandboximage

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestContractBoundsAreConsistent(t *testing.T) {
	if MinimumContract < 1 {
		t.Fatalf("MinimumContract = %d, want >= 1", MinimumContract)
	}
	if Contract < MinimumContract {
		t.Fatalf("Contract = %d < MinimumContract = %d", Contract, MinimumContract)
	}
}

func TestReadContractClassifiesEveryDeclaration(t *testing.T) {
	if v, state := ReadContract(""); v != 0 || state != ContractUndeclared {
		t.Errorf("ReadContract(\"\") = (%d, %v), want (0, ContractUndeclared)", v, state)
	}
	if v, state := ReadContract("  \n\t "); v != 0 || state != ContractUndeclared {
		t.Errorf("ReadContract(whitespace) = (%d, %v), want (0, ContractUndeclared)", v, state)
	}
	for _, bad := range []string{"0", "-1", "v1", "1.0", "abc"} {
		if v, state := ReadContract(bad); v != 0 || state != ContractUnreadable {
			t.Errorf("ReadContract(%q) = (%d, %v), want (0, ContractUnreadable)", bad, v, state)
		}
	}
	if v, state := ReadContract(ContractValue()); v != Contract || state != ContractSupported {
		t.Errorf("ReadContract(%q) = (%d, %v), want (%d, ContractSupported)", ContractValue(), v, state, Contract)
	}
	if v, state := ReadContract(" " + ContractValue() + "\n"); v != Contract || state != ContractSupported {
		t.Errorf("ReadContract(padded) = (%d, %v), want (%d, ContractSupported)", v, state, Contract)
	}
	if v, state := ReadContract("9999"); v != 9999 || state != ContractAhead {
		t.Errorf("ReadContract(\"9999\") = (%d, %v), want (9999, ContractAhead)", v, state)
	}
}

func TestDescribeContract(t *testing.T) {
	desc, state := DescribeContract(ContractValue())
	if state != ContractSupported || !strings.Contains(desc, "v"+ContractValue()) || !strings.Contains(desc, "current") {
		t.Errorf("DescribeContract(%q) = (%q, %v)", ContractValue(), desc, state)
	}
	desc, state = DescribeContract("")
	if state != ContractUndeclared || !strings.Contains(desc, ContractLabel) {
		t.Errorf("DescribeContract(\"\") = (%q, %v)", desc, state)
	}
	desc, state = DescribeContract("9999")
	if state != ContractAhead || !strings.Contains(desc, "newer than") {
		t.Errorf("DescribeContract(\"9999\") = (%q, %v)", desc, state)
	}
}

func TestCheckContract(t *testing.T) {
	// Supported container contract (no granule contract).
	c := CheckContract(Contract, 0)
	if !c.OK || c.NeedsBaseRebuild || c.State != ContractSupported {
		t.Errorf("CheckContract(%d, 0) = %+v, want OK and supported", Contract, c)
	}

	// Supported Granule VM contract (both contracts current).
	c = CheckContract(Contract, GranuleContract)
	if !c.OK || c.NeedsBaseRebuild || c.State != ContractSupported || c.GranuleState != ContractSupported {
		t.Errorf("CheckContract(%d, %d) = %+v, want OK and both supported", Contract, GranuleContract, c)
	}

	// Missing sandbox contract requires a base image rebuild.
	c = CheckContract(0, GranuleContract)
	if c.OK || !c.NeedsBaseRebuild || c.State != ContractUndeclared {
		t.Errorf("CheckContract(0, %d) = %+v, want !OK, NeedsBaseRebuild, ContractUndeclared", GranuleContract, c)
	}

	// Newer sandbox contract than daemon knows does NOT ask for a base rebuild;
	// it asks to upgrade grain.
	c = CheckContract(Contract+10, 0)
	if c.OK || c.NeedsBaseRebuild || c.State != ContractAhead {
		t.Errorf("CheckContract(%d, 0) = %+v, want !OK, !NeedsBaseRebuild, ContractAhead", Contract+10, c)
	}

	// Granule backend requires GranuleContract too.
	c = CheckContractForBackend(Contract, 0, BackendGranule)
	if c.OK || !c.NeedsBaseRebuild || c.GranuleState != ContractUndeclared {
		t.Errorf("CheckContractForBackend(%d, 0, granule) = %+v, want !OK and GranuleState=ContractUndeclared", Contract, c)
	}
}

func TestCheckImageLabels(t *testing.T) {
	k8sLabels := map[string]string{
		ContractLabel: ContractValue(),
	}
	if check := CheckImageLabels(k8sLabels, BackendK8s); !check.OK {
		t.Errorf("CheckImageLabels(k8s) = %+v, want OK", check)
	}
	if check := CheckImageLabels(k8sLabels, BackendGranule); check.OK || check.GranuleState != ContractUndeclared {
		t.Errorf("CheckImageLabels(granule missing granule label) = %+v, want !OK", check)
	}

	granuleLabels := map[string]string{
		ContractLabel:        ContractValue(),
		GranuleContractLabel: "1",
	}
	if check := CheckImageLabels(granuleLabels, BackendGranule); !check.OK {
		t.Errorf("CheckImageLabels(granule) = %+v, want OK", check)
	}
}

func TestVerifyScriptAndRequiredComponents(t *testing.T) {
	k8sItems := strings.Join(RequiredComponents(BackendK8s), "\n")
	for _, want := range []string{ContractFile, "debian", "/home/agent", "git", "curl", "ca-certificates", "python3", SandboxCLIPath, "host-script"} {
		if !strings.Contains(k8sItems, want) {
			t.Errorf("RequiredComponents(k8s) missing %q:\n%s", want, k8sItems)
		}
	}
	if strings.Contains(k8sItems, "grain-connect") {
		t.Errorf("RequiredComponents(k8s) unexpectedly requires grain-connect:\n%s", k8sItems)
	}

	granuleItems := strings.Join(RequiredComponents(BackendGranule), "\n")
	for _, want := range []string{ContractFile, GranuleContractFile, "sudo", "systemd", "/usr/local/bin/grain-connect"} {
		if !strings.Contains(granuleItems, want) {
			t.Errorf("RequiredComponents(granule) missing %q:\n%s", want, granuleItems)
		}
	}

	for _, backend := range []string{BackendK8s, BackendGranule} {
		script := VerifyScript(backend)
		for _, want := range []string{
			ContractFile,
			"id -u debian",
			"/home/debian",
			"/home/agent",
			"for cmd in git curl python3",
			"/etc/ssl/certs/ca-certificates.crt",
			"import os, socket, sys, threading",
			SandboxCLIPath,
			"GRAIN_SANDBOX_CLI=0",
		} {
			if !strings.Contains(script, want) {
				t.Errorf("VerifyScript(%s) missing %q:\n%s", backend, want, script)
			}
		}
		cmd := exec.Command("sh", "-n", "-c", script)
		cmd.Env = os.Environ()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("VerifyScript(%s) is not valid POSIX sh: %v\n%s", backend, err, out)
		}
	}
}

func TestContractIsDeclaredEverywhere(t *testing.T) {
	wantLabel := ContractLabel + `="` + ContractValue() + `"`
	wantFile := `printf '` + ContractValue() + `\n' > ` + ContractFile

	for _, rel := range []string{
		"Granulefile.guest-base",
		"Granulefile",
		"Dockerfile.k8s-sandbox",
	} {
		raw, err := os.ReadFile(filepath.Join("..", "..", rel))
		if err != nil {
			t.Fatalf("reading %s: %v", rel, err)
		}
		text := string(raw)
		if !strings.Contains(text, wantLabel) {
			t.Errorf("%s does not stamp %s (expected when Contract = %d)", rel, wantLabel, Contract)
		}
		if !strings.Contains(text, wantFile) {
			t.Errorf("%s does not write %s (expected when Contract = %d)", rel, wantFile, Contract)
		}
	}
}

// The contract's grain check passes a grain whose usage offers the
// sandbox CLI and fails the host-script-only grain older images carry, or
// none at all -- run against stand-ins for /usr/local/bin/grain.
func TestVerifyScriptChecksTheSandboxCLI(t *testing.T) {
	dir := t.TempDir()
	fake := func(name, usage string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\ncat <<'USAGE'\n"+usage+"USAGE\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	current := fake("current", "usage: grain host-script <name>\n       grain ls <uri>\n       grain read <uri>\n"+
		"       grain grep <re> <uri>\n       grain ci wait\n       grain pr status\n       grain task wait\n       grain api\n")
	old := fake("old", "usage: grain host-script <name> [arguments...]   run a granted host script\n")
	run := func(bin string) (string, error) {
		script := "fail() { echo \"$*\" >&2; exit 1; }\n" + strings.ReplaceAll(sandboxCLIVerify(), SandboxCLIPath, bin)
		out, err := exec.Command("sh", "-c", script).CombinedOutput()
		return string(out), err
	}
	if out, err := run(current); err != nil {
		t.Errorf("check of a grain with the sandbox CLI failed: %v\n%s", err, out)
	}
	if out, err := run(old); err == nil || !strings.Contains(out, "grain ls") {
		t.Errorf("check of a host-script-only grain = %v, %q; want it to fail naming grain ls", err, out)
	}
	if out, err := run(filepath.Join(dir, "missing")); err == nil || !strings.Contains(out, "missing executable") {
		t.Errorf("check with no grain = %v, %q; want it to fail as missing", err, out)
	}
}
