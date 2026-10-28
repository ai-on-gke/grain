package orchestrator

// What a dispatch's prompt costs when every part of it grain bounds is
// at its bound -- the half of "how big are this deployment's prompts
// really?" that can be measured from the code rather than from the
// record (grain/task-286).
//
// A distribution over recorded prompts says what runs were given; it
// cannot say what grain's own sections cost, because every prompt in it
// is standing text plus a task's material and nothing separates the two.
// This measures the standing text directly, and with it the ceiling:
// grain's sections, the attempt history at maxPreviousAttempts, and the
// thread at maxConversationBytes plus the handover carried beside it.
//
// The invariant that falls out is the one worth keeping. Everything
// grain bounds, all of it at its bound at once, still does not reach
// model.PromptWarnTokens -- so a `condition=prompt_size` warning in the
// daemon's log never means "this task talked too much". It means some
// part of a prompt nobody bounded grew: a description somebody pasted a
// log into, this deployment's memory, its prompt extension, a
// capability's own section. That is what makes the warning worth reading
// when it fires, and it is a property of maxConversationBytes' value
// rather than of its existence -- which is why it is asserted here, and
// why raising the bound far enough to break it should be a decision
// somebody makes on purpose.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// promptBudgetBody is a task description of a size a person writes:
// 2 KB, a few hundred words. Descriptions are not bounded anywhere and a
// pasted build log is the usual way one is enormous, so the ceiling
// below is quoted *with* the body it was measured against rather than
// pretending a description has a size.
const promptBudgetBody = 2 << 10

// maxStandingSections is what grain's own prompt may cost, description
// and task material excluded: 16 KB.
//
// Measured at about 7 KB (grain/task-286) for the most decorated
// dispatch BuildPrompt produces -- a repo task with a checkout, grain CI
// granted and open_pull_request in the roster. The assertion is at twice
// that, because the number to defend is not this month's byte count but
// the ratio the ceiling below rests on: grain's boilerplate is a small
// part of a prompt, and the task's own material is the rest. A sentence
// added to a section costs a few hundred bytes and should not fail a
// test; the boilerplate doubling is a different thing and should.
const maxStandingSections = 16 << 10

// promptBudgetTask is a repo task decorated the way a real dispatch is:
// grain CI granted, so the prompt carries the longer CI loop paragraph
// rather than the short one.
func promptBudgetTask() model.Task {
	return model.Task{
		ID:     "286",
		Title:  "Re-price the conversation bound from a window of recorded prompt sizes",
		Body:   strings.Repeat("b", promptBudgetBody),
		Target: &model.RepoRef{Owner: "bwsalmon", Name: "grain"},
		Grants: []model.Grant{{Capability: "grain-ci"}},
	}
}

// promptBudgetHistory is an attempt history and a thread both past their
// bounds: more attempts and commits than either section lists, and 400
// KB of conversation with a handover in the middle of it -- the part the
// cut would drop and conversationHandoverBudget carries anyway, so the
// thread comes out at the budget plus that rather than at the budget.
func promptBudgetHistory() History {
	var attempts []model.Run
	for i := 1; i <= maxPreviousAttempts+2; i++ {
		attempts = append(attempts, model.Run{
			Attempt: i,
			Outcome: "failed",
			// Past maxAttemptDetail, so the trim is exercised rather than
			// assumed: a framework's own error text is whatever the CLI
			// printed.
			Detail: strings.Repeat("d", 4*maxAttemptDetail),
		})
	}
	var commits []string
	for i := 0; i < maxBranchCommits+3; i++ {
		commits = append(commits, fmt.Sprintf("%09x %s", i, strings.Repeat("s", 70)))
	}

	var comments []model.Comment
	for i := 0; i < 400; i++ {
		comments = append(comments, model.Comment{
			ID:        int64(i),
			Body:      fmt.Sprintf("turn-%03d %s", i, strings.Repeat("y", 1<<10)),
			Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
			CreatedAt: time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute),
		})
	}
	comments[len(comments)/2].Handover = true
	comments[len(comments)/2].Body = strings.Repeat("h", maxConversationBytes)

	return History{Attempt: len(attempts) + 1, Attempts: attempts, Commits: commits, Comments: comments}
}

func TestGrainsOwnSectionsAreASmallPartOfAPrompt(t *testing.T) {
	t.Parallel()
	task := promptBudgetTask()
	prompt := BuildPrompt(task, CheckoutDir, "/home/debian", true,
		DefaultMaxRunRuntime, History{}, nil)

	standing := len(prompt) - len(task.Body)
	t.Logf("grain's standing sections: %s of a %s first dispatch (description %s)",
		model.PromptBytes(standing), model.PromptBytes(len(prompt)),
		model.PromptBytes(len(task.Body)))
	if standing > maxStandingSections {
		t.Errorf("grain's own sections came to %s, over the %s this test defends -- "+
			"the ceiling below assumes the boilerplate is the small part of a prompt",
			model.PromptBytes(standing), model.PromptBytes(maxStandingSections))
	}
}

func TestEverythingGrainBoundsAtOnceDoesNotWarn(t *testing.T) {
	t.Parallel()
	task := promptBudgetTask()
	history := promptBudgetHistory()
	prompt := BuildPrompt(task, CheckoutDir, "/home/debian", true,
		DefaultMaxRunRuntime, history, nil)

	// The split, logged rather than asserted: it is the answer to "how
	// much of a prompt is the thread rather than grain's own sections?"
	// for the one prompt where that question has a fixed answer, and it
	// is what a later reader re-pricing the bound wants in front of them.
	thread := len(commentThreadSection(history.Comments))
	attempts := len(previousAttemptsSection(history, task.BranchName()))
	size := model.MeasurePrompt(prompt)
	t.Logf("ceiling: %s", size)
	t.Logf("  thread at its bound:  %s", model.PromptBytes(thread))
	t.Logf("  attempt history:      %s", model.PromptBytes(attempts))
	t.Logf("  description:          %s", model.PromptBytes(len(task.Body)))
	t.Logf("  grain's own sections: %s", model.PromptBytes(len(prompt)-thread-attempts-len(task.Body)))

	if size.Large() {
		t.Errorf("a prompt with every bounded part at its bound came to %s, which trips "+
			"the prompt_size warning on its own -- so the warning no longer means "+
			"unbounded material grew, which is the whole of what it is read for", size)
	}
}
