package orchestrator

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwsalmon/grain/pkg/granule"
	"github.com/bwsalmon/grain/pkg/model"
)

// SandboxHealth is one live sandbox's status, as HostSandboxes' and
// GranuleSandboxes' own Health methods report it (bwsalmon/agents#536:
// the UI's System pane needs to see which sandbox a stuck or failing
// run is actually sitting on, and whether that sandbox itself looks
// healthy).
// pkg/cli/daemon.go is the only place this type and ui.SandboxSnapshot
// are ever both in scope -- see that file's own sandboxHealthAdapter doc
// comment for why this package does not import ui to spare a caller that
// conversion.
//
// It reports *live* sandboxes, which is a change of meaning rather than
// of name: it used to be one row per dispatch slot, always the same rows,
// each either holding a run or idle. A sandbox now exists only while a
// run does, so an idle deployment reports nothing at all, and every row
// here belongs to something actually running.
type SandboxHealth struct {
	// Sandbox is the sandbox's own name -- the run's ID (dispatch.RunID),
	// which is also what model.Run.Sandbox records, so a row here joins
	// straight onto the run it belongs to.
	Sandbox string
	// Kind names the holder: "ci" for CI's letter-prefixed sandbox names,
	// and "agent" for task-run sandboxes.
	Kind string
	// Backend names which Sandboxes implementation produced this entry --
	// "host" for HostSandboxes, "granule" for GranuleSandboxes -- since a
	// deployment only ever runs one of the two (`grain daemon`'s own
	// -granule-sandboxes flag picks between them), but the pane showing
	// this has no other way to know which.
	Backend string
	// Name is the sandbox's own identity within Backend -- the directory
	// on disk, or the granule VM name.
	Name string
	// CPUs, MemoryMB and DiskGB are the shape assigned to this sandbox.
	// They are zero for a backend without a separately configured shape
	// (HostSandboxes), where the host figures are the useful answer.
	CPUs, MemoryMB, DiskGB int
	// NetworkAccess is the normalized network policy assigned at acquire
	// time: "open" or "none".
	NetworkAccess string
	// Ready is true once this sandbox has been confirmed reachable -- for
	// HostSandboxes that is just "the directory exists"; for
	// GranuleSandboxes it means the VM's guest answered a command just
	// now, not merely that `granule vm create` once succeeded for it.
	Ready bool
	// Error is set instead of Ready when the sandbox could not be reached
	// just now -- the guest not answering. Empty when Ready is true.
	Error string
	// LoadAverage is the sandbox's own "/proc/loadavg" 1/5/15-minute
	// figures, space-joined verbatim, or empty when this backend has no
	// way to read it (HostSandboxes: nothing separates its own usage from
	// the host daemon's, which ui.Config.HostStats already reports).
	LoadAverage string
	// MemoryUsedMB and MemoryTotalMB are the sandbox's own memory, read
	// the same way ui.HostPressure's are for the daemon's host -- 0/0
	// when unavailable.
	MemoryUsedMB, MemoryTotalMB int
	// SwapUsedMB and SwapTotalMB are the sandbox's swap usage, from
	// /proc/meminfo's SwapTotal minus SwapFree -- 0/0 when the guest has
	// no swap configured or the reading is unavailable.
	SwapUsedMB, SwapTotalMB int
	// DiskUsedMB and DiskTotalMB are the sandbox's own root filesystem,
	// in MiB, from `df` inside the guest -- 0/0 when unavailable, the
	// same as the memory pair above.
	//
	// Reported in MiB rather than in the GiB
	// model.Config.SandboxDiskGB is expressed in, so that this stays a
	// plain integer at the sizes a sandbox actually reaches: the useful
	// reading is "3 GiB of 20 used", and a GiB-granular integer would
	// round the interesting half of that to zero. It is the figure the
	// disk-size setting is there to move -- a run that fails part way
	// through a build is often a guest that has simply filled its root
	// filesystem, which nothing in this pane could previously show.
	DiskUsedMB, DiskTotalMB int
	// ProbeMS is how long the probe that produced this reading took, in
	// milliseconds -- 0 for a backend that runs no probe at all
	// (HostSandboxes, whose whole check is "the directory exists").
	//
	// It is here because Error on its own cannot tell "down" from
	// "slow", and those want different things from an operator. A guest
	// that answered in 40 ms and a guest that was given the whole
	// healthTimeout and never answered produce rows that differ in one
	// boolean; a guest answering in eight seconds under a loaded host
	// looks, in that boolean, exactly like a guest answering instantly.
	// The duration is the reading that separates them, and it is free --
	// the probe is already timed by the budget it runs under.
	//
	// It is a fact about the probe, not about this sweep: a row a sick
	// guest's breaker answered from memory (healthBreaker) carries the
	// duration of the probe that reading came from, since that probe is
	// what the row reports. ProbeAgeMS below is how far back that was.
	ProbeMS int64
	// ProbeAgeMS is how much older than this sweep the probe behind this
	// row is, in milliseconds -- 0 on an ordinary row, whose probe ran
	// in this sweep, and 0 for a backend that runs no probe at all.
	//
	// It is non-zero only for a row answered from a guest's breaker
	// (healthBreaker), which repeats a probe of up to
	// healthBreakerCooldown ago without running one. Nothing else in the
	// response says so, and the sweep's own age cannot: a remembered row
	// is precisely what makes a sweep *fast*, so the sweep carrying it
	// comes back promptly and unmarked (ui.SandboxReading.Stale is
	// false) while the row inside it is up to fifteen seconds behind a
	// guest that has already recovered. A pane showing this row's Error
	// and ProbeMS wants to be able to say "as of twelve seconds ago"
	// rather than implying both were measured just now.
	ProbeAgeMS int64
	// NestedVirt is whether this sandbox can itself run virtual machines
	// -- one of the NestedVirt* constants below, or empty when the
	// backend has no way to answer (HostSandboxes, whose "sandbox" is a
	// directory on the host and so has whatever the host has).
	//
	// A granule sandbox is a cloud-hypervisor guest, so a VM started
	// inside one is a nested VM and needs three things at once: the
	// machine underneath to be running KVM with nesting enabled
	// (sysstat.NestedVirtualization reports that half, on the host
	// section of the same pane), cloud-hypervisor to pass the CPU's
	// virtualization flag through to the guest, and the guest to have
	// loaded its own kvm module and let the account tools run as open
	// /dev/kvm. Only the last of those is visible from inside the
	// sandbox, and it is the one that is visible from *exactly* here --
	// which is why this is a per-sandbox reading rather than a
	// deployment-wide setting the pane could show without asking.
	NestedVirt string
	// StartedAt is when this backend acquired the sandbox -- the moment
	// it went into the backend's live set, which is after its VM, pod,
	// container or directory was ready rather than when building it
	// began. A sandbox readopted after a daemon restart reports the
	// readopt, since nothing the backends keep says when the previous
	// process built it. Zero when the backend does not know.
	//
	// The System pane reads it to say how long each sandbox has been
	// held, and to name the longest-running one among the CI rows it
	// folds behind a summary.
	StartedAt time.Time
}

