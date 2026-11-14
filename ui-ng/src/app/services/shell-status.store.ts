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
  Injectable,
  NgZone,
  type OnDestroy,
  inject,
  signal,
} from "@angular/core";
import { GrainApi } from "../api/grain-api.service";
import type { GrainConfig } from "../api/models";
import { ConnectionService, REACH_TIMEOUT_MS } from "./connection.service";
import { TaskFeedStore } from "./task-feed.service";

// CONFIG_POLL_MS is how often the shell re-reads GET /api/config: often
// enough that a pause, a lifted pause or a recovered sync shows in the
// bell within seconds, and that an outage is noticed well inside the
// twelve seconds the walkthrough waited. While the deployment is
// unreachable it asks more often, so the banner clears soon after it is
// back.
export const CONFIG_POLL_MS = 5000;
export const DISCONNECTED_POLL_MS = 2500;

// ShellStatusStore is the shell's own heartbeat: GET /api/config on a
// timer, bounded by REACH_TIMEOUT_MS, reported to ConnectionService (the
// disconnected banner) and handed to the alerts the top bar's bell
// lists (shell/alerts.ts). It reads nothing while the tab is hidden, and
// catches up the moment it is shown.
@Injectable({ providedIn: "root" })
export class ShellStatusStore implements OnDestroy {
  private readonly api = inject(GrainApi);
  private readonly feed = inject(TaskFeedStore);
  private readonly connection = inject(ConnectionService);
  private readonly doc = inject(DOCUMENT);
  private readonly zone = inject(NgZone);

  // The last config read; the alerts bell (shell/alerts-button, loaded
  // on idle) turns it into rows with shell/alerts.ts.
  readonly config = signal<GrainConfig | null>(null);

  private timer: ReturnType<typeof setTimeout> | null = null;
  private started = false;
  private inFlight: Promise<void> | null = null;

  private readonly onVisibility = (): void => {
    if (!this.started) return;
    if (this.doc.hidden) {
      this.clearTimer();
    } else {
      void this.refresh().finally(() => this.schedule());
    }
  };

  start(): void {
    if (this.started) return;
    this.started = true;
    this.doc.addEventListener("visibilitychange", this.onVisibility);
    void this.refresh().finally(() => this.schedule());
  }

  stop(): void {
    this.started = false;
    this.doc.removeEventListener("visibilitychange", this.onVisibility);
    this.clearTimer();
  }

  ngOnDestroy(): void {
    this.stop();
  }

  // refresh reads the config once now; concurrent calls share one read.
  refresh(): Promise<void> {
    return (this.inFlight ??= this.load().finally(() => {
      this.inFlight = null;
    }));
  }

  private async load(): Promise<void> {
    if (typeof this.api.config !== "function") return;
    try {
      const cfg = await this.api.config({ timeoutMs: REACH_TIMEOUT_MS });
      if (cfg && typeof cfg === "object") {
        this.config.set(cfg);
        this.connection.restarting = !!(cfg as Record<string, unknown>)[
          "restarting"
        ];
      }
      const wasDown = this.connection.disconnection() !== null;
      this.connection.note("shell");
      // The feed may be the source still holding the banner up, and it
      // polls only every fifteen seconds when idle: ask it now, so the
      // first good answer takes the banner down rather than the next
      // idle tick.
      if (wasDown && this.connection.disconnection() !== null) {
        void this.feed.refresh();
      }
    } catch (err) {
      this.connection.note("shell", err);
    }
  }

  private clearTimer(): void {
    if (this.timer) {
      clearTimeout(this.timer);
      this.timer = null;
    }
  }

  private schedule(): void {
    this.clearTimer();
    if (!this.started || this.doc.hidden) return;
    const ms = this.connection.disconnection()
      ? DISCONNECTED_POLL_MS
      : CONFIG_POLL_MS;
    this.zone.runOutsideAngular(() => {
      this.timer = setTimeout(() => {
        this.timer = null;
        this.zone.run(() => {
          void this.refresh().finally(() => this.schedule());
        });
      }, ms);
    });
  }
}
