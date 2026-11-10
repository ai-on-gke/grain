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

// ADMISSION_HELP is the task pane's own wording for each admission value
// (detail/Actions.jsx, design/design/unified-grants.design.md §4.1).
const ADMISSION_HELP = {
  standard: "Queued with ordinary work, under the worker limit.",
  bypass:
    "Admitted past the worker and merger limits and a pipeline pause; still charged to the sandbox budget.",
  durable:
    "Bypass queue, plus no wall-clock deadline: suspended when idle and counted under the durable limit.",
};

// AdmissionField is the Admission setting (model.Template.Admission,
// model.Schedule.Admission) on a form that files tasks: the setting
// that replaced the priority-dispatch and durable legacy capabilities
// is a column on templates and schedules as well as tasks, and a form
// that did not show it left a template's or schedule's admission
// unsettable here and invisible once a repository declared it
// (2026-11-07 walkthrough). value is "standard", "bypass" or
// "durable" -- "" on the wire reads as standard. `noun` finishes the
// helper's "applies to every task this ... files".
export default function AdmissionField({ value, onChange, noun }) {
  const v = value || "standard";
  return (
    <TextField
      select
      name="admission"
      label="Admission"
      value={v}
      onChange={(e) => onChange(e.target.value)}
      helperText={`${ADMISSION_HELP[v] || ""} Applies to every task this ${noun} files.`}
      fullWidth
      margin="normal"
      size="small"
    >
      <MenuItem value="standard">Standard</MenuItem>
      <MenuItem value="bypass">Bypass queue</MenuItem>
      <MenuItem value="durable">Durable</MenuItem>
    </TextField>
  );
}