// The values SandboxHealth.NestedVirt takes, in the order they narrow:
// each one is the answer when everything the next one asks about is
// already true.
//
// They are deliberately four values rather than a boolean, because the
// three failures want different actions from an operator and read
// identically as "false": a guest with no CPU flag needs the host's own
// KVM nesting turned on (or a cloud-hypervisor that stops masking it), a
// guest with no /dev/kvm needs its kvm module loaded, and a guest whose
// device is there but unreadable needs the exec account in the "kvm"
// group -- which is what the repository's Granulefile grants, and the
// state every sandbox built before it did was in.
const (
	// NestedVirtReady: /dev/kvm is present in the guest and the account
	// this sandbox's tools run as can open it for reading and writing,
	// which is all a VMM inside the sandbox needs from the kernel.
	NestedVirtReady = "ready"
	// NestedVirtDenied: /dev/kvm is there but that account cannot open
	// it -- the device is root:kvm 0660, so this means "not in the kvm
	// group". A state a live sandbox should no longer be found in:
	// GranuleSandboxes.ensureGuestKVMAccess grants the group at acquire
	// time on any guest whose image did not, so reaching this pane means
	// that grant itself failed and said why in the daemon's log.
	NestedVirtDenied = "denied"
	// NestedVirtNoDevice: the guest's CPU offers vmx/svm but there is no
	// /dev/kvm, i.e. nothing loaded kvm_intel/kvm_amd. udev autoloads
	// them off the CPU's own feature bits on a systemd guest, so this is
	// mostly a guest image built without them.
	NestedVirtNoDevice = "no-device"
	// NestedVirtUnsupported: no vmx/svm in the guest's /proc/cpuinfo at
	// all. The flag is masked by the layer below rather than by anything
	// in the guest -- an unnested host kernel, or a cloud-hypervisor
	// asked for "--cpus nested=off".
	NestedVirtUnsupported = "unsupported"
)

func sandboxKind(name string) string {
	if strings.HasPrefix(name, "c") {
		return "ci"
	}
	return "agent"
}

// collectSandboxHealth probes every named sandbox at once while keeping the
// caller's stable name order in the returned slice. A probe is independently
// bounded by healthTimeout, so doing these serially made GET /api/sandboxes'
// latency grow by five seconds for every guest that was slow or unreachable.
// With enough live sandboxes that crossed the UI's upstream HTTP timeout and
// surfaced as "Failed to fetch", even though a later poll commonly worked.
//
// Each goroutine owns one result slot. The probe implementations only read the
// live-sandbox snapshots their callers took under lock, so no shared result or
// backend state is mutated here.
func collectSandboxHealth(names []string, probe func(string) SandboxHealth) []SandboxHealth {
	out := make([]SandboxHealth, len(names))
	var wg sync.WaitGroup
	wg.Add(len(names))
	for i, name := range names {
		go func() {
			defer wg.Done()
			out[i] = probe(name)
		}()
	}
	wg.Wait()
	return out
}

