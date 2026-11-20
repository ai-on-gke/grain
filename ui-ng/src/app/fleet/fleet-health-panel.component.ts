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
  type OnChanges,
  type OnInit,
  type SimpleChanges,
  ViewEncapsulation,
  computed,
  effect,
  inject,
  input,
  output,
  signal,
} from "@angular/core";
import { takeUntilDestroyed } from "@angular/core/rxjs-interop";
import { Router, RouterLink } from "@angular/router";
import { filter } from "rxjs";
import { GrainApi, splitRepo } from "../api/grain-api.service";
import { GrainEvents } from "../api/grain-events.service";
import type { Notification } from "../api/models";
import {
  type PendingRelease,
  approveReleaseLabel,
  releaseConfirm,
} from "../inbox/inbox-classifier";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import { TaskFeedStore } from "../services/task-feed.service";
import { ViewFilter } from "../services/view-filter.service";
import { errorText } from "../sessions/session-helpers";
import { type ConfirmRequest } from "../shared/confirm-dialog.component";
import {
  type UsagePauseInput,
  effectiveDisplayTimeZone,
  formatStamp,
} from "../shared/dispatch-status";
import { StatusIconComponent } from "../shared/status-icon.component";
import { TipDirective } from "../shared/tip.directive";
import { settingLabel, settingLocation } from "../settings/settings-sections";
import {
  type CLIRunSettingsResponse,
  type FormattedGitHubBudget,
  type GitHubMetricsSection,
  type HealthAlert,
  type HealthAlertAction,
  type HealthCountersResponse,
  type HealthSettingsResponse,
  type MetricsGitHubResponse,
  type StaleSchedule,
  type StaleSchedulesResponse,
  type UnrunnableSetting,
  alertScopeLabel,
  collectHealthAlerts,
  formatGitHubBudget,
  formatScheduleCadence,
  groupHealthAlerts,
  isScheduledUpgradeHold,
  liftPauseQuery,
  staleScheduleRepo,
} from "./fleet-health.models";
import type {
  HealthCounterSection,
  HostExecutorStatus,
  SandboxHealthResponse,
} from "./fleet.models";

export const BUDGET_REFRESH_MS = 10_000;
export const COUNTERS_REFRESH_MS = 10_000;
export const STALE_SCHEDULES_REFRESH_MS = 30_000;
export const NOTIFICATIONS_REFRESH_MS = 30_000;

const HOSTD_ALLOWLIST_PATH = "/etc/grain/hostd.yaml";

function safeHttpOrRelativeUrl(url?: string): string | null {
  const trimmed = (url ?? "").trim();
  if (!trimmed) return null;
  if (
    trimmed.startsWith("https://") ||
    trimmed.startsWith("http://") ||
    trimmed.startsWith("/")
  ) {
    return trimmed;
  }
  return null;
}

