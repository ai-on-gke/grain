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

package codex

import (
	"strings"

	"github.com/ai-on-gke/grain/pkg/agent"
)

var incompatibilityMarkers = []string{
	"unsupported model",
	"model is not supported",
	"model_not_supported",
	"does not support this model",
	"not available with your chatgpt account",
	"not available for your account",
}

func modelIncompatibilityFailure(model, resultText string, runErr error) *agent.ModelIncompatibilityError {
	text := resultText
	if runErr != nil {
		text = strings.TrimSpace(text + "\n" + runErr.Error())
	}
	lower := strings.ToLower(text)
	for _, marker := range incompatibilityMarkers {
		if strings.Contains(lower, marker) {
			return &agent.ModelIncompatibilityError{
				Framework: usageLimitFramework,
				Model:     model,
				Message:   agent.TrimLimitMessage(text),
				Err:       runErr,
			}
		}
	}
	return nil
}