// SandboxCount returns the number of sandboxes currently held by live runs
// without probing any of them for the resource readings Health returns.
func (h *HostSandboxes) SandboxCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.live)
}

// AgentSandboxCount is SandboxCount narrowed to the agent half: how many
// of those sandboxes belong to agent runs rather than to CI jobs, by the
// same name prefix the health rows are labelled with (sandboxKind).
//
// It exists for the sentence a CI job gets when the budget refuses it.
// CI is admitted against the deployment's budget less the agent pool's
// standing reservation (model.Limits.CIBudget), and a job told only that
// the budget is full cannot tell a reservation being used from one
// being held for nothing -- which on the night of grain/task-506 meant
// working out that six idle agent sandboxes held 24 vCPU took reading
// the health pane sandbox by sandbox. Counting keys in a map already
// under this mutex is cheap enough for the gate to ask on every
// admission attempt; probing the guests is not, which is why this is
// beside SandboxCount rather than inside Health.
func (h *HostSandboxes) AgentSandboxCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for name := range h.live {
		if sandboxKind(name) == "agent" {
			n++
		}
	}
	return n
}

// Health implements the same shape GranuleSandboxes.Health does: one entry
// per sandbox currently held by a live run, in name order. A
// HostSandboxes directory has no isolation from the host daemon runs on
// (this package's own doc comment), so there is no separate CPU/memory
// reading to give it beyond "yes, its directory exists" -- a deployment
// running HostSandboxes gets its host-wide pressure from
// ui.Config.HostStats instead, which is exactly the number a starved
// sandbox would actually be contending over.
func (h *HostSandboxes) Health(ctx context.Context) []SandboxHealth {
	h.mu.Lock()
	names := make([]string, 0, len(h.live))
	sandboxes := make(map[string]*hostSandbox, len(h.live))
	for name, sb := range h.live {
		names = append(names, name)
		sandboxes[name] = sb
	}
	h.mu.Unlock()
	sort.Strings(names)

	out := make([]SandboxHealth, 0, len(names))
	for _, name := range names {
		sb := sandboxes[name]
		out = append(out, SandboxHealth{Sandbox: name, Kind: sandboxKind(name), Backend: "host", Name: sb.root, Ready: true, StartedAt: sb.startedAt})
	}
	return out
}

// SandboxCount returns the number of sandbox pods currently held by live
// runs without probing any of them for the resource readings Health returns.
func (k *K8sSandboxes) SandboxCount() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.live)
}

// AgentSandboxCount is SandboxCount narrowed to the agent half -- see
// HostSandboxes.AgentSandboxCount for what asks and why.
func (k *K8sSandboxes) AgentSandboxCount() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	n := 0
	for name := range k.live {
		if sandboxKind(name) == "agent" {
			n++
		}
	}
	return n
}

// Health implements the same shape HostSandboxes.Health does: one entry
// per sandbox pod currently held by a live run, in name order, each with
// the same best-effort /proc/loadavg, /proc/meminfo, `df` and
// nested-virtualization reading GranuleSandboxes pulls out of a guest --
// over `kubectl exec` here, and out of one container rather than one
// VM.
//
// Two of those figures mean something slightly different in a pod than
// in a VM, and it is worth knowing which. The load average and the
// memory totals come from /proc, which in an ordinary container is the
// *node's* /proc rather than the cgroup's: without something like lxcfs
// mounted over it, what this reports is the pressure on the machine the
// sandbox is scheduled onto, not the sandbox's own share of it. That is
// still the number a starved run is actually contending over -- it is
// the same reading ui.Config.HostStats gives a host-backed deployment --
// but it is not the pod's limit, and a sandbox sitting at what looks
// like a busy load may simply have a busy neighbour.
//
// Unlike Acquire this never waits out an image pull: a pod that is not
// answering right now is exactly what this pane exists to surface, so
// the phase the API server reports is used to say *why* rather than to
// decide how long to wait.
func (k *K8sSandboxes) Health(ctx context.Context) []SandboxHealth {
	k.mu.Lock()
	names := make([]string, 0, len(k.live))
	sandboxes := make(map[string]*k8sSandbox, len(k.live))
	for name, sb := range k.live {
		names = append(names, name)
		sandboxes[name] = sb
	}
	defaults := k.defaults
	k.mu.Unlock()
	sort.Strings(names)

	return collectSandboxHealth(names, func(name string) SandboxHealth {
		sb := sandboxes[name]
		shape := sb.shape.orDefault(defaults)
		health := k.sandboxHealth(ctx, name, sb.podName)
		health.StartedAt = sb.startedAt
		health.CPUs, health.MemoryMB, health.DiskGB = shape.CPUs, shape.MemoryMB, shape.DiskGB
		health.NetworkAccess = string(model.NormalizeNetworkAccess(shape.NetworkAccess))
		return health
	})
}

