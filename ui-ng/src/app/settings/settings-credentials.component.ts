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
  type ElementRef,
  type OnInit,
  ViewEncapsulation,
  computed,
  inject,
  signal,
  viewChild,
} from "@angular/core";
import { GrainApi } from "../api/grain-api.service";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import {
  ConfirmDialogComponent,
  type ConfirmRequest,
} from "../shared/confirm-dialog.component";
import { SettingsStateService } from "./settings-state.service";

// SecretEntry is one secret as GET /api/secrets lists it: names and keys
// only, never a value (pkg/ui/secrets.go secretEntry).
export interface SecretEntry {
  name: string;
  keys?: string[];
  capabilityServers?: string[];
  orphaned?: boolean;
}

interface SecretsResponse {
  enabled?: boolean;
  secrets?: SecretEntry[];
  total?: number;
  truncated?: boolean;
}

// One row of the secrets table: a key of a secret.
interface SecretKeyRow {
  secret: string;
  key: string;
  // keyCount is how many keys the secret holds, so removing its last key
  // removes the secret, as `grain secrets delete` would.
  keyCount: number;
  servers: string[];
  orphaned: boolean;
}

// claimedSecret is a secret a control elsewhere in Settings owns: a
// model connection's credential (Settings → Models), the Hub
// registration token (Settings → Hub) and a repository's release
// credentials (its Releases tab). Those are left out of this
// list rather than shown twice. Everything else stays listed, including
// what retired in-process capabilities left behind (github-app,
// registry-push, cosign-*), which nothing else shows.
export function claimedSecret(name: string, backendIds: string[]): boolean {
  if (name.startsWith("repo-release-")) return true;
  if (name === "registry-registration-token") return true;
  return backendIds.some((id) => name === `agent-backend-${id}`);
}

