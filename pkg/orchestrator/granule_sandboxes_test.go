package orchestrator_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/dispatch"
	"github.com/bwsalmon/grain/pkg/granule"
	"github.com/bwsalmon/grain/pkg/mcp"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/sandboxaddr"
)

// defaultShapeArgs is the size every "granule vm create" carries when
// neither the run nor the deployment asked for one: grain's own default
// shape (orchestrator.DefaultShape, granule.DefaultCPUs/DefaultMemoryMB/
// DefaultDiskGB), in the MiB granule's disk flag takes. Spelled out as
// literal numbers rather than built from those constants, so that
// changing the size of every sandbox grain builds means editing a test
// that says what the new size is.
const defaultShapeArgs = " -cpus 2 -memory-mb 8192 -disk-size-mb 30720 -memory-limit-mb 8704 -oom-score-adj 500"

// readyWaitDeadline is the ReadyTimeout a test here gives an Acquire
// whose guest is expected to come up: how long the wait may take before
// the test calls it stuck.
//
// It is a liveness figure, not a latency one. These tests assert that
// Acquire keeps probing a guest that is not answering yet rather than
// giving up on the first refusal, and every probe is a fake shell script
// that answers in a millisecond or two on an idle machine -- nothing
// here is measuring how quickly a guest comes up. A second was the
// figure until grain's own CI started failing on it: the go job runs the
// whole repository's tests on a shared host, where a single process
// launch can take a hundred milliseconds, so a second is room for a
// handful of probes rather than the hundreds it buys locally, and a
// guest scripted to answer on its fourth was reported as one that never
// answered at all (pkg/ci's engineWaitDeadline is the same fix for the
// same reason).
//
// Ten seconds costs a passing run nothing -- every one of these waits
// returns the moment the guest answers -- and costs a genuinely stuck
// Acquire ten seconds before it says so, which is the right way round.
// Tests whose guest is meant to *never* answer keep their own short
// deadlines: there the wait giving up is the behaviour under test, and a
// slow host only makes it more certain.
const readyWaitDeadline = 10 * time.Second

// writeFakeGranule installs a shell script named "granule" on PATH --
// the operator-facing binary orchestrator.GranuleSandboxes actually execs
// via pkg/granule.Create/Delete, not the container-facing "granule" binary
// bwsalmon/granule's own cmd/granule is a distinct program from (see
// pkg/granule's package doc comment) -- that answers "vm create <name>
// -state-dir <dir> ..." by writing <dir>/<name>.json with the given port
// (granule's own real behavior, which is what Port later reads back) and
// "vm delete <name> -state-dir <dir>" by removing that same file (granule's
// own staticpod.Delete, mirrored here so a test that rebuilds under a
// name sees, on its second "vm create", a VM that genuinely doesn't exist
// yet rather than reusing stale state) -- logs every invocation's argv (one line per call) to
// argvLog, and otherwise succeeds silently.
func writeFakeGranule(t *testing.T, argvLog string, port int) {
	t.Helper()
	// The granule backend narrates what it did to a guest (a granted kvm
	// group, a dropped default route), and every test of it starts here.
	// See log_test.go.
	orchestrator.LogToTest(t)
	if runtime.GOOS == "windows" {
		t.Skip("fake granule script is POSIX shell only")
	}
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> %q
if [ "$1" = "vm" ] && { [ "$2" = "create" ] || [ "$2" = "delete" ]; }; then
  action="$2"
  name="$3"
  statedir=""
  shift 3
  while [ $# -gt 0 ]; do
    if [ "$1" = "-state-dir" ]; then
      statedir="$2"
    fi
    shift
  done
  if [ "$action" = "create" ]; then
    echo "{\"port\": %d}" > "$statedir/$name.json"
  else
    rm -f "$statedir/$name.json"
  fi
fi
`, argvLog, port)
	path := filepath.Join(dir, "granule")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestGranuleSandboxesConfigureGitCredentialsWritesToTheVMOverSSH(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30080)
	home := t.TempDir()
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 0, home)

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:    stateDir,
		SSHUser:     "debian",
		ExecKeyPath: "/images/key",
		Workspace:   "/workspace",
	})

	sb, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{})
	if err != nil {
		t.Fatal(err)
	}
	identity := mcp.GitIdentity{Name: "acme bot", Email: "bot@acme.example"}
	if err := sb.ConfigureGitCredentials(context.Background(), "http://10.100.0.1:8080/owner/repo.git", "secret-token", identity); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(home, ".git-credentials"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "http://sandbox:secret-token@10.100.0.1:8080\n"; string(data) != want {
		t.Errorf(".git-credentials = %q, want %q", data, want)
	}
	// The identity, not just the file's existence: a deployment that has
	// configured its committer has to get that committer inside the guest,
	// which is the whole route this test is here to cover.
	gitconfig, err := os.ReadFile(filepath.Join(home, ".gitconfig"))
	if err != nil {
		t.Fatalf(".gitconfig was not written on the VM: %v", err)
	}
	for _, want := range []string{"name = acme bot", "email = bot@acme.example"} {
		if !strings.Contains(string(gitconfig), want) {
			t.Errorf(".gitconfig = %q, want it to contain %q", gitconfig, want)
		}
	}

	// A second call for the same slot must not create a second VM --
	// ConfigureGitCredentials runs over the runner Acquire already
	// resolved, so it needs no setup of its own; this just confirms it
	// actually took effect end to end.
	if err := sb.ConfigureGitCredentials(context.Background(), "http://10.100.0.1:8080/owner/repo.git", "second-token", identity); err != nil {
		t.Fatal(err)
	}
}

// The capability half of the same story: a granule VM's only route for a
// gcp-key/gemini-key/github-sandbox placement is its own runner, and
// before granuleSandbox implemented orchestrator.SandboxPlacer there was
// no route at all -- a task granting any of them failed during
// preparation on every -granule-sandboxes deployment. The mode matters as
// much as the content: what lands here is a live service-account key.
func TestGranuleSandboxesPlaceFileWritesACredentialToTheVMOverSSH(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30081)
	home := t.TempDir()
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 0, home)

	k := orchestrator.NewGranuleSandboxes(granuleTestConfig(stateDir))
	sb, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{})
	if err != nil {
		t.Fatal(err)
	}

	// The interface assertion is the point: runOne finds this route by
	// type-asserting the Sandbox it was handed, so a granuleSandbox that
	// stopped satisfying it would silently go back to placing nothing.
	placer, ok := sb.(orchestrator.SandboxPlacer)
	if !ok {
		t.Fatal("a granule sandbox does not implement orchestrator.SandboxPlacer, so its placements have nowhere to land")
	}

	// An absolute path under the fake guest's own home, standing in for
	// gcpkey.SandboxKeyPath's /home/debian/.gcp-service-account.json --
	// the real one would write outside this test's temp directories.
	target := filepath.Join(home, ".gcp-service-account.json")
	const key = `{"type":"service_account","project_id":"grain"}`
	if err := placer.PlaceFile(context.Background(), target, key, "600"); err != nil {
		t.Fatalf("PlaceFile: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("the key never reached the VM: %v", err)
	}
	if string(got) != key {
		t.Errorf("key on the VM = %q, want %q", got, key)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key mode on the VM = %04o, want 0600 -- \"readable only by you\" is what the prompt tells the agent", perm)
	}
}

// A failure to place has to name the VM, not just the path: an operator
// reading a failed run's reason is looking at one slot out of a fleet.
func TestGranuleSandboxesPlaceFileReportsFailureAgainstTheVMName(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30082)
	home := t.TempDir()
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 0, home)

	k := orchestrator.NewGranuleSandboxes(granuleTestConfig(stateDir))
	sb, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{})
	if err != nil {
		t.Fatal(err)
	}
	placer := sb.(orchestrator.SandboxPlacer)

	occupied := filepath.Join(home, "occupied")
	if err := os.Mkdir(occupied, 0o755); err != nil {
		t.Fatal(err)
	}
	err = placer.PlaceFile(context.Background(), occupied, "material", "600")
	if err == nil {
		t.Fatal("PlaceFile reported success for a placement that cannot have landed")
	}
	if vm := sb.(interface{ VMName() string }).VMName(); !strings.Contains(err.Error(), vm) {
		t.Errorf("error = %v, want it to name the VM %q", err, vm)
	}
}

func TestGranuleSandboxesAcquireCreatesAVMAndReleaseDeletesIt(t *testing.T) {
	stateDir := t.TempDir()
	argvLog := filepath.Join(t.TempDir(), "granule-argv.log")
	writeFakeGranule(t, argvLog, 30080)
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 0, "")

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:          stateDir,
		SSHUser:           "debian",
		ExecKeyPath:       "/images/key",
		Workspace:         "/workspace",
		ReadyPollInterval: time.Millisecond,
	})

	sb, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{})
	if err != nil {
		t.Fatal(err)
	}
	if sb.Name() != "t1-1" {
		t.Errorf("Name() = %q, want the sandbox's own name", sb.Name())
	}

	tools, err := sb.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantNames := map[string]bool{"run_command": true, "read_file": true, "write_file": true, "edit_file": true}
	if len(tools) != len(wantNames) {
		t.Fatalf("Tools() returned %d tools, want %d", len(tools), len(wantNames))
	}
	for _, tool := range tools {
		if !wantNames[tool.Name] {
			t.Errorf("unexpected tool %q", tool.Name)
		}
	}

	if err := sb.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}

	data, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	lines := splitNonEmptyLines(string(data))
	wantCreate := "vm create g-t1-1 -state-dir " + stateDir + " -backend docker" + defaultShapeArgs + stateDirLabelArgs(stateDir)
	wantDelete := "vm delete g-t1-1 -state-dir " + stateDir
	var creates, deletes int
	for _, line := range lines {
		switch line {
		case wantCreate:
			creates++
		case wantDelete:
			deletes++
		}
	}
	if creates != 1 || deletes != 1 {
		t.Errorf("granule calls = %q, want exactly one create and one delete", lines)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "g-t1-1.json")); err == nil {
		t.Error("VM state still exists after Release, want it deleted")
	}
}

// A VM already sitting under the name a run is about to use is deleted
// and rebuilt, not adopted. Adopting one was right while a name meant a
// slot -- "reuse what's there" -- but a name means a run now, so anything
// already wearing it is a leftover whose filesystem this run must not
// start from.
func TestGranuleSandboxesAcquireRebuildsAVMLeftUnderTheSameName(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, "g-t1-1.json"), []byte(`{"port": 30080}`), 0o644); err != nil {
		t.Fatal(err)
	}
	argvLog := filepath.Join(t.TempDir(), "granule-argv.log")
	writeFakeGranule(t, argvLog, 30080)
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 0, "")

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:          stateDir,
		SSHUser:           "debian",
		ExecKeyPath:       "/images/key",
		Workspace:         "/workspace",
		ReadyPollInterval: time.Millisecond,
	})

	if _, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	lines := splitNonEmptyLines(string(data))
	if len(lines) < 2 || lines[0] != "vm delete g-t1-1 -state-dir "+stateDir {
		t.Fatalf("granule calls = %q, want the stale VM deleted before anything else", lines)
	}
	if lines[1] != "vm create g-t1-1 -state-dir "+stateDir+" -backend docker"+defaultShapeArgs+stateDirLabelArgs(stateDir) {
		t.Errorf("granule calls = %q, want a create straight after the delete", lines)
	}
}

// ReapOrphans is the startup half of the same rule: at startup no VM can
// belong to this process, so every one under this deployment's prefix is
// a leftover. VMs under another prefix are left alone.
func TestGranuleSandboxesReapOrphansDeletesOnlyItsOwnPrefix(t *testing.T) {
	stateDir := t.TempDir()
	for _, name := range []string{"g-t1-1", "g-t2-1", "other-vm"} {
		if err := os.WriteFile(filepath.Join(stateDir, name+".json"), []byte(`{"port": 30080}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	argvLog := filepath.Join(t.TempDir(), "granule-argv.log")
	writeFakeGranule(t, argvLog, 30080)

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir: stateDir, SSHUser: "debian",
		ExecKeyPath: "/images/key", Workspace: "/workspace",
	})

	reaped, err := k.ReapOrphans(context.Background())
	if err != nil {
		t.Fatalf("ReapOrphans: %v", err)
	}
	if reaped != 2 {
		t.Errorf("reaped = %d, want 2", reaped)
	}
	for _, name := range []string{"g-t1-1", "g-t2-1"} {
		if _, err := os.Stat(filepath.Join(stateDir, name+".json")); err == nil {
			t.Errorf("%s survived the reap", name)
		}
	}
	if _, err := os.Stat(filepath.Join(stateDir, "other-vm.json")); err != nil {
		t.Error("a VM under another prefix was reaped, want it left alone")
	}
}

