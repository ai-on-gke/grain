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

package engine

import (
	"time"

	"github.com/ai-on-gke/grain/pkg/workflows"
)

// queueClock measures one job's wait for a machine: how long it was
// between becoming startable and getting one, and which of the things
// that held it held it longest.
//
// It exists because a job's live Activity is state rather than record --
// it is cleared the instant the job is admitted, and cleared again by
// neverRan -- so nothing survived a wait to say it had happened. A
// starved job and a slow job read identically afterwards, which is how
// three jobs cancelled at exactly their timeout left nobody able to say
// whether they had been running or waiting (grain/task-326).
//
// Attribution is by WaitCause rather than by the sentence a gate says,
// because the sentence moves while the cause does not: a job held by the
// budget for half an hour is told about "8 GiB free", then "12 GiB
// free", then "4 GiB free", and bucketing those separately would report
// three ten-minute waits where there was one of thirty. What is kept per
// cause is the last sentence said about it, which is the gate's own
// description of the wait as it stood when it ended.
//
// A queueClock belongs to one job's goroutine and is not locked: every
// call into it comes from that job's own Acquire, one at a time.
type queueClock struct {
	// start is when this job became startable -- the beginning of the
	// span, whether or not anything ever reported holding it.
	start time.Time
	// since is when the current cause started holding this job, and
	// current is that cause; the zero cause is time no gate has
	// attributed to anything, which is what a job passing through a gate
	// that reports no wait spends.
	since   time.Time
	current workflows.WaitCause
	spent   map[workflows.WaitCause]time.Duration
	said    map[workflows.WaitCause]string
}

func newQueueClock(now time.Time) *queueClock {
	return &queueClock{
		start: now, since: now,
		spent: map[workflows.WaitCause]time.Duration{},
		said:  map[workflows.WaitCause]string{},
	}
}

// waiting records that what is holding this job has changed: the time
// since the last change goes to whatever was holding it until now, and
// the new wait starts running. The zero Wait is "nothing is holding it
// any more", which is what a gate reports as it admits the job.
func (q *queueClock) waiting(now time.Time, w workflows.Wait) {
	if q == nil {
		return
	}
	q.charge(now)
	q.current = w.Cause
	if w.Reason != "" {
		// Kept even when the cause is the zero one, so that a caller
		// reporting a reason without a cause is still quotable.
		q.said[w.Cause] = w.Reason
	}
}

// stop ends the span and reports it: how long this job queued in total,
// whichever cause held it longest, and the last thing said about that
// cause. Both are empty when nothing ever reported holding this job -- a
// job that waited only at a gate with nothing to say about it (the
// per-run concurrency bound) queued for a length of time and for no
// reason anybody wrote down, and inventing one would be worse than the
// gap.
//
// The cause is returned beside the sentence, rather than left to be read
// back out of it, because the two are different kinds of thing and only
// one of them can be added up -- Wait's own note. A report that wanted
// "how much of this deployment's CI waiting was the sandbox count" from
// the sentences alone would have to match prose that every gate is free
// to rewrite (metrics.CIQueueing, grain/task-345).
func (q *queueClock) stop(now time.Time) (time.Duration, workflows.WaitCause, string) {
	if q == nil {
		return 0, "", ""
	}
	q.charge(now)
	queued := now.Sub(q.start)
	if queued < 0 {
		// A clock that went backwards under us. Nothing sensible to
		// report, and a negative wait would read as a job that started
		// before it was startable.
		queued = 0
	}
	var (
		worst workflows.WaitCause
		most  time.Duration
	)
	for cause, spent := range q.spent {
		if cause == "" {
			continue
		}
		// Ties broken by name, because a map's order is not an order: two
		// causes that held a job for exactly as long would otherwise
		// report differently every time the same wait was measured.
		if spent > most || (spent == most && (worst == "" || cause < worst)) {
			worst, most = cause, spent
		}
	}
	return queued, worst, q.said[worst]
}

// charge attributes the time since the last change to whatever has been
// holding this job, and moves the mark up to now.
func (q *queueClock) charge(now time.Time) {
	if spent := now.Sub(q.since); spent > 0 {
		q.spent[q.current] += spent
	}
	q.since = now
}
