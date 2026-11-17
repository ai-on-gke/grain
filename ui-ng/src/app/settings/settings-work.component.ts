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
  type OnChanges,
  type OnInit,
  type SimpleChanges,
  ViewEncapsulation,
  computed,
  inject,
  input,
  signal,
} from "@angular/core";
import { RouterLink } from "@angular/router";
import { GrainApi } from "../api/grain-api.service";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import {
  ConfirmDialogComponent,
  type ConfirmRequest,
} from "../shared/confirm-dialog.component";
import {
  type PillOption,
  PillSelectComponent,
} from "../shared/pill-select.component";
import type {
  QAExperimentArm,
  QAExperimentSummary,
} from "../telemetry/qa-experiments.models";
import { type CapabilityCatalogue, retiredNotices } from "./capability-retired";
import type { SettingsSectionId } from "./settings-sections";
import { SettingsStateService, errText } from "./settings-state.service";

interface ConsoleSessionRow {
  id: string;
  name?: string;
  title?: string;
  model?: string;
  state?: string;
}

interface CapabilityServerConfig {
  command?: string;
  args?: string[];
  serverUrl?: string;
  disabled?: boolean;
  run?: string;
  builtin?: string;
}

interface CapabilityServerRow {
  id: string;
  config?: CapabilityServerConfig;
  builtin?: unknown;
  inProcess?: { name?: string; summary?: string } | null;
  status?: {
    attached?: boolean;
    error?: string;
    reason?: string;
    state?: string;
  } | null;
}

// capServerState is the chip a server row shows: disabled when an
// operator turned it off, otherwise the server's own status.state
// ("attached" reads as enabled; needs-config / needs-hostd say what is
// missing, in host wording; failed reads as an error) rather than
// "enabled" for a server that cannot attach yet.
export function capServerState(srv: CapabilityServerRow): {
  label: string;
  tone: string;
} {
  if (srv.config?.disabled) return { label: "disabled", tone: "neutral" };
  const st = String(srv.status?.state ?? "").trim();
  if (!st || st === "attached") return { label: "enabled", tone: "healthy" };
  if (st === "needs-config") {
    return { label: "needs-configuring", tone: "blast" };
  }
  // needs-hostd:<selector> is wire format (daemon.NeedsHostd); the chip
  // says host, as the classic CapabilityServersPanel's needsHostLabel does.
  if (st === "needs-hostd" || st.startsWith("needs-hostd:")) {
    const sel = st.slice("needs-hostd:".length).trim();
    return { label: sel ? `needs host ${sel}` : "needs host", tone: "blast" };
  }
  if (st === "failed") return { label: "failed", tone: "error" };
  return { label: st, tone: st.startsWith("needs") ? "blast" : "neutral" };
}

export interface CapabilityPolicyRule {
  capability: string;
  repos?: string[];
  runKinds?: string[];
  events?: string[];
  allowUntrusted?: boolean;
  deny?: boolean;
}

interface CapabilityPolicyDenial {
  kind: string;
  id: string;
  name?: string;
  repo?: string;
  grant: string;
  reason: string;
}

interface MemoryRow {
  repo?: string;
  text: string;
  updatedAt?: string;
  updatedBy?: string;
}

interface ScopeSetRow {
  name: string;
  scopes: string[];
  // "builtin" or "config": only a declared set can be removed.
  source?: string;
}

interface ScopeSetHolder {
  kind: string;
  id: string;
  name?: string;
}

interface SecretMetaRow {
  name: string;
  keys?: string[];
  // The capability servers whose own config names this secret: it is
  // theirs, listed apart, and goes when they are removed.
  capabilityServers?: string[];
  orphaned?: boolean;
}

// needsHostWarning is the sentence a server waiting on its host says
// under the servers table, or null for any other state: a grant of a
// server placed on a host waits until that host dials in (the classic
// panel's needsHostdHelp).
export function needsHostWarning(srv: CapabilityServerRow): string | null {
  if (srv.config?.disabled) return null;
  const st = String(srv.status?.state ?? "").trim();
  if (st !== "needs-hostd" && !st.startsWith("needs-hostd:")) return null;
  const host = st.slice("needs-hostd:".length).trim();
  return `${srv.id} is placed on ${host ? `host ${host}` : "a host"}, which is not connected: a grant of it waits until that host dials in. Start or reconnect it under Settings → Hosts.`;
}

// listedSecrets is what Stored secrets lists: every secret no other
// Settings control owns (a model connection's agent-backend-<id>, the
// hub registration token) and no release machinery made. A retired
// capability's secret has no control of its own any more, so it stays
// listed here rather than vanishing.
export function listedSecrets(
  secrets: readonly SecretMetaRow[],
  backendIds: readonly string[],
): SecretMetaRow[] {
  const claimed = new Set([
    "registry-registration-token",
    ...backendIds.map((id) => `agent-backend-${id}`),
  ]);
  return secrets.filter(
    (s) => !claimed.has(s.name) && !s.name.startsWith("repo-release-"),
  );
}