// SettingsCredentialsComponent is the Deployment credentials card at the
// foot of Settings → Capabilities, ported from the classic
// CredentialsSection and SecretsPanel: the GitHub host, the GCP project
// and legacy service-account pool, and the deployment's secrets. The
// fields stage into the page's save bar (settings:write, and locked
// while settings are managed in the config repository); the secrets save
// at once through /api/secrets (secrets:write), which stays writable
// when settings are locked.
@Component({
  selector: "g-settings-credentials",
  imports: [ConfirmDialogComponent],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  styles: `
    .cred-secret-value {
      letter-spacing: 2px;
      color: var(--on-surface-variant);
    }
    .cred-secret-form {
      display: grid;
      grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) minmax(0, 2fr);
      gap: 12px;
    }
    @media (max-width: 720px) {
      .cred-secret-form {
        grid-template-columns: minmax(0, 1fr);
      }
    }
    .cred-secret-form .s-textarea {
      min-height: 40px;
    }
  `,
  template: `
    <div
      class="s-card"
      id="credentials"
      data-testid="card-deployment-credentials"
      aria-labelledby="credentials-title"
      role="region"
    >
      <div class="s-card-head">
        <div>
          <h3 class="s-card-title" id="credentials-title">
            Deployment credentials
          </h3>
          <p class="s-card-desc">
            Settings shared across the deployment — the GitHub host grain talks
            to, and the GCP project and legacy service-account pool seeded onto
            the <code>gcp</code> capability server's <code>gcp-key</code> and
            <code>gemini-key</code> methods — and the secrets grain stores.
          </p>
        </div>
      </div>

      @if (readOnlyNote(); as note) {
        <div
          class="s-banner readonly"
          role="status"
          data-testid="credentials-read-only"
        >
          <span>{{ note }}</span>
        </div>
      }

      <div class="s-grid">
        <div class="s-field">
          <label class="s-label" for="cred-github-host">GitHub host</label>
          <input
            id="cred-github-host"
            class="s-input s-mono"
            type="text"
            autocomplete="off"
            spellcheck="false"
            data-testid="input-cred-github-host"
            placeholder="github.com"
            [value]="state.val('githubHost', '')"
            [disabled]="!canWriteFields()"
            (input)="state.setField('githubHost', $any($event.target).value)"
          />
          <span class="s-hint"
            >The GitHub grain talks to for pull requests, webhooks and the merge
            queue: a host name with an optional :port, such as github.com or
            ghe.example.com:8443 — not a URL.</span
          >
        </div>
        <div class="s-field">
          <label class="s-label" for="cred-gcp-project">GCP project</label>
          <input
            id="cred-gcp-project"
            class="s-input s-mono"
            type="text"
            autocomplete="off"
            spellcheck="false"
            data-testid="input-cred-gcp-project"
            placeholder="my-project-123"
            [value]="state.val('gcpProject', '')"
            [disabled]="!canWriteFields()"
            (input)="state.setField('gcpProject', $any($event.target).value)"
          />
          <span class="s-hint"
            >Optional. Used for IAP reconciliation and to seed migrated
            <code>gcp-key</code> and <code>gemini-key</code> settings; live
            permission sets are configured on the <code>gcp</code> capability
            server.</span
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
            data-testid="input-cred-gcp-sa"
            placeholder="grain-agent&#64;my-project-123.iam.gserviceaccount.com"
            [value]="state.val('gcpServiceAccountEmail', '')"
            [disabled]="!canWriteFields()"
            (input)="
              state.setField(
                'gcpServiceAccountEmail',
                $any($event.target).value
              )
            "
          />
          <span class="s-hint"
            >Optional. The legacy agent service account migrated
            <code>gcp-key</code> pools mint keys for. Its minter key is the
            <code>gcp-key-minter</code> secret (key <code>value</code>) under
            <a href="#other-secrets" (click)="jumpToSecrets($event)">Secrets</a>
            below.</span
          >
        </div>
        <div class="s-field">
          <label class="s-label" for="cred-gcp-count"
            >GCP service account count</label
          >
          <input
            id="cred-gcp-count"
            class="s-input s-num"
            type="number"
            min="1"
            step="1"
            inputmode="numeric"
            data-testid="input-cred-gcp-count"
            [value]="countText()"
            [disabled]="!canWriteFields()"
            [attr.aria-invalid]="countError() ? 'true' : null"
            [attr.aria-describedby]="
              countError() ? 'cred-gcp-count-error' : 'cred-gcp-count-hint'
            "
            (input)="onCountInput($event)"
          />
          @if (countError(); as err) {
            <span
              class="s-field-error"
              id="cred-gcp-count-error"
              data-testid="cred-gcp-count-error"
              >{{ err }}</span
            >
          } @else {
            <span class="s-hint" id="cred-gcp-count-hint"
              >How many service accounts to mint from (default 1). Above 1,
              grain uses &lt;prefix&gt;-0&#64;…, &lt;prefix&gt;-1&#64;… in
              turn.</span
            >
          }
        </div>
      </div>
      <div class="s-toggle-row">
        <div class="s-toggle-copy">
          <span class="s-toggle-name">Speak plain HTTP to GitHub host</span>
          <span class="s-hint"
            >For a test or mock GitHub only; never for a real one.</span
          >
        </div>
        <button
          type="button"
          class="g-switch"
          role="switch"
          data-testid="switch-cred-github-insecure"
          aria-label="Speak plain HTTP to GitHub host"
          [attr.aria-checked]="Boolean(state.val('githubInsecureHttp', false))"
          [disabled]="!canWriteFields()"
          (click)="
            state.setField(
              'githubInsecureHttp',
              !state.val('githubInsecureHttp', false)
            )
          "
        ></button>
      </div>

      <div class="s-subsection s-anchor" id="other-secrets">
        <div class="s-subhead">
          <h4 class="s-subhead-title" id="other-secrets-title">Secrets</h4>
        </div>
        <p class="s-card-desc">
          Everything in this deployment's secrets store that nothing else in
          Settings claims — a model connection's credential is set on it under
          Models. Names and keys only: a value can be set, replaced and removed
          here, and is never shown or read back.
        </p>

        @if (!canListSecrets()) {
          <span class="s-hint" data-testid="secrets-list-restricted">{{
            access.deniedReason("GET /api/secrets")
          }}</span>
        } @else if (!secretsEnabled()) {
          <span class="s-hint" data-testid="secrets-store-disabled"
            >This server has no secret store configured, so nothing can be
            stored here yet.</span
          >
        } @else if (loaded()) {
          @if (rows().length === 0) {
            <p class="s-hint" data-testid="secrets-empty">
              No other secrets are stored.
            </p>
          } @else {
            <div class="s-table-wrap s-table-stack-wrap">
              <table
                class="s-table s-table-stack"
                aria-labelledby="other-secrets-title"
                data-testid="secrets-table"
              >
                <thead>
                  <tr>
                    <th>Secret</th>
                    <th>Key</th>
                    <th>Value</th>
                    <th>Used by</th>
                    <th class="s-actions-cell">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  @for (row of rows(); track row.secret + "/" + row.key) {
                    <tr
                      [attr.data-testid]="
                        'secret-row-' + row.secret + '-' + row.key
                      "
                    >
                      <td class="s-mono">
                        {{ row.secret }}
                        @if (row.orphaned) {
                          <span
                            class="status-pill blast"
                            title="Grain made this for a capability server that no configured server uses any more"
                            >orphaned</span
                          >
                        }
                      </td>
                      <td class="s-mono" data-label="Key">{{ row.key }}</td>
                      <td data-label="Value">
                        <span class="cred-secret-value" aria-hidden="true"
                          >••••••••</span
                        >
                        <span class="sr-only">hidden</span>
                      </td>
                      <td data-label="Used by">
                        @if (row.servers.length > 0) {
                          <span class="s-mono">{{
                            row.servers.join(", ")
                          }}</span>
                        } @else {
                          <span class="s-hint">—</span>
                        }
                      </td>
                      <td class="s-actions-cell">
                        @if (row.servers.length > 0) {
                          <span
                            class="s-hint"
                            [attr.data-testid]="
                              'secret-server-owned-' + row.secret
                            "
                            >Set on its capability server</span
                          >
                        } @else {
                          <div class="s-row-actions">
                            <button
                              type="button"
                              class="btn-tonal s-btn-sm"
                              [attr.aria-label]="
                                'Replace ' + row.secret + '/' + row.key
                              "
                              [disabled]="!canWriteSecrets()"
                              [attr.title]="secretsWriteReason() || null"
                              (click)="startReplace(row)"
                            >
                              Replace
                            </button>
                            <button
                              type="button"
                              class="btn-text s-btn-sm"
                              [attr.aria-label]="
                                'Remove ' + row.secret + '/' + row.key
                              "
                              [disabled]="!canRemove(row)"
                              [attr.title]="secretsWriteReason() || null"
                              (click)="confirmRemove(row)"
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
            @if (truncated()) {
              <span class="s-hint" data-testid="secrets-truncated"
                >Showing the first {{ listedCount() }} of {{ total() }} secrets;
                <code>grain secrets list</code> on the host shows them
                all.</span
              >
            }
          }
        }

        @if (canListSecrets() && secretsEnabled()) {
          <form
            autocomplete="off"
            class="s-subsection"
            data-testid="secret-form"
            (submit)="$event.preventDefault(); putSecret()"
          >
            <div class="s-subhead">
              <h4 class="s-subhead-title">
                {{ replacing() ? "Replace " + replacing() : "Set a secret" }}
              </h4>
            </div>
            <div class="cred-secret-form">
              <div class="s-field">
                <label class="s-label" for="cred-secret-name">Secret</label>
                <input
                  id="cred-secret-name"
                  class="s-input s-mono"
                  type="text"
                  autocomplete="off"
                  spellcheck="false"
                  data-testid="input-secret-name"
                  placeholder="github-app"
                  [value]="secretName()"
                  [disabled]="!canWriteSecrets()"
                  (input)="onNameInput($any($event.target).value)"
                />
              </div>
              <div class="s-field">
                <label class="s-label" for="cred-secret-key">Key</label>
                <input
                  id="cred-secret-key"
                  class="s-input s-mono"
                  type="text"
                  autocomplete="off"
                  spellcheck="false"
                  data-testid="input-secret-key"
                  placeholder="value"
                  [value]="secretKey()"
                  [disabled]="!canWriteSecrets()"
                  (input)="onKeyInput($any($event.target).value)"
                />
              </div>
              <div class="s-field">
                <label class="s-label" for="cred-secret-value">Value</label>
                <textarea
                  #valueBox
                  id="cred-secret-value"
                  class="s-textarea s-mono s-masked"
                  rows="1"
                  autocomplete="new-password"
                  spellcheck="false"
                  data-lpignore="true"
                  data-1p-ignore="true"
                  data-testid="input-secret-value"
                  placeholder="Secret value or PEM…"
                  aria-describedby="cred-secret-value-hint"
                  [value]="secretVal()"
                  [disabled]="!canWriteSecrets()"
                  (input)="secretVal.set($any($event.target).value)"
                ></textarea>
                <span class="s-hint" id="cred-secret-value-hint"
                  >Write-only — never shown or read back.</span
                >
              </div>
            </div>
            <div class="s-inline">
              <button
                type="submit"
                class="btn-tonal s-btn-sm"
                data-testid="store-secret-btn"
                [disabled]="
                  !secretName().trim() ||
                  !secretKey().trim() ||
                  secretVal().length === 0 ||
                  !canWriteSecrets()
                "
              >
                {{ replacing() ? "Replace secret" : "Store secret" }}
              </button>
              @if (replacing()) {
                <button
                  type="button"
                  class="btn-text s-btn-sm"
                  data-testid="cancel-replace-btn"
                  (click)="cancelReplace()"
                >
                  Cancel
                </button>
              }
            </div>
          </form>
        }
        @if (!access.can("POST /api/secrets/redact")) {
          <span class="s-hint" data-testid="secrets-redact-restricted">{{
            access.deniedReason("POST /api/secrets/redact")
          }}</span>
        }
      </div>
    </div>
    @if (confirmReq(); as req) {
      <g-confirm-dialog [request]="req" (done)="onConfirmDone($event)" />
    }
  `,
})
export class SettingsCredentialsComponent implements OnInit {
  protected readonly api = inject(GrainApi);
  protected readonly access = inject(AccessService);
  protected readonly state = inject(SettingsStateService);
  private readonly snackbar = inject(SnackbarService);

