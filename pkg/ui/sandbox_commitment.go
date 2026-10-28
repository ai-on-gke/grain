package ui

import (
	"fmt"

	"github.com/ai-on-gke/grain/pkg/granule"
	"github.com/ai-on-gke/grain/pkg/model"
)

// HostCapacity is how much machine the sandboxes this deployment builds
// have to share -- the total the arithmetic in SandboxCommitment below
// is held against, not a point-in-time reading of what is free right now
// (that is HostPressure, and it is a different question: a host with
// three runs finished and two live looks idle while still being
// configured to commit five times a sandbox's shape).
//
// Deliberately only the two dimensions of a sandbox's shape that are
// really shared. Disk is left out: a sandbox VM's disk is a qcow2
// overlay that grows as it is written rather than a slice taken out of
// the host up front, so max_workers overlays of 30 GiB on a 200 GiB
// volume is a normal deployment rather than an overcommitment -- what
// fills that volume is what the runs write, which the host status pane
// already reports as it happens (DiskUsage).
type HostCapacity struct {
	// CPUs is the number of logical processors the host has -- vCPUs on
	// a cloud instance, which is what a sandbox's own vCPU count is
	// committed against.
	CPUs int
	// MemoryMB is the machine's total physical memory, the MemTotal
	// figure HostPressure.MemoryTotalMB carries. Total, not available:
	// what is available fluctuates with the page cache and with whatever
	// is running, and a setting is a standing commitment against the
	// machine rather than against this second's free list.
	MemoryMB int
	// DiskGB is the disk the sandboxes' overlays are written to, where a
	// backend can say -- a cluster's allocatable ephemeral storage. Zero
	// on a local host, whose sandbox volume is read live instead
	// (model.Budget.Disk). Only the sandbox disk budget reads it.
	DiskGB int
}

// The three levels SandboxCommitment reports per dimension, worst last.
// A level rather than a bare boolean because the two ends of this are
// genuinely different messages: "this deployment is deliberately
// overcommitted, which is usually right for agent runs" and "this
// deployment has committed more than the machine has."
const (
	// CommitmentOK is a commitment with nothing to say about it.
	CommitmentOK = "ok"
	// CommitmentCaution is overcommitted enough to be worth seeing
	// beside the fields, and not by itself evidence of a problem.
	CommitmentCaution = "caution"
	// CommitmentWarning is overcommitted enough that runs are expected
	// to suffer for it -- see the thresholds below for the evidence.
	CommitmentWarning = "warning"
)

// The thresholds those three levels are drawn at. They are a judgement
// call, not a law: the evidence behind them is one deployment's own
// task_run history over 36.9 hours and 185 runs (grain-home task 141),
// on a 4 vCPU / 15.8 GiB host running sandboxes of 3 vCPU / 3072 MiB.
// Bucketing each run on its own time-weighted mean concurrency, the rate
// at which a run's test suite hit "Test timed out" went 0/3 below 3
// concurrent, 3/16 (19%) at 3-4.5, 13/43 (30%) at 4.5-6 and 18/34 (53%)
// at 6 and above -- median wall time 24.5m, 35.3m, 41.5m and 58.1m
// across the same buckets. In vCPU committed per core that is roughly
// 2.2x, 2.2-3.4x, 3.4-4.5x and 4.5x and up.
//
// So: some CPU overcommitment is right and a rule that refused it would
// be wrong. An agent run spends most of its wall time waiting on a model
// rather than on a core, and holding that deployment to workers x cpus
// <= cores would have capped it at 2-3 concurrent runs -- a real
// throughput cost paid to avoid contention that mostly does not
// materialise. Caution starts where that deployment's timeouts started
// appearing at all, and a warning where they had become the ordinary
// outcome.
const (
	cpuCautionRatio = 2.0
	cpuWarningRatio = 3.0
	// And where a run has no shape at all to multiply -- host
	// sandboxing, where a sandbox is a directory and the agent, the
	// clone, the build and the test suite run on this machine with no cpu
	// or memory limit of any kind (NewUncappedSandboxCommitment). The
	// only ratio left is runs per core, so these are the same buckets
	// read the other way round: that deployment's measurements are
	// concurrency figures on a 4-core host, and dividing them by its 4
	// cores puts the first timeouts at 3 concurrent (0.75 runs per core)
	// and the bucket where they were the ordinary outcome at 6 (1.5).
	//
	// Held to be at least as true uncapped as it was there: the runs
	// those buckets were measured over were 3-vCPU guests on 4 cores, a
	// cap that barely constrained one run and does not constrain a
	// directory at all. So an uncapped run contends for the same machine
	// with more of it in reach, and a threshold drawn from capped runs is
	// the generous end of the range rather than an alarmist one.
	runsPerCPUCaution = 0.75
	runsPerCPUWarning = 1.5
	// Memory is a different kind of wrong from CPU, and its thresholds
	// are tighter for it: contention for a core costs a run time, while
	// memory a host does not have costs it the run. The shares are of
	// the whole machine, so the headroom left over is everything not
	// committed here -- the daemon itself, the container runtime, each
	// VM's own qemu overhead, and the page cache all of those read
	// through. A tenth of a 16 GiB host is not much of that, which is
	// why the warning starts below the point where the arithmetic
	// exceeds the machine outright.
	memoryCautionShare = 0.75
	memoryWarningShare = 0.90
)

