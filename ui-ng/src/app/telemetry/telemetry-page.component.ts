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
  ElementRef,
  type OnInit,
  ViewEncapsulation,
  computed,
  inject,
  signal,
  viewChild,
} from "@angular/core";
import { ActivatedRoute, Router, RouterLink } from "@angular/router";
import { GrainApi } from "../api/grain-api.service";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import { TaskFeedStore } from "../services/task-feed.service";
import { ViewFilter } from "../services/view-filter.service";
import { errorText } from "../sessions/session-view";
import { ActorChipComponent } from "../shared/actor-chip.component";
import { StatusIconComponent } from "../shared/status-icon.component";
import { TipDirective } from "../shared/tip.directive";
import {
  type ClusterStats,
  type MetricsReport,
  type MetricsWindow,
  type ReleaseNote,
  type TokenBudget,
  type UsageResponse,
  buildAlphabeticalContributors,
  buildThroughputGeometry,
  formatCompactNum,
  formatDurationSec,
  formatPct,
} from "./telemetry.models";

const WINDOWS: readonly { id: MetricsWindow; label: string }[] = [
  { id: "24h", label: "24h" },
  { id: "7d", label: "7d" },
  { id: "30d", label: "30d" },
];

const SPACE_PERIODS: readonly { id: string; label: string }[] = [
  { id: "last-week", label: "Last week" },
  { id: "this-week", label: "This week" },
  { id: "this-month", label: "This month" },
];

const BUDGET_WINDOWS: readonly string[] = ["5h", "24h", "168h"];