func (k *K8sSandboxes) sandboxHealth(ctx context.Context, sandbox, pod string) (health SandboxHealth) {
	health = SandboxHealth{Sandbox: sandbox, Kind: sandboxKind(sandbox), Backend: "kubernetes", Name: pod}

	started := time.Now()
	defer func() { health.ProbeMS = time.Since(started).Milliseconds() }()

	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()

	// The pod's own phase first, because it is the cheap question and
	// the one whose answer an operator can act on: a pod that is
	// Pending on an unschedulable node or stuck on an image pull would
	// otherwise report only that a command in it did not run.
	if st, err := k.client.Status(ctx, pod); err != nil {
		health.Error = err.Error()
		return health
	} else if !st.Ready {
		health.Error = "pod is " + k8sPodStatusDetail(st)
		return health
	}

	// One command rather than three, for the reason
	// GranuleSandboxes.sandboxHealth gives: this whole check runs inside
	// healthTimeout, and each extra round trip spends another exec's
	// worth of it for one more line of output.
	stdout, stderr, exitCode := k.execRunner(pod).Run(ctx, []string{"sh", "-c", guestStatsCommand}, "")
	if exitCode != 0 {
		if stderr == "" {
			stderr = fmt.Sprintf("exited %d with no output", exitCode)
		}
		health.Error = fmt.Sprintf("reading sandbox stats: %s", strings.TrimSpace(stderr))
		return health
	}

	health.Ready = true
	health.LoadAverage, health.MemoryUsedMB, health.MemoryTotalMB, health.SwapUsedMB, health.SwapTotalMB = parseProcStats(stdout)
	health.DiskUsedMB, health.DiskTotalMB = parseDiskStats(stdout)
	health.NestedVirt = parseNestedVirt(stdout)
	return health
}

// healthTimeout bounds how long one sandbox's Health check waits for its
// guest to answer. Still far shorter than cfg.readyTimeout (2 minutes by
// default): that timeout exists to give a VM this process just created
// real wall-clock time to boot before Acquire gives up on it, but a pane
// asking "is this sandbox healthy right now" wants a VM that is actually
// down reported as such reasonably quickly, not held up for the same
// boot-time grace period.
//
// It was 5 seconds, which was a budget measured against an idle host: one
// `docker exec` into a guest and back is tens of milliseconds there, so
// five seconds was already generous and anything slower really was a
// sandbox in trouble. The host this runs on is not idle when it matters.
// grain commits its sandboxes by count rather than by memory, and a
// deployment running the number of them it was sized for is a machine at
// or past its core count with swap in use (this pane's own LOAD_LEVEL
// vocabulary exists to show exactly that state) -- where the same round
// trip takes seconds, because docker, the VMM and the guest's own shell
// are all queueing behind everything else. A probe that was merely slow
// then timed out and painted a perfectly healthy sandbox with a red
// error chip, and it did it under precisely the load an operator opens
// this pane to look at, which is the one time the pane has to be
// believable (grain/task-134).
//
// Paid for in latency rather than in work: probes run concurrently
// (collectSandboxHealth), so this is how long GET /api/sandboxes can take
// in the worst case, not how long times the number of live sandboxes.
//
// It is per sweep, and a sweep is now shared: several callers of GET
// /api/sandboxes coalesce onto one (ui.sandboxHealthTTL), so a budget
// this generous is spent once for all of them rather than once each
// (grain/task-223).
const healthTimeout = 20 * time.Second

// healthBreakerBudget is the whole budget a sandbox whose last probe
// failed gets on its next one: a single attempt, rather than the ten or
// so healthTimeout's retry loop makes.
//
// One attempt's connect timeout with a couple of seconds on top, so that
// the error the row reports is the guest's own ("connection refused",
// "container has exited") rather than a deadline this budget imposed a
// hair before the attempt could produce one.
const healthBreakerBudget = healthProbeTimeout + 2*time.Second

// healthBreakerCooldown is how long a failed probe's own error answers
// for that sandbox before another probe is run at all.
//
// This, rather than the shorter budget above, is what actually takes the
// work off the host. A sweep is started on demand and coalesced
// (ui.sandboxHealthTTL), so shortening a probe only makes the *next*
// sweep start sooner: a sick guest that fails in a moment is re-probed
// every couple of seconds instead of every twenty, and the host does the
// same amount of docker work per minute for it. Refusing to re-probe is
// the part that costs nothing.
//
// Only failures are ever answered from memory, never readings. A load
// average or a memory figure fifteen seconds old, presented as current,
// is worse than no figure at all -- an operator cannot tell it from a
// live one -- but "this guest is not answering" is not a number that
// drifts, and the worst this can be is fifteen seconds behind a
// recovery. pkg/ui already serves a whole sweep up to a minute old for
// the same kind of reason (its sandboxHealthMaxAge), so this stays well
// inside the staleness the pane is already built to tolerate.
const healthBreakerCooldown = 15 * time.Second