// SandboxCommitment is what a deployment's settings commit its sandbox
// host to when every slot it allows is full: max_workers + max_mergers
// and grain CI sandboxes, each of the deployment-wide shape, held against
// what the machine those sandboxes are built on actually has. Console
// sessions are not in it: no count bounds them (model.Limits), so there
// is no slot of theirs to multiply, and what a live one commits is on the
// ledger the budget is held against instead.
//
// Or, on a deployment whose sandboxes have no shape at all, the same
// question with the shape taken out of it: max_workers + max_mergers +
// max_ci_jobs runs concurrently on that machine, each unlimited (Uncapped
// below, and NewUncappedSandboxCommitment). That is not a lesser version
// of this arithmetic -- it is the deployment that commits the most,
// since nothing caps any of it -- and leaving it out was reading
// "max_workers commits nothing here" off a backend that had merely
// stopped writing the commitment down.
//
// It exists because that arithmetic is spread across two panes and no
// pane did it. max_workers and max_mergers are on Settings -> General;
// sandbox_cpus and sandbox_memory_mb are on Settings -> Sandbox; the
// host's own size is on the System pane, if an operator thinks to look.
// Each individual field is checked -- a shape a backend cannot honour is
// refused per task (grain/task-33) and annotated per deployment
// (grain/task-9) -- but every one of those checks asks whether *one*
// sandbox of this shape can exist. Nothing asked whether all of them can
// exist at once, which is the question the grain-home deployment got
// wrong: 5 workers x 3 vCPU on a 4-core host, and 15 GiB of a 15.8 GiB
// machine committed to guests before the daemon, the container runtime
// and the page cache had any (grain-home task 141).
//
// It reports the numbers rather than a verdict. "15 vCPU committed
// against the 4 this host has" is what lets somebody decide the
// trade-off they are making; a bare "oversubscribed" is not, since the
// right answer depends on what the runs on this deployment actually do
// with a core. The levels are how a pane decides how loudly to say it,
// not a substitute for saying it.
//
// A warning, never a refusal, in both dimensions. CPU because
// overcommitment is often the right call (see the thresholds above).
// Memory because grain's own backend does not reserve it either: a
// granule sandbox is a qemu guest given "-memory-mb" inside a docker
// container created with no memory limit of its own
// (orchestrator.GranuleConfig.createArgs), so guest memory is committed
// rather than allocated, and becomes resident only as the guest touches
// it. A deployment whose slots are rarely all full can genuinely run
// this way, and grain refusing to build the sixth sandbox on arithmetic
// done at save time would be inventing a limit the backend does not
// have.
type SandboxCommitment struct {
	// Workers, Mergers, and CIJobs are the three terms of Runs.
	Workers int `json:"workers"`
	Mergers int `json:"mergers"`
	CIJobs  int `json:"ciJobs"`
	// Runs is how many counted sandboxes may exist at once:
	// model.Limits.SandboxTotal. Workers, mergers, and CI jobs each get
	// a sandbox of their own, so a deployment with 5
	// workers, 2 mergers, and 3 CI jobs can have 11 live. Counting only agent runs would hide
	// the extra capacity MaxCIJobs deliberately adds (grain/task-296).
	Runs int `json:"runs"`
	// Uncapped says that those runs are not limited in cpu or memory at
	// all: the deployment is on host sandboxing, where a sandbox is a
	// directory on the daemon's own machine and a run's agent, clone,
	// build and test suite compete for the whole host with nothing
	// between them.
	//
	// It changes which of the fields below mean anything, and every
	// reader of one has to branch on it. The per-sandbox shape and the
	// two committed totals are 0, because there is no per-run figure to
	// multiply and reporting one would be inventing a limit nobody set;
	// so are CPURatio and MemoryShare. RunsPerCPU carries the reading
	// instead, and MemoryLevel is empty -- an unlimited run can take as
	// much of the machine as it touches, which is not a share this can
	// place.
	Uncapped bool `json:"uncapped,omitempty"`
	// CPUsPerSandbox and MemoryMBPerSandbox are the deployment-wide
	// shape each of those sandboxes is built at -- the stored setting
	// where there is one, and grain's own default shape where there is
	// not (granule.DefaultCPUs/DefaultMemoryMB), since that is the shape
	// a sandbox would really be built at either way.
	//
	// A per-task override (model.Task.SandboxCPUs) can make a single
	// sandbox larger than this, so the totals below are what the
	// deployment's own settings commit rather than a ceiling on what it
	// can ever ask for.
	CPUsPerSandbox     int `json:"cpusPerSandbox"`
	MemoryMBPerSandbox int `json:"memoryMbPerSandbox"`
	// CPUsCommitted and MemoryMBCommitted are Runs times each of those.
	//
	// The CI slots in that product are a floor rather than a bound: a CI
	// job names its own `sandbox:` in its workflow file, and on this
	// repository they ask for 16 GiB against an 8 GiB agent sandbox
	// (grain/task-197). What holds them -- and the agent runs beside them
	// -- is Budget below, which every admission on this host is tested
	// against (grain/task-28).
	CPUsCommitted     int `json:"cpusCommitted"`
	MemoryMBCommitted int `json:"memoryMbCommitted"`
	// HostCPUs and HostMemoryMB are what the machine those sandboxes are
	// built on has -- Config.SandboxHostCapacity's own answer, and the
	// only figures here this package did not compute.
	HostCPUs     int `json:"hostCpus"`
	HostMemoryMB int `json:"hostMemoryMb"`
	// CPURatio is CPUsCommitted over HostCPUs (3.75 where 15 vCPU are
	// committed on a 4-core host) and MemoryShare is MemoryMBCommitted
	// over HostMemoryMB (0.95 for 15 GiB of 15.8). Carried as the
	// numbers rather than as formatted text so each caller renders them
	// in its own idiom -- the pane says "3.8x" and "95%", `grain
	// settings` says the same thing on one line. Both 0 when Uncapped,
	// where there is no committed total to divide.
	CPURatio    float64 `json:"cpuRatio"`
	MemoryShare float64 `json:"memoryShare"`
	// RunsPerCPU is Runs over HostCPUs (1.75 where 7 runs share 4 cores),
	// and is the whole cpu reading of an Uncapped commitment -- the one
	// ratio a deployment that caps nothing still has. 0 on a shaped
	// commitment, where CPURatio says the stronger thing: how much vCPU
	// was promised out, rather than how many runs are in the room.
	RunsPerCPU float64 `json:"runsPerCpu,omitempty"`
	// CPULevel and MemoryLevel are each dimension's own reading, and
	// Level is the worse of the two -- what a pane deciding on one
	// severity for one banner reads, so that the two ends of the same
	// arithmetic cannot disagree about how loud to be.
	//
	// CPULevel is read off RunsPerCPU rather than CPURatio when Uncapped,
	// against thresholds of its own; MemoryLevel is empty there, and
	// Level is the cpu reading alone.
	CPULevel    string `json:"cpuLevel"`
	MemoryLevel string `json:"memoryLevel,omitempty"`
	Level       string `json:"level"`
	// Budget is the deployment's own sandbox budget as the settings that
	// set it describe it -- what is promised, what is held back, what the
	// agent pool reserves and what that leaves grain CI (its own type
	// below). nil where there is no budget to report: a deployment whose
	// ratios leave both dimensions unbounded has nothing here, and the
	// count arithmetic above is then the whole of what can be said.
	//
	// It is what makes the numbers above a bound rather than an
	// observation. Before the budget existed (grain/task-173) every
	// figure here was what the settings *would* commit if the counts were
	// all full, with nothing stopping them; now the deployment refuses to
	// build the sandbox that would exceed this, so a pane showing the
	// commitment without showing the budget it is held inside is showing
	// half the sentence.
	Budget *SandboxBudget `json:"budget,omitempty"`
}

