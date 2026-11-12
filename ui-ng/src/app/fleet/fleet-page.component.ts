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
  type OnInit,
  ViewEncapsulation,
  computed,
  inject,
  signal,
} from "@angular/core";
import { ActivatedRoute, Router, RouterLink } from "@angular/router";
import { GrainApi } from "../api/grain-api.service";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import { TaskFeedStore } from "../services/task-feed.service";
import { ViewFilter } from "../services/view-filter.service";
import { errorText } from "../sessions/session-view";
import { ActorChipComponent } from "../shared/actor-chip.component";
import {
  ConfirmDialogComponent,
  type ConfirmRequest,
} from "../shared/confirm-dialog.component";
import { StatusIconComponent } from "../shared/status-icon.component";
import { TipDirective } from "../shared/tip.directive";
import {
  type ActionsCacheState,
  type AgentPauseEntry,
  type AgentPauseResponse,
  type DeleteRevokedHostdsResult,
  type FleetTab,
  type HostTopResponse,
  type HostdView,
  type IssuedHostdToken,
  type LogLinesResponse,
  type LogSourcesResponse,
  type LogUnit,
  type PackageCacheState,
  type PersistentAction,
  type PersistentSandboxStatus,
  type PipelinePauseResponse,
  type PipelinePauseStatus,
  type SandboxHealthResponse,
  type SandboxProbeResult,
  type SandboxSnapshot,
  extractTaskIdFromSandbox,
  formatBytes,
  formatMB,
  formatRelativeAge,
  parseArgv,
} from "./fleet.models";

const TABS: readonly { id: FleetTab; label: string }[] = [
  { id: "sandboxes", label: "Sandboxes" },
  { id: "hostds", label: "Host Daemons (hostds)" },
  { id: "controls", label: "Pauses & Controls" },
  { id: "logs", label: "Daemon Logs" },
];

const PERSISTENT_ACTIONS: readonly {
  action: PersistentAction;
  label: string;
}[] = [
  { action: "start", label: "Start" },
  { action: "restart", label: "Restart" },
  { action: "rebuild", label: "Rebuild" },
  { action: "stop", label: "Stop" },
];

