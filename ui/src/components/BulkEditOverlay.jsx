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
  Box,
  Button,
  Checkbox,
  FormControlLabel,
  MenuItem,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import api from "../api.js";
import {
  branchesInUse,
  connectionLabel,
  connectionsServingModels,
  defaultModels,
  deploymentRow,
  frameworkLabel,
  targetableRepos,
  connectionModels,
  modelOptions,
  teamDefaultLabel,
} from "../state.js";
import { CUSTOM_PREFIX } from "../taskAttributes.js";
import ModelSelect from "./ModelSelect.jsx";
import Overlay from "./Overlay.jsx";
import BranchField from "./BranchField.jsx";
import RepoField from "./RepoField.jsx";
import {
  CustomFieldInput,
  declaredFields,
  fieldLabel,
  valueList,
} from "./CustomFields.jsx";

// BulkEditOverlay is DetailOverlay's *fields* aimed at a whole
// selection, the way BatchActionsBar is its *buttons*: the half of
// UpdateTaskRequest that says where a task runs and how, set on every
// selected task at once instead of one pane at a time.
//
// It exists because those fields are exactly the ones that go wrong in
// batches. A pass of tasks filed against a base branch that has since
// merged and been deleted fails every attempt until each one is
// corrected; a framework key that ran out, a review nobody wants on
// twenty proposals, an auto-merge flag set the wrong way on a whole
// suite run -- each is one decision the operator has already made,
// re-typed once per task through the task pane. Approve, Defer and the
// capability toggles were widened to the selection for that reason
// (BatchActionsBar); this is the same widening for the fields those
// buttons do not cover.
//
// Every field is opt-in, behind its own "change this" tick, and only a
// ticked one is sent. That is what makes a mixed selection safe to edit:
// PATCH /api/tasks/{id} leaves out what a request does not name
// (ui.UpdateTaskRequest), so a selection spanning four repos can have
// its review detached without any of the four repos being touched. It
// is also what keeps "clear this override" expressible -- an empty base
// box, an empty prompt extension and "No review" are all real edits the
// server acts on, and a form that inferred intent from emptiness could
// not tell them from a field nobody meant to change.
//
// What is pre-filled is what the selection already agrees on: a
// selection whose tasks all target one repo opens with that repo in the
// box, so the common case ("all of these, but on release/2") is a tick,
// a word and Apply. Where the selection disagrees the control opens
// empty and says so, because there is no value that would be an honest
// default and silently picking one task's would rewrite the rest.
//
// The fields this deployment declares for itself (model.CustomField,
// grain/task-53) are here too, one row each under "Fields", rendered
// from the same declarations the new-task form and the task pane render
// (CustomFields.jsx). They are the half of a task that says what the
// work is *about* -- a customer, a priority, a release -- and that is
// the half a whole selection most often shares: "these six are all for
// Globex", "these are all critical" is the sentence somebody has
// otherwise had to say once per task through its own pane.
//
// Two things about them are decided here rather than left implicit. A
// ticked field left empty clears the value on every selected task,
// exactly as an emptied base branch clears that -- emptiness is a real
// edit in this pane, which is what the ticks are for. And a
// multi-valued field (model.CustomField.Multiple) *replaces* the set on
// every selected task rather than adding to what each already holds:
// every other row here replaces, "(mixed across the selection)" is
// already how this pane says the selection disagrees, and a row that
// added instead would be the one control in the form that cannot take
// a value off.
//
// Title and description are deliberately not here. They are what tells
// one task from another, and the same words on twenty of them would
// destroy exactly the information the list is read for -- they are also
// the two fields an edit re-sends to a *running* agent as an addendum
// (DetailOverlay's EditTaskForm), which is a conversation with one run,
// not a property of a batch.
export default function BulkEditOverlay({
  selection,
  tasks,
  config,
  templates,
  onClose,
  onRun,
}) {
  // The selection's own answer to each field, where it has one. Held as
  // the initial state of the controls below rather than read on every
  // render: the list behind this pane is re-fetched every few seconds
  // (App's POLL_INTERVAL_MS), and a form that re-seeded from it would
  // rewrite what somebody had typed while they were typing it.
  //
  // `initial` is that answer itself, kept apart from the controls: it is
  // what a row's "(mixed across the selection)" note and the Repo
  // picker's seed are read from, so neither drifts with what has been
  // typed since -- editing a mixed field and unticking it used to lose
  // the note for good, and a repo typed into "Other…" became the value
  // "Choose from list" and a re-tick fell back to.
  const declared = declaredFields(config);
  const [initial] = useState(() => ({
    repo: shared(selection, (t) => t.repo || ""),
    base: shared(selection, (t) => t.base || ""),
    autoMerge: shared(selection, (t) => (t.autoMerge ? "on" : "off")),
    framework: shared(selection, (t) => t.agentFramework || ""),
    model: shared(selection, (t) => t.model || ""),
    agentBackendId: shared(selection, (t) => t.agentBackendId || ""),
    review: shared(selection, (t) => t.reviewTemplateId || ""),
    promptExtension: shared(selection, (t) => t.promptExtension || ""),
    priority: shared(selection, (t) => String(t.priority ?? 5)),
    fields: sharedFieldValues(selection, declared),
  }));
  const [repo, setRepo] = useState(initial.repo);
  const [base, setBase] = useState(initial.base);
  const [autoMerge, setAutoMerge] = useState(initial.autoMerge);
  const [framework, setFramework] = useState(initial.framework);
  // The model (ui.UpdateTaskRequest.Model, "" being the team's default)
  // and the optional connection pin beside it, edited as one row: a pin
  // has to advertise the model, so one is never sent without the other.
  const [model, setModel] = useState(initial.model);
  const [agentBackendId, setAgentBackendId] = useState(initial.agentBackendId);
  const [review, setReview] = useState(initial.review);
  const [promptExtension, setPromptExtension] = useState(
    initial.promptExtension,
  );
  // The task priority (model.Task.Priority, 5 when unset), held as the
  // box's text so a half-typed value is not rewritten under the cursor.
  const [priority, setPriority] = useState(initial.priority ?? "");
  // The declared fields, by name, seeded the same way: what the whole
  // selection says about each, or null where it disagrees.
  const [fieldValues, setFieldValues] = useState(initial.fields);
  // Which fields this edit is actually about: the ticks, one per row.
  const [change, setChange] = useState({});
  const tick = (field) => (e) => {
    const checked = e.target.checked;
    // The Repo picker is remounted on its tick and seeds from the
    // selection's repo, so the value it reports has to start there too.
    if (field === "repo") setRepo(initial.repo);
    setChange((prev) => ({ ...prev, [field]: checked }));
  };

  // The repo options are the whole deployment's, not the selection's: a
  // bulk edit is most often what moves tasks onto a repo none of them
  // currently names, which is the one repo a list of the selection's own
  // targets would never offer.
  const repoOptions = targetableRepos(config, tasks || []);
  const mixed = (value) => value === null;
  // The empty string is a value a task really can hold for every field
  // here (no base, no override, no review), so "the selection
  // disagrees" cannot be spelled as one -- shared() says it with null,
  // and the controls take "" in its place.
  const orEmpty = (value) => (value === null ? "" : value);

  // A repo is the one field with no valid empty value: ui.Client.
  // UpdateTask refuses "" outright ("a task with no target cannot be
  // dispatched"), so a ticked-but-empty repo would be a request that
  // could only ever fail, once per selected task.
  const repoMissing = Boolean(change.repo) && orEmpty(repo).trim() === "";
  // A priority is a whole number from 1 up, as on the task pane's own
  // form (EditTaskForm's priorityValid). Unlike there, an empty box is
  // not "leave it as it is" -- the untick says that -- so a ticked row
  // must hold a number, or Apply would send every task the server's
  // reading of nothing.
  const priorityText = String(priority).trim();
  const priorityInvalid =
    Boolean(change.priority) &&
    !(/^\d+$/.test(priorityText) && Number(priorityText) >= 1);
  const fields = Object.keys(change).filter((f) => change[f]);
  const applicable = fields.length > 0 && !repoMissing && !priorityInvalid;

  // A declared field's tick is namespaced, for the reason its filter id
  // is (taskAttributes.js's CUSTOM_PREFIX): a deployment may declare a
  // field called `repo` or `review`, and it must add a row of its own
  // rather than quietly become the tick on the built-in of that name.
  const fieldKey = (f) => `${CUSTOM_PREFIX}${f.name}`;
  const changedFields = declared.filter((f) => change[fieldKey(f)]);
  // What one row's control holds: the selection's shared answer, and
  // the empty one where it disagrees or where the field arrived after
  // this form was opened. Always the shape the declaration promises --
  // an array for a set, a string otherwise -- so CustomFieldInput sees
  // what it does on every other form.
  const heldValue = (f) => {
    const v = fieldValues[f.name];
    if (v === null || v === undefined) return f.multiple ? [] : "";
    return f.multiple ? valueList(v) : v;
  };
  const setFieldValue = (f, v) =>
    setFieldValues((prev) => ({ ...prev, [f.name]: v }));

  // customFieldsFor is one task's whole `customFields` map as this edit
  // leaves it: the ticked fields set to what the form says, and every
  // *un*ticked field restated with what that task already holds.
  //
  // The restating is not optional. PATCH takes the map as the task's
  // whole answer about its declared fields -- a declared field the
  // request does not name is one the task no longer has a value for
  // (ui.mergeCustomFields, and the task pane's own editor, which sends
  // every field for the same reason) -- so a body carrying only the
  // ticked field would set a priority on six tasks and wipe the
  // customer off all six. A field the task says nothing about is left
  // out rather than sent empty, since "unstated and unheld" is already
  // "still nothing"; a value whose declaration has since been deleted
  // is left out too, and survives untouched on the server.
  //
  // What an untouched field restates is *everything* that task holds
  // for it, and the whole of that matters: a field that has stopped
  // being `multiple` leaves the tasks that used it carrying several
  // values, and sending the first of them made a bulk edit of the
  // priority quietly delete the other teams off every task in the
  // selection. The server reads a restatement of what is already stored
  // as saying nothing new about it, legacy values included
  // (model.NormalizeCustomFieldValuesFrom), so this neither loses them
  // nor fails the edit that was actually asked for.
  const customFieldsFor = (task) => {
    const out = {};
    for (const f of declared) {
      const ticked = Boolean(change[fieldKey(f)]);
      const values = ticked
        ? valueList(fieldValues[f.name])
        : valueList((task.customFields || {})[f.name]);
      if (values.length === 0) {
        // A ticked row left empty is a clear and has to be said out
        // loud; an untouched field this task never had says nothing.
        if (ticked) out[f.name] = "";
        continue;
      }
      // One value goes as the bare string this API has always taken, so
      // an ordinary bulk edit sends exactly the body it used to.
      out[f.name] = f.multiple || values.length > 1 ? values : values[0];
    }
    return out;
  };

  // A running task keeps the repo and base its run was dispatched
  // against: ui.Client.UpdateTask refuses an edit that moves either
  // until the run ends (grain/task-2310), so the PATCH for each running
  // task in the selection fails and the rest land. Said before Apply,
  // since correcting a dead base branch across a stalled batch is the
  // main thing this pane is for and a partial failure should not be the
  // first anyone hears of it.
  const running = selection.filter((t) => t.state === "running").length;

  const apply = () => {
    const body = {};
    if (change.repo) body.repo = orEmpty(repo).trim();
    if (change.base) body.base = orEmpty(base).trim();
    if (change.autoMerge) body.autoMerge = orEmpty(autoMerge) === "on";
    if (change.framework) body.agentFramework = orEmpty(framework);
    // Sent even when the framework row is ticked too: the server takes
    // the model as the more specific of the two and refuses a pair that
    // disagrees, rather than silently running the selection on something
    // nobody picked. The pin goes with it every time, so a task already
    // pinned to a connection that does not serve the new model is not
    // left holding a pair UpdateTask would refuse
    // (ui.UpdateTaskRequest.Model and .AgentBackendID).
    if (change.model) {
      body.model = orEmpty(model);
      body.agentBackendId = orEmpty(agentBackendId);
    }
    if (change.review) body.reviewTemplateId = orEmpty(review);
    if (change.promptExtension) body.promptExtension = orEmpty(promptExtension);
    if (change.priority) body.priority = Number(priorityText);
    // One PATCH per task, through App's own runBatch: failures are
    // collected rather than aborting the rest, and the selection stands
    // unless every one of them landed, so a partial failure can be
    // retried on the rows that are still ticked.
    //
    // The body is the same for every task except its fields, which are
    // that task's own (customFieldsFor) -- read from the selection this
    // form opened on, which is where every value in it came from. A row
    // that has left that selection since (App's list is re-fetched every
    // few seconds, and it drops an id the store has stopped reporting)
    // gets the rest of the edit with its fields left out: what it holds
    // is no longer known here, and a body built without it would clear
    // the values this edit was never about.
    onRun((id) => {
      const task = selection.find((t) => t.id === id);
      const patch =
        changedFields.length > 0 && task
          ? { ...body, customFields: customFieldsFor(task) }
          : body;
      return api(`/api/tasks/${id}`, {
        method: "PATCH",
        body: JSON.stringify(patch),
      });
    });
    onClose();
  };

  return (
    <Overlay
      onClose={onClose}
      label="Bulk edit"
      isDirty={() => fields.length > 0}
    >
      <Typography variant="h6" component="h2" sx={{ mt: 0 }}>
        Bulk edit
      </Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        {selection.length} selected task{selection.length === 1 ? "" : "s"}.
        Tick a field to change it; everything left unticked is untouched.
      </Typography>

      <Stack spacing={1}>
        <Row
          label="Repo"
          checked={Boolean(change.repo)}
          onChange={tick("repo")}
          mixed={mixed(initial.repo)}
        >
          {/* Remounted on the tick so the picker seeds from the
              selection's own repo each time the row is opened --
              RepoField is uncontrolled (defaultValue), and its "Other…"
              text path would otherwise keep whatever was typed into a
              row that was ticked, edited and unticked again. */}
          <RepoField
            key={change.repo ? "on" : "off"}
            options={repoOptions}
            defaultValue={orEmpty(initial.repo)}
            onChange={setRepo}
          />
          {repoMissing && (
            <Typography variant="caption" color="error">
              a task with no target cannot be dispatched -- pick a repo
            </Typography>
          )}
        </Row>

        <Row
          label="Base branch"
          checked={Boolean(change.base)}
          onChange={tick("base")}
          mixed={mixed(initial.base)}
        >
          {/* Suggested from the repo the edit is about -- the one the
              selection agrees on, or the one being moved to on the row
              above. A selection spanning repos has none to ask about,
              so the box is the plain one it has always been, plus
              whatever branch names the selection's own tasks carry. */}
          <BranchField
            label="Base branch"
            repo={mixed(repo) ? "" : orEmpty(repo)}
            options={branchesInUse(tasks, mixed(repo) ? "" : orEmpty(repo))}
            value={orEmpty(base)}
            onChange={setBase}
            helperText="the branch each task's next run checks out and pushes from -- empty means the repo's default branch"
          />
        </Row>

        <Row
          label="Auto-merge"
          checked={Boolean(change.autoMerge)}
          onChange={tick("autoMerge")}
          mixed={mixed(initial.autoMerge)}
        >
          <TextField
            select
            label="Auto-merge"
            value={orEmpty(autoMerge) || "off"}
            onChange={(e) => setAutoMerge(e.target.value)}
            helperText="merge each pull request on its own once its checks pass, with no human review"
            fullWidth
            size="small"
          >
            <MenuItem value="on">On</MenuItem>
            <MenuItem value="off">Off</MenuItem>
          </TextField>
        </Row>

        {/* Which model the selection runs on (grain/task-110), and
            optionally which connection: the same two pickers the new-task
            form has, offered only where there is a connection to choose
            between. A model only disabled
            connections serve is in the list, since pinning one of those
            is the only way work reaches it. Above the framework row
            because it is the more specific of the two: a model decides
            its own framework, and a pair that disagrees is refused. */}
        {(() => {
          const connections = config?.agentBackends || [];
          const models = connectionModels(connections);
          if (models.length === 0) return null;
          const deployment = deploymentRow(config);
          const teamDefaultModels = defaultModels(deployment);
          const teamDefaultModel = deployment?.defaultModel || "";
          const chosenModel = orEmpty(model);
          const effectiveModels = chosenModel
            ? [chosenModel]
            : teamDefaultModels;
          const effectiveModel = effectiveModels.join(" → ");
          const pinChoices = connectionsServingModels(
            models,
            connections,
            effectiveModels,
          );
          const pin = orEmpty(agentBackendId);
          return (
            <Row
              label="Model"
              checked={Boolean(change.model)}
              onChange={tick("model")}
              mixed={mixed(initial.model) || mixed(initial.agentBackendId)}
            >
              <Stack spacing={1.5}>
                <ModelSelect
                  label="Model"
                  value={orEmpty(model)}
                  onChange={(next) => {
                    setModel(next);
                    const nextModels = next ? [next] : teamDefaultModels;
                    const allowed = connectionsServingModels(
                      models,
                      connections,
                      nextModels,
                    );
                    if (
                      pin &&
                      nextModels.length > 0 &&
                      !allowed.some((c) => c.id === pin)
                    )
                      setAgentBackendId("");
                  }}
                  options={modelOptions(
                    models,
                    teamDefaultLabel(deployment || teamDefaultModel, models),
                  )}
                  helperText="a run already dispatched keeps the model it started on"
                />
                <TextField
                  select
                  label="Specific connection"
                  value={pinChoices.some((c) => c.id === pin) ? pin : ""}
                  onChange={(e) => setAgentBackendId(e.target.value)}
                  InputLabelProps={{ shrink: true }}
                  SelectProps={{ displayEmpty: true }}
                  helperText={
                    effectiveModel
                      ? `connections that serve ${effectiveModel} -- pin one only if these tasks need that account`
                      : "pin these tasks to one connection -- leave them on any connection unless they need that account"
                  }
                  fullWidth
                  size="small"
                >
                  <MenuItem value="">
                    Any enabled connection serving the model
                  </MenuItem>
                  {pinChoices.map((backend) => (
                    <MenuItem key={backend.id} value={backend.id}>
                      {connectionLabel(backend)}
                    </MenuItem>
                  ))}
                </TextField>
              </Stack>
            </Row>
          );
        })()}

        <Row
          label="Agent framework"
          checked={Boolean(change.framework)}
          onChange={tick("framework")}
          mixed={mixed(initial.framework)}
        >
          <TextField
            select
            label="Agent framework"
            value={orEmpty(framework)}
            onChange={(e) => setFramework(e.target.value)}
            // displayEmpty, as on the same picker in NewTaskOverlay: ""
            // is a real choice here (the deployment's own framework),
            // not the absence of one -- and the shrink with it, for the
            // reason that picker needs both: MUI floats a label clear of
            // the box only for a field it counts as filled, and a select
            // holding "" never is, so without this the label is painted
            // across the "Deployment default (...)" line displayEmpty
            // draws inside the box.
            InputLabelProps={{ shrink: true }}
            SelectProps={{ displayEmpty: true }}
            helperText="a run already dispatched keeps the framework it started with"
            fullWidth
            size="small"
          >
            <MenuItem value="">
              {`Deployment default${config && config.agentFramework ? ` (${frameworkLabel(config.agentFramework)})` : ""}`}
            </MenuItem>
            <MenuItem value="antigravity">Antigravity</MenuItem>
            <MenuItem value="claude">Claude</MenuItem>
            <MenuItem value="codex">Codex</MenuItem>
            <MenuItem value="custom">Custom command</MenuItem>
          </TextField>
        </Row>

        <Row
          label="Review"
          checked={Boolean(change.review)}
          onChange={tick("review")}
          mixed={mixed(initial.review)}
        >
          <TextField
            select
            label="Review"
            value={orEmpty(review)}
            onChange={(e) => setReview(e.target.value)}
            // displayEmpty and the shrink with it, for the reason above:
            // "" is "no review", a line this select draws inside its own
            // box, and the label has to be out of the way of it.
            InputLabelProps={{ shrink: true }}
            SelectProps={{ displayEmpty: true }}
            helperText="attaching one reaches any review not yet filed; a task whose review task already exists keeps it"
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
        </Row>

        <Row
          label="Prompt extension"
          checked={Boolean(change.promptExtension)}
          onChange={tick("promptExtension")}
          mixed={mixed(initial.promptExtension)}
        >
          <TextField
            label="Prompt extension override"
            value={orEmpty(promptExtension)}
            onChange={(e) => setPromptExtension(e.target.value)}
            multiline
            rows={3}
            helperText="replaces what this deployment and the repo would otherwise tell these runs -- empty clears the override"
            fullWidth
            size="small"
          />
        </Row>

        <Row
          label="Task priority"
          checked={Boolean(change.priority)}
          onChange={tick("priority")}
          mixed={mixed(initial.priority)}
        >
          <TextField
            label="Task priority"
            type="number"
            inputProps={{ min: 1, step: 1 }}
            value={priority}
            onChange={(e) => setPriority(e.target.value)}
            error={priorityInvalid}
            helperText={
              priorityInvalid
                ? "a whole number, 1 or more; default 5, higher numbers run first"
                : "default 5; higher numbers run first"
            }
            fullWidth
            size="small"
          />
        </Row>

        {declared.length > 0 && (
          <>
            <Typography variant="caption" color="text.secondary" sx={{ pt: 1 }}>
              Fields
            </Typography>
            {declared.map((f) => (
              <Row
                key={fieldKey(f)}
                label={fieldLabel(f)}
                checked={Boolean(change[fieldKey(f)])}
                onChange={tick(fieldKey(f))}
                mixed={mixed(initial.fields[f.name])}
              >
                {f.type === "boolean" && !f.multiple ? (
                  // A boolean is the one declared type whose own input
                  // is a checkbox (CustomFields.jsx), and a checkbox
                  // under this pane's tick would be two controls that
                  // look alike saying quite different things -- and one
                  // of them unable to say "clear it", which every other
                  // row here can. So a picker, the shape Auto-merge
                  // above already has, with the third answer the store
                  // really holds: no value at all.
                  <TextField
                    select
                    label={fieldLabel(f)}
                    value={heldValue(f)}
                    onChange={(e) => setFieldValue(f, e.target.value)}
                    // displayEmpty and the shrink with it, as on the
                    // pickers above: "" is a line this select draws
                    // inside its own box, not an empty field.
                    InputLabelProps={{ shrink: true }}
                    SelectProps={{ displayEmpty: true }}
                    helperText={f.description || undefined}
                    fullWidth
                    size="small"
                  >
                    <MenuItem value="true">Yes</MenuItem>
                    <MenuItem value="false">No</MenuItem>
                    <MenuItem value="">Not set</MenuItem>
                  </TextField>
                ) : (
                  <CustomFieldInput
                    field={f}
                    value={heldValue(f)}
                    onChange={(v) => setFieldValue(f, v)}
                  />
                )}
                <Typography
                  variant="caption"
                  color="text.secondary"
                  sx={{ display: "block", mt: 0.5 }}
                >
                  {f.multiple
                    ? "replaces what each selected task holds -- an empty box clears the field"
                    : "set on every selected task -- an empty value clears the field"}
                </Typography>
              </Row>
            ))}
          </>
        )}
      </Stack>

      {running > 0 && (change.repo || change.base) && (
        <Alert severity="info" sx={{ mt: 2 }}>
          {running} of these {running === 1 ? "is" : "are"} running now. A repo
          or base change will be refused for {running === 1 ? "it" : "them"}{" "}
          until {running === 1 ? "its run ends" : "their runs end"}, and the
          rest are changed.
        </Alert>
      )}

      <Stack direction="row" spacing={1} justifyContent="flex-end" mt={2}>
        <Button onClick={onClose}>Cancel</Button>
        <Button variant="contained" disabled={!applicable} onClick={apply}>
          Apply to {selection.length} task{selection.length === 1 ? "" : "s"}
        </Button>
      </Stack>
    </Overlay>
  );
}

