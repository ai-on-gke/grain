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

import {
  HttpErrorResponse,
  HttpResponse,
  type HttpInterceptorFn,
} from "@angular/common/http";
import { Injectable, NgZone, OnDestroy, inject, signal } from "@angular/core";
import { tap } from "rxjs";

// When a failed poll is worth telling anybody about.
//
// One banner policy for every poll in /next/: a read that fails once and
// then succeeds is silent, and only failure that lasts says anything --
// once, while the last good answer stays on screen. A single 503 through
// a sign-in proxy, or a read that timed out while the daemon was busy, is
// not news; three in a row, or thirty seconds of them, is. The classic UI
// applies VERIFIER_SUSTAINED_MS (30s) to the sign-in verifier's own 503s
// (ui/src/api.js); here verifier_unavailable follows that exact 30s clock
// for one global sign-in banner, and every other poll failure follows the
// three-reads-or-30s rule (grain tasks 5812, 5880).
//
// While a poll (or the server as a whole) is failing with 502/503/504 or a
// network error, background polls back off -- doubling up to
// POLL_BACKOFF_MAX_MS, or waiting as long as Retry-After asks -- rather
// than asking a struggling proxy across dozens of routes at full rate.

export const SUSTAINED_FAILURES = 3;
export const SUSTAINED_MS = 30_000;
export const VERIFIER_SUSTAINED_MS = SUSTAINED_MS;
export const POLL_BACKOFF_MAX_MS = 60_000;

export const VERIFIER_ACTION_REASON =
  "Sign-in verification could not check your identity just now, so this was not done — try again in a few seconds.";

// isVerifierUnavailable is the daemon's own 503 for "the sign-in verifier
// in front of me could not be reached" (pkg/ui/principalrefusal.go): a
// transient that retries clear, never a refusal of who the caller is.
export function isVerifierUnavailable(err: unknown): boolean {
  if (!err || typeof err !== "object") return false;
  const e = err as { status?: unknown; error?: unknown; code?: unknown };
  if (e.status !== 503) return false;
  if (e.code === "verifier_unavailable") return true;
  let body = e.error;
  if (typeof body === "string") {
    const trimmed = body.trim();
    if (!trimmed.startsWith("{")) return false;
    try {
      body = JSON.parse(trimmed);
    } catch {
      return false;
    }
  }
  return (
    Boolean(body) &&
    typeof body === "object" &&
    (body as { code?: unknown }).code === "verifier_unavailable"
  );
}

// isTransientServerError is whether a failed HTTP exchange means the
// gateway, sign-in proxy, or network in front of the daemon is failing
// (status 0, 502, 503, 504), so every background poll should hold off.
export function isTransientServerError(err: unknown): boolean {
  if (!err || typeof err !== "object") return false;
  const s = (err as { status?: unknown }).status;
  return s === 0 || s === 502 || s === 503 || s === 504;
}

export function verifierSustainedReason(
  sinceMs: number,
  now = Date.now(),
): string {
  const secs = Math.max(
    Math.round(VERIFIER_SUSTAINED_MS / 1000),
    Math.floor((now - sinceMs) / 1000),
  );
  const age = secs < 120 ? `${secs}s` : `${Math.floor(secs / 60)} min`;
  return `Sign-in verification has been temporarily unable to check your identity for ${age} — retrying automatically.`;
}

