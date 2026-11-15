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
// The head is the one the human's approval is about: a push landing after
// the Submit (other than a reviewer's own clean fix commit) is code nobody
// reviewed, so the approval does not carry to it.
func HumanSubmittedTarget(at time.Time, head string) string {
	return HumanSubmittedByTarget("", at, head)
}

// HumanSubmittedByTarget encodes who approved via Submit alongside when and
// which pull request head they saw (`[by|]RFC3339[@head]`).
func HumanSubmittedByTarget(by string, at time.Time, head string) string {
	by = strings.ReplaceAll(strings.TrimSpace(by), "|", "")
	when := at.UTC().Format(time.RFC3339)
	head = strings.ToLower(strings.TrimSpace(head))
	base := when
	if by != "" {
		base = by + "|" + when
	}
	if head == "" {
		return base
	}
	return base + "@" + head
}

// HumanSubmittedHead is the head a LinkHumanSubmitted target says the
// human was shown, or "" for one that names none -- written before the
// head was recorded, or by a Submit nothing could read the head for.
func HumanSubmittedHead(target string) string {
	_, _, head := splitHumanSubmitted(target)
	return head
}

// splitHumanSubmitted takes a `[by|]RFC3339[@head]` target apart. by is
// read off the front, up to the one '|' HumanSubmittedByTarget writes (it
// strips any from by itself), and head off what follows the timestamp: by is
// a person's ID, often an e-mail address, so the last '@' of the whole
// target is the head's only where one was written -- a Submit naming no
// head by alice@example.com would otherwise read as one of "example.com|...".
func splitHumanSubmitted(target string) (by, when, head string) {
	rest := target
	if b, r, ok := strings.Cut(rest, "|"); ok {
		by, rest = strings.TrimSpace(b), r
	}
	when, head, _ = strings.Cut(rest, "@")
	return by, strings.TrimSpace(when), strings.TrimSpace(head)
}

// HumanSubmittedBy returns who recorded the LinkHumanSubmitted approval, or ""
// when the target was written without an actor.
func HumanSubmittedBy(target string) string {
	by, _, _ := splitHumanSubmitted(target)
	return by
}

// HumanSubmittedAt parses the timestamp recorded in a LinkHumanSubmitted
// target.
func HumanSubmittedAt(target string) (time.Time, bool) {
	_, when, _ := splitHumanSubmitted(target)
	at, err := time.Parse(time.RFC3339, when)
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}
