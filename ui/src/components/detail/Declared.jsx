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
import { Alert, Button } from "@mui/material";
import api from "../../api.js";
import {
  connectionLabel,
  defaultModels,
  deploymentRow,
  frameworkLabel,
  NO_MODELS_LABEL,
  pinnedModels,
  PROVIDER_DEFAULT_LABEL,
  providerDefaultModel,
} from "../../state.js";
import { GlyphLabel } from "../ItemGlyph.jsx";
import { DeclaredPushRefs } from "../Declared.jsx";

// How the Model row names the declaration that chose a task's model,
// keyed by model.Task.ModelSource (model.CompositionLayer).
const MODEL_SOURCES = {
  template: "from the template",
  schedule: "from the schedule",
};

// How the Network access row names model.Task.NetworkAccess, in the
// words NewTaskOverlay's own picker offers it under.
const NETWORK_ACCESS_LABELS = {
  open: "Egress (outbound internet only)",
  none: "None (this deployment only)",
};

// Real columns on the task now, not directive lines parsed out of a
// body -- so they are rendered as fields rather than as the /repo,
// /base, /auto-merge syntax they used to have to be written in.
//
// What is left here is what nothing on this page can edit yet: the
// sandbox shape, the framework, the prompt extension and the review.
// Every row this used to open with -- repo, base,
// reads, auto-merge -- is now the control that sets it, above (Target,
// ReadOnlyRepos) or in Actions (the auto-merge checkbox), and a static
// row repeating one of them would be the same field on screen twice in a
// 260px column.
//
// The one exception is the button under the sandbox-shape warning
// (grain/task-41): not an editor for those rows, which stay read-only,
// but a way out of the single case where what they display stops the
// task running at all.
export default function Declared({ t, config, act }) {
  // clearing is the "the PATCH is in flight" state of the one control
  // this otherwise read-only column has (see shapeRefused below). Held
  // here rather than left to act(), which reports and refreshes but says
  // nothing while it is working, so a slow request cannot be clicked
  // twice.
  const [clearing, setClearing] = useState(false);
  const rows = [];
  // The deployment's own settings (config.deployment), whose
  // framework, pinned connection and default model a task that names
  // none of its own runs on.
  const deployment = deploymentRow(config);

  // bwsalmon/agents#534, grain/task-41: a per-task sandbox shape
  // override, shown only when set -- most tasks use the deployment
  // default and have none of the three fields, the same "0 means unset"
  // convention that keeps them out of the JSON response at all (Task's
  // own omitempty).
  if (t.sandboxCpus) rows.push(["Sandbox vCPUs", String(t.sandboxCpus)]);
  if (t.sandboxMemoryMb)
    rows.push(["Sandbox memory (MiB)", String(t.sandboxMemoryMb)]);
  if (t.sandboxDiskGb)
    rows.push(["Sandbox disk (GiB)", String(t.sandboxDiskGb)]);
  // The per-task network override the new-task form offers beside the
  // shape, shown only when set and named the way that form names it --
  // empty is the deployment's own answer (model.Task.NetworkAccess). It
  // had no row at all, so a task filed with no network said so nowhere
  // on its own page.
  if (t.networkAccess)
    rows.push([
      "Network access",
      NETWORK_ACCESS_LABELS[t.networkAccess] || t.networkAccess,
    ]);
  // grain/task-26: those three rows on a deployment whose sandboxes have
  // no shape of their own (config.sandboxShapeIgnored) are not a shape
  // this run will get -- HostSandboxes.Acquire refuses a non-zero one
  // rather than ignoring it, so every attempt at this task dies at
  // sandbox setup. Said here because the new-task form only stops the
  // next task being filed this way: a task filed before the deployment
  // moved onto host sandboxes, or through the API, arrives here with an
  // override and nothing else on the page explains why it will not run.
  const shapeRefused =
    !!config?.sandboxShapeIgnored &&
    !!(t.sandboxCpus || t.sandboxMemoryMb || t.sandboxDiskGb);
  // grain/task-41: and clearing it is the fix, not filing the task
  // again. PATCH /api/tasks/{id} has carried these three fields all
  // along, and ui.Client.UpdateTask refuses only the shape a request
  // *states*, so three zeroes in one request are accepted on exactly the
  // deployment this warning appears on (ui.Client.refuseIgnoredSandboxShape).
  // All three go together because the override is one thing: a task left
  // with only its disk override cleared still fails at sandbox setup.
  // Losing a task's comments, links and history to unset three numbers
  // was the worse trade.
  const clearShape = async () => {
    setClearing(true);
    try {
      await act(
        () =>
          api(`/api/tasks/${t.id}`, {
            method: "PATCH",
            body: JSON.stringify({
              sandboxCpus: 0,
              sandboxMemoryMb: 0,
              sandboxDiskGb: 0,
            }),
          }),
        t.id,
      );
    } finally {
      setClearing(false);
    }
  };
  // Same treatment for a per-task agent framework: shown only when this
  // task overrides the deployment's own, which most do not.
  if (t.agentFramework)
    rows.push(["Agent framework", frameworkLabel(t.agentFramework)]);
  // A task that named no framework or model of its own runs on its
  // team's, where the team set one (grain/task-1427) -- the same rule
  // dispatch applies in cmd/grain's agentFrameworks, so this pane says
  // what the run will actually use rather than "Rotation".
  const teamDefaults =
    !t.agentFramework && !t.agentBackendId && deployment ? deployment : null;
  if (teamDefaults?.agentFramework)
    rows.push([
      "Agent framework",
      `${frameworkLabel(teamDefaults.agentFramework)} (deployment default)`,
    ]);

  // The model and the connection are two rows, as they are two choices
  // (docs/connections-and-models.md), and the model row says what
  // dispatch (cmd/grain's agentPool.resolve) will actually ask for: the
  // task's own model; else, for a task pinned to a connection (its own
  // pin, or its team's when it named nothing), that connection's first
  // advertised model; else its team's default; else the rotation, where
  // each connection serves its own first model and the honest answer is
  // "one of these". The connection row is only there for a pinned task:
  // every other one runs on whichever enabled connection serves its
  // model.
  //
  // Every read of `config` here is optional because App.jsx holds it as
  // `useState(null)` and fills it from /api/config, while this pane is
  // opened from /tasks/:id by a mount effect of its own that only waits
  // on /api/tasks/:id. Nothing orders those two responses, so a page
  // loaded straight at a task can render this with no config at all --
  // rare, because /api/config is small and usually home first, and rare
  // in exactly the load-dependent way that makes it a flake rather than
  // a bug report (grain/task-182: two `ui e2e` runs red on two different
  // specs, both of them an assertion on this pane finding no element,
  // because this line threw and React unmounted the tree under it).
  const connections = config?.agentBackends || [];
  const connectionName = (id) => {
    // The bare id is the fallback for a connection removed in Settings
    // since this task was pinned -- the same one the run itself will fail
    // naming, so the pane should not pretend it is on the rotation.
    const conn = (config?.agentBackends || []).find((b) => b.id === id);
    return conn ? connectionLabel(conn) : id;
  };
  const pinID = t.agentBackendId || teamDefaults?.agentBackendId || "";
  const pinnedConn = (config?.agentBackends || []).find((b) => b.id === pinID);
  // A model a template or a schedule chose says so (model.Task.ModelSource),
  // the way the team default does; a bare model is the task's own choice,
  // or one filed before grain recorded where a model came from.
  const defaults = defaultModels(deployment);
  if (t.model) {
    const source = MODEL_SOURCES[t.modelSource];
    rows.push(["Model", source ? `${t.model} (${source})` : t.model]);
  } else if (pinID) {
    const served = providerDefaultModel(pinnedConn);
    // A model the connection pins is the operator's choice; one its
    // provider defaults to is nobody's, and says so.
    rows.push([
      "Model",
      !served
        ? NO_MODELS_LABEL
        : pinnedModels(pinnedConn).length
          ? `${served} (the connection's own)`
          : `${served} (${PROVIDER_DEFAULT_LABEL})`,
    ]);
  } else if (defaults.length > 0) {
    rows.push(["Model", `deployment default (${defaults.join(" → ")})`]);
  } else if (connections.length > 0) {
    rows.push(["Model", `Rotation · ${PROVIDER_DEFAULT_LABEL}`]);
  }
  if (t.agentBackendId) {
    rows.push(["Connection", connectionName(t.agentBackendId)]);
  } else if (teamDefaults?.agentBackendId) {
    rows.push([
      "Connection",
      `${connectionName(teamDefaults.agentBackendId)} (deployment default)`,
    ]);
  }

  // Same treatment again for a task that replaces the standing
  // instructions its deployment and repo would otherwise give its runs
  // (grain/task-114): shown only when it has one, since most tasks do
  // not and an empty row would suggest they had opted out of something.
  if (t.promptExtension) rows.push(["Prompt extension", t.promptExtension]);
  // The review attached to this task (grain/task-284), shown only when
  // there is one -- and saying whether it has been filed yet, since
  // "this will be reviewed once it is done" and "task 91 is reviewing it
  // right now" are the two different things this row means before and
  // after orchestrator.SyncReviews acts on it. The name is what somebody
  // picked it by; the bare id is the fallback for a template that has
  // since gone (ui.Task.reviewTemplateName).
  //
  // It is the one value in this column that names a template rather
  // than describing this task, so it carries the templates glyph
  // (ItemGlyph.jsx): "this came out of a template" said by the same
  // figure the Templates page and the nav rail use, in front of the
  // name it was picked by.
  if (t.reviewTemplateId) {
    const name = t.reviewTemplateName || t.reviewTemplateId;
    rows.push([
      "Review",
      <GlyphLabel kind="templates" key="review">
        {t.reviewTask ? `${name} (task ${t.reviewTask})` : name}
      </GlyphLabel>,
    ]);
  }
  // Shown only off its default, like the rows above: closing a task
  // whose agent found nothing to do is what every task does unless
  // somebody chose otherwise.
  if (t.nothingNeededAction === "ack") {
    rows.push([
      "When the agent finds nothing to do",
      "Leave for acknowledgement",
    ]);
  }
  if (t.creator) rows.push(["Creator", t.creator]);
  if (t.reviewers?.length) {
    rows.push(["Reviewers", t.reviewers.join(", ")]);
  }
  // What this task's runs may push besides their own branch, by the
  // `push:` of the repository template it was filed from (grain/task-2536)
  // -- shown only for the tasks that have one.
  if (t.declaredPush) {
    rows.push([
      "May also push",
      <DeclaredPushRefs push={t.declaredPush} key="declared-push" />,
    ]);
  }
  // Most tasks override none of the above, so this block is usually
  // empty now that the four fields every task has have moved to their
  // own editors -- an empty bordered box saying nothing is worse than no
  // box at all.
  if (rows.length === 0) return null;
  return (
    <div className="declared">
      {rows.map(([key, value]) => (
        <div className="declared-row" key={key}>
          <span className="declared-key">{key}</span>
          <span className="declared-value">{value}</span>
        </div>
      ))}
      {shapeRefused && (
        <Alert severity="warning" sx={{ fontSize: "0.8rem" }}>
          This deployment&apos;s sandboxes are directories on the daemon&apos;s
          own machine, which have no shape of their own, so this task fails at
          sandbox setup rather than running unshaped. Clear the override and it
          runs like any other task here.
          <Button
            size="small"
            color="inherit"
            disabled={clearing}
            onClick={clearShape}
            sx={{ display: "block", mt: 0.5, ml: -1 }}
          >
            {clearing ? "Clearing…" : "Clear sandbox shape"}
          </Button>
        </Alert>
      )}
    </div>
  );
}
