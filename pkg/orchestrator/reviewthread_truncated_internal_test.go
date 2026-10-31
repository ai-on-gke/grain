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
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
)

// A thread whose comment listing fills ListReviewThreads' page may have
// replies past it nobody read, so grain must not count it as all its own.
func TestThreadHasHumanCommentFailsClosedOnAFullCommentPage(t *testing.T) {
	t.Parallel()
	byID := map[int]github.ReviewComment{}
	var ids []int
	for i := 1; i <= reviewThreadCommentsPage; i++ {
		byID[i] = github.ReviewComment{ID: i, User: "grain[bot]"}
		ids = append(ids, i)
	}
	if !threadHasHumanComment(github.ReviewThread{ID: "T", CommentIDs: ids}, byID, "grain[bot]") {
		t.Error("a thread with a full page of comments must count as possibly having a person's reply")
	}
	if threadHasHumanComment(github.ReviewThread{ID: "T", CommentIDs: ids[:2]}, byID, "grain[bot]") {
		t.Error("a short thread of grain's own comments must not count as having a person's reply")
	}
}
