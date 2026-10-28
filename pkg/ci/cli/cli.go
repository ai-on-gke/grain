// Package cli is the part of `grain ci` (pkg/cli/cicmd.go) that is not
// flags or printing: polling a run until it is over, following a run in
// flight as a stream of only the output nobody has been shown yet, which
// steps' output a reader asked for, and what a finished run exits with.
// It speaks to a daemon only through RunSource, which pkg/ui's HTTPClient
// satisfies.
package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
)

// WatchInterval is how often -watch asks after the run it started. A
// CI job is a sandbox build followed by minutes of shell, so a poll a
// second would be asking a hundred times per thing that changes; five
// seconds is the same cadence the UI's own pane polls at, for the same
// reason.
const WatchInterval = 5 * time.Second

// RunSource is where a run's current state is read from: pkg/ui's
// HTTPClient, asking a daemon over REST.
type RunSource interface {
	CIRun(ctx context.Context, id string) (ci.Run, error)
}

// Watch polls one run until it is over, or until the context ends.
//
// A run that has been cancelled or has failed is still a run that
// finished, and this returns it rather than an error: what to make of a
// conclusion is the caller's (RunFailure's), and the reader wants the
// jobs either way.
func Watch(ctx context.Context, c RunSource, id string) (ci.Run, error) {
	return Poll(ctx, c, id, nil)
}

// Poll is Watch's loop with a hook: each snapshot of the run is
// handed to each, including the last one, before the wait for the next.
// That is what -follow is -- the same poll, with somebody reading every
// answer rather than only the final one.
func Poll(ctx context.Context, c RunSource, id string, each func(ci.Run)) (ci.Run, error) {
	ticker := time.NewTicker(WatchInterval)
	defer ticker.Stop()
	for {
		run, err := c.CIRun(ctx, id)
		if err != nil {
			return ci.Run{}, err
		}
		if each != nil {
			each(run)
		}
		if run.Conclusion.Done() {
			return run, nil
		}
		select {
		case <-ctx.Done():
			// Whoever pressed ^C is not owed a stack trace, and the run
			// itself is still going -- say where to pick it up again.
			return ci.Run{}, fmt.Errorf("stopped waiting on run %s, which is still going: %w", id, ctx.Err())
		case <-ticker.C:
		}
	}
}

// Follower turns repeated snapshots of one run into a terminal stream:
// per poll, per job and per step, whatever text this snapshot holds that
// the last one did not, under a header line whenever the writing moves
// from one step to another.
//
// It keeps the whole of what it has already written for each step rather
// than a byte count, because a step's live output does not only grow. It
// is bounded (ci.MaxStepOutput: the head is kept, the newest lines are
// kept, the middle is dropped and replaced by a note), so past that bound
// each poll *rewrites* the text -- and a follower resuming at "byte N of
// what I saw last time" would, from that moment on, print the middle of
// whatever line now sits at byte N and then nothing ever again. Holding
// the text makes that detectable: the bytes before the resume point stop
// matching, and NewOutput says so out loud rather than going quiet.
//
// The memory that costs is the run's own: a step is worth at most
// ci.MaxStepOutput either way, and the ci.Run this is fed is already
// carrying every byte of it.
type Follower struct {
	w    io.Writer
	mode OutputMode

	seen  map[string]string // source key -> what has already been written for it
	noted map[string]bool   // one-off lines (a job's or a step's error) already written
	last  string            // which source the last bytes came from
	// bol is whether the last thing written ended a line. A step's output
	// is written verbatim, so it need not end in a newline -- and a header
	// glued onto the end of a half-written line is unreadable.
	bol bool
}

// NewFollower returns a Follower writing to w.
func NewFollower(w io.Writer, mode OutputMode) *Follower {
	return &Follower{w: w, mode: mode, seen: map[string]string{}, noted: map[string]bool{}, bol: true}
}