@Component({
  selector: "g-telemetry-page",
  imports: [RouterLink, TipDirective, StatusIconComponent, ActorChipComponent],
  encapsulation: ViewEncapsulation.None,
  styleUrls: ["../shared/ops-surface.css", "./telemetry-page.component.css"],
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: {
    "(document:click)": "onDocClick($event)",
    "(document:keydown.escape)": "onEscape($event)",
  },
  template: `
    <section
      class="view-panel is-active"
      aria-labelledby="telemetry-headline"
      data-testid="telemetry-page"
    >
      <div class="stage-header">
        <div class="view-headline-row">
          <div class="headline-cluster">
            <h1
              class="view-headline"
              id="telemetry-headline"
              tabindex="-1"
              data-testid="telemetry-headline"
            >
              Telemetry
            </h1>
            <span class="status-pill neutral">
              <g-status-icon tone="neutral" kind="progress" />
              {{ window() }} window
            </span>
          </div>
          <div class="headline-actions">
            <button
              type="button"
              class="btn-tonal"
              data-testid="telemetry-refresh"
              [disabled]="loading()"
              (click)="refreshAll()"
            >
              Refresh
            </button>
          </div>
        </div>
        <p class="view-subtitle">
          Delivery throughput, cycle-time stages, CI and merge-queue health,
          token budgets, and alphabetical team activity.
        </p>

        <div
          class="ops-toolbar"
          role="toolbar"
          aria-label="Telemetry window and scope"
        >
          <div class="segmented" role="group" aria-label="Time window">
            @for (w of windows; track w.id) {
              <button
                type="button"
                [attr.aria-pressed]="window() === w.id"
                [attr.data-testid]="'telemetry-window-' + w.id"
                (click)="pickWindow(w.id)"
              >
                {{ w.label }}
              </button>
            }
          </div>

          <div class="pop-wrap">
            <button
              #repoTrigger
              type="button"
              class="pop-trigger"
              [class.is-active]="!!selectedRepo()"
              data-testid="telemetry-repo-trigger"
              aria-haspopup="listbox"
              [attr.aria-expanded]="openMenu() === 'repo'"
              aria-controls="telemetry-repo-menu"
              (click)="toggleMenu('repo', $event)"
              (keydown)="onMenuTriggerKeydown('repo', $event)"
            >
              <span>{{
                selectedRepo() ? "Repo: " + selectedRepo() : "All repositories"
              }}</span>
              <svg
                width="16"
                height="16"
                viewBox="0 0 24 24"
                aria-hidden="true"
              >
                <path d="M7 10l5 5 5-5z" fill="currentColor" />
              </svg>
            </button>
            @if (openMenu() === "repo") {
              <ul
                #repoListbox
                class="pop-menu"
                id="telemetry-repo-menu"
                role="listbox"
                tabindex="-1"
                aria-label="Filter by repository"
                data-testid="telemetry-repo-menu"
                (keydown)="onListboxKeydown('repo', $event)"
              >
                <li role="none">
                  <button
                    type="button"
                    class="pop-item"
                    role="option"
                    [disabled]="!canReadAllReposMetrics()"
                    [attr.aria-selected]="!selectedRepo()"
                    (click)="pickRepo('')"
                  >
                    <span>All repositories</span>
                    @if (!selectedRepo()) {
                      <span aria-hidden="true">✓</span>
                    }
                  </button>
                </li>
                @for (r of repoOptions(); track r) {
                  <li role="none">
                    <button
                      type="button"
                      class="pop-item"
                      role="option"
                      [attr.aria-selected]="selectedRepo() === r"
                      [attr.data-testid]="'telemetry-repo-opt-' + r"
                      (click)="pickRepo(r)"
                    >
                      <span class="tabular">{{ r }}</span>
                      @if (selectedRepo() === r) {
                        <span aria-hidden="true">✓</span>
                      }
                    </button>
                  </li>
                }
              </ul>
            }
          </div>

          <div class="pop-wrap">
            <button
              #authorTrigger
              type="button"
              class="pop-trigger"
              [class.is-active]="!!selectedAuthor()"
              data-testid="telemetry-author-trigger"
              aria-haspopup="listbox"
              [attr.aria-expanded]="openMenu() === 'author'"
              aria-controls="telemetry-author-menu"
              (click)="toggleMenu('author', $event)"
              (keydown)="onMenuTriggerKeydown('author', $event)"
            >
              <span>{{
                selectedAuthor()
                  ? "Author: " + selectedAuthor()
                  : "All contributors"
              }}</span>
              <svg
                width="16"
                height="16"
                viewBox="0 0 24 24"
                aria-hidden="true"
              >
                <path d="M7 10l5 5 5-5z" fill="currentColor" />
              </svg>
            </button>
            @if (openMenu() === "author") {
              <ul
                #authorListbox
                class="pop-menu"
                id="telemetry-author-menu"
                role="listbox"
                tabindex="-1"
                aria-label="Filter by contributor"
                data-testid="telemetry-author-menu"
                (keydown)="onListboxKeydown('author', $event)"
              >
                <li role="none">
                  <button
                    type="button"
                    class="pop-item"
                    role="option"
                    [attr.aria-selected]="!selectedAuthor()"
                    (click)="pickAuthor('')"
                  >
                    <span>All contributors</span>
                    @if (!selectedAuthor()) {
                      <span aria-hidden="true">✓</span>
                    }
                  </button>
                </li>
                @for (a of authorOptions(); track a) {
                  <li role="none">
                    <button
                      type="button"
                      class="pop-item"
                      role="option"
                      [attr.aria-selected]="selectedAuthor() === a"
                      (click)="pickAuthor(a)"
                    >
                      <span>{{ a }}</span>
                      @if (selectedAuthor() === a) {
                        <span aria-hidden="true">✓</span>
                      }
                    </button>
                  </li>
                }
              </ul>
            }
          </div>

          @if (
            (selectedRepo() && canReadAllReposMetrics()) || selectedAuthor()
          ) {
            <button
              type="button"
              class="btn-text"
              data-testid="telemetry-clear-scope"
              (click)="clearScope()"
            >
              Clear scope
            </button>
          }
        </div>
      </div>

      @if (loadError(); as err) {
        <p
          class="banner banner-error"
          role="alert"
          data-testid="telemetry-error"
        >
          {{ err }}
        </p>
      }

      @if (!canReadMetrics()) {
        <p class="empty-state" data-testid="telemetry-denied">
          {{ deniedMetricsReason(selectedRepo()) }}
        </p>
      } @else {
        <div class="ops-scroll">
          <!-- Headline KPI Cards -->
          <div class="ops-kpi-grid" data-testid="telemetry-kpis">
            <article class="ops-kpi-card" data-testid="kpi-merged">
              <div class="ops-kpi-top">
                <span class="ops-kpi-label">Merged &amp; Completed</span>
                <span class="status-pill healthy">
                  <g-status-icon tone="healthy" kind="merged" />
                  {{ fmtNum(report()?.merges?.landedPerDay ?? 0) }}/d
                </span>
              </div>
              <div class="ops-kpi-value tabular">
                {{ report()?.merges?.landed ?? 0 }} merged
              </div>
              <div class="ops-kpi-sub tabular">
                {{ report()?.throughput?.tasksCompleted ?? 0 }} tasks completed
                · {{ report()?.throughput?.tasksFiled ?? 0 }} filed
              </div>
            </article>

            <article class="ops-kpi-card" data-testid="kpi-cycle">
              <div class="ops-kpi-top">
                <span class="ops-kpi-label">Median Cycle Time</span>
                <span class="status-pill neutral">
                  <g-status-icon tone="neutral" kind="progress" />
                  p50
                </span>
              </div>
              <div class="ops-kpi-value tabular">
                {{ fmtDur(medianCycleSeconds()) }}
              </div>
              <div class="ops-kpi-sub tabular">
                Queue → merge p50:
                {{ fmtDur(report()?.merges?.completedToLanded?.p50Seconds) }}
              </div>
            </article>

            <article class="ops-kpi-card" data-testid="kpi-ci">
              <div class="ops-kpi-top">
                <span class="ops-kpi-label">CI Pass Rate &amp; Faults</span>
                <span
                  [class]="
                    infraFaultCount() > 0
                      ? 'status-pill blast'
                      : 'status-pill healthy'
                  "
                >
                  <g-status-icon
                    [tone]="infraFaultCount() > 0 ? 'blast' : 'healthy'"
                    [kind]="infraFaultCount() > 0 ? 'warning' : 'healthy'"
                  />
                  {{ infraFaultCount() }} infra faults
                </span>
              </div>
              <div class="ops-kpi-value tabular">{{ ciPassRateLabel() }}</div>
              <div class="ops-kpi-sub tabular">
                {{ report()?.checks?.greenRuns ?? 0 }} green runs · Cache hit
                {{ fmtPct(report()?.ciQueueing?.cacheHitShare) }}
              </div>
            </article>

            <article class="ops-kpi-card" data-testid="kpi-merge-queue">
              <div class="ops-kpi-top">
                <span class="ops-kpi-label">Merge Queue Health</span>
                <span
                  [class]="
                    (report()?.merges?.baseHolds?.open ?? 0) > 0
                      ? 'status-pill error'
                      : 'status-pill healthy'
                  "
                >
                  <g-status-icon
                    [tone]="
                      (report()?.merges?.baseHolds?.open ?? 0) > 0
                        ? 'error'
                        : 'healthy'
                    "
                    [kind]="
                      (report()?.merges?.baseHolds?.open ?? 0) > 0
                        ? 'blocked'
                        : 'healthy'
                    "
                  />
                  {{
                    (report()?.merges?.baseHolds?.open ?? 0) > 0
                      ? (report()?.merges?.baseHolds?.open ?? 0) + " base hold"
                      : "Clear"
                  }}
                </span>
              </div>
              <div class="ops-kpi-value tabular">
                {{ fmtPct(report()?.merges?.batches?.firstTryRate) }}
              </div>
              <div class="ops-kpi-sub tabular">
                {{ report()?.merges?.batches?.passed ?? 0 }}/{{
                  report()?.merges?.batches?.batches ?? 0
                }}
                batches · {{ report()?.merges?.batches?.saved ?? 0 }} CI suites
                saved
              </div>
            </article>

            <article class="ops-kpi-card" data-testid="kpi-agents">
              <div class="ops-kpi-top">
                <span class="ops-kpi-label">Active Agents &amp; Capacity</span>
                <span class="status-pill setup">
                  <g-status-icon tone="setup" kind="working" />
                  {{ fmtPct(report()?.runs?.utilization) }} util
                </span>
              </div>
              <div class="ops-kpi-value tabular">
                {{ report()?.runs?.live ?? feed.runningCount() }} /
                {{ report()?.runs?.maxConcurrent ?? 0 }}
              </div>
              <div class="ops-kpi-sub tabular">
                {{ succeededRunsCount() }} succeeded turns ·
                {{ (report()?.runs?.attemptsPerCompletion ?? 0).toFixed(1) }}
                attempts/task
              </div>
            </article>
          </div>

          <!-- Primary SVG Charts Grid -->
          <div class="ops-grid-2">
            <!-- Chart 1: Throughput & Merges Over Time -->
            <article class="ops-card" data-testid="card-throughput">
              <div class="ops-card-head">
                <div>
                  <h2 class="ops-card-title">Throughput &amp; Merges</h2>
                  <p class="ops-card-sub">
                    Tasks filed, completed, and pull requests landed across the
                    {{ window() }} window
                  </p>
                </div>
                <div class="tel-legend">
                  <span class="tel-legend-item">
                    <span class="tel-swatch is-filed"></span>Filed
                  </span>
                  <span class="tel-legend-item">
                    <span class="tel-swatch is-primary"></span>Completed
                  </span>
                  <span class="tel-legend-item">
                    <span class="tel-swatch is-tertiary"></span>Merged
                  </span>
                </div>
              </div>

              @let geo = throughputGeo();
              @if (geo.bars.length === 0) {
                <p class="muted" style="margin: 8px 0">
                  No bucketed throughput samples recorded in this window yet.
                </p>
              } @else {
                <svg
                  class="tel-chart-svg"
                  viewBox="0 0 520 148"
                  role="img"
                  [attr.aria-label]="
                    'Throughput and merges chart over ' +
                    window() +
                    ', peak ' +
                    geo.maxVal +
                    ' per slice'
                  "
                  data-testid="chart-throughput"
                >
                  <line x1="0" y1="10" x2="520" y2="10" class="tel-grid-line" />
                  <line x1="0" y1="70" x2="520" y2="70" class="tel-grid-line" />
                  <line
                    x1="0"
                    y1="130"
                    x2="520"
                    y2="130"
                    class="tel-grid-line"
                  />

                  @for (b of geo.bars; track b.index) {
                    <g>
                      <title>{{ b.label }}</title>
                      <rect
                        class="tel-bar-filed"
                        [attr.x]="b.x"
                        [attr.y]="b.filedY"
                        [attr.width]="b.width"
                        [attr.height]="b.filedH"
                        rx="3"
                      />
                      <rect
                        class="tel-bar-completed"
                        [attr.x]="b.x"
                        [attr.y]="b.completedY"
                        [attr.width]="b.width"
                        [attr.height]="b.completedH"
                        rx="3"
                      />
                    </g>
                  }
                  @if (geo.mergeArea) {
                    <path class="tel-merge-area" [attr.d]="geo.mergeArea" />
                  }
                  @if (geo.mergeLine) {
                    <polyline
                      class="tel-merge-line"
                      [attr.points]="geo.mergeLine"
                    />
                  }
                  @for (b of geo.bars; track b.index) {
                    <circle
                      class="tel-merge-dot"
                      [attr.cx]="b.mergeX"
                      [attr.cy]="b.mergeY"
                      r="3"
                    >
                      <title>{{ b.label }}</title>
                    </circle>
                  }
                </svg>
              }
            </article>

            <!-- Chart 2: Cycle-Time Stages (p50 -> p90) -->
            <article class="ops-card" data-testid="card-latency">
              <div class="ops-card-head">
                <div>
                  <h2 class="ops-card-title">Cycle-Time Stages</h2>
                  <p class="ops-card-sub">
                    Median (solid) and p90 tail (tonal) wall-clock duration per
                    lifecycle stage
                  </p>
                </div>
              </div>

              @if (latencyRows().length === 0) {
                <p class="muted" style="margin: 8px 0">
                  No completed stage timings in this window.
                </p>
              } @else {
                <div
                  class="tel-stage-list"
                  role="img"
                  aria-label="Cycle-time stage median and p90 bars"
                  data-testid="chart-latency"
                >
                  @for (s of latencyRows(); track s.stage) {
                    <div class="tel-stage-row" [gTip]="s.description">
                      <span class="tel-stage-name">{{
                        s.label || s.stage
                      }}</span>
                      <svg
                        width="100%"
                        height="14"
                        viewBox="0 0 200 14"
                        preserveAspectRatio="none"
                        aria-hidden="true"
                      >
                        <rect
                          x="0"
                          y="3"
                          width="200"
                          height="8"
                          rx="4"
                          fill="var(--md-sys-color-surface-container-highest)"
                        />
                        <rect
                          x="0"
                          y="3"
                          [attr.width]="s.p90Pct * 2"
                          height="8"
                          rx="4"
                          fill="var(--md-sys-color-secondary-container)"
                        />
                        <rect
                          x="0"
                          y="3"
                          [attr.width]="s.p50Pct * 2"
                          height="8"
                          rx="4"
                          fill="var(--md-sys-color-primary)"
                        />
                      </svg>
                      <span class="tel-stage-nums tabular">
                        p50 {{ fmtDur(s.p50Seconds) }} · p90
                        {{ fmtDur(s.p90Seconds) }} (n={{ s.n }})
                      </span>
                    </div>
                  }
                </div>
              }
            </article>

            <!-- Chart 3: Run Outcomes & Agent Templates -->
            <article class="ops-card" data-testid="card-outcomes">
              <div class="ops-card-head">
                <div>
                  <h2 class="ops-card-title">
                    Run Outcomes &amp; Agent Templates
                  </h2>
                  <p class="ops-card-sub">
                    Attempt conclusions and template wake efficiency
                  </p>
                </div>
              </div>

              @if (outcomeRows().length === 0) {
                <p class="muted" style="margin: 8px 0">
                  No finished agent runs in this window.
                </p>
              } @else {
                <svg
                  class="tel-stacked-svg"
                  viewBox="0 0 400 16"
                  preserveAspectRatio="none"
                  role="img"
                  aria-label="Stacked run outcome shares"
                  data-testid="chart-outcomes"
                >
                  @for (o of outcomeRows(); track o.key) {
                    <rect
                      [attr.x]="o.offsetPct * 4"
                      y="0"
                      [attr.width]="o.widthPct * 4"
                      height="16"
                      [attr.fill]="o.color"
                    >
                      <title>
                        {{ o.key }}: {{ o.count }} ({{ fmtPct(o.share) }})
                      </title>
                    </rect>
                  }
                </svg>
                <div class="tel-legend">
                  @for (o of outcomeRows(); track o.key) {
                    <span class="tel-legend-item tabular">
                      <span
                        class="tel-swatch"
                        [style.background]="o.color"
                      ></span>
                      {{ o.key }}: <strong>{{ o.count }}</strong> ({{
                        fmtPct(o.share)
                      }})
                    </span>
                  }
                </div>
              }

              @if ((report()?.agents?.templates?.length ?? 0) > 0) {
                <div class="ops-table-wrap">
                  <table class="ops-table">
                    <thead>
                      <tr>
                        <th scope="col">Template</th>
                        <th scope="col">Wakes</th>
                        <th scope="col">Calls p50 / p95</th>
                        <th scope="col">Uncited rate</th>
                      </tr>
                    </thead>
                    <tbody>
                      @for (
                        tpl of report()?.agents?.templates ?? [];
                        track tpl.templateId
                      ) {
                        <tr>
                          <td class="tabular">{{ tpl.templateId }}</td>
                          <td class="tabular">{{ tpl.wakes }}</td>
                          <td class="tabular">
                            {{ tpl.callsP50 }} / {{ tpl.callsP95 }}
                          </td>
                          <td class="tabular">
                            {{ fmtPct(tpl.report?.uncitedRate) }}
                          </td>
                        </tr>
                      }
                    </tbody>
                  </table>
                </div>
              }
            </article>

            <!-- Chart 4: Top Tools & CI Queue Wait Causes -->
            <article class="ops-card" data-testid="card-tools">
              <div class="ops-card-head">
                <div>
                  <h2 class="ops-card-title">
                    Top Tools &amp; CI Queue Causes
                  </h2>
                  <p class="ops-card-sub">
                    {{ fmtNum(report()?.tools?.calls ?? 0) }} tool calls ({{
                      fmtPct(report()?.tools?.erroredShare)
                    }}
                    errored) · CI queued share
                    {{ fmtPct(report()?.ciQueueing?.queuedShare) }}
                  </p>
                </div>
              </div>

              @if (toolRows().length === 0 && queueCauseRows().length === 0) {
                <p class="muted" style="margin: 8px 0">
                  No tool census or CI queue delays recorded in this window.
                </p>
              } @else {
                <div class="tel-stage-list" data-testid="chart-tools">
                  @for (t of toolRows(); track t.name) {
                    <div class="tel-stage-row">
                      <span class="tel-stage-name tabular">{{ t.name }}</span>
                      <svg
                        width="100%"
                        height="12"
                        viewBox="0 0 200 12"
                        preserveAspectRatio="none"
                        aria-hidden="true"
                      >
                        <rect
                          x="0"
                          y="2"
                          width="200"
                          height="8"
                          rx="4"
                          fill="var(--md-sys-color-surface-container-highest)"
                        />
                        <rect
                          x="0"
                          y="2"
                          [attr.width]="t.pct * 2"
                          height="8"
                          rx="4"
                          fill="var(--md-sys-color-secondary)"
                        />
                      </svg>
                      <span class="tel-stage-nums tabular">
                        {{ fmtNum(t.calls) }} calls ·
                        {{ fmtPct(t.errorRate) }} err
                      </span>
                    </div>
                  }
                  @for (c of queueCauseRows(); track c.cause) {
                    <div class="tel-stage-row" [gTip]="c.reason || ''">
                      <span class="tel-stage-name">CI wait: {{ c.cause }}</span>
                      <div class="meter-track">
                        <div
                          class="meter-fill is-warn"
                          [style.width.%]="c.share * 100"
                        ></div>
                      </div>
                      <span class="tel-stage-nums tabular">
                        {{ fmtPct(c.share) }} · {{ fmtDur(c.queuedSeconds) }}
                      </span>
                    </div>
                  }
                </div>
              }
            </article>
          </div>

          <!-- Token Usage & Budget Caps (GET /api/usage, PUT /api/usage/settings) -->
          <article class="ops-card" data-testid="telemetry-usage">
            <div class="ops-card-head">
              <div>
                <h2 class="ops-card-title">
                  Token Usage, Cost &amp; Concurrency Budgets
                </h2>
                <p class="ops-card-sub">
                  Per-connection and per-model token consumption, burn rate, and
                  sustainable worker ceilings
                </p>
              </div>
              <div class="headline-actions">
                @if (usage(); as u) {
                  <span class="status-pill neutral tabular">
                    <g-status-icon tone="neutral" kind="progress" />
                    Mode: {{ u.mode }} · {{ u.effectiveWorkers }}/{{
                      u.maxWorkers
                    }}
                    workers
                  </span>
                }
                <button
                  type="button"
                  class="btn-tonal"
                  data-testid="usage-editor-toggle"
                  [disabled]="!canWriteUsage()"
                  [gTip]="
                    canWriteUsage()
                      ? 'Configure concurrency mode and connection token budgets'
                      : access.deniedReason('PUT /api/usage/settings')
                  "
                  (click)="toggleUsageEditor()"
                >
                  {{
                    editingUsage() ? "Close budget editor" : "Configure budgets"
                  }}
                </button>
              </div>
            </div>

            @if (usage(); as u) {
              @for (w of u.warnings ?? []; track w) {
                <p class="banner banner-error" style="margin: 0" role="status">
                  {{ w }}
                </p>
              }
              <div class="ops-kpi-grid">
                <div class="ops-kpi-card">
                  <span class="ops-kpi-label">Billable Tokens</span>
                  <span class="ops-kpi-value tabular">{{
                    fmtNum(u.rollup.billable)
                  }}</span>
                  <span class="ops-kpi-sub tabular"
                    >{{ u.rollup.runs }} recorded runs</span
                  >
                </div>
                <div class="ops-kpi-card">
                  <span class="ops-kpi-label">Input / Output</span>
                  <span class="ops-kpi-value tabular"
                    >{{ fmtNum(u.rollup.inputTokens) }} /
                    {{ fmtNum(u.rollup.outputTokens) }}</span
                  >
                  <span class="ops-kpi-sub tabular">
                    Cache read {{ fmtNum(u.rollup.cacheReadTokens) }} · write
                    {{ fmtNum(u.rollup.cacheWriteTokens) }}
                  </span>
                </div>
                <div class="ops-kpi-card">
                  <span class="ops-kpi-label">Burn Rate</span>
                  <span class="ops-kpi-value tabular"
                    >{{ fmtNum(u.rollup.burnPerHour) }}/hr</span
                  >
                  <span class="ops-kpi-sub tabular">
                    @if (u.rollup.costUsd) {
                      \${{ u.rollup.costUsd.toFixed(2) }} est. spend
                    } @else {
                      Subscription / unpriced connections
                    }
                  </span>
                </div>
              </div>

              @if (editingUsage() && canWriteUsage()) {
                <div
                  class="tel-budget-editor"
                  data-testid="usage-budget-editor"
                >
                  <div class="tel-budget-controls">
                    <div class="tel-field-group">
                      <span>Concurrency mode</span>
                      <div
                        class="segmented"
                        role="group"
                        aria-label="Usage concurrency mode"
                      >
                        @for (m of ["advisory", "auto", "off"]; track m) {
                          <button
                            type="button"
                            [attr.aria-pressed]="draftMode() === m"
                            [attr.data-testid]="'usage-mode-' + m"
                            (click)="draftMode.set(m)"
                          >
                            {{ m }}
                          </button>
                        }
                      </div>
                    </div>
                    <label class="tel-field-group">
                      <span>Min workers</span>
                      <input
                        type="number"
                        min="1"
                        class="ops-input tabular"
                        style="width: 96px"
                        data-testid="usage-min-workers"
                        [value]="draftMinWorkers()"
                        (input)="
                          draftMinWorkers.set(+$any($event.target).value || 1)
                        "
                      />
                    </label>
                    <label class="tel-field-group">
                      <span>Retention (days)</span>
                      <input
                        type="number"
                        min="1"
                        class="ops-input tabular"
                        style="width: 110px"
                        data-testid="usage-retention-days"
                        [value]="draftRetentionDays()"
                        (input)="
                          draftRetentionDays.set(
                            +$any($event.target).value || 30
                          )
                        "
                      />
                    </label>
                    <button
                      type="button"
                      class="btn-tonal"
                      data-testid="usage-add-budget"
                      (click)="addBudgetRow()"
                    >
                      + Add budget cap
                    </button>
                  </div>

                  @for (b of draftBudgets(); track $index; let idx = $index) {
                    <div class="tel-budget-row" data-testid="usage-budget-row">
                      <input
                        type="text"
                        class="ops-input tabular"
                        placeholder="Connection ID (e.g. claude-main)"
                        aria-label="Budget connection ID"
                        [attr.data-testid]="'budget-conn-' + idx"
                        [value]="b.connection"
                        (input)="
                          patchBudget(idx, {
                            connection: $any($event.target).value,
                          })
                        "
                      />
                      <input
                        type="text"
                        class="ops-input tabular"
                        placeholder="Model (blank = whole connection)"
                        aria-label="Budget model filter"
                        [value]="b.model || ''"
                        (input)="
                          patchBudget(idx, {
                            model: $any($event.target).value,
                          })
                        "
                      />
                      <input
                        type="number"
                        min="1"
                        class="ops-input tabular"
                        placeholder="Tokens"
                        aria-label="Budget token limit"
                        [attr.data-testid]="'budget-tokens-' + idx"
                        [value]="b.tokens"
                        (input)="
                          patchBudget(idx, {
                            tokens: +$any($event.target).value || 0,
                          })
                        "
                      />
                      <div class="segmented" role="group" aria-label="Window">
                        @for (bw of budgetWindows; track bw) {
                          <button
                            type="button"
                            [attr.aria-pressed]="b.window === bw"
                            (click)="patchBudget(idx, { window: bw })"
                          >
                            {{ bw }}
                          </button>
                        }
                      </div>
                      <button
                        type="button"
                        class="btn-text danger"
                        [attr.aria-label]="'Remove budget row ' + (idx + 1)"
                        (click)="removeBudgetRow(idx)"
                      >
                        Remove
                      </button>
                    </div>
                  }

                  @if (usageError(); as uerr) {
                    <p
                      class="banner banner-error"
                      style="margin: 0"
                      role="alert"
                    >
                      {{ uerr }}
                    </p>
                  }

                  <div
                    class="headline-actions"
                    style="justify-content: flex-end"
                  >
                    <button
                      type="button"
                      class="btn-text"
                      (click)="editingUsage.set(false)"
                    >
                      Cancel
                    </button>
                    <button
                      type="button"
                      class="btn-primary"
                      data-testid="usage-save"
                      [disabled]="savingUsage()"
                      (click)="saveUsageSettings()"
                    >
                      {{ savingUsage() ? "Saving…" : "Save budgets" }}
                    </button>
                  </div>
                </div>
              }

              @if (u.scopes.length > 0) {
                <div class="ops-table-wrap">
                  <table class="ops-table" data-testid="usage-scopes-table">
                    <thead>
                      <tr>
                        <th scope="col">Connection / Model</th>
                        <th scope="col">Runs</th>
                        <th scope="col">Input / Output</th>
                        <th scope="col">Cached (R/W)</th>
                        <th scope="col">Window Usage &amp; Cap</th>
                        <th scope="col">Burn / hr</th>
                        <th scope="col">Status</th>
                      </tr>
                    </thead>
                    <tbody>
                      @for (
                        s of u.scopes;
                        track s.connection + ":" + (s.model || "*")
                      ) {
                        <tr>
                          <td>
                            <strong class="tabular">{{
                              s.connectionName || s.connection
                            }}</strong>
                            @if (s.model) {
                              <span class="muted tabular">
                                · {{ s.model }}</span
                              >
                            }
                          </td>
                          <td class="tabular">{{ s.runs }}</td>
                          <td class="tabular">
                            {{ fmtNum(s.inputTokens) }} /
                            {{ fmtNum(s.outputTokens) }}
                          </td>
                          <td class="tabular">
                            {{ fmtNum(s.cacheReadTokens) }} /
                            {{ fmtNum(s.cacheWriteTokens) }}
                          </td>
                          <td style="min-width: 170px">
                            <div class="tabular" style="font-size: 12px">
                              {{ fmtNum(s.used) }}
                              @if (s.limit) {
                                / {{ fmtNum(s.limit) }} ({{ s.limitSource }})
                              } @else {
                                ({{ s.window }})
                              }
                            </div>
                            @if (s.limit) {
                              <div class="meter-track" style="margin-top: 4px">
                                <div
                                  [class]="
                                    s.used / s.limit >= 0.9
                                      ? 'meter-fill is-error'
                                      : s.used / s.limit >= 0.75
                                        ? 'meter-fill is-warn'
                                        : 'meter-fill is-healthy'
                                  "
                                  [style.width.%]="
                                    Math.min(100, (s.used / s.limit) * 100)
                                  "
                                ></div>
                              </div>
                            }
                          </td>
                          <td class="tabular">{{ fmtNum(s.burnPerHour) }}</td>
                          <td>
                            @if (s.pausedUntil) {
                              <span class="status-pill error">
                                <g-status-icon tone="error" kind="blocked" />
                                Paused
                              </span>
                            } @else {
                              <span class="status-pill healthy">
                                <g-status-icon tone="healthy" kind="healthy" />
                                Active
                              </span>
                            }
                          </td>
                        </tr>
                      }
                    </tbody>
                  </table>
                </div>
              }
            } @else {
              <p class="muted" style="margin: 0">
                Token usage is unavailable or not yet recorded on this
                deployment.
              </p>
            }
          </article>

          <!-- Team Engagement & Shipped Summary (SPACE Guardrail: Alphabetical, NEVER Ranked) -->
          <article class="ops-card" data-testid="telemetry-team-space">
            <div class="ops-card-head">
              <div>
                <h2 class="ops-card-title">
                  Team Collaboration &amp; Shipped Summary
                </h2>
                <p class="ops-card-sub">
                  SPACE guardrail: team-level delivery and contributor context,
                  listed strictly alphabetically (A–Z) and never
                  leaderboard-ranked.
                </p>
              </div>
              <div class="headline-actions">
                <span
                  class="status-pill healthy"
                  data-testid="space-guardrail-pill"
                >
                  <g-status-icon tone="healthy" kind="healthy" />
                  Alphabetical · Non-ranked
                </span>
                @if (canReadReleaseNotes()) {
                  <div
                    class="segmented"
                    role="group"
                    aria-label="Rollup period"
                  >
                    @for (p of spacePeriods; track p.id) {
                      <button
                        type="button"
                        [attr.aria-pressed]="spacePeriod() === p.id"
                        [attr.data-testid]="'space-period-' + p.id"
                        (click)="pickSpacePeriod(p.id)"
                      >
                        {{ p.label }}
                      </button>
                    }
                  </div>
                }
              </div>
            </div>

            @if (clusterStats(); as cs) {
              <div class="tel-legend tabular">
                <span
                  >Period: <strong>{{ cs.period }}</strong></span
                >
                <span
                  >PRs merged:
                  <strong>{{ cs.pullRequestsMerged }}</strong></span
                >
                <span
                  >Lines:
                  <strong
                    >+{{ fmtNum(cs.linesAdded) }} / −{{
                      fmtNum(cs.linesDeleted)
                    }}</strong
                  ></span
                >
                <span
                  >Agent time:
                  <strong>{{ cs.agentHours.toFixed(1) }}h</strong></span
                >
              </div>

              @if ((cs.directions?.length ?? 0) > 0) {
                <div>
                  <h3 class="section-title">Active Workstream Directions</h3>
                  <ul class="tel-dir-list">
                    @for (dir of cs.directions ?? []; track dir.taskId) {
                      <li class="tel-dir-item">
                        <span>
                          <a
                            class="row-title"
                            [routerLink]="['/task', dir.taskId]"
                            >#{{ dir.taskId }}
                            {{ dir.title || "Workstream root" }}</a
                          >
                          @if (dir.repo) {
                            <span class="muted tabular"> · {{ dir.repo }}</span>
                          }
                        </span>
                        <span class="tabular muted">
                          {{ dir.pullRequestsMerged }} merged ·
                          {{ dir.tasksCompleted }} completed
                        </span>
                      </li>
                    }
                  </ul>
                </div>
              }
            }

            <div>
              <h3 class="section-title">
                Contributors &amp; Actors (Alphabetical A–Z)
              </h3>
              @if (alphabeticalActors().length === 0) {
                <p class="muted" style="margin: 0">
                  No contributor records in the current view.
                </p>
              } @else {
                <div class="tel-space-grid" data-testid="space-actors-grid">
                  @for (actor of alphabeticalActors(); track actor.id) {
                    <div class="tel-space-card" data-testid="space-actor-row">
                      <g-actor-chip [label]="actor.label" [kind]="actor.kind" />
                      <div class="tel-space-meta tabular">
                        @if (actor.workstreams > 0) {
                          <span
                            >{{ actor.workstreams }}
                            {{
                              actor.workstreams === 1
                                ? "workstream"
                                : "workstreams"
                            }}</span
                          >
                        }
                        @if (actor.activeTasks > 0) {
                          <span>{{ actor.activeTasks }} open tasks</span>
                        }
                        @if (actor.repos.length > 0) {
                          <span>{{ actor.repos.join(", ") }}</span>
                        }
                      </div>
                    </div>
                  }
                </div>
              }
            </div>
          </article>
        </div>
      }
    </section>
  `,
})
export class TelemetryPageComponent implements OnInit {
  protected readonly api = inject(GrainApi);
  protected readonly access = inject(AccessService);
  protected readonly feed = inject(TaskFeedStore);
  protected readonly viewFilter = inject(ViewFilter);
  private readonly snackbar = inject(SnackbarService);
  private readonly route = inject(ActivatedRoute);
  private readonly router = inject(Router);
  private readonly doc = inject(DOCUMENT);

