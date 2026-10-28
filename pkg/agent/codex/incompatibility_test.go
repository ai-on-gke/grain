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
	"errors"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
)

func TestModelIncompatibilityFailureClassifiesAccountRefusal(t *testing.T) {
	err := errors.New(`exit status 1 (stderr: The model "gpt-5.1-codex" is not available with your ChatGPT account)`)
	got := modelIncompatibilityFailure("gpt-5.1-codex", "failed prompt", err)
	if got == nil {
		t.Fatal("modelIncompatibilityFailure returned nil")
	}
	if got.Model != "gpt-5.1-codex" || got.Framework != "codex" {
		t.Fatalf("incompatibility = %+v", got)
	}
	if got.Message != "failed prompt exit status 1 (stderr: The model \"gpt-5.1-codex\" is not available with your ChatGPT account)" {
		t.Fatalf("unexpected Message: %q", got.Message)
	}
	if found, ok := agent.ModelIncompatibility(got); !ok || found != got {
		t.Fatal("typed incompatibility was not discoverable")
	}
}

func TestModelIncompatibilityFailureIgnoresOrdinaryFailure(t *testing.T) {
	if got := modelIncompatibilityFailure("gpt-5", "repository test failed", errors.New("exit status 1")); got != nil {
		t.Fatalf("ordinary failure classified as %+v", got)
	}
}
