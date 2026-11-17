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
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/metrics"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/qa"
	"github.com/ai-on-gke/grain/pkg/qa/checks"
	"github.com/ai-on-gke/grain/pkg/qa/expstats"
	"github.com/ai-on-gke/grain/pkg/qa/resolve"
	"github.com/ai-on-gke/grain/pkg/trustboundary"
)

// QAOpenPROptions carries optional pre-PR quality verification metadata from `open_pull_request`.
type QAOpenPROptions struct {
	CallerNotes         string
	AllowHoldAfterS3Cap bool
}

// S4VerifyFunc runs an isolated, read-only fresh-context verification prompt on a T2/T3 diff
// with zero coding-agent chain-of-thought.
type S4VerifyFunc func(ctx context.Context, task model.Task, prompt string) (output string, tokensIn, tokensOut int64, err error)

// MirrorRemoteResolver returns the clone URL and optional HTTP bearer token for repo on the controller.
type MirrorRemoteResolver func(ctx context.Context, repo model.RepoRef) (remoteURL, bearerToken string, err error)

// QARuntime holds controller-level dependencies used by pre-PR quality checks during
// `OpenPullRequestForTaskWithQA`, `salvagePushedBranch`, and async Control shadow runs.
type QARuntime struct {
	DataDir           string
	Profiler          *qa.Profiler
	ResolveRemote     MirrorRemoteResolver
	VerifyS4          S4VerifyFunc
	RunIsolatedPrompt func(ctx context.Context, task model.Task, runPrefix, prompt string, maxTokens int) (output string, tokensIn, tokensOut int64, err error)
	ExecInSandbox     func(ctx context.Context, dir string, argv []string, timeout time.Duration) (string, int, error)
}

var liveQARuntime atomic.Pointer[QARuntime]

// SetLiveQARuntime publishes controller-level QA runtime hooks for PR-open and salvage paths.
func SetLiveQARuntime(rt *QARuntime) {
	liveQARuntime.Store(rt)
}

// LiveQARuntime returns the currently registered controller QA runtime, or nil when unconfigured.
func LiveQARuntime() *QARuntime {
	return liveQARuntime.Load()
}

// OpenPullRequestForTaskWithQA enforces treatment-arm staging promotion and S3/S4 pre-PR gates
// before delegating to OpenPullRequestForTask, and triggers non-blocking shadow checks on Control.
func OpenPullRequestForTaskWithQA(
	ctx context.Context,
	store *model.Store,
	client github.Client,
	task model.Task,
	opts QAOpenPROptions,
) (PullRequestStatus, error) {
	if store == nil || task.Target == nil || client == nil {
		return OpenPullRequestForTask(ctx, store, client, task)
	}

	assign, qaCfg, err := resolve.ActiveQAResolutionForTask(ctx, store, task.ID)
	if err != nil || assign == nil || !qaCfg.Enabled {
		return OpenPullRequestForTask(ctx, store, client, task)
	}

	if assign.ArmID == model.QAArmControl {
		status, err := OpenPullRequestForTask(ctx, store, client, task)
		if err == nil {
			if assign.Shadow || (assign.Source == "legacy" && qaCfg.ShadowOnControlEnabled()) {
				maybeRunControlShadowS3(store, client, task, assign)
			}
			maybeEvaluateQACircuitBreakerThrottled(ctx, store, client, time.Now().UTC())
		}
		return status, err
	}

	if !assign.IsEnforcedTreatment() {
		return OpenPullRequestForTask(ctx, store, client, task)
	}

	freshTask, err := store.GetTask(ctx, task.ID)
	if err != nil {
		return PullRequestStatus{}, err
	}
	if freshTask != nil {
		task = *freshTask
	}

	stageBranch := model.QAStageBranchName(task.ID)
	stageExists, err := targetBranchExists(client, *task.Target, stageBranch)
	if err != nil {
		return PullRequestStatus{}, fmt.Errorf("orchestrator: checking staging branch %s: %w", stageBranch, err)
	}

	checkBranch := task.BranchName()
	if stageExists {
		checkBranch = stageBranch
	}

	flow := *assign.Spec
	remoteHead, err := client.GetBranchHead(task.Target.StoredOwner(), task.Target.Name, checkBranch)
	if err != nil {
		return PullRequestStatus{}, fmt.Errorf("orchestrator: reading branch %s head: %w", checkBranch, err)
	}
	headSHA := ""
	if remoteHead != nil {
		headSHA = remoteHead.SHA
	}
	if stageExists && headSHA == "" {
		return PullRequestStatus{}, fmt.Errorf("orchestrator: staging branch %s has no head commit", stageBranch)
	}

	s3Passed, s3Ran, blastTier, s3Summary := false, false, qa.BlastT1, ""
	if headSHA != "" {
		s3Passed, s3Ran, blastTier, s3Summary = loadRecordedS3Outcome(ctx, store, task.ID, headSHA)
	}
	if headSHA != "" && (!s3Ran || (!s3Passed && strings.TrimSpace(opts.CallerNotes) != "")) {
		rep, ran, s3Err := runMirrorS3IfConfigured(ctx, store, task, assign, false, checkBranch, opts.CallerNotes)
		if s3Err != nil {
			if rt := LiveQARuntime(); rt != nil && rt.DataDir != "" {
				return PullRequestStatus{}, fmt.Errorf("orchestrator: running pre-PR S3 checks on %s: %w", checkBranch, s3Err)
			}
		} else if ran {
			s3Ran = true
			s3Passed = rep.Passed
			blastTier = rep.BlastTier
			headSHA = rep.HeadSHA
			if !rep.Passed {
				s3Summary = rep.FormatAgentFailure()
			}
		}
	}
	if !s3Ran {
		// In unit tests without a recorded check-run or mirror runtime, proceed cleanly.
		s3Passed = true
	}

	if !s3Passed {
		if !opts.AllowHoldAfterS3Cap {
			return PullRequestStatus{}, fmt.Errorf("%s", nonEmptySummary(s3Summary, "pre-PR deterministic quality checks (S3) failed"))
		}
		// S3 retry cap reached: promote staged work onto a held PR with AutoMerge stripped and GateQA recorded.
		if stageExists {
			if _, err := promoteQAStageBranch(client, *task.Target, stageBranch, task.BranchName(), headSHA); err != nil {
				return PullRequestStatus{}, fmt.Errorf("orchestrator: promoting held staging branch %s to %s: %w",
					stageBranch, task.BranchName(), err)
			}
		}
		if err := holdTaskForQA(ctx, store, task.ID, headSHA, firstLineSummary(s3Summary), time.Now().UTC()); err != nil {
			return PullRequestStatus{}, err
		}
		status, err := OpenPullRequestForTask(ctx, store, client, task)
		if err == nil {
			maybeEvaluateQACircuitBreakerThrottled(ctx, store, client, time.Now().UTC())
		}
		return status, err
	}

	// On configured blast tiers under a flow with EnableS4, defer live PR branch promotion so the
	// orchestrator can run bounded fresh-context S4 verification once the coding turn exits.
	if stageExists && flow.RequiresS4ForTier(blastTier) && canRunS4RepairRound(task, flow, headSHA) {
		_ = store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
			t.Links = withoutTaskLinkKinds(t.Links, model.LinkQAHold)
			t.Links = append(t.Links, model.Link{Kind: model.LinkQAHold, Target: headSHA})
			return nil
		})
		return PullRequestStatus{
			ChecksKnown: true,
			Note: fmt.Sprintf("Pre-PR deterministic quality checks (S3) passed on staging ref %s (blast tier %s). "+
				"End your turn now; Grain will run fresh-context S4 verification and publish the pull request automatically.",
				stageBranch, blastTier),
		}, nil
	}

	if stageExists {
		if _, err := promoteQAStageBranch(client, *task.Target, stageBranch, task.BranchName(), headSHA); err != nil {
			return PullRequestStatus{}, fmt.Errorf("orchestrator: promoting verified staging branch %s to %s: %w",
				stageBranch, task.BranchName(), err)
		}
	}
	clearTaskQAHold(ctx, store, task.ID)
	status, err := OpenPullRequestForTask(ctx, store, client, task)
	if err == nil {
		var prFiles []string
		if task.Target != nil {
			if f, lErr := client.ListPullRequestFiles(task.Target.StoredOwner(), task.Target.Name, status.PullRequest.Number); lErr == nil {
				prFiles = f
			}
		}
		EvaluateAndApplyQATriage(ctx, store, client, task, assign, headSHA, prFiles, s3Passed)
		maybeEvaluateQACircuitBreakerThrottled(ctx, store, client, time.Now().UTC())
	}
	return status, err
}

