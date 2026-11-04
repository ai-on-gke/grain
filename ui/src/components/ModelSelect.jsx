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

import SearchSelect from "./SearchSelect.jsx";

// ModelSelect is the one control every Model picker is drawn with: a
// SearchSelect over options ({value, label}), searchable by typing any
// part of a model's name (searchFilter) -- a team whose connections
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
  return (
    <SearchSelect
      data-setting-id={settingId}
      // Always a hidden input with the model itself, as a select had.
      name={name ?? ""}
      label={label}
      value={value ?? ""}
      options={options}
      onChange={onChange}
      helperText={helperText}
      disabled={disabled}
      margin={margin}
      size={size}
      fullWidth={fullWidth}
      placeholder="Type to search models"
      InputLabelProps={{ shrink: true }}
    />
  );
}
