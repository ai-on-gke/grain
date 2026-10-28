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

import { Fragment, useEffect, useMemo, useRef, useState } from "react";
import { processPath } from "../paths.js";
import LinkedNote from "../linkedNote.jsx";
import { Alert, Button, Chip, Link, Typography } from "@mui/material";
import { branchRefs } from "../branchRefs.js";
import {
  STATE_LABELS,
  activityTitle,
  completionPhase,
  mergeQueueBatching,
  mergeQueueStall,
  internalPullRequestUrl,
  orphanedPullRequest,
  pullRequestUrl,
  runActivity,
  stateLabel,
  stateTitle,
  taskBadgeModifier,
  taskBadgeState,
  isAgentRunning,
  isTaskRepairing,
  taskPause,
} from "../state.js";
import { useTimeZone } from "../TimeZoneContext.jsx";
import { pausedUntil, pauseSubject } from "../alerts.js";
import AttachmentLinks from "./AttachmentLinks.jsx";
import Markdown from "./Markdown.jsx";
import MergeBatchPanel from "./MergeBatchPanel.jsx";
import Overlay, { DISCARD_PROMPT } from "./Overlay.jsx";
import PromptOverlay from "./PromptOverlay.jsx";
import StateDot from "./StateDot.jsx";
import TaskChat from "./TaskChat.jsx";
import Actions from "./detail/Actions.jsx";
import CapabilityToggles from "./detail/CapabilityToggles.jsx";
import CustomFieldsSection from "./detail/CustomFieldsSection.jsx";
import Declared from "./detail/Declared.jsx";
import Dependencies from "./detail/Dependencies.jsx";
import EditTaskForm from "./detail/EditTaskForm.jsx";
import GrainCIStatus from "./detail/GrainCIStatus.jsx";
import LiveAttemptTranscript from "./detail/LiveAttemptTranscript.jsx";
import PendingQuestion from "./detail/PendingQuestion.jsx";
import PendingSecret from "./detail/PendingSecret.jsx";
import LocalScriptApprovals from "./detail/LocalScriptApprovals.jsx";
import ReadOnlyRepos from "./detail/ReadOnlyRepos.jsx";
import SuspendedSession from "./detail/SuspendedSession.jsx";
import GoalSection from "./detail/GoalSection.jsx";
import Target from "./detail/Target.jsx";
import MergePolicy from "./detail/MergePolicy.jsx";
import TaskLink from "./detail/TaskLink.jsx";
import Timeline from "./detail/Timeline.jsx";
import WorkTree from "./detail/WorkTree.jsx";
import WriteRepos from "./detail/WriteRepos.jsx";

// useBranchPushed reports whether branch is really on repo's remote --
// false until GitHub's own refs say it is, and false too while nobody
// has asked yet or the refs cannot be read at all.
//
// It is what stands between a task and the Internal PR link below. A
// task's branch name is fixed the moment the task is filed
// (model.Task.Branch), long before any run of it pushes anything, so the
// name alone says nothing about whether there is a branch to compare: a
// proposal nobody has approved carries one just as readily as a finished
// run does, and grain's comparison of a branch that is not there has
// nothing to show but an error (grain/task-89).
//
// "Cannot tell" is answered as "no link" rather than as a link on the
// off chance, because the compare page behind it is read through the
// same credential this listing is: a repo whose refs grain cannot read
// is a repo whose diff it cannot draw either.
function useBranchPushed(repo, branch) {
  const [pushed, setPushed] = useState(false);
  useEffect(() => {
    // Reset first: the pane stays mounted as somebody clicks from task
    // to task, and the last task's answer must not decide this one's.
    setPushed(false);
    if (!repo || !branch) return;
    let live = true;
    branchRefs(repo).then(
      (names) => live && setPushed(names.includes(branch)),
      () => {},
    );
    return () => {
      live = false;
    };
  }, [repo, branch]);
  return pushed;
}