// SandboxBudget is model.Budget said in the terms the Settings pane
// shows beside the three fields that set it -- the arithmetic of
// grain/task-197's editable overcommit ratios and memory reserve.
//
// Everything here is computed from one model.Budget and one model.Limits
// rather than restated, so the pane and the gate that actually admits a
// job cannot disagree about what this deployment promises.
type SandboxBudget struct {
	// CPUOvercommit and MemoryOvercommit are the stored ratios, and
	// ReserveMemoryMB the reserve *resolved* against this host -- a
	// deployment that has chosen no reserve stores a negative number
	// meaning "an eighth of the machine", and what a pane has to show is
	// the MiB that came to here (ReserveIsDefault says which it was).
	CPUOvercommit    float64 `json:"cpuOvercommit"`
	MemoryOvercommit float64 `json:"memoryOvercommit"`
	// DiskOvercommit is the stored disk ratio (model.Budget.DiskOvercommit).
	DiskOvercommit   float64 `json:"diskOvercommit"`
	ReserveMemoryMB  int     `json:"reserveMemoryMb"`
	ReserveIsDefault bool    `json:"reserveIsDefault,omitempty"`
	// CPUs and MemoryMB are the totals those settings promise: host vCPU
	// times the cpu ratio, and the host's memory less the reserve times
	// the memory ratio. Each is meaningless unless its *Bounded flag is
	// set -- a ratio of zero leaves that dimension to the run counts
	// alone, which is a configuration an operator can deliberately want
	// and is not the same as a budget of nothing.
	CPUs          float64 `json:"cpus"`
	CPUsBounded   bool    `json:"cpusBounded"`
	MemoryMB      int     `json:"memoryMb"`
	MemoryBounded bool    `json:"memoryBounded"`
	// DiskGB is the sandbox disk that ratio lets be promised, against the
	// disk this host reading reports (a cluster's allocatable ephemeral
	// storage) less the floor. Only bounded where the reading had a disk
	// in it: a local host's volume is read live at each admission
	// instead, and is reported by the disk floor rather than here.
	DiskGB      int  `json:"diskGb,omitempty"`
	DiskBounded bool `json:"diskBounded,omitempty"`
	// Mode is the overcommit mode (model.OvercommitFixed or
	// model.OvercommitAuto). In auto mode the two ratios above are the
	// controller's effective ones, and Overcommit is its state: the live
	// ratios, the last host reading, recent adjustments and OOM kills.
	// Overcommit is nil on a UI with no controller to ask.
	Mode       string                 `json:"mode,omitempty"`
	Overcommit *model.OvercommitState `json:"overcommit,omitempty"`
	// There is deliberately nothing here for either pool's share of that
	// total, and there used to be four fields: the agent pool's standing
	// reservation (every configured slot at the deployment shape, held
	// whether or not a run was in it) and what that reservation left grain
	// CI. Neither exists (grain/task-28). Agent runs and CI jobs are
	// admitted against the one budget above, charged at the shapes they
	// actually hold, so the whole of it is every pool's share and a pane
	// splitting it would be inventing the reservation back.
	//
	// What is *live* against it is not a setting and is not reported here:
	// it moves every time a run starts or a job finishes, and it is said
	// where somebody is waiting on it -- in the sentence that refuses a
	// job or holds a dispatch (model.Budget's own refusals, which report
	// the live commitment against this total), and on the sandbox health
	// pane, which lists the sandboxes themselves.
}

