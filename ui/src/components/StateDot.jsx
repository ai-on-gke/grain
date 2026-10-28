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

import GrainMark from "./GrainMark.jsx";

// The circle every per-task state badge opens with. Every state but one
// is the plain solid dot .badge::before already draws in CSS; a task
// that is actually running right now swaps it for the grain mark's
// glyph cycle instead -- the same "agents are working" signal the
// sidebar shows (Sidebar.jsx), now down at the row that is doing the
// work (bwsalmon/agents#586).
//
// 20px, well above the 11px .badge::before dot it replaces: the v2 mark
// is a filled region rather than a line figure, and under about 20px
// there are not enough pixels across the glyph for the four to be
// tellable apart. The task row is ~40px tall and its text line is
// ~20px, so this costs the row no height.
//
// live=false is for a badge recording a past "running" moment rather
// than the task's live status (Timeline's superseded entries) -- that
// one keeps the plain, non-spinning solid dot .badge-static draws, the
// same as every other historical state.
//
// repairing is a task whose run is the merge queue repairing its own
// pull request branch rather than writing the change in the first place
// (ui.Task.Repairing, model.Observation.MergeQueueRepairAt). It is
// running either way -- the same mark, moving the same way -- so what
// tells the two apart is the colour it moves in: blue rather than the
// accent, .grain-mark-repair in style.css. A repair is the queue's own
// work on a change that is otherwise finished, and reading a row as
// "back to square one" when it is really being unstuck is exactly the
// misreading the second colour exists to prevent.
const MARK_SIZE = 20;

export function isLiveRunning(state, live = true) {
  return state === "running" && live;
}

export default function StateDot({
  state,
  live = true,
  title,
  repairing = false,
  interactive = false,
  agentRunning,
}) {
  const active =
    isLiveRunning(state, live) &&
    (agentRunning !== undefined ? agentRunning : true);
  if (!active) return null;
  const className = repairing
    ? "grain-mark-repair"
    : interactive
      ? "grain-mark-interactive"
      : undefined;
  return (
    <GrainMark size={MARK_SIZE} animated title={title} className={className} />
  );
}