// The panel splits like Plane's own issue peek: title, description and
// the conversation in a main column, everything about the task's current
// state and its declared shape (repo, capabilities, dependencies) in a
// narrow property column beside it. It fills the whole pane beside the
// sidebar rather than floating in the middle of it (grain/task-94, see
// Overlay's own `pane`): a task is mostly an agent's own prose, and a
// long answer wants the room.
// SuiteRunLink names the suite run that filed a task -- "pass 2 of
// walk4-suite run 1" -- as a link to that run's own pane
// (/processes/suites/runs/1). A plain click opens it in place, the way TaskLink
// opens a task; the href is the run's deep link, so a modified click or
// "open in new tab" reaches it too.
function SuiteRunLink({ origin, onOpenSuiteRun }) {
  const id = String(origin.runId);
  return (
    <>
      pass {origin.pass} of{" "}
      <Link
        href={processPath("run", id)}
        title={
          origin.templateName
            ? `Filed from template ${origin.templateName}`
            : undefined
        }
        onClick={(e) => {
          if (!onOpenSuiteRun) return;
          if (
            e.button !== 0 ||
            e.metaKey ||
            e.ctrlKey ||
            e.shiftKey ||
            e.altKey
          )
            return;
          e.preventDefault();
          onOpenSuiteRun(id);
        }}
      >
        {origin.suiteName} run {id}
      </Link>
    </>
  );
}

