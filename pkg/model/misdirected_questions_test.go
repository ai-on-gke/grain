package model_test

// ClearMisdirectedQuestions: the tasks another task's run parked by
// calling POST /api/tasks/{id}/messages on them are un-parked, and a
// task's own question is left alone.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestClearMisdirectedQuestionsLiftsOnlyAnotherRunsMessages(t *testing.T) {
	t.Parallel()
	st := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)

	grainFor := func(task string) model.Attribution {
		return model.Attribution{
			Actor:      model.Principal{Kind: model.PrincipalAutomation, ID: "grain"},
			OnBehalfOf: &model.Principal{Kind: model.PrincipalAgent, ID: task},
		}
	}
	// park writes what PostAgentMessage writes -- grain speaking as
	// task's run, with the task parked on it -- and, when sender is
	// another task, the note recordAgentCall leaves after it.
	park := func(task, sender string) int64 {
		t.Helper()
		if err := st.PutTask(ctx, model.Task{ID: task, Intent: model.IntentImplement, Title: "task " + task}); err != nil {
			t.Fatal(err)
		}
		id, err := st.AddComment(ctx, model.Comment{TaskID: task, Author: grainFor(task), Body: "advice", CreatedAt: now})
		if err != nil {
			t.Fatal(err)
		}
		if sender != "" {
			if _, err := st.AddComment(ctx, model.Comment{TaskID: task, Author: grainFor(sender), CreatedAt: now,
				Body: "Task " + sender + " called `POST /api/tasks/{id}/messages` through the agent API (scope `tasks:write`)."}); err != nil {
				t.Fatal(err)
			}
		}
		if err := st.ObserveField(ctx, task, now, func(o *model.Observation) { o.PendingQuestionCommentID = &id }); err != nil {
			t.Fatal(err)
		}
		return id
	}
	park("796", "768")
	own := park("800", "")

	lifted, err := st.ClearMisdirectedQuestions(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(lifted) != 1 || lifted[0] != "796" {
		t.Fatalf("lifted = %v, want task 796 alone", lifted)
	}
	if obs, err := st.GetObservation(ctx, "796"); err != nil || obs.PendingQuestionCommentID != nil {
		t.Errorf("task 796's observation = %+v (%v), want its question lifted", obs, err)
	}
	if obs, err := st.GetObservation(ctx, "800"); err != nil || obs.PendingQuestionCommentID == nil || *obs.PendingQuestionCommentID != own {
		t.Errorf("task 800's observation = %+v (%v), want its own question still pending", obs, err)
	}
	comments, err := st.Comments(ctx, "796")
	if err != nil {
		t.Fatal(err)
	}
	if last := comments[len(comments)-1]; !strings.Contains(last.Body, "task 768") || last.Author.Actor.Kind != model.PrincipalAutomation {
		t.Errorf("last comment = %+v, want grain's note naming task 768", last)
	}

	// Run again, nothing is left to lift.
	if again, err := st.ClearMisdirectedQuestions(ctx, now); err != nil || len(again) != 0 {
		t.Errorf("second pass lifted %v (%v), want nothing", again, err)
	}
}
