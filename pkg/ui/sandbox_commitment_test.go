package ui_test

// grain/task-223: every check grain already made about a sandbox shape
// asked whether *one* sandbox of it could exist (grain/task-9's
// deployment-wide annotation, grain/task-33's per-task refusal). These
// cover the question none of them asked -- whether max_workers +
// max_mergers + max_ci_jobs of them can exist at once on the machine they are built on
// -- which is the one the grain-home deployment got wrong: 5 workers x 3
// vCPU on 4 cores, and 15 of 15.8 GiB committed to guests.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/granule"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// homeHost is the machine grain-home's own numbers were measured on: 4
// vCPU and 15.8 GiB, as /proc/meminfo reports it.
var homeHost = ui.HostCapacity{CPUs: 4, MemoryMB: 16125}

// The deployment the whole thing is about, arithmetic first: the two
// totals, and the two ratios that put them against the host.
func TestSandboxCommitmentCountsEverySlotAgainstTheHost(t *testing.T) {
	t.Parallel()
	got := ui.NewSandboxCommitment(model.Config{
		MaxWorkers: 5, MaxMergers: 2,
		SandboxCPUs: 3, SandboxMemoryMB: 3072,
	}, homeHost)
	if got == nil {
		t.Fatal("NewSandboxCommitment = nil, want a commitment")
	}
	// Mergers are counted alongside workers because a merger is a run
	// like any other and every run acquires a sandbox of its own -- this
	// deployment can have seven live, not five.
	if got.Runs != 7 {
		t.Errorf("Runs = %d, want 7 (5 workers + 2 mergers)", got.Runs)
	}
	if got.CPUsCommitted != 21 || got.MemoryMBCommitted != 7*3072 {
		t.Errorf("committed %d vCPU / %d MiB, want 21 / %d", got.CPUsCommitted, got.MemoryMBCommitted, 7*3072)
	}
	if got.HostCPUs != 4 || got.HostMemoryMB != 16125 {
		t.Errorf("host = %d vCPU / %d MiB, want the capacity it was given", got.HostCPUs, got.HostMemoryMB)
	}
	if got.CPURatio < 5.2 || got.CPURatio > 5.3 {
		t.Errorf("CPURatio = %v, want ~5.25 (21 vCPU on 4 cores)", got.CPURatio)
	}
	if got.MemoryShare < 1.3 || got.MemoryShare > 1.4 {
		t.Errorf("MemoryShare = %v, want ~1.33 (21 GiB committed on a 15.8 GiB host)", got.MemoryShare)
	}
	if got.CPULevel != ui.CommitmentWarning || got.MemoryLevel != ui.CommitmentWarning {
		t.Errorf("levels = %s/%s, want both %s", got.CPULevel, got.MemoryLevel, ui.CommitmentWarning)
	}
	if got.Level != ui.CommitmentWarning || !got.Warned() {
		t.Errorf("Level = %s, Warned = %t, want %s and true", got.Level, got.Warned(), ui.CommitmentWarning)
	}
}

func TestSandboxCommitmentCountsCIJobsAsAdditionalSlots(t *testing.T) {
	t.Parallel()
	got := ui.NewSandboxCommitment(model.Config{
		MaxWorkers: 4, MaxMergers: 1, MaxCIJobs: 8,
		SandboxCPUs: 2, SandboxMemoryMB: 1024,
	}, homeHost)
	if got == nil {
		t.Fatal("NewSandboxCommitment = nil, want a commitment")
	}
	if got.Workers != 4 || got.Mergers != 1 || got.CIJobs != 8 || got.Runs != 13 {
		t.Errorf("commitment slots = %+v, want 4 workers + 1 merger + 8 CI jobs = 13", got)
	}
	if got.CPUsCommitted != 26 || got.MemoryMBCommitted != 13*1024 {
		t.Errorf("committed %d vCPU / %d MiB, want 26 / %d", got.CPUsCommitted, got.MemoryMBCommitted, 13*1024)
	}
}

