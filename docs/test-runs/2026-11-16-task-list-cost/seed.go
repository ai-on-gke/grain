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

//go:build ignore

// seed.go seeds a real PostgreSQL or SQLite grain store with ~5,000 tasks
// across a realistic mix of states (proposed, queued, blocked, paused, goal,
// run-now, grant-wait, running, rolled-over, deferred, resume_later,
// awaiting_reply, awaiting_submit, merge-queue, repair, failed, and closed),
// plus workstream lineages, templates, custom fields, RBAC scope sets and
// allowed principals for the 2026-11-16 task-list-cost walkthrough (§2).
//
// Usage:
//
//	go run ./docs/test-runs/2026-11-16-task-list-cost/seed.go -postgres-url <dsn> [-total 5000]
//	go run ./docs/test-runs/2026-11-16-task-list-cost/seed.go -sqlite-dir <dir> [-total 5000]
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

func main() {
	pgURL := flag.String("postgres-url", "", "PostgreSQL DSN")
	sqliteDir := flag.String("sqlite-dir", "", "SQLite data directory")
	total := flag.Int("total", 5000, "total number of tasks to seed (minimum 5000)")
	startLiveRuns := flag.Bool("start-live-runs", false, "only start live runs for tasks 71..135 (run after daemon startup to restore running tasks after startup orphan recovery)")
	flag.Parse()

	if (*pgURL == "" && *sqliteDir == "") || (*pgURL != "" && *sqliteDir != "") {
		log.Fatal("specify exactly one of -postgres-url or -sqlite-dir")
	}
	if *total < 500 {
		log.Fatal("-total must be at least 500")
	}

	var (
		db  *sql.DB
		err error
	)
	if *pgURL != "" {
		db, err = postgres.Open(postgres.Config{URL: *pgURL})
	} else {
		if err = os.MkdirAll(*sqliteDir, 0o755); err != nil {
			log.Fatalf("mkdir %s: %v", *sqliteDir, err)
		}
		db, err = sqlite.Open(sqlite.DefaultConfig(*sqliteDir))
	}
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		log.Fatalf("init store: %v", err)
	}

	started := time.Now()
	now := time.Now().UTC().Truncate(time.Second)

	if *startLiveRuns {
		restored := 0
		for i := 71; i <= 135; i++ {
			id := fmt.Sprint(i)
			liveStart := now.Add(-time.Duration(i-70) * time.Minute)
			liveRunID := fmt.Sprintf("run-%d-live-%d", i, now.Unix())
			if err := store.StartRun(ctx, model.Run{
				ID:             liveRunID,
				TaskID:         id,
				Sandbox:        fmt.Sprintf("sbx-%d-live", i),
				Model:          "test-model",
				AgentFramework: "test-fw",
				StartedAt:      liveStart,
			}, model.Limits{}); err != nil {
				log.Fatalf("start live run %s: %v", id, err)
			}
			if _, err := store.SetTaskActivity(ctx, id, fmt.Sprintf("running tests for task %d", i), now.Add(-15*time.Second)); err != nil {
				log.Fatalf("set activity %s: %v", id, err)
			}
			restored++
		}
		fmt.Printf("started %d live runs in %v\n", restored, time.Since(started).Round(time.Millisecond))
		return
	}

	cfg := model.Config{
		PollInterval:     model.DefaultPollInterval,
		ExternalAgentAPI: true,
		CIAPIRepos:       []string{"acme/widgets"},
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		log.Fatalf("put config: %v", err)
	}

	// Templates and custom fields referenced by seeded tasks.
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	secret := model.RepoRef{Owner: "acme", Name: "secret"}
	docs := model.RepoRef{Owner: "acme", Name: "docs"}

	for _, tmpl := range []model.Template{
		{ID: "tmpl-impl", Name: "Standard implementation", Title: "Implement feature", Target: &widgets},
		{ID: "tmpl-review", Name: "Code review", Title: "Code review", Target: &widgets},
	} {
		if err := store.PutTemplate(ctx, tmpl); err != nil {
			log.Fatalf("put template %s: %v", tmpl.ID, err)
		}
	}
	for _, cf := range []model.CustomField{
		{Name: "severity", Label: "Severity", Type: model.CustomFieldEnum, Options: []string{"p0", "p1", "p2"}},
		{Name: "area", Label: "Area", Type: model.CustomFieldEnum, Options: []string{"backend", "frontend", "infra"}, Multiple: true},
	} {
		if err := store.PutCustomField(ctx, cf); err != nil {
			log.Fatalf("put custom field %s: %v", cf.Name, err)
		}
	}

	// RBAC scope sets and allowed principals for verified-identity probes.
	for _, ss := range []model.APIScopeSet{
		{
			Name:        "custom/widgets-read",
			Description: "Read tasks in acme/widgets only",
			Scopes:      []string{"tasks:read"},
			Repos:       []string{"acme/widgets"},
		},
		{
			Name:        "custom/ci-only",
			Description: "CI read only (no tasks:read)",
			Scopes:      []string{"ci:read"},
		},
	} {
		if err := store.PutAPIScopeSet(ctx, ss); err != nil {
			log.Fatalf("put scope set %s: %v", ss.Name, err)
		}
	}
	for _, ap := range []struct {
		entry string
		role  string
	}{
		{"user:admin@example.com", "grain/operator"},
		{"user:limited@example.com", "custom/widgets-read"},
		{"user:ci-only@example.com", "custom/ci-only"},
	} {
		if _, _, err := store.AddAllowedPrincipalWithRole(ctx, ap.entry, "seed", "walkthrough setup", ap.role, now); err != nil {
			log.Fatalf("add allowed principal %s: %v", ap.entry, err)
		}
	}

	humanAttr := model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com"}}
	agentAttr := model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "5000"}}
	heldAttr := model.Attribution{
		Actor:      model.Principal{Kind: model.PrincipalAgent, ID: "5000"},
		OnBehalfOf: &model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com"},
	}

	// Task layout (default total=5000):
	//   1..20   : proposed (20, mix of repos + held + proposed-by closed root)
	//   21..70  : queued (50, including blocked on open/closed/paused/deferred/goal, run-now, paused, goal, grant-wait, base-repair, merge-queue repair)
	//   71..135 : running (65 -> >50 so /next/agents issues 2 summary chunks; includes rolled-over + merge-queue repair running)
	//   136..155: deferred (20: 10 with deferred_until, 10 resume_later without wake time)
	//   156..190: awaiting_reply (35 -> feeds /next/inbox: questions, access requests, secrets, stopped turns, unanswered comments, completed-no-PR)
	//   191..255: awaiting_submit / merge-queue (65 -> >50 so /next/tasks issues 2 summary chunks; includes holds & merge-queue states)
	//   256..265: failed (10, streak_count >= MaxConsecutiveFailures)
	//   266..N  : closed (majority; 300..359 are recent closed workstream roots within 2h;
	//             4990..4995 are rolled-over sources; 4997=widgets probe, 4998=secret probe,
	//             4999=no-repo probe, 5000=acting-agent task with tasks:read@acme/widgets)
	const openCount = 265
	n := *total
	if n < 5000 {
		log.Fatalf("this layout assumes -total >= 5000 (got %d)", n)
	}

	pickRepo := func(i int) *model.RepoRef {
		switch {
		case i == 4997 || i == 5000:
			r := widgets
			return &r
		case i == 4998:
			r := secret
			return &r
		case i == 4999:
			return nil
		case i%20 == 0:
			return nil
		case i%7 == 0:
			r := secret
			return &r
		case i%5 == 0:
			r := docs
			return &r
		default:
			r := widgets
			return &r
		}
	}

	baseReads := []model.RepoRef{
		{Owner: "acme", Name: "docs"},
		{Owner: "acme", Name: "gadgets"},
		{Owner: "acme", Name: "tools"},
	}
	baseWrites := []model.WriteTarget{
		{Repo: model.RepoRef{Owner: "acme", Name: "gadgets"}, Base: "main"},
	}
	baseGrants := []model.Grant{
		{Capability: "https://grain.dev/capabilities/api#ci:read", Via: model.GrantByDefault},
		{Capability: "https://grain.dev/capabilities/api#tasks:read", Via: model.GrantByLabel},
		{Capability: "network", Via: model.GrantByDefault},
	}

	counts := map[string]int{}

	for i := 1; i <= n; i++ {
		id := fmt.Sprint(i)
		created := now.Add(-time.Duration(n-i+300) * time.Minute)
		approved := created.Add(30 * time.Second)
		target := pickRepo(i)

		task := model.Task{
			ID:               id,
			Intent:           model.IntentImplement,
			Title:            fmt.Sprintf("Task %d", i),
			Body:             fmt.Sprintf("Detailed specification for task %d covering realistic description length and context for walkthrough verification.", i),
			Target:           target,
			Base:             "main",
			Priority:         5 + (i % 3),
			CreatedAt:        &created,
			Owner:            "alice@example.com",
			Creator:          "alice@example.com",
			Filing:           model.Filing{Attribution: humanAttr},
			Reads:            baseReads,
			Writes:           baseWrites,
			Grants:           baseGrants,
			Tags:             []string{"backlog", fmt.Sprintf("batch-%d", i%10)},
			TemplateID:       "tmpl-impl",
			SourceTemplateID: "tmpl-impl",
			CustomFields: map[string][]string{
				"severity": {[]string{"p0", "p1", "p2"}[i%3]},
				"area":     {"backend", []string{"frontend", "infra"}[i%2]},
			},
			Links: []model.Link{
				{Kind: model.LinkDesign, Target: fmt.Sprintf("design/doc-%d.md", i%25)},
			},
		}

		// Attach 60 open tasks (21..80) to recent closed workstream roots (300..359).
		if i >= 21 && i <= 80 {
			wsRoot := fmt.Sprint(300 + (i - 21))
			task.Links = append(task.Links, model.Link{Kind: model.LinkProposedBy, Target: wsRoot})
		}

		switch {
		case i >= 1 && i <= 20:
			// Proposed (unapproved)
			task.Title = fmt.Sprintf("Proposed feature %d", i)
			if i <= 5 {
				task.Filing = model.Filing{Attribution: heldAttr, Reason: model.ReasonProposal}
				task.Body += "\n\n" + model.HeldForApprovalNote("task 5000")
				task.Links = append(task.Links, model.Link{Kind: model.LinkProposedBy, Target: "300"})
			}
			counts["proposed"]++

		case i >= 21 && i <= 70:
			// Queued (approved)
			task.ApprovedAt = &approved
			task.Approval = &humanAttr
			task.Title = fmt.Sprintf("Queued work %d", i)
			switch i {
			case 21:
				// Blocked on proposed task 1 -> blocked_on_person
				task.Links = append(task.Links, model.Link{Kind: model.LinkDependsOn, Target: "1"})
			case 22:
				// Blocked on awaiting_reply task 156 -> blocked_on_person
				task.Links = append(task.Links, model.Link{Kind: model.LinkDependsOn, Target: "156"})
			case 23:
				// Blocked on paused task 25 -> blocked_on_person
				task.Links = append(task.Links, model.Link{Kind: model.LinkDependsOn, Target: "25"})
			case 24:
				// Blocked on deferred (with wake time) 136 and resume_later (no wake time) 146
				task.Links = append(task.Links,
					model.Link{Kind: model.LinkDependsOn, Target: "136"},
					model.Link{Kind: model.LinkDependsOn, Target: "146"},
				)
			case 29:
				// Blocked on goal task 26 and running task 71
				task.Links = append(task.Links,
					model.Link{Kind: model.LinkDependsOn, Target: "26"},
					model.Link{Kind: model.LinkDependsOn, Target: "71"},
				)
			case 30:
				// Depends on closed task 400 -> must NOT be blocked
				task.Links = append(task.Links, model.Link{Kind: model.LinkDependsOn, Target: "400"})
			case 31, 32:
				// Fixes base branch
				model.SetFixesBaseBranch(&task, true)
				task.Title = fmt.Sprintf("Base branch repair %d", i)
			case 66, 67, 68, 69, 70:
				// Merge-queue repair (queued)
				r := widgets
				task.Target = &r
				task.Title = fmt.Sprintf("Queued PR repair %d", i)
				task.Links = append(task.Links, model.Link{Kind: model.LinkFixes, Target: fmt.Sprintf("https://github.com/acme/widgets/pull/%d", i)})
			}
			counts["queued"]++

		case i >= 71 && i <= 135:
			// Running (65 tasks -> 2 summary chunks on /next/agents)
			r := widgets
			task.Target = &r
			task.ApprovedAt = &approved
			task.Approval = &humanAttr
			task.ReviewTemplateID = "tmpl-review"
			task.Title = fmt.Sprintf("Running agent task %d", i)
			if i >= 130 && i <= 135 {
				// Rolled-over tasks
				fromID := fmt.Sprint(4990 + (i - 130))
				task.Title = fmt.Sprintf("Rolled-over task %d (from %s)", i, fromID)
				task.Links = append(task.Links, model.Link{Kind: model.LinkRolledOverFrom, Target: fromID})
			}
			if i >= 125 && i <= 129 {
				// Merge-queue repair (running)
				task.Title = fmt.Sprintf("Running PR repair %d", i)
				task.Links = append(task.Links, model.Link{Kind: model.LinkFixes, Target: fmt.Sprintf("https://github.com/acme/widgets/pull/%d", i)})
			}
			counts["running"]++

		case i >= 136 && i <= 155:
			// Deferred (20 tasks: 10 timed, 10 resume_later without wake time)
			task.ApprovedAt = &approved
			task.Approval = &humanAttr
			defAt := now.Add(-30 * time.Minute)
			task.DeferredAt = &defAt
			if i <= 145 {
				until := now.Add(6 * time.Hour)
				task.DeferredUntil = &until
				task.Title = fmt.Sprintf("Deferred until later %d", i)
			} else {
				task.Title = fmt.Sprintf("Resume-later parked task %d", i)
			}
			counts["deferred"]++

		case i >= 156 && i <= 190:
			// Awaiting reply (35 tasks -> feeds /next/inbox)
			r := widgets
			task.Target = &r
			task.ApprovedAt = &approved
			task.Approval = &humanAttr
			task.Title = fmt.Sprintf("Awaiting reply %d", i)
			counts["awaiting_reply"]++

		case i >= 191 && i <= 255:
			// Awaiting submit / merge queue (65 tasks -> 2 summary chunks on /next/tasks)
			r := widgets
			task.Target = &r
			task.ApprovedAt = &approved
			task.Approval = &humanAttr
			task.AutoMerge = false
			task.ReviewTemplateID = "tmpl-review"
			task.Title = fmt.Sprintf("Awaiting submit PR %d", i)
			task.Links = append(task.Links, model.Link{Kind: model.LinkFixes, Target: fmt.Sprintf("https://github.com/acme/widgets/pull/%d", i)})
			if i >= 245 && i <= 249 {
				task.Links = append(task.Links, model.Link{Kind: model.LinkSecurityHold, Target: "security review findings pending"})
			}
			if i >= 250 && i <= 255 {
				task.Links = append(task.Links, model.Link{Kind: model.LinkPathTriggerHold, Target: "sensitive-path"})
			}
			counts["awaiting_submit"]++

		case i >= 256 && i <= 265:
			// Failed (10 tasks)
			task.ApprovedAt = &approved
			task.Approval = &humanAttr
			task.Title = fmt.Sprintf("Failed streak task %d", i)
			counts["failed"]++

		default:
			// Closed (266..total)
			task.ApprovedAt = &approved
			task.Approval = &humanAttr
			if i >= 300 && i <= 359 {
				task.Title = fmt.Sprintf("Workstream root %d", i)
			} else if i >= 4990 && i <= 4995 {
				toID := fmt.Sprint(130 + (i - 4990))
				task.Title = fmt.Sprintf("Rolled-over parent %d", i)
				task.Links = append(task.Links, model.Link{Kind: model.LinkRolledOverTo, Target: toID})
			} else if i == 4997 {
				task.Title = "Auth probe widgets task"
				task.Body = "Readable by widgets-scoped callers"
			} else if i == 4998 {
				task.Title = "Auth probe secret task"
				task.Body = "Secret body that must never leak to widgets-scoped callers"
			} else if i == 4999 {
				task.Title = "Auth probe no-repo task"
				task.Body = "No-repo body that must never leak to repo-scoped callers"
			} else if i == 5000 {
				task.Title = "Acting agent widgets caller"
				task.Grants = []model.Grant{
					{Capability: "api:tasks:read@acme/widgets", Via: model.GrantByLabel},
				}
			} else {
				task.Title = fmt.Sprintf("Closed historical task %d", i)
			}
			if i%3 == 0 {
				task.Links = append(task.Links, model.Link{Kind: model.LinkFixes, Target: fmt.Sprintf("https://github.com/acme/widgets/pull/%d", i)})
			}
			counts["closed"]++
		}

		if err := store.PutTask(ctx, task); err != nil {
			log.Fatalf("put task %s: %v", id, err)
		}

		// State-specific observations, runs, comments, pauses, goals, grant waits.
		switch {
		case i == 25 || i == 27:
			if _, err := store.Pause(ctx, model.TaskPause{
				TaskID:    id,
				PausedAt:  now.Add(-15 * time.Minute),
				PausedBy:  model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com"},
				FromState: model.StateQueued,
			}); err != nil {
				log.Fatalf("pause task %s: %v", id, err)
			}
		case i == 26 || i == 28:
			if err := store.SetTaskGoal(ctx, id, model.GoalDecl{
				Kind:  model.GoalPersonCloses,
				OnMet: model.GoalCloseOnMet,
			}, "user:alice@example.com", now.Add(-20*time.Minute)); err != nil {
				log.Fatalf("set goal %s: %v", id, err)
			}
		case i == 33 || i == 34:
			if err := store.SetGrantWait(ctx, model.GrantWait{
				TaskID:     id,
				Capability: "gcp#gcp-key",
				Resource:   "pool/default",
				WaitingAt:  now.Add(-5 * time.Minute),
			}); err != nil {
				log.Fatalf("set grant wait %s: %v", id, err)
			}
		case i == 35 || i == 36:
			runNowAt := now.Add(-2 * time.Minute)
			if err := store.Observe(ctx, model.Observation{TaskID: id, RunNowAt: &runNowAt}); err != nil {
				log.Fatalf("observe run-now %s: %v", id, err)
			}
		case i >= 66 && i <= 70:
			repairAt := now.Add(-10 * time.Minute)
			stateAt := now.Add(-10 * time.Minute)
			if err := store.Observe(ctx, model.Observation{
				TaskID:             id,
				MergeQueueRepairAt: &repairAt,
				MergeQueueState:    "repair_queued",
				MergeQueueNote:     "waiting for repair worker",
				MergeQueueReason:   "repair_queued/waiting",
				MergeQueueStateAt:  &stateAt,
			}); err != nil {
				log.Fatalf("observe repair queued %s: %v", id, err)
			}
		case i >= 71 && i <= 135:
			// Prior finished run + live active run with activity.
			prevStart := created.Add(2 * time.Minute)
			prevRunID := fmt.Sprintf("run-%d-1", i)
			if err := store.StartRun(ctx, model.Run{
				ID:             prevRunID,
				TaskID:         id,
				Sandbox:        fmt.Sprintf("sbx-%d-1", i),
				Model:          "test-model",
				AgentFramework: "test-fw",
				StartedAt:      prevStart,
			}, model.Limits{}); err != nil {
				log.Fatalf("start prior run %s: %v", id, err)
			}
			if err := store.FinishRun(ctx, prevRunID, prevStart.Add(3*time.Minute), "failed", "prior attempt"); err != nil {
				log.Fatalf("finish prior run %s: %v", id, err)
			}
			liveStart := now.Add(-time.Duration(i-70) * time.Minute)
			liveRunID := fmt.Sprintf("run-%d-2", i)
			if err := store.StartRun(ctx, model.Run{
				ID:             liveRunID,
				TaskID:         id,
				Sandbox:        fmt.Sprintf("sbx-%d-2", i),
				Model:          "test-model",
				AgentFramework: "test-fw",
				StartedAt:      liveStart,
			}, model.Limits{}); err != nil {
				log.Fatalf("start live run %s: %v", id, err)
			}
			if _, err := store.SetTaskActivity(ctx, id, fmt.Sprintf("running tests for task %d", i), now.Add(-30*time.Second)); err != nil {
				log.Fatalf("set activity %s: %v", id, err)
			}
			if i >= 125 && i <= 129 {
				repairAt := now.Add(-12 * time.Minute)
				stateAt := now.Add(-8 * time.Minute)
				if err := store.Observe(ctx, model.Observation{
					TaskID:             id,
					MergeQueueRepairAt: &repairAt,
					MergeQueueState:    "repairing",
					MergeQueueNote:     "repairing merge conflict",
					MergeQueueReason:   "repairing/agent_running",
					MergeQueueStateAt:  &stateAt,
				}); err != nil {
					log.Fatalf("observe repair running %s: %v", id, err)
				}
			}
			if _, err := store.AddComment(ctx, model.Comment{
				TaskID:    id,
				Author:    agentAttr,
				Body:      fmt.Sprintf("Working on task %d", i),
				CreatedAt: liveStart.Add(10 * time.Second),
			}); err != nil {
				log.Fatalf("add comment %s: %v", id, err)
			}
		case i >= 146 && i <= 155:
			// resume_later without wake time
			if err := store.HoldResumeOn(ctx, id, model.ResumeOn{Message: true}, now.Add(-20*time.Minute)); err != nil {
				log.Fatalf("hold resume_on %s: %v", id, err)
			}
		case i >= 156 && i <= 190:
			// Awaiting reply variants
			obs := model.Observation{TaskID: id}
			switch {
			case i <= 167:
				// Question with summary
				cid, err := store.AddComment(ctx, model.Comment{
					TaskID:    id,
					Author:    agentAttr,
					Summary:   fmt.Sprintf("Confirm API behavior for task %d?", i),
					Body:      fmt.Sprintf("Detailed question for task %d.", i),
					CreatedAt: now.Add(-15 * time.Minute),
				})
				if err != nil {
					log.Fatalf("add question comment %s: %v", id, err)
				}
				obs.PendingQuestionCommentID = &cid
			case i <= 173:
				// Pending access request
				cid, err := store.AddComment(ctx, model.Comment{
					TaskID:    id,
					Author:    agentAttr,
					Summary:   fmt.Sprintf("Grant read on acme/docs for task %d?", i),
					Body:      "Need read access to acme/docs.",
					CreatedAt: now.Add(-14 * time.Minute),
				})
				if err != nil {
					log.Fatalf("add access comment %s: %v", id, err)
				}
				obs.PendingQuestionCommentID = &cid
				obs.PendingAccess = &model.PendingAccessRequest{
					Summary: fmt.Sprintf("Grant read on acme/docs for task %d", i),
					Reason:  "Read docs",
					Items:   []model.AccessRequestItem{{Kind: "read", Repo: "acme/docs"}},
				}
			case i <= 178:
				// Pending secret
				cid, err := store.AddComment(ctx, model.Comment{
					TaskID:    id,
					Author:    agentAttr,
					Summary:   fmt.Sprintf("Set demo-api-key-%d?", i),
					Body:      "Need secret.",
					CreatedAt: now.Add(-13 * time.Minute),
				})
				if err != nil {
					log.Fatalf("add secret comment %s: %v", id, err)
				}
				obs.PendingQuestionCommentID = &cid
				obs.PendingSecret = fmt.Sprintf("demo-api-key-%d", i)
			case i <= 184:
				// Stopped turn
				cid, err := store.AddComment(ctx, model.Comment{
					TaskID:    id,
					Author:    agentAttr,
					Body:      model.TurnStoppedComment,
					CreatedAt: now.Add(-12 * time.Minute),
				})
				if err != nil {
					log.Fatalf("add stopped-turn comment %s: %v", id, err)
				}
				obs.PendingQuestionCommentID = &cid
			default:
				// Completed without change link + unanswered person comment
				comp := now.Add(-10 * time.Minute)
				obs.CompletedAt = &comp
				c1, err := store.AddComment(ctx, model.Comment{
					TaskID:    id,
					Author:    agentAttr,
					Summary:   fmt.Sprintf("Completed investigation %d", i),
					Body:      "Investigation report.",
					CreatedAt: comp,
				})
				if err != nil {
					log.Fatalf("add note comment %s: %v", id, err)
				}
				obs.DeliveredCommentID = &c1
				if _, err := store.AddComment(ctx, model.Comment{
					TaskID:    id,
					Author:    humanAttr,
					Body:      "Follow-up human comment.",
					CreatedAt: comp.Add(time.Minute),
				}); err != nil {
					log.Fatalf("add human comment %s: %v", id, err)
				}
			}
			if err := store.Observe(ctx, obs); err != nil {
				log.Fatalf("observe awaiting_reply %s: %v", id, err)
			}
		case i >= 191 && i <= 255:
			// Awaiting submit / merge queue states
			comp := now.Add(-time.Duration(i-190) * time.Minute)
			prOpen := comp.Add(-2 * time.Minute)
			obs := model.Observation{
				TaskID:      id,
				CompletedAt: &comp,
				PrOpenedAt:  &prOpen,
			}
			switch i % 4 {
			case 0:
				obs.MergeQueueState = "waiting_ci"
				obs.MergeQueueNote = "waiting on CI checks"
				obs.MergeQueueReason = "waiting_ci/github_running"
				obs.MergeQueueStateAt = &comp
			case 1:
				obs.MergeQueueState = "merging"
				obs.MergeQueueNote = "merging pull request"
				obs.MergeQueueReason = "merging/in_progress"
				obs.MergeQueueStateAt = &comp
			case 2:
				blocked := comp.Add(time.Minute)
				obs.MergeQueueState = "merge_failed"
				obs.MergeQueueNote = "merge queue gave up after repair"
				obs.MergeQueueReason = "merge_failed/conflict"
				obs.MergeQueueStateAt = &blocked
				obs.MergeQueueBlockedAt = &blocked
			default:
				obs.MergeQueueState = "waiting_review"
				obs.MergeQueueNote = "waiting for human submit"
				obs.MergeQueueReason = "waiting_review/manual"
				obs.MergeQueueStateAt = &comp
			}
			if err := store.Observe(ctx, obs); err != nil {
				log.Fatalf("observe awaiting_submit %s: %v", id, err)
			}
			if _, err := store.AddComment(ctx, model.Comment{
				TaskID:    id,
				Author:    agentAttr,
				Body:      fmt.Sprintf("Opened PR #%d for review.", i),
				CreatedAt: comp,
			}); err != nil {
				log.Fatalf("add awaiting_submit comment %s: %v", id, err)
			}
		case i >= 256 && i <= 265:
			// Failed streak (MaxConsecutiveFailures consecutive failed runs)
			for f := 1; f <= model.MaxConsecutiveFailures; f++ {
				failStart := now.Add(-time.Duration(30-f*3) * time.Minute)
				runID := fmt.Sprintf("run-%d-fail-%d", i, f)
				if err := store.StartRun(ctx, model.Run{
					ID:             runID,
					TaskID:         id,
					Sandbox:        fmt.Sprintf("sbx-%d-%d", i, f),
					Model:          "test-model",
					AgentFramework: "test-fw",
					StartedAt:      failStart,
				}, model.Limits{}); err != nil {
					log.Fatalf("start failed run %s/%d: %v", id, f, err)
				}
				if err := store.FinishRun(ctx, runID, failStart.Add(time.Minute), "failed", fmt.Sprintf("simulated failure %d", f)); err != nil {
					log.Fatalf("finish failed run %s/%d: %v", id, f, err)
				}
			}
		case i > openCount:
			// Closed tasks
			closed := created.Add(30 * time.Minute)
			if i >= 300 && i <= 359 {
				// Recent closed workstream roots within last 2h so Workstreams picks them up.
				closed = now.Add(-time.Duration(i-299) * time.Minute)
			}
			obs := model.Observation{TaskID: id, ClosedAt: &closed}
			if i%3 == 0 {
				prOpen := closed.Add(-10 * time.Minute)
				obs.PrOpenedAt = &prOpen
				if i%6 == 0 {
					obs.PrMergedAt = &closed
				} else {
					obs.PrClosedAt = &closed
				}
			}
			if err := store.Observe(ctx, obs); err != nil {
				log.Fatalf("observe closed %s: %v", id, err)
			}
			// Give the three auth-probe tasks rich comment/attempt history (25 comments, 12 runs)
			// so default comments_limit=20 / attempts_limit=10 and clamping can be verified live.
			if i == 4997 || i == 4998 || i == 4999 {
				for c := 1; c <= 25; c++ {
					if _, err := store.AddComment(ctx, model.Comment{
						TaskID:    id,
						Author:    humanAttr,
						Body:      fmt.Sprintf("probe task %d comment %d", i, c),
						CreatedAt: created.Add(time.Duration(c) * time.Second),
					}); err != nil {
						log.Fatalf("probe comment %s/%d: %v", id, c, err)
					}
				}
				for r := 1; r <= 12; r++ {
					rid := fmt.Sprintf("probe-run-%d-%d", i, r)
					rs := created.Add(time.Duration(r) * time.Minute)
					if err := store.StartRun(ctx, model.Run{
						ID:             rid,
						TaskID:         id,
						Sandbox:        fmt.Sprintf("sbx-%d-%d", i, r),
						Model:          "test-model",
						AgentFramework: "test-fw",
						StartedAt:      rs,
					}, model.Limits{}); err != nil {
						log.Fatalf("probe start run %s/%d: %v", id, r, err)
					}
					if err := store.FinishRun(ctx, rid, rs.Add(30*time.Second), "failed", fmt.Sprintf("attempt %d", r)); err != nil {
						log.Fatalf("probe finish run %s/%d: %v", id, r, err)
					}
				}
			}
		}
	}

	// Advance task_sequence past n so live API task creations get id n+1 without colliding.
	if *pgURL != "" {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO task_sequence (number, issued_at) VALUES ($1, $2) ON CONFLICT (number) DO NOTHING", n, now); err != nil {
			log.Fatalf("advance postgres task_sequence: %v", err)
		}
		if _, err := db.ExecContext(ctx,
			"SELECT setval(pg_get_serial_sequence('task_sequence', 'number'), (SELECT COALESCE(MAX(number), 1) FROM task_sequence), true)"); err != nil {
			log.Fatalf("resync postgres task_sequence: %v", err)
		}
	} else {
		if _, err := db.ExecContext(ctx,
			"INSERT OR IGNORE INTO `task_sequence` (`number`, `issued_at`) VALUES (?, ?)", n, now); err != nil {
			log.Fatalf("advance sqlite task_sequence: %v", err)
		}
	}

	// Verify state counts via Store.States.
	states, err := store.States(ctx)
	if err != nil {
		log.Fatalf("read states: %v", err)
	}
	actual := map[model.State]int{}
	for _, st := range states {
		actual[st]++
	}
	fmt.Printf("seeded %d tasks in %v (open=%d closed=%d)\n", n, time.Since(started).Round(time.Millisecond), openCount, n-openCount)
	fmt.Printf("observed task_state counts: %v\n", actual)
}