func TestGranuleSandboxesWaitsForVMToBecomeReady(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30080)
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 3, "")

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:          stateDir,
		SSHUser:           "debian",
		ExecKeyPath:       "/images/key",
		Workspace:         "/workspace",
		ReadyPollInterval: time.Millisecond,
		ReadyTimeout:      readyWaitDeadline,
	})

	if _, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{}); err != nil {
		t.Fatalf("Acquire() did not wait out the VM's slow start: %v", err)
	}
}

// A guest that answers is not yet a guest a task can be dispatched into,
// and these two cover the gap that opened when `granule exec` moved from
// SSH to vsock (bwsalmon/granule#46).
//
// Under SSH, reaching the guest at all *was* its network coming up, so
// "the agent answers" and "the guest is on the network" were one event.
// Over vsock they are not: granule-agent is ordered before sysinit.target
// precisely so a guest whose networking failed can still be got into, so
// it answers seconds before the units that configure the NIC from the
// ip= parameter have run. Confirmed against a real VM, which is how this
// was found: the guest ran commands happily with an empty routing table,
// and a dispatched run inside it could reach neither the git proxy nor
// any package registry.
func TestGranuleSandboxesWaitsForTheGuestToBeOnTheNetwork(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30080)
	// The agent answers from the first probe; the default route only
	// arrives on the fourth.
	dockerLog := filepath.Join(t.TempDir(), "docker-argv.log")
	writeFakeDockerGuestNetwork(t, dockerLog, filepath.Join(t.TempDir(), "counter"), 0, "", 3)

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:          stateDir,
		SSHUser:           "debian",
		Workspace:         "/workspace",
		ReadyPollInterval: time.Millisecond,
		ReadyTimeout:      readyWaitDeadline,
	})

	if _, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{}); err != nil {
		t.Fatalf("Acquire() did not wait for the guest's network: %v", err)
	}

	// Asserted rather than inferred from the absence of an error: a
	// readiness wait that asked the guest for nothing would return
	// against this same fake just as happily, and the difference between
	// the two is exactly what this test is for.
	log, err := os.ReadFile(dockerLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "/proc/net/route") {
		t.Errorf("docker argv log = %q, want a readiness probe that asked the guest about its routing table", log)
	}
}

// A guest that answers `granule exec` and never acquires a default route
// costs the same thing a VM container found dead does -- the whole
// dispatched run, or a whole CI job, on one unlucky boot -- and has the
// same shape of cause: an early-boot unit that did not run this time
// (grain task 137 found it, a one-shot whose `udevadm settle` timed out
// on a loaded host) and says nothing about the next boot. So Acquire
// rebuilds the VM once rather than handing the failure to the run.
//
// The route arriving is tied here to the rebuild itself rather than to a
// probe count or a wall clock, so what this asserts is that the second
// boot is what made the difference, on any machine at any load.
func TestGranuleSandboxesRebuildsAGuestThatNeverGetsADefaultRoute(t *testing.T) {
	stateDir := t.TempDir()
	granuleLog := filepath.Join(t.TempDir(), "granule-argv.log")
	writeFakeGranule(t, granuleLog, 30080)
	writeFakeDockerGuestRoutelessUntilRebuilt(t, filepath.Join(t.TempDir(), "docker-argv.log"), granuleLog)

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:          stateDir,
		SSHUser:           "debian",
		Workspace:         "/workspace",
		ReadyPollInterval: time.Millisecond,
		ReadyTimeout:      10 * time.Millisecond,
	})

	if _, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{}); err != nil {
		t.Fatalf("Acquire() on a guest whose first boot never got a default route: %v -- want the VM rebuilt once and the sandbox handed over", err)
	}
	if got := granuleCreateCount(t, granuleLog); got != 2 {
		t.Errorf("granule created the VM %d time(s), want 2: the first boot's guest never routed itself, which is worth one rebuild", got)
	}
}

func TestGranuleSandboxesSaysSoWhenTheGuestNeverGetsADefaultRoute(t *testing.T) {
	stateDir := t.TempDir()
	granuleLog := filepath.Join(t.TempDir(), "granule-argv.log")
	writeFakeGranule(t, granuleLog, 30080)
	writeFakeDockerGuestNetwork(t, filepath.Join(t.TempDir(), "docker-argv.log"),
		filepath.Join(t.TempDir(), "counter"), 0, "", 1000)

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:          stateDir,
		SSHUser:           "debian",
		Workspace:         "/workspace",
		ReadyPollInterval: time.Millisecond,
		ReadyTimeout:      10 * time.Millisecond,
	})

	_, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{})
	if err == nil {
		t.Fatal("Acquire() on a guest that never gets a default route: got nil error, want one")
	}
	// The distinction is the whole value of the message: a guest that
	// cannot be reached and a guest that answers but has no route are
	// different faults, in different halves of the boot, and an operator
	// reading this is deciding which one to go and look at.
	if !strings.Contains(err.Error(), "no default route") {
		t.Errorf("Acquire() error = %v, want it to say the guest answers but has no default route", err)
	}
	// And carries what the guest said about it. The sandbox is destroyed
	// moments after this error is returned, taking the failed unit and
	// the journal line that explain the failure with it, so an error
	// that only reports the symptom is one every occurrence has to be
	// diagnosed from scratch -- which is what happened three times
	// (grain tasks 109 and 116) before this evidence was collected at
	// all. See guestNetworkDump.
	if !strings.Contains(err.Error(), "What the guest itself says") ||
		!strings.Contains(err.Error(), "granule-net-cmdline.service") {
		t.Errorf("Acquire() error = %v, want it to carry the guest's own account of its networking", err)
	}
	// One rebuild, not a loop. A guest that never routes itself may be a
	// deployment whose sandboxes genuinely have no gateway to route to
	// (netshim's Identity.Gateway may be nil), and there every Acquire
	// fails however many times it is tried -- so the rebuild is bounded
	// at one, and the second failure is the one an operator is told
	// about.
	if got := granuleCreateCount(t, granuleLog); got != 2 {
		t.Errorf("granule created the VM %d time(s), want exactly 2: one rebuild and no more", got)
	}
	if !strings.Contains(err.Error(), "already been rebuilt once") {
		t.Errorf("Acquire() error = %v, want it to say this VM had already been rebuilt once -- the same message for a first and a second failed boot reads as a fresh flake", err)
	}
}

