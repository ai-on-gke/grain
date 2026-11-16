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
  effect,
  inject,
  signal,
  untracked,
} from "@angular/core";
import { GrainApi } from "../api/grain-api.service";
import type {
  Attempt,
  AttachmentUpload,
  CapabilityAuditEntry,
  Comment,
  Task,
  TaskDetail,
  TreeNode,
  WorkTree,
} from "../api/models";
import { InboxStore } from "../inbox/inbox.store";
import { errorText } from "../sessions/session-helpers";
import {
  LiveRefreshService,
  type TranscriptPollerHandle,
  eventTaskId,
} from "../services/live-refresh.service";
import { ReadTrouble } from "../services/request-trouble";
import { TaskFeedStore } from "../services/task-feed.service";

export type { TreeNode, WorkTree };

export const POLL_MS = 3000;
export const TRANSCRIPT_POLL_MS = 5000;
export const DETAIL_QUERY = "comments_limit=100&attempts_limit=20";

function mergeComments(older: Comment[], latest: Comment[]): Comment[] {
  if (!older.length) return latest;
  const byId = new Map<number, Comment>();
  for (const c of older) byId.set(c.id, c);
  for (const c of latest) byId.set(c.id, c);
  return [...byId.values()].sort((a, b) => a.id - b.id);
}

function mergeAttempts(older: Attempt[], latest: Attempt[]): Attempt[] {
  if (!older.length) return latest;
  const byNum = new Map<number, Attempt>();
  for (const a of older) byNum.set(a.number, a);
  for (const a of latest) byNum.set(a.number, a);
  return [...byNum.values()].sort((a, b) => a.number - b.number);
}

function taskRevisionKey(t: Task | TaskDetail | null | undefined): string {
  if (!t) return "";
  return `${t.updatedAt ?? ""}:${t.state ?? ""}:${t.activityAt ?? ""}`;
}

// TaskStore is the task page's state: one task's detail (refreshed on SSE
// events or list revision changes with ETags), its work tree, paged older
// comments/runs, capability audits, and the transcript of the attempt being read.
@Injectable({ providedIn: "root" })
export class TaskStore implements OnDestroy {
  private readonly api = inject(GrainApi);
  private readonly liveRefresh = inject(LiveRefreshService);
  private readonly feed = inject(TaskFeedStore);
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
  readonly auditFor = signal<number | null>(null);
  readonly auditRecords = signal<CapabilityAuditEntry[] | null>(null);
  readonly auditError = signal<string | null>(null);
  readonly draft = signal("");
  readonly stagedAttachments = signal<AttachmentUpload[]>([]);

  // Paged history preserved across live refreshes for the current task.
  private pagedComments: Comment[] = [];
  private pagedOlderComments: number | null = null;
  private pagedAttempts: Attempt[] = [];
  private pagedOlderAttempts: number | null = null;

  private isOpen = false;
  private lastFeedRevKey = "";
  private lastVisiblePulse = 0;
  // trouble is the detail read's run of failed reads: silent until it is
  // sustained, and retries back off while it lasts (request-trouble.ts).
  private readonly trouble = new ReadTrouble();
  private retryTimer: ReturnType<typeof setTimeout> | null = null;
  private unsubEvents: (() => void) | null = null;

  private readonly transcriptPoller: TranscriptPollerHandle =
    this.liveRefresh.createTranscriptPoller({
      fetch: async (taskId, attempt) => {
        const r = await this.api.get<{ transcript?: string }>(
          "GET /api/tasks/{id}/attempts/{number}/transcript",
          { id: taskId, number: String(attempt) },
        );
        return r?.transcript ?? "";
      },
      onText: (text) => {
        this.transcript.set(text);
        this.transcriptError.set(null);
      },
      onError: (err) => {
        this.transcriptError.set(
          `The transcript could not be read: ${errorText(err)}`,
        );
      },
      isActive: () => this.isOpen && this.transcriptFor() !== null,
    });

  constructor() {
    effect(() => {
      const list = this.feed.tasks();
      const id = this.id();
      untracked(() => {
        if (!this.isOpen || !id || this.trouble.failing) return;
        const row = list.find((t) => t.id === id);
        if (!row) return;
        const nextRev = taskRevisionKey(row);
        if (this.lastFeedRevKey && nextRev !== this.lastFeedRevKey) {
          this.lastFeedRevKey = nextRev;
          void this.refresh();
        } else if (!this.lastFeedRevKey) {
          this.lastFeedRevKey = nextRev;
        }
      });
    });
    effect(() => {
      const vp = this.liveRefresh.visiblePulse();
      untracked(() => {
        if (vp === this.lastVisiblePulse) return;
        this.lastVisiblePulse = vp;
        if (this.isOpen && this.id() && this.trouble.failing) {
          void this.refresh();
        }
      });
    });
  }

