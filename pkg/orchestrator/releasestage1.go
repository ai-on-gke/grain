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

// Stage 1 of a red release candidate (design/playbook/releases.playbook.md
// §3, §4 and §12). Stage 2's verdicts reach releaseredrc.go from
// qualifyCandidate; this is the same for Stage 1 (nightly.yml), which is
// where most candidates go red, so that a candidate red in Stage 1 holds
// rc.(N+1) and wakes the pipeline steward exactly as a Stage 2 failure
// does:
//
//   - pipeline.rc-failed:<rc_tag>, and pipeline.release-escalated:<version>
//     for the version's last attempt, once a Stage 1 job has failed its one
//     retry (or more jobs failed than may use one) -- the same verdict
//     scripts/candidate-gate.sh check-qualification reaches, read job by
//     job from the workflow's runs on rc_sha. When the forge connection
//     cannot list jobs, or the train drops the candidate first, the
//     `<rc_tag>-discarded` tag on a candidate whose Stage 1 run concluded
//     failure raises it instead (syncDiscardedStage1).
//   - pipeline.flake-budget:<repo>:<job> for a Stage 1 job that spends its
//     flake retry on FlakeBudgetRetries candidates inside FlakeBudgetWindow.
//   - pipeline.release-infra-stalled:<rc_tag> for a Stage 1 job cancelled,
//     timed out or starved of capacity stage1InfraStall attempts in a row,
//     or blocked on infrastructure for ReleaseInfraStalledAfter.
//
// None of them needs a check run, so all of them are raised on a forge
// connection grain cannot name an App for (a PAT): only grain/rc-hold
// itself is skipped there (syncRCHold).

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// stage1JobsReader is what reading Stage 1 job by job takes: the
// workflow's runs on a commit, with their ids, and every attempt of every
// job of each. github.RESTClient is one; a forge without it is judged on
// the workflow run's own conclusion and the train's -discarded tag alone.
type stage1JobsReader interface {
	WorkflowRunsOnCommit(owner, repo, headSHA string) ([]github.WorkflowRun, error)
	ListActionsJobAttempts(owner, repo string, runID int64) ([]github.ActionsRunJob, error)
}

// These mirror scripts/candidate-gate.sh's defaults, so grain and the
// train reach the same Stage 1 verdict on the same jobs.
const (
	// stage1RollupJob is nightly.yml's roll-up of every other job
	// (CHECK_QUALIFICATION_IGNORE): it fails whenever any of them does, and
	// carries no verdict of its own.
	stage1RollupJob = "stage 1 qualification verdict"
	// stage1MaxFlakes is CHECK_QUALIFICATION_MAX_FLAKES: more jobs than this
	// failing once on one candidate is systemic, not a flake.
	stage1MaxFlakes = 2
	// stage1InfraStall is CHECK_QUALIFICATION_INFRA_STALL.
	stage1InfraStall = 3
)

// What the reconciler remembers about Stage 1 between cycles: which jobs'
// flake retries it has counted, and which candidates' Stage 1 is settled
// (green with its flakes counted, or red and raised), so it is not read
// job by job again. Both are re-learnt after a restart.
var (
	stage1FlakesNoted = map[string]bool{} // store|repo|rc|job
	stage1Settled     = map[string]bool{} // store|repo|rc|sha -> stage1Verdict.passed
)

// stage1Job is one Stage 1 job's attempts on a candidate, oldest first:
// pending, success, failure, infra or skipped.
type stage1Job struct {
	name     string
	attempts []string
}

// stage1Verdict is Stage 1 read job by job.
type stage1Verdict struct {
	// read is whether the jobs could be read at all.
	read bool
	// red is the failing lines of a confirmed red Stage 1; empty otherwise.
	red []string
	// retried is every job that has spent its flake retry: failed once and
	// was run again.
	retried []stage1Job
	// infra is every job whose newest concluded attempt is infrastructure.
	infra []stage1Job
	// passed is whether every job read has passed (on its flake retry, if
	// it spent one) -- the verdict scripts/candidate-gate.sh reaches, which
	// stands even when nightly.yml's run concluded failure on its roll-up
	// job alone.
	passed bool
}