// Follow writes whatever this snapshot of the run holds that no earlier
// one did.
//
// In the run's own job order rather than ci.SortJobRuns's failures-first
// one: that order changes under the reader as jobs fail, and a stream
// whose sources reshuffle between polls reads as if work were happening
// twice. The summary printed at the end sorts, which is where sorting is
// worth something.
func (f *Follower) Follow(run ci.Run) {
	for _, job := range run.Jobs {
		name := JobName(job)
		key := job.WorkflowPath + "\x00" + job.JobID + "\x00" + name
		if job.Error != "" {
			// Not a step's non-zero exit: a sandbox that could not be
			// built, a transport that stopped answering, a job timeout --
			// and on a job that never ran a step, the only thing there is
			// to print.
			f.note(key+"\x00error", "job "+name, "error: "+job.Error)
		}
		for i, step := range job.Steps {
			stepKey := fmt.Sprintf("%s\x00%d\x00%s", key, i, step.Name)
			source := fmt.Sprintf("job %s, step %s", name, step.Name)
			if step.Error != "" {
				f.note(stepKey+"\x00error", source, "error: "+step.Error)
			}
			// Once a step has been followed it is followed to the end,
			// even if finishing takes it out of what the mode shows: the
			// default follows a step because it is running, and a stream
			// that dropped it the moment it went green would stop in the
			// middle of its output with no word on how it ended.
			if _, following := f.seen[stepKey]; following || f.mode.Shows(step) {
				f.write(stepKey, source, step.Output)
			}
		}
	}
}

// write emits the part of text that has not been written for this source
// yet, under a header if the source has changed.
func (f *Follower) write(key, source, text string) {
	prev := f.seen[key]
	f.seen[key] = text
	added, rewritten := NewOutput(prev, text)
	if added == "" && !rewritten {
		return
	}
	f.header(source)
	if rewritten {
		f.line("[grain ci] this step has now printed more than grain keeps of one step " +
			"(256KB): its middle has been dropped, so what follows carries on from the newest " +
			"text it still holds and may repeat a line printed above.")
	}
	if added == "" {
		return
	}
	// Verbatim and unindented, the way `grain ci show` prints a step's
	// output: somebody is about to copy a line of it into a search.
	fmt.Fprint(f.w, added)
	f.bol = strings.HasSuffix(added, "\n")
}

// note writes one line that belongs to a source and is written at most
// once -- an error, which unlike output does not accumulate.
func (f *Follower) note(key, source, line string) {
	if f.noted[key] {
		return
	}
	f.noted[key] = true
	f.header(source)
	f.line(line)
}

// header says where the text below it is coming from, and is printed only
// when that changes: a step writing over several polls in a row is one
// stream of output, not one per poll.
func (f *Follower) header(source string) {
	if f.last == source {
		return
	}
	if !f.bol {
		fmt.Fprintln(f.w)
		f.bol = true
	}
	if f.last != "" {
		fmt.Fprintln(f.w)
	}
	f.last = source
	f.line("=== " + source + " ===")
}

// line writes one whole line of grain's own, starting one if the output
// so far stopped mid-line.
func (f *Follower) line(s string) {
	if !f.bol {
		fmt.Fprintln(f.w)
	}
	fmt.Fprintln(f.w, s)
	f.bol = true
}

// NewOutput is what of cur a reader already shown prev has not seen,
// and whether cur is a rewrite of prev rather than a longer version of
// it.
//
// Nearly always a step's output only grows, prev is a prefix of cur, and
// the answer is the bytes past it. Past ci.MaxStepOutput it stops being
// so: the buffer keeps its head and its newest lines and elides the
// middle, so the text a reader was shown last time is no longer a prefix
// of the text this time, and there is no offset to carry on from. When
// that happens this resumes as close to where the reader left off as the
// remaining text allows -- the newest thing they were shown, looked for
// in what is left -- and reports the rewrite so the caller can say so.
// Going quiet instead is the one outcome worth ruling out: that is a
// follower that silently stops following the moment a step gets chatty.
func NewOutput(prev, cur string) (added string, rewritten bool) {
	if strings.HasPrefix(cur, prev) {
		return cur[len(prev):], false
	}
	if anchor := resumeAnchor(prev); anchor != "" {
		// LastIndex rather than Index: the anchor is the newest text the
		// reader has, so its last occurrence is the one they are at.
		if i := strings.LastIndex(cur, anchor); i >= 0 {
			return cur[i+len(anchor):], true
		}
	}
	// Nothing recognisable survived -- the step has printed a whole
	// bufferful since the last poll. Everything it still holds is new to
	// this reader, bar whatever the head repeats.
	return cur, true
}

