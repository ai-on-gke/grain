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
  Injectable,
  NgZone,
  OnDestroy,
  computed,
  effect,
  inject,
  signal,
  untracked,
} from "@angular/core";
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents } from "../api/grain-events.service";
import type { Notification } from "../api/models";
import { AccessService } from "../services/access.service";
import { TaskFeedStore } from "../services/task-feed.service";
import { effectiveDisplayTimeZone } from "../shared/dispatch-status";
import { collectAlerts, notificationAlerts, sortAlerts } from "./alerts";

// POLL_MS is how often open notifications are re-read with nothing on the
// event stream to say they changed: the classic bell's own interval
// (notifications.js POLL_MS).
const POLL_MS = 30_000;

// AlertsService is the one list of standing alerts both the bell and
// Fleet → Health read. Config comes from AccessService (the shell keeps
// it fresh), tasks from the shared feed, and open notifications are read
// here: on start, every POLL_MS while the tab is visible, and on each
// notification.* event. Nothing is dropped locally after an action --
// refresh() re-reads what the action changed, so a row clears when the
// daemon says its cause has.
@Injectable({ providedIn: "root" })
export class AlertsService implements OnDestroy {
  private readonly api = inject(GrainApi);
  private readonly access = inject(AccessService);
  private readonly feed = inject(TaskFeedStore);
  private readonly events = inject(GrainEvents);
  private readonly zone = inject(NgZone);

  readonly notifications = signal<readonly Notification[]>([]);
  private timer: ReturnType<typeof setInterval> | null = null;
  private eventSub: { unsubscribe(): void } | null = null;
  private users = 0;

  readonly zoneName = computed(() =>
    effectiveDisplayTimeZone(this.access.rawConfig()?.timeZone),
  );

  readonly alerts = computed(() =>
    sortAlerts([
      ...collectAlerts(
        this.access.rawConfig(),
        this.feed.tasks(),
        this.zoneName(),
        new Date(this.access.clockNow()),
      ),
      ...notificationAlerts(this.notifications()),
    ]),
  );

  constructor() {
    // The first read can come before GET /api/config has said who the
    // reader is, when the notifications route is not yet known to be
    // theirs to read: read again once it has.
    effect(() => {
      if (this.access.configLoaded() && untracked(() => this.users) > 0) {
        untracked(() => void this.reloadNotifications());
      }
    });
  }

  // attach starts reading notifications for one more component and
  // returns what stops it; the last to detach stops the poll.
  attach(): () => void {
    this.users++;
    if (this.users === 1) this.start();
    let done = false;
    return () => {
      if (done) return;
      done = true;
      this.users--;
      if (this.users === 0) this.stop();
    };
  }

  private start(): void {
    void this.reloadNotifications();
    this.zone.runOutsideAngular(() => {
      this.timer = setInterval(() => {
        if (typeof document === "undefined" || document.visibilityState !== "hidden") {
          this.zone.run(() => void this.reloadNotifications());
        }
      }, POLL_MS);
    });
    if (typeof this.events.events === "function") {
      this.eventSub = this.events.events().subscribe((e) => {
        if (e.type.startsWith("notification.")) void this.reloadNotifications();
      });
    }
  }

  private stop(): void {
    if (this.timer) clearInterval(this.timer);
    this.timer = null;
    this.eventSub?.unsubscribe();
    this.eventSub = null;
  }

  async reloadNotifications(): Promise<void> {
    if (!this.access.can("GET /api/notifications")) {
      this.notifications.set([]);
      return;
    }
    try {
      const list = await this.api.openNotifications();
      this.notifications.set(Array.isArray(list) ? list : []);
    } catch {
      // The daemon being unreachable is the disconnected banner's to say.
    }
  }

  // refresh re-reads everything an alert is derived from: config (into
  // AccessService, so the dispatch banner and live pill follow too) and
  // open notifications.
  async refresh(): Promise<void> {
    const cfg = await this.api.config().catch(() => null);
    if (cfg) this.access.setConfig(cfg);
    this.access.tickClock();
    await this.reloadNotifications();
  }

  ngOnDestroy(): void {
    this.stop();
  }
}
