package cli

// The deployment-wide sandbox VM shape (ui.Settings.SandboxCPUs/
// SandboxMemoryMB, bwsalmon/agents#534) used to be reachable only from
// the Settings pane or a raw PUT /api/settings: `grain settings` had no
// flag for it, printed nothing about it, and `grain sync` applied a
// change to it while reporting "nothing changed". These cover the three
// halves of that gap -- set it, read it back, and see a sync say it
// moved -- against a real embedded store, the same discipline
// sync_test.go holds cmd/grain's own tests to.

import (
	"context"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

var captureStdoutMu sync.Mutex

// captureStdout runs fn with os.Stdout redirected and returns what it
// printed -- printer writes straight to stdout (it is a CLI's output,
// not a value anything returns), so reading it back is the only way to
// assert on what an operator actually sees.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	captureStdoutMu.Lock()
	defer captureStdoutMu.Unlock()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		out, _ := io.ReadAll(r)
		done <- string(out)
	}()

	fn()

	w.Close()
	os.Stdout = old
	return <-done
}

// settingsTestServer starts a ui.Server over a real embedded SQLite
// store with settings already saved once, since the first-ever
// UpdateSettings demands the fields ui.Client.UpdateSettings's own doc
// comment lists and the sandbox shape is not among them.
//
// It speaks for a deployment whose sandbox shape is actually applied --
// the granule-managed case -- since that is what every other test here
// wants to read settings back from. shapelessSettingsTestServer below is
// the other one.
func settingsTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return settingsTestServerFor(t, ui.Config{Actor: ui.DefaultActor("operator"), Capabilities: ui.OfferedCapabilities()})
}

// shapelessSettingsTestServer is the same server speaking for a
// deployment running the default host-directory sandboxing, where the
// stored sandbox shape sizes nothing at all
// (ui.Config.SandboxShapeIgnored).
func shapelessSettingsTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return settingsTestServerFor(t, ui.Config{
		Actor:               ui.DefaultActor("operator"),
		Capabilities:        ui.OfferedCapabilities(),
		SandboxShapeIgnored: true,
	})
}

func settingsTestServerFor(t *testing.T, cfg ui.Config) *httptest.Server {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(t.TempDir(), "data")))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	srv := httptest.NewServer(ui.NewServer(withAccessCode(cfg), store))
	t.Cleanup(srv.Close)

	req := *settingsRequest("30s", 2, "github.example")
	if _, err := accessCodeClient(srv.URL).UpdateSettings(context.Background(), req); err != nil {
		t.Fatalf("seeding settings: %v", err)
	}
	return srv
}

func TestCmdSettingsSetsAndClearsTheSandboxShape(t *testing.T) {
	ctx := context.Background()
	srv := settingsTestServer(t)
	c := accessCodeClient(srv.URL)

	captureStdout(t, func() {
		if err := cmdSettings(ctx, c, &printer{}, []string{"-sandbox-cpus", "4", "-sandbox-memory-mb", "8192", "-sandbox-disk-gb", "40"}); err != nil {
			t.Errorf("cmdSettings: %v", err)
		}
	})
	settings, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if settings.SandboxCPUs != 4 || settings.SandboxMemoryMB != 8192 || settings.SandboxDiskGB != 40 {
		t.Fatalf("sandbox shape = %d cpus/%d MiB/%d GiB, want 4/8192/40",
			settings.SandboxCPUs, settings.SandboxMemoryMB, settings.SandboxDiskGB)
	}

	// 0 is a real value here -- "leave grain's own default shape in
	// place" -- so a flag set to it must be applied rather than read as
	// an omitted flag, which is what the fs.Visit convention buys.
	captureStdout(t, func() {
		if err := cmdSettings(ctx, c, &printer{}, []string{"-sandbox-cpus", "0"}); err != nil {
			t.Errorf("cmdSettings clearing sandbox-cpus: %v", err)
		}
	})
	settings, err = c.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if settings.SandboxCPUs != 0 {
		t.Errorf("SandboxCPUs after -sandbox-cpus=0 = %d, want 0", settings.SandboxCPUs)
	}
	if settings.SandboxMemoryMB != 8192 {
		t.Errorf("SandboxMemoryMB = %d, want the untouched 8192", settings.SandboxMemoryMB)
	}
	if settings.SandboxDiskGB != 40 {
		t.Errorf("SandboxDiskGB = %d, want the untouched 40", settings.SandboxDiskGB)
	}

	// Disk clears back to its own default the same way, which is a number
	// grain names (granule.DefaultDiskGB) like the other two -- it used to
	// mean "as large as the guest image behind the VM" instead.
	captureStdout(t, func() {
		if err := cmdSettings(ctx, c, &printer{}, []string{"-sandbox-disk-gb", "0"}); err != nil {
			t.Errorf("cmdSettings clearing sandbox-disk-gb: %v", err)
		}
	})
	settings, err = c.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if settings.SandboxDiskGB != 0 {
		t.Errorf("SandboxDiskGB after -sandbox-disk-gb=0 = %d, want 0", settings.SandboxDiskGB)
	}
	if settings.SandboxMemoryMB != 8192 {
		t.Errorf("SandboxMemoryMB = %d, want the untouched 8192", settings.SandboxMemoryMB)
	}
}

