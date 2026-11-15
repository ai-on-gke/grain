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

// When a failed poll is worth telling anybody about.
//
// One banner policy for every poll in /next/: a read that fails once and
// then succeeds is silent, and only failure that lasts says anything --
// once, while the last good answer stays on screen. A single 503 through
// a sign-in proxy, or a read that timed out while the daemon was busy, is
// not news; three in a row, or thirty seconds of them, is. The classic UI
// applies the same rule to the sign-in verifier's own 503s
// (ui/src/api.js, VERIFIER_SUSTAINED_MS); this is that rule for every
// failure a poll can have (grain task 5812).
//
// While a poll is failing it also backs off -- doubling from its own
// interval up to POLL_BACKOFF_MAX_MS, or waiting as long as a Retry-After
// header asks -- rather than asking a struggling server again at full rate.

// SUSTAINED_FAILURES is how many consecutive failed reads make a
// sustained failure on their own.
export const SUSTAINED_FAILURES = 3;
// SUSTAINED_MS is how long a run of failed reads has to have lasted to be
// sustained however few reads it took; the same 30s as the classic UI's
// VERIFIER_SUSTAINED_MS.
export const SUSTAINED_MS = 30_000;
// POLL_BACKOFF_MAX_MS caps a failing poll's backoff.
export const POLL_BACKOFF_MAX_MS = 60_000;

// retryAfterMs is the delay a failed response's Retry-After header asks
// for, in milliseconds, or null when it names none.
export function retryAfterMs(err: unknown, now = Date.now()): number | null {
  const headers = (err as { headers?: { get?(name: string): string | null } })
    ?.headers;
  const raw =
    typeof headers?.get === "function" ? headers.get("Retry-After") : null;
  if (!raw) return null;
  const secs = Number(raw.trim());
  if (Number.isFinite(secs) && secs >= 0) return secs * 1000;
  const at = Date.parse(raw);
  return Number.isNaN(at) ? null : Math.max(0, at - now);
}

// ReadTrouble counts one poll's run of consecutive failures.
export class ReadTrouble {
  private failures = 0;
  private since: number | null = null;
  private retryAfter: number | null = null;

  // fail records a failed read, and reports whether the run of them is
  // now sustained.
  fail(err: unknown, now = Date.now()): boolean {
    this.failures++;
    this.since ??= now;
    this.retryAfter = retryAfterMs(err, now);
    return this.sustained(now);
  }

  ok(): void {
    this.failures = 0;
    this.since = null;
    this.retryAfter = null;
  }

  get failing(): boolean {
    return this.failures > 0;
  }

  sustained(now = Date.now()): boolean {
    return (
      this.failures >= SUSTAINED_FAILURES ||
      (this.since !== null && now - this.since >= SUSTAINED_MS)
    );
  }

  // nextDelay is how long to wait before the next poll whose ordinary
  // interval is baseMs: baseMs while reads succeed, and while they fail,
  // the longer of Retry-After and baseMs doubled once per failure past
  // the first, capped at POLL_BACKOFF_MAX_MS.
  nextDelay(baseMs: number): number {
    if (this.failures === 0) return baseMs;
    const backoff = Math.min(
      baseMs * 2 ** (this.failures - 1),
      POLL_BACKOFF_MAX_MS,
    );
    return Math.max(backoff, this.retryAfter ?? 0);
  }
}
