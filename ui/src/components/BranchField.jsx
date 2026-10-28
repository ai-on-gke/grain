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
} from "@mui/material";
import api from "../api.js";
import { branchRefs, clearBranchRefCache } from "../branchRefs.js";
import { GlyphLabel } from "./ItemGlyph.jsx";
import { identifierField } from "../identifierField.js";
import { repoApiRoot } from "../repoApi.js";

// How long a repo's branch list is reused before it is asked for again.
// Branches are added from grain's own repo page and from the release
// schedules reconciler, so a list cached for the life of the tab would
// go stale while somebody is still using it -- and a fetch per opened
// dropdown would ask again for every keystroke-free glance at a box.
const CACHE_MS = 60_000;

// The branch names grain and GitHub know for each repo, keyed by the repo as
// it was asked for. Module-level rather than per-component because the
// same repo's branches are wanted by several boxes on one form (a
// task's own base branch and the base for each repo it may also write
// to), and by the same box every time it is opened.
const cache = new Map();

// clearBranchCache drops it, for tests: the cache outlives a component,
// so without this one test's fetched list would answer the next test's
// dropdown and nothing would ask the API fake anything. GitHub's own
// refs are cached in branchRefs.js, shared with everything else that
// asks about them, so they go with it.
export function clearBranchCache() {
  cache.clear();
  clearBranchRefCache();
}

// branchNames combines grain's recorded branches with GitHub's actual refs.
// Both are cached here, while the GitHub half is cached again on the server so
// separate browser tabs do not each spend the deployment's shared rate limit.
//
// Anything that goes wrong is answered with no suggestions rather than
// with an error: a picker that cannot suggest is still a text box that
// works, and a repo somebody is halfway through typing ("acme/wid") is
// a 404 on the way to a name that will resolve.
async function branchNames(repo) {
  const root = repoApiRoot(repo);
  // Keep the sources independent: a missing deployment credential or a
  // transient GitHub failure must not hide grain's own known branches.
  const results = await Promise.allSettled([
    api(`${root}/branches`),
    branchRefs(repo),
  ]);
  const names = [];
  let answered = false;
  for (const result of results) {
    if (result.status !== "fulfilled" || !Array.isArray(result.value)) continue;
    answered = true;
    for (const branch of result.value) {
      const name = typeof branch === "string" ? branch : branch?.name;
      if (typeof name === "string") names.push(name);
    }
  }
  if (!answered) throw results[0].reason || results[1].reason;
  return names;
}

// cachedBranchNames is branchNames through this module's cache, so the
// suggestion list and the missing-branch warning share one request.
function cachedBranchNames(target) {
  const hit = cache.get(target);
  if (hit && Date.now() - hit.at < CACHE_MS) return hit.names;
  const names = branchNames(target).catch(() => {
    // Dropped from the cache so the next open asks again: the failure
    // may have been a repo that was half typed at the time.
    if (cache.get(target)?.names === names) cache.delete(target);
    return [];
  });
  cache.set(target, { at: Date.now(), names });
  return names;
}

