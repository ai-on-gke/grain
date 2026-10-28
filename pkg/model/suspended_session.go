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

package model

import "time"

// SuspendedSession is the agent conversation a task's last run left
// parked, and which its next run picks back up: which framework's CLI
// holds it, the token that reopens it, and when and by which run it was
// left there.
//
// # What is suspended, and what is not
//
// A run does not usually end because the work is finished. It ends
// because the wall-clock cap cut it off, because the account met its
// usage limit, because the provider had no capacity, because the agent
// asked a person something, or because it simply failed -- and in every
// one of those the task comes back round and is dispatched again. What
// the second dispatch used to get was a fresh sandbox and a blank
// conversation: the repository read for a second time, the same
// architecture worked out again from the same files, the same dead ends
// walked down. The sandbox has to be new -- it is built per run -- but
// the conversation does not, and on a framework whose session store
// outlives the process that wrote it, nothing but the absence of this
// record was stopping the next run from continuing the last one's
// thought.
//
// So this is the *state*: a task either has a suspended session or it
// does not. Present, and the next dispatch resumes it
// (agent.RunConfig.ResumeSession) and the token is shown in the task's
// own pane, where a person debugging a task can reopen the very
// conversation grain will (`claude --resume <token>`). Absent -- every
// first dispatch, and every task on a framework that cannot do this --
// and a run starts exactly as runs always have.
//
// # Why it is not every framework
//
// Which framework can fill it in is a question about where that CLI
// keeps its conversations. claude's is the controller's own ~/.claude,
// keyed by HOME and working directory, both of which are the same on
// every dispatch this deployment makes, so claude has been able to do
// this since the day the state existed. codex and antigravity keep
// theirs in the home grain builds for a run, which used to be a temp
// directory deleted the moment that run returned -- so by the time a
// second dispatch could ask for the thread there was no thread. A
// deployment can now give those two a per-task home that outlives the
// run instead (pkg/agent/sessionstore), which is what lets them answer;
// one that has not still gets a temp home per run, and those runs report
// no session exactly as they did.
//
// So this stays a state a task may or may not have, and the seam stays
// written so that a framework which cannot answer simply never populates
// agent.Result.ResumableSession -- see that field, and this package's
// own Framework field below for why what wrote a token is recorded
// beside it.
//
// # It is a pointer into a store, not a value
//
// Token is an id in a CLI's session store on the controller's disk. It
// carries no credential and no content, which is what makes it safe to
// show in the UI and to dump into the state repository beside the rest
// of a task's observations. It is also not a promise: a token recorded
// weeks ago may name a session that is no longer there, whether because
// the CLI pruned its own store (claude's ~/.claude, which nothing in
// grain owns) or because grain pruned the directory it keeps for the
// other two, which it bounds by age, count and size on purpose. Every
// reader therefore has to treat it as a hint rather than a guarantee --
// each framework's resume falls back to running the task from its prompt
// when the CLI opens no session for it, which is the whole of what a
// stale token costs.
type SuspendedSession struct {
	// Framework is the agent framework whose CLI holds this session --
	// Config.AgentFramework's own vocabulary ("claude", "codex",
	// "antigravity"). Recorded because a token only means anything to
	// the CLI that wrote it: a
	// task whose framework has since been changed (Task.AgentFramework,
	// or the deployment default moving underneath it) must not have a
	// claude session id handed to codex, which would spend an exec
	// finding out. orchestrator.RunDispatch compares this against the
	// framework the run is about to use and ignores a session that does
	// not match.
	Framework string
	// Token is what that CLI takes to reopen the conversation -- a
	// claude session_id passed back as `--resume <token>`, a codex
	// thread id as `codex exec resume <token>`, an agy conversation id
	// as `--conversation <token>`. Never empty
	// in a stored record: a session with no token is the absence of a
	// session, which is recorded by there being no row rather than by an
	// empty one.
	Token string
	// RunID is the run that left the session behind, so a person reading
	// the pane can find the attempt this conversation belongs to and
	// what it was doing when it stopped.
	RunID string
	// At is when that run ended, which is the one thing that says how
	// much a resume is likely to be worth: a session from an hour ago
	// almost certainly still exists and is about work still in progress,
	// and one from a month ago probably names a conversation the CLI has
	// pruned.
	At time.Time
}

// ResumeTokenFor is the session token a run about to be dispatched on
// framework should be resumed into, and "" when there is none to resume.
//
// The framework check is the whole of the logic and is not a formality:
// a token is an id in one CLI's store, and handing claude's to codex
// buys a refused exec and a fallback, on every dispatch, for as long as
// the mismatch lasts. Everything else about whether a resume is a good
// idea is decided where the session is recorded (orchestrator's own
// suspend path), so that a reader here has one question to answer.
func ResumeTokenFor(obs *Observation, framework string) string {
	if obs == nil || obs.SuspendedSession == nil {
		return ""
	}
	s := obs.SuspendedSession
	if s.Token == "" || s.Framework != framework {
		return ""
	}
	return s.Token
}
