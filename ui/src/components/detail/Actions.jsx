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

import { useState } from "react";
import {
  Alert,
  Button,
  Checkbox,
  FormControlLabel,
  Stack,
  TextField,
} from "@mui/material";
import api from "../../api.js";
import { useTimeZone } from "../../TimeZoneContext.jsx";
import { formatDateTime, zoneAbbreviation, zoneOffset } from "../../time.js";
import { dayStart } from "../../taskFilters.js";
import {
  closablePullRequest,
  isTaskPaused,
  pausedDescription,
  taskPause,
} from "../../state.js";
import { heldForRunNow, pausedUntil, pauseSubject } from "../../alerts.js";
import { forceMergeOffered } from "../../taskWrites.js";
import TaskLink from "./TaskLink.jsx";
import { ReviewActions } from "../ClReview.jsx";
import ClSubmitActions from "../ClSubmit.jsx";

// closePullRequest is deliberately local state that dies with the
// overlay, and starts false every time it is mounted: it is a choice
// about this close, made in the moment (ui.CloseOptions' own doc
// comment), and the one thing it must never become is a preference that
// outlives the task it was ticked on. Closing the panel and reopening it
// unticks it, which is exactly right.
// earliestWakeDate is the first day a deferral can name -- tomorrow,
// in the zone the day is read in -- as the YYYY-MM-DD a date input
// wants.
//
// Tomorrow rather than today because the field means the *start* of the
// day it names, so today's is always an instant that has passed, and the
// server refuses one of those rather than deferring a task for a single
// cycle (ui.DeferOptions.Until). Somebody who means "later today" is
// after the ordinary Defer and an Undefer when they are ready.
//
// The zone is the deployment's, the one the "Picks itself back up on"
// note below prints the wake time in; empty is this browser's own.
function earliestWakeDate(zone, now = new Date()) {
  let day;
  if (zone) {
    // The wall clock in that zone, read through UTC's getters.
    const wall = new Date(now.getTime() + zoneOffset(now.getTime(), zone));
    day = new Date(
      Date.UTC(
        wall.getUTCFullYear(),
        wall.getUTCMonth(),
        wall.getUTCDate() + 1,
      ),
    );
    return day.toISOString().slice(0, 10);
  }
  day = new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1);
  return [
    day.getFullYear(),
    String(day.getMonth() + 1).padStart(2, "0"),
    String(day.getDate()).padStart(2, "0"),
  ].join("-");
}

// sentenceCase is s with its first letter capitalized, for a phrase that
// opens a sentence.
function sentenceCase(s) {
  return s ? s.charAt(0).toUpperCase() + s.slice(1) : s;
}

