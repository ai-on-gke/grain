import { Alert, Box, Button, Link } from "@mui/material";
import api from "../../api.js";
import { pullRequestUrl } from "../../state.js";
import {
  faultDetail,
  faultFixes,
  faultSource,
  faultSourceLabel,
  faultSummary,
  fixWhere,
} from "../../ciFaults.js";

// What grain's own CI last said about the branch this task pushed --
// t.ci, which is nil for almost every task (one that does not hold the
// grain-ci capability, or whose branch has not been through CI), and
// which renders nothing for those.
//
// The verdict is what this says, and it says it on its own: a grain CI
// run is kept only for a while (model.CIStatus) -- weeks now that it is
// stored rather than held in the daemon's memory (grain/task-303), and
// still not forever -- so a pane that could only point at "run 3f2a1c"
// would eventually be a dead reference. The conclusion and grain's
// one-line account of it are copied onto the task and stay true
// however long ago the run was.
//
// Beside it, and only while there is a run id to name, is the way to
// that run's own step output (grain/task-297): the verdict says a branch
// failed, and *why* is a job, a step and what that step printed, which
// exists nowhere but the CI pane. Reaching it used to mean closing the
// task, opening System, picking CI and finding the run in a list
// belonging to every repo at once. The run this points at may have been
// pruned by now -- far less often than when a restart took every run
// with it, and still possible -- which is not a reason to withhold the
// link: the CI pane says so in the daemon's own words and leaves the
// reader on the list (CIPage.jsx), and a link that is sometimes stale
// beats no way through at all.
//
// Said here as an alert rather than as a check row on the pull request,
// because that is what it is: a report on this task. What it is *not*,
// since grain/task-302, is a report with no consequence. A grain CI
// verdict now holds this task's merge exactly as a GitHub check does --
// a failure against the commit a pull request would land makes it
// failing and earns it the queue's one automatic repair, and a run still
// going holds it pending (orchestrator.withGrainCI) -- and this pane
// used to close by saying the opposite in as many words. That sentence
// was the whole reason a person could read the verdict here, read
// "waiting on checks" beside it, and have nothing tell them the first
// was why the second.
//
// So the consequence is what each case now says, and the severity
// follows it: failure is an error because it really does stop the merge,
// while a conclusion that holds nothing -- "cancelled", "skipped" --
// stays a warning and says so, since only a failure keeps a pull request
// out of the queue (grainCIVerdictOf's "only failure is failure").
//
// It is said conditionally on there being a pull request, because that
// is what there is to hold: a task whose branch has not been opened as
// one yet gets the verdict without the consequence, which is the whole
// truth for it -- grain CI is not even started until a pull request is
// open (orchestrator.reconcileCI).
//
// What this pane cannot check is the merge queue's other rule, that only
// a verdict about the commit being judged counts: nothing on the wire
// says where the branch's head is now. So the commit the run was about
// is named, and the failure case says in a clause that a verdict about
// an overtaken commit holds nothing -- rather than this asserting a hold
// that may already have lapsed.
//
// A run grain could not do at all ("errored", grain/task-361) is the one
// case that breaks the "failure is the only conclusion with a
// consequence" rule above, and it is spelled out separately for that
// reason. It holds the merge the way an unfinished run does
// (withGrainCI reads it as pending) without being a verdict on this
// branch, so it stays a warning rather than an error: telling somebody
// their branch is red when this deployment could not build a sandbox is
// exactly the confusion that conclusion exists to end.
function combinedTaskFaults(t) {
  const seen = new Set();
  const out = [];
  for (const f of [...(t?.ci?.faults || []), ...(t?.ciFaults || [])]) {
    if (!f) continue;
    const key = `${faultSource(f)}:${f.digest}`;
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(f);
  }
  return out;
}