  open(id: string): void {
    const changedId = this.id() !== id;
    if (changedId) {
      this.id.set(id);
      this.detail.set(null);
      this.tree.set(null);
      this.treeError.set(null);
      this.loadError.set(null);
      this.actionError.set(null);
      this.draft.set("");
      this.stagedAttachments.set([]);
      this.pagedComments = [];
      this.pagedOlderComments = null;
      this.pagedAttempts = [];
      this.pagedOlderAttempts = null;
      this.resetTranscript();
      this.resetAudit();
      this.trouble.ok();
      this.clearRetryTimer();
      this.lastFeedRevKey = "";
    }
    this.isOpen = true;
    this.lastVisiblePulse = this.liveRefresh.visiblePulse();
    const row = this.feed.tasks().find((t) => t.id === id);
    const feedRev = taskRevisionKey(row);
    const cur = this.detail();
    if (
      changedId ||
      !cur ||
      cur.id !== id ||
      (feedRev && feedRev !== this.lastFeedRevKey)
    ) {
      this.lastFeedRevKey = feedRev;
      void this.refresh();
    }
    this.unsubEvents ??= this.liveRefresh.subscribeEvents(
      (ev) => {
        if (this.trouble.failing) return false;
        if (ev.type && !ev.type.startsWith("task.")) return false;
        const evId = eventTaskId(ev);
        return !evId || evId === this.id();
      },
      () => {
        if (!this.trouble.failing) void this.refresh();
      },
      250,
    );
  }

  close(expectedId?: string): void {
    if (expectedId !== undefined && this.id() !== expectedId) return;
    this.isOpen = false;
    this.clearRetryTimer();
    this.unsubEvents?.();
    this.unsubEvents = null;
    this.resetTranscript();
    this.resetAudit();
  }

  ngOnDestroy(): void {
    this.close();
  }

  private clearRetryTimer(): void {
    if (this.retryTimer) {
      clearTimeout(this.retryTimer);
      this.retryTimer = null;
    }
  }

  private scheduleRetry(): void {
    this.clearRetryTimer();
    if (!this.isOpen || !this.id() || this.liveRefresh.hidden()) return;
    const backoffMs =
      typeof this.api.backoffRemainingMs === "function"
        ? this.api.backoffRemainingMs()
        : 0;
    const delay = Math.max(this.trouble.nextDelay(POLL_MS), backoffMs);
    this.zone.runOutsideAngular(() => {
      this.retryTimer = setTimeout(() => {
        this.retryTimer = null;
        if (this.isOpen && !this.liveRefresh.hidden()) {
          this.zone.run(() => void this.refresh());
        }
      }, delay);
    });
  }

  // resetTranscript forgets which run was being read, so another task (or
  // a later visit to Runs) starts on its own newest run.
  resetTranscript(): void {
    this.stopTranscript();
    this.transcriptFor.set(null);
    this.transcript.set(null);
    this.transcriptError.set(null);
  }

  resetAudit(): void {
    this.auditFor.set(null);
    this.auditRecords.set(null);
    this.auditError.set(null);
  }

  private seq = 0;
  private applied = 0;

