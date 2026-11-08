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

import { Fragment, useMemo, useRef, useState } from "react";
import {
  Autocomplete,
  Box,
  Chip,
  ListItemText,
  TextField,
} from "@mui/material";
import CheckBoxIcon from "@mui/icons-material/CheckBox";
import CheckBoxOutlineBlankIcon from "@mui/icons-material/CheckBoxOutlineBlank";
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
// With multiple it holds a list of values instead: value and
// defaultValue are arrays, onChange is given the whole new array, and
// name carries one hidden input per value, as a <select multiple> does.
// The list stays open while rows are ticked, each row has a checkbox,
// and the chosen values sit in the box as chips -- renderChip(option,
// props) draws one where a plain Chip will not do (GrantChip, a warning
// outline), and must spread props, which carry the delete button that
// takes the value back off. Typing searches as it does for one value;
// what was typed is cleared as a row is ticked, so the next search
// starts afresh.
//
// Rows of either kind may say more: secondary is a second line under
// the label, title the row's tooltip (a description too long for the
// row), and disabled leaves the row in the list but not pickable
// (getOptionDisabled, given, decides instead).
//
// renderInput draws the box; the default is a small MUI TextField with
// label, helperText (styled by FormHelperTextProps) and margin, plus any
// inputProps (a data-testid, say) and InputLabelProps (shrink, or shrinkLabel for short, over a ""
// option with a label of its own). A caller with its own look
// (RepoField's plain input) passes one, and must spread
// params.inputProps onto its input and attach params.InputProps.ref to
// the element the list hangs under.
//
// SHORT_LIST is the most options a fixed or declared list may have and
// still be drawn as a plain select rather than a SearchSelect: up to
// about seven (yes/no, a mode, a weekday) a select is quicker, above all
// on a phone, and there is nothing to search for (grain/task-2578).
export const SHORT_LIST = 7;

