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

import { Injectable, NgZone, OnDestroy, inject } from "@angular/core";
import { Observable, Subject } from "rxjs";
import { routePath } from "./grain-api.service";

// The event names GET /api/events?stream=1 sends (pkg/ui/events.go). A
// named SSE frame never reaches EventSource.onmessage, so each one is
// listened for by name.
export const GRAIN_EVENT_TYPES = [
  "task.state",
  "task.question",
  "task.comment",
  "task.ci",
  "notification.raised",
  "notification.acked",
  "notification.cleared",
  "local_script.pending",
  "local_script.answered",
] as const;

export interface GrainEvent {
  type: string;
  data: unknown;
}

export type EventSourceFactory = (url: string) => EventSource;

// GrainEvents is the one live connection to the server's event stream,
// shared by every page that wants to know something changed. It
// reconnects with backoff on its own; a page treats an event as "read
// again", never as the new state itself, so a dropped event costs a
// slower refresh rather than a wrong screen.
@Injectable({ providedIn: "root" })
export class GrainEvents implements OnDestroy {
  private readonly zone = inject(NgZone);
  private readonly subject = new Subject<GrainEvent>();
  private readonly ac = new AbortController();
  private source: EventSource | null = null;
  private retry: ReturnType<typeof setTimeout> | null = null;
  private backoff = 1000;
  private subscribers = 0;
  factory: EventSourceFactory = (url) =>
    new EventSource(url, { withCredentials: true });

  constructor() {
    if (typeof window !== "undefined") {
      const { signal } = this.ac;
      // Close the SSE stream on pagehide (navigation/BFCache) so HTTP/1.1
      // connections are not exhausted across reloads.
      window.addEventListener("pagehide", () => this.disconnect(), { signal });
      window.addEventListener(
        "pageshow",
        () => {
          if (this.subscribers > 0 && !this.source) this.connect();
        },
        { signal },
      );
    }
  }

  readonly url = `${routePath("GET /api/events")}?since=now&stream=1`;

  events(): Observable<GrainEvent> {
    return new Observable<GrainEvent>((observer) => {
      const sub = this.subject.subscribe(observer);
      this.subscribers++;
      if (!this.source) this.connect();
      return () => {
        sub.unsubscribe();
        this.subscribers--;
        if (this.subscribers === 0) this.disconnect();
      };
    });
  }

  private connect(): void {
    if (typeof EventSource === "undefined" && this.factory === undefined) {
      return;
    }
    this.zone.runOutsideAngular(() => {
      let src: EventSource;
      try {
        src = this.factory(this.url);
      } catch {
        this.scheduleReconnect();
        return;
      }
      this.source = src;
      const emit = (type: string) => (e: MessageEvent) => {
        let data: unknown = e.data;
        try {
          data = JSON.parse(e.data);
        } catch {
          // Not JSON: hand the raw text on.
        }
        this.backoff = 1000;
        this.zone.run(() => this.subject.next({ type, data }));
      };
      src.onmessage = emit("message");
      for (const t of GRAIN_EVENT_TYPES) {
        src.addEventListener(t, emit(t) as EventListener);
      }
      src.onerror = () => {
        if (src.readyState === 2 /* CLOSED */) {
          this.source = null;
          this.scheduleReconnect();
        }
      };
    });
  }

  private scheduleReconnect(): void {
    if (this.retry || this.subscribers === 0) return;
    this.retry = setTimeout(() => {
      this.retry = null;
      if (this.subscribers > 0 && !this.source) this.connect();
    }, this.backoff);
    this.backoff = Math.min(this.backoff * 2, 30000);
  }

  private disconnect(): void {
    if (this.retry) clearTimeout(this.retry);
    this.retry = null;
    this.backoff = 1000;
    this.source?.close();
    this.source = null;
  }

  ngOnDestroy(): void {
    this.ac.abort();
    this.disconnect();
    this.subject.complete();
  }
}
