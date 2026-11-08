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

package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type plainSink struct{}

func (plainSink) FlushRunEnd(context.Context, string, string, string) (bool, error) {
	return false, errors.New("bucket unavailable")
}
func (plainSink) RestoreRaw(context.Context, string, string) error { return nil }

type redactingSink struct {
	plainSink
	err error
}

func (s redactingSink) RedactNarrative(narrative string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return strings.ReplaceAll(narrative, "wlk-deploy-s3cr3t", "[redacted: secret deploy/token]"), nil
}

// TestFallbackNarrativeUsesTheSinksRedaction is the 2026-11-07
// walkthrough's leak (docs/test-runs/2026-11-07-instance-runtime-stores.md,
// item 4): with -object-store failing, the narrative went into
// task_run.transcript redacted only of the sandbox's own environment, so
// a deployment secret the bucket copy would have redacted was stored and
// served in the clear.
func TestFallbackNarrativeUsesTheSinksRedaction(t *testing.T) {
	const narrative = "DEPLOY_TOKEN=wlk-deploy-s3cr3t; done"
	if got := fallbackNarrative(redactingSink{}, "4-1", narrative); got != "DEPLOY_TOKEN=[redacted: secret deploy/token]; done" {
		t.Errorf("fallbackNarrative(redacting sink) = %q, want the secret redacted", got)
	}
	if got := fallbackNarrative(redactingSink{err: errors.New("secret store unreadable")}, "4-1", narrative); got != "" {
		t.Errorf("fallbackNarrative(sink whose redaction fails) = %q, want nothing kept", got)
	}
	if got := fallbackNarrative(plainSink{}, "4-1", narrative); got != narrative {
		t.Errorf("fallbackNarrative(sink without its own redaction) = %q, want the transcript unchanged", got)
	}
	if got := fallbackNarrative(redactingSink{}, "4-1", ""); got != "" {
		t.Errorf("fallbackNarrative(empty) = %q, want empty", got)
	}
}