// sandboxCommitment is Settings.SandboxCommitment's own computation: the
// stored settings, the shape they resolve to, and what host there is to
// hold them -- or nil where there is nothing honest to say.
//
// nil, rather than a zero struct or an error, for each of the ways this
// question does not arise:
//
//   - no Config.SandboxHostCapacity, which is a UI not colocated with a
//     backend whose host it can speak for -- the same
//     nil-means-unavailable contract every other optional seam gives;
//   - a capacity reading that failed (a daemon not on Linux has no
//     /proc to count), where a pane showing "0 vCPU" would be worse than
//     a pane showing nothing;
//   - nothing configured yet, where max_workers is 0 and no sandbox
//     would be built at all.
//
// A deployment whose sandbox shape is ignored entirely
// (Config.SandboxShapeIgnored) is not one of them, though it was: the
// two shape numbers size nothing there, so multiplying them would be
// arithmetic about a fiction, but max_workers is not a fiction on any
// backend. What that deployment gets is the uncapped commitment instead
// -- the same slots, counted against the same host, with the shape left
// out of the sentence rather than the sentence left out.
//
// Shape-ignored is read here as "the runs are uncapped", which is what
// it means for the only backend that can reach this code: the pair of
// facts needed to be here at all is a host this daemon can speak for
// (SandboxHostCapacity, wired in pkg/cli/daemon.go for the two
// backends that build sandboxes on this machine) and no shape applied to
// it, and that pair is orchestrator.HostSandboxes, whose sandbox is a
// directory with no limit of any kind. A future backend that ignored the
// deployment shape while imposing one of its own would make that reading
// wrong -- and would have to come to sandboxHostCapacity to be reported
// at all, which is where it would say so.
func (c *Client) sandboxCommitment(cfg model.Config) *SandboxCommitment {
	host, ok := c.sandboxHostCapacity()
	if !ok {
		return nil
	}
	// The counts are the deployment's own settings, the same ones the
	// daemon's journal states its commitment from
	// (NewSandboxCommitment's other caller). No team has counts of its
	// own to add up any more.
	// A deployment with CI disabled stores the setting for a future
	// enable but has no CI engine that could consume those slots.
	if c.Config.CI == nil {
		cfg.MaxCIJobs = 0
	}
	// In auto overcommit mode the budget a sandbox is admitted against is
	// the controller's, not the stored ratios, so that is what the pane
	// shows -- with the controller's own state beside it.
	var overcommit *model.OvercommitState
	if c.Config.Overcommit != nil {
		st := c.Config.Overcommit()
		overcommit = &st
		if cfg.SandboxOvercommit.Auto() && cfg.SandboxOvercommit.Validate() == nil {
			cfg.SandboxCPUOvercommit, cfg.SandboxMemoryOvercommit = st.CPUOvercommit, st.MemoryOvercommit
		}
	}
	var out *SandboxCommitment
	if c.Config.SandboxShapeIgnored {
		out = NewUncappedSandboxCommitment(cfg, host)
	} else {
		out = NewSandboxCommitment(cfg, host)
	}
	if out != nil && out.Budget != nil {
		out.Budget.Mode = cfg.SandboxOvercommit.Resolved().Mode
		out.Budget.Overcommit = overcommit
	}
	return out
}

