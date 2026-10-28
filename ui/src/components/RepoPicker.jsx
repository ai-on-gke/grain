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
import { GlyphLabel } from "./ItemGlyph.jsx";
import { identifierField } from "../identifierField.js";

// looksLikeRepo mirrors model.ParseRepo, which is what the server runs a
// repo through (ui.parseReads, ui.parseWrites): owner/name, or host/owner/name
// split on "/", no segment empty. Checked here so a typo is caught while
// it is still being typed rather than as a rejected submit that takes
// the rest of the form down with it.
function looksLikeRepo(text) {
  const parts = text.split("/");
  if (parts.length === 2) {
    return parts[0].length > 0 && parts[1].length > 0;
  }
  if (parts.length === 3) {
    return parts[0].length > 0 && parts[1].length > 0 && parts[2].length > 0;
  }
  return false;
}

// parseRepoList reads free text as the repos it names: a single
// owner/name, or the comma-separated list the read-only repos field used
// to be a plain text input for. Empty unless every segment is a repo, so
// the "add what I typed" row below is only ever offered for text the
// server would accept. Keeping the comma form working means a list
// pasted out of an older task, or out of somebody's notes, still lands
// in one go instead of having to be picked apart by hand.
function parseRepoList(text) {
  const parts = text
    .split(",")
    .map((r) => r.trim())
    .filter((r) => r !== "");
  if (parts.length === 0 || !parts.every(looksLikeRepo)) return [];
  return parts;
}

