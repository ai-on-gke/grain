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
  OnChanges,
  OnInit,
  ViewEncapsulation,
  computed,
  inject,
  input,
  signal,
} from "@angular/core";
import { GrainApi } from "../api/grain-api.service";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import {
  ConfirmDialogComponent,
  type ConfirmRequest,
} from "../shared/confirm-dialog.component";
import {
  type ModelBackendLike,
  availabilityNote,
  containsModel,
  modelAvailability,
  modelGroups,
  parseDefaultModels,
} from "../shared/model-catalog";
import { ModelPickerComponent } from "../shared/model-picker.component";
import {
  type PillOption,
  PillSelectComponent,
} from "../shared/pill-select.component";
import type { SettingsSectionId } from "./settings-sections";
import { SettingsStateService, errText } from "./settings-state.service";

interface AgentKeysResponse {
  enabled?: boolean;
  geminiApiKeySet?: boolean;
  claudeOAuthTokenSet?: boolean;
  openaiApiKeySet?: boolean;
  codexAuthJSONSet?: boolean;
  codexAuthRefreshError?: string;
}

interface KeyCheckResult {
  ok: boolean;
  detail: string;
  checkedAt?: string;
}

interface ModelSettingRow {
  model: string;
  effort?: string;
}

interface BackendEntry extends ModelBackendLike {
  id: string;
  name?: string;
  framework: string;
  models?: string[];
  authMode?: string;
  cliPath?: string;
  concurrency?: number;
  hostd?: string;
  disabled?: boolean;
}

interface TokenBudgetRow {
  connection: string;
  model?: string;
  tokens: number;
  window: string;
}

interface UsageSettingsDto {
  mode?: string;
  minWorkers?: number;
  retentionDays?: number;
  budgets?: TokenBudgetRow[];
}

interface HostdInstallDto {
  release?: string;
  image?: string;
  lines?: string[];
  note?: string;
  command?: string;
}

interface HostdRow {
  name: string;
  owner?: string;
  state?: string;
  revoked_at?: string | null;
  revokedAt?: string | null;
  labels?: Record<string, string> | null;
  capacity?: number;
}

// DeletedHostdRow is what deleting a host answers with: its name and the
// usage-limit pauses that went with it.
interface DeletedHostdRow {
  name: string;
  clearedPauses?: string[] | null;
}

function deletedHostdLine(d: DeletedHostdRow): string {
  let line = `Deleted host ${d.name}.`;
  if (d.clearedPauses?.length) {
    line += ` Cleared its usage-limit pauses: ${d.clearedPauses.join(", ")}.`;
  }
  return line;
}

interface EnrollmentCodeRow {
  name: string;
  prefix?: string;
  code?: string;
  expires_at?: string | null;
  max_uses?: number;
  uses?: number;
  revoked_at?: string | null;
}

interface PendingHostdRow {
  id: string;
  hostname?: string;
  suggested_name?: string;
  enrolled_by?: string;
  os?: string;
  arch?: string;
  key_fingerprint?: string;
  default_labels?: Record<string, string> | null;
  default_capacity?: number;
  created_at?: string;
  expires_at?: string;
}

export function parseHostdLabels(raw: string): {
  labels: Record<string, string>;
  error?: string;
} {
  const trimmed = raw.trim();
  if (!trimmed) return { labels: {} };
  const out: Record<string, string> = {};
  for (const part of trimmed.split(/[\s,]+/).filter(Boolean)) {
    const eq = part.indexOf("=");
    if (eq <= 0 || eq === part.length - 1) {
      return {
        labels: {},
        error: `Host label "${part}" must be key=value (no spaces or commas).`,
      };
    }
    const k = part.slice(0, eq).trim();
    const v = part.slice(eq + 1).trim();
    if (!k || !v || v.includes("=")) {
      return {
        labels: {},
        error: `Host label "${part}" must be key=value.`,
      };
    }
    out[k] = v;
  }
  return { labels: out };
}

export function formatHostdLabels(
  labels?: Record<string, string> | null,
): string {
  if (!labels || typeof labels !== "object" || Array.isArray(labels)) {
    return "—";
  }
  const entries = Object.entries(labels);
  if (entries.length === 0) return "—";
  return entries.map(([k, v]) => `${k}=${v}`).join(", ");
}

function formatRefreshedAt(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const months = [
    "Jan",
    "Feb",
    "Mar",
    "Apr",
    "May",
    "Jun",
    "Jul",
    "Aug",
    "Sep",
    "Oct",
    "Nov",
    "Dec",
  ];
  const hh = String(d.getUTCHours()).padStart(2, "0");
  const mm = String(d.getUTCMinutes()).padStart(2, "0");
  return `${months[d.getUTCMonth()]} ${d.getUTCDate()}, ${hh}:${mm} UTC`;
}