  protected readonly Boolean = Boolean;

  private readonly valueBox =
    viewChild<ElementRef<HTMLTextAreaElement>>("valueBox");

  protected readonly secrets = signal<SecretEntry[]>([]);
  // GET /api/secrets says enabled: false when the server has no secret
  // store; a PUT then 404s, so the form says so instead of offering it.
  protected readonly secretsEnabled = signal(true);
  protected readonly loaded = signal(false);
  protected readonly truncated = signal(false);
  protected readonly total = signal(0);
  protected readonly secretName = signal("");
  protected readonly secretKey = signal("value");
  protected readonly secretVal = signal("");
  // replacing is "<secret>/<key>" while the form replaces a listed key,
  // and clears once either name is edited away from it.
  protected readonly replacing = signal("");
  // countRaw is the count box's text while it holds an edit, tied to the
  // discard it was typed after so Discard puts the saved value back.
  protected readonly countRaw = signal<{ epoch: number; text: string } | null>(
    null,
  );

  protected readonly confirmReq = signal<ConfirmRequest | null>(null);

  protected readonly rows = computed<SecretKeyRow[]>(() => {
    const backends = this.state.val<unknown>("agentBackends", []);
    const ids = Array.isArray(backends)
      ? backends
          .map((b) => String((b as { id?: unknown })?.id ?? ""))
          .filter(Boolean)
      : [];
    const out: SecretKeyRow[] = [];
    for (const s of this.secrets()) {
      if (!s?.name || claimedSecret(s.name, ids)) continue;
      const keys = Array.isArray(s.keys) ? s.keys : [];
      for (const key of keys) {
        out.push({
          secret: s.name,
          key,
          keyCount: keys.length,
          servers: s.capabilityServers ?? [],
          orphaned: Boolean(s.orphaned),
        });
      }
    }
    return out;
  });

