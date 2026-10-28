package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/agent/claude"
	"github.com/ai-on-gke/grain/pkg/agent/codex"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// liveTranscriptDir returns the ui.LiveTranscript reader for
// transcriptDir. It can no longer be one framework's reader chosen at
// startup: a task carries its own model.Task.AgentFramework now, so two
// runs live in this same directory in two different formats
// (RunConfig.TranscriptPath's own doc comment), and picking by
// deployment default would leave every overriding task's live transcript
// blank until it finished.
//
// So the format is decided per file, by what is in it, rather than per
// deployment -- see liveTranscripts.Tail.
func LiveTranscriptDir(transcriptDir string) ui.LiveTranscript {
	return liveTranscripts{
		dir:         transcriptDir,
		claude:      claude.LiveTranscriptDir{Dir: transcriptDir},
		antigravity: antigravity.LiveTranscriptDir{Dir: transcriptDir},
		codex:       codex.LiveTranscriptDir{Dir: transcriptDir},
	}
}

// liveTranscripts reads a still-running run's transcript-in-progress
// with whichever framework's reader matches the file it finds.
type liveTranscripts struct {
	dir         string
	claude      claude.LiveTranscriptDir
	antigravity antigravity.LiveTranscriptDir
	codex       codex.LiveTranscriptDir
}

// Tail sniffs the file rather than being told which framework wrote it,
// the daemon having no per-run record of the framework to consult.
//
// All three formats are NDJSON -- each framework mirrors its own CLI's
// event stream -- so "does it start with a brace", which was enough
// while one framework tee'd a human-readable narrative, separates
// nothing. transcriptFramework is the discriminator, on the key and the
// event names each vocabulary uses.
//
// A file none of them claims, or one with no complete line yet, reads as
// the antigravity form -- whose PartialTranscript renders an empty
// string for it, the same "nothing to show yet" a caller already
// handles.
func (l liveTranscripts) Tail(runID string) (string, bool, error) {
	data, err := os.ReadFile(filepath.Join(l.dir, runID))
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	switch transcriptFramework(string(data)) {
	case model.AgentFrameworkClaude:
		return l.claude.Tail(runID)
	case model.AgentFrameworkCodex:
		return l.codex.Tail(runID)
	default:
		return l.antigravity.Tail(runID)
	}
}

// transcriptFramework names which framework wrote a capture, by the
// shape of the first line of it that parses -- not simply the first
// line, which is what keeps a file caught mid-write readable.
//
// The three vocabularies are told apart by two facts. agy tags its
// events with "event" where the other two use "type"; and codex's
// "type" is a dotted name from its own thread/item vocabulary
// ("item.completed", "turn.failed") or, on an older codex, a line whose
// payload is nested under "msg" -- where claude's is a bare word
// ("assistant", "result"). A line that is none of these leaves the
// question open and the next line is tried, since a framework whose
// stream this code does not recognize is better rendered as nothing than
// as somebody else's events.
func transcriptFramework(data string) string {
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var tagged struct {
			Type  string          `json:"type"`
			Event string          `json:"event"`
			Msg   json.RawMessage `json:"msg"`
		}
		if err := json.Unmarshal([]byte(line), &tagged); err != nil {
			continue
		}
		switch {
		case tagged.Event != "":
			return model.AgentFrameworkAntigravity
		case len(tagged.Msg) > 0:
			return model.AgentFrameworkCodex
		case strings.Contains(tagged.Type, "."):
			return model.AgentFrameworkCodex
		case tagged.Type != "":
			return model.AgentFrameworkClaude
		}
	}
	return ""
}