@Component({
  selector: "g-settings-compute",
  imports: [PillSelectComponent, ConfirmDialogComponent, ModelPickerComponent],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @switch (section()) {
      @case ("models") {
        <div class="s-card" data-testid="card-model-defaults">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">
                Default model &amp; per-model reasoning effort
              </h3>
              <p class="s-card-desc">
                The model a task runs on when it does not pin one, the fallback
                models tried after it, and per-model reasoning effort. The
                choices are the models your connections advertise.
              </p>
            </div>
          </div>
          <div class="s-grid">
            <div class="s-field">
              <span class="s-label">Deployment default model</span>
              <g-model-picker
                testid="picker-default-model"
                ariaLabel="Deployment default model"
                placeholder="Not set: each connection's own default"
                [groups]="modelGroups()"
                [value]="modelChain()[0] || ''"
                [disabled]="!canEditChain()"
                [refreshable]="canRefreshModels() && anyDiscovers()"
                [refreshing]="refreshingModels()"
                [refreshNote]="refreshNote()"
                (refresh)="refreshDiscoveredModels()"
                (valueChange)="setDefaultModel($event)"
              />
            </div>
          </div>

          <div class="s-field mp-fallbacks" data-testid="fallback-models">
            <span class="s-label">Fallback models</span>
            <span class="s-hint">
              Tried in order after the default model when it is paused after a
              usage limit, its connections are at their concurrency limit, or no
              enabled connection advertises it. A task that pins its own model
              never falls back.
            </span>
            @if (fallbackModels().length > 0) {
              <ol class="mp-chain" aria-label="Fallback models, in order">
                @for (
                  m of fallbackModels();
                  track m;
                  let i = $index, first = $first, last = $last
                ) {
                  <li
                    class="mp-chain-row"
                    [attr.data-testid]="'fallback-row-' + i"
                  >
                    <span class="mp-chain-pos" aria-hidden="true">{{
                      i + 1
                    }}</span>
                    <span class="mp-chain-model">{{ m }}</span>
                    @if (modelNote(m)) {
                      <span class="mp-flag">{{ modelNote(m) }}</span>
                    }
                    <span class="mp-chain-actions">
                      <button
                        type="button"
                        class="btn-text s-btn-sm"
                        [attr.aria-label]="'Move ' + m + ' up'"
                        [attr.data-testid]="'fallback-up-' + i"
                        [disabled]="first || !canEditChain()"
                        (click)="moveFallback(i, -1)"
                      >
                        Up
                      </button>
                      <button
                        type="button"
                        class="btn-text s-btn-sm"
                        [attr.aria-label]="'Move ' + m + ' down'"
                        [attr.data-testid]="'fallback-down-' + i"
                        [disabled]="last || !canEditChain()"
                        (click)="moveFallback(i, 1)"
                      >
                        Down
                      </button>
                      <button
                        type="button"
                        class="btn-text s-btn-sm"
                        [attr.aria-label]="'Remove ' + m"
                        [attr.data-testid]="'fallback-remove-' + i"
                        [disabled]="!canEditChain()"
                        (click)="removeFallback(i)"
                      >
                        Remove
                      </button>
                    </span>
                  </li>
                }
              </ol>
            }
            <g-model-picker
              testid="picker-add-fallback"
              ariaLabel="Add fallback model"
              [placeholder]="
                modelChain().length
                  ? 'Add fallback model'
                  : 'Choose a default model first'
              "
              [groups]="modelGroups()"
              [exclude]="modelChain()"
              [disabled]="!canEditChain() || modelChain().length === 0"
              [refreshable]="canRefreshModels() && anyDiscovers()"
              [refreshing]="refreshingModels()"
              [refreshNote]="refreshNote()"
              (refresh)="refreshDiscoveredModels()"
              (valueChange)="addFallback($event)"
            />
          </div>

          @if (chainWarning()) {
            <div
              class="s-banner warn"
              role="status"
              data-testid="default-model-warning"
            >
              {{ chainWarning() }}
            </div>
          }

          <div class="s-subsection">
            <div class="s-subhead">
              <h4 class="s-subhead-title">Per-model reasoning effort</h4>
            </div>
            @if (modelSettingsRows().length > 0) {
              <div class="s-table-wrap">
                <table
                  class="s-table"
                  aria-label="Per-model reasoning settings"
                >
                  <thead>
                    <tr>
                      <th>Model</th>
                      <th>Reasoning effort</th>
                      <th class="s-actions-cell">Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    @for (
                      ms of modelSettingsRows();
                      track ms.model;
                      let idx = $index
                    ) {
                      <tr [attr.data-testid]="'model-setting-row-' + ms.model">
                        <td class="s-mono">{{ ms.model }}</td>
                        <td>{{ ms.effort || "default" }}</td>
                        <td class="s-actions-cell">
                          <div class="s-row-actions">
                            <button
                              type="button"
                              class="btn-text s-btn-sm"
                              [disabled]="!canWriteDeployment()"
                              (click)="removeModelSetting(idx)"
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

            <div class="s-inline">
              <g-model-picker
                class="mp-inline"
                testid="picker-model-setting-model"
                ariaLabel="Model for a reasoning effort setting"
                placeholder="Choose a model"
                [groups]="modelGroups()"
                [value]="newModelSettingName()"
                [disabled]="!canWriteDeployment()"
                (valueChange)="newModelSettingName.set($event)"
              />
              <g-pill-select
                testid="select-model-setting-effort"
                ariaLabel="Reasoning effort"
                align="left"
                [options]="effortOptions"
                [value]="newModelSettingEffort()"
                [disabled]="!canWriteDeployment()"
                (valueChange)="newModelSettingEffort.set($event)"
              />
              <button
                type="button"
                class="btn-tonal s-btn-sm"
                data-testid="add-model-setting-btn"
                [disabled]="
                  !newModelSettingName().trim() || !canWriteDeployment()
                "
                (click)="addModelSetting()"
              >
                Set model effort
              </button>
            </div>
          </div>
        </div>

        <div class="s-card" data-testid="card-agent-keys">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Framework API keys &amp; Codex auth</h3>
              <p class="s-card-desc">
                Write-only credentials stored in the secret vault; values are
                never returned to the browser.
              </p>
            </div>
          </div>
          @if (!access.can("GET /api/agent-keys")) {
            <div class="s-banner warn" data-testid="agent-keys-restricted">
              {{ access.deniedReason("GET /api/agent-keys") }}
            </div>
          } @else {
            @if (!agentKeysEnabled()) {
              <div class="s-banner readonly" data-testid="agent-keys-disabled">
                Local secrets vault is not configured on this controller.
              </div>
            }
            <div class="s-table-wrap">
              <table class="s-table" aria-label="Framework API keys">
                <thead>
                  <tr>
                    <th>Framework</th>
                    <th>Status</th>
                    <th class="s-actions-cell">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  @for (fw of frameworks; track fw) {
                    <tr>
                      <td class="s-cell-title">{{ fw }}</td>
                      <td>
                        <span
                          [class]="
                            'status-pill ' +
                            (isKeyConfigured(fw) ? 'healthy' : 'neutral')
                          "
                          [attr.data-testid]="'agent-key-status-' + fw"
                        >
                          {{ isKeyConfigured(fw) ? "Configured" : "Not set" }}
                        </span>
                        @if (keyChecks()[fw]; as chk) {
                          <span
                            [class]="
                              'status-pill ' + (chk.ok ? 'healthy' : 'error')
                            "
                            [attr.data-testid]="'key-check-result-' + fw"
                            style="margin-left: 6px"
                          >
                            {{ chk.ok ? "Valid" : "Failed" }}: {{ chk.detail }}
                          </span>
                        }
                      </td>
                      <td class="s-actions-cell">
                        <div class="s-row-actions">
                          <button
                            type="button"
                            class="btn-tonal s-btn-sm"
                            [attr.data-testid]="'toggle-agent-key-' + fw"
                            [attr.aria-expanded]="openKeyFramework() === fw"
                            [attr.aria-label]="'Update API key for ' + fw"
                            [disabled]="
                              !agentKeysEnabled() ||
                              !access.can('PUT /api/agent-keys/{framework}')
                            "
                            (click)="toggleKeyEditor(fw)"
                          >
                            Update key
                          </button>
                          <button
                            type="button"
                            class="btn-tonal s-btn-sm"
                            [attr.data-testid]="'check-agent-key-' + fw"
                            [disabled]="
                              !access.can(
                                'POST /api/agent-keys/{framework}/check'
                              ) || busyKey() === fw
                            "
                            (click)="checkAgentKey(fw)"
                          >
                            Check
                          </button>
                          @if (isKeyConfigured(fw)) {
                            <button
                              type="button"
                              class="btn-text s-btn-sm"
                              [attr.data-testid]="'clear-agent-key-' + fw"
                              [disabled]="
                                !agentKeysEnabled() ||
                                !access.can(
                                  'DELETE /api/agent-keys/{framework}'
                                ) ||
                                busyKey() === fw
                              "
                              (click)="clearAgentKey(fw)"
                            >
                              Clear
                            </button>
                          }
                        </div>
                      </td>
                    </tr>
                    @if (openKeyFramework() === fw) {
                      <tr
                        class="s-subrow"
                        [attr.data-testid]="'agent-key-row-' + fw"
                      >
                        <td colspan="3">
                          <form
                            class="s-cred-bar"
                            autocomplete="off"
                            (submit)="$event.preventDefault(); saveAgentKey(fw)"
                          >
                            <span class="s-label"
                              >API key for
                              <span class="s-mono">{{ fw }}</span></span
                            >
                            <input
                              class="s-input s-input-inline"
                              type="password"
                              [attr.name]="'grain-agent-key-' + fw"
                              autocomplete="new-password"
                              data-lpignore="true"
                              data-1p-ignore="true"
                              [attr.aria-label]="fw + ' API key'"
                              [attr.data-testid]="'input-agent-key-' + fw"
                              placeholder="Paste secret key…"
                              [value]="keyDrafts()[fw] || ''"
                              [disabled]="
                                !agentKeysEnabled() ||
                                !access.can('PUT /api/agent-keys/{framework}')
                              "
                              (input)="
                                setKeyDraft(fw, $any($event.target).value)
                              "
                            />
                            <button
                              type="submit"
                              class="btn-primary s-btn-sm"
                              [attr.data-testid]="'save-agent-key-' + fw"
                              [disabled]="
                                !agentKeysEnabled() ||
                                !(keyDrafts()[fw] || '').trim() ||
                                !access.can(
                                  'PUT /api/agent-keys/{framework}'
                                ) ||
                                busyKey() === fw
                              "
                            >
                              Save key
                            </button>
                            <button
                              type="button"
                              class="btn-text s-btn-sm"
                              (click)="openKeyFramework.set(null)"
                            >
                              Close
                            </button>
                          </form>
                        </td>
                      </tr>
                    }
                  }
                </tbody>
              </table>
            </div>
          }

          <div class="s-subsection">
            <div class="s-field">
              <div class="s-inline">
                <label class="s-label" for="f-codex-auth"
                  >Codex auth.json (OAuth session bundle)</label
                >
                <span
                  [class]="
                    'status-pill ' +
                    (codexAuthJsonSet() ? 'healthy' : 'neutral')
                  "
                  data-testid="codex-auth-status"
                >
                  {{ codexAuthJsonSet() ? "Configured" : "Not set" }}
                </span>
                @if (codexAuthRefreshError()) {
                  <span class="status-pill error">{{
                    codexAuthRefreshError()
                  }}</span>
                }
              </div>
              <textarea
                id="f-codex-auth"
                class="s-textarea s-mono"
                rows="2"
                data-testid="input-codex-auth"
                placeholder='{"tokens": {...}}'
                [value]="codexAuthDraft()"
                [disabled]="!access.can('PUT /api/codex-auth')"
                (input)="codexAuthDraft.set($any($event.target).value)"
              ></textarea>
              <div class="s-inline">
                <button
                  type="button"
                  class="btn-tonal s-btn-sm"
                  data-testid="save-codex-auth"
                  [disabled]="
                    !codexAuthDraft().trim() ||
                    !access.can('PUT /api/codex-auth')
                  "
                  (click)="saveCodexAuth()"
                >
                  Save Codex auth
                </button>
                <button
                  type="button"
                  class="btn-text s-btn-sm"
                  data-testid="clear-codex-auth"
                  [disabled]="
                    !this.codexAuthJsonSet() ||
                    !access.can('DELETE /api/codex-auth')
                  "
                  (click)="clearCodexAuth()"
                >
                  Clear Codex auth
                </button>
              </div>
            </div>
          </div>
        </div>

        @if (!modelsConfigured()) {
          <div class="s-banner readonly">
            <span
              >No connections configured yet. Tasks run only on configured
              connections and wait until at least one is available.</span
            >
          </div>
        }
        <div class="s-card" data-testid="card-model-backends">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">
                Model connections &amp; discovered models
              </h3>
              <p class="s-card-desc">
                Named Vertex AI, Anthropic, or OpenAI-compatible backends with
                dedicated credentials and model discovery.
              </p>
            </div>
            <button
              type="button"
              class="btn-tonal s-btn-sm"
              data-testid="refresh-discovered-models"
              [disabled]="!access.can('GET /api/agent-models')"
              (click)="loadDiscoveredModels()"
            >
              Discover models
            </button>
          </div>

          @if (backends().length > 0) {
            <div class="s-table-wrap">
              <table class="s-table" aria-label="Model connections">
                <thead>
                  <tr>
                    <th>Connection</th>
                    <th>Framework</th>
                    <th>Placement &amp; discovery</th>
                    <th class="s-actions-cell">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  @for (b of backends(); track b.id) {
                    <tr [attr.data-testid]="'backend-row-' + b.id">
                      <td>
                        <div class="s-cell-stack">
                          <span class="s-cell-title">{{ b.name || b.id }}</span>
                          @if (b.name) {
                            <span class="s-meta-mono">{{ b.id }}</span>
                          }
                        </div>
                      </td>
                      <td>
                        <span class="status-pill neutral">{{
                          b.framework
                        }}</span>
                      </td>
                      <td>
                        <div>
                          {{ b.authMode || "default"
                          }}{{ b.hostd ? " · host " + b.hostd : ""
                          }}{{ b.concurrency ? " · max " + b.concurrency : "" }}
                        </div>
                        @if (b.models?.length) {
                          <div class="s-meta">
                            Pinned:
                            <span class="s-mono">{{
                              b.models?.join(", ")
                            }}</span>
                          </div>
                        }
                        @if (b.discovered) {
                          <details class="s-disc-details">
                            <summary>
                              {{ b.discovered.models?.length || 0 }} models
                              discovered
                              @if (b.discovered.refreshedAt) {
                                <span
                                  class="s-meta"
                                  [attr.title]="b.discovered.refreshedAt"
                                >
                                  ·
                                  {{
                                    formatRefreshedAt(b.discovered.refreshedAt)
                                  }}</span
                                >
                              }
                            </summary>
                            <div class="s-disc-models">
                              {{ b.discovered.models?.join(", ") || "None" }}
                            </div>
                          </details>
                          @if (b.discovered.error) {
                            <div
                              class="status-pill error"
                              style="margin-top: 6px"
                            >
                              {{ b.discovered.error }}
                            </div>
                          }
                        }
                      </td>
                      <td class="s-actions-cell">
                        <div class="s-row-actions">
                          @if (discovers(b)) {
                            <button
                              type="button"
                              class="btn-tonal s-btn-sm"
                              [attr.data-testid]="'refresh-backend-' + b.id"
                              [attr.aria-label]="'Refresh models of ' + b.id"
                              [disabled]="
                                !canRefreshModels() || refreshingModels()
                              "
                              (click)="refreshDiscoveredModels(b.id)"
                            >
                              Refresh
                            </button>
                          }
                          @if (b.authMode !== "ambient") {
                            <button
                              type="button"
                              class="btn-tonal s-btn-sm"
                              [attr.data-testid]="'toggle-cred-' + b.id"
                              [attr.aria-expanded]="
                                openCredBackendId() === b.id
                              "
                              [attr.aria-label]="
                                'Manage credential for ' + b.id
                              "
                              (click)="toggleCredEditor(b.id)"
                            >
                              Credential
                            </button>
                          }
                          <button
                            type="button"
                            class="btn-tonal s-btn-sm"
                            [attr.data-testid]="'edit-backend-' + b.id"
                            [attr.aria-label]="'Edit connection ' + b.id"
                            (click)="editBackend(b)"
                          >
                            Edit
                          </button>
                          <button
                            type="button"
                            class="btn-text s-btn-sm"
                            [attr.data-testid]="'remove-backend-' + b.id"
                            [attr.aria-label]="'Remove connection ' + b.id"
                            [disabled]="
                              !access.can(
                                'DELETE /api/deployment/agent-backends/{backendId}'
                              )
                            "
                            (click)="startDeleteBackend(b.id)"
                          >
                            Remove
                          </button>
                        </div>
                      </td>
                    </tr>
                    @if (
                      openCredBackendId() === b.id && b.authMode !== "ambient"
                    ) {
                      <tr
                        class="s-subrow"
                        [attr.data-testid]="'backend-cred-row-' + b.id"
                      >
                        <td colspan="4">
                          <form
                            class="s-cred-bar"
                            autocomplete="off"
                            (submit)="
                              $event.preventDefault(); saveBackendCred(b.id)
                            "
                          >
                            <span class="s-label"
                              >Credential for
                              <span class="s-mono">{{ b.id }}</span></span
                            >
                            <input
                              class="s-input s-input-inline"
                              type="password"
                              [attr.name]="'grain-backend-cred-' + b.id"
                              autocomplete="new-password"
                              data-lpignore="true"
                              data-1p-ignore="true"
                              [attr.aria-label]="'Credential for ' + b.id"
                              placeholder="Paste API key or secret…"
                              [value]="backendCredDrafts()[b.id] || ''"
                              [disabled]="
                                !access.can(
                                  'PUT /api/agent-backends/{id}/credential'
                                )
                              "
                              (input)="
                                setBackendCredDraft(
                                  b.id,
                                  $any($event.target).value
                                )
                              "
                            />
                            <button
                              type="submit"
                              class="btn-primary s-btn-sm"
                              [disabled]="
                                !(backendCredDrafts()[b.id] || '').trim() ||
                                !access.can(
                                  'PUT /api/agent-backends/{id}/credential'
                                )
                              "
                            >
                              Save secret
                            </button>
                            <button
                              type="button"
                              class="btn-tonal s-btn-sm"
                              [attr.data-testid]="'clear-cred-' + b.id"
                              [attr.aria-label]="'Clear credential for ' + b.id"
                              [disabled]="
                                !access.can(
                                  'DELETE /api/agent-backends/{id}/credential'
                                )
                              "
                              (click)="clearBackendCred(b.id)"
                            >
                              Clear stored secret
                            </button>
                            <button
                              type="button"
                              class="btn-text s-btn-sm"
                              (click)="openCredBackendId.set(null)"
                            >
                              Close
                            </button>
                          </form>
                        </td>
                      </tr>
                    }
                  }
                </tbody>
              </table>
            </div>
          }

          <div class="s-subsection">
            <div class="s-subhead">
              <h4 class="s-subhead-title">
                {{
                  editingBackendId()
                    ? "Edit connection: " + editingBackendId()
                    : "Add a model connection"
                }}
              </h4>
              @if (editingBackendId()) {
                <button
                  type="button"
                  class="btn-text s-btn-sm"
                  (click)="cancelEditBackend()"
                >
                  Cancel edit
                </button>
              }
            </div>
            <div class="s-grid">
              <div class="s-field">
                <label class="s-label" for="nb-id">Connection ID</label>
                <input
                  id="nb-id"
                  class="s-input s-mono"
                  type="text"
                  data-testid="input-new-backend-id"
                  placeholder="claude-team-1"
                  [value]="newBackendId()"
                  (input)="newBackendId.set($any($event.target).value)"
                />
              </div>
              <div class="s-field">
                <label class="s-label" for="nb-name">Display name</label>
                <input
                  id="nb-name"
                  class="s-input"
                  type="text"
                  data-testid="input-new-backend-name"
                  placeholder="Team Claude"
                  [value]="newBackendName()"
                  (input)="newBackendName.set($any($event.target).value)"
                />
              </div>
              <div class="s-field">
                <span class="s-label">Framework</span>
                <g-pill-select
                  testid="select-new-backend-fw"
                  ariaLabel="Backend framework"
                  align="left"
                  [options]="frameworkOptions"
                  [value]="newBackendFw()"
                  (valueChange)="newBackendFw.set($event)"
                />
              </div>
              <div class="s-field">
                <span class="s-label">Auth mode</span>
                <g-pill-select
                  testid="select-new-backend-auth"
                  ariaLabel="Backend auth mode"
                  align="left"
                  [options]="authModeOptions"
                  [value]="newBackendAuthMode()"
                  (valueChange)="newBackendAuthMode.set($event)"
                />
              </div>
              <div class="s-field">
                <label class="s-label" for="nb-models"
                  >Pinned models (comma-separated)</label
                >
                <input
                  id="nb-models"
                  class="s-input s-mono"
                  type="text"
                  data-testid="input-new-backend-models"
                  placeholder="empty = discover all"
                  [value]="newBackendModels()"
                  (input)="newBackendModels.set($any($event.target).value)"
                />
              </div>
              <div class="s-field">
                <label class="s-label" for="nb-hostd"
                  >Hostd name or label selector</label
                >
                <input
                  id="nb-hostd"
                  class="s-input s-mono"
                  type="text"
                  data-testid="input-new-backend-hostd"
                  placeholder="workstation-01 or env=dev"
                  [value]="newBackendHostd()"
                  (input)="newBackendHostd.set($any($event.target).value)"
                />
              </div>
              <div class="s-field">
                <label class="s-label" for="nb-clipath"
                  >CLI path override</label
                >
                <input
                  id="nb-clipath"
                  class="s-input s-mono"
                  type="text"
                  data-testid="input-new-backend-clipath"
                  placeholder="/usr/local/bin/agy"
                  [value]="newBackendCliPath()"
                  (input)="newBackendCliPath.set($any($event.target).value)"
                />
              </div>
              <div class="s-field">
                <label class="s-label" for="nb-conc"
                  >Concurrency limit (0 = default)</label
                >
                <input
                  id="nb-conc"
                  class="s-input s-num"
                  type="number"
                  min="0"
                  data-testid="input-new-backend-concurrency"
                  [value]="newBackendConcurrency()"
                  (input)="
                    newBackendConcurrency.set(Number($any($event.target).value))
                  "
                />
              </div>
            </div>
            <div class="s-inline">
              <button
                type="button"
                class="btn-tonal"
                data-testid="add-model-backend"
                [disabled]="
                  !newBackendId().trim() ||
                  !access.can('PUT /api/deployment/agent-backends/{backendId}')
                "
                (click)="saveBackend()"
              >
                Save model connection
              </button>
            </div>
          </div>

          @if (discoveredModels().length > 0) {
            <div class="s-field">
              <span class="s-label"
                >Discovered models ({{ discoveredModels().length }})</span
              >
              <div class="s-inline" data-testid="discovered-models-list">
                @for (m of discoveredModels(); track m) {
                  <span class="status-pill neutral s-mono">{{ m }}</span>
                }
              </div>
            </div>
          }
        </div>

        <div class="s-card" data-testid="card-token-budget">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Token budget &amp; usage guardrails</h3>
              <p class="s-card-desc">
                Governor mode, worker floor, retention days, and per-connection
                token budgets (PUT /api/usage/settings).
              </p>
            </div>
            <button
              type="button"
              class="btn-primary s-btn-sm"
              data-testid="save-token-budget"
              [disabled]="!access.can('PUT /api/usage/settings')"
              (click)="saveUsageSettings()"
            >
              Save usage settings
            </button>
          </div>

          <div class="s-grid">
            <div class="s-field">
              <span class="s-label">Budget enforcement mode</span>
              <g-pill-select
                testid="select-usage-mode"
                ariaLabel="Budget enforcement mode"
                align="left"
                [options]="usageModeOptions"
                [value]="usageMode()"
                [disabled]="!access.can('PUT /api/usage/settings')"
                (valueChange)="usageMode.set($event)"
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="us-min-workers"
                >Minimum workers floor</label
              >
              <input
                id="us-min-workers"
                class="s-input s-num"
                type="number"
                min="1"
                data-testid="input-usage-min-workers"
                [value]="usageMinWorkers()"
                [disabled]="!access.can('PUT /api/usage/settings')"
                (input)="usageMinWorkers.set(Number($any($event.target).value))"
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="us-retention"
                >Usage retention (days)</label
              >
              <input
                id="us-retention"
                class="s-input s-num"
                type="number"
                min="1"
                data-testid="input-usage-retention-days"
                [value]="usageRetentionDays()"
                [disabled]="!access.can('PUT /api/usage/settings')"
                (input)="
                  usageRetentionDays.set(Number($any($event.target).value))
                "
              />
            </div>
          </div>

          <div class="s-subsection">
            <div class="s-subhead">
              <h4 class="s-subhead-title">Per-connection token budgets</h4>
            </div>
            @if (usageBudgets().length > 0) {
              <div class="s-table-wrap">
                <table class="s-table" aria-label="Token budgets">
                  <thead>
                    <tr>
                      <th>Connection</th>
                      <th>Model</th>
                      <th>Tokens</th>
                      <th>Window</th>
                      <th class="s-actions-cell">Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    @for (b of usageBudgets(); track $index; let idx = $index) {
                      <tr [attr.data-testid]="'usage-budget-row-' + idx">
                        <td class="s-mono">{{ b.connection }}</td>
                        <td class="s-mono">{{ b.model || "*" }}</td>
                        <td class="s-num">{{ b.tokens }}</td>
                        <td class="s-num">{{ b.window }}</td>
                        <td class="s-actions-cell">
                          <div class="s-row-actions">
                            <button
                              type="button"
                              class="btn-text s-btn-sm"
                              [disabled]="
                                !access.can('PUT /api/usage/settings')
                              "
                              (click)="removeUsageBudget(idx)"
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

            <div class="s-inline">
              <input
                class="s-input s-input-inline s-mono"
                type="text"
                aria-label="Budget connection ID"
                data-testid="input-budget-connection"
                placeholder="connection-id"
                [value]="newBudgetConn()"
                [disabled]="!access.can('PUT /api/usage/settings')"
                (input)="newBudgetConn.set($any($event.target).value)"
              />
              <g-model-picker
                class="mp-inline"
                testid="picker-budget-model"
                ariaLabel="Budget model (optional)"
                placeholder="model (optional)"
                [groups]="modelGroups()"
                [value]="newBudgetModel()"
                [disabled]="!access.can('PUT /api/usage/settings')"
                (valueChange)="newBudgetModel.set($event)"
              />
              <input
                class="s-input s-input-inline s-num"
                type="number"
                min="1"
                aria-label="Token limit"
                data-testid="input-budget-tokens"
                placeholder="1000000"
                [value]="newBudgetTokens()"
                [disabled]="!access.can('PUT /api/usage/settings')"
                (input)="newBudgetTokens.set($any($event.target).value)"
              />
              <input
                class="s-input s-input-inline s-num"
                type="text"
                aria-label="Budget window duration"
                data-testid="input-budget-window"
                placeholder="24h"
                [value]="newBudgetWindow()"
                [disabled]="!access.can('PUT /api/usage/settings')"
                (input)="newBudgetWindow.set($any($event.target).value)"
              />
              <button
                type="button"
                class="btn-tonal s-btn-sm"
                data-testid="add-usage-budget-btn"
                [disabled]="
                  !newBudgetConn().trim() ||
                  !(Number(newBudgetTokens()) > 0) ||
                  !newBudgetWindow().trim() ||
                  !access.can('PUT /api/usage/settings')
                "
                (click)="addUsageBudget()"
              >
                Add budget
              </button>
              @if (usageSummary()) {
                <span class="s-hint s-num">{{ usageSummary() }}</span>
              }
            </div>
          </div>
        </div>
      }

      @case ("sandboxes") {
        <div class="s-card" data-testid="card-sandbox-sizing">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Sandbox backend &amp; sizing</h3>
              <p class="s-card-desc">
                Controls isolated task execution environments and per-sandbox
                compute limits.
              </p>
            </div>
            @if (access.can("POST /api/sandbox-probe")) {
              <button
                type="button"
                class="btn-tonal s-btn-sm"
                data-testid="run-sandbox-probe"
                (click)="runSandboxProbe()"
              >
                Probe sandbox runtime
              </button>
            }
          </div>
          @if (probeMessage()) {
            <div class="s-banner ok" data-testid="sandbox-probe-banner">
              {{ probeMessage() }}
            </div>
          }
          <div class="s-grid">
            <div class="s-field">
              <span class="s-label">Sandbox backend</span>
              <g-pill-select
                testid="select-sandbox-backend"
                ariaLabel="Sandbox backend"
                align="left"
                [options]="sandboxBackendOptions"
                [value]="state.val('sandboxBackend', 'granule')"
                [disabled]="!canWriteSettings()"
                (valueChange)="state.setField('sandboxBackend', $event)"
              />
            </div>
            <div class="s-field">
              <span class="s-label">Network access</span>
              <g-pill-select
                testid="select-sandbox-network"
                ariaLabel="Sandbox network access"
                align="left"
                [options]="networkOptions"
                [value]="state.val('networkAccess', 'egress')"
                [disabled]="!canWriteSettings()"
                (valueChange)="state.setField('networkAccess', $event)"
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="sb-cpus">vCPUs per sandbox</label>
              <input
                id="sb-cpus"
                class="s-input s-num"
                type="number"
                min="0"
                data-testid="input-sandbox-cpus"
                [value]="state.val('sandboxCpus', 4)"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField(
                    'sandboxCpus',
                    Number($any($event.target).value)
                  )
                "
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="sb-mem">Memory (MiB)</label>
              <input
                id="sb-mem"
                class="s-input s-num"
                type="number"
                min="0"
                data-testid="input-sandbox-memory"
                [value]="state.val('sandboxMemoryMb', 8192)"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField(
                    'sandboxMemoryMb',
                    Number($any($event.target).value)
                  )
                "
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="sb-disk">Disk (GiB)</label>
              <input
                id="sb-disk"
                class="s-input s-num"
                type="number"
                min="0"
                data-testid="input-sandbox-disk"
                [value]="state.val('sandboxDiskGb', 32)"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField(
                    'sandboxDiskGb',
                    Number($any($event.target).value)
                  )
                "
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="sb-ttl"
                >Session sandbox idle TTL</label
              >
              <input
                id="sb-ttl"
                class="s-input s-num"
                type="text"
                placeholder="30m"
                [value]="state.val('sessionSandboxIdleTtl', '')"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField(
                    'sessionSandboxIdleTtl',
                    $any($event.target).value
                  )
                "
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="sb-image"
                >Custom OCI sandbox image</label
              >
              <input
                id="sb-image"
                class="s-input s-mono"
                type="text"
                [value]="state.val('sandboxImage', '')"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField('sandboxImage', $any($event.target).value)
                "
              />
            </div>
          </div>
        </div>

        <div class="s-card" data-testid="card-sandbox-overcommit">
          <h3 class="s-card-title">Overcommit &amp; Kubernetes pod settings</h3>
          <div class="s-grid">
            <div class="s-field">
              <span class="s-label">Overcommit governor</span>
              <g-pill-select
                testid="select-overcommit-mode"
                ariaLabel="Overcommit governor"
                align="left"
                [options]="overcommitOptions"
                [value]="overcommitMode()"
                [disabled]="!canWriteSettings()"
                (valueChange)="setOvercommitMode($event)"
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="sb-cpu-oc">CPU overcommit (x)</label>
              <input
                id="sb-cpu-oc"
                class="s-input s-num"
                type="number"
                min="0"
                step="0.25"
                [value]="state.val('sandboxCpuOvercommit', 2)"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField(
                    'sandboxCpuOvercommit',
                    Number($any($event.target).value)
                  )
                "
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="sb-mem-oc"
                >Memory overcommit (x)</label
              >
              <input
                id="sb-mem-oc"
                class="s-input s-num"
                type="number"
                min="0"
                step="0.25"
                [value]="state.val('sandboxMemoryOvercommit', 1)"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField(
                    'sandboxMemoryOvercommit',
                    Number($any($event.target).value)
                  )
                "
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="k8s-ns">Kubernetes namespace</label>
              <input
                id="k8s-ns"
                class="s-input s-mono"
                type="text"
                [value]="state.val('k8sNamespace', '')"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField('k8sNamespace', $any($event.target).value)
                "
              />
            </div>
          </div>
          <div class="s-toggle-row">
            <div class="s-toggle-copy">
              <span class="s-toggle-name"
                >Cluster CNI enforces NetworkPolicy</span
              >
              <span class="s-hint"
                >Required for network: none isolation on Kubernetes pods.</span
              >
            </div>
            <button
              type="button"
              class="g-switch"
              role="switch"
              data-testid="switch-k8s-netpol"
              [attr.aria-checked]="
                Boolean(state.val('k8sNetworkPolicy', false))
              "
              [disabled]="!canWriteSettings()"
              aria-label="Cluster CNI enforces NetworkPolicy"
              (click)="
                state.setField(
                  'k8sNetworkPolicy',
                  !state.val('k8sNetworkPolicy', false)
                )
              "
            ></button>
          </div>
        </div>

        @if (persistentSandboxes().length > 0) {
          <div class="s-card" data-testid="card-persistent-sandboxes">
            <h3 class="s-card-title">Persistent sandboxes</h3>
            <div class="s-table-wrap">
              <table class="s-table" aria-label="Persistent sandboxes">
                <thead>
                  <tr>
                    <th>Name</th>
                    <th>State</th>
                    <th class="s-actions-cell">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  @for (ps of persistentSandboxes(); track ps.name) {
                    <tr>
                      <td class="s-mono">{{ ps.name }}</td>
                      <td>
                        <span class="status-pill neutral">{{
                          ps.state || "ready"
                        }}</span>
                      </td>
                      <td class="s-actions-cell">
                        <div class="s-row-actions">
                          @for (
                            act of ["start", "restart", "rebuild", "stop"];
                            track act
                          ) {
                            <button
                              type="button"
                              class="btn-tonal s-btn-sm"
                              [disabled]="
                                !access.can(
                                  'POST /api/persistent-sandboxes/{name}/{action}'
                                )
                              "
                              (click)="actPersistentSandbox(ps.name, act)"
                            >
                              {{ act }}
                            </button>
                          }
                        </div>
                      </td>
                    </tr>
                  }
                </tbody>
              </table>
            </div>
          </div>
        }
      }

      @case ("hostds") {
        <div class="s-card" data-testid="card-hostds-list">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">
                Registered workstation hosts (hostds)
              </h3>
              <p class="s-card-desc">
                grain-agentd daemons paired with this controller for local CLI
                tokens and workstation execution.
              </p>
            </div>
            @if (access.can("POST /api/hostds/delete-revoked")) {
              <button
                type="button"
                class="btn-text s-btn-sm"
                data-testid="prune-revoked-hostds"
                (click)="deleteRevokedHostds()"
              >
                Delete revoked
              </button>
            }
          </div>

          @if (!access.can("GET /api/hostds")) {
            <div class="s-banner warn">
              {{ access.deniedReason("GET /api/hostds") }}
            </div>
          } @else if (hostds().length === 0) {
            <p class="s-card-desc">No hostds registered yet.</p>
          } @else {
            <div class="s-table-wrap">
              <table class="s-table" aria-label="Registered hostds">
                <thead>
                  <tr>
                    <th>Name</th>
                    <th>Owner</th>
                    <th>Status</th>
                    <th>Labels</th>
                    <th class="s-actions-cell">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  @for (h of hostds(); track h.name) {
                    @let isRevoked =
                      Boolean(h.revokedAt || h.revoked_at) ||
                      h.state === "revoked";
                    <tr [attr.data-testid]="'hostd-row-' + h.name">
                      <td class="s-mono">{{ h.name }}</td>
                      <td>{{ h.owner || "—" }}</td>
                      <td>
                        <span
                          [class]="
                            'status-pill ' +
                            (isRevoked
                              ? 'error'
                              : h.state === 'connected'
                                ? 'healthy'
                                : 'neutral')
                          "
                        >
                          {{ isRevoked ? "revoked" : h.state || "offline" }}
                        </span>
                      </td>
                      <td class="s-mono">
                        {{ formatLabels(h.labels) }}
                      </td>
                      <td class="s-actions-cell">
                        <!-- local is the co-located host: it holds no
                             token to rotate and can be neither revoked
                             nor deleted, so it offers none of the three
                             (the server refuses each with a 400). -->
                        @if (h.name !== "local") {
                          <div class="s-row-actions">
                            <button
                              type="button"
                              class="btn-tonal s-btn-sm"
                              [attr.data-testid]="'rotate-hostd-' + h.name"
                              [disabled]="
                                !access.can('POST /api/hostds/{name}/rotate') ||
                                isRevoked
                              "
                              (click)="rotateHostd(h.name)"
                            >
                              Rotate
                            </button>
                            <button
                              type="button"
                              class="btn-tonal s-btn-sm"
                              [attr.data-testid]="'revoke-hostd-' + h.name"
                              [disabled]="
                                !access.can('POST /api/hostds/{name}/revoke') ||
                                isRevoked
                              "
                              (click)="revokeHostd(h.name)"
                            >
                              Revoke
                            </button>
                            <button
                              type="button"
                              class="btn-text s-btn-sm"
                              [attr.data-testid]="'delete-hostd-' + h.name"
                              [disabled]="
                                !access.can('DELETE /api/hostds/{name}') ||
                                !isRevoked
                              "
                              [attr.title]="
                                isRevoked
                                  ? null
                                  : 'Revoke a host before deleting it'
                              "
                              (click)="deleteHostd(h.name)"
                            >
                              Delete
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

          <div class="s-subsection">
            <div class="s-subhead">
              <h4 class="s-subhead-title">Register workstation host</h4>
            </div>
            <div class="s-grid">
              <div class="s-field">
                <label class="s-label" for="hd-name">New hostd name</label>
                <input
                  id="hd-name"
                  class="s-input s-mono"
                  type="text"
                  data-testid="input-new-hostd-name"
                  placeholder="workstation-01"
                  [value]="newHostdName()"
                  [disabled]="!access.can('POST /api/hostds')"
                  (input)="newHostdName.set($any($event.target).value)"
                />
              </div>
              <div class="s-field">
                <label class="s-label" for="hd-labels"
                  >Labels (key=value pairs)</label
                >
                <input
                  id="hd-labels"
                  class="s-input s-mono"
                  type="text"
                  data-testid="input-new-hostd-labels"
                  placeholder="os=linux,gpu=true"
                  [value]="newHostdLabels()"
                  [disabled]="!access.can('POST /api/hostds')"
                  (input)="newHostdLabels.set($any($event.target).value)"
                />
              </div>
            </div>
            <div class="s-inline">
              <button
                type="button"
                class="btn-primary"
                data-testid="create-hostd-btn"
                [disabled]="
                  !newHostdName().trim() || !access.can('POST /api/hostds')
                "
                (click)="createHostd()"
              >
                Register hostd
              </button>
            </div>
          </div>
          @if (mintedHostdToken(); as tok) {
            <div class="s-banner ok" data-testid="minted-hostd-token">
              <pre class="s-code" style="flex: 1">{{ tok }}</pre>
              <button
                type="button"
                class="btn-text s-btn-sm"
                (click)="mintedHostdToken.set(null)"
              >
                Dismiss
              </button>
            </div>
          }
        </div>

        <div class="s-card" data-testid="card-hostds-enrollment">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">
                Operator enrollment codes &amp; pending joins
              </h3>
              <p class="s-card-desc">
                Reusable enrollment codes and pending grain-agentd approval
                queue (operator-only).
              </p>
            </div>
          </div>

          @if (!access.can("GET /api/hostds/enrollment-codes")) {
            <div
              class="s-banner readonly"
              data-testid="hostds-enrollment-restricted"
            >
              <span>{{
                access.deniedReason("GET /api/hostds/enrollment-codes")
              }}</span>
            </div>
          } @else {
            @if (enrollmentCodes().length > 0) {
              <div class="s-table-wrap">
                <table class="s-table" aria-label="Enrollment codes">
                  <thead>
                    <tr>
                      <th>Code name</th>
                      <th>Uses</th>
                      <th>Expires</th>
                      <th class="s-actions-cell">Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    @for (c of enrollmentCodes(); track c.name) {
                      @let codeRevoked = Boolean(c.revoked_at);
                      <tr [attr.data-testid]="'enrollment-code-row-' + c.name">
                        <td class="s-mono">{{ c.name }}</td>
                        <td class="s-num">
                          {{ c.uses ?? 0 }} / {{ c.max_uses || "∞" }}
                        </td>
                        <td class="s-num">
                          {{
                            codeRevoked ? "revoked" : c.expires_at || "never"
                          }}
                        </td>
                        <td class="s-actions-cell">
                          <div class="s-row-actions">
                            <button
                              type="button"
                              class="btn-tonal s-btn-sm"
                              [disabled]="
                                !access.can(
                                  'POST /api/hostds/enrollment-codes/{name}/rotate'
                                ) || codeRevoked
                              "
                              (click)="rotateEnrollmentCode(c.name)"
                            >
                              Rotate
                            </button>
                            <button
                              type="button"
                              class="btn-text s-btn-sm"
                              [disabled]="
                                !access.can(
                                  'POST /api/hostds/enrollment-codes/{name}/revoke'
                                ) || codeRevoked
                              "
                              (click)="revokeEnrollmentCode(c.name)"
                            >
                              Revoke
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
                class="s-input s-input-inline s-mono"
                type="text"
                aria-label="Enrollment code name"
                data-testid="input-enrollment-code-name"
                placeholder="team-workstations"
                [value]="newCodeName()"
                [disabled]="!access.can('POST /api/hostds/enrollment-codes')"
                (input)="newCodeName.set($any($event.target).value)"
              />
              <button
                type="button"
                class="btn-tonal"
                data-testid="create-enrollment-code-btn"
                [disabled]="
                  !newCodeName().trim() ||
                  !access.can('POST /api/hostds/enrollment-codes')
                "
                (click)="createEnrollmentCode()"
              >
                Create enrollment code
              </button>
            </div>
          }

          @if (!access.can("GET /api/hostds/pending")) {
            <div
              class="s-banner readonly"
              data-testid="hostds-pending-restricted"
            >
              <span>{{ access.deniedReason("GET /api/hostds/pending") }}</span>
            </div>
          } @else if (pendingHostds().length > 0) {
            <div class="s-table-wrap" data-testid="hostds-pending-table">
              <table class="s-table" aria-label="Pending hostd joins">
                <thead>
                  <tr>
                    <th>ID / Hostname</th>
                    <th>Key fingerprint</th>
                    <th>Enrolled by</th>
                    <th>Platform</th>
                    <th>Approve / Reject</th>
                  </tr>
                </thead>
                <tbody>
                  @for (p of pendingHostds(); track p.id) {
                    <tr [attr.data-testid]="'pending-hostd-row-' + p.id">
                      <td class="s-mono">
                        {{ p.suggested_name || p.hostname || p.id }}
                      </td>
                      <!-- There is no pairing code to compare: the owner
                           checks who enrolled the host and the key it
                           will be bound to (docs/host-placement.md). -->
                      <td
                        class="s-fingerprint"
                        [attr.data-testid]="'pending-key-fingerprint-' + p.id"
                      >
                        {{ p.key_fingerprint || "—" }}
                      </td>
                      <td>{{ p.enrolled_by || "—" }}</td>
                      <td class="s-mono">
                        {{ p.os || "?" }}/{{ p.arch || "?" }}
                      </td>
                      <td>
                        <div class="s-inline">
                          <input
                            class="s-input s-input-inline s-mono"
                            type="text"
                            [attr.aria-label]="
                              'Labels for ' + (p.hostname || p.id)
                            "
                            [attr.data-testid]="'pending-labels-' + p.id"
                            placeholder="k=v (optional)"
                            [value]="
                              pendingLabelDrafts()[p.id] ||
                              formatLabelsInput(p.default_labels)
                            "
                            (input)="
                              setPendingLabelDraft(
                                p.id,
                                $any($event.target).value
                              )
                            "
                          />
                          <button
                            type="button"
                            class="btn-primary s-btn-sm"
                            [attr.data-testid]="'accept-pending-' + p.id"
                            [disabled]="
                              !access.can(
                                'POST /api/hostds/pending/{id}/accept'
                              )
                            "
                            (click)="acceptPendingHostd(p)"
                          >
                            Accept
                          </button>
                          <button
                            type="button"
                            class="btn-tonal s-btn-sm"
                            [attr.data-testid]="'reject-pending-' + p.id"
                            [disabled]="
                              !access.can(
                                'POST /api/hostds/pending/{id}/reject'
                              )
                            "
                            (click)="rejectPendingHostd(p)"
                          >
                            Reject
                          </button>
                        </div>
                      </td>
                    </tr>
                  }
                </tbody>
              </table>
            </div>
          }
        </div>
      }
    }

    @if (confirmRequest(); as req) {
      <g-confirm-dialog [request]="req" (done)="onConfirmDone($event)" />
    }
  `,
})
export class SettingsComputeComponent implements OnInit, OnChanges {
  protected readonly api = inject(GrainApi);
  protected readonly access = inject(AccessService);
  protected readonly state = inject(SettingsStateService);
  private readonly snackbar = inject(SnackbarService);

  readonly section = input.required<SettingsSectionId>();

  protected readonly Number = Number;
  protected readonly Boolean = Boolean;
  protected readonly formatLabels = formatHostdLabels;
  protected readonly formatRefreshedAt = formatRefreshedAt;
  protected readonly frameworks = ["antigravity", "claude", "codex"] as const;

  protected readonly frameworkOptions: readonly PillOption[] = [
    { value: "antigravity", label: "Antigravity (Gemini)" },
    { value: "claude", label: "Claude" },
    { value: "codex", label: "Codex" },
  ];
  protected readonly effortOptions: readonly PillOption[] = [
    { value: "", label: "Framework default" },
    { value: "low", label: "Low" },
    { value: "medium", label: "Medium" },
    { value: "high", label: "High" },
  ];
  protected readonly authModeOptions: readonly PillOption[] = [
    { value: "", label: "Provider default" },
    { value: "api_key", label: "API key / OAuth secret" },
    { value: "ambient", label: "Host ambient login" },
    { value: "vertex", label: "Vertex AI" },
  ];
  protected readonly usageModeOptions: readonly PillOption[] = [
    { value: "advisory", label: "Advisory" },
    { value: "auto", label: "Auto throttle" },
    { value: "off", label: "Off" },
  ];
  protected readonly sandboxBackendOptions: readonly PillOption[] = [
    { value: "granule", label: "Granule microVM" },
    { value: "docker", label: "Docker container" },
    { value: "podman", label: "Podman container" },
    { value: "kubernetes", label: "Kubernetes pod" },
    { value: "local", label: "Local host directory" },
  ];
  protected readonly networkOptions: readonly PillOption[] = [
    { value: "none", label: "None (isolated)" },
    { value: "egress", label: "Filtered egress" },
    { value: "full", label: "Full network" },
  ];
  protected readonly overcommitOptions: readonly PillOption[] = [
    { value: "fixed", label: "Fixed multiplier" },
    { value: "auto", label: "Pressure-governed auto" },
    { value: "off", label: "Disabled (1x)" },
  ];

  protected readonly agentKeysEnabled = signal(true);
  protected readonly agentKeys = signal<Record<string, boolean>>({});
  protected readonly codexAuthJsonSet = signal(false);
  protected readonly codexAuthRefreshError = signal("");
  protected readonly keyDrafts = signal<Record<string, string>>({});
  protected readonly keyChecks = signal<Record<string, KeyCheckResult>>({});
  protected readonly busyKey = signal<string | null>(null);
  protected readonly openKeyFramework = signal<string | null>(null);
  protected readonly codexAuthDraft = signal("");

  protected readonly newModelSettingName = signal("");
  protected readonly newModelSettingEffort = signal("high");

  protected readonly backends = signal<BackendEntry[]>([]);
  protected readonly backendCredDrafts = signal<Record<string, string>>({});
  protected readonly openCredBackendId = signal<string | null>(null);
  protected readonly newBackendId = signal("");
  protected readonly newBackendName = signal("");
  protected readonly newBackendFw = signal("antigravity");
  protected readonly editingBackendId = signal("");
  protected readonly newBackendAuthMode = signal("");

  protected readonly modelsConfigured = computed(
    () => this.backends().length > 0,
  );
  protected readonly newBackendModels = signal("");
  protected readonly newBackendHostd = signal("");
  protected readonly newBackendCliPath = signal("");
  protected readonly newBackendConcurrency = signal(0);
  protected readonly discoveredModels = signal<string[]>([]);

  protected readonly usageMode = signal("advisory");
  protected readonly usageMinWorkers = signal(1);
  protected readonly usageRetentionDays = signal(30);
  protected readonly usageBudgets = signal<TokenBudgetRow[]>([]);
  protected readonly newBudgetConn = signal("");
  protected readonly newBudgetModel = signal("");
  protected readonly newBudgetTokens = signal("");
  protected readonly newBudgetWindow = signal("24h");
  protected readonly usageSummary = signal("");

  protected readonly persistentSandboxes = signal<
    { name: string; state?: string }[]
  >([]);
  protected readonly probeMessage = signal("");

  protected readonly hostds = signal<HostdRow[]>([]);
  protected readonly confirmRequest = signal<ConfirmRequest | null>(null);
  protected readonly newHostdName = signal("");
  protected readonly newHostdLabels = signal("");
  protected readonly mintedHostdToken = signal<string | null>(null);
  protected readonly enrollmentCodes = signal<EnrollmentCodeRow[]>([]);
  protected readonly newCodeName = signal("");
  protected readonly pendingHostds = signal<PendingHostdRow[]>([]);
  protected readonly pendingLabelDrafts = signal<Record<string, string>>({});

  // The deployment's default model list (model.Config.DefaultModels):
  // the default first, then its fallbacks, tried in that order. Settings
  // and the deployment record are two views of one grain_config column, so
  // a staged edit on either wins over both stored values.
  protected readonly modelChain = computed<string[]>(() => {
    const dep = this.state.deployDraft();
    if ("defaultModels" in dep) return parseDefaultModels(dep["defaultModels"]);
    const draft = this.state.draft();
    if ("defaultModel" in draft)
      return parseDefaultModels(draft["defaultModel"]);
    const stored = this.state.settings()["defaultModel"];
    if (typeof stored === "string") return parseDefaultModels(stored);
    const d = this.state.deployment();
    return parseDefaultModels(d["defaultModels"] ?? d["defaultModel"]);
  });
  protected readonly fallbackModels = computed(() =>
    this.modelChain().slice(1),
  );
  protected readonly modelGroups = computed(() => modelGroups(this.backends()));
  protected readonly refreshingModels = signal(false);
  protected readonly refreshNote = signal("");

  protected readonly chainWarning = computed(() => {
    const chain = this.modelChain();
    const groups = this.modelGroups();
    if (!chain.length || !groups.length) return "";
    if (chain.some((m) => modelAvailability(m, groups) === "enabled")) {
      return "";
    }
    return chain.length === 1
      ? `No enabled connection advertises ${chain[0]}. Tasks on the default model wait in the queue until one does.`
      : `No enabled connection advertises any default or fallback model (${chain.join(", ")}). Tasks on the default model wait in the queue until one does.`;
  });

  ngOnInit(): void {
    this.loadForSection();
  }

  ngOnChanges(): void {
    this.loadForSection();
  }

  protected canWriteSettings(): boolean {
    return (
      this.access.can("POST /api/settings") &&
      !this.state.configRepoOnly() &&
      !this.state.sectionLocked(this.section())
    );
  }

  protected canWriteDeployment(): boolean {
    return (
      this.access.can("PUT /api/deployment") &&
      !this.state.configRepoOnly() &&
      !this.state.sectionLocked(this.section())
    );
  }

  protected canEditChain(): boolean {
    return this.canWriteSettings() || this.canWriteDeployment();
  }

  protected canRefreshModels(): boolean {
    return this.access.can(
      "POST /api/deployment/agent-backends/{backendId}/discover",
    );
  }

  protected modelNote(m: string): string {
    return availabilityNote(modelAvailability(m, this.modelGroups()));
  }

  // setModelChain stages the whole list through whichever write this
  // person has: POST /api/settings takes it comma-joined (defaultModel),
  // PUT /api/deployment as a list (defaultModels). Both normalise the same
  // way (model.NormalizeDefaultModel).
  private setModelChain(list: string[]): void {
    const next = parseDefaultModels(list);
    if (this.canWriteSettings()) {
      this.state.setField("defaultModel", next.join(","));
    } else if (this.canWriteDeployment()) {
      this.state.setDeployField("defaultModels", next);
    }
  }

  protected setDefaultModel(m: string): void {
    const rest = this.fallbackModels().filter(
      (x) => x.toLowerCase() !== m.toLowerCase(),
    );
    this.setModelChain(m ? [m, ...rest] : rest);
  }

  protected addFallback(m: string): void {
    const chain = this.modelChain();
    if (!m || !chain.length || containsModel(chain, m)) return;
    this.setModelChain([...chain, m]);
  }

  protected moveFallback(idx: number, delta: -1 | 1): void {
    const fb = [...this.fallbackModels()];
    const to = idx + delta;
    if (to < 0 || to >= fb.length) return;
    [fb[idx], fb[to]] = [fb[to], fb[idx]];
    this.setModelChain([this.modelChain()[0], ...fb]);
  }

  protected removeFallback(idx: number): void {
    const fb = this.fallbackModels().filter((_, i) => i !== idx);
    this.setModelChain([this.modelChain()[0], ...fb]);
  }

  protected anyDiscovers(): boolean {
    return this.backends().some((b) => this.discovers(b));
  }

  protected discovers(b: BackendEntry): boolean {
    return !(b.models ?? []).some((m) => m.trim());
  }

  // refreshDiscoveredModels asks providers which models they serve now
  // (POST .../discover): one connection, or every connection that
  // advertises what its provider reports.
  protected async refreshDiscoveredModels(only?: string): Promise<void> {
    const ids = this.backends()
      .filter((b) => (only ? b.id === only : this.discovers(b)))
      .map((b) => b.id);
    if (!ids.length || !this.canRefreshModels()) return;
    this.refreshingModels.set(true);
    this.refreshNote.set("");
    const problems: string[] = [];
    let asked = 0;
    for (const backendId of ids) {
      try {
        const res = await this.api.post<{
          backend?: BackendEntry;
          asked?: boolean;
          error?: string;
        }>("POST /api/deployment/agent-backends/{backendId}/discover", {
          backendId,
        });
        if (res?.backend) {
          const fresh = res.backend;
          this.backends.update((list) =>
            list.map((b) => (b.id === fresh.id ? { ...b, ...fresh } : b)),
          );
        }
        if (res?.asked) asked++;
        if (res?.error) problems.push(`${backendId}: ${res.error}`);
      } catch (err) {
        problems.push(`${backendId}: ${errText(err)}`);
      }
    }
    this.refreshingModels.set(false);
    const note =
      (asked
        ? `Refreshed ${asked} connection${asked === 1 ? "" : "s"}.`
        : "No provider was asked.") +
      (problems.length ? ` ${problems.join("; ")}` : "");
    this.refreshNote.set(note);
    this.snackbar.show(note);
  }

  protected modelSettingsRows(): ModelSettingRow[] {
    const v = this.state.deployVal<unknown>("modelSettings", []);
    return Array.isArray(v) ? (v as ModelSettingRow[]) : [];
  }

  protected addModelSetting(): void {
    const model = this.newModelSettingName().trim();
    if (!model) return;
    const effort = this.newModelSettingEffort().trim();
    const cur = this.modelSettingsRows().filter((r) => r.model !== model);
    const next: ModelSettingRow[] = [
      ...cur,
      { model, ...(effort ? { effort } : {}) },
    ];
    this.state.setDeployField("modelSettings", next);
    this.newModelSettingName.set("");
  }

  protected removeModelSetting(idx: number): void {
    const next = this.modelSettingsRows().filter((_, i) => i !== idx);
    this.state.setDeployField("modelSettings", next);
  }

  private applyUsageSettings(s?: UsageSettingsDto | null): void {
    if (!s) return;
    this.usageMode.set(s.mode || "advisory");
    this.usageMinWorkers.set(
      s.minWorkers && s.minWorkers > 0 ? s.minWorkers : 1,
    );
    this.usageRetentionDays.set(
      s.retentionDays && s.retentionDays > 0 ? s.retentionDays : 30,
    );
    this.usageBudgets.set(Array.isArray(s.budgets) ? [...s.budgets] : []);
  }

  private loadForSection(): void {
    const sec = this.section();
    if (sec === "models") {
      this.syncBackendsFromState();
      if (this.access.can("GET /api/agent-keys")) {
        void this.loadAgentKeys();
      }
      if (this.access.can("GET /api/usage")) {
        void this.api
          .get<Record<string, unknown>>("GET /api/usage")
          .then((u) => {
            if (!u) return;
            this.applyUsageSettings(
              u["settings"] as UsageSettingsDto | undefined,
            );
            const used = u["billable"] ?? u["tokensToday"] ?? u["totalTokens"];
            if (used !== undefined) {
              this.usageSummary.set(`Reported billable: ${used} tokens`);
            }
          })
          .catch(() => undefined);
      }
    } else if (sec === "sandboxes") {
      if (this.access.can("GET /api/persistent-sandboxes")) {
        void this.api
          .get<
            | { name: string; state?: string }[]
            | { sandboxes?: { name: string; state?: string }[] }
          >("GET /api/persistent-sandboxes")
          .then((r) => {
            const list = Array.isArray(r) ? r : (r?.sandboxes ?? []);
            this.persistentSandboxes.set(list);
          })
          .catch(() => undefined);
      }
    } else if (sec === "hostds") {
      void this.loadHostds();
    }
  }

  private syncBackendsFromState(): void {
    const raw =
      this.state.settings()["agentBackends"] ??
      this.state.deployment()["agentBackends"];
    if (Array.isArray(raw)) {
      this.backends.set(raw as BackendEntry[]);
    }
  }

  private applyAgentKeysResponse(
    res: AgentKeysResponse | null | undefined,
  ): void {
    if (!res || typeof res !== "object") return;
    if (res.enabled !== undefined) {
      this.agentKeysEnabled.set(Boolean(res.enabled));
    }
    this.agentKeys.set({
      antigravity: Boolean(res.geminiApiKeySet),
      gemini: Boolean(res.geminiApiKeySet),
      claude: Boolean(res.claudeOAuthTokenSet),
      codex: Boolean(res.openaiApiKeySet),
    });
    this.codexAuthJsonSet.set(Boolean(res.codexAuthJSONSet));
    this.codexAuthRefreshError.set(res.codexAuthRefreshError ?? "");
  }

  protected async loadAgentKeys(): Promise<void> {
    try {
      const res = await this.api.get<AgentKeysResponse>("GET /api/agent-keys");
      this.applyAgentKeysResponse(res);
    } catch {
      // Non-fatal
    }
  }

  protected isKeyConfigured(fw: string): boolean {
    return Boolean(this.agentKeys()[fw]);
  }

  protected setKeyDraft(fw: string, val: string): void {
    this.keyDrafts.update((p) => ({ ...p, [fw]: val }));
  }

  protected toggleKeyEditor(fw: string): void {
    this.openKeyFramework.update((cur) => (cur === fw ? null : fw));
  }

  protected async saveAgentKey(fw: string): Promise<void> {
    const value = (this.keyDrafts()[fw] ?? "").trim();
    if (!value) return;
    this.busyKey.set(fw);
    try {
      const res = await this.api.put<AgentKeysResponse>(
        "PUT /api/agent-keys/{framework}",
        { framework: fw },
        { value },
      );
      this.setKeyDraft(fw, "");
      this.openKeyFramework.set(null);
      if (res && "geminiApiKeySet" in res) {
        this.applyAgentKeysResponse(res);
      } else {
        this.agentKeys.update((p) => ({ ...p, [fw]: true }));
      }
      this.snackbar.show(`Saved ${fw} API key`);
    } catch (err) {
      this.state.recordError(err);
    } finally {
      this.busyKey.set(null);
    }
  }

  protected async clearAgentKey(fw: string): Promise<void> {
    this.busyKey.set(fw);
    try {
      const res = await this.api.delete<AgentKeysResponse>(
        "DELETE /api/agent-keys/{framework}",
        { framework: fw },
      );
      if (res && "geminiApiKeySet" in res) {
        this.applyAgentKeysResponse(res);
      } else {
        this.agentKeys.update((p) => ({ ...p, [fw]: false }));
      }
      this.snackbar.show(`Cleared ${fw} API key`);
    } catch (err) {
      this.state.recordError(err);
    } finally {
      this.busyKey.set(null);
    }
  }

  protected async checkAgentKey(fw: string): Promise<void> {
    this.busyKey.set(fw);
    try {
      const res = await this.api.post<{
        ok?: boolean;
        detail?: string;
        checkedAt?: string;
      }>("POST /api/agent-keys/{framework}/check", { framework: fw }, {});
      this.keyChecks.update((p) => ({
        ...p,
        [fw]: {
          ok: Boolean(res?.ok),
          detail: res?.detail || (res?.ok ? "Key verified" : "Check failed"),
          checkedAt: res?.checkedAt,
        },
      }));
    } catch (err) {
      this.keyChecks.update((p) => ({
        ...p,
        [fw]: { ok: false, detail: errText(err) },
      }));
    } finally {
      this.busyKey.set(null);
    }
  }

  protected async saveCodexAuth(): Promise<void> {
    const value = this.codexAuthDraft().trim();
    if (!value) return;
    try {
      const res = await this.api.put<AgentKeysResponse>(
        "PUT /api/codex-auth",
        undefined,
        { value },
      );
      this.codexAuthDraft.set("");
      if (res && "codexAuthJSONSet" in res) {
        this.applyAgentKeysResponse(res);
      }
      this.snackbar.show("Codex auth saved");
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async clearCodexAuth(): Promise<void> {
    try {
      const res = await this.api.delete<AgentKeysResponse>(
        "DELETE /api/codex-auth",
      );
      if (res && "codexAuthJSONSet" in res) {
        this.applyAgentKeysResponse(res);
      }
      this.snackbar.show("Codex auth cleared");
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected setBackendCredDraft(id: string, val: string): void {
    this.backendCredDrafts.update((p) => ({ ...p, [id]: val }));
  }

  protected async saveBackend(): Promise<void> {
    const backendId = this.newBackendId().trim();
    if (!backendId) return;
    if (
      this.editingBackendId() !== backendId &&
      this.backends().some((b) => b.id === backendId)
    ) {
      this.state.error.set(
        `Connection ${backendId} already exists. To edit it, click Edit on its row.`,
      );
      return;
    }
    const models = this.newBackendModels()
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean);
    const name = this.newBackendName().trim();
    const authMode = this.newBackendAuthMode().trim();
    const hostd = this.newBackendHostd().trim();
    const cliPath = this.newBackendCliPath().trim();
    const concurrency = this.newBackendConcurrency() || 0;
    if (concurrency !== 0 && concurrency < 1) {
      this.state.error.set(
        "Concurrency must be 1 or greater, or blank for unlimited.",
      );
      return;
    }
    const payload: BackendEntry = {
      id: backendId,
      framework: this.newBackendFw(),
      ...(name ? { name } : {}),
      ...(models.length > 0 ? { models } : {}),
      ...(authMode ? { authMode } : {}),
      ...(cliPath ? { cliPath } : {}),
      ...(concurrency > 0 ? { concurrency } : {}),
      ...(hostd ? { hostd } : {}),
    };
    try {
      await this.api.put(
        "PUT /api/deployment/agent-backends/{backendId}",
        { backendId },
        payload,
      );
      this.newBackendId.set("");
      this.newBackendName.set("");
      this.newBackendModels.set("");
      this.newBackendHostd.set("");
      this.newBackendCliPath.set("");
      this.newBackendConcurrency.set(0);
      this.newBackendFw.set("antigravity");
      this.newBackendAuthMode.set("");
      this.editingBackendId.set("");
      await this.state.loadCore();
      this.syncBackendsFromState();
      this.snackbar.show(`Saved model connection ${backendId}`);
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async saveBackendCred(id: string): Promise<void> {
    const value = (this.backendCredDrafts()[id] ?? "").trim();
    if (!value) return;
    try {
      await this.api.put(
        "PUT /api/agent-backends/{id}/credential",
        { id },
        { value },
      );
      this.setBackendCredDraft(id, "");
      this.snackbar.show(`Updated credential for ${id}`);
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async clearBackendCred(id: string): Promise<void> {
    try {
      await this.api.delete("DELETE /api/agent-backends/{id}/credential", {
        id,
      });
      this.setBackendCredDraft(id, "");
      this.snackbar.show(`Cleared credential for ${id}`);
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected startDeleteBackend(backendId: string): void {
    this.confirmRequest.set({
      title: `Remove connection ${backendId}?`,
      message: `Tasks routed to ${backendId} will fall back to remaining connections or wait until another connection advertises their model.`,
      accept: "Remove",
      destructive: true,
      run: () => this.deleteBackend(backendId),
    });
  }

  protected toggleCredEditor(id: string): void {
    this.openCredBackendId.update((cur) => (cur === id ? null : id));
  }

  protected cancelEditBackend(): void {
    this.editingBackendId.set("");
    this.newBackendId.set("");
    this.newBackendName.set("");
    this.newBackendFw.set("antigravity");
    this.newBackendAuthMode.set("");
    this.newBackendHostd.set("");
    this.newBackendCliPath.set("");
    this.newBackendModels.set("");
    this.newBackendConcurrency.set(0);
  }

  protected editBackend(b: BackendEntry): void {
    this.editingBackendId.set(b.id);
    this.newBackendId.set(b.id);
    this.newBackendName.set(b.name || "");
    this.newBackendFw.set(b.framework || "antigravity");
    this.newBackendAuthMode.set(b.authMode || "");
    this.newBackendHostd.set(b.hostd || "");
    this.newBackendCliPath.set(b.cliPath || "");
    this.newBackendModels.set(b.models?.join(", ") || "");
    this.newBackendConcurrency.set(b.concurrency || 0);
  }

  protected async deleteBackend(backendId: string): Promise<void> {
    try {
      await this.api.delete(
        "DELETE /api/deployment/agent-backends/{backendId}",
        { backendId },
      );
      this.backends.update((list) => list.filter((b) => b.id !== backendId));
      this.snackbar.show(`Removed connection ${backendId}`);
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async loadDiscoveredModels(): Promise<void> {
    try {
      const res = await this.api.get<
        | string[]
        | {
            models?: (string | { id?: string; name?: string })[];
            available?: boolean;
            error?: string;
          }
      >("GET /api/agent-models");
      const raw = Array.isArray(res) ? res : (res?.models ?? []);
      this.discoveredModels.set(
        raw
          .map((m) => (typeof m === "string" ? m : (m.id ?? m.name ?? "")))
          .filter(Boolean),
      );
      if (!Array.isArray(res) && res?.error) {
        this.state.error.set(res.error);
      }
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected addUsageBudget(): void {
    const connection = this.newBudgetConn().trim();
    const tokens = Math.floor(Number(this.newBudgetTokens()));
    const window = this.newBudgetWindow().trim();
    const model = this.newBudgetModel().trim();
    if (!connection || !(tokens > 0) || !window) return;
    this.usageBudgets.update((b) => [
      ...b,
      { connection, ...(model ? { model } : {}), tokens, window },
    ]);
    this.newBudgetConn.set("");
    this.newBudgetModel.set("");
    this.newBudgetTokens.set("");
  }

  protected removeUsageBudget(idx: number): void {
    this.usageBudgets.update((b) => b.filter((_, i) => i !== idx));
  }

  protected async saveUsageSettings(): Promise<void> {
    const payload: UsageSettingsDto = {
      mode: this.usageMode() || "advisory",
      minWorkers: Math.max(1, Math.floor(Number(this.usageMinWorkers()) || 1)),
      retentionDays: Math.max(
        1,
        Math.floor(Number(this.usageRetentionDays()) || 30),
      ),
      budgets: this.usageBudgets(),
    };
    try {
      const res = await this.api.put<{ settings?: UsageSettingsDto }>(
        "PUT /api/usage/settings",
        undefined,
        payload,
      );
      if (res?.settings) {
        this.applyUsageSettings(res.settings);
      }
      this.snackbar.show("Usage settings saved");
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected overcommitMode(): string {
    const v = this.state.val<unknown>("sandboxOvercommit", "fixed");
    if (v && typeof v === "object" && "mode" in v) {
      return String((v as { mode?: string }).mode || "fixed");
    }
    return typeof v === "string" && v ? v : "fixed";
  }

  protected setOvercommitMode(mode: string): void {
    const cur = this.state.val<unknown>("sandboxOvercommit", null);
    if (cur && typeof cur === "object") {
      this.state.setField("sandboxOvercommit", {
        ...(cur as Record<string, unknown>),
        mode,
      });
    } else {
      this.state.setField("sandboxOvercommit", mode);
    }
  }

  protected async runSandboxProbe(): Promise<void> {
    try {
      const res = await this.api.post<{ ok?: boolean; detail?: string }>(
        "POST /api/sandbox-probe",
        undefined,
        {},
      );
      this.probeMessage.set(
        res?.detail || (res?.ok === false ? "Probe failed" : "Sandbox ready"),
      );
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async actPersistentSandbox(
    name: string,
    action: string,
  ): Promise<void> {
    try {
      await this.api.post(
        "POST /api/persistent-sandboxes/{name}/{action}",
        { name, action },
        {},
      );
      this.snackbar.show(`Sandbox ${name}: ${action}`);
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async loadHostds(): Promise<void> {
    if (this.access.can("GET /api/hostds")) {
      try {
        const r = await this.api.get<
          HostdRow[] | { hostds?: HostdRow[]; items?: HostdRow[] }
        >("GET /api/hostds");
        this.hostds.set(Array.isArray(r) ? r : (r?.hostds ?? r?.items ?? []));
      } catch {
        // Non-fatal
      }
    }
    if (this.access.can("GET /api/hostds/enrollment-codes")) {
      try {
        const c = await this.api.get<
          | EnrollmentCodeRow[]
          | { codes?: EnrollmentCodeRow[]; items?: EnrollmentCodeRow[] }
        >("GET /api/hostds/enrollment-codes");
        this.enrollmentCodes.set(
          Array.isArray(c) ? c : (c?.codes ?? c?.items ?? []),
        );
      } catch {
        // Non-fatal
      }
    }
    if (this.access.can("GET /api/hostds/pending")) {
      try {
        const p = await this.api.get<
          | PendingHostdRow[]
          | { pending?: PendingHostdRow[]; items?: PendingHostdRow[] }
        >("GET /api/hostds/pending");
        this.pendingHostds.set(
          Array.isArray(p) ? p : (p?.pending ?? p?.items ?? []),
        );
      } catch {
        // Non-fatal
      }
    }
  }

  private formatIssuedHostdSecret(
    label: string,
    res?: { token?: string; install?: HostdInstallDto | string } | null,
  ): string | null {
    if (!res) return null;
    const parts: string[] = [];
    if (typeof res.token === "string" && res.token) {
      parts.push(`${label} (shown once): ${res.token}`);
    }
    if (typeof res.install === "string" && res.install.trim()) {
      parts.push(res.install.trim());
    } else if (res.install && typeof res.install === "object") {
      if (typeof res.install.command === "string" && res.install.command) {
        parts.push(res.install.command);
      } else if (Array.isArray(res.install.lines) && res.install.lines.length) {
        parts.push(res.install.lines.join("\n"));
      }
      if (typeof res.install.note === "string" && res.install.note) {
        parts.push(`# ${res.install.note}`);
      }
    }
    return parts.length > 0 ? parts.join("\n\n") : null;
  }

  protected async createHostd(): Promise<void> {
    const name = this.newHostdName().trim();
    if (!name) return;
    const parsed = parseHostdLabels(this.newHostdLabels());
    if (parsed.error) {
      this.state.error.set(parsed.error);
      return;
    }
    try {
      const res = await this.api.post<{
        token?: string;
        install?: HostdInstallDto | string;
        hostd?: HostdRow;
      }>("POST /api/hostds", undefined, {
        name,
        ...(Object.keys(parsed.labels).length > 0
          ? { labels: parsed.labels }
          : {}),
      });
      this.newHostdName.set("");
      this.newHostdLabels.set("");
      const msg = this.formatIssuedHostdSecret(`Hostd token (${name})`, res);
      if (msg) this.mintedHostdToken.set(msg);
      await this.loadHostds();
      this.snackbar.show(`Registered hostd ${name}`);
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected rotateHostd(name: string): void {
    this.confirmRequest.set({
      title: `Rotate host ${name}'s token?`,
      message:
        "Its current token stops working at once, so the host is disconnected until the new token is installed on it.",
      accept: "Rotate token",
      destructive: true,
      run: async () => {
        try {
          const res = await this.api.post<{
            token?: string;
            install?: HostdInstallDto | string;
          }>("POST /api/hostds/{name}/rotate", { name }, {});
          const msg = this.formatIssuedHostdSecret(
            `Rotated token (${name})`,
            res,
          );
          if (msg) this.mintedHostdToken.set(msg);
          await this.loadHostds();
          this.snackbar.show(`Rotated ${name}`);
        } catch (err) {
          this.state.recordError(err);
        }
      },
    });
  }

  protected revokeHostd(name: string): void {
    this.confirmRequest.set({
      title: `Revoke host ${name}?`,
      message:
        "It is disconnected at once and cannot be restored: using that machine again means enrolling it anew. A revoked host can then be deleted.",
      accept: "Revoke host",
      destructive: true,
      run: async () => {
        try {
          await this.api.post("POST /api/hostds/{name}/revoke", { name }, {});
          await this.loadHostds();
          this.snackbar.show(`Revoked ${name}`);
        } catch (err) {
          this.state.recordError(err);
        }
      },
    });
  }

  protected deleteHostd(name: string): void {
    this.confirmRequest.set({
      title: `Delete host ${name}?`,
      message:
        "Its tokens go with it and the name can be registered again; past runs keep showing it, marked deleted.",
      accept: "Delete host",
      destructive: true,
      run: async () => {
        try {
          const out = await this.api.delete<DeletedHostdRow>(
            "DELETE /api/hostds/{name}",
            { name },
          );
          await this.loadHostds();
          this.snackbar.show(deletedHostdLine(out ?? { name }));
        } catch (err) {
          this.state.recordError(err);
        }
      },
    });
  }

  protected deleteRevokedHostds(): void {
    this.confirmRequest.set({
      title: "Delete every revoked host?",
      message:
        "One a model connection or capability server still names is skipped.",
      accept: "Delete revoked",
      destructive: true,
      run: async () => {
        try {
          const out = await this.api.post<{
            deleted?: DeletedHostdRow[] | null;
            skipped?: { name: string; reason: string }[] | null;
          }>("POST /api/hostds/delete-revoked", undefined, {});
          await this.loadHostds();
          const lines = (out?.deleted ?? []).map(deletedHostdLine);
          for (const sk of out?.skipped ?? []) {
            lines.push(`Skipped ${sk.name}: ${sk.reason}`);
          }
          this.snackbar.show(
            lines.length ? lines.join(" ") : "No revoked hosts to delete.",
          );
        } catch (err) {
          this.state.recordError(err);
        }
      },
    });
  }

  protected async createEnrollmentCode(): Promise<void> {
    const name = this.newCodeName().trim();
    if (!name) return;
    try {
      const res = await this.api.post<{
        code?: string;
        token?: string;
        install?: HostdInstallDto | string;
      }>("POST /api/hostds/enrollment-codes", undefined, { name });
      this.newCodeName.set("");
      const secret = res?.code ?? res?.token;
      const msg = this.formatIssuedHostdSecret(`Enrollment code (${name})`, {
        token: secret,
        install: res?.install,
      });
      if (msg) this.mintedHostdToken.set(msg);
      await this.loadHostds();
      this.snackbar.show(`Created enrollment code ${name}`);
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected rotateEnrollmentCode(name: string): void {
    this.confirmRequest.set({
      title: `Rotate enrollment code ${name}?`,
      message:
        "Its invite command stops working at once, so hosts not yet joined with it need the new one. Hosts already joined keep their credentials.",
      accept: "Rotate code",
      destructive: true,
      run: async () => {
        try {
          const res = await this.api.post<{
            code?: string;
            token?: string;
            install?: HostdInstallDto | string;
          }>("POST /api/hostds/enrollment-codes/{name}/rotate", { name }, {});
          const secret = res?.code ?? res?.token;
          const msg = this.formatIssuedHostdSecret(
            `Rotated enrollment code (${name})`,
            { token: secret, install: res?.install },
          );
          if (msg) this.mintedHostdToken.set(msg);
          await this.loadHostds();
          this.snackbar.show(`Rotated code ${name}`);
        } catch (err) {
          this.state.recordError(err);
        }
      },
    });
  }

  protected revokeEnrollmentCode(name: string): void {
    this.confirmRequest.set({
      title: `Revoke enrollment code ${name}?`,
      message:
        "Its invite command stops working for good and the code cannot be restored. Hosts already joined keep their credentials.",
      accept: "Revoke code",
      destructive: true,
      run: async () => {
        try {
          await this.api.post(
            "POST /api/hostds/enrollment-codes/{name}/revoke",
            { name },
            {},
          );
          await this.loadHostds();
          this.snackbar.show(`Revoked code ${name}`);
        } catch (err) {
          this.state.recordError(err);
        }
      },
    });
  }

  protected formatLabelsInput(labels?: Record<string, string> | null): string {
    const formatted = formatHostdLabels(labels);
    return formatted === "—" ? "" : formatted;
  }

  protected setPendingLabelDraft(id: string, value: string): void {
    this.pendingLabelDrafts.update((prev) => ({ ...prev, [id]: value }));
  }

  protected async acceptPendingHostd(p: PendingHostdRow): Promise<void> {
    const rawLabels =
      this.pendingLabelDrafts()[p.id] ||
      this.formatLabelsInput(p.default_labels);
    const parsed = parseHostdLabels(rawLabels);
    if (parsed.error) {
      this.state.error.set(parsed.error);
      return;
    }
    const hostName = p.suggested_name || p.hostname || p.id;
    try {
      await this.api.post(
        "POST /api/hostds/pending/{id}/accept",
        { id: p.id },
        {
          name: hostName,
          ...(Object.keys(parsed.labels).length > 0
            ? { labels: parsed.labels }
            : {}),
          ...(p.default_capacity && p.default_capacity > 0
            ? { capacity: p.default_capacity }
            : {}),
        },
      );
      await this.loadHostds();
      this.snackbar.show(`Approved hostd ${hostName}`);
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected rejectPendingHostd(p: PendingHostdRow): void {
    this.confirmRequest.set({
      title: `Reject the join request from ${p.hostname || p.id}?`,
      message:
        "It is discarded, and that host has to run its join command again to ask once more.",
      accept: "Reject request",
      destructive: true,
      run: async () => {
        try {
          await this.api.post(
            "POST /api/hostds/pending/{id}/reject",
            { id: p.id },
            {},
          );
          await this.loadHostds();
          this.snackbar.show("Rejected pending hostd");
        } catch (err) {
          this.state.recordError(err);
        }
      },
    });
  }

  protected async onConfirmDone(accepted: boolean): Promise<void> {
    const req = this.confirmRequest();
    this.confirmRequest.set(null);
    if (accepted && req) {
      try {
        await req.run();
      } catch (err) {
        this.state.recordError(err);
      }
    }
  }
}