// salvageQAStageIfPresent runs S3 (if not yet evaluated for the staged commit) and bounded 1-round
// S4 verification on treatment-arm tasks before promoting grain-qa-stage/<task-id> to task.BranchName().
//
// Returns handled=true when the task was parked/requeued for repair (so finishWithPullRequest must NOT
// open a premature GitHub PR yet). Returns handled=false once any staging branch has been promoted
// and is ready for standard finishWithPullRequest processing.
func salvageQAStageIfPresent(
	ctx context.Context,
	store *model.Store,
	client github.Client,
	task model.Task,
	now time.Time,
) (handled bool, err error) {
	if store == nil || client == nil || task.Target == nil {
		return false, nil
	}
	assign, qaCfg, aErr := resolve.ActiveQAResolutionForTask(ctx, store, task.ID)
	qaActive := aErr == nil && assign != nil && qaCfg.Enabled && assign.IsEnforcedTreatment()

	stageBranch := model.QAStageBranchName(task.ID)
	stageExists, err := targetBranchExists(client, *task.Target, stageBranch)
	if err != nil {
		return false, fmt.Errorf("orchestrator: checking %s's staging branch: %w", task.ID, err)
	}

	if !qaActive && stageExists {
		err := PromoteStrandedQAStage(ctx, store, client, task, "Quality checks disabled or task no longer gated")
		if err != nil {
			return false, err
		}
		return true, nil
	}
	if !qaActive {
		return false, nil
	}
	checkBranch := stageBranch
	if !stageExists {
		// If the branch was pushed directly without staging (e.g. in-process test harness), check task.BranchName().
		pushedMain, pErr := targetBranchExists(client, *task.Target, task.BranchName())
		if pErr != nil || !pushedMain {
			return false, pErr
		}
		checkBranch = task.BranchName()
	}

	closed, err := taskClosed(ctx, store, task.ID)
	if err != nil {
		return true, err
	}
	if closed {
		if stageExists {
			_ = client.DeleteBranch(task.Target.StoredOwner(), task.Target.Name, stageBranch)
		}
		return true, noteOrphanedPullRequests(ctx, store, client, task.ID, now)
	}

	head, err := client.GetBranchHead(task.Target.StoredOwner(), task.Target.Name, checkBranch)
	if err != nil || head == nil {
		return false, err
	}
	headSHA := head.SHA

	fresh, err := store.GetTask(ctx, task.ID)
	if err != nil {
		return true, err
	}
	if fresh != nil {
		task = *fresh
	}

	// 1. Check whether in-sandbox S3 already evaluated headSHA; otherwise run S3 over shallow mirror.
	s3Passed, s3Ran, blastTier, s3Summary := loadRecordedS3Outcome(ctx, store, task.ID, headSHA)
	var mirrorWS *checks.MirrorWorkspace
	var s3HazardSites map[string][]int
	if !s3Ran {
		rep, ws, ran, mErr := runMirrorS3WithWorkspace(ctx, store, task, assign, false, checkBranch, "")
		if mErr == nil && ran {
			s3Ran = true
			s3Passed = rep.Passed
			blastTier = rep.BlastTier
			s3HazardSites = rep.HazardSites
			mirrorWS = ws
			if !rep.Passed {
				s3Summary = rep.FormatAgentFailure()
			}
		} else if mErr != nil && LiveQARuntime() != nil && LiveQARuntime().DataDir != "" {
			// Mirror infrastructure error: promote onto a held PR (AutoMerge stripped) rather than silently passing.
			if stageExists {
				if _, err := promoteQAStageBranch(client, *task.Target, stageBranch, task.BranchName(), headSHA); err != nil {
					return true, err
				}
			}
			if err := holdTaskForQA(ctx, store, task.ID, headSHA, fmt.Sprintf("pre-PR mirror check error: %v", mErr), now); err != nil {
				return true, err
			}
			return false, nil
		} else {
			s3Passed = true
		}
	}

	if !s3Passed {
		if !hasTaskLink(task, model.LinkQASalvageRetried) {
			if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
				if !hasTaskLink(*t, model.LinkQASalvageRetried) {
					t.Links = append(t.Links, model.Link{Kind: model.LinkQASalvageRetried, Target: headSHA})
				}
				return nil
			}); err != nil {
				return true, err
			}
			if err := noteOnTask(ctx, store, task.ID, s3Summary, now); err != nil {
				return true, err
			}
			if err := observeField(ctx, store, task.ID, now, func(o *model.Observation) {
				o.CompletedAt = nil
				o.RetryRequestedAt = &now
			}); err != nil {
				return true, err
			}
			return true, nil
		}

		// Retry cap reached: promote branch so work is visible on a held PR, strip auto-merge, and attach GateQA findings.
		if stageExists {
			if _, err := promoteQAStageBranch(client, *task.Target, stageBranch, task.BranchName(), headSHA); err != nil {
				return true, err
			}
		}
		if err := holdTaskForQA(ctx, store, task.ID, headSHA, firstLineSummary(s3Summary), now); err != nil {
			return true, err
		}
		return false, nil
	}

	// 2. S3 passed! Run bounded fresh-context S4 verifier when required for this blast tier and repair rounds remain.
	flow := *assign.Spec
	if flow.RequiresS4ForTier(blastTier) && canRunS4RepairRound(task, flow, headSHA) {
		s4Findings, s4Err := runFreshContextS4(ctx, store, task, assign, headSHA, checkBranch, mirrorWS, s3HazardSites)
		if s4Err != nil {
			log.Printf("orchestrator: task %s S4 verification degraded to S3-pass: %v", task.ID, s4Err)
		} else {
			if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
				if !hasTaskLinkTarget(*t, model.LinkQAS4Attempted, headSHA) {
					t.Links = append(t.Links, model.Link{Kind: model.LinkQAS4Attempted, Target: headSHA})
				}
				return nil
			}); err != nil {
				return true, err
			}
			if len(s4Findings) > 0 {
				note := formatS4RepairNote(headSHA, s4Findings)
				if err := noteOnTask(ctx, store, task.ID, note, now); err != nil {
					return true, err
				}
				if err := observeField(ctx, store, task.ID, now, func(o *model.Observation) {
					o.CompletedAt = nil
					o.RetryRequestedAt = &now
				}); err != nil {
					return true, err
				}
				return true, nil
			}
		}
	}

	// 2b. Run any post-S4 custom stages (insert-after: s4) that have not yet run for headSHA.
	if hasUnrecordedCustomStageForSlot(ctx, store, task.ID, headSHA, flow, "s4") {
		if mirrorWS == nil {
			if rt := LiveQARuntime(); rt != nil && rt.DataDir != "" {
				// A live runtime that cannot open the mirror must not let post-S4 custom
				// stages pass vacuously over an empty diff: hold the promoted PR instead,
				// matching the pre-PR mirror-error path above.
				var openErr error
				if rt.ResolveRemote == nil {
					openErr = fmt.Errorf("no remote resolver configured")
				} else if remoteURL, bearer, rErr := rt.ResolveRemote(ctx, *task.Target); rErr != nil {
					openErr = rErr
				} else if remoteURL == "" {
					openErr = fmt.Errorf("empty remote URL")
				} else {
					mirrorWS, openErr = checks.OpenMirrorWorkspace(ctx, filepath.Join(rt.DataDir, "qa-repos"), remoteURL, bearer, task.Base, checkBranch)
				}
				if mirrorWS == nil {
					if openErr == nil {
						openErr = fmt.Errorf("mirror workspace unavailable")
					}
					if stageExists {
						if _, err := promoteQAStageBranch(client, *task.Target, stageBranch, task.BranchName(), headSHA); err != nil {
							return true, err
						}
					}
					if err := holdTaskForQA(ctx, store, task.ID, headSHA, fmt.Sprintf("post-S4 custom stage mirror error: %v", openErr), now); err != nil {
						return true, err
					}
					return false, nil
				}
			}
		}
		var changedFiles []string
		if mirrorWS != nil {
			if rawDiff, dErr := mirrorWS.BaseHeadDiff(ctx); dErr == nil {
				changedFiles = checks.ParseUnifiedDiff(rawDiff).AllPaths()
			}
		}
		postRep := checks.Report{
			Passed:       true,
			HeadSHA:      headSHA,
			BlastTier:    blastTier,
			ChangedFiles: changedFiles,
		}
		executeCustomStages(ctx, store, task, "", assign, false, "", mirrorWS, &postRep, "s4")
		if !postRep.Passed {
			failSummary := postRep.FormatAgentFailure()
			if !hasTaskLink(task, model.LinkQASalvageRetried) {
				_ = store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
					if !hasTaskLink(*t, model.LinkQASalvageRetried) {
						t.Links = append(t.Links, model.Link{Kind: model.LinkQASalvageRetried, Target: headSHA})
					}
					return nil
				})
				if err := noteOnTask(ctx, store, task.ID, failSummary, now); err != nil {
					return true, err
				}
				if err := observeField(ctx, store, task.ID, now, func(o *model.Observation) {
					o.CompletedAt = nil
					o.RetryRequestedAt = &now
				}); err != nil {
					return true, err
				}
				return true, nil
			}
			if stageExists {
				if _, err := promoteQAStageBranch(client, *task.Target, stageBranch, task.BranchName(), headSHA); err != nil {
					return true, err
				}
			}
			if err := holdTaskForQA(ctx, store, task.ID, headSHA, firstLineSummary(failSummary), now); err != nil {
				return true, err
			}
			return false, nil
		}
	}

	// 3. Verified! Promote staging branch to real PR branch and clear any QA hold.
	if stageExists {
		if _, err := promoteQAStageBranch(client, *task.Target, stageBranch, task.BranchName(), headSHA); err != nil {
			return true, fmt.Errorf("orchestrator: promoting staging branch %s to %s: %w", stageBranch, task.BranchName(), err)
		}
	}
	clearTaskQAHold(ctx, store, task.ID)
	return false, nil
}

// promoteQAStageBranch moves/creates refs/heads/<prBranch> at refs/heads/<stageBranch>'s tip SHA
// (verifying expectedSHA when non-empty) and deletes the temporary staging branch ref.
func promoteQAStageBranch(client github.Client, target model.RepoRef, stageBranch, prBranch, expectedSHA string) (string, error) {
	stageHead, err := client.GetBranchHead(target.StoredOwner(), target.Name, stageBranch)
	if err != nil {
		return "", err
	}
	if stageHead == nil || stageHead.SHA == "" {
		return "", fmt.Errorf("staging branch %s has no head commit on %s", stageBranch, target)
	}
	if expectedSHA != "" && stageHead.SHA != expectedSHA {
		return "", fmt.Errorf("staging branch %s moved from verified commit %s to %s before promotion",
			stageBranch, shortSHA(expectedSHA), shortSHA(stageHead.SHA))
	}
	existing, err := client.GetBranchHead(target.StoredOwner(), target.Name, prBranch)
	if err != nil {
		return "", err
	}
	if existing == nil {
		if err := client.CreateBranch(target.StoredOwner(), target.Name, prBranch, stageHead.SHA); err != nil {
			// Race tolerance if created concurrently.
			if uErr := client.UpdateBranch(target.StoredOwner(), target.Name, prBranch, stageHead.SHA, true); uErr != nil {
				return "", err
			}
		}
	} else if existing.SHA != stageHead.SHA {
		if err := client.UpdateBranch(target.StoredOwner(), target.Name, prBranch, stageHead.SHA, true); err != nil {
			return "", err
		}
	}
	_ = client.DeleteBranch(target.StoredOwner(), target.Name, stageBranch)
	return stageHead.SHA, nil
}

// effectiveTaskPushHead reads the active staging branch head first on treatment tasks, falling back
// to task.BranchName() so pre-run/post-run movement checks see staging pushes before promotion.
func effectiveTaskPushHead(ctx context.Context, store *model.Store, client github.Client, task model.Task) (*github.BranchHead, error) {
	if client == nil || task.Target == nil {
		return nil, nil
	}
	if store != nil {
		if assign, qaCfg, err := resolve.ActiveQAResolutionForTask(ctx, store, task.ID); err == nil && qaCfg.Enabled && assign != nil && assign.IsEnforcedTreatment() {
			if stageHead, sErr := client.GetBranchHead(task.Target.StoredOwner(), task.Target.Name, model.QAStageBranchName(task.ID)); sErr == nil && stageHead != nil {
				return stageHead, nil
			}
		}
	}
	return client.GetBranchHead(task.Target.StoredOwner(), task.Target.Name, task.BranchName())
}

func maybeRunControlShadowS3(store *model.Store, client github.Client, task model.Task, assign *resolve.Resolution) {
	rt := LiveQARuntime()
	if rt == nil || rt.DataDir == "" || rt.ResolveRemote == nil || store == nil || client == nil || task.Target == nil || assign == nil {
		return
	}
	taskCopy := task
	assignCopy := *assign
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		head, err := client.GetBranchHead(taskCopy.Target.StoredOwner(), taskCopy.Target.Name, taskCopy.BranchName())
		if err != nil || head == nil {
			return
		}
		existing, err := store.ListQACheckRunsForTask(ctx, taskCopy.ID)
		if err == nil {
			for _, r := range existing {
				if r.Shadow && r.CommitSHA == head.SHA {
					return
				}
			}
		}
		_, _, _ = runMirrorS3IfConfigured(ctx, store, taskCopy, &assignCopy, true, taskCopy.BranchName(), "")
	}()
}

