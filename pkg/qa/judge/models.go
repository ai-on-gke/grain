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

package judge

import (
	"errors"
	"strings"
)

func ModelFamily(modelID string) string {
	lower := strings.ToLower(modelID)
	if strings.Contains(lower, "gemini") || strings.HasPrefix(lower, "agy") {
		return "gemini"
	}
	if strings.Contains(lower, "claude") || strings.Contains(lower, "anthropic") ||
		strings.Contains(lower, "opus") || strings.Contains(lower, "sonnet") || strings.Contains(lower, "haiku") {
		return "claude"
	}
	if strings.Contains(lower, "gpt") || strings.Contains(lower, "o1") || strings.Contains(lower, "o3") || strings.Contains(lower, "o4") || strings.Contains(lower, "openai") {
		return "gpt"
	}
	if strings.Contains(lower, "llama") {
		return "llama"
	}
	if strings.Contains(lower, "mistral") {
		return "mistral"
	}
	if strings.Contains(lower, "qwen") {
		return "qwen"
	}
	if strings.Contains(lower, "deepseek") {
		return "deepseek"
	}
	return "unknown"
}

func ValidateCrossFamilyJudge(judgeModel string, codingModels []string) error {
	judgeFam := ModelFamily(judgeModel)
	if judgeFam == "unknown" {
		return errors.New("judge model family is unknown; specify a recognized cross-family judge model (for example claude/opus/sonnet, gemini, gpt, llama, mistral, qwen, or deepseek)")
	}
	for _, cm := range codingModels {
		if cm == "" {
			continue
		}
		if ModelFamily(cm) == judgeFam {
			return errors.New("judge model family must be different from all coding model families")
		}
	}
	return nil
}
