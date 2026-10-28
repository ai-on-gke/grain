package sysstat

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The values NestedVirtualization returns for this machine's own KVM.
//
// They are about the *host* half of nested virtualization -- whether a
// VM this machine boots is allowed to run VMs of its own -- and are
// deliberately a different vocabulary from the guest-side states
// orchestrator.SandboxHealth.NestedVirt carries: an operator reading the
// sandbox health pane is looking at two different questions, asked in
// two different places, and giving them one set of words would hide
// which of the two answered.
const (
	// NestedVirtEnabled: kvm_intel/kvm_amd is loaded with nesting on, so
	// the virtualization flag reaches a guest's CPUID and a VM booted
	// here can run VMs.
	NestedVirtEnabled = "enabled"
	// NestedVirtDisabled: the module is loaded with nesting explicitly
	// off (nested=N/0), so guests get no vmx/svm however they are
	// configured. Turning it back on means reloading the module -- see
	// scripts/setup.sh's ensure_kontur_nested_virt, which writes the
	// modprobe drop-in that survives a reboot.
	NestedVirtDisabled = "disabled"
	// NestedVirtUnavailable: neither module has a "nested" parameter to
	// read, which is what a machine with no KVM at all looks like (no
	// hardware virtualization, or a kernel without kvm_intel/kvm_amd),
	// and also what a non-Linux host looks like. Distinct from
	// NestedVirtDisabled because nothing was actually established: there
	// is no setting here that was turned off.
	NestedVirtUnavailable = "unavailable"
)

// nestedVirtModules are the two KVM modules that carry a "nested"
// parameter, in the order NestedVirtualization tries them. Exactly one
// is ever loaded on a given machine (they are the Intel and AMD halves
// of the same driver), so the order is not a precedence rule so much as
// a way of asking both questions with one answer.
var nestedVirtModules = []string{"kvm_intel", "kvm_amd"}

// NestedVirtualization reports whether the KVM this machine runs will
// expose hardware virtualization to the VMs it boots -- the host half of
// what a kontur sandbox needs to run VMs of its own (see
// orchestrator.SandboxHealth.NestedVirt for the guest half, which is
// what the sandbox health pane shows beside this).
//
// detail is the reading itself, in the sysfs vocabulary it was taken in
// ("kvm_intel nested=Y"), so the pane can say what was actually looked
// at rather than only what it concluded. Empty for
// NestedVirtUnavailable, which is the absence of a reading rather than
// one.
//
// Nesting is on by default in both modules on any kernel this runs on,
// so NestedVirtEnabled is the ordinary answer on a physical host and
// nothing has to be configured to get it. It is worth reporting anyway
// because the two cases where it is not -- a distro or an operator that
// set nested=0, and a deployment on a cloud VM whose own hypervisor
// never gave this kernel hardware virtualization to pass on -- are both
// invisible from inside a sandbox, where the symptom is a missing CPU
// flag with nothing to attribute it to.
func NestedVirtualization() (status, detail string) {
	return nestedVirtualizationIn("/sys/module")
}

// nestedVirtualizationIn is NestedVirtualization against a given sysfs
// module root, so a test can supply one rather than depend on whether
// the machine running it happens to have KVM loaded.
func nestedVirtualizationIn(moduleRoot string) (status, detail string) {
	for _, module := range nestedVirtModules {
		raw, err := os.ReadFile(filepath.Join(moduleRoot, module, "parameters", "nested"))
		if err != nil {
			continue
		}
		value := strings.TrimSpace(string(raw))
		detail := fmt.Sprintf("%s nested=%s", module, value)
		// Both spellings the kernel uses for the same parameter: bool
		// parameters read back as "Y"/"N", and the ones declared as int
		// or with a permission-changing setter read back as "1"/"0".
		switch value {
		case "Y", "y", "1":
			return NestedVirtEnabled, detail
		case "N", "n", "0":
			return NestedVirtDisabled, detail
		default:
			// A value neither spelling covers is a kernel this code does
			// not know rather than a setting that is off, and saying
			// "disabled" about it would send an operator to reload a
			// module that is already doing the right thing. The detail
			// still carries what was actually read.
			return NestedVirtUnavailable, detail
		}
	}
	return NestedVirtUnavailable, ""
}