  private readonly repoTrigger =
    viewChild<ElementRef<HTMLButtonElement>>("repoTrigger");
  private readonly authorTrigger =
    viewChild<ElementRef<HTMLButtonElement>>("authorTrigger");
  private readonly repoListbox =
    viewChild<ElementRef<HTMLElement>>("repoListbox");
  private readonly authorListbox =
    viewChild<ElementRef<HTMLElement>>("authorListbox");

  protected readonly Math = Math;
  protected readonly windows = WINDOWS;
  protected readonly spacePeriods = SPACE_PERIODS;
  protected readonly budgetWindows = BUDGET_WINDOWS;
  protected readonly fmtDur = formatDurationSec;
  protected readonly fmtNum = formatCompactNum;
  protected readonly fmtPct = formatPct;

  protected readonly window = signal<MetricsWindow>("7d");
  protected readonly selectedRepo = signal<string>("");
  protected readonly selectedAuthor = signal<string>("");
  protected readonly spacePeriod = signal<string>("last-week");
  protected readonly openMenu = signal<"repo" | "author" | null>(null);

  protected readonly loading = signal(false);
  protected readonly loadError = signal<string | null>(null);
  protected readonly report = signal<MetricsReport | null>(null);
  protected readonly usage = signal<UsageResponse | null>(null);
  protected readonly clusterStats = signal<ClusterStats | null>(null);
  protected readonly releaseNotes = signal<ReleaseNote[]>([]);
  protected readonly configRepos = signal<string[]>([]);
  protected readonly knownConnections = signal<string[]>([]);