// sandboxHostCapacity is the machine this process can speak for, and
// whether it can speak for one at all -- the two failures the commitment
// above treats alike (a UI not colocated with a backend, and a reading
// that failed), in one place, because the settings that size a budget
// have to be validated against the same host the budget is built on.
func (c *Client) sandboxHostCapacity() (HostCapacity, bool) {
	if c.Config.SandboxHostCapacity == nil {
		return HostCapacity{}, false
	}
	host, err := c.Config.SandboxHostCapacity()
	if err != nil {
		return HostCapacity{}, false
	}
	return host, true
}

// sandboxMemoryReserveDefault is the reserve grain keeps back on this
// host when nobody has chosen one -- what model.NewBudget resolves a
// negative SandboxMemoryReserveMB to -- or 0 where there is no host to
// size it against.
func (c *Client) sandboxMemoryReserveDefault() int {
	host, ok := c.sandboxHostCapacity()
	if !ok {
		return 0
	}
	return model.DefaultReserveMemoryMB(host.MemoryMB)
}

// NewSandboxCommitment is the arithmetic itself, split out from the
// seams above so it can be read -- and tested -- as the pure function it
// is. nil for a deployment or a host with no whole question to ask.
//
// Exported for the one caller outside this package that has the same two
// operands and no HTTP request to answer: cmd/grain's daemon, which says
// the same thing in its journal at startup and each time a merged
// settings change reaches it (logSandboxCommitment). One computation,
// so a pane and a journal cannot disagree about what a deployment is
// committing.
func NewSandboxCommitment(cfg model.Config, host HostCapacity) *SandboxCommitment {
	c := newSandboxCommitment(cfg, host)
	if c == nil {
		return nil
	}
	cpus, memoryMB := cfg.SandboxCPUs, cfg.SandboxMemoryMB
	if cpus <= 0 {
		cpus = granule.DefaultCPUs
	}
	if memoryMB <= 0 {
		memoryMB = granule.DefaultMemoryMB
	}
	c.CPUsPerSandbox, c.MemoryMBPerSandbox = cpus, memoryMB
	shape := model.Commitment{CPUs: cpus, MemoryMB: memoryMB}
	c.Budget = newSandboxBudget(cfg, host, c, shape)
	// Every slot the teams allow, at this deployment's own shape. It used
	// to be the agent slots at that shape plus CI's *share* of the budget
	// -- the total less a standing reservation for the agent pool -- and
	// there is no share and no reservation now (grain/task-28): both
	// pools are admitted against the whole budget, so reporting each
	// one's slice of it would either invent the reservation back or count
	// the same gigabyte twice.
	//
	// The CI half of this product is a floor rather than a bound, and
	// says so on the field: a job names its own sandbox in its workflow
	// file, and on this repository they ask for twice the agent shape.
	// What actually holds them is Budget below, which is the figure their
	// gate admits each one against.
	committed := shape.Times(c.Runs)
	c.CPUsCommitted, c.MemoryMBCommitted = committed.CPUs, committed.MemoryMB
	c.CPURatio = float64(c.CPUsCommitted) / float64(host.CPUs)
	c.MemoryShare = float64(c.MemoryMBCommitted) / float64(host.MemoryMB)
	c.CPULevel = commitmentLevel(c.CPURatio, cpuCautionRatio, cpuWarningRatio)
	c.MemoryLevel = commitmentLevel(c.MemoryShare, memoryCautionShare, memoryWarningShare)
	c.Level = worseCommitmentLevel(c.CPULevel, c.MemoryLevel)
	return c
}