// grain/task-285: the same deployment on the backend that shapes
// nothing. max_workers still commits the machine there -- more of it,
// not less -- and the arithmetic that says so is the run count against
// the cores, with no product in it anywhere.
func TestUncappedSandboxCommitmentCountsRunsAgainstTheCores(t *testing.T) {
	t.Parallel()
	// The stored shape is passed in and must not appear in the answer: it
	// sizes nothing on this backend, and a "21 vCPU committed" made out
	// of it would report a limit no run here is held to.
	got := ui.NewUncappedSandboxCommitment(model.Config{
		MaxWorkers: 5, MaxMergers: 2,
		SandboxCPUs: 3, SandboxMemoryMB: 3072,
	}, homeHost)
	if got == nil {
		t.Fatal("NewUncappedSandboxCommitment = nil, want a commitment")
	}
	if !got.Uncapped {
		t.Error("Uncapped = false, want true -- every reader branches on this")
	}
	if got.Runs != 7 || got.HostCPUs != 4 || got.HostMemoryMB != 16125 {
		t.Errorf("commitment = %d runs on %d vCPU / %d MiB, want 7 on the host it was given",
			got.Runs, got.HostCPUs, got.HostMemoryMB)
	}
	if got.RunsPerCPU < 1.7 || got.RunsPerCPU > 1.8 {
		t.Errorf("RunsPerCPU = %v, want 1.75 (7 runs on 4 cores)", got.RunsPerCPU)
	}
	if got.CPUsPerSandbox != 0 || got.MemoryMBPerSandbox != 0 || got.CPUsCommitted != 0 || got.MemoryMBCommitted != 0 {
		t.Errorf("commitment = %+v, want no per-sandbox shape and no committed totals: nothing caps a run here", got)
	}
	if got.CPURatio != 0 || got.MemoryShare != 0 {
		t.Errorf("CPURatio = %v, MemoryShare = %v, want both 0 with no total to divide", got.CPURatio, got.MemoryShare)
	}
	// A run that may take the whole machine commits no share of it in
	// advance, so memory is not read at all rather than read leniently.
	if got.MemoryLevel != "" {
		t.Errorf("MemoryLevel = %q, want empty -- an unlimited run has no share to place", got.MemoryLevel)
	}
	if got.CPULevel != ui.CommitmentWarning || got.Level != ui.CommitmentWarning || !got.Warned() {
		t.Errorf("levels = %s/%s, Warned = %t, want %s and true -- 1.75 runs per core is where that deployment was",
			got.CPULevel, got.Level, got.Warned(), ui.CommitmentWarning)
	}
}

// Where the uncapped levels are drawn, in the units the evidence was
// measured in: concurrent runs on a 4-core host, which is the host
// grain-home task 141 bucketed.
func TestUncappedSandboxCommitmentLevels(t *testing.T) {
	t.Parallel()
	host := ui.HostCapacity{CPUs: 4, MemoryMB: 16125}
	for _, tc := range []struct {
		name  string
		runs  int
		want  string
		ratio float64
	}{
		// Below 3 concurrent that deployment saw no test timeouts at all.
		{"under the knee", 2, ui.CommitmentOK, 0.5},
		// 3 concurrent is where they started appearing (3/16 of runs).
		{"where timeouts started", 3, ui.CommitmentCaution, 0.75},
		// And 6 is the bucket where they were the ordinary outcome
		// (18/34), median wall time 58.1m.
		{"where they were the ordinary outcome", 6, ui.CommitmentWarning, 1.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ui.NewUncappedSandboxCommitment(model.Config{MaxWorkers: tc.runs}, host)
			if got == nil {
				t.Fatal("NewUncappedSandboxCommitment = nil, want a commitment")
			}
			if got.RunsPerCPU != tc.ratio {
				t.Errorf("RunsPerCPU = %v, want %v", got.RunsPerCPU, tc.ratio)
			}
			if got.Level != tc.want || got.CPULevel != tc.want {
				t.Errorf("levels = %s/%s, want %s", got.CPULevel, got.Level, tc.want)
			}
		})
	}
}

// The same nothing-to-say cases as the shaped constructor: this is a
// different sentence about a deployment, not a looser one.
func TestUncappedSandboxCommitmentIsNilWithNothingToSay(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		cfg  model.Config
		host ui.HostCapacity
	}{
		{"no slots configured", model.Config{}, homeHost},
		{"no host reading", model.Config{MaxWorkers: 4}, ui.HostCapacity{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ui.NewUncappedSandboxCommitment(tc.cfg, tc.host); got != nil {
				t.Fatalf("NewUncappedSandboxCommitment = %+v, want nil", got)
			}
		})
	}
}

