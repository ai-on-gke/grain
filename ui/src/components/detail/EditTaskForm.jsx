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

import { useEffect, useRef, useState } from "react";
import { Button, MenuItem, Stack, TextField } from "@mui/material";
import api from "../../api.js";
import {
  connectionLabel,
  connectionsServingModels,
  defaultModels,
  deploymentRow,
  modelOptions,
  connectionModels,
  teamDefaultLabel,
} from "../../state.js";
import ModelSelect from "../ModelSelect.jsx";

// EditTaskForm is the title/description editor DetailOverlay swaps its
// header for (bwsalmon/agents#523) -- those two fields are the only ones
// BuildPrompt actually hands a dispatched run, so an edit to either has
// to reach one already running, not just show up here; PATCH /api/tasks/
// {id} (ui.Client.UpdateTask) is what records that as an addendum
// comment (noteEdit) for orchestrator.addendaPoller to pick up. Every
// other UpdateTaskRequest field has its own editor on this same page --
// repo and base in Target, auto-merge in Actions, and reads,
// capabilities and dependencies in the three pickers below them -- so
// this form does not attempt to cover them too.
export default function EditTaskForm({
  t,
  config,
  templates,
  act,
  onDone,
  unsavedRef,
}) {
  const [title, setTitle] = useState(t.title);
  const [description, setDescription] = useState(t.description || "");
  // The review attached to this task (grain/task-284). Editable here and
  // not only on the new-task form because this is where the decision
  // usually gets made: a task is filed, and somewhere between queued and
  // finished somebody decides its change is one they want a second agent
  // over before it merges. An edit reaches any review not yet filed --
  // orchestrator.SyncReviews resolves this once the task's own work is
  // done rather than at creation -- which is why the picker locks once
  // there is a review task to point at.
  const [reviewTemplateId, setReviewTemplateId] = useState(
    t.reviewTemplateId || "",
  );
  const savedReviewers = (t.reviewers || []).join(", ");
  const [reviewers, setReviewers] = useState(savedReviewers);
  const reviewFiled = Boolean(t.reviewTask);
  // Which model this task's next run is driven by (grain/task-110),
  // editable here for the reason the review above is: the decision is
  // usually made after the task was filed. A job that ran out of turns on
  // the everyday model is the one somebody moves onto the expensive one,
  // and this pane is where they are standing when they decide it.
  //
  // model is ui.UpdateTaskRequest.Model, "" being the team's default;
  // agentBackendId is the optional connection pin beside it, "" letting
  // dispatch round-robin across the team's enabled connections that serve
  // the model -- the same pair NewTaskOverlay files a task with
  // (docs/connections-and-models.md).
  const [model, setModel] = useState(t.model || "");
  const [agentBackendId, setAgentBackendId] = useState(t.agentBackendId || "");
  // What a run ending on nothing_needed does to this task
  // (model.Task.NothingNeededAction); "" on the wire is "close".
  const savedNothingNeeded = t.nothingNeededAction || "close";
  const [nothingNeededAction, setNothingNeededAction] =
    useState(savedNothingNeeded);
  // The deployment's connections and default model: a task names no
  // team of its own.
  const connections = config?.agentBackends || [];
  const models = connectionModels(connections);
  const deployment = deploymentRow(config);
  const teamDefaultModels = defaultModels(deployment);
  const teamDefaultModel = deployment?.defaultModel || "";
  // The model(s) a pin is checked against: the one chosen here, or else the
  // team's default when it has one. A pin must advertise it or the
  // server refuses the pair, so only those connections are offered.
  const effectiveModels = model ? [model] : teamDefaultModels;
  const effectiveModel = effectiveModels.join(" → ");
  const pinChoices = connectionsServingModels(
    models,
    connections,
    effectiveModels,
  );
  // The pin this task already carries stays selectable even where it
  // does not advertise the model above -- a task pinned before models
  // and connections were separated -- so saving an unrelated edit does
  // not quietly take it off.
  const savedPin =
    agentBackendId &&
    agentBackendId === t.agentBackendId &&
    !pinChoices.some((c) => c.id === agentBackendId)
      ? connections.find((c) => c.id === agentBackendId) || {
          id: agentBackendId,
          missing: true,
        }
      : null;

  const isDirty = () => {
    if (title !== t.title) return true;
    if (description !== (t.description || "")) return true;
    if (reviewTemplateId !== (t.reviewTemplateId || "")) return true;
    if (reviewers !== savedReviewers) return true;
    if (nothingNeededAction !== savedNothingNeeded) return true;
    if (model !== (t.model || "")) return true;
    if (agentBackendId !== (t.agentBackendId || "")) return true;
    return false;
  };
  const isDirtyRef = useRef(isDirty);
  isDirtyRef.current = isDirty;
  useEffect(() => {
    if (!unsavedRef) return;
    unsavedRef.current = () => isDirtyRef.current();
    return () => {
      unsavedRef.current = null;
    };
  }, [unsavedRef]);

  // act() reports a failure to the user itself (showError, wired in by
  // App.jsx) and resolves to whether the edit landed. The form closes
  // only once it has: on a refused or dropped save it stays open with
  // what was typed, so the edit can be retried or copied out rather
  // than lost with the error (grain/task-1699).
  const save = async () => {
    if (!title.trim()) return;
    const parsedReviewers = reviewers
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean);
    const saved = await act(
      () =>
        api(`/api/tasks/${t.id}`, {
          method: "PATCH",
          body: JSON.stringify({
            title,
            description,
            reviewTemplateId,
            ...(reviewers !== savedReviewers
              ? { reviewers: parsedReviewers }
              : {}),
            ...(nothingNeededAction !== savedNothingNeeded
              ? { nothingNeededAction }
              : {}),
            // Sent on every save, like the review beside it: what the
            // pickers hold is this task's whole answer, and "" is the
            // real edit that hands it back to the deployment's default model
            // or to the rotation across connections
            // (ui.UpdateTaskRequest.Model and .AgentBackendID). Left out
            // where there is no connection to choose between.
            ...(connections.length > 0 ? { model, agentBackendId } : {}),
          }),
        }),
      t.id,
    );
    if (saved) onDone();
  };

  return (
    <Stack spacing={1.5} sx={{ mb: 2 }}>
      <TextField
        label="Title"
        value={title}
        onChange={(e) => setTitle(e.target.value)}
        autoFocus
        fullWidth
        size="small"
      />
      {/* minRows, not rows, for the reason NewTaskOverlay's own
          description field gives: the box grows with the text rather
          than scrolling inside itself. */}
      <TextField
        label="Description"
        value={description}
        onChange={(e) => setDescription(e.target.value)}
        multiline
        minRows={5}
        fullWidth
        size="small"
      />
      <TextField
        label="Reviewers"
        placeholder="alice, bob"
        value={reviewers}
        onChange={(e) => setReviewers(e.target.value)}
        helperText="comma-separated usernames passed to workspace grants and used when mailing a CL"
        fullWidth
        size="small"
      />
      <TextField
        select
        label="Review"
        value={reviewTemplateId}
        onChange={(e) => setReviewTemplateId(e.target.value)}
        // displayEmpty: "" is a real choice (no review), not a blank --
        // and the label has to shrink for it, since MUI only floats one
        // out of a field it counts as filled and "" is not: left to
        // itself the label sits on top of the "No review" displayEmpty
        // draws inside the box.
        InputLabelProps={{ shrink: true }}
        SelectProps={{ displayEmpty: true }}
        disabled={reviewFiled || (templates || []).length === 0}
        helperText={
          reviewFiled
            ? `Task ${t.reviewTask} is already reviewing this one -- exactly one review is ever filed per task`
            : (templates || []).length === 0
              ? "no templates yet -- write one in Templates to be able to attach a review here"
              : "run a second agent over this task's own code before it merges, with instructions from a template"
        }
        fullWidth
        size="small"
      >
        <MenuItem value="">No review</MenuItem>
        {(templates || []).map((tmpl) => (
          <MenuItem key={tmpl.id} value={tmpl.id}>
            {tmpl.name}
          </MenuItem>
        ))}
      </TextField>
      {/* The model, then optionally one connection to run it on: the
          same two pickers NewTaskOverlay has. A model only disabled
          connections serve is still offered, and says so, because
          pinning one of those is how it runs. Left out where the team
          has no connection at all. */}
      {models.length > 0 && (
        <ModelSelect
          label="Model"
          value={model}
          onChange={(next) => {
            setModel(next);
            // A pin the new model's connections do not include would be
            // refused, so it comes off.
            const nextModels = next ? [next] : teamDefaultModels;
            const allowed = connectionsServingModels(
              models,
              connections,
              nextModels,
            );
            if (
              agentBackendId &&
              nextModels.length > 0 &&
              !allowed.some((c) => c.id === agentBackendId)
            )
              setAgentBackendId("");
          }}
          options={modelOptions(
            models,
            teamDefaultLabel(deployment || teamDefaultModel, models),
            model,
          )}
          helperText="reaches this task's next run; one already dispatched keeps the model it started on"
        />
      )}
      {connections.length > 0 && (
        <TextField
          select
          label="Specific connection"
          value={
            pinChoices.some((c) => c.id === agentBackendId) || savedPin
              ? agentBackendId
              : ""
          }
          onChange={(e) => setAgentBackendId(e.target.value)}
          InputLabelProps={{ shrink: true }}
          SelectProps={{ displayEmpty: true }}
          helperText={
            effectiveModel
              ? `connections that serve ${effectiveModel} -- pin one only if this task needs that account`
              : "pin this task to one connection -- leave it on any connection unless it needs that account"
          }
          fullWidth
          size="small"
        >
          <MenuItem value="">Any enabled connection serving the model</MenuItem>
          {pinChoices.map((backend) => (
            <MenuItem key={backend.id} value={backend.id}>
              {connectionLabel(backend)}
            </MenuItem>
          ))}
          {savedPin && (
            <MenuItem value={savedPin.id}>
              {savedPin.missing
                ? `${savedPin.id} (not one of the deployment's connections)`
                : `${connectionLabel(savedPin)} (does not serve ${effectiveModel})`}
            </MenuItem>
          )}
        </TextField>
      )}
      <TextField
        select
        label="When the agent finds nothing to do"
        value={nothingNeededAction}
        onChange={(e) => setNothingNeededAction(e.target.value)}
        helperText="what happens when a run ends with nothing_needed: no change and no report anyone has to act on"
        fullWidth
        size="small"
      >
        <MenuItem value="close">Close</MenuItem>
        <MenuItem value="ack">Leave for acknowledgement</MenuItem>
      </TextField>
      <Stack direction="row" spacing={1} justifyContent="flex-end">
        <Button onClick={onDone}>Cancel</Button>
        <Button variant="contained" disabled={!title.trim()} onClick={save}>
          Save
        </Button>
      </Stack>
    </Stack>
  );
}
