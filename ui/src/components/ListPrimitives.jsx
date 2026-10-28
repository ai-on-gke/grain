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

import { Fragment, useEffect, useRef, useState } from "react";
import {
  FormControl,
  IconButton,
  InputAdornment,
  InputLabel,
  MenuItem,
  Select,
  TextField,
  Typography,
} from "@mui/material";
import CloseIcon from "@mui/icons-material/Close";
import {
  MoveHandle,
  MoveSlot,
  MovingBar,
  gapsOf,
  usePickup,
} from "./ReorderPickup.jsx";
import { identifierField } from "../identifierField.js";
import {
  PageHeaderActions,
  PageHeaderPageActions,
  useInPageHeader,
} from "./PageHeader.jsx";

// ListHeader/ListToolbar/ListEmpty are the pieces TaskList, RepoList,
// TemplatesList, and SchedulesList already agreed to share by convention
// (bwsalmon/agents#561 made RepoList's rows match the other three's, but
// left each page hand-rolling its own .content-header/.task-list-toolbar
// markup). Pulling them out here means a fifth list page -- or a future
// edit to one of these four -- gets the shared shape by construction
// instead of by copying the right CSS class names onto the right divs.
//
// The fifth page (SuitesList) took the toolbar too in grain/task-327,
// which is also where ReorderableList at the bottom of this file comes
// from: the four non-task pages had no order of their own to offer, so
// their rows carried no drag handle and no sort menu and read as blank
// beside the task rows next door.
//
// Deliberately not included: the row itself. TaskRow stays exported from
// TaskList.jsx for any view that wants a task row identical to that
// list's own; the four pages' *own* rows carry different columns (a
// repo's per-state counts, a template's single title line, a schedule's
// second "next run" line) that don't reduce to one shape without either
// losing information or growing a prop for every page's special case.

// `icon` is the page's own glyph (ItemGlyph.jsx) -- the same figure the
// nav entry that got here carries, at the size a heading can hold, so
// the icon in the rail and the page it opens are visibly the same
// thing. Ahead of the title and
// outside the heading itself, so it is decoration and the heading's
// accessible name stays the one word.
//
// Both this and ListToolbar are drawn into the page's header rail when
// there is one (PageHeader.jsx, grain/task-4915). The heading itself
// stays where it was, in the page's own body and so inside its main
// landmark, hidden from sight because the rail's title says what the
// page is; the rail gets the count and the action, under the title as
// a label for a page with a second list (style.css shows it on every
// header in the rail but the first).
export function ListHeader({ title, count, action, icon, style }) {
  const inRail = useInPageHeader();
  const heading = (
    <Typography
      variant="h6"
      component="h2"
      className={inRail ? "visually-hidden" : undefined}
      sx={{ m: 0, fontSize: "1rem", fontWeight: 600 }}
    >
      {title}
    </Typography>
  );
  if (inRail) {
    return (
      <>
        {heading}
        <PageHeaderActions>
          <div className="content-header" style={style}>
            {icon ? <span className="header-icon">{icon}</span> : null}
            <span className="content-header-label" aria-hidden="true">
              {title}
            </span>
            {count != null && <span className="count">{count}</span>}
            {action ? (
              <PageHeaderPageActions>{action}</PageHeaderPageActions>
            ) : null}
          </div>
        </PageHeaderActions>
      </>
    );
  }
  return (
    <div className="content-header" style={style}>
      {icon ? <span className="header-icon">{icon}</span> : null}
      {heading}
      {count != null && <span className="count">{count}</span>}
      {action}
    </div>
  );
}

export function ListToolbar({ children }) {
  return (
    <PageHeaderActions>
      <div className="task-list-toolbar">{children}</div>
    </PageHeaderActions>
  );
}