// A stored 0 means "grain's own default shape", the same reading the
// pane and `grain settings` already give it -- so the sum has to be made
// of the shape a sandbox would really be built at, not of zeroes.
func TestSandboxCommitmentUsesTheDefaultShapeWhereNothingIsStored(t *testing.T) {
	t.Parallel()
	got := ui.NewSandboxCommitment(model.Config{MaxWorkers: 2}, homeHost)
	if got == nil {
		t.Fatal("NewSandboxCommitment = nil, want a commitment")
	}
	if got.CPUsPerSandbox != granule.DefaultCPUs || got.MemoryMBPerSandbox != granule.DefaultMemoryMB {
		t.Fatalf("per-sandbox shape = %d vCPU / %d MiB, want grain's default %d / %d",
			got.CPUsPerSandbox, got.MemoryMBPerSandbox, granule.DefaultCPUs, granule.DefaultMemoryMB)
	}
	if got.CPUsCommitted != 2*granule.DefaultCPUs {
		t.Errorf("CPUsCommitted = %d, want %d", got.CPUsCommitted, 2*granule.DefaultCPUs)
	}
}

// The levels are a judgement call about where contention starts to cost
// runs (the thresholds' own comment carries the evidence), so what they
// have to be held to is that a modestly overcommitted deployment is not
// shouted at and a badly overcommitted one is.
func TestSandboxCommitmentLevels(t *testing.T) {
	t.Parallel()
	host := ui.HostCapacity{CPUs: 8, MemoryMB: 16384}
	for _, tc := range []struct {
		name                string
		cfg                 model.Config
		wantCPU, wantMemory string
	}{{
		// 4 runs x 2 vCPU on 8 cores, 4 x 2 GiB of 16: committed and
		// nothing to say about it.
		name:       "within the host",
		cfg:        model.Config{MaxWorkers: 4, SandboxCPUs: 2, SandboxMemoryMB: 2048},
		wantCPU:    ui.CommitmentOK,
		wantMemory: ui.CommitmentOK,
	}, {
		// 8 x 2 = 16 vCPU on 8 cores (2x), 8 x 1.5 GiB = 12 of 16 (75%).
		name:       "overcommitted on purpose",
		cfg:        model.Config{MaxWorkers: 8, SandboxCPUs: 2, SandboxMemoryMB: 1536},
		wantCPU:    ui.CommitmentCaution,
		wantMemory: ui.CommitmentCaution,
	}, {
		// 8 x 3 = 24 vCPU on 8 cores (3x), 8 x 2 GiB = 16 of 16 (100%).
		name:       "past what the machine has",
		cfg:        model.Config{MaxWorkers: 8, SandboxCPUs: 3, SandboxMemoryMB: 2048},
		wantCPU:    ui.CommitmentWarning,
		wantMemory: ui.CommitmentWarning,
	}, {
		// The two dimensions are read separately: a deployment can be
		// fine on cores and out of memory, which is the one that costs
		// a run rather than slowing it.
		name:       "memory alone",
		cfg:        model.Config{MaxWorkers: 4, SandboxCPUs: 1, SandboxMemoryMB: 4096},
		wantCPU:    ui.CommitmentOK,
		wantMemory: ui.CommitmentWarning,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got := ui.NewSandboxCommitment(tc.cfg, host)
			if got == nil {
				t.Fatal("NewSandboxCommitment = nil, want a commitment")
			}
			if got.CPULevel != tc.wantCPU || got.MemoryLevel != tc.wantMemory {
				t.Fatalf("levels = %s/%s, want %s/%s", got.CPULevel, got.MemoryLevel, tc.wantCPU, tc.wantMemory)
			}
			// Level is the louder of the two, so one bad dimension is
			// enough to make the whole reading say something.
			if tc.wantCPU == ui.CommitmentWarning || tc.wantMemory == ui.CommitmentWarning {
				if got.Level != ui.CommitmentWarning {
					t.Errorf("Level = %s, want %s", got.Level, ui.CommitmentWarning)
				}
			}
			if got.Warned() != (got.Level != ui.CommitmentOK) {
				t.Errorf("Warned = %t with Level %s", got.Warned(), got.Level)
			}
		})
	}
}

