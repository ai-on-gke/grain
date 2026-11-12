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
  type PillOption,
  PillSelectComponent,
} from "../shared/pill-select.component";
import type { SettingsSectionId } from "./settings-sections";
import { SettingsStateService, errText } from "./settings-state.service";

interface PrincipalRow {
  entry: string;
  role: string;
  label?: string;
  addedBy?: string;
}

interface AccessRoleChoiceDto {
  name: string;
  description?: string;
  builtin?: boolean;
  person?: boolean;
  denies?: boolean;
}

interface ExternalTokenRow {
  id: string;
  name: string;
  tokenPrefix?: string;
  prefix?: string;
  owner?: string;
  scopeSet?: string;
  scopes?: string[];
  revokedAt?: string | null;
  revoked?: boolean;
  expiresAt?: string | null;
  active?: boolean;
}

interface GcpKeyRow {
  name: string;
  projectId?: string;
  serviceAccountEmail?: string;
  serviceAccountCount?: number;
  maxKeyAge?: string;
  minterCredential?: string;
}

interface AuditEventRow {
  id: string;
  at: string;
  actor: string;
  kind: "config" | "secret" | "access";
  action: string;
  detail: string;
}

const DEFAULT_ROLE_OPTIONS: readonly PillOption[] = [
  { value: "grain/operator", label: "grain/operator (full)" },
  { value: "grain/admin", label: "grain/admin" },
  { value: "grain/developer", label: "grain/developer" },
  { value: "grain/viewer", label: "grain/viewer (read-only)" },
];

