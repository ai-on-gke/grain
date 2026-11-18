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
import {
  DestroyRef,
  Injectable,
  NgZone,
  computed,
  effect,
  inject,
  signal,
  untracked,
} from "@angular/core";
import { GrainApi } from "../api/grain-api.service";
import type { GrainConfig } from "../api/models";
import { type VersionInfo, versionLabel } from "../api/version";
import { errorText } from "../sessions/session-helpers";
import type { UpgradeStep } from "../settings/upgrade-progress";
import { AccessService } from "./access.service";

// GET /api/upgrade's answer (pkg/ui/upgrade.go's upgradeStatusResponse),
// as far as either the Upgrade pane or the shell reads it.
export interface UpgradeStatus {
  enabled?: boolean;
  mode?: string;
  running?: {
    image?: string;
    release?: string;
    edition?: string;
    commit?: string;
    ref?: string;
    branch?: string;
  } | null;
  channel?: string;
  branch?: string;
  phase?: string;
  detail?: string;
  unchanged?: boolean;
  steps?: UpgradeStep[];
  startedAt?: string;
  finishedAt?: string;
  graceUntil?: string;
  cancellable?: boolean;
  cancelBlockedReason?: string;
  inFlightRuns?: number;
  scheduled?: {
    branch?: string;
    busyRuns?: number;
    busyCiRuns?: number;
    byKind?: string;
    byId?: string;
  } | null;
  database?: { driver?: string; dsn?: string; isSqlite?: boolean } | null;
  imageRepo?: string;
  defaultImageRepo?: string;
  imageRepos?: string[];
  imageRepoWarning?: string;
  discardedRc?: {
    running?: string;
    latest?: string;
    replacementRc?: string;
  } | null;
  retractedRelease?: {
    running?: string;
    latest?: string;
    newer?: string;
  } | null;
  selfUpgradeDisabled?: boolean;
  managementUrl?: string;
  notices?: {
    kind?: string;
    message?: string;
    command?: string;
    docsUrl?: string;
  }[];
  currentVersion?: string;
  version?: string;
}

// STATUS_POLL_MS is how often GET /api/upgrade is re-read while an
// upgrade runs or waits to (the classic pane's STATUS_POLL_MS), and
// RESTART_POLL_MS how often GET /api/config is asked, once the upgrade
// says it is done, whether the replacement build is answering yet.
export const STATUS_POLL_MS = 3000;
export const RESTART_POLL_MS = 1000;
// RELOAD_AFTER_S is how long the "grain was upgraded" prompt waits
// before reloading by itself, when nothing on the page is unsaved.
export const RELOAD_AFTER_S = 5;

// UpgradeWatchService is this tab's one reading of the deployment's
// upgrade, shared by Settings -> Upgrade and the shell's banner, so an
// upgrade started on one page stays visible on every other: it polls
// GET /api/upgrade while one is running or scheduled, and it notices when
// the daemon comes back on another build (GET /api/config's commit
// changes) and reloads onto it -- the classic App's onReload -- rather
// than leaving the old bundle to 404 on the new build's lazy chunks.
@Injectable({ providedIn: "root" })
export class UpgradeWatchService {
  private readonly api = inject(GrainApi);
  private readonly access = inject(AccessService);
  private readonly zone = inject(NgZone);
  private readonly doc = inject(DOCUMENT);

  readonly status = signal<UpgradeStatus | null>(null);
  readonly loadError = signal("");
  readonly now = signal(Date.now());
  // Whether this tab has seen the upgrade running: only then is a
  // failure or a finish news worth a banner on every page.
  readonly sawRunning = signal(false);
  // The build the daemon came back on, once its commit differs from the
  // one this bundle was loaded against.
  readonly upgradedTo = signal<string | null>(null);
  // Seconds until the reload, or null while it waits on unsaved work.
  readonly reloadIn = signal<number | null>(null);

  readonly running = computed(() => this.status()?.phase === "running");
  // Whether the shell has anything to say about the upgrade: one in
  // flight, one this tab watched fail, or the new build to reload onto.
  readonly bannerWanted = computed(() => {
    const phase = this.status()?.phase;
    return (
      !!this.upgradedTo() ||
      phase === "running" ||
      (this.sawRunning() && phase === "failed")
    );
  });

  // reloadPage is replaceable so a unit test can watch it fire.
  reloadPage: () => void = () => this.doc.defaultView?.location.reload();

  private bootCommit = "";
  private reading = false;
  private bootRead = false;
  private pollTimer: ReturnType<typeof setInterval> | null = null;
  private clockTimer: ReturnType<typeof setInterval> | null = null;
  private probeTimer: ReturnType<typeof setInterval> | null = null;
  private countdownTimer: ReturnType<typeof setInterval> | null = null;

  constructor() {
    // Every GET /api/config the shell (or anything else) hands
    // AccessService is a chance to notice the daemon changed builds.
    effect(() => {
      const cfg = this.access.rawConfig();
      untracked(() => this.observeConfig(cfg));
    });
    inject(DestroyRef).onDestroy(() => this.stopAll());
  }