// Nothing to multiply, and nothing to multiply it against, are both
// "say nothing" rather than a commitment of zero.
func TestSandboxCommitmentIsNilWithNothingToSay(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		cfg  model.Config
		host ui.HostCapacity
	}{
		{"no slots configured", model.Config{}, homeHost},
		{"no host reading", model.Config{MaxWorkers: 4}, ui.HostCapacity{}},
		{"host with no memory figure", model.Config{MaxWorkers: 4}, ui.HostCapacity{CPUs: 4}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ui.NewSandboxCommitment(tc.cfg, tc.host); got != nil {
				t.Fatalf("NewSandboxCommitment = %+v, want nil", got)
			}
		})
	}
	// And a nil commitment answers both of its own questions without
	// being dereferenced, since every caller holds one that may be nil.
	var none *ui.SandboxCommitment
	if none.Warned() || none.Summary() != "" {
		t.Errorf("nil commitment: Warned = %t, Summary = %q, want false and empty", none.Warned(), none.Summary())
	}
}

// Summary is what `grain settings` and the daemon's journal print, and
// what it has to carry is the numbers -- both totals, both host figures.
func TestSandboxCommitmentSummarySaysTheNumbers(t *testing.T) {
	t.Parallel()
	got := ui.NewSandboxCommitment(model.Config{
		MaxWorkers: 5, MaxMergers: 2, MaxCIJobs: 3, SandboxCPUs: 3, SandboxMemoryMB: 3072,
	}, homeHost).Summary()
	for _, want := range []string{"5 workers", "2 mergers", "3 CI jobs", "3 vCPU", "30 vCPU committed", "the 4 this host has", "7.5x", "30.0 of 15.7 GiB", "191%"} {
		if !strings.Contains(got, want) {
			t.Errorf("Summary = %q, want it to contain %q", got, want)
		}
	}
}

// And the uncapped line, which is the sentence a host-sandbox deployment
// never got told: the slots, the host they are on, and the fact that
// nothing between them caps anything.
func TestUncappedSandboxCommitmentSummarySaysTheNumbers(t *testing.T) {
	t.Parallel()
	got := ui.NewUncappedSandboxCommitment(model.Config{
		MaxWorkers: 5, MaxMergers: 2, MaxCIJobs: 3, SandboxCPUs: 3, SandboxMemoryMB: 3072,
	}, homeHost).Summary()
	for _, want := range []string{
		"5 workers + 2 mergers + 3 CI jobs", "10 concurrent runs", "no cpu or memory limit at all",
		"4 vCPU and 15.7 GiB", "2.5 runs per core",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Summary = %q, want it to contain %q", got, want)
		}
	}
	// Nothing that would read as a limit somebody set.
	for _, unwanted := range []string{"committed", "3 vCPU"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("Summary = %q, want no %q in it -- the shape sizes nothing here", got, unwanted)
		}
	}
}

// sandboxHostCapacity is a fixed reading standing in for
// pkg/cli/daemon.go's own /proc one: this package's half of the
// feature is the arithmetic and the seam, not the counting of cores.
func settingsClientOnHost(t *testing.T, host ui.HostCapacity, err error) (*ui.Client, context.Context) {
	t.Helper()
	c, _, ctx := testClient(t)
	c.Config.SandboxHostCapacity = func() (ui.HostCapacity, error) { return host, err }
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}
	return c, ctx
}

// seedDeploymentSlots gives this deployment the counts the commitment is
// computed from: its own max_workers, max_mergers and max_ci_jobs, the
// same three the daemon's journal states its commitment from. No team
// has counts of its own to add up.
func seedDeploymentSlots(t *testing.T, c *ui.Client, ctx context.Context, workers, mergers, ciJobs int) {
	t.Helper()
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil {
		t.Fatalf("reading the deployment's settings: %v", err)
	}
	cfg.MaxWorkers, cfg.MaxMergers, cfg.MaxCIJobs = workers, mergers, ciJobs
	if err := c.Store.PutConfig(ctx, *cfg); err != nil {
		t.Fatalf("setting the deployment's counts: %v", err)
	}
}

