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
  inject,
  input,
  signal,
} from "@angular/core";
import { GrainApi } from "../api/grain-api.service";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import {
  type PillOption,
  PillSelectComponent,
} from "../shared/pill-select.component";
import type { SettingsSectionId } from "./settings-sections";
import { SettingsStateService, errText } from "./settings-state.service";

interface AgentKeyStatus {
  framework: string;
  configured?: boolean;
  set?: boolean;
  secret?: string;
}

interface KeyCheckResult {
  ok: boolean;
  detail: string;
  checkedAt?: string;
}

interface BackendEntry {
  id: string;
  framework: string;
  provider?: string;
  endpoint?: string;
  project?: string;
  location?: string;
  credentialSet?: boolean;
}

interface HostdRow {
  name: string;
  owner?: string;
  status?: string;
  revoked?: boolean;
  labels?: string[];
  capacity?: number;
}

interface EnrollmentCodeRow {
  name: string;
  expiresAt?: string;
  maxUses?: number;
  uses?: number;
  revoked?: boolean;
}

interface PendingHostdRow {
  id: string;
  hostname?: string;
  owner?: string;
  os?: string;
  arch?: string;
  requestedAt?: string;
}

@Component({
  selector: "g-settings-compute",
  imports: [PillSelectComponent],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @switch (section()) {
      @case ("models") {
        <div class="s-card" data-testid="card-model-defaults">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Default framework &amp; model</h3>
              <p class="s-card-desc">
                Selects the default agent runtime and model tiers when a task or
                repository does not pin one.
              </p>
            </div>
          </div>
          <div class="s-grid">
            <div class="s-field">
              <span class="s-label">Default agent framework</span>
              <g-pill-select
                testid="select-agent-framework"
                ariaLabel="Default agent framework"
                align="left"
                [options]="frameworkOptions"
                [value]="state.val('agentFramework', 'gemini')"
                [disabled]="!canWriteSettings()"
                (valueChange)="state.setField('agentFramework', $event)"
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="f-default-model"
                >Deployment default model</label
              >
              <input
                id="f-default-model"
                class="s-input"
                type="text"
                data-testid="input-default-model"
                placeholder="gemini-2.5-pro"
                [value]="state.deployVal('defaultModel', '')"
                [disabled]="!canWriteDeployment()"
                (input)="
                  state.setDeployField(
                    'defaultModel',
                    $any($event.target).value
                  )
                "
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="f-gemini-model">Gemini model</label>
              <input
                id="f-gemini-model"
                class="s-input"
                type="text"
                data-testid="input-gemini-model"
                [value]="state.val('geminiModel', '')"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField('geminiModel', $any($event.target).value)
                "
              />
            </div>
            <div class="s-field">
              <span class="s-label">Gemini reasoning effort</span>
              <g-pill-select
                testid="select-gemini-effort"
                ariaLabel="Gemini reasoning effort"
                align="left"
                [options]="effortOptions"
                [value]="state.val('geminiEffort', 'high')"
                [disabled]="!canWriteSettings()"
                (valueChange)="state.setField('geminiEffort', $event)"
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="f-claude-model">Claude model</label>
              <input
                id="f-claude-model"
                class="s-input"
                type="text"
                data-testid="input-claude-model"
                [value]="state.val('claudeModel', '')"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField('claudeModel', $any($event.target).value)
                "
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="f-codex-model">Codex model</label>
              <input
                id="f-codex-model"
                class="s-input"
                type="text"
                data-testid="input-codex-model"
                [value]="state.val('codexModel', '')"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField('codexModel', $any($event.target).value)
                "
              />
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
            <div class="s-table-wrap">
              <table class="s-table" aria-label="Framework API keys">
                <thead>
                  <tr>
                    <th>Framework</th>
                    <th>Status</th>
                    <th>Update key</th>
                    <th>Verify</th>
                  </tr>
                </thead>
                <tbody>
                  @for (fw of frameworks; track fw) {
                    <tr>
                      <td class="tabular">{{ fw }}</td>
                      <td>
                        <span
                          [class]="
                            'status-pill ' +
                            (isKeyConfigured(fw) ? 'healthy' : 'neutral')
                          "
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
                      <td>
                        <div class="s-inline">
                          <input
                            class="s-input"
                            style="width: 190px"
                            type="password"
                            autocomplete="off"
                            [attr.aria-label]="fw + ' API key'"
                            [attr.data-testid]="'input-agent-key-' + fw"
                            placeholder="Paste secret key…"
                            [value]="keyDrafts()[fw] || ''"
                            [disabled]="
                              !access.can('PUT /api/agent-keys/{framework}')
                            "
                            (input)="setKeyDraft(fw, $any($event.target).value)"
                          />
                          <button
                            type="button"
                            class="btn-tonal s-btn-sm"
                            [attr.data-testid]="'save-agent-key-' + fw"
                            [disabled]="
                              !(keyDrafts()[fw] || '').trim() ||
                              !access.can('PUT /api/agent-keys/{framework}') ||
                              busyKey() === fw
                            "
                            (click)="saveAgentKey(fw)"
                          >
                            Save key
                          </button>
                          @if (isKeyConfigured(fw)) {
                            <button
                              type="button"
                              class="btn-text s-btn-sm"
                              [attr.data-testid]="'clear-agent-key-' + fw"
                              [disabled]="
                                !access.can(
                                  'DELETE /api/agent-keys/{framework}'
                                ) || busyKey() === fw
                              "
                              (click)="clearAgentKey(fw)"
                            >
                              Clear
                            </button>
                          }
                        </div>
                      </td>
                      <td>
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
                      </td>
                    </tr>
                  }
                </tbody>
              </table>
            </div>
          }

          <div class="s-field">
            <label class="s-label" for="f-codex-auth"
              >Codex auth.json (OAuth session bundle)</label
            >
            <textarea
              id="f-codex-auth"
              class="s-textarea tabular"
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
                  !codexAuthDraft().trim() || !access.can('PUT /api/codex-auth')
                "
                (click)="saveCodexAuth()"
              >
                Save Codex auth
              </button>
              <button
                type="button"
                class="btn-text s-btn-sm"
                data-testid="clear-codex-auth"
                [disabled]="!access.can('DELETE /api/codex-auth')"
                (click)="clearCodexAuth()"
              >
                Clear Codex auth
              </button>
            </div>
          </div>
        </div>

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
                    <th>ID</th>
                    <th>Framework</th>
                    <th>Provider / Endpoint</th>
                    <th>Credential</th>
                    <th>Actions</th>
                  </tr>
                </thead>
                <tbody>
                  @for (b of backends(); track b.id) {
                    <tr>
                      <td class="tabular">{{ b.id }}</td>
                      <td>{{ b.framework }}</td>
                      <td class="tabular">
                        {{ b.provider || "api"
                        }}{{ b.endpoint ? " · " + b.endpoint : ""
                        }}{{ b.project ? " · " + b.project : "" }}
                      </td>
                      <td>
                        <div class="s-inline">
                          <input
                            class="s-input"
                            style="width: 140px"
                            type="password"
                            [attr.aria-label]="'Credential for ' + b.id"
                            placeholder="Set secret…"
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
                            type="button"
                            class="btn-tonal s-btn-sm"
                            [disabled]="
                              !(backendCredDrafts()[b.id] || '').trim() ||
                              !access.can(
                                'PUT /api/agent-backends/{id}/credential'
                              )
                            "
                            (click)="saveBackendCred(b.id)"
                          >
                            Set
                          </button>
                        </div>
                      </td>
                      <td>
                        <button
                          type="button"
                          class="btn-text s-btn-sm"
                          [attr.data-testid]="'remove-backend-' + b.id"
                          [disabled]="
                            !access.can(
                              'DELETE /api/deployment/agent-backends/{backendId}'
                            )
                          "
                          (click)="deleteBackend(b.id)"
                        >
                          Remove
                        </button>
                      </td>
                    </tr>
                  }
                </tbody>
              </table>
            </div>
          }

          <div class="s-grid">
            <div class="s-field">
              <label class="s-label" for="nb-id">Connection ID</label>
              <input
                id="nb-id"
                class="s-input tabular"
                type="text"
                data-testid="input-new-backend-id"
                placeholder="vertex-us-central1"
                [value]="newBackendId()"
                (input)="newBackendId.set($any($event.target).value)"
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
              <span class="s-label">Provider</span>
              <g-pill-select
                testid="select-new-backend-provider"
                ariaLabel="Backend provider"
                align="left"
                [options]="providerOptions"
                [value]="newBackendProvider()"
                (valueChange)="newBackendProvider.set($event)"
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="nb-endpoint"
                >Endpoint / GCP project</label
              >
              <input
                id="nb-endpoint"
                class="s-input"
                type="text"
                data-testid="input-new-backend-endpoint"
                placeholder="https://... or my-gcp-project"
                [value]="newBackendEndpoint()"
                (input)="newBackendEndpoint.set($any($event.target).value)"
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

          @if (discoveredModels().length > 0) {
            <div class="s-field">
              <span class="s-label"
                >Discovered models ({{ discoveredModels().length }})</span
              >
              <div class="s-inline" data-testid="discovered-models-list">
                @for (m of discoveredModels(); track m) {
                  <span class="status-pill neutral tabular">{{ m }}</span>
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
                Daily token ceiling across running agents (PUT
                /api/usage/settings).
              </p>
            </div>
          </div>
          <div class="s-inline">
            <input
              class="s-input tabular"
              style="width: 220px"
              type="number"
              min="0"
              aria-label="Daily token budget"
              data-testid="input-daily-token-budget"
              placeholder="0 (unlimited)"
              [value]="dailyTokenBudget()"
              [disabled]="!access.can('PUT /api/usage/settings')"
              (input)="dailyTokenBudget.set($any($event.target).value)"
            />
            <button
              type="button"
              class="btn-tonal"
              data-testid="save-token-budget"
              [disabled]="!access.can('PUT /api/usage/settings')"
              (click)="saveTokenBudget()"
            >
              Update budget
            </button>
            @if (usageSummary()) {
              <span class="s-hint tabular">{{ usageSummary() }}</span>
            }
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
                class="s-input tabular"
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
                class="s-input tabular"
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
                class="s-input tabular"
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
                class="s-input tabular"
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
                class="s-input tabular"
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
                class="s-input tabular"
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
                class="s-input tabular"
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
                class="s-input tabular"
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
                    <th>Actions</th>
                  </tr>
                </thead>
                <tbody>
                  @for (ps of persistentSandboxes(); track ps.name) {
                    <tr>
                      <td class="tabular">{{ ps.name }}</td>
                      <td>
                        <span class="status-pill neutral">{{
                          ps.state || "ready"
                        }}</span>
                      </td>
                      <td>
                        <div class="s-inline">
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
                    <th>Actions</th>
                  </tr>
                </thead>
                <tbody>
                  @for (h of hostds(); track h.name) {
                    <tr>
                      <td class="tabular">{{ h.name }}</td>
                      <td>{{ h.owner || "—" }}</td>
                      <td>
                        <span
                          [class]="
                            'status-pill ' +
                            (h.revoked
                              ? 'error'
                              : h.status === 'connected'
                                ? 'healthy'
                                : 'neutral')
                          "
                        >
                          {{ h.revoked ? "revoked" : h.status || "registered" }}
                        </span>
                      </td>
                      <td class="tabular">
                        {{ (h.labels || []).join(", ") || "—" }}
                      </td>
                      <td>
                        <div class="s-inline">
                          <button
                            type="button"
                            class="btn-tonal s-btn-sm"
                            [attr.data-testid]="'rotate-hostd-' + h.name"
                            [disabled]="
                              !access.can('POST /api/hostds/{name}/rotate')
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
                              !!h.revoked
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
                              !access.can('DELETE /api/hostds/{name}')
                            "
                            (click)="deleteHostd(h.name)"
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

          <div class="s-grid">
            <div class="s-field">
              <label class="s-label" for="hd-name">New hostd name</label>
              <input
                id="hd-name"
                class="s-input tabular"
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
                >Labels (comma-separated)</label
              >
              <input
                id="hd-labels"
                class="s-input tabular"
                type="text"
                placeholder="linux,gpu"
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
          @if (mintedHostdToken(); as tok) {
            <pre class="s-code" data-testid="minted-hostd-token">{{ tok }}</pre>
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
                      <th>Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    @for (c of enrollmentCodes(); track c.name) {
                      <tr>
                        <td class="tabular">{{ c.name }}</td>
                        <td class="tabular">
                          {{ c.uses ?? 0 }} / {{ c.maxUses ?? "∞" }}
                        </td>
                        <td class="tabular">{{ c.expiresAt || "never" }}</td>
                        <td>
                          <div class="s-inline">
                            <button
                              type="button"
                              class="btn-tonal s-btn-sm"
                              [disabled]="
                                !access.can(
                                  'POST /api/hostds/enrollment-codes/{name}/rotate'
                                )
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
                                ) || !!c.revoked
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
                class="s-input tabular"
                style="width: 220px"
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
                    <th>Owner</th>
                    <th>Platform</th>
                    <th>Approve / Reject</th>
                  </tr>
                </thead>
                <tbody>
                  @for (p of pendingHostds(); track p.id) {
                    <tr>
                      <td class="tabular">{{ p.hostname || p.id }}</td>
                      <td>{{ p.owner || "—" }}</td>
                      <td class="tabular">
                        {{ p.os || "?" }}/{{ p.arch || "?" }}
                      </td>
                      <td>
                        <div class="s-inline">
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
                            (click)="rejectPendingHostd(p.id)"
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
  protected readonly frameworks = ["gemini", "claude", "codex"] as const;

  protected readonly frameworkOptions: readonly PillOption[] = [
    { value: "gemini", label: "Gemini" },
    { value: "claude", label: "Claude" },
    { value: "codex", label: "Codex" },
  ];
  protected readonly effortOptions: readonly PillOption[] = [
    { value: "low", label: "Low" },
    { value: "medium", label: "Medium" },
    { value: "high", label: "High" },
  ];
  protected readonly providerOptions: readonly PillOption[] = [
    { value: "api", label: "API key" },
    { value: "vertex", label: "Vertex AI" },
    { value: "bedrock", label: "AWS Bedrock" },
    { value: "openai-compatible", label: "OpenAI-compatible" },
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

  protected readonly agentKeys = signal<Record<string, boolean>>({});
  protected readonly keyDrafts = signal<Record<string, string>>({});
  protected readonly keyChecks = signal<Record<string, KeyCheckResult>>({});
  protected readonly busyKey = signal<string | null>(null);
  protected readonly codexAuthDraft = signal("");

  protected readonly backends = signal<BackendEntry[]>([]);
  protected readonly backendCredDrafts = signal<Record<string, string>>({});
  protected readonly newBackendId = signal("");
  protected readonly newBackendFw = signal("gemini");
  protected readonly newBackendProvider = signal("vertex");
  protected readonly newBackendEndpoint = signal("");
  protected readonly discoveredModels = signal<string[]>([]);
  protected readonly dailyTokenBudget = signal("");
  protected readonly usageSummary = signal("");

  protected readonly persistentSandboxes = signal<
    { name: string; state?: string }[]
  >([]);
  protected readonly probeMessage = signal("");

  protected readonly hostds = signal<HostdRow[]>([]);
  protected readonly newHostdName = signal("");
  protected readonly newHostdLabels = signal("");
  protected readonly mintedHostdToken = signal<string | null>(null);
  protected readonly enrollmentCodes = signal<EnrollmentCodeRow[]>([]);
  protected readonly newCodeName = signal("");
  protected readonly pendingHostds = signal<PendingHostdRow[]>([]);

  ngOnInit(): void {
    this.loadForSection();
  }

  ngOnChanges(): void {
    this.loadForSection();
  }

  protected canWriteSettings(): boolean {
    return (
      this.access.can("POST /api/settings") &&
      !this.state.sectionLocked(this.section())
    );
  }

  protected canWriteDeployment(): boolean {
    return (
      this.access.can("PUT /api/deployment") &&
      !this.state.sectionLocked(this.section())
    );
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
            const b = u["dailyTokenBudget"] ?? u["budget"];
            if (b !== undefined) this.dailyTokenBudget.set(String(b));
            const used = u["tokensToday"] ?? u["totalTokens"];
            if (used !== undefined) {
              this.usageSummary.set(`Used today: ${used} tokens`);
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
      this.state.deployment()["agentBackends"] ??
      this.state.settings()["agentBackends"];
    if (Array.isArray(raw)) {
      this.backends.set(raw as BackendEntry[]);
    } else if (raw && typeof raw === "object") {
      this.backends.set(
        Object.entries(raw as Record<string, Record<string, unknown>>).map(
          ([id, cfg]) => ({
            id,
            framework: String(cfg?.["framework"] ?? "gemini"),
            provider: cfg?.["provider"] ? String(cfg["provider"]) : undefined,
            endpoint: cfg?.["endpoint"] ? String(cfg["endpoint"]) : undefined,
            project: cfg?.["project"] ? String(cfg["project"]) : undefined,
          }),
        ),
      );
    }
  }

  protected async loadAgentKeys(): Promise<void> {
    try {
      const res = await this.api.get<
        AgentKeyStatus[] | { keys?: AgentKeyStatus[] } | Record<string, unknown>
      >("GET /api/agent-keys");
      const map: Record<string, boolean> = {};
      if (Array.isArray(res)) {
        for (const k of res) {
          map[k.framework] = Boolean(k.configured ?? k.set);
        }
      } else if (res && typeof res === "object") {
        const list = (res as { keys?: AgentKeyStatus[] }).keys;
        if (Array.isArray(list)) {
          for (const k of list) {
            map[k.framework] = Boolean(k.configured ?? k.set);
          }
        } else {
          for (const fw of this.frameworks) {
            const v = (res as Record<string, unknown>)[fw];
            if (typeof v === "boolean") map[fw] = v;
            else if (v && typeof v === "object") {
              map[fw] = Boolean(
                (v as Record<string, unknown>)["configured"] ??
                (v as Record<string, unknown>)["set"],
              );
            }
          }
        }
      }
      this.agentKeys.set(map);
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

  protected async saveAgentKey(fw: string): Promise<void> {
    const value = (this.keyDrafts()[fw] ?? "").trim();
    if (!value) return;
    this.busyKey.set(fw);
    try {
      await this.api.put(
        "PUT /api/agent-keys/{framework}",
        { framework: fw },
        { value },
      );
      this.setKeyDraft(fw, "");
      this.agentKeys.update((p) => ({ ...p, [fw]: true }));
      this.snackbar.show(`Saved ${fw} API key`);
    } catch (err) {
      this.state.error.set(errText(err));
    } finally {
      this.busyKey.set(null);
    }
  }

  protected async clearAgentKey(fw: string): Promise<void> {
    this.busyKey.set(fw);
    try {
      await this.api.delete("DELETE /api/agent-keys/{framework}", {
        framework: fw,
      });
      this.agentKeys.update((p) => ({ ...p, [fw]: false }));
      this.snackbar.show(`Cleared ${fw} API key`);
    } catch (err) {
      this.state.error.set(errText(err));
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
      await this.api.put("PUT /api/codex-auth", undefined, { value });
      this.codexAuthDraft.set("");
      this.snackbar.show("Codex auth saved");
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  protected async clearCodexAuth(): Promise<void> {
    try {
      await this.api.delete("DELETE /api/codex-auth");
      this.snackbar.show("Codex auth cleared");
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  protected setBackendCredDraft(id: string, val: string): void {
    this.backendCredDrafts.update((p) => ({ ...p, [id]: val }));
  }

  protected async saveBackend(): Promise<void> {
    const backendId = this.newBackendId().trim();
    if (!backendId) return;
    try {
      const endpoint = this.newBackendEndpoint().trim();
      await this.api.put(
        "PUT /api/deployment/agent-backends/{backendId}",
        { backendId },
        {
          framework: this.newBackendFw(),
          provider: this.newBackendProvider(),
          ...(this.newBackendProvider() === "vertex"
            ? { project: endpoint }
            : { endpoint }),
        },
      );
      this.newBackendId.set("");
      this.newBackendEndpoint.set("");
      await this.state.loadCore();
      this.syncBackendsFromState();
      this.snackbar.show(`Saved model connection ${backendId}`);
    } catch (err) {
      this.state.error.set(errText(err));
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
      this.state.error.set(errText(err));
    }
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
      this.state.error.set(errText(err));
    }
  }

  protected async loadDiscoveredModels(): Promise<void> {
    try {
      const res = await this.api.get<
        string[] | { models?: (string | { id?: string; name?: string })[] }
      >("GET /api/agent-models");
      const raw = Array.isArray(res) ? res : (res?.models ?? []);
      this.discoveredModels.set(
        raw
          .map((m) => (typeof m === "string" ? m : (m.id ?? m.name ?? "")))
          .filter(Boolean),
      );
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  protected async saveTokenBudget(): Promise<void> {
    try {
      const n = Number(this.dailyTokenBudget() || "0");
      await this.api.put("PUT /api/usage/settings", undefined, {
        dailyTokenBudget: Number.isFinite(n) ? n : 0,
      });
      this.snackbar.show("Token budget updated");
    } catch (err) {
      this.state.error.set(errText(err));
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
      this.state.error.set(errText(err));
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
      this.state.error.set(errText(err));
    }
  }

  protected async loadHostds(): Promise<void> {
    if (this.access.can("GET /api/hostds")) {
      try {
        const r = await this.api.get<HostdRow[] | { hostds?: HostdRow[] }>(
          "GET /api/hostds",
        );
        this.hostds.set(Array.isArray(r) ? r : (r?.hostds ?? []));
      } catch {
        // Non-fatal
      }
    }
    if (this.access.can("GET /api/hostds/enrollment-codes")) {
      try {
        const c = await this.api.get<
          EnrollmentCodeRow[] | { codes?: EnrollmentCodeRow[] }
        >("GET /api/hostds/enrollment-codes");
        this.enrollmentCodes.set(Array.isArray(c) ? c : (c?.codes ?? []));
      } catch {
        // Non-fatal
      }
    }
    if (this.access.can("GET /api/hostds/pending")) {
      try {
        const p = await this.api.get<
          PendingHostdRow[] | { pending?: PendingHostdRow[] }
        >("GET /api/hostds/pending");
        this.pendingHostds.set(Array.isArray(p) ? p : (p?.pending ?? []));
      } catch {
        // Non-fatal
      }
    }
  }

  protected async createHostd(): Promise<void> {
    const name = this.newHostdName().trim();
    if (!name) return;
    const labels = this.newHostdLabels()
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean);
    try {
      const res = await this.api.post<{
        token?: string;
        install?: string;
        hostd?: HostdRow;
      }>("POST /api/hostds", undefined, { name, labels });
      this.newHostdName.set("");
      this.newHostdLabels.set("");
      if (res?.install || res?.token) {
        this.mintedHostdToken.set(res.install || `Token: ${res.token}`);
      }
      await this.loadHostds();
      this.snackbar.show(`Registered hostd ${name}`);
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  protected async rotateHostd(name: string): Promise<void> {
    try {
      const res = await this.api.post<{ token?: string }>(
        "POST /api/hostds/{name}/rotate",
        { name },
        {},
      );
      if (res?.token) {
        this.mintedHostdToken.set(`Rotated token (${name}): ${res.token}`);
      }
      await this.loadHostds();
      this.snackbar.show(`Rotated ${name}`);
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  protected async revokeHostd(name: string): Promise<void> {
    try {
      await this.api.post("POST /api/hostds/{name}/revoke", { name }, {});
      await this.loadHostds();
      this.snackbar.show(`Revoked ${name}`);
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  protected async deleteHostd(name: string): Promise<void> {
    try {
      await this.api.delete("DELETE /api/hostds/{name}", { name });
      await this.loadHostds();
      this.snackbar.show(`Deleted ${name}`);
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  protected async deleteRevokedHostds(): Promise<void> {
    try {
      await this.api.post("POST /api/hostds/delete-revoked", undefined, {});
      await this.loadHostds();
      this.snackbar.show("Removed revoked hostds");
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  protected async createEnrollmentCode(): Promise<void> {
    const name = this.newCodeName().trim();
    if (!name) return;
    try {
      const res = await this.api.post<{ code?: string; token?: string }>(
        "POST /api/hostds/enrollment-codes",
        undefined,
        { name },
      );
      this.newCodeName.set("");
      if (res?.code || res?.token) {
        this.mintedHostdToken.set(
          `Enrollment code (${name}): ${res.code ?? res.token}`,
        );
      }
      await this.loadHostds();
      this.snackbar.show(`Created enrollment code ${name}`);
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  protected async rotateEnrollmentCode(name: string): Promise<void> {
    try {
      await this.api.post(
        "POST /api/hostds/enrollment-codes/{name}/rotate",
        { name },
        {},
      );
      await this.loadHostds();
      this.snackbar.show(`Rotated code ${name}`);
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  protected async revokeEnrollmentCode(name: string): Promise<void> {
    try {
      await this.api.post(
        "POST /api/hostds/enrollment-codes/{name}/revoke",
        { name },
        {},
      );
      await this.loadHostds();
      this.snackbar.show(`Revoked code ${name}`);
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  protected async acceptPendingHostd(p: PendingHostdRow): Promise<void> {
    try {
      await this.api.post(
        "POST /api/hostds/pending/{id}/accept",
        { id: p.id },
        { name: p.hostname || p.id, labels: [], capacity: 2 },
      );
      await this.loadHostds();
      this.snackbar.show(`Approved hostd ${p.hostname || p.id}`);
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  protected async rejectPendingHostd(id: string): Promise<void> {
    try {
      await this.api.post("POST /api/hostds/pending/{id}/reject", { id }, {});
      await this.loadHostds();
      this.snackbar.show("Rejected pending hostd");
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }
}
