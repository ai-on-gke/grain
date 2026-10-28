package model

import (
	"fmt"
	"strconv"
	"strings"
)

// ReviewQuestions is what LinkReviewQuestions records about a change held
// on its review's open questions (orchestrator's reviewquestions.go).
//
// ReviewTaskID is the review that asked them. AfterCommentID is the
// comment that put them to the owner on this task: a human comment newer
// than it, here or on the review, is the owner's answer. RestoreAutoMerge
// says the change was merging itself before the questions held it -- by
// its own auto-merge, or under a hold grain would have released -- so the
// run that carries out the answer goes through the same way.
type ReviewQuestions struct {
	ReviewTaskID     string
	AfterCommentID   int64
	RestoreAutoMerge bool
}

// Target is the link's target: "<review task>@<comment id>", with
// "+auto-merge" when RestoreAutoMerge is set.
func (q ReviewQuestions) Target() string {
	s := fmt.Sprintf("%s@%d", q.ReviewTaskID, q.AfterCommentID)
	if q.RestoreAutoMerge {
		s += "+auto-merge"
	}
	return s
}

// ReviewQuestionsOf reads task's LinkReviewQuestions, if it has one that
// parses.
func ReviewQuestionsOf(task Task) (ReviewQuestions, bool) {
	for _, l := range task.Links {
		if l.Kind != LinkReviewQuestions {
			continue
		}
		target, restore := strings.CutSuffix(l.Target, "+auto-merge")
		id, after, ok := strings.Cut(target, "@")
		if !ok || id == "" {
			continue
		}
		n, err := strconv.ParseInt(after, 10, 64)
		if err != nil {
			continue
		}
		return ReviewQuestions{ReviewTaskID: id, AfterCommentID: n, RestoreAutoMerge: restore}, true
	}
	return ReviewQuestions{}, false
}
