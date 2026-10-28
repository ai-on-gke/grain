package orchestrator_test

// What model.NetworkAccess actually does to a sandbox (grain/task-269).
//
// The promise is narrow and worth stating: a run whose task asked for
// model.NetworkNone is handed a sandbox with no default route, so it
// reaches this deployment's own services (the git proxy every checkout
// clones through, on a segment the guest is directly connected to) and
// nothing else -- and if that cannot be delivered, it is handed no
// sandbox at all. These are the tests for both halves, plus the backend
// that can deliver neither and says so.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// guestNetworkFake is a fake `docker` whose guest answers every command,
// logs the ones that touch its routing table to routeLog, and fails the
// route deletion when refuse is true -- which is how a guest that cannot
// be cut off the network is modelled here.
func guestNetworkFake(t *testing.T, argvLog, routeLog string, refuse bool) {
	t.Helper()
	body := `
  shift
  while [ $# -gt 0 ] && [ "$1" != "--" ]; do shift; done
  shift
  case "$*" in
  *"route del default"*)
    echo "drop: $*" >> ` + routeLog + `
    ` + refusal(refuse) + `
    exit 0
    ;;
  *"route get"*)
    echo "get: $*" >> ` + routeLog + `
    exit 0
    ;;
  esac
  exit 0
`
	writeFakeDocker(t, argvLog, "running", body)
}

func refusal(refuse bool) string {
	if !refuse {
		return ":"
	}
	return `echo "RTNETLINK answers: Operation not permitted" >&2; exit 1`
}

// routeLog reads back what the fake guest was asked to do to its routing
// table. Whole file rather than lines: the commands are shell scripts,
// so one of them logs as a dozen lines and nothing useful is per-line.
func routeLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func networkTestConfig(stateDir string) orchestrator.GranuleConfig {
	cfg := granuleTestConfig(stateDir)
	cfg.GitProxyHost = "172.17.0.1"
	return cfg
}

// A task asking for no network gets a sandbox whose default route has
// been taken away -- and the git proxy is checked afterwards, because a
// sandbox that cannot clone is not the thing anybody asked for.
func TestGranuleSandboxesDropsTheDefaultRouteForASandboxWithNoNetwork(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30080)
	routes := filepath.Join(t.TempDir(), "routes.log")
	guestNetworkFake(t, filepath.Join(t.TempDir(), "docker-argv.log"), routes, false)

	k := orchestrator.NewGranuleSandboxes(networkTestConfig(stateDir))
	sb, err := k.Acquire(context.Background(), "t1-1",
		orchestrator.Shape{NetworkAccess: model.NetworkNone})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer sb.Release(context.Background())

	log := routeLog(t, routes)
	if !strings.Contains(log, "drop:") {
		t.Errorf("nothing deleted the guest's default route: %q", log)
	}
	if !strings.Contains(log, "get:") || !strings.Contains(log, "172.17.0.1") {
		t.Errorf("nothing checked the guest can still reach the git proxy: %q", log)
	}
}

// The ordinary sandbox is left exactly as it was. "open" is not a thing
// done to a guest, it is the guest as granule brought it up, so a run
// that asked for nothing must not have its routing table touched at all.
func TestGranuleSandboxesLeavesAnOpenSandboxesRoutingAlone(t *testing.T) {
	for _, access := range []model.NetworkAccess{"", model.NetworkOpen} {
		t.Run(string(access)+"access", func(t *testing.T) {
			stateDir := t.TempDir()
			writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30080)
			routes := filepath.Join(t.TempDir(), "routes.log")
			guestNetworkFake(t, filepath.Join(t.TempDir(), "docker-argv.log"), routes, false)

			k := orchestrator.NewGranuleSandboxes(networkTestConfig(stateDir))
			sb, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{NetworkAccess: access})
			if err != nil {
				t.Fatalf("Acquire: %v", err)
			}
			defer sb.Release(context.Background())

			if log := routeLog(t, routes); log != "" {
				t.Errorf("a sandbox with network %q had its routing touched: %q", access, log)
			}
		})
	}
}

