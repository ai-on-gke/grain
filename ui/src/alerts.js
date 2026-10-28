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

import api from "./api.js";
import { formatDateTime, formatTime } from "./time.js";
import { hasGitCredential } from "./gitConnections.js";
import { redBaseRepairSentence, waitingBaseRepair } from "./state.js";
import { forceMergeOffered } from "./taskWrites.js";

// alerts.js is every standing warning about the deployment as rows of one
// list, read from /api/config: what the alerts button in the top bar
// counts and its panel lists (components/AlertsButton.jsx,
// grain/task-4008).
//
// Each of these used to be a banner of its own across the top of every
// page (BannerStrip). That was overbearing, and it did not scale: a
// deployment with several teams, each with several model connections,
// can have half a dozen pauses at once, and a stack of red and orange
// strips pushed every page down the window to say so. Only the
// connection to the daemon being lost is still a banner
// (DisconnectedBanner.jsx), because while it is, nothing below it --
// this list included -- can be trusted.
//
// The sentences each banner said live here too, so the places that say
// the same thing elsewhere -- a queued task's pane (detail/Actions.jsx),
// Team settings' connections, the Inbox's state-sync row -- keep saying
// it in the same words.

// SEVERITY_RANK orders severities worst first. Three, matching MUI's own
// Alert colours (critical is MUI's "error"):
//
//   - critical: grain itself is not doing its job and will not recover
//     on its own -- the reconcile loop has stopped (no task is
//     dispatched or checked until the daemon restarts), or settings have
//     stopped reaching the state repository (the next restart imports a
//     stale export over everything changed since). Somebody has to act.
//   - warning: grain is deliberately holding work, or is configured in a
//     way somebody should know about -- a usage-limit, deployment or team
//     pause, or runs that execute on this host with nothing isolating
//     them. Nothing is broken and every pause lifts or can be lifted, but
//     the queue is not moving where it looks as if it should.
//   - info: something is unfinished rather than wrong -- a team's setup
//     steps left. Its tasks may wait on it, but nobody has been surprised.
export const SEVERITY_RANK = { critical: 0, warning: 1, info: 2 };

// muiSeverity is the MUI Alert / palette colour of a severity.
export function muiSeverity(severity) {
  return severity === "critical" ? "error" : severity;
}

// worstSeverity is the most severe of alerts, or null for none.
export function worstSeverity(alerts) {
  let worst = null;
  for (const a of alerts || []) {
    if (worst === null || SEVERITY_RANK[a.severity] < SEVERITY_RANK[worst])
      worst = a.severity;
  }
  return worst;
}

// sortAlerts is severity first, then the most recent first; an alert with
// no known start sorts after those that have one.
export function sortAlerts(alerts) {
  const at = (a) => (a.since ? Date.parse(a.since) || 0 : 0);
  return [...alerts].sort(
    (a, b) =>
      SEVERITY_RANK[a.severity] - SEVERITY_RANK[b.severity] || at(b) - at(a),
  );
}

// ---- Usage-limit pauses (config.agentPause; ui.AgentPauseStatus,
// orchestrator.Pause behind it).

// liftPath is the DELETE /api/pause that lifts entry and nothing else.
export function liftPath(entry) {
  const q = new URLSearchParams();
  if (entry?.connection) {
    q.set("connection", entry.connection);
    if (entry.model) q.set("model", entry.model);
  } else if (entry?.team) {
    q.set("team", entry.team);
    if (entry.framework) q.set("framework", entry.framework);
  }
  const query = q.toString();
  return query ? `/api/pause?${query}` : "/api/pause";
}

// liftAgentPause lifts one usage-limit pause. The server decides who may
// (pkg/ui/pause.go: that team's admins, or a system admin); entry.canLift
// is its answer, ahead of time, to whether this viewer is one.
export function liftAgentPause(entry) {
  return api(liftPath(entry), { method: "DELETE" });
}

// pauseSubject names what one pause stops: one model on a connection, a
// whole connection, or this deployment's runs on a framework that spent
// no connection's key (the deployment-level credentials).
export function pauseSubject(entry) {
  if (entry?.connection) {
    const name = entry.connectionName || entry.connection;
    if (entry.model) return `${entry.model} on ${name}`;
    return `connection ${name}`;
  }
  if (entry?.framework) return `this deployment's ${entry.framework} runs`;
  return "";
}

