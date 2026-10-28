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

import { Autocomplete, TextField } from "@mui/material";
import { fuzzyFilter } from "../fuzzy.js";

// ModelSelect is the one control every Model picker is drawn with: a
// dropdown over options ({value, label}), searchable by typing any part
// of a model's name in order (fuzzyFilter) -- a team whose connections
// advertise every model their providers list has dozens of them, too
// many to scroll for. It chooses only among options: a model name typed
// that no option carries is not a choice, because dispatch would refuse
// it (docs/connections-and-models.md). value "" is a real option (the
// team's default, or none), never an empty field, so the label stays
// shrunk over it. settingId is the data-setting-id the Settings filter
// finds a picker of a Settings section by (settingsIndex.js).
export default function ModelSelect({
  label,
  name,
  settingId,
  value,
  options,
  onChange,
  helperText,
  disabled,
  margin,
  size = "small",
  fullWidth = true,
}) {
  const current = value ?? "";
  const labels = new Map(options.map((o) => [o.value, o.label]));
  const values = options.map((o) => o.value);
  if (!labels.has(current)) {
    values.push(current);
    labels.set(current, current);
  }
  const labelOf = (v) => labels.get(v) ?? String(v ?? "");
  return (
    <Autocomplete
      data-setting-id={settingId}
      options={values}
      value={current}
      disabled={disabled}
      disableClearable
      autoHighlight
      fullWidth={fullWidth}
      size={size}
      getOptionLabel={labelOf}
      filterOptions={(opts, state) =>
        fuzzyFilter(opts, state.inputValue, labelOf)
      }
      onChange={(_event, next) => onChange(next ?? "")}
      renderOption={(props, option) => {
        const { key, ...rest } = props;
        return (
          <li key={key} {...rest} data-value={option}>
            {labelOf(option)}
          </li>
        );
      }}
      renderInput={(params) => (
        <TextField
          {...params}
          label={label}
          helperText={helperText}
          margin={margin}
          placeholder="Type to search models"
          InputLabelProps={{ ...params.InputLabelProps, shrink: true }}
          InputProps={{
            ...params.InputProps,
            // The value itself, as a select's hidden input carries it:
            // the visible input holds the option's label, not the model.
            endAdornment: (
              <>
                <input type="hidden" name={name} value={current} />
                {params.InputProps.endAdornment}
              </>
            ),
          }}
        />
      )}
    />
  );
}
