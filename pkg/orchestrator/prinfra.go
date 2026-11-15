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
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// # A pull request's infrastructure failure is waited out, not repaired
//
// From 13:56 UTC on 2026-11-11 every hosted runner's `apt-get install`
// died on "Temporary failure resolving 'security.ubuntu.com'" for three
// hours. Nothing in any branch could clear it, and the merge queue sent
// every red pull request back for an automatic repair anyway -- each
// charged "automatic repair 1 of 2", so an outage longer than two
// repairs would have left every one of them in merge_failed waiting for
// a person to press Retry.
//
// So a pull request whose every failing Actions job failed on the runner
// (github.OnlyInfraFailures, pkg/infrasig's list) gets what a trunk run
// with an `infra` verdict already gets (infraverdict.go): its failed jobs
// re-run, no sooner than infraRetryDelay after the run concluded, up to
// maxInfraRetries times, with no repair counted. Once those re-runs are
// spent the red is handled as the ordinary red it then is, the way trunk
// is (TestAnInfraFailureOnTrunkIsReRunBeforeItHoldsTheQueue).
//
// Nothing about what counts as a verdict changes: a red check still
// blocks the merge exactly as before. Only whether an agent is sent at it
// does.
//
// The re-run count lives on GitHub rather than on the task: a run
// re-run with rerun-failed-jobs is the same run's next attempt, so its
// run_attempt less one is how many re-runs it has had. A new commit is a
// new run at attempt 1, which is right -- it is a new question.

// infraWaitNotePrefix opens the merge-queue note of an entry waiting out
// an infrastructure failure. statusOf reads it back (infraWaitHeld) so
// the row keeps saying so on the cycles between re-runs, rather than
// flicking to repair_needed and back.
const infraWaitNotePrefix = "waiting out an infrastructure failure"

// infraRerunRun is one workflow run behind a pull request's infra-only
// red: what to re-run and how far along it is.
type infraRerunRun struct {
	id        int64
	attempt   int
	updatedAt time.Time
}

// infraRerunRuns are the distinct workflow runs logs came from, by id,
// each with the latest attempt and update any of its entries carries.
func infraRerunRuns(logs []github.JobLog) []infraRerunRun {
	byID := map[int64]*infraRerunRun{}
	for _, l := range logs {
		if l.RunID <= 0 {
			continue
		}
		r, ok := byID[l.RunID]
		if !ok {
			r = &infraRerunRun{id: l.RunID}
			byID[l.RunID] = r
		}
		r.attempt = max(r.attempt, l.RunAttempt)
		if l.RunUpdatedAt.After(r.updatedAt) {
			r.updatedAt = l.RunUpdatedAt
		}
	}
	out := make([]infraRerunRun, 0, len(byID))
	for _, r := range byID {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// infraSignatures names each failing job and the signature it failed on,
// for the note and the log line.
func infraSignatures(logs []github.JobLog) string {
	parts := make([]string, 0, len(logs))
	for _, l := range logs {
		parts = append(parts, fmt.Sprintf("%s (%q)", l.Name, l.InfraSignature()))
	}
	return strings.Join(parts, ", ")
}

// waitOutInfraFailure is the pull-request path's answer to an infra-only
// red (see the comment at the top of this file). It reports whether it
// took the entry -- re-ran its failed jobs, or is waiting infraRetryDelay
// to -- in which case the caller must neither ask for a repair nor count
// one. false hands the red back to the ordinary path: it is not
// infra-only, its re-runs are spent, or GitHub would not re-run it.
func waitOutInfraFailure(ctx context.Context, store *model.Store, client github.Client, e queueEntry,
	ev buildEvidence, now time.Time) (bool, error) {

	if e.health != model.PrFailing || e.grain.Failed || !github.OnlyInfraFailures(ev.logs) {
		return false, nil
	}
	runs := infraRerunRuns(ev.logs)
	if len(runs) == 0 {
		return false, nil
	}
	spent, concluded := 0, time.Time{}
	for _, r := range runs {
		spent = max(spent, max(1, r.attempt)-1)
		if r.updatedAt.After(concluded) {
			concluded = r.updatedAt
		}
	}
	why := infraSignatures(ev.logs)
	if spent >= maxInfraRetries {
		log.Printf("orchestrator: %s at %s failed on the runner again (%s) after %d automatic re-run(s): "+
			"handling it as an ordinary red", e.ref, shortSHA(e.detail.HeadSHA), why, spent)
		return false, nil
	}
	rerunner, ok := client.(github.WorkflowRunRerunner)
	if !ok {
		return false, nil
	}
	if !concluded.IsZero() {
		if wait := concluded.Add(infraRetryDelay).Sub(now); wait > 0 {
			note := fmt.Sprintf("%s: every failing job failed on the runner rather than the tree (%s); "+
				"re-running the failed jobs in %s (automatic re-run %d of %d), no repair counted",
				infraWaitNotePrefix, why, humanDuration(wait), spent+1, maxInfraRetries)
			return true, setMergeQueueState(ctx, store, e.obs, e.task.ID,
				queueStatus{state: mergeStateWaitingCI, note: note}, now)
		}
	}
	for _, r := range runs {
		if err := rerunner.RerunFailedJobs(e.ref.Repo.StoredOwner(), e.ref.Repo.Name, r.id); err != nil {
			log.Printf("orchestrator: re-running the failed jobs of run %d for %s: %v -- handling the "+
				"infrastructure failure as an ordinary red (the connection needs `actions: write`)",
				r.id, e.ref, err)
			return false, nil
		}
	}
	log.Printf("orchestrator: %s at %s failed on the runner rather than the tree (%s): re-ran the failed "+
		"jobs (automatic re-run %d of %d), no repair counted", e.ref, shortSHA(e.detail.HeadSHA), why,
		spent+1, maxInfraRetries)
	note := fmt.Sprintf("%s: every failing job failed on the runner rather than the tree (%s); "+
		"re-ran the failed jobs (automatic re-run %d of %d), no repair counted",
		infraWaitNotePrefix, why, spent+1, maxInfraRetries)
	return true, setMergeQueueState(ctx, store, e.obs, e.task.ID,
		queueStatus{state: mergeStateWaitingCI, note: note}, now)
}

// infraWaitHeld is the note waitOutInfraFailure last wrote on a red
// entry, for statusOf to keep on the row until something else decides
// otherwise: the checks going pending under the re-run, or the red being
// handed to a repair. The stall clause applyMergeQueueState may have
// appended is cut off, since it is added again on the write.
func infraWaitHeld(e queueEntry) (queueStatus, bool) {
	if e.health != model.PrFailing || e.obs == nil || e.obs.MergeQueueState != mergeStateWaitingCI ||
		!strings.HasPrefix(e.obs.MergeQueueNote, infraWaitNotePrefix) {
		return queueStatus{}, false
	}
	note := e.obs.MergeQueueNote
	if i := strings.Index(note, " -- and "); i >= 0 {
		note = note[:i]
	}
	return queueStatus{state: mergeStateWaitingCI, note: note}, true
}