func runMirrorS3IfConfigured(
	ctx context.Context,
	store *model.Store,
	task model.Task,
	assign *resolve.Resolution,
	shadow bool,
	branch, callerNotes string,
) (checks.Report, bool, error) {
	rep, _, ran, err := runMirrorS3WithWorkspace(ctx, store, task, assign, shadow, branch, callerNotes)
	return rep, ran, err
}

func runMirrorS3WithWorkspace(
	ctx context.Context,
	store *model.Store,
	task model.Task,
	assign *resolve.Resolution,
	shadow bool,
	branch, callerNotes string,
) (checks.Report, *checks.MirrorWorkspace, bool, error) {
	rt := LiveQARuntime()
	if rt == nil || rt.DataDir == "" || rt.ResolveRemote == nil || task.Target == nil || assign == nil {
		return checks.Report{}, nil, false, nil
	}
	remoteURL, bearer, err := rt.ResolveRemote(ctx, *task.Target)
	if err != nil || strings.TrimSpace(remoteURL) == "" {
		return checks.Report{}, nil, false, err
	}
	ws, err := checks.OpenMirrorWorkspace(ctx, filepath.Join(rt.DataDir, "qa-repos"), remoteURL, bearer, task.Base, branch)
	if err != nil {
		return checks.Report{}, nil, false, err
	}
	var flow qa.FlowSpec
	if assign.Spec != nil {
		flow = *assign.Spec
	}
	armLabel := model.QAArmTreatment
	if shadow || assign.ArmID == model.QAArmControl {
		armLabel = model.QAArmControl
	}

	// CustomChecks referenced by a CustomStageDecl are evaluated inside executeCustomStages
	// when that stage's WhenPaths and MinTier match.
	s3Flow := flow
	if len(flow.CustomStages) > 0 && len(flow.CustomChecks) > 0 {
		stagedIDs := map[string]bool{}
		for _, stg := range flow.CustomStages {
			for _, cid := range stg.CheckIDs {
				stagedIDs[cid] = true
			}
		}
		var ungrouped []qa.CustomCheckDecl
		for _, cc := range flow.CustomChecks {
			if !stagedIDs[cc.ID] {
				ungrouped = append(ungrouped, cc)
			}
		}
		s3Flow.CustomChecks = ungrouped
	}

	rep, err := checks.RunS3(ctx, ws, s3Flow, checks.RunOptions{
		TaskID:        task.ID,
		Arm:           armLabel,
		Shadow:        shadow,
		Attempt:       1,
		CallerNotes:   callerNotes,
		TaskSpecBytes: len(task.Title) + len(task.Body),
		ExecInSandbox: rt.ExecInSandbox,
	}, rt.Profiler)
	if err != nil {
		return checks.Report{}, ws, false, err
	}
	persistS3ReportToStore(ctx, store, task, "", assign, shadow, callerNotes, ws.BaseSHA, rep)
	slots := []string{"s2", "s3", "s4"}
	if !shadow && flow.RequiresS4ForTier(rep.BlastTier) && canRunS4RepairRound(task, flow, rep.HeadSHA) {
		// Defer post-S4 custom stages until after fresh-context S4 verification finishes.
		slots = []string{"s2", "s3"}
	}
	executeCustomStages(ctx, store, task, "", assign, shadow, callerNotes, ws, &rep, slots...)
	return rep, ws, true, nil
}

func persistS3ReportToStore(
	ctx context.Context,
	store *model.Store,
	task model.Task,
	runID string,
	assign *resolve.Resolution,
	shadow bool,
	callerNotes, baseSHA string,
	rep checks.Report,
) {
	if store == nil {
		return
	}
	comp := qa.AssessTaskComplexity(qa.ComplexityInput{
		Title:       task.Title,
		Body:        task.Body,
		ReadsCount:  len(task.Reads),
		WritesCount: len(task.Writes),
	}, nil)

	detailMap := map[string]any{
		"blastTier":    rep.BlastTier,
		"blastScore":   rep.BlastScore,
		"blastReasons": rep.BlastReasons,
		"changedFiles": rep.ChangedFiles,
		"testTampered": rep.TestTampered,
	}
	if strings.TrimSpace(callerNotes) != "" {
		detailMap["callerNotes"] = strings.TrimSpace(callerNotes)
	}
	detailBytes, _ := json.Marshal(detailMap)

	armCol := model.QAArmTreatment
	if shadow || (assign != nil && assign.ArmID == model.QAArmControl) {
		armCol = model.QAArmControl
	}
	var expID, armID, flowHash string
	var engineRev int
	if assign != nil {
		expID = assign.ExperimentID
		armID = assign.ArmID
		flowHash = assign.FlowHash
		engineRev = assign.QAEngineRev
	}

	for _, cr := range rep.Results {
		stage := cr.Stage
		if strings.TrimSpace(stage) == "" {
			stage = "S3"
		}
		var modelFindings []model.QAFinding
		for _, f := range cr.Findings {
			fStage := f.Stage
			if strings.TrimSpace(fStage) == "" {
				fStage = stage
			}
			modelFindings = append(modelFindings, model.QAFinding{
				TaskID:   task.ID,
				Stage:    fStage,
				RuleID:   f.RuleID,
				Severity: f.Severity,
				FilePath: f.FilePath,
				Line:     f.Line,
				Summary:  f.Summary,
				Evidence: f.Evidence,
			})
		}
		_, _ = store.RecordQACheckRun(ctx, model.QACheckRun{
			TaskID:         task.ID,
			RunID:          runID,
			CommitSHA:      rep.HeadSHA,
			Stage:          stage,
			CheckID:        cr.CheckID,
			Arm:            armCol,
			Shadow:         shadow,
			Verdict:        cr.Verdict,
			DurationMS:     cr.DurationMS,
			AllocBytes:     cr.AllocBytes,
			DetailJSON:     string(detailBytes),
			ExperimentID:   expID,
			ArmID:          armID,
			FlowHash:       flowHash,
			QAEngineRev:    engineRev,
			Model:          task.Model,
			BaseSHA:        baseSHA,
			BlastTier:      string(rep.BlastTier),
			BlastScore:     rep.BlastScore,
			ComplexityTier: string(comp.Tier),
		}, modelFindings)
		if !shadow && cr.Verdict == "pass" && rep.HeadSHA != "" {
			_ = store.MarkPriorQACheckRunsRepaired(ctx, task.ID, cr.CheckID, rep.HeadSHA)
		}
	}
}

func hasUnrecordedCustomStageForSlot(ctx context.Context, store *model.Store, taskID, headSHA string, flow qa.FlowSpec, slot string) bool {
	if store == nil || len(flow.CustomStages) == 0 {
		return false
	}
	var slotIDs []string
	for _, stg := range flow.CustomStages {
		s := strings.ToLower(strings.TrimSpace(stg.InsertAfter))
		if s == "" {
			s = "s3"
		}
		if s == slot {
			slotIDs = append(slotIDs, stg.ID)
		}
	}
	if len(slotIDs) == 0 {
		return false
	}
	runs, err := store.ListQACheckRunsForTask(ctx, taskID)
	if err != nil {
		return true
	}
	recorded := map[string]bool{}
	for _, r := range runs {
		if !r.Shadow && r.CommitSHA == headSHA {
			recorded[r.Stage] = true
			recorded[r.CheckID] = true
		}
	}
	for _, id := range slotIDs {
		if !recorded[id] {
			return true
		}
	}
	return false
}

