package orchestrator

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A run suspended off its model records the model, the next dispatch is
// asked to avoid every model that failed in a row, and the third distinct
// model to fail is said on the task, once.
func TestFailoverRecordsTheModelAndTellsAfterThree(t *testing.T) {
	store, ctx := openTestStore(t)
	tk := stdTask("subject")
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	notes := func() int {
		t.Helper()
		comments, err := store.Comments(ctx, tk.ID)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, c := range comments {
			if strings.Contains(c.Body, "different models have failed") {
				n++
			}
		}
		return n
	}
	for i, m := range []string{"m1", "m2", "m2", "m3", "m4"} {
		runID := tk.ID + "-" + string(rune('1'+i))
		at := standingNow.Add(time.Duration(i) * time.Hour)
		if err := store.StartRun(ctx, model.Run{ID: runID, TaskID: tk.ID, Attempt: i + 1, StartedAt: at},
			model.Limits{}); err != nil {
			t.Fatal(err)
		}
		cfg := Config{Now: func() time.Time { return at.Add(time.Minute) }}
		obs, err := store.GetObservation(ctx, tk.ID)
		if err != nil {
			t.Fatal(err)
		}
		end := runEnding{outcome: model.CapacityOutcome, detail: "overloaded", modelHint: m}
		if err := recordRunEnd(ctx, ctx, store, cfg, tk, runID, obs, nil, end, nil, false, "", ""); err != nil {
			t.Fatal(err)
		}
		if i == 3 && notes() != 1 {
			t.Fatalf("after m1, m2 and m3 failed: %d notes, want 1", notes())
		}
	}
	if notes() != 1 {
		t.Fatalf("%d notes after a fourth model, want still the one", notes())
	}
	choice := Deps{Store: store}.dispatchChoiceFor(context.Background(), tk)
	if !slices.Equal(choice.AvoidModels, []string{"m4", "m3", "m2", "m1"}) {
		t.Fatalf("AvoidModels = %v, want every failed model, most recent first", choice.AvoidModels)
	}
	suspensions, err := store.Suspensions(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(suspensions) != 5 || suspensions[0].ModelHint != "m1" || suspensions[0].Kind != model.SuspendCapacity {
		t.Fatalf("suspensions = %+v, want five capacity suspensions naming their models", suspensions)
	}

	// A run that is not a suspension ends the run of failures.
	at := standingNow.Add(10 * time.Hour)
	if err := store.StartRun(ctx, model.Run{ID: "subject-ok", TaskID: tk.ID, Attempt: 6, StartedAt: at},
		model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "subject-ok", at, "failed", ""); err != nil {
		t.Fatal(err)
	}
	if choice := (Deps{Store: store}).dispatchChoiceFor(ctx, tk); len(choice.AvoidModels) != 0 {
		t.Fatalf("AvoidModels after an ordinary run = %v, want none", choice.AvoidModels)
	}
}