// The flex basis is what keeps this box readable on a narrow screen.
// With `flex: 1` and no minimum, a toolbar that also holds a sort menu
// and two or three filter menus squeezed the search box down to about
// 28px of usable width on a 390px phone rather than wrapping -- a box
// you cannot see what you typed in. Given a basis of its own it takes a
// line to itself instead, and the menus wrap under it.
//
// The × at the right-hand end is how a search is put back, and it only
// exists while there is something to put back -- an empty box has no
// button in it to wonder about. Backspacing a phrase away was the only
// way out of a search until now, which is a dozen keystrokes for a
// gesture every other search box on the machine does in one click. The
// task views have a Clear in the toolbar beside this box, but that one
// undoes the filters too (NarrowingBar.jsx's clearNarrowing) and the
// other four list pages have nothing of the sort.
//
// Clearing puts the focus back in the box rather than leaving it on a
// button that has just disappeared out from under it: the click is
// nearly always the start of typing the next search, and a focus left
// on a removed element lands back on <body>, which is where keyboard
// navigation starts over from.
export function ListSearchField({ value, onChange, placeholder }) {
  const input = useRef(null);
  return (
    <TextField
      size="small"
      placeholder={placeholder}
      value={value}
      inputRef={input}
      onChange={(e) => onChange(e.target.value)}
      className="list-search-field"
      sx={{ flex: "1 1 12rem", minWidth: 180, maxWidth: 320 }}
      slotProps={{
        // htmlInput rather than the inputProps most of this UI passes,
        // because this field already spells its Input slot out here --
        // the two are the same prop under two names (identifierField.js).
        htmlInput: identifierField,
        input: {
          endAdornment: value ? (
            <InputAdornment position="end">
              <IconButton
                size="small"
                edge="end"
                aria-label="Clear the search box"
                title="Clear the search box"
                onClick={() => {
                  onChange("");
                  input.current?.focus();
                }}
              >
                <CloseIcon fontSize="small" />
              </IconButton>
            </InputAdornment>
          ) : null,
        },
      }}
    />
  );
}

// ListSortSelect's options take the same { [id]: { label } } shape as
// each page's own local SORTS map, so callers keep the cmp functions
// (which this component has no need of) next to the labels instead of
// having to split one map into two.
export function ListSortSelect({ id, value, onChange, options }) {
  const labelId = `${id}-label`;
  return (
    <FormControl size="small" sx={{ minWidth: 170 }}>
      <InputLabel id={labelId}>Sort</InputLabel>
      <Select
        labelId={labelId}
        label="Sort"
        value={value}
        onChange={(e) => onChange(e.target.value)}
      >
        {Object.entries(options).map(([key, { label }]) => (
          <MenuItem key={key} value={key}>
            {label}
          </MenuItem>
        ))}
      </Select>
    </FormControl>
  );
}

// There was a ListFilterSelect here too -- one attribute, one value
// picked, "Any repo" as its first entry, each option carrying the item
// glyph of the kind it named (grain/task-38) -- which the task list and
// the board each rendered one of per attribute. Both of them narrow
// themselves with conditions now (NarrowingBar.jsx, grain/task-42),
// whose controls are a row rather than a labelled Select, and no other
// list page ever had a filter to offer. It is gone rather than kept
// against a sixth page wanting it: the four that exist have a search box
// and a sort menu between them. The glyphs went with it, into the value
// menu the editor puts in its place -- an attribute still says which
// kind its values are (taskAttributes.js's glyphOf).

// RowLink is the name on a list row that opens on click, made the way a
// keyboard reaches that row too (grain/task-1429): a real link to the
// item's own deep link, so Tab lands on it, Enter follows it, a screen
// reader announces it as a link by the name the row shows, and
// ctrl/cmd/middle-click opens it in a new tab like any other link. A
// plain left click is kept in the page -- onOpen is the same in-place
// navigation the row's own click does, so one Back returns to the list.
//
// It replaces openableRowProps (grain/task-1358), which made the row
// itself the tab stop: a focusable <div> with no role is announced as
// its whole text run together, and the rows hold buttons of their own (a
// drag handle, a checkbox, "Use…", "Run…"), so the row cannot become a
// button either -- nesting interactive controls inside one is invalid.
// The row keeps its pointer click; the link is the keyboard's way in.
//
// The click stops at the link so the row's own onClick does not open the
// item a second time, and draggable={false} keeps a drag that starts on
// the name dragging the row it sits in (ReorderableList, TaskList,
// BoardList) rather than the link's URL.
//
// Most rows open a pane over the list, whose dialog hands focus back to
// the link when it closes. A board or a repo opens a page in the list's
// place instead, so coming Back -- the page's own "← Boards" or the
// browser's -- mounts a fresh list with focus left on <body>, and a
// keyboard user starts again from the top of the document
// (grain/task-1580). The link that opened it remembers its href by list
// section (so opening a task inside a board does not clobber the board's
// own remembered href) and, on mounting again with nothing else focused,
// takes focus back.
const returnFocusTo = new Map();

