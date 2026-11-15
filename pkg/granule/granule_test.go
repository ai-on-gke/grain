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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
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

// docker refuses a `docker rm -f` of a container it is already removing,
// which is what an orphan sweep racing a run's release ran into. A caller
// that wanted the container gone has what it wanted.
func TestRemoveContainersTreatsARemovalInProgressAsRemoved(t *testing.T) {
	writeFakeDockerOutput(t, filepath.Join(t.TempDir(), "argv"), "",
		"Error response from daemon: removal of container granule-vm-g-1 is already in progress\n"+
			"Error response from daemon: No such container: granule-vm-g-1-netns\n", 1)
	if err := RemoveContainers(context.Background(), "g-1"); err != nil {
		t.Fatalf("RemoveContainers: %v", err)
	}
}

// buildGranuleCLI builds the vendored granule (third_party/granule, which
// go.mod points github.com/gke-labs/granule at) so Delete is tested
// against the real `granule vm delete`, docker error handling included,
// rather than against a stub that says what this package expects.
func buildGranuleCLI(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake docker script is POSIX shell only")
	}
	bin := filepath.Join(t.TempDir(), "granule")
	out, err := exec.Command("go", "build", "-o", bin, "github.com/gke-labs/granule/cmd/granule").CombinedOutput()
	if err != nil {
		t.Fatalf("building granule: %v\n%s", err, out)
	}
	return bin
}

// writeFakeDockerRemoving installs, as granule's docker (GRANULE_DOCKER),
// a docker that is already removing the VM's container: every `rm` of it
// fails the way docker says so, and `inspect` still finds it for the first
// present polls, after which it is gone. present < 0 is a removal that
// never finishes. Every argv is logged to argvLog.
func writeFakeDockerRemoving(t *testing.T, argvLog string, present int) {
	t.Helper()
	dir := t.TempDir()
	counter := filepath.Join(dir, "inspect-count")
	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> %[1]q
case "$1" in
stop)
  exit 0 ;;
rm)
  name="$3"
  case "$name" in
  *-netns)
    echo "Error response from daemon: No such container: $name" >&2
    exit 1 ;;
  esac
  echo "Error response from daemon: removal of container $name is already in progress" >&2
  exit 1 ;;
inspect)
  name=$(eval echo "\${$#}")
  n=$(cat %[2]q 2>/dev/null || echo 0)
  n=$((n + 1))
  echo "$n" > %[2]q
  if [ %[3]d -lt 0 ] || [ "$n" -le %[3]d ]; then
    echo true
    exit 0
  fi
  echo "Error: No such object: $name" >&2
  exit 1 ;;
