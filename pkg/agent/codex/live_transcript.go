package codex

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
// file both sides agree on.
type LiveTranscriptDir struct {
	Dir string
}

// Tail reads Dir/<runID>'s current contents and parses however much of a
// transcript is in it so far. ok is false when that file does not exist
// yet -- a run whose Framework has not opened RunConfig.TranscriptPath
// yet, or was never given one at all -- which a caller should read the
// same way a LiveTranscript-less deployment would: fall back to whatever
// the store has recorded once the run finishes.
//
// A file written by another framework is not this reader's to interpret:
// each of the three writes its own event vocabulary, and which one wrote
// a given run's file is decided per task now (model.Task.AgentFramework),
// so cmd/grain's own liveTranscripts picks the reader by what is in the
// file rather than handing every run to one of these.
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