// stage1JobName is a job's name without its matrix suffix -- "go,
// shuffled and repeated (3)" is a shard of "go, shuffled and repeated" --
// as candidate-gate.sh judges every shard of a job as one.
func stage1JobName(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndex(name, " ("); i > 0 && strings.HasSuffix(name, ")") {
		return name[:i]
	}
	return name
}

// stage1Class is the class of one job attempt's outcome, as
// candidate-gate.sh's JQ_ATTEMPTS has it.
func stage1Class(j github.ActionsRunJob) string {
	if j.Status != "" && j.Status != "completed" {
		return "pending"
	}
	switch j.Conclusion {
	case "success":
		return "success"
	case "skipped":
		return "skipped"
	case "cancelled", "timed_out", "startup_failure", "stale", "neutral":
		return "infra"
	}
	return "failure"
}

// readStage1 reads every attempt of every job of cfg's Stage 1 workflow on
// sha and judges them as candidate-gate.sh check-qualification does.
func readStage1(forge ReleaseQualificationForge, repo model.RepoRef, sha string, cfg model.SuiteQualification) (stage1Verdict, error) {
	reader, ok := forge.(stage1JobsReader)
	if !ok || cfg.Stage1Workflow == "" {
		return stage1Verdict{}, nil
	}
	runs, err := reader.WorkflowRunsOnCommit(repo.Owner, repo.Name, sha)
	if err != nil {
		return stage1Verdict{}, err
	}
	type attemptKey struct {
		run     int64
		attempt int
	}
	classes := map[string]map[attemptKey][]string{}
	for _, run := range runs {
		if run.Name != cfg.Stage1Workflow {
			continue
		}
		jobs, err := reader.ListActionsJobAttempts(repo.Owner, repo.Name, run.ID)
		if err != nil {
			return stage1Verdict{}, fmt.Errorf("reading the jobs of %s run %d: %w", cfg.Stage1Workflow, run.ID, err)
		}
		for _, j := range jobs {
			if j.HeadSHA != "" && !strings.EqualFold(j.HeadSHA, sha) {
				continue
			}
			name := stage1JobName(j.Name)
			if name == "" || strings.EqualFold(name, stage1RollupJob) {
				continue
			}
			if classes[name] == nil {
				classes[name] = map[attemptKey][]string{}
			}
			k := attemptKey{run: run.ID, attempt: max(j.RunAttempt, 1)}
			classes[name][k] = append(classes[name][k], stage1Class(j))
		}
	}
	var jobs []stage1Job
	for name, byAttempt := range classes {
		keys := make([]attemptKey, 0, len(byAttempt))
		for k := range byAttempt {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].run != keys[j].run {
				return keys[i].run < keys[j].run
			}
			return keys[i].attempt < keys[j].attempt
		})
		job := stage1Job{name: name}
		for _, k := range keys {
			// Every shard of one attempt is one verdict: any pending shard
			// is pending, then any failure, then any infrastructure.
			c := "skipped"
			for _, rank := range []string{"pending", "failure", "infra", "success"} {
				if slices.Contains(byAttempt[k], rank) {
					c = rank
					break
				}
			}
			if c != "skipped" || len(keys) == 1 {
				job.attempts = append(job.attempts, c)
			}
		}
		if len(job.attempts) > 0 {
			jobs = append(jobs, job)
		}
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].name < jobs[j].name })
	return judgeStage1(jobs), nil
}