func TestSettingsReportsTheCommitmentAgainstTheSandboxHost(t *testing.T) {
	t.Parallel()
	c, ctx := settingsClientOnHost(t, homeHost, nil)
	seedDeploymentSlots(t, c, ctx, 5, 2, 0)
	cpus, memory := 3, 3072
	got, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{
		SandboxCPUs: &cpus, SandboxMemoryMB: &memory,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.SandboxCommitment == nil {
		t.Fatal("SandboxCommitment = nil, want the arithmetic for a deployment that overcommits its host")
	}
	if got.SandboxCommitment.Runs != 7 || got.SandboxCommitment.CPUsCommitted != 21 {
		t.Errorf("commitment = %+v, want 7 runs and 21 vCPU committed", got.SandboxCommitment)
	}
	// Recomputed on every read rather than carried from the save: the
	// host is what it is, and a settings row is not where this answer
	// comes from.
	read, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if read.SandboxCommitment == nil || read.SandboxCommitment.CPUsCommitted != 21 {
		t.Fatalf("GetSettings commitment = %+v, want the same 21 vCPU", read.SandboxCommitment)
	}
}

// Where the backend ignores the shape, the product of those two fields
// would contradict what the pane already says beside them
// (grain/task-9) -- but the slots are still real, and the deployment
// that has no shape is the one where a run is limited by nothing at all.
// So the pane gets the uncapped reading rather than silence
// (grain/task-285).
func TestSettingsReportsAnUncappedCommitmentWhereTheShapeIsIgnored(t *testing.T) {
	t.Parallel()
	c, ctx := settingsClientOnHost(t, homeHost, nil)
	c.Config.SandboxShapeIgnored = true
	seedDeploymentSlots(t, c, ctx, 5, 2, 0)
	cpus := 3
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{
		SandboxCPUs: &cpus,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.SandboxCommitment == nil {
		t.Fatal("SandboxCommitment = nil, want the runs this deployment puts on its host with nothing capping them")
	}
	if !got.SandboxCommitment.Uncapped || got.SandboxCommitment.Runs != 7 {
		t.Errorf("commitment = %+v, want 7 uncapped runs", got.SandboxCommitment)
	}
	// The stored shape is still stored, still shown, and still absent
	// from the sum: those are three different claims and only the last
	// one is this feature's.
	if got.SandboxCPUs != 3 || got.SandboxCommitment.CPUsCommitted != 0 {
		t.Errorf("sandbox cpus %d, committed %d: want the setting kept and nothing claimed committed out of it",
			got.SandboxCPUs, got.SandboxCommitment.CPUsCommitted)
	}
	if !got.SandboxCommitment.Warned() {
		t.Errorf("Warned = false at %v runs per core, want the deployment told", got.SandboxCommitment.RunsPerCPU)
	}
}

// A UI with no backend to speak for, and a host that cannot be read, are
// both silence rather than an error on a pane that is otherwise fine --
// the same nil-means-unavailable contract every other optional seam
// here gives.
func TestSettingsReportsNoCommitmentWithoutAHostReading(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings with no SandboxHostCapacity: %v", err)
	}
	if got.SandboxCommitment != nil {
		t.Fatalf("SandboxCommitment = %+v, want nil with no capacity seam wired", got.SandboxCommitment)
	}

	failing, ctx := settingsClientOnHost(t, ui.HostCapacity{}, errors.New("not on linux"))
	got, err = failing.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings with a failing capacity reading: %v", err)
	}
	if got.SandboxCommitment != nil {
		t.Fatalf("SandboxCommitment = %+v, want nil when the host could not be read", got.SandboxCommitment)
	}
}

