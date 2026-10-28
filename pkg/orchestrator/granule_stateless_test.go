package orchestrator_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/granule"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// fakeContainerHost is a docker host as far as a VM's containers go: one
// line per container in a file, "<container> <vm> <state-dir label>",
// shared between a fake granule that starts them and a fake docker that
// lists and removes them. It is what lets these tests see a VM that has
// a container and no state file -- the case nothing else in this suite
// can produce, because no other fake granule starts a container at all.
type fakeContainerHost struct {
	file string
}

// writeFakeContainerHost installs both fakes on PATH, ahead of the guest
// fake docker the caller has already installed (writeFakeDockerGuest),
// which keeps answering everything but "ps" and "rm".
//
// The fake granule's create starts the VM's container -- labelled with
// whatever grain.granule-state-dir label grain passed it, read off its
// own argv, so a create that forgot the label lists under no state dir
// -- and then saves the state file, unless dieBeforeSave, in which case
// it fails instead: granule's own submitVM killed between
// dockervm.Create and vmspec.Save. Its delete removes both, as granule's
// docker backend does.
func writeFakeContainerHost(t *testing.T, granuleArgvLog string, dieBeforeSave bool) fakeContainerHost {
	t.Helper()
	orchestrator.LogToTest(t)
	if runtime.GOOS == "windows" {
		t.Skip("fake granule/docker scripts are POSIX shell only")
	}
	inner, err := exec.LookPath("docker")
	if err != nil {
		t.Fatalf("install the guest fake docker first: %v", err)
	}
	dir := t.TempDir()
	host := fakeContainerHost{file: filepath.Join(dir, "containers")}
	if err := os.WriteFile(host.file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	die := 0
	if dieBeforeSave {
		die = 1
	}
	granuleScript := fmt.Sprintf(`#!/bin/sh
echo "$*" >> %[1]q
[ "$1" = vm ] || exit 0
action="$2"; name="$3"; shift 3
statedir=""; label=""
while [ $# -gt 0 ]; do
  case "$1" in
  -state-dir) statedir="$2" ;;
  %[3]s=*) label="${1#%[3]s=}" ;;
  esac
  shift
done
case "$action" in
create)
  echo "granule-vm-$name $name $label" >> %[2]q
  if [ %[4]d = 1 ]; then echo "killed before saving state" >&2; exit 1; fi
  echo '{}' > "$statedir/$name.json"
  ;;
delete)
  grep -v "^granule-vm-$name " %[2]q > %[2]q.new; mv %[2]q.new %[2]q
  rm -f "$statedir/$name.json"
  ;;
esac
`, granuleArgvLog, host.file, orchestrator.StateDirLabel, die)
	dockerScript := fmt.Sprintf(`#!/bin/sh
[ "$1" = --grain-fake-docker-warmup ] && exit 0
case "$1" in
ps)
  want=""
  for a in "$@"; do
    case "$a" in label=%[2]s=*) want="${a#label=%[2]s=}" ;; esac
  done
  while read -r c vm sd; do
    [ -z "$want" ] || [ "$sd" = "$want" ] && echo "$vm"
  done < %[1]q
  ;;
rm)
  shift; [ "$1" = -f ] && shift
  rc=0
  for c in "$@"; do
    if grep -q "^$c " %[1]q; then
      grep -v "^$c " %[1]q > %[1]q.new; mv %[1]q.new %[1]q
    else
      echo "Error response from daemon: No such container: $c" >&2; rc=1
    fi
  done
  exit $rc
  ;;
*) exec %[3]q "$@" ;;
esac
`, host.file, orchestrator.StateDirLabel, inner)
	for name, script := range map[string]string{"granule": granuleScript, "docker": dockerScript} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	warmUpFakeDocker(t, filepath.Join(dir, "docker"))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return host
}

// add puts a container on the host directly, as some earlier process did.
func (h fakeContainerHost) add(t *testing.T, vm, stateDir string) {
	t.Helper()
	f, err := os.OpenFile(h.file, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%s %s %s\n", granule.PodName(vm), vm, stateDir); err != nil {
		t.Fatal(err)
	}
}

// vms is every VM that still has a container, sorted.
func (h fakeContainerHost) vms(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(h.file)
	if err != nil {
		t.Fatal(err)
	}
	var vms []string
	for _, line := range splitNonEmptyLines(string(data)) {
		var c, vm string
		fmt.Sscan(line, &c, &vm)
		vms = append(vms, vm)
	}
	sort.Strings(vms)
	return vms
}

// A create that dies after granule started the VM's container and before
// it saved the state file leaves a running VM granule knows nothing about.
// A failed Acquire must still remove it: by name, since `granule vm
// delete` cannot reach a VM it has no state for.
func TestGranuleSandboxesAcquireRemovesTheContainerOfACreateThatDiedBeforeSavingState(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 0, "")
	granuleLog := filepath.Join(t.TempDir(), "granule-argv.log")
	host := writeFakeContainerHost(t, granuleLog, true)

	k := orchestrator.NewGranuleSandboxes(granuleTestConfig(stateDir))
	if _, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{}); err == nil {
		t.Fatal("expected Acquire to fail when create does")
	}
	if vms := host.vms(t); len(vms) != 0 {
		t.Errorf("containers left after a failed Acquire = %v, want none -- a VM with no state "+
			"file is still a running VM", vms)
	}
	data, _ := os.ReadFile(granuleLog)
	for _, line := range splitNonEmptyLines(string(data)) {
		if len(line) > 9 && line[:9] == "vm delete" {
			t.Errorf("granule was asked %q for a VM it has no state for; without state it "+
				"falls back to the static-pod backend and never reaches docker", line)
		}
	}
}