@Component({
  selector: "g-fleet-page",
  imports: [
    RouterLink,
    TipDirective,
    StatusIconComponent,
    ActorChipComponent,
    ConfirmDialogComponent,
  ],
  encapsulation: ViewEncapsulation.None,
  styleUrls: [
    "../shared/work-surface.css",
    "../shared/ops-surface.css",
    "./fleet-page.component.css",
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: {
    "(document:click)": "onDocClick()",
  },
  template: `
    <section
      class="view-panel is-active"
      aria-labelledby="fleet-headline"
      [attr.inert]="confirmReq() ? '' : null"
      data-testid="fleet-page"
    >
      <div class="stage-header">
        <div class="view-headline-row">
          <div class="headline-cluster">
            <h1
              class="view-headline"
              id="fleet-headline"
              tabindex="-1"
              data-testid="fleet-headline"
            >
              Fleet &amp; Host Operations
            </h1>
            <span
              class="status-pill neutral tabular"
              data-testid="fleet-backend-pill"
            >
              <g-status-icon tone="neutral" kind="progress" />
              Backend: {{ effectiveSandboxBackend() }}
            </span>
            @if (pipelinePause()?.paused) {
              <span class="status-pill blast" data-testid="fleet-paused-pill">
                <g-status-icon tone="blast" kind="warning" />
                Pipeline paused
              </span>
            }
          </div>
          <div class="headline-actions">
            <button
              type="button"
              class="btn-tonal"
              data-testid="fleet-top-toggle"
              [disabled]="!canReadSandboxes()"
              [gTip]="
                canReadSandboxes()
                  ? 'Inspect live host process snapshot'
                  : access.deniedReason('GET /api/host/top')
              "
              (click)="toggleHostTop()"
            >
              {{ topOpen() ? "Hide host top" : "Host top" }}
            </button>
            <button
              type="button"
              class="btn-tonal"
              data-testid="fleet-refresh"
              [disabled]="loading()"
              (click)="refreshAll()"
            >
              Refresh
            </button>
          </div>
        </div>
        <p class="view-subtitle">
          Live sandboxes, persistent staging environments, token-landing host
          daemons, dispatch gates, caches, and daemon journals.
        </p>

        <div
          class="tab-strip"
          role="tablist"
          aria-label="Fleet sections"
          data-testid="fleet-tabs"
        >
          @for (t of tabs; track t.id) {
            <button
              type="button"
              role="tab"
              class="tab-link fleet-tab-btn"
              [id]="'fleet-tab-btn-' + t.id"
              [attr.aria-selected]="tab() === t.id"
              [attr.aria-controls]="'fleet-panel-' + t.id"
              [attr.tabindex]="tab() === t.id ? 0 : -1"
              [attr.data-testid]="'fleet-tab-' + t.id"
              (click)="pickTab(t.id)"
              (keydown)="onTabKeydown($event, t.id)"
            >
              <span>{{ t.label }}</span>
              @if (t.id === "sandboxes") {
                <span class="tab-count tabular">{{
                  (sandboxes()?.sandboxes?.length ?? 0) +
                    persistentSandboxes().length
                }}</span>
              } @else if (t.id === "hostds") {
                <span class="tab-count tabular">{{ hostds().length }}</span>
              }
            </button>
          }
        </div>
      </div>

      @if (loadError(); as err) {
        <p class="banner banner-error" role="alert" data-testid="fleet-error">
          {{ err }}
        </p>
      }

      <div class="ops-scroll">
        <!-- Dispatch Blocked Alert (pkg/ui/dispatch_conditions.go) -->
        @for (blk of sandboxes()?.dispatchBlocked ?? []; track $index) {
          <div
            class="banner banner-error"
            style="margin: 0"
            role="alert"
            data-testid="fleet-dispatch-blocked"
          >
            <strong
              >Dispatch blocked{{
                blk.name ? " (" + blk.name + ")" : ""
              }}:</strong
            >
            {{ blk.error || blk.name }}
            @if (blk.since) {
              <span class="tabular"> · since {{ fmtAge(blk.since) }} ago</span>
            }
            @if (blk.remedy) {
              <span> — Remedy: {{ blk.remedy }}</span>
            }
          </div>
        }

        <!-- Host & Capacity Strip -->
        @if (canReadSandboxes()) {
          <div class="fleet-host-strip" data-testid="fleet-host-strip">
            <article class="ops-kpi-card" data-testid="meter-cpu">
              <div class="ops-kpi-top">
                <span class="ops-kpi-label">Host CPU &amp; Load</span>
                <span class="status-pill neutral tabular">
                  <g-status-icon tone="neutral" kind="progress" />
                  {{ sandboxes()?.host?.cpus ?? "—" }} cores
                </span>
              </div>
              <div class="ops-kpi-value tabular">
                {{
                  ((sandboxes()?.host?.cpuUtilization ?? 0) * 100).toFixed(0)
                }}%
              </div>
              <div class="meter-track">
                <div
                  [class]="
                    (sandboxes()?.host?.cpuUtilization ?? 0) >= 0.9
                      ? 'meter-fill is-error'
                      : (sandboxes()?.host?.cpuUtilization ?? 0) >= 0.75
                        ? 'meter-fill is-warn'
                        : 'meter-fill is-healthy'
                  "
                  [style.width.%]="
                    Math.min(
                      100,
                      (sandboxes()?.host?.cpuUtilization ?? 0) * 100
                    )
                  "
                ></div>
              </div>
              <div class="fleet-meter-meta tabular">
                <span
                  >Load 1/5/15m:
                  {{ (sandboxes()?.host?.loadAverage1 ?? 0).toFixed(2) }} /
                  {{ (sandboxes()?.host?.loadAverage5 ?? 0).toFixed(2) }} /
                  {{ (sandboxes()?.host?.loadAverage15 ?? 0).toFixed(2) }}</span
                >
              </div>
            </article>

            <article class="ops-kpi-card" data-testid="meter-vcpu">
              <div class="ops-kpi-top">
                <span class="ops-kpi-label">vCPU Allocation</span>
                <span
                  [class]="
                    sandboxes()?.budget?.withinCap === false
                      ? 'status-pill blast'
                      : 'status-pill healthy'
                  "
                >
                  <g-status-icon
                    [tone]="
                      sandboxes()?.budget?.withinCap === false
                        ? 'blast'
                        : 'healthy'
                    "
                    [kind]="
                      sandboxes()?.budget?.withinCap === false
                        ? 'warning'
                        : 'healthy'
                    "
                  />
                  {{
                    sandboxes()?.budget?.withinCap === false
                      ? "Over cap"
                      : "Within cap"
                  }}
                </span>
              </div>
              <div class="ops-kpi-value tabular">
                {{ sandboxes()?.budget?.committedCPUs ?? 0 }} /
                {{
                  sandboxes()?.budget?.budgetCPUs ??
                    sandboxes()?.host?.cpus ??
                    "—"
                }}
                vCPU
              </div>
              <div class="meter-track">
                <div class="meter-fill" [style.width.%]="vcpuPct()"></div>
              </div>
              <div class="fleet-meter-meta tabular">
                <span
                  >{{ sandboxes()?.budget?.committedCount ?? 0 }} committed
                  sandboxes</span
                >
              </div>
            </article>

            <article class="ops-kpi-card" data-testid="meter-memory">
              <div class="ops-kpi-top">
                <span class="ops-kpi-label">Host Memory</span>
                <span class="status-pill neutral tabular">
                  <g-status-icon tone="neutral" kind="progress" />
                  {{ fmtMB(sandboxes()?.host?.memoryTotalMB) }}
                </span>
              </div>
              <div class="ops-kpi-value tabular">
                {{ fmtMB(sandboxes()?.host?.memoryUsedMB) }}
              </div>
              <div class="meter-track">
                <div
                  [class]="
                    memPct() >= 90
                      ? 'meter-fill is-error'
                      : memPct() >= 75
                        ? 'meter-fill is-warn'
                        : 'meter-fill is-healthy'
                  "
                  [style.width.%]="memPct()"
                ></div>
              </div>
              <div class="fleet-meter-meta tabular">
                <span
                  >Swap: {{ fmtMB(sandboxes()?.host?.swapUsedMB) }} /
                  {{ fmtMB(sandboxes()?.host?.swapTotalMB) }}</span
                >
              </div>
            </article>

            <article class="ops-kpi-card" data-testid="meter-disk">
              <div class="ops-kpi-top">
                <span class="ops-kpi-label">Primary Disk</span>
                <span class="status-pill neutral tabular">
                  <g-status-icon tone="neutral" kind="progress" />
                  {{ primaryDisk()?.path || "/" }}
                </span>
              </div>
              <div class="ops-kpi-value tabular">
                {{ fmtMB(primaryDisk()?.usedMB) }} /
                {{ fmtMB(primaryDisk()?.totalMB) }}
              </div>
              <div class="meter-track">
                <div
                  [class]="
                    diskPct() >= 90
                      ? 'meter-fill is-error'
                      : diskPct() >= 75
                        ? 'meter-fill is-warn'
                        : 'meter-fill is-healthy'
                  "
                  [style.width.%]="diskPct()"
                ></div>
              </div>
              <div class="fleet-meter-meta tabular">
                <span>
                  {{ (primaryDisk()?.holds ?? []).join(", ") || "host volume" }}
                  @if (primaryDisk()?.floorMB) {
                    · floor {{ fmtMB(primaryDisk()?.floorMB) }}
                  }
                </span>
              </div>
            </article>
          </div>
        }

        <!-- Expandable Host Top Snapshot -->
        @if (topOpen()) {
          <article class="ops-card" data-testid="fleet-host-top">
            <div class="ops-card-head">
              <div>
                <h2 class="ops-card-title">Host Process Snapshot (top)</h2>
                <p class="ops-card-sub">
                  Live process utilization on the daemon host (GET
                  /api/host/top)
                </p>
              </div>
              <button type="button" class="btn-tonal" (click)="loadHostTop()">
                Refresh top
              </button>
            </div>
            @if (!hostTop()?.enabled) {
              <p class="muted" style="margin: 0">
                Host process snapshot is not enabled on this deployment.
              </p>
            } @else {
              <pre
                class="dark-well"
                tabindex="0"
                aria-label="Host top output"
                >{{
                  (hostTop()?.lines ?? []).join(
                    "
"
                  )
                }}</pre>
            }
          </article>
        }

        <!-- TAB 1: SANDBOXES -->
        @if (tab() === "sandboxes") {
          <div
            id="fleet-panel-sandboxes"
            role="tabpanel"
            aria-labelledby="fleet-tab-btn-sandboxes"
            style="display: flex; flex-direction: column; gap: 20px"
          >
            @if (!canReadSandboxes() && !canReadPersistent()) {
              <p class="empty-state" data-testid="fleet-sandboxes-denied">
                {{
                  access.deniedReason("GET /api/sandboxes") ||
                    deniedScopeFallback("deployment:read")
                }}
              </p>
            } @else {
              <article class="ops-card" data-testid="fleet-live-sandboxes">
                <div class="ops-card-head">
                  <div>
                    <h2 class="ops-card-title">Live Task Sandboxes</h2>
                    <p class="ops-card-sub">
                      Active isolated execution guests running in-flight tasks
                      and standing services
                    </p>
                  </div>
                </div>

                @if (!canReadSandboxes()) {
                  <p class="empty-state" data-testid="fleet-sandboxes-denied">
                    {{
                      access.deniedReason("GET /api/sandboxes") ||
                        deniedScopeFallback("deployment:read")
                    }}
                  </p>
                } @else if (filteredSandboxes().length === 0) {
                  <p
                    class="muted"
                    style="margin: 0"
                    data-testid="sandboxes-empty"
                  >
                    No active task sandboxes right now.
                  </p>
                } @else {
                  <div class="ops-table-wrap">
                    <table class="ops-table">
                      <thead>
                        <tr>
                          <th scope="col">Sandbox</th>
                          <th scope="col">Status</th>
                          <th scope="col">Task</th>
                          <th scope="col">Backend</th>
                          <th scope="col">Shape &amp; Memory</th>
                          <th scope="col">Load</th>
                          <th scope="col">Age</th>
                          <th scope="col">Actions</th>
                        </tr>
                      </thead>
                      <tbody>
                        @for (sb of filteredSandboxes(); track sb.sandbox) {
                          @let tid = taskIdOf(sb);
                          <tr [attr.data-testid]="'sandbox-row-' + sb.sandbox">
                            <td class="tabular">
                              <strong>{{ sb.name || sb.sandbox }}</strong>
                              @if (sb.networkAccess) {
                                <span class="muted">
                                  · {{ sb.networkAccess }}</span
                                >
                              }
                            </td>
                            <td>
                              <span
                                [class]="
                                  sb.ready && !sb.error
                                    ? 'status-pill healthy'
                                    : 'status-pill error'
                                "
                              >
                                <g-status-icon
                                  [tone]="
                                    sb.ready && !sb.error ? 'healthy' : 'error'
                                  "
                                  [kind]="
                                    sb.ready && !sb.error ? 'healthy' : 'error'
                                  "
                                />
                                {{
                                  sb.ready && !sb.error
                                    ? "Ready"
                                    : sb.error ||
                                      sb.terminationReason ||
                                      "Not ready"
                                }}
                              </span>
                            </td>
                            <td class="tabular">
                              @if (tid) {
                                <a [routerLink]="['/task', tid]">#{{ tid }}</a>
                              } @else {
                                <span class="muted">{{ sb.kind || "—" }}</span>
                              }
                            </td>
                            <td class="tabular">{{ sb.backend }}</td>
                            <td class="tabular">
                              {{ sb.cpus ?? "—" }} vCPU ·
                              {{ fmtMB(sb.memoryUsedMB ?? sb.memoryMB) }}
                            </td>
                            <td class="tabular">{{ sb.loadAverage || "—" }}</td>
                            <td class="tabular">{{ fmtAge(sb.startedAt) }}</td>
                            <td>
                              @if (tid && canRecreateTaskSandbox()) {
                                <button
                                  type="button"
                                  class="btn-text"
                                  [attr.data-testid]="'sandbox-recreate-' + tid"
                                  (click)="recreateTaskSandbox(tid)"
                                >
                                  Recreate
                                </button>
                              }
                            </td>
                          </tr>
                        }
                      </tbody>
                    </table>
                  </div>
                }

                <!-- Sandbox Exec Probe -->
                <div
                  class="fleet-item-card"
                  data-testid="fleet-sandbox-probe"
                  style="margin-top: 6px"
                >
                  <div class="fleet-item-head">
                    <span class="fleet-item-title"
                      >Sandbox Diagnostic Probe</span
                    >
                    <span class="muted" style="font-size: 12px"
                      >Boots a throwaway sandbox, executes argv, and tears it
                      down</span
                    >
                  </div>
                  <div class="fleet-inline-bar">
                    <input
                      type="text"
                      class="ops-input tabular"
                      style="flex: 1 1 240px"
                      aria-label="Probe command argv"
                      data-testid="probe-cmd-input"
                      [value]="probeCmd()"
                      (input)="probeCmd.set($any($event.target).value)"
                    />
                    <button
                      type="button"
                      class="btn-tonal"
                      data-testid="probe-run-btn"
                      [disabled]="probing() || !canProbeSandboxes()"
                      [gTip]="probeTip()"
                      (click)="runSandboxProbe()"
                    >
                      {{ probing() ? "Probing…" : "Run probe" }}
                    </button>
                  </div>
                  @if (probeResult(); as pr) {
                    <pre
                      class="dark-well"
                      tabindex="0"
                      data-testid="probe-output"
                    >
sandbox={{ pr.sandbox }} pod={{ pr.pod }} exit={{ pr.exitCode }}
{{ pr.stdout }}{{ pr.stderr }}</pre>
                  }
                </div>
              </article>

              <!-- Persistent / Staging Sandboxes -->
              <article
                class="ops-card"
                data-testid="fleet-persistent-sandboxes"
              >
                <div class="ops-card-head">
                  <div>
                    <h2 class="ops-card-title">
                      Persistent &amp; Staging Sandboxes
                    </h2>
                    <p class="ops-card-sub">
                      Long-lived repository checkouts and staging servers
                      supervised outside worker slots
                    </p>
                  </div>
                </div>

                @if (!canReadPersistent()) {
                  <p class="empty-state" data-testid="fleet-persistent-denied">
                    {{
                      access.deniedReason("GET /api/persistent-sandboxes") ||
                        deniedScopeFallback("sandboxes:read")
                    }}
                  </p>
                } @else if (persistentSandboxes().length === 0) {
                  <p class="muted" style="margin: 0">
                    No persistent or staging sandboxes configured on this
                    deployment.
                  </p>
                } @else {
                  <div class="fleet-card-list">
                    @for (ps of persistentSandboxes(); track ps.name) {
                      <div
                        class="fleet-item-card"
                        [attr.data-testid]="'persistent-card-' + ps.name"
                      >
                        <div class="fleet-item-head">
                          <div class="fleet-item-title">
                            <span class="tabular">{{ ps.name }}</span>
                            <span
                              [class]="
                                ps.state === 'running'
                                  ? 'status-pill healthy'
                                  : ps.lastError
                                    ? 'status-pill error'
                                    : 'status-pill neutral'
                              "
                            >
                              <g-status-icon
                                [tone]="
                                  ps.state === 'running'
                                    ? 'healthy'
                                    : ps.lastError
                                      ? 'error'
                                      : 'neutral'
                                "
                                [kind]="
                                  ps.state === 'running'
                                    ? 'running'
                                    : ps.lastError
                                      ? 'error'
                                      : 'neutral'
                                "
                              />
                              {{
                                ps.state || (ps.enabled ? "idle" : "stopped")
                              }}
                            </span>
                            <span class="muted tabular">{{ ps.repo }}</span>
                            @if (ps.ref) {
                              <span class="fleet-tag">{{ ps.ref }}</span>
                            }
                          </div>

                          <div class="fleet-inline-bar">
                            @for (act of persistentActions; track act.action) {
                              <button
                                type="button"
                                class="btn-tonal"
                                [attr.data-testid]="
                                  'persistent-' + act.action + '-' + ps.name
                                "
                                [disabled]="
                                  !canControlPersistent(ps) ||
                                  busyPersistent() ===
                                    ps.name + ':' + act.action
                                "
                                [gTip]="
                                  canControlPersistent(ps)
                                    ? act.label +
                                      ' persistent sandbox ' +
                                      ps.name
                                    : access.deniedReason(
                                        'POST /api/persistent-sandboxes/{name}/{action}',
                                        ps.repo || undefined
                                      )
                                "
                                (click)="controlPersistent(ps, act.action)"
                              >
                                {{ act.label }}
                              </button>
                            }
                            <button
                              type="button"
                              class="btn-text"
                              [attr.data-testid]="
                                'persistent-logs-btn-' + ps.name
                              "
                              [disabled]="!canReadPersistentLogs(ps)"
                              [gTip]="
                                canReadPersistentLogs(ps)
                                  ? 'View logs for ' + ps.name
                                  : access.deniedReason(
                                      'GET /api/persistent-sandboxes/{name}/logs',
                                      ps.repo || undefined
                                    )
                              "
                              (click)="togglePersistentLogs(ps)"
                            >
                              {{
                                openPersistentLogName() === ps.name
                                  ? "Hide logs"
                                  : "Logs"
                              }}
                            </button>
                          </div>
                        </div>

                        <div class="fleet-item-meta tabular">
                          <span>cmd: {{ ps.command }}</span>
                          <span>restarts: {{ ps.restarts }}</span>
                          @if (ps.uiSubpath) {
                            <span>subpath: {{ ps.uiSubpath }}</span>
                          }
                          @if (ps.hostPort) {
                            <span>port: :{{ ps.hostPort }}</span>
                          }
                          @if (ps.lastError) {
                            <span style="color: var(--md-sys-color-error)">{{
                              ps.lastError
                            }}</span>
                          }
                        </div>

                        @if (openPersistentLogName() === ps.name) {
                          <pre
                            class="dark-well"
                            tabindex="0"
                            [attr.data-testid]="'persistent-logs-' + ps.name"
                            >{{
                              persistentLogsText() || "(no output recorded)"
                            }}</pre>
                        }
                      </div>
                    }
                  </div>
                }
              </article>
            }
          </div>
        }

        <!-- TAB 2: HOST DAEMONS (hostds) -->
        @if (tab() === "hostds") {
          <div
            id="fleet-panel-hostds"
            role="tabpanel"
            aria-labelledby="fleet-tab-btn-hostds"
            style="display: flex; flex-direction: column; gap: 20px"
          >
            <article class="ops-card" data-testid="fleet-hostds">
              <div class="ops-card-head">
                <div>
                  <h2 class="ops-card-title">Host Daemons (grain-agentd)</h2>
                  <p class="ops-card-sub">
                    Co-located and remote token-landing hosts executing CLI
                    turns and host capabilities
                  </p>
                </div>
                <div class="headline-actions">
                  @if (hasRevokedHostds() && canAdminHostds()) {
                    <button
                      type="button"
                      class="btn-tonal"
                      data-testid="hostds-delete-revoked"
                      (click)="confirmDeleteRevokedHostds()"
                    >
                      Delete all revoked
                    </button>
                  }
                  <a class="btn-tonal" routerLink="/settings/hostds">
                    Enrollment &amp; settings
                  </a>
                </div>
              </div>

              @if (issuedToken(); as issued) {
                <div
                  class="fleet-token-banner"
                  data-testid="hostd-issued-token"
                >
                  <div class="fleet-item-head">
                    <strong
                      >New token for {{ issued.hostd.name }} (shown
                      once)</strong
                    >
                    <div class="fleet-inline-bar">
                      <button
                        type="button"
                        class="btn-tonal"
                        (click)="copyText(issued.token, 'Copied host token.')"
                      >
                        Copy token
                      </button>
                      <button
                        type="button"
                        class="btn-text"
                        (click)="issuedToken.set(null)"
                      >
                        Dismiss
                      </button>
                    </div>
                  </div>
                  <code class="fleet-token-code">{{ issued.token }}</code>
                </div>
              }

              @if (!canReadHostds()) {
                <p class="empty-state" data-testid="fleet-hostds-denied">
                  {{
                    access.deniedReason("GET /api/hostds") ||
                      deniedScopeFallback("capabilities:read")
                  }}
                </p>
              } @else if (filteredHostds().length === 0) {
                <p class="muted" style="margin: 0">
                  No host daemons registered on this deployment.
                </p>
              } @else {
                <div class="fleet-card-list">
                  @for (h of filteredHostds(); track h.name) {
                    @let isLocal = h.name === "local";
                    <div
                      class="fleet-item-card"
                      [attr.data-testid]="'hostd-row-' + h.name"
                    >
                      <div class="fleet-item-head">
                        <div class="fleet-item-title">
                          <span class="tabular">{{ h.name }}</span>
                          <span
                            [class]="
                              h.state === 'connected'
                                ? 'status-pill healthy'
                                : h.state === 'revoked'
                                  ? 'status-pill error'
                                  : 'status-pill quiet'
                            "
                          >
                            <g-status-icon
                              [tone]="
                                h.state === 'connected'
                                  ? 'healthy'
                                  : h.state === 'revoked'
                                    ? 'error'
                                    : 'quiet'
                              "
                              [kind]="
                                h.state === 'connected'
                                  ? 'healthy'
                                  : h.state === 'revoked'
                                    ? 'error'
                                    : 'warning'
                              "
                            />
                            {{ h.state }}
                          </span>
                          @if (isLocal) {
                            <span class="fleet-tag">co-located</span>
                          }
                          @if (h.owner) {
                            <g-actor-chip [label]="h.owner" kind="human" />
                          }
                        </div>

                        @if (!isLocal && !h.shared && canAdminHostds()) {
                          <div class="fleet-inline-bar">
                            @if (h.state !== "revoked") {
                              <button
                                type="button"
                                class="btn-tonal"
                                [attr.data-testid]="'hostd-rotate-' + h.name"
                                (click)="rotateHostd(h.name)"
                              >
                                Rotate token
                              </button>
                              <button
                                type="button"
                                class="btn-tonal"
                                [attr.data-testid]="
                                  'hostd-rename-open-' + h.name
                                "
                                (click)="startRenameHostd(h.name)"
                              >
                                Rename
                              </button>
                              <button
                                type="button"
                                class="btn-text danger"
                                [attr.data-testid]="'hostd-revoke-' + h.name"
                                (click)="confirmRevokeHostd(h.name)"
                              >
                                Revoke
                              </button>
                            } @else {
                              <button
                                type="button"
                                class="btn-text danger"
                                [attr.data-testid]="'hostd-delete-' + h.name"
                                (click)="confirmDeleteHostd(h.name)"
                              >
                                Delete
                              </button>
                            }
                          </div>
                        }
                      </div>

                      @if (renamingHostd() === h.name) {
                        <div class="fleet-inline-bar">
                          <input
                            type="text"
                            class="ops-input tabular"
                            aria-label="New hostd name"
                            [attr.data-testid]="'hostd-rename-input-' + h.name"
                            [value]="renameDraft()"
                            (input)="renameDraft.set($any($event.target).value)"
                          />
                          <button
                            type="button"
                            class="btn-primary"
                            [attr.data-testid]="'hostd-rename-save-' + h.name"
                            (click)="submitRenameHostd(h.name)"
                          >
                            Save name
                          </button>
                          <button
                            type="button"
                            class="btn-text"
                            (click)="renamingHostd.set(null)"
                          >
                            Cancel
                          </button>
                        </div>
                      }

                      <div class="fleet-item-meta tabular">
                        @if (h.hostname) {
                          <span>host: {{ h.hostname }}</span>
                        }
                        @if (h.os || h.arch) {
                          <span>{{ h.os }}/{{ h.arch }}</span>
                        }
                        @if (h.version) {
                          <span>{{ h.version }}</span>
                        }
                        @if (h.capacity) {
                          <span>cap: {{ h.capacity }}</span>
                        }
                        @if (h.token_prefix) {
                          <span>token: {{ h.token_prefix }}…</span>
                        }
                        @if (h.last_seen_at) {
                          <span>seen {{ fmtAge(h.last_seen_at) }} ago</span>
                        }
                      </div>

                      @if (
                        (h.clis?.length ?? 0) > 0 || (h.logins?.length ?? 0) > 0
                      ) {
                        <div class="fleet-chip-list">
                          @for (cli of h.clis ?? []; track cli) {
                            <span class="fleet-tag">cli:{{ cli }}</span>
                          }
                          @for (login of h.logins ?? []; track login) {
                            <span class="fleet-tag">login:{{ login }}</span>
                          }
                        </div>
                      }
                    </div>
                  }
                </div>
              }
            </article>
          </div>
        }

        <!-- TAB 3: PAUSES, CACHES & DAEMON LIFECYCLE -->
        @if (tab() === "controls") {
          <div
            id="fleet-panel-controls"
            role="tabpanel"
            aria-labelledby="fleet-tab-btn-controls"
            style="display: flex; flex-direction: column; gap: 20px"
          >
            @if (
              !canReadDispatch() &&
              !canReadCaches() &&
              !canReadSandboxes() &&
              !canRestartDaemon()
            ) {
              <p class="empty-state" data-testid="fleet-controls-denied">
                {{
                  access.deniedReason("GET /api/pipeline-pause") ||
                    deniedScopeFallback("dispatch:read")
                }}
              </p>
            } @else {
              <div class="ops-grid-2">
                <!-- Pipeline Dispatch Gate -->
                <article class="ops-card" data-testid="fleet-pipeline-card">
                  <div class="ops-card-head">
                    <div>
                      <h2 class="ops-card-title">Pipeline Dispatch Gate</h2>
                      <p class="ops-card-sub">
                        Holds new task pickup while letting in-flight runs
                        finish
                      </p>
                    </div>
                    @if (canReadDispatch()) {
                      <span
                        [class]="
                          pipelinePause()?.paused
                            ? 'status-pill blast'
                            : 'status-pill healthy'
                        "
                        data-testid="pipeline-pause-state"
                      >
                        <g-status-icon
                          [tone]="pipelinePause()?.paused ? 'blast' : 'healthy'"
                          [kind]="
                            pipelinePause()?.paused ? 'warning' : 'healthy'
                          "
                        />
                        {{ pipelinePause()?.paused ? "Paused" : "Dispatching" }}
                      </span>
                    }
                  </div>

                  @if (!canReadDispatch()) {
                    <p class="empty-state">
                      {{
                        access.deniedReason("GET /api/pipeline-pause") ||
                          deniedScopeFallback("dispatch:read")
                      }}
                    </p>
                  } @else if (pipelinePause()?.paused) {
                    <div class="fleet-item-meta">
                      <span
                        >Reason: {{ pipelinePause()?.reason || "paused" }}</span
                      >
                      @if (pipelinePause()?.byLabel || pipelinePause()?.byId) {
                        <g-actor-chip
                          [label]="
                            pipelinePause()?.byLabel ||
                            pipelinePause()?.byId ||
                            'Operator'
                          "
                          [kind]="pipelinePause()?.byKind || 'human'"
                        />
                      }
                    </div>
                    <div>
                      <button
                        type="button"
                        class="btn-primary"
                        data-testid="pipeline-resume-btn"
                        [disabled]="!canWriteDispatch() || busyDispatch()"
                        [gTip]="
                          canWriteDispatch()
                            ? 'Resume dispatching queued tasks'
                            : access.deniedReason('DELETE /api/pipeline-pause')
                        "
                        (click)="resumePipeline()"
                      >
                        Resume pipeline
                      </button>
                    </div>
                  } @else {
                    <div class="fleet-inline-bar">
                      <input
                        type="text"
                        class="ops-input"
                        style="flex: 1 1 200px"
                        placeholder="Optional pause reason (e.g. maintenance window)"
                        aria-label="Pipeline pause reason"
                        data-testid="pipeline-pause-reason"
                        [value]="pauseReasonDraft()"
                        (input)="
                          pauseReasonDraft.set($any($event.target).value)
                        "
                      />
                      <button
                        type="button"
                        class="btn-tonal"
                        data-testid="pipeline-pause-btn"
                        [disabled]="!canWriteDispatch() || busyDispatch()"
                        [gTip]="
                          canWriteDispatch()
                            ? 'Pause new task dispatch'
                            : access.deniedReason('POST /api/pipeline-pause')
                        "
                        (click)="pausePipeline()"
                      >
                        Pause pipeline
                      </button>
                    </div>
                  }
                </article>

                <!-- Usage-Limit Pauses -->
                <article class="ops-card" data-testid="fleet-usage-pauses-card">
                  <div class="ops-card-head">
                    <div>
                      <h2 class="ops-card-title">Model Usage-Limit Pauses</h2>
                      <p class="ops-card-sub">
                        Provider quota windows automatically holding affected
                        connections
                      </p>
                    </div>
                    @if (
                      (agentPause()?.pauses?.length ?? 0) > 1 &&
                      canWriteDispatch()
                    ) {
                      <button
                        type="button"
                        class="btn-tonal"
                        data-testid="lift-all-pauses-btn"
                        (click)="liftAgentPause()"
                      >
                        Lift all
                      </button>
                    }
                  </div>

                  @if (!canReadDispatch()) {
                    <p class="empty-state">
                      {{
                        access.deniedReason("GET /api/pause") ||
                          deniedScopeFallback("dispatch:read")
                      }}
                    </p>
                  } @else if ((agentPause()?.pauses?.length ?? 0) === 0) {
                    <p class="muted" style="margin: 0">
                      No model connections are currently paused on quota limits.
                    </p>
                  } @else {
                    <div class="fleet-card-list">
                      @for (
                        p of agentPause()?.pauses ?? [];
                        track (p.connection || "") + ":" + (p.model || "")
                      ) {
                        <div class="fleet-item-card">
                          <div class="fleet-item-head">
                            <span class="tabular"
                              ><strong>{{
                                p.connectionName || p.connection || p.framework
                              }}</strong>
                              @if (p.model) {
                                · {{ p.model }}
                              }
                            </span>
                            @if (p.canLift && canWriteDispatch()) {
                              <button
                                type="button"
                                class="btn-tonal"
                                (click)="liftAgentPause(p)"
                              >
                                Lift pause
                              </button>
                            }
                          </div>
                          <div class="fleet-item-meta tabular">
                            <span>{{
                              p.reason || "provider rate/usage limit"
                            }}</span>
                            <span
                              >Resets in
                              {{ Math.ceil(p.secondsRemaining / 60) }}m</span
                            >
                          </div>
                        </div>
                      }
                    </div>
                  }
                </article>

                <!-- Package & Actions Caches -->
                <article class="ops-card" data-testid="fleet-caches-card">
                  <div class="ops-card-head">
                    <div>
                      <h2 class="ops-card-title">Proxy &amp; Actions Caches</h2>
                      <p class="ops-card-sub">
                        On-controller package proxy and GitHub Actions build
                        caches
                      </p>
                    </div>
                  </div>

                  @if (!canReadCaches()) {
                    <p class="empty-state">
                      {{
                        access.deniedReason("GET /api/package-cache") ||
                          deniedScopeFallback("caches:read")
                      }}
                    </p>
                  } @else {
                    <div class="fleet-item-card">
                      <div class="fleet-item-head">
                        <span class="fleet-item-title"
                          >Package Proxy Cache</span
                        >
                        <button
                          type="button"
                          class="btn-tonal"
                          data-testid="flush-package-cache-btn"
                          [disabled]="
                            !canWriteCaches() || !packageCache()?.enabled
                          "
                          [gTip]="
                            canWriteCaches()
                              ? 'Flush cached packages'
                              : access.deniedReason(
                                  'POST /api/package-cache/flush'
                                )
                          "
                          (click)="confirmFlushPackageCache()"
                        >
                          Flush package cache
                        </button>
                      </div>
                      @if (packageCache()?.enabled) {
                        <div class="fleet-chip-list">
                          @for (
                            eco of packageCache()?.cache ?? [];
                            track eco.kind
                          ) {
                            <span class="fleet-tag"
                              >{{ eco.kind }}: {{ eco.entries }} ({{
                                fmtBytes(eco.bytes)
                              }})</span
                            >
                          }
                        </div>
                      } @else {
                        <span class="muted" style="font-size: 12px"
                          >Package proxy cache not enabled on this host.</span
                        >
                      }
                    </div>

                    <div class="fleet-item-card">
                      <div class="fleet-item-head">
                        <span class="fleet-item-title">Actions Cache</span>
                        <button
                          type="button"
                          class="btn-tonal"
                          data-testid="flush-actions-cache-btn"
                          [disabled]="
                            !canWriteCaches() || !actionsCache()?.enabled
                          "
                          [gTip]="
                            canWriteCaches()
                              ? 'Flush GitHub Actions cache archives'
                              : access.deniedReason(
                                  'POST /api/actions-cache/flush'
                                )
                          "
                          (click)="confirmFlushActionsCache()"
                        >
                          Flush actions cache
                        </button>
                      </div>
                      @if (actionsCache()?.enabled) {
                        <div class="fleet-item-meta tabular">
                          <span
                            >{{ actionsCache()?.entries ?? 0 }} entries</span
                          >
                          <span
                            >{{ fmtBytes(actionsCache()?.bytes) }} /
                            {{ fmtBytes(actionsCache()?.maxBytes) }}</span
                          >
                        </div>
                      } @else {
                        <span class="muted" style="font-size: 12px"
                          >Actions cache not enabled on this host.</span
                        >
                      }
                    </div>
                  }
                </article>

                <!-- Daemon Lifecycle & Restart -->
                <article class="ops-card" data-testid="fleet-daemon-card">
                  <div class="ops-card-head">
                    <div>
                      <h2 class="ops-card-title">Daemon Lifecycle</h2>
                      <p class="ops-card-sub">
                        Service uptime, start history, and controlled service
                        restart
                      </p>
                    </div>
                    @if (sandboxes()?.daemon?.restarting || watchRestarting()) {
                      <span class="status-pill error">
                        <g-status-icon tone="error" kind="error" />
                        Restarting
                      </span>
                    } @else {
                      <span class="status-pill healthy">
                        <g-status-icon tone="healthy" kind="healthy" />
                        Up {{ fmtAge(sandboxes()?.daemon?.startedAt) }}
                      </span>
                    }
                  </div>

                  <div class="fleet-item-meta tabular">
                    @if (sandboxes()?.daemon?.starts) {
                      <span
                        >Starts: {{ sandboxes()?.daemon?.starts }}
                        {{ sandboxes()?.daemon?.startsScope || "" }}</span
                      >
                    }
                    @if (sandboxes()?.daemon?.logCommand) {
                      <span>{{ sandboxes()?.daemon?.logCommand }}</span>
                    }
                  </div>

                  <div class="fleet-inline-bar">
                    <button
                      type="button"
                      class="btn-primary is-destructive"
                      data-testid="daemon-restart-btn"
                      [disabled]="!canRestartDaemon()"
                      [gTip]="
                        canRestartDaemon()
                          ? 'Restart the grain daemon service (asks confirmation first)'
                          : access.deniedReason('POST /api/host/restart')
                      "
                      (click)="confirmRestartDaemon()"
                    >
                      Restart daemon
                    </button>
                  </div>
                </article>
              </div>
            }
          </div>
        }

        <!-- TAB 4: DAEMON & UNIT LOGS -->
        @if (tab() === "logs") {
          <div
            id="fleet-panel-logs"
            role="tabpanel"
            aria-labelledby="fleet-tab-btn-logs"
            style="display: flex; flex-direction: column; gap: 20px"
          >
            @if (!canReadLogs()) {
              <p class="empty-state" data-testid="fleet-logs-denied">
                {{
                  access.deniedReason("GET /api/logs") ||
                    deniedScopeFallback("deployment:read")
                }}
              </p>
            } @else {
              <article class="ops-card" data-testid="fleet-logs-card">
                <div class="ops-card-head">
                  <div>
                    <h2 class="ops-card-title">Daemon &amp; Unit Logs</h2>
                    <p class="ops-card-sub">
                      Redacted journal and subsystem tails (GET
                      /api/logs/&#123;source&#125;)
                    </p>
                  </div>
                  <div class="headline-actions">
                    <span
                      class="status-pill neutral tabular"
                      data-testid="logs-line-count"
                    >
                      <g-status-icon tone="neutral" kind="progress" />
                      {{ filteredLogLines().length }} /
                      {{ logLines().length }} lines
                    </span>
                    <button
                      type="button"
                      class="btn-tonal"
                      data-testid="logs-refresh-btn"
                      (click)="loadLogLines()"
                    >
                      Refresh
                    </button>
                    <button
                      type="button"
                      class="btn-tonal"
                      data-testid="logs-copy-btn"
                      [disabled]="filteredLogLines().length === 0"
                      (click)="
                        copyText(
                          filteredLogLines().join(
                            '
'
                          ),
                          'Copied log lines.'
                        )
                      "
                    >
                      Copy
                    </button>
                  </div>
                </div>

                @if (!logSources()?.enabled) {
                  <p class="muted" style="margin: 0">
                    No log sources are configured on this deployment.
                  </p>
                } @else {
                  <div class="fleet-log-toolbar">
                    <div
                      class="fleet-log-pills"
                      role="group"
                      aria-label="Log source"
                    >
                      @for (src of logSources()?.sources ?? []; track src) {
                        <button
                          type="button"
                          class="filter-chip"
                          [attr.aria-pressed]="selectedLogSource() === src"
                          [attr.data-testid]="'log-source-' + src"
                          (click)="pickLogSource(src)"
                        >
                          {{ src }}
                        </button>
                      }
                    </div>

                    @if (logUnits().length > 1) {
                      <div
                        class="segmented"
                        role="group"
                        aria-label="Systemd unit"
                      >
                        @for (u of logUnits(); track u.unit) {
                          <button
                            type="button"
                            [attr.aria-pressed]="selectedLogUnit() === u.unit"
                            (click)="pickLogUnit(u.unit)"
                          >
                            {{ u.unit }} ({{ u.scope }})
                          </button>
                        }
                      </div>
                    }

                    <div class="segmented" role="group" aria-label="Tail lines">
                      @for (cnt of [200, 500, 1000]; track cnt) {
                        <button
                          type="button"
                          [attr.aria-pressed]="logLineLimit() === cnt"
                          (click)="pickLogLimit(cnt)"
                        >
                          {{ cnt }}
                        </button>
                      }
                    </div>

                    <input
                      type="search"
                      class="ops-input"
                      style="flex: 1 1 200px"
                      placeholder="Filter log lines…"
                      aria-label="Filter log lines"
                      data-testid="logs-search-input"
                      [value]="logQuery()"
                      (input)="logQuery.set($any($event.target).value)"
                    />
                  </div>

                  <pre
                    class="dark-well"
                    tabindex="0"
                    aria-label="Daemon log output"
                    data-testid="logs-viewer"
                    >{{
                      filteredLogLines().length > 0
                        ? filteredLogLines().join(
                            "
"
                          )
                        : "(no matching log lines)"
                    }}</pre>
                }
              </article>
            }
          </div>
        }
      </div>
    </section>

    @if (confirmReq(); as req) {
      <g-confirm-dialog [request]="req" (done)="onConfirmDone($event)" />
    }
  `,
})
export class FleetPageComponent implements OnInit {
  protected readonly api = inject(GrainApi);
  protected readonly access = inject(AccessService);
  protected readonly feed = inject(TaskFeedStore);
  protected readonly viewFilter = inject(ViewFilter);
  private readonly snackbar = inject(SnackbarService);
  private readonly route = inject(ActivatedRoute);
  private readonly router = inject(Router);
  private readonly doc = inject(DOCUMENT);

  protected readonly Math = Math;
  protected readonly tabs = TABS;
  protected readonly persistentActions = PERSISTENT_ACTIONS;
  protected readonly fmtBytes = formatBytes;
  protected readonly fmtMB = formatMB;
  protected readonly fmtAge = formatRelativeAge;
  protected readonly taskIdOf = extractTaskIdFromSandbox;

  protected readonly tab = signal<FleetTab>("sandboxes");
  protected readonly loading = signal(false);
  protected readonly loadError = signal<string | null>(null);
  protected readonly confirmReq = signal<ConfirmRequest | null>(null);

  protected readonly hostSandboxes = signal(false);
  protected readonly sandboxes = signal<SandboxHealthResponse | null>(null);
  protected readonly topOpen = signal(false);
  protected readonly hostTop = signal<HostTopResponse | null>(null);

  protected readonly persistentList = signal<PersistentSandboxStatus[] | null>(
    null,
  );
  protected readonly busyPersistent = signal<string | null>(null);
  protected readonly openPersistentLogName = signal<string | null>(null);
  protected readonly persistentLogsText = signal<string>("");

  protected readonly probeCmd = signal<string>("uname -a");
  protected readonly probing = signal(false);
  protected readonly probeResult = signal<SandboxProbeResult | null>(null);

  protected readonly hostds = signal<HostdView[]>([]);
  protected readonly issuedToken = signal<IssuedHostdToken | null>(null);
  protected readonly renamingHostd = signal<string | null>(null);
  protected readonly renameDraft = signal<string>("");

  protected readonly pipelinePause = signal<PipelinePauseStatus | null>(null);
  protected readonly pauseReasonDraft = signal<string>("");
  protected readonly busyDispatch = signal(false);
  protected readonly agentPause = signal<AgentPauseResponse["pause"] | null>(
    null,
  );
  protected readonly packageCache = signal<PackageCacheState | null>(null);
  protected readonly actionsCache = signal<ActionsCacheState | null>(null);
  protected readonly watchRestarting = signal(false);

  protected readonly logSources = signal<LogSourcesResponse | null>(null);
  protected readonly selectedLogSource = signal<string>("");
  protected readonly selectedLogUnit = signal<string>("");
  protected readonly logUnits = signal<LogUnit[]>([]);
  protected readonly logLineLimit = signal<number>(500);
  protected readonly logLines = signal<string[]>([]);
  protected readonly logQuery = signal<string>("");

  private isScopeRepoLimited(scope: string): boolean {
    return (
      !this.access.isFullAccess() &&
      Boolean(this.access.repoLimits()[scope]?.length)
    );
  }

  protected deniedScopeFallback(scope: string): string {
    return `Your role ${this.access.roleLabel() || "limited"} is repository-limited on ${scope} and cannot read deployment-wide fleet state.`;
  }

  protected readonly canReadSandboxes = computed(
    () =>
      this.access.can("GET /api/sandboxes") &&
      !this.isScopeRepoLimited("deployment:read"),
  );
  protected readonly canReadPersistent = computed(
    () =>
      this.access.can("GET /api/persistent-sandboxes") &&
      !this.isScopeRepoLimited("sandboxes:read"),
  );
  protected readonly canRestartDaemon = computed(
    () =>
      this.access.can("POST /api/host/restart") &&
      !this.isScopeRepoLimited("deployment:restart"),
  );
  protected readonly canRecreateTaskSandbox = computed(() =>
    this.access.can("POST /api/tasks/{id}/sandbox/recreate"),
  );
  protected readonly canReadHostds = computed(
    () =>
      this.access.can("GET /api/hostds") &&
      !this.isScopeRepoLimited("capabilities:read"),
  );
  protected readonly canAdminHostds = computed(
    () =>
      this.access.can("POST /api/hostds/{name}/revoke") &&
      !this.isScopeRepoLimited("capabilities:admin"),
  );
  protected readonly canReadDispatch = computed(
    () =>
      this.access.can("GET /api/pipeline-pause") &&
      !this.isScopeRepoLimited("dispatch:read"),
  );
  protected readonly canWriteDispatch = computed(
    () =>
      this.access.can("POST /api/pipeline-pause") &&
      !this.isScopeRepoLimited("dispatch:write"),
  );
  protected readonly canReadCaches = computed(
    () =>
      this.access.can("GET /api/package-cache") &&
      !this.isScopeRepoLimited("caches:read"),
  );
  protected readonly canWriteCaches = computed(
    () =>
      this.access.can("POST /api/package-cache/flush") &&
      !this.isScopeRepoLimited("caches:write"),
  );
  protected readonly canReadLogs = computed(
    () =>
      this.access.can("GET /api/logs") &&
      !this.isScopeRepoLimited("deployment:read"),
  );

  protected readonly effectiveSandboxBackend = computed<string>(() => {
    if (this.hostSandboxes()) return "host";
    const first = this.sandboxes()?.sandboxes?.[0]?.backend;
    if (first) return first;
    if (this.sandboxes()?.enabled === false) return "disabled";
    return "granule";
  });

  protected readonly canProbeSandboxes = computed(
    () =>
      !this.hostSandboxes() &&
      this.effectiveSandboxBackend() !== "host" &&
      this.sandboxes()?.enabled !== false &&
      !this.isScopeRepoLimited("deployment:restart") &&
      this.access.can("POST /api/sandbox-probe"),
  );

  protected readonly probeTip = computed<string>(() => {
    if (!this.access.can("POST /api/sandbox-probe")) {
      return this.access.deniedReason("POST /api/sandbox-probe");
    }
    if (this.isScopeRepoLimited("deployment:restart")) {
      return this.deniedScopeFallback("deployment:restart");
    }
    if (this.hostSandboxes() || this.effectiveSandboxBackend() === "host") {
      return "Sandbox diagnostic probe is unavailable when running host sandboxes.";
    }
    if (this.sandboxes()?.enabled === false) {
      return "Sandboxes are disabled on this deployment.";
    }
    return "Run diagnostic probe (POST /api/sandbox-probe)";
  });

  protected canControlPersistent(ps: PersistentSandboxStatus): boolean {
    const repo = ps.repo || undefined;
    if (!repo && this.isScopeRepoLimited("deployment:restart")) return false;
    return this.access.can(
      "POST /api/persistent-sandboxes/{name}/{action}",
      repo,
    );
  }

  protected canReadPersistentLogs(ps: PersistentSandboxStatus): boolean {
    const repo = ps.repo || undefined;
    if (!repo && this.isScopeRepoLimited("deployment:read")) return false;
    return this.access.can("GET /api/persistent-sandboxes/{name}/logs", repo);
  }

  protected readonly vcpuPct = computed<number>(() => {
    const b = this.sandboxes()?.budget;
    const cap = b?.budgetCPUs || this.sandboxes()?.host?.cpus || 0;
    if (!cap) return 0;
    return Math.min(100, ((b?.committedCPUs ?? 0) / cap) * 100);
  });

  protected readonly memPct = computed<number>(() => {
    const h = this.sandboxes()?.host;
    if (!h?.memoryTotalMB) return 0;
    return Math.min(100, (h.memoryUsedMB / h.memoryTotalMB) * 100);
  });

  protected readonly primaryDisk = computed(
    () => this.sandboxes()?.host?.disks?.[0] ?? null,
  );

  protected readonly diskPct = computed<number>(() => {
    const d = this.primaryDisk();
    if (!d?.totalMB) return 0;
    return Math.min(100, (d.usedMB / d.totalMB) * 100);
  });

  protected readonly persistentSandboxes = computed<PersistentSandboxStatus[]>(
    () => this.persistentList() ?? this.sandboxes()?.persistentSandboxes ?? [],
  );

  protected readonly filteredSandboxes = computed<SandboxSnapshot[]>(() => {
    const q = this.viewFilter.query().trim().toLowerCase();
    const list = this.sandboxes()?.sandboxes ?? [];
    if (!q) return list;
    return list.filter(
      (s) =>
        s.sandbox.toLowerCase().includes(q) ||
        s.name.toLowerCase().includes(q) ||
        s.backend.toLowerCase().includes(q),
    );
  });

  protected readonly filteredHostds = computed<HostdView[]>(() => {
    const q = this.viewFilter.query().trim().toLowerCase();
    const list = this.hostds();
    if (!q) return list;
    return list.filter(
      (h) =>
        h.name.toLowerCase().includes(q) ||
        (h.owner || "").toLowerCase().includes(q) ||
        (h.hostname || "").toLowerCase().includes(q),
    );
  });

  protected readonly hasRevokedHostds = computed<boolean>(() =>
    this.hostds().some((h) => h.state === "revoked"),
  );

  protected readonly filteredLogLines = computed<string[]>(() => {
    const q = (this.logQuery() || this.viewFilter.query()).trim().toLowerCase();
    const lines = this.logLines();
    if (!q) return lines;
    return lines.filter((line) => line.toLowerCase().includes(q));
  });

  ngOnInit(): void {
    const t = this.route.snapshot.queryParamMap.get("tab");
    if (
      t === "sandboxes" ||
      t === "hostds" ||
      t === "controls" ||
      t === "logs"
    ) {
      this.tab.set(t);
    }
    void this.bootstrap();
  }

  private async bootstrap(): Promise<void> {
    await this.loadBootstrapConfig();
    await this.refreshAll();
  }

  private async loadBootstrapConfig(): Promise<void> {
    if (!this.access.can("GET /api/config")) return;
    try {
      const cfg = await this.api.config();
      if (cfg) {
        this.access.setConfig(cfg);
        this.hostSandboxes.set(Boolean(cfg["hostSandboxes"]));
      }
    } catch {
      // Non-fatal.
    }
  }

  async refreshAll(): Promise<void> {
    this.loading.set(true);
    this.loadError.set(null);
    try {
      await Promise.all([
        this.loadSandboxes(),
        this.loadPersistentSandboxes(),
        this.loadHostds(),
        this.loadControls(),
        this.loadLogSources(),
      ]);
    } finally {
      this.loading.set(false);
    }
  }

  async loadSandboxes(): Promise<void> {
    if (!this.canReadSandboxes()) return;
    try {
      const res =
        await this.api.get<SandboxHealthResponse>("GET /api/sandboxes");
      this.sandboxes.set(res);
    } catch (err) {
      this.loadError.set(errorText(err));
    }
  }

  async loadPersistentSandboxes(): Promise<void> {
    if (!this.canReadPersistent()) return;
    try {
      const res = await this.api.get<
        | PersistentSandboxStatus[]
        | {
            items?: PersistentSandboxStatus[];
            sandboxes?: PersistentSandboxStatus[];
          }
      >("GET /api/persistent-sandboxes");
      const list = Array.isArray(res)
        ? res
        : (res?.items ?? res?.sandboxes ?? []);
      this.persistentList.set(list);
    } catch {
      // Falls back to sandboxes.persistentSandboxes.
    }
  }

  async loadHostds(): Promise<void> {
    if (!this.canReadHostds()) return;
    try {
      const res = await this.api.get<
        HostdView[] | { items?: HostdView[]; hostds?: HostdView[] }
      >("GET /api/hostds");
      this.hostds.set(
        Array.isArray(res) ? res : (res?.items ?? res?.hostds ?? []),
      );
    } catch {
      // Optional if caller lacks capabilities:read.
    }
  }

  async loadControls(): Promise<void> {
    const jobs: Promise<unknown>[] = [];
    if (this.canReadDispatch()) {
      jobs.push(
        this.api
          .get<PipelinePauseResponse>("GET /api/pipeline-pause")
          .then((r) => this.pipelinePause.set(r?.pause ?? null))
          .catch(() => undefined),
        this.api
          .get<AgentPauseResponse>("GET /api/pause")
          .then((r) => this.agentPause.set(r?.pause ?? null))
          .catch(() => undefined),
      );
    }
    if (this.canReadCaches()) {
      jobs.push(
        this.api
          .get<PackageCacheState>("GET /api/package-cache")
          .then((r) => this.packageCache.set(r))
          .catch(() => undefined),
        this.api
          .get<ActionsCacheState>("GET /api/actions-cache")
          .then((r) => this.actionsCache.set(r))
          .catch(() => undefined),
      );
    }
    await Promise.all(jobs);
  }

  async loadLogSources(): Promise<void> {
    if (!this.canReadLogs()) return;
    try {
      const res = await this.api.get<LogSourcesResponse>("GET /api/logs");
      this.logSources.set(res);
      const list = res?.sources ?? [];
      if (res?.enabled && list.length > 0 && !this.selectedLogSource()) {
        const chosen = list.includes("daemon") ? "daemon" : list[0];
        this.selectedLogSource.set(chosen);
        await this.loadLogLines();
      } else if (this.selectedLogSource()) {
        await this.loadLogLines();
      }
    } catch {
      // Optional when logs disabled.
    }
  }

  async loadLogLines(): Promise<void> {
    const src = this.selectedLogSource();
    if (!src || !this.canReadLogs()) return;
    const params = new URLSearchParams();
    params.set("lines", String(this.logLineLimit()));
    if (this.selectedLogUnit()) params.set("unit", this.selectedLogUnit());
    try {
      const res = await this.api.get<LogLinesResponse>(
        "GET /api/logs/{source}",
        { source: src },
        params.toString(),
      );
      this.logLines.set(res?.lines ?? []);
      this.logUnits.set(res?.units ?? []);
      if (res?.unit?.unit && !this.selectedLogUnit()) {
        this.selectedLogUnit.set(res.unit.unit);
      }
    } catch (err) {
      this.snackbar.show(errorText(err));
    }
  }

  protected pickTab(t: FleetTab): void {
    this.tab.set(t);
    void this.router.navigate([], {
      relativeTo: this.route,
      queryParams: { tab: t === "sandboxes" ? null : t },
      queryParamsHandling: "merge",
      replaceUrl: true,
    });
    if (t === "logs" && this.selectedLogSource() && !this.logLines().length) {
      void this.loadLogLines();
    }
  }

  protected onTabKeydown(ev: KeyboardEvent, current: FleetTab): void {
    const idx = this.tabs.findIndex((t) => t.id === current);
    if (idx < 0) return;
    let nextIdx = -1;
    if (ev.key === "ArrowRight") nextIdx = (idx + 1) % this.tabs.length;
    else if (ev.key === "ArrowLeft") {
      nextIdx = (idx - 1 + this.tabs.length) % this.tabs.length;
    } else if (ev.key === "Home") nextIdx = 0;
    else if (ev.key === "End") nextIdx = this.tabs.length - 1;
    if (nextIdx < 0) return;
    ev.preventDefault();
    const nextTab = this.tabs[nextIdx].id;
    this.pickTab(nextTab);
    this.doc.getElementById(`fleet-tab-btn-${nextTab}`)?.focus();
  }

  protected toggleHostTop(): void {
    const next = !this.topOpen();
    this.topOpen.set(next);
    if (next) void this.loadHostTop();
  }

  async loadHostTop(): Promise<void> {
    if (!this.canReadSandboxes()) return;
    try {
      const res = await this.api.get<HostTopResponse>(
        "GET /api/host/top",
        undefined,
        "lines=100",
      );
      this.hostTop.set(res);
    } catch (err) {
      this.snackbar.show(errorText(err));
    }
  }

  protected async recreateTaskSandbox(taskId: string): Promise<void> {
    if (!this.canRecreateTaskSandbox()) return;
    try {
      await this.api.post("POST /api/tasks/{id}/sandbox/recreate", {
        id: taskId,
      });
      this.snackbar.show(`Recreated sandbox for task #${taskId}.`);
      await this.loadSandboxes();
    } catch (err) {
      this.snackbar.show(errorText(err));
    }
  }

  protected async runSandboxProbe(): Promise<void> {
    if (!this.canProbeSandboxes() || this.probing()) return;
    const argv = parseArgv(this.probeCmd());
    if (argv.length === 0) return;
    this.probing.set(true);
    try {
      const res = await this.api.post<SandboxProbeResult>(
        "POST /api/sandbox-probe",
        undefined,
        { id: `ui-${Date.now()}`, argv },
      );
      this.probeResult.set(res);
    } catch (err) {
      this.snackbar.show(errorText(err));
    } finally {
      this.probing.set(false);
    }
  }

  protected async controlPersistent(
    ps: PersistentSandboxStatus,
    action: PersistentAction,
  ): Promise<void> {
    if (!this.canControlPersistent(ps)) return;
    const key = `${ps.name}:${action}`;
    this.busyPersistent.set(key);
    try {
      await this.api.post(
        "POST /api/persistent-sandboxes/{name}/{action}",
        { name: ps.name, action },
        {},
      );
      this.snackbar.show(`Persistent sandbox ${ps.name}: ${action} requested.`);
      await this.loadPersistentSandboxes();
    } catch (err) {
      this.snackbar.show(errorText(err));
    } finally {
      this.busyPersistent.set(null);
    }
  }

  protected async togglePersistentLogs(
    ps: PersistentSandboxStatus,
  ): Promise<void> {
    if (!this.canReadPersistentLogs(ps)) return;
    if (this.openPersistentLogName() === ps.name) {
      this.openPersistentLogName.set(null);
      return;
    }
    this.openPersistentLogName.set(ps.name);
    this.persistentLogsText.set("Loading…");
    try {
      const txt = await this.api.getText(
        "GET /api/persistent-sandboxes/{name}/logs",
        { name: ps.name },
        "format=text",
      );
      this.persistentLogsText.set(txt ?? "");
    } catch (err) {
      this.persistentLogsText.set(errorText(err));
    }
  }

  protected async rotateHostd(name: string): Promise<void> {
    if (!this.canAdminHostds()) return;
    try {
      const issued = await this.api.post<IssuedHostdToken>(
        "POST /api/hostds/{name}/rotate",
        { name },
        {},
      );
      this.issuedToken.set(issued);
      this.snackbar.show(`Rotated token for host ${name}.`);
      await this.loadHostds();
    } catch (err) {
      this.snackbar.show(errorText(err));
    }
  }

  protected startRenameHostd(name: string): void {
    this.renamingHostd.set(name);
    this.renameDraft.set(name);
  }

  protected async submitRenameHostd(oldName: string): Promise<void> {
    const next = this.renameDraft().trim();
    if (!next || next === oldName || !this.canAdminHostds()) return;
    try {
      await this.api.post(
        "POST /api/hostds/{name}/rename",
        { name: oldName },
        { name: next },
      );
      this.renamingHostd.set(null);
      this.snackbar.show(`Renamed host ${oldName} to ${next}.`);
      await this.loadHostds();
    } catch (err) {
      this.snackbar.show(errorText(err));
    }
  }

  protected confirmRevokeHostd(name: string): void {
    this.confirmReq.set({
      title: `Revoke host ${name}?`,
      message:
        "Revoking disconnects this host immediately and invalidates all of its tokens.",
      accept: "Revoke host",
      destructive: true,
      run: async () => {
        await this.api.post("POST /api/hostds/{name}/revoke", { name }, {});
        this.snackbar.show(`Revoked host ${name}.`);
        await this.loadHostds();
      },
    });
  }

  protected confirmDeleteHostd(name: string): void {
    this.confirmReq.set({
      title: `Delete revoked host ${name}?`,
      message: `Removes ${name} from the hostd registry and clears any host-scoped quota pauses.`,
      accept: "Delete host",
      destructive: true,
      run: async () => {
        await this.api.delete("DELETE /api/hostds/{name}", { name });
        this.snackbar.show(`Deleted host ${name}.`);
        await this.loadHostds();
      },
    });
  }

  protected confirmDeleteRevokedHostds(): void {
    this.confirmReq.set({
      title: "Delete all revoked hosts?",
      message:
        "Removes every revoked host not still referenced by model connections or capability servers.",
      accept: "Delete revoked",
      destructive: true,
      run: async () => {
        const res = await this.api.post<DeleteRevokedHostdsResult>(
          "POST /api/hostds/delete-revoked",
          undefined,
          {},
        );
        this.snackbar.show(
          `Deleted ${res?.deleted?.length ?? 0} revoked host(s).`,
        );
        await this.loadHostds();
      },
    });
  }

  protected async pausePipeline(): Promise<void> {
    if (!this.canWriteDispatch() || this.busyDispatch()) return;
    this.busyDispatch.set(true);
    try {
      const res = await this.api.post<PipelinePauseResponse>(
        "POST /api/pipeline-pause",
        undefined,
        { reason: this.pauseReasonDraft().trim() },
      );
      this.pipelinePause.set(res?.pause ?? { paused: true });
      this.pauseReasonDraft.set("");
      this.snackbar.show("Pipeline paused.");
    } catch (err) {
      this.snackbar.show(errorText(err));
    } finally {
      this.busyDispatch.set(false);
    }
  }

  protected async resumePipeline(): Promise<void> {
    if (!this.canWriteDispatch() || this.busyDispatch()) return;
    this.busyDispatch.set(true);
    try {
      const res = await this.api.delete<PipelinePauseResponse>(
        "DELETE /api/pipeline-pause",
      );
      this.pipelinePause.set(res?.pause ?? { paused: false });
      this.snackbar.show("Pipeline resumed.");
    } catch (err) {
      this.snackbar.show(errorText(err));
    } finally {
      this.busyDispatch.set(false);
    }
  }

  protected async liftAgentPause(entry?: AgentPauseEntry): Promise<void> {
    if (!this.canWriteDispatch()) return;
    const params = new URLSearchParams();
    if (entry?.connection) params.set("connection", entry.connection);
    if (entry?.model) params.set("model", entry.model);
    if (!entry?.connection && entry?.team) params.set("team", entry.team);
    if (!entry?.connection && entry?.framework) {
      params.set("framework", entry.framework);
    }
    try {
      const res = await this.api.delete<AgentPauseResponse>(
        "DELETE /api/pause",
        undefined,
        undefined,
        params.toString() || undefined,
      );
      this.agentPause.set(res?.pause ?? null);
      this.snackbar.show("Cleared usage-limit pause.");
    } catch (err) {
      this.snackbar.show(errorText(err));
    }
  }

  protected confirmFlushPackageCache(): void {
    this.confirmReq.set({
      title: "Flush package proxy cache?",
      message:
        "Deletes all cached packages across apt, npm, PyPI, Go, and Docker on this controller. Active runs remain safe and fetch from upstream on cache miss.",
      accept: "Flush cache",
      destructive: true,
      run: async () => {
        const res = await this.api.post<{
          entries: number;
          bytes: number;
          cache?: PackageCacheState["cache"];
        }>("POST /api/package-cache/flush", undefined, {});
        this.packageCache.set({ enabled: true, cache: res?.cache ?? [] });
        this.snackbar.show(
          `Flushed ${res?.entries ?? 0} cached package entries (${formatBytes(res?.bytes)}).`,
        );
      },
    });
  }

  protected confirmFlushActionsCache(): void {
    this.confirmReq.set({
      title: "Flush GitHub Actions cache?",
      message:
        "Empties cached CI build archives across all repositories on this controller.",
      accept: "Flush cache",
      destructive: true,
      run: async () => {
        const res = await this.api.post<{
          entries: number;
          bytes: number;
          cache: ActionsCacheState;
        }>("POST /api/actions-cache/flush", undefined, {});
        if (res?.cache) this.actionsCache.set(res.cache);
        this.snackbar.show(
          `Flushed ${res?.entries ?? 0} Actions cache archives (${formatBytes(res?.bytes)}).`,
        );
      },
    });
  }

  protected confirmRestartDaemon(): void {
    this.confirmReq.set({
      title: "Restart grain daemon?",
      message:
        "Interrupts in-flight agent runs driven by this process and briefly disconnects the UI while the service restarts.",
      accept: "Restart daemon",
      destructive: true,
      run: async () => {
        this.watchRestarting.set(true);
        try {
          await this.api.post("POST /api/host/restart", undefined, {});
        } catch {
          // A dropped connection is expected while the daemon restarts.
        }
        this.snackbar.show(
          "Restart requested. Waiting for grain to come back…",
        );
        try {
          const watch = await this.api.get<{ restarting: boolean }>(
            "GET /api/restart-watch",
            undefined,
            "quick=1",
          );
          this.watchRestarting.set(!!watch?.restarting);
        } catch {
          // Will clear on next refresh once reachable.
        }
      },
    });
  }

  protected async onConfirmDone(accepted: boolean): Promise<void> {
    const req = this.confirmReq();
    this.confirmReq.set(null);
    if (!accepted || !req) return;
    try {
      await req.run();
    } catch (err) {
      this.snackbar.show(errorText(err));
    }
  }

  protected pickLogSource(src: string): void {
    if (this.selectedLogSource() === src) return;
    this.selectedLogSource.set(src);
    this.selectedLogUnit.set("");
    this.logUnits.set([]);
    void this.loadLogLines();
  }

  protected pickLogUnit(unit: string): void {
    if (this.selectedLogUnit() === unit) return;
    this.selectedLogUnit.set(unit);
    void this.loadLogLines();
  }

  protected pickLogLimit(limit: number): void {
    if (this.logLineLimit() === limit) return;
    this.logLineLimit.set(limit);
    void this.loadLogLines();
  }

  protected copyText(text: string, notice: string): void {
    void this.doc.defaultView?.navigator?.clipboard
      ?.writeText(text)
      .then(() => this.snackbar.show(notice))
      .catch(() => this.snackbar.show(notice));
  }

  protected onDocClick(): void {
    // Reserved for future popovers on Fleet.
  }
}
