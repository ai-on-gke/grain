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
  type OnInit,
  ViewEncapsulation,
  computed,
  inject,
  input,
  output,
  signal,
} from "@angular/core";
import { takeUntilDestroyed } from "@angular/core/rxjs-interop";
import { ActivatedRoute, Router, RouterLink } from "@angular/router";
import { GrainApi } from "../api/grain-api.service";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import {
  type PillOption,
  PillSelectComponent,
} from "../shared/pill-select.component";
import { StatusIconComponent } from "../shared/status-icon.component";
import { TipDirective } from "../shared/tip.directive";
import { QualityV3CardsComponent } from "./quality-v3-cards.component";
import {
  QaExperimentSheetComponent,
  QaStartDialogComponent,
} from "./qa-experiment-sheet.component";
import type {
  QAExperimentDetail,
  QAExperimentResults,
  QAExperimentSummary,
} from "./qa-experiments.models";
import {
  armLabel,
  armVerdicts,
  checkPrecision,
  costMeasured,
  cupedTip,
  durWords,
  experimentPeriodLabel,
  experimentScope,
  matchedRecall,
  rateDiffWords,
  relDiffWords,
  stageWords,
  timeDiffWords,
  timeTip,
  tokenWords,
} from "./quality-verdict";
import {
  type QACheckRuleSummary,
  type QASummary,
  formatCheckDurationMs,
  formatPct,
} from "./telemetry.models";

interface MetricRow {
  id: string;
  label: string;
  tip: string;
  treatment: string;
  control: string;
  diff: string;
}

