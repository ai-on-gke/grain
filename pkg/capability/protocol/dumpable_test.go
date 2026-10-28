package protocol

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// dropCapsHelperEnv makes this test binary a process that restores
// dumpable, drops its capabilities when the variable says "drop", reports
// "ready" and waits for stdin to close.
const dropCapsHelperEnv = "GRAIN_PROTOCOL_TEST_DROPCAPS"

func dropCapsHelper(mode string) {
	MakeDumpable()
	if mode == "drop" {
		if err := DropCapabilities(); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
	}
	fmt.Println("ready")
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

// A process exec'd from a file-capability binary -- `grain capability`
// from the image's setcap'd /usr/local/bin/grain -- holds a capability its
// same-uid inspector does not, and ptrace's access check closes its
// /proc/<pid>/fd/<n> to that inspector even once it is dumpable. Dropping
// its capabilities opens them again, which is what lets the port relay and
// the installer's host-script probe see who holds a grant's port.
func TestDroppingCapabilitiesOpensASetcapProcessesFDs(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("file capabilities are Linux's")
	}
	if os.Geteuid() == 0 {
		t.Skip("root may inspect any process")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Not t.TempDir: /tmp is often nosuid, which ignores file capabilities.
	dir, err := os.MkdirTemp("/var/tmp", "grain-dropcaps-")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	bin := filepath.Join(dir, "helper")
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, data, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sudo", "-n", "setcap", "cap_net_bind_service+ep", bin).CombinedOutput(); err != nil {
		t.Skipf("cannot setcap a copy of the test binary: %v: %s", err, out)
	}

	readable := func(mode string) bool {
		cmd := exec.Command(bin, "-test.run=^$")
		cmd.Env = append(os.Environ(), dropCapsHelperEnv+"="+mode)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = stdin.Close(); _ = cmd.Wait() }()
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err != nil || line != "ready\n" {
			t.Fatalf("helper (%s) said %q: %v", mode, line, err)
		}
		_, err = os.Readlink(fmt.Sprintf("/proc/%d/fd/0", cmd.Process.Pid))
		return err == nil
	}
	if readable("keep") {
		t.Skip("this kernel lets a same-uid process inspect a setcap'd one; nothing to check")
	}
	if !readable("drop") {
		t.Error("after DropCapabilities, the setcap'd process's fds are still closed to a same-uid process")
	}
}
