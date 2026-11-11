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

import { Injectable, NgZone, inject, signal } from "@angular/core";
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents } from "../api/grain-events.service";
import type { TaskDetail } from "../api/models";
import { InboxStore } from "../inbox/inbox.store";
import { errorText } from "../sessions/session-view";

export const POLL_MS = 3000;
export const TRANSCRIPT_POLL_MS = 5000;
export const DETAIL_QUERY = "comments_limit=100&attempts_limit=20";

export interface TreeNode {
  id: string;
  title: string;
  status: string;
  pullRequest?: string;
  done: boolean;
  root?: boolean;
  children?: TreeNode[];
}

export interface WorkTree {
  origin?: string;
  rootTask?: string;
  done: number;
  total: number;
  nodes: TreeNode[];
}

// TaskStore is the task page's state: one task's detail (polled with
// ETags), its work tree, and the transcript of the attempt being read,
// polled while that attempt is still running.
@Injectable({ providedIn: "root" })
export class TaskStore {
  private readonly api = inject(GrainApi);
  private readonly events = inject(GrainEvents);
  private readonly zone = inject(NgZone);
  private readonly inbox = inject(InboxStore);

  readonly id = signal<string | null>(null);
  readonly detail = signal<TaskDetail | null>(null);
  readonly loadError = signal<string | null>(null);
  readonly actionError = signal<string | null>(null);
  readonly busy = signal<ReadonlySet<string>>(new Set());
  readonly tree = signal<WorkTree | null>(null);
  readonly treeError = signal<string | null>(null);
  readonly transcriptFor = signal<number | null>(null);
  readonly transcript = signal<string | null>(null);
  readonly transcriptError = signal<string | null>(null);
  readonly draft = signal("");

  private timer: ReturnType<typeof setInterval> | null = null;
  private transcriptTimer: ReturnType<typeof setInterval> | null = null;
  private eventSub: { unsubscribe(): void } | null = null;
  private eventTimer: ReturnType<typeof setTimeout> | null = null;

  open(id: string): void {
    if (this.id() !== id) {
      this.id.set(id);
      this.detail.set(null);
      this.tree.set(null);
      this.treeError.set(null);
      this.loadError.set(null);
      this.actionError.set(null);
      this.draft.set("");
      this.resetTranscript();
    }
    void this.refresh();
    this.eventSub ??= this.events.events().subscribe(() => {
      if (this.eventTimer) return;
      this.eventTimer = setTimeout(() => {
        this.eventTimer = null;
        void this.refresh();
      }, 250);
    });
    if (!this.timer) {
      this.zone.runOutsideAngular(() => {
        this.timer = setInterval(
          () => this.zone.run(() => void this.refresh()),
          POLL_MS,
        );
      });
    }
  }

  close(): void {
    if (this.timer) clearInterval(this.timer);
    this.timer = null;
    this.eventSub?.unsubscribe();
    this.eventSub = null;
    this.resetTranscript();
  }

  // resetTranscript forgets which run was being read, so another task (or
  // a later visit to Runs) starts on its own newest run.
  resetTranscript(): void {
    this.stopTranscript();
    this.transcriptFor.set(null);
    this.transcript.set(null);
    this.transcriptError.set(null);
  }

  private seq = 0;
  private applied = 0;

  async refresh(): Promise<void> {
    const id = this.id();
    if (!id) return;
    // Reads can overlap (the poll and an event); an older answer must not
    // overwrite a newer one.
    const mine = ++this.seq;
    try {
      const r = await this.api.getIfChanged<TaskDetail>(
        "GET /api/tasks/{id}",
        { id },
        DETAIL_QUERY,
      );
      // A navigation to another task while the read was out wins.
      if (this.id() !== id || mine < this.applied) return;
      this.applied = mine;
      if (r.changed || !this.detail()) this.detail.set(r.body);
      this.loadError.set(null);
    } catch (err) {
      if (this.id() !== id) return;
      const status = (err as { status?: number })?.status;
      this.loadError.set(
        status === 404
          ? `There is no task #${id}.`
          : `Task #${id} could not be read: ${errorText(err)}. This page tries again on its own.`,
      );
    }
  }

