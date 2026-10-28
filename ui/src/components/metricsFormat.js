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

// metricsFormat.js is how the measurement report's numbers are written
// for a reader: a duration rather than a float, a size in the units a
// truncation cap is set in, a percentile dimmed when there are too few
// samples behind it for its name to be true.
//
// Its own file because there are two pages drawing this report now --
// the Metrics pane and a report board (MetricsSections.jsx,
// grain/task-83) -- and these are pure functions over the JSON, which is
// exactly the part of a page worth testing directly and the part neither
// page should own.
import { STATE_ORDER } from "../state.js";

// formatSeconds renders one of the report's own second counts as a
// duration, the same reasoning as `grain metrics`' own `seconds`: a p50
// shown as "21m 30s" is read at a glance where "1290.4" is arithmetic
// homework. Coarser the longer it gets -- nobody reading a three-hour
// lead time needs the seconds on the end of it.
export function formatSeconds(value) {
  if (value === null || value === undefined || Number.isNaN(value)) return "—";
  const s = Math.round(value);
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
  if (s < 86400)
    return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
  return `${Math.floor(s / 86400)}d ${Math.floor((s % 86400) / 3600)}h`;
}

// isThinPercentile says whether a stage has too few samples for one of
// its percentiles to mean what its name says. A percentile only tells
// you something the maximum did not once at least one sample can fall
// in the tail it names, which takes 100/(100-p) of them: 2 for a p50,
// 10 for a p90, 100 for a p99. Below that the number *is* the maximum
// wearing a percentile's name -- exactly what pkg/ui's own MetricsStage
// comment warns about, and why `n` is a column here rather than a
// detail.
//
// The percentile is passed as a whole number (50, 90, 99) rather than a
// fraction so the threshold comes out exact: 1/(1-0.9) is 10.000000...2
// in floating point, which would quietly call a ten-sample p90 thin.
//
// A thin percentile is still the largest thing that happened, so the
// table dims and footnotes it rather than blanking it out.
export function isThinPercentile(n, percentile) {
  return n > 0 && n < 100 / (100 - percentile);
}

// sortedOutcomes puts the attempt outcomes in a stable order -- by count
// descending so the ending that dominates a window reads first, then by
// name so two renders of the same report never disagree. The same order
// `grain metrics`' own outcomeSummary prints them in.
export function sortedOutcomes(outcomes) {
  return Object.entries(outcomes || {}).sort(
    (a, b) => b[1] - a[1] || a[0].localeCompare(b[0]),
  );
}

// formatBytes renders a result size the way somebody sizing a truncation
// cap reads one. Binary units, because the cap they would set is written
// in them (mcp.maxToolResultBytes is 16 << 10), and no decimals below a
// megabyte: the difference between 15 KB and 16 KB matters and the
// difference between 63.4 and 63.5 does not.
export function formatBytes(value) {
  if (value === null || value === undefined || Number.isNaN(value)) return "—";
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${Math.round(value / 1024)} KB`;
  return `${(value / (1024 * 1024)).toFixed(1)} MB`;
}

// percent renders a rate (0..1) as whole percent. Rates here are read
// against each other -- edit_file's against run_command's -- rather than
// to a decimal place.
export function percent(rate) {
  if (!rate) return "0%";
  return `${Math.round(rate * 100)}%`;
}

// backlogRows orders the backlog by STATE_ORDER -- model.StateOf's own
// precedence, the order the sidebar already lists states in -- with any
// state this UI does not know about appended alphabetically rather than
// dropped, so a state added server-side still shows up here.
export function backlogRows(byState) {
  const entries = Object.entries(byState || {}).filter(([, n]) => n > 0);
  const known = STATE_ORDER.filter((s) =>
    entries.some(([state]) => state === s),
  );
  const unknown = entries
    .map(([state]) => state)
    .filter((s) => !STATE_ORDER.includes(s))
    .sort();
  return [...known, ...unknown].map((state) => ({
    state,
    count: byState[state],
  }));
}
