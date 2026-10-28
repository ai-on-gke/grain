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
  Box,
  Button,
  FormControl,
  IconButton,
  InputLabel,
  MenuItem,
  Select,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import ArrowDownwardIcon from "@mui/icons-material/ArrowDownward";
import ArrowUpwardIcon from "@mui/icons-material/ArrowUpward";
import DeleteOutlineIcon from "@mui/icons-material/DeleteOutline";
import {
  DISPLAY_STATE_ORDER,
  STATE_LABELS,
  displayState,
  stateLabels,
  statesOf,
} from "../state.js";
import {
  defaultColumns,
  hiddenStates,
  newColumn,
  normalizeColumns,
} from "../board.js";
import Overlay from "./Overlay.jsx";

// BoardColumnsOverlay edits the board's columns: what they are called,
// which states each one collects, what order they run in left to right
// (board.js).
//
// A centered dialog rather than a pane, by Overlay.jsx's own rule: this
// is one form you fill in and dismiss, taken over the board you are
// already looking at, not a destination with a URL of its own.
//
// The whole edit is held here and handed back once, on Save -- so
// Cancel, or Escape, or a click on the backdrop, leaves the board
// exactly as it was however far through a rearrangement somebody got.
// Nothing here writes to localStorage either; TaskBoard does that with
// what Save hands it, which keeps "what is on screen" and "what is
// stored" the same decision in one place.
export default function BoardColumnsOverlay({ columns, onSave, onClose }) {
  const [draft, setDraft] = useState(() =>
    columns.map((c) => ({ ...c, states: [...c.states] })),
  );
  // What the editor opened on, to tell an edit from a look: Escape,
  // "← Back" and browser Back used to close it and drop a changed board
  // without asking (2026-10-07 standing-work walkthrough).
  const [opened] = useState(() => JSON.stringify(columns));
  const isDirty = () => JSON.stringify(draft) !== opened;

  const update = (id, change) =>
    setDraft((prev) =>
      prev.map((c) => (c.id === id ? { ...c, ...change } : c)),
    );

  // A state belongs to exactly one column: assigning it here takes it
  // out of whichever column had it. The alternative -- letting two
  // columns claim "Running" and quietly showing the task in the leftmost
  // -- would render a board that disagrees with the editor that made it.
  const setStates = (id, states) =>
    setDraft((prev) =>
      prev.map((c) => {
        if (c.id === id) return { ...c, states };
        return { ...c, states: c.states.filter((s) => !states.includes(s)) };
      }),
    );

  // Where focus goes once the draft has re-rendered, for the three
  // edits that would otherwise leave it nowhere: a column moved to
  // either end has the arrow just pressed disabled under the finger, a
  // removed column takes its own buttons away, and a new column is
  // somewhere to type. A focused button that is disabled or unmounted
  // drops focus to the dialog, and a keyboard reader had to Tab back
  // from the top to find the column they were moving (2026-10-13
  // boards walkthrough).
  const controls = useRef(new Map());
  const control = (key) => (el) => {
    if (el) controls.current.set(key, el);
    else controls.current.delete(key);
  };
  const [focusNext, setFocusNext] = useState(null);
  useEffect(() => {
    if (focusNext === null) return;
    controls.current.get(focusNext)?.focus();
    setFocusNext(null);
  }, [focusNext, draft]);

  const move = (index, delta) => {
    const to = index + delta;
    if (to < 0 || to >= draft.length) return;
    const next = [...draft];
    [next[index], next[to]] = [next[to], next[index]];
    setDraft(next);
    // At either end the arrow pressed is now disabled; the other one
    // still moves the same column, so that is where focus belongs.
    if (to === 0) setFocusNext(`${draft[index].id}:right`);
    else if (to === next.length - 1) setFocusNext(`${draft[index].id}:left`);
  };

  const remove = (id) => {
    const index = draft.findIndex((c) => c.id === id);
    const next = draft.filter((c) => c.id !== id);
    setDraft(next);
    // The column that slid into its place, or the one before it when
    // the last went, or "+ Add column" when none are left.
    const neighbour = next[index] || next[index - 1];
    setFocusNext(neighbour ? `${neighbour.id}:remove` : "add");
  };

  const add = () => {
    const column = newColumn();
    setDraft((prev) => [...prev, column]);
    setFocusNext(`${column.id}:title`);
  };

  const save = () => {
    // normalizeColumns is the same pass a stored layout goes through on
    // the way in, so an edit can't produce a board a reload wouldn't:
    // an emptied title falls back to the states' own labels, and a
    // draft somebody deleted every column of falls back to the default
    // rather than saving a board with nothing on it.
    onSave(normalizeColumns(draft) || defaultColumns());
    onClose();
  };

  // What the board will not be showing once this is saved, said while
  // there is still a column to put it in. Closed tasks are the ordinary
  // case (the default board has no column for them) so this is
  // information rather than a warning.
  const offBoard = hiddenStates(draft);

  return (
    <Overlay onClose={onClose} wide label="Board columns" isDirty={isDirty}>
      <Typography variant="h6" component="h2" sx={{ mb: 0.5 }}>
        Board columns
      </Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        Each column collects the states you give it, left to right. A state can
        only be in one column, and a state in no column is left off the board
        altogether — which is how closed tasks stay out of the way by default.
        Kept in this browser, like the theme.
      </Typography>

      <Stack spacing={1.2}>
        {draft.map((c, i) => (
          // A column's title, its states and the three buttons that move
          // or remove it are one line on a screen with the width for it
          // and four wrapped lines on a phone, where an unwrapped row
          // squeezed the title box down to about 28px -- a field you
          // cannot see what you typed in (grain/task-23). Both fields
          // are given a basis rather than a fixed width so they take
          // what is there and wrap when it runs out.
          <Box
            key={c.id}
            sx={{
              display: "flex",
              alignItems: "center",
              flexWrap: "wrap",
              gap: 1,
            }}
          >
            <TextField
              size="small"
              label="Title"
              value={c.title}
              onChange={(e) => update(c.id, { title: e.target.value })}
              sx={{ flex: "1 1 12rem", minWidth: 160, maxWidth: 200 }}
              inputRef={control(`${c.id}:title`)}
              inputProps={{ "aria-label": `Column ${i + 1} title` }}
            />
            <FormControl size="small" sx={{ flex: "1 1 14rem", minWidth: 200 }}>
              {/* shrink, because the select below always draws something
                  in the box -- the states picked, or "No states" from
                  displayEmpty. A label MUI has not floated out is drawn
                  across whichever it is, and a column with no states yet
                  is exactly when somebody is reading this field. */}
              <InputLabel id={`board-col-${c.id}-label`} shrink>
                States
              </InputLabel>
              <Select
                multiple
                labelId={`board-col-${c.id}-label`}
                label="States"
                // Both asked states are one entry, "Asked", in the
                // picker as they are everywhere else (state.js's
                // displayState); picking it collects both. A column kept
                // from before that names only one of them still shows
                // "Asked" picked, and keeps only that one until it is
                // toggled.
                value={[...new Set(c.states.map(displayState))]}
                onChange={(e) => {
                  const picked =
                    typeof e.target.value === "string"
                      ? e.target.value.split(",")
                      : e.target.value;
                  setStates(
                    c.id,
                    picked.flatMap((s) =>
                      s === "asked"
                        ? c.states.some((x) => displayState(x) === "asked")
                          ? c.states.filter((x) => displayState(x) === "asked")
                          : statesOf(s)
                        : [s],
                    ),
                  );
                }}
                renderValue={(picked) =>
                  picked.length === 0
                    ? "No states"
                    : stateLabels(picked).join(", ")
                }
                displayEmpty
              >
                {DISPLAY_STATE_ORDER.map((s) => (
                  <MenuItem key={s} value={s}>
                    {STATE_LABELS[s] || s}
                  </MenuItem>
                ))}
              </Select>
            </FormControl>
            <IconButton
              size="small"
              ref={control(`${c.id}:left`)}
              aria-label={`Move ${c.title} left`}
              disabled={i === 0}
              onClick={() => move(i, -1)}
            >
              <ArrowUpwardIcon
                fontSize="small"
                sx={{ transform: "rotate(-90deg)" }}
              />
            </IconButton>
            <IconButton
              size="small"
              ref={control(`${c.id}:right`)}
              aria-label={`Move ${c.title} right`}
              disabled={i === draft.length - 1}
              onClick={() => move(i, 1)}
            >
              <ArrowDownwardIcon
                fontSize="small"
                sx={{ transform: "rotate(-90deg)" }}
              />
            </IconButton>
            <IconButton
              size="small"
              ref={control(`${c.id}:remove`)}
              aria-label={`Remove ${c.title}`}
              onClick={() => remove(c.id)}
            >
              <DeleteOutlineIcon fontSize="small" />
            </IconButton>
          </Box>
        ))}
      </Stack>

      {draft.length === 0 && (
        <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
          No columns. Saving now puts the default board back.
        </Typography>
      )}

      {offBoard.length > 0 && (
        <Typography
          variant="caption"
          color="text.secondary"
          component="p"
          sx={{ mt: 1.5 }}
        >
          Off the board: {stateLabels(offBoard).join(", ")}.
        </Typography>
      )}

      <Box sx={{ display: "flex", gap: 1, mt: 2.5 }}>
        <Button size="small" ref={control("add")} onClick={add}>
          + Add column
        </Button>
        <Button size="small" onClick={() => setDraft(defaultColumns())}>
          Reset to default
        </Button>
        <Box sx={{ flex: 1 }} />
        <Button size="small" onClick={onClose}>
          Cancel
        </Button>
        <Button size="small" variant="contained" onClick={save}>
          Save
        </Button>
      </Box>
    </Overlay>
  );
}