// resumeAnchor is the newest text a reader has already been shown, to
// be looked for in a buffer that has since been rewritten.
//
// A whole line at least, and up to a screenful: a one-word last line
// ("ok", "done") occurs everywhere in a test log and would resume in the
// wrong place, so this takes the last of prev up to anchorBytes and
// trims back to a line boundary, which makes an accidental match
// vanishingly unlikely without pinning the anchor to text so long that
// elision must have eaten part of it.
func resumeAnchor(prev string) string {
	s := strings.TrimRight(prev, "\n")
	if len(s) <= anchorBytes {
		return s
	}
	s = s[len(s)-anchorBytes:]
	if i := strings.IndexByte(s, '\n'); i >= 0 && i+1 < len(s) {
		s = s[i+1:]
	}
	return s
}

// anchorBytes is how much of what has already been printed NewOutput
// looks for when the buffer has been rewritten. Far below the tail grain
// keeps (ci.MaxStepOutput's three quarters), so the anchor is still in
// there after one elision, and far above one line, so it identifies a
// place rather than matching everywhere.
const anchorBytes = 512

// RunFailure is what `grain ci run -watch` exits with: nothing for a
// run that succeeded, and an error naming the conclusion for anything
// else, so a script can branch on the exit status rather than on the
// text. Deliberately only for -watch: a run that has been started and
// not waited for has no verdict to report yet.
func RunFailure(run ci.Run) error {
	if run.Conclusion == ci.ConclusionSuccess {
		return nil
	}
	if run.Error != "" {
		return fmt.Errorf("run %s: %s: %s", run.ID, run.Conclusion, run.Error)
	}
	return fmt.Errorf("run %s: %s", run.ID, run.Conclusion)
}

// OutputMode is which steps' output `show` prints.
type OutputMode int

const (
	// OutputFailed is the default: the steps that failed, which is
	// what somebody opening a red run came for, without the successful
	// half of a test suite scrolling past first.
	OutputFailed OutputMode = iota
	OutputAll
	OutputNone
)

func ParseOutputMode(s string) (OutputMode, error) {
	switch strings.TrimSpace(strings.ToLower(s)) {
	case "", "failed":
		return OutputFailed, nil
	case "all":
		return OutputAll, nil
	case "none":
		return OutputNone, nil
	default:
		return 0, fmt.Errorf("-output %q: one of failed, all or none", s)
	}
}

// Shows decides whose output is printed: the failing steps by default,
// every step under -output all, none under -output none.
//
// And the step that is *running*, which is the default's other half.
// grain CI publishes a step's output while it is still printing it
// (ci.LiveOutputInterval), so a `grain ci show` of a run in flight has a
// half-written log to offer -- and a step still going is, by definition,
// the only place anything is being written. Withholding it until the step
// ends would mean a terminal reader watching a twenty-minute job saw
// nothing at all, which is the very thing live output exists to fix; the
// pane beside this command already follows it (CIPage's StepRow).
func (m OutputMode) Shows(step ci.StepRun) bool {
	switch m {
	case OutputAll:
		return step.Output != ""
	case OutputFailed:
		return step.Output != "" &&
			(step.Conclusion == ci.ConclusionFailure || step.Conclusion == "")
	default:
		return false
	}
}

// JobName names a job the way its workflow does: the `name:` it was
// given, falling back to the id it is declared under.
func JobName(job ci.JobRun) string {
	if strings.TrimSpace(job.Name) != "" {
		return job.Name
	}
	return job.JobID
}