// healthBreakerFullInterval is how often a sandbox that keeps failing is
// given the full healthTimeout budget again anyway.
//
// It is there because "gone" and "slow" are not the same guest and the
// breaker cannot tell them apart from one failed attempt. The retry loop
// healthTimeout pays for buys nothing against a guest that is really
// gone -- ten connection refusals are one connection refusal ten times
// -- but it is the whole reason a guest that is merely buried under host
// load is not painted with an error chip (healthTimeout's own comment,
// grain/task-134). A guest that recovers normally answers the single
// attempt the cooldown lets through and closes the breaker within
// seconds; this covers the guest that is alive but cannot get a word out
// inside one attempt, which would otherwise be written off by the one
// bad minute that tripped the breaker and never re-examined.
//
// Five minutes, which is where it costs little: one full budget per five
// minutes against one per sweep is the difference between a sick guest
// costing this host four docker invocations a minute and sixty.
const healthBreakerFullInterval = 5 * time.Minute

// healthBreaker is what earlier sweeps learned about one guest, kept so
// that this one need not learn it again at full price. One per live
// sandbox, on GranuleSandboxes.breakers.
type healthBreaker struct {
	// failedAt is when this guest's last probe failed, and failedWith is
	// the error it failed with -- what a sweep arriving inside
	// healthBreakerCooldown is answered from. failedAt is zero while the
	// guest is answering, which is the breaker's closed state.
	failedAt   time.Time
	failedWith string
	// failedAfterMS is how long that probe took, carried onto the rows
	// answered from it (SandboxHealth.ProbeMS): the duration belongs to
	// the probe a row reports, and a remembered row reports that one.
	failedAfterMS int64
	// fullAt is when this guest was last given the whole healthTimeout
	// budget, succeed or fail. healthBreakerFullInterval is measured
	// against it.
	fullAt time.Time
}

// healthProbePlan is what one sweep has decided to spend on one guest --
// healthPlan's answer, and what recordHealthProbe needs back to know
// which kind of probe it is recording.
type healthProbePlan struct {
	// remembered, when set, is a failure from within
	// healthBreakerCooldown that this sweep reports as it stands. No
	// probe runs at all, and the other two fields are unused.
	remembered *healthBreaker
	// budget bounds the probe, and attempts caps how many tries it makes
	// inside that budget -- 1 for a guest whose breaker is open, 0
	// ("as many as the budget allows") for every other case, which is
	// the behaviour every probe had before breakers existed.
	budget   time.Duration
	attempts int
}

// full reports whether this plan is the generous one, which is what
// fullAt records having been spent.
func (p healthProbePlan) full() bool { return p.remembered == nil && p.attempts == 0 }

// healthPlan decides what this sweep owes one sandbox, from what the
// last probe of it found. See healthBreakerCooldown and
// healthBreakerFullInterval for the reasoning behind each branch.
func (k *GranuleSandboxes) healthPlan(sandbox string, now time.Time) healthProbePlan {
	k.mu.Lock()
	defer k.mu.Unlock()

	breaker := k.breakers[sandbox]
	if breaker == nil || breaker.failedAt.IsZero() {
		return healthProbePlan{budget: healthTimeout}
	}
	// Checked ahead of the cooldown so that a guest which has been
	// failing for hours still gets its periodic full probe on schedule
	// rather than on whichever sweep happens to land outside a cooldown.
	if now.Sub(breaker.fullAt) >= healthBreakerFullInterval {
		return healthProbePlan{budget: healthTimeout}
	}
	if now.Sub(breaker.failedAt) < healthBreakerCooldown {
		remembered := *breaker
		return healthProbePlan{remembered: &remembered}
	}
	return healthProbePlan{budget: healthBreakerBudget, attempts: 1}
}

// recordHealthProbe files what a probe found, so the next sweep's
// healthPlan can act on it. finishedAt is when the probe returned, not
// when it started: the cooldown is time since this host last tried, and
// a full budget spent failing has already taken twenty seconds of it.
func (k *GranuleSandboxes) recordHealthProbe(sandbox string, plan healthProbePlan, finishedAt time.Time, probeMS int64, err error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.breakers == nil {
		k.breakers = map[string]*healthBreaker{}
	}
	breaker := k.breakers[sandbox]
	if breaker == nil {
		breaker = &healthBreaker{}
		k.breakers[sandbox] = breaker
	}
	if plan.full() {
		breaker.fullAt = finishedAt
	}
	if err == nil {
		// Closed again: one answer is all it takes, since a guest that
		// answers is a guest a full probe will also get an answer out
		// of.
		*breaker = healthBreaker{fullAt: breaker.fullAt}
		return
	}
	breaker.failedAt = finishedAt
	breaker.failedWith = err.Error()
	breaker.failedAfterMS = probeMS
}

