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

package model

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Auto overcommit: a feedback controller that moves the sandbox budget's
// two overcommit ratios (Budget.CPUOvercommit, Budget.MemoryOvercommit)
// inside operator-set bounds, for throughput, instead of holding them at
// the fixed figures sandbox_cpu_overcommit and sandbox_memory_overcommit
// store.
//
// Why it exists: the fixed figures are chosen once, by somebody with no
// reading of what the sandboxes actually use. On 2026-10-23 a 188 vCPU
// host sat at 184 vCPU committed for hours, refusing every candidate for
// capacity, while nobody knew how much of that was idle agent sandboxes
// waiting on a model turn. Memory is the other direction of the same
// mistake: it starts at 1.0 because grain/task-173 swapped a host into
// the ground (DefaultMemoryOvercommit), and a fixed 1.0 never finds out
// whether that host could have held more.
//
// The shape of the controller is TCP's, because the problem is: probe
// upward slowly while nothing hurts and there is demand, back off sharply
// the moment something does.
//
//   - Additive increase, one step per dimension, only when every pressure
//     reading is under half its target (the hysteresis band), no swap-in
//     or OOM kill was seen, the dimension is close to full (work is
//     waiting for it), and the readings have been calm for
//     OvercommitCalmSamples samples in a row since the last change.
//   - Multiplicative decrease the first sample a reading crosses its
//     target, swap-in rises past OvercommitSwapInMBps, or the kernel
//     OOM-kills anything; an OOM kill cuts harder than pressure does.
//     Nothing goes up again for OvercommitHoldAfterDecrease after.
//   - Between half the target and the target, nothing moves.
//
// A lower ratio never kills anything: Budget.Admits is the only reader,
// so the smaller budget refuses the next admission and the sandboxes
// already running finish. For the same reason a decrease never goes below
// what is already committed -- the budget at that point already refuses
// everything, and dropping further would only delay recovery after the
// live sandboxes drain.

// Overcommit modes, OvercommitSettings.Mode.
const (
	// OvercommitFixed is today's behaviour, and the default: the budget
	// uses sandbox_cpu_overcommit and sandbox_memory_overcommit as stored.
	OvercommitFixed = "fixed"
	// OvercommitAuto hands the two ratios to OvercommitController.
	OvercommitAuto = "auto"
)

// Default bounds and targets for auto overcommit.
//
// The CPU range is wide because agent sandboxes spend most of their wall
// time waiting on a model: 6x is roughly what a host full of idle agent
// VMs holds at load under its core count. Memory tops out at 2x because
// requested memory that is never touched is the only slack there is, and
// the cost of being wrong is an OOM kill rather than a slow run.
//
// The targets are PSI "some" avg10 percentages (the share of the last ten
// seconds in which at least one task stalled on that resource,
// Documentation/accounting/psi.rst). Memory's is low on purpose: any
// sustained memory stall on a VM host is reclaim or swap, which is the
// grain/task-173 failure beginning.
const (
	DefaultOvercommitCPUMin            = 1.0
	DefaultOvercommitCPUMax            = 6.0
	DefaultOvercommitMemoryMin         = 1.0
	DefaultOvercommitMemoryMax         = 2.0
	DefaultOvercommitCPUPressureTarget = 25.0
	DefaultOvercommitMemPressureTarget = 5.0
)