  private applyPagedMerge(fresh: TaskDetail): TaskDetail {
    const hasPagedComments = this.pagedComments.length > 0;
    const hasPagedAttempts = this.pagedAttempts.length > 0;
    if (!hasPagedComments && !hasPagedAttempts) return fresh;
    return {
      ...fresh,
      ...(hasPagedComments
        ? {
            comments: mergeComments(this.pagedComments, fresh.comments ?? []),
            olderComments: this.pagedOlderComments ?? fresh.olderComments ?? 0,
          }
        : {}),
      ...(hasPagedAttempts
        ? {
            attempts: mergeAttempts(this.pagedAttempts, fresh.attempts ?? []),
            olderAttempts: this.pagedOlderAttempts ?? fresh.olderAttempts ?? 0,
          }
        : {}),
    };
  }

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
      if (r.changed || !this.detail()) {
        this.detail.set(this.applyPagedMerge(r.body));
      }
      const feedRow = this.feed.tasks().find((t) => t.id === id);
      this.lastFeedRevKey = taskRevisionKey(feedRow ?? r.body);
      this.loadError.set(null);
      this.trouble.ok();
      this.clearRetryTimer();
    } catch (err) {
      if (this.id() !== id) return;
      const status = (err as { status?: number })?.status;
      const sustained = this.trouble.fail(err);
      if (status !== 404) {
        this.scheduleRetry();
      }
      // One failed read keeps the detail already on screen and says
      // nothing; a missing task, a page with nothing to show yet, or
      // failure that lasts says so.
      if (status !== 404 && !sustained && this.detail()) return;
      this.loadError.set(
        status === 404
          ? `There is no task #${id}.`
          : `Task #${id} could not be read: ${errorText(err)}. This page tries again on its own.`,
      );
    }
  }

  async loadOlderComments(): Promise<void> {
    const id = this.id();
    const cur = this.detail();
    const first = cur?.comments?.[0];
    if (!id || !cur || !first || this.isBusy("older-comments", id)) return;
    const busyKey = `${id}:older-comments`;
    this.mark(busyKey, true);
    try {
      const page = await this.api.get<TaskDetail>(
        "GET /api/tasks/{id}",
        { id },
        `comments_limit=50&comments_before=${encodeURIComponent(String(first.id))}&attempts_limit=1`,
      );
      if (this.id() !== id) return;
      this.pagedComments = mergeComments(
        page.comments ?? [],
        this.pagedComments,
      );
      this.pagedOlderComments = page.olderComments ?? 0;
      const latest = this.detail();
      if (latest) {
        this.detail.set(this.applyPagedMerge(latest));
      }
    } catch (err) {
      if (this.id() === id) this.actionError.set(errorText(err));
    } finally {
      this.mark(busyKey, false);
    }
  }

  async loadOlderAttempts(): Promise<void> {
    const id = this.id();
    const cur = this.detail();
    const sorted = [...(cur?.attempts ?? [])].sort(
      (a, b) => a.number - b.number,
    );
    const oldest = sorted[0];
    if (!id || !cur || !oldest || this.isBusy("older-attempts", id)) return;
    const busyKey = `${id}:older-attempts`;
    this.mark(busyKey, true);
    try {
      const page = await this.api.get<TaskDetail>(
        "GET /api/tasks/{id}",
        { id },
        `comments_limit=1&attempts_limit=20&attempts_before=${encodeURIComponent(String(oldest.number))}`,
      );
      if (this.id() !== id) return;
      this.pagedAttempts = mergeAttempts(
        page.attempts ?? [],
        this.pagedAttempts,
      );
      this.pagedOlderAttempts = page.olderAttempts ?? 0;
      const latest = this.detail();
      if (latest) {
        this.detail.set(this.applyPagedMerge(latest));
      }
    } catch (err) {
      if (this.id() === id) this.actionError.set(errorText(err));
    } finally {
      this.mark(busyKey, false);
    }
  }

  async loadCapabilityAudit(attempt: number): Promise<void> {
    const id = this.id();
    if (!id) return;
    if (this.auditFor() === attempt && this.auditRecords() !== null) {
      this.resetAudit();
      return;
    }
    this.auditFor.set(attempt);
    this.auditRecords.set(null);
    this.auditError.set(null);
    try {
      const r = await this.api.get<{ records?: CapabilityAuditEntry[] }>(
        "GET /api/tasks/{id}/attempts/{number}/capability-audit",
        { id, number: String(attempt) },
      );
      if (this.id() === id && this.auditFor() === attempt) {
        this.auditRecords.set(r?.records ?? []);
      }
    } catch (err) {
      if (this.id() === id && this.auditFor() === attempt) {
        this.auditError.set(
          `Capability audit could not be read: ${errorText(err)}`,
        );
      }
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

  // watchTranscript reads one attempt's transcript, backing off when
  // unchanged and pausing while hidden.
  watchTranscript(attempt: number, live: boolean): void {
    const id = this.id();
    if (!id) return;
    if (this.transcriptFor() !== attempt) {
      this.transcript.set(null);
      this.transcriptError.set(null);
    }
    this.transcriptFor.set(attempt);
    this.transcriptPoller.watch(id, attempt, live);
  }

  stopTranscript(): void {
    this.transcriptPoller.stop();
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
  // asked), keeping anything typed and any staged attachments on failure.
  async send(
    expectedId: string | null = this.id(),
    refuse?: (d: TaskDetail | undefined) => string,
  ): Promise<boolean> {
    const typed = this.draft();
    const body = typed.trim();
    const attachments = this.stagedAttachments();
    if (
      (!body && attachments.length === 0) ||
      !expectedId ||
      this.id() !== expectedId
    ) {
      return false;
    }
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
        if (fresh && this.id() === id) {
          this.detail.set(this.applyPagedMerge(fresh));
        }
        if (this.id() !== expectedId) {
          throw new Error(`Moved away from #${expectedId} while sending.`);
        }
        const why = refuse?.(fresh);
        if (why) throw new Error(why);
        await this.api.post(
          "POST /api/tasks/{id}/comments",
          { id },
          { body, attachments },
        );
        if (this.draft() === typed) this.draft.set("");
        if (this.stagedAttachments() === attachments) {
          this.stagedAttachments.set([]);
        }
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
