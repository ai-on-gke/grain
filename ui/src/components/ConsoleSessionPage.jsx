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

import { isApiGrant, withoutApiRows } from "../apiGrants.js";
import GrantsPicker from "./GrantsPicker.jsx";
import { localGrantHandlers } from "./capabilityMethods.js";
import { isCapabilityRef } from "../capabilityArgs.js";
import { useEffect, useState } from "react";
import { PageHeaderActions, useInPageHeader } from "./PageHeader.jsx";
import {
  Alert,
  Box,
  Button,
  Checkbox,
  FormControlLabel,
  TextField,
  Typography,
} from "@mui/material";
import api from "../api.js";
import {
  configuredCapabilities,
  isAgentRunning,
  isTaskRepairing,
  knownRepos,
  stateLabel,
  stateTitle,
  targetableRepos,
  taskBadgeModifier,
  taskBadgeState,
} from "../state.js";
import ReadOnlyReposField from "./ReadOnlyReposField.jsx";
import StateDot from "./StateDot.jsx";
import TaskChat from "./TaskChat.jsx";
import WriteReposField from "./WriteReposField.jsx";
import ItemGlyph from "./ItemGlyph.jsx";
import ModelPicker from "./ModelPicker.jsx";
import WorkTree from "./detail/WorkTree.jsx";

// ConsoleSessionPage is a standing session -- the director, and whatever
// else this deployment configures (model.ConsoleSession, grain/task-415)
// -- as the pane the rail opens: the conversation, and the capabilities
// the thing on the other end of it holds.
//
// # Why the chat and the capabilities are one pane
//
// Because they are one question. Every second thing anybody asks a
// director is something it can only do with a capability it may not
// have, and the answer -- "I would need self-repair for that" -- is
// useless if acting on it means finding a settings dialog, a tab inside
// it, and a list of every capability this deployment offers. Here the
// grant is a tick box under the message that asked for it: the
// reconciler carries a changed set onto the session's task
// (orchestrator.syncConsoleSessionTask), so there is nothing to re-file.
// What it cannot do is reach into a run already going -- a run keeps the
// grants it started with, and a session's run outlives any one message
// (liveRunKey below) -- so the pane says when a change is waiting on the
// next run and offers to stop the open one.
//
// # Why the offer of a fresh chat is here too
//
// For the same reason, and it is the one control this pane has that
// ends something. A standing conversation eventually costs the runs it
// is handed to -- a prompt that starts with a quarter of the window
// already spent, or a thread grain had to cut the middle out of -- and
// the answer is a fresh chat, which is what closing the session's task
// already produces (orchestrator.reconcileConsoleSession files the
// session again, envelope and all, and carries the last handover
// across). grain never takes that decision for anybody and never says
// so in the thread: an offer in a message bubble reads as the agent
// asking to be restarted. So it is a button beside the capabilities
// toggle, shown only when the recorded prompt sizes say there is
// something to weigh (ConsoleSessionSetting.Crowded) and never on the
// conversation's age.
//
// # Why it is a task underneath
//
// A session is one task that is never finished, and this pane is that
// task's chat (TaskChat.jsx, the same component an ordinary interactive
// task's detail draws). Nothing here is a second way to run an agent:
// the messages are comments, the answers are comments, the runs behind
// them are ordinary dispatches with ordinary sandboxes, and everything
// this deployment records about a run is recorded about these. What the
// pane adds is that the conversation has no end -- which is the whole
// of what a standing session is.
// The capabilities toggle names the session, and a session's title is
// whatever somebody typed: one unbroken run in it ran the label out of
// the button and the page sideways on a phone. It wraps anywhere, and
// may shrink below its text in the row of actions it sits in.
const TITLED_BUTTON_SX = { minWidth: 0, overflowWrap: "anywhere" };

