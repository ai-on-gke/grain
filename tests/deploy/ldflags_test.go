package deploy

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The release build tells grain which images go with it by `-X` stamps
// in the Makefile's LDFLAGS, and the Go linker says nothing at all about
// an `-X` naming a symbol that does not exist. So when the daemon's wiring
// moved from package main into pkg/daemon (#3001), `-X
// main.defaultSandboxImage` and `-X main.defaultGrainImage` went on
// building fine and stamping nothing, and every release image fell back
// to the mutable `:latest` tags instead of its own commit's.
//
// The string checks in TestTheGrainImageIsStampedWithItsOwnReference and
// its neighbours cannot catch that: they hold the Makefile to a spelling,
// not to a symbol. This builds ./cmd/grain with the LDFLAGS the Makefile
// itself computes and asks the binary what it was stamped with, so the
// next move of one of these variables fails here rather than in a
// deployment running the wrong sandbox.
func TestMakefileLDFLAGSStampTheImagesTheBinaryReports(t *testing.T) {
	if testing.Short() {
		t.Skip("builds cmd/grain")
	}
	for _, tool := range []string{"make", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH: %v", tool, err)
		}
	}
	root := repoRoot(t)

	stamps := map[string]string{
		"SANDBOX_IMAGE":     "registry.example/stamp/guest:sha-test",
		"K8S_SANDBOX_IMAGE": "registry.example/stamp/k8s-sandbox:sha-test",
		"GRAIN_IMAGE_REF":   "registry.example/stamp/grain:sha-test",
	}
	// Ask make for LDFLAGS rather than copying its -X strings here: the
	// point is to test the Makefile, not a second copy of it.
	args := []string{"-s", "--no-print-directory", "-C", root,
		"--eval", "print-test-ldflags: ; @echo '$(LDFLAGS)'", "print-test-ldflags"}
	for k, v := range stamps {
		args = append(args, k+"="+v)
	}
	out, err := exec.Command("make", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("make print-test-ldflags: %v\n%s", err, out)
	}
	ldflags := strings.TrimSpace(string(out))
	for _, v := range stamps {
		if !strings.Contains(ldflags, v) {
			t.Fatalf("the Makefile's LDFLAGS do not carry %q: %s", v, ldflags)
		}
	}

	binary := filepath.Join(t.TempDir(), "grain")
	build := exec.Command("go", "build", "-o", binary, "-ldflags", ldflags, "./cmd/grain")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build -ldflags %q ./cmd/grain: %v\n%s", ldflags, err, out)
	}

	for cmd, want := range map[string]string{
		"sandbox-image":     stamps["SANDBOX_IMAGE"],
		"k8s-sandbox-image": stamps["K8S_SANDBOX_IMAGE"],
		"image":             stamps["GRAIN_IMAGE_REF"],
	} {
		out, err := exec.Command(binary, cmd).CombinedOutput()
		if err != nil {
			t.Errorf("grain %s: %v\n%s", cmd, err, out)
			continue
		}
		if got := strings.TrimSpace(string(out)); got != want {
			t.Errorf("grain %s = %q, want the stamped %q: the Makefile's -X names a symbol that no longer exists", cmd, got, want)
		}
	}
}