esac
echo "unexpected docker $*" >&2
exit 2
`, argvLog, counter, present)
	path := filepath.Join(dir, "docker")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GRANULE_DOCKER", path)
}

func writeSavedState(t *testing.T, stateDir, name string) string {
	t.Helper()
	path := filepath.Join(stateDir, name+".json")
	if err := os.WriteFile(path, []byte(`{"name": "`+name+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// A delete that finds docker already removing the container waits that
// removal out and then finishes the job -- the saved state included,
// which granule only clears once the container is gone. granule does the
// waiting itself now, so Delete runs it once.
func TestDeleteWaitsOutARemovalAlreadyInProgress(t *testing.T) {
	c := Command{buildGranuleCLI(t)}
	argvLog := filepath.Join(t.TempDir(), "docker-argv")
	writeFakeDockerRemoving(t, argvLog, 2)

	stateDir := t.TempDir()
	state := writeSavedState(t, stateDir, "g-1")
	if err := c.Delete(context.Background(), stateDir, "g-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Errorf("the VM's saved state survived the delete (stat: %v)", err)
	}
	data, _ := os.ReadFile(argvLog)
	if got := strings.Count(string(data), "rm -f granule-vm-g-1\n"); got != 1 {
		t.Errorf("docker rm of the VM container ran %d times, want 1 (granule waits, grain does not retry):\n%s", got, data)
	}
	if got := strings.Count(string(data), "inspect "); got != 3 {
		t.Errorf("docker inspect ran %d times, want 3 (two still there, then gone):\n%s", got, data)
	}
}

// One that never settles is reported once ctx runs out, and leaves the
// saved state for the next sweep: Delete no longer calls it success.
func TestDeleteReportsARemovalThatNeverSettles(t *testing.T) {
	c := Command{buildGranuleCLI(t)}
	argvLog := filepath.Join(t.TempDir(), "docker-argv")
	writeFakeDockerRemoving(t, argvLog, -1)

	stateDir := t.TempDir()
	state := writeSavedState(t, stateDir, "g-1")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := c.Delete(ctx, stateDir, "g-1")
	if err == nil {
		t.Fatal("Delete of a removal that never settled succeeded")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Delete error = %v, want errors.Is(..., context.DeadlineExceeded)", err)
	}
	if _, err := os.Stat(state); err != nil {
		t.Errorf("the VM's saved state went with a removal that never finished (stat: %v)", err)
	}
	data, _ := os.ReadFile(argvLog)
	if got := strings.Count(string(data), "rm -f granule-vm-g-1\n"); got != 1 {
		t.Errorf("docker rm of the VM container ran %d times, want 1:\n%s", got, data)
	}
}

// Anything else docker says is still an error, and is not retried.
func TestDeleteReportsAnyOtherDockerFailure(t *testing.T) {
	c := Command{buildGranuleCLI(t)}
	argvLog := filepath.Join(t.TempDir(), "docker-argv")
	dir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\necho \"$*\" >> %q\necho 'Cannot connect to the Docker daemon at unix:///var/run/docker.sock' >&2\nexit 1\n", argvLog)
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GRANULE_DOCKER", filepath.Join(dir, "docker"))

	err := c.Delete(context.Background(), t.TempDir(), "g-1")
	if err == nil || !strings.Contains(err.Error(), "Cannot connect") {
		t.Fatalf("Delete = %v, want docker's own failure", err)
	}
	data, _ := os.ReadFile(argvLog)
	if got := strings.Count(string(data), "\n"); got != 1 {
		t.Errorf("docker ran %d times, want the one failed stop and no retry:\n%s", got, data)
	}
}

// Docker 28+/29 changed `docker inspect`'s missing-object stderr from
// "Error: No such object: <name>" to lowercase "error: no such object: <name>".
// Until upstream gke-labs/granule's case-sensitive isNotFound is updated and
// re-vendored, vendored granule ccdbc4b fails on the final poll of waitRemoved
// right as the container disappears and before vmspec.Delete clears the state
// file. Delete finishes clearing state with one follow-up call.
func TestDeleteFinishesWhenDocker29InspectPrintsLowercaseNoSuchObject(t *testing.T) {
	c := Command{buildGranuleCLI(t)}
	argvLog := filepath.Join(t.TempDir(), "docker-argv")
	dir := t.TempDir()
	counter := filepath.Join(dir, "inspect-count")
	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> %[1]q
n=$(cat %[2]q 2>/dev/null || echo 0)
case "$1" in
stop)
  if [ "$n" -gt 2 ]; then
    echo "Error response from daemon: No such container: $4" >&2
    exit 1
  fi
  exit 0 ;;
rm)
  name="$3"
  case "$name" in
  *-netns)
    exit 0 ;;
  esac
  if [ "$n" -gt 2 ]; then
    exit 0
  fi
  echo "Error response from daemon: removal of container $name is already in progress" >&2
  exit 1 ;;
inspect)
  name=$(eval echo "\${$#}")
  n=$((n + 1))
  echo "$n" > %[2]q
  if [ "$n" -le 2 ]; then
    echo false
    exit 0
  fi
  echo "error: no such object: $name" >&2
  exit 1 ;;