// The dump has to be collected as root, and this is the only place that
// can be checked: an unprivileged dump does not fail, it comes back
// having answered "Failed to connect to bus: No such file or directory"
// for the failed units and "No journal files were opened due to
// insufficient permissions" for the journal -- which is what every one
// of the 87 dumps taken during the 21 Sep 2026 sandbox-creation failures
// said, and why none of them could settle what the guests were failing
// at.
//
// Leaving the account unset is what produced that, and it is not the
// obvious bug it looks like: guestexec's own default *is* root, but
// `docker exec` runs with the container's own environment, and the VM
// container already carries a GRANULE_EXEC_USER naming the deployment's
// ordinary sandbox account (`granule vm create -guest-user`). So the
// dump inherited the unprivileged account instead of falling back to
// anything. Naming root explicitly is what overrides it -- see
// guestDumpUser.
func TestGranuleSandboxesDumpsARoutelessGuestAsRoot(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30080)
	dockerLog := filepath.Join(t.TempDir(), "docker-argv.log")
	writeFakeDockerGuestNetwork(t, dockerLog, filepath.Join(t.TempDir(), "counter"), 0, "", 1000)

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:          stateDir,
		SSHUser:           "debian",
		Workspace:         "/workspace",
		ReadyPollInterval: time.Millisecond,
		ReadyTimeout:      10 * time.Millisecond,
	})

	if _, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{}); err == nil {
		t.Fatal("Acquire() on a guest that never gets a default route: got nil error, want one")
	}

	log, err := os.ReadFile(dockerLog)
	if err != nil {
		t.Fatal(err)
	}
	var dumps int
	for _, line := range splitNonEmptyLines(string(log)) {
		if !strings.Contains(line, "granule-guest-dump") {
			continue
		}
		dumps++
		if !strings.Contains(line, "GRANULE_EXEC_USER=root") {
			t.Errorf("the guest dump ran as %q, want it to name root: an unprivileged dump reports no failed units and no journal, which is the whole of what it exists to report", line)
		}
	}
	if dumps == 0 {
		t.Errorf("docker argv log = %q, want a guest dump in it", log)
	}
}

// The other half of the diagnosis, and the half that survives a guest
// nothing can be asked of at all: the VM container's own log, which is
// the guest's serial console. A guest that never started listening on
// vsock answers no dump, so without this such a failure travels with
// nothing but "could not connect" -- while the console carries whatever
// the guest's boot units printed, because they are declared
// StandardOutput=journal+console for exactly this.
//
// It is also the only copy. Acquire deletes or rebuilds the VM within
// seconds of this error, so an error that merely advised running
// `docker logs` pointed at a container that no longer exists by the time
// anybody reads it.
func TestGranuleSandboxesCarriesTheVMConsoleWhenTheGuestNeverAnswers(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30080)
	// readyAfter far beyond any probe this test will make: the guest
	// never answers, the way a guest that has not yet started listening
	// on vsock does not.
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 1000, "")

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:          stateDir,
		SSHUser:           "debian",
		Workspace:         "/workspace",
		ReadyPollInterval: time.Millisecond,
		ReadyTimeout:      10 * time.Millisecond,
	})

	_, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{})
	if err == nil {
		t.Fatal("Acquire() on a guest that never answers: got nil error, want one")
	}
	if !strings.Contains(err.Error(), fakeGuestConsoleLine) {
		t.Errorf("Acquire() error = %v, want it to carry what the VM container's console said -- the only account of this boot, and one that needs nothing inside the guest to be working", err)
	}
	// And says how long it really waited. What the probe itself reports
	// is its own connect timeout, one poll interval, which on its own
	// reads as an orchestrator that gave a booting guest two seconds.
	if !strings.Contains(err.Error(), "gave up after waiting") {
		t.Errorf("Acquire() error = %v, want it to say how long the wait actually was", err)
	}
}

// The routeless failure carries the console too, alongside the guest's
// own dump: the two answer different questions (what the guest's units
// think happened, and what the guest printed before anything could be
// asked), and a failure that turns out to be neither is one an operator
// has to have both to rule out.
func TestGranuleSandboxesCarriesTheVMConsoleWhenTheGuestNeverRoutesItself(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30080)
	writeFakeDockerGuestNetwork(t, filepath.Join(t.TempDir(), "docker-argv.log"),
		filepath.Join(t.TempDir(), "counter"), 0, "", 1000)

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:          stateDir,
		SSHUser:           "debian",
		Workspace:         "/workspace",
		ReadyPollInterval: time.Millisecond,
		ReadyTimeout:      10 * time.Millisecond,
	})

	_, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{})
	if err == nil {
		t.Fatal("Acquire() on a guest that never gets a default route: got nil error, want one")
	}
	if !strings.Contains(err.Error(), fakeGuestConsoleLine) {
		t.Errorf("Acquire() error = %v, want the guest's dump *and* the VM container's console", err)
	}
	if !strings.Contains(err.Error(), "granule-net-cmdline.service") {
		t.Errorf("Acquire() error = %v, want the guest's own dump kept alongside the console", err)
	}
}

func TestGranuleSandboxesGivesUpAfterReadyTimeout(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30080)
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 1000, "")

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:          stateDir,
		SSHUser:           "debian",
		ExecKeyPath:       "/images/key",
		Workspace:         "/workspace",
		ReadyPollInterval: time.Millisecond,
		ReadyTimeout:      10 * time.Millisecond,
	})

	if _, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{}); err == nil {
		t.Fatal("Acquire() on a VM that never becomes ready: got nil error, want one")
	}
}

// TestGranuleSandboxesFastFailsWhenTheVMContainerExitsEarly guards against
// a real failure mode confirmed by hand against a real docker daemon
// (29.7.2) and a deliberately broken CHV_DISK_IMAGE path: under
// BackendDocker, "granule vm create" starts the VM container with a
// plain "docker run -d" (bwsalmon/granule's own internal/dockervm), which
// -- like any "docker run -d" -- reports success the instant the
// container starts, not once cloud-hypervisor inside it has actually
// proven itself alive. A guest that fails before finishing boot exits
// within seconds of that "success", and without this check Acquire would
// have no way to tell that apart from "still booting," so it would poll a
// dead port for the entire ReadyTimeout before finally giving up with a
// generic connection-refused error. This drives Acquire against a fake
// docker whose VM container is already "exited" from the first inspect
// call, with a ReadyTimeout generous enough that a timing-based pass would
// be meaningless (a slow CI host might legitimately not fail by then) and
// asserts instead that Acquire returns quickly, well under that timeout,
// and that the error names the container and mentions its exited status
// rather than just "connection refused."
func TestGranuleSandboxesFastFailsWhenTheVMContainerExitsEarly(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30082)
	// "exited" from the start: the VM container is dead, so no exec into
	// it will ever answer, and nothing here should wait around confirming
	// that the slow way.
	writeFakeDocker(t, filepath.Join(t.TempDir(), "docker-argv.log"), "exited",
		`echo "Error response from daemon: container is not running" >&2; exit 1`)

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:          stateDir,
		SSHUser:           "debian",
		ExecKeyPath:       "/images/key",
		Workspace:         "/workspace",
		ReadyPollInterval: 10 * time.Millisecond,
		ReadyTimeout:      10 * time.Second,
	})

	started := time.Now()
	_, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{})
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("Acquire() against a VM container that already exited: got nil error, want one")
	}
	if elapsed > 2*time.Second {
		t.Errorf("Acquire() took %s to fail, want well under the 10s ReadyTimeout -- it should fast-fail on the dead container instead of polling out the full deadline", elapsed)
	}
	if !strings.Contains(err.Error(), "exited") {
		t.Errorf("error = %q, want it to mention the container's \"exited\" status", err)
	}
	if !strings.Contains(err.Error(), "granule-vm-g-t1-1") {
		t.Errorf("error = %q, want it to name the container granule-vm-g-t1-1", err)
	}
}

// NetMode, IP and Port reach granule nowhere, however they are set. All
// three are deprecated in the granule this repository vendors -- "-net"
// has only the former flat behaviour left, and "-ip"/"-port" are
// "deprecated and ignored" -- so passing them described a granule that no
// longer exists. They are kept on GranuleConfig only until a later release
// removes them, and cmd/grain warns a deployment still setting the flags
// behind them.
func TestGranuleSandboxesIgnoresTheDeprecatedNetModeIPAndPort(t *testing.T) {
	stateDir := t.TempDir()
	argvLog := filepath.Join(t.TempDir(), "granule-argv.log")
	writeFakeGranule(t, argvLog, 30080)
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 0, "")

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:          stateDir,
		SSHUser:           "debian",
		ExecKeyPath:       "/images/key",
		Workspace:         "/workspace",
		NetMode:           granule.NetModeNAT,
		IP:                "169.254.100.2",
		Port:              30080,
		ReadyPollInterval: time.Millisecond,
	})

	if _, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{}); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Acquire(context.Background(), "t2-1", orchestrator.Shape{}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	// No -net, no -ip, no -port, even though the config sets all three.
	want := []string{
		"vm create g-t1-1 -state-dir " + stateDir + " -backend docker" + defaultShapeArgs + stateDirLabelArgs(stateDir),
		"vm create g-t2-1 -state-dir " + stateDir + " -backend docker" + defaultShapeArgs + stateDirLabelArgs(stateDir),
	}
	got := splitNonEmptyLines(string(data))
	if len(got) != len(want) {
		t.Fatalf("granule invocations = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("invocation %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestGranuleSandboxesCreateAppendsDefaultShape confirms
// GranuleConfig.DefaultCPUs/DefaultMemoryMB/DefaultDiskGB
// (bwsalmon/agents#534, grain/task-41) reach "granule vm create" as
// -cpus/-memory-mb/-disk-size-mb, after CreateArgs (the last in MiB:
// see createArgs' own mibPerGiB) -- so an
// operator's own -granule-create-arg=-cpus (if not set here too) is never
// silently overridden by leaving DefaultCPUs at its zero "unset" value,
// but the deployment-wide setting wins when both are configured, the
// same "last one wins" precedent IP/Port already set for this same
// argument list. A run's own Shape overrides them per dimension.
func TestGranuleSandboxesCreateAppendsDefaultShape(t *testing.T) {
	stateDir := t.TempDir()
	argvLog := filepath.Join(t.TempDir(), "granule-argv.log")
	writeFakeGranule(t, argvLog, 30080)
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 0, "")

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:          stateDir,
		SSHUser:           "debian",
		ExecKeyPath:       "/images/key",
		Workspace:         "/workspace",
		CreateArgs:        []string{"-disk", "/images/current/disk.img"},
		DefaultCPUs:       4,
		DefaultMemoryMB:   8192,
		DefaultDiskGB:     40,
		ReadyPollInterval: time.Millisecond,
	})

	if _, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	want := "vm create g-t1-1 -state-dir " + stateDir + " -backend docker -disk /images/current/disk.img -cpus 4 -memory-mb 8192 -disk-size-mb 40960 -memory-limit-mb 8704 -oom-score-adj 500" + stateDirLabelArgs(stateDir)
	got := splitNonEmptyLines(string(data))
	if len(got) != 1 || got[0] != want {
		t.Errorf("granule invocations = %v, want [%q]", got, want)
	}
}

