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
  ChangeDetectionStrategy,
  Component,
  DestroyRef,
  NgZone,
  ViewEncapsulation,
  computed,
  effect,
  inject,
  input,
  signal,
  untracked,
} from "@angular/core";
import { RouterLink } from "@angular/router";
import { GrainApi } from "../api/grain-api.service";
import type { MergeBatch, MergeBatchesResponse } from "../api/models";
import { AccessService } from "../services/access.service";
import { errText } from "../settings/settings-state.service";
import { StatusIconComponent } from "./status-icon.component";

export const MERGE_BATCH_POLL_MS = 5_000;

export interface BatchStanding {
  label: "Testing" | "Passed" | "Failed" | "Abandoned";
  tone: "setup" | "healthy" | "error" | "blast";
  kind: "running" | "passed" | "failed" | "warning";
  title: string;
}

// batchAge formats how long a merge re-check has been holding its members
// against the bound past which the queue gives up on it (ui.MergeBatches.
// MaxAgeSeconds, model.MaxMergeBatchAge). Mirrors classic MergeBatchPanel.jsx.
export function batchAge(
  batch: Pick<MergeBatch, "startedAt" | "finishedAt" | "running">,
  maxAgeSeconds?: number,
  now = Date.now(),
): string {
  if (!batch?.startedAt) return "";
  const started = new Date(batch.startedAt).getTime();
  if (!Number.isFinite(started)) return "";
  const until = batch.finishedAt ? new Date(batch.finishedAt).getTime() : now;
  const minutes = Math.max(0, Math.round((until - started) / 60_000));
  const limit = maxAgeSeconds ? Math.round(maxAgeSeconds / 60) : 0;
  if (!batch.running) return `ran for ${minutes}m`;
  return limit ? `${minutes}m of ${limit}m` : `${minutes}m`;
}

// batchStanding turns a batch's live/concluded state into the standing chip
// label, CVD-safe tone/kind, and hover explanation. Only "failure" blames the
// merged tree; errored/cancelled/timed-out runs are "Abandoned".
export function batchStanding(
  batch: Pick<MergeBatch, "running" | "conclusion">,
): BatchStanding {
  if (batch.running) {
    return {
      label: "Testing",
      tone: "setup",
      kind: "running",
      title:
        "One grain CI run is judging the base branch with this change merged into it. It merges if the run passes; nothing has landed yet.",
    };
  }
  if (batch.conclusion === "success") {
    return {
      label: "Passed",
      tone: "healthy",
      kind: "passed",
      title: "The re-check was green, so the change merges.",
    };
  }
  if (batch.conclusion === "failure") {
    return {
      label: "Failed",
      tone: "error",
      kind: "failed",
      title:
        "The change broke grain CI when merged on top of the base branch. It goes back to its own agent for repair and the rest of the queue carries on.",
    };
  }
  return {
    label: "Abandoned",
    tone: "blast",
    kind: "warning",
    title:
      "The run said nothing about the tree -- it errored, was cancelled, was lost, or took longer than the queue waits. No failure can be read out of that, so the re-check was discarded.",
  };
}

// mergeQueueBatching reports whether a task is in an overlap re-check on the
// merge queue's throwaway ref (orchestrator's setRecheckState). Decides
// whether the task page mounts MergeBatchPanel so ordinary tasks do not poll
// GET /api/merge-batches.
export function mergeQueueBatching(
  t:
    | {
        state?: string;
        mergeQueueState?: string;
        mergeQueueNote?: string;
      }
    | null
    | undefined,
): boolean {
  if (!t || t.state === "closed") return false;
  if (t.mergeQueueState === "batching") return true;
  return (
    t.mergeQueueState === "waiting_ci" &&
    typeof t.mergeQueueNote === "string" &&
    t.mergeQueueNote.startsWith(
      "grain CI is re-checking this change on top of ",
    )
  );
}

// memberPrUrl builds the external git-host pull-request URL for a batch
// member. Returns null for grain/config (which has no external PR host).
export function memberPrUrl(repo: string, number?: number): string | null {
  if (!repo || !number || number <= 0) return null;
  if (repo.toLowerCase() === "grain/config") return null;
  const parts = repo.split("/").filter(Boolean);
  if (parts.length === 3) {
    return `https://${parts[0]}/${parts[1]}/${parts[2]}/pull/${number}`;
  }
  if (parts.length === 2) {
    return `https://github.com/${parts[0]}/${parts[1]}/pull/${number}`;
  }
  return null;
}

