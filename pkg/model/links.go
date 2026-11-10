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
	"net/url"
	"strconv"
)

// TaskPath is the UI's own address for a task, root-relative so it works
// behind whatever proxy the page is served through.
func TaskPath(taskID string) string {
	return "/tasks/" + url.PathEscape(taskID)
}

// CommentPath is the UI's address for one comment in a task's
// conversation: the task's own path with the comment's id (Comment.ID,
// the one the API returns) as its fragment. The UI scrolls to and
// highlights the comment it names (ui/src/commentLinks.js).
func CommentPath(taskID string, commentID int64) string {
	return TaskPath(taskID) + "#comment=" + strconv.FormatInt(commentID, 10)
}
