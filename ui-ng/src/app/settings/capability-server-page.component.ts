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

import { DOCUMENT, NgTemplateOutlet } from "@angular/common";
import {
  ChangeDetectionStrategy,
  Component,
  DestroyRef,
  ViewEncapsulation,
  computed,
  effect,
  inject,
  input,
  signal,
  untracked,
} from "@angular/core";
import { Router, RouterLink } from "@angular/router";
import { HttpErrorResponse } from "@angular/common/http";
import { GrainApi } from "../api/grain-api.service";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import {
  ConfirmDialogComponent,
  type ConfirmRequest,
} from "../shared/confirm-dialog.component";
import { InfoPopoverComponent } from "../shared/info-popover.component";
import {
  type PillOption,
  PillSelectComponent,
} from "../shared/pill-select.component";
import {
  CapabilitySchemaFormComponent,
  type SecretSlot,
} from "./capability-schema-form.component";
import {
  type CapabilityHolder,
  type CapabilityServerDetail,
  type ConnectionForm,
  type FigmaAccount,
  type HolderLink,
  type HostEntries,
  type JsonSchema,
  configFromForm,
  figmaAccounts,
  figmaRedirectURI,
  formFromServer,
  holderLink,
  hostChoice,
  isFigmaServer,
  isGcpServer,
  runsAt,
  secretFieldState,
  serverName,
  SECRET_PREFIX,
} from "./capability-server";
import { capServerState, needsHostWarning } from "./settings-work.component";
import { SettingsStateService, errText } from "./settings-state.service";

interface ProbeResult {
  ok: boolean;
  error?: string;
  warnings?: string[];
  methods?: { name: string; description?: string }[];
}

interface SecretRow {
  name: string;
  keys?: string[];
  capabilityServers?: string[];
}

interface HostRow {
  name: string;
  state?: string;
}

// The state help a server's page gives under its chip (the classic
// panel's stateHelp), by status.state.
const STATE_HELP: Record<string, string> = {
  attached:
    "Connected: grain reached the server and its methods can be granted.",
  pending: "Connecting: grain is starting or reaching the server.",
  "needs-config":
    "Needs configuring: the server is up but wants its settings first (Server settings below).",
  failed:
    "Failed: grain could not start or reach the server. The error below says why.",
  disabled:
    "Disabled: grain does not connect to it, and grants of its methods are refused.",
};