export default function SearchSelect({
  name,
  options,
  value,
  defaultValue,
  onChange,
  typedOption,
  label,
  shrinkLabel = false,
  inputAriaLabel,
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
  renderRow,
  className,
  noOptionsText = "No matches",
  InputLabelProps,
  FormHelperTextProps,
  error,
  renderChip,
  getOptionDisabled = (o) => Boolean(o.disabled),
  onOpen,
  onClose,
  onKeyDown,
  ...rest
}) {
  // Whether the list is open, for Enter (onKeyDown below). MUI's own
  // Enter picks the highlighted option and stops there; with no option
  // highlighted -- a search matching nothing, the list saying "No
  // matches" -- it does nothing, and the keypress went on to submit the
  // form around the box. That is the one way Enter in an open list still
  // submitted (the 2026-11-04 UI walkthrough, §10 item 3: Enter on a
  // Schedule picker reading "No matches" filed the schedule).
  const listOpen = useRef(false);
  const [inner, setInner] = useState(
    defaultValue !== undefined ? defaultValue : multiple ? [] : "",
  );
  // Bumped whenever a controlled single value reports a new choice: a
  // caller that declines it (Settings' Section picker, when "Discard
  // unsaved changes?" is cancelled) leaves value as it was, and MUI only
  // puts the box's text back to the chosen label when value changes. A
  // fresh copy of the kept option is that change, so the declined row's
  // label is not left in the box (2026-11-08 settings walkthrough).
  const [syncSeq, setSyncSeq] = useState(0);
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
  const optionOf = (v) => {
    const found = byValue.get(v) ?? null;
    // A disabled "" row is an unpicked placeholder ("Choose a template"),
    // not a chosen value: leaving the box empty lets the user type at once.
    if (found?.disabled && v === "") return null;
    return found;
  };
  const matchText = (o) => o.match ?? o.label;

  const choose = (next) => {
    if (value === undefined) setInner(next);
    else if (!multiple && next !== current) setSyncSeq((n) => n + 1);
    onChange?.(next);
  };

  const foundSingle = multiple ? null : optionOf(current);
  const singleValue = useMemo(
    () => (foundSingle && syncSeq > 0 ? { ...foundSingle } : foundSingle),
    [foundSingle?.value, foundSingle?.label, syncSeq],
  );

  return (
    <Autocomplete
      className={className}
      multiple={multiple}
      disableCloseOnSelect={multiple}
      options={all}
      value={
        multiple
          ? current.map((v) => byValue.get(v)).filter(Boolean)
          : singleValue
      }
      disabled={disabled}
      getOptionDisabled={getOptionDisabled}
      disableClearable={!clearable}
      onOpen={(event) => {
        listOpen.current = true;
        onOpen?.(event);
      }}
      onClose={(event, reason) => {
        listOpen.current = false;
        onClose?.(event, reason);
      }}
      onKeyDown={(event) => {
        onKeyDown?.(event);
        // Enter on an open list never reaches the form: MUI picks the
        // highlighted option when there is one, and otherwise this is
        // the whole of what Enter does.
        if (
          event.key === "Enter" &&
          (rest.open ?? listOpen.current) &&
          !event.nativeEvent?.isComposing
        ) {
          event.preventDefault();
        }
      }}
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
                const props = { ...tagProps, "data-value": o.value };
                return renderChip ? (
                  <Fragment key={key}>{renderChip(o, props)}</Fragment>
                ) : (
                  <Chip key={key} size="small" label={o.label} {...props} />
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
                matchText(item) === item.label
                  ? { ...item, ranges }
                  : {
                      ...item,
                      ranges: searchFilter([item.label], query)[0]?.ranges,
                    },
            )
          : searchable;
        if (rest.groupBy && searching) {
          const groupOrder = [];
          for (const o of searchable) {
            const g = rest.groupBy(o);
            if (!groupOrder.includes(g)) groupOrder.push(g);
          }
          hits.sort(
            (a, b) =>
              groupOrder.indexOf(rest.groupBy(a)) -
              groupOrder.indexOf(rest.groupBy(b)),
          );
        }
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
        const plain = option.secondary ? (
          <ListItemText
            primary={text}
            secondary={option.secondary}
            sx={{ my: 0 }}
          />
        ) : multiple ? (
          <ListItemText primary={text} sx={{ my: 0 }} />
        ) : (
          text
        );
        return (
          <li
            key={key}
            {...other}
            title={option.title}
            data-value={option.value}
            data-label={option.label}
          >
            {/* The row's tick, drawn rather than an <input>: the option
                already says it is picked (aria-selected), and a real
                checkbox inside role="option" was an unlabelled, nested
                control on every open multiple list (axe's label and
                nested-interactive, the 2026-11-04 UI walkthrough). */}
            {multiple && (
              <Box
                component="span"
                aria-hidden="true"
                className="search-select-check"
                data-checked={state.selected ? "true" : "false"}
                sx={{
                  display: "inline-flex",
                  flex: "none",
                  p: 0.5,
                  mr: 0.5,
                  color: state.selected ? "primary.main" : "text.secondary",
                }}
              >
                {state.selected ? (
                  <CheckBoxIcon fontSize="small" />
                ) : (
                  <CheckBoxOutlineBlankIcon fontSize="small" />
                )}
              </Box>
            )}
            {renderRow
              ? renderRow(option)
              : option.renderRow
                ? option.renderRow(text, state)
                : (option.row ?? plain)}
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
            ...(inputAriaLabel ? { "aria-label": inputAriaLabel } : {}),
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
        // name "" still draws one, carrying the value for anything that
        // reads the box's value off the page; a form skips an unnamed
        // input, so it adds nothing to a FormData.
        const hidden =
          name != null ? (
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
            InputLabelProps={{
              ...params.InputLabelProps,
              ...(shrinkLabel ? { shrink: true } : {}),
              ...InputLabelProps,
            }}
            FormHelperTextProps={FormHelperTextProps}
            label={label}
            helperText={helperText}
            placeholder={
              multiple && current.length > 0 ? undefined : placeholder
            }
            margin={margin}
            required={required}
            error={error}
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