// RepoPicker is the box the repo fields are built out of (grain/task-241
// wrote it inside ReadOnlyReposField; grain/task-54 lifted it out when a
// second field needed the same box): type to search, arrow keys and
// Enter or a click to pick, every repo already picked kept out of the
// results, and free text that parses as owner/name offered as its own
// "add this" row at the end.
//
// It picks repos and does not hold them. What a picked repo *becomes* is
// the caller's business -- a chip, for the read-only repos, or a row
// with a branch box beside it for the writeable ones -- and those are
// different enough shapes that sharing the list too would have meant a
// component with a mode switch in it. onPick is handed an array because
// one row can name several: the comma-separated form above is one paste
// of somebody's notes, and splitting it into one call per repo would
// make a caller holding state batch them back together itself.
//
// Two differences from TaskPicker, both because repos are not tasks. The
// options list is short and known up front (knownRepos, state.js), so
// focusing the box offers it straight away instead of waiting for a
// query -- there is nothing to narrow down when the whole list fits on
// screen. And options is never the full universe of valid repos, the
// same gap RepoField's "Other…" exists to cover, which is why typed text
// stays reachable at all.
export default function RepoPicker({
  label,
  // ariaLabel names the box for a caller that renders it without a
  // visible caption of its own -- the "add another repo" box the repos
  // field puts under the repo a task is filed against
  // (WriteReposField), where a second caption would be announcing a
  // second field when the whole point is that there is only one. The
  // name itself stays: a box with none is one a screen reader can only
  // read as "edit text".
  ariaLabel,
  helperText,
  placeholder = "Search repos, or type owner/name…",
  options = [],
  picked = [],
  onPick,
}) {
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  const [highlight, setHighlight] = useState(0);
  const anchorRef = useRef(null);
  // queryRef shadows query for the benefit of the blur handler below,
  // which has to know what is in the box *now* rather than what was in
  // it when the render that installed the handler ran -- a click on a
  // result blurs and picks in the same tick, and only a ref is already
  // cleared by the time the blur is handled.
  const queryRef = useRef("");

  const already = new Set(picked);
  const q = query.trim().toLowerCase();
  const matches = options
    .filter((r) => !already.has(r))
    .filter((r) => r.toLowerCase().includes(q))
    .slice(0, 8);

  // The typed text itself, offered only when it is a repo nothing in the
  // list already covers -- an exact match on an option would otherwise
  // show the same repo twice, once as itself and once as "add".
  const typed = parseRepoList(query).filter((r) => !already.has(r));
  const custom =
    typed.length > 0 && !(typed.length === 1 && matches.includes(typed[0]))
      ? typed
      : null;

  // rows is what the arrow keys walk and Enter picks: the matching
  // options, then the "add what I typed" row, as one list so both are
  // reachable the same way.
  const rows = [
    ...matches.map((r) => ({ key: r, label: r, repos: [r] })),
    ...(custom
      ? [
          {
            key: "__custom__",
            label: `Add ${custom.join(", ")}`,
            repos: custom,
          },
        ]
      : []),
  ];

  useEffect(() => {
    setHighlight(0);
  }, [query]);

  const setQueryText = (text) => {
    queryRef.current = text;
    setQuery(text);
  };

  const add = (repos) => {
    const added = repos.filter((r) => !already.has(r));
    if (added.length > 0) onPick(added);
  };

  const pick = (row) => {
    add(row.repos);
    setQueryText("");
    setOpen(false);
  };

  // Leaving the box with a repo typed into it but not picked adds it,
  // rather than throwing the text away at submit and filing a task with
  // nothing in this field at all. This field was a plain "owner/name,
  // comma-separated" input before it was a picker, so typing the whole
  // name and moving on is exactly what somebody who has used it before
  // will do -- and clicking Create is itself what blurs the box, so
  // silently dropping the text would lose it at the worst moment.
  // Anything that does not parse as a repo is left in the box, visible,
  // instead of being either added or discarded.
  const commitTyped = () => {
    const typedRepos = parseRepoList(queryRef.current);
    if (typedRepos.length === 0) return;
    add(typedRepos);
    setQueryText("");
  };

  const onKeyDown = (e) => {
    if (e.key === "Escape") {
      // An open list is what this Escape closes, and only that: left to
      // bubble, it also reaches the drawer this box sits in, which
      // closes the whole New task form. With the list already closed it
      // is the drawer's to have.
      if (open) e.stopPropagation();
      setOpen(false);
      return;
    }
    if (e.key === "Enter") {
      // Swallowed whether or not it picks anything: this box sits inside
      // a form, and Enter on it must never be the one that submits the
      // whole thing while somebody is still half way through naming a
      // repo.
      e.preventDefault();
      if (highlight < rows.length) pick(rows[highlight]);
      return;
    }
    if (rows.length === 0) return;
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setHighlight((h) => Math.min(h + 1, rows.length - 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setHighlight((h) => Math.max(h - 1, 0));
    }
  };

  return (
    <ClickAwayListener onClickAway={() => setOpen(false)}>
      <div ref={anchorRef} style={{ position: "relative" }}>
        <TextField
          label={label}
          helperText={helperText}
          placeholder={placeholder}
          value={query}
          autoComplete="off"
          inputProps={
            ariaLabel
              ? { ...identifierField, "aria-label": ariaLabel }
              : identifierField
          }
          fullWidth
          size="small"
          onChange={(e) => {
            setQueryText(e.target.value);
            setOpen(true);
          }}
          onFocus={() => setOpen(true)}
          // Clicking the box reopens the list as well as focusing it:
          // picking a repo leaves the focus where it was, so after one
          // pick a focus handler alone would never fire again and the
          // box would look inert on the way to the second.
          onClick={() => setOpen(true)}
          onBlur={commitTyped}
          onKeyDown={onKeyDown}
        />
        <Popper
          open={open}
          anchorEl={anchorRef.current}
          placement="bottom-start"
          style={{ width: anchorRef.current?.offsetWidth, zIndex: 1300 }}
        >
          {/* Mousedown on a result must not blur the box: the blur
              handler above would commit the half-typed query as a repo
              of its own, on the way to picking the result that was
              actually clicked. */}
          <Paper
            variant="outlined"
            sx={{ mt: 0.5, maxHeight: 220, overflowY: "auto" }}
            onMouseDown={(e) => e.preventDefault()}
          >
            <MenuList dense>
              {rows.length === 0 && (
                <MenuItem disabled>
                  <Typography variant="body2" color="text.secondary">
                    {q === ""
                      ? "No repos to suggest -- type owner/name"
                      : "No matching repos -- type owner/name"}
                  </Typography>
                </MenuItem>
              )}
              {/* Every row here names a repo -- a known one, or the one
                  being typed -- so each carries the repos figure
                  (ItemGlyph.jsx), the same ring the nav rail and the
                  Repos page use. It is what tells this popper apart from
                  the task picker it is built out of, which drops an
                  identically shaped list of ids under an identically
                  shaped box. */}
              {rows.map((row, i) => (
                <MenuItem
                  key={row.key}
                  selected={i === highlight}
                  onClick={() => pick(row)}
                  onMouseEnter={() => setHighlight(i)}
                >
                  <GlyphLabel kind="repos">
                    <ListItemText
                      primary={row.label}
                      primaryTypographyProps={{ noWrap: true }}
                    />
                  </GlyphLabel>
                </MenuItem>
              ))}
            </MenuList>
          </Paper>
        </Popper>
      </div>
    </ClickAwayListener>
  );
}