// pauseMessage is one pause's sentence: what has stopped, until when, and
// -- last -- what the provider itself said, which names the window and is
// worth keeping verbatim rather than paraphrasing.
//
// The instant is shown as a wall-clock time as well as as a remaining
// duration: a countdown answers "is this nearly over?", and a clock time
// is what somebody deciding whether to wait or to go to lunch actually
// plans around. On the deployment's clock (grain/task-368), like every
// other absolute time on screen -- zone empty is the browser's own,
// which is what a caller with no provider above it gets.
//
// A pause on one model says so first, as the model on its connection --
// "gemini-3-pro on Gemini (deployment default) paused until 3:40 PM — usage
// limit" -- since the connection's other models are still dispatching;
// one escalated to the whole connection says why.
export function pauseMessage(entry, zone = "") {
  const until = entry?.until ? formatTime(entry.until, zone) : "";
  const when = until
    ? `${until} (${formatRemaining(entry.secondsRemaining)})`
    : "the provider's window resets";
  const reason = entry?.reason ? ` — ${entry.reason}` : "";
  const subject = pauseSubject(entry);
  if (entry?.connection && entry.model) {
    return `${subject} paused until ${when} — usage limit; the connection's other models keep dispatching.${reason}`;
  }
  if (entry?.connection && entry.escalated) {
    return `Agent usage limit reached: nothing is being dispatched onto ${subject}, on any model, until ${when} (${entry.escalated}).${reason}`;
  }
  const what = subject
    ? `nothing is being dispatched onto ${subject}`
    : "nothing is being dispatched";
  return `Agent usage limit reached: ${what} until ${when}.${reason}`;
}

// pausedUntil is the same pause as a clause for a place that already
// names what is paused -- a connection on Team settings, a queued task's
// pane: "paused until 10:58 PM (about 5h 59m) — <reason>".
export function pausedUntil(pause, zone = "") {
  const until = pause?.until ? formatTime(pause.until, zone) : "";
  const when = until
    ? `until ${until} (${formatRemaining(pause.secondsRemaining)})`
    : "until the provider's window resets";
  const reason = pause?.reason ? ` — ${pause.reason}` : "";
  return `paused ${when}${reason}`;
}

// formatRemaining renders a countdown coarsely, in hours and minutes.
//
// Deliberately not the report's own formatSeconds, which carries
// seconds under an hour: this number is re-read every poll, and text
// that changes every three seconds flickers while somebody is trying to
// read it. Nothing is decided on the last minute of a five-hour window
// anyway.
export function formatRemaining(seconds) {
  const s = Math.max(0, Math.round(seconds || 0));
  if (s < 60) return "less than a minute";
  const hours = Math.floor(s / 3600);
  const minutes = Math.floor((s % 3600) / 60);
  if (hours === 0) return `about ${minutes}m`;
  return `about ${hours}h ${minutes}m`;
}

// ---- The deployment's own pause (config.pipelinePause;
// ui.PipelinePauseStatus, model.PipelinePause behind it).

// scheduledUpgradeHold is whether this pause is the one a scheduled
// upgrade keeps while it waits for the deployment to go idle
// (model.ScheduledUpgradePrincipal), as against an operator's own or the
// upgrade check's. Recognised by the principal rather than by the reason,
// which is prose and is allowed to change.
export function scheduledUpgradeHold(pause) {
  return pause?.byKind === "automation" && pause?.byId === "upgrade-schedule";
}

// resumeCancelsUpgrade is what Resume asks before it lifts that hold. It
// says what will be lost rather than what will happen, because "resume
// the pipeline" is what the operator already believes they are doing and
// the upgrade is the part they have not thought about.
export function resumeCancelsUpgrade(pause) {
  return (
    `This deployment is paused because it is waiting to upgrade${
      pause?.reason ? ` — ${pause.reason}` : ""
    }.\n\n` +
    "Resuming cancels that upgrade: tasks start again and the upgrade " +
    "will not be applied. Schedule it again from Settings → Upgrade."
  );
}

// resumePipeline lifts the deployment's pause -- after asking, when that
// would throw a scheduled upgrade away (ui.Client.ResumePipeline, and why
// it has to: the reconciler would re-take this hold within seconds
// otherwise). A dialog that only appears when something is actually at
// stake is one that still gets read. Resolves false when the operator
// said no.
export async function resumePipeline(pause) {
  if (scheduledUpgradeHold(pause) && !confirm(resumeCancelsUpgrade(pause)))
    return false;
  await api("/api/pipeline-pause", { method: "DELETE" });
  return true;
}

