package orchestrator

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/k8s"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
)

// fakePriorityCluster is a kubectl that keeps a cluster's PriorityClasses
// as files in a directory: `get priorityclass` reads them, `create`
// adds one (or is refused, as RBAC refuses a namespace-bound identity,
// once refuseCreate has been called), and a pod `apply` is refused the
// way the priority admission plugin refuses one naming a class that is
// not there. Every pod it accepts is recorded with the class it named.
type fakePriorityCluster struct {
	dir     string
	kubectl string
}

func newFakePriorityCluster(t *testing.T, present ...string) *fakePriorityCluster {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pc"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &fakePriorityCluster{dir: dir, kubectl: filepath.Join(dir, "kubectl")}
	for _, name := range present {
		c.add(t, name)
	}
	script := `#!/bin/sh
dir="` + dir + `"
echo "$*" >> "$dir/calls"
if [ "$1" = "-n" ]; then shift 2; fi
case "$1" in
get)
	if [ "$2" = "priorityclass" ]; then
		if [ -f "$dir/pc/$3" ]; then echo "priorityclass.scheduling.k8s.io/$3"; exit 0; fi
		echo "Error from server (NotFound): priorityclasses.scheduling.k8s.io \"$3\" not found" >&2
		exit 1
	fi
	;;
create)
	if [ -f "$dir/refuse-create" ]; then
		echo 'Error from server (Forbidden): priorityclasses.scheduling.k8s.io is forbidden: User "system:serviceaccount:grain:grain" cannot create resource "priorityclasses" in API group "scheduling.k8s.io" at the cluster scope' >&2
		exit 1
	fi
	name=$(sed -n 's/^  name: //p' | head -n 1)
	touch "$dir/pc/$name"
	exit 0
	;;
apply)
	class=$(sed -n 's/.*"priorityClassName":"\([^"]*\)".*/\1/p')
	if [ -n "$class" ] && [ ! -f "$dir/pc/$class" ]; then
		echo "Error from server (Forbidden): error when creating \"STDIN\": pods \"p\" is forbidden: no PriorityClass with name $class was found" >&2
		exit 1
	fi
	echo "${class:-none}" >> "$dir/pods"
	exit 0
	;;
esac
exit 0
`
	// Written as data and made executable by a child process, for the
	// ETXTBSY reason writeFakeKubectlForGranule gives.
	src := c.kubectl + ".src"
	if err := os.WriteFile(src, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("install", "-m", "0755", src, c.kubectl).CombinedOutput(); err != nil {
		t.Fatalf("installing fake kubectl: %v: %s", err, out)
	}
	return c
}

func (c *fakePriorityCluster) add(t *testing.T, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(c.dir, "pc", name), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (c *fakePriorityCluster) refuseCreate(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(c.dir, "refuse-create"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (c *fakePriorityCluster) has(name string) bool {
	_, err := os.Stat(filepath.Join(c.dir, "pc", name))
	return err == nil
}

func (c *fakePriorityCluster) lines(file string) []string {
	data, _ := os.ReadFile(filepath.Join(c.dir, file))
	return strings.Fields(string(data))
}

func (c *fakePriorityCluster) calls() string {
	data, _ := os.ReadFile(filepath.Join(c.dir, "calls"))
	return string(data)
}

func (c *fakePriorityCluster) granule(classes K8sPriorityClasses) *K8sGranuleSandboxes {
	return NewK8sGranuleSandboxes(K8sGranuleConfig{
		Kubectl: c.kubectl, Image: "example/granule", PriorityClasses: classes,
	})
}

// createGranulePod is what K8sGranuleSandboxes.create does once the
// stale pod is gone and the new one admitted: the part that names a
// class.
func createGranulePod(ctx context.Context, k *K8sGranuleSandboxes, class SandboxClass) error {
	shape := Shape{Class: class}
	return k.priority.createPod(ctx, k.pods(), "p", k.podShape(shape), k.podOptions(shape), k.cfg.StrictPriorityClasses)
}

func openPriorityNotification(t *testing.T, store *model.Store) *model.Notification {
	t.Helper()
	n, err := store.OpenNotification(context.Background(), k8sPriorityClassSubject)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// An upgraded cluster that never had grain's classes gets them at the
// first cycle, and pods are created naming them, with nothing to alert.
func TestK8sPriorityClassesMissingDefaultsAreCreated(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cluster := newFakePriorityCluster(t)
	k := cluster.granule(DefaultK8sPriorityClasses())
	store, _ := sqlitetest.Open(t)

	if err := reconcileK8sPriorityClasses(ctx, Deps{Store: store, Sandboxes: k}, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, spec := range k8s.SandboxPriorityClasses {
		if !cluster.has(spec.Name) {
			t.Errorf("PriorityClass %s was not created", spec.Name)
		}
	}
	if n := openPriorityNotification(t, store); n != nil {
		t.Errorf("raised %q with every class created", n.Title)
	}
	if err := createGranulePod(ctx, k, SandboxClassCI); err != nil {
		t.Fatalf("creating a CI pod: %v", err)
	}
	if got := cluster.lines("pods"); len(got) != 1 || got[0] != k8s.PriorityClassCI {
		t.Errorf("pods created with classes %v, want [%s]", got, k8s.PriorityClassCI)
	}
}

// A cluster whose classes this identity may not create has pods made with
// no class rather than failed, and an alert saying what to apply -- which
// clears once the classes appear, and the pods get their class back.
func TestK8sPriorityClassesForbiddenCreateFallsBackAndAlerts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cluster := newFakePriorityCluster(t)
	cluster.refuseCreate(t)
	k := cluster.granule(DefaultK8sPriorityClasses())
	store, _ := sqlitetest.Open(t)
	now := time.Now()

	if err := reconcileK8sPriorityClasses(ctx, Deps{Store: store, Sandboxes: k}, now); err != nil {
		t.Fatal(err)
	}
	n := openPriorityNotification(t, store)
	if n == nil {
		t.Fatal("no notification with every class missing and uncreatable")
	}
	for _, want := range []string{"grain-interactive", "grain-agent", "grain-ci", "kubectl apply -f deploy/kubernetes/grain-granule.yaml", "forbidden"} {
		if !strings.Contains(n.Title+n.Body, want) {
			t.Errorf("notification does not say %q:\n%s\n%s", want, n.Title, n.Body)
		}
	}
	if err := createGranulePod(ctx, k, SandboxClassInteractive); err != nil {
		t.Fatalf("creating an interactive pod with its class missing: %v", err)
	}
	if got := cluster.lines("pods"); len(got) != 1 || got[0] != "none" {
		t.Errorf("pods created with classes %v, want one with none", got)
	}

	// A second cycle inside the recheck interval asks nothing more.
	before := cluster.calls()
	if err := reconcileK8sPriorityClasses(ctx, Deps{Store: store, Sandboxes: k}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if cluster.calls() != before {
		t.Errorf("a cycle before the recheck was due ran kubectl:\n%s", strings.TrimPrefix(cluster.calls(), before))
	}

	// The operator applies the manifest.
	for _, spec := range k8s.SandboxPriorityClasses {
		cluster.add(t, spec.Name)
	}
	if err := reconcileK8sPriorityClasses(ctx, Deps{Store: store, Sandboxes: k}, now.Add(k8sPriorityClassMissingRecheck)); err != nil {
		t.Fatal(err)
	}
	if n := openPriorityNotification(t, store); n != nil {
		t.Errorf("notification still open once the classes exist: %q", n.Title)
	}
	if err := createGranulePod(ctx, k, SandboxClassInteractive); err != nil {
		t.Fatal(err)
	}
	if got := cluster.lines("pods"); len(got) != 2 || got[1] != k8s.PriorityClassInteractive {
		t.Errorf("pods created with classes %v, want the second at %s", got, k8s.PriorityClassInteractive)
	}
}

// A class the settings name that is not grain's own is never created:
// pods fall back to none, and the alert says to create it or change the
// setting.
func TestK8sPriorityClassesCustomMissingNameFallsBackAndAlerts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cluster := newFakePriorityCluster(t, k8s.PriorityClassInteractive, k8s.PriorityClassAgent)
	classes := DefaultK8sPriorityClasses()
	classes.CI = "batch-low"
	k := cluster.granule(classes)
	store, _ := sqlitetest.Open(t)

	if err := reconcileK8sPriorityClasses(ctx, Deps{Store: store, Sandboxes: k}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if cluster.has("batch-low") || strings.Contains(cluster.calls(), "create") {
		t.Errorf("created a class that is not grain's own; calls:\n%s", cluster.calls())
	}
	n := openPriorityNotification(t, store)
	if n == nil {
		t.Fatal("no notification for a configured class the cluster does not have")
	}
	if !strings.Contains(n.Body, "`batch-low`") || !strings.Contains(n.Body, "Settings -> Sandbox") {
		t.Errorf("notification does not name batch-low and the setting:\n%s", n.Body)
	}
	if strings.Contains(n.Body, "grain-agent") {
		t.Errorf("notification names a class the cluster has:\n%s", n.Body)
	}
	if err := createGranulePod(ctx, k, SandboxClassCI); err != nil {
		t.Fatalf("creating a CI pod with its class missing: %v", err)
	}
	if err := createGranulePod(ctx, k, SandboxClassAgent); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cluster.lines("pods"), ","); got != "none,"+k8s.PriorityClassAgent {
		t.Errorf("pods created with classes %s, want none,%s", got, k8s.PriorityClassAgent)
	}
}

// With every class there the check reads and changes nothing.
func TestK8sPriorityClassesPresentIsANoOp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cluster := newFakePriorityCluster(t, k8s.PriorityClassInteractive, k8s.PriorityClassAgent, k8s.PriorityClassCI)
	k := NewK8sSandboxes(K8sConfig{Kubectl: cluster.kubectl, Image: "example/sandbox", PriorityClasses: DefaultK8sPriorityClasses()})
	store, _ := sqlitetest.Open(t)

	if err := reconcileK8sPriorityClasses(ctx, Deps{Store: store, Sandboxes: k}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if calls := cluster.calls(); strings.Contains(calls, "create") || strings.Count(calls, "get priorityclass") != 3 {
		t.Errorf("want three reads and nothing else; calls:\n%s", calls)
	}
	if n := openPriorityNotification(t, store); n != nil {
		t.Errorf("raised %q with every class present", n.Title)
	}
	if got := k.podOptions(Shape{Class: SandboxClassCI}).PriorityClassName; got != k8s.PriorityClassCI {
		t.Errorf("CI pod names %q, want %s", got, k8s.PriorityClassCI)
	}
}

// A create the API server refuses for a missing class, before any check
// has run, is retried with no class -- unless the backend is strict,
// which fails it with the ErrNoPriorityClass message it always had.
func TestK8sPriorityClassRefusedPodIsRetriedWithoutOneUnlessStrict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cluster := newFakePriorityCluster(t)

	k := cluster.granule(DefaultK8sPriorityClasses())
	if err := createGranulePod(ctx, k, SandboxClassAgent); err != nil {
		t.Fatalf("creating a pod whose class is missing: %v", err)
	}
	if got := cluster.lines("pods"); len(got) != 1 || got[0] != "none" {
		t.Errorf("pods created with classes %v, want one with none", got)
	}
	if got := k.priority.missingNames(); len(got) != 1 || got[0] != k8s.PriorityClassAgent {
		t.Errorf("missing = %v, want [%s]", got, k8s.PriorityClassAgent)
	}

	strict := NewK8sGranuleSandboxes(K8sGranuleConfig{
		Kubectl: cluster.kubectl, Image: "example/granule", PriorityClasses: DefaultK8sPriorityClasses(),
		StrictPriorityClasses: true,
	})
	strict.priority.markMissing(k8s.PriorityClassAgent)
	err := createGranulePod(ctx, strict, SandboxClassAgent)
	if !errors.Is(err, k8s.ErrNoPriorityClass) || !strings.Contains(err.Error(), "apply deploy/kubernetes/grain.yaml") {
		t.Errorf("strict create = %v, want k8s.ErrNoPriorityClass saying to apply the manifests", err)
	}
}
