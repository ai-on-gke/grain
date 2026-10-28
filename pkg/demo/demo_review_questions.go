package demo

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/ui"
)

// seedReviewQuestionsDemo is `grain demo -review-questions`: a finished
// change whose review fixed what it could and left one question only its
// owner can settle, so the change waits on their decision
// (model.LinkReviewQuestions, orchestrator's reviewquestions.go). Opt-in,
// like the other hold demos, so the default demo's inbox stays as it is.
//
// githubSimRepo is -github-sim's bare repo, or "" without it. The review
// is filed on the held change's branch, which a real hold has already
// pushed; with the simulated GitHub answering the base check for real,
// the branch is pushed there first.
func seedReviewQuestionsDemo(ctx context.Context, store *model.Store, cfg ui.Config, repo model.RepoRef, githubSimRepo string) error {
	now := time.Now().UTC()
	client := ui.NewClient(cfg, store)
	held, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:       "Publish both daemon images",
		Description: "Release the core and google images from one workflow.",
		Repo:        repo.String(),
		Approved:    true,
		AutoMerge:   true,
	})
	if err != nil {
		return fmt.Errorf("seeding the held change: %w", err)
	}
	if githubSimRepo != "" {
		if err := pushDemoGitHubSimBranch(githubSimRepo, model.BranchName(held.ID)); err != nil {
			return fmt.Errorf("pushing the held change's branch: %w", err)
		}
	}
	review, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:       "Review: Publish both daemon images",
		Description: "Review " + model.BranchName(held.ID) + " and fix what you find.",
		Repo:        repo.String(),
		Base:        model.BranchName(held.ID),
		Approved:    true,
	})
	if err != nil {
		return fmt.Errorf("seeding the held change's review: %w", err)
	}
	commentID, err := store.AddComment(ctx, model.Comment{
		TaskID: held.ID,
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "review"}},
		Body: fmt.Sprintf("**Review needs your decision.** The review of this change (task %s) fixed "+
			"what it could and left 1 open question on %s#106 that it could not settle without you:\n\n"+
			"- `Makefile:41` -- Should the core build also publish the google image? I recommend not: "+
			"keep the core image core-only and publish google from its own target.\n\n"+
			"Reply here (or on task %s) with your decision, and grain sends another run on this task "+
			"to carry it out. Or ship it as it stands with **Ship as is** (Submit), leaving these open.",
			review.ID, repo, review.ID),
		CreatedAt: now.Add(-5 * time.Minute),
	})
	if err != nil {
		return fmt.Errorf("seeding the review's questions: %w", err)
	}
	questions := model.ReviewQuestions{ReviewTaskID: review.ID, AfterCommentID: commentID, RestoreAutoMerge: true}
	if err := store.UpdateTask(ctx, held.ID, func(t *model.Task) error {
		t.AutoMerge = false
		t.Links = append(t.Links,
			model.Link{Kind: model.LinkFixes, Target: repo.String() + "#106"},
			model.Link{Kind: model.LinkReviewTask, Target: review.ID},
			model.Link{Kind: model.LinkReviewQuestions, Target: questions.Target()})
		return nil
	}); err != nil {
		return fmt.Errorf("holding the seeded change: %w", err)
	}
	finished := now.Add(-10 * time.Minute)
	if err := store.ObserveField(ctx, held.ID, finished, func(o *model.Observation) {
		o.CompletedAt = &finished
	}); err != nil {
		return fmt.Errorf("finishing the held change's run: %w", err)
	}
	return nil
}

// demoReviewQuestionReconcilers is what -review-questions runs beside its
// seed, by the names orchestrator.Reconcilers gives them.
var demoReviewQuestionReconcilers = []string{"review-questions"}

// runDemoReviewQuestions runs the review-questions reconciler once
// straight away and then every interval until ctx is done, logging a
// failed cycle as runDemoSchedules does. Without it the seeded hold is
// one nothing can ever answer: a reply is written to the conversation,
// and the step a daemon's cycle would take next -- taking the change off
// its hold and back into the queue (orchestrator's reviewquestions.go) --
// never comes, so the change sits waiting on a decision it already has.
// Agents still never run: the change is queued and stays there.
func runDemoReviewQuestions(ctx context.Context, store *model.Store, interval time.Duration) {
	deps := orchestrator.Deps{Store: store}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := runDemoReconcilers(ctx, deps, time.Now().UTC(), demoReviewQuestionReconcilers...); err != nil {
			log.Printf("grain demo: review-questions cycle: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