  protected readonly editingUsage = signal(false);
  protected readonly savingUsage = signal(false);
  protected readonly usageError = signal<string | null>(null);
  protected readonly draftMode = signal<string>("advisory");
  protected readonly draftMinWorkers = signal<number>(1);
  protected readonly draftRetentionDays = signal<number>(30);
  protected readonly draftBudgets = signal<TokenBudget[]>([]);

  private metricsSeq = 0;

  protected readonly deploymentReadLimits = computed<string[]>(() =>
    this.access.isFullAccess()
      ? []
      : (this.access.repoLimits()["deployment:read"] ?? []),
  );

  protected readonly isMetricsRepoLimited = computed(
    () => this.deploymentReadLimits().length > 0,
  );

  protected readonly canReadAllReposMetrics = computed(
    () => this.access.can("GET /api/metrics") && !this.isMetricsRepoLimited(),
  );

  protected readonly canReadMetrics = computed(() => {
    const repo = this.selectedRepo();
    if (!repo) return this.canReadAllReposMetrics();
    return this.access.can("GET /api/metrics", repo);
  });

  protected deniedMetricsReason(repo: string): string {
    if (!repo && this.isMetricsRepoLimited()) {
      return `Your role ${this.access.roleLabel() || "limited"} holds deployment:read only on ${this.deploymentReadLimits().join(", ")} and cannot read deployment-wide metrics.`;
    }
    return this.access.deniedReason("GET /api/metrics", repo || undefined);
  }

