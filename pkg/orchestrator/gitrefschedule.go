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
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// RefSource answers where a ref of a repository is right now, without
// cloning it: the daemon's `git ls-remote`, with the credential this
// deployment holds for that repository (daemon.RepoDeclarationSource).
type RefSource interface {
	// Ref is the commit ref of repo peels to -- an annotated tag's target
	// commit rather than the tag object -- or "" when repo has no such ref.
	Ref(ctx context.Context, repo model.RepoRef, ref string) (string, error)
}

// gitRefPollInterval is how often one git_ref schedule's ref is looked at:
// declarationPollInterval's reasoning, for the same `git ls-remote`.
const gitRefPollInterval = 2 * time.Minute

// gitRefTriggerStarter is who a git_ref firing's note on its task is from.
const gitRefTriggerStarter = "git-ref-trigger"

// gitRefPolled is when each git_ref schedule last asked its RefSource,
// keyed by store and schedule ID. In memory only: a restarted daemon looks
// at once, which costs one ls-remote and fires nothing the cursor has
// already seen.
var gitRefPolled sync.Map

// reconcileGitRefSchedules fires every enabled git_ref schedule
// (model.RecurrenceGitRef) whose watched ref has come to name a different
// commit since it last looked -- the event-driven alternative to waking an
// agent every few minutes to run `git ls-remote` and end nothing_needed.
//
// The commit last seen is kept per schedule (model.ScheduleRefCursor), so
// a restart neither re-fires nor misses a move that happened while the
// daemon was down. A schedule's first look only records where the ref is.
// A firing its own overlap policy suppresses (an earlier firing's task is
// still open) leaves the cursor where it was, so the move fires once that
// task closes rather than being dropped.
//
// The watched repository must be one this deployment may work in
// (Config.TargetRepos): a declaration is not a way to have the daemon read
// a repository nobody put on the list.
func reconcileGitRefSchedules(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Store == nil || deps.Refs == nil {
		return nil
	}
	schedules, err := deps.Store.ListSchedules(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: listing schedules for git_ref triggers: %w", err)
	}
	var targets []string
	loadedTargets := false
	// One ls-remote per (repo, ref) a cycle, however many schedules watch it.
	type answer struct {
		commit string
		err    error
	}
	asked := map[string]answer{}
	var errs []error
	for _, sched := range schedules {
		if !sched.Enabled || sched.Recurrence.Kind != model.RecurrenceGitRef || sched.Recurrence.GitRef == nil {
			continue
		}
		pollKey := fmt.Sprintf("%d|%s", deps.Store.ID(), sched.ID)
		if last, ok := gitRefPolled.Load(pollKey); ok && now.Sub(last.(time.Time)) < gitRefPollInterval {
			continue
		}
		trig := *sched.Recurrence.GitRef
		repo, err := model.ParseRepo(trig.Repo)
		if err != nil {
			sayOnce("git-ref-repo:"+sched.ID+":"+trig.Repo,
				"orchestrator: schedule %s watches %q, which is not a repository: %v", sched.ID, trig.Repo, err)
			continue
		}
		if model.IsConfigRepo(repo) && model.IsRepoDeclaredID(sched.ID) {
			// The parser refuses this (repodecl.parseRecurrence); this is
			// for a row that got past it. grain/config is answered off
			// grain's own disk, and no repository's file may reach it.
			sayOnce("git-ref-config:"+sched.ID,
				"orchestrator: schedule %s is declared by a repository and watches %s, grain's own "+
					"configuration; it is not looked at", sched.ID, repo)
			continue
		}
		if !loadedTargets {
			if targets, err = deps.Store.TargetRepos(ctx); err != nil {
				return errors.Join(append(errs, fmt.Errorf("orchestrator: reading target repos for git_ref triggers: %w", err))...)
			}
			loadedTargets = true
		}
		if len(targets) > 0 && !model.RepoAllowed(targets, repo) {
			sayOnce("git-ref-off-target:"+sched.ID+":"+strings.ToLower(repo.String()),
				"orchestrator: schedule %s watches %s %s, which is not on this deployment's target repo list; "+
					"it is not looked at until it is", sched.ID, repo, trig.Ref)
			continue
		}
		gitRefPolled.Store(pollKey, now)

		askKey := strings.ToLower(repo.String()) + " " + trig.Ref
		a, ok := asked[askKey]
		if !ok {
			a.commit, a.err = deps.Refs.Ref(ctx, repo, trig.Ref)
			asked[askKey] = a
		}
		if a.err != nil {
			sayOnce("git-ref-read:"+sched.ID+":"+a.err.Error(),
				"orchestrator: schedule %s: reading %s %s: %v", sched.ID, repo, trig.Ref, a.err)
			continue
		}
		if err := fireGitRefSchedule(ctx, deps, sched, repo, trig.Ref, a.commit, now); err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: schedule %s: %w", sched.ID, err))
		}
	}
	return errors.Join(errs...)
}

