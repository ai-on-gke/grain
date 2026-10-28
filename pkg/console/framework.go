package console

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
)

// Framework drives one run as an interactive session instead of as a
// headless one: it starts the wrapped framework's own CLI on a PTY
// (agent.TerminalFramework), registers it with the Manager so the UI can
// attach to it, and then waits -- for as long as the person at the other
// end wants it, which is the whole point of an interactive task.
//
// It is an agent.Framework itself, which is what keeps this out of
// orchestrator.RunDispatch: everything that happens around a run --
// the clone, the capabilities, the branch, the pull request the run's
// commits become -- happens because RunDispatch cannot tell this apart
// from any other framework, and should not.
type Framework struct {
	inner    agent.TerminalFramework
	sessions *Manager
	spec     Spec
	now      func() time.Time
}

// Wrap builds that decorator. spec.StartedAt is set by Run if the caller
// left it zero, so a caller that only knows which task this is need
// supply nothing else.
func Wrap(inner agent.TerminalFramework, sessions *Manager, spec Spec) *Framework {
	return &Framework{inner: inner, sessions: sessions, spec: spec}
}

// Run starts the session and blocks until it ends.
//
// It ends one of three ways, and each is an ordinary outcome rather than
// a failure: somebody closes the session from the UI (Manager.Close),
// the CLI exits on its own because they typed /exit, or the run's own
// context is cancelled -- the task being closed, or the daemon shutting
// down. The Result carries what the terminal showed, so the attempt's
// transcript in the task pane is the session that produced the branch.
func (f *Framework) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	if f.sessions == nil {
		return nil, errors.New("console: this deployment holds no session manager")
	}
	cmd, err := f.inner.TerminalCommand(ctx, cfg)
	if err != nil {
		return nil, err
	}
	spec := f.spec
	if spec.TaskID == "" {
		spec.TaskID = cfg.TaskID
	}
	if spec.Repo == "" {
		spec.Repo = cfg.Repo
	}
	if spec.StartedAt.IsZero() {
		spec.StartedAt = f.clock()
	}
	// The same line a headless run's CLI gets (agent.LocalHost.Run), for
	// the same reason: a session that will not start, or that started
	// with the wrong model, is diagnosed from the invocation rather than
	// from a terminal nobody was attached to. Credential material is
	// taken out of it by agent.CommandLine.
	log.Printf("console: task %s: running: %s", spec.TaskID, agent.CommandLine(agent.Command{
		Path: cmd.Path,
		Args: cmd.Args,
		Env:  cmd.Env,
		Dir:  cmd.Dir,
	}))
	session, err := f.sessions.Start(spec, Command{
		Path:    cmd.Path,
		Args:    cmd.Args,
		Env:     cmd.Env,
		Dir:     cmd.Dir,
		Cleanup: cmd.Cleanup,
	})
	if err != nil {
		if cmd.Cleanup != nil {
			cmd.Cleanup()
		}
		return nil, err
	}
	// However this ends, the session stops being one the UI offers to
	// attach to: a closed session is already gone from the Manager, and
	// this is what takes a session whose CLI exited on its own -- or
	// whose run was cancelled underneath it -- off the list too.
	defer func() {
		session.Close()
		f.sessions.drop(spec.TaskID, session)
	}()

	waitErr := session.Wait(ctx)
	result := &agent.Result{
		FinalText:  f.ending(session, waitErr),
		Transcript: session.Transcript(),
	}
	// A cancelled context is the run being ended from outside it -- the
	// task closed, the daemon stopping -- and RunDispatch has its own
	// account of both. Everything else that could have gone wrong here is
	// the CLI failing to run at all, which is this run's failure and is
	// reported as one, with whatever the terminal managed to show still
	// travelling back alongside it (agent.Framework's own contract).
	if waitErr != nil {
		return result, fmt.Errorf("console: the interactive session for %s ended: %w", spec.TaskID, waitErr)
	}
	return result, nil
}

// ending is the sentence the task's own conversation gets when the
// session is over -- an interactive run's equivalent of the closing
// answer an unattended one writes for itself, since there is nobody here
// to write one and a blank comment would say less than the fact of the
// session does.
func (f *Framework) ending(s *Session, waitErr error) string {
	lasted := s.Duration(f.clock()).Round(time.Second)
	switch {
	case waitErr != nil:
		return fmt.Sprintf("This interactive session ran for %s and was cut short: %s.", lasted, waitErr)
	case s.ClosedByHand():
		return fmt.Sprintf("This interactive session ran for %s and was closed from the UI.", lasted)
	default:
		return fmt.Sprintf("This interactive session ran for %s; its agent CLI exited.", lasted)
	}
}

func (f *Framework) clock() time.Time {
	if f.now != nil {
		return f.now()
	}
	return time.Now()
}

// CanOpenPullRequest passes the wrapped framework's own answer through,
// so that a session's prompt -- and the tool roster behind it -- says the
// same thing about open_pull_request that the same framework says for an
// unattended run (orchestrator.frameworkOpensPullRequests).
func (f *Framework) CanOpenPullRequest() bool {
	if pr, ok := f.inner.(agent.PullRequestFramework); ok {
		return pr.CanOpenPullRequest()
	}
	return false
}
