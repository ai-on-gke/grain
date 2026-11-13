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
  status?: { attached?: boolean; error?: string } | null;
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
}

interface SecretMetaRow {
  name: string;
  keys?: string[];
}

@Component({
  selector: "g-settings-work",
  imports: [PillSelectComponent],
  encapsulation: ViewEncapsulation.None,
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
                (input)="
                  state.setField(
                    'maxAutoSubmitDepth',
                    Number($any($event.target).value)
                  )
                "
              />
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
                (input)="
                  state.setField(
                    'maxAutoSubmitFanOut',
                    Number($any($event.target).value)
                  )
                "
              />
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
                (input)="
                  state.setField(
                    'maxAgentTurns',
                    Number($any($event.target).value)
                  )
                "
              />
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
        <div class="s-card" data-testid="card-capability-servers">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Capability servers (MCP)</h3>
              <p class="s-card-desc">
                External tool servers, secret bindings, and live connectivity
                probes.
              </p>
            </div>
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
                        <span
                          [class]="
                            'status-pill ' +
                            (!isDisabled ? 'healthy' : 'neutral')
                          "
                        >
                          {{ !isDisabled ? "enabled" : "disabled" }}
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
                              (click)="removeCapServer(srv.id)"
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

          @if (capTestNotice()) {
            <div class="s-banner ok" data-testid="cap-test-notice">
              {{ capTestNotice() }}
            </div>
          }

          <div class="s-subsection">
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

        <div class="s-card" data-testid="card-capability-policy">
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

          <div class="s-inline">
            <input
              class="s-input s-input-inline s-mono"
              type="text"
              aria-label="Capability pattern"
              data-testid="input-policy-capability"
              placeholder="gh-cli"
              [value]="newRuleCap()"
              (input)="newRuleCap.set($any($event.target).value)"
            />
            <input
              class="s-input s-input-inline s-mono"
              type="text"
              aria-label="Repositories filter (comma-separated)"
              data-testid="input-policy-repos"
              placeholder="acme/* (empty = *)"
              [value]="newRuleRepos()"
              (input)="newRuleRepos.set($any($event.target).value)"
            />
            <g-pill-select
              testid="select-policy-effect"
              ariaLabel="Rule effect"
              align="left"
              [options]="policyEffectOptions"
              [value]="newRuleEffect()"
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
              [disabled]="!newRuleCap().trim()"
              (click)="addPolicyRule()"
            >
              Add rule
            </button>
          </div>
        </div>

        <div class="s-card" data-testid="card-scope-sets-secrets">
          <h3 class="s-card-title">API scope sets &amp; secret vault</h3>
          @if (scopeSets().length > 0) {
            <div class="s-inline">
              @for (ss of scopeSets(); track ss.name) {
                <span class="status-pill neutral s-mono"
                  >{{ ss.name }} ({{ ss.scopes.length }})</span
                >
              }
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
              (input)="newScopeSetName.set($any($event.target).value)"
            />
            <input
              class="s-input s-input-inline s-mono"
              type="text"
              aria-label="Scopes list"
              data-testid="input-scope-set-scopes"
              placeholder="tasks:read,ci:read"
              [value]="newScopeSetScopes()"
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

          <div class="s-field">
            <span class="s-label"
              >Stored secrets ({{ secretsList().length }}) — write-only</span
            >
            <div class="s-inline">
              <input
                class="s-input s-input-inline s-mono"
                type="text"
                aria-label="Secret name"
                data-testid="input-secret-name"
                placeholder="webhook-secret"
                [value]="secretName()"
                (input)="secretName.set($any($event.target).value)"
              />
              <input
                class="s-input s-input-inline s-mono"
                type="text"
                aria-label="Secret key"
                data-testid="input-secret-key"
                placeholder="token"
                [value]="secretKey()"
                (input)="secretKey.set($any($event.target).value)"
              />
              <textarea
                class="s-textarea s-input-inline s-mono"
                rows="1"
                aria-label="Secret value"
                data-testid="input-secret-value"
                placeholder="Secret value or PEM…"
                [value]="secretVal()"
                (input)="secretVal.set($any($event.target).value)"
              ></textarea>
              <button
                type="button"
                class="btn-tonal s-btn-sm"
                data-testid="store-secret-btn"
                [disabled]="
                  !secretName().trim() ||
                  !secretKey().trim() ||
                  secretVal().length === 0 ||
                  !access.can('PUT /api/secrets/{secret}/{key}')
                "
                (click)="putSecret()"
              >
                Store secret
              </button>
            </div>
          </div>

          @if (!access.can("POST /api/secrets/redact")) {
            <span class="s-hint" data-testid="secrets-redact-restricted">{{
              access.deniedReason("POST /api/secrets/redact")
            }}</span>
          }
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

  protected readonly policyRules = signal<CapabilityPolicyRule[]>([]);
  protected readonly newRuleCap = signal("");
  protected readonly newRuleRepos = signal("");
  protected readonly newRuleEffect = signal("admit");
  protected readonly newRuleAllowUntrusted = signal(false);

  protected readonly scopeSets = signal<ScopeSetRow[]>([]);
  protected readonly newScopeSetName = signal("");
  protected readonly newScopeSetScopes = signal("");

  protected readonly secretsList = signal<SecretMetaRow[]>([]);
  protected readonly secretName = signal("");
  protected readonly secretKey = signal("value");
  protected readonly secretVal = signal("");

  protected readonly memoryBody = signal("");
  protected readonly memoryDocs = signal<string[]>([]);
  private readonly qaRolloutRaw = signal<{
    epoch: number;
    text: string;
  } | null>(null);

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
        label: "golden-v1-minimal (S3 deterministic + S4 probe)",
      },
      {
        value: "deterministic-s3-only",
        label: "deterministic-s3-only (local AST & vet only)",
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
    const raw = this.qaRolloutRaw();
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
      this.qaRolloutRaw.set(null);
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

  protected setQAFlow(flowId: string): void {
    this.state.setField("qaFlowId", flowId);
  }

  protected onQARolloutInput(ev: Event): void {
    const target = ev.target;
    if (!(target instanceof HTMLInputElement)) return;
    const raw = target.value;
    this.qaRolloutRaw.set({ epoch: this.state.discardEpoch(), text: raw });

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
      this.qaRolloutRaw.set(null);
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
          policy?: { rules?: CapabilityPolicyRule[] };
          rules?: CapabilityPolicyRule[];
        }>("GET /api/capability-policy");
        this.policyRules.set(p?.policy?.rules ?? p?.rules ?? []);
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
    if (this.access.can("GET /api/secrets")) {
      try {
        const sec = await this.api.get<
          SecretMetaRow[] | { secrets?: SecretMetaRow[] }
        >("GET /api/secrets");
        this.secretsList.set(Array.isArray(sec) ? sec : (sec?.secrets ?? []));
      } catch {
        // Non-fatal
      }
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
      const res = await this.api.post<{ ok?: boolean; detail?: string }>(
        "POST /api/capability-servers/test",
        undefined,
        { id },
      );
      this.capTestNotice.set(
        `${id}: ${res?.detail || (res?.ok === false ? "failed" : "reachable")}`,
      );
    } catch (err) {
      this.state.recordError(err);
    }
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
      this.state.recordError(err);
    }
  }

  protected async saveCapPolicy(): Promise<void> {
    try {
      await this.api.put("PUT /api/capability-policy", undefined, {
        policy: { rules: this.policyRules() },
      });
      this.snackbar.show("Capability policy saved");
    } catch (err) {
      this.state.recordError(err);
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

  protected async putSecret(): Promise<void> {
    try {
      await this.api.put(
        "PUT /api/secrets/{secret}/{key}",
        { secret: this.secretName().trim(), key: this.secretKey().trim() },
        { value: this.secretVal() },
      );
      this.secretVal.set("");
      await this.loadCapabilities();
      this.snackbar.show("Secret stored");
    } catch (err) {
      this.state.recordError(err);
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

  protected async saveMemory(): Promise<void> {
    try {
      await this.api.put("PUT /api/memory", undefined, {
        text: this.memoryBody(),
      });
      this.snackbar.show("Memory updated");
    } catch (err) {
      this.state.recordError(err);
    }
  }
}