// A guest whose default route cannot be removed fails the acquire, and
// the VM goes with it. This is the whole difference between this and
// ensureGuestKVMAccess beside it: a repair that does not take is logged,
// a promise that cannot be kept is a failure -- because the alternative
// is dispatching the task that asked for no network into a sandbox that
// has one.
func TestGranuleSandboxesRefusesASandboxItCannotCutOffTheNetwork(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30080)
	routes := filepath.Join(t.TempDir(), "routes.log")
	guestNetworkFake(t, filepath.Join(t.TempDir(), "docker-argv.log"), routes, true)

	k := orchestrator.NewGranuleSandboxes(networkTestConfig(stateDir))
	sb, err := k.Acquire(context.Background(), "t1-1",
		orchestrator.Shape{NetworkAccess: model.NetworkNone})
	if err == nil {
		sb.Release(context.Background())
		t.Fatal("Acquire succeeded against a guest whose default route could not be removed")
	}
	if !strings.Contains(err.Error(), "no network access") {
		t.Errorf("Acquire failed with %q, want it to name what the sandbox was to have", err)
	}
	if _, statErr := os.Stat(filepath.Join(stateDir, "g-t1-1.json")); statErr == nil {
		t.Error("the VM is still there after a failed acquire, want it deleted")
	}
}

// The deployment-wide default reaches a run that asked for nothing of
// its own, the same way a deployment-wide size does -- per dimension,
// through Shape.orDefault, so this is the one setting deciding the
// network of every task that does not name one.
func TestGranuleSandboxesAppliesTheDeploymentWideNetworkDefault(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30080)
	routes := filepath.Join(t.TempDir(), "routes.log")
	guestNetworkFake(t, filepath.Join(t.TempDir(), "docker-argv.log"), routes, false)

	cfg := networkTestConfig(stateDir)
	cfg.DefaultNetworkAccess = model.NetworkNone
	k := orchestrator.NewGranuleSandboxes(cfg)
	sb, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer sb.Release(context.Background())

	if routeLog(t, routes) == "" {
		t.Error("a deployment defaulting to no network built a sandbox with its default route intact")
	}
}

// SetDefaultShape carries a changed deployment-wide network to the next
// sandbox, which is how a settings edit reaches a running daemon
// (pkg/cli/daemon.go's liveConfig).
func TestGranuleSandboxesSetDefaultShapeCarriesTheNetwork(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30080)
	routes := filepath.Join(t.TempDir(), "routes.log")
	guestNetworkFake(t, filepath.Join(t.TempDir(), "docker-argv.log"), routes, false)

	k := orchestrator.NewGranuleSandboxes(networkTestConfig(stateDir))
	k.SetDefaultShape(orchestrator.Shape{NetworkAccess: model.NetworkNone})
	sb, err := k.Acquire(context.Background(), "t1-1", orchestrator.Shape{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer sb.Release(context.Background())

	if routeLog(t, routes) == "" {
		t.Error("a network set through SetDefaultShape did not reach the next sandbox")
	}
}

// A per-task override still beats the deployment default the other way
// round: a deployment that withholds the network by default can still
// file the one task that needs it.
func TestGranuleSandboxesLetsATaskAskForTheNetworkBack(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30080)
	routes := filepath.Join(t.TempDir(), "routes.log")
	guestNetworkFake(t, filepath.Join(t.TempDir(), "docker-argv.log"), routes, false)

	cfg := networkTestConfig(stateDir)
	cfg.DefaultNetworkAccess = model.NetworkNone
	k := orchestrator.NewGranuleSandboxes(cfg)
	sb, err := k.Acquire(context.Background(), "t1-1",
		orchestrator.Shape{NetworkAccess: model.NetworkOpen})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer sb.Release(context.Background())

	if log := routeLog(t, routes); log != "" {
		t.Errorf("a task that asked for the network back had its routing cut anyway: %q", log)
	}
}