// liveRunKey names the run in flight on a session's task, or "" when
// none is: the newest attempt that has not finished, by number. A task
// whose detail carries no attempts yet still has a run if its agent is
// running, and "live" stands for it.
//
// It exists because a session's run is not one exchange long. Its agent
// blocks in the chat tool between messages, so a reply lands in the same
// run, sandbox and mcpserver it was sent to -- and capabilities, repo
// scopes and network access are all fixed when that run starts
// (orchestrator.RunDispatch). syncConsoleSessionTask carries a change
// onto the task row, never into a run already going. So the pane notes
// which run was live when something was saved, and says, for as long as
// that run lasts, that it does not have the change.
export function liveRunKey(task) {
  if (!isAgentRunning(task)) return "";
  const open = [...(task.attempts || [])].reverse().find((a) => !a.finishedAt);
  return open ? `attempt-${open.number}` : "live";
}

// RUN_FIXED is every session field a run takes on when it starts and
// keeps for its whole life; the rest of the pane (the Google Chat space,
// whether its tasks are filed approved) is read as it is needed.
const RUN_FIXED = ["capabilities", "reads", "writes", "networkAccess", "model"];

export default function ConsoleSessionPage({
  session,
  task,
  tasks = [],
  config = {},
  capabilities = [],
  networkAccessOptions = ["open", "none"],
  act,
  onOpenTask,
  draftRequest,
  onDraftConsumed,
  onSaved,
  onDeleted,
  showError,
}) {
  // saving is the id of the capability whose box was just clicked, so
  // the pane can leave that one box disabled while the save is in
  // flight rather than freezing the whole list.
  const inRail = useInPageHeader();
  const [saving, setSaving] = useState(null);
  const [capabilitiesOpen, setCapabilitiesOpen] = useState(false);
  const [deleting, setDeleting] = useState(false);
  // The offer of a fresh conversation, and whether the close behind it
  // is in flight. Two steps rather than one button, because the click
  // ends a conversation: what it costs and what it keeps has to be on
  // screen before anybody agrees to it.
  const [restartOpen, setRestartOpen] = useState(false);
  const [restarting, setRestarting] = useState(false);
  // App keeps this page mounted while the rail switches between
  // sessions, so the panels opened on one must not stay open on the next.
  useEffect(() => {
    setCapabilitiesOpen(false);
    setRestartOpen(false);
  }, [session?.id]);
  const [writes, setWrites] = useState(session?.writes || []);
  useEffect(() => {
    setWrites(session?.writes || []);
  }, [session?.writes]);
  const [googleChatSpace, setGoogleChatSpace] = useState(
    session?.googleChatSpace || "",
  );
  useEffect(() => {
    setGoogleChatSpace(session?.googleChatSpace || "");
  }, [session?.googleChatSpace]);
  const held = new Set(session?.capabilities || []);
  // staleRuns maps a session id to the run (liveRunKey) that was live
  // when something that run cannot pick up was saved on it. Kept per
  // session because App keeps this page mounted across sessions.
  const [staleRuns, setStaleRuns] = useState({});
  const [applying, setApplying] = useState(false);
  const runKey = liveRunKey(task);
  const runIsStale = Boolean(runKey) && staleRuns[session?.id] === runKey;

  const save = async (body, key) => {
    setSaving(key);
    const keyAtSave = runKey;
    try {
      await api(`/api/console-sessions/${session.id}`, {
        method: "PUT",
        body: JSON.stringify(body),
      });
      if (keyAtSave && Object.keys(body).some((k) => RUN_FIXED.includes(k))) {
        setStaleRuns((prev) => ({ ...prev, [session.id]: keyAtSave }));
      }
      await onSaved();
    } catch (err) {
      showError(err);
    } finally {
      setSaving(null);
    }
  };

  // methodGrants saves the session's capability server grants as
  // GrantsPicker changes them, keeping everything else it holds.
  const methodGrants = localGrantHandlers(
    (session?.capabilities || []).filter(isCapabilityRef),
    (refs) =>
      save(
        {
          capabilities: [
            ...(session.capabilities || []).filter((c) => !isCapabilityRef(c)),
            ...refs,
          ],
        },
        "methods",
      ),
  );

  const toggle = async (id) => {
    const next = held.has(id)
      ? (session.capabilities || []).filter((c) => c !== id)
      : [...(session.capabilities || []), id];
    await save({ capabilities: next }, id);
  };

  const restart = async (carryHandover = false) => {
    setRestarting(true);
    try {
      await api(`/api/console-sessions/${session.id}/restart`, {
        method: "POST",
        body: JSON.stringify({ carryHandover: Boolean(carryHandover) }),
      });
      setRestartOpen(false);
      await onSaved();
    } catch (err) {
      showError(err);
    } finally {
      setRestarting(false);
    }
  };

  const cancelTurn = async () => {
    try {
      await api(`/api/console-sessions/${session.id}/cancel`, {
        method: "POST",
      });
      await onSaved();
    } catch (err) {
      showError(err);
    }
  };

  // applyNow stops the run that does not have the saved changes. The
  // task, and the whole conversation on it, stays: the session goes back
  // to awaiting a reply, and the next message is answered by a fresh run
  // that starts with what is saved here.
  const applyNow = async () => {
    setApplying(true);
    try {
      await api(`/api/console-sessions/${session.id}/cancel`, {
        method: "POST",
      });
      setStaleRuns((prev) => {
        const next = { ...prev };
        delete next[session.id];
        return next;
      });
      await onSaved();
    } catch (err) {
      showError(err);
    } finally {
      setApplying(false);
    }
  };

  const handleDelete = async () => {
    if (
      !window.confirm(
        `Delete session "${session.title || session.id}"? This removes the session from this deployment.`,
      )
    ) {
      return;
    }
    setDeleting(true);
    try {
      await api(`/api/console-sessions/${session.id}`, {
        method: "DELETE",
      });
      if (onDeleted) {
        onDeleted(session.id);
      } else if (onSaved) {
        await onSaved();
      }
    } catch (err) {
      showError(err);
    } finally {
      setDeleting(false);
    }
  };

  if (!session) return null;

  const offerFresh = Boolean(task) && task.state !== "closed";
  const renderFreshOffer = () => (
    <Box
      id={`${session.id}-fresh-content`}
      className="console-session-capabilities"
      role="region"
      aria-labelledby={`${session.id}-fresh-header`}
    >
      <Typography variant="subtitle2" component="h3" sx={{ mb: 0.5 }}>
        Start a fresh chat
      </Typography>
      {session.crowdedNote && (
        <Typography
          variant="caption"
          color="text.secondary"
          component="p"
          sx={{ mb: 1 }}
        >
          {session.crowdedNote}
        </Typography>
      )}
      <Typography variant="body2" component="p" sx={{ mb: 1 }}>
        This closes the current chat and immediately opens a fresh one for this
        session. It keeps the capabilities, repositories, network access and
        model you have set here. You can reset completely fresh or carry over
        the last standing summary this chat&apos;s runs wrote.
      </Typography>
      <Box sx={{ display: "flex", gap: 1, flexWrap: "wrap" }}>
        <Button
          size="small"
          variant="outlined"
          disabled={restarting}
          onClick={() => restart(false)}
        >
          Close this chat and start a fresh one
        </Button>
        <Button
          size="small"
          variant="text"
          disabled={restarting}
          onClick={() => restart(true)}
        >
          Start fresh with summary
        </Button>
      </Box>
    </Box>
  );

  const renderCapabilitiesContent = () => (
    <Box
      id={`${session.id}-capabilities-content`}
      className="console-session-capabilities"
      role="region"
      aria-labelledby={`${session.id}-capabilities-header`}
    >
      <Box>
        <Typography variant="subtitle2" component="h3" sx={{ mb: 0.5 }}>
          What it may reach
        </Typography>
        <Typography
          variant="caption"
          color="text.secondary"
          component="p"
          sx={{ mb: 1 }}
        >
          {runKey
            ? "A run is open on this session now. Capabilities, repository access, network access and the model are fixed when a run starts, so a change saved here reaches the agent when its next run starts -- after this one ends, or once you stop it."
            : "No run is open on this session, so a change saved here takes effect on the next message you send."}
        </Typography>
        {/* Only what this deployment has configured, plus whatever this
        session already holds -- a box that cannot be reached by
        ticking it is not an offer, and one already ticked has to
        stay so it can be turned off (configuredCapabilities,
        state.js). */}
        {withoutApiRows(
          configuredCapabilities(capabilities, session.capabilities),
        ).map((c) => (
          <FormControlLabel
            key={c.id}
            control={
              <Checkbox
                size="small"
                checked={held.has(c.id)}
                disabled={saving === c.id}
                onChange={() => toggle(c.id)}
              />
            }
            label={
              <span>
                {c.name}
                <Typography
                  variant="caption"
                  color="text.secondary"
                  component="span"
                  sx={{ display: "block" }}
                >
                  {c.description}
                </Typography>
              </span>
            }
            sx={{
              display: "flex",
              alignItems: "flex-start",
              mb: 0.5,
            }}
          />
        ))}
        {/* The agent API's grants and capability server methods, with
        their with: arguments, in one list (GrantsPicker): each change
        is saved as it is made, like the boxes above. */}
        <GrantsPicker
          config={{ ...config, capabilities }}
          grants={session.capabilities || []}
          onApiChange={(next) =>
            save(
              {
                capabilities: [
                  ...(session.capabilities || []).filter((c) => !isApiGrant(c)),
                  ...next,
                ],
              },
              "api",
            )
          }
          onSubmit={methodGrants.onSubmit}
          onRemove={methodGrants.onRemove}
          disabled={saving === "api" || saving === "methods"}
          idPrefix={`session-${session.id}`}
        />
      </Box>

      <Box sx={{ mt: 2 }}>
        <Typography variant="subtitle2" component="h3" sx={{ mb: 0.5 }}>
          Repositories it may read
        </Typography>
        <Typography
          variant="caption"
          color="text.secondary"
          component="p"
          sx={{ mb: 1 }}
        >
          Repositories this session may clone or fetch.
        </Typography>
        <ReadOnlyReposField
          options={knownRepos(config, tasks)}
          value={session.reads || []}
          onChange={(next) => save({ reads: next }, "reads")}
        />
      </Box>

      <Box sx={{ mt: 2 }}>
        <Typography variant="subtitle2" component="h3" sx={{ mb: 0.5 }}>
          Repositories it may write
        </Typography>
        <Typography
          variant="caption"
          color="text.secondary"
          component="p"
          sx={{ mb: 1 }}
        >
          Repositories this session may push commits to.
        </Typography>
        <WriteReposField
          options={targetableRepos(config, tasks)}
          value={writes}
          onChange={setWrites}
          onCommit={(next) => save({ writes: next }, "writes")}
        />
      </Box>

      {/* The other half of "what it may reach", and the half that was
      only ever settable through the API: PUT /api/console-sessions
      has taken a networkAccess since this shipped, and
      GET /api/console-sessions answers with the very list of options
      this draws (ConsoleSessionSettingsResponse.NetworkAccessOptions),
      but no control here ever read either -- so a deployment could
      not change what the shipped default says.

      The warning under it is the other half. A host-mode deployment
      has no network of its own to withhold, so
      orchestrator.reconcileConsoleSessions files the session asking
      for the access it can actually be given, whatever is chosen
      here -- and a select that silently means nothing is worse than
      one that says so (grain/task-399). */}
      <Box sx={{ mt: 2 }}>
        <TextField
          select
          name="networkAccess"
          label="What it may reach on the internet"
          value={session.networkAccess || "open"}
          disabled={saving === "networkAccess"}
          onChange={(e) =>
            save({ networkAccess: e.target.value }, "networkAccess")
          }
          helperText={
            runKey
              ? "Fixed when a run starts: the open run keeps what it started with."
              : "Takes effect on the next message you send."
          }
          fullWidth
          margin="normal"
          SelectProps={{ native: true }}
          InputLabelProps={{ shrink: true }}
        >
          {networkAccessOptions.map((access) => (
            <option key={access} value={access}>
              {access === "none"
                ? "none (this deployment only)"
                : "egress (outbound internet only)"}
            </option>
          ))}
        </TextField>
      </Box>

      {/* Which model the session's agent runs on (model.ConsoleSession.
      Model): the same choices a template's picker offers, saved like
      everything else here and carried onto the session's task by the
      reconciler before its next exchange. "" leaves it on the
      deployment's default model. */}
      <Box sx={{ mt: 2 }}>
        <ModelPicker
          config={config}
          name="sessionModel"
          value={session.model || ""}
          onChange={(next) => {
            if (next !== (session.model || "")) {
              save({ model: next }, "model");
            }
          }}
          helperText={
            runKey
              ? "the model this session's agent runs on. Fixed when a run starts, like the grants above."
              : "the model this session's agent runs on. Takes effect on the next message you send."
          }
        />
      </Box>

      <Box sx={{ mt: 2 }}>
        <Typography variant="subtitle2" component="h3" sx={{ mb: 0.5 }}>
          Google Chat space
        </Typography>
        <Typography
          variant="caption"
          color="text.secondary"
          component="p"
          sx={{ mb: 1 }}
        >
          Link this session to a Google Chat space (e.g. spaces/AAAAAAAAAAA).
        </Typography>
        <TextField
          size="small"
          name="googleChatSpace"
          placeholder="spaces/..."
          value={googleChatSpace}
          disabled={saving === "googleChatSpace"}
          onChange={(e) => setGoogleChatSpace(e.target.value)}
          onBlur={() => {
            if (googleChatSpace !== (session.googleChatSpace || "")) {
              save({ googleChatSpace }, "googleChatSpace");
            }
          }}
          fullWidth
        />
      </Box>

      <Box sx={{ mt: 2 }}>
        <Typography variant="subtitle2" component="h3" sx={{ mb: 0.5 }}>
          What it may set going
        </Typography>
        {/* The other half of what a session may do, and the one worth a
        deliberate choice: a chat is a fast way to ask for a lot of
        work, so what it files waits for a person unless somebody
        here says otherwise (model.ConsoleSession.TasksApproved). */}
        <FormControlLabel
          control={
            <Checkbox
              size="small"
              checked={Boolean(session.tasksApproved)}
              disabled={saving === "tasksApproved"}
              onChange={() =>
                save({ tasksApproved: !session.tasksApproved }, "tasksApproved")
              }
            />
          }
          label={
            <span>
              File its tasks approved
              <Typography
                variant="caption"
                color="text.secondary"
                component="span"
                sx={{ display: "block" }}
              >
                Off, the tasks it proposes wait for you to approve them. On,
                they are queued and dispatched like any other.
              </Typography>
            </span>
          }
          sx={{ display: "flex", alignItems: "flex-start" }}
        />
        <FormControlLabel
          control={
            <Checkbox
              size="small"
              name="keepRunning"
              checked={Boolean(session.keepRunning)}
              disabled={saving === "keepRunning"}
              onChange={() =>
                save({ keepRunning: !session.keepRunning }, "keepRunning")
              }
            />
          }
          label={
            <span>
              Keep running
              <Typography
                variant="caption"
                color="text.secondary"
                component="span"
                sx={{ display: "block" }}
              >
                Off, the session rests when a turn ends and waits for your next
                message. On, it immediately starts a new turn whenever a run
                exits.
              </Typography>
            </span>
          }
          sx={{ display: "flex", alignItems: "flex-start", mt: 0.5 }}
        />
      </Box>
    </Box>
  );

  return (
    // role="main": this page is the shell's content, and without the
    // landmark a screen reader's "jump to main" found nothing on it while
    // Tasks, Inbox and CI had one. A role rather than a <main> so the
    // page keeps its own layout and not main's padding.
    <div className="console-session-page" role="main">
      {/* The heading stays in the page, inside its main landmark; on
          the desktop the rail's title says it on screen, so it is only
          hidden from sight there, and the rest of the header -- the
          task and its state -- is in the rail (PageHeader.jsx,
          grain/task-4915). */}
      {inRail && (
        <Typography
          component="h2"
          className="console-session-title visually-hidden"
        >
          {session.title}
        </Typography>
      )}
      <PageHeaderActions>
        <header className="content-header console-session-header">
          {!inRail && (
            <>
              <span className="header-icon">
                <ItemGlyph
                  kind={session.id === "director" ? "director" : "sessions"}
                  size={20}
                />
              </span>
              <Typography component="h2" className="console-session-title">
                {session.title}
              </Typography>
            </>
          )}
          {task && (
            <>
              {(() => {
                const modifier = taskBadgeModifier(task);
                const repairing = isTaskRepairing(task);
                const interactive = !repairing && Boolean(task.interactive);
                return (
                  <span
                    className={`badge badge-${taskBadgeState(task)}${isAgentRunning(task) ? " badge-mark" : ""}${modifier ? ` ${modifier}` : ""}`}
                    title={stateTitle(task)}
                  >
                    <StateDot
                      state={taskBadgeState(task)}
                      title={stateTitle(task)}
                      repairing={repairing}
                      interactive={interactive}
                      agentRunning={isAgentRunning(task)}
                    />
                    {stateLabel(task)}
                  </span>
                );
              })()}
            </>
          )}
        </header>
      </PageHeaderActions>

      <div className="console-session-body">
        {task ? (
          <TaskChat
            // Keyed by task so switching sessions remounts the composer:
            // a draft or picked files left in one chat must not be sent
            // to the next one's task.
            key={task.id}
            t={task}
            act={act}
            onStop={cancelTurn}
            draftRequest={draftRequest}
            onDraftConsumed={onDraftConsumed}
            actions={
              <>
                <Button
                  size="small"
                  variant="text"
                  onClick={() => setCapabilitiesOpen((open) => !open)}
                  aria-expanded={capabilitiesOpen}
                  aria-controls={`${session.id}-capabilities-content`}
                  id={`${session.id}-capabilities-header`}
                  sx={TITLED_BUTTON_SX}
                >
                  {capabilitiesOpen ? "Hide" : "Edit"} {session.title}{" "}
                  capabilities
                </Button>
                {/* Beside the capabilities toggle for that toggle's own
                reason: a control somebody has to go and find is a
                control nobody uses at the moment they want it. And a
                control rather than a message from grain in the thread,
                which is where an offer to restart reads as the agent
                asking to be restarted (consoleSessionResting is the
                only thing grain says in a session's conversation). */}
                {offerFresh && (
                  <Button
                    size="small"
                    variant="text"
                    onClick={() => setRestartOpen((open) => !open)}
                    aria-expanded={restartOpen}
                    aria-controls={`${session.id}-fresh-content`}
                    id={`${session.id}-fresh-header`}
                  >
                    Start a fresh chat
                  </Button>
                )}
                <Button
                  size="small"
                  variant="text"
                  color="error"
                  disabled={deleting}
                  onClick={handleDelete}
                >
                  Delete session
                </Button>
              </>
            }
          >
            <WorkTree t={task} tasks={tasks} onOpenTask={onOpenTask} bare />
            {/* Outside the capabilities panel, so closing the panel
            does not hide the one thing saying the change has not
            landed yet. */}
            {runIsStale && (
              <Alert
                severity="warning"
                className="console-session-pending"
                action={
                  <Button
                    color="inherit"
                    size="small"
                    disabled={applying}
                    onClick={applyNow}
                  >
                    Apply now
                  </Button>
                }
              >
                The run open on this session started before your change and does
                not have it: capabilities, repository access, network access and
                the model are fixed when a run starts. Apply now stops this run;
                the conversation is kept, and your next message starts a fresh
                run with what is saved here.
              </Alert>
            )}
            {restartOpen && offerFresh && renderFreshOffer()}
            {capabilitiesOpen && renderCapabilitiesContent()}
          </TaskChat>
        ) : (
          /* Configured, but grain has not filed its task yet -- the gap
           between a session appearing in the settings and the next
           cycle's reconciler filing the task its conversation lives on.
           It is a wait rather than a failure, and saying so is better
           than an empty chat window that swallows what somebody types
           into it. */
          <>
            <Alert severity="info">
              grain files this session&apos;s task on its next cycle. The chat
              opens as soon as it exists.
            </Alert>
            <Box sx={{ mt: 1, display: "flex", gap: 1 }}>
              <Button
                size="small"
                variant="text"
                onClick={() => setCapabilitiesOpen((open) => !open)}
                aria-expanded={capabilitiesOpen}
                aria-controls={`${session.id}-capabilities-content`}
                id={`${session.id}-capabilities-header`}
                sx={TITLED_BUTTON_SX}
              >
                {capabilitiesOpen ? "Hide" : "Edit"} {session.title}{" "}
                capabilities
              </Button>
              <Button
                size="small"
                variant="text"
                color="error"
                disabled={deleting}
                onClick={handleDelete}
              >
                Delete session
              </Button>
            </Box>
            {capabilitiesOpen && renderCapabilitiesContent()}
          </>
        )}
      </div>
    </div>
  );
}
