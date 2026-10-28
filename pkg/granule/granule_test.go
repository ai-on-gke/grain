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

package granule

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// Exists is how ensure() decides a slot's VM is already built, so it has
// to agree with what `granule vm create`/`vm delete` actually leave
// behind: one "<state-dir>/<name>.json" per VM, removed on delete.
func TestExistsFollowsTheStateFile(t *testing.T) {
	dir := t.TempDir()
	if Exists(dir, "sandbox-0") {
		t.Error("Exists() on an empty state directory = true, want false")
	}
	if err := os.WriteFile(filepath.Join(dir, "sandbox-0.json"), []byte(`{"port": 30080}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !Exists(dir, "sandbox-0") {
		t.Error("Exists() with the VM's state file present = false, want true")
	}
	if Exists(dir, "sandbox-1") {
		t.Error("Exists() for a different VM name = true, want false")
	}
}

func TestPodName(t *testing.T) {
	if got, want := PodName("sandbox-0"), "granule-vm-sandbox-0"; got != want {
		t.Errorf("PodName() = %q, want %q", got, want)
	}
}

func TestList(t *testing.T) {
	// Non-existent directory returns nil, nil
	missingDir := filepath.Join(t.TempDir(), "nonexistent")
	names, err := List(missingDir)
	if err != nil || names != nil {
		t.Fatalf("List on missing dir: got (%v, %v), want (nil, nil)", names, err)
	}

	dir := t.TempDir()
	// Empty dir
	names, err = List(dir)
	if err != nil || len(names) != 0 {
		t.Fatalf("List on empty dir: got (%v, %v), want (nil/empty, nil)", names, err)
	}

	// Populate with various files and directories
	if err := os.WriteFile(filepath.Join(dir, "vm-1.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vm-2.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ignore.txt"), []byte(`text`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir.json"), 0o755); err != nil {
		t.Fatal(err)
	}

	names, err = List(dir)
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	slices.Sort(names)
	want := []string{"vm-1", "vm-2"}
	if !slices.Equal(names, want) {
		t.Errorf("List() = %v, want %v", names, want)
	}
}

// writeFakeGranule installs a shell script named "granule" on PATH
// -- the operator-facing binary
// vm's Create/Delete actually exec -- which appends every invocation's argv
// to argvLog (one line per call, space-joined) and exits with exitCode.
func writeFakeGranule(t *testing.T, argvLog string, exitCode int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake granule script is POSIX shell only")
	}
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> %s
exit %d
`, argvLog, exitCode)
	path := filepath.Join(dir, "granule")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func writeFakeDocker(t *testing.T, status string, exitCode int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake docker script is POSIX shell only")
	}
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
if [ %d -ne 0 ]; then
  echo "docker inspect failed" >&2
  exit %d
fi
echo "%s"
exit 0
`, exitCode, exitCode, status)
	path := filepath.Join(dir, "docker")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestDockerContainerStatus(t *testing.T) {
	writeFakeDocker(t, "running", 0)
	status, err := DockerContainerStatus(context.Background(), "sandbox-0")
	if err != nil {
		t.Fatalf("DockerContainerStatus error: %v", err)
	}
	if status != "running" {
		t.Errorf("DockerContainerStatus = %q, want %q", status, "running")
	}
}

func TestDockerContainerStatusFailure(t *testing.T) {
	writeFakeDocker(t, "", 1)
	_, err := DockerContainerStatus(context.Background(), "sandbox-0")
	if err == nil {
		t.Fatal("DockerContainerStatus with failing docker inspect: got nil error, want one")
	}
}

func TestCreateRunsGranuleVMCreate(t *testing.T) {
	log := filepath.Join(t.TempDir(), "argv.log")
	writeFakeGranule(t, log, 0)

	if err := Create(context.Background(), "/var/lib/granule/vms", "sandbox-0", "-image", "grain-sandbox.qcow2"); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	want := "vm create sandbox-0 -state-dir /var/lib/granule/vms -image grain-sandbox.qcow2\n"
	if string(got) != want {
		t.Errorf("granule invoked with %q, want %q", got, want)
	}
}

func TestCreateReturnsErrorOnFailure(t *testing.T) {
	writeFakeGranule(t, filepath.Join(t.TempDir(), "argv.log"), 1)

	if err := Create(context.Background(), "/var/lib/granule/vms", "sandbox-0"); err == nil {
		t.Error("Create() with a failing granule binary: got nil error, want one")
	}
}

func TestDeleteRunsGranuleVMDelete(t *testing.T) {
	log := filepath.Join(t.TempDir(), "argv.log")
	writeFakeGranule(t, log, 0)

	if err := Delete(context.Background(), "/var/lib/granule/vms", "sandbox-0"); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	want := "vm delete sandbox-0 -state-dir /var/lib/granule/vms\n"
	if string(got) != want {
		t.Errorf("granule invoked with %q, want %q", got, want)
	}
}

// stubGranule writes a fake granule that prints what it is told to and
// exits with the status it is told to, and returns the Command that runs
// it. A single-element Command whose name is not "granule" is not local,
// which is what puts Exists and List on their remote path.
func stubGranule(t *testing.T, stdout string, exit int) Command {
	t.Helper()
	// Not t.TempDir(): a path with a space in it would be a fine
	// directory and a broken argv, and the test names these are built
	// from do contain them.
	dir, err := os.MkdirTemp("", "granule-stub-*")
	if err != nil {
		t.Fatalf("making a directory for the stub: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "granule-stub")
	script := fmt.Sprintf("#!/bin/sh\ncat <<'EOF'\n%s\nEOF\nexit %d\n", stdout, exit)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing the stub: %v", err)
	}
	return Command{path}
}

// Which granule a Command names decides whether its state directory is one
// this process can read at all -- see Command.Exists.
func TestACommandKnowsWhetherItsGranuleIsOnThisMachine(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    Command
		want bool
	}{
		{"the zero value is Local", nil, true},
		{"Local itself", Local, true},
		{"granule on PATH", Command{"granule"}, true},
		{"a wrapper into a VM", Command{"limactl", "shell", "vm", "--", "granule"}, false},
		{"a granule somewhere else", Command{"/opt/granule/bin/granule"}, false},
	} {
		if got := tc.c.IsLocal(); got != tc.want {
			t.Errorf("%s: IsLocal() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A local Command still answers off the state directory, which is the
// behaviour every deployment that runs granule beside grain has.
func TestALocalCommandReadsTheStateDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "g-t1-1.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, err := Local.Exists(context.Background(), dir, "g-t1-1")
	if err != nil || !ok {
		t.Errorf("Exists for a VM with state = %v, %v; want true, nil", ok, err)
	}
	ok, err = Local.Exists(context.Background(), dir, "g-nope")
	if err != nil || ok {
		t.Errorf("Exists for a VM with none = %v, %v; want false, nil", ok, err)
	}
	names, err := Local.List(context.Background(), dir)
	if err != nil || !slices.Equal(names, []string{"g-t1-1"}) {
		t.Errorf("List = %v, %v; want [g-t1-1], nil", names, err)
	}
}

// A granule that is not on this machine is asked, and its answer is read
// out of `granule vm list`'s own table.
func TestARemoteCommandAsksGranuleInsteadOfReadingADirectory(t *testing.T) {
	// A directory that does not exist, to prove the answer did not come
	// from the filesystem: locally this would be "no VMs", and the stub
	// says otherwise.
	const absent = "/nonexistent/granule/state"

	table := "NAME      CPUS  MEMORY_MB  DISK_MB  IMAGE\n" +
		"g-t1-1    2     8192       30720    grain-guest:x\n" +
		"g-t1-2    1     512        2048     grain-guest:x"
	c := stubGranule(t, table, 0)

	names, err := c.List(context.Background(), absent)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !slices.Equal(names, []string{"g-t1-1", "g-t1-2"}) {
		t.Errorf("List = %v, want [g-t1-1 g-t1-2] -- the NAME header is not a VM", names)
	}
	ok, err := c.Exists(context.Background(), absent, "g-t1-2")
	if err != nil || !ok {
		t.Errorf("Exists(g-t1-2) = %v, %v; want true, nil", ok, err)
	}
	ok, err = c.Exists(context.Background(), absent, "g-t9-9")
	if err != nil || ok {
		t.Errorf("Exists(g-t9-9) = %v, %v; want false, nil", ok, err)
	}
}

// An empty state directory reads as no VMs, note and all, rather than as a
// VM called "no" or "(a".
func TestARemoteCommandReadsAnEmptyListingAsNoVMs(t *testing.T) {
	c := stubGranule(t, "no VMs in /var/lib/granule/vms\n(a VM created as root has its state elsewhere)", 0)
	names, err := c.List(context.Background(), "/var/lib/granule/vms")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("List = %v, want none", names)
	}
}

// A question that could not be asked is an error, not an answer of "no".
//
// The two have opposite consequences: Release skips its delete when Exists
// is false, so a lost answer folded into false leaks the VM and the
// address it is holding.
func TestARemoteCommandThatFailsIsAnErrorRatherThanNo(t *testing.T) {
	c := stubGranule(t, "granule: could not reach the VM", 1)
	if _, err := c.List(context.Background(), "/var/lib/granule/vms"); err == nil {
		t.Error("List returned no error for a granule that failed")
	}
	ok, err := c.Exists(context.Background(), "/var/lib/granule/vms", "g-t1-1")
	if err == nil {
		t.Error("Exists returned no error for a granule that failed")
	}
	if ok {
		t.Error("Exists returned true alongside its error")
	}
}

// writeFakeDockerOutput puts a "docker" on PATH that logs its argv to argvLog,
// prints stdout and stderr, and exits with code.
func writeFakeDockerOutput(t *testing.T, argvLog, stdout, stderr string, code int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake docker script is POSIX shell only")
	}
	dir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\necho \"$*\" >> %q\nprintf %%b %q\nprintf %%b %q >&2\nexit %d\n",
		argvLog, stdout, stderr, code)
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// docker rm exits non-zero when any name it was given is missing, and the
// "-netns" holder is missing for every VM a current granule built. That
// is success: what was asked for is that neither container exists.
func TestRemoveContainersTreatsNoSuchContainerAsRemoved(t *testing.T) {
	argvLog := filepath.Join(t.TempDir(), "argv")
	writeFakeDockerOutput(t, argvLog, "granule-vm-g-1\n",
		"Error response from daemon: No such container: granule-vm-g-1-netns\n", 1)
	if err := RemoveContainers(context.Background(), "g-1"); err != nil {
		t.Fatalf("RemoveContainers: %v", err)
	}
	data, _ := os.ReadFile(argvLog)
	if got, want := string(data), "rm -f granule-vm-g-1 granule-vm-g-1-netns\n"; got != want {
		t.Errorf("docker argv = %q, want %q", got, want)
	}
}

func TestRemoveContainersReportsAnyOtherFailure(t *testing.T) {
	writeFakeDockerOutput(t, filepath.Join(t.TempDir(), "argv"), "",
		"Cannot connect to the Docker daemon at unix:///var/run/docker.sock\n", 1)
	if err := RemoveContainers(context.Background(), "g-1"); err == nil {
		t.Fatal("RemoveContainers succeeded against a docker that could not be reached")
	}
}

func TestListLabeledVMsFiltersOnBothLabelsAndDeduplicates(t *testing.T) {
	argvLog := filepath.Join(t.TempDir(), "argv")
	writeFakeDockerOutput(t, argvLog, "g-1\ng-2\n\ng-1\n", "", 0)
	got, err := ListLabeledVMs(context.Background(), "owner=me")
	if err != nil {
		t.Fatalf("ListLabeledVMs: %v", err)
	}
	if want := []string{"g-1", "g-2"}; !slices.Equal(got, want) {
		t.Errorf("ListLabeledVMs = %v, want %v", got, want)
	}
	data, _ := os.ReadFile(argvLog)
	want := `ps -a --filter label=granule.dev/vm --filter label=owner=me --format {{.Label "granule.dev/vm"}}` + "\n"
	if string(data) != want {
		t.Errorf("docker argv = %q, want %q", data, want)
	}
}

func TestParseContainerExit(t *testing.T) {
	got, err := ParseContainerExit("exited 137 true\n")
	if err != nil || got != (ContainerExit{Status: "exited", ExitCode: 137, OOMKilled: true}) {
		t.Errorf("ParseContainerExit = %+v, %v", got, err)
	}
	if _, err := ParseContainerExit("exited"); err == nil {
		t.Error("ParseContainerExit accepted a truncated state")
	}
}

func TestGuestOOMLine(t *testing.T) {
	console := "[ 1.0] booted\n[ 9.1] cc1 invoked oom-killer: gfp_mask=0x0\n" +
		"[ 9.2] Out of memory: Killed process 77 (cc1) total-vm:1kB\n[ 9.3] after\n"
	if got := GuestOOMLine(console); got != "[ 9.2] Out of memory: Killed process 77 (cc1) total-vm:1kB" {
		t.Errorf("GuestOOMLine = %q, want the Killed process line", got)
	}
	if got := GuestOOMLine("[ 1.0] booted\n[ 2.0] login: ok\n"); got != "" {
		t.Errorf("GuestOOMLine of a quiet console = %q, want empty", got)
	}
}
