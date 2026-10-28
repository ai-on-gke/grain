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

import { Chip, Tooltip } from "@mui/material";

// PresubmitBadge is a CL's presubmit as grain last observed it (the task's
// `presubmit`, polled by the orchestrator through the change provider,
// changelists.md §3): passed, failed, running, not run or unknown, beside
// its cl/N chip. A failed one names its failing checks, in the label when
// there is one and in the tooltip always.
const STATES = {
  passed: { label: "presubmit passed", color: "success", variant: "filled" },
  failed: { label: "presubmit failed", color: "error", variant: "filled" },
  running: { label: "presubmit running", color: "info", variant: "outlined" },
  not_run: {
    label: "presubmit not run",
    color: "default",
    variant: "outlined",
  },
  unknown: {
    label: "presubmit unknown",
    color: "default",
    variant: "outlined",
  },
};

export function presubmitLabel(presubmit) {
  const state = STATES[presubmit?.state] ? presubmit.state : "unknown";
  const failing = presubmit?.failing || [];
  let label = STATES[state].label;
  if (state === "failed" && failing.length === 1) {
    label += `: ${failing[0]}`;
  } else if (state === "failed" && failing.length > 1) {
    label += `: ${failing.length} checks`;
  }
  return label;
}

export default function PresubmitBadge({ presubmit }) {
  if (!presubmit) return null;
  const state = STATES[presubmit.state] ? presubmit.state : "unknown";
  const failing = presubmit.failing || [];
  const title =
    failing.length > 0
      ? `Failing: ${failing.join(", ")}`
      : `${STATES[state].label}${presubmit.snapshot ? ` on snapshot ${presubmit.snapshot}` : ""}`;
  return (
    <Tooltip title={title}>
      <Chip
        className="presubmit-badge"
        data-testid="presubmit-badge"
        data-state={state}
        size="small"
        color={STATES[state].color}
        variant={STATES[state].variant}
        label={presubmitLabel(presubmit)}
        aria-label={`${presubmitLabel(presubmit)}${failing.length ? `. Failing: ${failing.join(", ")}` : ""}`}
        sx={{ maxWidth: 260, verticalAlign: "middle" }}
      />
    </Tooltip>
  );
}
