package hypervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckDeviceOpenable(t *testing.T) {
	dir := t.TempDir()
	kvm := filepath.Join(dir, "kvm")
	if err := os.WriteFile(kvm, nil, 0o660); err != nil {
		t.Fatal(err)
	}
	if err := checkDevice(kvm, filepath.Join(dir, "mshv")); err != nil {
		t.Fatalf("checkDevice on a device this process can open: %v", err)
	}
}

func TestCheckDeviceMissing(t *testing.T) {
	dir := t.TempDir()
	err := checkDevice(filepath.Join(dir, "kvm"), filepath.Join(dir, "mshv"))
	if err == nil {
		t.Fatal("checkDevice with no device at all: got nil, want an error")
	}
	if !strings.Contains(err.Error(), "--device") {
		t.Errorf("error does not say how to pass the device in: %v", err)
	}
}

// An MSHV host has no /dev/kvm, and cloud-hypervisor uses /dev/mshv
// there instead; that is not a missing device.
func TestCheckDeviceMSHV(t *testing.T) {
	dir := t.TempDir()
	mshv := filepath.Join(dir, "mshv")
	if err := os.WriteFile(mshv, nil, 0o660); err != nil {
		t.Fatal(err)
	}
	if err := checkDevice(filepath.Join(dir, "kvm"), mshv); err != nil {
		t.Fatalf("checkDevice with only an MSHV device: %v", err)
	}
}

// The refusal names the remedy that matters in a user namespace: the
// device's group as a supplementary group, rather than a chmod.
func TestCheckDevicePermissionDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens a mode-0 file regardless, so there is no refusal to report")
	}
	dir := t.TempDir()
	kvm := filepath.Join(dir, "kvm")
	if err := os.WriteFile(kvm, nil, 0); err != nil {
		t.Fatal(err)
	}
	err := checkDevice(kvm, filepath.Join(dir, "mshv"))
	if err == nil {
		t.Fatal("checkDevice on a mode-0 file: got nil, want permission denied")
	}
	for _, want := range []string{"permission denied", "supplementary groups", "keep_original_groups", "mode 0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}
