package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// grain's own escalations, raised as operator notifications
// (docs/design/durable-tasks.md, DT-5's "Existing escalations, mapped").
//
// Each is raised where the escalation already happens and cleared by the
// same code when its condition goes away, so a notification is never the
// only record of anything: the comment, the inbox row and the health
// finding it maps stay where they were, and the notification is what
// reaches the bell, the director and a subscribed steward.

// Who raises and clears each family, as notification.raised_by and
// cleared_by write it.
var (
	baseCINotifier     = model.PrincipalString(model.Principal{Kind: model.PrincipalAutomation, ID: "base-branch-ci"})
	releaseNotifier    = model.PrincipalString(model.Principal{Kind: model.PrincipalAutomation, ID: "base-branch-release"})
	mergeQueueNotifier = model.PrincipalString(baseCIRepairer)
)

const (
	// notPublishingCriticalAfter is how long a branch may publish
	// nothing before pipeline.not-publishing turns critical.
	notPublishingCriticalAfter = 24 * time.Hour
	// repairFailingRuns and repairFailingFor are when a base-branch
	// repair is taken to be failing: this many runs, or this long open,
	// with the branch still confirmed red. They are DT-4's base-green
	// progress-limit defaults (6 resumes or 12h), applied to the repair
	// task's own runs until goals carry their own limit.
	repairFailingRuns = 6
	repairFailingFor  = 12 * time.Hour
)

// raisePipelineNotification raises n as of now, and delivers a new or
// risen one the way the API's raise does: into the director's chat, and
// to the durable tasks subscribed to its family. A failed delivery is
// logged -- the notification is raised either way, and
// reconcileNotifications tries the director again.
func raisePipelineNotification(ctx context.Context, store *model.Store, n model.Notification,
	now time.Time) (model.NotificationRaise, error) {

	n.RaisedAt = now
	raised, err := store.RaiseNotification(ctx, n)
	if err != nil {
		return raised, fmt.Errorf("orchestrator: raising %s: %w", n.Subject, err)
	}
	if raised.New || raised.Rose {
		log.Printf("orchestrator: raised notification %s (%s): %s", n.Subject, raised.Notification.Severity, n.Title)
		if _, err := store.DeliverNotificationToDirector(ctx, raised.Notification, now); err != nil {
			log.Printf("orchestrator: delivering notification %d to the director: %v", raised.Notification.ID, err)
		}
	}
	if raised.New {
		if _, err := store.WakeNotificationSubscribers(ctx, raised.Notification, now); err != nil {
			log.Printf("orchestrator: waking subscribers of notification %d: %v", raised.Notification.ID, err)
		}
	}
	return raised, nil
}

// ensurePipelineNotification is raisePipelineNotification for a standing
// condition a reconciler finds again every cycle: it raises only when
// the subject is not already open at n's severity or above, so the
// notification's raise count stays a count of something rather than of
// cycles.
func ensurePipelineNotification(ctx context.Context, store *model.Store, n model.Notification, now time.Time) error {
	open, err := store.OpenNotification(ctx, n.Subject)
	if err != nil {
		return err
	}
	if open != nil && open.Severity.Rank() >= n.Severity.Rank() {
		return nil
	}
	_, err = raisePipelineNotification(ctx, store, n, now)
	return err
}

// clearPipelineNotification clears subject's open instance, if any, as
// by. It reads before it writes: most callers run every cycle for a
// condition that is almost always already clear.
func clearPipelineNotification(ctx context.Context, store *model.Store, subject, by string) error {
	open, err := store.OpenNotification(ctx, subject)
	if err != nil || open == nil {
		return err
	}
	if _, err := store.ClearNotification(ctx, subject, by); err != nil {
		return err
	}
	log.Printf("orchestrator: cleared notification %s", subject)
	return nil
}

// raiseBaseRed is confirmBaseBranchCI's notification: the branch is
// confirmed red, and this is the repair grain has on it. The green
// verdict that closes the repair clears it (closeBaseRepairsOnGreen);
// clears_when base-green is the same condition read by
// reconcileNotifications, for a branch that goes green some way that
// does not pass through there.
func raiseBaseRed(ctx context.Context, deps Deps, row model.BaseBranchCI, reason string, now time.Time) error {
	key := model.BranchKey(row.Repo, row.Branch)
	body := fmt.Sprintf("grain CI on `%s` %s is confirmed red: %s.", row.Branch, row.Repo, reason)
	n := model.Notification{
		Subject:  model.NotificationSubject(model.FamilyBaseRed, key),
		Severity: model.NotificationWarning,
		Title:    fmt.Sprintf("%s %s is red", row.Repo, row.Branch),
		Repo:     row.Repo.String(),
		RaisedBy: baseCINotifier,
		ClearsWhen: &model.NotificationClear{
			Kind: model.ClearsOnBaseGreen, Repo: row.Repo.String(), Branch: row.Branch,
		},
		Actions: []string{"open-repair", "investigate"},
	}
	repair, err := openBaseRepair(ctx, deps.Store, row.Repo, row.Branch, &row)
	if err != nil {
		return err
	}
	if repair != nil {
		n.TaskID = repair.ID
		body += fmt.Sprintf(" [Task #%s](/tasks/%s) is repairing it; everything waiting on the branch waits on that.",
			repair.ID, repair.ID)
	} else {
		body += " grain has filed no repair for it (its triage is off here, or a person is to file one)."
	}
	n.Body = body
	_, err = raisePipelineNotification(ctx, deps.Store, n, now)
	return err
}

