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

// KeepRunningChip marks a task that asks to keep running
// (model.Task.KeepRunning): one restarted every time its run exits, so it
// never completes on its own. A proposal asking for it is held for a person
// to approve (model.PersonOnlyProposalSettings), and the Inbox row with its
// approve button is where that person decides -- so the row says so, not
// only the hold reason in the body on the task's own page.
import { Chip } from "@mui/material";

export default function KeepRunningChip() {
  return (
    <Chip
      size="small"
      variant="outlined"
      color="warning"
      className="chip-keep-running"
      data-testid="task-keep-running-badge"
      title="keep_running: restarted every time its run exits, so it never completes on its own"
      label="keeps running"
    />
  );
}
