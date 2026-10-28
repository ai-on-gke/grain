package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// grain hostd runs through the current link, comes back after a drain's
// clean exit, and a .path unit on that link drains it; a setup.sh re-run
// drains a hostd that holds runs rather than restarting it.
func TestHostdRunsTheCurrentLinkAndIsDrainedOntoANewOne(t *testing.T) {
	code := setupCode(t)
	units := body(t, code, "write_hostd_units() {")
	contains(t, units, "ExecStart=${HOSTD_VERSIONS_DIR}/current/grain hostd -socket ${HOSTD_SOCKET}")
	contains(t, units, "Restart=always")
	// The directory: a watch on the current link follows it to its old
	// target and never sees the rename that moves it.
	contains(t, units, "PathChanged=${HOSTD_VERSIONS_DIR}\n")
	if strings.Contains(units, "PathChanged=${HOSTD_VERSIONS_DIR}/current") {
		t.Error("grain-hostd-upgrade.path watches the current link, which inotify follows to its old target")
	}
	contains(t, units, "Unit=grain-hostd-upgrade.service")
	contains(t, units, "ExecStart=${HOSTD_UPGRADE_SCRIPT}")
	if strings.Contains(units, "Restart=on-failure") {
		t.Error("grain-hostd.service restarts only on failure, and a drained hostd exits 0")
	}

	enable := body(t, code, "enable_services() {")
	contains(t, enable, "systemctl enable --now grain-hostd-upgrade.path")
	contains(t, enable, `held="$(hostd_running_runs)"`)
	contains(t, enable, `hostd -drain -socket "$HOSTD_SOCKET"`)

	off := between(t, units, `if [ "${GRAIN_HOSTD:-0}" != "1" ]; then`, "return 0")
	contains(t, off, "systemctl disable --now grain-hostd-upgrade.path")

	readiness := body(t, code, "report_readiness() {")
	contains(t, readiness, `hostd_processes | sed`)
}

// The drain script drains a hostd running another binary than the
// current link's, as $GRAIN_USER, and leaves one already on it alone.
func TestHostdUpgradeScriptDrainsOnlyAHostdOnAnOldBinary(t *testing.T) {
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep")
	}
	dir := t.TempDir()
	versions := filepath.Join(dir, "hostd")
	stubs := filepath.Join(dir, "stubs")
	calls := filepath.Join(dir, "calls")
	for _, d := range []string{filepath.Join(versions, "old"), filepath.Join(versions, "new"), stubs} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(sleepBin)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"old", "new"} {
		if err := os.WriteFile(filepath.Join(versions, v, "grain"), data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("new", filepath.Join(versions, "current")); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "hostd-upgrade.sh")
	runBash(t, shellFunction(t, "write_hostd_upgrade_script")+"write_hostd_upgrade_script\n",
		"HOSTD_UPGRADE_SCRIPT="+script, "HOSTD_VERSIONS_DIR="+versions,
		"HOSTD_SOCKET=/run/grain-hostd/hostd.sock", "GRAIN_USER=grain")

	// hostd is "running" the binary in dir: a sleep started from it.
	upgrade := func(dir string) string {
		t.Helper()
		hostd := exec.Command(filepath.Join(versions, dir, "grain"), "30")
		if err := hostd.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { hostd.Process.Kill(); hostd.Wait() }()
		os.Remove(calls)
		for name, body := range map[string]string{
			"systemctl": "echo " + strconv.Itoa(hostd.Process.Pid) + "\n",
			"runuser":   `echo "runuser $*" >> ` + calls + "\n",
		} {
			if err := os.WriteFile(filepath.Join(stubs, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		runBash(t, script, "PATH="+stubs+":"+os.Getenv("PATH"))
		got, _ := os.ReadFile(calls)
		return string(got)
	}
	if got := upgrade("new"); got != "" {
		t.Errorf("a hostd already on the current binary was drained: %s", got)
	}
	want := "runuser -u grain -- " + filepath.Join(versions, "new", "grain") + " hostd -drain -socket /run/grain-hostd/hostd.sock\n"
	if got := upgrade("old"); got != want {
		t.Errorf("a hostd on the old binary: %q, want %q", got, want)
	}

	// The extractor holds the versions lock from making a new version's
	// directory, which starts this script, until it has moved current: a
	// run started meanwhile reads the moved link, not the one before it.
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("no flock")
	}
	current := filepath.Join(versions, "current")
	if err := os.Remove(current); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("old", current); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(versions+".lock", os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	lockFD := int(lock.Fd())
	if err := syscall.Flock(lockFD, syscall.LOCK_EX); err != nil {
		lock.Close()
		t.Fatal(err)
	}
	unlocked := make(chan struct{})
	defer func() {
		<-unlocked
		lock.Close()
	}()
	go func() {
		defer close(unlocked)
		time.Sleep(500 * time.Millisecond)
		os.Remove(current)
		os.Symlink("new", current)
		syscall.Flock(lockFD, syscall.LOCK_UN)
	}()
	if got := upgrade("old"); got != want {
		t.Errorf("a run started while the extractor held the lock: %q, want %q", got, want)
	}
}

// The extractor takes that lock before the new version's directory and
// lets it go only once current has moved.
func TestTheExtractorMovesCurrentUnderTheVersionsLock(t *testing.T) {
	install := body(t, setupCode(t), "install_hostd_version() {")
	locked := strings.Index(install, "flock 9")
	mkdir := strings.Index(install, `install -d -m0755 "\$dir/\$version"`)
	moved := strings.Index(install, `mv -fT "\$dir/current.new" "\$dir/current"`)
	unlocked := strings.Index(install, "exec 9>&-")
	if locked < 0 || mkdir < 0 || moved < 0 || unlocked < 0 || !(locked < mkdir && mkdir < moved && moved < unlocked) {
		t.Errorf("install_hostd_version does not hold the versions lock from the new directory to current's move:\n%s", install)
	}
}
