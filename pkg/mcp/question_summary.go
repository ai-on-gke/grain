package mcp

import (
	"fmt"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// questionSummaryGuidance is what every tool that asks a person
// something says about its `summary` argument: the one line the inbox,
// the task list and notifications show in place of the whole question
// (model.Comment.Summary).
var questionSummaryGuidance = fmt.Sprintf("A single line of at most %d characters, no newlines, "+
	"naming the decision or input you need -- what a person scanning their inbox reads "+
	"before opening the rest. It is shown alone in the inbox, on the task list and as "+
	"the title of any notification, with the full text behind an expander. Say what is "+
	"to be decided (\"Keep the v1 API alongside v2, or remove it?\"), never that you "+
	"have a question (\"Question about the API\", \"Need input\").", model.MaxQuestionSummaryLen)

// questionSummarySchema is the `summary` property, with what it summarizes
// ("the question", "what you need") said in its own description.
func questionSummarySchema(of string) map[string]any {
	return map[string]any{
		"type":        "string",
		"maxLength":   model.MaxQuestionSummaryLen,
		"description": "One-line summary of " + of + ". " + questionSummaryGuidance,
	}
}

// questionSummaryArg reads and checks a call's `summary`. A missing one
// is not an error -- grain derives one when it relays the question -- but
// one that is there has to be one line that fits.
func questionSummaryArg(args map[string]any) (string, error) {
	summary, _ := argString(args, "summary")
	if err := model.ValidateQuestionSummary(summary); err != nil {
		return "", err
	}
	return strings.TrimSpace(summary), nil
}