// SetDefaultShape is the same setting, changed while the daemon runs:
// model.Config.SandboxCPUs/SandboxMemoryMB/SandboxDiskGB edited in
// Settings reach the next sandbox built rather than only the next
// process (pkg/cli/daemon.go's liveConfig calls this once per
// reconcile tick when any of them has moved). A run that asked for a
// shape of its own still wins, per dimension, exactly as it does over
// the constructor's value.
func TestGranuleSandboxesSetDefaultShapeAppliesToTheNextCreate(t *testing.T) {
	stateDir := t.TempDir()
	argvLog := filepath.Join(t.TempDir(), "granule-argv.log")
	writeFakeGranule(t, argvLog, 30080)
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 0, "")

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:          stateDir,
		SSHUser:           "debian",
		ExecKeyPath:       "/images/key",
		Workspace:         "/workspace",
		DefaultCPUs:       2,
		DefaultMemoryMB:   2048,
		ReadyPollInterval: time.Millisecond,
	})
	k.SetDefaultShape(orchestrator.Shape{CPUs: 8, MemoryMB: 16384, DiskGB: 40})

	if _, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{}); err != nil {
		t.Fatal(err)
	}
	// And a run with its own request, to prove the new default is still
	// only a fallback: the task's CPUs win, the deployment's memory and
	// disk fill in the dimensions it left alone.
	if _, err := k.Acquire(context.Background(), "t2-1", orchestrator.Shape{CPUs: 1}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"vm create g-t1-1 -state-dir " + stateDir + " -backend docker -cpus 8 -memory-mb 16384 -disk-size-mb 40960 -memory-limit-mb 16896 -oom-score-adj 500" + stateDirLabelArgs(stateDir),
		"vm create g-t2-1 -state-dir " + stateDir + " -backend docker -cpus 1 -memory-mb 16384 -disk-size-mb 40960 -memory-limit-mb 16896 -oom-score-adj 500" + stateDirLabelArgs(stateDir),
	}
	got := splitNonEmptyLines(string(data))
	if len(got) != len(want) {
		t.Fatalf("granule invocations = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("invocation %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// Clearing every dimension back to "unset" has to reach the backend too
// -- otherwise a deployment that went back to grain's own default VM
// size would keep building the size it had been told to forget. What it
// lands on is that default, spelled out on the create: "unset" has never
// meant "let granule decide" since grain started naming all three sizes
// itself, and the numbers below are the ones every sandbox is built at
// on a deployment that has configured nothing.
func TestGranuleSandboxesSetDefaultShapeClearsBackToGrainsOwnDefault(t *testing.T) {
	stateDir := t.TempDir()
	argvLog := filepath.Join(t.TempDir(), "granule-argv.log")
	writeFakeGranule(t, argvLog, 30080)
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 0, "")

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:          stateDir,
		SSHUser:           "debian",
		ExecKeyPath:       "/images/key",
		Workspace:         "/workspace",
		DefaultCPUs:       4,
		DefaultMemoryMB:   8192,
		DefaultDiskGB:     40,
		ReadyPollInterval: time.Millisecond,
	})
	k.SetDefaultShape(orchestrator.Shape{})

	if _, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	want := "vm create g-t1-1 -state-dir " + stateDir + " -backend docker -cpus 2 -memory-mb 8192 -disk-size-mb 30720 -memory-limit-mb 8704 -oom-score-adj 500" + stateDirLabelArgs(stateDir)
	got := splitNonEmptyLines(string(data))
	if len(got) != 1 || got[0] != want {
		t.Errorf("granule invocations = %v, want [%q]", got, want)
	}
	// The same numbers, read off the constants the rest of the repo
	// resolves an unset dimension through, so that a build whose
	// DefaultShape drifted from the flags above fails here saying which
	// of the two moved.
	if def := orchestrator.DefaultShape(); def.CPUs != 2 || def.MemoryMB != 8192 || def.DiskGB != 30 {
		t.Errorf("orchestrator.DefaultShape() = %d vCPU/%d MiB/%d GiB, want 2/8192/30", def.CPUs, def.MemoryMB, def.DiskGB)
	}
}

func TestGranuleSandboxesSetsPerVMCgroupMemoryLimitAndPositiveOOMScoreAdj(t *testing.T) {
	granuleBinDir := buildGranule(t)
	dockerLog := filepath.Join(t.TempDir(), "docker-argv.log")
	writeFakeDockerGuest(t, dockerLog, filepath.Join(t.TempDir(), "counter"), 0, "")
	t.Setenv("PATH", granuleBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stateDir := t.TempDir()
	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:          stateDir,
		SSHUser:           "debian",
		Workspace:         "/workspace",
		ReadyPollInterval: time.Millisecond,
	})

	if _, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{MemoryMB: 4096}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(dockerLog)
	if err != nil {
		t.Fatal(err)
	}
	var runLine string
	for _, line := range splitNonEmptyLines(string(data)) {
		if strings.HasPrefix(line, "run ") {
			runLine = line
			break
		}
	}
	if runLine == "" {
		t.Fatalf("no `docker run` call recorded in:\n%s", data)
	}
	// 4096 MiB shape + 512 MiB VMM overhead = 4608 MiB cgroup memory limit,
	// with --memory-swap equal to --memory (bounding memory.swap.max to 0)
	// and --oom-score-adj 500.
	for _, want := range []string{
		"--oom-score-adj 500",
		"--memory 4608m",
		"--memory-swap 4608m",
	} {
		if !strings.Contains(runLine, want) {
			t.Errorf("docker run line = %q, missing %q", runLine, want)
		}
	}
}

func TestDefaultShapeForHost(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		cpus  int
		memMB int
		want  orchestrator.Shape
	}{
		{"unknown capacity", 0, 0, orchestrator.Shape{CPUs: 2, MemoryMB: 8192, DiskGB: 30}},
		{"small host (4 vCPU / 8 GiB)", 4, 8192, orchestrator.Shape{CPUs: 2, MemoryMB: 8192, DiskGB: 30}},
		{"enough RAM but <32 vCPUs", 31, 65536, orchestrator.Shape{CPUs: 2, MemoryMB: 8192, DiskGB: 30}},
		{"enough vCPUs but <64 GiB RAM", 32, 65535, orchestrator.Shape{CPUs: 2, MemoryMB: 8192, DiskGB: 30}},
		{"threshold large host (32 vCPU / 64 GiB)", 32, 65536, orchestrator.Shape{CPUs: 8, MemoryMB: 16384, DiskGB: 30}},
		{"n2 cloudtop (96 vCPU / 242 GiB)", 96, 247808, orchestrator.Shape{CPUs: 8, MemoryMB: 16384, DiskGB: 30}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := orchestrator.DefaultShapeForHost(tc.cpus, tc.memMB); got != tc.want {
				t.Errorf("DefaultShapeForHost(%d, %d) = %+v, want %+v", tc.cpus, tc.memMB, got, tc.want)
			}
		})
	}
}

func TestGranuleSandboxesVMNameForUsesPrefix(t *testing.T) {
	t.Parallel()
	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{})
	got, err := k.VMNameFor("t1-1")
	if err != nil {
		t.Fatal(err)
	}
	if want := "g-t1-1"; got != want {
		t.Errorf("VMNameFor() = %q, want %q", got, want)
	}
}

// The whole VM name has to fit 11 bytes: netshim derives "tap-<name>" and
// "ctl-<name>" from it, and Linux caps an interface name at 15. Catching
// it here says what the budget is and what is spending it, rather than
// letting `granule vm create` refuse a tap device name several layers
// down.
func TestGranuleSandboxesVMNameForRejectsAnOverLongName(t *testing.T) {
	t.Parallel()
	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{})
	// The prefix is a constant, so the only thing that can overrun the
	// budget now is the run id itself -- a task id that has grown past
	// what maxRunNameLen leaves for it.
	if _, err := k.VMNameFor("1234567-12"); err == nil {
		t.Fatal("expected VMNameFor to refuse a name over the tap-device budget")
	}

	// Exactly at the budget is fine.
	if _, err := k.VMNameFor("123456-12"); err != nil {
		t.Errorf("VMNameFor on an 11-byte name: %v", err)
	}
}

func splitNonEmptyLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '\n' {
			if i > start {
				lines = append(lines, s[start:i])
			}
			start = i + 1
		}
	}
	return lines
}

// fakeGuestConsoleLine is what the fake docker's `logs` answers with: a
// line shaped like what a real guest's early-boot networking unit writes
// to the console, since that output is declared
// StandardOutput=journal+console precisely so it is readable from the
// host when nothing inside the guest can be.
const fakeGuestConsoleLine = "[    3.114] granule-configure-net[214]: no network device appeared within 60s"