  protected readonly canReadUsage = computed(
    () => this.access.can("GET /api/usage") && !this.isMetricsRepoLimited(),
  );

  protected readonly canWriteUsage = computed(
    () =>
      this.access.can("PUT /api/usage/settings") &&
      !(
        !this.access.isFullAccess() &&
        Boolean(this.access.repoLimits()["settings:write"]?.length)
      ),
  );

  protected readonly canReadReleaseNotes = computed(
    () =>
      this.access.can("GET /api/release-notes/stats") &&
      !(
        !this.access.isFullAccess() &&
        Boolean(this.access.repoLimits()["release-notes:read"]?.length)
      ),
  );

  protected readonly repoOptions = computed<string[]>(() => {
    const set = new Set<string>([
      ...this.configRepos(),
      ...this.deploymentReadLimits(),
    ]);
    for (const r of this.report()?.scope?.repos ?? []) if (r) set.add(r);
    for (const rw of this.clusterStats()?.repos ?? []) {
      if (rw.repo) set.add(rw.repo);
    }
    for (const t of this.feed.tasks()) if (t.repo) set.add(t.repo);
    const list = [...set].sort((a, b) => a.localeCompare(b));
    if (!this.isMetricsRepoLimited()) return list;
    return list.filter((r) => this.access.can("GET /api/metrics", r));
  });