// Controller tuning. Constants rather than settings: the bounds and the
// targets are the operator's levers, and these are how carefully the
// controller moves between them.
const (
	// OvercommitInterval is how often the daemon samples the host and
	// steps the controller.
	OvercommitInterval = 30 * time.Second
	// OvercommitCPUStep and OvercommitMemoryStep are one additive
	// increase. Memory moves a fifth as fast as CPU because a mistake
	// there costs a sandbox rather than some seconds.
	OvercommitCPUStep    = 0.25
	OvercommitMemoryStep = 0.05
	// OvercommitPressureDecrease and OvercommitOOMDecrease are the
	// multiplicative decreases for a crossed target and an OOM kill.
	OvercommitPressureDecrease = 0.8
	OvercommitOOMDecrease      = 0.7
	// OvercommitCalmSamples is how many calm samples in a row an increase
	// needs: two minutes at OvercommitInterval.
	OvercommitCalmSamples = 4
	// OvercommitHoldAfterDecrease is how long after a decrease nothing is
	// increased: long enough for the sandboxes admitted just before it to
	// have touched their memory.
	OvercommitHoldAfterDecrease = 10 * time.Minute
	// OvercommitSwapInMBps is the swap-in rate treated as memory pressure
	// on its own: a host paging back in faster than this is already
	// paying grain/task-173's cost, whatever PSI says.
	OvercommitSwapInMBps = 4.0
	// OvercommitFullShare is how full a dimension's budget must be for
	// the controller to count work as waiting on it.
	OvercommitFullShare = 0.85
	// OvercommitHistory is how many adjustments are kept for the UI and
	// metrics.
	OvercommitHistory = 50
)

// OvercommitSettings is grain_config.sandbox_overcommit: whether the
// budget's ratios are fixed or controlled, and the bounds and targets of
// the controller. Stored as JSON in one column. The zero value is fixed
// mode with every bound at its default.
type OvercommitSettings struct {
	Mode      string  `json:"mode,omitempty"`
	CPUMin    float64 `json:"cpuMin,omitempty"`
	CPUMax    float64 `json:"cpuMax,omitempty"`
	MemoryMin float64 `json:"memoryMin,omitempty"`
	MemoryMax float64 `json:"memoryMax,omitempty"`
	// CPUPressureTarget and MemoryPressureTarget are PSI some avg10
	// percentages the controller keeps the host under.
	CPUPressureTarget    float64 `json:"cpuPressureTarget,omitempty"`
	MemoryPressureTarget float64 `json:"memoryPressureTarget,omitempty"`
}

// Auto reports whether these settings hand the ratios to the controller.
func (s OvercommitSettings) Auto() bool { return s.Mode == OvercommitAuto }

// Resolved is s with every unset field at its default.
func (s OvercommitSettings) Resolved() OvercommitSettings {
	if s.Mode == "" {
		s.Mode = OvercommitFixed
	}
	def := func(v *float64, d float64) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&s.CPUMin, DefaultOvercommitCPUMin)
	def(&s.CPUMax, DefaultOvercommitCPUMax)
	def(&s.MemoryMin, DefaultOvercommitMemoryMin)
	def(&s.MemoryMax, DefaultOvercommitMemoryMax)
	def(&s.CPUPressureTarget, DefaultOvercommitCPUPressureTarget)
	def(&s.MemoryPressureTarget, DefaultOvercommitMemPressureTarget)
	return s
}

// Validate refuses a mode grain does not know, a negative figure, a bound
// pair whose minimum is above its maximum, or a target outside 0-100.
func (s OvercommitSettings) Validate() error {
	switch s.Mode {
	case "", OvercommitFixed, OvercommitAuto:
	default:
		return fmt.Errorf("sandbox overcommit mode %q: want %q or %q", s.Mode, OvercommitFixed, OvercommitAuto)
	}
	for name, v := range map[string]float64{
		"cpuMin": s.CPUMin, "cpuMax": s.CPUMax, "memoryMin": s.MemoryMin, "memoryMax": s.MemoryMax,
		"cpuPressureTarget": s.CPUPressureTarget, "memoryPressureTarget": s.MemoryPressureTarget,
	} {
		if v < 0 {
			return fmt.Errorf("sandbox overcommit %s must be 0 (grain's default) or more, not %v", name, v)
		}
	}
	r := s.Resolved()
	if r.CPUMin > r.CPUMax {
		return fmt.Errorf("sandbox overcommit cpuMin %.2f is above cpuMax %.2f", r.CPUMin, r.CPUMax)
	}
	if r.MemoryMin > r.MemoryMax {
		return fmt.Errorf("sandbox overcommit memoryMin %.2f is above memoryMax %.2f", r.MemoryMin, r.MemoryMax)
	}
	if r.CPUPressureTarget > 100 || r.MemoryPressureTarget > 100 {
		return fmt.Errorf("sandbox overcommit pressure targets are percentages, at most 100")
	}
	return nil
}

