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

interface CapabilityServerRow {
  id: string;
  enabled?: boolean;
  transport?: string;
  url?: string;
  command?: string;
  status?: string;
}

interface PolicyRuleRow {
  capability: string;
  effect: string;
  repo?: string;
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
                class="s-input tabular"
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
                class="s-input tabular"
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
                class="s-input tabular"
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
      }

      @case ("agents") {
        <div class="s-card" data-testid="card-agents-runtime">
          <h3 class="s-card-title">Agent turn limits &amp; default grants</h3>
          <div class="s-grid">
            <div class="s-field">
              <label class="s-label" for="ag-turns">Max agent turns</label>
              <input
                id="ag-turns"
                class="s-input tabular"
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
                class="s-input tabular"
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
                    <th>Actions</th>
                  </tr>
                </thead>
                <tbody>
                  @for (cs of consoleSessions(); track cs.id) {
                    <tr>
                      <td class="tabular">
                        {{ cs.title || cs.name || cs.id }}
                      </td>
                      <td class="tabular">{{ cs.model || "default" }}</td>
                      <td>
                        <span class="status-pill neutral">{{
                          cs.state || "idle"
                        }}</span>
                      </td>
                      <td>
                        <div class="s-inline">
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
              class="s-input"
              style="width: 220px"
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
                    <th>Actions</th>
                  </tr>
                </thead>
                <tbody>
                  @for (srv of capServers(); track srv.id) {
                    <tr [attr.data-testid]="'cap-server-row-' + srv.id">
                      <td class="tabular">{{ srv.id }}</td>
                      <td class="tabular">
                        {{ srv.url || srv.command || srv.transport || "mcp" }}
                      </td>
                      <td>
                        <span
                          [class]="
                            'status-pill ' +
                            (srv.enabled !== false ? 'healthy' : 'neutral')
                          "
                        >
                          {{ srv.enabled !== false ? "enabled" : "disabled" }}
                        </span>
                      </td>
                      <td>
                        <div class="s-inline">
                          <button
                            type="button"
                            class="btn-tonal s-btn-sm"
                            [attr.data-testid]="'toggle-cap-server-' + srv.id"
                            [disabled]="
                              !access.can(
                                'POST /api/capability-servers/enable'
                              ) || state.sectionLocked('capabilities')
                            "
                            (click)="
                              toggleCapServer(srv.id, srv.enabled === false)
                            "
                          >
                            {{ srv.enabled === false ? "Enable" : "Disable" }}
                          </button>
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
                          <button
                            type="button"
                            class="btn-text s-btn-sm"
                            [disabled]="
                              !access.can(
                                'POST /api/capability-servers/remove'
                              ) || state.sectionLocked('capabilities')
                            "
                            (click)="removeCapServer(srv.id)"
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

          @if (capTestNotice()) {
            <div class="s-banner ok" data-testid="cap-test-notice">
              {{ capTestNotice() }}
            </div>
          }

          <div class="s-grid">
            <div class="s-field">
              <label class="s-label" for="cs-id">Server ID</label>
              <input
                id="cs-id"
                class="s-input tabular"
                type="text"
                data-testid="input-cap-server-id"
                placeholder="jira-mcp"
                [value]="newServerId()"
                (input)="newServerId.set($any($event.target).value)"
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="cs-url">HTTP URL or stdio cmd</label>
              <input
                id="cs-url"
                class="s-input tabular"
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

        <div class="s-card" data-testid="card-capability-policy">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Capability grant policy</h3>
              <p class="s-card-desc">
                Ordered rules deciding whether agent capability requests are
                allowed, held for Inbox approval, or denied.
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
                    <th></th>
                  </tr>
                </thead>
                <tbody>
                  @for (r of policyRules(); track $index; let idx = $index) {
                    <tr>
                      <td class="tabular">{{ r.capability }}</td>
                      <td>
                        <span class="status-pill neutral">{{ r.effect }}</span>
                      </td>
                      <td class="tabular">{{ r.repo || "*" }}</td>
                      <td>
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
              style="width: 200px"
              type="text"
              aria-label="Capability pattern"
              data-testid="input-policy-capability"
              placeholder="mcp:jira-mcp/*"
              [value]="newRuleCap()"
              (input)="newRuleCap.set($any($event.target).value)"
            />
            <g-pill-select
              testid="select-policy-effect"
              ariaLabel="Rule effect"
              align="left"
              [options]="policyEffectOptions"
              [value]="newRuleEffect()"
              (valueChange)="newRuleEffect.set($event)"
            />
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
                <span class="status-pill neutral tabular"
                  >{{ ss.name }} ({{ ss.scopes.length }})</span
                >
              }
            </div>
          }
          <div class="s-inline">
            <input
              class="s-input tabular"
              style="width: 170px"
              type="text"
              aria-label="Scope set name"
              data-testid="input-scope-set-name"
              placeholder="ci-readonly"
              [value]="newScopeSetName()"
              (input)="newScopeSetName.set($any($event.target).value)"
            />
            <input
              class="s-input tabular"
              style="width: 240px"
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
                class="s-input tabular"
                style="width: 160px"
                type="text"
                aria-label="Secret name"
                placeholder="webhook-secret"
                [value]="secretName()"
                (input)="secretName.set($any($event.target).value)"
              />
              <input
                class="s-input tabular"
                style="width: 120px"
                type="text"
                aria-label="Secret key"
                placeholder="token"
                [value]="secretKey()"
                (input)="secretKey.set($any($event.target).value)"
              />
              <input
                class="s-input"
                style="width: 190px"
                type="password"
                autocomplete="off"
                aria-label="Secret value"
                placeholder="Secret value…"
                [value]="secretVal()"
                (input)="secretVal.set($any($event.target).value)"
              />
              <button
                type="button"
                class="btn-tonal s-btn-sm"
                [disabled]="
                  !secretName().trim() ||
                  !secretKey().trim() ||
                  !secretVal().trim() ||
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
              class="s-textarea tabular"
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
                <span class="status-pill neutral tabular">{{ doc }}</span>
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
    { value: "allow", label: "Allow" },
    { value: "ask", label: "Ask in Inbox" },
    { value: "deny", label: "Deny" },
  ];

  protected readonly consoleSessions = signal<ConsoleSessionRow[]>([]);
  protected readonly newSessionTitle = signal("");

  protected readonly capServers = signal<CapabilityServerRow[]>([]);
  protected readonly newServerId = signal("");
  protected readonly newServerUrl = signal("");
  protected readonly capTestNotice = signal("");

  protected readonly policyRules = signal<PolicyRuleRow[]>([]);
  protected readonly newRuleCap = signal("");
  protected readonly newRuleEffect = signal("ask");

  protected readonly scopeSets = signal<ScopeSetRow[]>([]);
  protected readonly newScopeSetName = signal("");
  protected readonly newScopeSetScopes = signal("");

  protected readonly secretsList = signal<SecretMetaRow[]>([]);
  protected readonly secretName = signal("");
  protected readonly secretKey = signal("value");
  protected readonly secretVal = signal("");

  protected readonly memoryBody = signal("");
  protected readonly memoryDocs = signal<string[]>([]);

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
          policy?: { rules?: PolicyRuleRow[] };
          rules?: PolicyRuleRow[];
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
        config: url.startsWith("http")
          ? { transport: "http", url }
          : { transport: "stdio", command: url },
      });
      this.newServerId.set("");
      this.newServerUrl.set("");
      await this.loadCapabilities();
      this.snackbar.show(`Saved server ${id}`);
    } catch (err) {
      this.state.error.set(errText(err));
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
      this.state.error.set(errText(err));
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
      this.state.error.set(errText(err));
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
      this.state.error.set(errText(err));
    }
  }

  protected addPolicyRule(): void {
    const capability = this.newRuleCap().trim();
    if (!capability) return;
    this.policyRules.update((r) => [
      ...r,
      { capability, effect: this.newRuleEffect() },
    ]);
    this.newRuleCap.set("");
  }

  protected removePolicyRule(idx: number): void {
    this.policyRules.update((r) => r.filter((_, i) => i !== idx));
  }

  protected async previewCapPolicy(): Promise<void> {
    try {
      const res = await this.api.post<{ summary?: string; matched?: number }>(
        "POST /api/capability-policy/preview",
        undefined,
        { policy: { rules: this.policyRules() } },
      );
      this.capTestNotice.set(
        res?.summary ??
          `Policy preview valid (${this.policyRules().length} rules)`,
      );
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  protected async saveCapPolicy(): Promise<void> {
    try {
      await this.api.put("PUT /api/capability-policy", undefined, {
        policy: { rules: this.policyRules() },
      });
      this.snackbar.show("Capability policy saved");
    } catch (err) {
      this.state.error.set(errText(err));
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
      this.state.error.set(errText(err));
    }
  }

  protected async putSecret(): Promise<void> {
    try {
      await this.api.put(
        "PUT /api/secrets/{secret}/{key}",
        { secret: this.secretName().trim(), key: this.secretKey().trim() },
        { value: this.secretVal().trim() },
      );
      this.secretVal.set("");
      await this.loadCapabilities();
      this.snackbar.show("Secret stored");
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  private async loadMemory(): Promise<void> {
    if (this.access.can("GET /api/memory")) {
      try {
        const m = await this.api.get<
          string | { content?: string; memory?: string }
        >("GET /api/memory");
        this.memoryBody.set(
          typeof m === "string" ? m : (m?.content ?? m?.memory ?? ""),
        );
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
        content: this.memoryBody(),
      });
      this.snackbar.show("Memory updated");
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }
}