// fireGitRefSchedule is one schedule's turn, with the commit its ref is at
// now in hand.
func fireGitRefSchedule(ctx context.Context, deps Deps, sched model.Schedule, repo model.RepoRef,
	ref, commit string, now time.Time) error {
	if commit == "" {
		sayOnce("git-ref-missing:"+sched.ID+":"+strings.ToLower(repo.String())+" "+ref,
			"orchestrator: schedule %s watches %s %s, which does not exist yet; it fires once the ref is created and then moves",
			sched.ID, repo, ref)
		return nil
	}
	cursor, err := deps.Store.GetScheduleRefCursor(ctx, sched.ID)
	if err != nil {
		return err
	}
	watched := repo.String()
	next := model.ScheduleRefCursor{ScheduleID: sched.ID, Repo: watched, Ref: ref, Commit: commit, UpdatedAt: now}
	if cursor == nil || !strings.EqualFold(cursor.Repo, watched) || cursor.Ref != ref || cursor.Commit == "" {
		log.Printf("orchestrator: schedule %s starts watching %s %s at %s", sched.ID, watched, ref, shortCommit(commit))
		return deps.Store.PutScheduleRefCursor(ctx, next)
	}
	if cursor.Commit == commit {
		return nil
	}

	reason := fmt.Sprintf("%s of %s moved from %s to %s", ref, watched, cursor.Commit, commit)
	firing, err := deps.Store.FireScheduleWake(ctx, sched, now, deps.Config.settingsRepo(), reason)
	if err != nil {
		// Held, and retried on the next poll: the cursor stays where it
		// was. Logged once per distinct failure, as a cadence's is.
		news, rerr := deps.Store.RecordScheduleFiringFailure(ctx, sched, now, err)
		if rerr != nil {
			return errors.Join(fmt.Errorf("firing on %s: %w", reason, err), rerr)
		}
		if news {
			log.Printf("orchestrator: firing schedule %s on %s failed: %v; retried at each poll, "+
				"and this is not logged again until the failure changes", sched.ID, reason, err)
		}
		return nil
	}
	if firing.Skipped {
		// Its previous firing is still open. The move is not dropped: the
		// cursor stays, so it fires once that task closes.
		sayOnce("git-ref-held:"+sched.ID+":"+commit,
			"orchestrator: schedule %s: %s, but its previous firing is still open; it fires once that closes",
			sched.ID, reason)
		return nil
	}
	if err := parkScheduleFiring(ctx, deps, firing, now); err != nil {
		return err
	}
	switch {
	case firing.OffTargetRepos != nil:
		log.Printf("orchestrator: schedule %s: %s, but it fires into %s, which is not on this deployment's "+
			"target repo list; nothing was filed", sched.ID, reason, *firing.OffTargetRepos)
	case firing.StandingWorkOff != nil:
		log.Printf("orchestrator: schedule %s: %s, but it was skipped: %s", sched.ID, reason,
			model.StandingWorkOffReason(*firing.StandingWorkOff))
	default:
		log.Printf("orchestrator: schedule %s fired: %s", sched.ID, reason)
	}
	if firing.TaskID != "" && (sched.TemplateID == nil || !model.IsStandingTemplateID(*sched.TemplateID)) {
		// A standing template's woken task is told by FireScheduleWake
		// itself; an ordinary one is told here, since its prompt is the
		// template's and says nothing of which move it was filed for.
		if _, err := deps.Store.AddComment(ctx, model.Comment{
			TaskID: firing.TaskID,
			Author: model.Attribution{Actor: model.Principal{
				Kind: model.PrincipalAutomation, ID: gitRefTriggerStarter,
			}},
			Body:      fmt.Sprintf("Filed because `%s` of `%s` moved from `%s` to `%s`.", ref, watched, cursor.Commit, commit),
			CreatedAt: now,
		}); err != nil {
			log.Printf("orchestrator: telling %s why schedule %s fired: %v", firing.TaskID, sched.ID, err)
		}
	}
	return deps.Store.PutScheduleRefCursor(ctx, next)
}