@Component({
  selector: "g-settings-security",
  imports: [PillSelectComponent, ConfirmDialogComponent],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @switch (section()) {
      @case ("access") {
        <div class="s-card" data-testid="card-rbac-principals">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">RBAC allowlist &amp; roles</h3>
              <p class="s-card-desc">
                Assign signed-in users, groups, or OIDC subjects to Grain roles.
                Access stays outside the config repo so a bad PR cannot lock
                operators out.
              </p>
            </div>
            <span class="status-pill neutral tabular"
              >Your role: {{ access.roleLabel() || "operator" }}</span
            >
          </div>

          @if (!access.can("GET /api/access/principals")) {
            <div class="s-banner warn" data-testid="principals-restricted">
              {{ access.deniedReason("GET /api/access/principals") }}
            </div>
          } @else {
            @if (principals().length > 0) {
              <div class="s-table-wrap">
                <table class="s-table" aria-label="Access principals">
                  <thead>
                    <tr>
                      <th>Principal</th>
                      <th>Role</th>
                      <th>Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    @for (p of principals(); track p.entry) {
                      <tr [attr.data-testid]="'principal-row-' + p.entry">
                        <td class="tabular">
                          {{ p.entry }}{{ p.label ? " (" + p.label + ")" : "" }}
                        </td>
                        <td>
                          <g-pill-select
                            [testid]="'role-select-' + p.entry"
                            [ariaLabel]="'Role for ' + p.entry"
                            align="left"
                            [options]="roleOptions()"
                            [value]="p.role"
                            [disabled]="
                              !access.can(
                                'PATCH /api/access/principals/{entry}'
                              )
                            "
                            (valueChange)="updatePrincipalRole(p.entry, $event)"
                          />
                        </td>
                        <td>
                          <button
                            type="button"
                            class="btn-text s-btn-sm"
                            [attr.data-testid]="'remove-principal-' + p.entry"
                            [disabled]="
                              !access.can(
                                'DELETE /api/access/principals/{entry}'
                              )
                            "
                            (click)="removePrincipal(p.entry)"
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
                <label class="s-label" for="pr-entry"
                  >Email, group, or subject</label
                >
                <input
                  id="pr-entry"
                  class="s-input tabular"
                  type="text"
                  data-testid="input-principal-entry"
                  placeholder="alice@example.com"
                  [value]="newPrincipalEntry()"
                  [disabled]="!access.can('POST /api/access/principals')"
                  (input)="newPrincipalEntry.set($any($event.target).value)"
                />
              </div>
              <div class="s-field">
                <span class="s-label">Role</span>
                <g-pill-select
                  testid="select-new-principal-role"
                  ariaLabel="New principal role"
                  align="left"
                  [options]="roleOptions()"
                  [value]="newPrincipalRole()"
                  [disabled]="!access.can('POST /api/access/principals')"
                  (valueChange)="newPrincipalRole.set($event)"
                />
              </div>
              <div class="s-field">
                <label class="s-label" for="pr-reason"
                  >Reason (audit — required for operator/veto changes)</label
                >
                <input
                  id="pr-reason"
                  class="s-input"
                  type="text"
                  data-testid="input-principal-reason"
                  placeholder="Onboard or update team member"
                  [value]="newPrincipalReason()"
                  [disabled]="
                    !access.can('POST /api/access/principals') &&
                    !access.can('PATCH /api/access/principals/{entry}') &&
                    !access.can('DELETE /api/access/principals/{entry}')
                  "
                  (input)="newPrincipalReason.set($any($event.target).value)"
                />
              </div>
            </div>
            <div class="s-inline">
              <button
                type="button"
                class="btn-primary"
                data-testid="add-principal-btn"
                [disabled]="
                  !newPrincipalEntry().trim() ||
                  !access.can('POST /api/access/principals')
                "
                (click)="addPrincipal()"
              >
                Grant access
              </button>
            </div>
          }
        </div>

        <div class="s-card" data-testid="card-external-agent-tokens">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">External agent tokens (gma_)</h3>
              <p class="s-card-desc">
                Scoped bearer tokens for external CI/CLI agents calling this
                Grain API.
              </p>
            </div>
            @if (
              tokens().length > 0 &&
              access.can("POST /api/external-agents/tokens/revoke-all")
            ) {
              <button
                type="button"
                class="s-btn-danger s-btn-sm"
                data-testid="revoke-all-tokens-btn"
                (click)="revokeAllTokens()"
              >
                Revoke all
              </button>
            }
          </div>

          <div class="s-toggle-row">
            <div class="s-toggle-copy">
              <span class="s-toggle-name">Accept external agent tokens</span>
              <span class="s-hint"
                >Allow external callers bearing gma_ tokens to
                authenticate.</span
              >
            </div>
            <button
              type="button"
              class="g-switch"
              role="switch"
              data-testid="switch-external-agent-api"
              [attr.aria-checked]="Boolean(state.val('externalAgentApi', true))"
              [disabled]="!canWriteSettings()"
              aria-label="Accept external agent tokens"
              (click)="
                state.setField(
                  'externalAgentApi',
                  !state.val('externalAgentApi', true)
                )
              "
            ></button>
          </div>

          @if (mintedTokenSecret(); as secret) {
            <div class="s-banner ok" data-testid="minted-gma-token">
              <span class="tabular"
                >Copy token now (shown once):
                <strong>{{ secret }}</strong></span
              >
              <button
                type="button"
                class="btn-text s-btn-sm"
                (click)="mintedTokenSecret.set(null)"
              >
                Dismiss
              </button>
            </div>
          }

          @if (tokens().length > 0) {
            <div class="s-table-wrap">
              <table class="s-table" aria-label="External agent tokens">
                <thead>
                  <tr>
                    <th>Name</th>
                    <th>Scope set</th>
                    <th>State</th>
                    <th>Actions</th>
                  </tr>
                </thead>
                <tbody>
                  @for (tok of tokens(); track tok.id) {
                    @let tokState = tokenStatus(tok);
                    <tr [attr.data-testid]="'gma-token-row-' + tok.id">
                      <td class="tabular">{{ tok.name }}</td>
                      <td class="tabular">{{ tok.scopeSet || "custom" }}</td>
                      <td>
                        <span
                          [class]="
                            'status-pill ' +
                            (tokState === 'active'
                              ? 'healthy'
                              : tokState === 'revoked'
                                ? 'error'
                                : 'neutral')
                          "
                          [attr.data-testid]="'gma-token-status-' + tok.id"
                        >
                          {{ tokState }}
                        </span>
                      </td>
                      <td>
                        <div class="s-inline">
                          <button
                            type="button"
                            class="btn-tonal s-btn-sm"
                            [attr.data-testid]="'rotate-token-' + tok.id"
                            [disabled]="
                              !access.can(
                                'POST /api/external-agents/tokens/{id}/rotate'
                              )
                            "
                            (click)="rotateToken(tok.id)"
                          >
                            Rotate
                          </button>
                          <button
                            type="button"
                            class="btn-tonal s-btn-sm"
                            [attr.data-testid]="'revoke-token-' + tok.id"
                            [disabled]="
                              !access.can(
                                'POST /api/external-agents/tokens/{id}/revoke'
                              ) || tokState !== 'active'
                            "
                            (click)="revokeToken(tok.id)"
                          >
                            Revoke
                          </button>
                          <button
                            type="button"
                            class="btn-text s-btn-sm"
                            [disabled]="
                              !access.can(
                                'DELETE /api/external-agents/tokens/{id}'
                              )
                            "
                            (click)="deleteToken(tok.id)"
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
              <label class="s-label" for="tk-name">Token name</label>
              <input
                id="tk-name"
                class="s-input tabular"
                type="text"
                data-testid="input-gma-token-name"
                placeholder="ci-bot"
                [value]="newTokenName()"
                [disabled]="!access.can('POST /api/external-agents/tokens')"
                (input)="newTokenName.set($any($event.target).value)"
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="tk-scopes"
                >Scopes (comma-separated)</label
              >
              <input
                id="tk-scopes"
                class="s-input tabular"
                type="text"
                data-testid="input-gma-token-scopes"
                placeholder="tasks:read,tasks:write"
                [value]="newTokenScopes()"
                [disabled]="!access.can('POST /api/external-agents/tokens')"
                (input)="newTokenScopes.set($any($event.target).value)"
              />
            </div>
          </div>
          <div class="s-inline">
            <button
              type="button"
              class="btn-tonal"
              data-testid="mint-gma-token-btn"
              [disabled]="
                !newTokenName().trim() ||
                !access.can('POST /api/external-agents/tokens')
              "
              (click)="mintToken()"
            >
              Mint gma_ token
            </button>
          </div>
        </div>

        <div class="s-card" data-testid="card-gcp-keys">
          <h3 class="s-card-title">GCP service account keys</h3>
          <div class="s-grid">
            <div class="s-field">
              <label class="s-label" for="gcp-proj"
                >Default GCP project ID</label
              >
              <input
                id="gcp-proj"
                class="s-input tabular"
                type="text"
                data-testid="input-default-gcp-project"
                [value]="state.val('gcpProject', '')"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField('gcpProject', $any($event.target).value)
                "
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="gcp-sa"
                >Default GCP service account email</label
              >
              <input
                id="gcp-sa"
                class="s-input tabular"
                type="text"
                data-testid="input-default-gcp-sa"
                [value]="state.val('gcpServiceAccountEmail', '')"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField(
                    'gcpServiceAccountEmail',
                    $any($event.target).value
                  )
                "
              />
            </div>
          </div>
          @if (gcpKeys().length > 0) {
            <div class="s-inline">
              @for (gk of gcpKeys(); track gk.name) {
                <span
                  class="status-pill neutral tabular"
                  [attr.data-testid]="'gcp-key-pill-' + gk.name"
                >
                  {{ gk.name }}{{ gk.projectId ? " · " + gk.projectId : "" }}
                  <button
                    type="button"
                    class="btn-text s-btn-sm"
                    style="padding: 0 4px; min-width: 24px; min-height: 24px"
                    [attr.aria-label]="'Delete GCP key ' + gk.name"
                    [attr.data-testid]="'delete-gcp-key-' + gk.name"
                    [disabled]="!access.can('DELETE /api/gcp-keys/{name}')"
                    (click)="deleteGcpKey(gk.name)"
                  >
                    ×
                  </button>
                </span>
              }
            </div>
          }
          <div class="s-grid">
            <div class="s-field">
              <label class="s-label" for="gcp-kname">Key alias</label>
              <input
                id="gcp-kname"
                class="s-input tabular"
                type="text"
                data-testid="input-gcp-key-name"
                placeholder="vertex-runner"
                [value]="newGcpKeyName()"
                [disabled]="!access.can('POST /api/gcp-keys')"
                (input)="newGcpKeyName.set($any($event.target).value)"
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="gcp-kproj">Project ID</label>
              <input
                id="gcp-kproj"
                class="s-input tabular"
                type="text"
                data-testid="input-gcp-key-project"
                placeholder="my-gcp-project"
                [value]="newGcpKeyProject()"
                [disabled]="!access.can('POST /api/gcp-keys')"
                (input)="newGcpKeyProject.set($any($event.target).value)"
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="gcp-ksa">Service account email</label>
              <input
                id="gcp-ksa"
                class="s-input tabular"
                type="email"
                data-testid="input-gcp-key-sa"
                placeholder="runner@my-gcp-project.iam.gserviceaccount.com"
                [value]="newGcpKeySaEmail()"
                [disabled]="!access.can('POST /api/gcp-keys')"
                (input)="newGcpKeySaEmail.set($any($event.target).value)"
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="gcp-kcnt"
                >Account pool count (optional)</label
              >
              <input
                id="gcp-kcnt"
                class="s-input tabular"
                type="number"
                min="0"
                data-testid="input-gcp-key-count"
                [value]="newGcpKeyCount()"
                [disabled]="!access.can('POST /api/gcp-keys')"
                (input)="newGcpKeyCount.set($any($event.target).value)"
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="gcp-kage"
                >Max key age (Go duration, optional)</label
              >
              <input
                id="gcp-kage"
                class="s-input tabular"
                type="text"
                data-testid="input-gcp-key-max-age"
                placeholder="24h"
                [value]="newGcpKeyMaxAge()"
                [disabled]="!access.can('POST /api/gcp-keys')"
                (input)="newGcpKeyMaxAge.set($any($event.target).value)"
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="gcp-kminter"
                >Minter credential secret (optional)</label
              >
              <input
                id="gcp-kminter"
                class="s-input tabular"
                type="text"
                data-testid="input-gcp-key-minter"
                placeholder="gcp-minter"
                [value]="newGcpKeyMinter()"
                [disabled]="!access.can('POST /api/gcp-keys')"
                (input)="newGcpKeyMinter.set($any($event.target).value)"
              />
            </div>
          </div>
          <div class="s-inline">
            <button
              type="button"
              class="btn-tonal s-btn-sm"
              data-testid="save-gcp-key-btn"
              [disabled]="
                !newGcpKeyName().trim() ||
                !newGcpKeyProject().trim() ||
                !newGcpKeySaEmail().trim() ||
                !access.can('POST /api/gcp-keys')
              "
              (click)="saveGcpKey()"
            >
              Save GCP key
            </button>
          </div>
        </div>
      }

      @case ("audit") {
        <div class="s-card" data-testid="card-audit-viewer">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Audit log viewer</h3>
              <p class="s-card-desc">
                Attributed operator changes across settings, secrets, and RBAC
                principals.
              </p>
            </div>
            <button
              type="button"
              class="btn-tonal s-btn-sm"
              data-testid="refresh-audit-btn"
              (click)="loadAuditLogs()"
            >
              Refresh
            </button>
          </div>

          <div class="s-inline">
            <input
              class="s-input s-input-inline"
              type="search"
              aria-label="Filter by actor or action"
              data-testid="audit-actor-filter"
              placeholder="Filter actor or action…"
              [value]="auditFilterText()"
              (input)="auditFilterText.set($any($event.target).value)"
            />
            <g-pill-select
              testid="audit-kind-filter"
              ariaLabel="Audit log category"
              align="left"
              [options]="auditKindOptions"
              [value]="auditKindFilter()"
              (valueChange)="auditKindFilter.set($event)"
            />
            <g-pill-select
              testid="audit-time-filter"
              ariaLabel="Audit time window"
              align="left"
              [options]="auditTimeOptions"
              [value]="auditTimeFilter()"
              (valueChange)="auditTimeFilter.set($event)"
            />
          </div>

          @if (filteredAuditEvents().length === 0) {
            <p class="s-card-desc" data-testid="audit-empty">
              No matching audit events in this window.
            </p>
          } @else {
            <div class="s-table-wrap">
              <table class="s-table" aria-label="Audit events">
                <thead>
                  <tr>
                    <th>Timestamp</th>
                    <th>Category</th>
                    <th>Actor</th>
                    <th>Action</th>
                    <th>Detail</th>
                  </tr>
                </thead>
                <tbody>
                  @for (ev of filteredAuditEvents(); track ev.id) {
                    <tr data-testid="audit-row">
                      <td class="tabular">{{ ev.at }}</td>
                      <td>
                        <span class="status-pill neutral">{{ ev.kind }}</span>
                      </td>
                      <td class="tabular">{{ ev.actor }}</td>
                      <td class="tabular">{{ ev.action }}</td>
                      <td>{{ ev.detail }}</td>
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
export class SettingsSecurityComponent implements OnInit, OnChanges {
  protected readonly api = inject(GrainApi);
  protected readonly access = inject(AccessService);
  protected readonly state = inject(SettingsStateService);
  private readonly snackbar = inject(SnackbarService);

  readonly section = input.required<SettingsSectionId>();

  protected readonly Boolean = Boolean;

  protected readonly confirmRequest = signal<ConfirmRequest | null>(null);
  protected readonly roleOptions =
    signal<readonly PillOption[]>(DEFAULT_ROLE_OPTIONS);
  protected readonly auditKindOptions: readonly PillOption[] = [
    { value: "all", label: "All categories" },
    { value: "config", label: "Config changes" },
    { value: "secret", label: "Secret operations" },
    { value: "access", label: "Access & principals" },
  ];
  protected readonly auditTimeOptions: readonly PillOption[] = [
    { value: "all", label: "All time" },
    { value: "24h", label: "Last 24 hours" },
    { value: "7d", label: "Last 7 days" },
    { value: "30d", label: "Last 30 days" },
  ];

  protected readonly principals = signal<PrincipalRow[]>([]);
  protected readonly newPrincipalEntry = signal("");
  protected readonly newPrincipalRole = signal("grain/developer");
  protected readonly newPrincipalReason = signal("");

  protected readonly tokens = signal<ExternalTokenRow[]>([]);
  protected readonly newTokenName = signal("");
  protected readonly newTokenScopes = signal("tasks:read,tasks:write");
  protected readonly mintedTokenSecret = signal<string | null>(null);

  protected readonly gcpKeys = signal<GcpKeyRow[]>([]);
  protected readonly newGcpKeyName = signal("");
  protected readonly newGcpKeyProject = signal("");
  protected readonly newGcpKeySaEmail = signal("");
  protected readonly newGcpKeyCount = signal("");
  protected readonly newGcpKeyMaxAge = signal("");
  protected readonly newGcpKeyMinter = signal("");

  protected readonly auditEvents = signal<AuditEventRow[]>([]);
  protected readonly auditFilterText = signal("");
  protected readonly auditKindFilter = signal("all");
  protected readonly auditTimeFilter = signal("all");

  protected readonly filteredAuditEvents = computed(() => {
    const q = this.auditFilterText().trim().toLowerCase();
    const kind = this.auditKindFilter();
    const win = this.auditTimeFilter();
    const now = Date.now();
    const cutoff =
      win === "24h"
        ? now - 86400_000
        : win === "7d"
          ? now - 7 * 86400_000
          : win === "30d"
            ? now - 30 * 86400_000
            : 0;
    return this.auditEvents().filter((ev) => {
      if (kind !== "all" && ev.kind !== kind) return false;
      if (cutoff > 0 && ev.at) {
        const ts = Date.parse(ev.at);
        if (!Number.isNaN(ts) && ts < cutoff) return false;
      }
      if (!q) return true;
      return `${ev.actor} ${ev.action} ${ev.detail}`.toLowerCase().includes(q);
    });
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

  protected tokenStatus(
    tok: ExternalTokenRow,
  ): "revoked" | "expired" | "active" {
    if (tok.revokedAt || tok.revoked) return "revoked";
    if (tok.active === false) return "expired";
    return "active";
  }

  protected async onConfirmDone(accepted: boolean): Promise<void> {
    const req = this.confirmRequest();
    this.confirmRequest.set(null);
    if (accepted && req) {
      await req.run();
    }
  }

  private loadForSection(): void {
    if (this.section() === "access") {
      void this.loadAccessData();
    } else if (this.section() === "audit") {
      void this.loadAuditLogs();
    }
  }

  private async loadAccessData(): Promise<void> {
    if (this.access.can("GET /api/access/principals")) {
      try {
        const p = await this.api.get<
          | PrincipalRow[]
          | {
              entries?: PrincipalRow[];
              principals?: PrincipalRow[];
              items?: PrincipalRow[];
              roles?: AccessRoleChoiceDto[];
            }
        >("GET /api/access/principals");
        if (Array.isArray(p)) {
          this.principals.set(p);
        } else {
          this.principals.set(p?.entries ?? p?.principals ?? p?.items ?? []);
          if (Array.isArray(p?.roles) && p.roles.length > 0) {
            this.roleOptions.set(
              p.roles.map((r) => ({
                value: r.name,
                label: r.description ? `${r.name} — ${r.description}` : r.name,
              })),
            );
          }
        }
      } catch {
        // Non-fatal
      }
    }
    if (this.access.can("GET /api/external-agents/tokens")) {
      try {
        const t = await this.api.get<
          | ExternalTokenRow[]
          | { tokens?: ExternalTokenRow[]; items?: ExternalTokenRow[] }
        >("GET /api/external-agents/tokens");
        this.tokens.set(Array.isArray(t) ? t : (t?.tokens ?? t?.items ?? []));
      } catch {
        // Non-fatal
      }
    }
    if (this.access.can("GET /api/gcp-keys")) {
      try {
        const g = await this.api.get<
          GcpKeyRow[] | { keys?: GcpKeyRow[]; items?: GcpKeyRow[] }
        >("GET /api/gcp-keys");
        this.gcpKeys.set(Array.isArray(g) ? g : (g?.keys ?? g?.items ?? []));
      } catch {
        // Non-fatal
      }
    }
  }

  protected async addPrincipal(): Promise<void> {
    const entry = this.newPrincipalEntry().trim();
    if (!entry) return;
    const reason = this.newPrincipalReason().trim();
    try {
      await this.api.post("POST /api/access/principals", undefined, {
        entry,
        role: this.newPrincipalRole(),
        ...(reason ? { reason } : {}),
      });
      this.newPrincipalEntry.set("");
      this.newPrincipalReason.set("");
      await this.loadAccessData();
      this.snackbar.show(`Granted ${this.newPrincipalRole()} to ${entry}`);
    } catch (err) {
      this.state.recordError(err);
    }
  }

  private isSelfPrincipal(entry: string): boolean {
    const viewer = this.access.viewer().trim().toLowerCase();
    return Boolean(viewer && entry.trim().toLowerCase() === viewer);
  }

  private isSelfConfirmRefusal(err: unknown): boolean {
    const msg = errText(err).toLowerCase();
    return (
      msg.includes("confirmself") ||
      msg.includes("matches the caller") ||
      msg.includes("your own access") ||
      msg.includes("yourself")
    );
  }

  protected async updatePrincipalRole(
    entry: string,
    role: string,
  ): Promise<void> {
    const reason = this.newPrincipalReason().trim();
    if (this.isSelfPrincipal(entry)) {
      this.promptSelfRoleConfirm(entry, role, reason);
      return;
    }
    try {
      await this.api.patch(
        "PATCH /api/access/principals/{entry}",
        { entry },
        { role, ...(reason ? { reason } : {}) },
      );
      await this.loadAccessData();
      this.snackbar.show(`Updated ${entry} to ${role}`);
    } catch (err) {
      if (this.isSelfConfirmRefusal(err)) {
        this.promptSelfRoleConfirm(entry, role, reason);
      } else {
        await this.loadAccessData();
        this.state.recordError(err);
      }
    }
  }

  private promptSelfRoleConfirm(
    entry: string,
    role: string,
    reason: string,
  ): void {
    this.confirmRequest.set({
      title: "Change your own access role?",
      message: `Changing "${entry}" to "${role}" affects your own signed-in session and may narrow or revoke your operator permissions.`,
      accept: "Confirm role change",
      destructive: true,
      run: async () => {
        try {
          await this.api.patch(
            "PATCH /api/access/principals/{entry}",
            { entry },
            {
              role,
              confirmSelf: true,
              ...(reason ? { reason } : {}),
            },
          );
          await this.loadAccessData();
          this.snackbar.show(`Updated ${entry} to ${role}`);
        } catch (err) {
          await this.loadAccessData();
          this.state.recordError(err);
        }
      },
    });
  }

  protected async removePrincipal(entry: string): Promise<void> {
    const reason = this.newPrincipalReason().trim();
    if (this.isSelfPrincipal(entry)) {
      this.promptSelfRemoveConfirm(entry, reason);
      return;
    }
    const q = reason ? `reason=${encodeURIComponent(reason)}` : undefined;
    try {
      await this.api.delete(
        "DELETE /api/access/principals/{entry}",
        { entry },
        undefined,
        q,
      );
      await this.loadAccessData();
      this.snackbar.show(`Removed ${entry}`);
    } catch (err) {
      if (this.isSelfConfirmRefusal(err)) {
        this.promptSelfRemoveConfirm(entry, reason);
      } else {
        this.state.recordError(err);
      }
    }
  }

  private promptSelfRemoveConfirm(entry: string, reason: string): void {
    this.confirmRequest.set({
      title: "Remove your own access entry?",
      message: `Removing "${entry}" revokes access matching your own signed-in identity and may lock you out immediately.`,
      accept: "Confirm self-removal",
      destructive: true,
      run: async () => {
        const params = ["confirmSelf=1"];
        if (reason) params.push(`reason=${encodeURIComponent(reason)}`);
        try {
          await this.api.delete(
            "DELETE /api/access/principals/{entry}",
            { entry },
            undefined,
            params.join("&"),
          );
          await this.loadAccessData();
          this.snackbar.show(`Removed ${entry}`);
        } catch (err) {
          this.state.recordError(err);
        }
      },
    });
  }

  protected async mintToken(): Promise<void> {
    const name = this.newTokenName().trim();
    if (!name) return;
    const scopes = this.newTokenScopes()
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean);
    try {
      const res = await this.api.post<{ token?: string }>(
        "POST /api/external-agents/tokens",
        undefined,
        { name, scopes },
      );
      this.newTokenName.set("");
      if (res?.token) this.mintedTokenSecret.set(res.token);
      await this.loadAccessData();
      this.snackbar.show(`Minted token ${name}`);
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async rotateToken(id: string): Promise<void> {
    try {
      const res = await this.api.post<{ token?: string }>(
        "POST /api/external-agents/tokens/{id}/rotate",
        { id },
        {},
      );
      if (res?.token) this.mintedTokenSecret.set(res.token);
      await this.loadAccessData();
      this.snackbar.show("Token rotated");
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async revokeToken(id: string): Promise<void> {
    try {
      await this.api.post(
        "POST /api/external-agents/tokens/{id}/revoke",
        { id },
        {},
      );
      await this.loadAccessData();
      this.snackbar.show("Token revoked");
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async revokeAllTokens(): Promise<void> {
    try {
      await this.api.post(
        "POST /api/external-agents/tokens/revoke-all",
        undefined,
        {},
      );
      await this.loadAccessData();
      this.snackbar.show("Revoked all external agent tokens");
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async deleteToken(id: string): Promise<void> {
    try {
      await this.api.delete("DELETE /api/external-agents/tokens/{id}", { id });
      await this.loadAccessData();
      this.snackbar.show("Deleted token");
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async saveGcpKey(): Promise<void> {
    const name = this.newGcpKeyName().trim();
    const projectId = this.newGcpKeyProject().trim();
    const serviceAccountEmail = this.newGcpKeySaEmail().trim();
    if (!name || !projectId || !serviceAccountEmail) return;
    const count = Math.floor(Number(this.newGcpKeyCount()) || 0);
    const maxKeyAge = this.newGcpKeyMaxAge().trim();
    const minterCredential = this.newGcpKeyMinter().trim();
    try {
      await this.api.post("POST /api/gcp-keys", undefined, {
        name,
        projectId,
        serviceAccountEmail,
        ...(count > 0 ? { serviceAccountCount: count } : {}),
        ...(maxKeyAge ? { maxKeyAge } : {}),
        ...(minterCredential ? { minterCredential } : {}),
      });
      this.newGcpKeyName.set("");
      this.newGcpKeyProject.set("");
      this.newGcpKeySaEmail.set("");
      this.newGcpKeyCount.set("");
      this.newGcpKeyMaxAge.set("");
      this.newGcpKeyMinter.set("");
      await this.loadAccessData();
      this.snackbar.show(`Saved GCP key ${name}`);
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async deleteGcpKey(name: string): Promise<void> {
    try {
      await this.api.delete("DELETE /api/gcp-keys/{name}", { name });
      await this.loadAccessData();
      this.snackbar.show(`Deleted GCP key ${name}`);
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async loadAuditLogs(): Promise<void> {
    const combined: AuditEventRow[] = [];
    const seenConfigAuditKeys = new Set<string>();

    const unwrapItems = (raw: unknown): unknown[] => {
      if (Array.isArray(raw)) return raw;
      if (!raw || typeof raw !== "object") return [];
      const rec = raw as Record<string, unknown>;
      const list =
        rec["items"] ?? rec["entries"] ?? rec["logs"] ?? rec["history"];
      return Array.isArray(list) ? list : [];
    };

    const formatActor = (r: Record<string, unknown>): string => {
      const raw = r["actor"] ?? r["principal"] ?? r["by"];
      if (raw && typeof raw === "object") {
        const p = raw as { id?: string; kind?: string };
        return p.id || p.kind || "system";
      }
      if (typeof raw === "string" && raw) return raw;
      if (typeof r["taskId"] === "string" && r["taskId"]) {
        return `task:${r["taskId"]}`;
      }
      return "system";
    };

    const norm = (
      raw: unknown,
      source: "config" | "secret" | "access",
    ): void => {
      const arr = unwrapItems(raw);
      arr.forEach((item, idx) => {
        if (!item || typeof item !== "object") return;
        const r = item as Record<string, unknown>;
        const at = String(r["createdAt"] ?? r["at"] ?? r["timestamp"] ?? "");
        const surface = typeof r["surface"] === "string" ? r["surface"] : "";
        const effectiveKind: "config" | "secret" | "access" =
          source === "access" || surface === "access" ? "access" : source;

        // Deduplicate config_audit rows shared between /api/deployment/config-audit
        // and /api/access/history (both query Store.ConfigAudit*).
        if (source === "config" || source === "access") {
          const dedupKey =
            r["id"] !== undefined
              ? `cfg-id:${String(r["id"])}`
              : `cfg-sig:${at}:${String(r["summary"] ?? r["detail"] ?? "")}`;
          if (seenConfigAuditKeys.has(dedupKey)) return;
          seenConfigAuditKeys.add(dedupKey);
        }

        const secretRef = r["secretName"]
          ? r["key"]
            ? `${String(r["secretName"])}/${String(r["key"])}`
            : String(r["secretName"])
          : "";
        const detailParts = [
          r["resource"] ? String(r["resource"]) : "",
          secretRef,
          r["credential"] ? `cred:${String(r["credential"])}` : "",
          (r["detail"] ??
          r["summary"] ??
          r["outcome"] ??
          r["target"] ??
          r["entry"])
            ? String(
                r["detail"] ??
                  r["summary"] ??
                  r["outcome"] ??
                  r["target"] ??
                  r["entry"],
              )
            : "",
        ].filter(Boolean);

        combined.push({
          id: `${effectiveKind}-${r["id"] ?? idx}-${at}-${combined.length}`,
          at,
          actor: formatActor(r),
          kind: effectiveKind,
          action: String(
            r["action"] ??
              r["event"] ??
              r["operation"] ??
              surface ??
              effectiveKind,
          ),
          detail: Array.from(new Set(detailParts)).join(" · "),
        });
      });
    };

    const jobs: Promise<void>[] = [];
    if (this.access.can("GET /api/deployment/config-audit")) {
      jobs.push(
        this.api
          .get("GET /api/deployment/config-audit", undefined, "limit=100")
          .then((r) => norm(r, "config"))
          .catch(() => undefined),
      );
    }
    if (this.access.can("GET /api/deployment/audit-logs")) {
      jobs.push(
        this.api
          .get("GET /api/deployment/audit-logs", undefined, "limit=100")
          .then((r) => norm(r, "secret"))
          .catch(() => undefined),
      );
    } else if (this.access.can("GET /api/deployment/secret-audit")) {
      jobs.push(
        this.api
          .get("GET /api/deployment/secret-audit")
          .then((r) => norm(r, "secret"))
          .catch(() => undefined),
      );
    }
    if (this.access.can("GET /api/access/history")) {
      jobs.push(
        this.api
          .get("GET /api/access/history")
          .then((r) => norm(r, "access"))
          .catch(() => undefined),
      );
    }
    await Promise.all(jobs);
    combined.sort((a, b) => (b.at || "").localeCompare(a.at || ""));
    this.auditEvents.set(combined);
  }
}