// MergeBatchPanelComponent is the /next/ port of classic MergeBatchPanel.jsx
// (GET /api/merge-batches). Mounted in two places:
//   - On a repository's Merge Queue tab (no taskId): always shown so "no
//     re-check is running" is an explicit answer.
//   - On a task page (with taskId): shown only when that task belongs to a
//     batch in flight, highlighting the task's own row.
@Component({
  selector: "g-merge-batch-panel",
  imports: [RouterLink, StatusIconComponent],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  styles: `
    .merge-recheck-task-section {
      display: flex;
      flex-direction: column;
      gap: 8px;
      margin: 0 0 12px;
    }

    .merge-recheck-overline {
      margin: 0;
      font-size: 11px;
      line-height: 16px;
      font-weight: 600;
      letter-spacing: 0.06em;
      text-transform: uppercase;
      color: var(--md-sys-color-on-surface-variant);
    }

    .merge-batch-list {
      display: flex;
      flex-direction: column;
      gap: 10px;
    }

    .merge-batch-card {
      display: flex;
      flex-direction: column;
      gap: 6px;
      padding: 12px 14px;
      border-radius: var(--md-sys-shape-corner-medium);
      border: 1px solid var(--md-sys-color-outline-variant);
      background: var(--md-sys-color-surface-container-low);
      min-width: 0;
    }

    .merge-batch-head {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: 8px;
    }

    .merge-batch-title {
      font-size: 14px;
      line-height: 20px;
      font-weight: 600;
      color: var(--md-sys-color-on-surface);
      overflow-wrap: anywhere;
    }

    .merge-batch-age {
      font-size: 12px;
      line-height: 16px;
      color: var(--md-sys-color-on-surface-variant);
    }

    .merge-batch-spacer {
      flex: 1 1 auto;
    }

    .merge-batch-run-link {
      font-size: 13px;
      font-weight: 500;
      color: var(--md-sys-color-primary);
      text-decoration: none;
      white-space: nowrap;
    }

    .merge-batch-run-link:hover {
      text-decoration: underline;
    }

    .merge-batch-meta {
      font-size: 12px;
      line-height: 18px;
      color: var(--md-sys-color-on-surface-variant);
      overflow-wrap: anywhere;
    }

    .merge-batch-members {
      margin: 2px 0 0;
      padding-left: 20px;
      display: flex;
      flex-direction: column;
      gap: 4px;
      font-size: 13px;
      line-height: 20px;
    }

    .merge-batch-member-row {
      display: flex;
      flex-wrap: wrap;
      align-items: baseline;
      gap: 6px;
      min-width: 0;
    }

    .merge-batch-members li.is-current {
      font-weight: 600;
    }

    .merge-batch-task-link {
      color: var(--md-sys-color-primary);
      text-decoration: none;
      overflow-wrap: anywhere;
    }

    .merge-batch-task-link:hover {
      text-decoration: underline;
    }

    .merge-batch-pr-link {
      font-size: 12px;
      color: var(--md-sys-color-on-surface-variant);
      text-decoration: none;
      font-family: var(--grain-font-mono);
    }

    .merge-batch-pr-link:hover {
      text-decoration: underline;
      color: var(--md-sys-color-on-surface);
    }
  `,
  template: `
    @if (taskId()) {
      @if (canRead() && visibleBatches().length > 0) {
        <section
          class="merge-recheck-task-section"
          aria-label="Merge re-check"
          data-testid="task-merge-recheck"
        >
          @if (pane()) {
            <h3 class="merge-recheck-overline">Merge re-check</h3>
          } @else {
            <h2 class="merge-recheck-overline">Merge re-check</h2>
          }
          <div class="merge-batch-list">
            @for (
              b of visibleBatches();
              track b.repo + ":" + b.branch + ":" + b.ref
            ) {
              @let standing = standingOf(b);
              <div class="merge-batch-card" data-testid="merge-batch-card">
                <div class="merge-batch-head">
                  <span class="merge-batch-title">
                    {{ b.members.length }}
                    {{ b.members.length === 1 ? "change" : "changes" }} on
                    {{ b.branch }}
                  </span>
                  <span
                    [class]="'status-pill ' + standing.tone"
                    [attr.title]="standing.title"
                    data-testid="merge-batch-standing"
                  >
                    <g-status-icon
                      [tone]="standing.tone"
                      [kind]="standing.kind"
                    />
                    {{ standing.label }}
                  </span>
                  @if (ageOf(b); as age) {
                    <span
                      class="merge-batch-age tabular"
                      data-testid="merge-batch-age"
                      >{{ age }}</span
                    >
                  }
                  <span class="merge-batch-spacer"></span>
                  @if (b.runId) {
                    <a
                      class="merge-batch-run-link"
                      [routerLink]="['/ci', 'runs', b.runId]"
                      data-testid="merge-batch-run-link"
                      >Show the run</a
                    >
                  }
                </div>
                <div
                  class="merge-batch-meta s-mono"
                  data-testid="merge-batch-ref"
                  [attr.title]="refTitle(b)"
                >
                  {{ b.ref }} at {{ shortSha(b.commit) }}
                </div>
                @if (b.detail && !b.running) {
                  <div
                    class="merge-batch-meta"
                    data-testid="merge-batch-detail"
                  >
                    {{ b.detail }}
                  </div>
                }
                <ol class="merge-batch-members" aria-label="Re-check changes">
                  @for (m of b.members; track m.task) {
                    <li [class.is-current]="m.task === taskId()">
                      <span class="merge-batch-member-row">
                        <a
                          class="merge-batch-task-link"
                          [routerLink]="['/task', m.task]"
                          data-testid="merge-batch-member-task"
                          >{{ m.title || m.task }}</a
                        >
                        @if (m.number && m.number > 0) {
                          @if (prHref(b.repo, m.number); as href) {
                            <a
                              class="merge-batch-pr-link"
                              [href]="href"
                              target="_blank"
                              rel="noopener noreferrer"
                              data-testid="merge-batch-member-pr"
                              [attr.title]="memberTitle(m)"
                              >#{{ m.number }}</a
                            >
                          } @else {
                            <span
                              class="merge-batch-pr-link"
                              data-testid="merge-batch-member-pr"
                              [attr.title]="memberTitle(m)"
                              >#{{ m.number }}</span
                            >
                          }
                        }
                      </span>
                    </li>
                  }
                </ol>
              </div>
            }
          </div>
        </section>
      }
    } @else {
      <div class="s-card" data-testid="card-repo-merge-rechecks">
        <div class="s-card-head">
          <div>
            <h2 class="s-card-title">Merge re-checks</h2>
            <p class="s-card-desc">
              Overlap re-checks currently running on the merge queue&rsquo;s
              throwaway ref before changes land.
            </p>
          </div>
        </div>

        @if (!canRead()) {
          <p class="s-hint" data-testid="merge-rechecks-denied">
            {{ denied() }}
          </p>
        } @else if (loadError()) {
          <p
            class="s-field-error"
            role="alert"
            data-testid="merge-rechecks-error"
          >
            Could not read merge re-checks: {{ loadError() }}
          </p>
        } @else if (data() === null) {
          <p class="s-hint" data-testid="merge-rechecks-loading">
            Loading merge re-checks…
          </p>
        } @else if (visibleBatches().length === 0) {
          <p class="s-hint" data-testid="merge-rechecks-empty">
            No re-check is running. When a finished change overlaps what has
            landed on its base branch since the commit its own run tested, grain
            merges it onto a throwaway ref and re-checks it before it lands.
            What appears here is that re-check while it is running.
          </p>
        } @else {
          <div class="merge-batch-list" data-testid="merge-rechecks-list">
            @for (
              b of visibleBatches();
              track b.repo + ":" + b.branch + ":" + b.ref
            ) {
              @let standing = standingOf(b);
              <div class="merge-batch-card" data-testid="merge-batch-card">
                <div class="merge-batch-head">
                  <span class="merge-batch-title">
                    {{ b.members.length }}
                    {{ b.members.length === 1 ? "change" : "changes" }} on
                    {{ b.branch }}
                  </span>
                  <span
                    [class]="'status-pill ' + standing.tone"
                    [attr.title]="standing.title"
                    data-testid="merge-batch-standing"
                  >
                    <g-status-icon
                      [tone]="standing.tone"
                      [kind]="standing.kind"
                    />
                    {{ standing.label }}
                  </span>
                  @if (ageOf(b); as age) {
                    <span
                      class="merge-batch-age tabular"
                      data-testid="merge-batch-age"
                      >{{ age }}</span
                    >
                  }
                  <span class="merge-batch-spacer"></span>
                  @if (b.runId) {
                    <a
                      class="merge-batch-run-link"
                      [routerLink]="['/ci', 'runs', b.runId]"
                      data-testid="merge-batch-run-link"
                      >Show the run</a
                    >
                  }
                </div>
                <div
                  class="merge-batch-meta s-mono"
                  data-testid="merge-batch-ref"
                  [attr.title]="refTitle(b)"
                >
                  {{ b.ref }} at {{ shortSha(b.commit) }}
                </div>
                @if (b.detail && !b.running) {
                  <div
                    class="merge-batch-meta"
                    data-testid="merge-batch-detail"
                  >
                    {{ b.detail }}
                  </div>
                }
                <ol class="merge-batch-members" aria-label="Re-check changes">
                  @for (m of b.members; track m.task) {
                    <li>
                      <span class="merge-batch-member-row">
                        <a
                          class="merge-batch-task-link"
                          [routerLink]="['/task', m.task]"
                          data-testid="merge-batch-member-task"
                          >{{ m.title || m.task }}</a
                        >
                        @if (m.number && m.number > 0) {
                          @if (prHref(b.repo, m.number); as href) {
                            <a
                              class="merge-batch-pr-link"
                              [href]="href"
                              target="_blank"
                              rel="noopener noreferrer"
                              data-testid="merge-batch-member-pr"
                              [attr.title]="memberTitle(m)"
                              >#{{ m.number }}</a
                            >
                          } @else {
                            <span
                              class="merge-batch-pr-link"
                              data-testid="merge-batch-member-pr"
                              [attr.title]="memberTitle(m)"
                              >#{{ m.number }}</span
                            >
                          }
                        }
                      </span>
                    </li>
                  }
                </ol>
              </div>
            }
          </div>
        }
      </div>
    }
  `,
})
export class MergeBatchPanelComponent {
  private readonly api = inject(GrainApi);
  private readonly access = inject(AccessService);
  private readonly zone = inject(NgZone);
  private readonly destroyRef = inject(DestroyRef);

