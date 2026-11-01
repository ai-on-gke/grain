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
  Button,
  Checkbox,
  Chip,
  IconButton,
  ListItemText,
  Menu,
  MenuItem,
  Select,
  TextField,
} from "@mui/material";
import AddIcon from "@mui/icons-material/Add";
import CloseIcon from "@mui/icons-material/Close";
import FilterListIcon from "@mui/icons-material/FilterList";
import SwapVertIcon from "@mui/icons-material/SwapVert";
import {
  NONE_LABEL_FALLBACK,
  TYPE_DATE,
  attributeById,
  attributesFor,
  valueLabel,
} from "../taskAttributes.js";
import {
  NONE,
  OPERATORS,
  conditionFor,
  describeCondition,
  describeSort,
  describeUnknownConditions,
  directionLabels,
  isNarrowed,
  newCondition,
  operatorsFor,
  optionsFor,
  resolveConditions,
  unknownConditions,
} from "../taskFilters.js";
import { GlyphLabel } from "./ItemGlyph.jsx";
import { ListSearchField, ListToolbar } from "./ListPrimitives.jsx";
import { identifierField } from "../identifierField.js";

// NarrowingBar is the toolbar every task view narrows itself with: the
// search box, the filter editor, the sort editor and the one Clear that
// undoes them (grain/task-42).
//
// It is one component rather than a pattern the list and the board each
// follow, for the reason taskFilters.js is one module: the two views ask
// the same question, so an attribute added to that file has to appear on
// both, and a control added here has to behave the same on both. The
// board's Columns button is what is *not* shared -- a view passes its
// own controls through as children, and they sit at the end of the same
// toolbar row. (The list used to pass two: "Show closed tasks" and "Show
// deferred tasks". They are gone -- what they said is the condition
// every list opens narrowed by, and the chips under this row say it.)
//
// # Why an editor rather than a row of menus
//
// Until this change the toolbar was one Select per attribute, each
// holding a single value. That shape could only ever say "repo is
// acme/widgets": not "either of these two repos", not "created this
// week", not "title mentions flake", and not one word about any
// attribute beyond the seven that had a menu -- and every new attribute
// cost another permanent Select in a row that was already six wide on a
// laptop.
//
// A condition is a sentence instead -- attribute, comparison, value --
// added when it is wanted and gone when it is not, so the toolbar is
// three controls wide whether the vocabulary is seven attributes or
// seventy. That is also what makes room for the fields a deployment
// declares for itself (taskAttributes.js's attributesFor): they arrive
// in the same menus as everything else, with no space to find for them.
//
// # Where the state lives
//
// Nowhere here except which panels are open. The narrowing itself is
// App's and the URL's (paths.js), so a link carries the whole question;
// this component reads it and hands back patches. Panel open/closed is
// deliberately not in the URL: it is whether you are still editing, not
// what you are asking, and a link that reopened somebody else's editor
// over their list would be saying something it does not mean.
export default function NarrowingBar({
  tasks,
  config,
  narrowing,
  onNarrow,
  // What this view opens narrowed by, which is what Clear puts it back
  // to and what "narrowed" is measured against (taskFilters.js's
  // isNarrowed). The task list passes the deployment's own default
  // condition, and so does a dashboard of panels, since a board that
  // narrows nothing is a view nobody has narrowed (boards.js's
  // boardNarrowing, grain/task-309). A Kanban board and a timeline pass
  // nothing: each opens on its own saved question and answers for
  // itself which states it is about, so this is a view that starts out
  // asking nothing.
  defaults = [],
  searchPlaceholder = "Search tasks…",
  // What this toolbar is over, for the views that are not over tasks.
  //
  // The CI pane narrows runs with these same conditions (ciFilters.js),
  // and it can: nothing in this component or in taskFilters.js touches a
  // task's own fields -- a condition only ever asks its attribute
  // descriptor for values. So a view brings its own vocabulary and its
  // own word for the order it has when nothing is sorted, and gets the
  // same toolbar a task list wears. One toolbar rather than two, because
  // a second set of filter controls in the same UI is a second thing for
  // a reader to learn and a second place for the same question to be
  // written differently.
  attributes: ownAttributes = null,
  orderLabel = "Backlog order",
  orderNote = "The backlog's own order, which is the one a drag changes.",
  children,
}) {
  const attributes = ownAttributes || attributesFor(config);
  const { search, sort, filters } = narrowing;
  // The sort keys this view has an attribute for. A key naming none --
  // a stale or mistyped link's `?sort=bogus` -- has no row in the
  // editor (SortRow) and no words in describeSort, so the button and
  // the editor go by these: going by `sort` itself drew the sort button
  // with no label at all and an editor offering "Then by…" under
  // nothing (found in the 2026-11-01 Processes walkthrough).
  const knownSort = sort.filter((key) => attributeById(attributes, key.attr));
  const conditions = resolveConditions(filters, attributes);
  // The conditions resolveConditions dropped, which get a chip of their
  // own saying so (taskFilters.js's unknownConditions). Not until the
  // config has arrived on a view over tasks: before it, the fields this
  // deployment declares are not attributes yet, and a link narrowed by
  // one would flash a warning about a condition that is about to work.
  const unknown =
    ownAttributes || config ? unknownConditions(filters, attributes) : [];

  // The two panels open on their own: a list narrowed by a link opens
  // with both closed and its conditions shown as chips, and clicking a
  // chip opens the editor on them.
  const [editingFilters, setEditingFilters] = useState(false);
  const [editingSort, setEditingSort] = useState(false);

  const patchFilters = (next) => onNarrow({ filters: next });
  const addCondition = (attr) => {
    patchFilters([...filters, newCondition(attr)]);
    setEditingFilters(true);
  };
  const setCondition = (index, cond) =>
    patchFilters(filters.map((c, i) => (i === index ? cond : c)));
  const removeCondition = (index) =>
    patchFilters(filters.filter((_, i) => i !== index));

  const patchSort = (next) => onNarrow({ sort: next });
  const addSortKey = (attr) => {
    // A date's useful end is the recent one, so "Created" starts on
    // newest first; everything else starts at the top of its own order.
    patchSort([
      ...sort,
      { attr: attr.id, dir: attr.type === TYPE_DATE ? "desc" : "asc" },
    ]);
    setEditingSort(true);
  };
  const setSortKey = (index, key) =>
    patchSort(sort.map((k, i) => (i === index ? key : k)));
  const removeSortKey = (index) =>
    patchSort(sort.filter((_, i) => i !== index));

  // clearNarrowing puts the view back to the one it opens as -- the one
  // gesture for getting out of a search and half a dozen conditions,
  // which is otherwise as many gestures as it took to get in. Back to
  // `defaults` rather than to nothing at all, since on the task list
  // that default is itself a condition (the set-aside states) and
  // clearing to nothing would fill the list with closed tasks and leave
  // no gesture for undoing it. The sort is not part of it: an order is
  // not something a list has to be rescued from.
  const clearNarrowing = () => onNarrow({ search: "", filters: defaults });

  return (
    <>
      <ListToolbar>
        <ListSearchField
          placeholder={searchPlaceholder}
          value={search}
          onChange={(value) => onNarrow({ search: value })}
        />
        <Button
          size="small"
          startIcon={<FilterListIcon />}
          aria-expanded={editingFilters}
          title="Add and edit the conditions this list is narrowed by"
          onClick={() => setEditingFilters((open) => !open)}
        >
          {conditions.length > 0 ? `Filters (${conditions.length})` : "Filter"}
        </Button>
        <Button
          size="small"
          startIcon={<SwapVertIcon />}
          aria-expanded={editingSort}
          title="Choose the order this list is shown in"
          onClick={() => setEditingSort((open) => !open)}
        >
          {knownSort.length > 0 ? describeSort(sort, attributes) : orderLabel}
        </Button>
        {isNarrowed(narrowing, defaults) && (
          <Button
            size="small"
            title="Clear the search and every filter"
            onClick={clearNarrowing}
          >
            Clear
          </Button>
        )}
        {children}
      </ListToolbar>
      {/* What the closed editor leaves behind: every condition as a
          sentence, deletable where it stands, and opening the editor on
          click. A narrowed list should say what it is narrowed by
          without anybody having to open anything.

          A condition naming an attribute this deployment does not have
          is not applied, and says so here whether or not the editor is
          open -- it has no row there, since there is no attribute to
          put in one, and a list quietly showing more than it was asked
          for reads exactly like one whose question matched it all. */}
      {((!editingFilters && conditions.length > 0) || unknown.length > 0) && (
        <div className="narrowing-chips">
          {!editingFilters &&
            conditions.map(({ attr, cond, index }) => (
              <Chip
                key={index}
                size="small"
                label={describeCondition(attr, cond, config)}
                onClick={() => setEditingFilters(true)}
                onDelete={() => removeCondition(index)}
              />
            ))}
          {unknown.map(({ cond, index, badOp }) => (
            <Chip
              key={`unknown-${index}`}
              size="small"
              color="warning"
              variant="outlined"
              label={describeUnknownConditions([{ cond, badOp }])}
              title={
                badOp
                  ? `“${cond?.op}” is not a comparison “${cond?.attr}” offers, so this condition is not applied`
                  : `“${cond?.attr}” is not an attribute here, so this condition is not applied`
              }
              onDelete={() => removeCondition(index)}
            />
          ))}
        </div>
      )}
      {editingFilters && (
        <div className="narrowing-panel" role="group" aria-label="Filters">
          {conditions.map(({ attr, cond, index }, row) => (
            <ConditionRow
              key={index}
              row={row + 1}
              attr={attr}
              cond={cond}
              attributes={attributes}
              tasks={tasks}
              config={config}
              onChange={(next) => setCondition(index, next)}
              onRemove={() => removeCondition(index)}
            />
          ))}
          <AttributeMenu
            label={conditions.length > 0 ? "Add filter" : "Filter by…"}
            attributes={attributes}
            onPick={addCondition}
          />
        </div>
      )}
      {editingSort && (
        <div className="narrowing-panel" role="group" aria-label="Sort">
          {sort.map((key, index) => (
            <SortRow
              key={index}
              row={index + 1}
              sortKey={key}
              attributes={attributes}
              onChange={(next) => setSortKey(index, next)}
              onRemove={() => removeSortKey(index)}
            />
          ))}
          <AttributeMenu
            label={knownSort.length > 0 ? "Then by…" : "Sort by…"}
            attributes={attributes}
            onPick={addSortKey}
          />
          {knownSort.length === 0 && (
            <span className="narrowing-note">{orderNote}</span>
          )}
        </div>
      )}
    </>
  );
}