export default function Actions({ t, config, act, onOpenTask }) {
  const zone = useTimeZone();
  const closable = closablePullRequest(t);
  const frameworkPaused = taskPause(t, config);
  const [closePullRequest, setClosePullRequest] = useState(false);
  // The wake date beside the Defer button, local to this overlay for
  // closePullRequest's own reason: it is a choice about this deferral,
  // and a date left in a field is not a preference. Empty is the
  // deferral grain has always made -- aside until somebody picks it back
  // up (ui.DeferOptions.Until).
  const [wakeDate, setWakeDate] = useState("");
  // A date names a day, so the instant sent is the start of it -- in
  // the deployment's zone, the one the note below prints the wake time
  // in. Read in this browser's zone instead, a day picked in Tokyo came
  // back as "Picks itself back up on 12/30/2026, 3:00:00 PM" for a pick
  // of the 31st. Tomorrow is the earliest one offered, because a wake
  // time already past is refused by the server and today's midnight
  // always is.
  const wakeAt = wakeDate ? dayStart(wakeDate, zone) : null;
  const defer = () =>
    act(async () => {
      await api(`/api/tasks/${t.id}/defer`, {
        method: "POST",
        body: JSON.stringify({
          deferredUntil: wakeAt === null ? "" : new Date(wakeAt).toISOString(),
        }),
      });
      // The date belonged to this deferral, not to the pane: the pane
      // stays mounted through Undefer, and a field still holding it
      // made the next "plain" Defer quietly wake on the old date.
      setWakeDate("");
    }, t.id);
  // The operator's override of the merge queue (ui.Client.ForceMerge,
  // orchestrator's mergepredicate.go). One prompt rather than a confirm
  // and then a form, the way CIRunOverlay's cancel does it: dismissing
  // it is still "no, leave the queue alone" (prompt answers null), and
  // what is typed instead is the reason.
  //
  // Unlike that cancel, the reason is not optional -- the server refuses
  // an empty one and says so, which is the refusal a person should see
  // rather than one this file invents. It is the whole record of why a
  // change landed without the checks the queue asks for, and it is read
  // afterwards by people and by agents both.
  //
  // The commit this page is showing goes with it, because the override is
  // for that commit and no other (bwsalmon/grain#2399): the server
  // refuses it if the branch is already somewhere else, and the queue
  // withdraws it if anything is pushed before the merge. With no CI
  // commit to show, the server takes the pull request's head as it is.
  const forceMerge = () => {
    const head = t.ci?.commit || undefined;
    const reason = prompt(
      "Force this merge? grain merges the pull request itself, whatever its checks say, " +
        "and records that you did -- on the task, on the pull request, and in the count " +
        "that says how often this is done.\n\n" +
        (t.state === "running"
          ? "This also stops the agent run in flight and cancels its CI jobs still running, " +
            (t.pullRequest ? "" : "opens the pull request for its branch, ") +
            "and merges the reviewed commit.\n\n"
          : "") +
        (head
          ? `The override is for ${head.slice(0, 12)} only: if anything is pushed to the ` +
            "branch first, grain withdraws it and you will have to force again.\n\n"
          : "") +
        "Say why. It is required, and it is what whoever reads this afterwards has to go on.",
      "",
    );
    if (reason === null) return undefined;
    return act(
      () =>
        api(`/api/tasks/${t.id}/force-merge`, {
          method: "POST",
          body: JSON.stringify({ reason: reason.trim(), head }),
        }),
      t.id,
    );
  };
  // The flag is sent on every close, true or false, so that what was
  // asked for is on the wire either way -- and false whenever there is
  // no open pull request to act on, whatever the box was left at before
  // one merged underneath it.
  const close = () =>
    act(
      () =>
        api(`/api/tasks/${t.id}/close`, {
          method: "POST",
          body: JSON.stringify({
            close_pull_request: Boolean(closable) && closePullRequest,
          }),
        }),
      t.id,
    );
  return (
    <Stack className="actions" spacing={1}>
      {/* Offered on an unapproved deferred task as well as a proposed
          one: a task filed unapproved with a deferredUntil wake time
          reads "deferred", and approving it in place lets it wake
          directly onto the queue at that date without Undefer clearing
          deferredUntil first. */}
      {(t.state === "proposed" || (t.state === "deferred" && !t.approved)) && (
        <Button
          variant="contained"
          onClick={() =>
            act(
              () => api(`/api/tasks/${t.id}/approve`, { method: "POST" }),
              t.id,
            )
          }
        >
          Approve
        </Button>
      )}
      {/* Approve's own undo, offered on the states it applies to: a
          queued task, or an approved deferred one, has been approved and
          has not started, so clearing that approval puts it back among
          the proposals (or wakes it back into them) rather than closing
          it (Client.WithdrawApproval, which refuses the states this
          button is never shown for). */}
      {(t.state === "queued" || (t.state === "deferred" && t.approved)) && (
        <Button
          variant="outlined"
          onClick={() =>
            act(
              () =>
                api(`/api/tasks/${t.id}/withdraw-approval`, { method: "POST" }),
              t.id,
            )
          }
        >
          Withdraw approval
        </Button>
      )}
      {/* Putting a task aside, and picking it back up: offered on
          exactly the states ui.Client.Defer accepts, which are the two a
          task can be in before it has ever run. It is neither closing
          (this task is still meant to happen) nor withdrawing approval
          (the approval survives, so a task deferred off the queue comes
          back onto it) -- so it is its own button rather than a variant
          of either, sitting between them here the way it sits between
          them in the life of a task. */}
      {(t.state === "proposed" || t.state === "queued") && (
        <>
          {/* The optional half: a date this task picks itself back up on
              (model.Task.DeferredUntil). Left empty it defers exactly as
              it always has, which is why it is a field beside the button
              rather than a second button -- "put this aside" and "put
              this aside until the 14th" are one decision with a date on
              it, and the date is the thing a task body could never
              hold. */}
          <TextField
            type="date"
            size="small"
            label="Until (optional)"
            value={wakeDate}
            onChange={(e) => setWakeDate(e.target.value)}
            slotProps={{
              inputLabel: { shrink: true },
              htmlInput: { min: earliestWakeDate(zone) },
            }}
            helperText={`Picks itself back up at the start of this day${
              zone ? `, ${zoneAbbreviation(zone) || zone}` : ""
            }. Leave it empty to keep it aside until you say otherwise.`}
          />
          <Button
            variant="outlined"
            title="Put this task aside for later. It stops waiting to run and drops out of the lists until you ask for it -- or until the date above, if you set one -- and nothing about it is lost."
            onClick={defer}
          >
            Defer
          </Button>
        </>
      )}
      {t.state === "deferred" && (
        <>
          {/* What the deferral says about itself, which is the only
              place a reader can see it: a task aside until a date looks
              exactly like one aside indefinitely everywhere else. */}
          {t.deferredUntil && (
            <p className="hint">
              Picks itself back up on {formatDateTime(t.deferredUntil, zone)}
              {t.approved
                ? ", onto the queue."
                : ", back into the proposals for someone to approve."}
            </p>
          )}
          <Button
            variant="contained"
            title="Pick this task back up now. It returns to the queue, or to the proposals, exactly as it was left, and forgets any date it was going to wake on."
            onClick={() =>
              act(
                () => api(`/api/tasks/${t.id}/undefer`, { method: "POST" }),
                t.id,
              )
            }
          >
            Undefer
          </Button>
        </>
      )}
      {/* An operator's pause (ui.Client.Pause): offered in every open
          state. Pausing releases everything the task holds -- its run,
          its sandbox, its CI, its place in the merge queue -- and keeps
          its standing, so Resume puts it back exactly as it was. A
          running task is given a grace period to push and write its
          handover; "Pause now" skips it. */}
      {t.state !== "closed" && !isTaskPaused(t) && (
        <>
          <Button
            variant="outlined"
            data-testid="pause-task"
            title="Pause this task. It stops being dispatched and lets go of its sandbox, CI and merge-queue slot, and keeps its place. A running task is first told to push its work and write its handover."
            onClick={() =>
              act(
                () => api(`/api/tasks/${t.id}/pause`, { method: "POST" }),
                t.id,
              )
            }
          >
            Pause
          </Button>
          {t.state === "running" && (
            <Button
              variant="outlined"
              color="warning"
              title="Pause this task and stop its run at once, without the grace period. Whatever the run has not pushed is lost."
              onClick={() =>
                act(
                  () =>
                    api(`/api/tasks/${t.id}/pause`, {
                      method: "POST",
                      body: JSON.stringify({ immediate: true }),
                    }),
                  t.id,
                )
              }
            >
              Pause now
            </Button>
          )}
        </>
      )}
      {isTaskPaused(t) && (
        <>
          <p className="hint" data-testid="paused-note">
            {pausedDescription(t)}.
          </p>
          <Button
            variant="contained"
            data-testid="resume-task"
            title="Resume this task: it returns to exactly the state and place it had when it was paused."
            onClick={() =>
              act(
                () => api(`/api/tasks/${t.id}/resume`, { method: "POST" }),
                t.id,
              )
            }
          >
            Resume
          </Button>
        </>
      )}
      {/* Once a task's run has produced a pull request, submitting is
          what puts it on the merge queue for automatic conflict
          resolution and merging. Already-submitted tasks (autoMerge
          already true) have nothing left for this button to do. */}
      {/* Why this change is not merging itself, beside the button that
          overrides it: a security hold (model.LinkSecurityHold) waits on
          the review grain filed, and once the hold has ended -- on that
          review's findings, or because the paths it touches are ones only
          a person may let through -- the review is still what to read
          before pressing Submit (orchestrator's securityhold.go). */}
      {t.pullRequest &&
        !t.autoMerge &&
        t.state !== "closed" &&
        (t.securityHold || t.securityReviewTask) && (
          <Alert
            severity="warning"
            sx={{ fontSize: "0.8rem" }}
            data-testid="security-hold"
          >
            {t.securityHold
              ? `Security hold (${t.securityHold}): this change touches grain's trust boundary, so it merges automatically only once its security review finishes without findings. Submit merges it without waiting.`
              : "This change touches grain's trust boundary and will not merge automatically: read its security review, then Submit."}
            {t.securityReviewTask && (
              <>
                {" "}
                <TaskLink id={t.securityReviewTask} onOpenTask={onOpenTask}>
                  Security review: task {t.securityReviewTask}
                </TaskLink>
              </>
            )}
          </Alert>
        )}
      {/* A repository's own gating triggers (pathtriggerhold.go) and the
          named approvers a sensitive path, or grain's guard over the
          repository's triggers, waits for (approvals.go): each rule, where
          it came from, the reviews it filed, and who may release it. A
          Submit from anybody else is refused by the server. */}
      {t.pullRequest &&
        !t.autoMerge &&
        t.state !== "closed" &&
        (t.pathTriggerHold ||
          t.pathTriggerTasks?.length > 0 ||
          t.approvals?.length > 0) && (
          <Alert
            severity="warning"
            sx={{ fontSize: "0.8rem" }}
            data-testid="sensitive-hold"
          >
            {t.pathTriggerHold
              ? `Held by this repository's gating triggers (human: ${t.pathTriggerHold}).`
              : "This change touches paths that need a named approver."}
            {t.pathTriggerTasks?.map((entry) => {
              const [trigger, id] = entry.split("=");
              return (
                <div key={entry}>
                  {`.grain/triggers/${trigger}.yml: `}
                  <TaskLink id={id} onOpenTask={onOpenTask}>
                    review task {id}
                  </TaskLink>
                </div>
              );
            })}
            {t.approvals?.map((a) => (
              <div key={a.rule} data-testid="approval-rule">
                {`${a.rule} (${a.source}): `}
                {a.approvedBy
                  ? `approved by ${a.approvedBy}`
                  : `waiting for ${a.approvers.join(" or ")}`}
              </div>
            ))}
            {t.repoRules && <div>{`Decided under ${t.repoRules}`}</div>}
          </Alert>
        )}
      {t.pullRequest && t.baseRepairBypass && t.state !== "closed" && (
        <Alert
          severity="info"
          sx={{ fontSize: "0.8rem" }}
          data-testid="base-repair-bypass"
        >
          Human gate bypassed under{" "}
          <code>base_repair_bypasses_human_gates</code> while{" "}
          <code>{t.redBaseBranch || t.base || "the base branch"}</code> is red:
          auto-merge remains enabled so this repair can land on green CI.
        </Alert>
      )}
      {/* A change held on its review's open questions
          (orchestrator's reviewquestions.go) is a question to its owner,
          not a bare Submit: the questions are the newest comment in the
          conversation below, a reply there is the decision and sends
          another run to carry it out, and Submit becomes "ship as is". */}
      {t.pullRequest &&
        !t.autoMerge &&
        t.state !== "closed" &&
        t.reviewQuestions && (
          <Alert
            severity="info"
            sx={{ fontSize: "0.8rem" }}
            data-testid="review-questions"
          >
            Review needs your decision: it fixed what it could and left
            questions only you can settle, in the conversation below. Reply with
            your decision and another run carries it out, or ship it as it
            stands.{" "}
            <TaskLink id={t.reviewQuestions} onOpenTask={onOpenTask}>
              Review: task {t.reviewQuestions}
            </TaskLink>
          </Alert>
        )}
      {/* A CL's review loop: Mail it to its reviewers, and publish the
          replies its runs left as drafts (ClReview.jsx). */}
      {t.changelists?.length > 0 && t.clReview && t.state !== "closed" && (
        <ReviewActions t={t} act={act} />
      )}
      {/* A CL's Submit and Sync, Submit disabled with its reasons until
          the CL is submittable (ClSubmit.jsx). */}
      {t.clSubmit && <ClSubmitActions t={t} act={act} />}
      {t.pullRequest && !t.autoMerge && (
        <Button
          variant={t.reviewQuestions ? "outlined" : "contained"}
          title={
            t.reviewQuestions
              ? "Submit the change as it stands, leaving the review's questions open."
              : undefined
          }
          onClick={() =>
            act(
              () => api(`/api/tasks/${t.id}/submit`, { method: "POST" }),
              t.id,
            )
          }
        >
          {t.reviewQuestions ? "Ship as is" : "Submit"}
        </Button>
      )}
      {/* The flag Submit sets, as the flag rather than as the button:
          ui.Client.Submit is `task.AutoMerge = true` and nothing else, so
          this box and that button are one control, not two -- ticking it
          is what Submit does, and Submit disappears the moment it is
          ticked because there is nothing left for it to do. Which leaves
          the box as the half Submit never was: the way to take a task
          back off the merge queue, and the way to say "merge this when
          it is done" before there is a pull request to submit at all.

          It sits here, among the actions, rather than in Target above:
          auto-merge decides nothing about how a task runs, only what
          happens to what it produced, and this is the column where that
          is decided (Submit, Close, "Close acme/widgets#42 too"). It is
          also where the autoMergeDegraded warning below has always been,
          and that warning keys off the flag itself, so it goes on
          applying whichever of the two set it. */}
      <FormControlLabel
        control={
          <Checkbox
            size="small"
            checked={!!t.autoMerge}
            // The flag to send is computed from the task, not read back
            // off the event's own checkbox: the box is controlled by
            // t.autoMerge, so by the time act() runs the request the DOM
            // has already snapped back to what the task still says.
            onChange={() =>
              act(
                () =>
                  api(`/api/tasks/${t.id}`, {
                    method: "PATCH",
                    body: JSON.stringify({ autoMerge: !t.autoMerge }),
                  }),
                t.id,
              )
            }
          />
        }
        label="Auto-merge once checks pass"
        title="The same flag Submit sets: grain puts this task's pull request on the merge queue and merges it once its checks pass. Untick to take it back off."
        sx={{ display: "flex", m: 0 }}
        slotProps={{ typography: { fontSize: "0.8rem" } }}
      />
      {/* "This fixes a red <base>" (ui.UpdateTaskRequest.FixesBaseBranch,
          grain/task-3451): while a base branch is red, grain makes the
          work aimed at it wait on the branch's repair task, shown above
          as "blocked on #N". A task that is itself fixing the branch must
          not, so ticking this removes that wait and keeps it from coming
          back. grain's own repair of the branch is one by definition, and
          cannot be unticked. */}
      {t.repo && t.state !== "closed" && (
        <FormControlLabel
          control={
            <Checkbox
              size="small"
              name="fixesBaseBranch"
              checked={!!t.fixesBaseBranch}
              disabled={!!t.grainBaseRepair}
              onChange={() =>
                act(
                  () =>
                    api(`/api/tasks/${t.id}`, {
                      method: "PATCH",
                      body: JSON.stringify({
                        fixesBaseBranch: !t.fixesBaseBranch,
                      }),
                    }),
                  t.id,
                )
              }
            />
          }
          label={`This fixes a red ${t.base || "main"}`}
          title={
            t.grainBaseRepair
              ? "grain filed this task to repair the branch it lands on, so it is that branch's repair by definition."
              : "While the branch a task lands on is red, grain makes the work aimed at it wait on that branch's repair task. Tick this for a task that is itself fixing the branch: it stops waiting, counts as the branch's repair, and is left open with a comment, not closed, when the branch goes green."
          }
          sx={{ display: "flex", m: 0 }}
          slotProps={{ typography: { fontSize: "0.8rem" } }}
        />
      )}
      {/* The one field on this page an edit does reach mid-run, and the
          reason it is worth saying: auto-merge is read after the run,
          when its pull request is considered for the merge queue
          (orchestrator's own merge pass), not when the run starts -- the
          opposite of the repo and base above, whose checkout is already
          made by then. */}
      {t.state === "running" && (
        <p className="hint">
          Read after this run finishes, when its pull request is considered for
          the merge queue, so a change now still counts for the run in flight.
        </p>
      )}
      {/* Submit still sets autoMerge -- it just never resolves into an
          actual merge on a deployment whose GitHub credential can't read
          check runs (config.autoMergeDegraded, ui.Config.
          AutoMergeDegraded's own doc comment). Without this, clicking
          Submit here looked identical to clicking nothing at all
          (bwsalmon/agents#483). */}
      {t.autoMerge && config?.autoMergeDegraded && (
        <Alert severity="warning" sx={{ fontSize: "0.8rem" }}>
          Queued for auto-merge, but this deployment can't read pull request
          checks, so it will never merge automatically.
        </Alert>
      )}
      {/* The merge queue gives up once and never revisits it, so this is
          the only way back onto the queue for a pull request it stopped
          driving (ui.Client.RetryMerge, and state.js's own "Merge
          blocked" chip for the other half of the same fact). Offered on
          exactly the tasks that have been given up on: a task the queue
          is still driving has nothing to retry, and the queue's comment
          on this one is right above the button, which is where the
          person deciding whether the thing it named has been dealt with
          is already reading.

          A closed task is left without it -- no cycle looks at one at
          all, so RetryMerge refuses it and asks for a reopen first --
          and so is one whose auto-merge has since been unticked, which
          is off the queue entirely: the box above (or Submit) is what
          puts it back on, and this is what asks the queue to try again
          once it is. */}
      {t.mergeQueueBlockedAt && t.autoMerge && t.state !== "closed" && (
        <Button
          variant="contained"
          title="Put this pull request back on the merge queue. Its checks are read again (and grain's own CI run again, where its last answer was anything but a pass), its base branch merged in if it has fallen behind, and it gets one more repair run of its own if that is not enough."
          onClick={() =>
            act(
              () => api(`/api/tasks/${t.id}/retry-merge`, { method: "POST" }),
              t.id,
            )
          }
        >
          Retry merge
        </Button>
      )}
      {/* And the override of the queue's own decision, which is a
          different thing from asking it to try again: "Retry merge"
          hands the change back to the rules, this one sets them aside
          for one merge (ui.Client.ForceMerge). grain performs the merge
          itself on its next cycle, so its record of this task stays
          true -- the alternative, and the thing this replaces, is a
          person merging the pull request on GitHub while grain goes on
          waiting for a change that has already landed.

          Offered on any task with a pull request still open, not only
          the ones the queue gave up on: the case it is most often
          wanted for is a change whose checks are never going to go
          green while the queue waits patiently for them. That includes
          a task whose run is still in flight, blocked waiting on that CI:
          ForceMerge stops the run, opens the pull request if it never
          got that far (a CI commit is the sign its branch was pushed),
          and merges the commit this page shows. A closed task is left
          without it -- there is no cycle to act on one. */}
      {t.state !== "closed" &&
        (t.pullRequest || (t.state === "running" && t.ci?.commit)) &&
        forceMergeOffered(config, t) && (
          <Button
            variant="outlined"
            color="warning"
            title="Merge this change now, overriding the merge queue's own decision. A reason is required and is recorded on the task and on the pull request, along with what was not green at the time. A branch that does not merge cleanly is still refused: no override can authorise a merge GitHub will not perform."
            onClick={forceMerge}
          >
            Force merge
          </Button>
        )}
      {t.state === "failed" && (
        <Button
          variant="contained"
          onClick={() =>
            act(() => api(`/api/tasks/${t.id}/retry`, { method: "POST" }), t.id)
          }
        >
          Retry
        </Button>
      )}
      {/* "Work on this one next", which is the one thing every other
          button here cannot say: the queue picks what runs, in the
          backlog's order and only from what it considers ready
          (ui.Client.RunNow). Offered on every state but the two where it
          would mean nothing -- a closed task, whose run would be
          cancelled the moment it started, and a running one, which has
          an agent on it already.

          That includes the states after a run, deliberately, and they
          are half of why this exists: a completed task, or one whose
          pull request is sitting on the merge queue, has no other way
          back to an agent. Nothing else here sends one back except the
          queue's own repair, which is automatic and granted once. */}
      {t.state !== "closed" && t.state !== "running" && (
        <Button
          variant="outlined"
          disabled={Boolean(t.runNowAt)}
          title={
            t.pullRequest
              ? `Put an agent on this task next, ahead of the queue. Work continues on the branch ${t.pullRequest} is open from, and nothing merges it while that run is in flight.`
              : "Put an agent on this task next, ahead of the queue and whatever its state says -- a blocked, deferred or unapproved task is started all the same, including one waiting on the repair of the red branch it lands on."
          }
          onClick={() =>
            act(
              () => api(`/api/tasks/${t.id}/run-now`, { method: "POST" }),
              t.id,
            )
          }
        >
          Run now
        </Button>
      )}
      {/* The seconds between the click and a dispatch cycle picking the
          task up, which is the one stretch where a button that worked
          looks like one that did not (ui.TaskDetail.RunNowAt -- the
          request, cleared the moment its run starts). */}
      {t.runNowAt && t.state !== "running" && (
        <p className="hint">
          {isTaskPaused(t)
            ? "Asked for a run. It starts ahead of the queue once this task is resumed."
            : "Asked for a run. It starts on the next dispatch cycle, or as soon as there is capacity for it."}
        </p>
      )}
      {/* Manual mode: under a pause Run now passes, this is the button
          that starts this task, and nothing else will; under an
          upgrade's hold even Run now waits, and the hint says so rather
          than leaving a request looking ignored. */}
      {heldForRunNow(config?.pipelinePause, t) && (
        <p className="hint held-for-run-now-hint">
          This deployment is paused: this task waits until somebody presses Run
          now on it.
        </p>
      )}
      {t.state !== "closed" &&
        t.state !== "running" &&
        config?.pipelinePause?.paused &&
        !config.pipelinePause.runNowPasses && (
          <p className="hint">
            An upgrade is holding the queue. A run asked for now waits until the
            upgrade is over, because its restart would discard the run.
          </p>
        )}
      {/* Run now does not override a usage-limit pause (the connection
          pool and dispatch.PausedFor honour it for a request too), so a
          run asked for while every connection this task could run on is
          paused waits for the pause, and the button says so rather than
          looking like it did nothing. */}
      {t.state !== "closed" && t.state !== "running" && frameworkPaused && (
        <p className="hint framework-paused-hint">
          {sentenceCase(pauseSubject(frameworkPaused))} is{" "}
          {pausedUntil(frameworkPaused, zone)}. A run asked for now waits for
          that, unless the pause is lifted with Resume now on the banner.
        </p>
      )}
      {/* The choice grain makes nowhere else: closing this task's pull
          request along with it. Offered only where there is an open one
          to close (state.js's closablePullRequest), unticked every time,
          and read at the moment Close is clicked -- never stored, never
          a default. Closing a pull request on GitHub keeps the branch
          and every commit on it, and reopening the pull request brings
          it back whole, which is what makes it an offer worth making at
          all rather than a way to lose an agent's work. */}
      {t.state !== "closed" && closable && (
        <FormControlLabel
          control={
            <Checkbox
              size="small"
              checked={closePullRequest}
              onChange={(e) => setClosePullRequest(e.target.checked)}
            />
          }
          label={`Close ${closable} too`}
          title={`Closes ${closable} on GitHub without merging it. The branch and its commits are left untouched, and reopening the pull request restores it.`}
          sx={{ display: "flex", m: 0 }}
          slotProps={{ typography: { fontSize: "0.8rem" } }}
        />
      )}
      {t.state === "closed" ? (
        <Button
          variant="outlined"
          onClick={() =>
            act(
              () => api(`/api/tasks/${t.id}/reopen`, { method: "POST" }),
              t.id,
            )
          }
        >
          Reopen
        </Button>
      ) : t.state === "running" ? (
        // Closing a running task stops it from ever being re-dispatched
        // or opened as a pull request, and cancels the run in flight --
        // "Cancel" is that same close call, surfaced under a name that
        // matches what a running task's close button actually does.
        <Button
          variant="outlined"
          color="error"
          onClick={() => {
            if (
              !confirm(
                cancelPrompt(closable && closePullRequest ? closable : null),
              )
            )
              return;
            close();
          }}
        >
          Cancel
        </Button>
      ) : (
        <Button variant="outlined" color="error" onClick={close}>
          Close
        </Button>
      )}
    </Stack>
  );
}

// cancelPrompt is the running task's confirmation, which has to name the
// pull request when the box beside it is ticked: cancelling a run is
// already destructive enough to confirm, and this is the one path where
// confirming it also shuts a pull request. A cancel that leaves the pull
// request alone reads exactly as it always did.
function cancelPrompt(ref) {
  const base =
    "Cancel this job? Its run will be abandoned: no pull request will be opened for it.";
  if (!ref) return base;
  return `Cancel this job, and close ${ref} on GitHub? Its run will be abandoned. The branch and its commits are kept, and reopening ${ref} restores it.`;
}