// judgeStage1 is check-qualification's judge for Stage 1's jobs: a second
// failure is confirmed red, and so is more than stage1MaxFlakes jobs
// failing once.
func judgeStage1(jobs []stage1Job) stage1Verdict {
	v := stage1Verdict{read: true, passed: len(jobs) > 0}
	var failedOnce []string
	for _, j := range jobs {
		fails, first := 0, -1
		for i, a := range j.attempts {
			if a == "failure" {
				fails++
				if first < 0 {
					first = i
				}
			}
		}
		if last := j.attempts[len(j.attempts)-1]; last != "success" && last != "skipped" {
			v.passed = false
		}
		attempts := strings.Join(j.attempts, ",")
		if fails > 0 {
			failedOnce = append(failedOnce, j.name)
			if first < len(j.attempts)-1 {
				v.retried = append(v.retried, j)
			}
		}
		if fails >= 2 {
			v.red = append(v.red, fmt.Sprintf("%s: failed again on its retry (%s): confirmed red", j.name, attempts))
		}
		if infraBlockedStage1(j.attempts) {
			v.infra = append(v.infra, j)
		}
	}
	if len(failedOnce) > stage1MaxFlakes {
		v.red = append(v.red, fmt.Sprintf("systemic: %d jobs failed (%s), more than the %d that may use a flake retry",
			len(failedOnce), strings.Join(failedOnce, ", "), stage1MaxFlakes))
	}
	v.passed = v.passed && len(v.red) == 0
	return v
}

// infraBlockedStage1 reports whether a job's newest concluded attempt is
// infrastructure, a re-run still pending included (infraBlocked's rule).
func infraBlockedStage1(attempts []string) bool {
	for i := len(attempts) - 1; i >= 0; i-- {
		if attempts[i] == "pending" {
			continue
		}
		return attempts[i] == "infra"
	}
	return false
}

// infraTail is how many of a job's newest attempts in a row were
// infrastructure, pending re-runs aside.
func infraTail(attempts []string) int {
	n := 0
	for i := len(attempts) - 1; i >= 0; i-- {
		switch attempts[i] {
		case "pending":
		case "infra":
			n++
		default:
			return n
		}
	}
	return n
}

// watchStage1 reads Stage 1 on q's candidate while Stage 2 has not
// started: it counts every flake retry Stage 1 spent, raises
// pipeline.rc-failed (and the hold, and the escalation) for a red rc
// candidate, and raises or clears pipeline.release-infra-stalled. It
// reports whether Stage 1 is green, so Stage 2 may start. attempt is the
// Stage 2 attempt that would start: past the first, an open stall is
// Stage 2's own and is left alone.
func (q *rcQualification) watchStage1(ctx context.Context, cfg model.SuiteQualification, commits map[string]string,
	rc *model.RCTag, attempt int) (bool, error) {

	green, err := stage1Green(q.forge, q.repo, q.sha, cfg)
	if err != nil {
		return false, fmt.Errorf("reading Stage 1 on %s: %w", q.rcTag, err)
	}
	settledKey := redRCKey(q.store, q.repo.String(), q.rcTag, q.sha)
	redRCMu.Lock()
	jobsPassed, settled := stage1Settled[settledKey]
	redRCMu.Unlock()
	if settled {
		return green || jobsPassed, nil
	}
	v, err := readStage1(q.forge, q.repo, q.sha, cfg)
	if err != nil {
		return false, fmt.Errorf("reading Stage 1's jobs on %s: %w", q.rcTag, err)
	}
	// The roll-up job carries no verdict of its own, so a run that failed on
	// it alone is green when every other job passed, as the train judges
	// it; read by its conclusion only, the candidate would wait on a
	// Stage 2 grain never started, with nothing raised.
	green = green || v.passed
	var errs []error
	for _, j := range v.retried {
		errs = append(errs, q.noteStage1Flake(ctx, cfg.Stage1Name(), j))
	}
	red := !green && len(v.red) > 0
	if red && rc != nil {
		errs = append(errs, q.raiseStage1Failed(ctx, commits, *rc, cfg.Stage1Name(), v.red))
	}
	if !green && !red {
		errs = append(errs, q.trackStage1Infra(ctx, cfg.Stage1Name(), v.infra, attempt))
	} else if attempt == 1 {
		errs = append(errs, clearPipelineNotification(ctx, q.store,
			model.NotificationSubject(model.FamilyReleaseInfraStalled, q.notifyKey), releaseQualificationNotifier))
	}
	err = errors.Join(errs...)
	if err == nil && v.read && (green || red) {
		redRCMu.Lock()
		stage1Settled[settledKey] = v.passed
		redRCMu.Unlock()
	}
	return green, err
}