// normalizeSnackMessage strips per-response `(ref <id>)` suffixes when
// comparing two snackbar messages, so two surfaces reporting the same
// verifier/gateway error with distinct request refs do not show twice.
export function normalizeSnackMessage(msg: string): string {
  return msg.replace(/\s*\(ref\s+[A-Za-z0-9._:-]+\)/g, "").trim();
}

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
  private onlyVerifier = true;

  // fail records a failed read, and reports whether the run of them is
  // now sustained. A run consisting solely of verifier_unavailable 503s is
  // sustained only after VERIFIER_SUSTAINED_MS (30s), matching ui/src/api.js.
  fail(err: unknown, now = Date.now()): boolean {
    this.failures++;
    this.since ??= now;
    if (!isVerifierUnavailable(err)) {
      this.onlyVerifier = false;
    }
    this.retryAfter = retryAfterMs(err, now);
    return this.sustained(now);
  }

  ok(): void {
    this.failures = 0;
    this.since = null;
    this.retryAfter = null;
    this.onlyVerifier = true;
  }

  get failing(): boolean {
    return this.failures > 0;
  }

  sustained(now = Date.now()): boolean {
    if (this.failures === 0) return false;
    if (this.onlyVerifier) {
      return this.since !== null && now - this.since >= VERIFIER_SUSTAINED_MS;
    }
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

const GLOBAL_BASE_BACKOFF_MS = 3_000;

// ServerTrouble tracks tab-wide 502/503/504/network trouble across every
// /api/ call so secondary polls (transcripts, config, sandboxes, sessions,
// local-script approvals) hold off while the server or sign-in proxy is
// failing, and exposes the classic 30s verifier_unavailable banner state.
@Injectable({ providedIn: "root" })
export class ServerTrouble implements OnDestroy {
  private readonly zone = inject(NgZone);
  private readonly trouble = new ReadTrouble();
  private readonly seenErrors = new WeakSet<object>();
  private backoffUntilMs = 0;
  private verifierTimer: ReturnType<typeof setTimeout> | null = null;

  readonly verifierSince = signal<number | null>(null);
  readonly verifierSustained = signal(false);

  get failing(): boolean {
    return this.trouble.failing;
  }

  coolingDown(now = Date.now()): boolean {
    return this.trouble.failing && now < this.backoffUntilMs;
  }

  remainingMs(now = Date.now()): number {
    return this.trouble.failing ? Math.max(0, this.backoffUntilMs - now) : 0;
  }

  recordFailure(
    err: unknown,
    baseMs = GLOBAL_BASE_BACKOFF_MS,
    now = Date.now(),
  ): void {
    if (err && typeof err === "object") {
      if (this.seenErrors.has(err)) return;
      this.seenErrors.add(err);
    }
    if (!isTransientServerError(err)) {
      this.clearVerifier();
      return;
    }
    this.trouble.fail(err, now);
    this.backoffUntilMs = Math.max(
      this.backoffUntilMs,
      now + this.trouble.nextDelay(baseMs),
    );
    if (isVerifierUnavailable(err)) {
      const since = this.verifierSince() ?? now;
      if (this.verifierSince() === null) {
        this.verifierSince.set(since);
      }
      const elapsed = now - since;
      if (elapsed >= VERIFIER_SUSTAINED_MS) {
        this.clearVerifierTimer();
        this.verifierSustained.set(true);
      } else if (!this.verifierTimer) {
        const wait = VERIFIER_SUSTAINED_MS - elapsed;
        this.zone.runOutsideAngular(() => {
          this.verifierTimer = setTimeout(() => {
            this.verifierTimer = null;
            if (this.verifierSince() !== null) {
              this.zone.run(() => this.verifierSustained.set(true));
            }
          }, wait);
        });
      }
    }
  }

  recordSuccess(): void {
    this.trouble.ok();
    this.backoffUntilMs = 0;
    this.clearVerifier();
  }

  clearVerifier(): void {
    this.clearVerifierTimer();
    if (this.verifierSince() !== null) this.verifierSince.set(null);
    if (this.verifierSustained()) this.verifierSustained.set(false);
  }

  private clearVerifierTimer(): void {
    if (this.verifierTimer) {
      clearTimeout(this.verifierTimer);
      this.verifierTimer = null;
    }
  }

  ngOnDestroy(): void {
    this.clearVerifierTimer();
  }
}

function isApiUrl(url: string): boolean {
  return url === "/api" || url.startsWith("/api/") || url.startsWith("/api?");
}

export const serverTroubleInterceptor: HttpInterceptorFn = (req, next) => {
  if (!isApiUrl(req.url)) return next(req);
  const serverTrouble = inject(ServerTrouble);
  return next(req).pipe(
    tap({
      next: (ev) => {
        if (ev instanceof HttpResponse) {
          serverTrouble.recordSuccess();
        }
      },
      error: (err: unknown) => {
        if (err instanceof HttpErrorResponse && err.status === 304) {
          serverTrouble.recordSuccess();
          return;
        }
        serverTrouble.recordFailure(err);
      },
    }),
  );
};