// AttributeMenu is the "which attribute?" step both editors start from:
// one button, every attribute this deployment has, custom ones included.
// A menu rather than a Select because there is nothing selected -- it is
// an action that adds a row, and a Select showing the last attribute
// added would read as a filter that is already applied.
function AttributeMenu({ label, attributes, onPick }) {
  const [anchor, setAnchor] = useState(null);
  return (
    <>
      <Button
        size="small"
        startIcon={<AddIcon />}
        onClick={(e) => setAnchor(e.currentTarget)}
      >
        {label}
      </Button>
      <Menu
        anchorEl={anchor}
        open={!!anchor}
        onClose={() => setAnchor(null)}
        MenuListProps={{ dense: true }}
      >
        {attributes.map((a) => (
          <MenuItem
            key={a.id}
            onClick={() => {
              setAnchor(null);
              onPick(a);
            }}
          >
            {a.label}
          </MenuItem>
        ))}
      </Menu>
    </>
  );
}

// ConditionRow is one condition as it is edited: attribute, comparison,
// value, and the button that takes it away. It reads left to right as
// the sentence it is -- "Repo | is any of | acme/widgets" -- which is
// also why the three controls carry aria-labels naming the row rather
// than sharing one visible label each: with four conditions on screen,
// "Attribute" four times over says nothing about which is which.
function ConditionRow({
  row,
  attr,
  cond,
  attributes,
  tasks,
  config,
  onChange,
  onRemove,
}) {
  const ops = operatorsFor(attr);
  return (
    <div className="narrowing-row">
      <Select
        size="small"
        value={attr.id}
        inputProps={{ "aria-label": `Attribute for filter ${row}` }}
        onChange={(e) => {
          const next = attributes.find((a) => a.id === e.target.value);
          if (next) onChange(conditionFor(next, cond));
        }}
      >
        {attributes.map((a) => (
          <MenuItem key={a.id} value={a.id}>
            {a.label}
          </MenuItem>
        ))}
      </Select>
      <Select
        size="small"
        value={cond.op}
        inputProps={{ "aria-label": `Comparison for filter ${row}` }}
        onChange={(e) =>
          // The values go with the comparison that asked for them: a set
          // of choices is not a date, and keeping them would leave the
          // row filtering on something its own controls no longer show.
          onChange({
            ...cond,
            op: e.target.value,
            values:
              OPERATORS[e.target.value]?.input === OPERATORS[cond.op]?.input
                ? cond.values
                : [],
          })
        }
      >
        {ops.map((op) => (
          <MenuItem key={op} value={op}>
            {OPERATORS[op].label}
          </MenuItem>
        ))}
      </Select>
      <ConditionValue
        row={row}
        attr={attr}
        cond={cond}
        tasks={tasks}
        config={config}
        onChange={(values) => onChange({ ...cond, values })}
      />
      <IconButton
        size="small"
        aria-label={`Remove filter ${row}`}
        onClick={onRemove}
      >
        <CloseIcon fontSize="small" />
      </IconButton>
    </div>
  );
}