function returnFocusKey(href) {
  return typeof href === "string" ? href.split("/")[1] || "" : "";
}

function rememberReturnFocus(href) {
  if (typeof href === "string" && href) {
    returnFocusTo.set(returnFocusKey(href), href);
  }
}

export function RowLink({ href, onOpen, className, children, ...rest }) {
  const ref = useRef(null);
  useEffect(() => {
    const key = returnFocusKey(href);
    if (returnFocusTo.get(key) !== href) return;
    returnFocusTo.delete(key);
    const active = document.activeElement;
    if (active && active !== document.body) return;
    ref.current?.focus();
  }, [href]);
  return (
    <a
      ref={ref}
      href={href}
      className={className ? `row-link ${className}` : "row-link"}
      draggable={false}
      onClick={(e) => {
        e.stopPropagation();
        if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey)
          return;
        e.preventDefault();
        rememberReturnFocus(href);
        onOpen();
      }}
      {...rest}
    >
      {children}
    </a>
  );
}

// focusRowLink is for a row's own pointer click, the one that lands
// anywhere but its RowLink: on a timeline's bar, a card's chips, the
// space at the end of a list row. The task pane that click opens hands
// focus back on closing to whatever held it on opening, and a click on
// a plain <div> leaves that as <body> -- so Escape dropped a keyboard
// user at the top of the document rather than on the row they opened
// (2026-10-13 and 2026-10-15 boards walkthroughs). Focusing the row's
// own link first -- and remembering its href when the row replaces the
// list page, as on /boards and /repos -- gives Back and Escape the same
// element to return to that a click on the title, or Enter on it,
// already gives them.
export function focusRowLink(e) {
  const link = e.currentTarget.querySelector(".row-link");
  if (!link) return;
  rememberReturnFocus(link.getAttribute("href"));
  link.focus({ preventScroll: true });
}

export function ListEmpty({ children }) {
  return <p className="empty">{children}</p>;
}