  protected readonly listedCount = computed(() => this.secrets().length);

  ngOnInit(): void {
    void this.load();
  }

  protected canWriteFields(): boolean {
    return (
      this.access.can("POST /api/settings") &&
      !this.state.sectionLocked("capabilities")
    );
  }

  protected canListSecrets(): boolean {
    return this.access.can("GET /api/secrets");
  }

  protected canWriteSecrets(): boolean {
    return (
      this.secretsEnabled() &&
      this.access.can("PUT /api/secrets/{secret}/{key}")
    );
  }

  protected canRemove(row: SecretKeyRow): boolean {
    return row.keyCount > 1
      ? this.access.can("DELETE /api/secrets/{secret}/{key}")
      : this.access.can("DELETE /api/secrets/{secret}");
  }

  protected secretsWriteReason(): string {
    return this.access.deniedReason("PUT /api/secrets/{secret}/{key}");
  }

  // readOnlyNote says which half of the card the reader cannot change,
  // and why: the fields follow settings:write and the config repository's
  // lock, the secrets secrets:write.
  protected readOnlyNote(): string {
    const locked = this.state.sectionLocked("capabilities");
    const settingsReason = this.access.deniedReason("POST /api/settings");
    const secretsReason = this.secretsWriteReason();
    if (settingsReason && secretsReason) {
      return `Read-only for you. ${settingsReason}`;
    }
    if (settingsReason) {
      return `The GitHub host and GCP settings are read-only for you. ${settingsReason}`;
    }
    if (locked) {
      return "The GitHub host and GCP settings are managed in the config repository (tables/grain_config.json). Secrets stay editable here.";
    }
    if (secretsReason) {
      return `Secrets are read-only for you. ${secretsReason}`;
    }
    return "";
  }

