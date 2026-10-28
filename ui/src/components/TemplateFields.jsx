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

import {
  Box,
  Button,
  Checkbox,
  FormControlLabel,
  IconButton,
  MenuItem,
  Stack,
  TextField,
  Typography,
} from "@mui/material";

// The inputs a template asks for (model.TemplateField, grain/task-58),
// in the two shapes a frontend ever needs them: the rows that *declare*
// them, on the template's own form (TemplateFieldsEditor), and the boxes
// that *fill them in*, on the form that files a task from that template
// (TemplateFieldInputs).
//
// A field is a named hole written into the template's title and body as
// `{{name}}`. It is a text box when it offers no options and a picker
// when it offers some -- that is the whole of the difference between the
// two kinds, here as in the model, so neither of these components has a
// type to choose or to keep in step with anything.
//
// Nothing here substitutes anything. What the values do to the prose is
// the server's answer (model.SubstituteFields), shown back through the
// composition preview the filing form sits beside, so a preview cannot
// promise something the filing would not do.

// initialTemplateFieldValues is what the fill-in form starts holding:
// every declared field on its declared default, which is also what an
// unattended firing of the same template would carry.
export function initialTemplateFieldValues(fields) {
  const values = {};
  for (const f of fields || []) values[f.name] = f.default || "";
  return values;
}

// TemplateFieldInputs is one input per field a template asks for: a
// picker where the field offers options, a text box where it does not.
//
// values is a {name: value} map and onChange(name, value) reports one
// edit, CustomFieldInputs' own division of labour -- the caller holds
// the map because the caller is what submits it. Renders nothing for a
// template that asks for nothing, which is most of them, and which is
// what leaves the form it sits on exactly as it was.
export function TemplateFieldInputs({ fields, values, onChange, onCommit }) {
  if (!fields || fields.length === 0) return null;
  return (
    <Stack spacing={1.5} sx={{ mt: 2 }}>
      <Typography variant="caption" color="text.secondary">
        Fill in
      </Typography>
      {fields.map((f) => (
        <TemplateFieldInput
          key={f.name}
          field={f}
          value={values[f.name] || ""}
          onChange={(v) => onChange(f.name, v)}
          // onCommit is a value somebody has settled on rather than one
          // they are still typing -- what a caller that asks the server
          // something per value (a composition preview) listens to
          // instead of onChange. A caller with nothing to send until
          // submit leaves it out (WriteReposField's own split).
          onCommit={onCommit && ((v) => onCommit(f.name, v))}
        />
      ))}
    </Stack>
  );
}

function TemplateFieldInput({ field, value, onChange, onCommit }) {
  const common = {
    label: field.label || field.name,
    value,
    onChange: (e) => onChange(e.target.value),
    onBlur: onCommit ? (e) => onCommit(e.target.value) : undefined,
    required: field.required,
    // The name is what the template's own prose refers to
    // (`{{service}}`), so it is worth saying under a box whose label is
    // a human sentence -- that is how somebody editing the template
    // recognises which hole they are filling.
    helperText: `{{${field.name}}}`,
    fullWidth: true,
    size: "small",
  };
  if ((field.options || []).length > 0) {
    return (
      <TextField {...common} select InputLabelProps={{ shrink: true }}>
        {/* The empty choice is how a value is taken back off again on a
            field nothing requires: blank means "take the default", which
            is what every unattended firing of this template carries. A
            required field has no default to fall back to, so it offers
            no way to say nothing. */}
        {!field.required && (
          <MenuItem value="">
            <em>{field.default ? `default (${field.default})` : "none"}</em>
          </MenuItem>
        )}
        {(field.options || []).map((o) => (
          <MenuItem key={o} value={o}>
            {o}
          </MenuItem>
        ))}
      </TextField>
    );
  }
  return <TextField {...common} autoComplete="off" />;
}

// templateFieldsPayload is the editor's rows as the API takes them:
// names and labels trimmed, blank option lines dropped, and a row nobody
// named dropped whole -- an "+ Add input" pressed by mistake is not a
// reason to refuse the save. The declaration is checked properly on the
// server (model.ValidateTemplateFields); this is only about what the
// form leaves lying around.
export function templateFieldsPayload(fields) {
  return (fields || [])
    .map((f) => ({
      ...f,
      name: (f.name || "").trim(),
      label: (f.label || "").trim(),
      options: (f.options || []).map((o) => o.trim()).filter((o) => o !== ""),
    }))
    .filter((f) => f.name !== "");
}