// pipelinePauseMessage is the deployment pause's sentence: that nothing
// is being picked up, since when, why, and -- for grain's own pause
// around an upgrade check -- that it lifts itself when that check is
// done, because reaching for Resume on a pause that is about to end on
// its own is exactly the mistake this sentence can prevent.
//
// Runs already in flight are named as still running because that is the
// question an operator asks next, and the answer is not obvious: pausing
// stops the queue being drained, it does not cancel work under way.
export function pipelinePauseMessage(pause, zone = "") {
  const since = pause?.since ? ` since ${formatTime(pause.since, zone)}` : "";
  const reason = pause?.reason ? ` — ${pause.reason}` : "";
  const waiting = pause?.untilTask
    ? ` It lifts itself when task ${pause.untilTask} finishes.`
    : scheduledUpgradeHold(pause)
      ? " The upgrade starts once they have, and Resume now cancels it."
      : "";
  // Manual mode (model.PipelinePause.AdmitsRunNow): what somebody starts
  // with Run now still runs under every pause but an upgrade's hold.
  const manual = pause?.runNowPasses
    ? " Run now still starts the task it is pressed on."
    : " Run now waits for the upgrade too: its restart would discard the run.";
  return (
    `No new tasks are being picked up${since}: this deployment is paused. ` +
    `Runs already in flight are left to finish.${manual}${waiting}${reason}`
  );
}

// pipelinePauseTitle is the banner's headline. Under a pause Run now
// passes, it says the one thing that still starts work.
export function pipelinePauseTitle(pause) {
  return pause?.runNowPasses
    ? "Paused — only tasks you Run now will start"
    : "Deployment paused";
}

// heldForRunNow is whether task t is waiting only because of the pause:
// the deployment is paused under a pause Run now passes, and t is queued
// -- one the queue would otherwise pick up -- and not already asked for,
// individually paused, exempt by priority-dispatch / standing session,
// or the task the pause itself is waiting on. A blocked task is not
// waiting only for the pause -- lifting it would not start the task
// either -- so it says "blocked", not this.
export function heldForRunNow(pause, t) {
  if (!pause?.paused || !pause.runNowPasses || !t) return false;
  if (
    t.runNowAt ||
    t.paused ||
    t.blocked ||
    t.consoleSession ||
    (t.capabilities || []).includes("priority-dispatch") ||
    pause.untilTask === t.id
  ) {
    return false;
  }
  return t.state === "queued";
}

// ---- Settings not reaching the state repository
// (config.stateRepoSyncFailure; ui.StateRepoSyncFailure).

// STATE_SYNC_TITLE is what the alert and the Inbox's group
// (InboxPage.jsx's StateSyncGroup) both call it.
export const STATE_SYNC_TITLE = "Settings are not being exported";

// stateSyncSentence is the one sentence both the alert and the Inbox row
// say about config.stateRepoSyncFailure: which repository the settings
// are not reaching, since when, and why.
export function stateSyncSentence(failure, zone = "") {
  const repo = failure.repo || "the state repository";
  // On the reader's clock (TimeZoneContext), as every other time on the
  // page is, rather than the browser's own.
  const since = failure.since
    ? formatDateTime(failure.since, zone) || "an unknown time"
    : "an unknown time";
  return `Settings have not been exported to ${repo} since ${since}: ${failure.error}`;
}

// ---- The deployment's setup steps left.

// deploymentSetupStepsLeft is what the deployment still lacks before its
// tasks can run, in the order it is set up: a git connection (the same
// rule as the "no GitHub credential" notice, grain/task-3079), an
// enabled model connection, and a repository to work in. Each step names
// the Settings tab that fixes it (`tab`) or, for a repository, which is
// added from the Repositories page, the view (`view`). `deployment` is
// the default team's row, where stage 1 of the single-tenant reset keeps
// these; its config repository is grain's own and not counted.
//
// The team checklist's optional steps -- adding members and picking a
// default model -- were never counted here, and their "Not needed" note
// (onboardingDismissed) was written with PUT /api/teams/{id}, which is
// gone (single-tenant reset 3e-iv-b2).
export function deploymentSetupStepsLeft(deployment, config) {
  if (!deployment) return [];
  const repos = new Set(deployment.targetRepos || []);
  for (const r of deployment.repos || []) if (r?.repo) repos.add(r.repo);
  const steps = [
    {
      id: "git",
      label: "Connect a git host",
      done: hasGitCredential(deployment),
      tab: "git",
    },
    {
      id: "connections",
      label: "Add a model connection",
      done: (config?.agentBackends || []).some((b) => !b.disabled),
      tab: "models",
    },
    {
      id: "repos",
      label: "Add a repository",
      done: repos.size > 0,
      view: "repos",
    },
  ];
  return steps.filter((s) => !s.done);
}