// fakeGuestStatsOutput is what health.go's guestStatsCommand prints in a
// real Linux guest: /proc/loadavg, then the whole of /proc/meminfo, then
// `df -Pk /`, then the one line nestedVirtProbe echoes. The fake guest
// answers that command with this instead of running it.
//
// Recorded, not written. Captured 2026-10-01 from the grain-sbx Lima VM
// on the mac this suite also runs on -- Ubuntu 24.04.5, Linux
// 6.8.0-142-generic, aarch64 -- with
// `limactl shell grain-sbx -- sh -c 'cat /proc/loadavg /proc/meminfo; df -Pk /'`
// and the nestedVirtProbe conditional run in the same guest. That
// provenance is the entire value of the fixture: what parseProcStats,
// parseDiskStats and parseNestedVirt can get wrong is the *format* --
// MemAvailable sitting forty rows down among names that also end in a
// colon, `df -Pk`'s six POSIX columns arriving under a header line that
// has six fields of its own, the nested-virt line landing after both at
// a position nothing can count to -- and only real kernel and real
// coreutils text exercises that. Numbers invented to please the parsers
// would prove nothing, so do not edit these in place: replace the block
// with another verbatim capture.
//
// Answered rather than exec'd for the same reason the /proc/net/route
// probe is (writeFakeDockerGuestNetwork): a grain guest is always Linux,
// this fake is impersonating one, and so it owes its caller a Linux
// guest's /proc no matter what the machine running the suite is.
// guestStatsCommand is right to read /proc unconditionally and must not
// be portabilised. Running it for real against the host's filesystem is
// what broke: on a mac there is no /proc, so every health test that
// asserts a reading was parsed failed with "cat: /proc/loadavg: No such
// file or directory" -- a platform split in the test's fake, not in the
// code under test.
//
// Serving the fixture on every platform, rather than falling back to it
// only where /proc is missing, is deliberate: a fallback would leave
// Linux CI exercising a branch no mac ever runs and the mac exercising a
// branch CI never runs, which is precisely the asymmetry that let the
// darwin breakage sit here unnoticed. One path means a regression in it
// shows up wherever the suite runs. What darwin loses by this is only
// that the figures are a fixed snapshot rather than live, so assert that
// a reading was parsed, never on a particular figure.
//
// The guest this came from had no swap configured, so SwapTotal/SwapFree
// are a genuine 0 and parseProcStats' swap figures come out 0/0 here.
// health_internal_test.go covers the swap arithmetic directly.
const fakeGuestStatsOutput = `0.14 0.13 0.19 1/175 926237
MemTotal:        6052120 kB
MemFree:         2379800 kB
MemAvailable:    5520616 kB
Buffers:          101336 kB
Cached:          2828728 kB
SwapCached:            0 kB
Active:           798812 kB
Inactive:        2245976 kB
Active(anon):      61788 kB
Inactive(anon):    61476 kB
Active(file):     737024 kB
Inactive(file):  2184500 kB
Unevictable:       26236 kB
Mlocked:           26236 kB
SwapTotal:             0 kB
SwapFree:              0 kB
Zswap:                 0 kB
Zswapped:              0 kB
Dirty:                 0 kB
Writeback:             0 kB
AnonPages:        140960 kB
Mapped:            85420 kB
Shmem:              1088 kB
KReclaimable:     414492 kB
Slab:             519876 kB
SReclaimable:     414492 kB
SUnreclaim:       105384 kB
KernelStack:        2856 kB
ShadowCallStack:     720 kB
PageTables:         3680 kB
SecPageTables:         0 kB
NFS_Unstable:          0 kB
Bounce:                0 kB
WritebackTmp:          0 kB
CommitLimit:     3026060 kB
Committed_AS:    1044440 kB
VmallocTotal:   133141626880 kB
VmallocUsed:       12296 kB
VmallocChunk:          0 kB
Percpu:             1744 kB
HardwareCorrupted:     0 kB
AnonHugePages:         0 kB
ShmemHugePages:        0 kB
ShmemPmdMapped:        0 kB
FileHugePages:         0 kB
FilePmdMapped:         0 kB
CmaTotal:          32768 kB
CmaFree:            1964 kB
HugePages_Total:       0
HugePages_Free:        0
HugePages_Rsvd:        0
HugePages_Surp:        0
Hugepagesize:       2048 kB
Hugetlb:               0 kB
Filesystem     1024-blocks     Used Available Capacity Mounted on
/dev/vda1         59848952 24160684  35671884      41% /
nested-virt: ready
`

// writeFakeDocker installs a shell script named "docker" on PATH for the
// docker-exec transport: it answers `docker exec` by running execBody,
// answers granule.DockerContainerStatus's own
// "docker inspect -f {{.State.Status}}" with status, and *fails* every
// other inspect -- notably any asking for NetworkSettings, which is how a
// container address lookup is spelled.
//
// That last part is the point: nothing should ever look a VM's container
// address up, so a test whose fake cannot answer that lookup proves the
// lookup never happened rather than merely asserting its absence from a
// log. The same goes for the guest's sshd -- these
// tests deliberately never listenTCP, since there is no port for anything
// out here to dial.
//
// `docker logs` is answered with one canned line
// (fakeGuestConsoleLine), standing in for the VM container's stdout --
// which for a real granule VM is the guest's serial console, and the
// half of a failure diagnosis that is read from out here rather than
// from inside a guest that may not be answering at all.
func writeFakeDocker(t *testing.T, argvLog, status, execBody string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake docker script is POSIX shell only")
	}
	dir := t.TempDir()
	guestBin := fakeGuestBin(t)
	script := fmt.Sprintf(`#!/bin/sh
[ "$1" = --grain-fake-docker-warmup ] && exit 0
echo "$*" >> %[1]q
case "$1" in
exec)
  PATH=%[6]q:$PATH; export PATH
  %[2]s
  ;;
logs)
  echo %[3]q
  ;;
network)
  # ensureNetwork's two verbs. "inspect" fails the first time and succeeds
  # once "create" has run, which is how a real docker behaves and is what
  # makes the create path actually get exercised.
  case "$2" in
  # The marker doubles as the network's recorded subnet, so that a test can
  # seed it with a different one and exercise ensureNetwork's mismatch
  # refusal -- which is what a network left behind by an earlier deployment
  # looks like. An inspect with -f prints it, the way docker's own template
  # output does; without -f it only has to succeed.
  inspect)
    [ -f %[5]q ] || exit 1
    [ "$3" = "-f" ] && cat %[5]q
    ;;
  create)
    subnet=""
    while [ $# -gt 0 ]; do
      [ "$1" = "--subnet" ] && { subnet="$2"; break; }
      shift
    done
    printf '%%s' "$subnet" > %[5]q
    ;;
  *) echo "fake docker: unexpected network subcommand: $*" >&2; exit 1 ;;
  esac
  ;;
ps|rm|run|image)
  # SweepOrphans' container listing and the stateless removal: no VM this
  # fake granule creates ever has a real container, so there is nothing
  # to list and nothing to remove; real granule's "image inspect" and
  # "run" succeed and record their argv in the log above.
  ;;
inspect)
  case "$*" in
  *State.Status*) echo %[4]q ;;
  *) echo "fake docker: unexpected inspect: $*" >&2; exit 1 ;;
  esac
  ;;
*)
  echo "fake docker: unexpected subcommand: $*" >&2
  exit 1
  ;;
esac
`, argvLog, execBody, fakeGuestConsoleLine, status, filepath.Join(dir, "network-created"), guestBin)
	path := filepath.Join(dir, "docker")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	warmUpFakeDocker(t, path)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// fakeGuestBin is a directory holding a `sudo` that refuses everything,
// for a fake guest's commands to find ahead of the real one.
//
// A fake guest runs what it is handed on the machine running the suite,
// and some of what an Acquire hands a guest is root maintenance of that
// guest: guestDockerNetworkRepair rewrites /etc/docker/daemon.json and
// restarts dockerd, guestKVMGrant runs usermod, guestNetworkNoneCommand
// deletes the default route. Every one of them goes through `sudo -n`, so
// on a host where that succeeds -- every grain sandbox and CI runner --
// the suite did all of it to the host itself: one docker restart per
// granule test, until systemd's start limit left docker down for the
// rest of the job (docs/test-runs/2026-10-01-sandbox-isolation-port-block.md).
// Refused here, each of them takes the path a guest without sudo takes.
func fakeGuestBin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	sudo := "#!/bin/sh\necho \"fake guest: sudo is refused, so a guest's root maintenance never runs on the test host: $*\" >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte(sudo), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// warmUpFakeDocker runs the script once with nothing to do -- the
// --grain-fake-docker-warmup guard on its first line returns before it
// logs an invocation or touches any of its state files -- so that
// whatever the operating system charges for the *first* execution of a
// file it has never seen is paid out here, during setup, rather than
// inside the window a test is measuring.
//
// The guard is a sentinel argument rather than an environment variable
// on purpose. mcp.DockerExecRunner runs `docker` without setting Env
// (it is one of the entries in .grain/baselines/exec-env-implicit.txt),
// so the fake inherits whatever this machine exports -- and a variable
// exported in a developer's shell would make every fake `docker` return
// 0 before logging anything, which is not a visible failure but a suite
// that quietly stops testing. No real docker argv begins with this, and
// nothing a shell exports can reach $1.
//
// macOS charges about a fifth of a second for it: the first exec of a
// newly written executable goes through the system policy check, and
// every exec of that same file afterwards is free. A Linux host charges
// nothing, which is why this went unnoticed.
//
// A fifth of a second is longer than the deadline some of these tests
// deliberately give a health sweep.
// TestGranuleSandboxesHealthDoesNotReprobeAGuestThatJustFailed gives one
// 100ms and then asserts the sweep reached the guest at all, which is
// what makes the *next* sweep costing nothing mean something -- and on a
// mac the very first `docker` launch of the fake it had just installed
// was SIGKILLed on that deadline before the script's first line ran. The
// test saw "exited -1 with no output" and an argv log that did not exist:
// a probe that looked as though it had never been sent, from a fake whose
// own installation cost had eaten the whole budget.
//
// Raising those deadlines instead would be the wrong fix twice over: it
// would make the suite slower everywhere to accommodate a cost that is
// not the code under test's, and it would leave the cost sitting inside
// the measured window, where a slower machine can always make it bigger
// again.
func warmUpFakeDocker(t *testing.T, path string) {
	t.Helper()
	cmd := exec.Command(path, "--grain-fake-docker-warmup")
	if err := cmd.Run(); err != nil {
		t.Fatalf("warming up the fake docker at %s: %v", path, err)
	}
}

// writeFakeDockerGuest installs a fake "docker" whose `exec` runs the
// command it was handed, in homeDir -- the docker-exec counterpart to the
// fake `ssh` these tests used to install, and the same trick: everything
// after "granule exec --" is a real argv (see mcp.DockerExecRunner.Run on
// why it is not shell-quoted), so the fake can exec it directly rather
// than re-parsing anything. homeDir stands in for the guest's own
// filesystem, which is what lets a test assert on files a tool call left
// behind; "" means succeed without running anything.
//
// readyAfter models a guest that is still booting: the first readyAfter
// exec calls fail the way `granule exec` does when the guest is not
// answering yet (its own "granule: exec: " log prefix, which
// DockerExecRunner reads as "the command never ran"), and every call
// after that succeeds. counterFile is where the count lives, so it
// survives across the separate processes each exec is.
func writeFakeDockerGuest(t *testing.T, argvLog, counterFile string, readyAfter int, homeDir string) {
	t.Helper()
	writeFakeDockerGuestNetwork(t, argvLog, counterFile, readyAfter, homeDir, 0)
}

