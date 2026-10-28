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
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync/atomic"
	"unicode"
	"unicode/utf8"
)

// A question's summary is the one line a person reads before deciding
// whether to open the rest of it: the inbox shows only it, with the full
// question behind an expander, the task list puts it on the row's status
// line, and a notification uses it as its title (Comment.Summary).
//
// The agent asking writes it (ask_question's, request_secret's and
// chat's `summary`, and POST /api/tasks/{id}/messages's). It is optional
// on the wire, because a run dispatched before the parameter existed --
// or an agent that ignored it -- still has to be relayed, so a question
// without one gets one derived from its own first sentence
// (DeriveQuestionSummary) and the fallback is logged and counted
// (QuestionSummaryFallbacks), which is how missing summaries show up.

// MaxQuestionSummaryLen is the longest summary, in characters, a tool
// call may give: about what one inbox row or one notification title
// holds before it has to be cut.
const MaxQuestionSummaryLen = 100

// ErrQuestionSummaryMultiline is ValidateQuestionSummary's answer to a
// summary of more than one line.
var ErrQuestionSummaryMultiline = errors.New("summary must be a single line: put the detail in the question itself")

// ValidateQuestionSummary checks a summary an agent gave: one line, and
// at most MaxQuestionSummaryLen characters once surrounding space is
// trimmed. An empty one is valid here -- the parameter is optional on the
// wire, and an empty one is derived instead.
func ValidateQuestionSummary(summary string) error {
	s := strings.TrimSpace(summary)
	if strings.ContainsAny(s, "\r\n") {
		return ErrQuestionSummaryMultiline
	}
	if n := utf8.RuneCountInString(s); n > MaxQuestionSummaryLen {
		return fmt.Errorf("summary is %d characters; keep it to one line of at most %d "+
			"that says what decision is needed, and put the rest in the question", n, MaxQuestionSummaryLen)
	}
	return nil
}

// NormalizeQuestionSummary is summary as it is stored: every run of
// whitespace, newlines included, collapsed to one space, and cut to
// MaxQuestionSummaryLen characters with an ellipsis. Validation refuses
// what this would have to change at the tool; this is what a path that
// cannot refuse -- relaying a finished run, an API call from an older
// client -- does instead.
func NormalizeQuestionSummary(summary string) string {
	return truncateRunes(strings.Join(strings.Fields(summary), " "), MaxQuestionSummaryLen)
}

// DeriveQuestionSummary is the summary a question gets when its asker
// gave none: its first sentence, or failing that its first line, with
// markdown's leading decoration stripped and cut to fit.
func DeriveQuestionSummary(body string) string {
	line := ""
	for _, l := range strings.Split(body, "\n") {
		l = strings.TrimSpace(l)
		l = strings.TrimLeft(l, "#>*-+ \t")
		l = orderedListMarker.ReplaceAllString(l, "")
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "```") {
			line = l
			break
		}
	}
	if line == "" {
		return ""
	}
	line = strings.NewReplacer("**", "", "__", "", "`", "").Replace(line)
	runes := []rune(line)
	for i, r := range runes {
		if r != '.' && r != '?' && r != '!' {
			continue
		}
		// A sentence ends at punctuation followed by a space or the end
		// of the line -- not at the dot in "pkg/model.Store" or "v1.2".
		if i == len(runes)-1 || unicode.IsSpace(runes[i+1]) {
			line = string(runes[:i+1])
			break
		}
	}
	return NormalizeQuestionSummary(line)
}

// orderedListMarker is a "1." or "2)" opening a line, which would
// otherwise read as a one-word first sentence.
var orderedListMarker = regexp.MustCompile(`^[0-9]+[.)]\s+`)

var questionSummaryFallbacks atomic.Int64

// QuestionSummaryFallbacks is how many questions this process has had to
// derive a summary for because their asker gave none.
func QuestionSummaryFallbacks() int64 { return questionSummaryFallbacks.Load() }

// QuestionSummary is the summary to store for a question: the one given,
// normalized, or -- when none was -- one derived from body, logged and
// counted against where (which path asked: "ask_question", "chat", ...)
// so a missing summary is visible rather than silently papered over.
func QuestionSummary(summary, body, where string) string {
	if s := NormalizeQuestionSummary(summary); s != "" {
		return s
	}
	derived := DeriveQuestionSummary(body)
	n := questionSummaryFallbacks.Add(1)
	log.Printf("question summary: %s gave none; derived %q (%d fallbacks since start)", where, derived, n)
	return derived
}

// truncateRunes cuts s to at most max characters, ending in an ellipsis
// when it had to cut, and preferring a word boundary when one is near.
func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	cut := runes[:max-1]
	if i := strings.LastIndexFunc(string(cut), unicode.IsSpace); i > 0 {
		if head := []rune(string(cut)[:i]); len(head) >= max*2/3 {
			cut = head
		}
	}
	return strings.TrimRightFunc(string(cut), func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r)
	}) + "…"
}