  protected countText(): string {
    const raw = this.countRaw();
    if (raw && raw.epoch === this.state.discardEpoch()) return raw.text;
    const n = Number(this.state.val<unknown>("gcpServiceAccountCount", 1));
    return String(Number.isFinite(n) && n > 0 ? n : 1);
  }

  protected countError(): string | null {
    return this.state.fieldErrors()["gcpServiceAccountCount"] ?? null;
  }

  protected onCountInput(ev: Event): void {
    const raw = String((ev.target as HTMLInputElement | null)?.value ?? "");
    this.countRaw.set({ epoch: this.state.discardEpoch(), text: raw });
    const n = Number(raw.trim());
    if (raw.trim() === "" || !Number.isInteger(n) || n < 1) {
      this.state.setFieldError(
        "gcpServiceAccountCount",
        "GCP service account count must be a whole number of at least 1.",
      );
      return;
    }
    this.state.setFieldError("gcpServiceAccountCount", null);
    this.state.setField("gcpServiceAccountCount", n);
  }

  protected jumpToSecrets(ev: Event): void {
    ev.preventDefault();
    document
      .getElementById("other-secrets")
      ?.scrollIntoView?.({ behavior: "smooth", block: "start" });
  }

  protected onNameInput(v: string): void {
    this.secretName.set(v);
    this.dropReplaceIfEdited();
  }

