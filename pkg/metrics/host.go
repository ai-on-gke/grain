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

package metrics

// What the machine underneath this deployment was doing while it did it.
//
// Every other section here measures grain. This one measures the host, and
// it is here because the two cannot be read apart: a CI job that took
// twice as long did not get slower, it got less of a machine, and nothing
// in a report of job durations can say which. The evening this was built
// for had one instantaneous reading of a host at 99.4% CPU and load 47.4
// on 28 cores, taken by hand, and no way at all to say whether that was
// unusual (grain/task-495).
//
// It is the one section whose input is neither a row nor derivable from
// one, twice over: a load average is a fact about a moment that leaves no
// record, and the machine is the thing being measured, so the measurement
// has to be cheap. Both of those are settled outside this package --
// sysstat.History folds a reading a minute into hourly rollups in a
// bounded ring -- and what arrives here is that fold. This package's rules
// then apply as they do everywhere else: nothing stored, windowed, and a
// missing reading skipped rather than guessed at.
//
// # What the buckets are for
//
// The same thing every other series' buckets are for, and this is the
// series they exist to be read against. Host pressure sliced onto the same
// spans as CIQueueing's run times and Changes' settings events is the whole
// of "the jobs got slower because the host was full, and here is the hour
// somebody did something about it" -- three lines of the same picture. It
// is why the rollups are re-bucketed onto the window's own slices here
// rather than handed over as hours: an hour is the storage resolution, and
// a report's slice is whatever the reader asked for.
//
// # What it cannot say
//
// How far back it reaches, beyond the ring's own bound: a daemon restarted
// this morning has this morning's history and no more, whatever window is
// asked for. Reaches and Hours say so rather than letting a short history
// read as a quiet one.

import "time"

// HostRollup is one hour of the host's own pressure, as
// sysstat.History.Rollups reports it. It mirrors that type field for field;
// the caller converts, so this package keeps deciding what readings mean
// and stays out of the business of taking them.
type HostRollup struct {
	Since              time.Time
	Samples            int
	Load1Mean          float64
	Load1Max           float64
	CPUUtilizationMean float64
	CPUUtilizationMax  float64
	MemUsedMeanMB      float64
	MemUsedMaxMB       int
	SwapUsedMaxMB      int
	CPUs               int
	MemTotalMB         int
}

// HostHistory is what the caller has to hand over: the rollups, oldest
// first, and how far back the ring holding them could reach if it had been
// running long enough.
type HostHistory struct {
	Rollups []HostRollup
	Hours   int
}

// Host is how hard the machine was working during the window.
type Host struct {
	// Available is whether there was a sampler to ask. False is a
	// deployment whose UI is not colocated with one, or a host with no
	// /proc to read, and must not read as an idle machine.
	Available bool
	// Samples is how many readings the window's rollups were folded from,
	// and Hours how many hourly rollups fell in it -- the two denominators
	// for everything below.
	Samples int
	Hours   int
	// CPUs and MemTotalMB are the machine as last seen in the window,
	// which is the denominator every figure here is only meaningful over.
	CPUs       int
	MemTotalMB int
	// Load1Mean is the window's mean one-minute load and Load1Max its
	// worst hour's worst reading; LoadPerCPUMean is the first over CPUs,
	// which is the number that compares two deployments and the one an
	// operator actually reasons with (1.0 is a machine with exactly enough
	// work to keep every core busy).
	Load1Mean      float64
	Load1Max       float64
	LoadPerCPUMean float64
	// CPUUtilizationMean and CPUUtilizationMax are 0..1, and
	// MemUsedMeanMB/MemUsedMaxMB the memory the machine could not have
	// given a new process. SwapUsedMaxMB is the worst swap use seen: on a
	// host sized the way a deployment's own terraform sizes one, anything
	// above zero here is a commitment written that the machine could not
	// keep.
	CPUUtilizationMean float64
	CPUUtilizationMax  float64
	MemUsedMeanMB      float64
	MemUsedMaxMB       int
	SwapUsedMaxMB      int
	// Reaches is the oldest rollup the sampler still holds, window or no
	// window -- what says whether an empty window means a quiet host or a
	// daemon that has not been running long enough to know. RingHours is
	// the bound it is working towards.
	Reaches   *time.Time
	RingHours int
	// Buckets is the same pressure on the window's own slices, oldest
	// first: the series CIQueueing's and Changes' series are read against.
	Buckets []HostBucket
}