// noteStage1Flake counts j's flake retry on q's candidate once.
func (q *rcQualification) noteStage1Flake(ctx context.Context, stage1 string, j stage1Job) error {
	key := redRCKey(q.store, q.repo.String(), q.rcTag, j.name)
	redRCMu.Lock()
	noted := stage1FlakesNoted[key]
	stage1FlakesNoted[key] = true
	redRCMu.Unlock()
	if noted {
		return nil
	}
	return q.spendFlakeRetry(ctx, j.name, fmt.Sprintf("Stage 1 job %s of %s is flaking", j.name, q.repo),
		fmt.Sprintf("Stage 1 (%s)", stage1), fmt.Sprintf("attempts %s", strings.Join(j.attempts, ",")))
}

// raiseStage1Failed raises pipeline.rc-failed for rc once: a candidate
// whose notification a person or the steward has already cleared is not
// raised again by the same red Stage 1.
func (q *rcQualification) raiseStage1Failed(ctx context.Context, commits map[string]string, rc model.RCTag,
	stage1 string, lines []string) error {

	raised, err := rcFailedRaised(ctx, q.store, q.repo, rc)
	if err != nil || raised {
		return err
	}
	return q.raiseRCFailed(ctx, commits, rc, stage1Failure(stage1), lines)
}

// rcFailedRaised reports whether pipeline.rc-failed has ever been raised
// for rc of repo, open or cleared.
func rcFailedRaised(ctx context.Context, store *model.Store, repo model.RepoRef, rc model.RCTag) (bool, error) {
	failed, err := failedCandidates(ctx, store, repo, rc.Version())
	if err != nil {
		return false, err
	}
	subject := model.NotificationSubject(model.FamilyRCFailed, rc.String())
	for _, n := range failed {
		if n.Subject == subject {
			return true, nil
		}
	}
	return false, nil
}

// trackStage1Infra raises pipeline.release-infra-stalled once a Stage 1
// job has been cancelled, timed out or starved of capacity
// stage1InfraStall attempts in a row, or blocked on infrastructure for
// ReleaseInfraStalledAfter; and clears it, on the first attempt, once no
// job is.
func (q *rcQualification) trackStage1Infra(ctx context.Context, stage1 string, blocked []stage1Job, attempt int) error {
	prefix := redRCKey(q.store, q.tag, "stage1:")
	redRCMu.Lock()
	still := map[string]bool{}
	var stalled []string
	for _, j := range blocked {
		key := prefix + strings.ToLower(j.name)
		still[key] = true
		since, ok := redRCInfraSince[key]
		if !ok {
			since = q.now
			redRCInfraSince[key] = since
		}
		switch tail := infraTail(j.attempts); {
		case tail >= stage1InfraStall:
			stalled = append(stalled, fmt.Sprintf("Stage 1 (%s) job %s was cancelled, timed out or starved of capacity %d attempts in a row",
				stage1, j.name, tail))
		case q.now.Sub(since) >= model.ReleaseInfraStalledAfter:
			stalled = append(stalled, fmt.Sprintf("Stage 1 (%s) job %s has been blocked on infrastructure for %s",
				stage1, j.name, humanDuration(q.now.Sub(since).Round(time.Minute))))
		}
	}
	for k := range redRCInfraSince {
		if strings.HasPrefix(k, prefix) && !still[k] {
			delete(redRCInfraSince, k)
		}
	}
	redRCMu.Unlock()
	if len(stalled) > 0 {
		return q.raiseInfraStalled(ctx, strings.Join(stalled, "; "), false)
	}
	if attempt > 1 {
		return nil
	}
	return clearPipelineNotification(ctx, q.store,
		model.NotificationSubject(model.FamilyReleaseInfraStalled, q.notifyKey), releaseQualificationNotifier)
}