// Row is one field of the form: the tick that says whether this edit is
// about it, and the control, shown only once it is ticked. Hidden rather
// than disabled, so a form of six fields reads as the one or two the
// operator is actually changing instead of a wall of greyed-out boxes.
function Row({ label, checked, onChange, mixed, children }) {
  return (
    <Box>
      <FormControlLabel
        control={
          <Checkbox checked={checked} onChange={onChange} size="small" />
        }
        label={
          <span>
            {label}
            {mixed && " "}
            {mixed && (
              <Typography
                component="span"
                variant="caption"
                color="text.secondary"
                sx={{ ml: 1 }}
              >
                (mixed across the selection)
              </Typography>
            )}
          </span>
        }
      />
      {checked && <Box sx={{ pl: 4, pb: 1 }}>{children}</Box>}
    </Box>
  );
}

// shared is the selection's own value for one field, or null where its
// tasks disagree -- the "appropriate value" each control opens on. null
// rather than "" because "" is a value tasks can genuinely share (no
// base, no override), and a form that pre-filled a disagreement with one
// task's answer would quietly propose overwriting the others with it.
function shared(selection, read) {
  if (selection.length === 0) return null;
  const first = read(selection[0]);
  return selection.every((t) => read(t) === first) ? first : null;
}

// JOIN separates a multi-valued field's values while shared() is
// comparing them, since shared() compares one value per task and a set
// has to be one string to be compared as one. A NUL rather than a comma:
// a text field's values are whatever was typed into the chips, commas
// included, and two selections differing only in where the commas fell
// must not read as agreeing.
const JOIN = "\u0000";

// sharedFieldValues is shared() over the declared fields: what the whole
// selection says about each one, null where its tasks disagree, in the
// shape that field's control takes (an array for a set, a string
// otherwise).
//
// A field's values are compared as a set, not as what happens to be
// stored: the server sorts and deduplicates them
// (model.CustomField.NormalizeValues), so two tasks tagged the same two
// teams agree here whichever order either was written in.
function sharedFieldValues(selection, declared) {
  const values = {};
  for (const f of declared) {
    const joined = shared(selection, (t) =>
      valueList((t && t.customFields)?.[f.name])
        .slice()
        .sort()
        .join(JOIN),
    );
    if (joined === null) {
      values[f.name] = null;
      continue;
    }
    const held = joined === "" ? [] : joined.split(JOIN);
    // The shape that field's control takes -- and on a single-valued
    // field the selection agrees is carrying several values, that is
    // the array: a field which has stopped being `multiple` leaves a
    // set behind, and CustomFieldInput draws such a set as the chips it
    // is rather than as one value picked out of it.
    values[f.name] = f.multiple || held.length > 1 ? held : joined;
  }
  return values;
}
