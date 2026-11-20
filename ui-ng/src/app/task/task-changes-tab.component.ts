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
  DestroyRef,
  NgZone,
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
  QAFinding,
  TaskDetail,
  TaskQAResponse,
  VoteQAFindingResponse,
} from "../api/models";
import { AccessService } from "../services/access.service";
import { BranchRefsService } from "../services/branch-refs.service";
import { SnackbarService } from "../services/snackbar.service";
import { errorText } from "../sessions/session-helpers";
import { parseUnifiedDiff } from "../shared/diff-parse";
import { DiffViewerComponent } from "../shared/diff-viewer.component";
import { unsatisfiedGates } from "../shared/gates";
import { MergeGatesListComponent } from "../shared/merge-gates-list.component";
import { StatusIconComponent } from "../shared/status-icon.component";
import { TipDirective } from "../shared/tip.directive";
import { TaskStore } from "./task.store";

@Component({
  selector: "g-task-changes-tab",
  imports: [
    RouterLink,
    DiffViewerComponent,
    StatusIconComponent,
    TipDirective,
    MergeGatesListComponent,
  ],
  encapsulation: ViewEncapsulation.None,
  styleUrl: "./task-changes-tab.css",
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="changes-tab">
      <dl class="facts" data-testid="changes">
        <dt>Branch</dt>
        <dd class="mono">{{ task().branch || "—" }}</dd>
        @if (taskQa()?.stageBranch && !task().pullRequest) {
          <dt>Staging branch</dt>
          <dd class="mono" data-testid="changes-stage-branch">
            {{ taskQa()?.stageBranch }}
          </dd>
        }
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
          <dd class="mono">{{ task().changelists?.join(", ") ?? "" }}</dd>
        }
      </dl>

      @if (task().mergeGates?.length) {
        <g-merge-gates-list [task]="task()" />
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

      <!-- Pre-PR Quality Checks (shown only when QA is active or recorded on this task) -->
      @if (showQaCard()) {
        <section
          class="ci-runs-card"
          aria-labelledby="task-qa-h"
          data-testid="task-qa-section"
        >
          <div class="ci-card-head">
            <h3
              [attr.aria-level]="pane() ? 4 : null"
              id="task-qa-h"
              class="section-title"
            >
              Pre-PR quality checks
            </h3>
            @if (taskQa()?.assignment; as assign) {
              <span
                class="mono muted"
                data-testid="task-qa-arm"
                gTip="Work-tree A/B arm and quality flow package assigned to this task"
              >
                {{ assign.arm }} · {{ assign.flowId }}
              </span>
            }
          </div>

          @if (canBypassS3()) {
            <div
              class="tc-qa-bypass-bar"
              role="region"
              aria-label="S3 gate blocked"
              data-testid="qa-s3-blocked-bar"
            >
              <div class="tc-qa-bypass-copy">
                <strong>Pre-PR S3 gate is blocking PR promotion</strong>
                <span>
                  Review the findings below, or override the S3 gate to promote
                  <code class="mono">{{
                    taskQa()?.stageBranch || task().branch || "staged commits"
                  }}</code>
                  and open the pull request.
                </span>
              </div>
              <button
                type="button"
                class="btn-outlined danger"
                data-testid="qa-bypass-s3-btn"
                gTip="Override the blocked S3 staging gate for this commit, promote the staging branch, and open the pull request with a recorded reason."
                [disabled]="store.isBusy('bypass-s3', task().id)"
                (click)="emitBypassS3()"
              >
                Bypass S3 gate &amp; open PR…
              </button>
            </div>
          }

          @if (visibleQaCheckRuns().length === 0 && qaFindings().length === 0) {
            <p class="muted" style="margin: 0" data-testid="task-qa-empty">
              @if (taskQa()?.assignment?.arm === "control") {
                Control arm — baseline path (no pre-PR gate applied).
              } @else {
                No pre-PR quality check runs recorded yet.
              }
            </p>
          }

          @if (qaSummaryStats(); as stats) {
            <div
              class="tc-run-row"
              data-testid="qa-summary-stats"
              style="gap: 12px; background: var(--md-sys-color-surface-container-lowest);"
            >
              @if (stats.complexityTier) {
                <span
                  class="status-pill info"
                  gTip="Task complexity assessment"
                >
                  Complexity: {{ stats.complexityTier }}
                </span>
              }
              @if (stats.blastTier) {
                <span
                  class="status-pill warning"
                  gTip="Blast radius assessment"
                >
                  Blast: {{ stats.blastTier }} &middot;
                  {{ stats.blastScore }}/100
                </span>
              }
              @if (stats.triageDecision) {
                <span
                  class="status-pill"
                  [class.success]="stats.triageDecision === 'auto_merge'"
                  [class.error]="stats.triageDecision !== 'auto_merge'"
                  gTip="Triage decision"
                >
                  {{
                    stats.triageDecision === "auto_merge"
                      ? "Auto-merge eligible"
                      : "Human review required"
                  }}
                </span>
              }
              @if (stats.repairedInNextSha === 1) {
                <span
                  class="badge"
                  style="background: var(--md-sys-color-tertiary-container); color: var(--md-sys-color-on-tertiary-container);"
                  gTip="Issues repaired in next push"
                >
                  Repaired in next push
                </span>
              }
            </div>
          }

          @for (chk of visibleQaCheckRuns(); track chk.id) {
            <div class="tc-run-row" [attr.data-testid]="'qa-check-' + chk.id">
              <div class="ci-run-summary">
                <span [class]="'status-pill ' + qaVerdictTone(chk.verdict)">
                  <g-status-icon
                    [tone]="qaVerdictTone(chk.verdict)"
                    [kind]="chk.verdict"
                  />
                  {{ chk.verdict }}
                </span>
                <strong class="mono">{{ chk.checkId }}</strong>
                <span
                  class="ci-provider-tag"
                  [gTip]="qaStageTooltip(chk.stage)"
                  >{{ chk.stage }}</span
                >
                @if (chk.shadow) {
                  <span
                    class="ci-provider-tag"
                    gTip="Passive control-arm shadow run — recorded for A/B comparison without blocking PR creation"
                    >shadow</span
                  >
                }
                @if (chk.commitSha) {
                  <span class="mono muted">{{
                    chk.commitSha.slice(0, 8)
                  }}</span>
                }
                <span class="muted tabular">{{ chk.durationMs }} ms</span>
              </div>
            </div>
          }

          @if (qaFindings().length > 0) {
            <div class="tc-qa-findings-list" data-testid="task-qa-findings">
              @for (f of qaFindings(); track f.id) {
                <div
                  class="tc-qa-finding-row"
                  [attr.data-testid]="'qa-finding-' + f.id"
                >
                  <div class="tc-qa-finding-main">
                    <div class="ci-step-head">
                      @let fTone = qaFindingTone(f);
                      <span
                        [class]="'status-pill ' + fTone"
                        [gTip]="qaStageTooltip(f.stage)"
                      >
                        <g-status-icon
                          [tone]="fTone"
                          [kind]="fTone === 'error' ? 'failed' : fTone"
                        />
                        {{ f.stage }}
                      </span>
                      <code class="mono">{{ f.ruleId }}</code>
                      @if (isShadowFinding(f)) {
                        <span
                          class="ci-provider-tag"
                          gTip="Passive shadow finding — did not block pull request creation"
                          >shadow</span
                        >
                      }
                      @if (f.filePath) {
                        <span class="mono muted">
                          {{ f.filePath }}{{ f.line ? ":" + f.line : "" }}
                        </span>
                      }
                    </div>
                    <p class="tc-qa-finding-summary">{{ f.summary }}</p>
                    @if (f.evidence) {
                      <button
                        type="button"
                        class="btn-text"
                        [attr.aria-expanded]="openQaEvidence().has(f.id)"
                        [attr.data-testid]="'qa-evidence-btn-' + f.id"
                        (click)="toggleQaEvidence(f.id)"
                      >
                        {{
                          openQaEvidence().has(f.id)
                            ? "Hide details"
                            : "Show details"
                        }}
                      </button>
                      @if (openQaEvidence().has(f.id)) {
                        <pre class="ci-step-log">{{ f.evidence }}</pre>
                      }
                    }
                  </div>
                  @if (
                    can("POST /api/tasks/{id}/qa/findings/{finding}/feedback")
                  ) {
                    <div
                      class="tc-qa-vote-group"
                      role="group"
                      aria-label="Finding usefulness feedback"
                    >
                      <button
                        type="button"
                        class="filter-chip"
                        gTip="Mark this finding as a helpful catch"
                        [attr.aria-pressed]="f.humanVote === 1"
                        [attr.data-testid]="'qa-vote-up-' + f.id"
                        [disabled]="votingFindingIds().has(f.id)"
                        (click)="voteFinding(f, f.humanVote === 1 ? 0 : 1)"
                      >
                        Helpful
                      </button>
                      <button
                        type="button"
                        class="filter-chip"
                        gTip="Mark this finding as unhelpful or a false positive"
                        [attr.aria-pressed]="f.humanVote === -1"
                        [attr.data-testid]="'qa-vote-down-' + f.id"
                        [disabled]="votingFindingIds().has(f.id)"
                        (click)="voteFinding(f, f.humanVote === -1 ? 0 : -1)"
                      >
                        Not helpful
                      </button>
                    </div>
                  }
                </div>
              }
            </div>
          }
        </section>
      }

      <!-- CI Runs & Step Breakdown -->
      <section
        class="ci-runs-card"
        aria-labelledby="task-ci-h"
        data-testid="task-ci-section"
      >
        <div class="ci-card-head">
          <h3
            [attr.aria-level]="pane() ? 4 : null"
            id="task-ci-h"
            class="section-title"
          >
            CI checks &amp; runs
          </h3>
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
            <div class="tc-run-row" [attr.data-testid]="'ci-run-' + run.id">
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
            <h3
              [attr.aria-level]="pane() ? 4 : null"
              id="task-diff-h"
              class="section-title"
            >
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
        } @else if (diffSkipReason()) {
          <p class="muted" style="margin: 0" data-testid="task-diff-skipped">
            {{ diffSkipReason() }}
          </p>
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
  protected readonly qaStageTooltip = qaStageTooltip;

  protected readonly qaSummaryStats = computed<{
    complexityTier?: string;
    blastTier?: string;
    blastScore?: number;
    triageDecision?: string;
    repairedInNextSha?: number;
  } | null>(() => {
    const runs = this.visibleQaCheckRuns();
    const qaSummary = this.task().qaSummary;
    const assignedComplexityTier = this.taskQa()?.assignment?.complexityTier;
    const withTiers = runs.find((x) => x.complexityTier || x.blastTier);
    const withTriage = runs.find((x) => x.triageDecision);
    const withRepair = runs.find((x) => x.repairedInNextSha === 1);
    const complexityTier =
      withTiers?.complexityTier ||
      assignedComplexityTier ||
      qaSummary?.complexityTier;
    const blastTier = withTiers?.blastTier || qaSummary?.blastTier;
    const blastScore = withTiers?.blastScore ?? qaSummary?.blastScore;
    const triageDecision =
      withTriage?.triageDecision || qaSummary?.triageDecision;
    if (!complexityTier && !blastTier && !triageDecision && !withRepair) {
      return null;
    }
    return {
      complexityTier,
      blastTier,
      blastScore,
      triageDecision,
      repairedInNextSha: withRepair?.repairedInNextSha,
    };
  });

  protected readonly store = inject(TaskStore);
  private readonly api = inject(GrainApi);
  protected readonly access = inject(AccessService);
  private readonly branchRefs = inject(BranchRefsService);
  private readonly snackbar = inject(SnackbarService);
  private readonly doc = inject(DOCUMENT);
  private readonly zone = inject(NgZone);
  private readonly destroyRef = inject(DestroyRef);

  readonly task = input.required<TaskDetail>();
  // pane is set when the task detail sits inside another page (the
  // Tasks split): its title is an h2 there, so these headings step down
  // one level with it (aria-level 3, and 4 below them).
  readonly pane = input(false);
  private readonly taskId = computed(() => this.task().id);
  readonly prUrl = input<string | null>(null);
  readonly prLabel = input<string>("");
  readonly acted = output<void>();

  protected readonly preview = signal<DiffPreview | null>(null);
  protected readonly fullDiffFiles = signal<DiffPreviewFile[] | null>(null);
  protected readonly fullDiffLoaded = signal(false);
  protected readonly diffLoading = signal(false);
  protected readonly fullDiffLoading = signal(false);
  protected readonly diffError = signal<string | null>(null);
  protected readonly diffSkipReason = signal<string | null>(null);
  protected readonly branchPresent = signal<boolean | null>(null);

  protected readonly ciRuns = signal<CIRun[]>([]);
  protected readonly runDetails = signal<Record<string, CIRun | undefined>>({});
  protected readonly expandedRunId = signal<string | null>(null);
  protected readonly openStepLogs = signal<ReadonlySet<string>>(new Set());
  protected readonly ciLoading = signal(false);
  protected readonly ciError = signal<string | null>(null);

  protected readonly taskQa = signal<TaskQAResponse | null>(null);
  protected readonly openQaEvidence = signal<ReadonlySet<number>>(new Set());
  protected readonly votingFindingIds = signal<ReadonlySet<number>>(new Set());

  protected readonly qaFindings = computed<QAFinding[]>(
    () => this.taskQa()?.findings ?? [],
  );

  protected readonly visibleQaCheckRuns = computed(() =>
    (this.taskQa()?.checkRuns ?? []).filter(
      (r) => r.checkId !== "skill-inject",
    ),
  );

  protected readonly showQaCard = computed(() => {
    const q = this.taskQa();
    if (!q) return false;
    return !!(
      q.enabled ||
      q.assignment ||
      q.checkRuns.length > 0 ||
      q.findings.length > 0
    );
  });

  readonly bypassS3 =
    output<import("./bypass-s3-dialog.component").BypassS3Request>();

  protected readonly canBypassS3 = computed(() => {
    const qa = this.taskQa();
    const t = this.task();
    return !!(
      qa?.s3Blocked &&
      (!t.pullRequest || qa.stageBranch) &&
      t.state !== "closed" &&
      this.can("POST /api/tasks/{id}/qa/bypass-s3")
    );
  });

  protected emitBypassS3(): void {
    const t = this.task();
    const qa = this.taskQa();
    const head = qa?.s3HeadSha || this.preview()?.head || t.ci?.commit;
    this.bypassS3.emit({
      task: t,
      stageBranch: qa?.stageBranch,
      head,
      run: (reason) =>
        this.store
          .act(
            "bypass-s3",
            (id) =>
              this.api.post(
                "POST /api/tasks/{id}/qa/bypass-s3",
                { id },
                { reason, ...(head ? { head } : {}) },
              ),
            t.id,
          )
          .then(async (ok) => {
            if (ok) {
              this.snackbar.show(
                `Bypassed S3 gate and opened pull request for #${t.id}`,
              );
              await this.refreshTaskQa(t.id);
              this.acted.emit();
            }
            return ok;
          }),
    });
  }

  protected readonly overrideGateIds = signal<ReadonlySet<string>>(new Set());

  protected readonly hasChangelist = computed(
    () =>
      !!(
        this.task().changelists?.length ||
        this.task().changeTargetKind === "changelist"
      ),
  );

  protected readonly canCompare = computed(() => {
    const t = this.task();
    const parts = (t.repo ?? "").trim().split("/").filter(Boolean);
    return !!(
      t.repo &&
      parts.length === 2 &&
      t.branch &&
      (t.state !== "proposed" || this.branchPresent() === true) &&
      this.branchPresent() !== false &&
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
    unsatisfiedGates(this.task()),
  );

  private lastQaStateKey = "";
  private qaReqSeq = 0;
  private lastBranchKey = "";

  constructor() {
    effect(() => {
      const t = this.task();
      const id = t.id;
      const branchKey = `${id}|${t.branch ?? ""}|${t.base ?? ""}`;
      void [t.pullRequest, t.prMergedAt, t.ci?.runId];
      untracked(() => {
        const branchChanged = this.lastBranchKey !== branchKey;
        this.lastBranchKey = branchKey;
        this.lastQaStateKey = `${id}:${t.state}:${t.attempts?.length ?? 0}:${t.pullRequest ?? ""}`;
        this.qaReqSeq++;
        if (branchChanged) {
          this.preview.set(null);
          this.fullDiffFiles.set(null);
          this.fullDiffLoaded.set(false);
          this.ciRuns.set([]);
          this.expandedRunId.set(null);
          this.ciError.set(null);
          this.taskQa.set(null);
          this.openQaEvidence.set(new Set());
          this.votingFindingIds.set(new Set());
        }
        this.diffError.set(null);
        this.diffSkipReason.set(null);
        this.branchPresent.set(null);
        void this.loadInitial(id);
      });
    });

    effect(() => {
      const t = this.task();
      const key = `${t.id}:${t.state}:${t.attempts?.length ?? 0}:${t.pullRequest ?? ""}`;
      untracked(() => {
        if (this.lastQaStateKey && this.lastQaStateKey !== key) {
          this.lastQaStateKey = key;
          void this.refreshTaskQa(t.id);
        }
      });
    });

    // Live-poll QA check runs while viewing a non-closed task so in-turn S3 checks and
    // async control-arm shadow runs appear in place without collapsing open evidence.
    this.zone.runOutsideAngular(() => {
      const timer = setInterval(() => {
        const t = untracked(() => this.task());
        if (!t?.id || t.state === "closed" || t.state === "merged") return;
        this.zone.run(() => void this.refreshTaskQa(t.id));
      }, 3000);
      this.destroyRef.onDestroy(() => clearInterval(timer));
    });
  }

  protected can(route: Parameters<AccessService["can"]>[0]): boolean {
    return this.access.canAccessTask(route, this.task().repo ?? null);
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

  protected qaVerdictTone(verdict?: string): string {
    if (verdict === "pass" || verdict === "passed") return "healthy";
    if (verdict === "bypassed" || verdict === "warn" || verdict === "shadow") {
      return "proposal";
    }
    if (verdict === "fail" || verdict === "failed" || verdict === "error") {
      return "error";
    }
    return "neutral";
  }

  protected isShadowFinding(f: QAFinding): boolean {
    const runs = this.taskQa()?.checkRuns ?? [];
    const run = runs.find((r) => r.id === f.checkRunId);
    return !!(run?.shadow || this.taskQa()?.assignment?.arm === "control");
  }

  protected qaFindingTone(f: QAFinding): "error" | "proposal" | "neutral" {
    if (f.severity === "info") return "neutral";
    if (
      this.isShadowFinding(f) ||
      f.severity === "warning" ||
      f.severity === "warn" ||
      f.severity === "shadow"
    ) {
      return "proposal";
    }
    return "error";
  }

  protected toggleQaEvidence(id: number): void {
    this.openQaEvidence.update((s) => {
      const next = new Set(s);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  protected async voteFinding(finding: QAFinding, vote: number): Promise<void> {
    const taskId = this.task().id;
    this.votingFindingIds.update((s) => new Set(s).add(finding.id));
    try {
      const res = await this.api.post<VoteQAFindingResponse>(
        "POST /api/tasks/{id}/qa/findings/{finding}/feedback",
        { id: taskId, finding: String(finding.id) },
        { vote },
      );
      if (this.task().id !== taskId) return;
      this.taskQa.update((cur) => {
        if (!cur) return cur;
        return {
          ...cur,
          findings: cur.findings.map((item) =>
            item.id === finding.id
              ? { ...item, humanVote: res.vote, votedBy: res.votedBy }
              : item,
          ),
        };
      });
      this.snackbar.show(
        vote === 1
          ? "Marked finding as helpful"
          : vote === -1
            ? "Marked finding as not helpful"
            : "Cleared finding feedback",
      );
    } catch (err) {
      if (this.task().id === taskId) {
        this.snackbar.show(`Could not save feedback: ${errorText(err)}`);
      }
    } finally {
      this.votingFindingIds.update((s) => {
        const next = new Set(s);
        next.delete(finding.id);
        return next;
      });
    }
  }

  private async refreshTaskQa(id: string): Promise<void> {
    if (
      typeof this.api.get !== "function" ||
      !this.can("GET /api/tasks/{id}/qa")
    ) {
      return;
    }
    const seq = ++this.qaReqSeq;
    try {
      const qaRes = await this.api.get<TaskQAResponse>(
        "GET /api/tasks/{id}/qa",
        { id },
      );
      if (seq === this.qaReqSeq && this.task().id === id && qaRes) {
        this.taskQa.set(qaRes);
      }
    } catch {
      // Older server or QA disabled.
    }
  }

  private async loadInitial(id: string): Promise<void> {
    const t0 = this.task();
    let skipDiff: string | null = null;
    if (!t0.branch && !t0.changelists?.length && !t0.pullRequest) {
      skipDiff =
        t0.state === "proposed"
          ? "Proposals do not have a branch diff yet."
          : "No branch associated with this task yet.";
    } else if (!t0.repo) {
      // The server previews only a repository branch (it answers 404
      // for a task with no repository), so a changelist has none here.
      skipDiff =
        "This task's change is a changelist, with no repository branch to preview.";
    } else if (t0.repo && t0.branch) {
      const present = await this.branchRefs.hasBranch(t0.repo, t0.branch);
      if (this.task().id !== id) return;
      this.branchPresent.set(present);
      if (present === false) {
        skipDiff = t0.prMergedAt
          ? "Pull request merged; branch is no longer present."
          : t0.state === "proposed"
            ? "Proposals do not have a branch diff yet."
            : "Branch is no longer present on the remote.";
      } else if (
        t0.state === "proposed" &&
        present !== true &&
        !t0.pullRequest &&
        !t0.changelists?.length
      ) {
        skipDiff = "Proposals do not have a branch diff yet.";
      } else if (present === null && t0.prMergedAt) {
        skipDiff = "Pull request already merged.";
      }
    } else if (t0.state === "proposed") {
      skipDiff = "Proposals do not have a branch diff yet.";
    } else if (t0.prMergedAt) {
      skipDiff = "Pull request already merged.";
    }
    this.diffSkipReason.set(skipDiff);

    if (
      !skipDiff &&
      typeof this.api.diffPreview === "function" &&
      this.can("GET /api/tasks/{id}/diff-preview")
    ) {
      this.diffLoading.set(true);
      try {
        const p = await this.api.diffPreview(id, 12);
        if (this.task().id === id) this.preview.set(p);
      } catch (err: unknown) {
        const status = (err as { status?: number } | null)?.status;
        const msg = errorText(err);
        if (
          this.task().id === id &&
          status !== 404 &&
          !/not found|no branch|missing/i.test(msg)
        ) {
          this.diffError.set(`Diff preview could not be loaded: ${msg}`);
        }
      } finally {
        if (this.task().id === id) this.diffLoading.set(false);
      }
    }

    await this.refreshTaskQa(id);

    if (typeof this.api.get === "function" && this.can("GET /api/ci/runs")) {
      this.ciLoading.set(true);
      try {
        const t = this.task();
        // GET /api/ci/runs has no task filter (pkg/ui/ci.go ciRunFilterFrom):
        // scope by repo + ref, and fall back to the task's own run id.
        const res: CIRunsResponse | CIRun[] =
          t.repo && t.branch
            ? await this.api.get<CIRunsResponse | CIRun[]>(
                "GET /api/ci/runs",
                undefined,
                `repo=${encodeURIComponent(t.repo)}&ref=${encodeURIComponent(t.branch)}&limit=10`,
              )
            : [];
        if (this.task().id !== id) return;
        let runs = Array.isArray(res) ? res : (res?.runs ?? []);
        const stageRef = this.taskQa()?.stageBranch;
        if (
          t.repo &&
          stageRef &&
          stageRef !== t.branch &&
          (runs.length === 0 || !t.pullRequest)
        ) {
          try {
            const stageRes = await this.api.get<CIRunsResponse | CIRun[]>(
              "GET /api/ci/runs",
              undefined,
              `repo=${encodeURIComponent(t.repo)}&ref=${encodeURIComponent(stageRef)}&limit=10`,
            );
            if (this.task().id !== id) return;
            const stageRuns = Array.isArray(stageRes)
              ? stageRes
              : (stageRes?.runs ?? []);
            if (stageRuns.length > 0) {
              const seen = new Set(runs.map((r) => r.id));
              runs = t.pullRequest
                ? [...runs, ...stageRuns.filter((r) => !seen.has(r.id))]
                : [
                    ...stageRuns,
                    ...runs.filter(
                      (r) => !stageRuns.some((s) => s.id === r.id),
                    ),
                  ];
            }
          } catch {
            /* ignore */
          }
        }
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

function qaStageTooltip(stage: string): string {
  switch (stage) {
    case "S0":
      return "Stage S0 — pre-clone EARS acceptance criteria & ambiguity extraction";
    case "S0b":
      return "Stage S0b — complexity-gated slice plan or wave-ordered Workstream DAG decomposition";
    case "S1":
      return "Stage S1 — repository context & sibling importer discovery";
    case "S2":
      return "Stage S2 — fresh-context pre-edit plan critique";
    case "S3":
      return "Stage S3 — fast deterministic AST & diff gate run before pull request creation";
    case "S4":
      return "Stage S4 — 1-round fresh-context orthogonal diff verifier";
    case "S5":
      return "Stage S5 — blast-tier scorecard & merge triage classification";
    default:
      return `Quality stage ${stage}`;
  }
}
