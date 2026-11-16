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

import { DOCUMENT } from "@angular/common";
import { Injectable, NgZone, OnDestroy, inject, signal } from "@angular/core";
import {
  GrainApi,
  READ_TIMEOUT_MS,
  isCancelledRead,
  isVerifierUnavailable,
} from "../api/grain-api.service";
import { AccessService } from "./access.service";

export const PROBE_TIMEOUT_MS = 5_000;
export const CONFIRM_DELAY_MS = 1_000;
export const HEARTBEAT_IDLE_MS = 10_000;
export const RECOVERY_POLL_MS = 3_000;
const PROBE_OK_COOLDOWN_MS = 3_000;

export interface DisconnectionNotice {
  readonly headline: string;
  readonly detail: string;
  readonly restarting: boolean;
  readonly since: number;
}

// isUnreachable reports whether an API rejection means the daemon could not be
// reached at all -- a timed-out read, a dropped/refused connection (Angular's
// HttpErrorResponse status 0 or fetch TypeError), or a 502/503/504 from a
// gateway in front of it (excluding the daemon's own verifier_unavailable 503).
export function isUnreachable(err: unknown): boolean {
  if (!err || typeof err !== "object") return false;
  if (isVerifierUnavailable(err)) return false;
  if ((err as { timedOut?: unknown }).timedOut === true) return true;
  if (err instanceof TypeError) return true;
  const status = (err as { status?: unknown }).status;
  if (typeof status === "number") {
    return status === 0 || status === 502 || status === 503 || status === 504;
  }
  return false;
}

function formatSinceTime(ts: number, timeZone?: string): string {
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return "";
  try {
    return new Intl.DateTimeFormat(undefined, {
      hour: "2-digit",
      minute: "2-digit",
      ...(timeZone ? { timeZone } : {}),
    }).format(d);
  } catch {
    return new Intl.DateTimeFormat(undefined, {
      hour: "2-digit",
      minute: "2-digit",
    }).format(d);
  }
}

export function describeDisconnection(
  err: unknown,
  since: number,
  restarting: boolean,
  timeZone?: string,
): DisconnectionNotice {
  const at = formatSinceTime(since, timeZone);
  const sinceText = at ? ` since ${at}` : "";
  if (restarting) {
    return {
      headline: "Grain is restarting",
      detail: `Unavailable${sinceText} — retrying automatically; this page will pick back up on its own once grain is back.`,
      restarting: true,
      since,
    };
  }
  const timedOut = (err as { timedOut?: unknown })?.timedOut === true;
  if (timedOut) {
    const ms =
      typeof (err as { timeoutMs?: unknown })?.timeoutMs === "number"
        ? ((err as { timeoutMs: number }).timeoutMs ?? READ_TIMEOUT_MS)
        : READ_TIMEOUT_MS;
    const secs = Math.max(1, Math.round(ms / 1000));
    return {
      headline: "Server is not responding",
      detail: `It accepted the connection but sent no answer within ${secs}s${sinceText} — retrying automatically; this page will pick back up on its own once it answers again.`,
      restarting: false,
      since,
    };
  }
  const status = (err as { status?: unknown })?.status;
  if (status === 502) {
    return {
      headline: "Server cannot be reached",
      detail: `A gateway in front of it reports it is down (502 Bad Gateway)${sinceText} — retrying automatically; this page will pick back up on its own once it answers again.`,
      restarting: false,
      since,
    };
  }
  if (status === 503) {
    return {
      headline: "Server cannot be reached",
      detail: `A gateway in front of it reports it is unavailable (503 Service Unavailable)${sinceText} — retrying automatically; this page will pick back up on its own once it answers again.`,
      restarting: false,
      since,
    };
  }
  if (status === 504) {
    return {
      headline: "Server cannot be reached",
      detail: `A gateway in front of it timed out waiting for an answer (504 Gateway Timeout)${sinceText} — retrying automatically; this page will pick back up on its own once it answers again.`,
      restarting: false,
      since,
    };
  }
  return {
    headline: "Server cannot be reached",
    detail: `The connection to the daemon failed${sinceText} — retrying automatically; this page will pick back up on its own once it answers again.`,
    restarting: false,
    since,
  };
}

// ConnectionService watches API activity across the /next/ shell, confirms
// daemon-wide silence or connection refusals against GET /api/config so a
// single route's 502/503 never masquerades as a dead server, and probes every
// 3s while disconnected so the notice clears as soon as the daemon answers.
@Injectable({ providedIn: "root" })
export class ConnectionService implements OnDestroy {
  private readonly api = inject(GrainApi);
  private readonly access = inject(AccessService);
  private readonly doc = inject(DOCUMENT);
  private readonly zone = inject(NgZone);

  readonly disconnected = signal<DisconnectionNotice | null>(null);

  private started = false;
  private unobserve: (() => void) | null = null;
  private firstFailure: { err: unknown; since: number } | null = null;
  private lastOkAt = 0;
  private lastProbeOkAt = 0;
  private probing = false;
  private confirmTimer: ReturnType<typeof setTimeout> | null = null;
  private heartbeatTimer: ReturnType<typeof setTimeout> | null = null;
  private recoveryTimer: ReturnType<typeof setTimeout> | null = null;
  private readonly disconnectListeners = new Set<() => void>();
  private readonly reconnectListeners = new Set<() => void>();