@Component({
  selector: "g-settings-work",
  imports: [PillSelectComponent, ConfirmDialogComponent, RouterLink],
  encapsulation: ViewEncapsulation.None,
  styleUrl: "./settings-capabilities.css",
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @switch (section()) {
      @case ("tasks") {
        <div class="s-card" data-testid="card-tasks-defaults">
          <h3 class="s-card-title">Task approval, merge queue &amp; gates</h3>
          <div class="s-toggle-row">
            <div class="s-toggle-copy">
              <span class="s-toggle-name"
                >Queue new tasks immediately by default</span
              >
              <span class="s-hint"
                >When off, agent-filed tasks start as proposals until a human
                approves them.</span
              >
            </div>
            <button
              type="button"
              class="g-switch"
              role="switch"
              data-testid="switch-approved-by-default"
              [attr.aria-checked]="
                Boolean(state.val('approvedByDefault', false))
              "
              [disabled]="!canWriteSettings()"
              aria-label="Queue new tasks immediately by default"
              (click)="
                state.setField(
                  'approvedByDefault',
                  !state.val('approvedByDefault', false)
                )
              "
            ></button>
          </div>

          <div class="s-toggle-row">
            <div class="s-toggle-copy">
              <span class="s-toggle-name">Auto-merge new tasks by default</span>
              <span class="s-hint"
                >Automatically merge verified pull requests once CI and review
                gates pass.</span
              >
            </div>
            <button
              type="button"
              class="g-switch"
              role="switch"
              data-testid="switch-automerge-by-default"
              [attr.aria-checked]="
                Boolean(state.val('autoMergeByDefault', false))
              "
              [disabled]="!canWriteSettings()"
              aria-label="Auto-merge new tasks by default"
              (click)="
                state.setField(
                  'autoMergeByDefault',
                  !state.val('autoMergeByDefault', false)
                )
              "
            ></button>
          </div>

          <div class="s-toggle-row">
            <div class="s-toggle-copy">
              <span class="s-toggle-name">Show closed tasks by default</span>
              <span class="s-hint"
                >Include closed and superseded tasks in unfiltered board
                views.</span
              >
            </div>
            <button
              type="button"
              class="g-switch"
              role="switch"
              [attr.aria-checked]="
                Boolean(state.val('showClosedByDefault', false))
              "
              [disabled]="!canWriteSettings()"
              aria-label="Show closed tasks by default"
              (click)="
                state.setField(
                  'showClosedByDefault',
                  !state.val('showClosedByDefault', false)
                )
              "
            ></button>
          </div>

          <div class="s-grid">
            <div class="s-field">
              <label class="s-label" for="t-depth"
                >Max unreviewed follow-up depth</label
              >
              <input
                id="t-depth"
                class="s-input s-num"
                type="number"
                min="1"
                data-testid="input-max-autosubmit-depth"
                [value]="state.val('maxAutoSubmitDepth', 3)"
                [disabled]="!canWriteSettings()"
                [attr.aria-invalid]="
                  state.fieldErrors()['maxAutoSubmitDepth'] ? true : null
                "
                [attr.aria-describedby]="
                  state.fieldErrors()['maxAutoSubmitDepth']
                    ? 't-depth-err'
                    : null
                "
                (input)="
                  setCount(
                    'maxAutoSubmitDepth',
                    1,
                    'Max unreviewed follow-up depth',
                    $event
                  )
                "
              />
              @if (state.fieldErrors()["maxAutoSubmitDepth"]; as countErr) {
                <span
                  id="t-depth-err"
                  class="s-field-error"
                  [attr.data-testid]="'error-t-depth-err'"
                  >{{ countErr }}</span
                >
              }
            </div>
            <div class="s-field">
              <label class="s-label" for="t-fanout"
                >Max unreviewed follow-ups per task</label
              >
              <input
                id="t-fanout"
                class="s-input s-num"
                type="number"
                min="1"
                [value]="state.val('maxAutoSubmitFanOut', 5)"
                [disabled]="!canWriteSettings()"
                [attr.aria-invalid]="
                  state.fieldErrors()['maxAutoSubmitFanOut'] ? true : null
                "
                [attr.aria-describedby]="
                  state.fieldErrors()['maxAutoSubmitFanOut']
                    ? 't-fanout-err'
                    : null
                "
                (input)="
                  setCount(
                    'maxAutoSubmitFanOut',
                    1,
                    'Max unreviewed follow-ups per task',
                    $event
                  )
                "
              />
              @if (state.fieldErrors()["maxAutoSubmitFanOut"]; as countErr) {
                <span
                  id="t-fanout-err"
                  class="s-field-error"
                  [attr.data-testid]="'error-t-fanout-err'"
                  >{{ countErr }}</span
                >
              }
            </div>
            <div class="s-field">
              <label class="s-label" for="t-branch">Branch name template</label>
              <input
                id="t-branch"
                class="s-input s-mono"
                type="text"
                placeholder="grain/task-{id}"
                [value]="state.val('taskBranchTemplate', '')"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField(
                    'taskBranchTemplate',
                    $any($event.target).value
                  )
                "
              />
            </div>
          </div>
        </div>

        <div class="s-card" id="card-qa-rollout" data-testid="card-qa-rollout">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Quality control &amp; PR rollout</h3>
              <p class="s-card-desc">
                Off by default. Assigns a configurable share of work-tree pull
                requests to deterministic and semantic quality checks for live
                single-instance A/B evaluation.
              </p>
            </div>
          </div>

          <div class="s-toggle-row">
            <div class="s-toggle-copy">
              <span class="s-toggle-name">Enable quality control engine</span>
              <span class="s-hint"
                >When off, tasks run with zero additional quality gates or stage
                overhead.</span
              >
            </div>
            <button
              type="button"
              class="g-switch"
              role="switch"
              data-testid="switch-qa-enabled"
              [attr.aria-checked]="qaEnabled()"
              [attr.title]="writeDeniedReason()"
              [disabled]="!canWriteSettings()"
              aria-label="Enable quality control engine"
              (click)="toggleQAEnabled()"
            ></button>
          </div>

          @if (runningExp()) {
            <div
              class="s-grid s-grid-qa"
              data-testid="qa-running-experiment-notice"
              style="padding: 12px 16px; background: var(--md-sys-color-surface-container-low); border-radius: var(--md-sys-shape-corner-small);"
            >
              <span class="s-toggle-name"
                >Running: {{ runningExp()!.name }} ({{
                  armSummary(runningExp()!)
                }}) &middot;
                <a
                  [routerLink]="['/telemetry']"
                  [queryParams]="{ tab: 'quality', exp: runningExp()!.id }"
                  >Manage</a
                ></span
              >
            </div>
          } @else {
            <div class="s-grid s-grid-qa">
              <div class="s-field">
                <span class="s-label">Quality flow package</span>
                <g-pill-select
                  testid="pill-qa-flow"
                  ariaLabel="Quality flow package"
                  align="left"
                  [attr.title]="qaControlsDisabledReason()"
                  [options]="qaFlowOptions()"
                  [value]="qaFlowValue()"
                  [disabled]="!canEditQAControls()"
                  (valueChange)="setQAFlow($event)"
                />
              </div>

              <div class="s-field">
                <label class="s-label" for="t-qa-rollout">PR rollout (%)</label>
                <input
                  id="t-qa-rollout"
                  class="s-input s-num"
                  type="number"
                  min="0"
                  max="100"
                  step="1"
                  data-testid="input-qa-rollout-pct"
                  [value]="qaRolloutInputValue()"
                  [attr.aria-invalid]="qaRolloutError() ? 'true' : null"
                  [attr.aria-describedby]="
                    qaRolloutError() ? 't-qa-rollout-err' : null
                  "
                  [attr.title]="qaControlsDisabledReason()"
                  [disabled]="!canEditQAControls()"
                  (input)="onQARolloutInput($event)"
                  (blur)="onQARolloutBlur()"
                />
                @if (qaRolloutError(); as rolloutErr) {
                  <span
                    id="t-qa-rollout-err"
                    class="s-field-error"
                    data-testid="error-qa-rollout-pct"
                    >{{ rolloutErr }}</span
                  >
                }
              </div>
            </div>
          }

          <div class="s-toggle-row">
            <div class="s-toggle-copy">
              <span class="s-toggle-name"
                >Passive shadow scoring on control PRs</span
              >
              <span class="s-hint"
                >Records non-blocking static check telemetry on control-arm pull
                requests after final commit for unbiased A/B comparison.</span
              >
            </div>
            <button
              type="button"
              class="g-switch"
              role="switch"
              data-testid="switch-qa-shadow-control"
              [attr.aria-checked]="qaShadowOnControl()"
              [attr.title]="qaControlsDisabledReason()"
              [disabled]="!canEditQAControls()"
              aria-label="Passive shadow scoring on control PRs"
              (click)="toggleQAShadowOnControl()"
            ></button>
          </div>
        </div>
      }
      @case ("agents") {
        <div class="s-card" data-testid="card-agents-runtime">
          <h3 class="s-card-title">Agent turn limits &amp; default grants</h3>
          <div class="s-grid">
            <div class="s-field">
              <label class="s-label" for="ag-turns">Max agent turns</label>
              <input
                id="ag-turns"
                class="s-input s-num"
                type="number"
                min="0"
                data-testid="input-max-agent-turns"
                [value]="state.val('maxAgentTurns', 40)"
                [disabled]="!canWriteSettings()"
                [attr.aria-invalid]="
                  state.fieldErrors()['maxAgentTurns'] ? true : null
                "
                [attr.aria-describedby]="
                  state.fieldErrors()['maxAgentTurns'] ? 'ag-turns-err' : null
                "
                (input)="
                  setCount('maxAgentTurns', 0, 'Max agent turns', $event)
                "
              />
              @if (state.fieldErrors()["maxAgentTurns"]; as countErr) {
                <span
                  id="ag-turns-err"
                  class="s-field-error"
                  [attr.data-testid]="'error-ag-turns-err'"
                  >{{ countErr }}</span
                >
              }
            </div>
            <div class="s-field">
              <label class="s-label" for="ag-caps"
                >Default capabilities (comma-separated)</label
              >
              <input
                id="ag-caps"
                class="s-input s-mono"
                type="text"
                placeholder="repo:read,ci:read"
                [value]="defaultCapsText()"
                [disabled]="!canWriteSettings()"
                (input)="setDefaultCapsText($any($event.target).value)"
              />
            </div>
          </div>
        </div>

        <div class="s-card" data-testid="card-console-sessions">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Standing console sessions</h3>
              <p class="s-card-desc">
                Persistent interactive sessions (including Director and role
                specialists).
              </p>
            </div>
          </div>
          @if (consoleSessions().length > 0) {
            <div class="s-table-wrap">
              <table class="s-table" aria-label="Console sessions">
                <thead>
                  <tr>
                    <th>ID / Title</th>
                    <th>Model</th>
                    <th>State</th>
                    <th class="s-actions-cell">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  @for (cs of consoleSessions(); track cs.id) {
                    <tr>
                      <td>
                        {{ cs.title || cs.name || cs.id }}
                      </td>
                      <td class="s-mono">{{ cs.model || "default" }}</td>
                      <td>
                        <span class="status-pill neutral">{{
                          cs.state || "idle"
                        }}</span>
                      </td>
                      <td class="s-actions-cell">
                        <div class="s-row-actions">
                          <button
                            type="button"
                            class="btn-tonal s-btn-sm"
                            [disabled]="
                              !access.can(
                                'POST /api/console-sessions/{id}/restart'
                              )
                            "
                            (click)="restartSession(cs.id)"
                          >
                            Restart
                          </button>
                          <button
                            type="button"
                            class="btn-text s-btn-sm"
                            [disabled]="
                              !access.can(
                                'DELETE /api/console-sessions/{id}'
                              ) || state.sectionLocked('agents')
                            "
                            (click)="deleteSession(cs.id)"
                          >
                            Delete
                          </button>
                        </div>
                      </td>
                    </tr>
                  }
                </tbody>
              </table>
            </div>
          }
          <div class="s-inline">
            <input
              class="s-input s-input-inline"
              type="text"
              aria-label="New console session title"
              data-testid="input-new-session-title"
              placeholder="Release Coordinator"
              [value]="newSessionTitle()"
              [disabled]="
                !access.can('POST /api/console-sessions') ||
                state.sectionLocked('agents')
              "
              (input)="newSessionTitle.set($any($event.target).value)"
            />
            <button
              type="button"
              class="btn-tonal"
              data-testid="create-console-session-btn"
              [disabled]="
                !newSessionTitle().trim() ||
                !access.can('POST /api/console-sessions') ||
                state.sectionLocked('agents')
              "
              (click)="createConsoleSession()"
            >
              Add standing session
            </button>
          </div>
        </div>
      }

      @case ("capabilities") {
        <div
          class="s-card"
          id="capability-servers"
          data-testid="card-capability-servers"
        >
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Capability servers (MCP)</h3>
              <p class="s-card-desc">
                External tool servers, secret bindings, and live connectivity
                probes.
              </p>
            </div>
          </div>
          <div class="s-banner" data-testid="capability-servers-vs-legacy">
            <span>
              The older capabilities built into grain, granted by a bare name
              such as <code>gcp-key</code>, have all been retired onto these
              servers' methods; the GitHub host, GCP settings and secrets they
              used are under
              <a [routerLink]="[]" fragment="credentials"
                >Deployment credentials</a
              >.
            </span>
          </div>

          @if (capServers().length > 0) {
            <div class="s-table-wrap">
              <table class="s-table" aria-label="Capability servers">
                <thead>
                  <tr>
                    <th>Server ID</th>
                    <th>Endpoint / Command</th>
                    <th>State</th>
                    <th class="s-actions-cell">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  @for (srv of capServers(); track srv.id) {
                    @let isDisabled = Boolean(srv.config?.disabled);
                    <tr [attr.data-testid]="'cap-server-row-' + srv.id">
                      <td class="s-mono">{{ srv.id }}</td>
                      <td class="s-mono">
                        {{
                          srv.config?.serverUrl ||
                            srv.config?.command ||
                            (srv.inProcess ? "in-process" : "mcp")
                        }}
                      </td>
                      <td>
                        @let chip = capServerState(srv);
                        <span
                          [class]="'status-pill ' + chip.tone"
                          [attr.data-testid]="'cap-server-state-' + srv.id"
                          [attr.title]="
                            srv.status?.error || srv.status?.reason || null
                          "
                        >
                          {{ chip.label }}
                        </span>
                      </td>
                      <td class="s-actions-cell">
                        <div class="s-row-actions">
                          @if (!srv.inProcess) {
                            <button
                              type="button"
                              class="btn-tonal s-btn-sm"
                              [attr.data-testid]="'toggle-cap-server-' + srv.id"
                              [disabled]="
                                !(isDisabled
                                  ? access.can(
                                      'POST /api/capability-servers/enable'
                                    )
                                  : access.can(
                                      'POST /api/capability-servers/disable'
                                    )) || state.sectionLocked('capabilities')
                              "
                              (click)="toggleCapServer(srv.id, isDisabled)"
                            >
                              {{ isDisabled ? "Enable" : "Disable" }}
                            </button>
                          }
                          <!-- An in-process server has no connection to
                               probe: the server answers Test with "no
                               capability server ... is configured". -->
                          @if (!srv.inProcess) {
                            <button
                              type="button"
                              class="btn-tonal s-btn-sm"
                              [attr.data-testid]="'test-cap-server-' + srv.id"
                              [disabled]="
                                !access.can('POST /api/capability-servers/test')
                              "
                              (click)="testCapServer(srv.id)"
                            >
                              Test
                            </button>
                          }
                          @if (
                            !srv.builtin &&
                            !srv.config?.builtin &&
                            !srv.inProcess
                          ) {
                            <button
                              type="button"
                              class="btn-text s-btn-sm"
                              [attr.data-testid]="'remove-cap-server-' + srv.id"
                              [disabled]="
                                !access.can(
                                  'POST /api/capability-servers/remove'
                                ) || state.sectionLocked('capabilities')
                              "
                              (click)="confirmRemoveCapServer(srv.id)"
                            >
                              Remove
                            </button>
                          }
                        </div>
                      </td>
                    </tr>
                  }
                </tbody>
              </table>
            </div>
          }

          @for (w of awayHostWarnings(); track w.id) {
            <div
              class="s-banner warn"
              [attr.data-testid]="'cap-server-needs-host-' + w.id"
            >
              {{ w.text }}
            </div>
          }

          @if (capTestNotice()) {
            <div
              class="s-banner"
              [class.ok]="capTestOk()"
              [class.warn]="!capTestOk()"
              data-testid="cap-test-notice"
            >
              {{ capTestNotice() }}
            </div>
          }

          <div
            class="s-subsection"
            [class.s-dimmed]="state.sectionLocked('capabilities')"
          >
            <div class="s-subhead">
              <h4 class="s-subhead-title">Add capability server</h4>
            </div>
            <div class="s-grid">
              <div class="s-field">
                <label class="s-label" for="cs-id">Server ID</label>
                <input
                  id="cs-id"
                  class="s-input s-mono"
                  type="text"
                  data-testid="input-cap-server-id"
                  placeholder="jira-mcp"
                  [value]="newServerId()"
                  [disabled]="
                    !access.can('PUT /api/capability-servers') ||
                    state.sectionLocked('capabilities')
                  "
                  (input)="newServerId.set($any($event.target).value)"
                />
              </div>
              <div class="s-field">
                <label class="s-label" for="cs-url"
                  >HTTP URL or stdio cmd</label
                >
                <input
                  id="cs-url"
                  class="s-input s-mono"
                  type="text"
                  data-testid="input-cap-server-url"
                  placeholder="https://mcp.internal/sse"
                  [value]="newServerUrl()"
                  [disabled]="
                    !access.can('PUT /api/capability-servers') ||
                    state.sectionLocked('capabilities')
                  "
                  (input)="newServerUrl.set($any($event.target).value)"
                />
              </div>
            </div>
            <div class="s-inline">
              <button
                type="button"
                class="btn-tonal"
                data-testid="save-cap-server-btn"
                [disabled]="
                  !newServerId().trim() ||
                  !newServerUrl().trim() ||
                  !access.can('PUT /api/capability-servers') ||
                  state.sectionLocked('capabilities')
                "
                (click)="saveCapServer()"
              >
                Save capability server
              </button>
            </div>
          </div>
        </div>

        <div
          class="s-card"
          id="capability-policy"
          data-testid="card-capability-policy"
          [class.s-dimmed]="state.sectionLocked('capabilities')"
        >
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Capability grant policy</h3>
              <p class="s-card-desc">
                Ordered rules admitting or denying capability references across
                repositories and untrusted triggers.
              </p>
            </div>
            <div class="s-inline">
              <button
                type="button"
                class="btn-tonal s-btn-sm"
                data-testid="preview-cap-policy-btn"
                [disabled]="!access.can('POST /api/capability-policy/preview')"
                (click)="previewCapPolicy()"
              >
                Preview policy
              </button>
              <button
                type="button"
                class="btn-primary s-btn-sm"
                data-testid="save-cap-policy-btn"
                [disabled]="
                  !access.can('PUT /api/capability-policy') ||
                  state.sectionLocked('capabilities')
                "
                (click)="saveCapPolicy()"
              >
                Save policy
              </button>
            </div>
          </div>
          @if (policyError(); as e) {
            <div
              class="s-banner error"
              role="alert"
              data-testid="cap-policy-error"
            >
              <span>{{ e }}</span>
              <button
                type="button"
                class="btn-text s-btn-sm"
                (click)="policyError.set(null)"
              >
                Dismiss
              </button>
            </div>
          }

          @if (policyRules().length > 0) {
            <div class="s-table-wrap">
              <table class="s-table" aria-label="Capability policy rules">
                <thead>
                  <tr>
                    <th>Capability pattern</th>
                    <th>Effect</th>
                    <th>Repo filter</th>
                    <th>Untrusted</th>
                    <th class="s-actions-cell">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  @for (r of policyRules(); track $index; let idx = $index) {
                    <tr [attr.data-testid]="'policy-rule-row-' + idx">
                      <td class="s-mono">{{ r.capability }}</td>
                      <td>
                        <span
                          [class]="
                            'status-pill ' + (r.deny ? 'error' : 'healthy')
                          "
                        >
                          {{ r.deny ? "Deny" : "Admit" }}
                        </span>
                      </td>
                      <td class="s-mono">
                        {{ r.repos?.join(", ") || "*" }}
                      </td>
                      <td>
                        {{
                          !r.deny && r.allowUntrusted
                            ? "Allowed"
                            : "Trusted only"
                        }}
                      </td>
                      <td class="s-actions-cell">
                        <div class="s-row-actions">
                          <button
                            type="button"
                            class="btn-text s-btn-sm"
                            [disabled]="
                              !access.can('PUT /api/capability-policy') ||
                              state.sectionLocked('capabilities')
                            "
                            (click)="removePolicyRule(idx)"
                          >
                            Remove
                          </button>
                        </div>
                      </td>
                    </tr>
                  }
                </tbody>
              </table>
            </div>
          }

          @if (retiredEntries().length > 0) {
            <div class="s-subsection" data-testid="retired-grant-notices">
              <div class="s-subhead">
                <h4 class="s-subhead-title">Retired in-process capabilities</h4>
              </div>
              <span class="s-hint"
                >settings/capabilities.yml still configures these under
                <code>in_process</code>. They keep working through the
                migration; move each entry to where it lives now.</span
              >
              @for (n of retiredEntries(); track n.name) {
                <div
                  class="s-banner warn s-retired"
                  [attr.data-testid]="'retired-grant-' + n.name"
                >
                  <span>
                    <code>in_process.{{ n.name }}</code> is deprecated ({{
                      n.details.join(" · ")
                    }}): it is granted as
                    <code class="s-wrap">{{ n.grant }}</code> now; move it to
                    <code class="s-wrap">{{ n.moveTo }}</code
                    >.
                  </span>
                </div>
              }
            </div>
          }

          <div class="s-inline">
            <input
              class="s-input s-input-inline s-mono"
              type="text"
              aria-label="Capability pattern"
              data-testid="input-policy-capability"
              placeholder="https://github.com/acme/caps/cloud#*"
              aria-describedby="policy-capability-hint"
              [attr.aria-invalid]="newRuleCapInvalid() ? 'true' : null"
              [value]="newRuleCap()"
              [disabled]="
                !access.can('PUT /api/capability-policy') ||
                state.sectionLocked('capabilities')
              "
              (input)="newRuleCap.set($any($event.target).value)"
            />
            <input
              class="s-input s-input-inline s-mono"
              type="text"
              aria-label="Repositories filter (comma-separated)"
              data-testid="input-policy-repos"
              placeholder="acme/* (empty = *)"
              [value]="newRuleRepos()"
              [disabled]="
                !access.can('PUT /api/capability-policy') ||
                state.sectionLocked('capabilities')
              "
              (input)="newRuleRepos.set($any($event.target).value)"
            />
            <g-pill-select
              testid="select-policy-effect"
              ariaLabel="Rule effect"
              align="left"
              [options]="policyEffectOptions"
              [value]="newRuleEffect()"
              [disabled]="
                !access.can('PUT /api/capability-policy') ||
                state.sectionLocked('capabilities')
              "
              (valueChange)="newRuleEffect.set($event)"
            />
            @if (newRuleEffect() !== "deny") {
              <div class="s-inline">
                <button
                  type="button"
                  class="g-switch"
                  role="switch"
                  data-testid="switch-policy-allow-untrusted"
                  [attr.aria-checked]="newRuleAllowUntrusted()"
                  [disabled]="
                    !access.can('PUT /api/capability-policy') ||
                    state.sectionLocked('capabilities')
                  "
                  aria-label="Allow untrusted triggers"
                  (click)="newRuleAllowUntrusted.set(!newRuleAllowUntrusted())"
                ></button>
                <span class="s-label">Allow untrusted</span>
              </div>
            }
            <button
              type="button"
              class="btn-tonal s-btn-sm"
              data-testid="add-policy-rule-btn"
              [disabled]="
                !newRuleCap().trim() ||
                newRuleCapInvalid() ||
                !access.can('PUT /api/capability-policy') ||
                state.sectionLocked('capabilities')
              "
              (click)="addPolicyRule()"
            >
              Add rule
            </button>
          </div>
          <!-- The server only takes a capability reference here
               (model.CapabilityPolicy.Validate): a bare retired name
               such as gh-cli is refused when the policy is saved. -->
          <span
            class="s-hint"
            id="policy-capability-hint"
            data-testid="policy-capability-hint"
            [class.s-field-error]="newRuleCapInvalid()"
          >
            @if (newRuleCapInvalid()) {
              A pattern is a capability reference starting with https://, not a
              bare name.
            } @else {
              &lt;capability_id&gt;#&lt;method&gt;; * matches any run of
              characters.
            }
          </span>
        </div>

        <div
          class="s-card"
          id="api-scope-sets"
          data-testid="card-scope-sets-secrets"
          [class.s-dimmed]="state.sectionLocked('capabilities')"
        >
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">API scope sets</h3>
              <p class="s-card-desc">
                Named sets of agent API scopes, granted as
                <code>api:&lt;name&gt;</code>. Declaring a set grants it to
                nobody.
              </p>
            </div>
          </div>
          @if (scopeSets().length > 0) {
            <div class="s-table-wrap">
              <table class="s-table" aria-label="API scope sets">
                <thead>
                  <tr>
                    <th>Name</th>
                    <th class="s-hide-phone">Scopes</th>
                    <th class="s-hide-phone">Source</th>
                    <th class="s-actions-cell">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  @for (ss of scopeSets(); track ss.name) {
                    <tr [attr.data-testid]="'scope-set-row-' + ss.name">
                      @let scopeList = (ss.scopes || []).join(", ");
                      <td
                        class="s-mono s-wrap"
                        [attr.title]="scopeList || null"
                      >
                        {{ ss.name }}
                      </td>
                      <td
                        class="s-mono s-scope-list s-hide-phone"
                        [attr.title]="scopeList || null"
                      >
                        {{ scopeList || "—" }}
                      </td>
                      <td class="s-hide-phone">
                        <span class="status-pill neutral">{{
                          ss.source === "config" ? "declared" : "built-in"
                        }}</span>
                      </td>
                      <td class="s-actions-cell">
                        @if (ss.source === "config") {
                          <div class="s-row-actions">
                            <button
                              type="button"
                              class="btn-text s-btn-sm"
                              [attr.data-testid]="'remove-scope-set-' + ss.name"
                              [disabled]="
                                !access.can(
                                  'POST /api/api-scope-sets/delete'
                                ) || state.sectionLocked('capabilities')
                              "
                              (click)="askRemoveScopeSet(ss.name)"
                            >
                              Remove
                            </button>
                          </div>
                        }
                      </td>
                    </tr>
                  }
                </tbody>
              </table>
            </div>
          }
          <div class="s-inline">
            <input
              class="s-input s-input-inline s-mono"
              type="text"
              aria-label="Scope set name"
              data-testid="input-scope-set-name"
              placeholder="ci-readonly"
              [value]="newScopeSetName()"
              [disabled]="
                !access.can('PUT /api/api-scope-sets') ||
                state.sectionLocked('capabilities')
              "
              (input)="newScopeSetName.set($any($event.target).value)"
            />
            <input
              class="s-input s-input-inline s-mono"
              type="text"
              aria-label="Scopes list"
              data-testid="input-scope-set-scopes"
              placeholder="tasks:read,ci:read"
              [value]="newScopeSetScopes()"
              [disabled]="
                !access.can('PUT /api/api-scope-sets') ||
                state.sectionLocked('capabilities')
              "
              (input)="newScopeSetScopes.set($any($event.target).value)"
            />
            <button
              type="button"
              class="btn-tonal s-btn-sm"
              data-testid="save-scope-set-btn"
              [disabled]="
                !newScopeSetName().trim() ||
                !access.can('PUT /api/api-scope-sets') ||
                state.sectionLocked('capabilities')
              "
              (click)="saveScopeSet()"
            >
              Save scope set
            </button>
          </div>
        </div>

        <!-- Deployment credentials writes through PUT /api/settings
             (settings:write) and /api/secrets (secrets:write), not the
             capabilities:admin routes above, so each half follows its own
             scope; a config-repo lock dims the settings but leaves the
             secrets usable, as the API does (settings-mutability D-2). -->
        <div class="s-card" id="credentials" data-testid="card-credentials">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Deployment credentials</h3>
              <p class="s-card-desc">
                The GitHub host grain talks to, the GCP project and legacy
                service-account pool seeded onto the
                <a
                  [routerLink]="[]"
                  fragment="capability-servers"
                  data-testid="credentials-gcp-link"
                  >gcp capability server's</a
                >&ngsp;<code>gcp-key</code> and <code>gemini-key</code> methods,
                and the secrets grain stores.
              </p>
            </div>
          </div>
          @if (credentialsRefusal()) {
            <div class="s-banner readonly" data-testid="credentials-read-only">
              {{ credentialsRefusal() }}
            </div>
          }
          <div
            class="s-subsection"
            data-testid="credentials-settings"
            [class.s-dimmed]="!canWriteCredentials()"
          >
            <div class="s-grid">
              <div class="s-field">
                <label class="s-label" for="cred-github-host"
                  >GitHub host</label
                >
                <input
                  id="cred-github-host"
                  class="s-input s-mono"
                  type="text"
                  autocomplete="off"
                  spellcheck="false"
                  data-testid="input-credentials-github-host"
                  placeholder="github.com"
                  [value]="state.val('githubHost', '')"
                  [disabled]="!canWriteCredentials()"
                  (input)="
                    state.setField('githubHost', $any($event.target).value)
                  "
                />
                <span class="s-hint"
                  >A host name with an optional :port, such as ghe.example.com,
                  not a URL.</span
                >
              </div>
              <div class="s-field">
                <label class="s-label" for="cred-gcp-project"
                  >GCP project</label
                >
                <input
                  id="cred-gcp-project"
                  class="s-input s-mono"
                  type="text"
                  autocomplete="off"
                  spellcheck="false"
                  data-testid="input-credentials-gcp-project"
                  placeholder="my-project-123"
                  [value]="state.val('gcpProject', '')"
                  [disabled]="!canWriteCredentials()"
                  (input)="
                    state.setField('gcpProject', $any($event.target).value)
                  "
                />
                <span class="s-hint"
                  >Optional. Used for IAP reconciliation and to seed migrated
                  <code>gcp-key</code> / <code>gemini-key</code> settings.</span
                >
              </div>
              <div class="s-field">
                <label class="s-label" for="cred-gcp-sa"
                  >GCP service account email</label
                >
                <input
                  id="cred-gcp-sa"
                  class="s-input s-mono"
                  type="text"
                  autocomplete="off"
                  spellcheck="false"
                  data-testid="input-credentials-gcp-sa"
                  placeholder="grain-agent@my-project-123.iam.gserviceaccount.com"
                  [value]="state.val('gcpServiceAccountEmail', '')"
                  [disabled]="!canWriteCredentials()"
                  (input)="
                    state.setField(
                      'gcpServiceAccountEmail',
                      $any($event.target).value
                    )
                  "
                />
                <span class="s-hint"
                  >The legacy pool migrated <code>gcp-key</code> grants mint
                  keys for. Store its minter as <code>gcp-key-minter</code> (key
                  <code>value</code>) under
                  <a [routerLink]="[]" fragment="other-secrets"
                    >Stored secrets</a
                  >
                  below.</span
                >
              </div>
              <div class="s-field">
                <label class="s-label" for="cred-gcp-count"
                  >GCP service account count</label
                >
                <input
                  id="cred-gcp-count"
                  class="s-input s-mono"
                  type="number"
                  min="1"
                  step="1"
                  data-testid="input-credentials-gcp-count"
                  [value]="state.val('gcpServiceAccountCount', 1) || 1"
                  [disabled]="!canWriteCredentials()"
                  (input)="
                    state.setNumber(
                      'gcpServiceAccountCount',
                      $any($event.target).value,
                      1
                    )
                  "
                />
                <span class="s-hint"
                  >Above 1, grain mints from &lt;prefix&gt;-0&#64;…,
                  &lt;prefix&gt;-1&#64;… in turn.</span
                >
              </div>
            </div>
            <div class="s-toggle-row">
              <div class="s-toggle-copy">
                <span class="s-toggle-name"
                  >Speak plain HTTP to GitHub host</span
                >
                <span class="s-hint"
                  >For a test or mock GitHub only; never for a real one.</span
                >
              </div>
              <button
                type="button"
                class="g-switch"
                role="switch"
                data-testid="switch-credentials-github-http"
                [attr.aria-checked]="
                  Boolean(state.val('githubInsecureHttp', false))
                "
                [disabled]="!canWriteCredentials()"
                aria-label="Speak plain HTTP to GitHub host"
                (click)="
                  state.setField(
                    'githubInsecureHttp',
                    !state.val('githubInsecureHttp', false)
                  )
                "
              ></button>
            </div>
          </div>

          <div
            class="s-subsection s-anchor"
            id="other-secrets"
            data-testid="stored-secrets"
          >
            <div class="s-subhead">
              <h4 class="s-subhead-title">
                Stored secrets{{
                  access.can("GET /api/secrets")
                    ? " (" + listedSecrets().length + ")"
                    : ""
                }}
              </h4>
            </div>
            <span class="s-hint"
              >Names and keys only, never values: a secret can be set, replaced
              and removed here, and never read back. Model connection keys are
              set under Settings → Models and the hub token under Settings →
              Hub.</span
            >
            @if (!secretsEnabled()) {
              <span class="s-hint" data-testid="secrets-store-disabled"
                >This server has no secret store configured, so nothing can be
                stored here yet.</span
              >
            } @else {
              @if (!access.can("GET /api/secrets")) {
                <span class="s-hint" data-testid="secrets-read-restricted">
                  Stored secret names cannot be read with your role (<code
                    >secrets:read</code
                  >
                  required){{
                    canWriteSecrets()
                      ? "; storing a secret overwrites any existing key of that name."
                      : "."
                  }}
                </span>
              } @else {
                @if (serverSecrets().length > 0) {
                  <span class="s-label">Used by capability servers</span>
                  <ul
                    class="s-secret-list"
                    aria-label="Secrets used by capability servers"
                  >
                    @for (sec of serverSecrets(); track sec.name) {
                      <li
                        class="s-secret-row"
                        [attr.data-testid]="'secret-row-' + sec.name"
                      >
                        <span class="s-mono s-secret-name">{{ sec.name }}</span>
                        <span class="s-secret-keys">
                          @for (k of sec.keys || []; track k) {
                            <span class="status-pill neutral s-mono">{{
                              k
                            }}</span>
                          }
                        </span>
                        <span class="s-hint s-wrap"
                          >Used by
                          @for (
                            id of sec.capabilityServers || [];
                            track id;
                            let last = $last
                          ) {
                            <a
                              class="s-mono"
                              [routerLink]="[]"
                              fragment="capability-servers"
                              >{{ id }}</a
                            >{{ last ? "" : ", " }}
                          }
                        </span>
                      </li>
                    }
                  </ul>
                }
                @if (otherSecrets().length > 0) {
                  <ul class="s-secret-list" aria-label="Other secrets">
                    @for (sec of otherSecrets(); track sec.name) {
                      <li
                        class="s-secret-row"
                        [attr.data-testid]="'secret-row-' + sec.name"
                      >
                        <span class="s-mono s-secret-name">{{ sec.name }}</span>
                        @if (sec.orphaned) {
                          <span
                            class="status-pill blast"
                            title="Grain made this for a capability server that no configured server uses any more"
                            >orphaned</span
                          >
                        }
                        <span class="s-secret-keys">
                          @for (k of sec.keys || []; track k) {
                            <span class="s-secret-key">
                              <span class="s-mono" [attr.title]="k">{{
                                k
                              }}</span>
                              <button
                                type="button"
                                class="btn-text s-btn-sm"
                                [attr.data-testid]="
                                  'replace-secret-' + sec.name + '-' + k
                                "
                                [attr.aria-label]="
                                  'Replace ' + sec.name + '/' + k
                                "
                                [disabled]="!canWriteSecrets()"
                                (click)="replaceSecret(sec.name, k)"
                              >
                                Replace
                              </button>
                              <button
                                type="button"
                                class="btn-text s-btn-sm"
                                [attr.data-testid]="
                                  'remove-secret-key-' + sec.name + '-' + k
                                "
                                [attr.aria-label]="
                                  'Remove ' + sec.name + '/' + k
                                "
                                [disabled]="!canWriteSecrets()"
                                (click)="confirmRemoveSecretKey(sec.name, k)"
                              >
                                Remove
                              </button>
                            </span>
                          }
                        </span>
                        <button
                          type="button"
                          class="btn-text s-btn-sm"
                          [attr.data-testid]="'remove-secret-' + sec.name"
                          [disabled]="!canWriteSecrets()"
                          (click)="confirmRemoveSecret(sec.name)"
                        >
                          Remove secret
                        </button>
                      </li>
                    }
                  </ul>
                } @else {
                  <span class="s-hint" data-testid="secrets-empty"
                    >No other secrets set.</span
                  >
                }
              }
              <form
                class="s-inline"
                autocomplete="off"
                data-testid="secret-form"
                (submit)="$event.preventDefault(); submitSecret()"
              >
                <input
                  class="s-input s-input-inline s-mono"
                  type="text"
                  autocomplete="off"
                  spellcheck="false"
                  aria-label="Secret name"
                  data-testid="input-secret-name"
                  placeholder="webhook-secret"
                  [value]="secretName()"
                  [disabled]="!canWriteSecrets()"
                  (input)="secretName.set($any($event.target).value)"
                />
                <input
                  class="s-input s-input-inline s-mono"
                  type="text"
                  autocomplete="off"
                  spellcheck="false"
                  aria-label="Secret key"
                  data-testid="input-secret-key"
                  placeholder="token"
                  [value]="secretKey()"
                  [disabled]="!canWriteSecrets()"
                  (input)="secretKey.set($any($event.target).value)"
                />
                <!-- type=password, so the value is masked as it is typed in
                     every browser; a PEM or other multi-line value is loaded
                     from a file instead, keeping its newlines. -->
                <input
                  class="s-input s-input-inline s-mono"
                  type="password"
                  autocomplete="new-password"
                  spellcheck="false"
                  data-lpignore="true"
                  data-1p-ignore="true"
                  aria-label="Secret value"
                  data-testid="input-secret-value"
                  [placeholder]="
                    secretFileName()
                      ? 'loaded from ' + secretFileName()
                      : 'Secret value'
                  "
                  [value]="secretFileName() ? '' : secretVal()"
                  [disabled]="!canWriteSecrets()"
                  (input)="setSecretValue($any($event.target).value)"
                />
                <label
                  class="btn-text s-btn-sm s-file-btn"
                  [class.disabled]="!canWriteSecrets()"
                >
                  From file…
                  <input
                    type="file"
                    class="sr-only"
                    data-testid="input-secret-file"
                    [disabled]="!canWriteSecrets()"
                    (change)="loadSecretFile($event)"
                  />
                </label>
                <button
                  type="submit"
                  class="btn-tonal s-btn-sm"
                  data-testid="store-secret-btn"
                  [disabled]="
                    secretSaving() ||
                    !secretName().trim() ||
                    !secretKey().trim() ||
                    secretVal().length === 0 ||
                    !canWriteSecrets()
                  "
                >
                  {{ secretExists() ? "Replace secret" : "Store secret" }}
                </button>
              </form>
            }
            @if (!access.can("POST /api/secrets/redact")) {
              <span class="s-hint" data-testid="secrets-redact-restricted">{{
                access.deniedReason("POST /api/secrets/redact")
              }}</span>
            }
          </div>
        </div>
      }

      @case ("memory") {
        <div class="s-card" data-testid="card-memory">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">
                Standing instructions &amp; deployment memory
              </h3>
              <p class="s-card-desc">
                Injected into every agent turn alongside repository
                instructions.
              </p>
            </div>
            <button
              type="button"
              class="btn-primary s-btn-sm"
              data-testid="save-memory-btn"
              [disabled]="
                !access.can('PUT /api/memory') || state.sectionLocked('memory')
              "
              (click)="saveMemory()"
            >
              Save memory
            </button>
          </div>
          <div class="s-field">
            <label class="s-label" for="mem-prompt"
              >Standing instructions (promptExtension)</label
            >
            <textarea
              id="mem-prompt"
              class="s-textarea"
              rows="4"
              data-testid="input-prompt-extension"
              [value]="state.val('promptExtension', '')"
              [disabled]="!canWriteSettings()"
              (input)="
                state.setField('promptExtension', $any($event.target).value)
              "
            ></textarea>
          </div>
          <div class="s-field">
            <label class="s-label" for="mem-doc"
              >Deployment memory markdown (/api/memory)</label
            >
            <textarea
              id="mem-doc"
              class="s-textarea s-mono"
              rows="6"
              data-testid="input-deployment-memory"
              [value]="memoryBody()"
              [disabled]="
                !access.can('PUT /api/memory') || state.sectionLocked('memory')
              "
              (input)="memoryBody.set($any($event.target).value)"
            ></textarea>
          </div>
          @if (memoryDocs().length > 0) {
            <div class="s-inline">
              <span class="s-label">Documents:</span>
              @for (doc of memoryDocs(); track doc) {
                <span class="status-pill neutral s-mono">{{ doc }}</span>
              }
            </div>
          }
        </div>
      }
    }
    @if (confirmReq(); as req) {
      <g-confirm-dialog [request]="req" (done)="onConfirmDone($event)" />
    }
  `,
})
export class SettingsWorkComponent implements OnInit, OnChanges {
  protected readonly api = inject(GrainApi);
  protected readonly access = inject(AccessService);
  protected readonly state = inject(SettingsStateService);
  private readonly snackbar = inject(SnackbarService);

  readonly section = input.required<SettingsSectionId>();

  protected readonly Number = Number;
  protected readonly Boolean = Boolean;

  protected readonly policyEffectOptions: readonly PillOption[] = [
    { value: "admit", label: "Admit" },
    { value: "deny", label: "Deny" },
  ];

  protected readonly consoleSessions = signal<ConsoleSessionRow[]>([]);
  protected readonly newSessionTitle = signal("");

  protected readonly capServers = signal<CapabilityServerRow[]>([]);
  protected readonly newServerId = signal("");
  protected readonly newServerUrl = signal("");
  protected readonly capTestNotice = signal("");
  protected readonly capTestOk = signal(true);
  protected readonly capServerState = capServerState;

  protected readonly policyRules = signal<CapabilityPolicyRule[]>([]);
  protected readonly newRuleCap = signal("");
  // newRuleCapInvalid is a typed pattern the server would refuse: every
  // policy rule names a capability reference (https://...).
  protected readonly newRuleCapInvalid = computed(() => {
    const v = this.newRuleCap().trim();
    return v !== "" && !v.startsWith("https://");
  });
  // policyError is a refused preview or save, shown in the policy card
  // rather than in the page banner scrolled out of sight above it.
  protected readonly policyError = signal<string | null>(null);
  protected readonly newRuleRepos = signal("");
  protected readonly newRuleEffect = signal("admit");
  protected readonly newRuleAllowUntrusted = signal(false);

  protected readonly scopeSets = signal<ScopeSetRow[]>([]);
  protected readonly newScopeSetName = signal("");
  protected readonly newScopeSetScopes = signal("");

  protected readonly secretsList = signal<SecretMetaRow[]>([]);
  // GET /api/secrets says enabled: false when the server has no secret
  // store; a PUT then 404s, so the form says so instead of offering it.
  protected readonly secretsEnabled = signal(true);
  protected readonly secretName = signal("");
  protected readonly secretKey = signal("value");
  protected readonly secretVal = signal("");
  protected readonly secretFileName = signal("");
  protected readonly secretSaving = signal(false);

  protected readonly catalogue = signal<CapabilityCatalogue | null>(null);
  protected readonly retiredEntries = computed(() =>
    retiredNotices(this.catalogue()),
  );
  protected readonly awayHostWarnings = computed(() =>
    this.capServers()
      .map((srv) => ({ id: srv.id, text: needsHostWarning(srv) }))
      .filter((w): w is { id: string; text: string } => Boolean(w.text)),
  );
  protected readonly listedSecrets = computed(() => {
    const backends = this.state.settings()["agentBackends"];
    const ids = Array.isArray(backends)
      ? backends
          .map((b) => String((b as { id?: unknown })?.id ?? ""))
          .filter(Boolean)
      : [];
    return listedSecrets(this.secretsList(), ids);
  });
  protected readonly serverSecrets = computed(() =>
    this.listedSecrets().filter((s) => s.capabilityServers?.length),
  );
  protected readonly otherSecrets = computed(() =>
    this.listedSecrets().filter((s) => !s.capabilityServers?.length),
  );
  protected readonly secretExists = computed(() => {
    const name = this.secretName().trim();
    const key = this.secretKey().trim();
    return this.secretsList().some(
      (s) => s.name === name && (s.keys ?? []).includes(key),
    );
  });

  protected readonly memoryBody = signal("");
  protected readonly memoryDocs = signal<string[]>([]);

  readonly runningExp = signal<QAExperimentSummary | null>(null);

  async loadRunningExp(): Promise<void> {
    if (
      this.section() !== "tasks" ||
      !this.access.can("GET /api/qa/experiments")
    ) {
      return;
    }
    try {
      const res = (await this.api
        .get("GET /api/qa/experiments", undefined, "state=running&limit=1")
        .catch(() => null)) as {
        experiments?: QAExperimentSummary[];
      } | null;
      this.runningExp.set(res?.experiments?.[0] ?? null);
    } catch {
      this.runningExp.set(null);
    }
  }

  armSummary(exp: QAExperimentSummary): string {
    if (!exp.arms?.length) return "";
    return exp.arms
      .map((a: QAExperimentArm) => `${a.label} ${a.weight}%`)
      .join(" / ");
  }

  ngOnInit(): void {
    this.loadForSection();
    this.loadRunningExp();
  }

  ngOnChanges(changes: SimpleChanges): void {
    if (changes["section"] && !changes["section"].firstChange) {
      this.loadForSection();
      this.loadRunningExp();
    }
  }

  protected canWriteSettings(): boolean {
    return (
      this.access.can("POST /api/settings") &&
      !this.state.sectionLocked(this.section())
    );
  }

  protected writeDeniedReason(): string | null {
    if (this.state.sectionLocked(this.section())) {
      return "Managed in the config repository";
    }
    if (!this.access.can("POST /api/settings")) {
      return (
        this.access.deniedReason("POST /api/settings") ||
        "settings:write scope required"
      );
    }
    return null;
  }

  // The Deployment credentials card's settings save through PUT
  // /api/settings and are config-managed (locked with the section); its
  // secrets go through /api/secrets under secrets:write and stay usable
  // while settings are locked. Neither follows capabilities:admin.
  protected canWriteCredentials(): boolean {
    return (
      this.access.can("POST /api/settings") &&
      !this.state.sectionLocked("capabilities")
    );
  }

  protected canWriteSecrets(): boolean {
    return (
      this.secretsEnabled() &&
      this.access.can("PUT /api/secrets/{secret}/{key}")
    );
  }

  protected credentialsRefusal(): string {
    const settingsNo = !this.access.can("POST /api/settings");
    const secretsNo = !this.access.can("PUT /api/secrets/{secret}/{key}");
    if (settingsNo && secretsNo) {
      return "Read-only for you: your role holds neither settings:write nor secrets:write. An operator can change your role in Settings → Access.";
    }
    if (settingsNo) {
      return "The GitHub host and GCP settings are read-only for you (settings:write scope required); secrets stay editable.";
    }
    if (secretsNo) {
      return "Secrets are read-only for you (secrets:write scope required).";
    }
    if (this.state.sectionLocked("capabilities")) {
      return "The GitHub host and GCP settings are managed in the config repository (tables/grain_config.json); stored secrets stay editable.";
    }
    return "";
  }

  protected qaControlsDisabledReason(): string | null {
    const writeReason = this.writeDeniedReason();
    if (writeReason) return writeReason;
    if (!this.qaEnabled()) {
      return "Enable quality control engine first";
    }
    return null;
  }

  protected qaEnabled(): boolean {
    return Boolean(this.state.val("qaEnabled", false));
  }

  protected canEditQAControls(): boolean {
    return this.canWriteSettings() && this.qaEnabled();
  }

  protected qaFlowValue(): string {
    const current = this.state.val<unknown>("qaFlowId", "golden-v1-minimal");
    return typeof current === "string" && current.trim() !== ""
      ? current
      : "golden-v1-minimal";
  }

  protected qaFlowOptions(): readonly PillOption[] {
    const serverFlows = this.state.val<unknown>("qaFlowIds", []);
    const defaults: readonly PillOption[] = [
      {
        value: "golden-v1-minimal",
        label: "golden-v1-minimal (Recommended)",
      },
      {
        value: "deterministic-s3-only",
        label: "deterministic-s3-only (Zero-LLM Ablation)",
      },
      {
        value: "repo-custom",
        label: "repo-custom (.grain/qa-flow.yml)",
      },
    ];
    if (!Array.isArray(serverFlows) || serverFlows.length === 0) {
      return defaults;
    }
    const labelByValue = new Map(defaults.map((opt) => [opt.value, opt.label]));
    return serverFlows
      .filter((item): item is string => typeof item === "string" && item !== "")
      .map((value) => ({
        value,
        label: labelByValue.get(value) ?? value,
      }));
  }

  protected qaRolloutPercent(): number {
    const raw = this.state.val<unknown>("qaRolloutPct", 0);
    const num = typeof raw === "number" ? raw : Number(raw);
    return Number.isFinite(num) ? num : 0;
  }

  protected qaRolloutInputValue(): string {
    const raw = this.state.qaRolloutRaw();
    if (raw && raw.epoch === this.state.discardEpoch()) {
      return raw.text;
    }
    return String(this.qaRolloutPercent());
  }

  protected qaRolloutError(): string | null {
    return this.state.fieldErrors()["qaRolloutPct"] ?? null;
  }

  protected qaShadowOnControl(): boolean {
    return Boolean(this.state.val("qaShadowOnControl", true));
  }

  protected toggleQAEnabled(): void {
    const nextEnabled = !this.qaEnabled();
    this.state.setField("qaEnabled", nextEnabled);
    if (!nextEnabled) {
      this.state.qaRolloutRaw.set(null);
      this.state.setFieldError("qaRolloutPct", null);
      if (!this.state.settings()["qaEnabled"]) {
        // Clear unsaved dependent edits when flipping back off on a deployment where QA was off.
        const s = this.state.settings();
        this.state.setField("qaFlowId", s["qaFlowId"] ?? "golden-v1-minimal");
        this.state.setField("qaRolloutPct", s["qaRolloutPct"] ?? 0);
        this.state.setField(
          "qaShadowOnControl",
          s["qaShadowOnControl"] ?? true,
        );
      }
    }
  }

  // setCount stages a whole-number setting, refusing a value under min
  // by the field's own label before anything is sent (the server would
  // refuse it too, naming the JSON key instead), and clearing the refusal
  // once the box holds a good value again.
  protected setCount(key: string, min: number, label: string, ev: Event): void {
    const raw = String((ev.target as HTMLInputElement | null)?.value ?? "");
    const n = Number(raw.trim());
    if (raw.trim() === "" || !Number.isInteger(n) || n < min) {
      this.state.setFieldError(
        key,
        `${label} must be a whole number of at least ${min}.`,
      );
      return;
    }
    this.state.setFieldError(key, null);
    this.state.setField(key, n);
  }

  protected setQAFlow(flowId: string): void {
    this.state.setField("qaFlowId", flowId);
  }

  protected onQARolloutInput(ev: Event): void {
    const target = ev.target;
    if (!(target instanceof HTMLInputElement)) return;
    const raw = target.value;
    this.state.qaRolloutRaw.set({
      epoch: this.state.discardEpoch(),
      text: raw,
    });

    const trimmed = raw.trim();
    if (trimmed === "" || !/^\d+$/.test(trimmed)) {
      this.state.setFieldError(
        "qaRolloutPct",
        "Enter a whole number from 0 to 100.",
      );
      return;
    }
    const parsed = Number(trimmed);
    if (!Number.isInteger(parsed) || parsed < 0 || parsed > 100) {
      this.state.setFieldError(
        "qaRolloutPct",
        "Rollout percentage must be between 0 and 100.",
      );
      return;
    }
    this.state.setFieldError("qaRolloutPct", null);
    this.state.setField("qaRolloutPct", parsed);
  }

  protected onQARolloutBlur(): void {
    if (!this.qaRolloutError()) {
      this.state.qaRolloutRaw.set(null);
    }
  }

  protected toggleQAShadowOnControl(): void {
    this.state.setField("qaShadowOnControl", !this.qaShadowOnControl());
  }

  protected defaultCapsText(): string {
    const v = this.state.val<unknown>("defaultCapabilities", []);
    return Array.isArray(v) ? v.join(", ") : String(v || "");
  }

  protected setDefaultCapsText(raw: string): void {
    const list = raw
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean);
    this.state.setField("defaultCapabilities", list);
  }

  private loadForSection(): void {
    const sec = this.section();
    if (sec === "agents") {
      if (this.access.can("GET /api/console-sessions")) {
        void this.loadConsoleSessions();
      }
    } else if (sec === "capabilities") {
      void this.loadCapabilities();
    } else if (sec === "memory") {
      void this.loadMemory();
    }
  }

  private async loadConsoleSessions(): Promise<void> {
    try {
      const r = await this.api.get<
        ConsoleSessionRow[] | { sessions?: ConsoleSessionRow[] }
      >("GET /api/console-sessions");
      this.consoleSessions.set(Array.isArray(r) ? r : (r?.sessions ?? []));
    } catch {
      // Non-fatal
    }
  }

  protected async createConsoleSession(): Promise<void> {
    const title = this.newSessionTitle().trim();
    if (!title) return;
    try {
      await this.api.post("POST /api/console-sessions", undefined, {
        title,
        name: title.toLowerCase().replace(/[^a-z0-9]+/g, "-"),
      });
      this.newSessionTitle.set("");
      await this.loadConsoleSessions();
      this.snackbar.show(`Created session ${title}`);
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  protected async restartSession(id: string): Promise<void> {
    try {
      await this.api.post(
        "POST /api/console-sessions/{id}/restart",
        { id },
        {},
      );
      this.snackbar.show(`Restarted ${id}`);
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  protected async deleteSession(id: string): Promise<void> {
    try {
      await this.api.delete("DELETE /api/console-sessions/{id}", { id });
      await this.loadConsoleSessions();
      this.snackbar.show(`Deleted ${id}`);
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  private async loadCapabilities(): Promise<void> {
    if (this.access.can("GET /api/capability-servers")) {
      try {
        const s = await this.api.get<
          CapabilityServerRow[] | { servers?: CapabilityServerRow[] }
        >("GET /api/capability-servers");
        this.capServers.set(Array.isArray(s) ? s : (s?.servers ?? []));
      } catch {
        // Non-fatal
      }
    }
    if (this.access.can("GET /api/capability-policy")) {
      try {
        const p = await this.api.get<{
          policy?: {
            rules?: CapabilityPolicyRule[];
            catalogue?: CapabilityCatalogue;
          };
          rules?: CapabilityPolicyRule[];
        }>("GET /api/capability-policy");
        this.policyRules.set(p?.policy?.rules ?? p?.rules ?? []);
        this.catalogue.set(p?.policy?.catalogue ?? null);
      } catch {
        // Non-fatal
      }
    }
    if (this.access.can("GET /api/api-scope-sets")) {
      try {
        const ss = await this.api.get<ScopeSetRow[] | { sets?: ScopeSetRow[] }>(
          "GET /api/api-scope-sets",
        );
        this.scopeSets.set(Array.isArray(ss) ? ss : (ss?.sets ?? []));
      } catch {
        // Non-fatal
      }
    }
    await this.loadSecrets();
  }

  private async loadSecrets(): Promise<void> {
    if (!this.access.can("GET /api/secrets")) return;
    try {
      const sec = await this.api.get<
        SecretMetaRow[] | { secrets?: SecretMetaRow[]; enabled?: boolean }
      >("GET /api/secrets");
      this.secretsList.set(Array.isArray(sec) ? sec : (sec?.secrets ?? []));
      this.secretsEnabled.set(Array.isArray(sec) || sec?.enabled !== false);
    } catch {
      // Non-fatal
    }
  }

  protected async saveCapServer(): Promise<void> {
    const id = this.newServerId().trim();
    const url = this.newServerUrl().trim();
    if (!id || !url) return;
    try {
      await this.api.put("PUT /api/capability-servers", undefined, {
        id,
        config: url.startsWith("http") ? { serverUrl: url } : { command: url },
      });
      this.newServerId.set("");
      this.newServerUrl.set("");
      await this.loadCapabilities();
      this.snackbar.show(`Saved server ${id}`);
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async toggleCapServer(id: string, enable: boolean): Promise<void> {
    try {
      await this.api.post(
        enable
          ? "POST /api/capability-servers/enable"
          : "POST /api/capability-servers/disable",
        undefined,
        { id },
      );
      await this.loadCapabilities();
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async testCapServer(id: string): Promise<void> {
    try {
      const res = await this.api.post<{
        ok?: boolean;
        detail?: string;
        error?: string;
        methods?: unknown[];
      }>("POST /api/capability-servers/test", undefined, { id });
      // The probe answers ok/error/methods; it has no `detail`, so a
      // failure read only "failed" with the reason dropped.
      const ok = res?.ok !== false;
      const n = Array.isArray(res?.methods) ? res.methods.length : 0;
      this.capTestOk.set(ok);
      this.capTestNotice.set(
        `${id}: ${
          res?.detail ||
          (ok
            ? `reachable${n ? `, ${n} method${n === 1 ? "" : "s"}` : ""}`
            : `failed — ${res?.error || "no reason given"}`)
        }`,
      );
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected readonly confirmReq = signal<ConfirmRequest | null>(null);

  protected async onConfirmDone(accepted: boolean): Promise<void> {
    const req = this.confirmReq();
    this.confirmReq.set(null);
    if (!accepted || !req) return;
    await req.run();
  }

  // Removing a server drops it from every grant naming it, so it asks
  // first, as host and Git connection removal do.
  protected confirmRemoveCapServer(id: string): void {
    this.confirmReq.set({
      title: "Remove capability server?",
      message: `Remove ${id}? Tasks that hold a grant on it can no longer use it.`,
      accept: "Remove",
      destructive: true,
      run: () => this.removeCapServer(id),
    });
  }

  protected async removeCapServer(id: string): Promise<void> {
    try {
      await this.api.post("POST /api/capability-servers/remove", undefined, {
        id,
      });
      await this.loadCapabilities();
      this.snackbar.show(`Removed ${id}`);
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected addPolicyRule(): void {
    const capability = this.newRuleCap().trim();
    if (!capability) return;
    const repos = this.newRuleRepos()
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean);
    const deny = this.newRuleEffect() === "deny";
    const rule: CapabilityPolicyRule = {
      capability,
      ...(repos.length > 0 ? { repos } : {}),
      ...(deny
        ? { deny: true }
        : this.newRuleAllowUntrusted()
          ? { allowUntrusted: true }
          : {}),
    };
    this.policyRules.update((r) => [...r, rule]);
    this.newRuleCap.set("");
    this.newRuleRepos.set("");
    this.newRuleAllowUntrusted.set(false);
  }

  protected removePolicyRule(idx: number): void {
    this.policyRules.update((r) => r.filter((_, i) => i !== idx));
  }

  protected async previewCapPolicy(): Promise<void> {
    this.policyError.set(null);
    try {
      const res = await this.api.post<{
        denied?: CapabilityPolicyDenial[];
      }>("POST /api/capability-policy/preview", undefined, {
        policy: { rules: this.policyRules() },
      });
      const denied = Array.isArray(res?.denied) ? res.denied : [];
      if (denied.length === 0) {
        this.capTestNotice.set(
          `Policy preview: 0 existing grants newly denied (${this.policyRules().length} rules)`,
        );
      } else {
        const items = denied
          .slice(0, 5)
          .map((d) => `${d.kind}:${d.name || d.id} (${d.reason || d.grant})`)
          .join("; ");
        this.capTestNotice.set(
          `Policy preview: ${denied.length} grant(s) newly denied — ${items}`,
        );
      }
    } catch (err) {
      this.policyError.set(errText(err));
    }
  }

  protected async saveCapPolicy(): Promise<void> {
    this.policyError.set(null);
    try {
      await this.api.put("PUT /api/capability-policy", undefined, {
        policy: { rules: this.policyRules() },
      });
      this.snackbar.show("Capability policy saved");
    } catch (err) {
      this.policyError.set(errText(err));
    }
  }

  protected async saveScopeSet(): Promise<void> {
    const name = this.newScopeSetName().trim();
    if (!name) return;
    const scopes = this.newScopeSetScopes()
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean);
    try {
      await this.api.put("PUT /api/api-scope-sets", undefined, {
        set: { name, scopes },
      });
      this.newScopeSetName.set("");
      this.newScopeSetScopes.set("");
      await this.loadCapabilities();
      this.snackbar.show(`Saved scope set ${name}`);
    } catch (err) {
      this.state.recordError(err);
    }
  }

  // askRemoveScopeSet counts what grants the set before asking: every
  // one of those grants grants nothing once the set is gone.
  protected async askRemoveScopeSet(name: string): Promise<void> {
    let holders: ScopeSetHolder[] | null = null;
    try {
      const usage = await this.api.get<
        | { name: string; holders?: ScopeSetHolder[] }[]
        | { items?: { name: string; holders?: ScopeSetHolder[] }[] }
      >("GET /api/api-scope-sets/usage");
      const rows = Array.isArray(usage) ? usage : (usage?.items ?? []);
      holders = rows.find((u) => u.name === name)?.holders ?? [];
    } catch {
      // The count is the warning's detail; removing works without it.
    }
    const grant = `api:${name}`;
    let message: string;
    if (holders === null) {
      message = `Grain could not count what grants ${grant}. Every grant of it will grant nothing once it is removed.`;
    } else if (holders.length === 0) {
      message = `Nothing grants ${grant}.`;
    } else {
      const n = holders.length;
      const listed = holders
        .slice(0, 5)
        .map((h) => `${h.kind} ${h.name || h.id}`)
        .join(", ");
      message = `${n} ${n === 1 ? "grant holds" : "grants hold"} ${grant}, and will grant nothing once it is removed: ${listed}${n > 5 ? `, and ${n - 5} more` : ""}.`;
    }
    this.confirmReq.set({
      title: `Remove scope set ${name}?`,
      message,
      accept: "Remove",
      destructive: true,
      run: () => this.removeScopeSet(name),
    });
  }

  protected async removeScopeSet(name: string): Promise<void> {
    try {
      await this.api.post("POST /api/api-scope-sets/delete", undefined, {
        name,
      });
      await this.loadCapabilities();
      this.snackbar.show(`Removed scope set ${name}`);
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected setSecretValue(v: string): void {
    this.secretFileName.set("");
    this.secretVal.set(v);
  }

  // loadSecretFile takes a value from a file -- a PEM, a JSON key --
  // keeping its newlines, which a password box would drop.
  protected async loadSecretFile(ev: Event): Promise<void> {
    const input = ev.target as HTMLInputElement | null;
    const file = input?.files?.[0];
    if (!file) return;
    try {
      this.secretVal.set(await file.text());
      this.secretFileName.set(file.name);
    } catch (err) {
      this.state.recordError(err);
    } finally {
      if (input) input.value = "";
    }
  }

  // replaceSecret fills the form with a stored key's name, so the next
  // value stored replaces it; nothing can read the old one back.
  protected replaceSecret(name: string, key: string): void {
    this.secretName.set(name);
    this.secretKey.set(key);
    this.setSecretValue("");
    queueMicrotask(() =>
      (
        document.querySelector(
          '[data-testid="input-secret-value"]',
        ) as HTMLInputElement | null
      )?.focus(),
    );
  }

  // Both removals ask first: nothing here can read a value back, so a
  // stray press would lose it for good.
  protected confirmRemoveSecretKey(name: string, key: string): void {
    this.confirmReq.set({
      title: `Remove ${name}/${key}?`,
      message: `The stored value cannot be read back, so putting it back means pasting it again.`,
      accept: "Remove",
      destructive: true,
      run: () => this.deleteSecret(name, key),
    });
  }

  protected confirmRemoveSecret(name: string): void {
    this.confirmReq.set({
      title: `Remove the secret ${name}?`,
      message: `Removes ${name} and every key in it. No stored value can be read back, so putting them back means pasting each one again.`,
      accept: "Remove",
      destructive: true,
      run: () => this.deleteSecret(name),
    });
  }

  protected async deleteSecret(name: string, key?: string): Promise<void> {
    try {
      if (key) {
        await this.api.delete("DELETE /api/secrets/{secret}/{key}", {
          secret: name,
          key,
        });
      } else {
        await this.api.delete("DELETE /api/secrets/{secret}", { secret: name });
      }
      await this.loadSecrets();
      this.snackbar.show(`Removed ${key ? `${name}/${key}` : name}`);
    } catch (err) {
      this.state.recordError(err);
    }
  }

  // submitSecret is the form's submit (the button, or Enter in a box):
  // it stores only what the button would.
  protected submitSecret(): void {
    if (
      this.secretSaving() ||
      !this.secretName().trim() ||
      !this.secretKey().trim() ||
      this.secretVal().length === 0 ||
      !this.canWriteSecrets()
    ) {
      return;
    }
    void this.putSecret();
  }

  protected async putSecret(): Promise<void> {
    const name = this.secretName().trim();
    const key = this.secretKey().trim();
    const value = this.secretVal();
    const fileName = this.secretFileName();
    const replacing = this.secretExists();
    this.secretSaving.set(true);
    try {
      await this.api.put(
        "PUT /api/secrets/{secret}/{key}",
        { secret: name, key },
        { value },
      );
      if (
        this.secretName().trim() === name &&
        this.secretKey().trim() === key &&
        this.secretVal() === value &&
        this.secretFileName() === fileName
      ) {
        this.setSecretValue("");
      }
      await this.loadSecrets();
      this.snackbar.show(
        replacing ? `Replaced ${name}/${key}` : `Stored ${name}/${key}`,
      );
    } catch (err) {
      this.state.recordError(err);
    } finally {
      this.secretSaving.set(false);
    }
  }

  private async loadMemory(): Promise<void> {
    if (this.access.can("GET /api/memory")) {
      try {
        const m = await this.api.get<MemoryRow[]>("GET /api/memory");
        if (Array.isArray(m)) {
          this.memoryBody.set(m.find((r) => !r.repo)?.text ?? "");
        }
      } catch {
        // Non-fatal
      }
    }
    if (this.access.can("GET /api/memory/documents")) {
      try {
        const docs = await this.api.get<
          (string | { name?: string })[] | { documents?: string[] }
        >("GET /api/memory/documents");
        const arr = Array.isArray(docs) ? docs : (docs?.documents ?? []);
        this.memoryDocs.set(
          arr
            .map((d) => (typeof d === "string" ? d : (d.name ?? "")))
            .filter(Boolean),
        );
      } catch {
        // Non-fatal
      }
    }
  }

  // saveMemory saves both boxes the Memory card shows: the deployment
  // memory document and, when it was edited, the standing instructions
  // (a setting, which Save memory used to leave unsaved).
  protected async saveMemory(): Promise<void> {
    try {
      await this.state.saveFields(["promptExtension"]);
      await this.api.put("PUT /api/memory", undefined, {
        text: this.memoryBody(),
      });
      this.snackbar.show("Memory updated");
    } catch (err) {
      this.state.recordError(err);
    }
  }
}
