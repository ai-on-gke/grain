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
	for _, want := range []string{ContractFile, "debian", "/home/agent", "git", "curl", "ca-certificates", "python3"} {
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