// EncodeOvercommitSettings is grain_config.sandbox_overcommit's text: ""
// for the zero value, so a deployment that never chose stores nothing.
func EncodeOvercommitSettings(s OvercommitSettings) (string, error) {
	if s == (OvercommitSettings{}) {
		return "", nil
	}
	b, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("encoding sandbox_overcommit: %w", err)
	}
	return string(b), nil
}

// DecodeOvercommitSettings reads EncodeOvercommitSettings' text back.
func DecodeOvercommitSettings(text string) (OvercommitSettings, error) {
	var s OvercommitSettings
	if strings.TrimSpace(text) == "" {
		return s, nil
	}
	if err := json.Unmarshal([]byte(text), &s); err != nil {
		return OvercommitSettings{}, fmt.Errorf("decoding sandbox_overcommit: %w", err)
	}
	return s, nil
}

// OvercommitSignals is one sample of the host, as the controller reads it.
type OvercommitSignals struct {
	At time.Time `json:"at"`
	// CPUSome, MemorySome, MemoryFull, IOSome and IOFull are PSI avg10
	// percentages. IO is recorded for the reader rather than acted on:
	// an IO stall on these hosts is overlay writeback, which neither
	// ratio controls.
	CPUSome    float64 `json:"cpuSome"`
	MemorySome float64 `json:"memorySome"`
	MemoryFull float64 `json:"memoryFull"`
	IOSome     float64 `json:"ioSome"`
	IOFull     float64 `json:"ioFull"`
	// SwapInMBps is the swap-in rate since the previous sample.
	SwapInMBps float64 `json:"swapInMBps"`
	// SwapUsedMB and AvailableMemoryMB are levels, recorded for the reader.
	SwapUsedMB        int `json:"swapUsedMB"`
	AvailableMemoryMB int `json:"availableMemoryMB"`
	// Load1 and CPUs are the one-minute load average and the core count;
	// a load above OvercommitMaxLoadPerCPU per core is CPU pressure even
	// on a kernel without PSI.
	Load1 float64 `json:"load1"`
	CPUs  int     `json:"cpus"`
	// OOMKills is how many processes the kernel OOM-killed since the
	// previous sample, plus every sandbox grain itself saw die of OOM
	// since then (OvercommitController.NoteOOM).
	OOMKills int `json:"oomKills"`
	// Committed is what the live sandboxes hold right now (Ledger.Live).
	Committed Commitment `json:"committed"`
}

// OvercommitMaxLoadPerCPU is the load per core counted as CPU pressure.
const OvercommitMaxLoadPerCPU = 1.5

// OvercommitAdjustment is one change the controller made, with the sample
// that caused it.
type OvercommitAdjustment struct {
	At        time.Time         `json:"at"`
	Dimension string            `json:"dimension"` // "cpu" or "memory"
	From      float64           `json:"from"`
	To        float64           `json:"to"`
	Reason    string            `json:"reason"`
	Signals   OvercommitSignals `json:"signals"`
}

// String is the adjustment as the daemon logs it.
func (a OvercommitAdjustment) String() string {
	s := a.Signals
	return fmt.Sprintf("auto overcommit: %s %.2fx -> %.2fx (%s; psi cpu %.1f%% memory %.1f/%.1f%% io %.1f/%.1f%%, "+
		"swap-in %.1f MB/s, swap %d MiB, available %d MiB, load %.2f on %d cpus, %d oom kill(s), committed %s)",
		a.Dimension, a.From, a.To, a.Reason, s.CPUSome, s.MemorySome, s.MemoryFull, s.IOSome, s.IOFull,
		s.SwapInMBps, s.SwapUsedMB, s.AvailableMemoryMB, s.Load1, s.CPUs, s.OOMKills, s.Committed)
}

