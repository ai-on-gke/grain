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
  HostListener,
  NgZone,
  OnDestroy,
  OnInit,
  ViewEncapsulation,
  computed,
  effect,
  inject,
  input,
  signal,
} from "@angular/core";
import { Router, RouterLink } from "@angular/router";
import { GrainApi } from "../api/grain-api.service";
import { AccessService } from "../services/access.service";
import {
  ConfirmDialogComponent,
  type ConfirmRequest,
} from "../shared/confirm-dialog.component";
import { InfoPopoverComponent } from "../shared/info-popover.component";
import {
  type PillOption,
  PillSelectComponent,
} from "../shared/pill-select.component";
import { TipDirective } from "../shared/tip.directive";
import {
  CapabilityArgsFormComponent,
  type ServerConfigSecretsContext,
} from "./capability-args-form.component";
import {
  ConfigMapConfigEditorComponent,
  ConfigMapListComponent,
  FigmaAccountListComponent,
  GChatSpaceListComponent,
  HostScriptListComponent,
} from "./capability-server-builtins.component";
import {
  CapabilityMethodTableComponent,
  CapabilityProbeResultComponent,
  CapabilityUsageListComponent,
  ConnectionHostdFieldComponent,
  HostdMissingNoteComponent,
} from "./capability-server-panels.component";
import {
  type CapabilityGrantHolder,
  type CapabilityServer,
  type CapabilityServerConfig,
  type CapabilityServerManagedField,
  type ConfigMapEditorState,
  GRANTED_HELP,
  HOLDER_KIND_LABELS,
  type HostEntriesResponse,
  KIND_HELP,
  NEEDS_HOSTD_HELP,
  type ProbeResultData,
  SECRET_PREFIX,
  STATE_HELP,
  STATE_LABELS,
  type ServerFormState,
  WAITING_HELP,
  configFromForm,
  configMapEditorValue,
  extractFieldErrors,
  formFromServer,
  hostChoice,
  hostdEntryProblem,
  isConfigMapServer,
  isFigmaServer,
  isGChatServer,
  isHostScriptServer,
  needsHostLabel,
  needsHostd,
  normalizeConfigMapPayload,
  secretFieldNames,
  serverDisplayTitle,
  serverRuns,
  serverState,
  serverSummaryText,
  statePillTone,
  summarizeServer,
  waitingFor,
} from "./capability-server-types";
import { SettingsStateService, errText } from "./settings-state.service";

const UNREPORTED_READ_LIMIT = 5;

const TRANSPORT_OPTIONS: readonly PillOption[] = [
  { value: "stdio", label: "stdio (command)" },
  { value: "http", label: "HTTP (serverUrl)" },
];

function formatHolderLine(h: CapabilityGrantHolder): string {
  if (h.kind === "default") {
    return h.repo
      ? `Default grants of ${h.repo} (${h.grant})`
      : `Deployment defaults (${h.grant})`;
  }
  const head =
    h.kind === "task"
      ? `Task #${h.id}`
      : `${HOLDER_KIND_LABELS[h.kind] ?? h.kind} ${h.name ? `“${h.name}”` : h.id}`;
  return `${head}${h.repo ? ` on ${h.repo}` : ""} (${h.grant})`;
}

