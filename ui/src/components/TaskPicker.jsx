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
import {
  ClickAwayListener,
  ListItemText,
  MenuItem,
  MenuList,
  Paper,
  Popper,
  TextField,
  Typography,
} from "@mui/material";
import { identifierField } from "../identifierField.js";

// TaskPicker is a search box that resolves free text to a task: type to
// filter by id or title, click (or arrow down + Enter) to pick one. It
// replaces the raw "type a task id and hope you copied it right" inputs
// that used to sit wherever a task needed to reference another one.
//
// No item glyph on these rows, unlike the repo picker built out of this
// one (ReadOnlyReposField). A task has a figure of its own since
// grain/task-372, so the reason is no longer that there is none to draw:
// it is that a row here already carries the two things that identify a
// task, its id and its title, and that every row in this list is a task
// -- stamping all eight with one figure that never varies would be
// decoration saying nothing the box above them does not. The figure
// earns its place where a task is named among things that are not tasks
// (the schedule editor's "A task" against "A suite"), or where it says
// what a whole list is (the rail's All tasks, the list's own heading).
//
// It holds no selection state of its own -- onPick fires once per pick
// and the field clears, so a caller wanting a persistent choice (a chip,
// a form field) keeps that in its own state and renders it alongside.
// That keeps a single-pick "attach immediately" use (DetailOverlay's
// dependency add) and a multi-pick "build a list, submit once" use
// (NewTaskOverlay's dependsOn) working off the same component.
export default function TaskPicker({
  tasks,
  exclude = [],
  onPick,
  placeholder = "Search tasks…",
  autoFocus = false,
}) {
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  const [highlight, setHighlight] = useState(0);
  const anchorRef = useRef(null);

  // Matches are newest first, and sorted here rather than taken in the
  // order tasks arrives in: that list is backlog order now (top-to-bottom
  // the order grain will run them, grain/task-201), and the eight rows
  // this shows would otherwise be the eight closest to being dispatched
  // rather than the eight most recently filed. Somebody picking a task to
  // depend on is nearly always reaching for one they have just seen or
  // just filed, which is what this order keeps within reach of a short
  // query.
  //
  // Recency only orders what is left once the id has had its say, though.
  // Somebody who types "14" means task 14, and a newer task whose title
  // happens to carry the digits ("Port acme/widgets#140 …", "Go 1.24")
  // must not take Enter from it -- that attaches the wrong dependency
  // with nothing on screen to say so. So an exact id match ranks first,
  // then ids that start with the query, then everything else newest
  // first. "#14", "task 14" and "task-14" are read as the id 14 too.
  const excludeSet = new Set(exclude);
  const q = query.trim().toLowerCase();
  const idQuery = q.replace(/^(#|task[\s-]*#?)\s*/, "");
  const rank = (t) => {
    const id = t.id.toLowerCase();
    if (idQuery === "") return 2;
    if (id === idQuery) return 0;
    if (id.startsWith(idQuery)) return 1;
    return 2;
  };
  const matches =
    q === ""
      ? []
      : tasks
          .filter((t) => !excludeSet.has(t.id))
          .filter(
            (t) =>
              t.id.toLowerCase().includes(q) ||
              t.title.toLowerCase().includes(q) ||
              (idQuery !== "" && t.id.toLowerCase().startsWith(idQuery)),
          )
          .sort(
            (a, b) =>
              rank(a) - rank(b) ||
              new Date(b.createdAt || 0) - new Date(a.createdAt || 0),
          )
          .slice(0, 8);

  useEffect(() => {
    setHighlight(0);
  }, [query]);

  const pick = (t) => {
    onPick(t);
    setQuery("");
    setOpen(false);
  };

  const onKeyDown = (e) => {
    if (e.key === "Escape") {
      // Closes the results rather than the drawer around this field, as
      // RepoPicker's does; with no results showing it bubbles as before.
      if (showResults) e.stopPropagation();
      setOpen(false);
      return;
    }
    if (matches.length === 0) return;
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setHighlight((h) => Math.min(h + 1, matches.length - 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setHighlight((h) => Math.max(h - 1, 0));
    } else if (e.key === "Enter") {
      e.preventDefault();
      pick(matches[highlight]);
    }
  };

  const showResults = open && q !== "";

  return (
    <ClickAwayListener onClickAway={() => setOpen(false)}>
      <div ref={anchorRef} style={{ position: "relative" }}>
        <TextField
          size="small"
          fullWidth
          placeholder={placeholder}
          value={query}
          autoFocus={autoFocus}
          autoComplete="off"
          inputProps={identifierField}
          onChange={(e) => {
            setQuery(e.target.value);
            setOpen(true);
          }}
          onFocus={() => setOpen(true)}
          onKeyDown={onKeyDown}
        />
        <Popper
          open={showResults}
          anchorEl={anchorRef.current}
          placement="bottom-start"
          style={{ width: anchorRef.current?.offsetWidth, zIndex: 1300 }}
        >
          <Paper
            variant="outlined"
            sx={{ mt: 0.5, maxHeight: 220, overflowY: "auto" }}
          >
            <MenuList dense>
              {matches.length === 0 && (
                <MenuItem disabled>
                  <Typography variant="body2" color="text.secondary">
                    No matching tasks
                  </Typography>
                </MenuItem>
              )}
              {matches.map((t, i) => (
                <MenuItem
                  key={t.id}
                  selected={i === highlight}
                  onClick={() => pick(t)}
                  onMouseEnter={() => setHighlight(i)}
                >
                  <ListItemText
                    primary={t.title}
                    secondary={t.id}
                    primaryTypographyProps={{ noWrap: true }}
                  />
                </MenuItem>
              ))}
            </MenuList>
          </Paper>
        </Popper>
      </div>
    </ClickAwayListener>
  );
}
