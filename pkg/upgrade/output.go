package upgrade

// Running an upgrade's commands so that what they print is not lost
// (grain/task-37).
//
// Every command an upgrade runs used to be run through CombinedOutput:
// nothing was visible until it had finished, and what it had printed
// was then either discarded (on success) or pasted whole into the
// status file (on failure). A `make container-build` that takes twenty
// minutes therefore looked identical, from outside, to one that had
// hung -- and a build that failed on line three of four thousand handed
// the browser all four thousand.
//
// runLogged runs the same commands through a streaming writer instead.
// Each line goes to the daemon log as the command prints it, so
// journalctl follows a build live, and to the running step's Detail, so
// the Upgrade pane shows the most recent one. Only the tail goes into
// the error -- the end of the output is the part that says why a
// command failed, and it is the part that fits in a status pane.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// maxLoggedLines bounds how many of one command's output lines reach
// the daemon log. A BuildKit build reports progress continuously and
// can print tens of thousands of lines, which is more journal than an
// upgrade is worth; the tail is kept regardless of this cap and is what
// a failure reports, so the interesting end of a long build survives
// being cut off here.
const maxLoggedLines = 5000

// tailLines and maxTailBytes bound what an error carries: the last few
// lines of the command's output, since that is where a failure explains
// itself, and no more of them than a status pane can show.
const (
	tailLines    = 20
	maxTailBytes = 4000
)

// runLogged runs one of an upgrade's commands, streaming what it prints
// into the daemon log and into step's own Detail as it goes. The error
// it returns on failure names the command, its exit status and the tail
// of its output -- the same shape the CombinedOutput calls it replaced
// produced, minus the unbounded middle.
func runLogged(ctx context.Context, p *progress, step, dir string, argv []string) error {
	where := ""
	if dir != "" {
		where = " (in " + dir + ")"
	}
	p.logf("%s: running: %s%s", step, strings.Join(argv, " "), where)
	cmd := newCommand(ctx, dir, argv[0], argv[1:]...)
	out := &lineLogger{p: p, step: step}
	// One writer for both streams: os/exec gives the child a single
	// descriptor when Stdout and Stderr are the same value, so the
	// interleaving is the child's own, exactly as CombinedOutput had it.
	cmd.Stdout = out
	cmd.Stderr = out
	started := time.Now()
	err := cmd.Run()
	out.close()
	if err != nil {
		return fmt.Errorf("%s: %w%s", strings.Join(argv, " "), runError(ctx, time.Since(started), err), tailOf(out))
	}
	return nil
}

// runError names the deadline when one is what actually stopped the
// command -- the same distinction askError draws for a command run for
// its answer, and it matters more here, since the steps a deadline
// actually bounds (a cold build, an image pull, a restart) are all run
// through runLogged. newCommand kills the process group when its context
// ends, so what os/exec hands back on its own is "signal: killed", which
// says nothing about which of the several things that could produce it
// did.
//
// took is said out loud because askError's half of this pair says its
// bound and this half could not: "it ran out of time and was killed" is
// the whole of what three release runs had to go on
// (.grain/workflows/release.yml, grain/task-329), and the reader's next
// question -- ten seconds or forty-five minutes? -- was answerable only
// by finding which of this package's bounds wraps this particular
// command. It is how long *this command* ran, which is the bound itself
// where one was taken out for the command alone (healthCheckImage,
// rehearse) and less than it where the command shares the upgrade's own
// Timeout with everything before it (pullImage).
func runError(ctx context.Context, took time.Duration, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("it ran out of time after %s and was killed (%w)", shortDuration(took), err)
	}
	return err
}

// tailOf is the end of what a command printed, ready to append to an
// error -- and, like printed, says so rather than trailing a colon off
// the end of the message when it printed nothing at all. A command
// killed on its deadline is routinely one that printed nothing, which is
// worth reading as a fact about it rather than as a message that appears
// to have been cut off.
func tailOf(out *lineLogger) string {
	if tail := out.tail(); tail != "" {
		return ": " + tail
	}
	return " (it printed nothing)"
}

// runAsked runs one of an upgrade's commands for its *answer* rather
// than for its exit status alone: what it prints on stdout comes back to
// the caller, and everything else about it is treated exactly as
// runLogged treats a command it only needs to succeed.
//
// That last part is the whole reason this exists. An upgrade asks the
// build it is cutting over to two questions -- which
// schema it knows (schema.go) and which sandbox image it expects
// (image.go) -- and both used to be exec.Cmd.Output() calls whose
// stderr went nowhere. So a question that failed produced an error
// naming the command and "exit status 125", with the one line that said
// why (a registry that had gone away, a container runtime out of disk,
// a build refusing the subcommand) discarded by the call that ran it.
// Every other command an upgrade runs reports its output; there is no
// reason for the two that are asked something to be the exception, and
// an operator meeting `asking the build being upgraded to which schema
// it knows: exit status 1` has nothing to go on at all.
//
// bound is applied here rather than by the caller so that a command that
// answers nothing in time can be reported as that, and not as the
// "signal: killed" that cancelling it produces.
func runAsked(ctx context.Context, p *progress, step string, bound time.Duration, argv []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	p.logf("%s: asking: %s", step, strings.Join(argv, " "))
	cmd := newCommand(ctx, "", argv[0], argv[1:]...)
	var stdout bytes.Buffer
	// stdout is the answer and is kept whole; stderr is the command
	// talking about itself, and goes where a runLogged command's output
	// goes -- the daemon log, the running step's Detail, and the tail of
	// the error below.
	stderr := &lineLogger{p: p, step: step}
	cmd.Stdout = &stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	stderr.close()
	if err != nil {
		return stdout.String(), fmt.Errorf("%s: %w%s", strings.Join(argv, " "),
			askError(ctx, bound, err), printed(stderr, &stdout))
	}
	return stdout.String(), nil
}