  protected onKeyInput(v: string): void {
    this.secretKey.set(v);
    this.dropReplaceIfEdited();
  }

  private dropReplaceIfEdited(): void {
    if (
      this.replacing() &&
      this.replacing() !==
        `${this.secretName().trim()}/${this.secretKey().trim()}`
    ) {
      this.replacing.set("");
    }
  }

  protected startReplace(row: SecretKeyRow): void {
    this.secretName.set(row.secret);
    this.secretKey.set(row.key);
    this.secretVal.set("");
    this.replacing.set(`${row.secret}/${row.key}`);
    const box = this.valueBox()?.nativeElement;
    box?.focus();
    box?.scrollIntoView?.({ block: "nearest" });
  }

  protected cancelReplace(): void {
    this.replacing.set("");
    this.secretName.set("");
    this.secretKey.set("value");
    this.secretVal.set("");
  }

  protected async onConfirmDone(accepted: boolean): Promise<void> {
    const req = this.confirmReq();
    this.confirmReq.set(null);
    if (!accepted || !req) return;
    await req.run();
  }

  // Removing asks first: nothing here can read a value back, so putting
  // it back means pasting it again.
  protected confirmRemove(row: SecretKeyRow): void {
    const last = row.keyCount <= 1;
    this.confirmReq.set({
      title: last ? "Remove secret?" : "Remove secret key?",
      message: last
        ? `Remove the secret ${row.secret}? Its value cannot be read back, so putting it back means pasting it again.`
        : `Remove ${row.secret}/${row.key}? The rest of ${row.secret} stays. The value cannot be read back, so putting it back means pasting it again.`,
      accept: "Remove",
      destructive: true,
      run: () => this.remove(row),
    });
  }

  private async remove(row: SecretKeyRow): Promise<void> {
    try {
      if (row.keyCount <= 1) {
        await this.api.delete("DELETE /api/secrets/{secret}", {
          secret: row.secret,
        });
      } else {
        await this.api.delete("DELETE /api/secrets/{secret}/{key}", {
          secret: row.secret,
          key: row.key,
        });
      }
      if (this.replacing() === `${row.secret}/${row.key}`) {
        this.cancelReplace();
      }
      await this.load();
      this.snackbar.show(
        row.keyCount <= 1
          ? `Removed ${row.secret}`
          : `Removed ${row.secret}/${row.key}`,
      );
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async putSecret(): Promise<void> {
    const secret = this.secretName().trim();
    const key = this.secretKey().trim();
    if (!secret || !key || this.secretVal().length === 0) return;
    const replaced = Boolean(this.replacing());
    try {
      await this.api.put(
        "PUT /api/secrets/{secret}/{key}",
        { secret, key },
        { value: this.secretVal() },
      );
      this.secretVal.set("");
      this.secretName.set("");
      this.secretKey.set("value");
      this.replacing.set("");
      await this.load();
      this.snackbar.show(
        replaced ? `Replaced ${secret}/${key}` : `Stored ${secret}/${key}`,
      );
    } catch (err) {
      this.state.recordError(err);
    }
  }

  private async load(): Promise<void> {
    if (!this.canListSecrets()) return;
    try {
      const res = await this.api.get<SecretsResponse | SecretEntry[]>(
        "GET /api/secrets",
      );
      const list = Array.isArray(res) ? res : (res?.secrets ?? []);
      this.secrets.set(list);
      this.secretsEnabled.set(Array.isArray(res) || res?.enabled !== false);
      this.truncated.set(!Array.isArray(res) && Boolean(res?.truncated));
      this.total.set(
        Array.isArray(res) ? list.length : (res?.total ?? list.length),
      );
      this.loaded.set(true);
    } catch (err) {
      this.state.recordError(err);
    }
  }
}