esac
echo "unexpected docker $*" >&2
exit 2
`, argvLog, counter)
	dockerPath := filepath.Join(dir, "docker")
	if err := os.WriteFile(dockerPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GRANULE_DOCKER", dockerPath)

	stateDir := t.TempDir()
	state := writeSavedState(t, stateDir, "g-1")
	if err := c.Delete(context.Background(), stateDir, "g-1"); err != nil {
		t.Fatalf("Delete against Docker 29 lowercase inspect error: %v", err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Errorf("the VM's saved state survived the delete (stat: %v)", err)
	}
	data, _ := os.ReadFile(argvLog)
	if got := strings.Count(string(data), "inspect "); got != 3 {
		t.Errorf("docker inspect ran %d times, want 3:\n%s", got, data)
	}
}

func TestCreateFinishesWhenDocker29StaleCleanupPrintsLowercaseNoSuchObject(t *testing.T) {
	c := stubGranuleWithStatefulExit(t,
		"granule: removing stale VM container granule-vm-g-1: docker inspect -f {{.State.Running}} granule-vm-g-1: exit status 1: error: no such object: granule-vm-g-1",
		"started granule-vm-g-1")
	if err := c.Create(context.Background(), t.TempDir(), "g-1"); err != nil {
		t.Fatalf("Create after stale-container lowercase no-such-object: %v", err)
	}
}

func stubGranuleWithStatefulExit(t *testing.T, firstErr, secondOut string) Command {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell stub only")
	}
	dir, err := os.MkdirTemp("", "granule-stateful-stub-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	marker := filepath.Join(dir, "called")
	path := filepath.Join(dir, "granule-stub")
	script := fmt.Sprintf("#!/bin/sh\nif [ ! -f %q ]; then\n  touch %q\n  printf '%%s\\n' %q >&2\n  exit 1\nfi\nprintf '%%s\\n' %q\nexit 0\n",
		marker, marker, firstErr, secondOut)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return Command{path}
}

func TestRemoveContainersHandlesCaseInsensitiveErrorsAndCancellation(t *testing.T) {
	writeFakeDockerOutput(t, filepath.Join(t.TempDir(), "argv"), "",
		"error response from daemon: removal of container granule-vm-g-1 is already in progress\n"+
			"error response from daemon: no such container: granule-vm-g-1-netns\n", 1)
	if err := RemoveContainers(context.Background(), "g-1"); err != nil {
		t.Fatalf("RemoveContainers on lowercase stderr: %v", err)
	}

	// A cancelled context must not be swallowed as success even if stderr only
	// printed "No such container" before SIGKILL landed.
	if runtime.GOOS != "windows" {
		dir := t.TempDir()
		script := "#!/bin/sh\necho 'Error response from daemon: No such container: granule-vm-g-1-netns' >&2\nexec sleep 5\n"
		if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		err := RemoveContainers(ctx, "g-1")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("RemoveContainers on expired ctx = %v, want context.DeadlineExceeded", err)
		}
	}
}

// Exercises Command.Delete and RemoveContainers against a real Docker daemon
// when one is reachable, covering the Docker 29 concurrent teardown race that
// fakedocker missed.
func TestRealDockerConcurrentDeleteSettlement(t *testing.T) {
	if testing.Short() || runtime.GOOS != "linux" {
		t.Skip("skipping real-Docker concurrent delete test")
	}
	realDocker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("docker binary not on PATH")
	}
	checkCtx, checkCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer checkCancel()
	if err := exec.CommandContext(checkCtx, realDocker, "info").Run(); err != nil {
		t.Skipf("docker daemon unavailable: %v", err)
	}

	c := Command{buildGranuleCLI(t)}
	suffix := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	imgTag := "grain-granule-del-test:" + suffix

	imgDir := t.TempDir()
	initSrc := filepath.Join(imgDir, "init.go")
	const src = `package main