// setupAlertText is the setup alert's title: "2 setup steps left".
export function setupAlertText(left) {
  return `${left} setup ${left === 1 ? "step" : "steps"} left`;
}

// HOST_SANDBOXES_TITLE is the "no isolation" alert's title
// (config.hostSandboxes).
export const HOST_SANDBOXES_TITLE = "No isolation: runs execute on this host";

// collectAlerts is every standing alert config describes for this viewer,
// sorted (sortAlerts). Each is
//
//   { id, severity, title, sentence, scope, since?, until?, impact?, actions }
//
// where scope is { kind: "deployment" } or { kind: "connection",
// connection, connectionName }, and each action is { kind, label, ... }
// for the panel to carry out (AlertsButton.jsx).
//
// tasks is the task list, read for the one alert that is about a task
// rather than config: a base branch that is red while its repair pull
// request waits (redBaseRepairAlerts).
export function collectAlerts(config, zone = "", tasks = []) {
  if (!config) return [];
  const out = [...redBaseRepairAlerts(config, tasks)];

  if (config.reconcilerDown) {
    out.push({
      id: "reconciler-down",
      severity: "critical",
      title: "Reconcile loop stopped",
      sentence:
        "This deployment's reconcile loop has stopped -- no tasks are being dispatched or checked. Restart the daemon to recover.",
      scope: { kind: "deployment" },
      impact: "No task is dispatched or checked until the daemon restarts.",
      actions: [],
    });
  }

  const sync = config.stateRepoSyncFailure;
  if (sync) {
    out.push({
      id: "state-sync-failing",
      severity: "critical",
      title: STATE_SYNC_TITLE,
      sentence: `${stateSyncSentence(sync, zone)}.`,
      scope: { kind: "deployment" },
      since: sync.since || undefined,
      impact:
        "Changes made in the UI since then exist only in this deployment's database until the sync is fixed.",
      // The Inbox row's own button. The access code is owner access, so
      // whoever reads this can open Settings → Config repo (settings-panel.md §6.1).
      actions: [{ kind: "open-config-repo", label: "Config repo" }],
    });
  }

  // Not an incident: host sandboxes are a deliberate choice on a
  // workstation one person owns (-host-sandboxes, which the daemon refuses
  // on one others can reach unless told otherwise). But nothing else on
  // screen distinguishes a deployment whose runs have no isolation at all
  // from one giving each a VM or container, and it stays true until the
  // daemon is restarted onto another backend -- so it stands for as long
  // as config.hostSandboxes does, for everybody filing work here.
  if (config.hostSandboxes) {
    out.push({
      id: "host-sandboxes",
      severity: "warning",
      title: HOST_SANDBOXES_TITLE,
      sentence:
        "Every dispatched run executes in a plain directory on the machine this daemon runs on, as its user, with that machine's filesystem, network and credentials -- nothing isolates an agent from it, and what a run changes outside its directory is not thrown away with it.",
      scope: { kind: "deployment" },
      impact:
        "Do not file work here you would not run on this machine yourself. Restart the daemon with -container-sandboxes or -granule-sandboxes (or re-run scripts/setup.sh without GRAIN_HOST_SANDBOXES) to isolate each run.",
      actions: [],
    });
  }

  // One alert per pause the server reports -- a connection, or the
  // deployment's runs on a framework -- so that per-connection pauses each get their
  // own row, scope and Resume button. An older daemon's single
  // deployment-wide pause (no pauses list) is one row of its own.
  const agentPause = config.agentPause;
  if (agentPause?.paused) {
    const entries = agentPause.pauses?.length
      ? agentPause.pauses
      : [agentPause];
    entries.forEach((entry, i) => {
      const key = entry.connection
        ? entry.connection + (entry.model ? `/${entry.model}` : "")
        : entry.framework || String(i);
      const scope = entry.connection
        ? {
            kind: "connection",
            connection: entry.connection,
            connectionName: entry.connectionName || entry.connection,
          }
        : { kind: "deployment" };
      const actions = [];
      if (entry.canLift)
        actions.push({ kind: "lift-agent-pause", label: "Resume now", entry });
      if (entry.connection)
        actions.push({
          kind: "open-connection",
          label: "Open connection",
          connection: entry.connection,
        });
      out.push({
        id: `agent-pause/${entry.team || ""}/${key}`,
        severity: "warning",
        title: entry.model
          ? `Usage limit reached (${entry.model})`
          : entry.framework
            ? `Usage limit reached (${entry.framework})`
            : "Usage limit reached",
        sentence: (zone) => pauseMessage(entry, zone),
        scope,
        until: entry.until || undefined,
        impact: "Runs in flight on it were cancelled; queued work waits.",
        actions,
      });
    });
  }

  const pipeline = config.pipelinePause;
  if (pipeline?.paused) {
    out.push({
      id: "pipeline-pause",
      severity: "warning",
      title: pipelinePauseTitle(pipeline),
      sentence: (zone) => pipelinePauseMessage(pipeline, zone),
      scope: { kind: "deployment" },
      since: pipeline.since || undefined,
      actions: [
        { kind: "resume-pipeline", label: "Resume now", pause: pipeline },
      ],
    });
  }

  // What the deployment still lacks before its tasks can run
  // (deploymentSetupStepsLeft). Every reader the UI serves is the
  // deployment's owner, so it is shown to whoever reads it. It linked to
  // the team home page's checklist until stage 3e-ii; it now opens the
  // Settings tab (or the Repos page) that fixes the first step left.
  const deployment = config.deployment;
  if (deployment && !deployment.archived) {
    const missing = deploymentSetupStepsLeft(deployment, config);
    if (missing.length > 0) {
      const blocking = missing.some(
        (s) => s.id === "git" || s.id === "connections",
      );
      const first = missing[0];
      out.push({
        id: "setup",
        severity: "info",
        title: setupAlertText(missing.length),
        sentence: `Still to do: ${missing.map((s) => s.label.toLowerCase()).join(", ")}.`,
        scope: { kind: "deployment" },
        impact: blocking
          ? "Tasks wait until the deployment has a git host and a model connection."
          : undefined,
        actions: [
          {
            kind: "open-setup",
            label: "Finish setup",
            tab: first.tab || null,
            view: first.view || null,
          },
        ],
      });
    }
  }

  return sortAlerts(out);
}