@Component({
  selector: "g-telemetry-quality",
  imports: [
    RouterLink,
    QualityV3CardsComponent,
    TipDirective,
    StatusIconComponent,
    PillSelectComponent,
    QaExperimentSheetComponent,
    QaStartDialogComponent,
  ],
  encapsulation: ViewEncapsulation.None,
  styleUrl: "./quality-checks.component.css",
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @if (deniedReason(); as why) {
      <p class="empty-state" data-testid="qa-denied">{{ why }}</p>
    } @else if (expDenied()) {
      <p class="empty-state" data-testid="qa-exp-denied">
        You need the <span class="mono">qa:read</span> permission to view
        quality experiments. Ask a deployment admin.
      </p>
    } @else if (activeV2Mode()) {
      @if (expLoading() && !exp()) {
        <article
          class="ops-card tq-card"
          aria-busy="true"
          data-testid="qa-loading"
        >
          <h2 class="ops-card-title">Loading quality experiment…</h2>
          <div class="tq-skel" aria-hidden="true"><i></i><i></i><i></i></div>
        </article>
      } @else if (expError()) {
        <div
          class="banner banner-error tq-error"
          role="alert"
          data-testid="qa-error"
        >
          <span>Could not load quality experiment: {{ expError() }}</span>
          <button
            type="button"
            class="btn-text"
            data-testid="qa-retry"
            (click)="loadExperiments()"
          >
            Try again
          </button>
        </div>
      } @else if (exp()) {
        @let detail = exp()!;
        <article
          class="ops-card tq-card"
          aria-label="Experiment"
          data-testid="card-qa-experiment"
        >
          <div class="tq-expbar">
            <g-pill-select
              testid="pill-qa-experiment"
              ariaLabel="Experiment"
              [options]="expOptions()"
              [value]="detail.id"
              (valueChange)="setExp($event)"
            />
            @if (detail.state === "draft") {
              <span class="status-pill neutral" data-testid="qa-exp-state-pill">
                <g-status-icon tone="neutral" kind="idle" />Draft
              </span>
            } @else if (detail.state === "running") {
              <span class="status-pill healthy" data-testid="qa-exp-state-pill">
                <g-status-icon tone="healthy" kind="running" />Running
              </span>
            } @else {
              <span class="status-pill neutral" data-testid="qa-exp-state-pill">
                <g-status-icon tone="neutral" kind="idle" />{{
                  detail.state === "archived" ? "Archived" : "Stopped"
                }}
              </span>
            }

            @if (res()?.periodLabel; as pLabel) {
              <span class="status-pill neutral" data-testid="qa-period-chip">
                <g-status-icon tone="neutral" kind="progress" />{{ pLabel }}
              </span>
            }

            @if (res()?.assignmentAudit && !res()!.assignmentAudit.passed) {
              <span
                class="status-pill error"
                data-testid="qa-audit-pill"
                tabindex="0"
                [gTip]="
                  res()!.assignmentAudit.mismatches.length +
                  ' enrollment mismatches detected'
                "
              >
                <g-status-icon tone="error" kind="error" />Assignment check
                failed
              </span>
            } @else if (res()?.assignmentAudit?.passed) {
              <span
                class="status-pill healthy"
                data-testid="qa-audit-pill"
                tabindex="0"
                gTip="Every enrolled work tree's arm matches its deterministic salt or explicit key."
              >
                <g-status-icon tone="healthy" kind="ok" />Assignment checked
              </span>
            }

            <div class="tq-exp-actions">
              @if (canAdmin()) {
                @if (detail.state === "draft") {
                  <button
                    type="button"
                    class="btn-primary"
                    data-testid="btn-exp-open-start"
                    (click)="openStartDialog()"
                  >
                    Start experiment
                  </button>
                } @else if (detail.enrollmentHeld === "engine-change") {
                  <button
                    type="button"
                    class="btn-primary"
                    data-testid="btn-exp-resume"
                    (click)="transition('resume-enrollment')"
                  >
                    Resume enrollment (new segment)
                  </button>
                  <button
                    type="button"
                    class="btn-tonal"
                    data-testid="btn-exp-stop"
                    (click)="transition('stop')"
                  >
                    Stop experiment
                  </button>
                } @else if (detail.state === "running") {
                  @if (haltableArmId(); as haltArm) {
                    <button
                      type="button"
                      class="btn-tonal"
                      data-testid="btn-exp-halt-arm"
                      (click)="haltSingleArm(haltArm)"
                    >
                      Halt arm
                    </button>
                  }
                  <button
                    type="button"
                    class="btn-tonal"
                    data-testid="btn-exp-stop"
                    (click)="transition('stop')"
                  >
                    Stop experiment
                  </button>
                }
                <button
                  type="button"
                  class="btn-tonal"
                  data-testid="btn-new-experiment"
                  (click)="showCreateSheet.set(true)"
                >
                  New experiment
                </button>
              }
              <a class="btn-text" routerLink="/processes/quality"
                >Manage flows</a
              >
            </div>
          </div>

          <div class="tq-armline" data-testid="qa-exp-arms">
            @for (a of detail.arms; track a.id) {
              <div
                class="tq-arm-item"
                [class.is-halted]="a.halted"
                [attr.data-testid]="'qa-arm-' + a.id"
              >
                <span>{{ a.label }} · {{ a.weight }}%</span>
                <span class="tabular s-num">
                  {{ enrolledForArm(a.id) }} of
                  {{ detail.plan.plannedOriginsPerArm }} work trees
                </span>
                @if (a.halted) {
                  <span class="status-pill error">
                    <g-status-icon tone="error" kind="error" />Halted{{
                      a.haltedAt ? " · " + a.haltedAt : ""
                    }}
                  </span>
                }
              </div>
            }
          </div>
          <p class="ops-card-sub" style="margin: 8px 0 0;">
            A <strong>work tree</strong> is a root task and every child task
            filed under it.
            @if (detail.scope.repos.length > 0) {
              Scope: {{ detail.scope.repos.join(", ") }}.
            }
          </p>
        </article>

        @if (detail.state === "draft") {
          <article class="ops-card tq-card" data-testid="qa-exp-draft-card">
            <h2 class="ops-card-title">Draft, not started</h2>
            <p class="tq-lead muted">
              No work trees are enrolled until the experiment starts. Planned
              decision-rule power:
              {{ pct(detail.plan.power.observedEffect.power) }} for
              {{ detail.plan.power.observedEffect.label }} (MDE 80%:
              {{ detail.plan.power.mde80.label }}).
            </p>
          </article>
        } @else if (res()?.assignmentAudit && !res()!.assignmentAudit.passed) {
          <article
            class="ops-card tq-card"
            data-testid="qa-exp-audit-failed-card"
          >
            <h2 class="ops-card-title">Assignment check failed</h2>
            <div class="banner banner-error tq-banner" role="alert">
              <g-status-icon tone="error" kind="error" />
              <span>
                <strong>Readout is blocked.</strong> Enrollment mismatches:
                {{ auditMismatches() }}. Do not make a ship decision from these
                counts.
              </span>
            </div>
          </article>
        } @else if (detail.enrollmentHeld === "engine-change") {
          <article
            class="ops-card tq-card"
            data-testid="qa-exp-engine-held-card"
          >
            <h2 class="ops-card-title">Enrollment held after engine change</h2>
            <div class="banner tq-banner" role="status">
              <g-status-icon tone="blast" kind="warning" />
              <span>
                The quality-check engine digest changed while this experiment
                was running. New work trees are held until you resume enrollment
                in a new segment or stop the experiment.
              </span>
            </div>
          </article>
        }

        @if (
          res() &&
          detail.state !== "draft" &&
          (!res()!.assignmentAudit || res()!.assignmentAudit.passed)
        ) {
          @let r = res()!;
          <g-quality-v3-cards [results]="r" />
          <article class="ops-card tq-card" data-testid="qa-exp-results-card">
            <div class="ops-card-head">
              <div>
                <h2 class="ops-card-title">Results</h2>
                <p class="ops-card-sub">
                  Every row is plotted in improvement units (right of the zero
                  line is better).
                </p>
              </div>
            </div>
            <ul class="tq-verdicts" aria-label="Verdict by arm">
              @for (v of r.verdicts; track v.armId) {
                <li class="tq-verdict" data-testid="qa-verdict">
                  <span [class]="'status-pill ' + verdictTone(v.status)">
                    <g-status-icon
                      [tone]="verdictTone(v.status)"
                      [kind]="verdictKind(v.status)"
                    />
                    {{ v.status }}
                  </span>
                  <p class="tq-sentence">
                    <strong>{{ v.title }}</strong> {{ v.summary }}
                  </p>
                </li>
              }
            </ul>

            @if (r.forestRows.length > 0) {
              <div class="tq-axishead" aria-hidden="true">
                <div></div>
                <div style="display: flex; justify-content: space-between;">
                  <span>worse</span><span>no difference</span
                  ><span>better</span>
                </div>
                <div></div>
              </div>
              @for (row of r.forestRows; track row.id) {
                <div class="tq-frow" [attr.data-testid]="'qa-forest-' + row.id">
                  <div>
                    <span class="tq-term" tabindex="0" [gTip]="row.tip">{{
                      row.label
                    }}</span>
                    <span class="tq-frow-sub">{{ row.sub }}</span>
                  </div>
                  <div class="tq-ftrack" aria-hidden="true">
                    <div class="tq-ftrack-base"></div>
                    <div class="tq-ftrack-zero"></div>
                    <div
                      class="tq-ftrack-ci"
                      [style.left.%]="row.ciLowPct"
                      [style.width.%]="row.ciWidthPct"
                    ></div>
                    <div class="tq-ftrack-pt" [style.left.%]="row.meanPct">
                      ●
                    </div>
                    <div class="tq-ftrack-ends">
                      <span>{{ row.minLabel }}</span>
                      <span>{{ row.maxLabel }}</span>
                    </div>
                  </div>
                  <div class="tabular s-num">
                    <strong>{{ row.valueStr }}</strong>
                    @if (r.provisional) {
                      <span class="tq-prov-tag">valid at planned n only</span>
                    }
                  </div>
                </div>
              }
            }
          </article>

          <div class="ops-grid-2">
            <article
              class="ops-card tq-card"
              data-testid="qa-exp-guardrails-card"
            >
              <div class="ops-card-head">
                <div><h2 class="ops-card-title">Guardrails</h2></div>
              </div>
              <table class="ops-table stack">
                <thead>
                  <tr>
                    <th>Guardrail</th>
                    <th>Value</th>
                    <th>State</th>
                  </tr>
                </thead>
                <tbody>
                  @for (g of r.guardrails; track g.id) {
                    <tr>
                      <td>{{ g.label }}</td>
                      <td class="tabular s-num">{{ g.value }}</td>
                      <td>
                        @if (g.state === "ok") {
                          <span class="status-pill healthy"
                            ><g-status-icon tone="healthy" kind="ok" />ok</span
                          >
                        } @else if (g.state === "watch") {
                          <span class="status-pill blast"
                            ><g-status-icon
                              tone="blast"
                              kind="warning"
                            />watch</span
                          >
                        } @else {
                          <span class="status-pill error"
                            ><g-status-icon
                              tone="error"
                              kind="error"
                            />tripped</span
                          >
                        }
                      </td>
                    </tr>
                  }
                </tbody>
              </table>
            </article>

            <article class="ops-card tq-card" data-testid="card-qa-checks">
              <div class="ops-card-head">
                <div><h2 class="ops-card-title">Check health</h2></div>
              </div>
              <table class="ops-table stack">
                <thead>
                  <tr>
                    <th>Check</th>
                    <th>Precision</th>
                    <th>Applies</th>
                    <th>Time (p95)</th>
                  </tr>
                </thead>
                <tbody>
                  @for (c of r.checkHealth; track c.id) {
                    <tr>
                      <td class="mono">{{ c.id }}</td>
                      <td class="tabular s-num">{{ c.precision }}</td>
                      <td class="tabular s-num">{{ c.applies }}</td>
                      <td class="tabular s-num">{{ c.p95 }}</td>
                    </tr>
                  }
                </tbody>
              </table>
            </article>
          </div>

          <div class="ops-grid-2">
            @if (r.rubric; as rub) {
              <article
                class="ops-card tq-card"
                data-testid="qa-exp-rubric-card"
              >
                <div class="ops-card-head">
                  <div>
                    <h2 class="ops-card-title">Senior review score (1–10)</h2>
                  </div>
                  @if (!rub.validated) {
                    <span class="status-pill blast">
                      <g-status-icon tone="blast" kind="warning" />Not validated
                    </span>
                  }
                </div>
                @for (d of rub.dimensions; track d.id) {
                  <div class="tq-rdim">
                    <strong>{{ d.label }}</strong>
                    @for (a of d.arms; track a.armId) {
                      <div class="tq-rrow">
                        <span>{{ a.armId }}</span>
                        <div class="tq-rtrack" aria-hidden="true">
                          <div class="tq-rtrack-base"></div>
                          <div
                            class="tq-rtrack-ci"
                            [style.left.%]="a.ciLow * 10"
                            [style.width.%]="(a.ciHigh - a.ciLow) * 10"
                          ></div>
                          <div
                            class="tq-rtrack-pt"
                            [style.left.%]="a.mean * 10"
                          >
                            ●
                          </div>
                        </div>
                        <span class="tabular s-num">
                          {{ a.mean.toFixed(2) }}
                          @if (!rub.validated) {
                            <span class="tq-prov-tag">Not validated</span>
                          }
                        </span>
                      </div>
                    }
                  </div>
                }
              </article>
            }

            <article class="ops-card tq-card" data-testid="qa-exp-prshape-card">
              <div class="ops-card-head">
                <div>
                  <h2 class="ops-card-title">PR size</h2>
                  <p class="ops-card-sub">Medians per arm</p>
                </div>
              </div>
              <table class="ops-table stack">
                <thead>
                  <tr>
                    <th>Arm</th>
                    <th>PRs/work tree</th>
                    <th>Prod lines</th>
                    <th>Test lines</th>
                    <th>Test:Prod</th>
                  </tr>
                </thead>
                <tbody>
                  @for (s of r.prShape; track s.armId) {
                    <tr>
                      <td>{{ s.armId }}</td>
                      <td class="tabular s-num">{{ s.prsPerWorkTree }}</td>
                      <td class="tabular s-num">{{ s.prodLines }}</td>
                      <td class="tabular s-num">{{ s.testLines }}</td>
                      <td class="tabular s-num">{{ s.testProdRatio }}</td>
                    </tr>
                  }
                </tbody>
              </table>
            </article>

            <article
              class="ops-card tq-card"
              data-testid="qa-exp-limitations-card"
            >
              <div class="ops-card-head">
                <div><h2 class="ops-card-title">Limitations</h2></div>
              </div>
              <ul class="tq-list">
                @if (r.limitations.sharedMain) {
                  <li>
                    Shared main branch: merged pull requests from one arm enter
                    the repository seen by later tasks in both arms.
                  </li>
                }
                <li>
                  Repository memory mode: {{ r.limitations.memoryMode }} ({{
                    memoryWrites()
                  }}).
                </li>
                @if (r.limitations.exposureBreaks > 0) {
                  <li>
                    {{ r.limitations.exposureBreaks }} work trees experienced a
                    mid-flight kill-switch exposure break (kept under
                    intent-to-treat).
                  </li>
                }
                <li>
                  Model mix balanced across arms:
                  {{ r.modelMix.balanced ? "Yes" : "No (p < 0.01)" }}.
                </li>
              </ul>
              <div class="tq-links" style="margin-top: 12px;">
                <a
                  class="btn-tonal"
                  data-testid="link-iterate-flow"
                  [routerLink]="['/processes', 'quality']"
                  [queryParams]="{ from: primaryTreatmentFlowHash() }"
                  >Iterate on this flow</a
                >
              </div>
            </article>
          </div>
        }
      }
    } @else if (!summary()) {
      @if (error(); as err) {
        <div
          class="banner banner-error tq-error"
          role="alert"
          data-testid="qa-error"
        >
          <span>Could not load quality results: {{ err }}</span>
          <button
            type="button"
            class="btn-text"
            data-testid="qa-retry"
            (click)="retry.emit()"
          >
            Try again
          </button>
        </div>
      } @else {
        <article
          class="ops-card tq-card"
          aria-busy="true"
          data-testid="qa-loading"
        >
          <h2 class="ops-card-title">Loading quality results…</h2>
          <div class="tq-skel" aria-hidden="true"><i></i><i></i><i></i></div>
        </article>
      }
    } @else {
      @let qa = summary()!;
      @if (isOff()) {
        <article class="ops-card tq-card tq-off" data-testid="card-qa-rollout">
          <div class="ops-card-head tq-head">
            <div class="tq-head-text">
              <h2 class="ops-card-title">Quality checks</h2>
              <p class="ops-card-sub">
                Compare pull requests made with and without pre-PR quality
                checks.
              </p>
            </div>
            <span class="status-pill neutral" data-testid="qa-rollout-pill">
              <g-status-icon tone="neutral" kind="idle" />
              Off
            </span>
          </div>
          <p class="tq-lead" data-testid="qa-rollout-empty">
            Quality control is off on this deployment. Turn on a rollout in
            Settings → Tasks: each new work tree is then assigned either to run
            with the checks or without them, and this tab compares the two.
          </p>
          <div class="tq-links">
            <a
              class="btn-tonal"
              routerLink="/settings/tasks"
              data-testid="qa-configure-link"
            >
              Settings → Tasks
            </a>
            @if (canAdmin()) {
              <button
                type="button"
                class="btn-tonal"
                data-testid="btn-new-experiment"
                (click)="showCreateSheet.set(true)"
              >
                New experiment
              </button>
            }
            <a
              class="btn-text"
              routerLink="/processes/quality"
              data-testid="qa-rules-link"
            >
              See the checks
            </a>
          </div>
        </article>
      } @else {
        <article
          class="ops-card tq-card"
          data-testid="card-qa-rollout"
          aria-labelledby="tq-results-title"
        >
          <div class="ops-card-head tq-head">
            <div class="tq-head-text">
              <h2 class="ops-card-title" id="tq-results-title">
                Rollout results
              </h2>
              <p class="ops-card-sub">
                A work tree is a root task and every child task filed under it.
                Each runs entirely in one arm: with quality checks ({{ arm() }})
                or without them (control).
              </p>
            </div>
            <div class="headline-actions">
              @if (expOptions().length > 1) {
                <g-pill-select
                  testid="pill-qa-experiment"
                  ariaLabel="Experiment"
                  [options]="expOptions()"
                  [value]="selectedExpId() || expOptions()[0].value"
                  (valueChange)="setExp($event)"
                />
              }
              <span
                [class]="'status-pill tabular ' + rollout().tone"
                data-testid="qa-rollout-pill"
                [gTip]="rollout().tip"
                tabindex="0"
              >
                <g-status-icon
                  [tone]="rollout().tone"
                  [kind]="rollout().kind"
                />
                {{ rollout().label }}
              </span>
              @if (canAdmin()) {
                <button
                  type="button"
                  class="btn-tonal"
                  data-testid="btn-new-experiment"
                  (click)="showCreateSheet.set(true)"
                >
                  New experiment
                </button>
              }
              <a
                class="btn-tonal"
                routerLink="/settings/tasks"
                data-testid="qa-configure-link"
              >
                Configure
              </a>
            </div>
          </div>

          @if (period(); as p) {
            <p class="tq-scope" data-testid="qa-scope">
              <span class="status-pill neutral" data-testid="qa-period-chip">
                <g-status-icon tone="neutral" kind="progress" />
                {{ p }}
              </span>
            </p>
          } @else {
            <p class="tq-scope" data-testid="qa-scope">
              Covers work trees assigned in the last {{ windowWords()
              }}{{ repoWords() }}
              ·
              <span
                class="tq-term"
                tabindex="0"
                [gTip]="
                  'Epoch ' +
                  qa.epoch +
                  ' started when the rollout last changed; regime ' +
                  qa.regimeId +
                  ' is the current rollout percentage step. Changing the window above changes these results.'
                "
                >epoch {{ qa.epoch }}</span
              >
            </p>
          }

          @if ((qa.circuitBreaker.reasons?.length ?? 0) > 0) {
            <p
              class="banner banner-error tq-banner"
              role="status"
              data-testid="qa-circuit-breaker-banner"
            >
              <g-status-icon tone="error" kind="blocked" />
              <span>
                Safety guardrail tripped:
                {{ (qa.circuitBreaker.reasons ?? []).join(" · ") }}. New work
                trees run without enforcement until the rollout is reset.
              </span>
            </p>
          }

          @if (noAssignments()) {
            <p class="tq-lead" data-testid="qa-rollout-empty">
              Waiting for the first work trees under epoch {{ qa.epoch }}.
              Results appear once both arms have work.
            </p>
          } @else {
            <ul class="tq-verdicts" aria-label="Verdict by arm">
              @for (v of verdicts(); track v.arm) {
                <li
                  class="tq-verdict"
                  data-testid="qa-verdict"
                  [attr.data-state]="v.state"
                >
                  <span
                    [class]="'status-pill ' + v.tone"
                    data-testid="qa-verdict-pill"
                    tabindex="0"
                    [gTip]="v.tip || null"
                  >
                    <g-status-icon [tone]="v.tone" [kind]="v.kind" />
                    {{ v.arm }} · {{ v.label }}
                  </span>
                  <p class="tq-sentence" data-testid="qa-verdict-sentence">
                    {{ v.sentence }}
                  </p>
                  @if (v.need) {
                    <p class="tq-need" data-testid="qa-verdict-need">
                      {{ v.need }}
                    </p>
                  }
                </li>
              }
            </ul>

            <div
              class="tq-table"
              role="table"
              aria-label="Rollout measures by arm"
              data-testid="chart-qa-rollout"
            >
              <div class="tq-row tq-row-head" role="row">
                <span role="columnheader">Measure</span>
                <span role="columnheader" class="tabular"
                  >{{ arm() }} · {{ qa.treatment.assignedOrigins }} work
                  trees</span
                >
                <span role="columnheader" class="tabular"
                  >Control · {{ qa.control.assignedOrigins }} work trees</span
                >
                <span role="columnheader">Difference</span>
              </div>
              @for (m of metrics(); track m.id) {
                <div
                  class="tq-row"
                  role="row"
                  [attr.data-testid]="'qa-metric-' + m.id"
                >
                  <span role="rowheader">
                    <span class="tq-term" tabindex="0" [gTip]="m.tip">{{
                      m.label
                    }}</span>
                  </span>
                  <span role="cell" class="tabular" [attr.data-arm]="arm()">{{
                    m.treatment
                  }}</span>
                  <span role="cell" class="tabular" data-arm="Control">{{
                    m.control
                  }}</span>
                  <span
                    role="cell"
                    class="tabular tq-diff"
                    data-arm="Difference"
                    >{{ m.diff }}</span
                  >
                </div>
              }
            </div>
            <p class="tq-foot muted">
              {{ qa.treatment.prsOpened }} vs {{ qa.control.prsOpened }} pull
              requests opened · {{ qa.treatment.heldByQaGate }} held by the
              quality gate.
              @if (!costShown()) {
                Dollar cost was not measured for these runs, so usage is shown
                in tokens.
              }
            </p>
          }
        </article>

        <article
          class="ops-card tq-card"
          data-testid="card-qa-checks"
          aria-labelledby="tq-checks-title"
        >
          <div class="ops-card-head tq-head">
            <div class="tq-head-text">
              <h2 class="ops-card-title" id="tq-checks-title">Check health</h2>
              <p class="ops-card-sub">
                On {{ arm() }} pull requests a failing check blocks the PR. On
                control pull requests checks run in shadow: they never block and
                only record what would have happened.
              </p>
            </div>
            <a
              class="btn-tonal"
              routerLink="/processes/quality"
              data-testid="qa-rules-link"
            >
              Manage checks
            </a>
          </div>

          @if (qa.shadowRecall.controlTasksChecked > 0) {
            <p class="tq-lead tabular" data-testid="qa-shadow-summary">
              On control, shadow checks would have blocked
              {{ qa.shadowRecall.shadowWouldBlock }} of
              {{ qa.shadowRecall.controlTasksChecked }} tasks ({{
                pct(qa.shadowRecall.shadowCatchRate)
              }}).
              @if (matched(); as mr) {
                <span
                  class="tq-term"
                  tabindex="0"
                  gTip="Escapes whose file, line and problem class match a shadow finding, divided by all escapes on control."
                  >They matched
                  @if (mr.matched !== null) {
                    {{ mr.matched }} of
                    {{ qa.shadowRecall.controlBlindEscapes }}
                  }
                  escapes found later ({{ pct(mr.rate) }}).</span
                >
              } @else if (qa.shadowRecall.controlBlindEscapes > 0) {
                <span
                  class="tq-term"
                  tabindex="0"
                  gTip="Counts control tasks where a shadow check flagged something and an escape was also found. It is PR co-occurrence overlap, not matched findings."
                  >PR co-occurrence (not matched file:line findings):
                  {{ pct(qa.shadowRecall.pairedRecall) }}
                  of escapes.</span
                >
              }
            </p>
          }

          @if (qa.checks.length === 0) {
            <p class="tq-lead muted" data-testid="qa-checks-empty">
              No pre-PR quality check runs recorded in this window.
            </p>
          } @else {
            <ul
              class="tq-checks"
              aria-label="Health of each quality check"
              data-testid="qa-checks-table"
            >
              @for (c of qa.checks; track c.stage + ":" + c.ruleId) {
                <li class="tq-check" [attr.data-rule]="c.ruleId">
                  <div class="tq-check-top">
                    <strong class="tq-check-name">{{ c.ruleId }}</strong>
                    <span
                      class="muted tq-term"
                      tabindex="0"
                      [gTip]="'Stage ' + c.stage"
                      >{{ stage(c.stage) }}</span
                    >
                    @if (c.errorCount > 0) {
                      <span
                        class="status-pill error tabular"
                        data-testid="qa-check-errors"
                      >
                        <g-status-icon tone="error" kind="error" />
                        {{ c.errorCount }}
                        {{ c.errorCount === 1 ? "error" : "errors" }}
                      </span>
                    }
                  </div>
                  <dl class="tq-check-stats tabular">
                    <div>
                      <dt>Runs</dt>
                      <dd>
                        {{ c.enforcedRuns }} blocking ·
                        {{ c.shadowRuns }} shadow
                      </dd>
                    </div>
                    <div>
                      <dt>Flagged</dt>
                      <dd>
                        {{ c.failCount }} of
                        {{ c.enforcedRuns + c.shadowRuns }} ({{
                          pct(c.catchRate)
                        }})
                      </dd>
                    </div>
                    <div>
                      <dt>Findings</dt>
                      <dd>{{ c.findingsCount }}</dd>
                    </div>
                    <div>
                      <dt>
                        <span
                          class="tq-term"
                          tabindex="0"
                          [gTip]="precisionTip(c)"
                          >Precision</span
                        >
                      </dt>
                      <dd data-testid="qa-check-precision">
                        {{ precisionText(c) }}
                      </dd>
                    </div>
                    @if (c.helpfulVotes + c.notHelpfulVotes > 0) {
                      <div>
                        <dt>
                          <span
                            class="tq-term"
                            tabindex="0"
                            [gTip]="
                              'Operator votes on findings. Not-helpful share ' +
                              pct(c.notHelpfulRate) +
                              ', Wilson lower bound ' +
                              pct(c.notHelpfulWilsonLb) +
                              '.'
                            "
                            >Votes</span
                          >
                        </dt>
                        <dd>
                          {{ c.helpfulVotes }} helpful ·
                          {{ c.notHelpfulVotes }} not helpful
                        </dd>
                      </div>
                    }
                    <div>
                      <dt>
                        <span
                          class="tq-term"
                          tabindex="0"
                          gTip="Median / 95th percentile run time"
                          >Time</span
                        >
                      </dt>
                      <dd>
                        {{ ms(c.p50DurationMs) }} / {{ ms(c.p95DurationMs) }}
                      </dd>
                    </div>
                  </dl>
                </li>
              }
            </ul>
          }
        </article>
      }
    }

    @if (showCreateSheet()) {
      <g-qa-experiment-sheet
        [preselectFlow]="createExpFlow()"
        (dismiss)="showCreateSheet.set(false)"
        (done)="onCreated($event)"
      />
    }

    @if (showStartDialog() && exp()) {
      @let detail = exp()!;
      <g-qa-start-dialog
        [expId]="detail.id"
        [n]="detail.plan.plannedOriginsPerArm * detail.arms.length"
        [flowHash]="primaryTreatmentFlowHash()"
        [splitLabel]="splitLabel(detail)"
        [scopeLabel]="
          detail.scope.repos.length > 0
            ? detail.scope.repos.join(', ')
            : 'configured repositories'
        "
        (dismiss)="showStartDialog.set(false)"
        (done)="onStarted()"
      />
    }
  `,
})
export class TelemetryQualityComponent implements OnInit {
  readonly summary = input<QASummary | null>(null);
  readonly error = input<string | null>(null);
  readonly deniedReason = input<string | null>(null);
  readonly windowId = input<string>("7d");
  readonly repo = input<string>("");
  readonly retry = output<void>();

  private readonly route = inject(ActivatedRoute);
  private readonly router = inject(Router);
  private readonly api = inject(GrainApi);
  private readonly access = inject(AccessService);
  private readonly snackbar = inject(SnackbarService);

  protected readonly pct = formatPct;
  protected readonly ms = formatCheckDurationMs;
  protected readonly stage = stageWords;

  readonly expLoading = signal(false);
  readonly expError = signal("");
  readonly exps = signal<QAExperimentSummary[]>([]);
  readonly selectedExpId = signal<string>("");
  readonly exp = signal<QAExperimentDetail | null>(null);
  readonly res = signal<QAExperimentResults | null>(null);

  readonly showCreateSheet = signal(false);
  readonly showStartDialog = signal(false);
  readonly createExpFlow = signal<string | null>(null);

  readonly expDenied = computed(
    () => !this.deniedReason() && !this.summary() && !this.canReadExperiments(),
  );

  readonly activeV2Mode = computed(() => {
    const id = this.selectedExpId();
    if (!id || id.startsWith("legacy")) return false;
    return (
      this.exps().some((e) => e.id === id && !e.legacy) ||
      this.exp()?.id === id ||
      this.expLoading() ||
      Boolean(this.expError())
    );
  });

  readonly expOptions = computed<PillOption[]>(() => {
    const list = this.exps();
    const ordered = [
      ...list.filter((e) => e.state === "running"),
      ...list.filter(
        (e) => e.state === "stopped" && !e.id.startsWith("legacy"),
      ),
      ...list.filter((e) => e.state === "draft"),
      ...list.filter(
        (e) => e.state === "archived" || e.id.startsWith("legacy"),
      ),
    ];
    return ordered.map((e) => ({ value: e.id, label: e.name }));
  });

  readonly haltableArmId = computed<string | null>(() => {
    const d = this.exp();
    if (!d) return null;
    const activeTreatments = d.arms.filter(
      (a) => a.id !== "control" && !a.halted,
    );
    return activeTreatments.length >= 2 ? activeTreatments[0].id : null;
  });

  constructor() {
    this.route.queryParams.pipe(takeUntilDestroyed()).subscribe((q) => {
      if (q["createExp"]) {
        this.createExpFlow.set(
          typeof q["flow"] === "string" ? q["flow"] : null,
        );
        this.showCreateSheet.set(true);
        void this.router.navigate([], {
          queryParams: { createExp: null, flow: null },
          queryParamsHandling: "merge",
          replaceUrl: true,
        });
      }
      const qpExp = typeof q["exp"] === "string" ? q["exp"] : "";
      if (qpExp && qpExp !== this.selectedExpId()) {
        this.selectedExpId.set(qpExp);
        if (!qpExp.startsWith("legacy")) {
          void this.loadExp(qpExp);
        }
      }
    });
  }

  ngOnInit(): void {
    void this.loadExperiments();
  }

  canReadExperiments(): boolean {
    return this.access.can("GET /api/qa/experiments");
  }

  canAdmin(): boolean {
    return this.access.can("POST /api/qa/experiments");
  }

  async loadExperiments(): Promise<void> {
    if (!this.canReadExperiments()) return;
    this.expError.set("");
    try {
      const resp = (await this.api
        .get("GET /api/qa/experiments", undefined, "limit=50")
        .catch(() => null)) as { experiments?: QAExperimentSummary[] } | null;
      const list = Array.isArray(resp?.experiments) ? resp.experiments : [];
      this.exps.set(list);
      const qpExp = this.route.snapshot.queryParams["exp"];
      const preferred =
        (typeof qpExp === "string" && list.find((e) => e.id === qpExp)?.id) ||
        list.find((e) => e.state === "running" && !e.id.startsWith("legacy"))
          ?.id ||
        list.find((e) => !e.id.startsWith("legacy"))?.id ||
        "";
      if (preferred) {
        this.selectedExpId.set(preferred);
        await this.loadExp(preferred);
      }
    } catch (e: unknown) {
      this.expError.set(String(e));
    }
  }

  setExp(id: string): void {
    this.selectedExpId.set(id);
    void this.router.navigate([], {
      queryParams: { exp: id },
      queryParamsHandling: "merge",
    });
    if (!id.startsWith("legacy")) {
      void this.loadExp(id);
    }
  }

  async loadExp(id?: string): Promise<void> {
    if (!id || id.startsWith("legacy") || !this.canReadExperiments()) return;
    this.expLoading.set(true);
    this.expError.set("");
    try {
      const rawDetail = (await this.api.get("GET /api/qa/experiments/{id}", {
        id,
      })) as QAExperimentDetail | { experiment: QAExperimentDetail } | null;
      if (!rawDetail) return;
      const detail =
        "experiment" in rawDetail ? rawDetail.experiment : rawDetail;
      this.exp.set(detail);
      if (detail.state !== "draft") {
        const results = (await this.api.get(
          "GET /api/qa/experiments/{id}/results",
          { id },
        )) as QAExperimentResults | null;
        this.res.set(results);
      } else {
        this.res.set(null);
      }
    } catch (e: unknown) {
      this.expError.set(String(e));
    } finally {
      this.expLoading.set(false);
    }
  }

  enrolledForArm(armId: string): number {
    return (
      this.res()?.srm.counts[armId] ?? this.exp()?.enrolledByArm?.[armId] ?? 0
    );
  }

  primaryTreatmentFlowHash(): string {
    const arms = this.exp()?.arms ?? [];
    const treat = arms.find((a) => a.id !== "control") ?? arms[1] ?? arms[0];
    return treat?.flowHash ?? "";
  }

  splitLabel(detail: QAExperimentDetail): string {
    return detail.arms.map((a) => `${a.label} ${a.weight}%`).join(" / ");
  }

  verdictTone(
    status: string,
  ): "healthy" | "reply" | "error" | "blast" | "neutral" {
    switch (status.toLowerCase()) {
      case "better":
        return "healthy";
      case "provisional":
      case "leading":
        return "reply";
      case "halted":
      case "worse":
        return "error";
      case "trailing":
        return "blast";
      default:
        return "neutral";
    }
  }

  verdictKind(status: string): string {
    switch (status.toLowerCase()) {
      case "better":
        return "ok";
      case "provisional":
      case "leading":
        return "progress";
      case "halted":
      case "worse":
        return "error";
      case "trailing":
        return "warning";
      default:
        return "idle";
    }
  }

  auditMismatches(): string {
    return (
      this.res()
        ?.assignmentAudit.mismatches.map(
          (m) =>
            `${m.rootOrigin} (expected ${m.expectedArm}, got ${m.actualArm})`,
        )
        .join(", ") || "none"
    );
  }

  memoryWrites(): string {
    const entries = Object.entries(
      this.res()?.limitations.memoryWritesByArm ?? {},
    );
    if (entries.length === 0) return "0 writes";
    return entries.map(([k, v]) => `${v} from ${k}`).join(", ");
  }

  openStartDialog(): void {
    this.showStartDialog.set(true);
  }

  async haltSingleArm(armId: string): Promise<void> {
    await this.transition("halt-arm", armId);
  }

  async transition(action: string, armId?: string): Promise<void> {
    const id = this.selectedExpId() || this.exp()?.id;
    if (!id) return;
    try {
      const body: Record<string, unknown> = {
        action,
        reason: `Operator ${action} from Telemetry Quality view`,
      };
      if (action === "stop") {
        body["decision"] = "inconclusive";
      }
      if (armId) {
        body["arm"] = armId;
      }
      await this.api.post(
        "POST /api/qa/experiments/{id}/transition",
        { id },
        body,
      );
      this.snackbar.show(`Experiment updated (${action}).`);
      await this.loadExperiments();
    } catch (e: unknown) {
      this.snackbar.show(`Failed to update experiment: ${String(e)}`);
    }
  }

  onCreated(id: string): void {
    this.showCreateSheet.set(false);
    void this.loadExperiments().then(() => this.setExp(id));
  }

  onStarted(): void {
    this.showStartDialog.set(false);
    void this.loadExperiments();
  }

  // Legacy summary computed helpers (M7 / v1 epoch fallback)
  protected readonly arm = computed(() => {
    const qa = this.summary();
    return qa ? armLabel(qa) : "Treatment";
  });

  protected readonly noAssignments = computed(() => {
    const qa = this.summary();
    return (
      !qa ||
      (qa.treatment?.assignedOrigins ?? 0) +
        (qa.control?.assignedOrigins ?? 0) ===
        0
    );
  });

  protected readonly isOff = computed(() => {
    const qa = this.summary();
    return (
      !!qa && !qa.enabled && this.noAssignments() && qa.checks.length === 0
    );
  });

  protected readonly verdicts = computed(() => {
    const qa = this.summary();
    return qa ? armVerdicts(qa) : [];
  });

  protected readonly period = computed(() => {
    const e = experimentScope(this.summary());
    return e ? experimentPeriodLabel(e) : null;
  });

  protected readonly matched = computed(() => {
    const qa = this.summary();
    return qa ? matchedRecall(qa) : null;
  });

  protected readonly costShown = computed(() => {
    const qa = this.summary();
    return !!qa && costMeasured(qa);
  });

  protected readonly windowWords = computed(() => {
    switch (this.windowId()) {
      case "24h":
        return "24 hours";
      case "30d":
        return "30 days";
      default:
        return "7 days";
    }
  });

  protected readonly repoWords = computed(() =>
    this.repo() ? ` in ${this.repo()}` : " across all repositories",
  );

  protected readonly rollout = computed(() => {
    const qa = this.summary();
    if (!qa) return { tone: "neutral", kind: "idle", label: "Off", tip: "" };
    const tip = `Rollout: ${qa.rolloutPct}% of new work trees run with ${qa.flowId}${qa.shadowOnControl ? "; control runs the checks in shadow" : ""}.`;
    if (
      qa.circuitBreaker.shouldAutoDowngrade ||
      (qa.enabled && qa.rolloutPct === 0 && qa.regimeId > 1)
    ) {
      return {
        tone: "blast",
        kind: "warning",
        label: `0% shadow-only · ${qa.flowId}`,
        tip,
      };
    }
    if (qa.enabled) {
      return {
        tone: "healthy",
        kind: "running",
        label: `${qa.rolloutPct}% · ${qa.flowId}`,
        tip,
      };
    }
    return {
      tone: "neutral",
      kind: "idle",
      label: "Off",
      tip: "Rollout is off; results below are from work trees assigned before it was turned off.",
    };
  });

  protected readonly metrics = computed<MetricRow[]>(() => {
    const qa = this.summary();
    if (!qa) return [];
    const t = qa.treatment;
    const c = qa.control;
    const of = (n: number, d: number) =>
      `${formatPct(d ? n / d : Number.NaN)} (${n} of ${d})`;
    const rows: MetricRow[] = [
      {
        id: "escapes",
        label: "Work trees with an escape",
        tip: `An escape is a problem found after the PR opened, by blind review or a failed first-push CI run. Wilson 95% intervals: ${formatPct(t.blindEscapeCiLow)}–${formatPct(t.blindEscapeCiHigh)} vs ${formatPct(c.blindEscapeCiLow)}–${formatPct(c.blindEscapeCiHigh)}.`,
        treatment: of(t.blindEscapes, t.assignedOrigins),
        control: of(c.blindEscapes, c.assignedOrigins),
        diff: rateDiffWords(t.blindEscapeRate, c.blindEscapeRate),
      },
      {
        id: "first-push-ci",
        label: "First CI run green",
        tip: "Share of pull requests whose first CI run passed.",
        treatment: of(t.firstPushCiGreen, t.firstPushCiEvaluated),
        control: of(c.firstPushCiGreen, c.firstPushCiEvaluated),
        diff: rateDiffWords(t.firstPushCiGreenRate, c.firstPushCiGreenRate),
      },
      {
        id: "churn",
        label: "Fixed again within 7 days of merge",
        tip: "Merged pull requests that needed a follow-up repair within 7 days (post-merge churn).",
        treatment: of(t.postMergeChurn7d, t.prsMerged),
        control: of(c.postMergeChurn7d, c.prsMerged),
        diff: rateDiffWords(t.postMergeChurn7dRate, c.postMergeChurn7dRate),
      },
    ];
    const isCostZero =
      (qa as QASummary & { costMeasured?: boolean }).costMeasured === false;
    rows.push(
      this.timeRow(
        "time-to-pr",
        "Time to PR (median)",
        "Time to PR, per task",
        qa.overallTimeToPrSeconds,
      ),
      this.timeRow(
        "fast-lane",
        "Time to PR, small tasks",
        "Time to PR for low-risk tasks (guardrail)",
        qa.fastLaneTimeToPrSeconds,
      ),
      this.timeRow(
        "time-to-land",
        "Time to land a work tree",
        "Time from a work tree's first task to its last merge",
        qa.originTimeToLandSeconds,
      ),
      {
        id: "tokens",
        label: "Tokens per work tree",
        tip:
          cupedTip("Tokens per work tree", qa.tokensCuped, tokenWords) +
          (isCostZero
            ? " Token usage (provider does not report dollar billing)."
            : ""),
        treatment: qa.tokensCuped.treatN
          ? tokenWords(qa.tokensCuped.adjTreatMean)
          : "—",
        control: qa.tokensCuped.ctrlN
          ? tokenWords(qa.tokensCuped.adjCtrlMean)
          : "—",
        diff: relDiffWords(qa.tokensCuped, "more tokens", "fewer tokens"),
      },
    );
    if (costMeasured(qa)) {
      const usd = (v: number) => `$${v.toFixed(2)}`;
      rows.push({
        id: "cost",
        label: "Cost per work tree",
        tip: cupedTip("Measured cost per work tree", qa.costCuped, usd),
        treatment: qa.costCuped.treatN ? usd(qa.costCuped.adjTreatMean) : "—",
        control: qa.costCuped.ctrlN ? usd(qa.costCuped.adjCtrlMean) : "—",
        diff: relDiffWords(qa.costCuped, "higher cost", "lower cost"),
      });
    }
    rows.push(
      {
        id: "prompt",
        label: "Prompt added by quality guidance",
        tip: `Share of prompt bytes taken by the injected quality guidance, over ${t.promptsMeasured} measured prompts. Measured in bytes, not tokens.`,
        treatment: `${formatPct(t.qaSkillPromptByteShare)} of prompt bytes`,
        control: `${formatPct(c.qaSkillPromptByteShare)} of prompt bytes`,
        diff: rateDiffWords(t.qaSkillPromptByteShare, c.qaSkillPromptByteShare),
      },
      {
        id: "thread-cuts",
        label: "Conversations trimmed for length",
        tip: "Share of agent runs whose context was cut to fit the model window.",
        treatment: `${formatPct(t.threadCuts ? t.threadCutShare : 0)} (${t.threadCuts})`,
        control: `${formatPct(c.threadCuts ? c.threadCutShare : 0)} (${c.threadCuts})`,
        diff: rateDiffWords(t.threadCutShare, c.threadCutShare),
      },
    );
    return rows;
  });

  private timeRow(
    id: string,
    label: string,
    tipLabel: string,
    hl: QASummary["overallTimeToPrSeconds"],
  ): MetricRow {
    return {
      id,
      label,
      tip: timeTip(tipLabel, hl),
      treatment: hl?.treatN ? durWords(hl.treatMedian) : "—",
      control: hl?.ctrlN ? durWords(hl.ctrlMedian) : "—",
      diff: timeDiffWords(hl),
    };
  }

  protected precisionText(c: QACheckRuleSummary): string {
    const p = checkPrecision(c);
    if (p === null) return "not measured";
    const tp = (c as QACheckRuleSummary & { confirmedTruePositives?: number })
      .confirmedTruePositives;
    return typeof tp === "number"
      ? `${formatPct(p)} (${tp} confirmed)`
      : formatPct(p);
  }

  protected precisionTip(c: QACheckRuleSummary): string {
    return checkPrecision(c) === null
      ? "Share of flags confirmed as real problems. Not reported for this check yet, so a high flag count does not mean real problems were caught."
      : "Share of flags confirmed as real problems (by an operator vote or a substantive fix).";
  }
}