// askError names the deadline when one is what actually stopped the
// command. newCommand kills the process group when its context ends, so
// the error os/exec hands back in that case is "signal: killed" -- true,
// and the least useful of the several things that could have produced
// it.
func askError(ctx context.Context, bound time.Duration, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("no answer within %s, so it was killed (%w)", bound, err)
	}
	return err
}

// The exit statuses docker keeps for its own failures rather than for
// the container's: 125 is `docker run` itself failing -- no such image,
// a daemon that would not answer, a runtime out of disk -- and 126 and
// 127 are a container that was created but whose command could not be
// invoked or was not there at all. An image answering a question never
// exits with one of these, so ranAndFailed does not let them be read as
// an answer.
const (
	dockerRunFailed          = 125
	dockerCommandNotRunnable = 126
	dockerCommandNotFound    = 127
)

// errCouldNotAsk marks a question that was never put -- the failures
// ranAndFailed rejects -- for a caller that carries on past one and
// still has to say which kind of failure it survived. A caller that
// stops instead (image.go's sandboxImage) needs no marker: the error it
// returns is the whole story.
var errCouldNotAsk = errors.New("this host could not run the question")

// ranAndFailed reports whether err, from runAsked or runLogged, is the
// command's own verdict -- it started, ran to the end and chose the
// status it exited with -- rather than this host failing to run it at
// all.
//
// It exists because one caller has to tell those apart before deciding
// what a failure *means*. image.go's sandboxImage asks a pulled image
// which sandbox container it expects, and a build older than that
// subcommand answers with a usage error: that is "there is nothing to
// pull", and failing a rollback over it would be wrong. A deadline that
// expired, a killed process, a docker that never got a container
// started -- those look identical from the exit status alone, and
// reading them as "no sandbox image" is how an upgrade cuts over having
// pulled no guest at all (grain/task-338).
//
// The three things that are not the command's own verdict:
//
//   - exec could not start it (no such binary, no permission, a working
//     directory that is not there), so there is no *exec.ExitError to
//     find at all -- as there is none for exec.ErrWaitDelay, a command
//     whose output pipes outlived it;
//   - it was killed by a signal, which for every command this package
//     runs means newCommand's process-group kill on a deadline, and
//     never a status the command chose;
//   - docker exited with one of its own statuses above, which says the
//     container is what did not happen.
func ranAndFailed(err error) bool {
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return false
	}
	if !exit.Exited() {
		return false
	}
	switch exit.ExitCode() {
	case dockerRunFailed, dockerCommandNotRunnable, dockerCommandNotFound:
		return false
	}
	return true
}

// printed is the tail of what a failed command said, ready to append to
// an error: stderr when it wrote any, and otherwise stdout, since a
// command that fails with its complaint on the wrong stream is still
// complaining. Empty -- rather than a dangling colon -- when it said
// nothing at all, which is itself worth being able to read.
func printed(stderr *lineLogger, stdout *bytes.Buffer) string {
	if tail := stderr.tail(); tail != "" {
		return ": " + tail
	}
	if tail := strings.TrimSpace(stdout.String()); tail != "" {
		if len(tail) > maxTailBytes {
			tail = "…" + tail[len(tail)-maxTailBytes:]
		}
		return " (nothing on stderr; it printed): " + tail
	}
	return " (it printed nothing)"
}

// lineLogger is the io.Writer runLogged hands a command: it splits what
// it is given into lines, logs them, shows the latest in the pane, and
// keeps the last few for the error.
type lineLogger struct {
	p    *progress
	step string

	mu      sync.Mutex
	partial []byte
	logged  int
	recent  []string
}

func (l *lineLogger) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.partial = append(l.partial, b...)
	for {
		i := bytes.IndexByte(l.partial, '\n')
		if i < 0 {
			return len(b), nil
		}
		line := string(l.partial[:i])
		l.partial = l.partial[i+1:]
		l.emitLocked(line)
	}
}

// close flushes a final line the command left without a newline -- the
// usual shape of a shell's own error message when it dies mid-line.
func (l *lineLogger) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.partial) > 0 {
		l.emitLocked(string(l.partial))
		l.partial = nil
	}
}

func (l *lineLogger) emitLocked(line string) {
	line = strings.TrimRight(line, "\r \t")
	if strings.TrimSpace(line) == "" {
		return
	}
	l.logged++
	switch {
	case l.logged < maxLoggedLines:
		l.p.logf("%s: %s", l.step, line)
	case l.logged == maxLoggedLines:
		l.p.logf("%s: %d lines in -- logging no more of this command's output; "+
			"its last lines are still reported if it fails", l.step, l.logged)
	}
	l.recent = append(l.recent, line)
	if len(l.recent) > tailLines {
		l.recent = l.recent[len(l.recent)-tailLines:]
	}
	l.p.note(line)
}

// tail is the end of what the command printed, for the error message.
func (l *lineLogger) tail() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	joined := strings.Join(l.recent, "\n")
	if len(joined) > maxTailBytes {
		joined = "…" + joined[len(joined)-maxTailBytes:]
	}
	return joined
}