func TestCmdSettingsPrintsTheSandboxShape(t *testing.T) {
	ctx := context.Background()
	srv := settingsTestServer(t)
	c := accessCodeClient(srv.URL)

	// Unset: what prints is the shape actually in effect (grain's own
	// default, ui.Settings.SandboxCPUsDefault), not the stored 0. Disk is
	// no longer the exception it was: it has a default of its own now
	// (SandboxDiskGBDefault), so all three lines name the size a sandbox
	// would really be built at.
	out := captureStdout(t, func() {
		if err := cmdSettings(ctx, c, &printer{}, nil); err != nil {
			t.Errorf("cmdSettings: %v", err)
		}
	})
	for _, want := range []string{
		"sandbox cpus:   2 (grain default, unset)",
		"sandbox memory mb: 8192 (grain default, unset)",
		"sandbox disk gb: 30 (grain default, unset)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("`grain settings` printed %q, which does not contain %q", out, want)
		}
	}

	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{
		SandboxCPUs: intPtr(4), SandboxMemoryMB: intPtr(8192), SandboxDiskGB: intPtr(40),
	}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	out = captureStdout(t, func() {
		if err := cmdSettings(ctx, c, &printer{}, nil); err != nil {
			t.Errorf("cmdSettings: %v", err)
		}
	})
	for _, want := range []string{"sandbox cpus:   4", "sandbox memory mb: 8192", "sandbox disk gb: 40"} {
		if !strings.Contains(out, want) {
			t.Errorf("`grain settings` printed %q, which does not contain %q", out, want)
		}
	}
	// Scoped to the three shape lines rather than the whole output: other
	// settings are annotated the same way when *they* are unset (the agent
	// git identity, grain/task-14), and this is a claim about the shape.
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "sandbox cpus") && !strings.HasPrefix(line, "sandbox memory") &&
			!strings.HasPrefix(line, "sandbox disk") {
			continue
		}
		if strings.Contains(line, "grain default") {
			t.Errorf("`grain settings` printed %q, which still calls a set shape a default", line)
		}
	}
}

// A deployment whose sandboxes are directories on the daemon's own
// machine applies none of these three numbers -- pkg/cli/daemon.go's
// defaultShaper path skips that backend entirely -- so every line that
// prints one says so. Before this, `grain settings` printed a bare
// "sandbox cpus: 2" there, which reads as the cap runs are held to when
// they are held to nothing (grain/task-9).
func TestCmdSettingsSaysTheSandboxShapeIsNotInEffect(t *testing.T) {
	ctx := context.Background()
	srv := shapelessSettingsTestServer(t)
	c := accessCodeClient(srv.URL)

	out := captureStdout(t, func() {
		if err := cmdSettings(ctx, c, &printer{}, nil); err != nil {
			t.Errorf("cmdSettings: %v", err)
		}
	})
	for _, line := range []string{"sandbox cpus:", "sandbox memory mb:", "sandbox disk gb:"} {
		for _, l := range strings.Split(out, "\n") {
			if !strings.HasPrefix(l, line) {
				continue
			}
			if !strings.Contains(l, "not in effect") || !strings.Contains(l, "no shape") {
				t.Errorf("`grain settings` printed %q, which does not say the shape is not in effect", l)
			}
		}
	}

	// A stored value is still printed, not hidden: it is what this
	// deployment would build VMs at if it were switched over, and it is
	// the value the box on the pane is editing.
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{SandboxCPUs: intPtr(4)}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	out = captureStdout(t, func() {
		if err := cmdSettings(ctx, c, &printer{}, nil); err != nil {
			t.Errorf("cmdSettings: %v", err)
		}
	})
	if !strings.Contains(out, "sandbox cpus:   4 -- not in effect") {
		t.Errorf("`grain settings` printed %q, want the stored 4 printed and annotated", out)
	}

	// And the annotation is this deployment's, not something every
	// deployment now says: a granule-managed one applies the shape, and
	// saying otherwise there would be its own lie.
	shaped := accessCodeClient(settingsTestServer(t).URL)
	out = captureStdout(t, func() {
		if err := cmdSettings(ctx, shaped, &printer{}, nil); err != nil {
			t.Errorf("cmdSettings: %v", err)
		}
	})
	if strings.Contains(out, "not in effect") {
		t.Errorf("`grain settings` printed %q against a shaped backend, which should say nothing about shapes not applying", out)
	}
}

