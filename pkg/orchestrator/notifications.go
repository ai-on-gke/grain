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
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// reconcileNotifications is the per-cycle half of operator notifications
// (docs/design/durable-tasks.md, DT-5): every open notification with a
// clears_when is checked, and cleared ("condition") once it holds --
// a task closed or merged, a base branch's latest grain CI verdict
// green, or its ttl passed. An open warning or critical one the director
// has not been given yet (no director when it was raised, or a delivery
// that failed) is delivered again here.
//
// It is also where the standing pipeline conditions are raised and
// cleared (pipelinenotifications.go, stewardarms.go): a base branch
// nothing is publishing from, a stalled merge the queue is driving
// again, a stalled task, a stale port, and a judgement model nothing
// resolves.
func reconcileNotifications(ctx context.Context, deps Deps, now time.Time) error {
	if err := reconcilePublishingNotifications(ctx, deps, now); err != nil {
		log.Printf("grain: pipeline.not-publishing notifications: %v", err)
	}
	if err := reconcileMergeStalledClears(ctx, deps.Store); err != nil {
		log.Printf("grain: pipeline.merge-stalled notifications: %v", err)
	}
	if err := reconcileStallNotifications(ctx, deps, now); err != nil {
		log.Printf("grain: pipeline.task-stalled and pipeline.port-stale notifications: %v", err)
	}
	if err := reconcileJudgementModel(ctx, deps, now); err != nil {
		log.Printf("grain: %s notification: %v", model.JudgementModelUnresolvedSubject, err)
	}
	if err := ReconcileStewardRegression(ctx, deps.Store, now); err != nil {
		log.Printf("grain: %s notification: %v", model.SubjectStewardRegression, err)
	}
	return ClearMetNotifications(ctx, deps.Store, now)
}

// ClearMetNotifications is reconcileNotifications' sweep of the open
// notifications on store: each whose clears_when holds now is cleared
// ("condition"), and an open warning or critical one the director has
// not been given yet is delivered again. It needs nothing but the store,
// which is what lets `grain demo -notifications` run it on its own.
func ClearMetNotifications(ctx context.Context, store *model.Store, now time.Time) error {
	open, err := store.ListNotifications(ctx, model.NotificationFilter{Open: true, Limit: 1000})
	if err != nil {
		return err
	}
	for _, n := range open {
		if n.ClearsWhen != nil {
			met, err := notificationConditionMet(ctx, store, *n.ClearsWhen, now)
			if err != nil {
				log.Printf("grain: notification %d (%s): checking clears_when: %v", n.ID, n.Subject, err)
				continue
			}
			if met {
				if _, err := store.ClearNotificationByID(ctx, n.ID, "condition"); err != nil {
					return fmt.Errorf("clearing notification %d: %w", n.ID, err)
				}
				continue
			}
		}
		if n.Delivered.Director == 0 && n.Severity.Rank() >= model.NotificationWarning.Rank() {
			if _, err := store.DeliverNotificationToDirector(ctx, n, now); err != nil {
				log.Printf("grain: notification %d: delivering to the director: %v", n.ID, err)
			}
		}
	}
	return nil
}

// notificationConditionMet reports whether c holds now.
func notificationConditionMet(ctx context.Context, store *model.Store, c model.NotificationClear, now time.Time) (bool, error) {
	switch c.Kind {
	case model.ClearsOnTTL:
		return c.At != nil && !now.Before(*c.At), nil
	case model.ClearsOnTask:
		target, err := store.WaitTargetOf(ctx, c.Task)
		if err != nil || target == nil {
			return false, err
		}
		if c.Until == "merged" {
			return target.Merged, nil
		}
		return target.Closed, nil
	case model.ClearsOnBaseGreen:
		repo, err := model.ParseRepo(c.Repo)
		if err != nil {
			return false, err
		}
		row, err := store.GetBaseBranchCI(ctx, repo, c.Branch)
		if err != nil || row == nil {
			return false, err
		}
		return row.Conclusion == "success", nil
	}
	return false, nil
}
