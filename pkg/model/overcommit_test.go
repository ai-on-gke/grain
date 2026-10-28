package model

import (
	"testing"
	"time"
)

// overcommitHost is a 34-core, 136 GiB host: 32 usable vCPU and 128 GiB
// usable memory once the reserves are off, so ratios read as round
// commitments.
var overcommitHost = Budget{
	Host:        Commitment{CPUs: 34, MemoryMB: 136 * 1024},
	ReserveCPUs: 2, ReserveMemoryMB: 8 * 1024,
}

func autoSettings() OvercommitSettings { return OvercommitSettings{Mode: OvercommitAuto} }

// full is a commitment at the controller's current ratios, so both
// dimensions count as waiting for capacity.
func full(c *OvercommitController) Commitment {
	cpu, mem := c.Effective()
	return Commitment{CPUs: int(32 * cpu), MemoryMB: int(128 * 1024 * mem)}
}

func calmAt(c *OvercommitController, at time.Time) OvercommitSignals {
	return OvercommitSignals{At: at, CPUs: 34, Load1: 4, Committed: full(c)}
}

func TestOvercommitRisesWhenCalmWithBacklog(t *testing.T) {
	c := NewOvercommitController(autoSettings(), 2, 1)
	start := time.Unix(0, 0)
	var ups int
	for i := range 40 {
		ups += len(c.Step(overcommitHost, calmAt(c, start.Add(time.Duration(i)*OvercommitInterval))))
	}
	cpu, mem := c.Effective()
	if cpu <= 2 || mem <= 1 || ups == 0 {
		t.Fatalf("after 20 calm minutes at full commitment: cpu %.2f mem %.2f (%d changes), want both raised", cpu, mem, ups)
	}
	// Additive: ten calm samples per step (four calm, then one change)
	// can be at most ten steps.
	if cpu > 2+10*OvercommitCPUStep+1e-9 {
		t.Errorf("cpu rose to %.2f, faster than one additive step per calm run", cpu)
	}
}

func TestOvercommitHoldsWithoutBacklog(t *testing.T) {
	c := NewOvercommitController(autoSettings(), 2, 1)
	start := time.Unix(0, 0)
	for i := range 40 {
		s := calmAt(c, start.Add(time.Duration(i)*OvercommitInterval))
		s.Committed = Commitment{CPUs: 8, MemoryMB: 16 * 1024}
		if got := c.Step(overcommitHost, s); len(got) != 0 {
			t.Fatalf("raised with nothing waiting: %v", got)
		}
	}
}

func TestOvercommitFallsOnPressureSwapAndOOM(t *testing.T) {
	for name, tc := range map[string]struct {
		signal  func(*OvercommitSignals)
		dim     string
		factor  float64
		initial [2]float64
	}{
		"memory psi": {func(s *OvercommitSignals) { s.MemorySome = 20 }, "memory", OvercommitPressureDecrease, [2]float64{3, 1.8}},
		"swap-in":    {func(s *OvercommitSignals) { s.SwapInMBps = 50 }, "memory", OvercommitPressureDecrease, [2]float64{3, 1.8}},
		"oom":        {func(s *OvercommitSignals) { s.OOMKills = 1 }, "memory", OvercommitOOMDecrease, [2]float64{3, 1.8}},
		"cpu psi":    {func(s *OvercommitSignals) { s.CPUSome = 60 }, "cpu", OvercommitPressureDecrease, [2]float64{5, 1.8}},
		"load":       {func(s *OvercommitSignals) { s.Load1 = 100 }, "cpu", OvercommitPressureDecrease, [2]float64{5, 1.8}},
	} {
		t.Run(name, func(t *testing.T) {
			c := NewOvercommitController(autoSettings(), tc.initial[0], tc.initial[1])
			s := OvercommitSignals{At: time.Unix(0, 0), CPUs: 34, Load1: 4}
			tc.signal(&s)
			got := c.Step(overcommitHost, s)
			if len(got) != 1 || got[0].Dimension != tc.dim {
				t.Fatalf("adjustments %v, want one %s decrease", got, tc.dim)
			}
			want := tc.initial[0] * tc.factor
			if tc.dim == "memory" {
				want = tc.initial[1] * tc.factor
			}
			if d := got[0].To - want; d > 1e-9 || d < -1e-9 {
				t.Errorf("%s fell to %.3f, want %.3f", tc.dim, got[0].To, want)
			}
		})
	}
}

func TestOvercommitNoteOOMIsADecrease(t *testing.T) {
	c := NewOvercommitController(autoSettings(), 2, 1.5)
	c.NoteOOM(time.Unix(5, 0))
	got := c.Step(overcommitHost, OvercommitSignals{At: time.Unix(10, 0), CPUs: 34})
	if len(got) != 1 || got[0].Dimension != "memory" || got[0].Signals.OOMKills != 1 {
		t.Fatalf("adjustments %v, want the noted OOM to cut memory", got)
	}
	if st := c.State(); st.OOMKills != 1 || !st.LastOOMAt.Equal(time.Unix(5, 0)) {
		t.Errorf("state counts %d OOM kills at %v, want 1 at 5s", st.OOMKills, st.LastOOMAt)
	}
}

