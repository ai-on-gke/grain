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

import "fmt"

// NothingNeededAction is what grain does with a task whose run ended on
// the nothing_needed tool -- an agent saying that, having looked, the task
// needs no change and no report anybody has to act on.
//
// Before the tool existed the only way to say that was comment_on_issue,
// which completes the task into awaiting_reply beside every investigation
// whose report somebody has to read, so deliberate no-ops piled up in the
// operator's inbox with nothing telling them apart. Closing is therefore
// the default; a task whose "nothing to do" still wants a human's eye
// says NothingNeededAck.
type NothingNeededAction string

const (
	// NothingNeededClose closes the task outright: Observation.ClosedAt
	// is stamped and it never shows in awaiting_reply. The empty string
	// means this too.
	NothingNeededClose NothingNeededAction = "close"
	// NothingNeededAck completes the task the way a closing comment
	// does, leaving it in awaiting_reply until somebody acknowledges it.
	NothingNeededAck NothingNeededAction = "ack"
)

// NothingNeededClosedNote is the sentence posted on a task
// NothingNeededClose closed, after the agent's own reason.
const NothingNeededClosedNote = "Closed: the agent found nothing needed."

// Resolved is a, with "" read as its default.
func (a NothingNeededAction) Resolved() NothingNeededAction {
	if a == "" {
		return NothingNeededClose
	}
	return a
}

// ParseNothingNeededAction validates a value arriving from the API, a
// declaration or a proposal. "" is accepted and kept as "": it is the
// "nobody chose" that Resolved reads as NothingNeededClose.
func ParseNothingNeededAction(s string) (NothingNeededAction, error) {
	switch a := NothingNeededAction(s); a {
	case "", NothingNeededClose, NothingNeededAck:
		return a, nil
	default:
		return "", fmt.Errorf("nothing_needed_action %q: want %q or %q", s, NothingNeededClose, NothingNeededAck)
	}
}