  protected readonly alphabeticalActors = computed(() => {
    const q = this.viewFilter.query().trim().toLowerCase();
    const all = buildAlphabeticalContributors(
      this.clusterStats(),
      this.feed.tasks(),
    );
    if (!q) return all;
    return all.filter(
      (a) =>
        a.label.toLowerCase().includes(q) ||
        a.repos.some((r) => r.toLowerCase().includes(q)),
    );
  });

  protected readonly authorOptions = computed<string[]>(() =>
    buildAlphabeticalContributors(this.clusterStats(), this.feed.tasks()).map(
      (a) => a.id,
    ),
  );

  protected readonly medianCycleSeconds = computed<number | undefined>(() => {
    const m = this.report()?.merges?.filedToLanded;
    if (m && m.n > 0) return m.p50Seconds;
    const stages = this.report()?.latency ?? [];
    const lead =
      stages.find((s) => s.stage === "lead_time" || s.stage === "total") ??
      stages[stages.length - 1];
    return lead?.n ? lead.p50Seconds : undefined;
  });

  protected readonly infraFaultCount = computed<number>(
    () => this.report()?.runs?.execTunnelFailures ?? 0,
  );

  protected readonly ciPassRateLabel = computed<string>(() => {
    const verdicts = this.report()?.checks?.verdicts;
    if (!verdicts) return "—";
    let total = 0;
    let passed = 0;
    for (const [k, v] of Object.entries(verdicts)) {
      total += v;
      if (k === "passed" || k === "success" || k === "green") passed += v;
    }
    if (total === 0) return "—";
    return formatPct(passed / total);
  });