// NewUncappedSandboxCommitment is the same arithmetic for a deployment
// whose sandboxes have no shape at all -- host sandboxing, where a
// sandbox is a directory on the daemon's own machine
// (orchestrator.HostSandboxes) and a run's agent, clone, build and test
// suite get whatever of the host they can take.
//
// A separate constructor rather than a flag on the one above, because
// what is honest to say is different rather than smaller and the caller
// has to know which it is asking for. There is no per-run figure here,
// so there is no committed total: "7 x 3 vCPU = 21 vCPU committed" has
// no counterpart, and inventing one out of the stored shape would report
// a limit this backend does not apply -- the exact misreading the pane
// already annotates those fields to prevent (Config.SandboxShapeIgnored).
//
// What survives the shape's removal is the thing that was doing most of
// the work anyway: how many runs are on the machine at once. That is
// max_workers + max_mergers + max_ci_jobs over
// the host's cores
// (RunsPerCPU), and it
// is enough to say the sentence a host-sandbox deployment never got
// told -- "7 concurrent runs on 4 vCPU, each unlimited" -- which is the
// same deployment, at the same settings, that the evidence behind
// runsPerCPUWarning was measured on.
//
// nil on the same nothing-to-say cases as above: no host reading, and no
// slots configured.
func NewUncappedSandboxCommitment(cfg model.Config, host HostCapacity) *SandboxCommitment {
	c := newSandboxCommitment(cfg, host)
	if c == nil {
		return nil
	}
	c.Uncapped = true
	// The budget still applies to this deployment even though nothing
	// here has a shape to charge: CI jobs name their own `sandbox:`, and
	// the gate holds those against it. The agent side of it reserves
	// nothing, which is what SandboxBudget.AgentCPUs' doc comment says
	// out loud.
	c.Budget = newSandboxBudget(cfg, host, c, model.Commitment{})
	c.RunsPerCPU = float64(c.Runs) / float64(host.CPUs)
	c.CPULevel = commitmentLevel(c.RunsPerCPU, runsPerCPUCaution, runsPerCPUWarning)
	// No memory reading at all, rather than a lenient one: MemoryLevel is
	// left empty and Level is the cpu reading alone. A run with no memory
	// limit is not committing a share of the host that can be added up in
	// advance -- what it takes is what it touches -- and the honest thing
	// to say about that is the host's own total, which Summary and the
	// pane print as context beside the run count.
	c.Level = c.CPULevel
	return c
}