import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)
func main() {
	_ = os.MkdirAll("/work", 0o755)
	buf := make([]byte, 4096)
	for i := 0; i < 6000; i++ {
		d := filepath.Join("/work", fmt.Sprintf("d%02d", i%64))
		if i < 64 {
			_ = os.MkdirAll(d, 0o755)
		}
		_ = os.WriteFile(filepath.Join(d, fmt.Sprintf("f%05d", i)), buf, 0o644)
	}
	fmt.Println("READY")
	for {
		time.Sleep(time.Hour)
	}
}
`
	if err := os.WriteFile(initSrc, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	initBin := filepath.Join(imgDir, "init")
	buildCmd := exec.Command("go", "build", "-o", initBin, initSrc)
	buildCmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("compiling scratch init: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(imgDir, "Dockerfile"), []byte("FROM scratch\nCOPY init /init\nENTRYPOINT [\"/init\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(realDocker, "build", "-q", "-t", imgTag, imgDir).CombinedOutput(); err != nil {
		t.Fatalf("docker build: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command(realDocker, "rmi", "-f", imgTag).Run()
	})

	startContainer := func(t *testing.T, vmName string) string {
		t.Helper()
		pod := PodName(vmName)
		t.Cleanup(func() {
			_ = exec.Command(realDocker, "rm", "-f", pod, pod+"-netns").Run()
		})
		out, err := exec.Command(realDocker, "run", "-d", "--name", pod, "--label", VMLabel+"="+vmName, imgTag).CombinedOutput()
		if err != nil {
			t.Fatalf("docker run %s: %v\n%s", pod, err, out)
		}
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			logs, _ := exec.Command(realDocker, "logs", pod).CombinedOutput()
			if strings.Contains(string(logs), "READY") {
				return pod
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("container %s never logged READY", pod)
		return ""
	}

	waitRemoving := func(t *testing.T, pod string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			out, err := exec.Command(realDocker, "inspect", "-f", "{{.State.Status}}", pod).Output()
			if err != nil {
				t.Fatalf("container %s vanished before 'removing' state was seen: %v", pod, err)
			}
			if strings.TrimSpace(string(out)) == "removing" {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s to enter 'removing' state", pod)
	}

	// 1. Direct `docker rm -f` racing `Command.Delete` + short ctx timeout check.
	vm1 := "rt1-" + suffix
	pod1 := startContainer(t, vm1)
	stateDir1 := t.TempDir()
	statePath1 := filepath.Join(stateDir1, vm1+".json")
	if err := os.WriteFile(statePath1, []byte(`{"name":"`+vm1+`","terminationGracePeriodSeconds":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	rmDone := make(chan error, 1)
	go func() {
		rmDone <- exec.Command(realDocker, "rm", "-f", pod1).Run()
	}()
	waitRemoving(t, pod1)

	shortCtx, shortCancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer shortCancel()
	shortErr := c.Delete(shortCtx, stateDir1, vm1)
	if !errors.Is(shortErr, context.DeadlineExceeded) {
		t.Fatalf("short Delete = %v, want context.DeadlineExceeded", shortErr)
	}
	if !Exists(stateDir1, vm1) {
		t.Fatal("saved state wiped despite short Delete timing out")
	}

	if err := c.Delete(context.Background(), stateDir1, vm1); err != nil {
		t.Fatalf("racing Delete after timeout: %v", err)
	}
	if err := <-rmDone; err != nil {
		t.Fatalf("direct docker rm -f: %v", err)
	}
	if Exists(stateDir1, vm1) {
		t.Fatal("saved state survived completed Delete")
	}
	if err := exec.Command(realDocker, "inspect", pod1).Run(); err == nil {
		t.Fatalf("container %s still exists after Delete", pod1)
	}

	// 2. Two concurrent Command.Delete calls + concurrent RemoveContainers.
	vm2 := "rt2-" + suffix
	pod2 := startContainer(t, vm2)
	stateDir2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir2, vm2+".json"), []byte(`{"name":"`+vm2+`","terminationGracePeriodSeconds":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i := range 2 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = c.Delete(context.Background(), stateDir2, vm2)
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		errs[2] = RemoveContainers(context.Background(), vm2)
	}()
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent teardown caller %d failed: %v", i, err)
		}
	}
	if Exists(stateDir2, vm2) {
		t.Fatal("saved state survived concurrent Command.Delete")
	}
	if err := exec.Command(realDocker, "inspect", pod2).Run(); err == nil {
		t.Fatalf("container %s still exists after concurrent teardown", pod2)
	}
}