func TestPrintSettingsDiffReportsTheSandboxShape(t *testing.T) {
	before := ui.Settings{SandboxCPUs: 2, SandboxMemoryMB: 2048}
	after := ui.Settings{SandboxCPUs: 8, SandboxMemoryMB: 2048}

	out := captureStdout(t, func() { printSettingsDiff(before, after, false) })
	if !strings.Contains(out, "sandbox cpus") || !strings.Contains(out, "\"8\"") {
		t.Errorf("printSettingsDiff printed %q, want the sandbox cpus change", out)
	}
	if strings.Contains(out, "nothing changed") {
		t.Errorf("printSettingsDiff printed %q for a real change", out)
	}
	if strings.Contains(out, "sandbox memory mb") {
		t.Errorf("printSettingsDiff printed %q, which names a field that did not change", out)
	}

	out = captureStdout(t, func() { printSettingsDiff(before, before, false) })
	if !strings.Contains(out, "nothing changed") {
		t.Errorf("printSettingsDiff printed %q for an unchanged settings row, want \"nothing changed\"", out)
	}

	// Disk on its own, since it is the dimension a config file could
	// move with neither of the other two: before this row existed, such
	// a sync applied the change and then reported "nothing changed".
	disked := ui.Settings{SandboxCPUs: 2, SandboxMemoryMB: 2048, SandboxDiskGB: 40}
	out = captureStdout(t, func() { printSettingsDiff(before, disked, false) })
	if !strings.Contains(out, "sandbox disk gb") || !strings.Contains(out, "\"40\"") {
		t.Errorf("printSettingsDiff printed %q, want the sandbox disk gb change", out)
	}
	if strings.Contains(out, "nothing changed") {
		t.Errorf("printSettingsDiff printed %q for a real disk change", out)
	}
}

func TestSandboxShapeValue(t *testing.T) {
	t.Parallel()
	if got := sandboxShapeValue(4, 2, false); got != "4" {
		t.Errorf("sandboxShapeValue(4, 2) = %q, want %q", got, "4")
	}
	if got := sandboxShapeValue(0, 2, false); !strings.Contains(got, "2") || !strings.Contains(got, "default") {
		t.Errorf("sandboxShapeValue(0, 2) = %q, want grain's own default named", got)
	}
	if got := sandboxShapeValue(0, 0, false); got != "unset" {
		t.Errorf("sandboxShapeValue(0, 0) = %q, want %q", got, "unset")
	}

	// Ignored: the number stays -- it is what is stored, and what would
	// be built to under a backend that has a shape -- and the line says
	// it is not in effect here.
	if got := sandboxShapeValue(4, 2, true); !strings.HasPrefix(got, "4 ") || !strings.Contains(got, "not in effect") {
		t.Errorf("sandboxShapeValue(4, 2, ignored) = %q, want the stored 4 annotated as not in effect", got)
	}
	if got := sandboxShapeValue(0, 2, true); !strings.Contains(got, "grain default") || !strings.Contains(got, "not in effect") {
		t.Errorf("sandboxShapeValue(0, 2, ignored) = %q, want the default named and annotated", got)
	}
	if got := sandboxShapeValue(0, 0, true); !strings.HasPrefix(got, "unset") || !strings.Contains(got, "not in effect") {
		t.Errorf("sandboxShapeValue(0, 0, ignored) = %q, want %q annotated", got, "unset")
	}
}

// The daemon reads the annotation off the backend it built rather than
// off the flag that chose it, so what the pane calls "not in effect" is
// exactly what liveConfig.refresh declines to hand a changed shape to.
func TestSandboxShapeIgnoredFollowsTheBackend(t *testing.T) {
	t.Parallel()
	if got := daemon.SandboxShapeIgnored(orchestrator.NewHostSandboxes(t.TempDir())); !got {
		t.Errorf("sandboxShapeIgnored(HostSandboxes) = %v, want true -- a host directory has no shape", got)
	}
	if got := daemon.SandboxShapeIgnored(orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{})); got {
		t.Errorf("sandboxShapeIgnored(GranuleSandboxes) = %v, want false -- it implements SetDefaultShape", got)
	}
}