  protected readonly succeededRunsCount = computed<number>(() => {
    const o = this.report()?.runs?.outcomes ?? {};
    return (o["succeeded"] ?? 0) + (o["completed"] ?? 0);
  });

  protected readonly throughputGeo = computed(() =>
    buildThroughputGeometry(
      this.report()?.throughput?.buckets,
      this.report()?.merges?.buckets,
    ),
  );

  protected readonly latencyRows = computed(() => {
    const stages = (this.report()?.latency ?? []).filter((s) => s.n > 0);
    const maxP90 = Math.max(
      1,
      ...stages.map((s) => s.p90Seconds || s.p50Seconds),
    );
    return stages.map((s) => ({
      ...s,
      p50Pct: Math.min(100, (s.p50Seconds / maxP90) * 100),
      p90Pct: Math.min(100, ((s.p90Seconds || s.p50Seconds) / maxP90) * 100),
    }));
  });

  protected readonly outcomeRows = computed(() => {
    const raw = this.report()?.runs?.outcomes ?? {};
    const entries = Object.entries(raw).filter(([, v]) => v > 0);
    const total = entries.reduce((acc, [, v]) => acc + v, 0);
    if (total === 0) return [];
    let offset = 0;
    const colorOf = (k: string): string => {
      if (k === "succeeded" || k === "completed") {
        return "var(--md-sys-color-tertiary)";
      }
      if (k === "awaiting_reply") return "var(--md-sys-color-secondary)";
      if (k === "failed" || k === "errored") return "var(--md-sys-color-error)";
      return "var(--md-sys-color-warning)";
    };
    return entries.map(([key, count]) => {
      const share = count / total;
      const widthPct = share * 100;
      const row = {
        key,
        count,
        share,
        offsetPct: offset,
        widthPct,
        color: colorOf(key),
      };
      offset += widthPct;
      return row;
    });
  });

  protected readonly toolRows = computed(() => {
    const q = this.viewFilter.query().trim().toLowerCase();
    const list = (this.report()?.tools?.byTool ?? [])
      .filter((t) => !q || t.name.toLowerCase().includes(q))
      .slice(0, 6);
    const maxCalls = Math.max(1, ...list.map((t) => t.calls));
    return list.map((t) => ({
      ...t,
      pct: Math.min(100, (t.calls / maxCalls) * 100),
    }));
  });

  protected readonly queueCauseRows = computed(() =>
    (this.report()?.ciQueueing?.byCause ?? []).slice(0, 4),
  );

  ngOnInit(): void {
    const qp = this.route.snapshot.queryParamMap;
    const w = qp.get("window");
    if (w === "24h" || w === "7d" || w === "30d") {
      this.window.set(w);
    }
    const r = qp.get("repo");
    if (r) this.selectedRepo.set(r);
    const a = qp.get("author");
    if (a) this.selectedAuthor.set(a);

    this.ensurePermittedRepoSelected();
    void this.bootstrap();
  }

  private ensurePermittedRepoSelected(): void {
    if (this.selectedRepo()) return;
    const first = this.deploymentReadLimits()[0];
    if (first) {
      this.selectedRepo.set(first);
      this.syncUrl();
    }
  }

  private async bootstrap(): Promise<void> {
    await this.loadBootstrapConfig();
    this.ensurePermittedRepoSelected();
    await this.refreshAll();
  }

  private async loadBootstrapConfig(): Promise<void> {
    if (!this.access.can("GET /api/config")) return;
    try {
      const cfg = await this.api.config();
      if (cfg) {
        this.access.setConfig(cfg);
        const repos = Array.isArray(cfg["targetRepos"])
          ? (cfg["targetRepos"] as unknown[])
              .filter((r): r is string => typeof r === "string" && !!r.trim())
              .map((r) => r.trim())
          : [];
        this.configRepos.set(repos);
        const backends = Array.isArray(cfg["agentBackends"])
          ? (cfg["agentBackends"] as { id?: string }[])
          : [];
        this.knownConnections.set(
          backends.map((b) => b.id || "").filter(Boolean),
        );
      }
    } catch {
      // Non-fatal: page still loads metrics under existing access context.
    }
  }

  async refreshAll(): Promise<void> {
    await Promise.all([
      this.loadMetrics(),
      this.loadUsage(),
      this.loadSpaceStats(),
    ]);
  }

  async loadMetrics(): Promise<void> {
    if (!this.canReadMetrics()) return;
    const seq = ++this.metricsSeq;
    this.loading.set(true);
    this.loadError.set(null);
    const params = new URLSearchParams();
    params.set("window", this.window());
    if (this.selectedRepo()) params.append("repo", this.selectedRepo());
    if (this.selectedAuthor()) params.append("author", this.selectedAuthor());
    try {
      const rep = await this.api.get<MetricsReport>(
        "GET /api/metrics",
        undefined,
        params.toString(),
      );
      if (seq !== this.metricsSeq) return;
      this.report.set(rep);
    } catch (err) {
      if (seq !== this.metricsSeq) return;
      this.loadError.set(errorText(err));
    } finally {
      if (seq === this.metricsSeq) this.loading.set(false);
    }
  }

  async loadUsage(): Promise<void> {
    if (!this.canReadUsage()) return;
    try {
      const u = await this.api.get<UsageResponse>("GET /api/usage");
      this.usage.set(u);
    } catch {
      // Optional section when usage store is empty/unwired.
    }
  }

  async loadSpaceStats(): Promise<void> {
    if (!this.canReadReleaseNotes()) return;
    const params = new URLSearchParams();
    if (this.spacePeriod()) params.set("period", this.spacePeriod());
    try {
      const [stats, notes] = await Promise.all([
        this.api.get<ClusterStats>(
          "GET /api/release-notes/stats",
          undefined,
          params.toString(),
        ),
        this.api
          .get<ReleaseNote[]>("GET /api/release-notes")
          .catch(() => [] as ReleaseNote[]),
      ]);
      this.clusterStats.set(stats);
      this.releaseNotes.set(Array.isArray(notes) ? notes : []);
    } catch {
      // Optional section when release-notes scope is restricted.
    }
  }

