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

// syncpoll.go is how often SyncPullRequests reads a pull request nothing
// is happening to.
//
// Every open pull request used to be read every cycle -- the pull
// request, its head and its check runs -- which made the sync reconciler
// the largest GitHub spender on a deployment with a few dozen finished
// changes waiting on a human. Those reads now go through the same
// adaptive schedule review comments and path triggers do
// (github.PollSchedule), but with one classification of their own,
// because this is the merge state machine and a read skipped at the
// wrong moment is a merge that waits or a merge that lands out of order.
//
// An entry is *active* -- read every cycle, exactly as before -- when any
// of these holds, judged before the read from what the last read saw and
// what grain itself knows:
//
//   - an agent run is in flight for it, it is in a merge batch, or a
//     repair the queue asked for is in flight (Observation.RepairInFlight);
//   - its CI was running or not yet settled at the last read: unfinished
//     or unregistered checks, grain CI running, a mergeability GitHub had
//     not finished computing, or a PENDING verdict that is not a
//     withdrawn one (verdictWithdrawn);
//   - its head moved within syncRecentPush of now, counting the first
//     read this process made of it as a move;
//   - grain's own CI verdict for the head the last read saw has changed
//     since -- grain CI writes onto the task, not onto GitHub, so the
//     task is where the news is;
//   - it is a merge-queue member and the walk could still let it merge
//     this cycle (its lane's slot is open), since reading it is how the
//     walk decides that.
//
// The head of its lane in the merge queue is PollCritical: read every
// cycle whatever the budget. Everything else is PollNormal and idle,
// backed off by Observe on a fingerprint of what decides its next move
// (syncFingerprint).
//
// What a skipped entry costs is lateness, bounded by github.PollCap: a
// human merging or closing an idle pull request, or pushing to it by
// hand, is noticed within that rather than within a cycle. Anything grain
// does to the pull request itself (a RESTClient write) resets it, a merge
// resets its whole repository, and a run starting grain CI for its branch
// (ui.Client.StartTaskCIRunOpt, which wait_for_checks calls) resets it
// too.

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// syncRecentPush is how long after its head moves a pull request is
// still read every cycle: CI registering, a review landing, a second push.
const syncRecentPush = 10 * time.Minute

// syncPollAspect is the PollKey aspect for SyncPullRequests' own read.
const syncPollAspect = "pull request"

// syncSeen is what the last read of one entry saw that the next cycle's
// classification needs before it reads again.
type syncSeen struct {
	head      string
	base      string
	headAt    time.Time
	unsettled bool
	grain     string
	asked     time.Time
}

// syncSeens is syncSeen per PollKey; entries nobody has asked about for
// an hour are dropped, as github.PollSchedule drops its own.
var syncSeens sync.Map

func syncPollKey(store *model.Store, ref model.PullRequestRef) github.PollKey {
	return github.PollKey{Scope: storeScope(store),
		Subject: github.PullRequestSubject(ref.Repo.Owner, ref.Repo.Name, ref.Number), Aspect: syncPollAspect}
}

// syncGrainSummary is grain CI's verdict on head, as a string a later
// cycle can compare. The gate is forced on so a verdict is summarised
// whether or not this task's own grant would count it.
func syncGrainSummary(e queueEntry, head string) string {
	return fmt.Sprintf("%+v", grainCIVerdictOf(e.task, e.obs, head, true))
}

// syncDue says whether e is read this cycle. It is asked on e's own turn
// in the act loop, after every entry ahead of it, so the walk already
// knows whether e heads its lane and whether its lane can still merge.
func syncDue(store *model.Store, e queueEntry, walk *mergeWalk, now time.Time) bool {
	key := syncPollKey(store, e.ref)
	class, active := syncClass(key, e, walk, now)
	return github.Polls().Due(key, class, active, now)
}

func syncClass(key github.PollKey, e queueEntry, walk *mergeWalk, now time.Time) (github.PollClass, bool) {
	v, ok := syncSeens.Load(key)
	var seen syncSeen
	if ok {
		seen = v.(syncSeen)
	}
	// The lane is the one the last read placed it in where the task names
	// no base of its own: laneOf can only say that after a read.
	lane := laneOf(e)
	if laneBase(e) == "" && seen.base != "" {
		lane = laneKey(e.ref.Repo.String(), seen.base)
	}
	member := isQueueMember(e)
	if member && walk.headOf(lane) == "" {
		return github.PollCritical, true
	}
	if e.running || e.recheckingHead != "" || e.obs.RepairInFlight() {
		return github.PollNormal, true
	}
	if member && walk.mayMerge(lane) {
		return github.PollNormal, true
	}
	if !ok {
		return github.PollNormal, true
	}
	seen.asked = now
	syncSeens.Store(key, seen)
	active := seen.unsettled || now.Sub(seen.headAt) < syncRecentPush ||
		syncGrainSummary(e, seen.head) != seen.grain
	return github.PollNormal, active
}

// syncSkipped is what the walk is told about a queue member this cycle
// did not read: nothing behind it may be reordered around it, the same
// as for a member nobody could read. Only a low budget skips a member
// whose lane is still open (syncClass reads every other one), so this is
// the conservative direction for exactly that case.
func syncSkipped(e queueEntry, walk *mergeWalk) {
	if isQueueMember(e) && walk.mayMerge(laneOf(e)) {
		walk.closeEntry(e)
	}
}

// syncRead records what e's read saw, for the schedule's backoff and for
// the next cycle's classification. Called by syncEntry right after a
// read that succeeded.
func syncRead(store *model.Store, e queueEntry, now time.Time) {
	key := syncPollKey(store, e.ref)
	head := e.detail.HeadSHA
	seen := syncSeen{head: head, base: e.detail.BaseRef, headAt: now}
	if v, ok := syncSeens.Load(key); ok && v.(syncSeen).head == head {
		seen.headAt = v.(syncSeen).headAt
	}
	seen.unsettled = syncUnsettled(e)
	seen.grain = syncGrainSummary(e, head)
	seen.asked = now
	syncSeens.Store(key, seen)
	syncSeens.Range(func(k, v any) bool {
		if now.Sub(v.(syncSeen).asked) > time.Hour {
			syncSeens.Delete(k)
		}
		return true
	})
	github.Polls().Observe(key, syncFingerprint(e), now)
}

// syncUnsettled is "CI running or not yet settled" as the last read saw
// it, for an open pull request.
func syncUnsettled(e queueEntry) bool {
	if e.detail.State == "closed" {
		return false
	}
	return !e.checksKnown || !e.checksSettled || len(unfinishedChecks(e.checks)) > 0 ||
		e.grain.Running || e.detail.Mergeable == nil || e.health == model.PrUnknown ||
		(e.health == model.PrPending && !verdictWithdrawn(e))
}

// syncFingerprint is what about e's read decides its next move: the head
// commit, whether it can merge, and how each check ended. Any change puts
// it back to the base cadence.
func syncFingerprint(e queueEntry) string {
	checks := make([]string, 0, len(e.checks))
	for _, c := range e.checks {
		conclusion := ""
		if c.Conclusion != nil {
			conclusion = *c.Conclusion
		}
		checks = append(checks, c.Name+"="+c.Status+"/"+conclusion)
	}
	sort.Strings(checks)
	return strings.Join([]string{e.detail.State, e.detail.HeadSHA, e.detail.BaseRef,
		e.detail.MergeableState, string(e.health), strings.Join(checks, ",")}, "|")
}
