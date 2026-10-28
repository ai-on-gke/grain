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

package sysstat

// This machine's own pressure over time, as hourly rollups in a bounded
// ring.
//
// Read gives an excellent instantaneous reading and nothing else, which is
// enough to answer "is the host starved right now" and no help at all with
// every question that follows it. The evening that asked for this
// (grain/task-495): CI jobs taking twice as long as they had earlier, a
// host at 99.4% CPU and load 47.4 on 28 cores, and a settings change made
// on that evidence -- and then no way to say whether the change helped,
// because the only reading anybody had was the one taken while it was bad.
// Contention cannot be correlated with anything, least of all with the
// settings changes meant to relieve it, without a series.
//
// # Why hours, and why a ring
//
// The questions are all comparisons over days: this evening against last
// Tuesday, this week against the week before the change. A rollup per hour
// answers every one of them and costs four dozen bytes an hour, where a
// sample per minute kept for the same span is sixty times that for a
// resolution nobody reads at. So the samples are folded into the hour they
// were taken in as they arrive, and what is kept is the fold.
//
// The bound is DefaultHistoryHours and it is stated rather than implied:
// this holds two weeks and then forgets, in a few kilobytes, whatever the
// sampler's cadence is.
//
// # What it does not survive
//
// A restart. This is memory, like orchestrator.CycleTimes, which argues
// the same trade in its own doc comment: nothing is written per sample,
// there is nothing to sweep, nothing to migrate and no table to export --
// and the cost is that a daemon restarted this morning can say nothing
// about last week. That is the honest bound to publish, and a report says
// how far back it actually reaches (metrics.Host.Reaches) rather than
// letting a short history read as a quiet one. Persisting the rollups is a
// follow-up worth doing on its own evidence; it is not worth a table on a
// guess.

import (
	"sync"
	"time"
)

// DefaultHistoryHours is how many hourly rollups NewHistory keeps when the
// caller does not say: two weeks, which is "this week against last week"
// with room to spare, and a few kilobytes at any sampling cadence.
const DefaultHistoryHours = 14 * 24

// Rollup is one hour of this machine's pressure: the mean of the samples
// taken during it and the worst of them.
//
// Both, because they answer different questions and a mean alone hides the
// one that matters. An hour that averages 60% CPU with a peak of 99% is an
// hour in which jobs were fighting; the mean on its own reads as an hour
// with room in it.
type Rollup struct {
	// Since is the hour this covers, truncated to it, and Samples how many
	// readings went into it -- the denominator for every mean here, and the
	// thing to read first: an hour holding one sample reports that sample
	// as its mean, its max and its whole self.
	Since   time.Time
	Samples int
	// Load1Mean and Load1Max are /proc/loadavg's one-minute figure. Read
	// them against CPUs, which is the only denominator they mean anything
	// over -- load 26 is a busy machine on 28 cores and a dead one on 4.
	Load1Mean float64
	Load1Max  float64
	// CPUUtilizationMean and CPUUtilizationMax are the fraction of
	// aggregate CPU time spent doing work, 0..1.
	CPUUtilizationMean float64
	CPUUtilizationMax  float64
	// MemUsedMeanMB and MemUsedMaxMB are MemTotal minus MemAvailable, in
	// MiB, and SwapUsedMaxMB the worst swap use of the hour -- which on a
	// host that is not meant to swap at all is the reading that says a
	// commitment was written that the machine could not keep.
	MemUsedMeanMB float64
	MemUsedMaxMB  int
	SwapUsedMaxMB int
	// CPUs and MemTotalMB are the machine itself, as last seen during the
	// hour. They are carried on every rollup rather than looked up once
	// because a report over two weeks may span a deployment being resized,
	// and a load average read against the wrong core count is worse than
	// no load average.
	CPUs       int
	MemTotalMB int
}

// History is a bounded ring of hourly rollups, safe for one sampler and
// any number of readers.
type History struct {
	mu    sync.Mutex
	hours int
	// ring is oldest first. An hour is appended when the first sample
	// lands in it and updated in place by every later one, so the last
	// entry is the hour in progress.
	ring []accumulator
}

// accumulator is one hour being folded: the sums the means come from, kept
// apart from Rollup so that nothing outside this file can read a total and
// mistake it for a reading.
type accumulator struct {
	out         Rollup
	load1Total  float64
	cpuTotal    float64
	memUsedSumM float64
}

// NewHistory allocates a ring of hours rollups, or DefaultHistoryHours for
// a count of zero or less.
func NewHistory(hours int) *History {
	if hours <= 0 {
		hours = DefaultHistoryHours
	}
	return &History{hours: hours}
}

// Add folds one reading into the hour it was taken in. Out-of-order
// readings are folded into their own hour where that hour is still in the
// ring and dropped where it is not, which is what a clock stepping
// backwards looks like and is not worth a wrong rollup.
func (h *History) Add(at time.Time, s Snapshot) {
	if h == nil {
		return
	}
	hour := at.UTC().Truncate(time.Hour)
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.ring) - 1; i >= 0; i-- {
		if h.ring[i].out.Since.Equal(hour) {
			h.ring[i].fold(s)
			return
		}
		if h.ring[i].out.Since.Before(hour) {
			break
		}
	}
	if len(h.ring) > 0 && h.ring[len(h.ring)-1].out.Since.After(hour) {
		// Older than an hour already closed and not one of the hours still
		// held: nothing to fold it into.
		return
	}
	entry := accumulator{out: Rollup{Since: hour}}
	entry.fold(s)
	h.ring = append(h.ring, entry)
	if len(h.ring) > h.hours {
		h.ring = append(h.ring[:0], h.ring[len(h.ring)-h.hours:]...)
	}
}

// fold adds one reading to an hour.
func (a *accumulator) fold(s Snapshot) {
	a.out.Samples++
	a.load1Total += s.LoadAverage1
	a.cpuTotal += s.CPUUtilization
	a.memUsedSumM += float64(s.MemUsedMB)
	a.out.Load1Max = max(a.out.Load1Max, s.LoadAverage1)
	a.out.CPUUtilizationMax = max(a.out.CPUUtilizationMax, s.CPUUtilization)
	a.out.MemUsedMaxMB = max(a.out.MemUsedMaxMB, s.MemUsedMB)
	a.out.SwapUsedMaxMB = max(a.out.SwapUsedMaxMB, s.SwapUsedMB)
	if s.CPUs > 0 {
		a.out.CPUs = s.CPUs
	}
	if s.MemTotalMB > 0 {
		a.out.MemTotalMB = s.MemTotalMB
	}
}

// Rollups returns what is held, oldest first, with the means computed. A
// copy, so a reader is never looking at an hour a sampler is still folding
// into.
func (h *History) Rollups() []Rollup {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Rollup, 0, len(h.ring))
	for _, entry := range h.ring {
		rollup := entry.out
		if rollup.Samples > 0 {
			n := float64(rollup.Samples)
			rollup.Load1Mean = entry.load1Total / n
			rollup.CPUUtilizationMean = entry.cpuTotal / n
			rollup.MemUsedMeanMB = entry.memUsedSumM / n
		}
		out = append(out, rollup)
	}
	return out
}

// Hours is the bound this ring was built with -- how far back it can
// reach once it has been running that long, which is the number a report
// states beside a history that is shorter than the window asked for.
func (h *History) Hours() int {
	if h == nil {
		return 0
	}
	return h.hours
}
