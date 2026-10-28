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

package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLiveTranscriptDirTailsAPartialTranscript(t *testing.T) {
	dir := t.TempDir()
	raw := strings.Join([]string{
		streamJSONLine(t, map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"content": []map[string]any{{"type": "text", "text": "still working"}},
			},
		}),
		// No terminal "result" event yet -- the run is still in progress --
		// and a truncated trailing line, the shape reading a file mid-write
		// always risks.
		`{"type": "assistant", "message": {"content"`,
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "r1"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	live := LiveTranscriptDir{Dir: dir}
	text, ok, err := live.Tail("r1")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if !strings.Contains(text, "still working") {
		t.Errorf("text = %q, want it to contain %q", text, "still working")
	}
}

func TestLiveTranscriptDirReportsNotOKForAMissingRun(t *testing.T) {
	live := LiveTranscriptDir{Dir: t.TempDir()}
	_, ok, err := live.Tail("no-such-run")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("ok = true for a run with no transcript file, want false")
	}
}