  private readonly onVisibility = (): void => {
    if (!this.started) return;
    if (this.doc.hidden) {
      this.clearTimers();
      return;
    }
    if (this.disconnected()) {
      void this.runRecoveryProbe();
    } else {
      this.scheduleHeartbeat();
    }
  };

  // quietsReadError says whether a page should keep a failed read off its
  // own banner: a read cancelled on purpose never is news, and an
  // unreachable read over data the page already shows is the outage the
  // shell's one disconnected banner reports -- once it is up, or while its
  // confirmation is in flight -- so the page says nothing of its own.
  quietsReadError(err: unknown, hasData: boolean): boolean {
    if (isCancelledRead(err)) return true;
    if (!hasData || !isUnreachable(err)) return false;
    if (this.disconnected() !== null) return true;
    // Before the outage is confirmed, only a read that got no answer at all
    // (timed out, connection dropped or refused) waits on the confirmation.
    // A gateway's 502/503/504 for one route while /api/config still answers
    // is that route's own failure, and the page says so as it always has.
    const status = (err as { status?: unknown })?.status;
    const noAnswer =
      (err as { timedOut?: unknown })?.timedOut === true ||
      err instanceof TypeError ||
      status === 0;
    return (
      noAnswer &&
      (this.firstFailure !== null || this.probing || this.confirmTimer !== null)
    );
  }

  // whileOutage clears a page's banner when the shell's disconnected banner
  // goes up over data the page already has, and runs onBack (a recovery
  // refresh) when the server answers again. Root stores call it once.
  whileOutage(
    clear: () => void,
    hasData: () => boolean,
    onBack?: () => void,
  ): () => void {
    const offDown = this.onDisconnect(() => {
      if (hasData()) clear();
    });
    const offUp = onBack ? this.onReconnect(onBack) : () => undefined;
    return () => {
      offDown();
      offUp();
    };
  }

  onDisconnect(fn: () => void): () => void {
    this.disconnectListeners.add(fn);
    return () => this.disconnectListeners.delete(fn);
  }

  onReconnect(fn: () => void): () => void {
    this.reconnectListeners.add(fn);
    return () => this.reconnectListeners.delete(fn);
  }

  start(): void {
    if (
      this.started ||
      typeof this.api.observeActivity !== "function" ||
      typeof this.api.probeConfig !== "function"
    ) {
      return;
    }
    this.started = true;
    this.lastOkAt = Date.now();
    this.unobserve = this.api.observeActivity({
      onOk: () => this.onDaemonAnswered(),
      onError: (url, err) => this.onRequestError(url, err),
    });
    this.doc.addEventListener("visibilitychange", this.onVisibility);
    if (!this.doc.hidden) {
      this.scheduleHeartbeat();
    }
  }

  stop(): void {
    if (!this.started) return;
    this.started = false;
    this.unobserve?.();
    this.unobserve = null;
    this.doc.removeEventListener("visibilitychange", this.onVisibility);
    this.clearTimers();
    this.firstFailure = null;
    this.probing = false;
  }

  ngOnDestroy(): void {
    this.stop();
    this.disconnectListeners.clear();
    this.reconnectListeners.clear();
  }

  private clearTimers(): void {
    if (this.confirmTimer) {
      clearTimeout(this.confirmTimer);
      this.confirmTimer = null;
    }
    if (this.heartbeatTimer) {
      clearTimeout(this.heartbeatTimer);
      this.heartbeatTimer = null;
    }
    if (this.recoveryTimer) {
      clearTimeout(this.recoveryTimer);
      this.recoveryTimer = null;
    }
  }

  private onDaemonAnswered(): void {
    if (!this.started) return;
    this.lastOkAt = Date.now();
    this.firstFailure = null;
    if (this.confirmTimer) {
      clearTimeout(this.confirmTimer);
      this.confirmTimer = null;
    }
    if (this.disconnected()) {
      this.markRecovered();
    } else if (!this.doc.hidden) {
      this.scheduleHeartbeat();
    }
  }

  private onRequestError(url: string, err: unknown): void {
    if (!this.started) return;
    if (isCancelledRead(err)) return;
    if (!isUnreachable(err)) {
      // Any HTTP status from the daemon itself proves it is reachable.
      this.onDaemonAnswered();
      return;
    }
    if (this.disconnected()) {
      if (!this.doc.hidden && !this.probing && !this.recoveryTimer) {
        this.scheduleRecoveryProbe();
      }
      return;
    }
    const now = Date.now();
    if (
      !url.startsWith("/api/config") &&
      now - this.lastProbeOkAt < PROBE_OK_COOLDOWN_MS
    ) {
      return;
    }
    const sentAt =
      typeof (err as { sentAt?: unknown })?.sentAt === "number"
        ? (err as { sentAt: number }).sentAt
        : now;
    this.firstFailure ??= { err, since: sentAt };
    if (this.heartbeatTimer) {
      clearTimeout(this.heartbeatTimer);
      this.heartbeatTimer = null;
    }
    if (this.probing || this.confirmTimer) return;

    const immediate =
      (err as { timedOut?: unknown })?.timedOut === true ||
      Boolean(this.access.rawConfig()?.["restarting"]);
    const delay = immediate ? 0 : CONFIRM_DELAY_MS;
    if (delay === 0) {
      void this.confirmDisconnection();
      return;
    }
    this.zone.runOutsideAngular(() => {
      this.confirmTimer = setTimeout(() => {
        this.confirmTimer = null;
        this.zone.run(() => void this.confirmDisconnection());
      }, delay);
    });
  }

