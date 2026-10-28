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

package mcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// questionTools are every tool that asks a person something and parks or
// waits on the answer -- each of which carries a one-line summary.
func questionTools(t *testing.T) map[string]Tool {
	t.Helper()
	mock := NewMockTools(&MockSink{}, nil)
	return map[string]Tool{
		"ask_question":   toolNamed(t, mock, "ask_question"),
		"request_secret": toolNamed(t, mock, "request_secret"),
		"chat":           toolNamed(t, NewChatTools(&fakeChatter{}, nil), "chat"),
	}
}

func TestQuestionToolsDeclareASummary(t *testing.T) {
	t.Parallel()
	for name, tool := range questionTools(t) {
		props := tool.InputSchema["properties"].(map[string]any)
		summary, ok := props["summary"].(map[string]any)
		if !ok {
			t.Errorf("%s has no summary property", name)
			continue
		}
		if summary["maxLength"] != model.MaxQuestionSummaryLen {
			t.Errorf("%s summary maxLength = %v, want %d", name, summary["maxLength"], model.MaxQuestionSummaryLen)
		}
		desc, _ := summary["description"].(string)
		for _, want := range []string{"single line", "decision", "inbox"} {
			if !strings.Contains(desc, want) {
				t.Errorf("%s summary description does not mention %q: %s", name, want, desc)
			}
		}
		if req, _ := tool.InputSchema["required"].([]string); !slices.Contains(req, "summary") {
			t.Errorf("%s does not list summary as required: %v", name, req)
		}
	}
}

// Required in the schema, so a model is told to give it -- but a call
// without one still works, because an older agent or an in-flight run
// never saw the parameter and grain derives one when it relays.
func TestQuestionToolsRefuseABadSummaryAndTolerateAMissingOne(t *testing.T) {
	t.Parallel()
	base := map[string]map[string]any{
		"ask_question":   {"question": "which config file?"},
		"request_secret": {"secret": "stripe-api-key", "reason": "the deploy script authenticates with it"},
		"chat":           {"message": "which config file?"},
	}
	with := func(name string, summary any) map[string]any {
		args := map[string]any{}
		for k, v := range base[name] {
			args[k] = v
		}
		if summary != nil {
			args["summary"] = summary
		}
		return args
	}
	for name, tool := range questionTools(t) {
		if res := tool.Handler(context.Background(), with(name, nil)); res.IsError {
			t.Errorf("%s refused a call with no summary: %s", name, res.Text)
		}
		if res := tool.Handler(context.Background(), with(name, "Pick the config file")); res.IsError {
			t.Errorf("%s refused a good summary: %s", name, res.Text)
		}
		if res := tool.Handler(context.Background(), with(name, "Pick the\nconfig file")); !res.IsError ||
			!strings.Contains(res.Text, "single line") {
			t.Errorf("%s accepted a two-line summary: %+v", name, res)
		}
		long := strings.Repeat("x", model.MaxQuestionSummaryLen+1)
		if res := tool.Handler(context.Background(), with(name, long)); !res.IsError {
			t.Errorf("%s accepted a %d-character summary", name, len(long))
		}
	}
}

func TestChatPassesTheSummaryOn(t *testing.T) {
	t.Parallel()
	chatter := &fakeChatter{turn: ChatTurn{Reply: "dev"}}
	res := chat(t, context.Background(), chatter, map[string]any{
		"message": "which config file? there are two", "summary": "  Pick the config file ",
	})
	if res.IsError {
		t.Fatalf("chat: %s", res.Text)
	}
	if len(chatter.summaries) != 1 || chatter.summaries[0] != "Pick the config file" {
		t.Errorf("summaries = %q, want the trimmed summary", chatter.summaries)
	}
}