// forgetHealthBreakers drops what is remembered about sandboxes that are
// no longer live. Called by Health, which is where the live set and the
// breaker map are both already in hand under one lock -- a sandbox name
// is a run ID and never comes back, so anything not in live is a run
// that has finished.
//
// The caller must hold k.mu.
func (k *GranuleSandboxes) forgetHealthBreakers(live map[string]*granuleSandbox) {
	for sandbox := range k.breakers {
		if _, ok := live[sandbox]; !ok {
			delete(k.breakers, sandbox)
		}
	}
}

// healthProbeTimeout bounds one attempt within healthTimeout -- what
// readGuestStats passes as the transport's own connect timeout, so that a
// guest which is not answering at all leaves room for another attempt
// rather than sitting on guestexec's 30s default and overshooting the
// budget this is measured against.
//
// Roughly a third of healthTimeout, which under readyPollInterval's own
// 2-second gap is two full attempts and the start of a third: enough that
// a single blocked exec is not the whole reading, without cutting an
// attempt so short that a loaded guest can never finish one.
const healthProbeTimeout = 6 * time.Second

// SandboxCount returns the number of sandbox VMs currently held by live
// runs without probing any of them for the resource readings Health returns.
func (k *GranuleSandboxes) SandboxCount() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.live)
}

// AgentSandboxCount is SandboxCount narrowed to the agent half -- see
// HostSandboxes.AgentSandboxCount for what asks and why. This is the
// backend the deployment grain/task-506 was measured on runs, so it is
// the one whose answer ends up in a queued job's own activity line.
func (k *GranuleSandboxes) AgentSandboxCount() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	n := 0
	for name := range k.live {
		if sandboxKind(name) == "agent" {
			n++
		}
	}
	return n
}

// Health implements the same shape HostSandboxes.Health does: one entry
// per VM currently held by a live run, each with a best-effort live
// /proc/loadavg, /proc/meminfo, `df` and nested-virtualization reading
// pulled out of the guest over the same transport that run's own tools
// use. Unlike Acquire, this never
// waits out a VM's multi-minute boot -- a sandbox not reachable within
// healthTimeout gets Error set instead, which is exactly the condition
// this pane exists to surface (bwsalmon/agents#536), not one this method
// should hide behind a long wait or an error return that would take the
// whole pane down with it.
//
// It does, within that budget, retry a guest that is not answering yet:
// a sandbox can be in the middle of a Rebuild, whose VM is deleted and
// created again while the run goes on holding it, and a guest under a
// loaded host blinks. Without that retry, a state nothing was actually
// wrong with surfaced verbatim as a connection error, with nothing in the
// daemon's own logs because nothing was wrong long enough to log
// (bwsalmon/agents#553).
//
// It pays that budget once per sandbox, though, and not again while the
// same guest goes on failing. A guest that has spent a whole
// healthTimeout not answering has already had the retry loop's question
// put to it and answered it: whatever it is, it is not a blink. So the
// probe that failed trips a breaker on that sandbox (healthBreaker),
// which answers the next few sweeps from the error it produced and then
// re-probes with a single attempt, and the twenty seconds of docker work
// a sick guest used to cost on every sweep is spent once. It costs the
// host what it is worth to find out something the host already knows,
// which matters because the host an operator is looking at this pane
// about is usually a host with nothing to spare (grain/task-363).
func (k *GranuleSandboxes) Health(ctx context.Context) []SandboxHealth {
	k.mu.Lock()
	names := make([]string, 0, len(k.live))
	sandboxes := make(map[string]*granuleSandbox, len(k.live))
	for name, sb := range k.live {
		names = append(names, name)
		sandboxes[name] = sb
	}
	defaults := k.defaults
	k.forgetHealthBreakers(k.live)
	k.mu.Unlock()
	sort.Strings(names)

	return collectSandboxHealth(names, func(name string) SandboxHealth {
		sb := sandboxes[name]
		shape := sb.shape.orDefault(defaults).orDefault(DefaultShape())
		health := k.sandboxHealth(ctx, name, sb.vmName)
		health.StartedAt = sb.startedAt
		health.CPUs, health.MemoryMB, health.DiskGB = shape.CPUs, shape.MemoryMB, shape.DiskGB
		health.NetworkAccess = string(model.NormalizeNetworkAccess(shape.NetworkAccess))
		return health
	})
}