// ConditionValue is whatever the chosen comparison compares against: a
// menu of the values the tasks in view actually carry, a line of text, a
// number, a date, a number of days -- or nothing at all, for the two
// comparisons ("is empty", "is set") that are complete without one.
function ConditionValue({ row, attr, cond, tasks, config, onChange }) {
  const label = `Value for filter ${row}`;
  const input = OPERATORS[cond.op]?.input;
  if (input === "none") return null;
  if (input === "choices") {
    const options = optionsFor(attr, tasks, config, cond.values);
    const glyphed = options.some((o) => o.glyph);
    return (
      <Select
        size="small"
        multiple
        displayEmpty
        className="narrowing-values"
        value={cond.values}
        inputProps={{ "aria-label": label }}
        onChange={(e) => onChange(e.target.value)}
        renderValue={(picked) =>
          picked.length === 0
            ? "Any"
            : picked
                .map((v) =>
                  v === NONE
                    ? attr.noneLabel || NONE_LABEL_FALLBACK
                    : valueLabel(attr, v, config),
                )
                .join(", ")
        }
      >
        {options.length === 0 && (
          <MenuItem disabled value="">
            No values to choose from
          </MenuItem>
        )}
        {options.map((o) => (
          <MenuItem key={o.value} value={o.value}>
            <Checkbox size="small" checked={cond.values.includes(o.value)} />
            {/* The figure of the kind this value names, where it names
                one -- a repo, a schedule, a suite (ItemGlyph.jsx,
                grain/task-38). One glyphed option glyphs the whole menu,
                so "No repo" holds the slot open and empty rather than
                starting its label a figure's width to the left. */}
            {glyphed ? (
              <GlyphLabel kind={o.glyph || null}>{o.label}</GlyphLabel>
            ) : (
              <ListItemText primary={o.label} />
            )}
          </MenuItem>
        ))}
      </Select>
    );
  }
  const type = input === "date" ? "date" : input === "text" ? "text" : "number";
  return (
    <>
      <TextField
        size="small"
        type={type}
        className="narrowing-value"
        value={cond.values[0] || ""}
        inputProps={{ ...identifierField, "aria-label": label }}
        onChange={(e) =>
          onChange(e.target.value === "" ? [] : [e.target.value])
        }
      />
      {input === "days" && <span className="narrowing-note">days</span>}
      {input === "hours" && <span className="narrowing-note">hours</span>}
    </>
  );
}