// writeFakeDockerGuestNetwork is writeFakeDockerGuest with the guest's
// *network* modelled separately from its agent, which is the distinction
// waitForGuestReady exists for: the two come up at different instants in
// a real guest, the agent first, and readiness needs both.
//
// netReadyAfter is how many readiness probes fail before the guest has a
// default route. The probe is recognized by the file it reads
// (guestReadyProbe's /proc/net/route) and answered here rather than run,
// which also keeps these tests off the *host's* routing table -- a fake
// that exec'd the probe for real would be asserting that whatever
// machine runs the suite has a default route.
//
// The diagnostic dump waitForGuestReady asks a routeless guest for on
// its way out is answered here too, and for the same reason: it is
// recognized by its own marker (guestNetworkDump's leading
// "granule-guest-dump") and answered with one canned line, so a test can
// assert the answer reaches the error without any of it depending on
// what the machine running the suite has in its own journal.
//
// The health pane's stats command (health.go's guestStatsCommand,
// recognized by the /proc/loadavg it reads) is answered the same way,
// from fakeGuestStatsOutput -- see that fixture for why a fake guest has
// to supply its own /proc rather than let the host's answer for it.
func writeFakeDockerGuestNetwork(t *testing.T, argvLog, counterFile string, readyAfter int, homeDir string, netReadyAfter int) {
	t.Helper()
	run := "exit 0"
	if homeDir != "" {
		run = fmt.Sprintf(`cd %q && exec "$@"`, homeDir)
	}
	statsFile := filepath.Join(t.TempDir(), "guest-stats")
	if err := os.WriteFile(statsFile, []byte(fakeGuestStatsOutput), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFakeDocker(t, argvLog, "running", fmt.Sprintf(`
  n=0
  [ -f %[1]q ] && n=$(cat %[1]q)
  n=$((n+1))
  echo "$n" > %[1]q
  if [ "$n" -le %[2]d ]; then
    echo "granule: exec: dialing guest: connection refused" >&2
    exit 1
  fi
  shift
  while [ $# -gt 0 ] && [ "$1" != "--" ]; do shift; done
  shift
  case "$*" in
  *granule-guest-dump*)
    echo "## failed units"
    echo "granule-net-cmdline.service loaded failed failed Configure networking from the ip= kernel command line"
    exit 0
    ;;
  */proc/net/route*)
    m=0
    [ -f %[4]q ] && m=$(cat %[4]q)
    m=$((m+1))
    echo "$m" > %[4]q
    if [ "$m" -le %[5]d ]; then
      exit 1
    fi
    exit 0
    ;;
  */proc/loadavg*)
    cat %[6]q
    exit 0
    ;;
  esac
  %[3]s
`, counterFile, readyAfter, run, counterFile+".net", netReadyAfter, statsFile))
}

// writeFakeDockerGuestRoutelessUntilRebuilt installs a fake docker whose
// guest answers from the first probe and only has a default route once
// its VM has been created a *second* time -- the failure Acquire's
// rebuild exists for, driven from the one thing that really distinguishes
// the two boots rather than from a probe count or a wall clock.
//
// It reads that from the fake granule's own argv log (writeFakeGranule
// logs one line per invocation), which the two fakes share by being
// handed the same path. Everything else it answers exactly as
// writeFakeDockerGuestNetwork does, the guest dump included: the first
// attempt has to fail the way a real routeless guest fails, dump and all,
// for the rebuild it triggers to mean anything.
func writeFakeDockerGuestRoutelessUntilRebuilt(t *testing.T, argvLog, granuleArgvLog string) {
	t.Helper()
	writeFakeDocker(t, argvLog, "running", fmt.Sprintf(`
  shift
  while [ $# -gt 0 ] && [ "$1" != "--" ]; do shift; done
  shift
  case "$*" in
  *granule-guest-dump*)
    echo "## failed units"
    echo "granule-net-cmdline.service loaded failed failed Configure networking from the ip= kernel command line"
    exit 0
    ;;
  */proc/net/route*)
    creates=$(grep -c "^vm create" %[1]q 2>/dev/null || true)
    [ "${creates:-0}" -ge 2 ]
    exit $?
    ;;
  esac
  exit 0
`, granuleArgvLog))
}

// granuleCreateCount is how many VMs the fake granule was asked to
// create over a test -- i.e. how many boots an Acquire paid for.
func granuleCreateCount(t *testing.T, granuleArgvLog string) int {
	t.Helper()
	data, err := os.ReadFile(granuleArgvLog)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, line := range splitNonEmptyLines(string(data)) {
		if strings.HasPrefix(line, "vm create ") {
			n++
		}
	}
	return n
}

// stateDirLabelArgs is the label every create puts on its VM container
// (orchestrator.StateDirLabel), as it lands at the end of granule's argv.
func stateDirLabelArgs(stateDir string) string {
	return " -docker-run-opt --label -docker-run-opt " + orchestrator.StateDirLabel + "=" + stateDir
}

func granuleTestConfig(stateDir string) orchestrator.GranuleConfig {
	return orchestrator.GranuleConfig{
		StateDir:          stateDir,
		SSHUser:           "debian",
		ExecKeyPath:       "/images/granule_id_ed25519",
		Workspace:         "/workspace",
		ReadyPollInterval: time.Millisecond,
		ReadyTimeout:      5 * time.Second,
	}
}

// Under DockerExec, Acquire has to reach the guest without resolving any
// address for it at all: no external port out of granule's state file, no
// container IP out of `docker inspect`, and no TCP dial to confirm a port
// is answering. The fake docker here cannot answer an address lookup and
// nothing is listening anywhere, so getting tools back at all is what
// proves none of that was consulted.
func TestGranuleSandboxesDockerExecReachesTheGuestWithoutResolvingAnAddress(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30090)
	dockerLog := filepath.Join(t.TempDir(), "docker-argv.log")
	writeFakeDocker(t, dockerLog, "running", "exit 0")

	k := orchestrator.NewGranuleSandboxes(granuleTestConfig(stateDir))

	sb, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := sb.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 4 {
		t.Fatalf("Tools() returned %d tools, want the same 4 the SSH path returns", len(tools))
	}

	argv, err := os.ReadFile(dockerLog)
	if err != nil {
		t.Fatalf("docker was never invoked at all: %v", err)
	}
	if !strings.Contains(string(argv), "exec ") {
		t.Errorf("docker invoked with %q, want a `docker exec` among the calls", argv)
	}
	if strings.Contains(string(argv), "NetworkSettings") {
		t.Errorf("docker invoked with %q, want no container-address lookup under DockerExec", argv)
	}
	if strings.Contains(string(argv), "-netns") {
		t.Errorf("docker invoked with %q, want it to exec into the VM container, never the netns holder", argv)
	}
}

// A tool call has to arrive in the guest through
// `docker exec <vm container> granule exec --`, carrying the guest account
// and the in-container key path -- the same guest, account and key the
// SSH path uses, reached from inside the VM's own container instead.
func TestGranuleSandboxesDockerExecRunsToolCallsThroughTheVMContainer(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30091)
	dockerLog := filepath.Join(t.TempDir(), "docker-argv.log")
	writeFakeDocker(t, dockerLog, "running", `echo "hello from the guest"`)

	k := orchestrator.NewGranuleSandboxes(granuleTestConfig(stateDir))
	sb, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := sb.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var runCommand *mcp.Tool
	for i := range tools {
		if tools[i].Name == "run_command" {
			runCommand = &tools[i]
		}
	}
	if runCommand == nil {
		t.Fatal("Tools() returned no run_command tool")
	}
	result := runCommand.Handler(context.Background(), map[string]any{"command": "echo hi"})
	if result.IsError {
		t.Fatalf("run_command reported an error: %+v", result)
	}

	argv, err := os.ReadFile(dockerLog)
	if err != nil {
		t.Fatal(err)
	}
	var execLine string
	for _, line := range strings.Split(string(argv), "\n") {
		if strings.HasPrefix(line, "exec ") && strings.Contains(line, "granule exec --") {
			execLine = line
		}
	}
	if execLine == "" {
		t.Fatalf("docker invoked with %q, want a `docker exec ... granule exec --` call", argv)
	}
	for _, want := range []string{
		"GRANULE_EXEC_USER=debian",
		"GRANULE_EXEC_KEY=/images/granule_id_ed25519",
		"granule-vm-g-t1-1 granule exec --",
	} {
		if !strings.Contains(execLine, want) {
			t.Errorf("docker exec line = %q, want it to carry %q", execLine, want)
		}
	}
}

// Acquire has to fast-fail on a dead VM container rather than wait:
// exec'ing into a container that has already exited will never start
// answering, so waiting out the full ReadyTimeout finding that out is
// just a slower way to fail.
func TestGranuleSandboxesDockerExecFastFailsWhenTheVMContainerExitsEarly(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30092)
	writeFakeDocker(t, filepath.Join(t.TempDir(), "docker-argv.log"), "exited",
		`echo "Error response from daemon: container is not running" >&2; exit 1`)

	cfg := granuleTestConfig(stateDir)
	cfg.ReadyPollInterval = 10 * time.Millisecond
	cfg.ReadyTimeout = 10 * time.Second
	k := orchestrator.NewGranuleSandboxes(cfg)

	started := time.Now()
	_, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{})
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("Acquire() against a VM container that already exited: got nil error, want one")
	}
	if elapsed > 2*time.Second {
		t.Errorf("Acquire() took %s to fail, want well under the 10s ReadyTimeout", elapsed)
	}
	if !strings.Contains(err.Error(), "exited") {
		t.Errorf("error = %q, want it to mention the container's \"exited\" status", err)
	}
	if !strings.Contains(err.Error(), "granule-vm-g-t1-1") {
		t.Errorf("error = %q, want it to name the container", err)
	}
}

