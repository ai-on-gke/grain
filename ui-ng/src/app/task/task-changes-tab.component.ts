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
  ChangeDetectionStrategy,
  Component,
  NgZone,
  type OnDestroy,
  ViewEncapsulation,
  computed,
  effect,
  inject,
  input,
  output,
  signal,
  untracked,
} from "@angular/core";
import { RouterLink } from "@angular/router";
import { GrainApi, splitRepo } from "../api/grain-api.service";
import type {
  CIRun,
  CIRunsResponse,
  CompareResponse,
  DiffPreview,
  DiffPreviewFile,
  MergeGate,
  TaskDetail,
} from "../api/models";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import { errorText } from "../sessions/session-view";
import { parseUnifiedDiff } from "../shared/diff-parse";
import { DiffViewerComponent } from "../shared/diff-viewer.component";
import { APPROVAL_GATE, submitBlock } from "../shared/gates";
import { TipDirective } from "../shared/tip.directive";
import { TaskStore } from "./task.store";

@Component({
  selector: "g-task-changes-tab",
  imports: [RouterLink, DiffViewerComponent, TipDirective],
  encapsulation: ViewEncapsulation.None,
  styleUrl: "./task-changes-tab.css",
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="changes-tab">
      <dl class="facts" data-testid="changes">
        <dt>Branch</dt>
        <dd class="mono">{{ task().branch || "—" }}</dd>
        @if (task().base) {
          <dt>Base</dt>
          <dd class="mono">{{ task().base }}</dd>
        }
        <dt>Pull request</dt>
        <dd>
          @if (prUrl(); as url) {
            <a [href]="url" target="_blank" rel="noopener noreferrer">{{
              prLabel()
            }}</a>
          } @else {
            None yet
          }
        </dd>
        <dt>Merge</dt>
        <dd>
          {{
            task().autoMerge
              ? "Merges on its own once CI and review pass"
              : "Waits for someone to submit it"
          }}
        </dd>
        @if (task().mergeQueueState) {
          <dt>Merge queue</dt>
          <dd>
            {{ task().mergeQueueState }}
            @if (task().mergeQueueNote) {
              <span class="muted">· {{ task().mergeQueueNote }}</span>
            }
          </dd>
        }
        @if (task().changelists?.length) {
          <dt>Changelists</dt>
          <dd class="mono">{{ task().changelists!.join(", ") }}</dd>
        }
      </dl>

      @if (task().mergeGates?.length) {
        <div
          class="gates-chip-row"
          role="group"
          aria-label="Merge gates"
          data-testid="merge-gates-list"
        >
          @for (g of task().mergeGates; track g.id) {
            <span
              [class]="'status-pill ' + gateTone(g)"
              [gTip]="g.detail || g.state"
            >
              {{ g.label || g.id }}: {{ g.state }}
            </span>
          }
        </div>
      }

      <!-- PR, Merge & Changelist Actions -->
      <div
        class="changes-toolbar"
        role="group"
        aria-label="Change and merge actions"
      >
        @if (
          task().branch &&
          !task().pullRequest &&
          can("POST /api/tasks/{id}/pull-request")
        ) {
          <button
            type="button"
            class="btn-tonal"
            data-testid="changes-open-pr"
            [disabled]="store.isBusy('open-pr', task().id)"
            (click)="openPr()"
          >
            Open pull request
          </button>
        }

        @if (
          (prUrl() || task().state === "awaiting_submit") &&
          can("POST /api/tasks/{id}/submit")
        ) {
          <button
            type="button"
            class="btn-primary"
            data-testid="changes-submit"
            [gTip]="
              submitOff() || 'Submit the pull request; review gates still apply'
            "
            [disabled]="store.isBusy('submit', task().id) || !!submitOff()"
            (click)="submitPr()"
          >
            Submit
          </button>
        }

        @if (
          (prUrl() || task().mergeQueueBlockedAt || task().mergeQueueState) &&
          can("POST /api/tasks/{id}/retry-merge")
        ) {
          <button
            type="button"
            class="btn-outlined"
            data-testid="changes-retry-merge"
            [disabled]="store.isBusy('retry-merge', task().id)"
            (click)="retryMerge()"
          >
            Retry merge
          </button>
        }

        @if (
          (prUrl() || task().state === "awaiting_submit") &&
          can("POST /api/tasks/{id}/force-merge")
        ) {
          <button
            type="button"
            class="btn-outlined danger"
            data-testid="changes-force-merge-open"
            [disabled]="store.isBusy('force-merge', task().id)"
            (click)="emitForceMerge()"
          >
            Force merge…
          </button>
        }

        @if (
          hasChangelist() &&
          task().clSubmit?.canSync &&
          can("POST /api/tasks/{id}/change-sync")
        ) {
          <button
            type="button"
            class="btn-outlined"
            data-testid="changes-cl-sync"
            [disabled]="store.isBusy('cl-sync', task().id)"
            (click)="clAction('sync', 'POST /api/tasks/{id}/change-sync')"
          >
            Sync change
          </button>
        }
        @if (
          hasChangelist() &&
          task().clReview?.canPublish &&
          can("POST /api/tasks/{id}/change-publish")
        ) {
          <button
            type="button"
            class="btn-outlined"
            data-testid="changes-cl-publish"
            [disabled]="store.isBusy('cl-publish', task().id)"
            (click)="clAction('publish', 'POST /api/tasks/{id}/change-publish')"
          >
            Publish replies
          </button>
        }
        @if (
          hasChangelist() &&
          task().clReview?.canMail &&
          can("POST /api/tasks/{id}/change-mail")
        ) {
          <button
            type="button"
            class="btn-outlined"
            data-testid="changes-cl-mail"
            [disabled]="store.isBusy('cl-mail', task().id)"
            (click)="clAction('mail', 'POST /api/tasks/{id}/change-mail')"
          >
            Mail change
          </button>
        }
      </div>

      <!-- CI Runs & Step Breakdown -->
      <section
        class="ci-runs-card"
        aria-labelledby="task-ci-h"
        data-testid="task-ci-section"
      >
        <div class="ci-card-head">
          <h3 id="task-ci-h" class="section-title">CI checks &amp; runs</h3>
          <div class="changes-toolbar">
            @if (can("POST /api/ci/runs") && (task().repo || task().branch)) {
              <button
                type="button"
                class="btn-tonal"
                data-testid="ci-rerun-btn"
                [disabled]="store.isBusy('ci-rerun', task().id)"
                (click)="rerunCi(false)"
              >
                Re-run CI
              </button>
              @if (hasFailedCi()) {
                <button
                  type="button"
                  class="btn-outlined"
                  data-testid="ci-rerun-failed-btn"
                  [disabled]="store.isBusy('ci-rerun-failed', task().id)"
                  (click)="rerunCi(true)"
                >
                  Re-run failed jobs
                </button>
              }
            }
          </div>
        </div>

        @if (task().ciFaults?.length) {
          @for (flt of task().ciFaults; track $index) {
            <p class="banner banner-error" role="status">
              <strong>Shared CI fault ({{ flt.kind || "infra" }}):</strong>
              {{ flt.summary }}
              @if (flt.detail) {
                <span>— {{ flt.detail }}</span>
              }
            </p>
          }
        }

        @if (ciError(); as err) {
          <p class="banner banner-error" role="alert">{{ err }}</p>
        } @else if (ciLoading()) {
          <p class="muted" style="margin: 0">Reading CI runs…</p>
        } @else if (ciRuns().length === 0) {
          @if (task().ci; as obsCi) {
            <div class="ci-run-summary">
              <span [class]="'status-pill ' + ciTone(obsCi.conclusion)">{{
                !obsCi.startedAt && !obsCi.conclusion
                  ? "pending"
                  : obsCi.conclusion || "running"
              }}</span>
              @if (obsCi.commit) {
                <span class="mono muted">{{ obsCi.commit.slice(0, 8) }}</span>
              }
              @if (obsCi.detail) {
                <span class="muted">{{ obsCi.detail }}</span>
              }
              @if (obsCi.runId) {
                <a
                  [routerLink]="['/ci/runs', obsCi.runId]"
                  data-testid="ci-run-link"
                  >Open run #{{ obsCi.runId }}</a
                >
              }
            </div>
          } @else {
            <p class="muted" style="margin: 0">
              No CI runs recorded for this task yet.
            </p>
          }
        } @else {
          @for (run of ciRuns(); track run.id) {
            <div class="ci-run-row" [attr.data-testid]="'ci-run-' + run.id">
              <div class="ci-run-summary">
                <span
                  [class]="
                    'status-pill ' + ciTone(run.conclusion || run.status)
                  "
                  >{{
                    !run.startedAt && !run.conclusion
                      ? "pending"
                      : run.conclusion || run.status
                  }}</span
                >
                <strong>{{ run.workflow || "CI run #" + run.id }}</strong>
                @if (run.commit) {
                  <span class="mono muted">{{ run.commit.slice(0, 8) }}</span>
                }
                <a
                  [routerLink]="['/ci/runs', run.id]"
                  [attr.data-testid]="'ci-run-link-' + run.id"
                  >View run #{{ run.id }}</a
                >
                <button
                  type="button"
                  class="btn-text"
                  [attr.data-testid]="'ci-expand-' + run.id"
                  (click)="toggleRunDetail(run)"
                >
                  {{
                    expandedRunId() === run.id ? "Hide steps" : "Inspect steps"
                  }}
                </button>
              </div>
              @if (run.fault || run.conclusion === "errored") {
                <p class="banner banner-info" style="margin: 0">
                  Shared infrastructure fault:
                  {{
                    run.fault?.summary ||
                      run.detail ||
                      "errored outside branch code"
                  }}
                </p>
              }
              @if (expandedRunId() === run.id) {
                @let detailRun = runDetails()[run.id] ?? run;
                @if (detailRun.jobs?.length) {
                  @for (job of detailRun.jobs; track job.name) {
                    <div class="ci-job-block">
                      <div class="ci-step-head">
                        <span
                          [class]="
                            'status-pill ' +
                            ciTone(job.conclusion || job.status)
                          "
                          >{{ job.conclusion || job.status || "job" }}</span
                        >
                        <strong>{{ job.name }}</strong>
                      </div>
                      @for (step of job.steps ?? []; track step.name) {
                        @let skey = run.id + ":" + job.name + ":" + step.name;
                        <div class="ci-step-row">
                          <div class="ci-step-head">
                            <span
                              [class]="
                                'status-pill ' +
                                ciTone(step.conclusion || step.status)
                              "
                              >{{
                                step.conclusion || step.status || "step"
                              }}</span
                            >
                            <span>{{ step.name }}</span>
                            @if (step.output) {
                              <button
                                type="button"
                                class="btn-text"
                                [attr.data-testid]="'ci-step-log-btn-' + $index"
                                (click)="toggleStepLog(skey)"
                              >
                                {{
                                  openStepLogs().has(skey)
                                    ? "Hide log"
                                    : "Show log"
                                }}
                              </button>
                            }
                          </div>
                          @if (step.output && openStepLogs().has(skey)) {
                            <pre
                              class="ci-step-log"
                              data-testid="ci-step-log"
                              >{{ step.output }}</pre>
                          }
                        </div>
                      }
                    </div>
                  }
                } @else {
                  <p class="muted" style="margin: 0">
                    {{ detailRun.detail || "No step breakdown reported." }}
                  </p>
                }
              }
            </div>
          }
        }
      </section>

      <!-- Diff Preview & Full Branch Compare -->
      <section
        class="diff-card"
        aria-labelledby="task-diff-h"
        data-testid="task-diff-section"
      >
        <div class="ci-card-head">
          <div>
            <h3 id="task-diff-h" class="section-title">
              {{ fullDiffLoaded() ? "Full branch diff" : "Diff preview" }}
            </h3>
            @if (diffSummary(); as sum) {
              <span class="muted" style="font-size: 12px">{{ sum }}</span>
            }
          </div>
          @if (canCompare() && !fullDiffLoaded()) {
            <button
              type="button"
              class="btn-tonal"
              data-testid="load-full-diff"
              [disabled]="fullDiffLoading()"
              (click)="loadFullDiff()"
            >
              {{ fullDiffLoading() ? "Loading full diff…" : "Load full diff" }}
            </button>
          }
        </div>

        @if (diffError(); as err) {
          <p class="banner banner-error" role="alert">{{ err }}</p>
        } @else if (diffLoading() && activeDiffFiles().length === 0) {
          <p class="muted" style="margin: 0">Reading diff…</p>
        } @else if (activeDiffFiles().length === 0) {
          <p class="muted" style="margin: 0">
            No branch changes against base yet.
          </p>
        } @else {
          <g-diff-viewer [files]="activeDiffFiles()" testid="task-diff-files" />
        }
      </section>
    </div>

    <!-- Force Merge Gate-Override Confirmation Dialog -->
  `,
})
export class TaskChangesTabComponent {
  protected readonly store = inject(TaskStore);
  private readonly api = inject(GrainApi);
  private readonly access = inject(AccessService);
  private readonly snackbar = inject(SnackbarService);
  private readonly doc = inject(DOCUMENT);
  private readonly zone = inject(NgZone);

  readonly task = input.required<TaskDetail>();
  readonly prUrl = input<string | null>(null);
  readonly prLabel = input<string>("");
  readonly acted = output<void>();

  protected readonly preview = signal<DiffPreview | null>(null);
  protected readonly fullDiffFiles = signal<DiffPreviewFile[] | null>(null);
  protected readonly fullDiffLoaded = signal(false);
  protected readonly diffLoading = signal(false);
  protected readonly fullDiffLoading = signal(false);
  protected readonly diffError = signal<string | null>(null);

  protected readonly ciRuns = signal<CIRun[]>([]);
  protected readonly runDetails = signal<Record<string, CIRun | undefined>>({});
  protected readonly expandedRunId = signal<string | null>(null);
  protected readonly openStepLogs = signal<ReadonlySet<string>>(new Set());
  protected readonly ciLoading = signal(false);
  protected readonly ciError = signal<string | null>(null);

  readonly forceMerge =
    output<import("./force-merge-dialog.component").ForceMergeRequest>();

  protected emitForceMerge(): void {
    const t = this.task();
    this.forceMerge.emit({
      task: t,
      unsatisfiedGates: this.unsatisfiedGates(),
      head: this.preview()?.head || t.ci?.commit,
      run: (reason, overrideGates, head) =>
        this.store
          .act(
            "force-merge",
            (id) =>
              this.api.post(
                "POST /api/tasks/{id}/force-merge",
                { id },
                {
                  reason,
                  ...(head ? { head } : {}),
                  ...(overrideGates.length ? { overrideGates } : {}),
                },
              ),
            t.id,
          )
          .then((ok) => {
            if (ok) {
              this.snackbar.show(`Force-merge queued for #${t.id}`);
              this.acted.emit();
            }
            return ok;
          }),
    });
  }

  protected readonly overrideGateIds = signal<ReadonlySet<string>>(new Set());

  protected readonly submitOff = computed(() => submitBlock(this.task()));

  protected readonly hasChangelist = computed(
    () =>
      !!(
        this.task().changelists?.length ||
        this.task().changeTargetKind === "changelist"
      ),
  );

  protected readonly canCompare = computed(() => {
    const t = this.task();
    return !!(
      t.repo &&
      t.branch &&
      this.can("GET /api/repos/{owner}/{name}/compare")
    );
  });

  protected readonly activeDiffFiles = computed<DiffPreviewFile[]>(() => {
    if (this.fullDiffLoaded() && this.fullDiffFiles()) {
      return this.fullDiffFiles()!;
    }
    return this.preview()?.files ?? [];
  });

  protected readonly diffSummary = computed(() => {
    const full = this.fullDiffFiles();
    if (this.fullDiffLoaded() && full) {
      return `${full.length} file${full.length === 1 ? "" : "s"} changed`;
    }
    const p = this.preview();
    if (!p || !p.totalFiles) return "";
    return p.truncated
      ? `Showing ${p.files.length} of ${p.totalFiles} files (preview)`
      : `${p.totalFiles} file${p.totalFiles === 1 ? "" : "s"} changed`;
  });

  protected readonly hasFailedCi = computed(() => {
    if (
      this.task().ci?.conclusion === "failure" ||
      this.task().ci?.conclusion === "errored"
    ) {
      return true;
    }
    return this.ciRuns().some(
      (r) => r.conclusion === "failure" || r.conclusion === "errored",
    );
  });

  protected readonly unsatisfiedGates = computed<MergeGate[]>(() =>
    (this.task().mergeGates ?? []).filter(
      (g) =>
        g.id !== APPROVAL_GATE &&
        g.state !== "passed" &&
        g.state !== "overridden",
    ),
  );

  constructor() {
    effect(() => {
      const id = this.task().id;
      untracked(() => {
        this.preview.set(null);
        this.fullDiffFiles.set(null);
        this.fullDiffLoaded.set(false);
        this.diffError.set(null);
        this.ciRuns.set([]);
        this.expandedRunId.set(null);
        this.ciError.set(null);
        void this.loadInitial(id);
      });
    });
  }


  protected can(route: Parameters<AccessService["can"]>[0]): boolean {
    return this.access.can(route, this.task().repo || undefined);
  }

  protected gateTone(g: MergeGate): string {
    if (g.state === "passed" || g.state === "overridden") return "healthy";
    if (g.state === "failing" || g.state === "findings") return "error";
    if (g.state === "running") return "setup";
    return "proposal";
  }

  protected ciTone(conclusion?: string): string {
    if (conclusion === "success" || conclusion === "passed") return "healthy";
    if (
      conclusion === "failure" ||
      conclusion === "errored" ||
      conclusion === "cancelled"
    ) {
      return "error";
    }
    if (conclusion === "running" || conclusion === "queued") return "setup";
    return "neutral";
  }

  private async loadInitial(id: string): Promise<void> {
    if (
      typeof this.api.diffPreview === "function" &&
      this.can("GET /api/tasks/{id}/diff-preview")
    ) {
      this.diffLoading.set(true);
      try {
        const p = await this.api.diffPreview(id, 12);
        if (this.task().id === id) this.preview.set(p);
      } catch {
        // Task may have no branch/diff yet; leave empty quietly unless compare fails.
      } finally {
        if (this.task().id === id) this.diffLoading.set(false);
      }
    }

    if (typeof this.api.get === "function" && this.can("GET /api/ci/runs")) {
      this.ciLoading.set(true);
      try {
        const t = this.task();
        const res = await this.api.get<CIRunsResponse | CIRun[]>(
          "GET /api/ci/runs",
          undefined,
          `repo=${encodeURIComponent(t.repo!)}&ref=${encodeURIComponent(t.branch!)}&limit=10`,
        );
        if (this.task().id !== id) return;
        const runs = Array.isArray(res) ? res : (res?.runs ?? []);
        this.ciRuns.set(runs);
        const autoRunId = this.task().ci?.runId || runs[0]?.id;
        if (autoRunId) {
          if (!runs.some((r) => r.id === autoRunId)) {
            try {
              const run = await this.api.get<CIRun>("GET /api/ci/runs/{id}", {
                id: autoRunId,
              });
              if (run) runs.unshift(run);
            } catch {
              /* ignore */
            }
          }
          await this.fetchRunDetail(autoRunId);
        }
      } catch {
        // CI disabled or unavailable on deployment.
      } finally {
        if (this.task().id === id) this.ciLoading.set(false);
      }
    }
  }

  protected async loadFullDiff(): Promise<void> {
    const t = this.task();
    if (!t.repo || !t.branch || this.fullDiffLoading()) return;
    const { owner, name } = splitRepo(t.repo);
    if (!owner || !name) return;
    this.fullDiffLoading.set(true);
    this.diffError.set(null);
    try {
      const qs = new URLSearchParams();
      if (t.base) qs.set("base", t.base);
      qs.set("head", t.branch);
      const cmp = await this.api.get<CompareResponse>(
        "GET /api/repos/{owner}/{name}/compare",
        { owner, name },
        qs.toString(),
      );
      if (this.task().id !== t.id) return;
      this.fullDiffFiles.set(parseUnifiedDiff(cmp?.diff ?? ""));
      this.fullDiffLoaded.set(true);
    } catch (err) {
      if (this.task().id === t.id) {
        this.diffError.set(`Full diff could not be loaded: ${errorText(err)}`);
      }
    } finally {
      if (this.task().id === t.id) this.fullDiffLoading.set(false);
    }
  }

  protected async toggleRunDetail(run: CIRun): Promise<void> {
    if (this.expandedRunId() === run.id) {
      this.expandedRunId.set(null);
      return;
    }
    this.expandedRunId.set(run.id);
    await this.fetchRunDetail(run.id);
  }

  private async fetchRunDetail(runId: string): Promise<void> {
    if (!this.can("GET /api/ci/runs/{id}")) return;
    try {
      const d = await this.api.get<CIRun>("GET /api/ci/runs/{id}", {
        id: runId,
      });
      if (d) {
        this.runDetails.update((m) => ({ ...m, [runId]: d }));
      }
    } catch {
      // Keep summary row if run detail was evicted.
    }
  }

  protected toggleStepLog(key: string): void {
    this.openStepLogs.update((s) => {
      const next = new Set(s);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }

  protected async rerunCi(failedOnly: boolean): Promise<void> {
    const t = this.task();
    const key = failedOnly ? "ci-rerun-failed" : "ci-rerun";
    const body = failedOnly
      ? { taskId: t.id, recheck: true, recheckFailed: true }
      : { taskId: t.id };
    const ok = await this.store.act(
      key,
      () => this.api.post("POST /api/ci/runs", undefined, body),
      t.id,
    );
    if (ok) {
      this.snackbar.show(
        failedOnly ? "Triggered CI for failed jobs" : "Triggered CI run",
      );
      void this.loadInitial(t.id);
      this.acted.emit();
    }
  }

  protected async openPr(): Promise<void> {
    const t = this.task();
    const ok = await this.store.act(
      "open-pr",
      (id) => this.api.post("POST /api/tasks/{id}/pull-request", { id }),
      t.id,
    );
    if (ok) {
      this.snackbar.show(`Opened pull request for #${t.id}`);
      this.acted.emit();
    }
  }

  protected async submitPr(): Promise<void> {
    const t = this.task();
    if (submitBlock(t)) return;
    const ok = await this.store.act(
      "submit",
      (id) => this.api.post("POST /api/tasks/{id}/submit", { id }),
      t.id,
    );
    if (ok) {
      this.snackbar.show(`Submitted #${t.id}`);
      this.acted.emit();
    }
  }

  protected async retryMerge(): Promise<void> {
    const t = this.task();
    const ok = await this.store.act(
      "retry-merge",
      (id) => this.api.post("POST /api/tasks/{id}/retry-merge", { id }),
      t.id,
    );
    if (ok) {
      this.snackbar.show(`Retrying merge for #${t.id}`);
      this.acted.emit();
    }
  }

  protected async clAction(
    name: string,
    route:
      | "POST /api/tasks/{id}/change-sync"
      | "POST /api/tasks/{id}/change-publish"
      | "POST /api/tasks/{id}/change-mail",
  ): Promise<void> {
    const t = this.task();
    const ok = await this.store.act(
      `cl-${name}`,
      (id) => this.api.post(route, { id }),
      t.id,
    );
    if (ok) {
      this.snackbar.show(`Changelist ${name} requested for #${t.id}`);
      this.acted.emit();
    }
  }
}
