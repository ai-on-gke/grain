package agent

import (
	"bytes"
	"io"
	"sync"
)

// SessionWatcher is the io.Writer a Framework puts in front of the live
// mirror of its CLI's own stream, so that the conversation a run is in
// is reported the moment the CLI names it rather than only when Run
// returns (RunConfig.OnResumableSession).
//
// It exists for the run that never returns a Result at all: a daemon
// stopped mid-turn -- an upgrade, a restart -- cancels the run, and the
// partial Result a cancelled run hands back is read too late if the
// drain is cut off, and never if the process is killed. The CLI named
// its session in the first line it wrote; this is what writes it down.
//
// Each line is handed to extract, which returns the session it names or
// "" -- in practice the framework's own parser run over that one line,
// so the early answer and the final one can never be read two different
// ways. report is called once per change of session, never with "", and
// never concurrently with itself. Writes pass through to next unchanged;
// a nil next discards them, for a run with no transcript path.
type SessionWatcher struct {
	next    io.Writer
	extract func(line []byte) string
	report  func(session string)

	mu      sync.Mutex
	partial []byte
	last    string
}

// NewSessionWatcher wraps next. A nil report makes it next itself (or
// io.Discard's equivalent for a nil next): there is nothing to watch for.
func NewSessionWatcher(next io.Writer, extract func(line []byte) string, report func(session string)) io.Writer {
	if report == nil || extract == nil {
		return next
	}
	return &SessionWatcher{next: next, extract: extract, report: report}
}

// Write implements io.Writer.
func (w *SessionWatcher) Write(p []byte) (int, error) {
	w.scan(p)
	if w.next == nil {
		return len(p), nil
	}
	return w.next.Write(p)
}

func (w *SessionWatcher) scan(p []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.partial = append(w.partial, p...)
	for {
		i := bytes.IndexByte(w.partial, '\n')
		if i < 0 {
			break
		}
		line := w.partial[:i]
		w.partial = w.partial[i+1:]
		if id := w.extract(bytes.TrimSpace(line)); id != "" && id != w.last {
			w.last = id
			w.report(id)
		}
	}
	// A line is one stream event, and no event is anywhere near this
	// long; a writer that never sees a newline must not grow forever.
	if len(w.partial) > maxWatchedLine {
		w.partial = w.partial[:0]
	}
	if len(w.partial) == 0 {
		w.partial = nil
	}
}

// maxWatchedLine bounds the unterminated tail SessionWatcher holds. The
// session id is on a CLI's first, short event; a longer line is a tool
// result, and losing one of those to the bound costs this nothing.
const maxWatchedLine = 1 << 20