func executeCustomStages(
	ctx context.Context,
	store *model.Store,
	task model.Task,
	runID string,
	assign *resolve.Resolution,
	shadow bool,
	callerNotes string,
	ws *checks.MirrorWorkspace,
	rep *checks.Report,
	slots ...string,
) {
	if store == nil || assign == nil || assign.Spec == nil || rep == nil || len(assign.Spec.CustomStages) == 0 {
		return
	}
	if len(slots) == 0 {
		slots = []string{"s2", "s3", "s4"}
	}
	flow := *assign.Spec
	rt := LiveQARuntime()

	comp := qa.AssessTaskComplexity(qa.ComplexityInput{
		Title:       task.Title,
		Body:        task.Body,
		ReadsCount:  len(task.Reads),
		WritesCount: len(task.Writes),
	}, nil)

	armCol := model.QAArmTreatment
	if shadow || assign.ArmID == model.QAArmControl {
		armCol = model.QAArmControl
	}
	baseSHA := ""
	var rawDiff string
	var pd checks.ParsedDiff
	if ws != nil {
		baseSHA = ws.BaseSHA
		rawDiff, _ = ws.BaseHeadDiff(ctx)
		pd = checks.ParseUnifiedDiff(rawDiff)
	}

	customCheckByID := make(map[string]qa.CustomCheckDecl, len(flow.CustomChecks))
	for _, cc := range flow.CustomChecks {
		customCheckByID[cc.ID] = cc
	}

	detailBytes, _ := json.Marshal(map[string]any{
		"blastTier":    rep.BlastTier,
		"blastScore":   rep.BlastScore,
		"blastReasons": rep.BlastReasons,
		"changedFiles": rep.ChangedFiles,
		"testTampered": rep.TestTampered,
	})

	for _, insertSlot := range slots {
		for _, stg := range flow.CustomStages {
			slot := strings.ToLower(strings.TrimSpace(stg.InsertAfter))
			if slot == "" {
				slot = "s3"
			}
			if slot != insertSlot {
				continue
			}
			tierMatch := qa.BlastTierAtLeast(rep.BlastTier, qa.BlastTier(stg.MinTier))
			pathMatch := len(stg.WhenPaths) == 0 || qa.MatchesAnyGlob(stg.WhenPaths, rep.ChangedFiles)
			if !tierMatch || !pathMatch {
				continue
			}

			kind := strings.ToLower(strings.TrimSpace(stg.Kind))
			if kind == "" {
				kind = "deterministic_batch"
			}
			stageVerdict := "pass"
			var stageFindings []model.QAFinding
			var tokIn, tokOut int64
			start := time.Now()

			switch kind {
			case "deterministic_batch", "isolated_sandbox_check":
				for _, cid := range stg.CheckIDs {
					if cc, ok := customCheckByID[cid]; ok {
						fs, cErr := qa.EvaluateCustomCheck(cc, qa.CustomCheckContext{
							ChangedFiles:    pd.AllPaths(),
							AddedLines:      pd.AddedLinesMap(),
							TotalLinesAdded: pd.NonTestAdded + pd.TestAdded,
							CallerNotes:     callerNotes,
							ReadFile: func(p string) ([]byte, error) {
								if ws == nil {
									return nil, fmt.Errorf("no workspace")
								}
								return ws.ReadFileAtHead(ctx, p)
							},
						})
						if cErr != nil {
							continue
						}
						var chkFindings []checks.Finding
						var chkModelFindings []model.QAFinding
						hasBlock := false
						for _, f := range fs {
							sev := f.Severity
							if sev == "" {
								sev = cc.Severity
							}
							if sev == "" || sev == "blocking" || sev == "error" {
								hasBlock = true
							}
							chkFindings = append(chkFindings, checks.Finding{
								RuleID:   f.RuleID,
								Stage:    stg.ID,
								Severity: sev,
								FilePath: f.FilePath,
								Line:     f.Line,
								Summary:  f.Summary,
								Evidence: f.Evidence,
							})
							mf := model.QAFinding{
								TaskID:   task.ID,
								Stage:    stg.ID,
								RuleID:   f.RuleID,
								Severity: sev,
								FilePath: f.FilePath,
								Line:     f.Line,
								Summary:  f.Summary,
								Evidence: f.Evidence,
							}
							chkModelFindings = append(chkModelFindings, mf)
							stageFindings = append(stageFindings, mf)
						}
						cVerdict := "pass"
						if len(chkFindings) > 0 {
							if hasBlock {
								cVerdict = "fail"
								stageVerdict = "fail"
								rep.Passed = false
							} else if stageVerdict == "pass" {
								cVerdict = "warn"
								stageVerdict = "warn"
							} else {
								cVerdict = "warn"
							}
						}
						rep.Results = append(rep.Results, checks.CheckResult{
							CheckID:  cc.ID,
							Stage:    stg.ID,
							Verdict:  cVerdict,
							Findings: chkFindings,
						})
						rep.Findings = append(rep.Findings, chkFindings...)
						_, _ = store.RecordQACheckRun(ctx, model.QACheckRun{
							TaskID:         task.ID,
							RunID:          runID,
							CommitSHA:      rep.HeadSHA,
							Stage:          "S3",
							CheckID:        cc.ID,
							Arm:            armCol,
							Shadow:         shadow,
							Verdict:        cVerdict,
							DetailJSON:     string(detailBytes),
							ExperimentID:   assign.ExperimentID,
							ArmID:          assign.ArmID,
							FlowHash:       assign.FlowHash,
							QAEngineRev:    assign.QAEngineRev,
							Model:          task.Model,
							BaseSHA:        baseSHA,
							BlastTier:      string(rep.BlastTier),
							BlastScore:     rep.BlastScore,
							ComplexityTier: string(comp.Tier),
						}, chkModelFindings)
						if !shadow && cVerdict == "pass" && rep.HeadSHA != "" {
							_ = store.MarkPriorQACheckRunsRepaired(ctx, task.ID, cc.ID, rep.HeadSHA)
						}
						continue
					}
					for _, cr := range rep.Results {
						if cr.CheckID == cid {
							if cr.Verdict == "fail" {
								stageVerdict = "fail"
								rep.Passed = false
							} else if cr.Verdict == "warn" && stageVerdict == "pass" {
								stageVerdict = "warn"
							}
						}
					}
				}

			case "isolated_critic":
				if !shadow && assign.ArmID != model.QAArmControl && rt != nil && rt.RunIsolatedPrompt != nil {
					skillText := resolveCustomStageSkillText(flow, stg.SkillRef)
					tSec := stg.TimeoutSeconds
					if tSec <= 0 {
						tSec = stg.Timeout
					}
					if tSec <= 0 {
						tSec = 30
					}
					cCtx, cancel := context.WithTimeout(ctx, time.Duration(tSec)*time.Second)
					var promptBuf strings.Builder
					promptBuf.WriteString("You are an isolated pre-PR critic stage evaluating a candidate diff.\n")
					if skillText != "" {
						promptBuf.WriteString("\n## Critic Skill Instructions\n")
						promptBuf.WriteString(skillText)
						promptBuf.WriteString("\n")
					}
					fmt.Fprintf(&promptBuf, "\n## Task\nTitle: %s\n\n%s\n", task.Title, strings.TrimSpace(task.Body))
					if rawDiff != "" {
						diffExcerpt := rawDiff
						if len(diffExcerpt) > 32*1024 {
							diffExcerpt = diffExcerpt[:32*1024] + "\n... [diff truncated]"
						}
						fmt.Fprintf(&promptBuf, "\n## Candidate Diff\n```diff\n%s\n```\n", diffExcerpt)
					}
					out, inTok, outTok, pErr := rt.RunIsolatedPrompt(cCtx, task, task.ID+"-"+stg.ID, promptBuf.String(), 25000)
					cancel()
					tokIn = inTok
					tokOut = outTok
					if pErr == nil {
						trimmed := strings.TrimSpace(out)
						if parsed, jErr := qa.ExtractS4JSONFindings(trimmed); jErr == nil {
							for _, pf := range parsed {
								summary := strings.TrimSpace(pf.TriggerScenario)
								if summary == "" {
									summary = "isolated critic finding"
								}
								cf := checks.Finding{
									RuleID:   stg.ID,
									Stage:    stg.ID,
									Severity: "blocking",
									FilePath: pf.File,
									Line:     pf.Line,
									Summary:  summary,
									Evidence: pf.FixHint,
								}
								rep.Findings = append(rep.Findings, cf)
								stageFindings = append(stageFindings, model.QAFinding{
									TaskID:   task.ID,
									Stage:    stg.ID,
									RuleID:   stg.ID,
									Severity: "blocking",
									FilePath: pf.File,
									Line:     pf.Line,
									Summary:  summary,
									Evidence: pf.FixHint,
								})
							}
						} else if trimmed != "" && !strings.EqualFold(trimmed, "PASS") && !strings.EqualFold(trimmed, "OK") && trimmed != "[]" {
							summary := firstLineSummary(trimmed)
							cf := checks.Finding{
								RuleID:   stg.ID,
								Stage:    stg.ID,
								Severity: "blocking",
								Summary:  summary,
								Evidence: trimmed,
							}
							rep.Findings = append(rep.Findings, cf)
							stageFindings = append(stageFindings, model.QAFinding{
								TaskID:   task.ID,
								Stage:    stg.ID,
								RuleID:   stg.ID,
								Severity: "blocking",
								Summary:  summary,
								Evidence: trimmed,
							})
						}
						if len(stageFindings) > 0 {
							stageVerdict = "fail"
							rep.Passed = false
						}
					}
				}

			case "human_gate":
				stageVerdict = "hold"
				if !shadow && assign.ArmID != model.QAArmControl {
					label := strings.TrimSpace(stg.Label)
					if label == "" {
						label = stg.ID
					}
					_ = store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
						model.AddHumanReviewReason(t, model.HumanReviewReason{
							Source:   model.HumanReviewQATriage,
							Category: model.HumanReviewRisk,
							Reason:   fmt.Sprintf("custom human_gate %s matched (%s)", stg.ID, label),
						})
						return nil
					})
				}
			}

			_, _ = store.RecordQACheckRun(ctx, model.QACheckRun{
				TaskID:         task.ID,
				RunID:          runID,
				CommitSHA:      rep.HeadSHA,
				Stage:          stg.ID,
				CheckID:        stg.ID,
				Arm:            armCol,
				Shadow:         shadow,
				Verdict:        stageVerdict,
				DurationMS:     time.Since(start).Milliseconds(),
				TokensIn:       tokIn,
				TokensOut:      tokOut,
				DetailJSON:     string(detailBytes),
				ExperimentID:   assign.ExperimentID,
				ArmID:          assign.ArmID,
				FlowHash:       assign.FlowHash,
				QAEngineRev:    assign.QAEngineRev,
				Model:          task.Model,
				BaseSHA:        baseSHA,
				BlastTier:      string(rep.BlastTier),
				BlastScore:     rep.BlastScore,
				ComplexityTier: string(comp.Tier),
			}, stageFindings)
			if !shadow && stageVerdict == "pass" && rep.HeadSHA != "" {
				_ = store.MarkPriorQACheckRunsRepaired(ctx, task.ID, stg.ID, rep.HeadSHA)
			}
		}
	}
}

func resolveCustomStageSkillText(flow qa.FlowSpec, skillRef string) string {
	ref := strings.TrimSpace(skillRef)
	if ref == "" {
		return ""
	}
	for _, ss := range flow.SkillSteps {
		if ss.ID == ref && strings.TrimSpace(ss.Text) != "" {
			return strings.TrimSpace(ss.Text)
		}
	}
	for _, rp := range flow.ReadabilityPacks {
		if rp.ID == ref {
			if txt := qa.BuiltInReadabilityPackText(rp.ID); txt != "" {
				return txt
			}
			if strings.TrimSpace(rp.CustomText) != "" {
				return strings.TrimSpace(rp.CustomText)
			}
		}
	}
	switch strings.ToLower(ref) {
	case "verifier":
		return strings.TrimSpace(flow.VerifierSkillText)
	case "security":
		return strings.TrimSpace(flow.SecuritySkillText)
	case "plan-critic", "plan_critic":
		return strings.TrimSpace(flow.PlanCriticSkillText)
	case "intent":
		return strings.TrimSpace(flow.IntentSkillText)
	case "coding":
		return strings.TrimSpace(flow.SkillText)
	}
	if txt := qa.BuiltInReadabilityPackText(ref); txt != "" {
		return txt
	}
	return ref
}

func isPrePRDeterministicOrCustomStage(stage string) bool {
	if stage == "S3" || strings.HasPrefix(stage, "S3") {
		return true
	}
	switch stage {
	case "", "S0", "S0b", "S1", "S2", "S4", "S5", "S-promote":
		return false
	default:
		return true
	}
}

func loadRecordedS3Outcome(ctx context.Context, store *model.Store, taskID, headSHA string) (passed, ran bool, tier qa.BlastTier, summary string) {
	runs, err := store.ListQACheckRunsForTask(ctx, taskID)
	if err != nil || len(runs) == 0 {
		return false, false, qa.BlastT1, ""
	}
	passed = true
	tier = qa.BlastT1
	runIDs := map[int64]bool{}
	for _, r := range runs {
		if r.Shadow || !isPrePRDeterministicOrCustomStage(r.Stage) || r.CommitSHA != headSHA {
			continue
		}
		ran = true
		runIDs[r.ID] = true
		if r.Verdict == "fail" {
			passed = false
		}
		if r.DetailJSON != "" {
			var meta struct {
				BlastTier string `json:"blastTier"`
			}
			if json.Unmarshal([]byte(r.DetailJSON), &meta) == nil && meta.BlastTier != "" {
				tier = qa.BlastTier(meta.BlastTier)
			}
		}
	}
	if !ran {
		return false, false, qa.BlastT1, ""
	}
	if !passed {
		findings, _ := store.ListQAFindingsForTask(ctx, taskID)
		var lines []string
		for _, f := range findings {
			if runIDs[f.CheckRunID] {
				loc := f.FilePath
				if loc != "" && f.Line > 0 {
					loc = fmt.Sprintf("%s:%d", loc, f.Line)
				}
				lines = append(lines, fmt.Sprintf("- [%s] %s %s", f.RuleID, loc, f.Summary))
			}
		}
		summary = "Pre-PR deterministic quality checks (S3) found issues on " + shortSHA(headSHA) + ":\n" + strings.Join(lines, "\n") +
			"\nFix these issues, push to your branch, and call `open_pull_request`."
	}
	return passed, true, tier, summary
}

