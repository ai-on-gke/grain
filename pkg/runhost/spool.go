package runhost

// The spool: a run host's whole output, on a volume the daemon can read.
//
// One directory per run host:
//
//	command.json  what to run (Invocation), written by the daemon before
//	              the run host starts
//	env           the environment to add, mode 0600, written by the
//	              daemon and removed by the run host as soon as it has
//	              read it, so a credential the CLI is handed spends
//	              seconds on disk rather than the whole run
//	stdin         the CLI's stdin
//	stdout        append-only: the CLI's stream, byte for byte. The
//	              daemon reads it from a cursor and never rewrites it
//	stderr        append-only
//	exit.json     written once, by rename, when the CLI has exited
//	              (Exit). Its presence is what "finished" means
//
// The run host writes nothing else and never opens the store.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/procgroup"
)

const (
	spoolCommand = "command.json"
	spoolEnv     = "env"
	spoolStdin   = "stdin"
	spoolStdout  = "stdout"
	spoolStderr  = "stderr"
	spoolExit    = "exit.json"
)

// Invocation is command.json: one CLI invocation, as agent.Command
// describes it minus the environment, which goes in its own file.
type Invocation struct {
	Path string   `json:"path"`
	Args []string `json:"args"`
	Dir  string   `json:"dir,omitempty"`
}

// Exit is exit.json: how the CLI ended.
type Exit struct {
	Code       int       `json:"code"`
	Error      string    `json:"error,omitempty"`
	FinishedAt time.Time `json:"finished_at"`
}

// WriteSpool lays out dir for a run host that has not started yet.
func WriteSpool(dir string, inv Invocation, env []string, stdin string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(inv)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, spoolCommand), data, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, spoolEnv), []byte(strings.Join(env, "\x00")), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, spoolStdin), []byte(stdin), 0o600)
}

// ReadStdout returns what the spool's stdout holds from cursor on, and the
// cursor after it. It reads only whole bytes that are already there, so
// calling it again from the returned cursor neither repeats nor skips
// anything: the transcript of a run tailed by two daemons in turn has no
// hole and no duplicate as long as each resumes from the cursor the last
// one persisted.
func ReadStdout(dir string, cursor int64) ([]byte, int64, error) {
	f, err := os.Open(filepath.Join(dir, spoolStdout))
	if errors.Is(err, os.ErrNotExist) {
		return nil, cursor, nil
	}
	if err != nil {
		return nil, cursor, err
	}
	defer f.Close()
	if _, err := f.Seek(cursor, io.SeekStart); err != nil {
		return nil, cursor, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, cursor, err
	}
	return data, cursor + int64(len(data)), nil
}

// ReadExit returns the spool's exit record, or nil while the CLI is still
// running.
func ReadExit(dir string) (*Exit, error) {
	data, err := os.ReadFile(filepath.Join(dir, spoolExit))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var e Exit
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, fmt.Errorf("runhost: reading %s: %w", spoolExit, err)
	}
	return &e, nil
}

// ReadStderrTail returns at most limit bytes from the end of the spool's
// stderr.
func ReadStderrTail(dir string, limit int64) string {
	data, err := os.ReadFile(filepath.Join(dir, spoolStderr))
	if err != nil {
		return ""
	}
	if int64(len(data)) > limit {
		data = data[int64(len(data))-limit:]
	}
	return strings.TrimSpace(string(data))
}

// Exec is the run host's side: what "grain runhost exec" does inside the
// container. It runs the spooled invocation to completion or to deadline,
// appending its output to the spool, and records how it ended. Its own
// return is for the container's exit status; the spool is the record.
func Exec(ctx context.Context, dir string, deadline time.Time) error {
	data, err := os.ReadFile(filepath.Join(dir, spoolCommand))
	if err != nil {
		return writeExit(dir, Exit{Code: -1, Error: err.Error()})
	}
	var inv Invocation
	if err := json.Unmarshal(data, &inv); err != nil {
		return writeExit(dir, Exit{Code: -1, Error: err.Error()})
	}
	env, err := os.ReadFile(filepath.Join(dir, spoolEnv))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return writeExit(dir, Exit{Code: -1, Error: err.Error()})
	}
	// Read once, then gone: nothing that lists the spool later finds
	// the run's credentials in it.
	_ = os.Remove(filepath.Join(dir, spoolEnv))

	if !deadline.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	stdout, err := os.OpenFile(filepath.Join(dir, spoolStdout), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return writeExit(dir, Exit{Code: -1, Error: err.Error()})
	}
	defer stdout.Close()
	stderr, err := os.OpenFile(filepath.Join(dir, spoolStderr), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return writeExit(dir, Exit{Code: -1, Error: err.Error()})
	}
	defer stderr.Close()
	stdin, err := os.Open(filepath.Join(dir, spoolStdin))
	if err != nil {
		return writeExit(dir, Exit{Code: -1, Error: err.Error()})
	}
	defer stdin.Close()

	cmd := exec.CommandContext(ctx, inv.Path, inv.Args...)
	procgroup.Prepare(cmd)
	cmd.Dir = inv.Dir
	cmd.Env = os.Environ()
	if len(env) > 0 {
		cmd.Env = append(cmd.Env, strings.Split(string(env), "\x00")...)
	}
	cmd.Stdin = bufio.NewReader(stdin)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	runErr := cmd.Run()
	// ExitCode is -1 for a process that never started (nil ProcessState).
	e := Exit{Code: cmd.ProcessState.ExitCode()}
	if runErr != nil {
		e.Error = runErr.Error()
		if ctx.Err() != nil && !deadline.IsZero() && !time.Now().Before(deadline) {
			e.Error = fmt.Sprintf("run host deadline %s reached: %v", deadline.UTC().Format(time.RFC3339), runErr)
		}
	}
	if err := writeExit(dir, e); err != nil {
		return err
	}
	return runErr
}

// writeExit writes exit.json by rename, so a reader sees all of it or none.
func writeExit(dir string, e Exit) error {
	if e.FinishedAt.IsZero() {
		e.FinishedAt = time.Now().UTC()
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, spoolExit+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, spoolExit)); err != nil {
		return err
	}
	if e.Code != 0 && e.Error != "" {
		return errors.New(e.Error)
	}
	return nil
}