export default function DetailOverlay({
  task: t,
  tasks,
  config,
  templates,
  onClose,
  onOpenTask: rawOpenTask,
  onNewTaskFrom: rawNewTaskFrom,
  // onOpenCIRun opens the shared run overlay over this task -- the way
  // from its grain CI verdict to the step output behind it. Optional: a
  // caller that has nowhere to send a reader simply gets the verdict with
  // no link on it.
  onOpenCIRun,
  // onOpenSuiteRun opens the suite run that filed this task in its own
  // pane. Optional, like onOpenCIRun: without it the link's href still
  // reaches the run by a page load.
  onOpenSuiteRun: rawOpenSuiteRun,
  act,
  showError,
  // animate: false when this pane takes the place of one already on
  // screen (Overlay's own note), so the swap does not fade.
  animate = true,
}) {
  const byId = useMemo(
    () => new Map((tasks || []).map((other) => [other.id, other])),
    [tasks],
  );
  const phase = completionPhase(t);
  // And grain's verdict that this task's merge-queue wait has stopped
  // being one (state.js's mergeQueueStall) -- the same chip the row in
  // the list carries, so the page somebody opens because of it says the
  // same thing the row did.
  const stall = mergeQueueStall(t);
  const orphaned = orphanedPullRequest(t);
  const activity = runActivity(t);
  // A queued task whose framework is under a usage-limit pause is being
  // passed over by every dispatch cycle until it lifts (dispatch.PausedFor),
  // which the badge alone, "queued", does not say.
  const zone = useTimeZone();
  const frameworkPaused = t.state === "queued" ? taskPause(t, config) : null;
  // The branch is only worth asking GitHub about when it would be
  // offered: a task whose pull request is already open links to that
  // instead, and one with no repo or no branch has nothing to compare.
  const comparable = Boolean(!t.pullRequest && t.repo && t.branch);
  const branchPushed = useBranchPushed(
    comparable ? t.repo : "",
    comparable ? t.branch : "",
  );
  const internalPullRequest =
    comparable && branchPushed
      ? internalPullRequestUrl(t.repo, t.branch, t.base)
      : null;
  // editing is local to DetailOverlay, not lifted to App.jsx, the same
  // as Timeline's own openAttempt -- nothing outside this overlay needs
  // to know a task's title and description are mid-edit, and closing
  // the overlay (which unmounts it) is itself "cancel" for free.
  const [editing, setEditing] = useState(false);
  // showPrompt is the same local treatment editing gets, for the same
  // reason: nothing outside this overlay needs to know the prompt pane
  // is open, and closing the task closes it too.
  const [showPrompt, setShowPrompt] = useState(false);
  const editTaskUnsavedRef = useRef(null);
  const customFieldsUnsavedRef = useRef(null);
  const isDirty = () =>
    Boolean(
      editTaskUnsavedRef.current?.() || customFieldsUnsavedRef.current?.(),
    );
  const onOpenTask = rawOpenTask
    ? (id) => {
        if (
          String(id) !== String(t.id) &&
          isDirty() &&
          !window.confirm(DISCARD_PROMPT)
        ) {
          return;
        }
        rawOpenTask(id);
      }
    : undefined;
  const onNewTaskFrom = rawNewTaskFrom
    ? (seed) => {
        if (isDirty() && !window.confirm(DISCARD_PROMPT)) {
          return;
        }
        rawNewTaskFrom(seed);
      }
    : undefined;
  const onOpenSuiteRun = rawOpenSuiteRun
    ? (runId) => {
        if (isDirty() && !window.confirm(DISCARD_PROMPT)) {
          return;
        }
        rawOpenSuiteRun(runId);
      }
    : undefined;
  const activeAttempt =
    t.state === "running"
      ? [...(t.attempts || [])].reverse().find((a) => !a.finishedAt)
      : null;
  return (
    <Overlay
      onClose={onClose}
      pane
      animate={animate}
      label={`Task ${t.id}: ${t.title}`}
      isDirty={isDirty}
    >
      <div className="detail-layout">
        <div className="detail-main">
          {editing ? (
            <EditTaskForm
              t={t}
              config={config}
              templates={templates}
              act={act}
              onDone={() => setEditing(false)}
              unsavedRef={editTaskUnsavedRef}
            />
          ) : (
            <>
              <div className="detail-header">
                {/* The number is a bubble in the task's own state colour
                    rather than a bare figure run into the title, which
                    read as part of the sentence the title makes
                    (grain/task-85) -- the same colour the state badge
                    beside it uses, so the header says which task and how
                    it is doing in one glance. The space between the two
                    is deliberate: it keeps the heading's text content
                    "12 Fix the thing" rather than "12Fix the thing". */}
                <Typography
                  variant="h6"
                  component="h2"
                  sx={{ m: 0, fontWeight: 600, fontSize: "1.15rem" }}
                >
                  <span
                    className={`task-id-bubble task-id-${t.state}`}
                    title={stateTitle(t, byId)}
                  >
                    {t.id}
                  </span>{" "}
                  {t.title}
                </Typography>
                {/* The one way in to the whole prompt a task's agent was
                    handed (grain/task-175 moved it off every task row):
                    this is where someone works out why a task went the
                    way it did, and the title and description above are
                    only part of what its agent was actually told. */}
                <Button size="small" onClick={() => setShowPrompt(true)}>
                  Prompt
                </Button>
                <Button size="small" onClick={() => setEditing(true)}>
                  Edit
                </Button>
              </div>

              <div className="freshness">
                as of just now
                {t.pullRequest ? (
                  <>
                    {" "}
                    ·{" "}
                    <Link
                      href={pullRequestUrl(t.pullRequest, t.branch, t.base)}
                      target="_blank"
                      rel="noopener noreferrer"
                    >
                      {t.pullRequest}
                    </Link>
                  </>
                ) : (
                  internalPullRequest && (
                    <>
                      {" "}
                      ·{" "}
                      <Link
                        href={internalPullRequest}
                        target="_blank"
                        rel="noopener noreferrer"
                      >
                        Internal PR
                      </Link>
                    </>
                  )
                )}
                {/* The changelists (CLs) that are this task's work
                    (model.LinkChangelist) sit beside its pull request,
                    each its cl/<number> go link. */}
                {(t.changelists || []).map((cl) => (
                  <Fragment key={`changelist-${cl}`}>
                    {" "}
                    ·{" "}
                    <Link
                      href={`http://${cl}`}
                      target="_blank"
                      rel="noopener noreferrer"
                    >
                      {cl}
                    </Link>
                  </Fragment>
                ))}
                {/* Provenance links and the tree of work this task belongs
                    to (docs/work-trees.md) fold under a compact disclosure
                    that starts collapsed by default so long work trees do
                    not push task status, CI checks, and timeline down the
                    page. */}
                {(() => {
                  const items = [];
                  if (t.generatedFrom) {
                    items.push(
                      <Fragment key="generated-from">
                        generated from{" "}
                        <TaskLink
                          id={t.generatedFrom}
                          onOpenTask={onOpenTask}
                        />
                      </Fragment>,
                    );
                  }
                  // The way back to the suite run that filed this task
                  // (ui.TaskDetail.FiledBySuiteRun), the other direction
                  // from the run's pane, which links to each of its
                  // tasks. Absent once the run itself is gone: the
                  // list's "suite" chip is all that is left to say.
                  if (t.filedBySuiteRun) {
                    items.push(
                      <SuiteRunLink
                        key="filed-by-suite-run"
                        origin={t.filedBySuiteRun}
                        onOpenSuiteRun={onOpenSuiteRun}
                      />,
                    );
                  }
                  // Where this task's tree of work began
                  // (model.WorkOrigin): a GitHub pull request or issue,
                  // or the grain task -- a chat, usually -- at its root.
                  if (t.origin) {
                    items.push(
                      <Fragment key="origin">
                        origin{" "}
                        {t.origin.startsWith("task:") ? (
                          <TaskLink
                            id={t.origin.slice("task:".length)}
                            onOpenTask={onOpenTask}
                          />
                        ) : (
                          <Link
                            href={`https://github.com/${t.origin.replace("#", "/issues/")}`}
                            target="_blank"
                            rel="noopener noreferrer"
                          >
                            {t.origin}
                          </Link>
                        )}
                      </Fragment>,
                    );
                  }
                  // The other end of "generated from" on a security
                  // review's own page: the held change's way to the
                  // review deciding it (model.LinkSecurityReviewTask).
                  if (t.securityReviewTask) {
                    items.push(
                      <Fragment key="security-review">
                        security review{" "}
                        <TaskLink
                          id={t.securityReviewTask}
                          onOpenTask={onOpenTask}
                        />
                      </Fragment>,
                    );
                  }
                  // Both halves of a supersede, each a link to the other
                  // task: the point of recording the relation at all is
                  // that a chain of re-filed revisions can be walked
                  // from either end without reading five descriptions
                  // (model.LinkSupersedes).
                  if (t.supersedes) {
                    items.push(
                      <Fragment key="supersedes">
                        supersedes{" "}
                        <TaskLink id={t.supersedes} onOpenTask={onOpenTask} />
                      </Fragment>,
                    );
                  }
                  if (t.supersededBy) {
                    items.push(
                      <Fragment key="superseded-by">
                        superseded by{" "}
                        <TaskLink id={t.supersededBy} onOpenTask={onOpenTask} />
                      </Fragment>,
                    );
                  }
                  // Both halves of a revert (model.LinkReverts / model.LinkRevertedBy):
                  // allows walking back and forth between a revert task and its origin task.
                  if (t.reverts) {
                    items.push(
                      <Fragment key="reverts">
                        reverts{" "}
                        <TaskLink id={t.reverts} onOpenTask={onOpenTask} />
                      </Fragment>,
                    );
                  }
                  if (t.revertedBy) {
                    items.push(
                      <Fragment key="reverted-by">
                        reverted by{" "}
                        <TaskLink id={t.revertedBy} onOpenTask={onOpenTask} />
                      </Fragment>,
                    );
                  }
                  // Both halves of a port (model.LinkPortOf / model.LinkPortedBy):
                  // a change merged on one paired branch and its port onto
                  // the other.
                  if (t.portOf) {
                    items.push(
                      <Fragment key="port-of">
                        port of{" "}
                        <TaskLink id={t.portOf} onOpenTask={onOpenTask} />
                      </Fragment>,
                    );
                  }
                  for (const id of t.portedBy || []) {
                    items.push(
                      <Fragment key={`ported-by-${id}`}>
                        ported by <TaskLink id={id} onOpenTask={onOpenTask} />
                      </Fragment>,
                    );
                  }
                  if (t.noPort) {
                    items.push(<Fragment key="no-port">not ported</Fragment>);
                  }
                  if (t.googleChatSpace) {
                    items.push(
                      <Fragment key="google-chat-space">
                        Google Chat space{" "}
                        <Link
                          href={
                            t.googleChatSpace.startsWith("http://") ||
                            t.googleChatSpace.startsWith("https://")
                              ? t.googleChatSpace
                              : `https://mail.google.com/chat/u/0/#chat/space/${t.googleChatSpace.replace(/^spaces\//, "")}`
                          }
                          target="_blank"
                          rel="noopener noreferrer"
                        >
                          {t.googleChatSpace}
                        </Link>
                      </Fragment>,
                    );
                  }
                  const provenance =
                    items.length > 0
                      ? items.map((item, idx) => (
                          <Fragment key={item.key}>
                            {idx > 0 && " · "}
                            {item}
                          </Fragment>
                        ))
                      : null;
                  return (
                    <WorkTree
                      t={t}
                      tasks={tasks}
                      onOpenTask={onOpenTask}
                      provenance={provenance}
                    />
                  );
                })()}
              </div>

              <PendingQuestion t={t} act={act} onOpenTask={onOpenTask} />

              {/* The placeholder is grain's own words, not the task's,
                  so it stays a plain string -- only a real description
                  (often an agent's own markdown, for a task another run
                  proposed) goes through the renderer. */}
              {t.description ? (
                <Markdown className="description">{t.description}</Markdown>
              ) : (
                <div className="description">(no description)</div>
              )}
              <AttachmentLinks taskId={t.id} attachments={t.attachments} />
            </>
          )}

          {t.state === "failed" && (
            <Alert severity="error" sx={{ mb: 2 }}>
              <strong>
                {t.failedAttempts} consecutive failed attempt
                {t.failedAttempts === 1 ? "" : "s"}.
              </strong>
              {t.lastFailureReason && <> Last failure: {t.lastFailureReason}</>}
            </Alert>
          )}

          {/* A pull request nobody is going to merge, said where the
              person who closed the task is looking -- see state.js's
              orphanedPullRequest for why a closed task can be carrying
              one and why a merged one is not it. */}
          {orphaned && (
            <Alert severity="warning" sx={{ mb: 2 }}>
              <strong>
                {orphaned} is still open, and grain has stopped watching it.
              </strong>{" "}
              Closing this task took it off the merge queue for good, so grain
              will not merge or update that pull request. Merge or close it on
              GitHub by hand, or reopen this task to put it back under grain's
              watch.
            </Alert>
          )}

          <GrainCIStatus t={t} onOpenCIRun={onOpenCIRun} act={act} />

          <PendingSecret t={t} act={act} showError={showError} />

          {/* A run's local-script calls waiting on a person. An
              interactive task shows them in its chat instead (TaskChat),
              which is where the person sitting with it is looking. */}
          {!t.interactive && <LocalScriptApprovals t={t} act={act} />}

          {activeAttempt && (
            <LiveAttemptTranscript
              taskId={t.id}
              attempt={activeAttempt}
              showError={showError}
            />
          )}

          {/* An interactive task is a conversation somebody is having
              right now, so it is read as one: the chat window is the
              pane, and the timeline it used to borrow -- every state
              transition, every attempt, every pull request event --
              folds up underneath it, where it is still one click away
              for the reader who wants to know what the run behind a
              reply actually did (grain/task-414). Every other task is
              unchanged: the timeline is the whole of its conversation
              and is not hidden behind anything. */}
          {t.interactive ? (
            <>
              <TaskChat t={t} act={act} />
              <details className="task-chat-history">
                <summary>Task history</summary>
                <Timeline
                  t={t}
                  act={act}
                  showError={showError}
                  onOpenTask={onOpenTask}
                />
              </details>
            </>
          ) : (
            <Timeline
              t={t}
              act={act}
              showError={showError}
              onOpenTask={onOpenTask}
            />
          )}
        </div>

        <div className="detail-side">
          <div className="detail-state">
            {(() => {
              const modifier = taskBadgeModifier(t);
              const repairing = isTaskRepairing(t);
              const interactive = !repairing && Boolean(t.interactive);
              return (
                <span
                  className={`badge badge-${taskBadgeState(t)}${isAgentRunning(t) ? " badge-mark" : ""}${modifier ? ` ${modifier}` : ""}`}
                  title={stateTitle(t, byId)}
                >
                  <StateDot
                    state={taskBadgeState(t)}
                    title={stateTitle(t, byId)}
                    repairing={repairing}
                    interactive={interactive}
                    agentRunning={isAgentRunning(t)}
                  />
                  {stateLabel(t)}
                </span>
              );
            })()}
            {/* These chips read as annotations beside the state dot, not a
                replacement for it -- a task whose pull request the merge
                queue gave up on is still "completed" as far as model.State
                goes. A blocked task has no chip: its badge is the blocked
                oval itself, whose title names what it waits on. */}
            {stall && (
              <Chip
                size="small"
                color={stall.color}
                title={stall.title}
                label={stall.label}
              />
            )}
            {phase && (
              <Chip
                size="small"
                color={phase.color}
                title={phase.title}
                label={phase.label}
              />
            )}
            {t.stall && (
              <Chip
                size="small"
                color="warning"
                title={t.stall}
                label="Stalled"
              />
            )}
            {t.base && (
              <Chip
                size="small"
                variant="outlined"
                label={`target: ${t.base}`}
              />
            )}
          </div>
          {/* And the sentence itself, spelled out under the badge it is
              about, for the same reason the merge queue's note is
              spelled out below: the row has to make do with a chip and a
              tooltip, and this is the page somebody opens *because* the
              row said the state had stood too long. It says which
              component ought to have ended the wait and where to look
              (ui.Task.Stall, orchestrator's taskLane). */}
          {t.stall && <div className="detail-stall">{t.stall}</div>}
          {/* What the run is doing, under the badge that says only that
              it is running (state.js's runActivity) -- the run's own
              words through update_status, or grain's own account of the
              setup it is still doing before the agent's first turn. The
              same line the task list shows, kept here so the page
              somebody opens *because* the list said "running" does not
              answer with less than the list did. */}
          {activity && (
            <div className="detail-activity" title={activityTitle(activity)}>
              {activity.bySetup && (
                <span className="task-activity-by">grain</span>
              )}
              {activity.note}
              {activity.age && (
                <span className="task-activity-age">{activity.age}</span>
              )}
              {/* The merge queue's own account of the wait, spelled out
                  here where there is room for a sentence: a row has to
                  make do with the phrase and the tooltip, but this is
                  the page somebody opens *because* the row said
                  "Merging" and nothing had happened for an hour. */}
              {activity.detail && (
                <div className="detail-activity-note">
                  <LinkedNote text={activity.detail} />
                </div>
              )}
            </div>
          )}

          {/* And the hold no grant wait records: every connection this
              task could run on is paused (config.agentPause), so no cycle
              will pick it up before the pause ends or is lifted from
              the banner. */}
          {frameworkPaused && (
            <div
              className="detail-activity detail-framework-paused"
              title={`Nothing is dispatched onto ${pauseSubject(frameworkPaused)} while the pause lasts; Resume now on the banner lifts it.`}
            >
              <span className="task-activity-by">grain</span>
              waiting: {pauseSubject(frameworkPaused)} is{" "}
              {pausedUntil(frameworkPaused, zone)}
            </div>
          )}

          {/* And, when the queue is testing this change together with
              others, the batch itself (grain/task-301). The line above
              says this change is in one and how many others there are;
              this says which others, in the order a failure is halved
              towards, and links to the single run deciding all of them.
              It draws nothing for a task that is not in a batch, which is
              almost every task almost always. */}
          {/* What the repository's merge rules require of this change,
              and which rule asked (grain/task-1993). */}
          <MergePolicy t={t} />

          {mergeQueueBatching(t) && (
            <MergeBatchPanel
              repo={t.repo}
              taskId={t.id}
              onOpenRun={onOpenCIRun}
              onOpenTask={onOpenTask}
              showError={showError}
            />
          )}

          <Actions t={t} config={config} act={act} onOpenTask={onOpenTask} />
          {/* What grain checks to close this task, rather than its agent's
              word (docs/design/durable-tasks.md#DT-4). */}
          <GoalSection t={t} act={act} />
          {/* The repos this task may change, in one section
              (grain/task-202): the one it is filed against, then the
              others under it, rather than a "Target" pane and a second
              pane elsewhere in the column for the rest. */}
          <Target
            t={t}
            tasks={tasks}
            config={config}
            act={act}
            showError={showError}
          >
            <WriteRepos
              t={t}
              tasks={tasks}
              config={config}
              act={act}
              showError={showError}
            />
          </Target>
          <ReadOnlyRepos
            t={t}
            tasks={tasks}
            config={config}
            act={act}
            showError={showError}
          />
          <Declared t={t} config={config} act={act} />
          <CustomFieldsSection
            t={t}
            config={config}
            act={act}
            onNewTaskFrom={onNewTaskFrom}
            unsavedRef={customFieldsUnsavedRef}
          />
          <CapabilityToggles t={t} config={config} act={act} />
          <SuspendedSession t={t} />
          <Dependencies t={t} tasks={tasks} act={act} onOpenTask={onOpenTask} />
        </div>
      </div>
      {showPrompt && (
        <PromptOverlay taskId={t.id} onClose={() => setShowPrompt(false)} />
      )}
    </Overlay>
  );
}