export default function GrainCIStatus({ t, onOpenCIRun, act }) {
  const ci = t.ci;
  const faults = combinedTaskFaults(t);
  if (!ci) {
    if (faults.length === 0) return null;
    return (
      <Alert severity="warning" sx={{ mb: 2 }}>
        <SharedFaultNote faults={faults} branch={t.branch} />
      </Alert>
    );
  }
  const showRun = ci.runId && onOpenCIRun && (
    <>
      {" "}
      <Link
        component="button"
        type="button"
        underline="hover"
        onClick={() => onOpenCIRun(ci.runId)}
      >
        Show the run
      </Link>
    </>
  );
  // The commit the verdict is about, which is what makes it a verdict
  // about anything: grain replaces this record whenever the branch moves
  // (model.CIStatus), so naming the commit is what lets a reader tell a
  // verdict about the head from one the head has left behind.
  const against = ci.commit ? <> against {ci.commit.slice(0, 12)}</> : null;
  // Whether there is a merge for any of this to hold. Nothing else on
  // the wire says where the pull request stands, and nothing needs to:
  // what holds it is this verdict, and what it does to it is the same
  // whether the queue has got to it yet or not.
  const held = Boolean(t.pullRequest);
  if (!ci.conclusion) {
    return (
      <Alert severity="info" sx={{ mb: 2 }}>
        <strong>grain CI is running</strong> against this task&apos;s branch
        {ci.commit ? <> ({ci.commit.slice(0, 12)})</> : null}.{" "}
        {held ? (
          <>
            Its pull request is held pending until this reports: grain&apos;s
            merge queue waits for grain CI as it waits for a GitHub check, so a
            pull request with every check green still does not merge while this
            is going.
          </>
        ) : (
          <>
            Nothing is held by it yet -- there is no pull request open for this
            task.
          </>
        )}
        {showRun}
      </Alert>
    );
  }
  const passed = ci.conclusion === "success";
  const failed = ci.conclusion === "failure";
  const errored = ci.conclusion === "errored";
  return (
    <Alert
      severity={passed ? "success" : failed && held ? "error" : "warning"}
      sx={{ mb: 2 }}
    >
      <strong>
        grain CI: {ci.conclusion}
        {against}.
      </strong>
      {ci.detail && <> {ci.detail}.</>} This is grain&apos;s own CI
      (.grain/workflows), not a GitHub check: it is reported here and in this
      task&apos;s conversation rather than to GitHub.{" "}
      {errored && held && (
        <>
          grain could not run it, so nothing here is a verdict on this branch:
          the merge waits as it does for a check that has not finished, and it
          is this deployment that needs looking at rather than the code.
        </>
      )}
      {errored && !held && (
        <>
          grain could not run it, so nothing here is a verdict on this branch --
          it is this deployment that needs looking at. Nothing is held by it
          yet, either: there is no pull request open for this task.
        </>
      )}
      {failed && held && (
        <>
          It still keeps this pull request out of the merge queue exactly as a
          red check would -- the queue counts the pull request as failing and
          spends its one automatic repair on it -- for as long as this is the
          verdict on the branch&apos;s head commit. A push that makes grain CI
          pass is what releases it.
        </>
      )}
      {failed && !held && (
        <>
          Nothing is held by it yet -- there is no pull request open for this
          task -- but a red run keeps one out of the merge queue once there is.
        </>
      )}
      {!failed && !passed && !errored && (
        <>
          That holds nothing: only a failure keeps a pull request out of the
          merge queue, so this branch is judged on GitHub&apos;s own checks
          until a later grain CI run against it fails.
        </>
      )}
      {showRun}
      <SharedFaultNote faults={faults} branch={t.branch} />
      <RerunCI t={t} act={act} />
    </Alert>
  );
}

// RERUNNABLE is the verdicts a person may ask grain to run again: the
// ones that are over without a pass.
const RERUNNABLE = new Set(["errored", "failure", "cancelled"]);

// RerunCI is "Re-run CI" on a verdict that did not pass: POST
// /api/ci/runs with this task's id, which starts the complete suite on
// its branch and records it as the task's grain CI verdict
// (ui.Client.StartTaskCIRunOpt) -- the same run wait_for_checks starts.
//
// It exists for the verdict grain has stopped answering by itself. An
// errored or system-cancelled run is retried a bounded number of times
// (orchestrator.ciReplacementFor) and then left for a person, and until
// this button the person's only moves were a push or a forced merge
// (grain/task-480). "Re-run failed jobs" is retrigger_ci's default: an
// uncached recheck of only the jobs that did not pass, for a red run
// somebody suspects of being a flake.
function RerunCI({ t, act }) {
  const ci = t.ci;
  if (!act || !ci || !RERUNNABLE.has(ci.conclusion)) return null;
  const start = (body) =>
    act(
      () =>
        api("/api/ci/runs", {
          method: "POST",
          body: JSON.stringify({ taskId: t.id, ...body }),
        }),
      t.id,
    );
  return (
    <Box sx={{ mt: 1, display: "flex", gap: 1, flexWrap: "wrap" }}>
      <Button
        size="small"
        variant="outlined"
        title="Run grain CI's complete suite on this task's branch again and record it as this task's verdict."
        onClick={() => start({})}
      >
        Re-run CI
      </Button>
      {ci.conclusion !== "cancelled" && (
        <Button
          size="small"
          variant="outlined"
          title="Run only the jobs that did not pass again, bypassing the cache."
          onClick={() => start({ recheck: true, recheckFailed: true })}
        >
          Re-run failed jobs
        </Button>
      )}
    </Box>
  );
}

// SharedFaultNote is the sentence inside that alert that says the red is
// not this branch's own: the same fault grain has found on other
// branches right now, and the open pull request that fixes it where
// there is one (ciFaults.js, pkg/ci/sharedfault.go).
//
// It is inside the verdict rather than beside it because of what it is
// for. This alert is where somebody decides to spend a repair run on a
// branch, and the decision that cost an afternoon -- four pull requests
// repaired one at a time against a cause none of them owned -- was taken
// in front of exactly this text with nothing under it. It says nothing
// at all unless grain found the same failure on another branch, which is
// every ordinary red build.
function SharedFaultNote({ faults = [], branch }) {
  if (faults.length === 0) return null;
  return (
    <>
      {faults.map((fault) => {
        const summary = faultSummary(fault, branch);
        if (!summary) return null;
        const src = faultSource(fault);
        return (
          <p key={`${src}-${fault.digest}`} className="ci-fault-note">
            <span
              className="ci-fault-source"
              data-testid={`ci-fault-source-${src}`}
            >
              [{faultSourceLabel(fault)}]
            </span>{" "}
            <strong title={faultDetail(fault, branch)}>{summary}</strong>{" "}
            Repairing this branch will not fix it.
            {faultFixes(fault).map((fix) => (
              <span key={fix.pullRequest}>
                {" "}
                <Link
                  href={pullRequestUrl(fix.pullRequest)}
                  target="_blank"
                  rel="noopener noreferrer"
                  underline="hover"
                  title={fix.title ? `${fix.task}: ${fix.title}` : undefined}
                >
                  {fix.pullRequest}
                </Link>{" "}
                {fixWhere(fix)}.
              </span>
            ))}
          </p>
        );
      })}
    </>
  );
}