func runFreshContextS4(
	ctx context.Context,
	store *model.Store,
	task model.Task,
	assign *resolve.Resolution,
	headSHA, branch string,
	ws *checks.MirrorWorkspace,
	hazardSites map[string][]int,
) ([]qa.ValidatedS4Finding, error) {
	rt := LiveQARuntime()
	if rt == nil || (rt.VerifyS4 == nil && rt.RunIsolatedPrompt == nil) || task.Target == nil {
		return nil, nil
	}
	if ws == nil && rt.DataDir != "" && rt.ResolveRemote != nil {
		if remoteURL, bearer, rErr := rt.ResolveRemote(ctx, *task.Target); rErr == nil && remoteURL != "" {
			ws, _ = checks.OpenMirrorWorkspace(ctx, filepath.Join(rt.DataDir, "qa-repos"), remoteURL, bearer, task.Base, branch)
		}
	}
	if ws == nil {
		return nil, nil
	}

	rawDiff, err := ws.BaseHeadDiff(ctx)
	if err != nil || strings.TrimSpace(rawDiff) == "" {
		return nil, err
	}
	pd := checks.ParseUnifiedDiff(rawDiff)

	headFiles := map[string]string{}
	for _, df := range pd.Files {
		if df.IsDeleted || df.NewPath == "" {
			continue
		}
		if b, fErr := ws.ReadFileAtHead(ctx, df.NewPath); fErr == nil {
			headFiles[df.NewPath] = string(b)
		}
	}
	if hazardSites == nil {
		_, hz, _, _ := checks.CheckSiblingExportHazard(ctx, ws, pd, 2, "s4-context")
		hazardSites = hz
	}
	for file := range hazardSites {
		if _, exists := headFiles[file]; !exists {
			if b, fErr := ws.ReadFileAtHead(ctx, file); fErr == nil {
				headFiles[file] = string(b)
			}
		}
	}

	var spec qa.FlowSpec
	s4Timeout := 90 * time.Second
	maxS4Tokens := 50000
	if assign != nil && assign.Spec != nil {
		spec = *assign.Spec
		if spec.MaxS4Seconds > 0 {
			s4Timeout = time.Duration(spec.MaxS4Seconds) * time.Second
		}
		if spec.MaxS4Tokens > 0 {
			maxS4Tokens = spec.MaxS4Tokens
		}
	}

	prompt := buildS4VerifierPrompt(task, rawDiff, hazardSites, spec)
	s4Ctx, cancel := context.WithTimeout(ctx, s4Timeout)
	defer cancel()

	var rawOut string
	var tokIn, tokOut int64
	var kept []qa.ValidatedS4Finding
	var filteredCnt int

	sample, vErr := rt.Profiler.Measure(qa.PerfSample{
		TaskID:  task.ID,
		Stage:   "S4",
		CheckID: qa.RuleS4OrthogonalVerifier,
	}, func() (string, error) {
		var out string
		var inTok, outTok int64
		var err error
		if rt.RunIsolatedPrompt != nil {
			out, inTok, outTok, err = rt.RunIsolatedPrompt(s4Ctx, task, task.ID+"-qa-s4", prompt, maxS4Tokens)
			if (err != nil || strings.TrimSpace(out) == "") && rt.VerifyS4 != nil {
				out, inTok, outTok, err = rt.VerifyS4(s4Ctx, task, prompt)
			} else if rt.VerifyS4 != nil {
				if _, pErr := qa.ExtractS4JSONFindings(out); pErr != nil {
					out, inTok, outTok, err = rt.VerifyS4(s4Ctx, task, prompt)
				}
			}
		} else if rt.VerifyS4 != nil {
			out, inTok, outTok, err = rt.VerifyS4(s4Ctx, task, prompt)
		}
		rawOut = out
		tokIn = inTok
		tokOut = outTok
		if err != nil {
			return "error", err
		}
		parsed, pErr := qa.ExtractS4JSONFindings(rawOut)
		if pErr != nil {
			return "error", pErr
		}
		kept, filteredCnt = qa.FilterS4Findings(parsed, qa.InquisitorContext{
			HeadFiles:        headFiles,
			DiffHunks:        pd.HunkMap(),
			HazardSites:      hazardSites,
			FullDiffText:     rawDiff,
			SeenFingerprints: map[string]bool{},
		})
		if len(kept) > 0 {
			return "fail", nil
		}
		return "pass", nil
	})
	if vErr != nil {
		return nil, vErr
	}

	detailBytes, _ := json.Marshal(map[string]any{
		"filteredTheaterCount": filteredCnt,
	})
	var modelFindings []model.QAFinding
	for _, f := range kept {
		modelFindings = append(modelFindings, model.QAFinding{
			TaskID:   task.ID,
			Stage:    "S4",
			RuleID:   qa.RuleS4OrthogonalVerifier,
			Severity: "blocking",
			FilePath: f.File,
			Line:     f.Line,
			Summary:  f.Summary,
			Evidence: f.Evidence,
		})
	}
	var expID, armID, flowHash string
	var engineRev int
	if assign != nil {
		expID = assign.ExperimentID
		armID = assign.ArmID
		flowHash = assign.FlowHash
		engineRev = assign.QAEngineRev
	}
	_, _ = store.RecordQACheckRun(ctx, model.QACheckRun{
		TaskID:       task.ID,
		CommitSHA:    headSHA,
		Stage:        "S4",
		CheckID:      qa.RuleS4OrthogonalVerifier,
		Arm:          model.QAArmTreatment,
		Shadow:       false,
		Verdict:      sample.Verdict,
		DurationMS:   sample.DurationMS,
		TokensIn:     tokIn,
		TokensOut:    tokOut,
		AllocBytes:   sample.AllocBytes,
		DetailJSON:   string(detailBytes),
		ExperimentID: expID,
		ArmID:        armID,
		FlowHash:     flowHash,
		QAEngineRev:  engineRev,
		Model:        task.Model,
		BaseSHA:      ws.BaseSHA,
	}, modelFindings)

	return kept, nil
}

func buildS4VerifierPrompt(task model.Task, rawDiff string, hazardSites map[string][]int, spec qa.FlowSpec) string {
	const maxDiffPromptBytes = 48 * 1024
	diffExcerpt := rawDiff
	if len(diffExcerpt) > maxDiffPromptBytes {
		diffExcerpt = diffExcerpt[:maxDiffPromptBytes] + "\n... [diff truncated]"
	}
	var b strings.Builder
	b.WriteString("You are an orthogonal pre-PR defect verifier inspecting a candidate diff with zero author context.\n")
	b.WriteString("Report ONLY concrete, reproducible logic bugs, missed sibling caller breakages, concurrency/state bugs, or spec violations introduced by this diff.\n")
	b.WriteString("Do NOT report style nits, defensive boilerplate, hypothetical architecture wishes, or pre-existing code outside the diff.\n\n")
	if spec.VerifierChecklist == qa.ChecklistEscapeClassesV1 {
		b.WriteString("## Verification Checklist\n")
		b.WriteString(qa.EscapeClassesV1ChecklistText)
		b.WriteString("\n\n")
	}
	if vSkill := strings.TrimSpace(spec.VerifierSkillText); vSkill != "" {
		b.WriteString("## Verifier Skill Instructions\n")
		b.WriteString(vSkill)
		b.WriteString("\n\n")
	}
	if secSkill := strings.TrimSpace(spec.SecuritySkillText); secSkill != "" {
		b.WriteString("## Security Verification Instructions\n")
		b.WriteString(secSkill)
		b.WriteString("\n\n")
	}
	for _, step := range spec.SkillSteps {
		if step.Enabled && strings.EqualFold(step.Stage, "s4") && strings.TrimSpace(step.Text) != "" {
			b.WriteString(strings.TrimSpace(step.Text))
			b.WriteString("\n\n")
		}
	}
	fmt.Fprintf(&b, "## Task Specification\nTitle: %s\n\n%s\n\n", task.Title, strings.TrimSpace(task.Body))
	if len(hazardSites) > 0 {
		b.WriteString("## Untouched Sibling Caller Sites\n")
		for file, lines := range hazardSites {
			fmt.Fprintf(&b, "- %s (lines %v)\n", file, lines)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "## Candidate Unified Diff\n```diff\n%s\n```\n\n", diffExcerpt)
	b.WriteString("Respond ONLY with a JSON array (`[]` if no real defect exists). Each element MUST have exact keys:\n")
	b.WriteString("`{\"file\": \"path/in/repo.go\", \"line\": 42, \"category\": \"logic_bug|missed_caller|race|contract_mismatch\", \"trigger_scenario\": \"1-3 hop concrete input/state causing failure\", \"fix_hint\": \"minimal fix\"}`\n")
	return b.String()
}

func formatS4RepairNote(headSHA string, findings []qa.ValidatedS4Finding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Pre-PR verification (S4) identified %d defect(s) on %s before opening the pull request:\n\n", len(findings), shortSHA(headSHA))
	for i, f := range findings {
		fmt.Fprintf(&b, "%d. `%s:%d` — %s\n", i+1, f.File, f.Line, f.Summary)
		if f.Evidence != "" {
			fmt.Fprintf(&b, "   Fix hint: %s\n", f.Evidence)
		}
	}
	b.WriteString("\nAddress the defect(s) above, run tests, push your branch, and call `open_pull_request`.")
	return b.String()
}

func holdTaskForQA(ctx context.Context, store *model.Store, taskID, headSHA, summary string, now time.Time) error {
	return store.UpdateTask(ctx, taskID, func(t *model.Task) error {
		t.Links = withoutTaskLinkKinds(t.Links, model.LinkQAHold)
		t.Links = append(t.Links, model.Link{Kind: model.LinkQAHold, Target: headSHA})
		model.ClearReviewFindingsForGate(t, model.GateQA)
		t.Links = append(t.Links, model.ReviewFindingsRecord{
			Gate:    model.GateQA,
			Summary: nonEmptySummary(summary, "unresolved pre-PR quality check findings"),
			HeadSHA: headSHA,
			At:      now,
		}.Link())
		model.AwaitPersonForAutoMerge(t, "pre-PR quality checks failed after retry cap")
		return nil
	})
}

func clearTaskQAHold(ctx context.Context, store *model.Store, taskID string) {
	if store == nil {
		return
	}
	_ = store.UpdateTask(ctx, taskID, func(t *model.Task) error {
		t.Links = withoutTaskLinkKinds(t.Links, model.LinkQAHold)
		model.ClearReviewFindingsForGate(t, model.GateQA)
		return nil
	})
}

func hasTaskLink(t model.Task, kind model.LinkKind) bool {
	return slices.ContainsFunc(t.Links, func(l model.Link) bool { return l.Kind == kind })
}

func hasTaskLinkTarget(t model.Task, kind model.LinkKind, target string) bool {
	return slices.ContainsFunc(t.Links, func(l model.Link) bool { return l.Kind == kind && l.Target == target })
}

func countTaskLinks(t model.Task, kind model.LinkKind) int {
	n := 0
	for _, l := range t.Links {
		if l.Kind == kind {
			n++
		}
	}
	return n
}

func maxS4RepairRounds(flow qa.FlowSpec) int {
	return max(0, min(flow.MaxS4RepairRounds, 3))
}

func canRunS4RepairRound(t model.Task, flow qa.FlowSpec, headSHA string) bool {
	capRounds := maxS4RepairRounds(flow)
	if capRounds <= 0 {
		return false
	}
	if headSHA != "" && hasTaskLinkTarget(t, model.LinkQAS4Attempted, headSHA) {
		return false
	}
	return countTaskLinks(t, model.LinkQAS4Attempted) < capRounds
}

func withoutTaskLinkKinds(links []model.Link, kinds ...model.LinkKind) []model.Link {
	out := make([]model.Link, 0, len(links))
	for _, l := range links {
		if !slices.Contains(kinds, l.Kind) {
			out = append(out, l)
		}
	}
	return out
}

func firstLineSummary(s string) string {
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t != "" {
			if len(t) > 160 {
				return t[:160]
			}
			return t
		}
	}
	return ""
}

