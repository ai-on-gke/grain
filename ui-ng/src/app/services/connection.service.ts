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

import { HttpErrorResponse } from "@angular/common/http";
import { Injectable, signal } from "@angular/core";

// REACH_TIMEOUT_MS is how long a polled read may go unanswered before the
// shell counts the deployment as unreachable: the classic UI's
// CONFIG_POLL_TIMEOUT_MS (ui/src/App.jsx), so both UIs give up after the
// same silence.
export const REACH_TIMEOUT_MS = 10000;

// ReachTimeoutError is what a read bounded by REACH_TIMEOUT_MS throws when
// nothing came back in time. sentAt is when it was asked, which is when
// the silence began rather than when the wait for it gave up.
export class ReachTimeoutError extends Error {
  readonly timedOut = true;
  constructor(
    readonly timeoutMs: number,
    readonly sentAt: number,
    url = "",
  ) {
    super(`${url || "The request"} sent no answer within ${timeoutMs}ms`);
    this.name = "ReachTimeoutError";
  }
}

// Disconnection is the banner's state: why, since when (epoch ms), and
// whether the daemon had said it was restarting just before it went away.
export interface Disconnection {
  reason: string;
  since: number;
  restarting: boolean;
}

export const NETWORK_UNREACHABLE_REASON =
  "This deployment cannot be reached: the connection to it failed.";

// RESTARTING_REASON is the sentence for an outage that began right after
// GET /api/config said `restarting`: an upgrade or a restart somebody
// asked for, which should not read as an outage.
export const RESTARTING_REASON =
  "grain is restarting -- an upgrade or a restart is replacing the server.";

function gatewayReason(status: number): string {
  if (status === 502) {
    return "This deployment cannot be reached: a gateway in front of it reports it is down (502 Bad Gateway).";
  }
  if (status === 503) {
    return "This deployment cannot be reached: a gateway in front of it reports it is unavailable (503 Service Unavailable).";
  }
  return "This deployment cannot be reached: a gateway in front of it timed out waiting for an answer (504 Gateway Timeout).";
}

// grainAnswered is whether an error response came from grain itself: the
// daemon answers every error with a JSON object, while a proxy or load
// balancer in front of it answers 502/503/504 with HTML or nothing. A 503
// grain sends on purpose (a verifier hiccup, a draining route) is the
// daemon reachable and answering, not gone.
function grainAnswered(err: HttpErrorResponse): boolean {
  const body = err.error as unknown;
  return (
    !!body &&
    typeof body === "object" &&
    !(typeof Blob !== "undefined" && body instanceof Blob) &&
    !(typeof ProgressEvent !== "undefined" && body instanceof ProgressEvent) &&
    ("error" in (body as object) || "message" in (body as object))
  );
}

// isUnreachable is whether err means nothing answered at all -- a dead
// connection, a read that timed out, or a gateway saying the backend is
// not there -- as opposed to grain answering with an error of its own.
export function isUnreachable(err: unknown): boolean {
  if (err instanceof ReachTimeoutError) return true;
  if ((err as { timedOut?: boolean })?.timedOut === true) return true;
  if (err instanceof HttpErrorResponse) {
    if (err.status === 0) return true;
    if ([502, 503, 504].includes(err.status)) return !grainAnswered(err);
    return false;
  }
  // Anything else -- a TypeError from a bug, a parse failure -- is this
  // page's own problem, not the deployment being out of reach.
  return false;
}

// unreachableReason is the sentence the banner leads with for err, in the
// classic UI's words (ui/src/api.js), rather than the browser's own
// "Failed to fetch", which is not written for an operator.
export function unreachableReason(err: unknown): string {
  const t = err as { timedOut?: boolean; timeoutMs?: number };
  if (t?.timedOut) {
    const secs = Math.round((t.timeoutMs ?? REACH_TIMEOUT_MS) / 1000);
    return `This deployment cannot be reached: it accepted the connection but sent no answer within ${secs} seconds.`;
  }
  if (err instanceof HttpErrorResponse && err.status !== 0) {
    return gatewayReason(err.status);
  }
  return NETWORK_UNREACHABLE_REASON;
}

// ConnectionService is whether this browser can reach the deployment at
// all, as the shared pollers see it (ShellStatusStore's GET /api/config,
// bounded at REACH_TIMEOUT_MS, and TaskFeedStore's GET /api/tasks). The shell's DisconnectedBanner is
// what it looks like on screen.
//
// Each poller names itself, and the banner stays up while any of them is
// failing, so one source answering cannot take down a banner another
// raised and flap it every tick (classic App.jsx's noteUnreachable /
// noteReached). A source has to fail twice in a row before the banner
// goes up -- one dropped request is not an outage -- except for a read
// that sat ten seconds unanswered, which is already a sustained silence,
// and except while the daemon had said it was restarting.
@Injectable({ providedIn: "root" })
export class ConnectionService {
  readonly disconnection = signal<Disconnection | null>(null);

  // The last GET /api/config's `restarting` (ShellStatusStore sets it).
  restarting = false;

  private readonly streaks = new Map<
    string,
    { count: number; firstSeenAt: number }
  >();
  private readonly failing = new Set<string>();

  // noteUnreachable records one unreachable failure from source and
  // returns whether the banner is now up.
  noteUnreachable(source: string, err: unknown): boolean {
    const prev = this.streaks.get(source);
    const sentAt = (err as { sentAt?: number })?.sentAt;
    const firstSeenAt = prev
      ? prev.firstSeenAt
      : typeof sentAt === "number"
        ? sentAt
        : Date.now();
    const count = (prev?.count ?? 0) + 1;
    this.streaks.set(source, { count, firstSeenAt });
    const immediate =
      (err as { timedOut?: boolean })?.timedOut === true || this.restarting;
    if (!immediate && count < 2) return this.disconnection() !== null;
    this.failing.add(source);
    if (!this.disconnection()) {
      this.disconnection.set(
        this.restarting
          ? { reason: RESTARTING_REASON, since: firstSeenAt, restarting: true }
          : {
              reason: unreachableReason(err),
              since: firstSeenAt,
              restarting: false,
            },
      );
    }
    return true;
  }

  // noteReached records that source got an answer -- any answer grain
  // itself sent, an error included -- and takes the banner down once no
  // source is failing.
  noteReached(source: string): void {
    this.streaks.delete(source);
    this.failing.delete(source);
    if (this.failing.size === 0 && this.disconnection() !== null) {
      this.disconnection.set(null);
    }
  }

  // note routes one poll outcome: err undefined (or an error grain
  // answered with) is reached, anything isUnreachable calls one is not.
  note(source: string, err?: unknown): void {
    if (err !== undefined && isUnreachable(err)) {
      this.noteUnreachable(source, err);
    } else {
      this.noteReached(source);
    }
  }
}
