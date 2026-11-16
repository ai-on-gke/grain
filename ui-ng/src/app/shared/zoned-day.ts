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

// Calendar days in a named IANA zone, for a date picker whose day means
// the deployment's day rather than the browser's: "defer until the 31st"
// wakes at the start of the 31st in the zone the wake time is printed in
// (classic's dayStart in ui/src/components/detail/Actions.jsx).

const DAY_RE = /^(\d{4})-(\d{2})-(\d{2})$/;

// zoneOffsetMs is how far tz's wall clock is ahead of UTC at ms.
function zoneOffsetMs(ms: number, tz: string): number {
  const parts = new Intl.DateTimeFormat("en-US", {
    timeZone: tz,
    hourCycle: "h23",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  }).formatToParts(new Date(ms));
  const n = (type: string) =>
    Number(parts.find((p) => p.type === type)?.value ?? 0);
  const wall = Date.UTC(
    n("year"),
    n("month") - 1,
    n("day"),
    n("hour") % 24,
    n("minute"),
    n("second"),
  );
  return wall - Math.floor(ms / 1000) * 1000;
}

// zonedDayStart is the instant day ("YYYY-MM-DD") starts in tz, or null
// for a value that is not a day.
export function zonedDayStart(day: string, tz: string): number | null {
  const m = DAY_RE.exec(day);
  if (!m) return null;
  const wall = Date.UTC(Number(m[1]), Number(m[2]) - 1, Number(m[3]));
  if (!Number.isFinite(wall)) return null;
  // Two passes settle a midnight that sits across an offset change.
  let at = wall - zoneOffsetMs(wall, tz);
  at = wall - zoneOffsetMs(at, tz);
  return at;
}

// zonedDay is the "YYYY-MM-DD" day ms falls on in tz.
export function zonedDay(ms: number, tz: string): string {
  return new Intl.DateTimeFormat("en-CA", {
    timeZone: tz,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).format(new Date(ms));
}

// earliestWakeDay is tomorrow in tz: today's midnight has always passed,
// and the server refuses a wake time in the past.
export function earliestWakeDay(tz: string, nowMs = Date.now()): string {
  const [y, mo, d] = zonedDay(nowMs, tz).split("-").map(Number);
  return new Date(Date.UTC(y, mo - 1, d + 1)).toISOString().slice(0, 10);
}

// formatWakeTime prints a wake instant in tz, with the zone named.
export function formatWakeTime(iso: string, tz: string): string {
  const ms = Date.parse(iso);
  if (!Number.isFinite(ms)) return iso;
  try {
    return new Intl.DateTimeFormat(undefined, {
      timeZone: tz,
      weekday: "short",
      month: "short",
      day: "numeric",
      year: "numeric",
      hour: "numeric",
      minute: "2-digit",
      timeZoneName: "short",
    }).format(new Date(ms));
  } catch {
    return new Date(ms).toISOString();
  }
}
