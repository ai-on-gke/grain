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