// TestGranuleSandboxesFlatModeOmitsAddressing covers the default mode:
// docker assigns the guest's address, so no -ip is derived or passed --
// granule rejects one outright under flat mode -- and no -port either,
// since nothing forwards one. IP/Port are set here anyway, as a
// deployment's own systemd unit may still carry them from before the
// switch, to confirm they are ignored rather than fatal.
func TestGranuleSandboxesFlatModeOmitsAddressing(t *testing.T) {
	stateDir := t.TempDir()
	argvLog := filepath.Join(t.TempDir(), "granule-argv.log")
	writeFakeGranule(t, argvLog, 30080)
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 0, "")

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:          stateDir,
		SSHUser:           "debian",
		ExecKeyPath:       "/images/key",
		Workspace:         "/workspace",
		IP:                "169.254.100.2",
		Port:              30080,
		ReadyPollInterval: time.Millisecond,
	})

	for _, sandbox := range []string{"t1-1", "t2-1"} {
		if _, err := k.Acquire(context.Background(), sandbox, orchestrator.Shape{}); err != nil {
			t.Fatal(err)
		}
	}

	data, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"vm create g-t1-1 -state-dir " + stateDir + " -backend docker" + defaultShapeArgs + stateDirLabelArgs(stateDir),
		"vm create g-t2-1 -state-dir " + stateDir + " -backend docker" + defaultShapeArgs + stateDirLabelArgs(stateDir),
	}
	got := splitNonEmptyLines(string(data))
	if len(got) != len(want) {
		t.Fatalf("granule invocations = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("invocation %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestGranuleSandboxesNamesNoNetModeAtAll pins the other half of the
// deprecation: grain does not tell granule which network mode to use,
// because granule has only one left.
//
// This used to assert the opposite -- that an unset NetMode reached
// granule as "-net flat", since flat was what a deployment got by saying
// nothing. The flag it was asserting is now "deprecated: only the former
// flat behaviour exists now, and it is the default", so naming it said
// nothing granule did not already assume. What is worth pinning is that
// nothing names it, so a later change does not reintroduce a flag that
// describes a choice there is no longer any way to make.
func TestGranuleSandboxesNamesNoNetModeAtAll(t *testing.T) {
	stateDir := t.TempDir()
	argvLog := filepath.Join(t.TempDir(), "granule-argv.log")
	writeFakeGranule(t, argvLog, 30080)
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 0, "")

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:          stateDir,
		SSHUser:           "debian",
		ExecKeyPath:       "/images/key",
		Workspace:         "/workspace",
		ReadyPollInterval: time.Millisecond,
	})
	if _, err := k.Acquire(context.Background(), "1", orchestrator.Shape{}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "-net") {
		t.Errorf("granule invocation = %q, want no -net at all: granule has only the former flat "+
			"behaviour left, so there is nothing for grain to select", string(data))
	}
}

// The VM-name budget is now settled entirely in code -- a constant prefix
// and a constant cap -- so what is worth pinning is not that a check
// rejects a bad prefix but that the constants leave room for the run ids a
// real deployment actually produces. Task ids are a monotonically
// increasing counter (Store.NewTaskID), so this is the ceiling a
// long-lived deployment climbs toward: the two halves together get nine
// bytes, one of which the "-" spends, so eight digits of task id and
// attempt combined. Dropping the "r" dispatch.RunID used to carry bought
// exactly one of those digits.
func TestVMNameBudgetCoversRealisticRunIDs(t *testing.T) {
	t.Parallel()
	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{})
	for _, tc := range []struct {
		taskID  string
		attempt int
		want    bool
		why     string
	}{
		{"1", 1, true, "the very first run a deployment ever makes"},
		{"99999", 99, true, "a five-digit task id on its 99th attempt"},
		{"999999", 99, true, "a six-digit task id on its 99th attempt, exactly at the budget"},
		{"9999999", 99, false, "a seven-digit task id on its 99th attempt is one byte over"},
		{"99999999", 1, false, "an eight-digit task id is over even on its first attempt"},
	} {
		name := dispatch.RunID(tc.taskID, tc.attempt)
		_, err := k.VMNameFor(name)
		if got := err == nil; got != tc.want {
			t.Errorf("VMNameFor(%q) fits = %v, want %v -- %s (err: %v)", name, got, tc.want, tc.why, err)
		}
	}
}

// A cancelled Acquire still tears its half-built VM down. granule.Delete
// execs granule through exec.CommandContext, so cleanup that rode on
// the caller's own ctx did not merely fail on an already-cancelled one --
// it never ran, and the VM was left behind with nothing holding a handle
// to it until the next startup's ReapOrphans.
//
// This is the ordinary interruption, not a rare one: runOne's ctx is
// cancelled whenever the daemon is shutting down or the task was closed
// mid-run, which is exactly why granuleSandbox.Release already detaches.
func TestGranuleSandboxesAcquireDeletesTheVMWhenTheContextIsCancelled(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30080)
	// A guest that never answers, so Acquire is still waiting when ctx dies.
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"),
		filepath.Join(t.TempDir(), "counter"), 1<<30, "")

	cfg := granuleTestConfig(stateDir)
	cfg.ReadyTimeout = 30 * time.Second
	cfg.ReadyPollInterval = time.Millisecond
	k := orchestrator.NewGranuleSandboxes(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	if _, err := k.Acquire(ctx, "t1-1", orchestrator.Shape{}); err == nil {
		t.Fatal("expected Acquire to fail once its context was cancelled")
	}
	if granule.Exists(stateDir, "g-t1-1") {
		t.Error("the VM is still there after a cancelled Acquire -- cleanup has to run on a " +
			"context detached from the caller's, or `granule vm delete` never executes at all")
	}
}

// The same holds when the create itself fails: granule brings a VM's
// netns holder and its own container up in separate steps, so a failure
// between them leaves a half-built VM that no Release will ever reach.
func TestGranuleSandboxesAcquireDeletesTheVMWhenCreateFails(t *testing.T) {
	stateDir := t.TempDir()
	dir := t.TempDir()
	// A granule that writes the state file and *then* fails the create,
	// standing in for a create that got partway and gave up.
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "vm" ] && [ "$2" = "create" ]; then
  echo '{"port": 1}' > %q/"$3".json
  echo "boom" >&2
  exit 1
fi
if [ "$1" = "vm" ] && [ "$2" = "delete" ]; then
  rm -f %q/"$3".json
fi
`, stateDir, stateDir)
	path := filepath.Join(dir, "granule")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	k := orchestrator.NewGranuleSandboxes(granuleTestConfig(stateDir))
	if _, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{}); err == nil {
		t.Fatal("expected Acquire to fail when create does")
	}
	if granule.Exists(stateDir, "g-t1-1") {
		t.Error("a failed create left its VM behind")
	}
}

// SweepOrphans is the same rule applied while the daemon is up, which is
// what stands behind the disk floor (grain/task-213): a VM nothing is
// using is 60-80 GB of CI overlay held by nobody, and reclaiming it
// should not need a restart. What is in use is left where it is.
func TestGranuleSandboxesSweepOrphansSparesALiveSandbox(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30080)
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 0, "")

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir: stateDir, SSHUser: "debian",
		ExecKeyPath: "/images/key", Workspace: "/workspace",
	})
	live, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	// A VM a dead process left behind, and one belonging to something
	// that is not this deployment at all.
	for _, name := range []string{"g-t9-1", "other-vm"} {
		if err := os.WriteFile(filepath.Join(stateDir, name+".json"), []byte(`{"port": 30080}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	swept, err := k.SweepOrphans(context.Background())
	if err != nil {
		t.Fatalf("SweepOrphans: %v", err)
	}
	if swept != 1 {
		t.Errorf("swept = %d, want only the orphan", swept)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "g-t1-1.json")); err != nil {
		t.Error("a live run's VM was deleted by a sweep, which costs the run rather than reclaiming anything")
	}
	if _, err := os.Stat(filepath.Join(stateDir, "g-t9-1.json")); err == nil {
		t.Error("the orphaned VM survived the sweep")
	}
	if _, err := os.Stat(filepath.Join(stateDir, "other-vm.json")); err != nil {
		t.Error("a VM under another prefix was swept, want it left alone")
	}

	// And once it is released it is the sweep's, like any other leftover.
	if err := live.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if swept, err := k.SweepOrphans(context.Background()); err != nil || swept != 0 {
		t.Errorf("sweeping after a clean release = %d, %v; want nothing left to do", swept, err)
	}
}

// The window this sweep has that the startup reap does not: a VM exists
// from the moment granule creates it and only becomes live once its
// guest has answered, minutes later on a deployment building a guest
// inline. A sweep that ran in between would delete the VM of a run that
// was still starting.
func TestGranuleSandboxesSweepOrphansSparesAVMThatIsStillBooting(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30080)
	// A guest that answers only on the fourth probe, so the Acquire below
	// is still inside waitForGuestReady while the sweep runs.
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 3, "")

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir: stateDir, SSHUser: "debian",
		ExecKeyPath: "/images/key", Workspace: "/workspace",
		ReadyPollInterval: 50 * time.Millisecond,
		ReadyTimeout:      10 * time.Second,
	})

	acquired := make(chan error, 1)
	go func() {
		sb, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{})
		if err == nil {
			err = sb.Release(context.Background())
		}
		acquired <- err
	}()

	// The state file appears when granule creates the VM, which is
	// after the name has been claimed and long before the guest answers.
	vmState := filepath.Join(stateDir, "g-t1-1.json")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(vmState); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the VM was never created")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if swept, err := k.SweepOrphans(context.Background()); err != nil || swept != 0 {
		t.Errorf("sweeping mid-Acquire = %d, %v; want a booting VM left alone", swept, err)
	}
	if err := <-acquired; err != nil {
		t.Fatalf("the Acquire a sweep ran alongside failed: %v", err)
	}
}

