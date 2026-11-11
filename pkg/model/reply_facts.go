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

// ReplyFacts is what tells apart the asks an awaiting_reply task can be
// parked on, beyond what Task itself says (Task.Interactive): the facts a
// reader resolves from the task's observation and its pending comment.
// The orchestrator's liveness sub-case reads them (awaiting_reply/question,
// /answered, /unanswered_comment, /your_message, /access_request, /stopped), and they are the
// same facts the UI's askedCase tells its badge apart by, so the badge and
// the reason beside it cannot disagree.
//
// The zero value is a task parked on nothing but its own last comment: a
// run that answered in a comment and opened no pull request.
type ReplyFacts struct {
	// Question is a pending question a person has to answer: an
	// ask_question comment (Observation.PendingQuestionCommentID, other
	// than the comment Stop posts) or a request_secret
	// (Observation.PendingSecret).
	Question bool
	// Access is a pending request_access call
	// (Observation.PendingAccess).
	Access bool
	// Stopped is a turn a person stopped: the pending comment is
	// TurnStoppedComment.
	Stopped bool
	// UnansweredComment is a person's comment no run has been shown
	// (UndeliveredPersonComment): the task is waiting on a run, not on
	// its owner's Reply.
	UnansweredComment bool
}
