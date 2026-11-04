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
import { Autocomplete, Chip, TextField } from "@mui/material";
import { searchFilter } from "../fuzzy.js";
import { identifierField } from "../identifierField.js";

// SearchSelect is the one searchable dropdown a picker over a list that
// can grow is drawn with: a select whose box can be typed into, matching
// what is typed anywhere in an option's text -- every space-separated
// word, in any order, falling back to the letters in order when nothing
// matches outright (searchFilter, fuzzy.js). A native <select> only
// jumps to an option by the start of its text, so "grain" never found
// "ai-on-gke/grain" in one (grain/task-2879).
//
// options are {value, label}: label is what the box shows once the
// option is chosen, and what typing is matched against unless match
// says otherwise; row, when given, is what the open list shows instead
// (a longer description, say). An option with pinned set is offered
// after the matches whatever has been typed -- an escape hatch such as
// RepoField's "Other…" must stay reachable from a search that found
// nothing. typedOption(text) may add one more row for the text in the
// box itself (RepoField's "Use owner/name") when it is a value the list
// does not hold.
//
// Like a <select> it works both ways: controlled with value, or
// uncontrolled from defaultValue, and with name it carries the chosen
// value in a hidden input, so a form read with FormData sees it exactly
// where a select would have put it. The visible box holds the label,
// never the value.
//
// Keys are MUI's own and match the hand-rolled pickers' rules
// (BranchField.jsx, TaskPicker.jsx): arrows walk the list, Enter takes
// the highlighted row while the list is open -- and only then, so on a
// closed list it still submits the form around it -- and Escape closes
// an open list without reaching the drawer the field sits in; on a
// closed one it bubbles as before.
//
// renderInput draws the box; the default is a small MUI TextField with
// label and helperText. A caller with its own look (RepoField's plain
// input) passes one, and must spread params.inputProps onto its input
// and attach params.InputProps.ref to the element the list hangs under.
export default function SearchSelect({
  name,
  options,
  value,
  defaultValue,
  onChange,
  typedOption,
  label,
  helperText,
  placeholder,
  disabled,
  required,
  multiple = false,
  clearable = false,
  size = "small",
  margin,
  fullWidth = true,
  inputProps: callerInputProps,
  renderInput,
  className,
  noOptionsText = "No matches",
  ...rest
}) {
  const [inner, setInner] = useState(
    defaultValue !== undefined ? defaultValue : multiple ? [] : "",
  );
  const rawCurrent = value !== undefined ? value : inner;
  const current = multiple
    ? Array.isArray(rawCurrent)
      ? rawCurrent
      : []
    : rawCurrent;

  const byValue = new Map(options.map((o) => [o.value, o]));
  const all = [...options];
  // A value no option carries (one typed in, or a list still loading) is
  // still the value: it is shown as itself rather than blanked, and MUI
  // is given an option for it so it has something to draw.
  for (const v of multiple ? current : [current]) {
    if (!byValue.has(v) && v !== "" && v != null) {
      const own = { value: v, label: String(v) };
      all.push(own);
      byValue.set(v, own);
    }
  }
  const optionOf = (v) => byValue.get(v) ?? null;
  const matchText = (o) => o.match ?? o.label;

  const choose = (next) => {
    if (value === undefined) setInner(next);
    onChange?.(next);
  };

  return (
    <Autocomplete
      className={className}
      multiple={multiple}
      disableCloseOnSelect={multiple}
      options={all}
      value={
        multiple
          ? current.map((v) => byValue.get(v)).filter(Boolean)
          : optionOf(current)
      }
      disabled={disabled}
      disableClearable={!clearable}
      selectOnFocus={!multiple}
      autoHighlight
      fullWidth={fullWidth}
      size={size}
      noOptionsText={noOptionsText}
      isOptionEqualToValue={(o, v) => o.value === v.value}
      getOptionLabel={(o) => o?.label ?? ""}
      renderTags={
        multiple
          ? (picked, getTagProps) =>
              picked.map((o, index) => {
                const { key, ...tagProps } = getTagProps({ index });
                return (
                  <Chip key={key} size="small" label={o.label} {...tagProps} />
                );
              })
          : undefined
      }
      // Ranges ride on the filtered rows so renderOption can highlight
      // them without matching a second time.
      filterOptions={(opts, state) => {
        const query = state.inputValue;
        const pinned = opts.filter((o) => o.pinned);
        const typed = typedOption?.(query.trim());
        const searchable = opts.filter((o) => !o.pinned);
        // On opening, a single-value box still holds the chosen option's
        // label, and that is not a search: everything is offered, as a
        // select would.
        const searching = multiple
          ? query.trim() !== ""
          : query.trim() !== "" && query !== (optionOf(current)?.label ?? "");
        const hits = searching
          ? searchFilter(searchable, query, matchText).map(
              ({ item, ranges }) =>
                matchText(item) === item.label ? { ...item, ranges } : item,
            )
          : searchable;
        const extra =
          typed && !hits.some((o) => o.value === typed.value) ? [typed] : [];
        return [...hits, ...extra, ...pinned];
      }}
      onChange={(_event, next) => {
        if (multiple) {
          choose(next.map((o) => o.value));
          return;
        }
        if (next == null) {
          choose("");
          return;
        }
        choose(next.value);
      }}
      renderOption={(props, option, state) => {
        const { key, ...other } = props;
        const text = (
          <HighlightedText text={option.label} ranges={option.ranges} />
        );
        return (
          <li
            key={key}
            {...other}
            data-value={option.value}
            data-label={option.label}
          >
            {option.renderRow
              ? option.renderRow(text, state)
              : (option.row ?? text)}
          </li>
        );
      }}
      renderInput={(base) => {
        // Leaving the box with typedOption's text in it takes that value,
        // rather than snapping back to the old choice: typing a whole
        // name and moving on is what somebody who knows the name does.
        const params = {
          ...base,
          inputProps: {
            ...base.inputProps,
            onBlur: (e) => {
              const typed = typedOption?.(e.target.value.trim());
              if (typed) {
                if (multiple) {
                  if (!current.includes(typed.value)) {
                    choose([...current, typed.value]);
                  }
                } else if (typed.value !== current) {
                  choose(typed.value);
                }
              }
              base.inputProps.onBlur?.(e);
            },
          },
        };
        const hidden = name ? (
          multiple ? (
            current.map((v, i) => (
              <input key={i} type="hidden" name={name} value={v ?? ""} />
            ))
          ) : (
            <input type="hidden" name={name} value={current ?? ""} />
          )
        ) : null;
        if (renderInput) {
          return (
            <>
              {renderInput(params)}
              {hidden}
            </>
          );
        }
        return (
          <TextField
            {...params}
            // A search box over names, not prose: a phone's keyboard must
            // not capitalize or "correct" what is typed into it.
            inputProps={{
              ...params.inputProps,
              ...identifierField,
              ...callerInputProps,
            }}
            label={label}
            helperText={helperText}
            placeholder={
              multiple && current.length > 0 ? undefined : placeholder
            }
            margin={margin}
            required={required}
            InputProps={{
              ...params.InputProps,
              endAdornment: (
                <>
                  {hidden}
                  {params.InputProps.endAdornment}
                </>
              ),
            }}
          />
        );
      }}
      {...rest}
    />
  );
}

// HighlightedText is text with the parts a search hit (ranges, from
// searchFilter) in bold -- what tells somebody who typed "grain" why
// ai-on-gke/grain is in the list. RepoPicker draws its rows with it too.
export function HighlightedText({ text, ranges = [] }) {
  if (ranges.length === 0) return text;
  const parts = [];
  let at = 0;
  ranges.forEach(([s, e], i) => {
    if (s > at) parts.push(text.slice(at, s));
    parts.push(
      <strong key={i} className="search-select-hit">
        {text.slice(s, e)}
      </strong>,
    );
    at = e;
  });
  if (at < text.length) parts.push(text.slice(at));
  // Named as the whole text, so a screen reader -- which may read each
  // bold run as a word of its own -- still hears one name.
  return <span aria-label={text}>{parts}</span>;
}
