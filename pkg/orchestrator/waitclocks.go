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

package orchestrator

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// How long a pull request has been waiting on GitHub or on its checks, for
// its row: "waiting for GitHub to work out whether this branch merges
// cleanly (for 7m)", "waiting on checks that have not finished
// (`cla/google`) for 16m". Each member holds only itself (mergeWalk), so
// its own row is the whole account of what it waits on, and how long it
// has waited is half of that account: UNKNOWN for seconds after a base
// moves is GitHub at work, and the same word for an hour is something to
// go and look at.
//
// Nothing decides anything from these. They are in memory, keyed as
// syncSeens is (syncPollKey), and a restart starts them again.

// waitSeen is one pull request's current run of one kind of wait: since
// is when its head, or what it is waiting on, last moved.
type waitSeen struct {
	head        string
	fingerprint string
	since       time.Time
	touched     time.Time
}

var (
	// unknownClocks is each pull request's run of UNKNOWN reads on one
	// head: GitHub not having worked out its mergeability.
	unknownClocks sync.Map
	// pendingClocks is each pull request's run of PENDING reads on one
	// head with nothing about its checks changing.
	pendingClocks sync.Map
)

// observeUnknown advances e's UNKNOWN clock from this cycle's read and
// returns how long e has read UNKNOWN on its current head, zero when it
// does not -- or when it is UNKNOWN because grain cannot read its checks,
// which is about the deployment rather than the pull request.
func observeUnknown(key github.PollKey, e queueEntry, now time.Time) time.Duration {
	return observeWait(&unknownClocks, key, e.health == model.PrUnknown && e.checksKnown, e.detail.HeadSHA, "", now)
}

// observePending is observeUnknown for PENDING, restarted whenever any of
// e's checks changes: a check starting or finishing is progress.
func observePending(key github.PollKey, e queueEntry, now time.Time) time.Duration {
	return observeWait(&pendingClocks, key, e.health == model.PrPending, e.detail.HeadSHA, checksFingerprint(e), now)
}

func observeWait(clocks *sync.Map, key github.PollKey, waiting bool, head, fingerprint string, now time.Time) time.Duration {
	defer clocks.Range(func(k, v any) bool {
		if now.Sub(v.(waitSeen).touched) > time.Hour {
			clocks.Delete(k)
		}
		return true
	})
	if !waiting || head == "" {
		clocks.Delete(key)
		return 0
	}
	seen := waitSeen{head: head, fingerprint: fingerprint, since: now}
	if v, ok := clocks.Load(key); ok {
		if old := v.(waitSeen); old.head == head && old.fingerprint == fingerprint {
			seen.since = old.since
		}
	}
	seen.touched = now
	clocks.Store(key, seen)
	return now.Sub(seen.since)
}

// checksFingerprint is everything about e's checks whose change counts as
// progress: each check's name, status and conclusion, grain CI's verdict,
// and which required checks have not reported.
func checksFingerprint(e queueEntry) string {
	var parts []string
	for _, c := range e.checks {
		conclusion := ""
		if c.Conclusion != nil {
			conclusion = *c.Conclusion
		}
		parts = append(parts, c.Name+"|"+c.Status+"|"+conclusion)
	}
	sort.Strings(parts)
	return fmt.Sprintf("%s;%+v;%s", strings.Join(parts, ","), e.grain, strings.Join(e.missingChecks, ","))
}

// waitedFor is " for 16m" on a row, or nothing under a minute.
func waitedFor(d time.Duration) string {
	if d < time.Minute {
		return ""
	}
	return " for " + roundWait(d)
}

// roundWait is a wait in the units a person reads it in: whole minutes,
// or hours and minutes.
func roundWait(d time.Duration) string {
	d = d.Truncate(time.Minute)
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return fmt.Sprintf("%dh%dm", int(d/time.Hour), int(d%time.Hour/time.Minute))
}
