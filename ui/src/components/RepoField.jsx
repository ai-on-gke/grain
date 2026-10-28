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
import ItemGlyph from "./ItemGlyph.jsx";
import { identifierField } from "../identifierField.js";

// CUSTOM is the select's escape hatch, not a real option a repo could
// itself be named -- model.ParseRepo requires a "/", which this value
// deliberately has none of, so it can never collide with one.
const CUSTOM = "custom";

// NO_REPO is the "this task has no repo at all" choice
// (bwsalmon/agents#614), an option in the same list as the repos rather
// than a checkbox beside it (grain/task-85) -- picking a target and
// declaring there is none are one decision, and a checkbox that blanked
// the field next to it made them look like two. Like CUSTOM it carries
// no "/", so model.ParseRepo could never accept it as a repo name and
// no real option can collide with it.
export const NO_REPO = "none";

// GOOGLE3 is the "this task's change is a google3 CL" choice
// (google/docs/design/google3-cls.md#G3-1): no repo either, but a CitC
// workspace instead, offered only where the binary registers the kind
// (config.changeTargets). Not a valid repo name, so it cannot collide
// with one.
export const GOOGLE3 = "google3";

// RepoField is the repo dropdown bwsalmon/agents#447 asked for in place
// of a bare "type an owner/name and hope you spelled it right" input:
// options is normally knownRepos(config, tasks) (state.js), the same
// repos CreateTask would actually accept plus whatever existing tasks
// already target. Typing stays reachable through "Other…", since
// options is never the full universe of valid repos -- an unrestricted
// deployment's very first task, or a restricted one CreateTask hasn't
// widened targetRepos for yet, both need a way to name a repo nothing
// has used before.
//
// Falls back to a plain text input outright when there is nothing to
// choose from yet, rather than rendering a one-item "Other…" dropdown
// that would only cost a click for no benefit -- unless allowNoRepo puts
// "No repo" in the list, which is a real choice to offer even on a
// deployment whose first task is being filed and has no known repos yet.
//
// onChange fires on every keystroke of the text path, which is what a
// form collecting an answer to submit later wants and exactly what a
// field that saves each edit as it is made does not: PATCHing per
// keystroke would send "a", "ac", "acm"… on the way to one repo name.
// onCommit is that second reading -- the value somebody has settled on:
// a pick from the list (a choice is complete the moment it is made), or
// the text in the box once it is left or Enter is pressed. Both are
// optional and independent, so a caller can take either reading alone.
export default function RepoField({
  name,
  options,
  defaultValue = "",
  required = false,
  placeholder = "owner/name",
  allowNoRepo = false,
  allowGoogle3 = false,
  onChange,
  onCommit,
}) {
  const knownDefault =
    defaultValue === "" ||
    options.includes(defaultValue) ||
    (allowNoRepo && defaultValue === NO_REPO) ||
    (allowGoogle3 && defaultValue === GOOGLE3);
  // What the dropdown shows when it is (re)drawn, and what "Choose from
  // list" reports back: the default when the list holds it, otherwise
  // the list's own empty row (or, for a required field, its first
  // option). A free-text default the list does not hold -- the one an
  // "Other…" box was seeded with -- is never it: the select would show
  // something else while the caller still held the typed repo.
  const selectDefault =
    options.includes(defaultValue) ||
    (allowNoRepo && defaultValue === NO_REPO) ||
    (allowGoogle3 && defaultValue === GOOGLE3) ||
    (!required && defaultValue === "")
      ? defaultValue
      : required
        ? (options[0] ?? (allowNoRepo ? NO_REPO : ""))
        : "";
  // Which path this field takes is derived from the props on every
  // render, not frozen at the first one, until somebody says otherwise:
  // `chosen` is null while nobody has, and true/false once "Other…" or
  // "Choose from list" has been pressed.
  //
  // Frozen was a race. options is knownRepos(config, tasks), and config
  // arrives from a fetch, so anything that renders this field before
  // that fetch lands -- a deep link straight into a task's pane, which
  // is the one navigation with nothing in front of it -- saw an empty
  // list, took the text path for good, and then grew a "Choose from
  // list" link the moment the repos turned up. A dropdown that only
  // appears if you were slow enough to be shown it.
  const [chosen, setChosen] = useState(null);
  const custom =
    chosen ?? ((options.length === 0 && !allowNoRepo) || !knownDefault);
  const setCustom = setChosen;

  if (custom) {
    return (
      <div className="repo-field">
        <div className="repo-field-control">
          <RepoGlyph />
          <input
            name={name}
            placeholder={placeholder}
            defaultValue={defaultValue}
            required={required}
            autoComplete="off"
            {...identifierField}
            onChange={(e) => onChange?.(e.target.value)}
            onBlur={(e) => onCommit?.(e.target.value)}
            // Enter is swallowed only for a caller that asked for
            // onCommit: in a form (NewTaskOverlay, TemplateOverlay) it is
            // the key that submits, and taking that away from a field
            // whose caller reads the value at submit time would be a
            // change nobody asked for.
            onKeyDown={(e) => {
              if (!onCommit || e.key !== "Enter") return;
              e.preventDefault();
              onCommit(e.target.value);
            }}
          />
        </div>
        {(options.length > 0 || allowNoRepo) && (
          <button
            type="button"
            className="link-button"
            onClick={() => {
              // Going back to the list abandons whatever was typed, so
              // the caller is told what the list now shows: without
              // this it kept the typed repo -- warning about it, saving
              // it in a draft and filing the task against it -- under a
              // dropdown that read "—".
              setCustom(false);
              onChange?.(selectDefault);
            }}
          >
            Choose from list
          </button>
        )}
      </div>
    );
  }

  return (
    <div className="repo-field">
      <div className="repo-field-control">
        <RepoGlyph />
        <select
          name={name}
          defaultValue={selectDefault}
          required={required}
          onChange={(e) => {
            if (e.target.value === CUSTOM) {
              // "Other…" reports the value the text input it swaps in
              // is about to show (its own defaultValue), not the option
              // that was just left behind: a caller tracking this
              // field's value -- to enable a submit button on it, say --
              // would otherwise still be holding a repo that is no
              // longer displayed anywhere, and in particular would
              // still think NO_REPO was chosen.
              setCustom(true);
              onChange?.(defaultValue);
              return;
            }
            onChange?.(e.target.value);
            onCommit?.(e.target.value);
          }}
        >
          {!required && <option value="">—</option>}
          {options.map((r) => (
            <option key={r} value={r}>
              {r}
            </option>
          ))}
          {allowNoRepo && (
            <option value={NO_REPO}>
              No repo (standalone task -- nothing to check out)
            </option>
          )}
          {allowGoogle3 && (
            <option value={GOOGLE3}>
              google3 (CitC) -- a CL in a CitC workspace, no git checkout
            </option>
          )}
          <option value={CUSTOM}>Other…</option>
        </select>
      </div>
    </div>
  );
}

// The repos figure in front of the field, not on each option: an
// <option> can hold no markup, and this stays a native <select> for the
// semantics its own tests and a phone's own picker rely on. In front of
// the whole control the ring says the same thing once -- what this field
// names -- for a picker whose caption is often the single word "Repo",
// and it is the same figure the Repos page and the read-only repos
// picker beside this one carry (ItemGlyph.jsx).
function RepoGlyph() {
  return (
    <span className="repo-field-icon">
      <ItemGlyph kind="repos" />
    </span>
  );
}