func nonEmptySummary(s, fallback string) string {
	if strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s)
	}
	return fallback
}

var (
	qaCircuitBreakerThrottleMu sync.Mutex
	qaLastEvalStore            *model.Store
	qaLastEvalAt               time.Time
)

const qaCircuitBreakerEvalWindow = 30 * 24 * time.Hour

func maybeEvaluateQACircuitBreakerThrottled(ctx context.Context, store *model.Store, client github.Client, now time.Time) {
	if store == nil {
		return
	}
	defer func() {
		if p := recover(); p != nil {
			log.Printf("orchestrator: recovered panic in QA circuit breaker evaluation: %v", p)
		}
	}()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	qaCircuitBreakerThrottleMu.Lock()
	if qaLastEvalStore == store && now.Sub(qaLastEvalAt) < 30*time.Second && !now.Before(qaLastEvalAt) {
		qaCircuitBreakerThrottleMu.Unlock()
		return
	}
	qaLastEvalStore = store
	qaLastEvalAt = now
	qaCircuitBreakerThrottleMu.Unlock()

	if _, err := EvaluateRunningExperimentHarm(ctx, store, client, now); err != nil {
		log.Printf("orchestrator: evaluating QA experiment harm guardrail: %v", err)
	}
	if _, _, err := MaybeAutoDowngradeQARollout(ctx, store, now); err != nil {
		log.Printf("orchestrator: evaluating QA circuit breaker: %v", err)
	}
}

// EvaluateRunningExperimentHarm evaluates the pre-registered one-sided mSPRT harm rule
// (alpha = 0.01, Hodges-Lehmann shift > +25%, >= 15 work trees per arm) on work-tree median
// flow.time_to_pr for a running experiment. When triggered, it halts the offending arm
// (if >= 2 active treatments) or stops the experiment (if 1 active treatment) and promotes
// stranded QA staging branches fail-open.
func EvaluateRunningExperimentHarm(ctx context.Context, store *model.Store, client github.Client, now time.Time) (bool, error) {
	if store == nil {
		return false, nil
	}
	running, err := store.GetRunningQAExperiment(ctx)
	if err != nil || running == nil {
		return false, err
	}
	enrollments, err := store.ListQAEnrollments(ctx, running.ID)
	if err != nil || len(enrollments) == 0 {
		return false, err
	}
	armByOrigin := map[string]string{}
	for _, e := range enrollments {
		if e.ArmID != "excluded" && e.ArmID != "" {
			armByOrigin[e.RootOrigin] = e.ArmID
		}
	}
	if len(armByOrigin) < 30 {
		return false, nil
	}

	tasks, err := store.ListTasks(ctx)
	if err != nil {
		return false, err
	}
	timings, err := store.TaskTimings(ctx)
	if err != nil {
		return false, err
	}
	runs, err := store.RunTimings(ctx)
	if err != nil {
		return false, err
	}

	firstRunByTask := map[string]time.Time{}
	for _, r := range runs {
		if r.StartedAt.IsZero() {
			continue
		}
		if prev, ok := firstRunByTask[r.TaskID]; !ok || r.StartedAt.Before(prev) {
			firstRunByTask[r.TaskID] = r.StartedAt
		}
	}
	prOpenedByTask := map[string]time.Time{}
	for _, tm := range timings {
		if tm.PrOpenedAt != nil && !tm.PrOpenedAt.IsZero() {
			prOpenedByTask[tm.TaskID] = *tm.PrOpenedAt
		}
	}

	originDurations := map[string][]float64{}
	for _, t := range tasks {
		root := model.ResolveQARootOrigin(t.WorkOrigin, t.ID)
		if armByOrigin[root] == "" {
			continue
		}
		started, okS := firstRunByTask[t.ID]
		opened, okP := prOpenedByTask[t.ID]
		if okS && okP && !opened.Before(started) {
			originDurations[root] = append(originDurations[root], opened.Sub(started).Seconds())
		}
	}

	timesByArm := map[string][]float64{}
	for root, durs := range originDurations {
		if len(durs) == 0 {
			continue
		}
		slices.Sort(durs)
		med := durs[len(durs)/2]
		if len(durs)%2 == 0 {
			med = 0.5 * (durs[len(durs)/2-1] + durs[len(durs)/2])
		}
		timesByArm[armByOrigin[root]] = append(timesByArm[armByOrigin[root]], med)
	}

	ctrlTimes := timesByArm[model.QAArmControl]
	if len(ctrlTimes) < 15 {
		return false, nil
	}
	tau := running.Plan.HarmRefTau
	if tau <= 0 {
		tau = 300.0
	}

	activeTreatments := 0
	for _, a := range running.Arms {
		if a.ID != model.QAArmControl && !a.Halted {
			activeTreatments++
		}
	}
	if activeTreatments == 0 {
		return false, nil
	}

	for _, arm := range running.Arms {
		if arm.ID == model.QAArmControl || arm.Halted {
			continue
		}
		treatTimes := timesByArm[arm.ID]
		if len(treatTimes) < 15 {
			continue
		}
		_, _, harmed, hlShiftPct := expstats.NormalMixtureSPRTOneSided(treatTimes, ctrlTimes, tau, 0.01)
		if !harmed || hlShiftPct <= 0.25 {
			continue
		}

		reason := fmt.Sprintf("auto-tripped flow.time_to_pr harm rule on arm %s (+%.1f%% HL shift, alpha=0.01)", arm.ID, hlShiftPct*100)
		if activeTreatments >= 2 {
			_, uErr := store.UpdateQAExperiment(ctx, running.ID, 0, func(e *model.QAExperiment) error {
				for i := range e.Arms {
					if e.Arms[i].ID == arm.ID && !e.Arms[i].Halted {
						e.Arms[i].Halted = true
						e.Arms[i].HaltReason = reason
						e.Arms[i].HaltedAt = now.Format(time.RFC3339)
					}
				}
				return nil
			})
			if uErr != nil {
				return false, uErr
			}
		} else {
			_, uErr := store.UpdateQAExperiment(ctx, running.ID, 0, func(e *model.QAExperiment) error {
				if e.State == model.QAExpStateRunning {
					e.State = model.QAExpStateStopped
					e.StoppedAt = &now
					e.Decision = &model.QAExperimentDecision{
						Outcome:   "reject",
						Arm:       arm.ID,
						Reason:    reason,
						DecidedBy: "qa-harm-guardrail",
						DecidedAt: now.Format(time.RFC3339),
					}
				}
				return nil
			})
			if uErr != nil {
				return false, uErr
			}
		}
		_, _ = store.RecordConfigAudit(ctx, model.ConfigAuditEntry{
			At: now,
			By: model.Principal{
				Kind:   model.PrincipalAutomation,
				ID:     "qa-harm-guardrail",
				Source: model.SourceInternal,
			},
			Surface: "qa-experiment:" + running.ID,
			Summary: reason,
		})
		PromoteStrandedQAStagesForExperiment(ctx, store, client, running.ID, reason)
		return true, nil
	}
	return false, nil
}

