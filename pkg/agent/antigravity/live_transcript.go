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

package antigravity

import (
	"os"
	"path/filepath"
)

// LiveTranscriptDir implements pkg/ui's LiveTranscript interface --
// structurally, not by importing it, the same way pkg/systemlog
// implements ui.LogSource without either package depending on the other
// -- by reading back whatever Framework.Run has mirrored so far into
// Dir/<runID> (RunConfig.TranscriptPath's own doc comment) and rendering
// it with PartialTranscript. A deployment wires one of these into
// ui.Config.LiveTranscripts at the same Dir it hands orchestrator.Config
// as TranscriptDir, so the run ID a task's own Attempt names is the same
// file both sides agree on (bwsalmon/agents#467).
type LiveTranscriptDir struct {
	Dir string
}

// Tail reads Dir/<runID>'s current contents and parses however much of a
// transcript is in it so far. ok is false when that file does not exist
// yet -- a run whose Framework has not opened RunConfig.TranscriptPath
// yet, was never given one at all, or is running a Framework other than
// this package's own (agent/claude mirrors its own subprocess's
// stream-json, a different event vocabulary read back by its own
// claude.LiveTranscriptDir, since a deployment only ever wires one
// Framework's reader into ui.Config.LiveTranscripts at a time) -- which a
// caller should read the same way a LiveTranscript-less deployment would:
// fall back to whatever the store has recorded once the run finishes.
func (d LiveTranscriptDir) Tail(runID string) (string, bool, error) {
	data, err := os.ReadFile(filepath.Join(d.Dir, runID))
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	text := PartialTranscript(string(data))
	return text, text != "", nil
}