// A guest that lost the git proxy along with the internet is refused
// too. The setting is "reach this deployment and nothing else"; a
// sandbox that reaches nothing at all is a run that fails at its first
// clone, well away from anything that would explain why.
func TestGranuleSandboxesRefusesASandboxThatCanNoLongerReachTheGitProxy(t *testing.T) {
	stateDir := t.TempDir()
	writeFakeGranule(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30080)
	routes := filepath.Join(t.TempDir(), "routes.log")
	writeFakeDocker(t, filepath.Join(t.TempDir(), "docker-argv.log"), "running", `
  shift
  while [ $# -gt 0 ] && [ "$1" != "--" ]; do shift; done
  shift
  case "$*" in
  *"route get"*)
    echo "get: $*" >> `+routes+`
    echo "no route to the git proxy at 172.17.0.1: RTNETLINK answers: Network is unreachable" >&2
    exit 1
    ;;
  esac
  exit 0
`)

	k := orchestrator.NewGranuleSandboxes(networkTestConfig(stateDir))
	sb, err := k.Acquire(context.Background(), "t1-1",
		orchestrator.Shape{NetworkAccess: model.NetworkNone})
	if err == nil {
		sb.Release(context.Background())
		t.Fatal("Acquire succeeded against a guest that can no longer reach the git proxy")
	}
	if !strings.Contains(err.Error(), "git proxy") {
		t.Errorf("Acquire failed with %q, want it to name the git proxy", err)
	}
}

// The host-directory backend refuses rather than pretending. It has no
// network of its own to withhold -- a run there executes on the host,
// through the host's own stack -- so the only honest answer to a task
// asking for none is no sandbox.
func TestHostSandboxesRefuseATaskThatAsksForNoNetwork(t *testing.T) {
	t.Parallel()
	h := orchestrator.NewHostSandboxes(t.TempDir())
	sb, err := h.Acquire(context.Background(), "t1-1",
		orchestrator.Shape{NetworkAccess: model.NetworkNone})
	if err == nil {
		sb.Release(context.Background())
		t.Fatal("a host-directory sandbox was handed to a task that asked for no network")
	}
	if !strings.Contains(err.Error(), "network") {
		t.Errorf("Acquire failed with %q, want it to say the network is what it cannot give", err)
	}
}

// ...and says nothing about a task content with the network it has: a
// deployment on the host backend is not one where every task has to
// leave the field blank, only one where "none" is unavailable.
func TestHostSandboxesAcceptATaskThatAsksForTheOrdinaryNetwork(t *testing.T) {
	t.Parallel()
	h := orchestrator.NewHostSandboxes(t.TempDir())
	sb, err := h.Acquire(context.Background(), "t1-1",
		orchestrator.Shape{NetworkAccess: model.NetworkOpen})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := sb.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

// Shape.orDefault fills the network like any other dimension, and
// SizeIsZero keeps saying what it always said, so a shape that asks only
// for a network is not read as a shape that asks for a size.
func TestShapeNetworkResolvesPerDimension(t *testing.T) {
	t.Parallel()
	deployment := orchestrator.Shape{CPUs: 4, MemoryMB: 4096, DiskGB: 20, NetworkAccess: model.NetworkNone}
	asked := orchestrator.Shape{CPUs: 8}
	got := orchestrator.ShapeOrDefaultForTest(asked, deployment)
	if got.CPUs != 8 || got.MemoryMB != 4096 || got.NetworkAccess != model.NetworkNone {
		t.Errorf("orDefault = %+v, want the task's CPUs and the deployment's memory and network", got)
	}
	networkOnly := orchestrator.Shape{NetworkAccess: model.NetworkNone}
	if !networkOnly.SizeIsZero() {
		t.Error("a shape that names only a network reads as asking for a size")
	}
	if networkOnly.IsZero() {
		t.Error("a shape that names a network reads as asking for nothing")
	}
	if !(orchestrator.Shape{}).IsZero() {
		t.Error("the zero shape does not read as asking for nothing")
	}
}