func TestCmdSettingsSetsAndPrintsKubernetesSandboxSettings(t *testing.T) {
	ctx := context.Background()
	srv := settingsTestServerFor(t, ui.Config{
		Actor:           ui.DefaultActor("operator"),
		Capabilities:    ui.OfferedCapabilities(),
		SandboxBackends: []model.SandboxBackend{model.SandboxBackendKubernetes, model.SandboxBackendKubernetesGranule},
	})
	c := accessCodeClient(srv.URL)

	out := captureStdout(t, func() {
		err := cmdSettings(ctx, c, &printer{}, []string{
			"-sandbox-backend=kubernetes-granule",
			"-k8s-namespace=grain-k8s",
			"-k8s-kubectl=/usr/local/bin/kubectl",
			"-k8s-sandbox-image=example/k8s-sandbox:v1",
			"-k8s-granule-image=example/k8s-granule:v1",
			"-k8s-workspace=/workspace/k8s",
			"-k8s-git-proxy-host=10.244.0.10",
			"-k8s-sandbox-service=grain-proxy",
			"-k8s-sandbox-service-account=sandbox-sa",
			"-k8s-node-selector=pool=sandboxes,zone=us-central1-a",
			"-k8s-sandbox-privileged=true",
			"-k8s-network-policy=true",
			"-k8s-deployment=grain-daemon",
			"-k8s-container=grain-ctr",
		})
		if err != nil {
			t.Fatalf("cmdSettings: %v", err)
		}
	})

	for _, want := range []string{
		"sandbox backend: kubernetes-granule",
		"k8s namespace:  grain-k8s",
		"k8s kubectl:    /usr/local/bin/kubectl",
		"k8s sandbox image: example/k8s-sandbox:v1",
		"k8s granule image: example/k8s-granule:v1",
		"k8s workspace:  /workspace/k8s",
		"k8s git proxy host: 10.244.0.10",
		"k8s sandbox service: grain-proxy",
		"k8s service account: sandbox-sa",
		"k8s node selector: pool=sandboxes, zone=us-central1-a",
		"k8s privileged: true",
		"k8s network policy: true",
		"k8s deployment: grain-daemon",
		"k8s container:  grain-ctr",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("`grain settings` output %q missing %q", out, want)
		}
	}
}

func TestSettingsSandboxOvercommitFlagsAndPrint(t *testing.T) {
	ctx := context.Background()
	ctrl := model.NewOvercommitController(model.OvercommitSettings{Mode: model.OvercommitAuto}, 3.25, 1.5)
	srv := settingsTestServerFor(t, ui.Config{
		Actor:        ui.DefaultActor("operator"),
		Capabilities: ui.OfferedCapabilities(),
		SandboxHostCapacity: func() (ui.HostCapacity, error) {
			return ui.HostCapacity{CPUs: 16, MemoryMB: 65536}, nil
		},
		Overcommit: ctrl.State,
	})
	c := accessCodeClient(srv.URL)

	out := captureStdout(t, func() {
		if err := cmdSettings(ctx, c, &printer{}, nil); err != nil {
			t.Fatalf("settings: %v", err)
		}
	})
	wantDefault := "sandbox overcommit: fixed (CPU 1.00x..6.00x, memory 1.00x..2.00x, targets CPU 25.0% / memory 5.0%)"
	if !strings.Contains(out, wantDefault) {
		t.Fatalf("default settings output:\n%s\nmissing %q", out, wantDefault)
	}

	out = captureStdout(t, func() {
		if err := cmdSettings(ctx, c, &printer{}, []string{
			"-sandbox-overcommit", "auto",
			"-sandbox-overcommit-cpu-max", "4.5",
			"-sandbox-overcommit-memory-pressure-target", "3.5",
		}); err != nil {
			t.Fatalf("settings auto overcommit: %v", err)
		}
	})
	wantAuto := "sandbox overcommit: auto (CPU 1.00x..4.50x, memory 1.00x..2.00x, targets CPU 25.0% / memory 3.5%) -- effective CPU 3.25x, memory 1.50x"
	if !strings.Contains(out, wantAuto) {
		t.Fatalf("auto settings output:\n%s\nmissing %q", out, wantAuto)
	}

	// Updating one bound on a subsequent call preserves the mode and other bounds.
	out = captureStdout(t, func() {
		if err := cmdSettings(ctx, c, &printer{}, []string{
			"-sandbox-overcommit-cpu-min", "1.5",
		}); err != nil {
			t.Fatalf("settings partial overcommit update: %v", err)
		}
	})
	wantMerged := "sandbox overcommit: auto (CPU 1.50x..4.50x, memory 1.00x..2.00x, targets CPU 25.0% / memory 3.5%) -- effective CPU 3.25x, memory 1.50x"
	if !strings.Contains(out, wantMerged) {
		t.Fatalf("merged settings output:\n%s\nmissing %q", out, wantMerged)
	}
}

func intPtr(v int) *int { return &v }
