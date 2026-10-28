import { isApiGrant, withoutApiRows } from "../apiGrants.js";
import GrantsPicker from "./GrantsPicker.jsx";
import { localGrantHandlers } from "./capabilityMethods.js";
import { isCapabilityRef } from "../capabilityArgs.js";
import { useState } from "react";
import {
  Alert,
  Box,
  Button,
  Checkbox,
  Chip,
  FormControl,
  FormControlLabel,
  FormHelperText,
  InputLabel,
  ListItemText,
  MenuItem,
  Select,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import api from "../api.js";
import { useHeldIds } from "../useHeldIds.js";
import { CustomFieldInputs, initialFieldValues } from "./CustomFields.jsx";
import ItemGlyph from "./ItemGlyph.jsx";
import ModelPicker from "./ModelPicker.jsx";
import { DeclaredFields, DeclaredNotice } from "./Declared.jsx";
import Overlay from "./Overlay.jsx";
import { StaleEditNotice, isStaleEdit, withReload } from "./StaleEdit.jsx";
import { useUnsavedEdits } from "../unsavedEdits.js";
import ReadOnlyReposField from "./ReadOnlyReposField.jsx";
import {
  TemplateFieldsEditor,
  templateFieldsPayload,
} from "./TemplateFields.jsx";
import BranchField from "./BranchField.jsx";
import RepoField from "./RepoField.jsx";
import WriteReposField from "./WriteReposField.jsx";
import {
  configuredCapabilities,
  LEGACY_CAPABILITIES_HINT,
  NO_CAPABILITIES_HINT,
  targetableRepos,
} from "../state.js";

// TemplateOverlay is a template's own sub page (bwsalmon/agents#545):
// "+ New" on the Processes page (ProcessesList) opens this blank, and clicking
// an existing row opens the same overlay pre-filled with that
// template's fields, the same NewTaskOverlay/DetailOverlay split
// between a flat list of key details and everything about one item
// living behind a click rather than crowding the list -- so templates
// stop being the one list on this page that also carries an always-open
// form of its own.
//
// The target repo and branch here are the optional binding
// (grain/task-285), which is why they are not required and why the form
// says so: left blank -- the ordinary case -- whatever fires this
// template (ScheduleOverlay.jsx, SuiteRunOverlay.jsx) asks for a repo
// and branch of its own at the point of use. Filled in, they are what
// every firing targets instead, and those pickers stop asking.
// Re-submitting this form with the repo cleared unbinds the template,
// which is exactly what ui.UpdateTemplateRequest reads an empty repo as.
//
// The fields at the bottom are this deployment's own declared ones
// (grain/task-105): what they say here is what every task filed from
// this template says, whichever of the things that fire a template does
// it -- a schedule, a suite pass, a qualification run, a review.
function TemplateOverlay({
  template,
  repoOptions = [],
  tasks = [],
  config,
  onClose,
  onSaved,
  showError,
  onReload,
}) {
  const isNew = !template;
  // A repository's own template (Declared.jsx): shown, never saved or
  // deleted from here.
  const declaredIn = template?.declaredIn || null;
  // One of grain's own built-in templates (ui.Template.System): shown so
  // somebody can read what it files, and locked for the same reason a
  // declared one is -- the server refuses to edit or delete it.
  const system = !!template?.system;
  const locked = !!declaredIn || system;
  const targetOptions = targetableRepos(config, tasks);
  const [capabilities, setCapabilities] = useState(
    template?.capabilities || [],
  );
  // What this deployment has configured, plus whatever this template
  // already names: an unconfigured capability is not a choice worth
  // offering here, and one already on the template needs its row to be
  // unticked from (configuredCapabilities, state.js).
  // Capability server grants, with their with: arguments, and the agent
  // API's grants (apiGrants.js) share the list and are edited together
  // by GrantsPicker rather than the legacy checklist.
  const capabilityIds = capabilities.filter(
    (id) => !isCapabilityRef(id) && !isApiGrant(id),
  );
  const apiIds = capabilities.filter(isApiGrant);
  const capabilityRefs = capabilities.filter(isCapabilityRef);
  const heldCapabilityIds = useHeldIds(capabilityIds);
  const capabilityOptions = withoutApiRows(
    configuredCapabilities(config?.capabilities, heldCapabilityIds),
  );
  const methodGrants = localGrantHandlers(capabilityRefs, (refs) =>
    setCapabilities([...capabilityIds, ...apiIds, ...refs]),
  );
  // reads is state for the reason capabilities is: its picker
  // (ReadOnlyReposField) is a search box, not a form field submit could
  // read the answer off.
  const [reads, setReads] = useState(template?.reads || []);
  // writes is the extra writeable repos ({repo, base} objects,
  // ui.Template.Writes) every task filed from this template carries --
  // state for reads' own reason, and because each row's branch box
  // belongs beside the repo it names rather than in a form field named
  // after neither.
  const [writes, setWrites] = useState(template?.writes || []);
  // repo tracks the binding as it is picked, which the form itself has
  // no need of -- submit reads the field. The branch box under it does:
  // it offers the branches grain knows about on the repo it is under
  // (BranchField.jsx), and cannot ask about a repo nobody is holding.
  const [repo, setRepo] = useState(template?.repo || "");
  // What every task filed from this template says about this
  // deployment's own fields (grain/task-105) -- state, like the pickers
  // above, since CustomFieldInputs holds no form field of its own.
  // Seeded from the template's saved values, falling back to each
  // declaration's default, exactly as the new-task form and the task
  // detail's own editor seed theirs.
  const [customFields, setCustomFields] = useState(() =>
    initialFieldValues(config, template?.customFields),
  );
  // The inputs this template asks for when somebody files a task from
  // it (ui.Template.Fields, grain/task-58) -- state like every other
  // picker here, since it is a list of rows rather than a form field
  // submit could read an answer off, and since the whole list is what
  // is submitted.
  const [fields, setFields] = useState(template?.fields || []);
  // The model every task this template files asks for
  // (ui.CreateTemplateRequest.Model); "" leaves it to the deployment's
  // default.
  const [chosenModel, setChosenModel] = useState(template?.model || "");

  // Whether leaving now would lose an edit: the text boxes are read off
  // the form, and the pickers above keep their answers in state.
  const { formRef, isDirty } = useUnsavedEdits({
    capabilities,
    reads,
    writes,
    customFields,
    fields,
    chosenModel,
  });
  const [stale, setStale] = useState(null);
  // Held from when the pane opened, ScheduleOverlay's openedRevision.
  const [openedRevision] = useState(template?.revision);

  const submit = async (evt) => {
    evt.preventDefault();
    const form = evt.target;
    const data = new FormData(form);
    const payload = {
      name: data.get("name"),
      title: data.get("title"),
      description: data.get("description") || "",
      repo: data.get("repo") || "",
      base: data.get("base") || "",
      autoMerge: form.elements.autoMerge.checked,
      // Every task this template files is a live chat
      // (model.Template.Interactive).
      interactive: form.elements.interactive.checked,
      // How much of the internet the sandbox of every task this
      // template files may reach (grain/task-269). "" is a template
      // that fixes nothing, so each firing takes the deployment's own
      // default -- unlike the per-task picker, it is offered whatever
      // this deployment's sandbox backend is, since a template outlives
      // the backend and the firing is where that question is asked.
      networkAccess: data.get("networkAccess") || "",
      // What a run ending on nothing_needed does to every task this
      // template files (model.Template.NothingNeededAction).
      nothingNeededAction: data.get("nothingNeededAction") || "close",
      // Only when it changed: absent leaves the saved model alone
      // (ui.UpdateTemplateRequest.Model), and "" on a new template is
      // the same as saying nothing.
      ...(chosenModel !== (template?.model || "")
        ? { model: chosenModel }
        : {}),
      reads,
      writes,
      capabilities,
      customFields,
      // Always sent, empty list included: the form renders every input
      // this template asks for, so what it sends is the whole
      // declaration and removing the last one has to be sayable
      // (ui.UpdateTemplateRequest.Fields).
      fields: templateFieldsPayload(fields),
    };
    try {
      const saved = isNew
        ? await api("/api/templates", {
            method: "POST",
            body: JSON.stringify(payload),
          })
        : // ScheduleOverlay's own reason for the revision.
          await api(`/api/templates/${template.id}`, {
            method: "PATCH",
            body: JSON.stringify({ ...payload, revision: openedRevision }),
          });
      await onSaved(saved);
      onClose();
    } catch (err) {
      if (isStaleEdit(err)) setStale(err);
      else showError(err);
    }
  };

  const remove = async () => {
    if (
      !confirm(
        `Delete the template "${template.name}"? Schedules and suites using it must be repointed or deleted first.`,
      )
    )
      return;
    try {
      await api(`/api/templates/${template.id}`, { method: "DELETE" });
      await onSaved();
      onClose();
    } catch (err) {
      showError(err);
    }
  };

  return (
    <Overlay
      onClose={onClose}
      pane
      label={isNew ? "New template" : locked ? "Template" : "Edit template"}
      backLabel="Templates"
      isDirty={locked ? null : isDirty}
    >
      {/* Headed by the figure of the thing being edited, the way the
          Templates page this pane opens over is (ListPrimitives' own
          `icon`, ItemGlyph.jsx): 20px, ahead of the heading rather than
          inside it, so the heading's accessible name is still its own
          two words. */}
      <Stack direction="row" alignItems="center" spacing={1} sx={{ mb: 1 }}>
        <ItemGlyph kind="templates" size={20} />
        <Typography variant="h6" component="h2" sx={{ m: 0 }}>
          {isNew ? "New template" : locked ? "Template" : "Edit template"}
        </Typography>
      </Stack>
      <DeclaredNotice declaredIn={declaredIn} noun="template" />
      {system && (
        <Alert severity="info" className="system-notice" sx={{ mt: 1, mb: 1 }}>
          This template is built into grain, so it cannot be edited or deleted
          here. File a task from it with <strong>Use…</strong> on its row, or
          create a template of your own to change what it asks for.
        </Alert>
      )}
      <StaleEditNotice error={stale} onReload={onReload} />
      <form className="pane-form" onSubmit={submit} ref={formRef}>
        <DeclaredFields declaredIn={locked}>
          <TextField
            name="name"
            label="Name"
            defaultValue={template?.name}
            required
            InputLabelProps={{ required: false }}
            helperText="shown wherever a template is picked"
            autoComplete="off"
            fullWidth
            margin="normal"
          />
          <TextField
            name="title"
            label="Task title"
            defaultValue={template?.title}
            required
            InputLabelProps={{ required: false }}
            autoComplete="off"
            fullWidth
            margin="normal"
          />
          <TextField
            name="description"
            label="Description"
            defaultValue={template?.description}
            multiline
            rows={4}
            fullWidth
            margin="normal"
          />
          {/* The holes this template's title and description leave, for
            whoever files a task from it to fill in (TemplateFields.jsx).
            Directly under the two text boxes they are written into, so
            "{{service}} in the title" and "service, a text box" read as
            one thing. */}
          <TemplateFieldsEditor fields={fields} onChange={setFields} />
          {/* Every repo a task from this template may change, in one
            block (grain/task-202): the repo it is bound to first, then
            any others under it. NewTaskOverlay's own repos block says
            why they are not two fields. */}
          <Box sx={{ mt: 2, mb: 1 }}>
            <Box component="label" sx={{ display: "block" }}>
              <Typography
                variant="caption"
                color="text.secondary"
                sx={{ display: "block", mb: 0.5 }}
              >
                Repos{" "}
                <span className="hint">
                  optional -- leave the first empty to let whatever fires this
                  template choose it
                </span>
              </Typography>
              <RepoField
                name="repo"
                options={targetOptions}
                defaultValue={template?.repo || ""}
                onChange={setRepo}
              />
            </Box>
            <BranchField
              name="base"
              label="Base branch"
              defaultValue={template?.base || ""}
              helperText="optional, and only used with a bound repo"
              margin="dense"
              size="medium"
              repo={repo}
              warnMissing="tasks filed from this template fail until it is cut"
            />
            <WriteReposField
              options={repoOptions}
              value={writes}
              onChange={setWrites}
            />
          </Box>
          <ReadOnlyReposField
            options={repoOptions}
            value={reads}
            onChange={setReads}
            // The saved binding: a bound template's firings all target
            // that repo, so reading it says nothing there. An unbound
            // template passes "" and is offered everything -- which repo
            // its firings target is not decided here, so no read of them
            // can be redundant yet.
            target={template?.repo || ""}
          />
          <FormControlLabel
            control={
              <Checkbox name="autoMerge" defaultChecked={template?.autoMerge} />
            }
            label="Auto-merge once checks pass"
            sx={{ display: "flex", mt: 1 }}
          />
          <FormControlLabel
            control={
              <Checkbox
                name="interactive"
                defaultChecked={template?.interactive}
              />
            }
            label="Interactive (agent can chat with you)"
            sx={{ display: "flex" }}
          />
          <TextField
            select
            name="networkAccess"
            label="Network access"
            defaultValue={template?.networkAccess || ""}
            // displayEmpty and the shrink with it, for the reason the
            // new-task form's own pickers need both: "" is a real choice
            // (leave it to the deployment), not a blank field.
            InputLabelProps={{ shrink: true }}
            SelectProps={{ displayEmpty: true }}
            helperText="fixes the network of every task this template files. A task with no network still reaches this deployment's git proxy."
            fullWidth
            margin="normal"
            size="small"
          >
            <MenuItem value="">Deployment default</MenuItem>
            <MenuItem value="open">Egress (outbound internet only)</MenuItem>
            <MenuItem value="none">None (this deployment only)</MenuItem>
          </TextField>
          <TextField
            select
            name="nothingNeededAction"
            label="When the agent finds nothing to do"
            // "" on the wire is close (model.NothingNeededAction).
            defaultValue={template?.nothingNeededAction || "close"}
            helperText="applies to every task this template files"
            fullWidth
            margin="normal"
            size="small"
          >
            <MenuItem value="close">Close</MenuItem>
            <MenuItem value="ack">Leave for acknowledgement</MenuItem>
          </TextField>
          <ModelPicker
            config={config}
            value={chosenModel}
            onChange={setChosenModel}
            helperText="the model every task this template files asks for. A task filed from it can still choose its own."
          />
          <FormControl fullWidth margin="normal" size="small">
            <InputLabel id="template-capabilities-label">
              Legacy capabilities
            </InputLabel>
            <Select
              labelId="template-capabilities-label"
              label="Legacy capabilities"
              multiple
              value={capabilityIds}
              onChange={(e) =>
                setCapabilities([
                  ...e.target.value,
                  ...apiIds,
                  ...capabilityRefs,
                ])
              }
              renderValue={(selected) => (
                <Box sx={{ display: "flex", flexWrap: "wrap", gap: 0.5 }}>
                  {selected.map((id) => {
                    const c = (config?.capabilities || []).find(
                      (cap) => cap.id === id,
                    );
                    return (
                      <Chip key={id} size="small" label={c ? c.name : id} />
                    );
                  })}
                </Box>
              )}
            >
              {capabilityOptions.length === 0 && (
                <MenuItem disabled>{NO_CAPABILITIES_HINT}</MenuItem>
              )}
              {capabilityOptions.map((c) => (
                <MenuItem key={c.id} value={c.id} title={c.description}>
                  <Checkbox
                    checked={capabilityIds.includes(c.id)}
                    size="small"
                  />
                  <ListItemText primary={c.name} />
                </MenuItem>
              ))}
            </Select>
            <FormHelperText>{LEGACY_CAPABILITIES_HINT}</FormHelperText>
          </FormControl>
          <GrantsPicker
            config={config}
            grants={capabilities}
            onApiChange={(next) =>
              setCapabilities([...capabilityIds, ...next, ...capabilityRefs])
            }
            onSubmit={methodGrants.onSubmit}
            onRemove={methodGrants.onRemove}
            idPrefix="template"
          />
          {/* Renders nothing at all on a deployment that declares no
            fields, which is what leaves this form exactly as it was for
            everybody who has never declared one. */}
          <CustomFieldInputs
            config={config}
            values={customFields}
            onChange={(name, value) =>
              setCustomFields((prev) => ({ ...prev, [name]: value }))
            }
          />
        </DeclaredFields>
        <Stack
          direction="row"
          justifyContent={isNew ? "flex-end" : "space-between"}
          alignItems="center"
          sx={{ mt: 2 }}
        >
          {!isNew && (
            <Button color="error" onClick={remove} disabled={locked}>
              Delete
            </Button>
          )}
          <Stack direction="row" spacing={1}>
            <Button onClick={onClose}>{locked ? "Close" : "Cancel"}</Button>
            <Button type="submit" variant="contained" disabled={locked}>
              {isNew ? "Add template" : "Save"}
            </Button>
          </Stack>
        </Stack>
      </form>
    </Overlay>
  );
}

// Reloadable (StaleEdit.jsx), so a save refused as stale can start over
// from the saved copy.
export default withReload(TemplateOverlay);