// readGuestStats runs guestStatsCommand in name's guest and hands back
// what it printed, retrying a guest that does not answer until ctx's
// deadline.
//
// The retry is what waitForGuestReady used to contribute to this path,
// without the thing that wait actually asks for, which is a default
// route. Acquire wants that -- a sandbox handed to a task that cannot
// reach anything fails every checkout in it -- but a health probe does
// not, and asking for it here was wrong in both directions. A sandbox
// whose shape is model.NetworkNone has had its default route deliberately
// removed (enforceGuestNetwork), so it could never satisfy the probe: its
// row reported "answers but has no default route yet" forever, after
// burning the whole health budget establishing it on every single poll.
// And a guest that is answering but off the network is a sandbox worth
// seeing the load and memory of, not a blank row.
//
// Running the stats command itself is also the cheaper question. It
// answers "does this guest answer" as well as any probe does, so one exec
// per attempt does what the ready-probe-then-stats pair spent two on --
// half the work this pane asks of a host that is already loaded enough
// for somebody to have opened it.
//
// A VM container that has already exited fails immediately rather than
// waiting out the rest of the budget exec'ing into something that will
// never answer.
//
// attempts caps how many tries it makes regardless of what is left of
// the budget -- 1 for a guest whose breaker is open, where the retry has
// already been paid for once and bought nothing, and 0 for "as many as
// the budget allows", which is what every caller used to get.
func (k *GranuleSandboxes) readGuestStats(ctx context.Context, name string, attempts int) (string, error) {
	runner := k.execRunner(name)
	runner.ConnectTimeout = healthProbeTimeout
	for attempt := 1; ; attempt++ {
		// One command rather than a `cat` and a `df`: each extra round
		// trip over this transport spends another guest login's worth of
		// the budget for one more line of output. "granule exec" hands its
		// argv to the guest's own shell (mcp.DockerExecRunner.Run), so
		// the two run in one shell here rather than as separate calls.
		// `df -Pk` is the POSIX single-line-per-filesystem form, which is
		// what keeps parseDiskStats' own "the line whose last field is /"
		// reliable against a long device name that plain `df` would wrap.
		//
		// The `|| exit` and the `|| true` are what keep the exit status
		// meaning what it did before this line grew a second and a third
		// command: the two /proc files are what says the guest is
		// answering at all, so their status is the one reported, while a
		// `df` that fails (a busybox build without -P, say) leaves the
		// disk figure at 0/0 -- "unavailable", which the pane already has
		// a shape for -- rather than turning a perfectly reachable
		// sandbox into an errored row. The order matters too:
		// parseProcStats reads /proc/loadavg off the first line, so the
		// `cat` has to come first regardless.
		stdout, stderr, exitCode := runner.Run(ctx, []string{"sh", "-c", guestStatsCommand}, "")
		if exitCode == 0 {
			return stdout, nil
		}
		lastErr := fmt.Errorf("reading guest stats: %s", guestCommandDetail(stderr, exitCode))
		if status, dead := dockerExitedEarly(ctx, name); dead {
			return "", fmt.Errorf("VM container %q has exited (status %q): %w", granule.PodName(name), status, lastErr)
		}
		if attempts > 0 && attempt >= attempts {
			return "", lastErr
		}
		select {
		case <-ctx.Done():
			return "", lastErr
		case <-time.After(k.cfg.readyPollInterval()):
		}
	}
}

func (k *GranuleSandboxes) sandboxHealth(ctx context.Context, sandbox, name string) SandboxHealth {
	health := SandboxHealth{Sandbox: sandbox, Kind: sandboxKind(sandbox), Backend: "granule", Name: name}

	started := k.now()
	plan := k.healthPlan(sandbox, started)
	if remembered := plan.remembered; remembered != nil {
		health.Error = remembered.failedWith
		health.ProbeMS = remembered.failedAfterMS
		// How far back the probe this row reports actually ran, measured
		// from this sweep -- see ProbeAgeMS. Clamped at zero because a
		// clock that has gone backwards between the two would otherwise
		// date the row into the future, which reads as a bug in the pane
		// rather than as one in the clock.
		health.ProbeAgeMS = max(started.Sub(remembered.failedAt).Milliseconds(), 0)
		return health
	}

	ctx, cancel := context.WithTimeout(ctx, plan.budget)
	defer cancel()

	stdout, err := k.readGuestStats(ctx, name, plan.attempts)
	finished := k.now()
	health.ProbeMS = finished.Sub(started).Milliseconds()
	k.recordHealthProbe(sandbox, plan, finished, health.ProbeMS, err)
	if err != nil {
		health.Error = err.Error()
		return health
	}

	health.Ready = true
	health.LoadAverage, health.MemoryUsedMB, health.MemoryTotalMB, health.SwapUsedMB, health.SwapTotalMB = parseProcStats(stdout)
	health.DiskUsedMB, health.DiskTotalMB = parseDiskStats(stdout)
	health.NestedVirt = parseNestedVirt(stdout)
	return health
}

// nestedVirtPrefix is what the guest prints its nested-virtualization
// answer behind, so parseNestedVirt can find that line among the /proc
// files and the `df` sharing the same stream. Nothing else in that
// output starts a line with it.
const nestedVirtPrefix = "nested-virt:"