// TemplateFieldsEditor is the declaration half: one row per field, each
// with the name its `{{name}}` refers to, the label a form puts over it,
// the options that turn it into a picker (one per line -- a comma is
// something an option may legitimately contain), a default and whether
// it has to be filled in.
//
// The whole list lives in the caller's state and is submitted as the
// whole answer, the way every other picker on these forms works: the
// form renders every field, so what it sends is the complete
// declaration, empty list included (ui.UpdateTemplateRequest.Fields).
//
// The names are not checked here. The server refuses a name that is not
// a key and a default outside its own options
// (model.ValidateTemplateFields), and a second opinion in the browser is
// a second place for the rule to be written down slightly differently.
export function TemplateFieldsEditor({ fields, onChange }) {
  const set = (i, patch) =>
    onChange(fields.map((f, n) => (n === i ? { ...f, ...patch } : f)));
  const remove = (i) => onChange(fields.filter((_, n) => n !== i));
  const add = () =>
    onChange([...fields, { name: "", label: "", options: [], default: "" }]);

  return (
    <Box sx={{ mt: 2, mb: 1 }}>
      <Typography variant="caption" color="text.secondary">
        Inputs{" "}
        <span className="hint">
          optional -- write {"{{name}}"} in the title or description above, and
          whoever files a task from this template fills it in
        </span>
      </Typography>
      <Stack gap={1.5} sx={{ mt: 1 }}>
        {fields.map((f, i) => (
          <Stack
            key={i}
            direction="row"
            gap={1}
            alignItems="flex-start"
            className="template-field-row"
          >
            {/* Each box carries its own accessible name as well as its
                label: four rows of "Name" and "Default" on one form,
                beside the template's own Name, is ambiguous read aloud
                and ambiguous to anything else that finds a field by its
                label (WriteReposField's own per-row ariaLabel). */}
            <TextField
              label="Name"
              inputProps={{ "aria-label": `Input ${i + 1} name` }}
              value={f.name}
              onChange={(e) => set(i, { name: e.target.value })}
              placeholder="service"
              autoComplete="off"
              size="small"
              sx={{ width: "10rem" }}
            />
            <TextField
              label="Label"
              inputProps={{ "aria-label": `Input ${i + 1} label` }}
              value={f.label || ""}
              onChange={(e) => set(i, { label: e.target.value })}
              placeholder="optional"
              autoComplete="off"
              size="small"
              sx={{ width: "10rem" }}
            />
            {/* One option per line rather than a comma-separated box:
                an option is a human-written label ("blocked, waiting on
                legal") and a separator a value may contain is not a
                separator (model.CustomField's options column makes the
                same argument about its own JSON). No options at all is
                what leaves this field a plain text box. */}
            <TextField
              label="Options"
              inputProps={{ "aria-label": `Input ${i + 1} options` }}
              value={(f.options || []).join("\n")}
              // Split and nothing else: trimming or dropping blank
              // lines as they are typed would take the newline back out
              // from under the person typing the next option, and the
              // space out of the middle of one. Both happen once, at
              // submit (templateFieldsPayload).
              onChange={(e) => set(i, { options: e.target.value.split("\n") })}
              placeholder={"one per line\nleave empty for a text box"}
              multiline
              minRows={1}
              size="small"
              sx={{ flex: 1, minWidth: "8rem" }}
            />
            <TextField
              label="Default"
              inputProps={{ "aria-label": `Input ${i + 1} default` }}
              value={f.default || ""}
              onChange={(e) => set(i, { default: e.target.value })}
              autoComplete="off"
              size="small"
              sx={{ width: "9rem" }}
            />
            <FormControlLabel
              control={
                <Checkbox
                  size="small"
                  checked={!!f.required}
                  onChange={(e) => set(i, { required: e.target.checked })}
                />
              }
              label="Required"
              sx={{ mt: 0.5, whiteSpace: "nowrap" }}
            />
            <IconButton
              size="small"
              aria-label={`Remove input ${f.name || i + 1}`}
              title={`Remove input ${f.name || i + 1}`}
              onClick={() => remove(i)}
              sx={{ mt: 0.5 }}
            >
              ×
            </IconButton>
          </Stack>
        ))}
      </Stack>
      <Button size="small" onClick={add} sx={{ mt: 1 }}>
        + Add input
      </Button>
    </Box>
  );
}