  // start reads the upgrade status once, for a page that has not asked
  // yet: the shell calls it so an upgrade already under way when the tab
  // opened is shown, and is polled, wherever the operator is.
  start(): void {
    if (this.bootRead || !this.access.can("GET /api/upgrade")) return;
    this.bootRead = true;
    void this.refresh();
  }

  async refresh(): Promise<UpgradeStatus | null> {
    if (this.reading || typeof this.api.get !== "function") {
      return this.status();
    }
    this.reading = true;
    try {
      const next = await this.api.get<UpgradeStatus>("GET /api/upgrade");
      this.loadError.set("");
      this.apply(next);
      return next;
    } catch (err) {
      // A read that fails mid-upgrade is the restart it ends in: keep
      // what was last known, and keep polling through it.
      this.loadError.set(errorText(err));
      return this.status();
    } finally {
      this.reading = false;
    }
  }

  // apply takes a status from anywhere that has one -- a poll, or the
  // answer to POST /api/upgrade or DELETE /api/upgrade/current.
  // It says whether it took it: an answer that is not a status (an older
  // server's {ok: true}) is left for the next read to replace.
  apply(next: UpgradeStatus | null | undefined): boolean {
    if (!next || typeof next !== "object") return false;
    if (!("phase" in next) && !("enabled" in next)) return false;
    if (next.phase === "running") this.sawRunning.set(true);
    this.now.set(Date.now());
    this.status.set(next);
    this.schedule();
    return true;
  }

  observeConfig(cfg: GrainConfig | null): void {
    const v = cfg?.version as VersionInfo | string | undefined;
    const commit = v && typeof v === "object" ? v.commit : "";
    if (!commit) return;
    if (!this.bootCommit) {
      this.bootCommit = commit;
      return;
    }
    if (commit !== this.bootCommit) {
      this.announceUpgrade(versionLabel(v) || commit.slice(0, 7));
    }
  }

  // reloadNow is the prompt's Reload button, and the countdown's end.
  reloadNow(): void {
    this.stopAll();
    this.reloadPage();
  }

  private schedule(): void {
    const s = this.status();
    const watching =
      !!s &&
      !this.upgradedTo() &&
      (s.phase === "running" || !!s.scheduled || !!s.cancelBlockedReason);
    if (watching && !this.pollTimer) {
      this.pollTimer = this.every(STATUS_POLL_MS, () => void this.refresh());
    } else if (!watching && this.pollTimer) {
      clearInterval(this.pollTimer);
      this.pollTimer = null;
    }

    const ticking = !!s && s.phase === "running";
    if (ticking && !this.clockTimer) {
      this.clockTimer = this.every(1000, () => this.now.set(Date.now()));
    } else if (!ticking && this.clockTimer) {
      clearInterval(this.clockTimer);
      this.clockTimer = null;
    }

    // Once an upgrade this tab watched says it is done, the daemon
    // restarts onto the new build: ask GET /api/config until it answers
    // with another commit. Downtime in between is expected.
    const probing =
      !!s &&
      this.sawRunning() &&
      s.phase === "ok" &&
      !s.unchanged &&
      !this.upgradedTo();
    if (probing && !this.probeTimer) {
      void this.probe();
      this.probeTimer = this.every(RESTART_POLL_MS, () => void this.probe());
    } else if (!probing && this.probeTimer) {
      clearInterval(this.probeTimer);
      this.probeTimer = null;
    }
  }

  private async probe(): Promise<void> {
    try {
      const cfg = await this.api.get<GrainConfig>(
        "GET /api/config",
        undefined,
        `after-upgrade=${Date.now()}`,
      );
      if (cfg && typeof cfg === "object") this.observeConfig(cfg);
    } catch {
      // The restart's downtime: the next tick asks again.
    }
  }

  private announceUpgrade(label: string): void {
    if (this.upgradedTo()) return;
    this.upgradedTo.set(label);
    this.schedule();
    this.reloadIn.set(this.hasUnsavedWork() ? null : RELOAD_AFTER_S);
    this.countdownTimer = this.every(1000, () => {
      if (this.hasUnsavedWork()) {
        this.reloadIn.set(null);
        return;
      }
      const left = (this.reloadIn() ?? RELOAD_AFTER_S + 1) - 1;
      if (left <= 0) {
        this.reloadNow();
        return;
      }
      this.reloadIn.set(left);
    });
  }

  // hasUnsavedWork asks the page the way the browser would before
  // leaving it: every page holding an unsaved draft already cancels
  // beforeunload (Settings, a repository's plan, a QA flow), so a
  // cancelled synthetic one means there is something to lose.
  hasUnsavedWork(): boolean {
    const win = this.doc.defaultView;
    if (!win) return false;
    const ev = new Event("beforeunload", { cancelable: true });
    win.dispatchEvent(ev);
    return ev.defaultPrevented;
  }

  private every(ms: number, fn: () => void): ReturnType<typeof setInterval> {
    return this.zone.runOutsideAngular(() =>
      setInterval(() => this.zone.run(fn), ms),
    );
  }

  private stopAll(): void {
    for (const t of [
      this.pollTimer,
      this.clockTimer,
      this.probeTimer,
      this.countdownTimer,
    ]) {
      if (t) clearInterval(t);
    }
    this.pollTimer = this.clockTimer = this.probeTimer = null;
    this.countdownTimer = null;
  }
}