// nestedVirtProbe is the guest-side half of SandboxHealth.NestedVirt: it
// prints one "nested-virt: <state>" line, using the same four spellings
// the NestedVirt* constants hold.
//
// It asks the questions in the order those constants narrow, and it asks
// them *as the account tools run as* rather than about the system in the
// abstract: `[ -r ] && [ -w ]` is access(2) with this session's own
// credentials, which is the whole question for a VMM the agent itself
// would start. A `stat` of the device's mode would answer a different and
// less useful one, since /dev/kvm is root:kvm 0660 in every guest whether
// or not the exec account is in that group.
//
// /proc/cpuinfo is only consulted once /dev/kvm has been ruled out, to
// tell "the kernel module is not loaded" from "this CPU was never offered
// the feature" -- the two need different fixes, in different places (see
// the NestedVirt* constants).
const nestedVirtProbe = "if [ -r /dev/kvm ] && [ -w /dev/kvm ]; then echo '" + nestedVirtPrefix + " " + NestedVirtReady + "'; " +
	"elif [ -e /dev/kvm ]; then echo '" + nestedVirtPrefix + " " + NestedVirtDenied + "'; " +
	"elif grep -qE '^flags.*[ ](vmx|svm)([ ]|$)' /proc/cpuinfo; then echo '" + nestedVirtPrefix + " " + NestedVirtNoDevice + "'; " +
	"else echo '" + nestedVirtPrefix + " " + NestedVirtUnsupported + "'; fi"

// guestStatsCommand is the single shell command sandboxHealth runs in
// the guest for every figure this pane shows -- see its own comment on
// why one command rather than three, and on which part of it decides the
// exit status.
const guestStatsCommand = "cat /proc/loadavg /proc/meminfo || exit $?; " +
	"{ df -Pk / || true; }; " +
	"{ " + nestedVirtProbe + "; } || true"

// parseProcStats picks the three /proc/loadavg fields and MemTotal/
// MemAvailable out of "cat /proc/loadavg /proc/meminfo"'s combined
// output. MemAvailable, not MemFree, is the kernel's own estimate of
// memory a new process could actually get without swapping (man 5 proc)
// -- the same number `free`'s "available" column shows -- so
// MemoryUsedMB is MemTotal minus that, not minus MemFree.
func parseProcStats(output string) (loadAverage string, usedMB, totalMB, swapUsedMB, swapTotalMB int) {
	lines := strings.Split(output, "\n")
	if len(lines) > 0 {
		if fields := strings.Fields(lines[0]); len(fields) >= 3 {
			loadAverage = strings.Join(fields[:3], " ")
		}
	}

	var totalKB, availKB, swapTotalKB, swapFreeKB int
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		val, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			totalKB = val
		case "MemAvailable:":
			availKB = val
		case "SwapTotal:":
			swapTotalKB = val
		case "SwapFree:":
			swapFreeKB = val
		}
	}
	if totalKB > 0 {
		totalMB = totalKB / 1024
		usedMB = (totalKB - availKB) / 1024
	}
	if swapTotalKB > 0 {
		swapTotalMB = swapTotalKB / 1024
		swapUsedMB = (swapTotalKB - swapFreeKB) / 1024
	}
	return loadAverage, usedMB, totalMB, swapUsedMB, swapTotalMB
}

// parseNestedVirt picks the nested-virtualization line out of the same
// combined output parseProcStats and parseDiskStats read -- the
// nestedVirtProbe half of it (see sandboxHealth, which asks for all
// three in one command).
//
// Found by its prefix rather than by position, for the same reason
// parseDiskStats finds its row by shape: the line lands after however
// many lines /proc/meminfo and `df` happened to produce. An answer that
// is not one of the four known states, or no such line at all -- an
// older guest, or a `grep`-less one -- reads as empty, which
// SandboxHealth.NestedVirt documents as "no reading" and the pane shows
// as a dash, rather than as a definite "unsupported" this did not
// actually establish.
func parseNestedVirt(output string) string {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != nestedVirtPrefix {
			continue
		}
		switch fields[1] {
		case NestedVirtReady, NestedVirtDenied, NestedVirtNoDevice, NestedVirtUnsupported:
			return fields[1]
		}
	}
	return ""
}

// parseDiskStats picks the root filesystem's size and usage out of the
// same combined output parseProcStats reads -- the "df -Pk /" half of it
// (see sandboxHealth, which asks for both in one command).
//
// The row is found by shape rather than by position: the line whose last
// field is "/" and which has df's own six POSIX columns
// (filesystem, 1024-blocks, used, available, capacity, mounted-on). That
// survives the header line, a shell warning printed ahead of the output,
// and the /proc lines the same stream carries, none of which look like
// that. Used, not "total minus available", so the reading matches what
// `df` itself reports: the two differ by the reserved blocks only root
// may use, and it is `df`'s answer an operator will compare this pane
// against.
//
// 0/0 when no such line is there at all -- a guest whose `df` does not
// take -P, or a busybox build that words its output differently -- which
// SandboxHealth.DiskUsedMB documents as "unavailable" and the pane shows
// as a dash, rather than an error that would take a perfectly healthy
// sandbox's whole row down with it.
func parseDiskStats(output string) (usedMB, totalMB int) {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 6 || fields[5] != "/" {
			continue
		}
		totalKB, err1 := strconv.Atoi(fields[1])
		usedKB, err2 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil || totalKB <= 0 {
			continue
		}
		return usedKB / 1024, totalKB / 1024
	}
	return 0, 0
}