// HostBucket is one slice of the window: how hard the machine was working
// during it.
type HostBucket struct {
	Since time.Time
	Until time.Time
	// Hours is how many hourly rollups fell in this slice and Samples how
	// many readings they held. A slice with none reports zeroes, which is a
	// slice nothing was sampled in rather than an idle machine -- read
	// Hours first, as everywhere else here.
	Hours              int
	Samples            int
	Load1Mean          float64
	Load1Max           float64
	CPUUtilizationMean float64
	CPUUtilizationMax  float64
	MemUsedMeanMB      float64
	MemUsedMaxMB       int
}

// hostOf builds the section from the rollups the caller read.
//
// A rollup belongs to the window when the hour it covers *starts* inside
// it. That is the same rule as everywhere else stated for a measurement
// that is a span rather than a moment: an hour is attributed where it
// began, so the hour a window opens halfway through belongs to the window
// before it and is counted once rather than split in two.
//
// available says whether there was a sampler at all, and is separate from
// the slice being empty for the reason Cycles' own flag is: a deployment
// with nothing to ask and a deployment whose sampler has just started must
// not read the same.
func hostOf(w Window, buckets []Bucket, available bool, history HostHistory) Host {
	out := Host{Available: available, RingHours: history.Hours}
	if len(buckets) > 0 {
		out.Buckets = make([]HostBucket, len(buckets))
		for i, b := range buckets {
			out.Buckets[i].Since, out.Buckets[i].Until = b.Since, b.Until
		}
	}
	// The means here are over samples rather than over hours, so a busy
	// hour sampled twelve times and a quiet one sampled twice weigh what
	// they are worth: an hour is a fold of readings, not a reading.
	var loadTotal, cpuTotal, memTotal float64
	bucketTotals := make([]struct{ load, cpu, mem float64 }, len(out.Buckets))
	for _, r := range history.Rollups {
		since := r.Since
		if out.Reaches == nil || since.Before(*out.Reaches) {
			oldest := since
			out.Reaches = &oldest
		}
		if !w.holds(&since) || r.Samples == 0 {
			continue
		}
		n := float64(r.Samples)
		out.Hours++
		out.Samples += r.Samples
		loadTotal += r.Load1Mean * n
		cpuTotal += r.CPUUtilizationMean * n
		memTotal += r.MemUsedMeanMB * n
		out.Load1Max = max(out.Load1Max, r.Load1Max)
		out.CPUUtilizationMax = max(out.CPUUtilizationMax, r.CPUUtilizationMax)
		if r.MemUsedMaxMB > out.MemUsedMaxMB {
			out.MemUsedMaxMB = r.MemUsedMaxMB
		}
		if r.SwapUsedMaxMB > out.SwapUsedMaxMB {
			out.SwapUsedMaxMB = r.SwapUsedMaxMB
		}
		// The machine itself, as last seen: a window that spans a resize
		// reports the shape it ended at, which is the one an operator
		// reading the report is about to make a decision about.
		if r.CPUs > 0 {
			out.CPUs = r.CPUs
		}
		if r.MemTotalMB > 0 {
			out.MemTotalMB = r.MemTotalMB
		}
		if i := bucketOf(buckets, w, &since); i >= 0 {
			b := &out.Buckets[i]
			b.Hours++
			b.Samples += r.Samples
			bucketTotals[i].load += r.Load1Mean * n
			bucketTotals[i].cpu += r.CPUUtilizationMean * n
			bucketTotals[i].mem += r.MemUsedMeanMB * n
			b.Load1Max = max(b.Load1Max, r.Load1Max)
			b.CPUUtilizationMax = max(b.CPUUtilizationMax, r.CPUUtilizationMax)
			if r.MemUsedMaxMB > b.MemUsedMaxMB {
				b.MemUsedMaxMB = r.MemUsedMaxMB
			}
		}
	}
	if out.Samples > 0 {
		n := float64(out.Samples)
		out.Load1Mean = loadTotal / n
		out.CPUUtilizationMean = cpuTotal / n
		out.MemUsedMeanMB = memTotal / n
		if out.CPUs > 0 {
			out.LoadPerCPUMean = out.Load1Mean / float64(out.CPUs)
		}
	}
	for i := range out.Buckets {
		if out.Buckets[i].Samples == 0 {
			continue
		}
		n := float64(out.Buckets[i].Samples)
		out.Buckets[i].Load1Mean = bucketTotals[i].load / n
		out.Buckets[i].CPUUtilizationMean = bucketTotals[i].cpu / n
		out.Buckets[i].MemUsedMeanMB = bucketTotals[i].mem / n
	}
	return out
}