@Component({
  selector: "g-capability-server-detail",
  imports: [
    RouterLink,
    InfoPopoverComponent,
    PillSelectComponent,
    TipDirective,
    ConfirmDialogComponent,
    CapabilityArgsFormComponent,
    CapabilityMethodTableComponent,
    CapabilityUsageListComponent,
    CapabilityProbeResultComponent,
    HostdMissingNoteComponent,
    ConnectionHostdFieldComponent,
    HostScriptListComponent,
    FigmaAccountListComponent,
    GChatSpaceListComponent,
    ConfigMapConfigEditorComponent,
    ConfigMapListComponent,
  ],
  encapsulation: ViewEncapsulation.None,
  styleUrls: ["./capability-server-detail.css"],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="cap-detail-wrap" data-testid="capability-server-detail-page">
      <div class="cap-back-row">
        <a
          class="btn-text s-btn-sm cap-back-link"
          [routerLink]="['/settings', 'capabilities']"
          data-testid="capability-servers-back"
        >
          ← All capability servers
        </a>
      </div>

      @if (error(); as errMsg) {
        <div
          class="s-banner error s-wrap"
          role="alert"
          data-testid="capability-server-error"
        >
          <span>{{ errMsg }}</span>
          <button
            type="button"
            class="btn-text s-btn-sm"
            (click)="error.set('')"
          >
            Dismiss
          </button>
        </div>
      }

      @if (loading() && servers() === null) {
        <div class="s-card" data-testid="capability-server-loading">
          <div class="s-skel-line s-skel-title"></div>
          <div class="s-skel-line"></div>
          <div class="s-skel-line s-skel-short"></div>
        </div>
      } @else if (!activeServer()) {
        <div
          class="s-banner warn s-wrap"
          role="status"
          data-testid="capability-server-unknown"
        >
          <span>
            No capability server here is called
            <code class="s-mono">{{ serverId() }}</code
            >.
          </span>
        </div>
      } @else {
        @let srv = activeServer()!;
        @let where = currentRuns();
        @let sum = summaryParts();
        @let isBuiltin = !!srv.builtin;
        @let isInProcess = !!srv.inProcess;
        @let isStdio = !!srv.config?.command;

        <div
          class="s-card"
          [attr.data-capability-server]="srv.id"
          [attr.data-builtin]="srv.builtin ? srv.builtin.name : null"
          [attr.data-in-process]="srv.inProcess ? srv.inProcess.name : null"
        >
          <div class="cap-detail-header">
            <h2 class="cap-detail-title">{{ displayTitle() }}</h2>

            @if (sum.short) {
              <div
                class="cap-summary-line"
                data-testid="capability-server-summary"
              >
                <span>{{ sum.short }}</span>
                @if (sum.truncated) {
                  <g-info-popover [title]="sum.full" [label]="displayTitle()" />
                }
              </div>
            }

            <p
              class="cap-id-line"
              title="The capability ID: the name grants use for this server's methods, as <capability ID>#<method>."
            >
              Capability ID: <code class="s-mono">{{ srv.id }}</code>
            </p>

            <div class="cap-chip-row">
              <span
                class="status-pill"
                [class.proposal]="isBuiltin || isInProcess"
                [class.neutral]="!isBuiltin && !isInProcess"
                tabindex="0"
                [gTip]="kindHelpText()"
                data-testid="capability-server-kind"
              >
                {{ isBuiltin || isInProcess ? "built-in" : "custom" }}
              </span>
              <span
                class="status-pill neutral"
                tabindex="0"
                [gTip]="where.help"
                [attr.data-runs]="where.key"
                data-testid="capability-server-runs"
              >
                {{ where.label }}
              </span>
              <span
                class="status-pill neutral"
                tabindex="0"
                [gTip]="grantedHelp"
                data-testid="capability-server-usage-count"
              >
                granted by {{ holders().length }}
              </span>
              <span
                [class]="'status-pill ' + stateTone()"
                tabindex="0"
                [gTip]="stateTooltip()"
                data-testid="capability-server-state"
              >
                {{ stateChipLabel() }}
              </span>
            </div>

            @if (stateHelpText()) {
              <p class="s-card-desc" data-testid="capability-server-state-help">
                {{ stateHelpText() }}
              </p>
            }
          </div>

          @if (!isInProcess) {
            @if (srv.config?.notice) {
              <div
                class="s-banner readonly s-wrap"
                role="status"
                data-testid="capability-server-notice"
              >
                <span>{{ srv.config?.notice }}</span>
                <button
                  type="button"
                  class="btn-text s-btn-sm"
                  [disabled]="readOnly()"
                  (click)="dismissNotice(srv)"
                >
                  Dismiss
                </button>
              </div>
            }

            @if (srv.status?.reason && waitingText()) {
              <div
                class="s-banner readonly s-wrap"
                role="status"
                data-testid="capability-server-waiting"
              >
                <span>
                  Waiting for {{ waitingText() }}. grain tries it again every
                  minute, and it attaches by itself once that is done. What it
                  said: {{ srv.status?.reason }}
                </span>
              </div>
            }

            @if (srv.status?.reason && !waitingText()) {
              <div
                class="s-banner readonly s-wrap"
                role="status"
                data-testid="capability-server-needs-config"
              >
                <span>Needs configuring: {{ srv.status?.reason }}</span>
              </div>
            }

            @if (srv.config?.run === "host" && !srv.config?.hostd) {
              <g-hostd-missing-note [problem]="hostdProblem()" />
            }

            @if (srv.status?.error) {
              <div
                class="s-banner error s-wrap cap-prewrap"
                role="alert"
                data-testid="capability-server-status-error"
              >
                <span>
                  @if (!srv.status?.configSchema && isBuiltin) {
                    Configure becomes available once the server starts — fix:
                    {{ srv.status?.error }}
                  } @else {
                    {{ srv.status?.error }}
                  }
                </span>
              </div>
            }
          }

          <section
            class="cap-section"
            aria-label="What it gives a task"
            data-testid="capability-server-methods"
          >
            <div>
              <h3 class="cap-section-title">What it gives a task</h3>
              <p class="s-card-desc" data-testid="capability-section-caption">
                @if (isInProcess) {
                  Each method is one capability a task, template, schedule or CI
                  job can be granted, named &lt;capability
                  ID&gt;#&lt;method&gt;.
                } @else {
                  Each method is one capability a task, template, schedule or CI
                  job can be granted, named &lt;capability
                  ID&gt;#&lt;method&gt;. Methods appear once grain has connected
                  to the server.
                }
              </p>
            </div>

            @if ((srv.status?.methodDetails?.length ?? 0) > 0) {
              <g-capability-method-table
                [details]="srv.status?.methodDetails ?? []"
              />
            } @else if ((srv.status?.methods?.length ?? 0) > 0) {
              <p class="s-card-desc">
                Methods: {{ (srv.status?.methods ?? []).join(", ") }}
              </p>
            } @else {
              <p class="s-card-desc">
                {{
                  isInProcess
                    ? "No methods reported."
                    : "No methods reported yet."
                }}
              </p>
            }
          </section>

          <section
            class="cap-section"
            aria-label="Connection"
            data-testid="capability-server-connection"
          >
            <div>
              <h3 class="cap-section-title">Connection</h3>
              <p class="s-card-desc" data-testid="capability-section-caption">
                @if (isInProcess) {
                  Where this server runs.
                } @else {
                  How grain starts or reaches this server: its command or URL,
                  where it runs, and the environment or headers it is given.
                  Saved to grain/config; attempts that start after a change use
                  it.
                }
              </p>
            </div>

            @if (isInProcess) {
              <p class="s-card-desc" data-testid="capability-server-in-process">
                Built into grain and run inside its own process: there is no
                command, URL, secret or server setting to change, and it cannot
                be disabled or removed. Who may hold its methods is decided by
                grants and the Capability policy.
              </p>
            } @else {
              <dl class="cap-dl">
                <dt>{{ isStdio ? "Command" : "Server URL" }}</dt>
                <dd class="s-mono">
                  @if (isStdio) {
                    {{ commandLine(srv) }}
                  } @else {
                    {{ srv.config?.serverUrl }}
                  }
                </dd>
                <dt>Runs</dt>
                <dd>{{ where.label }} — {{ where.help }}</dd>
                @for (pair of connectionEntries(); track pair[0]) {
                  <dt>{{ isStdio ? "Env" : "Header" }} {{ pair[0] }}</dt>
                  <dd class="s-mono">{{ pair[1] }}</dd>
                }
              </dl>

              @if ((srv.secrets?.length ?? 0) > 0) {
                <div class="cap-chip-row">
                  @for (sec of srv.secrets ?? []; track sec.name) {
                    <span
                      class="status-pill"
                      [class.healthy]="sec.present"
                      [class.warn]="!sec.present"
                      tabindex="0"
                      [gTip]="
                        sec.present
                          ? 'The secrets store holds a value for $secret:' +
                            sec.name +
                            '.'
                          : 'The entry refers to $secret:' +
                            sec.name +
                            ', but the secrets store has no value for it. Bind a secret with that name to fill it in.'
                      "
                    >
                      {{ sec.name }}: {{ sec.present ? "set" : "missing" }}
                    </span>
                  }
                </div>
              }

              <div class="s-inline">
                <button
                  type="button"
                  class="btn-outlined s-btn-sm"
                  [disabled]="readOnly()"
                  [attr.aria-expanded]="editing()"
                  data-testid="capability-edit-connection-btn"
                  (click)="toggleEditing(srv)"
                >
                  Edit connection
                </button>
                <button
                  type="button"
                  class="btn-text s-btn-sm"
                  [disabled]="readOnly()"
                  [attr.aria-expanded]="binding()"
                  data-testid="capability-bind-secret-btn"
                  (click)="toggleBinding()"
                >
                  Bind a secret
                </button>
              </div>

              <p class="s-hint" data-testid="capability-action-help">
                Bind a secret stores a credential in the encrypted secrets store
                and adds {{ isStdio ? "an environment variable" : "a header" }}
                to this entry that refers to it as
                <code class="s-mono">$secret:&lt;name&gt;</code>, so the value
                itself never reaches grain/config.
              </p>

              @if (editing()) {
                @let ef = editForm();
                @let hc = editHostChoice();
                <form
                  class="cap-form-stack"
                  data-testid="capability-server-edit"
                  (submit)="submitEdit($event, srv)"
                >
                  <div class="s-field">
                    <label class="s-label" for="cap-edit-id">
                      Capability ID *
                    </label>
                    <input
                      id="cap-edit-id"
                      class="s-input s-mono"
                      type="text"
                      required
                      placeholder="https://github.com/my-org/capabilities/custom"
                      autocapitalize="none"
                      autocomplete="off"
                      spellcheck="false"
                      data-testid="capability-field-capabilityId"
                      [disabled]="readOnly() || isBuiltin"
                      [value]="ef.id"
                      (input)="patchEditForm({ id: $any($event.target).value })"
                    />
                    <span class="s-hint">
                      @if (isBuiltin) {
                        A built-in server's ID is grain's own and the same on
                        every deployment.
                      } @else if (ef.id.trim() !== srv.id) {
                        Renaming: grants naming the old ID will no longer
                        resolve.
                      } @else {
                        The name tasks grant this server's methods by, as
                        &lt;ID&gt;#&lt;method&gt;: a URL you control, such as
                        https://github.com/my-org/capabilities/cloud. Pick it
                        once; renaming it later breaks the grants that name it.
                      }
                    </span>
                  </div>

                  <div class="s-field">
                    <span class="s-label">Transport</span>
                    <g-pill-select
                      [options]="transportOptions"
                      [value]="ef.transport"
                      [disabled]="readOnly()"
                      ariaLabel="Transport"
                      align="left"
                      testid="capability-field-transport"
                      (valueChange)="
                        patchEditForm({
                          transport: $event === 'http' ? 'http' : 'stdio',
                        })
                      "
                    />
                    <span class="s-hint">
                      @if (ef.transport === "stdio") {
                        stdio: grain starts a program and talks to it over its
                        standard input and output.
                      } @else {
                        HTTP: grain connects to a server that is already running
                        at a URL.
                      }
                    </span>
                  </div>

                  @if (ef.transport === "stdio") {
                    <div class="s-field">
                      <label class="s-label" for="cap-edit-cmd">
                        Command *
                      </label>
                      <input
                        id="cap-edit-cmd"
                        class="s-input s-mono"
                        type="text"
                        required
                        autocapitalize="none"
                        autocomplete="off"
                        spellcheck="false"
                        data-testid="capability-field-command"
                        [disabled]="readOnly()"
                        [value]="ef.command"
                        (input)="
                          patchEditForm({ command: $any($event.target).value })
                        "
                      />
                      <span class="s-hint">
                        The program grain starts: a name on PATH (grain), an
                        absolute path (/usr/local/bin/my-cap), or a path inside
                        grain/config (plugins/my-cap/server).
                      </span>
                    </div>

                    <div class="s-field">
                      <label class="s-label" for="cap-edit-args">
                        Arguments, one per line
                      </label>
                      <textarea
                        id="cap-edit-args"
                        class="s-textarea s-mono"
                        rows="2"
                        data-testid="capability-field-args"
                        [disabled]="readOnly()"
                        [value]="ef.args"
                        (input)="
                          patchEditForm({ args: $any($event.target).value })
                        "
                      ></textarea>
                      <span class="s-hint">
                        Given to the command, one per line. grain's own servers
                        take two: capability, then the server's name (gcp).
                      </span>
                    </div>

                    <div class="s-field">
                      <label class="s-label" for="cap-edit-env">
                        Env, NAME=VALUE per line
                      </label>
                      <textarea
                        id="cap-edit-env"
                        class="s-textarea s-mono"
                        rows="2"
                        data-testid="capability-field-env"
                        [disabled]="readOnly()"
                        [value]="ef.env"
                        (input)="
                          patchEditForm({ env: $any($event.target).value })
                        "
                      ></textarea>
                      <span class="s-hint">
                        Environment variables the command starts with, such as
                        REGION=us-central1. Write a credential as
                        $secret:&lt;name&gt; (API_TOKEN=$secret:my-token), or
                        add it with Bind a secret after saving, so its value
                        never reaches grain/config.
                      </span>
                    </div>

                    <div class="s-field">
                      <span class="s-label">Run location</span>
                      <g-pill-select
                        [options]="runLocationOptions()"
                        [value]="ef.run"
                        [disabled]="readOnly()"
                        ariaLabel="Run location"
                        align="left"
                        testid="capability-server-run"
                        (valueChange)="
                          patchEditForm({
                            run: $event === 'host' ? 'host' : 'container',
                          })
                        "
                      />
                      <span class="s-hint">
                        @if (hc.ok) {
                          Where grain starts the command. container: beside the
                          grain daemon, in its container. host: on the bare
                          machine through the host agent, for a server that
                          needs the host's own tools or files.
                        } @else {
                          Where grain starts the command. container: beside the
                          grain daemon, in its container. host is not available:
                          {{ hc.why }}
                        }
                      </span>
                    </div>

                    @if (ef.run === "host") {
                      <g-connection-hostd-field
                        [hostd]="ef.hostd"
                        [disabled]="readOnly()"
                        testId="capability-server-hostd"
                        (hostdChange)="patchEditForm({ hostd: $event })"
                      />
                    }
                  } @else {
                    <div class="s-field">
                      <label class="s-label" for="cap-edit-url">
                        Server URL *
                      </label>
                      <input
                        id="cap-edit-url"
                        class="s-input s-mono"
                        type="text"
                        required
                        placeholder="https://cap.example.com/mcp"
                        autocapitalize="none"
                        autocomplete="off"
                        spellcheck="false"
                        data-testid="capability-field-serverUrl"
                        [disabled]="readOnly()"
                        [value]="ef.serverUrl"
                        (input)="
                          patchEditForm({
                            serverUrl: $any($event.target).value,
                          })
                        "
                      />
                      <span class="s-hint">
                        Where the server answers MCP requests, such as
                        https://cap.example.com/mcp.
                      </span>
                    </div>

                    <div class="s-field">
                      <label class="s-label" for="cap-edit-headers">
                        Headers, NAME=VALUE per line
                      </label>
                      <textarea
                        id="cap-edit-headers"
                        class="s-textarea s-mono"
                        rows="2"
                        data-testid="capability-field-headers"
                        [disabled]="readOnly()"
                        [value]="ef.headers"
                        (input)="
                          patchEditForm({ headers: $any($event.target).value })
                        "
                      ></textarea>
                      <span class="s-hint">
                        Sent with every request to the server, such as
                        Authorization=Bearer $secret:cap-token. Write a
                        credential as $secret:&lt;name&gt;, or add it with Bind
                        a secret after saving, so its value never reaches
                        grain/config.
                      </span>
                    </div>
                  }

                  <div class="s-inline">
                    <button
                      type="submit"
                      class="btn-primary s-btn-sm"
                      [disabled]="readOnly() || editBusy()"
                    >
                      Save connection
                    </button>
                    <button
                      type="button"
                      class="btn-outlined s-btn-sm"
                      [disabled]="readOnly() || editTesting() || !ef.id.trim()"
                      (click)="testEditForm(srv)"
                    >
                      {{ editTesting() ? "Testing…" : "Test" }}
                    </button>
                    <button
                      type="button"
                      class="btn-text s-btn-sm"
                      (click)="editing.set(false)"
                    >
                      Cancel
                    </button>
                  </div>

                  <p class="s-hint">
                    Test starts the server from the values above, unsaved, lists
                    what it offers and saves nothing. Saving applies to attempts
                    that start afterwards.
                  </p>

                  <g-capability-probe-result [result]="editProbeResult()" />
                </form>
              }

              @if (binding()) {
                <form
                  class="cap-bind-grid"
                  data-testid="capability-bind-secret-form"
                  (submit)="submitBindSecret($event, srv)"
                >
                  <div class="s-field">
                    <label class="s-label" for="cap-bind-name">
                      {{ isStdio ? "Env name" : "Header name" }} *
                    </label>
                    <input
                      id="cap-bind-name"
                      class="s-input s-mono"
                      type="text"
                      required
                      autocapitalize="none"
                      autocomplete="off"
                      spellcheck="false"
                      [disabled]="readOnly()"
                      [value]="bindName()"
                      (input)="bindName.set($any($event.target).value)"
                    />
                    <span class="s-hint">
                      {{
                        isStdio
                          ? "The variable the server reads, such as API_TOKEN."
                          : "The header sent to the server, such as Authorization."
                      }}
                    </span>
                  </div>

                  <div class="s-field">
                    <label class="s-label" for="cap-bind-secret">
                      Secret name *
                    </label>
                    <input
                      id="cap-bind-secret"
                      class="s-input s-mono"
                      type="text"
                      required
                      autocapitalize="none"
                      autocomplete="off"
                      spellcheck="false"
                      [disabled]="readOnly()"
                      [value]="bindSecret()"
                      (input)="bindSecret.set($any($event.target).value)"
                    />
                    <span class="s-hint">
                      What the value is stored under, such as cap-token. The
                      entry keeps $secret:cap-token.
                    </span>
                  </div>

                  <div class="s-field">
                    <label class="s-label" for="cap-bind-prefix">
                      Prefix (optional)
                    </label>
                    <input
                      id="cap-bind-prefix"
                      class="s-input s-mono"
                      type="text"
                      [placeholder]="isStdio ? '' : 'Bearer '"
                      [disabled]="readOnly()"
                      [value]="bindPrefix()"
                      (input)="bindPrefix.set($any($event.target).value)"
                    />
                    <span class="s-hint">
                      Text put before the secret, such as “Bearer ” for an
                      Authorization header.
                    </span>
                  </div>

                  <div class="s-field">
                    <label class="s-label" for="cap-bind-value">
                      Value (optional)
                    </label>
                    <input
                      id="cap-bind-value"
                      class="s-input s-mono"
                      type="password"
                      autocomplete="new-password"
                      [disabled]="readOnly()"
                      [value]="bindValue()"
                      (input)="bindValue.set($any($event.target).value)"
                    />
                    <span class="s-hint">
                      The credential itself. Leave it empty to point at a secret
                      the store already holds.
                    </span>
                  </div>

                  <div class="cap-bind-actions s-inline">
                    <button
                      type="submit"
                      class="btn-outlined s-btn-sm"
                      [disabled]="readOnly() || bindBusy()"
                    >
                      {{ bindBusy() ? "Binding…" : "Bind secret" }}
                    </button>
                    <button
                      type="button"
                      class="btn-text s-btn-sm"
                      (click)="binding.set(false)"
                    >
                      Cancel
                    </button>
                  </div>
                </form>
              }
            }
          </section>

          @if (!isInProcess && hasSettingsSection()) {
            <section
              class="cap-section"
              aria-label="Server settings"
              data-testid="capability-server-settings"
            >
              <div>
                <h3 class="cap-section-title">Server settings</h3>
                <p class="s-card-desc" data-testid="capability-section-caption">
                  Options this server defines for itself, such as the accounts,
                  scripts or chat spaces it offers. grain sends them to the
                  server each time it connects; attempts that start after a
                  change use them.
                </p>
              </div>

              @if (srv.status?.configSchema) {
                <div>
                  <button
                    type="button"
                    class="btn-outlined s-btn-sm"
                    [disabled]="readOnly()"
                    [attr.aria-expanded]="configuring()"
                    data-testid="capability-edit-settings-btn"
                    (click)="toggleConfiguring(srv)"
                  >
                    Edit server settings
                  </button>
                </div>
              } @else if (isBuiltin && srv.status?.error) {
                <div>
                  <button
                    type="button"
                    class="btn-outlined s-btn-sm"
                    disabled
                    data-testid="capability-server-configure-unavailable"
                  >
                    Edit server settings
                  </button>
                </div>
              }

              @if (configuring() && srv.status?.configSchema) {
                <div
                  class="cap-form-stack"
                  data-testid="capability-server-config"
                >
                  <p class="s-hint">
                    The fields below are the ones this server asks for. A secret
                    field is write-only: its chip says whether the store holds a
                    value (set), the entry names one the store lacks (missing)
                    or none was given (not set); type to replace it, or leave it
                    empty to keep it.
                  </p>

                  @if (isConfigMap()) {
                    <g-config-map-editor
                      [server]="srv"
                      [value]="configMapDraft()"
                      [perSecretValues]="configPerSecretValues()"
                      [errors]="configFieldErrors()"
                      [readOnly]="readOnly()"
                      (valueChange)="configMapDraft.set($event)"
                      (perSecretValuesChange)="
                        configPerSecretValues.set($event)
                      "
                    />
                  } @else {
                    <g-capability-args-form
                      [schema]="srv.status?.configSchema ?? null"
                      [value]="configDraft()"
                      [errors]="configFieldErrors()"
                      [idPrefix]="'config-' + srv.id"
                      [disabled]="readOnly()"
                      [secrets]="configSecretsAdapter()"
                      (valueChange)="configDraft.set($event)"
                      (secretChange)="onConfigSecretChange($event)"
                      (rotateManaged)="onRotateManaged(srv)"
                    />
                  }

                  <div class="s-inline">
                    <button
                      type="button"
                      class="btn-primary s-btn-sm"
                      [disabled]="readOnly() || configBusy()"
                      data-testid="save-server-settings-btn"
                      (click)="saveServerConfig(srv)"
                    >
                      Save server settings
                    </button>
                    <button
                      type="button"
                      class="btn-outlined s-btn-sm"
                      [disabled]="readOnly() || configBusy()"
                      data-testid="test-server-settings-btn"
                      (click)="testServerConfig(srv)"
                    >
                      Test server settings
                    </button>
                    <button
                      type="button"
                      class="btn-text s-btn-sm"
                      (click)="configuring.set(false)"
                    >
                      Cancel
                    </button>
                  </div>

                  <p class="s-hint">
                    Test tries the values above, typed secrets included, without
                    storing them. Saving checks them against the server's own
                    rules first; attempts that start afterwards use them.
                  </p>

                  <g-capability-probe-result [result]="configProbeResult()" />
                </div>
              }

              @if (isHostScript()) {
                <g-host-script-list [server]="srv" [readOnly]="readOnly()" />
              }
              @if (isGChat()) {
                <g-gchat-space-list [server]="srv" [readOnly]="readOnly()" />
              }
              @if (isFigma()) {
                <g-figma-account-list [server]="srv" [readOnly]="readOnly()" />
              }
              @if (isConfigMap()) {
                <g-config-map-list [server]="srv" />
              }
            </section>
          }

          <section
            class="cap-section"
            aria-label="Granted by"
            data-testid="capability-server-usage"
          >
            <div>
              <h3 class="cap-section-title">Granted by</h3>
              <p class="s-card-desc" data-testid="capability-section-caption">
                The open tasks, templates, schedules, Settings-declared workflow
                jobs and default grants that name one of this server's methods.
              </p>
            </div>
            <g-capability-usage-list [holders]="holders()" />
          </section>

          @if (!isInProcess) {
            <section
              class="cap-section"
              aria-label="Check and manage"
              data-testid="capability-server-manage"
            >
              <div>
                <h3 class="cap-section-title">Check and manage</h3>
                <p class="s-card-desc" data-testid="capability-section-caption">
                  Try the server as it is saved, or take it out of use.
                </p>
              </div>

              <div class="cap-action-row">
                <div class="cap-action-btn-cell">
                  <button
                    type="button"
                    class="btn-outlined s-btn-sm"
                    [disabled]="readOnly() || savedTesting()"
                    data-testid="capability-server-test-btn"
                    (click)="testSavedServer(srv)"
                  >
                    {{ savedTesting() ? "Testing…" : "Test" }}
                  </button>
                </div>
                <p class="s-card-desc" data-testid="capability-action-help">
                  Starts the saved entry in a session of its own, lists what it
                  offers and runs its server settings. Saves nothing and changes
                  nothing for any task.
                </p>
              </div>

              <g-capability-probe-result [result]="savedProbeResult()" />

              <div class="cap-action-row">
                <div class="cap-action-btn-cell">
                  <button
                    type="button"
                    class="btn-outlined s-btn-sm"
                    [disabled]="readOnly() || toggling()"
                    data-testid="capability-server-toggle-btn"
                    (click)="toggleEnabled(srv)"
                  >
                    {{ srv.config?.disabled ? "Enable" : "Disable" }}
                  </button>
                </div>
                <p class="s-card-desc" data-testid="capability-action-help">
                  @if (srv.config?.disabled) {
                    Turns the server back on: grain connects to it again and
                    admits grants of its methods.
                  } @else {
                    Turns the server off but keeps its entry and settings. Until
                    it is enabled again, anything that grants its methods is
                    refused when it next starts.
                  }
                </p>
              </div>

              @if (!isBuiltin) {
                <div class="cap-action-row">
                  <div class="cap-action-btn-cell">
                    <button
                      type="button"
                      class="btn-outlined danger s-btn-sm"
                      [disabled]="readOnly()"
                      data-testid="capability-server-remove-btn"
                      (click)="askRemove(srv)"
                    >
                      Remove
                    </button>
                  </div>
                  <p class="s-card-desc" data-testid="capability-action-help">
                    Deletes the entry from grain/config. Anything still granting
                    its methods is refused until those grants change or a server
                    is added back under the same ID; you see that list before
                    anything is removed.
                  </p>
                </div>
              }
            </section>
          }
        </div>
      }

      @if (confirmReq(); as req) {
        <g-confirm-dialog [request]="req" (done)="onConfirmDone($event)" />
      }
    </div>
  `,
})
export class CapabilityServerDetailComponent implements OnInit, OnDestroy {
  private readonly api = inject(GrainApi);
  private readonly router = inject(Router);
  protected readonly access = inject(AccessService);
  protected readonly state = inject(SettingsStateService);
  private readonly zone = inject(NgZone);

  readonly serverId = input.required<string>();

  protected readonly transportOptions = TRANSPORT_OPTIONS;
  protected readonly grantedHelp = GRANTED_HELP;

  protected readonly loading = signal(true);
  protected readonly error = signal("");
  protected readonly servers = signal<CapabilityServer[] | null>(null);
  protected readonly usageByServer = signal<
    Record<string, CapabilityGrantHolder[]>
  >({});
  protected readonly hostEntries = signal<HostEntriesResponse | null>(null);

  // Open forms: editing connection, binding a secret, configuring server settings.
  protected readonly editing = signal(false);
  protected readonly editForm = signal<ServerFormState>(formFromServer(null));
  private editInitialJson = "";
  protected readonly editBusy = signal(false);
  protected readonly editTesting = signal(false);
  protected readonly editProbeResult = signal<ProbeResultData | null>(null);

  protected readonly binding = signal(false);
  protected readonly bindName = signal("");
  protected readonly bindSecret = signal("");
  protected readonly bindPrefix = signal("");
  protected readonly bindValue = signal("");
  protected readonly bindBusy = signal(false);

  protected readonly configuring = signal(false);
  protected readonly configDraft = signal<Record<string, unknown>>({});
  protected readonly configMapDraft = signal<ConfigMapEditorState>({});
  private configInitialJson = "";
  protected readonly configSecretValues = signal<Record<string, string>>({});
  protected readonly configPerSecretValues = signal<Record<string, string>>({});
  protected readonly configFieldErrors = signal<Record<string, string>>({});
  protected readonly configBusy = signal(false);
  protected readonly configProbeResult = signal<ProbeResultData | null>(null);

  protected readonly savedTesting = signal(false);
  protected readonly savedProbeResult = signal<ProbeResultData | null>(null);
  protected readonly toggling = signal(false);
  protected readonly confirmReq = signal<ConfirmRequest | null>(null);

  private hostEntriesTimer: ReturnType<typeof setInterval> | null = null;
  private pollTimer: ReturnType<typeof setTimeout> | null = null;
  private unreportedReads = 0;
  private alive = true;

  protected readonly readOnly = computed(
    () =>
      this.state.sectionLocked("capabilities") ||
      !this.access.can("PUT /api/capability-servers"),
  );

  protected readonly activeServer = computed<CapabilityServer | null>(() => {
    const list = this.servers();
    if (!list) return null;
    const id = this.serverId();
    return list.find((s) => s.id === id) ?? null;
  });

  protected readonly holders = computed<CapabilityGrantHolder[]>(() => {
    const srv = this.activeServer();
    if (!srv) return [];
    return this.usageByServer()[srv.id] ?? [];
  });

  protected readonly displayTitle = computed(() => {
    const srv = this.activeServer();
    return srv ? serverDisplayTitle(srv) : "";
  });

  protected readonly summaryParts = computed(() => {
    const srv = this.activeServer();
    return summarizeServer(srv ? serverSummaryText(srv) : "");
  });

  protected readonly currentState = computed(() => {
    const srv = this.activeServer();
    return srv ? serverState(srv) : "unknown";
  });

  protected readonly currentRuns = computed(() => {
    const srv = this.activeServer();
    return srv
      ? serverRuns(srv)
      : { key: "http", label: "remote (HTTP)", help: "" };
  });

  protected readonly waitingText = computed(() => {
    const srv = this.activeServer();
    return srv ? waitingFor(srv) : "";
  });

  protected readonly stateTone = computed(() =>
    statePillTone(this.currentState()),
  );

  protected readonly stateChipLabel = computed(() => {
    const w = this.waitingText();
    if (w) return `waiting for ${w}`;
    const st = this.currentState();
    return STATE_LABELS[st] || needsHostLabel(st) || st;
  });

  protected readonly stateTooltip = computed(() => {
    if (this.waitingText()) return WAITING_HELP;
    const st = this.currentState();
    return STATE_HELP[st] || (needsHostd(st) ? NEEDS_HOSTD_HELP : "");
  });

  protected readonly stateHelpText = computed(() => {
    const st = this.currentState();
    return STATE_HELP[st] || (needsHostd(st) ? NEEDS_HOSTD_HELP : "");
  });

  protected readonly kindHelpText = computed(() => {
    const srv = this.activeServer();
    if (!srv) return "";
    if (srv.inProcess) return KIND_HELP.inProcess;
    if (srv.builtin) return KIND_HELP.builtin;
    return KIND_HELP.custom;
  });

  protected readonly hostdProblem = computed(() => {
    const srv = this.activeServer();
    return hostdEntryProblem(this.hostEntries(), srv?.config?.command);
  });

  protected readonly connectionEntries = computed<[string, string][]>(() => {
    const srv = this.activeServer();
    if (!srv || srv.inProcess) return [];
    const map = srv.config?.command
      ? srv.config.env || {}
      : srv.config?.headers || {};
    return Object.entries(map);
  });

  protected readonly isHostScript = computed(() =>
    isHostScriptServer(this.activeServer()),
  );
  protected readonly isGChat = computed(() =>
    isGChatServer(this.activeServer()),
  );
  protected readonly isFigma = computed(() =>
    isFigmaServer(this.activeServer()),
  );
  protected readonly isConfigMap = computed(() =>
    isConfigMapServer(this.activeServer()),
  );

  protected readonly hasSettingsSection = computed(() => {
    const srv = this.activeServer();
    if (!srv || srv.inProcess) return false;
    return (
      Boolean(srv.status?.configSchema) ||
      Boolean(srv.builtin && srv.status?.error) ||
      this.isHostScript() ||
      this.isGChat() ||
      this.isFigma() ||
      this.isConfigMap()
    );
  });

  protected readonly editHostChoice = computed(() =>
    hostChoice(this.hostEntries(), this.editForm().command),
  );

  protected readonly runLocationOptions = computed<PillOption[]>(() => [
    { value: "container", label: "container (beside the daemon)" },
    {
      value: "host",
      label: "host (through the host agent)",
      disabled: !this.editHostChoice().ok,
    },
  ]);

  protected readonly configSecretsAdapter =
    computed<ServerConfigSecretsContext>(() => {
      const srv = this.activeServer();
      const schema = srv?.status?.configSchema;
      const stored = srv?.config?.config || {};
      const present: Record<string, boolean> = {};
      for (const s of srv?.secrets || []) present[s.name] = s.present;
      const fields: Record<string, "set" | "missing" | "unset"> = {};
      for (const name of secretFieldNames(schema)) {
        const ref = stored[name];
        if (typeof ref === "string" && ref.startsWith(SECRET_PREFIX)) {
          fields[name] = present[ref.slice(SECRET_PREFIX.length)]
            ? "set"
            : "missing";
        } else {
          fields[name] = "unset";
        }
      }
      const managed: Record<string, CapabilityServerManagedField> = {};
      for (const m of srv?.managed || []) managed[m.field] = m;
      return {
        fields,
        values: this.configSecretValues(),
        managed,
        canRotate: !this.readOnly() && !this.configBusy() && Boolean(srv),
      };
    });

  protected onConfigSecretChange(ev: { name: string; value: string }): void {
    this.configSecretValues.update((prev) => ({
      ...prev,
      [ev.name]: ev.value,
    }));
  }

  protected onRotateManaged(srv: CapabilityServer): void {
    void this.rotateManaged(srv);
  }

  private readonly hasUnsavedChanges = computed(() => {
    if (this.editing()) {
      if (JSON.stringify(this.editForm()) !== this.editInitialJson) {
        return true;
      }
    }
    if (this.configuring()) {
      const cur = this.isConfigMap()
        ? JSON.stringify(this.configMapDraft())
        : JSON.stringify(this.configDraft());
      if (cur !== this.configInitialJson) return true;
      if (Object.values(this.configSecretValues()).some(Boolean)) return true;
      if (Object.values(this.configPerSecretValues()).some(Boolean)) {
        return true;
      }
    }
    return false;
  });

  constructor() {
    // Reset inline forms when navigating to a different server ID.
    effect(() => {
      void this.serverId();
      this.editing.set(false);
      this.binding.set(false);
      this.configuring.set(false);
      this.editProbeResult.set(null);
      this.configProbeResult.set(null);
      this.savedProbeResult.set(null);
      this.error.set("");
    });
  }

  @HostListener("window:beforeunload", ["$event"])
  protected onBeforeUnload(ev: BeforeUnloadEvent): void {
    if (!this.hasUnsavedChanges()) return;
    ev.preventDefault();
    ev.returnValue = "";
  }

  ngOnInit(): void {
    void this.refresh();
    void this.refreshUsage();
    void this.refreshHostEntries();
    this.zone.runOutsideAngular(() => {
      this.hostEntriesTimer = setInterval(
        () => this.zone.run(() => void this.refreshHostEntries()),
        30_000,
      );
    });
  }

  ngOnDestroy(): void {
    this.alive = false;
    if (this.hostEntriesTimer) clearInterval(this.hostEntriesTimer);
    if (this.pollTimer) clearTimeout(this.pollTimer);
  }

  protected commandLine(srv: CapabilityServer): string {
    return [srv.config?.command || "", ...(srv.config?.args || [])]
      .filter(Boolean)
      .join(" ");
  }

  private schedulePollIfNeeded(list: CapabilityServer[]): void {
    if (this.pollTimer) {
      clearTimeout(this.pollTimer);
      this.pollTimer = null;
    }
    if (!this.alive) return;
    const live = list.filter((s) => !s.config?.disabled);
    const pending =
      live.some((s) => s.status?.state === "pending") ||
      list.some(
        (s) => s.config?.disabled && s.status && s.status.state !== "disabled",
      );
    const unreported = live.some((s) => !s.status);
    const settling = unreported && this.unreportedReads < UNREPORTED_READ_LIMIT;
    if (!pending && !settling) return;
    this.zone.runOutsideAngular(() => {
      this.pollTimer = setTimeout(() => {
        this.zone.run(() => {
          if (!pending) this.unreportedReads += 1;
          void this.refresh();
        });
      }, 2000);
    });
  }

  private async refresh(): Promise<void> {
    try {
      const data = await this.api.get<CapabilityServer[]>(
        "GET /api/capability-servers",
      );
      if (!this.alive) return;
      const list = Array.isArray(data) ? data : [];
      this.servers.set(list);
      this.loading.set(false);
      this.schedulePollIfNeeded(list);
    } catch (err) {
      if (!this.alive) return;
      this.loading.set(false);
      this.error.set(errText(err));
    }
  }

  private async refreshUsage(): Promise<void> {
    try {
      const data = await this.api.get<
        { id: string; holders?: CapabilityGrantHolder[] }[]
      >("GET /api/capability-servers/usage");
      if (!this.alive) return;
      const byId: Record<string, CapabilityGrantHolder[]> = {};
      for (const u of Array.isArray(data) ? data : []) {
        byId[u.id] = u.holders ?? [];
      }
      this.usageByServer.set(byId);
    } catch {
      if (!this.alive) return;
      this.usageByServer.set({});
    }
  }

  private async refreshHostEntries(): Promise<void> {
    try {
      const data = await this.api.get<HostEntriesResponse>(
        "GET /api/capability-servers/host-entries",
      );
      if (!this.alive) return;
      this.hostEntries.set(data || { available: false });
    } catch (err) {
      if (!this.alive) return;
      this.hostEntries.set({
        available: false,
        reason: errText(err),
        entries: [],
      });
    }
  }

  private applyServerList(data: unknown): void {
    const list = Array.isArray(data) ? (data as CapabilityServer[]) : [];
    this.unreportedReads = 0;
    this.api.clearConditionalCache("/api/capability-methods");
    this.servers.set(list);
    this.schedulePollIfNeeded(list);
    void this.refreshUsage();
  }

  protected toggleEditing(srv: CapabilityServer): void {
    if (this.editing()) {
      this.editing.set(false);
      return;
    }
    const next = formFromServer(srv);
    this.editForm.set(next);
    this.editInitialJson = JSON.stringify(next);
    this.editProbeResult.set(null);
    this.binding.set(false);
    this.editing.set(true);
  }

  protected patchEditForm(patch: Partial<ServerFormState>): void {
    this.editForm.update((f) => ({ ...f, ...patch }));
  }

  protected toggleBinding(): void {
    if (this.binding()) {
      this.binding.set(false);
      return;
    }
    this.bindName.set("");
    this.bindSecret.set("");
    this.bindPrefix.set("");
    this.bindValue.set("");
    this.editing.set(false);
    this.binding.set(true);
  }

  private buildConnectionConfig(
    srv: CapabilityServer,
  ): CapabilityServerConfig | null {
    try {
      return configFromForm(this.editForm(), srv.config);
    } catch (err) {
      this.error.set(errText(err));
      return null;
    }
  }

  protected async submitEdit(ev: Event, srv: CapabilityServer): Promise<void> {
    ev.preventDefault();
    const config = this.buildConnectionConfig(srv);
    if (!config) return;
    const nextId = this.editForm().id.trim();
    if (!nextId) return;
    if (nextId !== srv.id) {
      this.askRename(srv, nextId, config);
      return;
    }
    this.editBusy.set(true);
    this.error.set("");
    try {
      const data = await this.api.put<CapabilityServer[]>(
        "PUT /api/capability-servers",
        undefined,
        { id: nextId, config },
      );
      this.applyServerList(data);
      this.editing.set(false);
      this.editProbeResult.set(null);
    } catch (err) {
      this.error.set(errText(err));
    } finally {
      this.editBusy.set(false);
    }
  }

  protected async testEditForm(srv: CapabilityServer): Promise<void> {
    const config = this.buildConnectionConfig(srv);
    if (!config) return;
    this.editTesting.set(true);
    this.editProbeResult.set(null);
    try {
      const res = await this.api.post<ProbeResultData>(
        "POST /api/capability-servers/test",
        undefined,
        { id: this.editForm().id.trim(), config },
      );
      this.editProbeResult.set(res);
    } catch (err) {
      this.editProbeResult.set({ ok: false, error: errText(err) });
    } finally {
      this.api.clearConditionalCache("/api/capability-methods");
      this.editTesting.set(false);
    }
  }

  protected async submitBindSecret(
    ev: Event,
    srv: CapabilityServer,
  ): Promise<void> {
    ev.preventDefault();
    this.bindBusy.set(true);
    this.error.set("");
    try {
      const data = await this.api.post<CapabilityServer[]>(
        "POST /api/capability-servers/bind-secret",
        undefined,
        {
          id: srv.id,
          kind: srv.config?.command ? "env" : "header",
          name: this.bindName().trim(),
          secret: this.bindSecret().trim(),
          prefix: this.bindPrefix(),
          value: this.bindValue(),
        },
      );
      this.applyServerList(data);
      this.binding.set(false);
      this.bindValue.set("");
    } catch (err) {
      this.error.set(errText(err));
    } finally {
      this.bindBusy.set(false);
    }
  }

  protected toggleConfiguring(srv: CapabilityServer): void {
    if (this.configuring()) {
      this.configuring.set(false);
      return;
    }
    const schema = srv.status?.configSchema;
    const secretNames = secretFieldNames(schema);
    const stored = srv.config?.config || {};
    const plain: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(stored)) {
      if (!secretNames.includes(k)) plain[k] = v;
    }
    if (isConfigMapServer(srv)) {
      const cmVal = configMapEditorValue(plain);
      this.configMapDraft.set(cmVal);
      this.configInitialJson = JSON.stringify(cmVal);
    } else {
      this.configDraft.set(plain);
      this.configInitialJson = JSON.stringify(plain);
    }
    this.configSecretValues.set({});
    this.configPerSecretValues.set({});
    this.configFieldErrors.set({});
    this.configProbeResult.set(null);
    this.configuring.set(true);
  }

  private configPayload(srv: CapabilityServer): Record<string, unknown> {
    const schema = srv.status?.configSchema;
    const stored = srv.config?.config || {};
    const refs: Record<string, string> = {};
    for (const name of secretFieldNames(schema)) {
      const ref = stored[name];
      if (typeof ref === "string" && ref.startsWith(SECRET_PREFIX)) {
        refs[name] = ref;
      }
    }
    if (isConfigMapServer(srv)) {
      const normalized = normalizeConfigMapPayload(
        this.configMapDraft(),
        this.configPerSecretValues(),
      );
      return {
        id: srv.id,
        config: {
          ...srv.config,
          config: { ...normalized.config, ...refs },
        },
        configSecrets: normalized.configSecrets,
      };
    }
    const typed: Record<string, string> = {};
    for (const [k, v] of Object.entries(this.configSecretValues())) {
      if (v) typed[k] = v;
    }
    return {
      id: srv.id,
      config: {
        ...srv.config,
        config: { ...this.configDraft(), ...refs },
      },
      configSecrets: typed,
    };
  }

  protected async saveServerConfig(srv: CapabilityServer): Promise<void> {
    this.configBusy.set(true);
    this.error.set("");
    try {
      const data = await this.api.put<CapabilityServer[]>(
        "PUT /api/capability-servers",
        undefined,
        this.configPayload(srv),
      );
      this.configFieldErrors.set({});
      this.configSecretValues.set({});
      this.configPerSecretValues.set({});
      this.applyServerList(data);
      this.configuring.set(false);
    } catch (err) {
      const fieldErrs = extractFieldErrors(err);
      if (Object.keys(fieldErrs).length > 0) {
        this.configFieldErrors.set(fieldErrs);
      } else {
        this.configFieldErrors.set({});
        this.error.set(errText(err));
      }
    } finally {
      this.configBusy.set(false);
    }
  }

  protected async testServerConfig(srv: CapabilityServer): Promise<void> {
    this.configBusy.set(true);
    this.configProbeResult.set(null);
    try {
      const res = await this.api.post<ProbeResultData>(
        "POST /api/capability-servers/test",
        undefined,
        this.configPayload(srv),
      );
      this.configProbeResult.set(res);
    } catch (err) {
      this.configProbeResult.set({ ok: false, error: errText(err) });
    } finally {
      this.api.clearConditionalCache("/api/capability-methods");
      this.configBusy.set(false);
    }
  }

  private async rotateManaged(srv: CapabilityServer): Promise<void> {
    this.configBusy.set(true);
    this.error.set("");
    try {
      const data = await this.api.post<CapabilityServer[]>(
        "POST /api/capability-servers/rotate-managed",
        undefined,
        { id: srv.id },
      );
      this.applyServerList(data);
    } catch (err) {
      this.error.set(errText(err));
    } finally {
      this.configBusy.set(false);
    }
  }

  protected async dismissNotice(srv: CapabilityServer): Promise<void> {
    this.error.set("");
    try {
      const data = await this.api.post<CapabilityServer[]>(
        "POST /api/capability-servers/dismiss-notice",
        undefined,
        { id: srv.id },
      );
      this.applyServerList(data);
    } catch (err) {
      this.error.set(errText(err));
    }
  }

  protected async testSavedServer(srv: CapabilityServer): Promise<void> {
    this.savedTesting.set(true);
    this.savedProbeResult.set(null);
    try {
      const res = await this.api.post<ProbeResultData>(
        "POST /api/capability-servers/test",
        undefined,
        { id: srv.id },
      );
      this.savedProbeResult.set(res);
    } catch (err) {
      this.savedProbeResult.set({ ok: false, error: errText(err) });
    } finally {
      this.api.clearConditionalCache("/api/capability-methods");
      this.savedTesting.set(false);
    }
  }

  protected async toggleEnabled(srv: CapabilityServer): Promise<void> {
    this.toggling.set(true);
    this.error.set("");
    try {
      const data = await this.api.post<CapabilityServer[]>(
        srv.config?.disabled
          ? "POST /api/capability-servers/enable"
          : "POST /api/capability-servers/disable",
        undefined,
        { id: srv.id },
      );
      this.applyServerList(data);
    } catch (err) {
      this.error.set(errText(err));
    } finally {
      this.toggling.set(false);
    }
  }

  private holdersSummaryMessage(
    lead: string,
    holders: CapabilityGrantHolder[],
  ): string {
    if (holders.length === 0) {
      return `${lead} Nothing grants this server's capabilities.`;
    }
    return (
      `${lead} These still grant its capabilities, and will be refused them until they are changed: ` +
      holders.map(formatHolderLine).join("; ")
    );
  }

  protected onConfirmDone(accepted: boolean): void {
    const req = this.confirmReq();
    this.confirmReq.set(null);
    if (accepted && req) {
      void req.run();
    }
  }

  protected askRemove(srv: CapabilityServer): void {
    this.confirmReq.set({
      title: "Remove this capability server?",
      message: this.holdersSummaryMessage(
        `${srv.id} will be detached.`,
        this.holders(),
      ),
      accept: "Remove",
      destructive: true,
      run: async () => {
        try {
          const data = await this.api.post<CapabilityServer[]>(
            "POST /api/capability-servers/remove",
            undefined,
            { id: srv.id },
          );
          this.applyServerList(data);
          await this.router.navigate(["/settings", "capabilities"]);
        } catch (err) {
          this.error.set(errText(err));
        }
      },
    });
  }

  private askRename(
    srv: CapabilityServer,
    nextId: string,
    config: CapabilityServerConfig,
  ): void {
    this.confirmReq.set({
      title: "Rename this capability server?",
      message: this.holdersSummaryMessage(
        `${srv.id} becomes ${nextId}.`,
        this.holders(),
      ),
      accept: "Rename",
      destructive: false,
      run: async () => {
        try {
          const data = await this.api.put<CapabilityServer[]>(
            "PUT /api/capability-servers",
            undefined,
            { id: nextId, config, renameFrom: srv.id },
          );
          this.applyServerList(data);
          this.editing.set(false);
          await this.router.navigate(["/settings", "capabilities", nextId]);
        } catch (err) {
          this.error.set(errText(err));
        }
      },
    });
  }
}