// The pane reads this over HTTP, so the fields it renders have to
// survive the JSON -- an omitted key where there is nothing to say, and
// the levels spelled as the pane's own severities key off them.
func TestSettingsCommitmentOverHTTP(t *testing.T) {
	t.Parallel()
	c, ctx := settingsClientOnHost(t, homeHost, nil)
	seedDeploymentSlots(t, c, ctx, 5, 1, 0)
	cpus, memory := 3, 3072
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{
		SandboxCPUs: &cpus, SandboxMemoryMB: &memory,
	}); err != nil {
		t.Fatal(err)
	}
	srv := ui.NewServerWithClient(c)
	rec := do(t, srv, http.MethodGet, "/api/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/settings = %d, want 200: %s", rec.Code, rec.Body)
	}
	body := decode[struct {
		SandboxCommitment *struct {
			Runs          int     `json:"runs"`
			CPUsCommitted int     `json:"cpusCommitted"`
			HostCPUs      int     `json:"hostCpus"`
			CPURatio      float64 `json:"cpuRatio"`
			Level         string  `json:"level"`
		} `json:"sandboxCommitment"`
	}](t, rec)
	if body.SandboxCommitment == nil {
		t.Fatal("sandboxCommitment absent from GET /api/settings")
	}
	if body.SandboxCommitment.Runs != 6 || body.SandboxCommitment.CPUsCommitted != 18 {
		t.Errorf("commitment = %+v, want 6 runs (5 workers + the default merger) and 18 vCPU", body.SandboxCommitment)
	}
	if body.SandboxCommitment.HostCPUs != 4 || body.SandboxCommitment.Level != ui.CommitmentWarning {
		t.Errorf("commitment = %+v, want the host's 4 vCPU and level %q", body.SandboxCommitment, ui.CommitmentWarning)
	}
	if body.SandboxCommitment.CPURatio == 0 {
		t.Error("cpuRatio = 0, want the ratio the pane prints as \"4.5x\"")
	}
}

// And the uncapped commitment over the same wire: the pane renders a
// different sentence for it, so the flag it branches on and the ratio it
// prints both have to arrive.
func TestSettingsUncappedCommitmentOverHTTP(t *testing.T) {
	t.Parallel()
	c, ctx := settingsClientOnHost(t, homeHost, nil)
	c.Config.SandboxShapeIgnored = true
	seedDeploymentSlots(t, c, ctx, 5, 2, 0)
	rec := do(t, ui.NewServerWithClient(c), http.MethodGet, "/api/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/settings = %d, want 200: %s", rec.Code, rec.Body)
	}
	body := decode[struct {
		SandboxCommitment *struct {
			Uncapped    bool    `json:"uncapped"`
			Runs        int     `json:"runs"`
			HostCPUs    int     `json:"hostCpus"`
			RunsPerCPU  float64 `json:"runsPerCpu"`
			MemoryLevel string  `json:"memoryLevel"`
			Level       string  `json:"level"`
		} `json:"sandboxCommitment"`
	}](t, rec)
	if body.SandboxCommitment == nil {
		t.Fatal("sandboxCommitment absent from GET /api/settings on a shape-ignored deployment")
	}
	if !body.SandboxCommitment.Uncapped || body.SandboxCommitment.Runs != 7 || body.SandboxCommitment.HostCPUs != 4 {
		t.Errorf("commitment = %+v, want 7 uncapped runs on the host's 4 vCPU", body.SandboxCommitment)
	}
	if body.SandboxCommitment.RunsPerCPU == 0 || body.SandboxCommitment.Level != ui.CommitmentWarning {
		t.Errorf("commitment = %+v, want the runs-per-core ratio and level %q", body.SandboxCommitment, ui.CommitmentWarning)
	}
	// Omitted rather than sent as "ok": the pane must not draw a memory
	// reading nobody took.
	if body.SandboxCommitment.MemoryLevel != "" {
		t.Errorf("memoryLevel = %q, want it omitted", body.SandboxCommitment.MemoryLevel)
	}
}

// The pane's reserve field is blank for grain's own reserve, and says
// what blank comes to on this host rather than leaving the operator to
// work out an eighth of it (docs/test-runs/2026-10-03-deployment-operations.md).
func TestSettingsReportsTheDefaultMemoryReserveForThisHost(t *testing.T) {
	t.Parallel()
	c, ctx := settingsClientOnHost(t, homeHost, nil)
	got, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := model.DefaultReserveMemoryMB(homeHost.MemoryMB); got.SandboxMemoryReserveMBDefault != want {
		t.Errorf("SandboxMemoryReserveMBDefault = %d, want %d", got.SandboxMemoryReserveMBDefault, want)
	}

	unread, _, ctx := testClient(t)
	if _, err := unread.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}
	if got, err = unread.GetSettings(ctx); err != nil {
		t.Fatal(err)
	}
	if got.SandboxMemoryReserveMBDefault != 0 {
		t.Errorf("SandboxMemoryReserveMBDefault = %d with no host reading, want 0", got.SandboxMemoryReserveMBDefault)
	}
}
