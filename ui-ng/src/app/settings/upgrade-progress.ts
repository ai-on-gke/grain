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

// Reading GET /api/upgrade's step list: the classic pane's
// ui/src/upgradeProgress.js, ported as it is so both UIs count an
// upgrade's steps and time them the same way.
//
// pkg/upgrade decides an upgrade's whole plan before it starts and
// reports it as `steps`, each carrying a state ("pending", "running",
// "done", "failed", "skipped"), whatever it last had to say, and when it
// started and finished. A status with no steps at all (one written by an
// older grain) is tolerated everywhere here.

export interface UpgradeStep {
  name: string;
  state: string;
  detail?: string;
  startedAt?: string;
  finishedAt?: string;
}

export interface UpgradeProgress {
  total: number;
  settled: number;
  percent: number;
  current: UpgradeStep | null;
  position: number;
}

// upgradeProgress summarises a step list: how many steps the upgrade
// plans, how many are behind it (a skipped step counts, so a bar on a
// deployment with no health check still fills), and the step it is on --
// the running one, else the one that failed or was cancelled -- with its
// 1-based position for "step 2/9".
export function upgradeProgress(steps: unknown): UpgradeProgress {
  const list: UpgradeStep[] = Array.isArray(steps) ? steps : [];
  const total = list.length;
  const settled = list.filter((step) =>
    ["done", "skipped", "failed", "cancelled"].includes(step.state),
  ).length;
  let index = list.findIndex((step) => step.state === "running");
  if (index < 0) index = list.findIndex((step) => step.state === "failed");
  if (index < 0) index = list.findIndex((step) => step.state === "cancelled");
  return {
    total,
    settled,
    percent: total === 0 ? 0 : Math.round((settled / total) * 100),
    current: index < 0 ? null : list[index],
    position: index < 0 ? 0 : index + 1,
  };
}

// stepElapsedMs is how long a step has been running, or took: from its
// startedAt to its finishedAt, or to now while it is still going. null
// for a step that never started or whose timestamps do not parse.
export function stepElapsedMs(
  step: UpgradeStep | null | undefined,
  now = Date.now(),
): number | null {
  if (!step || !step.startedAt) return null;
  const started = new Date(step.startedAt).getTime();
  if (Number.isNaN(started)) return null;
  const end = step.finishedAt ? new Date(step.finishedAt).getTime() : now;
  if (Number.isNaN(end)) return null;
  return Math.max(0, end - started);
}

// formatElapsed writes a duration in whole units: seconds until a
// minute, minutes and seconds until an hour, hours and minutes past it.
export function formatElapsed(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || Number.isNaN(ms)) return "";
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

// upgradeStepLine is the one line the shell's banner and the pane both
// say about where an upgrade is: "step 2/9 pull image (12s)", or how
// many steps are finished when none is current.
export function upgradeStepLine(steps: unknown, now = Date.now()): string {
  const p = upgradeProgress(steps);
  if (p.total === 0) return "";
  if (!p.current) return `${p.settled} of ${p.total} steps finished`;
  const elapsed = formatElapsed(stepElapsedMs(p.current, now));
  return (
    `step ${p.position}/${p.total} ${p.current.name}` +
    (elapsed ? ` (${elapsed})` : "")
  );
}

// failedStepError is what a failed upgrade has to say for itself: the
// failed step's own detail (the registry's "unauthorized: authentication
// failed"), else the status's detail.
export function failedStepError(status: {
  steps?: unknown;
  detail?: string;
}): string {
  const steps: UpgradeStep[] = Array.isArray(status.steps) ? status.steps : [];
  const failed = steps.find((s) => s.state === "failed");
  return (failed?.detail || status.detail || "").trim();
}
