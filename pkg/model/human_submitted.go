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
	"strings"
	"time"
)

// HumanSubmittedTarget is a LinkHumanSubmitted link's target: when the
// human submitted, and the pull request head they were shown when they
// did (head; "" where nothing said which it was).
//
// The head is the one the human's decision is about, and the only one
// orchestrator's pinHumanSubmit will pin it to: a push landing between
// the Submit and the cycle that reads the branch is a commit nobody
// reviewed, and is classified like any other moved head rather than
// cleared on the strength of a decision about its parent.
func HumanSubmittedTarget(at time.Time, head string) string {
	when := at.UTC().Format(time.RFC3339)
	head = strings.ToLower(strings.TrimSpace(head))
	if head == "" {
		return when
	}
	return when + "@" + head
}

// HumanSubmittedHead is the head a LinkHumanSubmitted target says the
// human was shown, or "" for one that names none -- written before the
// head was recorded, or by a Submit nothing could read the head for.
func HumanSubmittedHead(target string) string {
	if i := strings.LastIndexByte(target, '@'); i >= 0 {
		return target[i+1:]
	}
	return ""
}
