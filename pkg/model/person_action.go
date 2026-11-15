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
	"regexp"
	"strings"
)

// A run whose outcome is something a person has to *do* -- set a variable,
// grant access, run a command, make a decision -- has to say so in the one
// line the inbox shows, not halfway down a closing note. Task #4256 is the
// case this was written for: a base-branch release repair found that
// publishing needed an Actions variable only a repository admin could set,
// and ended with comment_on_issue. A closing note carried no summary, so
// the inbox showed the note's opening ("I didn't change any code…") and
// the operator found the action only after somebody rewrote it as three
// steps.
//
// PersonActionInstruction is the rule every run is told
// (orchestrator.BuildPrompt) and the repair and release-triage task bodies
// repeat, kept here so they cannot drift apart: end with ask_question, its
// summary naming the action and its body opening with the exact steps.
// NoteAsksAPerson is the safety net behind it, which comment_on_issue uses
// to catch a note that asks for an action without a summary saying so.
const PersonActionInstruction = "If the outcome of your work needs a person to do " +
	"something -- set a variable or a secret, grant access, run a command, make a decision -- " +
	"end with ask_question, not comment_on_issue. Its `summary` names the action " +
	"(\"Set GRAIN_MERGE_LOGINS on ai-on-gke/grain to unblock image publishing\"). Its " +
	"question starts with a numbered checklist of the exact steps, each with its UI path and " +
	"the CLI command that does the same; then the option you recommend, if there is a choice; " +
	"then your diagnosis. Use comment_on_issue only for a report nobody has to act on, and " +
	"give it a `summary` too: that line is what the inbox and the task list show of a task " +
	"resting on the note."

// RepairNeedsAPersonReminder is the paragraph a grain-filed base-branch
// repair or release-triage task body ends its procedure with: the one kind
// of task most likely to find that the fix is not code at all -- an unset
// variable, a missing permission, a credential only an admin holds -- and
// the one that produced #4256.
const RepairNeedsAPersonReminder = "If the fix turns out not to be a code change you can " +
	"push -- an unset repository variable or secret, a permission or setting only an admin " +
	"can change -- do not end with a closing note. " + PersonActionInstruction

// personActionPatterns are the phrasings that make a closing note read as
// a request to a person rather than a report. Deliberately narrow: each
// one is a sentence addressed to somebody about something they have to
// do, not a verb that a report of finished work would use as well.
var personActionPatterns = []*regexp.Regexp{
	// "please set …", "please grant …"
	regexp.MustCompile(`(?i)\bplease\s+(set|grant|run|add|create|enable|configure|approve|install|rotate|decide|choose|confirm|update|give|merge|re-?run)\b`),
	// "only someone with admin …", "only a repo admin can …"
	regexp.MustCompile(`(?i)\bonly\s+(someone|somebody|a\s+person|a\s+human|the\s+operator|an?\s+(repo(sitory)?\s+|org(ani[sz]ation)?\s+)?(admin(istrator)?|owner|maintainer))\b`),
	// "you need to set …", "an admin must grant …", "the operator has to decide …"
	regexp.MustCompile(`(?i)\b(you|someone|somebody|the\s+operator|an?\s+(repo(sitory)?\s+)?admin(istrator)?|a\s+maintainer)\s+(will\s+)?(needs?|must|has\s+to|have\s+to|should)\s+(to\s+)?(set|grant|run|add|create|enable|configure|approve|install|rotate|decide|choose|give|merge)\b`),
	// "needs a human", "requires manual action", "needs admin access"
	regexp.MustCompile(`(?i)\b(needs?|requires?)\s+(a\s+)?(human|person|operator|manual\s+(action|intervention|step)|(repo(sitory)?\s+)?admin(istrator)?\s+(access|rights|permissions?|action))\b`),
	// The asks request_secret and request_access exist for, written as prose.
	regexp.MustCompile(`(?i)\b(grant|give)\s+(grain|this\s+task|the\s+task|me|us|the\s+agent)\s+(\w+\s+)?(access|permission|write|read)\b`),
}

// NoteAsksAPerson reports whether a closing note reads as needing a person
// to act, and the sentence that made it so (for the nudge to quote back).
// It is a heuristic over the note's prose, used only to ask the run to
// think again -- comment_on_issue's nudge -- never to refuse a note
// outright.
func NoteAsksAPerson(body string) (string, bool) {
	for _, re := range personActionPatterns {
		if loc := re.FindStringIndex(body); loc != nil {
			return sentenceAround(body, loc[0], loc[1]), true
		}
	}
	return "", false
}

// sentenceAround is the sentence of body holding [start, end): from just
// past the previous sentence end or line break to the next, cut to one
// summary's length.
func sentenceAround(body string, start, end int) string {
	from := strings.LastIndexAny(body[:start], ".!?\n") + 1
	to := len(body)
	if i := strings.IndexAny(body[end:], ".!?\n"); i >= 0 {
		to = end + i + 1
	}
	return truncateRunes(strings.Join(strings.Fields(body[from:to]), " "), MaxQuestionSummaryLen)
}