// BranchField is the branch box everywhere a branch is picked rather
// than invented (grain/task-370): type to search, arrow keys and Enter
// or a click to pick, and the branches grain knows about on the repo
// beside it offered the moment the box is focused. It is the branch
// half of what RepoField and RepoPicker already do for repos -- before
// this, every branch on these forms was a bare text box and a hope that
// the name had been spelled the way GitHub spells it.
//
// Free text remains the point, not the fallback. A credential or GitHub read
// can be unavailable, and a newly pushed ref can be newer than the cache, so
// nothing is refused here: suggestions save typing when they can and stay
// out of the way when they cannot, and a form that asks (warnMissing) is
// told about a branch GitHub does not have without being stopped.
//
// Controlled or not, like the fields it replaces. A caller that holds
// the value passes value/onChange; one that reads the form at submit
// (SuiteRunOverlay's FormData) passes defaultValue and lets the input
// keep it, which is why `name` reaches the <input> either way.
//
// onChange fires per keystroke and per pick, for a caller collecting an
// answer to submit later. onCommit is the settled value -- a pick, or
// the text in the box once it is left -- for a caller that saves each
// edit as it is made and must not PATCH once per character. Both are
// optional, exactly as they are on RepoField.
export default function BranchField({
  name,
  label = "Base branch",
  ariaLabel,
  helperText,
  placeholder = "main",
  // The repo whose branches are offered, as owner/name. Empty -- no
  // repo picked yet, or a form that is not about one -- simply means
  // there is nothing to ask about, and the box behaves as it always did.
  repo = "",
  // Extra branch names to offer ahead of grain's own, from whatever the
  // caller already knows: the bases other tasks in this repo are
  // targeting (state.js's branchesInUse) is the one that matters.
  options = [],
  value,
  defaultValue = "",
  required = false,
  disabled = false,
  size = "small",
  margin,
  fullWidth = true,
  sx,
  onChange,
  onCommit,
  // What a missing branch costs on this form, said after "No branch X on
  // owner/name" in place of helperText when GitHub's own listing was
  // read and does not have the typed branch. Unset -- the default --
  // never warns. It warns rather than refuses: a schedule or template
  // may name a release branch that is cut later (grain/task-1620), and
  // the server already refuses the one write where a missing branch can
  // only fail, a task filed with nothing to wait on.
  warnMissing,
}) {
  const controlled = value !== undefined;
  const [own, setOwn] = useState(defaultValue);
  const text = controlled ? value : own;

  const [open, setOpen] = useState(false);
  const [highlight, setHighlight] = useState(0);
  const [known, setKnown] = useState([]);
  const anchorRef = useRef(null);
  // textRef shadows the value for the blur handler, which has to report
  // what is in the box now rather than what was in it when the render
  // that installed the handler ran -- a click on a suggestion picks and
  // blurs in the same tick.
  const textRef = useRef(text);
  textRef.current = text;

  const target = repo.trim();
  const askable = target.includes("/") && !target.endsWith("/");

  // The list is fetched when the box is opened rather than when it is
  // mounted: most forms carrying one of these are filled in and
  // submitted without the branch box ever being touched, and a request
  // per repo per form that nobody reads is a request nobody asked for.
  useEffect(() => {
    if (!open || !askable) return;
    let live = true;
    cachedBranchNames(target)
      .then((names) => live && setKnown(names))
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [open, askable, target]);

  // warnMissing: once the typed branch has settled for a moment, ask
  // whether GitHub has it. Only an answer GitHub actually gave can raise
  // the warning -- a listing that could not be read is "cannot tell",
  // not "missing" (branchRefs.js) -- and a branch grain has been asked to
  // create counts as there, since the branches reconciler is about to
  // cut it.
  const settledText = (text || "").trim();
  const [missing, setMissing] = useState("");
  useEffect(() => {
    setMissing("");
    if (!warnMissing || !askable || settledText === "") return;
    let live = true;
    const timer = setTimeout(async () => {
      try {
        const refs = await branchRefs(target);
        if (refs.includes(settledText)) return;
        const all = await cachedBranchNames(target);
        if (live && !all.includes(settledText)) setMissing(settledText);
      } catch {
        // Cannot tell, so say nothing.
      }
    }, 400);
    return () => {
      live = false;
      clearTimeout(timer);
    };
  }, [warnMissing, askable, target, settledText]);

  // A repo change invalidates what is on offer immediately, rather than
  // leaving the last repo's branches under a box that is now about a
  // different one.
  useEffect(() => {
    setKnown([]);
  }, [target]);

  const q = (text || "").trim().toLowerCase();
  const seen = new Set();
  const matches = [];
  for (const branch of [...options, ...known]) {
    if (typeof branch !== "string" || branch === "") continue;
    if (seen.has(branch)) continue;
    seen.add(branch);
    if (!branch.toLowerCase().includes(q)) continue;
    matches.push(branch);
    if (matches.length === 8) break;
  }

  // A list holding nothing but the branch already in the box is not
  // worth showing: there is nothing left to choose, and the one thing a
  // dropdown that stays open over a finished answer reliably does is
  // cover the button under it -- which on these forms is Apply, or
  // Create, or Run.
  const settled = matches.length === 1 && matches[0] === (text || "").trim();
  const showing = open && matches.length > 0 && !settled;

  useEffect(() => {
    setHighlight(0);
  }, [text]);

  const set = (next) => {
    if (!controlled) setOwn(next);
    textRef.current = next;
    onChange?.(next);
  };

  const pick = (branch) => {
    set(branch);
    setOpen(false);
    // A pick is a finished answer, the way choosing a repo from
    // RepoField's list is: there is nothing further to settle.
    onCommit?.(branch);
  };

  const onKeyDown = (e) => {
    if (e.key === "Escape") {
      // Closes the list rather than the drawer around this field, as
      // RepoPicker's does; with no list showing it bubbles as before.
      if (showing) e.stopPropagation();
      setOpen(false);
      return;
    }
    if (e.key === "ArrowDown" && matches.length > 0 && !settled) {
      e.preventDefault();
      setOpen(true);
      setHighlight((h) => Math.min(h + 1, matches.length - 1));
      return;
    }
    if (e.key === "ArrowUp" && matches.length > 0 && !settled) {
      e.preventDefault();
      setHighlight((h) => Math.max(h - 1, 0));
      return;
    }
    if (e.key !== "Enter") return;
    // Enter takes the highlighted suggestion while the list is showing
    // one, and never submits the form from under somebody who was
    // choosing a branch.
    if (showing) {
      e.preventDefault();
      pick(matches[highlight]);
      return;
    }
    // Otherwise it is Enter on a typed branch, and only a caller that
    // asked for onCommit has it swallowed -- in a form that submits on
    // Enter (NewTaskOverlay, SuiteRunOverlay), taking that away from a
    // field whose caller reads the value at submit time would be a
    // change nobody asked for.
    if (!onCommit) return;
    e.preventDefault();
    setOpen(false);
    onCommit(textRef.current);
  };

  return (
    <ClickAwayListener onClickAway={() => setOpen(false)}>
      <div ref={anchorRef} style={{ position: "relative" }}>
        <TextField
          name={name}
          label={label}
          helperText={
            missing
              ? `No branch "${missing}" on ${target}: ${warnMissing}`
              : helperText
          }
          FormHelperTextProps={
            missing ? { sx: { color: "warning.main" } } : undefined
          }
          placeholder={placeholder}
          required={required}
          disabled={disabled}
          InputLabelProps={required ? { required: false } : undefined}
          value={text}
          autoComplete="off"
          inputProps={{
            ...identifierField,
            ...(ariaLabel ? { "aria-label": ariaLabel } : {}),
            // On the input rather than on the field: TextField hands a
            // bare onClick to the whole FormControl, helper text
            // included, so a link under the box ("use it", on a task's
            // own base) would open the suggestions on its way to doing
            // its own job.
            onClick: () => setOpen(true),
          }}
          fullWidth={fullWidth}
          size={size}
          margin={margin}
          sx={sx}
          onChange={(e) => {
            set(e.target.value);
            setOpen(true);
          }}
          // Focus opens the list, and so does a click (in inputProps
          // above): picking a branch leaves the focus in the box, so a
          // focus handler alone would never fire again and the box
          // would look inert on the way to a second look at the list.
          onFocus={() => setOpen(true)}
          onBlur={() => {
            setOpen(false);
            onCommit?.(textRef.current);
          }}
          onKeyDown={onKeyDown}
        />
        <Popper
          open={showing}
          anchorEl={anchorRef.current}
          placement="bottom-start"
          style={{ width: anchorRef.current?.offsetWidth, zIndex: 1300 }}
        >
          {/* Mousedown on a suggestion must not blur the box first: the
              blur handler commits, and a caller that saves on commit
              would save the half-typed text on the way to the branch
              that was actually clicked. */}
          <Paper
            variant="outlined"
            sx={{ mt: 0.5, maxHeight: 220, overflowY: "auto" }}
            onMouseDown={(e) => e.preventDefault()}
          >
            {/* Every row here names a branch, so each carries the
                branches figure (ItemGlyph.jsx, grain/task-372) -- the
                same one a repo's Branches list and the task views' Base
                menu use, and the same thing the repo picker this box is
                shaped like does with the ring. It is also what tells
                this popper apart from that one and from the task
                picker: three identically shaped lists under three
                identically shaped boxes, on forms that carry two of
                them at once. */}
            <MenuList dense>
              {matches.map((branch, i) => (
                <MenuItem
                  key={branch}
                  selected={i === highlight}
                  onClick={() => pick(branch)}
                  onMouseEnter={() => setHighlight(i)}
                >
                  <GlyphLabel kind="branches">
                    <ListItemText
                      primary={branch}
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