// CapabilityServerPageComponent is one capability server's page,
// /settings/capabilities/<id> (the classic serverPath): what it gives a
// task, its connection (edited, renamed and tested here, with host
// placement), its secrets, its own server settings drawn from the
// schema its configure tool declares, what holds a grant on it, and Test,
// Disable and Remove. An in-process server shows only what it gives and
// who holds it, since it has nothing else.
@Component({
  selector: "g-capability-server-page",
  imports: [
    NgTemplateOutlet,
    RouterLink,
    ConfirmDialogComponent,
    InfoPopoverComponent,
    PillSelectComponent,
    CapabilitySchemaFormComponent,
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  encapsulation: ViewEncapsulation.None,
  styleUrl: "./capability-server-page.css",
  template: `
    <a
      class="btn-text s-btn-sm cap-back"
      [routerLink]="['/settings', 'capabilities']"
      fragment="capability-servers"
      data-testid="capability-server-back"
      >← All capability servers</a
    >

    @if (loading()) {
      <div class="s-card" data-testid="capability-server-loading">
        <div class="s-skel-line s-skel-title"></div>
        <div class="s-skel-line"></div>
      </div>
    } @else if (!server()) {
      <div class="s-card" data-testid="capability-server-missing">
        <h2 class="s-card-title">No such capability server</h2>
        <p class="s-card-desc">
          No configured server has the capability ID
          <code class="s-mono">{{ serverId() }}</code
          >. It may have been removed or renamed.
        </p>
      </div>
    } @else {
      @let srv = server()!;
      @let chip = chipFor(srv);
      @let locked = state.sectionLocked("capabilities");
      @let canAdmin = access.can("PUT /api/capability-servers") && !locked;

      <div
        class="s-card"
        data-testid="capability-server-page"
        [attr.data-capability-server]="srv.id"
      >
        <div class="s-card-head">
          <div class="cap-head">
            <h2 class="s-card-title" data-testid="capability-server-name">
              {{ name() }}
            </h2>
            @if (summary()) {
              <p class="s-card-desc">{{ summary() }}</p>
            }
            <p class="s-hint cap-id-line">
              Capability ID:
              <code class="s-mono" data-testid="capability-server-id">{{
                srv.id
              }}</code>
              <g-info-popover
                label="the capability ID"
                testid="info-capability-id"
                text="The name grants use for this server's methods, as <capability ID>#<method>. A built-in server's ID is grain's own and the same on every deployment."
              />
            </p>
          </div>
          <span
            [class]="'status-pill ' + chip.tone"
            data-testid="capability-server-state"
            >{{ chip.label }}</span
          >
        </div>
        @if (stateHelp(); as help) {
          <p class="s-hint" data-testid="capability-server-state-help">
            {{ help }}
          </p>
        }
        @if (!srv.inProcess) {
          <p class="s-hint" data-testid="capability-server-runs">
            Runs {{ runsAt(srv.config) }}.
          </p>
        }
        @if (srv.config.notice) {
          <div class="s-banner" data-testid="capability-server-notice">
            <span>{{ srv.config.notice }}</span>
            <button
              type="button"
              class="btn-text s-btn-sm"
              [disabled]="
                !access.can('POST /api/capability-servers/dismiss-notice') ||
                locked
              "
              (click)="dismissNotice()"
            >
              Dismiss
            </button>
          </div>
        }
        @if (srv.status?.reason) {
          <div class="s-banner" data-testid="capability-server-needs-config">
            <span>Needs configuring: {{ srv.status?.reason }}</span>
          </div>
        }
        @if (needsHost(); as w) {
          <div class="s-banner warn" data-testid="capability-server-needs-host">
            <span>{{ w }}</span>
          </div>
        }
        @if (srv.status?.error) {
          <div class="s-banner error" data-testid="capability-server-error">
            <span class="s-wrap">{{ srv.status?.error }}</span>
          </div>
        }
      </div>

      <!-- What it gives a task -->
      <div class="s-card" data-testid="capability-server-methods">
        <div class="s-card-head">
          <div>
            <h3 class="s-card-title">What it gives a task</h3>
            <p class="s-card-desc">
              Each method is one capability a task, template, schedule or CI job
              can be granted, named
              <code class="s-mono">&lt;capability ID&gt;#&lt;method&gt;</code>.
            </p>
          </div>
        </div>
        @if (methods().length) {
          <ul class="cap-methods">
            @for (m of methods(); track m.name) {
              <li [attr.data-method]="m.name">
                <code class="s-mono">{{ m.name }}</code>
                @if (m.summary) {
                  <span class="s-hint">{{ m.summary }}</span>
                }
                @if (m.more) {
                  <g-info-popover [text]="m.more" [label]="m.name" />
                }
              </li>
            }
          </ul>
        } @else {
          <p class="s-hint">
            No methods reported yet: they appear once grain has connected to the
            server.
          </p>
        }
      </div>

      <!-- Connection -->
      <div class="s-card" data-testid="capability-server-connection">
        <div class="s-card-head">
          <div>
            <h3 class="s-card-title">Connection</h3>
            <p class="s-card-desc">
              @if (srv.inProcess) {
                Built into grain and run inside its own process: there is no
                command, URL, secret or server setting to change, and it cannot
                be disabled or removed. Who may hold its methods is decided by
                grants and the capability grant policy.
              } @else {
                How grain starts or reaches this server: its command or URL,
                where it runs, and the environment or headers it is given. Saved
                to grain/config; attempts that start after a change use it.
              }
            </p>
          </div>
          @if (!srv.inProcess && !editing()) {
            <button
              type="button"
              class="btn-tonal s-btn-sm"
              data-testid="edit-connection-btn"
              [disabled]="!canAdmin"
              (click)="startEdit()"
            >
              Edit connection
            </button>
          }
        </div>

        @if (!srv.inProcess && !editing()) {
          <dl class="cap-facts">
            @if (srv.config.command) {
              <dt>Command</dt>
              <dd class="s-mono">
                {{ srv.config.command }} {{ (srv.config.args || []).join(" ") }}
              </dd>
              @if (envNames().length) {
                <dt>Environment</dt>
                <dd class="s-mono">{{ envNames().join(", ") }}</dd>
              }
            } @else {
              <dt>URL</dt>
              <dd class="s-mono">{{ srv.config.serverUrl || "—" }}</dd>
              @if (envNames().length) {
                <dt>Headers</dt>
                <dd class="s-mono">{{ envNames().join(", ") }}</dd>
              }
            }
          </dl>
        }

        @if (editing(); as f) {
          <form
            class="cap-conn-form"
            data-testid="capability-server-edit"
            autocomplete="off"
            (submit)="$event.preventDefault(); saveConnection()"
          >
            <div class="s-grid">
              <div class="s-field cap-field-wide">
                <label class="s-label" for="cs-edit-id">Capability ID</label>
                <input
                  id="cs-edit-id"
                  class="s-input s-mono"
                  data-testid="cs-edit-id"
                  spellcheck="false"
                  [value]="f.id"
                  [disabled]="isBuiltin()"
                  (input)="patch({ id: $any($event.target).value })"
                />
                <span class="s-hint">{{
                  isBuiltin()
                    ? "A built-in server's ID is grain's own and the same on every deployment."
                    : f.id.trim() !== srv.id
                      ? "Renaming: grants naming the old ID no longer resolve. Saving asks first."
                      : "The name tasks grant this server's methods by. Renaming it breaks the grants that name it."
                }}</span>
              </div>
              <div class="s-field">
                <span class="s-label" id="cs-edit-transport-l">Transport</span>
                <g-pill-select
                  align="left"
                  testid="cs-edit-transport"
                  ariaLabel="Transport"
                  [options]="transportOptions"
                  [value]="f.transport"
                  (valueChange)="patch({ transport: $any($event) })"
                />
              </div>
              @if (f.transport === "stdio") {
                <div class="s-field">
                  <label class="s-label" for="cs-edit-command">Command</label>
                  <input
                    id="cs-edit-command"
                    class="s-input s-mono"
                    data-testid="cs-edit-command"
                    spellcheck="false"
                    [value]="f.command"
                    (input)="patch({ command: $any($event.target).value })"
                  />
                </div>
                <div class="s-field">
                  <label class="s-label" for="cs-edit-args"
                    >Arguments, one per line</label
                  >
                  <textarea
                    id="cs-edit-args"
                    class="s-textarea s-mono"
                    data-testid="cs-edit-args"
                    spellcheck="false"
                    [value]="f.args"
                    (input)="patch({ args: $any($event.target).value })"
                  ></textarea>
                </div>
                <div class="s-field">
                  <label class="s-label" for="cs-edit-env"
                    >Env, NAME=VALUE per line</label
                  >
                  <textarea
                    id="cs-edit-env"
                    class="s-textarea s-mono"
                    data-testid="cs-edit-env"
                    spellcheck="false"
                    [value]="f.env"
                    (input)="patch({ env: $any($event.target).value })"
                  ></textarea>
                  <span class="s-hint"
                    >Write a credential as
                    <code class="s-mono">$secret:&lt;name&gt;</code>, or add it
                    with Bind a secret, so its value never reaches
                    grain/config.</span
                  >
                </div>
                <div class="s-field">
                  <span class="s-label cap-label-row"
                    >Run location
                    <g-info-popover
                      label="run location"
                      testid="info-run-location"
                      text="container: beside the grain daemon, in its container. host: on a host's own machine through its host agent, for a server that needs the host's tools or files. A server placed on a named host waits (needs host) while that host is not connected."
                  /></span>
                  <g-pill-select
                    align="left"
                    testid="cs-edit-run"
                    ariaLabel="Run location"
                    [options]="runOptions()"
                    [value]="f.run"
                    (valueChange)="patch({ run: $any($event) })"
                  />
                </div>
                @if (f.run === "host") {
                  <div class="s-field">
                    <span class="s-label">Host</span>
                    <g-pill-select
                      align="left"
                      testid="cs-edit-hostd"
                      ariaLabel="Host"
                      [options]="hostOptions()"
                      [value]="f.hostd"
                      (valueChange)="patch({ hostd: $event })"
                    />
                    <span class="s-hint"
                      >Which host this server runs on, for every run that holds
                      a grant of it. Run the same server on two hosts as two
                      entries under two IDs.</span
                    >
                  </div>
                }
              } @else {
                <div class="s-field">
                  <label class="s-label" for="cs-edit-url">Server URL</label>
                  <input
                    id="cs-edit-url"
                    class="s-input s-mono"
                    data-testid="cs-edit-url"
                    spellcheck="false"
                    placeholder="https://cap.example.com/mcp"
                    [value]="f.serverUrl"
                    (input)="patch({ serverUrl: $any($event.target).value })"
                  />
                </div>
                <div class="s-field">
                  <label class="s-label" for="cs-edit-headers"
                    >Headers, NAME=VALUE per line</label
                  >
                  <textarea
                    id="cs-edit-headers"
                    class="s-textarea s-mono"
                    data-testid="cs-edit-headers"
                    spellcheck="false"
                    [value]="f.headers"
                    (input)="patch({ headers: $any($event.target).value })"
                  ></textarea>
                  <span class="s-hint"
                    >Write a credential as
                    <code class="s-mono">$secret:&lt;name&gt;</code>, or add it
                    with Bind a secret, so its value never reaches
                    grain/config.</span
                  >
                </div>
              }
            </div>
            @if (formError()) {
              <span class="s-field-error" data-testid="cs-edit-error">{{
                formError()
              }}</span>
            }
            <div class="s-inline">
              <button
                type="submit"
                class="btn-primary"
                data-testid="save-connection-btn"
                [disabled]="busy() || !f.id.trim()"
              >
                Save connection
              </button>
              <button
                type="button"
                class="btn-tonal"
                data-testid="test-connection-btn"
                [disabled]="
                  busy() || !access.can('POST /api/capability-servers/test')
                "
                (click)="testConnection()"
              >
                Test
              </button>
              <button
                type="button"
                class="btn-text"
                (click)="editing.set(null)"
              >
                Cancel
              </button>
            </div>
            <p class="s-hint">
              Test starts the server from the values above, unsaved, lists what
              it offers and saves nothing.
            </p>
            @if (connProbe(); as r) {
              <ng-container
                *ngTemplateOutlet="probe; context: { $implicit: r }"
              />
            }
          </form>
        }
      </div>

      @if (!srv.inProcess) {
        <!-- Secrets -->
        <div class="s-card" data-testid="capability-server-secrets">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Secrets</h3>
              <p class="s-card-desc">
                Credentials this server is given from the encrypted secrets
                store. Its entry keeps only
                <code class="s-mono">$secret:&lt;name&gt;</code>; no value is
                ever shown or written to grain/config.
              </p>
            </div>
          </div>
          @if (srv.secrets?.length) {
            <ul class="cap-secret-refs" aria-label="Secrets this entry names">
              @for (sec of srv.secrets; track sec.name) {
                <li [attr.data-testid]="'server-secret-' + sec.name">
                  <code class="s-mono">$secret:{{ sec.name }}</code>
                  <span
                    class="status-pill"
                    [class.healthy]="sec.present"
                    [class.blast]="!sec.present"
                    >{{ sec.present ? "set" : "missing" }}</span
                  >
                </li>
              }
            </ul>
          }
          @if (ownedSecrets().length) {
            <span class="s-label">Stored for this server</span>
            <ul
              class="s-secret-list"
              aria-label="Secrets stored for this server"
            >
              @for (sec of ownedSecrets(); track sec.name) {
                @for (k of sec.keys || []; track k) {
                  <li
                    class="s-secret-row"
                    [attr.data-testid]="'owned-secret-' + sec.name + '-' + k"
                  >
                    <span class="s-mono s-secret-name"
                      >{{ sec.name }}/{{ k }}</span
                    >
                    @if (replacing() === sec.name + "/" + k) {
                      <form
                        class="cap-replace"
                        autocomplete="off"
                        (submit)="
                          $event.preventDefault(); replaceSecret(sec.name, k)
                        "
                      >
                        <input
                          class="s-input"
                          type="password"
                          autocomplete="new-password"
                          [attr.aria-label]="
                            'New value for ' + sec.name + '/' + k
                          "
                          data-testid="owned-secret-value"
                          [value]="replaceValue()"
                          (input)="replaceValue.set($any($event.target).value)"
                        />
                        <button
                          type="submit"
                          class="btn-tonal s-btn-sm"
                          data-testid="owned-secret-save"
                          [disabled]="!replaceValue() || busy()"
                        >
                          Replace
                        </button>
                        <button
                          type="button"
                          class="btn-text s-btn-sm"
                          (click)="replacing.set(null)"
                        >
                          Cancel
                        </button>
                      </form>
                    } @else {
                      <button
                        type="button"
                        class="btn-text s-btn-sm"
                        [attr.data-testid]="
                          'replace-owned-' + sec.name + '-' + k
                        "
                        [disabled]="
                          !access.can('PUT /api/secrets/{secret}/{key}')
                        "
                        (click)="startReplace(sec.name, k)"
                      >
                        Replace value
                      </button>
                    }
                  </li>
                }
              }
            </ul>
          }
          @if (!srv.secrets?.length && !ownedSecrets().length) {
            <p class="s-hint" data-testid="capability-server-no-secrets">
              This server is given no secrets.
            </p>
          }

          <details class="s-subsection cap-bind" data-testid="bind-secret">
            <summary class="s-subhead-title">Bind a secret</summary>
            <p class="s-hint">
              Stores a credential in the encrypted secrets store and adds
              {{ srv.config.command ? "an environment variable" : "a header" }}
              to this entry that refers to it as
              <code class="s-mono">$secret:&lt;name&gt;</code>.
            </p>
            <form
              class="s-grid"
              autocomplete="off"
              (submit)="$event.preventDefault(); bindSecret()"
            >
              <div class="s-field">
                <label class="s-label" for="bind-name">{{
                  srv.config.command ? "Env name" : "Header name"
                }}</label>
                <input
                  id="bind-name"
                  class="s-input s-mono"
                  data-testid="bind-name"
                  spellcheck="false"
                  [placeholder]="
                    srv.config.command ? 'API_TOKEN' : 'Authorization'
                  "
                  [value]="bind().name"
                  [disabled]="!canBind()"
                  (input)="setBind('name', $any($event.target).value)"
                />
              </div>
              <div class="s-field">
                <label class="s-label" for="bind-secret">Secret name</label>
                <input
                  id="bind-secret"
                  class="s-input s-mono"
                  data-testid="bind-secret-name"
                  spellcheck="false"
                  placeholder="cap-token"
                  [value]="bind().secret"
                  [disabled]="!canBind()"
                  (input)="setBind('secret', $any($event.target).value)"
                />
              </div>
              <div class="s-field">
                <label class="s-label" for="bind-prefix"
                  >Prefix (optional)</label
                >
                <input
                  id="bind-prefix"
                  class="s-input"
                  data-testid="bind-prefix"
                  [placeholder]="srv.config.command ? '' : 'Bearer '"
                  [value]="bind().prefix"
                  [disabled]="!canBind()"
                  (input)="setBind('prefix', $any($event.target).value)"
                />
              </div>
              <div class="s-field">
                <label class="s-label" for="bind-value">Value (optional)</label>
                <input
                  id="bind-value"
                  class="s-input"
                  type="password"
                  autocomplete="new-password"
                  data-testid="bind-value"
                  [value]="bind().value"
                  [disabled]="!canBind()"
                  (input)="setBind('value', $any($event.target).value)"
                />
                <span class="s-hint"
                  >Leave empty to point at a secret the store already
                  holds.</span
                >
              </div>
              <div class="s-inline cap-field-wide">
                <button
                  type="submit"
                  class="btn-tonal"
                  data-testid="bind-secret-btn"
                  [disabled]="
                    !canBind() ||
                    busy() ||
                    !bind().name.trim() ||
                    !bind().secret.trim()
                  "
                >
                  Bind secret
                </button>
              </div>
            </form>
          </details>
        </div>

        <!-- Server settings -->
        @if (schema() || isFigma()) {
          <div class="s-card" data-testid="capability-server-settings">
            <div class="s-card-head">
              <div>
                <h3 class="s-card-title">Server settings</h3>
                <p class="s-card-desc">
                  Options this server defines for itself, drawn from the schema
                  its configure tool declares. grain sends them to the server
                  each time it connects; attempts that start after a change use
                  them.
                </p>
              </div>
              @if (schema() && !configuring()) {
                <button
                  type="button"
                  class="btn-tonal s-btn-sm"
                  data-testid="configure-btn"
                  [disabled]="!canAdmin"
                  (click)="startConfigure()"
                >
                  {{ hasConfig() ? "Edit server settings" : "Configure" }}
                </button>
              }
            </div>

            @if (isGcp()) {
              <div class="s-banner" data-testid="gcp-modes">
                <span>
                  Each permission set is <strong>keyless</strong> (credential
                  <code class="s-mono">token</code>: short-lived access tokens,
                  through a service account grain creates or one you name in
                  <code class="s-mono">service_account</code>) or uses
                  <strong>service-account keys</strong> (credential
                  <code class="s-mono">key</code>: a per-attempt key from a
                  pool, refused where key creation is blocked). Keyless is
                  recommended.
                </span>
              </div>
            }

            @if (configuring() && schema(); as sch) {
              <form
                class="cap-config-form"
                data-testid="capability-server-config"
                autocomplete="off"
                (submit)="$event.preventDefault(); saveConfig()"
              >
                <p class="s-hint">
                  A secret field is write-only: its chip says whether the store
                  holds a value (set), the entry names one the store lacks
                  (missing) or none was given (not set). Type to replace it, or
                  leave it empty to keep it.
                </p>
                <g-cap-schema-form
                  [root]="sch"
                  [schema]="sch"
                  [value]="configValue()"
                  [secrets]="secretSlots()"
                  [errors]="configErrors()"
                  [idPrefix]="'cfg'"
                  [gcpMode]="isGcp()"
                  (valueChange)="configValue.set($event)"
                  (secretChange)="typeSecret($event)"
                  (rotate)="rotateManaged()"
                />
                <div class="s-inline">
                  <button
                    type="submit"
                    class="btn-primary"
                    data-testid="save-config-btn"
                    [disabled]="busy()"
                  >
                    Save server settings
                  </button>
                  <button
                    type="button"
                    class="btn-tonal"
                    data-testid="test-config-btn"
                    [disabled]="
                      busy() || !access.can('POST /api/capability-servers/test')
                    "
                    (click)="testConfig()"
                  >
                    Test server settings
                  </button>
                  <button
                    type="button"
                    class="btn-text"
                    (click)="configuring.set(false)"
                  >
                    Cancel
                  </button>
                </div>
                <p class="s-hint">
                  Test tries the values above, typed secrets included, without
                  storing them. Saving checks them against the server's own
                  rules first.
                </p>
                @if (configProbe(); as r) {
                  <ng-container
                    *ngTemplateOutlet="probe; context: { $implicit: r }"
                  />
                }
              </form>
            } @else if (schema() && !hasConfig()) {
              <p class="s-hint">Nothing configured yet.</p>
            }

            @if (isFigma()) {
              <div class="cap-figma" data-testid="figma-accounts">
                <span class="s-label">Figma accounts</span>
                @for (a of figma(); track a.name) {
                  <div class="cap-figma-row" [attr.data-figma-account]="a.name">
                    <div class="cap-figma-head">
                      <code class="s-mono cap-figma-name">{{ a.name }}</code>
                      <span class="status-pill neutral">{{
                        a.auth_mode === "oauth"
                          ? "OAuth"
                          : "personal access token"
                      }}</span>
                      @if (a.allow_sandbox_token) {
                        <span class="status-pill neutral"
                          >figma#token allowed</span
                        >
                      }
                      <span class="s-row-actions cap-figma-actions">
                        @if (a.auth_mode === "oauth") {
                          <button
                            type="button"
                            class="btn-tonal s-btn-sm"
                            [attr.data-testid]="'figma-connect-' + a.name"
                            [disabled]="
                              !!figmaBusy() ||
                              !access.can(
                                'POST /api/capability-servers/figma-oauth-authorize'
                              )
                            "
                            (click)="connectFigma(a)"
                          >
                            {{
                              figmaBusy() === "connect:" + a.name
                                ? "Connecting…"
                                : "Connect Figma"
                            }}
                          </button>
                        }
                        <button
                          type="button"
                          class="btn-tonal s-btn-sm"
                          [attr.data-testid]="'figma-test-' + a.name"
                          [attr.title]="
                            figmaAttached()
                              ? null
                              : 'Test needs the server attached'
                          "
                          [disabled]="
                            !!figmaBusy() ||
                            !figmaAttached() ||
                            !access.can(
                              'POST /api/capability-servers/figma-test'
                            )
                          "
                          (click)="testFigma(a)"
                        >
                          {{
                            figmaBusy() === "test:" + a.name
                              ? "Testing…"
                              : "Test"
                          }}
                        </button>
                      </span>
                    </div>
                    @if (a.auth_mode === "oauth") {
                      <p class="s-hint" data-testid="figma-oauth-help">
                        Register
                        <code class="s-mono">{{ redirectUri }}</code> as a
                        callback URL on the Figma OAuth app, put its client
                        secret in Server settings, then Connect Figma: grain
                        stores the refresh token Figma hands back.
                      </p>
                    }
                    @if (figmaResult()[a.name]; as r) {
                      <div
                        class="s-banner"
                        [class.ok]="r.ok"
                        [class.error]="!r.ok"
                        data-testid="figma-account-result"
                      >
                        <span>{{ r.text }}</span>
                      </div>
                    }
                  </div>
                } @empty {
                  <p class="s-hint" data-testid="figma-no-accounts">
                    No Figma accounts yet: Configure adds them (an OAuth app's
                    client ID, or a personal access token).
                  </p>
                }
              </div>
            }
          </div>
        }
      }

      <!-- Granted by -->
      <div class="s-card" data-testid="capability-server-usage">
        <div class="s-card-head">
          <div>
            <h3 class="s-card-title">Granted by</h3>
            <p class="s-card-desc">
              The open tasks, templates, schedules, workflow jobs and default
              grants that name one of this server's methods.
            </p>
          </div>
        </div>
        @if (holders().length) {
          <ul class="cap-holders" data-testid="capability-usage">
            @for (h of holders(); track h.key) {
              <li [attr.data-holder]="h.key">
                @if (h.href) {
                  <a [href]="h.href">{{ h.label }}</a>
                } @else if (h.route) {
                  <a [routerLink]="h.route">{{ h.label }}</a>
                } @else {
                  {{ h.label }}
                }
                @if (h.detail) {
                  <span class="cap-holder-detail">{{ h.detail }}</span>
                }
                <code class="s-mono cap-holder-grant">{{ h.grant }}</code>
              </li>
            }
          </ul>
        } @else {
          <p class="s-hint" data-testid="capability-usage-empty">
            Nothing grants this server's capabilities.
          </p>
        }
      </div>

      @if (!srv.inProcess) {
        <!-- Check and manage -->
        <div class="s-card" data-testid="capability-server-manage">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Check and manage</h3>
              <p class="s-card-desc">
                Try the server as it is saved, or take it out of use.
              </p>
            </div>
          </div>
          <div class="s-inline">
            <button
              type="button"
              class="btn-tonal"
              data-testid="test-saved-btn"
              [disabled]="
                busy() || !access.can('POST /api/capability-servers/test')
              "
              (click)="testSaved()"
            >
              Test
            </button>
            <button
              type="button"
              class="btn-tonal"
              data-testid="toggle-server-btn"
              [disabled]="
                busy() ||
                locked ||
                !access.can(
                  srv.config.disabled
                    ? 'POST /api/capability-servers/enable'
                    : 'POST /api/capability-servers/disable'
                )
              "
              (click)="toggle()"
            >
              {{ srv.config.disabled ? "Enable" : "Disable" }}
            </button>
            @if (!isBuiltin()) {
              <button
                type="button"
                class="btn-text s-btn-danger"
                data-testid="remove-server-btn"
                [disabled]="
                  busy() ||
                  locked ||
                  !access.can('POST /api/capability-servers/remove')
                "
                (click)="confirmRemove()"
              >
                Remove
              </button>
            }
          </div>
          @if (savedProbe(); as r) {
            <ng-container
              *ngTemplateOutlet="probe; context: { $implicit: r }"
            />
          }
        </div>
      }
    }

    <ng-template #probe let-r>
      <div class="cap-probe" data-testid="capability-test-result">
        <div class="s-banner" [class.ok]="r.ok" [class.error]="!r.ok">
          <span class="s-wrap">{{
            r.ok
              ? "The server answered with " +
                (r.methods?.length || 0) +
                (r.methods?.length === 1 ? " method." : " methods.")
              : r.error || "The server could not be reached."
          }}</span>
        </div>
        @for (w of r.warnings || []; track w) {
          <div class="s-banner warn" data-testid="capability-test-warning">
            <span>{{ w }}</span>
          </div>
        }
        @if (r.methods?.length) {
          <p class="s-hint s-mono">
            {{ methodNames(r) }}
          </p>
        }
      </div>
    </ng-template>

    @if (confirmReq(); as req) {
      <g-confirm-dialog [request]="req" (done)="onConfirmDone($event)" />
    }
  `,
})
export class CapabilityServerPageComponent {
  readonly serverId = input.required<string>();

  protected readonly access = inject(AccessService);
  protected readonly state = inject(SettingsStateService);
  private readonly api = inject(GrainApi);
  private readonly snackbar = inject(SnackbarService);
  private readonly router = inject(Router);
  private readonly doc = inject(DOCUMENT);

  protected readonly loading = signal(true);
  protected readonly servers = signal<CapabilityServerDetail[]>([]);
  protected readonly usage = signal<Record<string, CapabilityHolder[]>>({});
  protected readonly hostEntries = signal<HostEntries | null>(null);
  protected readonly hosts = signal<HostRow[]>([]);
  protected readonly secretsList = signal<SecretRow[]>([]);

  protected readonly editing = signal<ConnectionForm | null>(null);
  protected readonly formError = signal("");
  protected readonly configuring = signal(false);
  protected readonly configValue = signal<Record<string, unknown>>({});
  protected readonly typedSecrets = signal<Record<string, string>>({});
  protected readonly configErrors = signal<Record<string, string>>({});
  protected readonly busy = signal(false);
  protected readonly connProbe = signal<ProbeResult | null>(null);
  protected readonly configProbe = signal<ProbeResult | null>(null);
  protected readonly savedProbe = signal<ProbeResult | null>(null);
  protected readonly replacing = signal<string | null>(null);
  protected readonly replaceValue = signal("");
  protected readonly bind = signal({
    name: "",
    secret: "",
    prefix: "",
    value: "",
  });
  protected readonly figmaBusy = signal("");
  protected readonly figmaResult = signal<
    Record<string, { ok: boolean; text: string }>
  >({});
  protected readonly confirmReq = signal<ConfirmRequest | null>(null);

  protected readonly runsAt = runsAt;
  protected readonly redirectUri = figmaRedirectURI(
    this.doc.location?.origin ?? "",
  );
  protected readonly transportOptions: PillOption[] = [
    { value: "stdio", label: "stdio (command)" },
    { value: "http", label: "HTTP (server URL)" },
  ];

  protected readonly server = computed(
    () => this.servers().find((s) => s.id === this.serverId()) ?? null,
  );
  protected readonly name = computed(() => {
    const s = this.server();
    return s ? serverName(s) : "";
  });
  protected readonly summary = computed(() => {
    const s = this.server();
    return (
      s?.inProcess?.summary ||
      s?.builtin?.summary ||
      s?.status?.summary ||
      s?.config.summary ||
      ""
    );
  });
  protected readonly isBuiltin = computed(() => {
    const s = this.server();
    return Boolean(s?.builtin || s?.config.builtin);
  });
  protected readonly isGcp = computed(() => {
    const s = this.server();
    return s ? isGcpServer(s) : false;
  });
  protected readonly isFigma = computed(() => {
    const s = this.server();
    return s ? isFigmaServer(s) : false;
  });
  protected readonly figma = computed(() => {
    const s = this.server();
    return s ? figmaAccounts(s) : [];
  });
  protected readonly figmaAttached = computed(() =>
    Boolean(this.server()?.status?.tools?.includes("test_account")),
  );
  protected readonly schema = computed<JsonSchema | null>(
    () => this.server()?.status?.configSchema ?? null,
  );
  protected readonly hasConfig = computed(
    () => Object.keys(this.server()?.config.config ?? {}).length > 0,
  );
  protected readonly envNames = computed(() => {
    const c = this.server()?.config;
    return Object.keys((c?.command ? c?.env : c?.headers) ?? {});
  });
  protected readonly stateHelp = computed(() => {
    const s = this.server();
    if (!s) return "";
    if (s.config.disabled) return STATE_HELP["disabled"];
    const st = String(s.status?.state ?? "");
    if (st.startsWith("needs-hostd")) return "";
    return STATE_HELP[st] ?? "";
  });
  protected readonly needsHost = computed(() => {
    const s = this.server();
    return s ? needsHostWarning(s) : null;
  });
  protected readonly methods = computed(() => {
    const st = this.server()?.status;
    const details = st?.methodDetails?.length
      ? st.methodDetails
      : (st?.methods ?? []).map((name) => ({ name, description: "" }));
    return details.map((m) => {
      const [first, ...rest] = String(m.description ?? "").split("\n\n");
      return {
        name: m.name,
        summary: first.trim(),
        more: rest.join("\n\n").trim(),
      };
    });
  });
  protected readonly holders = computed<HolderLink[]>(() =>
    (this.usage()[this.serverId()] ?? []).map(holderLink),
  );
  protected readonly ownedSecrets = computed(() =>
    this.secretsList().filter((s) =>
      s.capabilityServers?.includes(this.serverId()),
    ),
  );
  protected readonly canBind = computed(
    () =>
      this.access.can("POST /api/capability-servers/bind-secret") &&
      !this.state.sectionLocked("capabilities"),
  );
  protected readonly runOptions = computed<PillOption[]>(() => {
    const f = this.editing();
    const host = hostChoice(
      this.hostEntries(),
      f?.command ?? "",
      f?.hostd ?? "",
    );
    const named = this.hosts().length > 0;
    return [
      { value: "container", label: "container (beside the daemon)" },
      {
        value: "host",
        label: "host (through a host agent)",
        // A named host is always choosable: the server waits for it.
        disabled: !host.ok && !named,
        hint: host.ok || named ? undefined : host.why,
      },
    ];
  });
  protected readonly hostOptions = computed<PillOption[]>(() => [
    {
      value: "",
      label: "The default host",
      hint:
        hostChoice(this.hostEntries(), this.editing()?.command ?? "", "").why ||
        undefined,
    },
    ...this.hosts()
      .filter((h) => h.state !== "revoked")
      .map((h) => ({
        value: h.name,
        label: h.name,
        hint: h.state && h.state !== "connected" ? h.state : undefined,
      })),
  ]);
  protected readonly secretSlots = computed<Record<string, SecretSlot>>(() => {
    const s = this.server();
    const sch = this.schema();
    if (!s || !sch?.properties) return {};
    const managed = Object.fromEntries(
      (s.managed ?? []).map((m) => [m.field, m]),
    );
    const out: Record<string, SecretSlot> = {};
    for (const [name, p] of Object.entries(sch.properties)) {
      if (!(p.writeOnly === true || p.format === "password")) continue;
      out[name] = {
        state: secretFieldState(s, name),
        typed: this.typedSecrets()[name] ?? "",
        managed: managed[name]
          ? {
              present: managed[name].present,
              overridden: managed[name].overridden,
            }
          : undefined,
      };
    }
    return out;
  });

  constructor() {
    // A server attaching after a save, or a host coming back, changes
    // what the page says: read again every 30 seconds while it is open.
    const t = setInterval(() => void this.load(false), 30000);
    inject(DestroyRef).onDestroy(() => clearInterval(t));
    effect(() => {
      this.serverId();
      untracked(() => {
        this.editing.set(null);
        this.configuring.set(false);
        this.connProbe.set(null);
        this.configProbe.set(null);
        this.savedProbe.set(null);
        void this.load(true);
      });
    });
  }

  protected chipFor(srv: CapabilityServerDetail) {
    return capServerState(srv);
  }

  protected methodNames(r: ProbeResult): string {
    return (r.methods ?? []).map((m) => m.name).join(", ");
  }

  private async load(first: boolean): Promise<void> {
    if (first) this.loading.set(true);
    const jobs: Promise<unknown>[] = [this.loadServers()];
    if (this.access.can("GET /api/capability-servers/usage")) {
      jobs.push(
        this.api
          .get<{ id: string; holders?: CapabilityHolder[] }[]>(
            "GET /api/capability-servers/usage",
          )
          .then((rows) => {
            const by: Record<string, CapabilityHolder[]> = {};
            for (const u of Array.isArray(rows) ? rows : []) {
              by[u.id] = u.holders ?? [];
            }
            this.usage.set(by);
          })
          .catch(() => this.usage.set({})),
      );
    }
    if (this.access.can("GET /api/capability-servers/host-entries")) {
      jobs.push(
        this.api
          .get<HostEntries>("GET /api/capability-servers/host-entries")
          .then((e) => this.hostEntries.set(e ?? { available: false }))
          .catch((err) =>
            this.hostEntries.set({ available: false, reason: errText(err) }),
          ),
      );
    }
    if (this.access.can("GET /api/hostds")) {
      jobs.push(
        this.api
          .get<HostRow[] | { hostds?: HostRow[] }>("GET /api/hostds")
          .then((h) => this.hosts.set(Array.isArray(h) ? h : (h?.hostds ?? [])))
          .catch(() => undefined),
      );
    }
    jobs.push(this.loadSecrets());
    await Promise.all(jobs);
    if (first) this.loading.set(false);
  }

  private async loadServers(): Promise<void> {
    if (!this.access.can("GET /api/capability-servers")) return;
    try {
      const s = await this.api.get<CapabilityServerDetail[]>(
        "GET /api/capability-servers",
      );
      this.servers.set(Array.isArray(s) ? s : []);
    } catch (err) {
      this.state.recordError(err);
    }
  }

  private async loadSecrets(): Promise<void> {
    if (!this.access.can("GET /api/secrets")) return;
    try {
      const sec = await this.api.get<SecretRow[] | { secrets?: SecretRow[] }>(
        "GET /api/secrets",
      );
      this.secretsList.set(Array.isArray(sec) ? sec : (sec?.secrets ?? []));
    } catch {
      // The page works without it.
    }
  }

  // apply takes a write's answer (the whole list, as every capability
  // server write returns) and reads usage and secrets again: a server's
  // own secrets are made, renamed and removed with it.
  private async apply(data: unknown): Promise<void> {
    if (Array.isArray(data)) {
      this.servers.set(data as CapabilityServerDetail[]);
    } else {
      await this.loadServers();
    }
    await this.loadSecrets();
  }

  protected startEdit(): void {
    const s = this.server();
    if (!s) return;
    this.formError.set("");
    this.connProbe.set(null);
    this.editing.set(formFromServer(s));
  }

  protected patch(p: Partial<ConnectionForm>): void {
    const f = this.editing();
    if (f) this.editing.set({ ...f, ...p });
  }

  private buildConfig() {
    const f = this.editing();
    const s = this.server();
    if (!f || !s) return null;
    try {
      this.formError.set("");
      return configFromForm(f, s.config);
    } catch (err) {
      this.formError.set(errText(err));
      return null;
    }
  }

  protected async saveConnection(): Promise<void> {
    const s = this.server();
    const f = this.editing();
    const config = this.buildConfig();
    if (!s || !f || !config) return;
    const id = f.id.trim();
    if (id !== s.id) {
      const items = this.holders();
      this.confirmReq.set({
        title: "Rename this capability server?",
        message: items.length
          ? `Renaming ${s.id} to ${id} leaves these grants naming an ID that no longer resolves:`
          : `Rename ${s.id} to ${id}? Nothing grants it now; any grant written with the old ID will no longer resolve.`,
        items,
        itemsLabel: "Grants naming the old ID",
        accept: "Rename",
        destructive: true,
        run: () => this.putConnection(id, config, s.id),
      });
      return;
    }
    await this.putConnection(id, config);
  }

  private async putConnection(
    id: string,
    config: ReturnType<typeof configFromForm>,
    renameFrom?: string,
  ): Promise<void> {
    this.busy.set(true);
    try {
      const data = await this.api.put(
        "PUT /api/capability-servers",
        undefined,
        renameFrom ? { id, config, renameFrom } : { id, config },
      );
      this.editing.set(null);
      await this.apply(data);
      this.snackbar.show(renameFrom ? `Renamed to ${id}` : `Saved ${id}`);
      if (renameFrom) {
        void this.router.navigate(["/settings", "capabilities", id], {
          replaceUrl: true,
        });
      }
    } catch (err) {
      this.formError.set(errText(err));
    } finally {
      this.busy.set(false);
    }
  }

  private async probe(body: Record<string, unknown>): Promise<ProbeResult> {
    try {
      return await this.api.post<ProbeResult>(
        "POST /api/capability-servers/test",
        undefined,
        body,
      );
    } catch (err) {
      return { ok: false, error: errText(err) };
    }
  }

  protected async testConnection(): Promise<void> {
    const f = this.editing();
    const config = this.buildConfig();
    if (!f || !config) return;
    this.busy.set(true);
    this.connProbe.set(await this.probe({ id: f.id.trim(), config }));
    this.busy.set(false);
  }

  protected async testSaved(): Promise<void> {
    this.busy.set(true);
    this.savedProbe.set(await this.probe({ id: this.serverId() }));
    this.busy.set(false);
  }

  // The stored config with the secret fields left out: what the schema
  // form edits. Their $secret: references go back unchanged on save.
  protected startConfigure(): void {
    const s = this.server();
    const sch = this.schema();
    if (!s || !sch) return;
    const secret = new Set(Object.keys(this.secretSlots()));
    const plain: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(s.config.config ?? {})) {
      if (!secret.has(k)) plain[k] = structuredClone(v);
    }
    this.configValue.set(plain);
    this.typedSecrets.set({});
    this.configErrors.set({});
    this.configProbe.set(null);
    this.configuring.set(true);
  }

  protected typeSecret(e: { name: string; value: string }): void {
    this.typedSecrets.set({ ...this.typedSecrets(), [e.name]: e.value });
  }

  private configBody(): Record<string, unknown> | null {
    const s = this.server();
    if (!s) return null;
    const refs: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(s.config.config ?? {})) {
      if (typeof v === "string" && v.startsWith(SECRET_PREFIX)) refs[k] = v;
    }
    const typed = Object.fromEntries(
      Object.entries(this.typedSecrets()).filter(([, v]) => v),
    );
    return {
      id: s.id,
      config: { ...s.config, config: { ...this.configValue(), ...refs } },
      configSecrets: typed,
    };
  }

  protected async saveConfig(): Promise<void> {
    const body = this.configBody();
    if (!body) return;
    this.busy.set(true);
    try {
      const data = await this.api.put(
        "PUT /api/capability-servers",
        undefined,
        body,
      );
      this.configErrors.set({});
      this.typedSecrets.set({});
      this.configuring.set(false);
      await this.apply(data);
      this.snackbar.show("Server settings saved");
    } catch (err) {
      // A refusal keyed by field is shown on that field.
      const fields =
        err instanceof HttpErrorResponse &&
        err.error &&
        typeof err.error === "object"
          ? (err.error as { fields?: Record<string, string> }).fields
          : undefined;
      if (fields && Object.keys(fields).length) {
        this.configErrors.set(fields);
      } else {
        this.configErrors.set({ "": errText(err) });
      }
    } finally {
      this.busy.set(false);
    }
  }

  protected async testConfig(): Promise<void> {
    const body = this.configBody();
    if (!body) return;
    this.busy.set(true);
    this.configProbe.set(await this.probe(body));
    this.busy.set(false);
  }

  protected async rotateManaged(): Promise<void> {
    this.busy.set(true);
    try {
      const data = await this.api.post(
        "POST /api/capability-servers/rotate-managed",
        undefined,
        { id: this.serverId() },
      );
      await this.apply(data);
      this.snackbar.show("Rotated");
    } catch (err) {
      this.state.recordError(err);
    } finally {
      this.busy.set(false);
    }
  }

  protected startReplace(name: string, key: string): void {
    this.replaceValue.set("");
    this.replacing.set(`${name}/${key}`);
  }

  protected async replaceSecret(name: string, key: string): Promise<void> {
    const value = this.replaceValue();
    if (!value) return;
    this.busy.set(true);
    try {
      await this.api.put(
        "PUT /api/secrets/{secret}/{key}",
        { secret: name, key },
        { value },
      );
      this.replacing.set(null);
      this.replaceValue.set("");
      await this.apply(null);
      this.snackbar.show(`Replaced ${name}/${key}`);
    } catch (err) {
      this.state.recordError(err);
    } finally {
      this.busy.set(false);
    }
  }

  protected setBind(
    field: "name" | "secret" | "prefix" | "value",
    v: string,
  ): void {
    this.bind.set({ ...this.bind(), [field]: v });
  }

  protected async bindSecret(): Promise<void> {
    const s = this.server();
    const b = this.bind();
    if (!s) return;
    this.busy.set(true);
    try {
      const data = await this.api.post(
        "POST /api/capability-servers/bind-secret",
        undefined,
        {
          id: s.id,
          kind: s.config.command ? "env" : "header",
          name: b.name.trim(),
          secret: b.secret.trim(),
          prefix: b.prefix,
          value: b.value,
        },
      );
      this.bind.set({ name: "", secret: "", prefix: "", value: "" });
      await this.apply(data);
      this.snackbar.show(`Bound $secret:${b.secret.trim()}`);
    } catch (err) {
      this.state.recordError(err);
    } finally {
      this.busy.set(false);
    }
  }

  protected async dismissNotice(): Promise<void> {
    try {
      const data = await this.api.post(
        "POST /api/capability-servers/dismiss-notice",
        undefined,
        { id: this.serverId() },
      );
      await this.apply(data);
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async toggle(): Promise<void> {
    const s = this.server();
    if (!s) return;
    this.busy.set(true);
    try {
      const data = await this.api.post(
        s.config.disabled
          ? "POST /api/capability-servers/enable"
          : "POST /api/capability-servers/disable",
        undefined,
        { id: s.id },
      );
      await this.apply(data);
      // The answer to Disable still carries the state from before the
      // daemon let go: read once more shortly after.
      setTimeout(() => void this.loadServers(), 2000);
    } catch (err) {
      this.state.recordError(err);
    } finally {
      this.busy.set(false);
    }
  }

  // Remove asks first, listing what holds a grant on the server, each
  // linked to where that grant is changed.
  protected confirmRemove(): void {
    const s = this.server();
    if (!s) return;
    const items = this.holders();
    this.confirmReq.set({
      title: "Remove capability server?",
      message: items.length
        ? `Remove ${s.id}? Its own secrets go with it, and these grants can no longer be used:`
        : `Remove ${s.id}? Nothing grants it now. Its own secrets go with it.`,
      items,
      itemsLabel: "Grants on this server",
      accept: "Remove",
      destructive: true,
      run: () => this.remove(s.id),
    });
  }

  private async remove(id: string): Promise<void> {
    this.busy.set(true);
    try {
      await this.api.post("POST /api/capability-servers/remove", undefined, {
        id,
      });
      this.snackbar.show(`Removed ${id}`);
      // The list reads servers and stored secrets again as it opens, so
      // the secrets that went with this server are gone from it.
      void this.router.navigate(["/settings", "capabilities"], {
        fragment: "capability-servers",
      });
    } catch (err) {
      this.state.recordError(err);
    } finally {
      this.busy.set(false);
    }
  }

  protected async onConfirmDone(accepted: boolean): Promise<void> {
    const req = this.confirmReq();
    this.confirmReq.set(null);
    if (accepted && req) await req.run();
  }

  private figmaSay(account: string, ok: boolean, text: string): void {
    this.figmaResult.set({ ...this.figmaResult(), [account]: { ok, text } });
  }

  protected async testFigma(a: FigmaAccount): Promise<void> {
    this.figmaBusy.set(`test:${a.name}`);
    try {
      const res = await this.api.post<{
        error?: string;
        handle?: string;
        email?: string;
        user_id?: string;
      }>("POST /api/capability-servers/figma-test", undefined, {
        id: this.serverId(),
        account: a.name,
      });
      if (res?.error)
        this.figmaSay(a.name, false, `Figma refused: ${res.error}`);
      else {
        const who =
          [res?.handle, res?.email && `<${res.email}>`]
            .filter(Boolean)
            .join(" ") ||
          res?.user_id ||
          "an unnamed user";
        this.figmaSay(a.name, true, `Signed in to Figma as ${who}.`);
      }
    } catch (err) {
      this.figmaSay(a.name, false, errText(err));
    } finally {
      this.figmaBusy.set("");
    }
  }

  // connectFigma runs the OAuth app's authorization in a popup opened
  // before the first await (so the browser allows it), waits for Figma
  // to send it back to this origin with ?code, and has grain exchange
  // the code for a refresh token it keeps in the secrets store.
  protected async connectFigma(a: FigmaAccount): Promise<void> {
    this.figmaBusy.set(`connect:${a.name}`);
    const win = this.doc.defaultView;
    const popup = win?.open("", "grain-figma-oauth", "width=600,height=760");
    try {
      const auth = await this.api.post<{ authorizeUrl: string; state: string }>(
        "POST /api/capability-servers/figma-oauth-authorize",
        undefined,
        { id: this.serverId(), account: a.name, redirectUri: this.redirectUri },
      );
      if (!popup) {
        throw new Error(
          "the browser blocked the Figma window: allow popups for this site and try again",
        );
      }
      popup.location.href = auth.authorizeUrl;
      const { code, state } = await waitForOAuthCode(popup, win!);
      if (state !== auth.state) {
        throw new Error("Figma answered with a different OAuth state");
      }
      const res = await this.api.post<{ account: string; userId?: string }>(
        "POST /api/capability-servers/figma-oauth-exchange",
        undefined,
        {
          id: this.serverId(),
          account: a.name,
          code,
          state,
          redirectUri: this.redirectUri,
        },
      );
      this.figmaSay(
        a.name,
        true,
        `Connected: the refresh token for ${res.account}${res.userId ? ` (Figma user ${res.userId})` : ""} is in the secrets store.`,
      );
      await this.apply(null);
    } catch (err) {
      if (popup && !popup.closed) popup.close();
      this.figmaSay(a.name, false, `Not connected: ${errText(err)}`);
    } finally {
      this.figmaBusy.set("");
    }
  }
}

function waitForOAuthCode(
  popup: Window,
  win: Window,
): Promise<{ code: string; state: string }> {
  return new Promise((resolve, reject) => {
    const timer = win.setInterval(() => {
      if (popup.closed) {
        win.clearInterval(timer);
        reject(new Error("the Figma window was closed before it finished"));
        return;
      }
      let search: string;
      try {
        if (popup.location.origin !== win.location.origin) return;
        search = popup.location.search;
      } catch {
        return;
      }
      const params = new URLSearchParams(search);
      const code = params.get("code");
      const error = params.get("error");
      if (!code && !error) return;
      win.clearInterval(timer);
      popup.close();
      if (error) reject(new Error(`Figma refused: ${error}`));
      else resolve({ code: code!, state: params.get("state") ?? "" });
    }, 500);
  });
}