// TestGranuleSandboxesNamesNoAddressWithoutAPool is the default every
// deployment has run under: grain names no address and docker assigns one,
// so nothing about the create changes until a pool is configured.
func TestGranuleSandboxesNamesNoAddressWithoutAPool(t *testing.T) {
	stateDir := t.TempDir()
	argvLog := filepath.Join(t.TempDir(), "granule-argv.log")
	writeFakeGranule(t, argvLog, 30080)
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 0, "")

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir: stateDir, SSHUser: "debian", ExecKeyPath: "/images/key",
		Workspace: "/workspace", ReadyPollInterval: time.Millisecond,
	})
	if _, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	// The state-directory label (stateDirLabelArgs) is on every create and
	// is not addressing; nothing else may ride on -docker-run-opt.
	if got, want := strings.Count(string(data), "-docker-run-opt"), 2; got != want {
		t.Errorf("granule invocation = %q, want only the state-directory label's %d -docker-run-opt", string(data), want)
	}
	for _, unwanted := range []string{"--ip", "--network"} {
		if strings.Contains(string(data), unwanted) {
			t.Errorf("granule invocation = %q, want no %s with no pool configured", string(data), unwanted)
		}
	}
}

// TestGranuleSandboxesGivesEachVMItsAllocatedAddress: with a pool, the
// address reaches the VM as docker run options, because granule's own -ip
// is deprecated and ignored and the container runtime is what assigns the
// address a flat-mode guest takes over.
func TestGranuleSandboxesGivesEachVMItsAllocatedAddress(t *testing.T) {
	stateDir := t.TempDir()
	argvLog := filepath.Join(t.TempDir(), "granule-argv.log")
	writeFakeGranule(t, argvLog, 30080)
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 0, "")

	pool, err := sandboxaddr.NewPool("10.128.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir: stateDir, SSHUser: "debian", ExecKeyPath: "/images/key",
		Workspace: "/workspace", ReadyPollInterval: time.Millisecond,
		Addresses: pool, DockerNetwork: "grain-sandboxes",
	})
	if _, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{}); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Acquire(context.Background(), "t2-1", orchestrator.Shape{}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	// Each option and each value is its own -docker-run-opt: granule's own
	// convention, so no quoting rule has to be invented.
	first, ok := pool.Addr("t1-1")
	if !ok {
		t.Fatal("t1-1 has no address")
	}
	second, ok := pool.Addr("t2-1")
	if !ok {
		t.Fatal("t2-1 has no address")
	}
	if first == second {
		t.Fatalf("both sandboxes were given %s", first)
	}
	for _, want := range []string{
		"-docker-run-opt --network -docker-run-opt grain-sandboxes -docker-run-opt --ip -docker-run-opt " + first.String(),
		"-docker-run-opt --network -docker-run-opt grain-sandboxes -docker-run-opt --ip -docker-run-opt " + second.String(),
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("granule invocations = %q, want one carrying %q", string(data), want)
		}
	}
	// Two sandboxes, two families: the second is not inside the first's
	// block, so a rule written for one never covers the other.
	firstFamily, _ := pool.FamilyPrefix("t1-1")
	if firstFamily.Contains(second) {
		t.Errorf("t2-1 at %s fell inside t1-1's family %s", second, firstFamily)
	}
}

// TestReleasingAGranuleSandboxGivesBackItsAddress: an address outliving
// its sandbox is a block the deployment can never hand out again.
func TestReleasingAGranuleSandboxGivesBackItsAddress(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30080)
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 0, "")

	pool, err := sandboxaddr.NewPool("10.128.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir: stateDir, SSHUser: "debian", ExecKeyPath: "/images/key",
		Workspace: "/workspace", ReadyPollInterval: time.Millisecond,
		Addresses: pool, DockerNetwork: "grain-sandboxes",
	})
	sb, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pool.Addr("t1-1"); !ok {
		t.Fatal("no address after Acquire")
	}
	if err := sb.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if addr, ok := pool.Addr("t1-1"); ok {
		t.Errorf("t1-1 still holds %s after Release", addr)
	}
}

// TestAGranuleAcquireRecreatesARemovedSandboxNetwork: `docker network rm`
// of grain's sandbox network between two dispatches. The second dispatch's
// create is refused by docker ("network ... not found"), and the acquire
// has to bring the network back and build the VM again rather than fail.
// Against real VMs the first acquire after the removal used to fail and
// only the one after it recreated the network
// (docs/test-runs/2026-10-02-sandbox-backends-addressing.md).
func TestAGranuleAcquireRecreatesARemovedSandboxNetwork(t *testing.T) {
	stateDir := t.TempDir()
	granuleLog := filepath.Join(t.TempDir(), "granule-argv.log")
	writeFakeGranule(t, granuleLog, 30080)
	writeFakeDockerGuest(t, filepath.Join(t.TempDir(), "docker-argv.log"), filepath.Join(t.TempDir(), "counter"), 0, "")
	fakeGranule, err := exec.LookPath("granule")
	if err != nil {
		t.Fatal(err)
	}
	fakeDocker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	// writeFakeDocker's network marker: present once a create has run.
	marker := filepath.Join(filepath.Dir(fakeDocker), "network-created")
	// A granule whose create fails the way docker run does on a missing
	// network, and otherwise is the fake above.
	dir := t.TempDir()
	writeGuestStub(t, dir, "granule", fmt.Sprintf("#!/bin/sh\n"+
		"if [ \"$1 $2\" = 'vm create' ] && [ ! -f %q ]; then\n"+
		"  echo \"$*\" >> %q\n"+
		"  echo 'docker: Error response from daemon: network grain-sandboxes not found' >&2\n"+
		"  exit 1\n"+
		"fi\n"+
		"exec %q \"$@\"\n", marker, granuleLog, fakeGranule))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	pool, err := sandboxaddr.NewPool("10.128.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir: stateDir, SSHUser: "debian", ExecKeyPath: "/images/key",
		Workspace: "/workspace", ReadyPollInterval: time.Millisecond,
		Addresses: pool, DockerNetwork: "grain-sandboxes",
	})
	first, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	// docker network rm, behind the daemon's back.
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	second, err := k.Acquire(context.Background(), "t2-1", orchestrator.Shape{})
	if err != nil {
		t.Fatalf("the first Acquire after the sandbox network was removed failed: %v", err)
	}
	defer second.Release(context.Background())
	if _, err := os.Stat(marker); err != nil {
		t.Error("the sandbox network was not created again")
	}
	if got := granuleCreateCount(t, granuleLog); got != 3 {
		t.Errorf("granule vm create ran %d times, want 3: one for t1-1, the refused one for t2-1 and its retry", got)
	}
}

// TestAFailedAcquireDoesNotLeakAnAddress: an Acquire that never returned a
// sandbox has nothing to release it later, so the address has to come back
// on the way out.
func TestAFailedAcquireDoesNotLeakAnAddress(t *testing.T) {
	stateDir := t.TempDir()
	// A granule that refuses to create: Acquire fails before any guest.
	dir := t.TempDir()
	script := filepath.Join(dir, "granule")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'create refused' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	pool, err := sandboxaddr.NewPool("10.128.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir: stateDir, SSHUser: "debian", ExecKeyPath: "/images/key",
		Workspace: "/workspace", ReadyPollInterval: time.Millisecond,
		Addresses: pool, DockerNetwork: "grain-sandboxes",
	})
	if _, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{}); err == nil {
		t.Fatal("Acquire succeeded against a granule that refuses to create")
	}
	if addr, ok := pool.Addr("t1-1"); ok {
		t.Errorf("a failed Acquire left t1-1 holding %s", addr)
	}
	// And the block is available again, which is what "not leaked" means.
	if _, err := pool.AcquireRoot("t1-1"); err != nil {
		t.Errorf("the name is still taken after a failed Acquire: %v", err)
	}
}

// A docker network whose subnet is not the one this deployment hands out
// addresses from is refused, naming both.
//
// This is what a network left behind by an earlier deployment looks like,
// and before ensureNetwork checked it the failure arrived from docker, once
// per acquire, as
//
//	invalid endpoint settings: no configured subnet contains IP address 10.199.0.96
//
// which names neither the network's subnet nor grain's, and is followed by
// a dispatch backoff that makes it read as a scheduling problem. Measured
// on a real deployment: a network created the previous day with
// 10.128.0.0/16 under a pool of 10.199.0.0/16 failed every acquire until
// somebody inspected the network by hand.
func TestGranuleSandboxesRefuseANetworkWhoseSubnetIsNotTheirs(t *testing.T) {
	const (
		poolCIDR    = "10.199.0.0/16"
		networkCIDR = "10.128.0.0/16"
	)
	// A fake docker of this test's own rather than writeFakeDocker's: what
	// is under test is the answer to `network inspect`, which that one
	// derives from whether a create has run.
	dir := t.TempDir()
	writeGuestStub(t, dir, "docker", "#!/bin/sh\n"+
		"case \"$1 $2\" in\n"+
		"'network inspect')\n"+
		"  [ \"$3\" = -f ] && printf '%s' "+shellSingleQuoted(networkCIDR)+"\n"+
		"  exit 0\n"+
		"  ;;\n"+
		"esac\n"+
		"echo \"fake docker: unexpected: $*\" >&2\n"+
		"exit 1\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	pool, err := sandboxaddr.NewPool(poolCIDR)
	if err != nil {
		t.Fatal(err)
	}
	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:      t.TempDir(),
		Addresses:     pool,
		DockerNetwork: "grain-sandboxes",
		SSHUser:       "debian",
		Workspace:     "/home/debian",
	})

	_, err = k.Acquire(context.Background(), "t1-1", orchestrator.Shape{})
	if err == nil {
		t.Fatal("Acquire succeeded against a network whose subnet is not the pool's")
	}
	// Both numbers, because the operator has to decide which one is wrong:
	// an error naming only one leaves them inspecting docker by hand, which
	// is the whole cost this is meant to remove.
	for _, want := range []string{networkCIDR, poolCIDR, "grain-sandboxes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}
