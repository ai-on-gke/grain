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
  Box,
  Button,
  Checkbox,
  FormControl,
  FormControlLabel,
  InputLabel,
  MenuItem,
  Select,
  Stack,
  Typography,
} from "@mui/material";
import api from "../api.js";
import BranchField from "./BranchField.jsx";
import CompositionPanel from "./CompositionPanel.jsx";
import ItemGlyph, { GlyphLabel } from "./ItemGlyph.jsx";
import Overlay from "./Overlay.jsx";
import RepoField from "./RepoField.jsx";
import { targetableRepos } from "../state.js";
import {
  initialTemplateFieldValues,
  TemplateFieldInputs,
} from "./TemplateFields.jsx";
import { templateLabeler } from "../templateLabels.js";

// UseTemplateOverlay files one task from a template (grain/task-58) --
// opened from a template row's own "Use…" action, with that template
// preselected but still changeable, SuiteRunOverlay's own shape for the
// same reason: it is a single action taken over the page you are already
// on, not somewhere you navigate to, so it is a centered box with an X
// (Overlay.jsx's own note on which shape is which).
//
// The whole of the form is the template's own inputs, plus where the
// task runs. Everything else a task carries -- title, description,
// reads, writes, capabilities, declared-field values, auto-merge,
// network -- comes from the template, filled in server-side
// (ui.Client.fromTemplate), which is the point: filling a template in
// should be two dropdowns and a button, not the dozen-field new-task
// form with most of it already decided.
//
// What that produces is shown below the form before anything is filed,
// through the same composition endpoint the filing itself goes through
// (CompositionPanel, GET /api/templates/{id}/composition). Nothing here
// substitutes a `{{name}}` itself: a second implementation in the
// browser is a second answer to disagree with the one that gets filed.
export default function UseTemplateOverlay({
  templates = [],
  templateId,
  tasks = [],
  config,
  onClose,
  onCreated,
  onOpenTask,
  showError,
}) {
  const [selectedId, setSelectedId] = useState(
    templateId || (templates[0] && templates[0].id) || "",
  );
  const template = templates.find((t) => t.id === selectedId) || null;
  // Names alone where they are unique, more where they are not
  // (templateLabels.js).
  const templateLabel = templateLabeler(templates);
  const targetOptions = targetableRepos(config, tasks);
  const fields = template?.fields || [];
  // What the inputs hold, and what the preview below is composed
  // against. Two states rather than one, SuiteRunOverlay's own split:
  // `values` follows every keystroke, because a controlled text box
  // needs it to, and `previewValues` only moves when a value is settled
  // -- a box blurred, a picker picked -- so filling the form in does not
  // ask the server for a composition per character. The filing itself
  // reads `values`.
  const [values, setValues] = useState(() =>
    initialTemplateFieldValues(fields),
  );
  const [previewValues, setPreviewValues] = useState(values);
  const [repo, setRepo] = useState("");
  const [base, setBase] = useState("");
  const [approved, setApproved] = useState(!!config?.approvedByDefault);
  const [submitting, setSubmitting] = useState(false);

  // Picking a different template is a different set of holes, so the
  // values start again from that template's defaults rather than
  // carrying a value that belonged to a field this one does not have.
  const pickTemplate = (id) => {
    setSelectedId(id);
    const next = initialTemplateFieldValues(
      (templates.find((t) => t.id === id) || {}).fields,
    );
    setValues(next);
    setPreviewValues(next);
  };

  const setValue = (name, value) =>
    setValues((prev) => ({ ...prev, [name]: value }));
  const commitValue = (name, value) =>
    setPreviewValues((prev) => ({ ...prev, [name]: value }));

  // A bound template decides where its firings go (model.Template.
  // Target), so there is nothing to ask and the form says where instead
  // of offering a box whose answer would be ignored.
  const bound = !!template?.repo;
  // One of grain's own templates that names no repo is about this
  // deployment rather than any repository, and the server files it with
  // none (ui.Client.fromTemplate) -- so there is nothing to ask here
  // either.
  const repoless = !bound && !!template?.system;
  const effectiveApproved = template?.interactive ? true : approved;

  const previewPath = selectedId
    ? `/api/templates/${selectedId}/composition?` +
      new URLSearchParams([
        ["repo", bound || repoless ? "" : repo],
        ["base", bound || repoless ? "" : base],
        ["approved", effectiveApproved ? "true" : "false"],
        ...Object.entries(previewValues).map(([name, value]) => [
          `field.${name}`,
          value,
        ]),
      ]).toString()
    : null;

  const submit = async (evt) => {
    evt.preventDefault();
    if (submitting) return;
    setSubmitting(true);
    try {
      const task = await api("/api/tasks", {
        method: "POST",
        body: JSON.stringify({
          templateId: selectedId,
          fields: values,
          repo: bound || repoless ? "" : repo,
          base: bound || repoless ? "" : base,
          approved: effectiveApproved,
        }),
      });
      await onCreated();
      onClose();
      // Straight into the task that was just filed: the point of the
      // preview above is to be sure before filing, and the point of
      // this is to read what was actually filed. For an interactive
      // template that is its chat pane (DetailOverlay shows TaskChat for
      // an interactive task), where the agent's first message lands.
      if (onOpenTask && task && task.id) onOpenTask(task.id);
    } catch (err) {
      showError(err);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Overlay onClose={onClose} label="File a task from a template">
      <Stack direction="row" alignItems="center" spacing={1} sx={{ mb: 1 }}>
        <ItemGlyph kind="templates" size={20} />
        <Typography variant="h6" component="h2" sx={{ m: 0 }}>
          File a task from a template
        </Typography>
      </Stack>
      <form onSubmit={submit}>
        <FormControl fullWidth margin="normal" size="small">
          <InputLabel id="use-template-label">Template</InputLabel>
          <Select
            labelId="use-template-label"
            label="Template"
            value={selectedId}
            onChange={(e) => pickTemplate(e.target.value)}
            required
          >
            {templates.map((t) => (
              <MenuItem key={t.id} value={t.id}>
                <GlyphLabel kind="templates">{templateLabel(t)}</GlyphLabel>
              </MenuItem>
            ))}
          </Select>
        </FormControl>
        {/* The holes this template leaves. A template that asks for
            nothing renders nothing here, and this form is then just
            "file that template against this repo". */}
        <TemplateFieldInputs
          fields={fields}
          values={values}
          onChange={(name, value) => {
            setValue(name, value);
            // A picker's answer is settled the moment it is picked, so
            // it moves the preview at once; a text box waits for its
            // blur below.
            const field = fields.find((f) => f.name === name);
            if (field && (field.options || []).length > 0)
              commitValue(name, value);
          }}
          onCommit={commitValue}
        />
        {bound ? (
          <Typography variant="body2" color="text.secondary" sx={{ mt: 2 }}>
            Files against {template.repo}
            {template.base ? ` @ ${template.base}` : ""} -- this template is
            bound to it.
          </Typography>
        ) : repoless ? (
          <Typography
            variant="body2"
            color="text.secondary"
            sx={{ mt: 2 }}
            className="use-template-repoless"
          >
            Files with no repo -- this built-in template works on this
            deployment itself, not on a repository.
          </Typography>
        ) : (
          <>
            <Box component="label" sx={{ display: "block", mt: 2, mb: 1 }}>
              <Typography
                variant="caption"
                color="text.secondary"
                sx={{ display: "block", mb: 0.5 }}
              >
                Repo <span className="hint">owner/name</span>
              </Typography>
              <RepoField
                name="repo"
                options={targetOptions}
                defaultValue=""
                onCommit={setRepo}
              />
            </Box>
            <BranchField
              name="base"
              label="Base branch"
              placeholder="default branch"
              repo={repo}
              margin="dense"
              size="medium"
              onCommit={setBase}
            />
          </>
        )}
        {/* An interactive template files a live chat, which is approved
            and queued at once whatever this box would say
            (ui.CreateTaskRequest.Interactive). */}
        {template?.interactive ? (
          <Typography variant="body2" color="text.secondary" sx={{ mt: 2 }}>
            This template files a live chat: the agent can talk with you while
            it works, and the chat opens as soon as the task is filed.
          </Typography>
        ) : (
          <FormControlLabel
            control={
              <Checkbox
                checked={approved}
                onChange={(e) => setApproved(e.target.checked)}
              />
            }
            label="Approved (dispatch without waiting for a review of the task)"
            sx={{ display: "flex", mt: 1 }}
          />
        )}
        <CompositionPanel path={previewPath} heading="What this files" />
        <Stack
          direction="row"
          justifyContent="flex-end"
          spacing={1}
          sx={{ mt: 2 }}
        >
          <Button onClick={onClose}>Cancel</Button>
          <Button
            type="submit"
            variant="contained"
            disabled={!selectedId || submitting}
          >
            Create task
          </Button>
        </Stack>
      </form>
    </Overlay>
  );
}