// ReorderableList is TaskList's own drag-to-reorder gesture -- a handle
// on every row, a rule drawn where the drop would land, a zone under the
// last row for "put it at the end" -- for the four list pages whose
// order is a display order rather than a backlog (listOrder.js,
// grain/task-327). It owns the <ul>, the <li>s and the drag state; the
// caller renders whatever its own row is, which is the part no two of
// these pages agree on.
//
// `reorder` is null on a list that is not currently in its custom order:
// no handles, nothing draggable, no drop zone. Same rule TaskList
// applies to the backlog (taskFilters.js's SORTS) -- while a list is
// sorted by name or by date, a drop has nowhere meaningful to land, so
// the gesture is withdrawn rather than left to silently rewrite an order
// the view no longer shows.
//
// The row is a render prop rather than a component prop so the handle
// can go *inside* it: the handle belongs in the row's own flex line,
// ahead of the name, and only the caller knows where that is.
//
// The handle is also the tap-to-move affordance these lists get on a
// touchscreen (ReorderPickup.jsx): tapping it picks the row up and the
// list draws a "Move here" slot at every position it could go to, since
// a phone browser never fires the drag events the <li>s below use.
//
// `nameOf` is what a row is called, for the labels those slots and
// handles carry ("Move here, above Nightly release") -- the one thing
// about a row this component cannot read off `items` itself. A list that
// leaves it out still reorders; its buttons just say "Move here" with no
// mention of what they sit above, which is thin on a screen reader.
// `noun` is the same for the bar at the top ("Moving 1 schedule").
export function ReorderableList({
  className,
  items,
  idOf,
  nameOf,
  noun,
  reorder,
  children,
}) {
  // dragId is the row being dragged right now, or null. overId is
  // purely the drop-target highlight -- "__end__" for the trailing zone
  // -- and never drives the actual move, which only happens in onDrop.
  const [dragId, setDragId] = useState(null);
  const [overId, setOverId] = useState(null);
  const enabled = !!reorder;

  // The tap-to-move half of the same gesture. A pick-up made while the
  // list was in its custom order means nothing once it isn't, so a
  // change of sort puts the row back down -- the same withdrawal
  // `enabled` makes of the drag.
  const { picked, toggle, cancel } = usePickup();
  const pickedPresent =
    enabled &&
    Boolean(picked?.ids?.length) &&
    items.some((i) => idOf(i) === picked.ids[0]);
  useEffect(() => {
    if (!enabled || (picked && !pickedPresent)) cancel();
  }, [enabled, picked, pickedPresent, cancel]);
  const pickedId = pickedPresent ? picked.ids[0] : null;

  const stop = () => {
    setDragId(null);
    setOverId(null);
  };
  const drop = (beforeId) => {
    if (dragId !== null && dragId !== beforeId) reorder(dragId, beforeId);
    stop();
  };
  const move = (beforeId) => {
    if (pickedId !== null && pickedId !== beforeId) reorder(pickedId, beforeId);
    cancel();
  };

  const name = (item) => (nameOf ? nameOf(item) : null);

  // The rows a pick-up can land between, and which of the gaps between
  // them are worth a slot: gapsOf leaves out the one the row is already
  // in, which for a single row lifted out of the middle of a list is one
  // of the two either side of where it was.
  const kept =
    pickedId !== null ? items.filter((i) => idOf(i) !== pickedId) : items;
  const gapBefore = new Map(kept.map((i, n) => [idOf(i), n]));
  const gaps =
    pickedId !== null ? new Set(gapsOf(items.map(idOf), [pickedId])) : null;

  return (
    <>
      {pickedId !== null && (
        <MovingBar
          count={1}
          noun={noun}
          nowhere={gaps.size === 0}
          onCancel={cancel}
        />
      )}
      <ul className={className}>
        {items.map((item) => {
          const id = idOf(item);
          const over = dragId !== null && dragId !== id && overId === id;
          const gap = gapBefore.has(id) ? gapBefore.get(id) : null;
          // One handle per row rather than one shared element, now that
          // it carries this row's own name and whether this row is the
          // one picked up. The click it swallows is the row's own
          // onClick (which on all four of these pages opens the thing
          // the row names) rather than the drag.
          const handle = enabled ? (
            <MoveHandle
              label={
                pickedId === id
                  ? `Stop moving ${name(item) || "this row"}`
                  : `Move ${name(item) || "this row"}`
              }
              picked={pickedId === id}
              disabled={items.length < 2}
              onToggle={() => toggle([id])}
            />
          ) : null;
          return (
            <Fragment key={id}>
              {pickedId !== null && gap !== null && gaps.has(gap) && (
                <MoveSlot
                  className="reorder-slot-gutter"
                  label={
                    name(item) ? `Move here, above ${name(item)}` : "Move here"
                  }
                  onMove={() => move(id)}
                />
              )}
              <li
                className={over ? "task-drop-target" : undefined}
                draggable={enabled}
                onDragStart={enabled ? () => setDragId(id) : undefined}
                onDragEnd={enabled ? stop : undefined}
                onDragOver={
                  enabled
                    ? (e) => {
                        if (dragId === null || dragId === id) return;
                        e.preventDefault();
                        setOverId(id);
                      }
                    : undefined
                }
                onDrop={
                  enabled
                    ? (e) => {
                        e.preventDefault();
                        drop(id);
                      }
                    : undefined
                }
              >
                {children(item, {
                  handle,
                  dragging: dragId === id,
                  picked: pickedId === id,
                })}
              </li>
            </Fragment>
          );
        })}
        {pickedId !== null && gaps.has(kept.length) && (
          <MoveSlot
            className="reorder-slot-gutter"
            label="Move here, to the end of the list"
            onMove={() => move(null)}
          />
        )}
        {enabled && dragId !== null && (
          <li
            className={`task-drop-end${overId === "__end__" ? " task-drop-target" : ""}`}
            onDragOver={(e) => {
              e.preventDefault();
              setOverId("__end__");
            }}
            onDrop={(e) => {
              e.preventDefault();
              drop(null);
            }}
          />
        )}
      </ul>
    </>
  );
}
