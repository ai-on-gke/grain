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

package cli

import (
	"context"
	"strings"
	"testing"
)

func TestCreateUpdateGetAndListExternalRef(t *testing.T) {
	_, c := externalCLIServer(t)
	ctx := context.Background()

	captureStdout(t, func() {
		if err := cmdCreate(ctx, c, &printer{}, []string{
			"-title", "Capability root",
			"-no-repo",
			"-external-ref", "CA-204",
		}); err != nil {
			t.Fatalf("cmdCreate: %v", err)
		}
		if err := cmdCreate(ctx, c, &printer{}, []string{
			"-title", "Unreferenced task",
			"-no-repo",
		}); err != nil {
			t.Fatalf("cmdCreate unreferenced: %v", err)
		}
	})

	gotText := captureStdout(t, func() {
		if err := cmdGet(ctx, c, &printer{}, []string{"1"}); err != nil {
			t.Fatalf("cmdGet: %v", err)
		}
	})
	if !strings.Contains(gotText, "external ref: CA-204") {
		t.Errorf("cmdGet output missing external ref line:\n%s", gotText)
	}

	filtered := captureStdout(t, func() {
		if err := cmdList(ctx, c, &printer{}, []string{"-external-ref", "CA-204"}); err != nil {
			t.Fatalf("cmdList -external-ref: %v", err)
		}
	})
	if !strings.Contains(filtered, "Capability root") || strings.Contains(filtered, "Unreferenced task") {
		t.Errorf("cmdList -external-ref output unexpected:\n%s", filtered)
	}

	emptyOnly := captureStdout(t, func() {
		if err := cmdList(ctx, c, &printer{}, []string{"-filter", "externalRef:empty"}); err != nil {
			t.Fatalf("cmdList -filter externalRef:empty: %v", err)
		}
	})
	if strings.Contains(emptyOnly, "Capability root") || !strings.Contains(emptyOnly, "Unreferenced task") {
		t.Errorf("cmdList -filter externalRef:empty output unexpected:\n%s", emptyOnly)
	}

	// Clear via grain update -external-ref "".
	captureStdout(t, func() {
		if err := cmdUpdate(ctx, c, &printer{}, []string{"1", "-external-ref", ""}); err != nil {
			t.Fatalf("cmdUpdate clear: %v", err)
		}
	})
	task, err := c.Task(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	if task.ExternalRef != "" {
		t.Errorf("after clear, ExternalRef = %q, want empty", task.ExternalRef)
	}
}