// OvercommitState is the controller as the UI and metrics read it.
type OvercommitState struct {
	Mode             string                 `json:"mode"`
	Settings         OvercommitSettings     `json:"settings"`
	CPUOvercommit    float64                `json:"cpuOvercommit"`
	MemoryOvercommit float64                `json:"memoryOvercommit"`
	Last             *OvercommitSignals     `json:"last,omitempty"`
	Adjustments      []OvercommitAdjustment `json:"adjustments"`
	OOMKills         int                    `json:"oomKills"`
	LastOOMAt        time.Time              `json:"lastOomAt,omitzero"`
}

// OvercommitController holds the effective ratios auto mode admits
// against. Safe for concurrent use: the sampler steps it, every admission
// reads it.
type OvercommitController struct {
	mu          sync.Mutex
	settings    OvercommitSettings
	cpu, mem    float64
	calm        int
	lastDecr    time.Time
	last        *OvercommitSignals
	history     []OvercommitAdjustment
	pendingOOM  int
	totalOOM    int
	lastOOMAt   time.Time
	initialised bool
}

// NewOvercommitController is a controller starting from the fixed ratios
// (clamped into the bounds): the operator's own figures are the best
// first guess there is, and memory's default of 1.0 is grain/task-173's
// lesson rather than an arbitrary floor.
func NewOvercommitController(s OvercommitSettings, cpu, mem float64) *OvercommitController {
	c := &OvercommitController{}
	c.Configure(s, cpu, mem)
	return c
}

// Configure applies changed settings. The effective ratios are kept where
// they are, clamped into the new bounds; cpu and mem are only the
// starting point of a controller that has none yet.
func (c *OvercommitController) Configure(s OvercommitSettings, cpu, mem float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.settings = s.Resolved()
	if !c.initialised {
		c.cpu, c.mem = cpu, mem
		c.initialised = true
	}
	c.cpu = clamp(c.cpu, c.settings.CPUMin, c.settings.CPUMax)
	c.mem = clamp(c.mem, c.settings.MemoryMin, c.settings.MemoryMax)
}

// Effective is the two ratios to admit against now.
func (c *OvercommitController) Effective() (cpu, mem float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cpu, c.mem
}

// NoteOOM records that grain saw a sandbox die of OOM. It is folded into
// the next Step as a decrease signal, whether or not the kernel's own
// counter saw it (a guest kernel's OOM kill is not the host's).
func (c *OvercommitController) NoteOOM(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pendingOOM++
	c.totalOOM++
	if at.After(c.lastOOMAt) {
		c.lastOOMAt = at
	}
}

// State is the controller as the UI and metrics read it.
func (c *OvercommitController) State() OvercommitState {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := OvercommitState{
		Mode: c.settings.Mode, Settings: c.settings, CPUOvercommit: c.cpu, MemoryOvercommit: c.mem,
		Adjustments: append([]OvercommitAdjustment(nil), c.history...),
		OOMKills:    c.totalOOM, LastOOMAt: c.lastOOMAt,
	}
	if c.last != nil {
		l := *c.last
		st.Last = &l
	}
	return st
}

