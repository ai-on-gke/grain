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

// RelayedReviewThread maps an inline review comment relayed onto a pull
// request by one of grain's review tasks (by its GitHub REST comment ID)
// to its GraphQL review thread ID, and records whether the owner has
// decided what to do about that review's findings.
type RelayedReviewThread struct {
	ReviewTaskID string
	ReviewID     int
	CommentID    int
	ThreadID     string
	Decided      bool
}

// Target encodes the mapping as "<reviewTaskID>:<reviewID>:<commentID>:<state>:<threadID>".
func (r RelayedReviewThread) Target() string {
	state := "open"
	if r.Decided {
		state = "decided"
	}
	return fmt.Sprintf("%s:%d:%d:%s:%s", r.ReviewTaskID, r.ReviewID, r.CommentID, state, r.ThreadID)
}

// ParseRelayedReviewThread decodes a LinkRelayedReviewThread target.
func ParseRelayedReviewThread(target string) (RelayedReviewThread, bool) {
	parts := strings.SplitN(target, ":", 5)
	if len(parts) != 5 || parts[4] == "" {
		return RelayedReviewThread{}, false
	}
	reviewID, err := strconv.Atoi(parts[1])
	if err != nil {
		return RelayedReviewThread{}, false
	}
	commentID, err := strconv.Atoi(parts[2])
	if err != nil || commentID <= 0 {
		return RelayedReviewThread{}, false
	}
	return RelayedReviewThread{
		ReviewTaskID: parts[0],
		ReviewID:     reviewID,
		CommentID:    commentID,
		Decided:      parts[3] == "decided",
		ThreadID:     parts[4],
	}, true
}

// RelayedReviewThreadsOf returns every parsed LinkRelayedReviewThread on task.
func RelayedReviewThreadsOf(task Task) []RelayedReviewThread {
	var out []RelayedReviewThread
	for _, l := range task.Links {
		if l.Kind != LinkRelayedReviewThread {
			continue
		}
		if rt, ok := ParseRelayedReviewThread(l.Target); ok {
			out = append(out, rt)
		}
	}
	return out
}