// redBaseRepairAlerts is a critical row for each base branch that is red
// while its repair pull request waits on anything (state.js's
// waitingBaseRepair), whether base_repair_bypasses_human_gates is on or
// off: until it merges, other work targeting that branch waits behind it.
// This was a filled strip across the top of every page
// (RedBaseRepairBanner) until it joined the rest here; its buttons came
// with it.
export function redBaseRepairAlerts(config, tasks) {
  return (tasks || []).filter(waitingBaseRepair).map((t) => {
    const actions = [
      { kind: "open-task", label: `Open #${t.id}`, taskId: t.id },
    ];
    if (!t.autoMerge)
      actions.push({ kind: "submit-task", label: "Submit", taskId: t.id });
    if (forceMergeOffered(config, t))
      actions.push({ kind: "force-merge-task", label: "Force merge", task: t });
    return {
      id: `red-base-repair/${t.id}`,
      severity: "critical",
      title: `\`${t.redBaseBranch}\` is red — repair #${t.id}`,
      sentence: redBaseRepairSentence(t),
      scope: { kind: "deployment" },
      impact: `Other work targeting ${t.redBaseBranch} waits until the repair merges.`,
      actions,
    };
  });
}

// alertSentence is an alert's sentence, on the deployment's clock where
// it names a time.
export function alertSentence(alert, zone = "") {
  return typeof alert.sentence === "function"
    ? alert.sentence(zone)
    : alert.sentence;
}

// scopeLabel is where an alert applies, in words: "Deployment", or
// "Connection Alpha's Claude".
export function scopeLabel(scope) {
  if (!scope || scope.kind !== "connection") return "Deployment";
  return `Connection ${scope.connectionName || scope.connection}`;
}
