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

import { Link } from "@mui/material";

// TaskLink is a link from one task's pane to another task: a plain click
// opens it in place, the way every other task link does, while the href
// names the task itself, so a middle click, a modified click, "open in
// new tab" and "copy link" all reach it too -- an href of "#" sent each
// of those to the pane already open instead.
export default function TaskLink({ id, onOpenTask, children }) {
  return (
    <Link
      href={`/tasks/${encodeURIComponent(id)}`}
      onClick={(e) => {
        if (!onOpenTask) return;
        if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey)
          return;
        e.preventDefault();
        onOpenTask(id);
      }}
    >
      {children ?? id}
    </Link>
  );
}
