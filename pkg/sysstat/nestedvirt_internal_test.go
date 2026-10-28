package sysstat

// The sysfs files NestedVirtualization reads are written by whichever
// KVM module the machine running these tests happens to have loaded --
// possibly neither -- so the reading itself is exercised against a
// directory laid out the way /sys/module is, and the real one only has
// to answer *something* known (nestedvirt_test.go).

import (
	"os"
	"path/filepath"
	"testing"
)

// writeModuleParam lays out "<root>/<module>/parameters/nested" holding
// value, the shape the kernel gives a loaded module's own parameters.
func writeModuleParam(t *testing.T, root, module, value string) {
	t.Helper()
	dir := filepath.Join(root, module, "parameters")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested"), []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNestedVirtualizationReadsTheLoadedModulesParameter(t *testing.T) {
	for _, tc := range []struct {
		name       string
		module     string
		value      string
		wantStatus string
		wantDetail string
	}{
		// "Y\n" and "N\n" are how a bool module parameter reads back,
		// trailing newline included -- the ordinary case for both
		// modules.
		{"intel nesting on", "kvm_intel", "Y\n", NestedVirtEnabled, "kvm_intel nested=Y"},
		{"intel nesting off", "kvm_intel", "N\n", NestedVirtDisabled, "kvm_intel nested=N"},
		{"amd nesting on", "kvm_amd", "1\n", NestedVirtEnabled, "kvm_amd nested=1"},
		{"amd nesting off", "kvm_amd", "0\n", NestedVirtDisabled, "kvm_amd nested=0"},
		// Neither spelling: reported as no reading rather than as a
		// setting that is off, since nothing here was established.
		{"unknown spelling", "kvm_intel", "maybe\n", NestedVirtUnavailable, "kvm_intel nested=maybe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeModuleParam(t, root, tc.module, tc.value)

			status, detail := nestedVirtualizationIn(root)
			if status != tc.wantStatus {
				t.Errorf("status = %q, want %q", status, tc.wantStatus)
			}
			if detail != tc.wantDetail {
				t.Errorf("detail = %q, want %q", detail, tc.wantDetail)
			}
		})
	}
}

// A machine with no KVM loaded at all -- the case a deployment on a cloud
// VM without nested virtualization enabled is in, and the one this must
// not report as "disabled": nothing was turned off, there is simply
// nothing to read.
func TestNestedVirtualizationReportsUnavailableWithNoKVMModule(t *testing.T) {
	status, detail := nestedVirtualizationIn(t.TempDir())
	if status != NestedVirtUnavailable {
		t.Errorf("status = %q, want %q", status, NestedVirtUnavailable)
	}
	if detail != "" {
		t.Errorf("detail = %q, want empty: there was no reading to describe", detail)
	}
}

// kvm_amd is consulted when kvm_intel is not there: the two are the same
// driver's two halves, and a host is one or the other.
func TestNestedVirtualizationFallsThroughToTheModuleThatIsLoaded(t *testing.T) {
	root := t.TempDir()
	writeModuleParam(t, root, "kvm_amd", "Y\n")

	if status, _ := nestedVirtualizationIn(root); status != NestedVirtEnabled {
		t.Errorf("status = %q, want %q from kvm_amd alone", status, NestedVirtEnabled)
	}
}
