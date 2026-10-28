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
)

// The judgement model (docs/design/durable-tasks.md#DT-11).
//
// A durable judgement agent -- the pipeline steward -- is pinned to a
// strong model rather than run on the rotation: 768's first wake ran on
// the default rotation and described the operator's proposals as port
// tasks. A concrete model id in grain's own template would not resolve on
// every deployment, so the template names a symbol, JudgementModelName,
// and dispatch resolves it (ResolveJudgementModel): to
// Config.JudgementModel when the deployment set one, and otherwise to the
// strongest model its connections offer, by JudgementModelRanking.
//
// A deployment where nothing resolves runs the task on its rotation, as
// if it had chosen no model, and orchestrator raises
// JudgementModelUnresolvedSubject so that somebody is told.

// JudgementModelName is the symbolic model a template or task names to
// mean "this deployment's judgement model".
const JudgementModelName = "judgement"

// JudgementModelUnresolvedSubject is the notification raised while a
// task asking for JudgementModelName runs on the rotation because
// nothing resolved it.
const JudgementModelUnresolvedSubject = "durable.no-judgement-model"

// JudgementModelRanking is the static ranking ResolveJudgementModel walks
// when Config.JudgementModel is unset: strongest first. Each entry is
// matched case-insensitively against the models connections advertise,
// with '.' and '-' read as the same, so Antigravity's "opus-5.5" and
// Claude's "claude-opus-5-5" both answer the first entry. A medium-effort
// variant ranks below its high-effort sibling and above anything weaker:
// DT-11 prefers it to the Gemini half of the rotation.
var JudgementModelRanking = []string{
	"claude-opus-5-5",
	"opus-5-5",
	"opus-5-5-high",
	"opus-5-5-medium",
	"claude-fable-5-1",
	"fable-5-1",
	"claude-sonnet-5-5",
	"sonnet-5-5",
	"gemini-3-1-pro",
	"gpt-5",
}

// IsJudgementModel reports whether model is the symbolic judgement model.
func IsJudgementModel(model string) bool {
	return strings.EqualFold(strings.TrimSpace(model), JudgementModelName)
}

// ResolveJudgementModel is the concrete model JudgementModelName stands
// for on a deployment configured as cfg, with backends its connections
// (disabled ones are skipped): cfg.JudgementModel whenever it is set --
// the deployment's own word, which a task then runs on or waits for like
// any model it chose -- and otherwise the first model of
// JudgementModelRanking a connection advertises, in that connection's own
// spelling. ok is false when neither answers -- the caller runs the task
// on the rotation and raises JudgementModelUnresolvedSubject.
func ResolveJudgementModel(cfg Config, backends []AgentBackend) (model string, ok bool) {
	if want := strings.TrimSpace(cfg.JudgementModel); want != "" && !IsJudgementModel(want) {
		return want, true
	}
	var offered []string
	for _, b := range backends {
		if b.Disabled {
			continue
		}
		offered = append(offered, b.SupportedModels()...)
	}
	for _, rank := range JudgementModelRanking {
		for _, m := range offered {
			if judgementKey(m) == rank {
				return m, true
			}
		}
	}
	return "", false
}

// judgementKey is m as JudgementModelRanking spells it: lower case, with
// '.' and '_' read as '-'.
func judgementKey(m string) string {
	return strings.NewReplacer(".", "-", "_", "-").Replace(strings.ToLower(strings.TrimSpace(m)))
}