// SortRow is one key of the sort: which attribute, which way. Several of
// them are a real sort rather than a list of alternatives -- the first
// decides, the rest break its ties -- which is what "State, then Newest
// first" means in the toolbar's own button.
function SortRow({ row, sortKey, attributes, onChange, onRemove }) {
  const attr = attributes.find((a) => a.id === sortKey.attr);
  if (!attr) return null;
  const labels = directionLabels(attr);
  return (
    <div className="narrowing-row">
      <Select
        size="small"
        value={attr.id}
        inputProps={{ "aria-label": `Attribute for sort ${row}` }}
        onChange={(e) => onChange({ ...sortKey, attr: e.target.value })}
      >
        {attributes.map((a) => (
          <MenuItem key={a.id} value={a.id}>
            {a.label}
          </MenuItem>
        ))}
      </Select>
      <Select
        size="small"
        value={sortKey.dir}
        inputProps={{ "aria-label": `Direction for sort ${row}` }}
        onChange={(e) => onChange({ ...sortKey, dir: e.target.value })}
      >
        <MenuItem value="asc">{labels.asc}</MenuItem>
        <MenuItem value="desc">{labels.desc}</MenuItem>
      </Select>
      <IconButton
        size="small"
        aria-label={`Remove sort ${row}`}
        onClick={onRemove}
      >
        <CloseIcon fontSize="small" />
      </IconButton>
    </div>
  );
}
