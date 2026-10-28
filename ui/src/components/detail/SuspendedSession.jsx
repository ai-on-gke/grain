import { useTimeZone } from "../../TimeZoneContext.jsx";
import { formatDateTime } from "../../time.js";
import { frameworkLabel } from "../../state.js";

// SuspendedSession is the agent conversation this task's last run left
// parked, and which its next run will be resumed into
// (model.SuspendedSession). It renders nothing for a task that has none,
// which is every task before its first run and every task whose agent
// framework keeps no session that outlives a run.
//
// It is here rather than on the timeline because it is a *state* of the
// task as it stands, not something that happened at a moment: what it
// answers is "if this is dispatched again, does it start from nothing or
// carry on?" -- which is the question somebody asks when a long task has
// just been cut off by its runtime cap, and which nothing on this page
// could answer before.
//
// The token is shown rather than merely alluded to because it is usable.
// It names a conversation in the CLI's own session store on the
// controller, so an operator debugging a task can open the very
// conversation grain will resume ("claude --resume <token>") and read
// what the run was actually thinking. It is not a credential and it is
// not the conversation's content -- see model.SuspendedSession -- so
// there is nothing here to withhold from someone already looking at the
// task.
//
// Read-only, deliberately. There is no "forget this session" button:
// grain replaces the record after every run of the task, a token the CLI
// no longer has costs the next dispatch one refused process start and
// nothing else (pkg/agent/claude's resume falls back to the prompt), and
// a button whose whole effect is to make the next run slower is not one
// worth offering.
export default function SuspendedSession({ t }) {
  const zone = useTimeZone();
  const session = t.suspendedSession;
  if (!session) return null;
  return (
    <fieldset>
      <legend>Suspended session</legend>
      <p className="hint">
        Left by {session.runId || "an earlier run"} on{" "}
        {formatDateTime(session.at, zone)}
        {session.framework ? <> ({frameworkLabel(session.framework)})</> : null}
        . Any further run of this task resumes this conversation instead of
        starting from nothing.
      </p>
      <code className="suspended-session-token">{session.token}</code>
    </fieldset>
  );
}
