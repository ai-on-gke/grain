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

// clock.ts formats instants for the shell's banner and alerts. It is kept
// apart from alerts.ts so the banner, which is in the initial bundle, does
// not pull the alert rules (loaded with the deferred bell) in with it.

// ---- Time, on the clock Settings → Preferences picked ("local" or an
// IANA zone, the same localStorage key settings-instance writes).

export function preferredZone(): string {
  try {
    const z = globalThis.localStorage?.getItem("grain.timeZone") ?? "";
    return z && z !== "local" ? z : "";
  } catch {
    return "";
  }
}

function fmt(
  value: string | number | Date,
  zone: string,
  options: Intl.DateTimeFormatOptions,
): string {
  const at = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(at.getTime())) return "";
  const opts: Intl.DateTimeFormatOptions = { ...options };
  if (zone) opts.timeZone = zone;
  try {
    return at.toLocaleString(undefined, opts);
  } catch {
    delete opts.timeZone;
    return at.toLocaleString(undefined, opts);
  }
}

export function formatClock(value: string | number | Date, zone = ""): string {
  return fmt(value, zone, { hour: "numeric", minute: "2-digit" });
}

// formatMoment is a clock time for today, a date and time otherwise: a
// pause that lifts on Saturday must not read "until 9:13 AM".
export function formatMoment(
  value: string | number | Date,
  zone = "",
  now = new Date(),
): string {
  const day = (v: string | number | Date) =>
    fmt(v, zone, { year: "numeric", month: "numeric", day: "numeric" });
  const on = day(value);
  if (!on) return "";
  return on === day(now)
    ? formatClock(value, zone)
    : fmt(value, zone, {
        year: "numeric",
        month: "short",
        day: "numeric",
        hour: "numeric",
        minute: "2-digit",
      });
}
