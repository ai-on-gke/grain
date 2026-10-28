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

import { useEffect, useRef } from "react";
import { Alert, Box, Chip } from "@mui/material";

// A template, suite or schedule a repository declares in its own `.grain`
// directory (docs/declarations.md, grain/task-60) arrives with
// `declaredIn: {repo, path}` (ui.Declaration). The server refuses to edit
// or delete one (Client.refuseDeclaredEdit), so a row that looks like any
// other and an editor whose Save and Delete only answer 400 is a form
// that fails without having said why it would. These two say it up
// front: the chip on the row, and the notice at the head of the editor
// naming the file to change instead.

// declaredLocation is "acme/widgets, .grain/templates/nightly.yml" -- the
// whole of where a declared row came from, in one string a tooltip or an
// accessible name can carry.
export function declaredLocation(declaredIn) {
  return `${declaredIn.repo}, ${declaredIn.path}`;
}

// DeclaredChip marks a declared row in a list. The repository is in the
// label, since that is who owns the row; the file is in the tooltip,
// since it is only wanted by whoever is about to go and change it -- and
// the editor that row opens says it in full.
export function DeclaredChip({ declaredIn }) {
  if (!declaredIn) return null;
  return (
    <Chip
      size="small"
      variant="outlined"
      color="info"
      className="declared-chip"
      label={`Declared by ${declaredIn.repo}`}
      title={`Declared in ${declaredLocation(declaredIn)}`}
    />
  );
}

// DeclaredNotice heads the editor of a declared row and says why nothing
// in it can be saved or deleted. `also` is a sentence about what still
// works here -- a declared schedule can still be run now.
export function DeclaredNotice({ declaredIn, noun, also }) {
  if (!declaredIn) return null;
  return (
    <Alert severity="info" className="declared-notice" sx={{ mt: 1, mb: 1 }}>
      This {noun} is declared by <strong>{declaredIn.repo}</strong> in{" "}
      <code>{declaredIn.path}</code>, so it cannot be edited or deleted here.
      Change that file in the repository and grain picks it up within a couple
      of minutes.{also ? ` ${also}` : ""}
    </Alert>
  );
}

// DeclaredFields wraps an editor's fields, and none of its action row,
// in a fieldset that is disabled when the row is declared. The notice
// above says nothing here can be changed, and Save is disabled -- but
// every text box still took typing and a "+ New template" or "+ Add
// input" inside a declared editor still worked, the first of them
// filing a real template the declared suite could never be saved with.
// A disabled fieldset turns off every native control under it (text
// boxes, checkboxes, the buttons), while leaving their values on screen
// and in the accessibility tree to be read. MUI's selects are a div,
// which a fieldset does not reach, so they are kept from opening by
// pointer events -- and, since a pointer is not the only way in, by
// swallowing every key but Tab and Escape that lands on one. Only the
// pointer used to be stopped, and a select reached by Tab still opened
// on ArrowDown and took a choice: a declared schedule's Repeat read
// "Daily" over a cadence nothing would ever save (2026-10-11
// walkthrough). The selects stay focusable, so a keyboard can still
// reach and read each value, and say aria-disabled so a screen reader
// does not offer one as a choice.
// swallowSelectKeys stops a key on its way to a select inside a declared
// editor: captured on the fieldset, ahead of MUI's own handler on the
// select, which opens on ArrowUp/ArrowDown/Enter/Space. Tab still moves
// focus on, and Escape still reaches the pane, which closes on it.
function swallowSelectKeys(e) {
  if (e.key === "Tab" || e.key === "Escape") return;
  if (!e.target.closest?.('[role="combobox"]')) return;
  e.preventDefault();
  e.stopPropagation();
}

export function DeclaredFields({ declaredIn, children }) {
  const ref = useRef(null);
  // After every render rather than once: a select MUI draws later (a
  // section that opens) is a new element without the attribute.
  useEffect(() => {
    if (!declaredIn || !ref.current) return;
    for (const el of ref.current.querySelectorAll('[role="combobox"]')) {
      el.setAttribute("aria-disabled", "true");
    }
  });
  return (
    <Box
      component="fieldset"
      ref={ref}
      disabled={!!declaredIn}
      onKeyDownCapture={declaredIn ? swallowSelectKeys : undefined}
      className={declaredIn ? "declared-fields" : undefined}
      sx={{
        border: 0,
        p: 0,
        m: 0,
        minWidth: 0,
        ...(declaredIn && {
          "& .MuiSelect-select, & .MuiAutocomplete-root": {
            pointerEvents: "none",
          },
        }),
      }}
    >
      {children}
    </Box>
  );
}

// A declared template's `push:` (grain/task-2536) arrives as
// `{repo, path, commit, refs: [{ref, force, delete}]}` (ui.DeclaredPush):
// the refs beyond their own branch that the runs of tasks filed from it
// may push in repo, as the repository's default branch declares them.
// declaredPushRef is one entry as it reads -- "refs/tags/latest (force)".
export function declaredPushRef(r) {
  const flags = [r.force && "force", r.delete && "delete"].filter(Boolean);
  return flags.length ? `${r.ref} (${flags.join(", ")})` : r.ref;
}

// DeclaredPushRefs lists one push set, naming the file it is declared in
// so whoever wants it changed knows where.
export function DeclaredPushRefs({ push }) {
  if (!push || !(push.refs || []).length) return null;
  return (
    <span
      className="declared-push"
      title={`Declared in ${declaredLocation(push)}`}
    >
      {push.refs.map(declaredPushRef).join(", ")}{" "}
      <span className="declared-push-source">
        (<code>{push.path}</code>)
      </span>
    </span>
  );
}
