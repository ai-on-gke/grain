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
  type HttpInterceptorFn,
  HttpResponse,
} from "@angular/common/http";
import { Injectable, inject, signal } from "@angular/core";
import { tap, throwError, timeout } from "rxjs";

// HANG_MS bounds how long a polled read (the task feed's GET /api/tasks and
// the shell's GET /api/config) may go unanswered before the daemon counts as
// out of reach: one that accepts the connection and then never answers --
// deadlocked, stopped, or behind a proxy holding the socket open -- is as
// unreachable as one that refuses it, and without a bound the poll would
// wait forever and nothing could ever say so. The classic UI uses the same
// ten seconds (ui/src/api.js).
export const HANG_MS = 10_000;

const POLLED_READS = new Set(["/api/tasks", "/api/config"]);

export interface Disconnection {
  reason: string;
  since: number;
}

const NETWORK_REASON =
  "This deployment cannot be reached: the connection to it failed.";

// unreachableReason is the sentence for an error that means the daemon could
// not be reached at all, or "" when the daemon (or something speaking for it)
// answered. A network failure surfaces as status 0; a 502/503/504 is a
// gateway in front of the daemon saying it is not there -- except the
// daemon's own verifier_unavailable 503, which is the daemon answering.
export function unreachableReason(err: unknown): string {
  if (!(err instanceof HttpErrorResponse)) return "";
  const body = err.error as { code?: string } | null;
  switch (err.status) {
    case 0:
      return NETWORK_REASON;
    case 502:
      return "This deployment cannot be reached: a gateway in front of it reports it is down (502 Bad Gateway).";
    case 503:
      if (
        body &&
        typeof body === "object" &&
        body.code === "verifier_unavailable"
      )
        return "";
      return "This deployment cannot be reached: a gateway in front of it reports it is unavailable (503 Service Unavailable).";
    case 504:
      return "This deployment cannot be reached: a gateway in front of it timed out waiting for an answer (504 Gateway Timeout).";
    default:
      return "";
  }
}

export const HANG_REASON = `This deployment cannot be reached: it accepted the connection but sent no answer within ${HANG_MS / 1000} seconds.`;

// ConnectionStatus is whether the daemon is answering. The shell shows a
// standing banner while `disconnection` is set; any answer at all clears it,
// so it takes itself down without a reload once the daemon is back.
@Injectable({ providedIn: "root" })
export class ConnectionStatus {
  readonly disconnection = signal<Disconnection | null>(null);

  answered(): void {
    if (this.disconnection() !== null) this.disconnection.set(null);
  }

  unreachable(reason: string, now = Date.now()): void {
    const prev = this.disconnection();
    if (prev?.reason === reason) return;
    // Keep the outage's start when only the way it fails changes.
    this.disconnection.set({ reason, since: prev?.since ?? now });
  }
}

// connectionInterceptor feeds ConnectionStatus from every /api/ request the
// app makes, and bounds the polled reads by HANG_MS so a held connection
// both raises the banner and lets the next poll go out.
export const connectionInterceptor: HttpInterceptorFn = (req, next) => {
  const path = req.url.split("?")[0] ?? req.url;
  if (!path.startsWith("/api/")) return next(req);
  const conn = inject(ConnectionStatus);
  const out = next(req).pipe(
    tap({
      next: (ev) => {
        if (ev instanceof HttpResponse) conn.answered();
      },
      error: (err: unknown) => {
        const reason = unreachableReason(err);
        if (reason) conn.unreachable(reason);
        else if (err instanceof HttpErrorResponse) conn.answered();
      },
    }),
  );
  if (req.method !== "GET" || !POLLED_READS.has(path)) return out;
  return out.pipe(
    timeout({
      each: HANG_MS,
      with: () => {
        conn.unreachable(HANG_REASON);
        return throwError(
          () =>
            new HttpErrorResponse({
              status: 0,
              statusText: "No answer",
              url: req.url,
              error: { error: HANG_REASON },
            }),
        );
      },
    }),
  );
};