// Release is the same: a Rebuild whose create died before saving state
// leaves the sandbox holding a VM with a container and no state.
func TestGranuleSandboxesReleaseRemovesTheContainerOfAVMWithNoState(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 0, "")
	host := writeFakeContainerHost(t, filepath.Join(t.TempDir(), "granule-argv.log"), false)

	k := orchestrator.NewGranuleSandboxes(granuleTestConfig(stateDir))
	sb, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := os.Remove(filepath.Join(stateDir, "g-t1-1.json")); err != nil {
		t.Fatal(err)
	}
	if err := sb.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if vms := host.vms(t); len(vms) != 0 {
		t.Errorf("containers left after Release = %v, want none", vms)
	}
}

// SweepOrphans finds a stateless VM by its container's labels, and only
// one this deployment built: a container labelled with another state
// directory belongs to another daemon on the same docker host, which may
// well be running it right now.
func TestGranuleSandboxesSweepOrphansRemovesItsOwnStatelessContainers(t *testing.T) {
	stateDir := t.TempDir()
	otherStateDir := t.TempDir()
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 0, "")
	host := writeFakeContainerHost(t, filepath.Join(t.TempDir(), "granule-argv.log"), false)

	k := orchestrator.NewGranuleSandboxes(granuleTestConfig(stateDir))
	live, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	// The live VM's state goes missing too, so the only thing sparing it
	// below is that this process is using it.
	if err := os.Remove(filepath.Join(stateDir, "g-t1-1.json")); err != nil {
		t.Fatal(err)
	}
	host.add(t, "g-t9-1", stateDir)      // a create that died: ours, no state
	host.add(t, "g-t8-1", otherStateDir) // another daemon's
	host.add(t, "other-vm", stateDir)    // not under VMNamePrefix

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	swept, err := k.SweepOrphans(ctx)
	if err != nil {
		t.Fatalf("SweepOrphans: %v", err)
	}
	if swept != 1 {
		t.Errorf("swept = %d, want only g-t9-1", swept)
	}
	want := []string{"g-t1-1", "g-t8-1", "other-vm"}
	if got := host.vms(t); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("containers after the sweep = %v, want %v", got, want)
	}

	if err := live.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
}