  protected pickWindow(w: MetricsWindow): void {
    if (this.window() === w) return;
    this.window.set(w);
    this.syncUrl();
    void this.loadMetrics();
  }

  protected pickRepo(repo: string): void {
    if (!repo && !this.canReadAllReposMetrics()) return;
    this.openMenu.set(null);
    if (this.selectedRepo() === repo) return;
    this.selectedRepo.set(repo);
    this.syncUrl();
    void this.loadMetrics();
  }

  protected pickAuthor(author: string): void {
    this.openMenu.set(null);
    if (this.selectedAuthor() === author) return;
    this.selectedAuthor.set(author);
    this.syncUrl();
    void this.loadMetrics();
  }

  protected clearScope(): void {
    this.selectedRepo.set(
      this.canReadAllReposMetrics()
        ? ""
        : (this.deploymentReadLimits()[0] ?? ""),
    );
    this.selectedAuthor.set("");
    this.syncUrl();
    void this.loadMetrics();
  }

  protected pickSpacePeriod(p: string): void {
    if (this.spacePeriod() === p) return;
    this.spacePeriod.set(p);
    void this.loadSpaceStats();
  }

  private syncUrl(): void {
    void this.router.navigate([], {
      relativeTo: this.route,
      queryParams: {
        window: this.window(),
        repo: this.selectedRepo() || null,
        author: this.selectedAuthor() || null,
      },
      queryParamsHandling: "merge",
      replaceUrl: true,
    });
  }

  protected toggleMenu(which: "repo" | "author", ev: Event): void {
    ev.stopPropagation();
    this.openMenu.set(this.openMenu() === which ? null : which);
  }

  protected onMenuTriggerKeydown(
    which: "repo" | "author",
    ev: KeyboardEvent,
  ): void {
    if (ev.key !== "ArrowDown" && ev.key !== "ArrowUp") return;
    ev.preventDefault();
    if (this.openMenu() !== which) {
      this.openMenu.set(which);
    }
    const wantLast = ev.key === "ArrowUp";
    setTimeout(() => {
      const opts = this.enabledMenuOptions(which);
      const target = wantLast ? opts[opts.length - 1] : opts[0];
      target?.focus();
    });
  }

  protected onListboxKeydown(
    which: "repo" | "author",
    ev: KeyboardEvent,
  ): void {
    const opts = this.enabledMenuOptions(which);
    if (opts.length === 0) return;
    const cur = opts.indexOf(this.doc.activeElement as HTMLButtonElement);
    let next = -1;
    if (ev.key === "ArrowDown") {
      next = cur < 0 ? 0 : (cur + 1) % opts.length;
    } else if (ev.key === "ArrowUp") {
      next = cur <= 0 ? opts.length - 1 : cur - 1;
    } else if (ev.key === "Home") {
      next = 0;
    } else if (ev.key === "End") {
      next = opts.length - 1;
    }
    if (next >= 0) {
      ev.preventDefault();
      opts[next]?.focus();
    }
  }

  private enabledMenuOptions(which: "repo" | "author"): HTMLButtonElement[] {
    const box =
      which === "repo"
        ? this.repoListbox()?.nativeElement
        : this.authorListbox()?.nativeElement;
    if (!box) return [];
    return Array.from(
      box.querySelectorAll<HTMLButtonElement>(
        'button[role="option"]:not(:disabled)',
      ),
    );
  }

  protected onDocClick(ev: Event): void {
    const t = ev.target as HTMLElement | null;
    if (this.openMenu() && !t?.closest(".pop-wrap")) {
      this.openMenu.set(null);
    }
  }

  protected onEscape(ev: Event): void {
    const cur = this.openMenu();
    if (cur) {
      ev.preventDefault();
      this.openMenu.set(null);
      if (cur === "repo") this.repoTrigger()?.nativeElement.focus();
      else this.authorTrigger()?.nativeElement.focus();
    }
  }

  protected toggleUsageEditor(): void {
    if (!this.canWriteUsage()) return;
    const next = !this.editingUsage();
    if (next) {
      const s = this.usage()?.settings;
      this.draftMode.set(s?.mode || this.usage()?.mode || "advisory");
      this.draftMinWorkers.set(s?.minWorkers || this.usage()?.minWorkers || 1);
      this.draftRetentionDays.set(s?.retentionDays || 30);
      this.draftBudgets.set(
        (s?.budgets ?? []).map((b) => ({
          connection: b.connection,
          model: b.model || "",
          tokens: b.tokens,
          window: b.window || "5h",
        })),
      );
      this.usageError.set(null);
    }
    this.editingUsage.set(next);
  }

  protected addBudgetRow(): void {
    const defaultConn =
      this.knownConnections()[0] ?? this.usage()?.scopes?.[0]?.connection ?? "";
    this.draftBudgets.update((rows) => [
      ...rows,
      { connection: defaultConn, model: "", tokens: 1_000_000, window: "5h" },
    ]);
  }

  protected removeBudgetRow(idx: number): void {
    this.draftBudgets.update((rows) => rows.filter((_, i) => i !== idx));
  }

  protected patchBudget(idx: number, patch: Partial<TokenBudget>): void {
    this.draftBudgets.update((rows) =>
      rows.map((r, i) => (i === idx ? { ...r, ...patch } : r)),
    );
  }

  protected async saveUsageSettings(): Promise<void> {
    if (!this.canWriteUsage() || this.savingUsage()) return;
    const budgets = this.draftBudgets()
      .map((b) => ({
        connection: b.connection.trim(),
        ...(b.model?.trim() ? { model: b.model.trim() } : {}),
        tokens: Number(b.tokens),
        window: (b.window || "5h").trim(),
      }))
      .filter((b) => b.connection !== "" || b.tokens > 0);

    for (const b of budgets) {
      if (!b.connection) {
        this.usageError.set("Every budget cap must name a connection ID.");
        return;
      }
      if (!Number.isFinite(b.tokens) || b.tokens <= 0) {
        this.usageError.set(
          `Budget for ${b.connection} needs a positive token limit.`,
        );
        return;
      }
    }

    this.savingUsage.set(true);
    this.usageError.set(null);
    try {
      const updated = await this.api.put<UsageResponse>(
        "PUT /api/usage/settings",
        undefined,
        {
          mode: this.draftMode(),
          minWorkers: Math.max(1, Math.round(this.draftMinWorkers())),
          retentionDays: Math.max(1, Math.round(this.draftRetentionDays())),
          budgets,
        },
      );
      this.usage.set(updated);
      this.editingUsage.set(false);
      this.snackbar.show("Saved token budget and concurrency settings.");
    } catch (err) {
      this.usageError.set(errorText(err));
    } finally {
      this.savingUsage.set(false);
    }
  }
}