// stage1Failed reports whether Stage 1's newest run (or status) on sha
// concluded red -- not merely "not green yet".
func stage1Failed(forge ReleaseQualificationForge, repo model.RepoRef, sha string, cfg model.SuiteQualification) (bool, error) {
	if cfg.Stage1Status != "" {
		reader, ok := forge.(github.CommitStatusReader)
		if !ok {
			return false, nil
		}
		statuses, err := reader.CommitStatuses(repo.Owner, repo.Name, sha)
		if err != nil {
			return false, err
		}
		for _, s := range statuses {
			if s.Context == cfg.Stage1Status {
				return s.State == "failure" || s.State == "error", nil
			}
		}
		return false, nil
	}
	runs, err := forge.ListWorkflowRuns(repo.Owner, repo.Name, sha)
	if err != nil {
		return false, err
	}
	for _, r := range runs {
		if r.Name != cfg.Stage1Workflow {
			continue
		}
		return r.Status == "completed" && r.Conclusion != nil && *r.Conclusion == "failure", nil
	}
	return false, nil
}

// syncDiscardedStage1 raises pipeline.rc-failed for every candidate the
// train has dropped (`<rc_tag>-discarded`) on a red Stage 1 that grain has
// raised nothing for yet -- one the train judged before grain did, or on a
// forge connection whose jobs grain cannot read -- while it is still the
// version's newest candidate: the hold it posts is what keeps the train
// from cutting rc.(N+1) before the steward has triaged it. A candidate
// the daily cap dropped (droppedUnreleased) is not red, whatever its Stage
// 1 run concluded, and raises nothing.
func syncDiscardedStage1(ctx context.Context, store *model.Store, forge ReleaseQualificationForge, gate qualificationGate,
	commits map[string]string, now time.Time) error {

	var errs []error
	for name, sha := range commits {
		rc, ok := model.ParseRCTag(name)
		if !ok || sha == "" || superseded(commits, rc) {
			continue
		}
		if _, dropped := commits[name+"-discarded"]; !dropped {
			continue
		}
		raised, err := rcFailedRaised(ctx, store, gate.repo, rc)
		if err != nil || raised {
			errs = append(errs, err)
			continue
		}
		red, err := stage1Failed(forge, gate.repo, sha, gate.cfg)
		if err != nil {
			errs = append(errs, fmt.Errorf("reading Stage 1 on %s: %w", name, err))
			continue
		}
		if !red {
			continue
		}
		if unjudged, err := droppedUnreleased(forge, gate.repo, name); err != nil || unjudged {
			if err != nil {
				errs = append(errs, fmt.Errorf("reading %s-discarded: %w", name, err))
			}
			continue
		}
		v, err := readStage1(forge, gate.repo, sha, gate.cfg)
		if err != nil {
			errs = append(errs, fmt.Errorf("reading Stage 1's jobs on %s: %w", name, err))
			continue
		}
		lines := v.red
		if len(lines) == 0 {
			lines = []string{fmt.Sprintf("%s concluded failure on %s, and the train dropped %s (%s-discarded)",
				gate.cfg.Stage1Name(), shortSHA(sha), name, name)}
		}
		q := rcQualification{store: store, forge: forge, repo: gate.repo, suite: gate.suite, rcTag: name, sha: sha,
			tag: model.QualificationTag(gate.stem, gate.repo, name), now: now, check: gate.cfg.Check,
			release: rc.Version(), notifyKey: name}
		errs = append(errs, q.raiseRCFailed(ctx, commits, rc, stage1Failure(gate.cfg.Stage1Name()), lines))
	}
	return errors.Join(errs...)
}