// newSandboxCommitment is the half both constructors share: the slots a
// deployment allows, and the machine they are held against. nil where
// there is no whole question -- a host that could not be counted, or a
// deployment that would build no sandbox at all.
func newSandboxCommitment(cfg model.Config, host HostCapacity) *SandboxCommitment {
	if host.CPUs <= 0 || host.MemoryMB <= 0 {
		return nil
	}
	workers, mergers := max(cfg.MaxWorkers, 0), max(cfg.MaxMergers, 0)
	ciJobs := max(cfg.MaxCIJobs, 0)
	runs := workers + mergers + ciJobs
	if runs <= 0 {
		return nil
	}
	return &SandboxCommitment{
		Workers:      workers,
		Mergers:      mergers,
		CIJobs:       ciJobs,
		Runs:         runs,
		HostCPUs:     host.CPUs,
		HostMemoryMB: host.MemoryMB,
	}
}

// newSandboxBudget is the deployment's budget, as its settings and this
// host reading make it -- model.NewBudget's own arithmetic, so nothing
// here is a second definition of what an admission is held against. The
// whole of it, with nothing taken off the top for either pool: that is
// the one deployment-wide capacity control there is (grain/task-28).
//
// nil where the ratios bound neither dimension, which is a deployment
// that has deliberately (or, on a row written before these columns
// existed and never migrated, accidentally) left the run counts as the
// whole limit. There is no budget to show there, and showing a zero one
// would read as "nothing may run".
func newSandboxBudget(cfg model.Config, host HostCapacity, c *SandboxCommitment, shape model.Commitment) *SandboxBudget {
	budget := model.NewBudget(
		model.Commitment{CPUs: host.CPUs, MemoryMB: host.MemoryMB},
		cfg.SandboxMemoryReserveMB, cfg.SandboxCPUOvercommit, cfg.SandboxMemoryOvercommit)
	budget.Host.DiskGB = host.DiskGB
	budget.DiskOvercommit = cfg.SandboxDiskOvercommit
	budget.DiskFloor = model.DiskFloor{Share: cfg.SandboxDiskFloorShare, MinMB: cfg.SandboxDiskFloorMinMB}
	if !budget.Known() {
		return nil
	}
	out := &SandboxBudget{
		CPUOvercommit:    cfg.SandboxCPUOvercommit,
		MemoryOvercommit: cfg.SandboxMemoryOvercommit,
		DiskOvercommit:   cfg.SandboxDiskOvercommit,
		ReserveMemoryMB:  budget.ReserveMemoryMB,
		ReserveIsDefault: cfg.SandboxMemoryReserveMB < 0,
	}
	out.CPUs, out.CPUsBounded = budget.CPUBudget()
	out.MemoryMB, out.MemoryBounded = budget.MemoryBudgetMB()
	out.DiskGB, out.DiskBounded = budget.DiskBudgetGB()
	return out
}

// commitmentLevel places one dimension's own figure against its two
// thresholds.
func commitmentLevel(value, caution, warning float64) string {
	switch {
	case value >= warning:
		return CommitmentWarning
	case value >= caution:
		return CommitmentCaution
	}
	return CommitmentOK
}

