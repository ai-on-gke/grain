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

import { readStorage } from "../services/theme.service";

// TIME_ZONE_STORAGE_KEY holds this browser's Display timezone (Settings →
// General), the same key the classic UI keeps it under; absent means the
// browser's own zone.
export const TIME_ZONE_STORAGE_KEY = "grain.timeZone";

export function displayTimeZone(): string | undefined {
  const tz = readStorage(TIME_ZONE_STORAGE_KEY);
  return tz && tz !== "local" ? tz : undefined;
}

// formatTimestamp prints a server timestamp (RFC 3339) to the second in
// the Display timezone, naming the zone; text that is not a time comes
// back as it was.
export function formatTimestamp(
  raw: string,
  timeZone: string | undefined = displayTimeZone(),
): string {
  const d = new Date(raw);
  if (!raw || Number.isNaN(d.getTime())) return raw;
  const opts: Intl.DateTimeFormatOptions = {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hourCycle: "h23",
    timeZoneName: "short",
  };
  try {
    return new Intl.DateTimeFormat("en-US", { ...opts, timeZone }).format(d);
  } catch {
    // An unknown stored zone: fall back to the browser's.
    return new Intl.DateTimeFormat("en-US", opts).format(d);
  }
}
