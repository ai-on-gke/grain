package hypervisor

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoveStaleSocket_RemovesOneNothingIsListeningOn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api.sock")

	// A unix socket whose process is gone: bound, then closed with the
	// file left behind, which is exactly what an earlier boot in this
	// container leaves for the next one.
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if ul, ok := l.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	if err := RemoveStaleSocket(path, "the test's socket"); err != nil {
		t.Fatalf("RemoveStaleSocket() error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("stale socket still at %s (stat err = %v)", path, err)
	}
}

func TestRemoveStaleSocket_RefusesOneStillBeingListenedOn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	err = RemoveStaleSocket(path, "cloud-hypervisor's API socket")
	if err == nil {
		t.Fatal("RemoveStaleSocket() accepted a socket a running VM is still listening on")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("error does not say a VM is already running: %v", err)
	}
	// The point of the refusal: the running VM keeps the name it is
	// reachable by. Unlinking it is what used to leave a live VM with no
	// working "granule exec"/"granule resize" for the rest of its life.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the live socket was removed anyway: %v", err)
	}
}

func TestRemoveStaleSocket_NoSocketIsFine(t *testing.T) {
	if err := RemoveStaleSocket(filepath.Join(t.TempDir(), "absent.sock"), "a socket"); err != nil {
		t.Errorf("RemoveStaleSocket() on a path with nothing there error = %v", err)
	}
	if err := RemoveStaleSocket("", "a socket"); err != nil {
		t.Errorf("RemoveStaleSocket(\"\") error = %v", err)
	}
}