// MaybeAutoDowngradeQARollout evaluates mSPRT latency/escape, context ThreadCut, and operator
// Not-Helpful guardrails once at least MinTreatmentSampleForAutoDowngrade WorkOrigins have been
// assigned in the active (epoch, regime). When a safety rail trips, it atomically downgrades
// rolloutPct to 0% inside Store.UpdateConfig (preserving shadowOnControl=true and advancing
// regimeId) and records a config_audit entry.
func MaybeAutoDowngradeQARollout(ctx context.Context, store *model.Store, now time.Time) (bool, []string, error) {
	if store == nil {
		return false, nil, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	cfg, err := store.GetConfig(ctx)
	if err != nil || cfg == nil {
		return false, nil, err
	}
	if !cfg.QA.Enabled || cfg.QA.RolloutPct <= 0 {
		return false, nil, nil
	}
	epoch := cfg.QA.EpochOrDefault()
	regime := cfg.QA.RegimeIDOrDefault()
	assignments, err := store.ListQAAssignments(ctx)
	if err != nil {
		return false, nil, err
	}
	activeOrigins := map[string]bool{}
	var activeAssignments []model.QAAssignment
	treatCount := 0
	for _, a := range assignments {
		if a.Epoch == epoch && a.RegimeID == regime {
			activeAssignments = append(activeAssignments, a)
			activeOrigins[a.RootOrigin] = true
			if a.Arm == model.QAArmTreatment {
				treatCount++
			}
		}
	}
	if treatCount < metrics.MinTreatmentSampleForAutoDowngrade {
		return false, nil, nil
	}

	tasks, err := store.ListTasks(ctx)
	if err != nil {
		return false, nil, err
	}
	activeTaskIDs := map[string]bool{}
	for _, t := range tasks {
		if activeOrigins[model.ResolveQARootOrigin(t.WorkOrigin, t.ID)] {
			activeTaskIDs[t.ID] = true
		}
	}

	allCheckRuns, err := store.ListAllQACheckRuns(ctx)
	if err != nil {
		return false, nil, err
	}
	checkRuns := make([]model.QACheckRun, 0, len(allCheckRuns))
	activeCheckRunIDs := map[int64]bool{}
	for _, cr := range allCheckRuns {
		if activeTaskIDs[cr.TaskID] {
			checkRuns = append(checkRuns, cr)
			if cr.ID > 0 {
				activeCheckRunIDs[cr.ID] = true
			}
		}
	}

	allFindings, err := store.ListAllQAFindings(ctx)
	if err != nil {
		return false, nil, err
	}
	findings := make([]model.QAFinding, 0, len(allFindings))
	for _, f := range allFindings {
		if (f.CheckRunID > 0 && activeCheckRunIDs[f.CheckRunID]) || activeTaskIDs[f.TaskID] {
			findings = append(findings, f)
		}
	}

	timings, err := store.TaskTimings(ctx)
	if err != nil {
		return false, nil, err
	}
	runs, err := store.RunTimings(ctx)
	if err != nil {
		return false, nil, err
	}
	prompts, err := store.RunPrompts(ctx)
	if err != nil {
		return false, nil, err
	}
	usages, err := store.RunUsagesSince(ctx, time.Time{})
	if err != nil {
		return false, nil, err
	}
	ciRuns, err := store.CIRunOutcomes(ctx)
	if err != nil {
		return false, nil, err
	}

	summary := metrics.ComputeQASummary(metrics.QASummaryInput{
		Window: metrics.Window{
			Since: now.Add(-qaCircuitBreakerEvalWindow),
			Until: now.Add(time.Second),
		},
		Config:      cfg.QA,
		Assignments: activeAssignments,
		CheckRuns:   checkRuns,
		Findings:    findings,
		Tasks:       tasks,
		Timings:     timings,
		Runs:        runs,
		Prompts:     prompts,
		Usages:      usages,
		CIRuns:      ciRuns,
	})
	if !summary.CircuitBreaker.ShouldAutoDowngrade {
		return false, nil, nil
	}

	var didDowngrade bool
	var prevPct int
	err = store.UpdateConfig(ctx, func(fresh *model.Config) error {
		if !fresh.QA.Enabled || fresh.QA.RolloutPct <= 0 {
			return nil
		}
		if fresh.QA.EpochOrDefault() != epoch || fresh.QA.RegimeIDOrDefault() != regime {
			return nil
		}
		prevPct = fresh.QA.RolloutPct
		downgraded, advErr := model.AdvanceQAConfig(fresh.QA, model.QAConfig{
			Enabled:         true,
			FlowID:          fresh.QA.FlowIDOrDefault(),
			RolloutPct:      0,
			ShadowOnControl: model.BoolPtr(true),
			Epoch:           fresh.QA.EpochOrDefault(),
		})
		if advErr != nil {
			return advErr
		}
		fresh.QA = downgraded
		didDowngrade = true
		return nil
	})
	if err != nil || !didDowngrade {
		return false, nil, err
	}
	reasons := slices.Clone(summary.CircuitBreaker.Reasons)
	auditSummary := fmt.Sprintf("auto-downgraded quality control rollout from %d%% to 0%% shadow-only (%s)",
		prevPct, strings.Join(reasons, "; "))
	_, _ = store.RecordConfigAudit(ctx, model.ConfigAuditEntry{
		At: now,
		By: model.Principal{
			Kind:   model.PrincipalAutomation,
			ID:     "qa-circuit-breaker",
			Source: model.SourceInternal,
		},
		Surface: "settings",
		Summary: auditSummary,
	})
	log.Printf("orchestrator: %s", auditSummary)
	return true, reasons, nil
}

// PromoteStrandedQAStage promotes a stranded QA stage branch to the task's live PR branch, if it exists,
// and records a fail-open check run.
func PromoteStrandedQAStage(ctx context.Context, store *model.Store, client github.Client, task model.Task, reason string) error {
	if store == nil || client == nil || task.Target == nil {
		return nil
	}
	stageRef := model.QAStageBranchName(task.ID)
	head, err := client.GetBranchHead(task.Target.StoredOwner(), task.Target.Name, stageRef)
	if err != nil || head == nil || head.SHA == "" {
		return nil // Doesn't exist, nothing to promote.
	}

	assign, qaCfg, err := resolve.ActiveQAResolutionForTask(ctx, store, task.ID)
	if err != nil {
		return err
	}
	if qaCfg.Enabled && assign != nil && assign.IsEnforcedTreatment() {
		return nil // Task is actively in treatment, not stranded.
	}

	promotedSHA, err := promoteQAStageBranch(client, *task.Target, model.QAStageBranchName(task.ID), task.BranchName(), head.SHA)
	if err != nil {
		return fmt.Errorf("promoting stranded QA stage: %w", err)
	}
	clearTaskQAHold(ctx, store, task.ID)

	status, err := OpenPullRequestForTask(ctx, store, client, task)
	if err != nil {
		return fmt.Errorf("opening PR after promotion: %w", err)
	}

	if status.PullRequest.Number > 0 {
		note := "Quality checks did not run: " + reason
		_, _ = client.CreateComment(task.Target.StoredOwner(), task.Target.Name, status.PullRequest.Number, note)
	}

	var expID, armID string
	if assign != nil {
		expID = assign.ExperimentID
		armID = assign.ArmID
	}
	detailBytes, _ := json.Marshal(map[string]string{"reason": reason})
	run := model.QACheckRun{
		TaskID:       task.ID,
		CommitSHA:    promotedSHA,
		Stage:        "S-promote",
		CheckID:      "qa:fail-open-promote",
		Arm:          "control",
		Verdict:      "unchecked",
		DetailJSON:   string(detailBytes),
		ExperimentID: expID,
		ArmID:        armID,
		QAEngineRev:  qa.QAEngineRev,
		Model:        task.Model,
	}
	_, err = store.RecordQACheckRun(ctx, run, nil)
	return err
}

// PromoteStrandedQAStagesForExperiment sweeps all tasks enrolled in experimentID and promotes any
// stranded staging branches whose arm is no longer actively enforced.
func PromoteStrandedQAStagesForExperiment(ctx context.Context, store *model.Store, client github.Client, experimentID, reason string) {
	if store == nil || client == nil || strings.TrimSpace(experimentID) == "" {
		return
	}
	enrollments, err := store.ListQAEnrollments(ctx, experimentID)
	if err != nil || len(enrollments) == 0 {
		return
	}
	enrolledOrigins := make(map[string]bool, len(enrollments))
	for _, e := range enrollments {
		enrolledOrigins[e.RootOrigin] = true
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		return
	}
	for _, t := range tasks {
		if t.Target == nil || !model.TaskEligibleForQAStaging(t) {
			continue
		}
		if closed, cErr := taskClosed(ctx, store, t.ID); cErr != nil || closed {
			continue
		}
		if !enrolledOrigins[model.ResolveQARootOrigin(t.WorkOrigin, t.ID)] {
			continue
		}
		_ = PromoteStrandedQAStage(ctx, store, client, t, reason)
	}
}

// MaybeRunPostPRQATriage evaluates S5 human-review vs auto-merge triage on a newly opened or
// updated pull request when S5 has not yet run for the PR's current head commit.
func MaybeRunPostPRQATriage(ctx context.Context, store *model.Store, client github.Client, task model.Task) {
	if store == nil || client == nil || task.Target == nil {
		return
	}
	assign, qaCfg, err := resolve.ActiveQAResolutionForTask(ctx, store, task.ID)
	if err != nil || !qaCfg.Enabled || assign == nil || assign.Spec == nil {
		return
	}
	if assign.Spec.S5MergeTriage.Mode == "" || assign.Spec.S5MergeTriage.Mode == "off" {
		return
	}
	fresh, err := store.GetTask(ctx, task.ID)
	if err == nil && fresh != nil {
		task = *fresh
	}
	head, err := client.GetBranchHead(task.Target.StoredOwner(), task.Target.Name, task.BranchName())
	if err != nil || head == nil || head.SHA == "" {
		return
	}
	s3Passed, s3Ran, _, _ := loadRecordedS3Outcome(ctx, store, task.ID, head.SHA)
	if !s3Ran {
		s3Passed = true
	}
	EvaluateAndApplyQATriage(ctx, store, client, task, assign, head.SHA, nil, s3Passed)
}

func EvaluateAndApplyQATriage(
	ctx context.Context,
	store *model.Store,
	client github.Client,
	task model.Task,
	assign *resolve.Resolution,
	headSHA string,
	prFiles []string,
	s3Passed bool,
) {
	if store == nil || assign == nil || assign.Spec == nil || assign.Spec.S5MergeTriage.Mode == "" || assign.Spec.S5MergeTriage.Mode == "off" {
		return
	}
	fresh, err := store.GetTask(ctx, task.ID)
	if err == nil && fresh != nil {
		task = *fresh
	}

	// Idempotent per (taskID, headSHA): skip if S5 already evaluated this commit.
	var s3BlastTier qa.BlastTier
	var s3BlastScore int
	var s3Reasons []string
	var s3Files []string
	var s3Tampered bool
	var hasCustomHumanGateHold bool
	if runs, rErr := store.ListQACheckRunsForTask(ctx, task.ID); rErr == nil {
		for _, r := range runs {
			if r.Stage == "S5" && r.CommitSHA == headSHA && headSHA != "" {
				return
			}
			if r.Verdict == "hold" && (headSHA == "" || r.CommitSHA == headSHA) && !r.Shadow && r.Stage != "S0" {
				hasCustomHumanGateHold = true
			}
			if r.Stage == "S3" && (headSHA == "" || r.CommitSHA == headSHA) && !r.Shadow {
				if r.BlastTier != "" {
					s3BlastTier = qa.MaxBlastTier(s3BlastTier, qa.BlastTier(r.BlastTier))
				}
				if r.BlastScore > s3BlastScore {
					s3BlastScore = r.BlastScore
				}
				var dm struct {
					BlastReasons []string `json:"blastReasons"`
					ChangedFiles []string `json:"changedFiles"`
					TestTampered bool     `json:"testTampered"`
				}
				if json.Unmarshal([]byte(r.DetailJSON), &dm) == nil {
					if len(s3Reasons) == 0 && len(dm.BlastReasons) > 0 {
						s3Reasons = dm.BlastReasons
					}
					if len(s3Files) == 0 && len(dm.ChangedFiles) > 0 {
						s3Files = dm.ChangedFiles
					}
					if dm.TestTampered {
						s3Tampered = true
					}
				}
			}
		}
	}

	if len(prFiles) == 0 && client != nil && task.Target != nil {
		for _, l := range task.Links {
			if l.Kind == model.LinkFixes {
				if prNum, ok := parsePRNumberFromLink(l.Target); ok && prNum > 0 {
					if f, lErr := client.ListPullRequestFiles(task.Target.StoredOwner(), task.Target.Name, prNum); lErr == nil {
						prFiles = f
					}
					break
				}
			}
		}
	}
	if len(prFiles) == 0 && len(s3Files) > 0 {
		prFiles = s3Files
	}

	risk := qa.AssessDiffBlast(qa.DiffStats{
		Files:        prFiles,
		TestTampered: s3Tampered,
	}, assign.Spec.RiskOverrides, nil)
	if s3BlastTier != "" {
		risk.Tier = qa.MaxBlastTier(risk.Tier, s3BlastTier)
	}
	if s3BlastScore > risk.Score {
		risk.Score = s3BlastScore
	}
	if len(risk.Reasons) == 0 && len(s3Reasons) > 0 {
		risk.Reasons = s3Reasons
	}
	if len(risk.Reasons) == 0 {
		risk.Reasons = []string{fmt.Sprintf("blast tier %s", risk.Tier)}
	}

	comp := qa.AssessTaskComplexity(qa.ComplexityInput{
		Title:       task.Title,
		Body:        task.Body,
		ReadsCount:  len(task.Reads),
		WritesCount: max(len(task.Writes), len(prFiles)),
	}, nil)

	var autoTiers, humTiers []qa.BlastTier
	for _, s := range assign.Spec.S5MergeTriage.AutoMergeTiers {
		autoTiers = append(autoTiers, qa.BlastTier(s))
	}
	for _, s := range assign.Spec.S5MergeTriage.RequireHumanTiers {
		humTiers = append(humTiers, qa.BlastTier(s))
	}

	isControlOrShadow := assign.Shadow || assign.ArmID == model.QAArmControl
	effectiveMode := assign.Spec.S5MergeTriage.Mode
	if isControlOrShadow {
		effectiveMode = string(qa.TriageShadow)
	}

	mergeRulesRequireHuman := false
	if p, ok := model.MergePolicyOf(task); ok && p.AutoMergeNever {
		mergeRulesRequireHuman = true
	}
	for _, r := range model.HumanReviewReasons(task) {
		if r.Source != model.HumanReviewQATriage {
			mergeRulesRequireHuman = true
			break
		}
	}
	for _, p := range prFiles {
		if slices.Contains(model.MergeRulesPaths, p) {
			mergeRulesRequireHuman = true
			break
		}
	}
	known := len(prFiles) > 0 && len(prFiles) < github.PullRequestFilesCap
	if !mergeRulesRequireHuman && task.Target != nil {
		if rules, rErr := store.MergeRulesOf(ctx, *task.Target); rErr == nil && rules != nil && !rules.Empty() {
			ciMode := model.CIModeGitHub
			if rc, cErr := store.GetRepoConfig(ctx, *task.Target); cErr == nil && rc != nil && rc.CIMode != "" {
				ciMode = rc.CIMode
			}
			eff := rules.Effective(prFiles, known, ciMode)
			if eff.AutoMergeNever {
				mergeRulesRequireHuman = true
			}
		}
	}

	gateTriggerMatched := hasPathTriggerHold(task) || hasCustomHumanGateHold
	if !gateTriggerMatched && task.Target != nil {
		if triggers, tErr := store.PathTriggersOf(ctx, *task.Target); tErr == nil {
			for _, rt := range triggers {
				if !rt.Trigger.Gate {
					continue
				}
				if !known || len(rt.Trigger.Paths) == 0 || len(rt.Trigger.Match(prFiles)) > 0 {
					gateTriggerMatched = true
					break
				}
			}
		}
	}

	ciRedOrPending := false
	if obs, oErr := store.GetObservation(ctx, task.ID); oErr == nil && obs != nil && obs.CI != nil {
		if !obs.CI.Done() {
			ciRedOrPending = true
		} else {
			switch strings.ToLower(strings.TrimSpace(obs.CI.Conclusion)) {
			case "failure", "errored", "cancelled", "timed_out", "action_required":
				ciRedOrPending = true
			}
		}
	}
	if !ciRedOrPending && client != nil && task.Target != nil && headSHA != "" {
		if checksList, cErr := client.ListCheckRuns(task.Target.StoredOwner(), task.Target.Name, headSHA); cErr == nil {
			for _, chk := range checksList {
				st := strings.ToLower(strings.TrimSpace(chk.Status))
				if st != "" && st != "completed" {
					ciRedOrPending = true
					break
				}
				concl := ""
				if chk.Conclusion != nil {
					concl = strings.ToLower(strings.TrimSpace(*chk.Conclusion))
				}
				if concl != "" && concl != "success" && concl != "neutral" && concl != "skipped" {
					ciRedOrPending = true
					break
				}
			}
		}
	}
	if !ciRedOrPending && task.Target != nil && headSHA != "" {
		repoFull := task.Target.StoredOwner() + "/" + task.Target.Name
		if ghaRuns, gErr := store.GitHubCIOutcomes(ctx); gErr == nil {
			for _, gr := range ghaRuns {
				if gr.Repo == repoFull && gr.Commit == headSHA {
					concl := strings.ToLower(strings.TrimSpace(gr.Conclusion))
					if concl != "success" && concl != "neutral" && concl != "skipped" {
						ciRedOrPending = true
						break
					}
				}
			}
		}
	}

	triageIn := qa.TriageInput{
		Mode:                       effectiveMode,
		AutoMergeTiers:             autoTiers,
		RequireHumanTiers:          humTiers,
		Assessment:                 risk,
		TrustBoundaryRequiresHuman: risk.TrustBoundaryHit || len(trustboundary.Classify(prFiles)) > 0,
		MergeRulesRequireHuman:     mergeRulesRequireHuman,
		GateTriggerMatched:         gateTriggerMatched,
		FilesTruncated:             len(prFiles) >= github.PullRequestFilesCap,
		CIRedOrPending:             ciRedOrPending,
		HasUnresolvedFindings:      !s3Passed || hasTaskLink(task, model.LinkQAHold) || hasTaskLink(task, model.LinkReviewFindings) || hasTaskLink(task, model.LinkSecurityHold),
		OperatorAutoMergeEnabled:   task.AutoMerge,
	}

	verdict := qa.EvaluateTriage(triageIn, nil)

	cr := model.QACheckRun{
		TaskID:         task.ID,
		RunID:          fmt.Sprintf("%s-qa-s5-%s", task.ID, shortSHA(headSHA)),
		CommitSHA:      headSHA,
		Stage:          "S5",
		CheckID:        "qa:merge-triage",
		Arm:            model.QAArmTreatment,
		Shadow:         isControlOrShadow || verdict.Mode == string(qa.TriageShadow),
		Verdict:        "pass",
		ExperimentID:   assign.ExperimentID,
		ArmID:          assign.ArmID,
		FlowHash:       assign.FlowHash,
		QAEngineRev:    assign.QAEngineRev,
		Model:          task.Model,
		BlastTier:      string(verdict.Tier),
		BlastScore:     verdict.BlastScore,
		ComplexityTier: string(comp.Tier),
		TriageDecision: string(verdict.Decision),
	}
	if isControlOrShadow {
		cr.Arm = model.QAArmControl
	}
	_, _ = store.RecordQACheckRun(ctx, cr, nil)

	if !isControlOrShadow && effectiveMode == string(qa.TriageEnforced) {
		if verdict.Decision == qa.TriageRequireHuman && verdict.SetTriageHold {
			reasonSummary := strings.Join(verdict.Reasons, ", ")
			_ = store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
				model.AddHumanReviewReason(t, model.HumanReviewReason{
					Source:   model.HumanReviewQATriage,
					Category: model.HumanReviewRisk,
					Reason:   fmt.Sprintf("[%s · %d/100] %s", verdict.Tier, verdict.BlastScore, reasonSummary),
				})
				return nil
			})
			notePrefix := fmt.Sprintf("QA Triage (S5) held %s for human review", shortSHA(headSHA))
			if !hasTaskLink(task, model.LinkQATriageHold) {
				_ = noteOnTask(ctx, store, task.ID, notePrefix+": "+reasonSummary, time.Now().UTC())
			}
		} else if verdict.Decision == qa.TriageRequireHuman && verdict.WithdrawAutoMerge {
			_ = store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
				t.AutoMerge = false
				return nil
			})
		} else if verdict.Decision == qa.TriageAutoMerge && !verdict.BlockedByFloor && !hasCustomHumanGateHold && hasTaskLink(task, model.LinkQATriageHold) {
			_ = store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
				t.Links = slices.DeleteFunc(t.Links, func(l model.Link) bool {
					if l.Kind == model.LinkQATriageHold {
						return true
					}
					if l.Kind == model.LinkHumanReviewHold {
						if r, ok := model.ParseHumanReviewReason(l.Target); ok && r.Source == model.HumanReviewQATriage {
							return true
						}
					}
					return false
				})
				return nil
			})
		}
	}

	if assign.ExperimentID != "" {
		maybeEvaluateExperimentTriageHarmBreaker(ctx, store, assign.ExperimentID)
	}
}

