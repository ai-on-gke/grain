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
import type {
  Attempt,
  AttachmentUpload,
  CapabilityAuditEntry,
  Comment,
  TaskDetail,
  TreeNode,
  WorkTree,
} from "../api/models";
import { InboxStore } from "../inbox/inbox.store";
import { errorText } from "../sessions/session-helpers";
import { ReadTrouble } from "../services/request-trouble";

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

// TaskStore is the task page's state: one task's detail (polled with
// ETags), its work tree, paged older comments/runs, capability audits,
// and the transcript of the attempt being read.
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

  private timer: ReturnType<typeof setInterval> | null = null;
  // trouble is the detail poll's run of failed reads: silent until it is
  // sustained, and the poll backs off (pollAfter) while it lasts
  // (request-trouble.ts).
  private readonly trouble = new ReadTrouble();
  private pollAfter = 0;
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
      this.stagedAttachments.set([]);
      this.pagedComments = [];
      this.pagedOlderComments = null;
      this.pagedAttempts = [];
      this.pagedOlderAttempts = null;
      this.resetTranscript();
      this.resetAudit();
      this.trouble.ok();
      this.pollAfter = 0;
    }
    void this.refresh();
    this.eventSub ??= this.events.events().subscribe(() => {
      if (this.eventTimer || this.trouble.failing) return;
      this.eventTimer = setTimeout(() => {
        this.eventTimer = null;
        void this.refresh();
      }, 250);
    });
    if (!this.timer) {
      this.zone.runOutsideAngular(() => {
        this.timer = setInterval(() => {
          if (Date.now() < this.pollAfter) return;
          this.zone.run(() => void this.refresh());
        }, POLL_MS);
      });
    }
  }

  close(expectedId?: string): void {
    if (expectedId !== undefined && this.id() !== expectedId) return;
    if (this.timer) clearInterval(this.timer);
    this.timer = null;
    this.eventSub?.unsubscribe();
    this.eventSub = null;
    this.resetTranscript();
    this.resetAudit();
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
      this.loadError.set(null);
      this.trouble.ok();
      this.pollAfter = 0;
    } catch (err) {
      if (this.id() !== id) return;
      const status = (err as { status?: number })?.status;
      const sustained = this.trouble.fail(err);
      this.pollAfter = Date.now() + this.trouble.nextDelay(POLL_MS) - POLL_MS;
      // One failed poll keeps the detail already on screen and says
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

  // Both page-back readers (Load earlier, a #comment= link's walk) share
  // one in-flight slot, so a second one waits for the first rather than
  // being turned away and read as "that comment is not here".
  private olderCommentsInFlight: { id: string; promise: Promise<void> } | null =
    null;

  async loadOlderComments(): Promise<void> {
    const id = this.id();
    const cur = this.detail();
    const first = cur?.comments?.[0];
    if (!id || !cur || !first || this.isBusy("older-comments", id)) return;
    const busyKey = `${id}:older-comments`;
    this.mark(busyKey, true);
    const slot = { id, promise: Promise.resolve() };
    this.olderCommentsInFlight = slot;
    slot.promise = (async () => {
      try {
        const page = await this.api.get<TaskDetail>(
          "GET /api/tasks/{id}",
          { id },
          `comments_limit=50&comments_before=${encodeURIComponent(String(first.id))}&attempts_limit=1`,
        );
        if (this.id() !== id) return;
        this.keepPage(page.comments ?? [], page.olderComments ?? 0);
      } catch (err) {
        if (this.id() === id) this.actionError.set(errorText(err));
      } finally {
        if (this.olderCommentsInFlight === slot) {
          this.olderCommentsInFlight = null;
        }
        this.mark(busyKey, false);
      }
    })();
    await slot.promise;
  }

  // keepPage folds a page of earlier comments into the paged history
  // together with every comment drawn now. The poll only ever returns the
  // newest page, so without the drawn ones a poll after new comments
  // arrive slides that page forward and the comments at the seam between
  // it and the paged history drop out of the thread.
  private keepPage(page: Comment[], older: number): void {
    const latest = this.detail();
    this.pagedComments = mergeComments(
      page,
      mergeComments(this.pagedComments, latest?.comments ?? []),
    );
    this.pagedOlderComments = older;
    if (latest) this.detail.set(this.applyPagedMerge(latest));
  }

  // loadCommentsThrough pages back through the conversation, up to 500
  // comments a request, until it holds comment `through` or reaches the
  // first comment, and merges every page at once, so a #comment= link to
  // an early comment of a long thread draws once rather than per page.
  // It answers "found", "missing" (the conversation has no such comment)
  // or "aborted" (the task changed or a read failed, so nothing is known).
  async loadCommentsThrough(
    through: number,
  ): Promise<"found" | "missing" | "aborted"> {
    const id = this.id();
    const has = () =>
      (this.detail()?.comments ?? []).some((c) => c.id === through);
    if (!id || !this.detail()) return "aborted";
    if (has()) return "found";
    while (this.olderCommentsInFlight?.id === id) {
      await this.olderCommentsInFlight.promise;
      if (this.id() !== id || !this.detail()) return "aborted";
      if (has()) return "found";
    }
    if ((this.detail()?.olderComments ?? 0) <= 0) return "missing";
    const busyKey = `${id}:older-comments`;
    this.mark(busyKey, true);
    let outcome: "found" | "missing" | "aborted" = "missing";
    const slot = { id, promise: Promise.resolve() };
    this.olderCommentsInFlight = slot;
    slot.promise = (async () => {
      let pages: Comment[] = [];
      let older: number | null = null;
      let first = this.detail()?.comments?.[0]?.id;
      try {
        while (first !== undefined && first > through) {
          const page = await this.api.get<TaskDetail>(
            "GET /api/tasks/{id}",
            { id },
            `comments_limit=500&comments_before=${encodeURIComponent(String(first))}&attempts_limit=1`,
          );
          if (this.id() !== id) {
            outcome = "aborted";
            return;
          }
          const got = page.comments ?? [];
          // A page that does not reach further back ends the walk.
          if (!got.length || got[0].id >= first) break;
          pages = mergeComments(got, pages);
          older = page.olderComments ?? 0;
          first = got[0].id;
          if (!older) break;
        }
        if (pages.length) this.keepPage(pages, older ?? 0);
        outcome = has() ? "found" : "missing";
      } catch (err) {
        if (this.id() === id) this.actionError.set(errorText(err));
        outcome = "aborted";
      } finally {
        if (this.olderCommentsInFlight === slot) {
          this.olderCommentsInFlight = null;
        }
        this.mark(busyKey, false);
      }
    })();
    await slot.promise;
    return outcome;
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
