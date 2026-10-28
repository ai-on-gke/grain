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

import CancelIcon from "@mui/icons-material/Cancel";
import {
  Autocomplete,
  Box,
  Chip,
  MenuItem,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import { useState } from "react";
import {
  CUSTOM_PREFIX,
  declaredFields,
  fieldLabel,
  isOffered,
  legacyValues,
  valueList,
} from "../taskAttributes.js";

// The fields a deployment adds to its own tasks (model.CustomField),
// rendered in the two ways anything here ever needs them: as inputs on a
// form (CustomFieldInputs) and as what a task actually says (
// CustomFieldValues).
//
// The declarations come from GET /api/config, which is where the whole
// frontend gets them -- one list for the page, joined to a task's own
// values by name (ui.Task.CustomFields). Nothing here writes a
// declaration, and there is no pane that does: the set of fields is
// decided in the state repository, in a pull request an agent opens
// (grain/task-53). Settings says as much and shows what is declared.

// declaredFields (the config's fields, always an array), fieldLabel
// (what one is called on screen) and the three that read a declaration
// against a value it is holding (valueList, isOffered, legacyValues)
// are taskAttributes.js's, where a declaration is also what a filter
// and a sort are built out of (grain/task-64, grain/task-201 --
// "is a legacy value" is a comparison in the narrowing bar, so the
// predicate behind it cannot live in a view). Re-exported here because
// this is the file everything about a custom field is reached through.
export { declaredFields, fieldLabel, isOffered, legacyValues, valueList };

// initialFieldValues is what a form's inputs start holding: the values
// the thing being edited carries (a task, a template, a schedule) or
// that a link carried (paths.js's newTaskPath, so "the same customer,
// the same priority as that task" is one click), and otherwise each
// field's declared default.
//
// A prefilled value for a field this deployment no longer declares is
// dropped rather than kept invisibly: the form renders declarations, so
// a value with no field to render it in could only be filed by
// accident. A stale link therefore opens the form on everything else it
// named. (What a *task* carries for such a field is not dropped from
// the task -- the server leaves it alone precisely because no form can
// restate it; it is shown by CustomFieldValues below.)
//
// A value its own declaration has moved on from -- a retired enum
// option, a set left by a field that has stopped holding one -- is kept
// exactly as it stands, and the inputs below show it as what it is. An
// edit restating it is what keeps it on the task
// (model.NormalizeCustomFieldValuesFrom), so a form that quietly
// dropped it here would clear it the next time anybody saved anything.
// A form that *files* rather than edits has nothing to grandfather and
// wants fileableFieldValues instead.
export function initialFieldValues(config, prefill) {
  const values = {};
  for (const f of declaredFields(config)) {
    const given = prefill ? prefill[f.name] : undefined;
    const value = given !== undefined ? given : f.default || "";
    // A field that holds a set holds one whatever it was handed: a
    // string from a link or a declared default is the set of one it
    // stands for, so every input below and everything that submits them
    // sees the shape the declaration promises rather than two.
    values[f.name] = f.multiple ? valueList(value) : value;
  }
  return values;
}

// fileableFieldValues is initialFieldValues for a form that creates
// something rather than editing it, where a value the declarations
// would refuse is not a legacy value but a filing that cannot succeed:
// there is nothing already stored for a new task to be grandfathered
// against, so a retired option carried in by a "new task from this one"
// link would fail the create rather than opening the form.
//
// So the offer is narrowed to what can actually be filed today: values
// the declaration still produces, and one of them on a field that holds
// one. Everything else about the link is honoured, exactly as an
// undeclared name in it is dropped and the rest kept.
export function fileableFieldValues(config, prefill) {
  const values = initialFieldValues(config, prefill);
  for (const f of declaredFields(config)) {
    const offered = valueList(values[f.name]).filter((v) => isOffered(f, v));
    values[f.name] = f.multiple ? offered : offered[0] || "";
  }
  return values;
}

// LEGACY_NOTICE and LEGACY_SET_NOTICE are what a form says about one:
// what happened, and what the person in front of it can do about it.
// Neither is an error -- nothing is wrong with the task, and saving the
// form will work -- so they are worded as the state of affairs rather
// than as a complaint.
const LEGACY_NOTICE =
  "kept from an earlier declaration, which no longer offers it -- " +
  "it stays until you pick another value or clear it";
const LEGACY_CHIP_NOTICE =
  "one of these is kept from an earlier declaration, which no longer " +
  "offers it -- it stays until you remove it";
const LEGACY_SET_NOTICE =
  "kept from when this field held a set, which it no longer does -- " +
  "they stay until you remove all but one";

// CustomFieldInputs is one input per declared field, typed the way the
// declaration says: a picker for an enum, a date picker for a date, a
// checkbox for a boolean, a number box for a number and a text box for
// everything else.
//
// A field declared `multiple` gets a box of chips instead
// (MultiValueInput), whichever of the two types it is.
//
// values is a {name: value} map and onChange(name, value) reports one
// edit -- the caller holds the map, since it is the caller that submits
// it. One value is a string, except on a multi-valued field, where it is
// always an array: the shape a field's value has here is the
// declaration's to say, so nothing downstream has to ask twice.
// Renders nothing at all for a deployment that declares no fields,
// which is what keeps the form it sits on exactly as it was for
// everybody who has never declared one.
export function CustomFieldInputs({ config, values, onChange }) {
  const fields = declaredFields(config);
  if (fields.length === 0) return null;
  return (
    <Stack spacing={1.5} sx={{ mt: 2 }}>
      <Typography variant="caption" color="text.secondary">
        Fields
      </Typography>
      {fields.map((f) => (
        <CustomFieldInput
          key={f.name}
          field={f}
          value={f.multiple ? valueList(values[f.name]) : values[f.name] || ""}
          onChange={(v) => onChange(f.name, v)}
        />
      ))}
    </Stack>
  );
}

// CustomFieldInput is one field's control on its own, which is what a
// form rendering the fields one at a time needs (BulkEditOverlay puts
// each behind a tick of its own, so it cannot take the whole block).
// Exported for that; a form wanting all of them still asks for
// CustomFieldInputs above rather than looping here itself.
export function CustomFieldInput({ field, value, onChange }) {
  const label = fieldLabel(field);
  if (field.multiple) {
    return <MultiValueInput field={field} values={value} onChange={onChange} />;
  }
  const held = valueList(value);
  if (held.length > 1) {
    // A set on a field that holds one value: no single control can show
    // it, and showing one of them would be showing the task as saying
    // something it does not say.
    return <LegacySetInput field={field} values={held} onChange={onChange} />;
  }
  // The single-valued controls take a string, whichever shape the value
  // arrived in -- the wire sends one value as a bare string and several
  // as an array (ui.CustomFieldValue), and a set pared down to one
  // value by the control above arrives here as an array of one.
  const single = typeof value === "string" ? value : held[0] || "";
  // What this control cannot otherwise show: an enum option that has
  // been retired, a date or a number stored when the field was
  // something else. It is added to the picker, or shown in a box that
  // can hold it, rather than left to render as a blank that the next
  // save would turn into a real clear.
  const legacy = isOffered(field, single) ? "" : single.trim();
  // A picker's value is picked and never typed, and the store trims a
  // value before it compares it (model.CustomField.NormalizeValue), so
  // a value that differs from an option by nothing but space *is* that
  // option and the row for it should be the one that shows as chosen.
  // The boxes below are left alone: trimming what somebody is typing
  // takes the space bar off them.
  const picked = single.trim();
  const helperText = legacy ? LEGACY_NOTICE : field.description || undefined;
  const helperProps = legacy
    ? { FormHelperTextProps: { sx: { color: "warning.main" } } }
    : {};
  if (field.type === "boolean") {
    return (
      <TextField
        select
        label={label}
        value={picked}
        onChange={(e) => onChange(e.target.value)}
        helperText={helperText}
        {...helperProps}
        InputLabelProps={{ shrink: true }}
        SelectProps={{ displayEmpty: true }}
        fullWidth
        size="small"
      >
        {/* Boolean fields have three answers in the model: yes, no and
            nothing said. A checkbox only has two visible states, which
            made an explicit false indistinguishable from an unset field
            and left no direct way to move between them. */}
        <MenuItem value="">
          <em>Not set</em>
        </MenuItem>
        <MenuItem value="true">Yes</MenuItem>
        <MenuItem value="false">No</MenuItem>
        {legacy && <MenuItem value={legacy}>{legacyLabel(legacy)}</MenuItem>}
      </TextField>
    );
  }
  const common = {
    label,
    value: single,
    onChange: (e) => onChange(e.target.value),
    helperText,
    ...helperProps,
    fullWidth: true,
    size: "small",
  };
  if (field.type === "enum") {
    return (
      <TextField {...common} value={picked} select>
        {/* The empty choice is how a picked value is taken back off
            again -- there is no required field here (model.CustomField.
            NormalizeValue), so "nothing" has to stay reachable. */}
        <MenuItem value="">
          <em>none</em>
        </MenuItem>
        {(field.options || []).map((o) => (
          <MenuItem key={o} value={o}>
            {o}
          </MenuItem>
        ))}
        {/* The value this task actually holds, when the declaration has
            stopped offering it. Without a row of its own the select
            renders empty over a value that is still there, and Save
            sends it back anyway -- so the box said "none", the store
            said "critical", and the request was refused for restating
            what nobody could see. */}
        {legacy && <MenuItem value={legacy}>{legacyLabel(legacy)}</MenuItem>}
      </TextField>
    );
  }
  if (field.type === "date") {
    // A stored value this input cannot hold is shown in one that can:
    // <input type="date"> blanks anything that is not a calendar date,
    // which would hide it and then clear it on the next save.
    if (legacy) return <TextField {...common} autoComplete="off" />;
    return (
      <TextField {...common} type="date" InputLabelProps={{ shrink: true }} />
    );
  }
  if (field.type === "number") {
    if (legacy) return <TextField {...common} autoComplete="off" />;
    return <TextField {...common} type="number" />;
  }
  return <TextField {...common} autoComplete="off" />;
}

// legacyLabel is how such a value reads in a picker: the value itself,
// which is the part being chosen, and what is unusual about it.
function legacyLabel(value) {
  return `${value} (no longer offered)`;
}

// LegacySetInput is the control for a field left holding several values
// by a declaration that has stopped saying `multiple`: a chip per
// value, each removable, and nothing to add with.
//
// It is what "surface it rather than hide it" means for this case. The
// single-valued controls above can show one value, so before this a
// task carrying two showed one of them or none, and any save silently
// kept whichever the form had picked. Here the task says what it says,
// every value can be taken off, and the moment one is left the field
// goes back to its ordinary control -- which is the whole migration
// path, done a value at a time by whoever is looking at it.
//
// Nothing to add with, because there is nothing a new value could mean:
// the field takes one value now, and the way to set that one value is
// to get down to one first. Removing every chip clears the field, the
// same as emptying any other control here.
function LegacySetInput({ field, values, onChange }) {
  const label = fieldLabel(field);
  return (
    <Box>
      <Typography variant="caption" color="text.secondary" component="div">
        {label}
      </Typography>
      <Stack
        direction="row"
        spacing={0.5}
        useFlexGap
        flexWrap="wrap"
        sx={{ my: 0.5 }}
      >
        {values.map((v) => (
          <Chip
            key={v}
            size="small"
            label={v}
            onDelete={() => onChange(values.filter((other) => other !== v))}
            deleteIcon={<CancelIcon titleAccess={`remove ${v}`} />}
          />
        ))}
      </Stack>
      <Typography variant="caption" color="warning.main" component="div">
        {LEGACY_SET_NOTICE}
      </Typography>
      {field.description && (
        <Typography variant="caption" color="text.secondary" component="div">
          {field.description}
        </Typography>
      )}
    </Box>
  );
}

// MultiValueInput is the input for a field that holds a set
// (model.CustomField.Multiple): a box of chips, one per value, added by
// typing or by picking.
//
// One control for both types the flag is allowed on, because the two
// differ in one prop. A multi-valued enum offers its declared options
// and nothing else, exactly as the single-valued one does; a
// multi-valued text field offers no list at all, so anything typed and
// entered is a value (freeSolo). The alternative -- a row of checkboxes
// for the enum, a comma-separated text box for the text -- would be two
// controls to learn, and the comma would be a separator a value could
// legitimately contain.
//
// onChange reports the whole set, deduplicated, since a set is what the
// caller submits and what the server will store either way
// (model.CustomField.NormalizeValues).
function MultiValueInput({ field, values, onChange }) {
  const label = fieldLabel(field);
  const enumerated = field.type === "enum";
  // The values in the box that the declaration has stopped offering --
  // an option retired out of tables/custom_field.json since somebody
  // ticked it. They are chips like the rest, marked and removable: a
  // set is edited a value at a time already, so the migration path here
  // is the one the control has always had.
  const legacy = new Set(values.filter((v) => !isOffered(field, v)));
  // What is typed in the box, held here so leaving it can still read it:
  // the Autocomplete clears its own copy on blur before ours runs.
  const [typed, setTyped] = useState("");
  return (
    <Autocomplete
      multiple
      fullWidth
      size="small"
      // A text field has no vocabulary to offer, so what is typed is the
      // value; an enum has one, and only its own options are values.
      freeSolo={!enumerated}
      // A free-text value is committed when the box loses focus too.
      // Requiring Enter made clicking a form's Save/Create button discard
      // the text still visible in this input.
      autoSelect={!enumerated}
      // An enum's Enter picks the first option still listed. Without it
      // Enter did nothing until an arrow key had highlighted one, so
      // typing "backend" and pressing Enter left no chip and the text
      // was dropped on submit (docs/test-runs/
      // 2026-10-12-task-intake-inbox-lifecycle.md). autoSelect stays off
      // for an enum: with an option always highlighted, merely tabbing
      // through the box would add the first one.
      autoHighlight={enumerated}
      inputValue={typed}
      onInputChange={(_evt, next) => setTyped(next)}
      // The enum's half of committing on blur: text naming an option
      // exactly (case aside) becomes that option, as Enter would. Text
      // matching none is still discarded, as it is no value.
      onBlur={() => {
        if (!enumerated) return;
        const want = typed.trim().toLowerCase();
        const match = (field.options || []).find(
          (o) => String(o).toLowerCase() === want,
        );
        if (want && match !== undefined && !values.includes(match)) {
          onChange([...values, match]);
        }
      }}
      options={enumerated ? field.options || [] : []}
      value={values}
      onChange={(_evt, next) => {
        // Match the store's text normalization while the chips are still
        // on screen: trim first, then drop empties and duplicates, so a
        // phantom chip cannot disappear or merge only after Save.
        const normalized = [];
        for (const raw of next) {
          const value = String(raw).trim();
          if (value && !normalized.includes(value)) normalized.push(value);
        }
        onChange(normalized);
      }}
      renderTags={(tags, getTagProps) =>
        tags.map((tag, index) => {
          const { key, ...rest } = getTagProps({ index });
          return (
            <Chip
              key={key}
              size="small"
              label={tag}
              color={legacy.has(tag) ? "warning" : undefined}
              title={legacy.has(tag) ? legacyLabel(tag) : undefined}
              {...rest}
            />
          );
        })
      }
      renderInput={(params) => (
        <TextField
          {...params}
          label={label}
          helperText={
            legacy.size > 0
              ? LEGACY_CHIP_NOTICE
              : field.description || undefined
          }
          FormHelperTextProps={
            legacy.size > 0 ? { sx: { color: "warning.main" } } : undefined
          }
          // The chips are the value, so a browser offering to complete
          // the box from what was typed into some other form is noise.
          autoComplete="off"
        />
      )}
    />
  );
}

// CustomFieldValues is what a task says about its fields, for a pane
// that shows rather than edits: one row per value, labelled by its
// declaration.
//
// Only fields the task actually carries a value for are listed. A pane
// that printed every declared field with "--" beside most of them would
// bury the one or two that were filled in, and "nobody said" is the
// ordinary answer.
//
// A value whose declaration has since been deleted from the state
// repository is still shown, under its bare name: it is what somebody
// fieldRows is the join between the declarations and one task's values:
// declared fields first, in the order the deployment declared them,
// then anything the task carries that nothing declares any more, by
// name. Exported for the panes that lay the rows out themselves.
// A field carrying several values is one row, its values written the
// way a person would write them: "store, prompt". They are a set
// (model.CustomField.Multiple), so there is no order to lay out down
// the pane and nothing a row each would say that the one row does not.
export function fieldRows(config, values) {
  const held = values || {};
  const rows = [];
  const seen = new Set();
  for (const f of declaredFields(config)) {
    if (!f || !f.name || seen.has(f.name)) continue;
    seen.add(f.name);
    const value = displayValue(held[f.name], f);
    if (!value) continue;
    // Shown like any other value, and said to be what it is: a value
    // its own declaration has moved on from is still what somebody
    // recorded about this task, and a reader wondering why "critical"
    // is not in the picker should be able to find out by hovering it
    // rather than by reading the state repository.
    const legacy = legacyValues(f, held[f.name]);
    rows.push({
      name: f.name,
      label: fieldLabel(f),
      value,
      legacy: legacy.length > 0 ? legacy : undefined,
    });
  }
  for (const name of Object.keys(held).sort()) {
    if (seen.has(name)) continue;
    const value = displayValue(held[name]);
    if (!value) continue;
    // Legacy in the fullest sense: not only is the value not one the
    // deployment offers, there is no declaration left to offer it.
    rows.push({
      name,
      label: name,
      value,
      undeclared: true,
      legacy: valueList(held[name]),
    });
  }
  return rows;
}

// legacyRowTitle is the hover a row carrying a legacy value gets, and
// undefined for the ordinary row that carries none -- so nothing is
// added to a pane on a deployment whose declarations have not moved.
export function legacyRowTitle(row) {
  if (!row || !row.legacy) return undefined;
  if (row.undeclared) {
    return `${row.label} is no longer declared on this deployment -- this is what the task still says`;
  }
  return `${row.legacy.join(", ")}: kept from an earlier declaration, which no longer offers it`;
}

// displayValue is one field's values as the single line a pane shows --
// "" for a field the task says nothing about, whichever of the shapes
// the wire uses that arrived as (ui.CustomFieldValue).
//
// A declared boolean says Yes or No, the way its own editor and the
// list's filter menu already do (taskAttributes.js's BOOLEAN_LABELS),
// rather than the "true" the store holds. Anything else it holds is
// shown as written, which is what the editor's legacy row does too.
const BOOLEAN_SHOWN = { true: "Yes", false: "No" };
function displayValue(value, field) {
  const values = valueList(value);
  if (field?.type !== "boolean") return values.join(", ");
  return values.map((v) => BOOLEAN_SHOWN[v] || v).join(", ");
}

// ROW_FIELD_LIMIT is how many field values one row or card will carry.
//
// A row already says a good deal -- state, number, title, what the run
// is doing, repo, base, read-only repos, capabilities, origin, phase,
// blocked -- and it says all of it on one line that does not wrap, so
// every chip added to it is width the title gives up. Three is what a
// row can spare and still be a list of task titles; a deployment with
// six declared fields cannot have all six here, whatever it flags.
//
// The limit is applied here rather than refused at declaration time
// (model.CustomField.ShowOnRow says why): the row is the only thing that
// knows how much space there is, and a declaration flagging four fields
// is a preference, not a mistake to fail a pull request over.
const ROW_FIELD_LIMIT = 3;

// rowFields is which declared fields a row or a card shows the value of,
// in the order it shows them -- the join between the two halves of that
// question.
//
// The narrowing's half comes first: a list *sorted* by a field is in an
// order the reader cannot otherwise see, which is the one case where the
// value has to be on the row, and it has to be the field that survives
// the limit rather than the one dropped by it. Every custom field the
// sort names is here, in the order the sort names them, so a tie-broken
// "Priority, then Due date" shows both.
//
// The declaration's half follows: the fields flagged `showOnRow`
// (model.CustomField.ShowOnRow), in declared order -- the ones this
// deployment reads its backlog by, on every row whether or not anything
// is being asked about them.
//
// Filters deliberately add nothing. A condition already says what it is
// asking on the toolbar's own chip ("Priority is any of high"), and what
// it does to the list is leave only the rows that answer it -- so
// repeating that answer down every row is the one thing on this row that
// a reader could have worked out without reading it. The case that
// argues the other way is a condition admitting several values, where
// the rows genuinely differ; sorting by that same field is how a reader
// asks for the difference to be visible, and that is already what the
// first half of this does.
export function rowFields(config, sort) {
  const declared = declaredFields(config);
  if (declared.length === 0) return [];
  const byName = new Map(
    declared.filter((f) => f && f.name).map((f) => [f.name, f]),
  );
  const chosen = [];
  const seen = new Set();
  const take = (f) => {
    if (!f || seen.has(f.name)) return;
    seen.add(f.name);
    chosen.push(f);
  };
  for (const { attr } of sort || []) {
    // Only the custom half of the vocabulary has a field behind it: a
    // sort by "State" or "Newest" names no declaration, and a sort by a
    // field this deployment has since dropped names one that is gone
    // (taskAttributes.js's CUSTOM_PREFIX and attributeById take the same
    // view of a stale name).
    if (typeof attr !== "string" || !attr.startsWith(CUSTOM_PREFIX)) continue;
    take(byName.get(attr.slice(CUSTOM_PREFIX.length)));
  }
  for (const f of declared) if (f && f.name && f.showOnRow) take(f);
  return chosen.slice(0, ROW_FIELD_LIMIT);
}

// CustomFieldChips is those fields as one task carries them: a chip per
// field the task actually has a value for, "Priority high", the name set
// quietly against the value that is the point of reading it.
//
// A field this task says nothing about gets no chip rather than an empty
// one -- "nobody said" is the ordinary answer for a field
// (model.CustomField.NormalizeValue), and a row of blanks would cost the
// title its width to say nothing. A task sorted to the bottom for having
// no priority is already saying that by being at the bottom.
//
// `fields` is what rowFields chose; nothing here re-decides it, so a row
// and the card of the same task on a board show the same values.
export function CustomFieldChips({ fields, values }) {
  return (fields || []).map((f) => {
    const value = displayValue((values || {})[f.name]);
    if (!value) return null;
    const label = fieldLabel(f);
    return (
      <Chip
        key={f.name}
        size="small"
        variant="outlined"
        className="chip-field"
        title={`${label}: ${value}`}
        label={
          <>
            <span className="chip-field-name">{label}</span>
            {value}
          </>
        }
      />
    );
  });
}