// worseCommitmentLevel is the louder of two levels --
// SandboxCommitment.Level.
func worseCommitmentLevel(a, b string) string {
	for _, l := range []string{CommitmentWarning, CommitmentCaution} {
		if a == l || b == l {
			return l
		}
	}
	return CommitmentOK
}

// Warned reports whether this commitment is worth saying out loud at all
// -- anything past CommitmentOK, and false for the nil commitment a
// deployment with nothing to say about it reports. The one place that
// decides it, so a pane, `grain settings` and the daemon's own startup
// line cannot drift apart on when they speak.
func (c *SandboxCommitment) Warned() bool {
	return c != nil && c.Level != CommitmentOK
}

// Summary is this commitment as one line of prose, for the callers with
// no layout to lay the numbers out in: `grain settings` and the daemon's
// own startup log. The pane formats the fields itself.
//
// It says the arithmetic and not a verdict, deliberately -- "5 workers +
// 2 mergers + 3 CI jobs x 3 vCPU = 30 vCPU committed against the 4 this
// host has (7.5x); 30.0 of 15.7 GiB memory (190%)" -- because the trade-off is
// the operator's to make and the numbers are what it takes to make it.
//
// An Uncapped commitment says the same thing with no product in it: "5
// workers + 2 mergers + 3 CI jobs = 10 concurrent runs, each with no cpu or memory
// limit at all, on a host with 4 vCPU and 15.7 GiB (1.8 runs per core)".
// The host's two figures are still there, as the context the run count
// means nothing without, but nothing is claimed to be committed out of
// them -- what those runs take is what they touch.
func (c *SandboxCommitment) Summary() string {
	if c == nil {
		return ""
	}
	if c.Uncapped {
		return fmt.Sprintf("%d workers + %d mergers + %d CI jobs = %d concurrent runs, each with no cpu or memory limit at all, "+
			"on a host with %d vCPU and %s GiB (%.1f runs per core)",
			c.Workers, c.Mergers, c.CIJobs, c.Runs, c.HostCPUs, gibibytes(c.HostMemoryMB), c.RunsPerCPU)
	}
	tail := fmt.Sprintf("%d vCPU committed against the %d this host has (%.1fx); %s of %s GiB memory (%.0f%%)",
		c.CPUsCommitted, c.HostCPUs, c.CPURatio, gibibytes(c.MemoryMBCommitted), gibibytes(c.HostMemoryMB), c.MemoryShare*100)
	// Where a budget bounds both dimensions, the budget is said rather
	// than either pool's share of it: there are no shares (grain/task-28).
	// Every sandbox on this host -- each team's agent runs and every CI
	// job, at whatever shape it actually holds -- is admitted against the
	// one total, so what an operator needs beside the counts is that
	// total and the arithmetic it came out of. A deployment with no
	// budget, or with only one dimension bounded, keeps the one sentence
	// it has always had.
	if b := c.Budget; b != nil && b.CPUsBounded && b.MemoryBounded {
		return fmt.Sprintf("%d workers + %d mergers + %d CI jobs, "+
			"sharing a %.0f vCPU / %s GiB sandbox budget "+
			"(%s GiB host less %s GiB reserved, at %.2fx cpu / %.2fx memory): %s",
			c.Workers, c.Mergers, c.CIJobs,
			b.CPUs, gibibytes(b.MemoryMB),
			gibibytes(c.HostMemoryMB), gibibytes(b.ReserveMemoryMB), b.CPUOvercommit, b.MemoryOvercommit, tail)
	}
	return fmt.Sprintf("%d workers + %d mergers + %d CI jobs x %d vCPU = %s",
		c.Workers, c.Mergers, c.CIJobs, c.CPUsPerSandbox, tail)
}

// gibibytes renders a MiB figure as the GiB an operator sizing a host
// thinks in, to one decimal place: memory settings are in MiB and
// machines are sold in GiB, and a line mixing the two would be leaving
// the conversion to be done in the reader's head.
func gibibytes(mb int) string {
	return fmt.Sprintf("%.1f", float64(mb)/1024)
}