func parsePRNumberFromLink(target string) (int, bool) {
	idx := strings.LastIndex(target, "#")
	if idx < 0 || idx+1 >= len(target) {
		return 0, false
	}
	n := 0
	for _, ch := range target[idx+1:] {
		if ch < '0' || ch > '9' {
			return 0, false
		}
		n = n*10 + int(ch-'0')
	}
	return n, n > 0
}

func maybeEvaluateExperimentTriageHarmBreaker(ctx context.Context, store *model.Store, expID string) {
	exp, err := store.GetQAExperiment(ctx, expID)
	if err != nil || exp.State != model.QAExpStateRunning || len(exp.Arms) < 2 {
		return
	}
	refArmID := exp.ReferenceArmID
	if refArmID == "" {
		refArmID = exp.Arms[0].ID
	}
	enrollments, err := store.ListQAEnrollments(ctx, expID)
	if err != nil || len(enrollments) == 0 {
		return
	}
	armByOrigin := make(map[string]string, len(enrollments))
	for _, e := range enrollments {
		if e.ArmID != "" && e.ArmID != "excluded" {
			armByOrigin[e.RootOrigin] = e.ArmID
		}
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		return
	}
	escapedOrigin := map[string]bool{}
	originByTask := map[string]string{}
	for _, t := range tasks {
		root := model.ResolveQARootOrigin(t.WorkOrigin, t.ID)
		if armByOrigin[root] == "" {
			continue
		}
		originByTask[t.ID] = root
		for _, l := range t.Links {
			if l.Kind == model.LinkReviewFindings {
				if rec, ok := model.ParseReviewFindingsRecord(l.Target); ok && rec.Gate != model.GateQA && strings.TrimSpace(rec.Summary) != "" {
					escapedOrigin[root] = true
				}
			}
		}
	}
	runs, err := store.ListAllQACheckRuns(ctx)
	if err != nil {
		return
	}
	obsByArm := map[string][]expstats.TriageObservation{}
	seenOriginArm := map[string]bool{}
	for _, r := range runs {
		if r.ExperimentID != expID || r.Stage != "S5" {
			continue
		}
		root := originByTask[r.TaskID]
		if root == "" {
			continue
		}
		key := r.ArmID + "|" + root
		if seenOriginArm[key] {
			continue
		}
		seenOriginArm[key] = true
		obsByArm[r.ArmID] = append(obsByArm[r.ArmID], expstats.TriageObservation{
			RootOrigin: root,
			Decision:   r.TriageDecision,
			Escaped:    escapedOrigin[root],
		})
	}
	refObs := obsByArm[refArmID]
	for i := range exp.Arms {
		candArm := exp.Arms[i]
		if candArm.ID == refArmID || candArm.Halted {
			continue
		}
		v := expstats.EvaluateTriageHarmBreaker(refObs, obsByArm[candArm.ID], 0.01, 15)
		if v.Tripped {
			now := time.Now().UTC()
			haltReason := fmt.Sprintf("S5 auto-merged escape mSPRT breaker tripped (delta=+%.1f%%, p=%.4f)", v.DeltaEscapeRate*100, v.PValue)
			_, _ = store.UpdateQAExperiment(ctx, expID, exp.Revision, func(e *model.QAExperiment) error {
				for j := range e.Arms {
					if e.Arms[j].ID == candArm.ID {
						e.Arms[j].Halted = true
						e.Arms[j].HaltReason = haltReason
						e.Arms[j].HaltedAt = now.Format(time.RFC3339)
					}
				}
				return nil
			})
			_, _ = store.RecordConfigAudit(ctx, model.ConfigAuditEntry{
				At: now,
				By: model.Principal{
					Kind:   model.PrincipalAutomation,
					ID:     "qa-s5-triage-breaker",
					Source: model.SourceInternal,
				},
				Surface: "qa-experiment:" + expID,
				Summary: fmt.Sprintf("halted arm %s: %s", candArm.ID, haltReason),
			})
		}
	}
}