// clearBaseRed is the green half: row's branch is no longer broken.
func clearBaseRed(ctx context.Context, deps Deps, row model.BaseBranchCI) error {
	return clearPipelineNotification(ctx, deps.Store,
		model.NotificationSubject(model.FamilyBaseRed, model.BranchKey(row.Repo, row.Branch)), baseCINotifier)
}

// reconcileRepairFailing raises pipeline.repair-failing for the repair of
// a confirmed-red branch that has had repairFailingRuns runs, or been
// open repairFailingFor, without the branch going green. It clears when
// the repair closes (clears_when), and when the branch goes green with a
// repair a person marked still open (closeBaseRepairsOnGreen).
func reconcileRepairFailing(ctx context.Context, deps Deps, row model.BaseBranchCI, now time.Time) error {
	if !row.Confirmed() {
		return nil
	}
	repair, err := openBaseRepair(ctx, deps.Store, row.Repo, row.Branch, &row)
	if err != nil || repair == nil {
		return err
	}
	runs, err := deps.Store.Attempts(ctx, repair.ID)
	if err != nil {
		return err
	}
	var open time.Duration
	if repair.CreatedAt != nil {
		open = now.Sub(*repair.CreatedAt)
	}
	if runs < repairFailingRuns && open < repairFailingFor {
		return nil
	}
	return ensurePipelineNotification(ctx, deps.Store, model.Notification{
		Subject:  model.NotificationSubject(model.FamilyRepairFailing, repair.ID),
		Severity: model.NotificationWarning,
		Title:    fmt.Sprintf("The repair of %s %s is not getting it green", row.Repo, row.Branch),
		Body: fmt.Sprintf("[Task #%s](/tasks/%s) has been repairing `%s` on %s for %s over %d run(s), "+
			"and the branch is still confirmed red at %s (%s). A repair that keeps failing needs a "+
			"person: a revert, a different approach, or closing it.",
			repair.ID, repair.ID, row.Branch, row.Repo, humanDuration(open.Round(time.Minute)), runs,
			shortSHA(row.Commit), truncate(row.Detail, 200)),
		TaskID:     repair.ID,
		Repo:       row.Repo.String(),
		RaisedBy:   baseCINotifier,
		ClearsWhen: &model.NotificationClear{Kind: model.ClearsOnTask, Task: repair.ID, Until: "closed"},
		Actions:    []string{"open-task", "investigate"},
	}, now)
}

// reconcilePublishingNotifications raises pipeline.not-publishing for
// every base branch model.BaseBranchPublishingFindings reports -- the
// same reading the deployment health page shows -- as a warning, and as
// critical once the branch has published nothing for a day. One whose
// branch is no longer reported (a release published, or releases were
// switched off) is cleared.
func reconcilePublishingNotifications(ctx context.Context, deps Deps, now time.Time) error {
	findings, err := deps.Store.BaseBranchPublishingFindings(ctx, now)
	if err != nil {
		return err
	}
	var errs []error
	current := map[string]bool{}
	for _, f := range findings {
		subject := model.NotificationSubject(model.FamilyNotPublishing, f.Repo+"@"+f.Branch)
		current[subject] = true
		severity := model.NotificationWarning
		if time.Duration(f.FailingForMS)*time.Millisecond >= notPublishingCriticalAfter {
			severity = model.NotificationCritical
		}
		branch := f.Branch
		if branch == "" {
			branch = "its default branch"
		}
		errs = append(errs, ensurePipelineNotification(ctx, deps.Store, model.Notification{
			Subject:  subject,
			Severity: severity,
			Title:    fmt.Sprintf("Nothing is publishing from %s %s", f.Repo, branch),
			Body:     f.Detail,
			TaskID:   f.TriageTaskID,
			Repo:     f.Repo,
			RaisedBy: releaseNotifier,
			Actions:  []string{"investigate"},
		}, now))
	}
	open, err := deps.Store.ListNotifications(ctx, model.NotificationFilter{
		Open: true, Family: model.FamilyNotPublishing, Limit: 1000,
	})
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, n := range open {
		if current[n.Subject] {
			continue
		}
		if _, err := deps.Store.ClearNotification(ctx, n.Subject, releaseNotifier); err != nil {
			errs = append(errs, err)
			continue
		}
		log.Printf("orchestrator: cleared notification %s: the branch is publishing again", n.Subject)
	}
	return errors.Join(errs...)
}

// reconcileMergeStalledClears clears each open pipeline.merge-stalled
// whose task the merge queue is driving again: its block lifted by Retry
// merge, a new push, or the pull request landing. grain no longer raises
// it (the automatic stall escalation is gone); this clears ones an older
// grain raised.
func reconcileMergeStalledClears(ctx context.Context, store *model.Store) error {
	open, err := store.ListNotifications(ctx, model.NotificationFilter{
		Open: true, Family: model.FamilyMergeStalled, Limit: 1000,
	})
	if err != nil {
		return err
	}
	var errs []error
	for _, n := range open {
		taskID := n.TaskID
		if taskID == "" {
			_, taskID, _ = strings.Cut(n.Subject, ":")
		}
		obs, err := store.GetObservation(ctx, taskID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if obs != nil && obs.MergeQueueBlockedAt != nil {
			continue
		}
		if _, err := store.ClearNotification(ctx, n.Subject, mergeQueueNotifier); err != nil {
			errs = append(errs, err)
			continue
		}
		log.Printf("orchestrator: cleared notification %s: the merge queue is driving it again", n.Subject)
	}
	return errors.Join(errs...)
}