  async loadTree(): Promise<void> {
    const id = this.id();
    if (!id) return;
    try {
      const t = await this.api.get<WorkTree>("GET /api/tasks/{id}/tree", {
        id,
      });
      if (this.id() === id) {
        this.tree.set(t);
        this.treeError.set(null);
      }
    } catch (err) {
      if (this.id() === id) {
        this.treeError.set(
          `The work tree could not be read: ${errorText(err)}`,
        );
      }
    }
  }

  // watchTranscript reads one attempt's transcript, again every few
  // seconds while that attempt has not finished.
  watchTranscript(attempt: number, live: boolean): void {
    this.stopTranscript();
    this.transcriptFor.set(attempt);
    this.transcript.set(null);
    void this.loadTranscript(attempt);
    if (live) {
      this.zone.runOutsideAngular(() => {
        this.transcriptTimer = setInterval(
          () => this.zone.run(() => void this.loadTranscript(attempt)),
          TRANSCRIPT_POLL_MS,
        );
      });
    }
  }

  stopTranscript(): void {
    if (this.transcriptTimer) clearInterval(this.transcriptTimer);
    this.transcriptTimer = null;
  }

  private async loadTranscript(attempt: number): Promise<void> {
    const id = this.id();
    if (!id) return;
    try {
      const r = await this.api.get<{ transcript?: string }>(
        "GET /api/tasks/{id}/attempts/{number}/transcript",
        { id, number: String(attempt) },
      );
      if (this.id() === id && this.transcriptFor() === attempt) {
        this.transcript.set(r?.transcript ?? "");
        this.transcriptError.set(null);
      }
    } catch (err) {
      if (this.id() === id && this.transcriptFor() === attempt) {
        this.transcriptError.set(
          `The transcript could not be read: ${errorText(err)}`,
        );
      }
    }
  }

  isBusy(key: string, id = this.id()): boolean {
    return !!id && this.busy().has(`${id}:${key}`);
  }

  // act makes one call on this task and reads it again; the caller says
  // what happened, or the error shows on the page.
  // The id is fixed when the action starts: an Undo pressed after
  // navigating to another task still acts on this one.
  async act(
    key: string,
    call: (id: string) => Promise<unknown>,
    id: string | null = this.id(),
  ): Promise<boolean> {
    if (!id || this.isBusy(key, id)) return false;
    const busyKey = `${id}:${key}`;
    this.mark(busyKey, true);
    this.actionError.set(null);
    try {
      await call(id);
      return true;
    } catch (err) {
      this.actionError.set(errorText(err));
      return false;
    } finally {
      await this.refresh();
      void this.inbox.refresh();
      this.mark(busyKey, false);
    }
  }

  // send verifies the task live before posting (as a reply when the task
  // asked), keeping anything typed if verification or posting fails.
  async send(
    expectedId: string | null = this.id(),
    refuse?: (d: TaskDetail | undefined) => string,
  ): Promise<boolean> {
    const typed = this.draft();
    const body = typed.trim();
    if (!body || !expectedId || this.id() !== expectedId) return false;
    return this.act(
      "send",
      async (id) => {
        let fresh: TaskDetail | undefined;
        try {
          const r = await this.api.getIfChanged<TaskDetail>(
            "GET /api/tasks/{id}",
            { id },
            DETAIL_QUERY,
          );
          fresh = r.changed
            ? r.body
            : (r.body ??
              (this.detail()?.id === id
                ? (this.detail() ?? undefined)
                : undefined));
        } catch {
          throw new Error(`Could not verify #${id} before sending.`);
        }
        if (fresh && this.id() === id) this.detail.set(fresh);
        if (this.id() !== expectedId) {
          throw new Error(`Moved away from #${expectedId} while sending.`);
        }
        const why = refuse?.(fresh);
        if (why) throw new Error(why);
        await this.api.post(
          "POST /api/tasks/{id}/comments",
          { id },
          { body, attachments: [] },
        );
        if (this.draft() === typed) this.draft.set("");
      },
      expectedId,
    );
  }

  private mark(key: string, on: boolean): void {
    this.busy.update((b) => {
      const next = new Set(b);
      if (on) next.add(key);
      else next.delete(key);
      return next;
    });
  }
}