  private async confirmDisconnection(): Promise<void> {
    if (!this.started || this.probing || this.disconnected()) return;
    this.probing = true;
    try {
      const cfg = await this.api.probeConfig(PROBE_TIMEOUT_MS);
      if (!this.started) return;
      if (cfg) this.access.setConfig(cfg);
      this.lastProbeOkAt = Date.now();
      this.onDaemonAnswered();
    } catch (probeErr) {
      if (!this.started) return;
      if (!isUnreachable(probeErr)) {
        this.lastProbeOkAt = Date.now();
        this.onDaemonAnswered();
        return;
      }
      const cause = this.firstFailure?.err ?? probeErr;
      const since = this.firstFailure?.since ?? Date.now();
      const rawCfg = this.access.rawConfig();
      const restarting = Boolean(rawCfg?.["restarting"]);
      const notice = describeDisconnection(
        cause,
        since,
        restarting,
        rawCfg?.timeZone,
      );
      if (this.heartbeatTimer) {
        clearTimeout(this.heartbeatTimer);
        this.heartbeatTimer = null;
      }
      this.zone.run(() => {
        this.disconnected.set(notice);
        for (const fn of this.disconnectListeners) fn();
      });
      if (!this.doc.hidden) {
        this.scheduleRecoveryProbe();
      }
    } finally {
      this.probing = false;
    }
  }

  private scheduleHeartbeat(): void {
    if (
      this.heartbeatTimer ||
      !this.started ||
      this.doc.hidden ||
      this.probing ||
      this.firstFailure !== null ||
      this.disconnected()
    ) {
      return;
    }
    const elapsed = Date.now() - this.lastOkAt;
    const wait = Math.max(250, HEARTBEAT_IDLE_MS - elapsed);
    this.zone.runOutsideAngular(() => {
      this.heartbeatTimer = setTimeout(() => {
        this.heartbeatTimer = null;
        if (
          !this.started ||
          this.doc.hidden ||
          this.probing ||
          this.firstFailure !== null ||
          this.disconnected()
        ) {
          return;
        }
        if (Date.now() - this.lastOkAt < HEARTBEAT_IDLE_MS) {
          this.scheduleHeartbeat();
          return;
        }
        this.zone.run(() => {
          void this.api
            .config()
            .then((cfg) => {
              if (cfg && this.started) this.access.setConfig(cfg);
            })
            .catch(() => undefined)
            .finally(() => {
              if (this.started && !this.doc.hidden && !this.disconnected()) {
                this.scheduleHeartbeat();
              }
            });
        });
      }, wait);
    });
  }

  private scheduleRecoveryProbe(): void {
    if (
      this.recoveryTimer ||
      !this.started ||
      this.doc.hidden ||
      !this.disconnected()
    ) {
      return;
    }
    this.zone.runOutsideAngular(() => {
      this.recoveryTimer = setTimeout(() => {
        this.recoveryTimer = null;
        this.zone.run(() => void this.runRecoveryProbe());
      }, RECOVERY_POLL_MS);
    });
  }

  private async runRecoveryProbe(): Promise<void> {
    if (!this.started || this.doc.hidden || !this.disconnected()) return;
    if (this.probing) {
      this.scheduleRecoveryProbe();
      return;
    }
    this.probing = true;
    try {
      const cfg = await this.api.probeConfig(PROBE_TIMEOUT_MS);
      if (!this.started) return;
      this.lastProbeOkAt = Date.now();
      if (cfg) this.access.setConfig(cfg);
      this.markRecovered();
    } catch (err) {
      if (!this.started) return;
      if (!isUnreachable(err)) {
        this.markRecovered();
        return;
      }
      this.scheduleRecoveryProbe();
    } finally {
      this.probing = false;
    }
  }

  private markRecovered(): void {
    if (this.recoveryTimer) {
      clearTimeout(this.recoveryTimer);
      this.recoveryTimer = null;
    }
    this.firstFailure = null;
    this.lastOkAt = Date.now();
    this.lastProbeOkAt = this.lastOkAt;
    if (typeof this.api.abortStaleReads === "function") {
      this.api.abortStaleReads(1_500);
    }
    this.zone.run(() => {
      this.disconnected.set(null);
      for (const fn of this.reconnectListeners) fn();
    });
    if (!this.doc.hidden) {
      this.scheduleHeartbeat();
    }
  }
}
