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

// The shaping behind ReleaseNotesPanel.jsx, kept out of the component
// for memory.js's reason: these are answers about what a week's notes
// mean, they are worth testing without a DOM, and a component that both
// decided them and drew them would be one nobody could ask a question
// of.

// weekLabel is an ISO week as a reader sees it in the picker: the week
// itself, which is the key and the file name, and the year said plainly
// beside it rather than left as a prefix to decode.
//
// Deliberately not a date range. Which Monday "2026-W38" starts on is a
// calendar question the server has already answered for the week being
// shown (ClusterStats.since/until), and a second answer computed in the
// browser -- against the browser's zone rather than the deployment's --
// would be the one that eventually disagrees.
export function weekLabel(period) {
  if (typeof period !== "string") return "";
  const [year, week] = period.split("-W");
  if (!year || !week) return period;
  return `Week ${Number(week)}, ${year}`;
}

const MONTH_NAMES = [
  "January",
  "February",
  "March",
  "April",
  "May",
  "June",
  "July",
  "August",
  "September",
  "October",
  "November",
  "December",
];

// isWeekPeriod reports whether period is an ISO week key ("2026-W38")
// that weekly release-note prose can be fetched under.
export function isWeekPeriod(period) {
  return typeof period === "string" && /^\d{4}-W\d{2}$/.test(period);
}

// periodLabel formats a rollup period key -- a calendar day
// ("2026-11-04"), an ISO week ("2026-W38") or a calendar month
// ("2026-11") -- from the string itself without consulting the browser's
// clock or time zone.
export function periodLabel(period) {
  if (typeof period !== "string") return "";
  if (isWeekPeriod(period)) return weekLabel(period);
  const monthMatch = /^(\d{4})-(\d{2})$/.exec(period);
  if (monthMatch) {
    const m = Number(monthMatch[2]);
    if (m >= 1 && m <= 12) return `${MONTH_NAMES[m - 1]} ${monthMatch[1]}`;
  }
  const dayMatch = /^(\d{4})-(\d{2})-(\d{2})$/.exec(period);
  if (dayMatch) {
    const m = Number(dayMatch[2]);
    const d = Number(dayMatch[3]);
    if (m >= 1 && m <= 12 && d >= 1 && d <= 31) {
      return `${MONTH_NAMES[m - 1]} ${d}, ${dayMatch[1]}`;
    }
  }
  return weekLabel(period);
}

// ROLLUP_WINDOWS are the quick daily, weekly and monthly rollup presets
// offered above the cluster numbers.
export const ROLLUP_WINDOWS = [
  { value: "today", label: "Today" },
  { value: "yesterday", label: "Yesterday" },
  { value: "this-week", label: "This week" },
  { value: "", label: "Last week" },
  { value: "this-month", label: "This month" },
  { value: "last-month", label: "Last month" },
];

// releaseNoteAuthor is who published a week, in the words this pane says
// it in.
//
// Three answers, not two, for memoryAuthor's reason: a person correcting
// a week and the weekly job take the same route, so the stored kind is
// the only thing that tells them apart -- and a row written before that
// column meant anything says nothing rather than being dressed up as
// either. What a reader is deciding here is whether the account they are
// reading is the machine's own or somebody's revision of it, which is
// exactly the distinction that would be lost by guessing.
export function releaseNoteAuthor(note) {
  const kind = note?.updatedByKind;
  if (kind === "human") return "Written by hand";
  if (kind === "agent" || kind === "automation") {
    return "Written by grain's release-notes job";
  }
  return "Published";
}