  readonly repo = input<string>("");
  readonly taskId = input<string>("");
  readonly pane = input<boolean>(false);
  readonly pollMs = input<number>(MERGE_BATCH_POLL_MS);

  protected readonly data = signal<MergeBatchesResponse | null>(null);
  protected readonly loadError = signal<string | null>(null);
  protected readonly now = signal(Date.now());

  private inFlight = false;
  private reqSeq = 0;

  protected readonly canRead = computed(() => {
    // Re-evaluate once /api/config lands.
    this.access.rawConfig();
    const r = this.repo().trim();
    return r
      ? this.access.can("GET /api/merge-batches", r)
      : this.access.canGlobal("GET /api/merge-batches");
  });

  protected readonly denied = computed(() => {
    const r = this.repo().trim();
    return r
      ? this.access.deniedReason("GET /api/merge-batches", r)
      : this.access.globalDeniedReason("GET /api/merge-batches");
  });

  protected readonly visibleBatches = computed(() => {
    const all = this.data()?.batches ?? [];
    const tid = this.taskId().trim();
    if (!tid) return all;
    return all.filter((b) => (b.members ?? []).some((m) => m.task === tid));
  });

  constructor() {
    effect(() => {
      const r = this.repo().trim();
      const allowed = this.canRead();
      untracked(() => {
        this.reqSeq++;
        this.inFlight = false;
        this.data.set(null);
        this.loadError.set(null);
        if (allowed) {
          void this.refresh(r, this.reqSeq);
        }
      });
    });

    effect((onCleanup) => {
      const ms = this.pollMs();
      const r = this.repo().trim();
      const allowed = this.canRead();
      if (!allowed || ms <= 0) return;
      let timer: ReturnType<typeof setInterval> | null = null;
      this.zone.runOutsideAngular(() => {
        timer = setInterval(() => {
          if (this.inFlight) return;
          this.zone.run(() => void this.refresh(r, this.reqSeq));
        }, ms);
      });
      onCleanup(() => {
        if (timer) clearInterval(timer);
      });
    });

    this.destroyRef.onDestroy(() => {
      this.reqSeq++;
    });
  }

