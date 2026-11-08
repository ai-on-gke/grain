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

import { MenuItem, TextField } from "@mui/material";

// HOSTING_HELP explains each hosting value, under the task pane's
// Hosting select (detail/Actions.jsx) and HostingField below
// (design/design/unified-grants.design.md §4.1).
export const HOSTING_HELP = {
  unattended:
    "Hands the agent its prompt and runs the turn to completion on its own.",
  console:
    "Runs the agent CLI on an interactive terminal in the UI until somebody closes it.",
};

// HostingField is the Hosting setting (model.Template.Hosting,
// model.Schedule.Hosting) on a form that files tasks, AdmissionField's
// sibling: the setting that replaced the console legacy capability is a
// column on templates and schedules as well as tasks, so a form without
// it left a template's or schedule's hosting unsettable here. value is
// "unattended" or "console" -- "" on the wire reads as unattended.
// `noun` finishes the helper's "applies to every task this ... files".
export default function HostingField({ value, onChange, noun }) {
  const v = value || "unattended";
  return (
    <TextField
      select
      name="hosting"
      label="Hosting"
      value={v}
      onChange={(e) => onChange(e.target.value)}
      helperText={`${HOSTING_HELP[v] || ""} Applies to every task this ${noun} files.`}
      fullWidth
      margin="normal"
      size="small"
    >
      <MenuItem value="unattended">Unattended</MenuItem>
      <MenuItem value="console">Interactive console</MenuItem>
    </TextField>
  );
}