@Component({
  selector: "g-fleet-health-panel",
  imports: [RouterLink, StatusIconComponent, TipDirective],
  encapsulation: ViewEncapsulation.None,
  styleUrls: ["./fleet-health-panel.component.css"],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @if (!canReadAnyHealth()) {
      <p class="empty-state" data-testid="fleet-health-denied">
        {{
          access.deniedReason("GET /api/sandboxes") ||
            deniedScopeFallback("deployment:read")
        }}
      </p>
    } @else {
      <!-- Unmigrated host-mode install notice (classic HealthTab.jsx) -->
      @if (installNotice(); as notice) {
        <div
          class="health-notice is-error"
          role="alert"
          data-testid="health-install-notice"
        >
          <div class="health-notice-body">
            <g-status-icon tone="error" kind="error" />
            <div class="health-notice-copy">
              <span>
                <strong>Unmigrated host-mode install.</strong>
                {{ notice.message.charAt(0).toUpperCase()
                }}{{ notice.message.slice(1) }}.
                @if (notice.command) {
                  Run
                  <code data-testid="health-install-notice-command">{{
                    notice.command
                  }}</code
                  >.
                }
                @if (notice.affectedClis?.length) {
                  CLIs that lost their run location:
                  {{ notice.affectedClis!.join("; ") }}.
                }
                @if (safeDocsUrl(notice.docsUrl); as docsHref) {
                  <a [href]="docsHref" target="_blank" rel="noopener noreferrer"
                    >Migration docs</a
                  >
                }
              </span>
            </div>
          </div>
        </div>
      }

      <!-- Host readiness note (classic HostdHealthNote + suggested host CLI entries) -->
      @if (effectiveHostd(); as hd) {
        @if (!hd.reachable) {
          <div
            class="health-notice is-warn"
            role="status"
            data-testid="hostd-unreachable"
          >
            <div class="health-notice-body">
              <g-status-icon tone="blast" kind="warning" />
              <div class="health-notice-copy">
                <span>
                  The host agent is not reachable, so no
                  <code>run: host</code> server can start{{
                    hd.error ? ": " + hd.error : "."
                  }}
                </span>
              </div>
            </div>
          </div>
        } @else {
          @if ((hd.missing?.length ?? 0) > 0) {
            <div
              style="display: flex; flex-direction: column; gap: 10px"
              data-testid="hostd-health-missing"
            >
              @for (m of hd.missing ?? []; track m.name) {
                <div
                  class="health-notice is-warn"
                  role="status"
                  data-testid="hostd-entry-missing"
                >
                  <div class="health-notice-body">
                    <g-status-icon tone="blast" kind="warning" />
                    <div class="health-notice-copy">
                      <span>
                        The host agent's allowlist on the host has no
                        capabilities entry <code>{{ m.name }}</code
                        >.
                        @if (m.command || m.entry) {
                          It is grain's own entry: grain adds it automatically
                          the next time the host agent starts or is upgraded,
                          and this server attaches by itself once it is there.
                          @if (m.command) {
                            To add it now, run <code>{{ m.command }}</code> on
                            the host (nothing restarts; on an install whose
                            setup.sh predates that service, re-run setup.sh
                            once){{
                              m.entry
                                ? ", or paste this under capabilities: in " +
                                  hostdAllowlistPath +
                                  ":"
                                : "."
                            }}
                          }
                        } @else {
                          Add one under <code>capabilities:</code> in
                          <code>{{ hostdAllowlistPath }}</code> on the host
                          (grain never edits entries it does not own), or run
                          the server in the container.
                        }
                      </span>
                      @if (m.entry) {
                        <pre
                          class="dark-well"
                          tabindex="0"
                          data-testid="hostd-entry-text"
                          >{{ m.entry }}</pre>
                      }
                    </div>
                  </div>
                </div>
              }
            </div>
          }
          @if ((hd.suggest?.length ?? 0) > 0) {
            <div
              class="health-notice is-info"
              role="status"
              data-testid="hostd-suggest"
            >
              <div class="health-notice-body">
                <g-status-icon tone="neutral" kind="progress" />
                <div class="health-notice-copy">
                  <span>
                    Suggested host CLI allowlist
                    {{ hd.suggest!.length === 1 ? "entry" : "entries" }}:
                    <code>{{ hd.suggest!.join(", ") }}</code
                    >. This edition runs
                    {{ hd.suggest!.length === 1 ? "it" : "them" }} on the host
                    by default, and <code>{{ hostdAllowlistPath }}</code> has no
                    such entry. Copy it from
                    <code>{{ hostdAllowlistPath }}.default</code>; grain never
                    edits your allowlist.
                  </span>
                </div>
              </div>
            </div>
          }
        }
      }

      <!-- Stale schedules alert (GET /api/sandboxes?section=staleSchedules) -->
      @if (staleSchedules().length > 0) {
        <div
          class="health-notice is-error"
          role="alert"
          data-testid="stale-schedules"
        >
          <div class="health-notice-body">
            <g-status-icon tone="error" kind="error" />
            <div class="health-notice-copy">
              <strong>
                {{
                  staleSchedules().length === 1
                    ? "A schedule has stopped running"
                    : staleSchedules().length +
                      " schedules have stopped running"
                }}
              </strong>
              <span>
                No firing that ran anything in more than twice its cadence.
              </span>
              <ul class="health-stale-list">
                @for (s of staleSchedules(); track s.id) {
                  @let where = fmtScheduleRepo(s.repo);
                  <li data-testid="stale-schedule">
                    <a [routerLink]="['/processes', 'schedules', s.id]">{{
                      s.title || s.id
                    }}</a>
                    @if (where) {
                      <span> ({{ where }})</span>
                    }
                    @if (s.cadence) {
                      <span> · every {{ fmtCadence(s.cadence) }}</span>
                    }
                    @if (s.lastFiredAt) {
                      <span> · last ran {{ fmtStamp(s.lastFiredAt) }}</span>
                    }
                  </li>
                }
              </ul>
            </div>
          </div>
        </div>
      }

      <!-- Config-repo reset notice -->
      @if (hasConfigResetNotice()) {
        <div
          class="health-notice is-warn"
          role="status"
          data-testid="health-config-repo"
        >
          <div class="health-notice-body">
            <g-status-icon tone="blast" kind="warning" />
            <div class="health-notice-copy">
              <span>A merged change reset settings on this deployment.</span>
            </div>
          </div>
          <div class="health-notice-actions">
            <a
              class="btn-tonal"
              style="text-decoration: none"
              [routerLink]="['/settings', 'config-repo']"
              >Config repo</a
            >
          </div>
        </div>
      }

      <!-- Settings saved, pending daemon restart -->
      @if (pendingRestart().length > 0) {
        <div
          class="health-notice is-info"
          role="status"
          data-testid="health-pending-restart"
        >
          <div class="health-notice-body">
            <g-status-icon tone="neutral" kind="progress" />
            <div class="health-notice-copy">
              <span>
                Saved, not applied until restart:
                @for (key of pendingRestart(); track key; let idx = $index) {
                  @let loc = settingLoc(key);
                  @if (idx > 0) {
                    <span>, </span>
                  }
                  @if (loc) {
                    <a
                      [routerLink]="['/settings', loc.section]"
                      [fragment]="loc.target"
                      [attr.data-testid]="'pending-setting-link-' + key"
                      >{{ settingName(key) }}</a
                    >
                  } @else {
                    <span>{{ settingName(key) }}</span>
                  }
                }
                .
              </span>
            </div>
          </div>
          <div class="health-notice-actions">
            <button
              type="button"
              class="btn-tonal"
              data-testid="health-open-controls"
              (click)="openControls.emit()"
            >
              Controls
            </button>
          </div>
        </div>
      }

      <!-- Settings stored, but not runnable -->
      @if (unrunnable().length > 0) {
        <div
          class="health-notice is-warn"
          role="status"
          data-testid="health-unrunnable"
        >
          <div class="health-notice-body">
            <g-status-icon tone="blast" kind="warning" />
            <div class="health-notice-copy">
              <span>
                Stored, but not runnable:
                @for (u of unrunnable(); track u.key; let idx = $index) {
                  @let loc = settingLoc(u.key);
                  @if (idx > 0) {
                    <span>, </span>
                  }
                  @if (loc) {
                    <a
                      [routerLink]="['/settings', loc.section]"
                      [fragment]="loc.target"
                      [attr.data-testid]="'unrunnable-setting-link-' + u.key"
                      >{{ settingName(u.key) }}</a
                    >
                  } @else {
                    <span>{{ settingName(u.key) }}</span>
                  }
                  <span class="tabular">
                    (stored {{ u.stored }}, running {{ u.running }})</span
                  >
                }
                . Grain is running with a value it can use instead; save it in
                Settings to store it.
              </span>
            </div>
          </div>
        </div>
      }

      <!-- GitHub API budget & idle-poll backoff (GET /api/metrics?section=github) -->
      @if (githubView(); as gh) {
        <article
          class="ops-card"
          aria-labelledby="health-github-title"
          data-testid="health-github-budget"
        >
          <div class="ops-card-head">
            <div>
              <h2 class="ops-card-title" id="health-github-title">
                GitHub API Budget &amp; Poll Backoff
              </h2>
              <p class="ops-card-sub">
                Hourly REST rate-limit headroom, projected exhaustion, and
                per-caller spend
              </p>
            </div>
            <span
              [class]="
                gh.tone === 'error'
                  ? 'status-pill error tabular'
                  : gh.tone === 'warn'
                    ? 'status-pill blast tabular'
                    : 'status-pill healthy tabular'
              "
            >
              <g-status-icon
                [tone]="
                  gh.tone === 'error'
                    ? 'error'
                    : gh.tone === 'warn'
                      ? 'blast'
                      : 'healthy'
                "
                [kind]="
                  gh.tone === 'error'
                    ? 'error'
                    : gh.tone === 'warn'
                      ? 'warning'
                      : 'healthy'
                "
              />
              {{
                gh.tone === "error"
                  ? "Low budget"
                  : gh.tone === "warn"
                    ? "Projected exhaustion"
                    : "Within budget"
              }}
            </span>
          </div>

          <div
            class="health-notice"
            [class.is-error]="gh.tone === 'error'"
            [class.is-warn]="gh.tone === 'warn'"
            [class.is-info]="gh.tone === 'info'"
          >
            <div class="health-notice-body">
              <g-status-icon
                [tone]="
                  gh.tone === 'error'
                    ? 'error'
                    : gh.tone === 'warn'
                      ? 'blast'
                      : 'neutral'
                "
                [kind]="
                  gh.tone === 'error'
                    ? 'error'
                    : gh.tone === 'warn'
                      ? 'warning'
                      : 'progress'
                "
              />
              <div class="health-notice-copy tabular">
                <span>{{ gh.headline }}</span>
                @if (gh.runsOutLine) {
                  <span data-testid="health-github-runs-out">{{
                    gh.runsOutLine
                  }}</span>
                }
                @if (gh.pollsLine) {
                  <span data-testid="health-github-polls">{{
                    gh.pollsLine
                  }}</span>
                }
                @if (gh.lowLine) {
                  <span data-testid="health-github-low">{{ gh.lowLine }}</span>
                }
              </div>
            </div>
          </div>

          @if (gh.callers.length > 0) {
            <div
              class="ops-table-wrap health-callers-wrap"
              role="region"
              aria-label="GitHub API spend by caller"
            >
              <table
                class="ops-table health-callers-table"
                data-testid="health-github-callers"
              >
                <thead>
                  <tr>
                    <th scope="col">Caller</th>
                    <th scope="col" class="health-num-col">Spent</th>
                    <th scope="col" class="health-num-col">Free (304)</th>
                    <th scope="col">Mostly</th>
                  </tr>
                </thead>
                <tbody>
                  @for (c of gh.callers; track c.caller) {
                    <tr>
                      <td>{{ c.caller }}</td>
                      <td class="tabular health-num-col" data-label="Spent">
                        {{ c.spent }}
                      </td>
                      <td
                        class="tabular health-num-col"
                        data-label="Free (304)"
                      >
                        {{ c.revalidated }}
                      </td>
                      <td class="tabular" data-label="Mostly">
                        {{ c.mostly }}
                      </td>
                    </tr>
                  }
                </tbody>
              </table>
            </div>
          }
        </article>
      }

      <!-- Registered daemon health counters (GET /api/sandboxes?section=counters) -->
      @if (counterSections().length > 0) {
        <article
          class="ops-card"
          aria-labelledby="health-counters-title"
          data-testid="health-counters"
        >
          <div class="ops-card-head">
            <div>
              <h2 class="ops-card-title" id="health-counters-title">
                Daemon Health Counters
              </h2>
              <p class="ops-card-sub">
                In-process counters recorded since this daemon started
              </p>
            </div>
          </div>

          <div class="health-counter-sections">
            @for (sec of counterSections(); track sec.name + ":" + $index) {
              @let keys = sortedCounterKeys(sec);
              <section
                class="health-counter-section"
                data-testid="health-counters-section"
              >
                <div class="health-counter-head">
                  <h3 class="health-counter-title">{{ sec.name }}</h3>
                  <span class="muted" style="font-size: 12px"
                    >Since the daemon started.</span
                  >
                </div>
                @if (keys.length === 0) {
                  <p class="muted" style="margin: 0">Nothing counted yet.</p>
                } @else {
                  <div
                    class="ops-table-wrap"
                    role="region"
                    [attr.aria-label]="sec.name + ' counters'"
                  >
                    <table class="ops-table health-counters-table">
                      <thead>
                        <tr>
                          <th scope="col">Counter</th>
                          <th scope="col" class="health-num-col">Count</th>
                        </tr>
                      </thead>
                      <tbody>
                        @for (k of keys; track k) {
                          <tr>
                            <td class="tabular">{{ k }}</td>
                            <td class="tabular health-num-col">
                              {{ fmtCount(sec.counters[k]) }}
                            </td>
                          </tr>
                        }
                      </tbody>
                    </table>
                  </div>
                }
              </section>
            }
          </div>
        </article>
      }

      <!-- Full standing alerts list (classic AlertsPanel) -->
      <article
        class="ops-card"
        aria-labelledby="health-alerts-title"
        data-testid="health-alerts-card"
      >
        <div class="ops-card-head">
          <div>
            <h2 class="ops-card-title" id="health-alerts-title">
              Standing Alerts
            </h2>
            <p class="ops-card-sub">
              Active pauses, stalled merges, release approvals, state-repo sync
              warnings, and setup checks across this deployment
            </p>
          </div>
          <span
            [class]="
              alerts().length === 0
                ? 'status-pill healthy tabular'
                : worstAlertSeverity() === 'critical'
                  ? 'status-pill error tabular'
                  : 'status-pill blast tabular'
            "
            data-testid="health-alerts-count"
          >
            <g-status-icon
              [tone]="
                alerts().length === 0
                  ? 'healthy'
                  : worstAlertSeverity() === 'critical'
                    ? 'error'
                    : 'blast'
              "
              [kind]="
                alerts().length === 0
                  ? 'healthy'
                  : worstAlertSeverity() === 'critical'
                    ? 'error'
                    : 'warning'
              "
            />
            {{
              alerts().length === 0
                ? "No active alerts"
                : alerts().length + " active"
            }}
          </span>
        </div>

        @let groups = groupedAlerts();
        @if (alerts().length === 0) {
          <p class="muted" style="margin: 0" data-testid="alerts-empty">
            No alerts. Pauses and warnings about this deployment show up here.
          </p>
        } @else if (filteredAlerts().length === 0) {
          <p class="muted" style="margin: 0" data-testid="alerts-filter-empty">
            No alerts match "{{ viewFilter.query() }}".
          </p>
        } @else if (groups) {
          @for (g of groups; track g.heading) {
            <div data-testid="alerts-group">
              <h3 class="health-group-heading">{{ g.heading }}</h3>
              <div class="fleet-card-list">
                @for (a of g.rows; track a.id) {
                  <div
                    class="health-alert-row"
                    [class.is-critical]="a.severity === 'critical'"
                    [class.is-warning]="a.severity === 'warning'"
                    [class.is-info]="a.severity === 'info'"
                    data-testid="alert-row"
                    [attr.data-alert-id]="a.id"
                    [attr.data-severity]="a.severity"
                  >
                    <div class="health-alert-head">
                      <span
                        [class]="
                          a.severity === 'critical'
                            ? 'status-pill error'
                            : a.severity === 'warning'
                              ? 'status-pill blast'
                              : 'status-pill neutral'
                        "
                      >
                        <g-status-icon
                          [tone]="
                            a.severity === 'critical'
                              ? 'error'
                              : a.severity === 'warning'
                                ? 'blast'
                                : 'neutral'
                          "
                          [kind]="
                            a.severity === 'critical'
                              ? 'error'
                              : a.severity === 'warning'
                                ? 'warning'
                                : 'progress'
                          "
                        />
                        {{
                          a.severity === "critical"
                            ? "Critical"
                            : a.severity === "warning"
                              ? "Warning"
                              : "Info"
                        }}
                      </span>
                      <h4 class="health-alert-title">{{ a.title }}</h4>
                    </div>
                    <p class="health-alert-sentence">{{ a.sentence }}</p>
                    @if (a.impact) {
                      <p class="health-alert-impact" data-testid="alert-impact">
                        {{ a.impact }}
                      </p>
                    }
                    <div class="health-alert-foot">
                      <span class="fleet-tag" data-testid="alert-scope">{{
                        scopeLabel(a.scope)
                      }}</span>
                      @if (whenText(a); as when) {
                        <span class="muted tabular" style="font-size: 12px">{{
                          when
                        }}</span>
                      }
                      @if (a.actions.length > 0) {
                        <div class="health-alert-actions">
                          @for (act of a.actions; track act.label) {
                            @if (act.kind === "link") {
                              <a
                                class="btn-tonal"
                                style="text-decoration: none"
                                [routerLink]="act.route"
                                [queryParams]="act.queryParams ?? null"
                                [fragment]="act.fragment"
                                >{{ act.label }}</a
                              >
                            } @else {
                              <button
                                type="button"
                                [class]="
                                  act.kind === 'approve-release'
                                    ? 'btn-primary'
                                    : 'btn-tonal'
                                "
                                [disabled]="
                                  busyAction() !== null || !canRunAction(act)
                                "
                                [gTip]="actionTip(act)"
                                (click)="runAction(a.id, act)"
                              >
                                {{
                                  busyAction() === a.id + ":" + act.kind
                                    ? "Working…"
                                    : act.label
                                }}
                              </button>
                            }
                          }
                        </div>
                      }
                    </div>
                  </div>
                }
              </div>
            </div>
          }
        } @else {
          <div class="fleet-card-list">
            @for (a of filteredAlerts(); track a.id) {
              <div
                class="health-alert-row"
                [class.is-critical]="a.severity === 'critical'"
                [class.is-warning]="a.severity === 'warning'"
                [class.is-info]="a.severity === 'info'"
                data-testid="alert-row"
                [attr.data-alert-id]="a.id"
                [attr.data-severity]="a.severity"
              >
                <div class="health-alert-head">
                  <span
                    [class]="
                      a.severity === 'critical'
                        ? 'status-pill error'
                        : a.severity === 'warning'
                          ? 'status-pill blast'
                          : 'status-pill neutral'
                    "
                  >
                    <g-status-icon
                      [tone]="
                        a.severity === 'critical'
                          ? 'error'
                          : a.severity === 'warning'
                            ? 'blast'
                            : 'neutral'
                      "
                      [kind]="
                        a.severity === 'critical'
                          ? 'error'
                          : a.severity === 'warning'
                            ? 'warning'
                            : 'progress'
                      "
                    />
                    {{
                      a.severity === "critical"
                        ? "Critical"
                        : a.severity === "warning"
                          ? "Warning"
                          : "Info"
                    }}
                  </span>
                  <h3 class="health-alert-title">{{ a.title }}</h3>
                </div>
                <p class="health-alert-sentence">{{ a.sentence }}</p>
                @if (a.impact) {
                  <p class="health-alert-impact" data-testid="alert-impact">
                    {{ a.impact }}
                  </p>
                }
                <div class="health-alert-foot">
                  <span class="fleet-tag" data-testid="alert-scope">{{
                    scopeLabel(a.scope)
                  }}</span>
                  @if (whenText(a); as when) {
                    <span class="muted tabular" style="font-size: 12px">{{
                      when
                    }}</span>
                  }
                  @if (a.actions.length > 0) {
                    <div class="health-alert-actions">
                      @for (act of a.actions; track act.label) {
                        @if (act.kind === "link") {
                          <a
                            class="btn-tonal"
                            style="text-decoration: none"
                            [routerLink]="act.route"
                            [queryParams]="act.queryParams ?? null"
                            [fragment]="act.fragment"
                            >{{ act.label }}</a
                          >
                        } @else {
                          <button
                            type="button"
                            [class]="
                              act.kind === 'approve-release'
                                ? 'btn-primary'
                                : 'btn-tonal'
                            "
                            [disabled]="
                              busyAction() !== null || !canRunAction(act)
                            "
                            [gTip]="actionTip(act)"
                            (click)="runAction(a.id, act)"
                          >
                            {{
                              busyAction() === a.id + ":" + act.kind
                                ? "Working…"
                                : act.label
                            }}
                          </button>
                        }
                      }
                    </div>
                  }
                </div>
              </div>
            }
          </div>
        }
      </article>
    }
  `,
})
export class FleetHealthPanelComponent implements OnInit, OnChanges {
  protected readonly api = inject(GrainApi);
  protected readonly access = inject(AccessService);
  protected readonly feed = inject(TaskFeedStore);
  protected readonly viewFilter = inject(ViewFilter);
  private readonly snackbar = inject(SnackbarService);
  private readonly router = inject(Router);
  private readonly events = inject(GrainEvents);
  private readonly destroyRef = inject(DestroyRef);
  private readonly zone = inject(NgZone);
  private readonly doc = inject(DOCUMENT);

  readonly sandboxes = input<SandboxHealthResponse | null>(null);
  readonly refreshSeq = input<number>(0);
  readonly confirm = output<ConfirmRequest>();
  readonly openControls = output<void>();
  readonly alertCountChange = output<number>();

  protected readonly hostdAllowlistPath = HOSTD_ALLOWLIST_PATH;
  protected readonly safeDocsUrl = safeHttpOrRelativeUrl;
  protected readonly settingName = settingLabel;
  protected readonly settingLoc = settingLocation;
  protected readonly scopeLabel = alertScopeLabel;
  protected readonly fmtScheduleRepo = staleScheduleRepo;
  protected readonly fmtCadence = formatScheduleCadence;

  protected readonly settings = signal<HealthSettingsResponse | null>(null);
  protected readonly cliHostExecutor = signal<HostExecutorStatus | null>(null);
  protected readonly githubMetrics = signal<GitHubMetricsSection | null>(null);
  protected readonly counterSectionsState = signal<
    HealthCounterSection[] | null
  >(null);
  protected readonly staleSchedules = signal<StaleSchedule[]>([]);
  protected readonly notifications = signal<Notification[]>([]);
  protected readonly busyAction = signal<string | null>(null);

  private budgetPolling = false;
  private countersPolling = false;
  private stalePolling = false;
  private notificationsPolling = false;
  private destroyed = false;

  constructor() {
    this.destroyRef.onDestroy(() => {
      this.destroyed = true;
    });
    effect(() => {
      this.alertCountChange.emit(this.alerts().length);
    });
  }

  private isScopeRepoLimited(scope: string): boolean {
    return (
      !this.access.isFullAccess() &&
      Boolean(this.access.repoLimits()[scope]?.length)
    );
  }

  protected deniedScopeFallback(scope: string): string {
    return `Your role ${this.access.roleLabel() || "limited"} is repository-limited on ${scope} and cannot read deployment-wide fleet state.`;
  }

  protected readonly canReadSettings = computed(
    () =>
      this.access.can("GET /api/settings") &&
      !this.isScopeRepoLimited("settings:read"),
  );
  protected readonly canReadCLIRun = computed(
    () =>
      this.access.can("GET /api/settings/cli-run") &&
      !this.isScopeRepoLimited("settings:read"),
  );
  protected readonly canReadMetrics = computed(
    () =>
      this.access.can("GET /api/metrics") &&
      !this.isScopeRepoLimited("deployment:read"),
  );
  protected readonly canReadSandboxes = computed(
    () =>
      this.access.can("GET /api/sandboxes") &&
      !this.isScopeRepoLimited("deployment:read"),
  );
  protected readonly canReadNotifications = computed(() =>
    this.access.can("GET /api/notifications"),
  );
  protected readonly canWriteDispatch = computed(
    () =>
      this.access.can("DELETE /api/pipeline-pause") &&
      !this.isScopeRepoLimited("dispatch:write"),
  );
  protected readonly canReadAnyHealth = computed(
    () =>
      this.access.can("GET /api/config") ||
      this.canReadSettings() ||
      this.canReadMetrics() ||
      this.canReadSandboxes() ||
      this.canReadNotifications(),
  );

  protected readonly installNotice = computed(() => {
    const n = this.access.rawConfig()?.installNotice;
    return n?.message ? n : null;
  });

  protected readonly hasConfigResetNotice = computed(() =>
    Boolean(this.access.rawConfig()?.["stateRepoResetNotice"]),
  );

  protected readonly effectiveHostd = computed(
    () =>
      this.cliHostExecutor()?.hostd ??
      this.sandboxes()?.hostExecutor?.hostd ??
      null,
  );

  protected readonly pendingRestart = computed<string[]>(() => {
    const p = this.settings()?.pendingRestart;
    return Array.isArray(p) ? p.filter((k) => typeof k === "string" && k) : [];
  });

  protected readonly unrunnable = computed<UnrunnableSetting[]>(() => {
    const u = this.settings()?.unrunnable;
    return Array.isArray(u) ? u.filter((item) => Boolean(item?.key)) : [];
  });

  protected readonly githubView = computed<FormattedGitHubBudget | null>(() =>
    formatGitHubBudget(this.githubMetrics(), this.access.clockNow()),
  );

  protected readonly counterSections = computed<HealthCounterSection[]>(
    () => this.counterSectionsState() ?? this.sandboxes()?.counters ?? [],
  );

  protected readonly alerts = computed<HealthAlert[]>(() =>
    collectHealthAlerts(
      this.access.rawConfig(),
      this.feed.tasks(),
      this.notifications(),
      this.access.rawConfig()?.timeZone,
      this.access.clockNow(),
    ),
  );

  protected readonly worstAlertSeverity = computed(
    () => this.alerts()[0]?.severity ?? null,
  );

  protected readonly filteredAlerts = computed<HealthAlert[]>(() => {
    const q = this.viewFilter.query().trim().toLowerCase();
    const all = this.alerts();
    if (!q) return all;
    return all.filter(
      (a) =>
        a.title.toLowerCase().includes(q) ||
        a.sentence.toLowerCase().includes(q) ||
        (a.impact || "").toLowerCase().includes(q) ||
        alertScopeLabel(a.scope).toLowerCase().includes(q),
    );
  });

  protected readonly groupedAlerts = computed(() =>
    groupHealthAlerts(this.filteredAlerts()),
  );

  ngOnInit(): void {
    if (this.access.can("GET /api/tasks")) {
      this.feed.start(false);
      this.destroyRef.onDestroy(() => this.feed.stop(false));
    }
    void this.loadAll();

    this.zone.runOutsideAngular(() => {
      const budgetTimer = setInterval(() => {
        if (this.doc.visibilityState !== "hidden") {
          this.zone.run(() => void this.loadGitHubBudget());
        }
      }, BUDGET_REFRESH_MS);
      const countersTimer = setInterval(() => {
        if (this.doc.visibilityState !== "hidden") {
          this.zone.run(() => void this.loadCounters());
        }
      }, COUNTERS_REFRESH_MS);
      const staleTimer = setInterval(() => {
        if (this.doc.visibilityState !== "hidden") {
          this.zone.run(() => void this.loadStaleSchedules());
        }
      }, STALE_SCHEDULES_REFRESH_MS);
      const notifTimer = setInterval(() => {
        if (this.doc.visibilityState !== "hidden") {
          this.zone.run(() => void this.loadNotifications());
        }
      }, NOTIFICATIONS_REFRESH_MS);

      this.destroyRef.onDestroy(() => {
        clearInterval(budgetTimer);
        clearInterval(countersTimer);
        clearInterval(staleTimer);
        clearInterval(notifTimer);
      });
    });

    this.events
      .events()
      .pipe(
        filter((ev) => Boolean(ev.type?.startsWith("notification"))),
        takeUntilDestroyed(this.destroyRef),
      )
      .subscribe(() => void this.loadNotifications());
  }

  ngOnChanges(changes: SimpleChanges): void {
    if (changes["refreshSeq"] && !changes["refreshSeq"].firstChange) {
      void this.loadAll(true);
    }
  }

  async loadAll(refreshConfig = false): Promise<void> {
    const jobs: Promise<void>[] = [
      this.loadSettings(),
      this.loadCLIRun(),
      this.loadGitHubBudget(),
      this.loadCounters(),
      this.loadStaleSchedules(),
      this.loadNotifications(),
    ];
    if (refreshConfig && this.access.can("GET /api/config")) {
      this.api.clearConditionalCache?.("/api/config");
      jobs.push(
        this.api
          .config()
          .then((cfg) => {
            if (cfg) this.access.setConfig(cfg);
          })
          .catch(() => undefined),
      );
    }
    await Promise.all(jobs);
  }

  private async loadSettings(): Promise<void> {
    if (!this.canReadSettings()) return;
    const res = await this.api
      .get<HealthSettingsResponse>("GET /api/settings")
      .catch(() => null);
    if (res) this.settings.set(res);
  }

  private async loadCLIRun(): Promise<void> {
    if (!this.canReadCLIRun()) return;
    const res = await this.api
      .get<CLIRunSettingsResponse>("GET /api/settings/cli-run")
      .catch(() => null);
    if (res) this.cliHostExecutor.set(res.hostExecutor ?? null);
  }

  private async loadGitHubBudget(): Promise<void> {
    if (!this.canReadMetrics() || this.budgetPolling) return;
    this.budgetPolling = true;
    try {
      const res = await this.api
        .get<MetricsGitHubResponse>(
          "GET /api/metrics",
          undefined,
          "section=github",
        )
        .catch(() => null);
      if (res?.github) this.githubMetrics.set(res.github);
    } finally {
      this.budgetPolling = false;
    }
  }

  private async loadCounters(): Promise<void> {
    if (!this.canReadSandboxes() || this.countersPolling) return;
    this.countersPolling = true;
    try {
      const res = await this.api
        .get<HealthCountersResponse>(
          "GET /api/sandboxes",
          undefined,
          "section=counters",
        )
        .catch(() => null);
      if (res && "counters" in res) {
        this.counterSectionsState.set(res.counters ?? []);
      }
    } finally {
      this.countersPolling = false;
    }
  }

  private async loadStaleSchedules(): Promise<void> {
    if (!this.canReadSandboxes() || this.stalePolling) return;
    this.stalePolling = true;
    try {
      const res = await this.api
        .get<StaleSchedulesResponse>(
          "GET /api/sandboxes",
          undefined,
          "section=staleSchedules",
        )
        .catch(() => null);
      if (res) this.staleSchedules.set(res.staleSchedules ?? []);
    } finally {
      this.stalePolling = false;
    }
  }

  private async loadNotifications(): Promise<void> {
    if (!this.canReadNotifications() || this.notificationsPolling) return;
    this.notificationsPolling = true;
    try {
      const res = await this.api
        .get<Notification[] | { notifications?: Notification[] }>(
          "GET /api/notifications",
          undefined,
          "open=1&fields=list",
        )
        .catch(() => null);
      if (res) {
        const list = Array.isArray(res) ? res : (res.notifications ?? []);
        this.notifications.set(list);
      }
    } finally {
      this.notificationsPolling = false;
    }
  }

  protected sortedCounterKeys(sec: HealthCounterSection): string[] {
    return Object.keys(sec.counters ?? {}).sort();
  }

  protected fmtCount(n: number | undefined): string {
    return Number(n ?? 0).toLocaleString();
  }

  protected fmtStamp(iso: string): string {
    return formatStamp(iso, this.access.rawConfig()?.timeZone);
  }

  protected whenText(a: HealthAlert): string {
    const tz = effectiveDisplayTimeZone(this.access.rawConfig()?.timeZone);
    const parts: string[] = [];
    if (a.since) parts.push(`Since ${formatStamp(a.since, tz)}`);
    if (a.until) parts.push(`Until ${formatStamp(a.until, tz)}`);
    return parts.join(" · ");
  }

  protected canRunAction(act: HealthAlertAction): boolean {
    switch (act.kind) {
      case "link":
      case "open-power":
        return true;
      case "lift-agent-pause":
        return Boolean(act.entry.canLift) && this.canWriteDispatch();
      case "resume-pipeline":
        return this.canWriteDispatch();
      case "submit-task":
        return (
          !act.blocked &&
          this.access.can("POST /api/tasks/{id}/submit", act.repo || undefined)
        );
      case "approve-release":
        return this.access.can(
          "POST /api/repos/{owner}/{name}/pending-releases/{version}/approve",
          act.release.repo,
        );
      case "reject-release":
        return this.access.can(
          "POST /api/repos/{owner}/{name}/pending-releases/{version}/reject",
          act.release.repo,
        );
      case "ack-notification":
        return this.access.can(
          "POST /api/notifications/{id}/ack",
          act.notification.repo || undefined,
        );
      case "investigate-notification":
        return this.access.can(
          "POST /api/notifications/{id}/investigate",
          act.notification.repo || undefined,
        );
    }
  }

  protected actionTip(act: HealthAlertAction): string {
    switch (act.kind) {
      case "link":
      case "open-power":
        return "";
      case "lift-agent-pause":
        return this.canWriteDispatch()
          ? "Lift this usage-limit pause immediately"
          : this.access.deniedReason("DELETE /api/pause") ||
              this.deniedScopeFallback("dispatch:write");
      case "resume-pipeline":
        return this.canWriteDispatch()
          ? "Resume dispatching queued tasks"
          : this.access.deniedReason("DELETE /api/pipeline-pause") ||
              this.deniedScopeFallback("dispatch:write");
      case "submit-task":
        if (act.blocked) return act.blocked;
        return this.access.can(
          "POST /api/tasks/{id}/submit",
          act.repo || undefined,
        )
          ? "Approve repair pull request to merge"
          : this.access.deniedReason("POST /api/tasks/{id}/submit");
      case "approve-release":
        return this.access.can(
          "POST /api/repos/{owner}/{name}/pending-releases/{version}/approve",
          act.release.repo,
        )
          ? `Promote ${act.release.version}`
          : this.access.deniedReason(
              "POST /api/repos/{owner}/{name}/pending-releases/{version}/approve",
            );
      case "reject-release":
        return this.access.can(
          "POST /api/repos/{owner}/{name}/pending-releases/{version}/reject",
          act.release.repo,
        )
          ? `Reject ${act.release.version}`
          : this.access.deniedReason(
              "POST /api/repos/{owner}/{name}/pending-releases/{version}/reject",
            );
      case "ack-notification":
        return this.access.can(
          "POST /api/notifications/{id}/ack",
          act.notification.repo || undefined,
        )
          ? "Hide this alert from the active list until raised again"
          : this.access.deniedReason("POST /api/notifications/{id}/ack");
      case "investigate-notification":
        return this.access.can(
          "POST /api/notifications/{id}/investigate",
          act.notification.repo || undefined,
        )
          ? "File an investigation task for this alert"
          : this.access.deniedReason(
              "POST /api/notifications/{id}/investigate",
            );
    }
  }

  protected async runAction(
    alertId: string,
    act: HealthAlertAction,
  ): Promise<void> {
    if (!this.canRunAction(act) || this.busyAction() !== null) return;
    if (act.kind === "open-power") {
      this.openControls.emit();
      return;
    }
    if (act.kind === "resume-pipeline") {
      const pause = act.pause;
      const upgradeHold = isScheduledUpgradeHold(pause);
      this.confirm.emit(
        upgradeHold
          ? {
              title: "Resume and cancel the scheduled upgrade?",
              message:
                `This deployment is paused because it is waiting to upgrade${pause.reason ? ` — ${pause.reason}` : ""}. ` +
                "Resuming cancels that upgrade: tasks start again and the upgrade will not be applied. Schedule it again from Settings → Upgrade.",
              accept: "Resume and cancel upgrade",
              destructive: true,
              run: () => this.executeResumePipeline(),
            }
          : {
              title: "Resume the pipeline?",
              message:
                "Grain will start dispatching queued agent and CI runs again.",
              accept: "Resume pipeline",
              destructive: false,
              run: () => this.executeResumePipeline(),
            },
      );
      return;
    }
    if (act.kind === "approve-release" || act.kind === "reject-release") {
      const approve = act.kind === "approve-release";
      this.confirmReleaseDecision(act.release, approve);
      return;
    }

    const busyKey = `${alertId}:${act.kind}`;
    this.busyAction.set(busyKey);
    try {
      if (act.kind === "lift-agent-pause") {
        const query = liftPauseQuery(act.entry);
        await this.api.delete(
          "DELETE /api/pause",
          undefined,
          query || undefined,
        );
        await this.syncAfterDispatchMutation(true);
        this.snackbar.show("Cleared usage-limit pause.");
      } else if (act.kind === "ack-notification") {
        await this.api.post(
          "POST /api/notifications/{id}/ack",
          { id: String(act.notification.id) },
          {},
        );
        await this.loadNotifications();
        this.snackbar.show("Alert acknowledged.");
      } else if (act.kind === "investigate-notification") {
        const res = await this.api.post<{ taskId?: string; id?: string }>(
          "POST /api/notifications/{id}/investigate",
          { id: String(act.notification.id) },
          {},
        );
        await this.loadNotifications();
        const taskId = res?.taskId || res?.id;
        if (taskId && !this.destroyed) {
          void this.router.navigate(["/task", String(taskId)]);
        } else if (!this.destroyed) {
          this.snackbar.show("Investigation filed.");
        }
      } else if (act.kind === "submit-task") {
        await this.api.post(
          "POST /api/tasks/{id}/submit",
          { id: act.taskId },
          {},
        );
        void this.feed.refresh();
        this.snackbar.show(`Submitted #${act.taskId} for merge.`);
      }
    } catch (err) {
      this.snackbar.show(errorText(err));
    } finally {
      this.busyAction.set(null);
    }
  }

  private async executeResumePipeline(): Promise<void> {
    await this.api.delete("DELETE /api/pipeline-pause");
    this.access.patchConfig({ pipelinePause: { paused: false } });
    await this.syncAfterDispatchMutation(false);
    this.snackbar.show("Pipeline resumed.");
  }

  private confirmReleaseDecision(
    release: PendingRelease,
    approve: boolean,
  ): void {
    const shortSoakOk = approve && Boolean(release.shortSoak);
    const allowBreaking = approve && Boolean(release.breaking);
    const { owner, name } = splitRepo(release.repo || "ai-on-gke/grain");
    const body = approve
      ? {
          ...(shortSoakOk ? { short_soak_ok: true } : {}),
          ...(allowBreaking ? { allow_breaking: true } : {}),
        }
      : {};
    this.confirm.emit({
      title: approve
        ? `Approve ${release.version}`
        : `Reject ${release.version}`,
      message: releaseConfirm(release, approve, shortSoakOk, allowBreaking),
      accept: approve ? approveReleaseLabel(release) : "Reject",
      destructive: !approve || shortSoakOk || allowBreaking,
      run: async () => {
        await this.api.post(
          approve
            ? "POST /api/repos/{owner}/{name}/pending-releases/{version}/approve"
            : "POST /api/repos/{owner}/{name}/pending-releases/{version}/reject",
          { owner, name, version: release.version },
          body,
        );
        await this.loadNotifications();
        this.snackbar.show(
          approve
            ? `Promotion started for ${release.version}.`
            : `Rejected ${release.version}.`,
        );
      },
    });
  }

  private async syncAfterDispatchMutation(
    refreshUsage: boolean,
  ): Promise<void> {
    this.api.clearConditionalCache?.("/api/config");
    const cfg = await this.api.config().catch(() => null);
    if (cfg) this.access.setConfig(cfg);
    if (refreshUsage && this.access.can("GET /api/usage")) {
      this.api.clearConditionalCache?.("/api/usage");
      const usage = await this.api
        .get<UsagePauseInput>("GET /api/usage")
        .catch(() => null);
      if (usage && ("warnings" in usage || "scopes" in usage)) {
        this.access.setUsage(usage);
      }
    }
    this.access.tickClock();
  }
}