  protected standingOf(batch: MergeBatch): BatchStanding {
    return batchStanding(batch);
  }

  protected ageOf(batch: MergeBatch): string {
    return batchAge(batch, this.data()?.maxAgeSeconds, this.now());
  }

  protected shortSha(sha?: string): string {
    return sha ? sha.slice(0, 12) : "";
  }

  protected prHref(repo: string, number?: number): string | null {
    return memberPrUrl(repo, number);
  }

  protected refTitle(batch: MergeBatch): string {
    return `Built on ${batch.ref} from ${batch.branch} at ${this.shortSha(batch.baseCommit)}. Nothing merges from this ref -- it is where the re-check is run, and what lands is the change's own pull request.`;
  }

  protected memberTitle(m: { head?: string; headRef?: string }): string {
    return `Merged onto the re-check ref pinned to ${this.shortSha(m.head)}: a push landing on ${m.headRef || "its branch"} while this runs discards the re-check rather than merging a commit it never tested.`;
  }

  private async refresh(repo: string, seq: number): Promise<void> {
    if (this.inFlight) return;
    this.inFlight = true;
    try {
      const query = repo ? `repo=${encodeURIComponent(repo)}` : undefined;
      const res = await this.api.get<MergeBatchesResponse>(
        "GET /api/merge-batches",
        undefined,
        query,
      );
      if (seq !== this.reqSeq) return;
      this.data.set({
        batches: Array.isArray(res?.batches) ? res.batches : [],
        maxAgeSeconds: res?.maxAgeSeconds ?? 0,
      });
      this.loadError.set(null);
      this.now.set(Date.now());
    } catch (err) {
      if (seq !== this.reqSeq) return;
      this.loadError.set(errText(err));
    } finally {
      if (seq === this.reqSeq) {
        this.inFlight = false;
      }
    }
  }
}