func TestOvercommitRespectsBounds(t *testing.T) {
	s := OvercommitSettings{Mode: OvercommitAuto, CPUMin: 1.5, CPUMax: 2.5, MemoryMin: 1, MemoryMax: 1.1}
	c := NewOvercommitController(s, 10, 0.2)
	if cpu, mem := c.Effective(); cpu != 2.5 || mem != 1 {
		t.Fatalf("starting ratios %.2f/%.2f, want clamped to 2.5/1.0", cpu, mem)
	}
	start := time.Unix(0, 0)
	for i := range 200 {
		c.Step(overcommitHost, calmAt(c, start.Add(time.Duration(i)*OvercommitInterval)))
	}
	if cpu, mem := c.Effective(); cpu > 2.5 || mem > 1.1+1e-9 {
		t.Fatalf("rose past the maxima: %.2f/%.2f", cpu, mem)
	}
	for i := range 50 {
		c.Step(overcommitHost, OvercommitSignals{At: start.Add(time.Hour + time.Duration(i)*OvercommitInterval),
			CPUs: 34, CPUSome: 90, MemorySome: 90})
	}
	if cpu, mem := c.Effective(); cpu != 1.5 || mem != 1 {
		t.Fatalf("fell to %.2f/%.2f, want the minima 1.5/1.0", cpu, mem)
	}
}

func TestOvercommitNeverBelowCommitted(t *testing.T) {
	c := NewOvercommitController(autoSettings(), 4, 1.8)
	// 3.6x of the cpu and 1.7x of the memory are already held.
	held := Commitment{CPUs: 116, MemoryMB: 222823}
	c.Step(overcommitHost, OvercommitSignals{At: time.Unix(0, 0), CPUs: 34, CPUSome: 90, MemorySome: 90, Committed: held})
	cpu, mem := c.Effective()
	if cpu < 3.6-1e-9 || mem < 1.7-1e-6 {
		t.Fatalf("fell to %.3f/%.3f, below the 3.6/1.7 already committed", cpu, mem)
	}
}

func TestOvercommitHysteresis(t *testing.T) {
	c := NewOvercommitController(autoSettings(), 2, 1)
	start := time.Unix(0, 0)
	// Inside the band -- under target, over half of it -- forever: no
	// change either way.
	for i := range 100 {
		s := calmAt(c, start.Add(time.Duration(i)*OvercommitInterval))
		s.MemorySome = DefaultOvercommitMemPressureTarget * 0.75
		if got := c.Step(overcommitHost, s); len(got) != 0 {
			t.Fatalf("moved inside the hysteresis band: %v", got)
		}
	}
	// After a decrease, nothing rises again until the hold has passed,
	// however calm it is.
	at := start.Add(time.Hour)
	s := calmAt(c, at)
	s.CPUSome = 99
	s.Committed = Commitment{}
	if got := c.Step(overcommitHost, s); len(got) != 1 {
		t.Fatalf("want one decrease, got %v", got)
	}
	for i := 1; ; i++ {
		now := at.Add(time.Duration(i) * OvercommitInterval)
		got := c.Step(overcommitHost, calmAt(c, now))
		if len(got) > 0 {
			if now.Sub(at) < OvercommitHoldAfterDecrease {
				t.Fatalf("rose %v after a decrease, inside the %v hold", now.Sub(at), OvercommitHoldAfterDecrease)
			}
			break
		}
		if i > 100 {
			t.Fatal("never rose again")
		}
	}
}

func TestOvercommitSettingsRoundTripAndValidate(t *testing.T) {
	text, err := EncodeOvercommitSettings(OvercommitSettings{})
	if err != nil || text != "" {
		t.Fatalf("zero settings encode as %q, %v; want empty", text, err)
	}
	in := OvercommitSettings{Mode: OvercommitAuto, CPUMax: 4, MemoryMax: 1.5}
	text, err = EncodeOvercommitSettings(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := DecodeOvercommitSettings(text)
	if err != nil || out != in {
		t.Fatalf("round trip %+v -> %q -> %+v, %v", in, text, out, err)
	}
	for _, bad := range []OvercommitSettings{
		{Mode: "turbo"}, {CPUMin: 3, CPUMax: 2}, {MemoryMin: -1}, {MemoryPressureTarget: 120},
	} {
		if bad.Validate() == nil {
			t.Errorf("%+v validated", bad)
		}
	}
	if r := (OvercommitSettings{}).Resolved(); r.Mode != OvercommitFixed || r.MemoryMin != 1 || r.CPUMax != 6 {
		t.Errorf("defaults resolved to %+v", r)
	}
}