// Step feeds one sample to the controller against budget b (the host and
// its reserves; the ratios on it are ignored) and returns the adjustments
// it made, oldest first -- none, most of the time.
func (c *OvercommitController) Step(b Budget, s OvercommitSignals) []OvercommitAdjustment {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s.OOMKills > 0 {
		c.totalOOM += s.OOMKills
		c.lastOOMAt = s.At
	}
	s.OOMKills += c.pendingOOM
	c.pendingOOM = 0
	last := s
	c.last = &last

	set := c.settings
	usableCPU := float64(b.Host.CPUs - max(b.ReserveCPUs, 0))
	usableMem := float64(b.Host.MemoryMB - max(b.ReserveMemoryMB, 0))
	// The ratio the live sandboxes already hold: the floor of a decrease.
	var heldCPU, heldMem float64
	if usableCPU > 0 {
		heldCPU = float64(s.Committed.CPUs) / usableCPU
	}
	if usableMem > 0 {
		heldMem = float64(s.Committed.MemoryMB) / usableMem
	}

	var out []OvercommitAdjustment
	change := func(dim string, to float64, reason string) {
		from := &c.cpu
		if dim == "memory" {
			from = &c.mem
		}
		if to == *from {
			return
		}
		a := OvercommitAdjustment{At: s.At, Dimension: dim, From: *from, To: to, Reason: reason, Signals: s}
		*from = to
		out = append(out, a)
		c.history = append(c.history, a)
		if len(c.history) > OvercommitHistory {
			c.history = c.history[len(c.history)-OvercommitHistory:]
		}
	}
	decrease := func(dim string, cur, factor, held, lo float64, reason string) {
		to := max(cur*factor, held, lo)
		if to < cur {
			change(dim, to, reason)
		}
		// A decrease is a decrease even when the floor stopped it moving:
		// the hold still applies, since the pressure was real.
		c.lastDecr = s.At
	}

	loadPerCPU := 0.0
	if s.CPUs > 0 {
		loadPerCPU = s.Load1 / float64(s.CPUs)
	}
	var memReason, cpuReason string
	memFactor := OvercommitPressureDecrease
	switch {
	case s.OOMKills > 0:
		memReason, memFactor = fmt.Sprintf("%d OOM kill(s)", s.OOMKills), OvercommitOOMDecrease
	case s.MemorySome > set.MemoryPressureTarget:
		memReason = fmt.Sprintf("memory pressure %.1f%% over its %.1f%% target", s.MemorySome, set.MemoryPressureTarget)
	case s.SwapInMBps > OvercommitSwapInMBps:
		memReason = fmt.Sprintf("swap-in %.1f MB/s over %.1f MB/s", s.SwapInMBps, OvercommitSwapInMBps)
	}
	switch {
	case s.CPUSome > set.CPUPressureTarget:
		cpuReason = fmt.Sprintf("cpu pressure %.1f%% over its %.1f%% target", s.CPUSome, set.CPUPressureTarget)
	case loadPerCPU > OvercommitMaxLoadPerCPU:
		cpuReason = fmt.Sprintf("load %.2f per cpu over %.2f", loadPerCPU, OvercommitMaxLoadPerCPU)
	}
	if memReason != "" || cpuReason != "" {
		c.calm = 0
		if memReason != "" {
			decrease("memory", c.mem, memFactor, heldMem, set.MemoryMin, memReason)
		}
		if cpuReason != "" {
			decrease("cpu", c.cpu, OvercommitPressureDecrease, heldCPU, set.CPUMin, cpuReason)
		}
		return out
	}

	// The hysteresis band: under target but not by half is a hold, not
	// a calm sample.
	calm := s.MemorySome <= set.MemoryPressureTarget/2 && s.CPUSome <= set.CPUPressureTarget/2 &&
		s.SwapInMBps <= OvercommitSwapInMBps/4 && loadPerCPU <= OvercommitMaxLoadPerCPU*2/3
	if !calm {
		c.calm = 0
		return out
	}
	c.calm++
	if c.calm < OvercommitCalmSamples || (!c.lastDecr.IsZero() && s.At.Sub(c.lastDecr) < OvercommitHoldAfterDecrease) {
		return out
	}
	increased := false
	if usableMem > 0 && heldMem >= c.mem*OvercommitFullShare && c.mem < set.MemoryMax {
		change("memory", min(c.mem+OvercommitMemoryStep, set.MemoryMax),
			fmt.Sprintf("memory %.0f%% committed and the host calm", 100*heldMem/c.mem))
		increased = true
	}
	if usableCPU > 0 && heldCPU >= c.cpu*OvercommitFullShare && c.cpu < set.CPUMax {
		change("cpu", min(c.cpu+OvercommitCPUStep, set.CPUMax),
			fmt.Sprintf("cpu %.0f%% committed and the host calm", 100*heldCPU/c.cpu))
		increased = true
	}
	if increased {
		// Each increase needs its own run of calm samples: one step, then
		// a look at what the sandboxes it admitted did to the host.
		c.calm = 0
	}
	return out
}

func clamp(v, lo, hi float64) float64 { return min(max(v, lo), hi) }
