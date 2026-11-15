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
  type OnChanges,
  type OnInit,
  type SimpleChanges,
  ViewEncapsulation,
  computed,
  effect,
  inject,
  input,
  signal,
  untracked,
} from "@angular/core";
import { GrainApi } from "../api/grain-api.service";
import { type VersionInfo, versionLabel } from "../api/version";
import { AccessService } from "../services/access.service";
import { KeyboardService } from "../services/keyboard.service";
import { NavConfigService } from "../services/nav-config.service";
import { SnackbarService } from "../services/snackbar.service";
import {
  type ThemeMode,
  ThemeService,
  readStorage,
  writeStorage,
} from "../services/theme.service";
import {
  ConfirmDialogComponent,
  type ConfirmRequest,
} from "../shared/confirm-dialog.component";
import {
  type PillOption,
  PillSelectComponent,
} from "../shared/pill-select.component";
import { MarkdownDirective } from "../shared/code-highlight";
import { effectiveDisplayTimeZone } from "../shared/dispatch-status";
import { StatusIconComponent } from "../shared/status-icon.component";
import { TipDirective } from "../shared/tip.directive";
import { formatWakeTime } from "../shared/zoned-day";
import type { SettingsSectionId } from "./settings-sections";
import { SettingsStateService } from "./settings-state.service";

const TZ_STORAGE_KEY = "grain.timeZone";

// The secret and key the daemon reads its hub registration credential
// from (pkg/secrets HubRegistrationTokenSecret / AgentCredentialKey).
const HUB_TOKEN_SECRET = "registry-registration-token";
const HUB_TOKEN_KEY = "value";

// HubSnapshot is the part of GET /api/registry (model.HubSnapshot) the
// Hub page shows.
interface HubSnapshot {
  enabled?: boolean;
  state?: string;
  effectiveHostKind?: string;
  tokenConfigured?: boolean;
  lastHeartbeatAt?: string;
  heartbeatIntervalSeconds?: number;
  lastError?: string;
  health?: string;
  healthDetail?: string;
  activeTasks?: number;
  awaitingReplyTasks?: number;
  scopeConflicts?: HubScopeConflict[] | null;
  advertisedScopes?: HubAdvertisedScope[] | null;
}

interface HubScopeConflict {
  repo: string;
  path_prefix?: string;
  conflicting_entry?: string;
}

interface HubAdvertisedScope {
  repo: string;
  pathPrefix?: string;
  role?: string;
  origin?: string;
}

// HubScopeRule is one of settings.hub.scopes (model.HubScopeRule).
interface HubScopeRule {
  repo: string;
  pathPrefix?: string;
  role?: string;
  exclude?: boolean;
}

// hubStateBadge says GET /api/registry's state in words, with the
// status-pill tone that goes with it.
export function hubStateBadge(
  state: string | undefined,
  enabled: boolean,
): { label: string; tone: string } {
  if (!enabled || !state || state === "disabled") {
    return { label: "Not configured", tone: "neutral" };
  }
  switch (state) {
    case "connected":
      return { label: "Connected", tone: "healthy" };
    case "degraded":
      return { label: "Degraded", tone: "blast" };
    case "error":
      return { label: "Heartbeat error", tone: "error" };
    case "connecting":
      return { label: "Connecting", tone: "neutral" };
    default:
      return { label: state, tone: "neutral" };
  }
}

export function hubOriginLabel(origin: string | undefined): string {
  switch (origin) {
    case "override":
      return "override";
    case "added":
      return "added in settings";
    default:
      return "from repo config";
  }
}

interface UpgradeTargetRow {
  name?: string;
  branch?: string;
  kind?: string;
  ref?: string;
  version?: string;
  commit?: string;
}

interface ReleaseNotePreviewItem {
  version?: string;
  title?: string;
  body?: string;
  text?: string;
}

function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 B";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  if (bytes < 1024 * 1024 * 1024) {
    return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`;
  }
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(2)} GiB`;
}

@Component({
  selector: "g-settings-instance",
  imports: [
    PillSelectComponent,
    ConfirmDialogComponent,
    TipDirective,
    StatusIconComponent,
    MarkdownDirective,
  ],
  styleUrl: "./settings-hub.css",
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @switch (section()) {
      @case ("general") {
        <div class="s-card" data-testid="card-general">
          <h3 class="s-card-title">Deployment identity &amp; UI lock</h3>
          <div class="s-grid">
            <div class="s-field">
              <label class="s-label" for="g-env">Environment name</label>
              <input
                id="g-env"
                class="s-input"
                type="text"
                data-testid="input-environment-name"
                placeholder="Production"
                [value]="state.val('environmentName', '')"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField('environmentName', $any($event.target).value)
                "
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="g-dep-id">Deployment ID</label>
              <input
                id="g-dep-id"
                class="s-input s-mono"
                type="text"
                [value]="state.val('deploymentId', '')"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField('deploymentId', $any($event.target).value)
                "
              />
            </div>
            <div class="s-field">
              <span class="s-label">Display timezone</span>
              <g-pill-select
                testid="select-timezone"
                ariaLabel="Display timezone"
                align="left"
                [options]="timezoneOptions"
                [value]="timeZone()"
                (valueChange)="setTimeZone($event)"
              />
            </div>
          </div>

          <div class="s-toggle-row">
            <div class="s-toggle-copy">
              <span class="s-toggle-name"
                >Lock settings in UI (require config repo PRs)</span
              >
              <span class="s-hint"
                >Makes config-managed sections read-only outside StateRepo pull
                requests.</span
              >
            </div>
            <button
              type="button"
              class="g-switch"
              role="switch"
              data-testid="switch-settings-locked"
              [attr.aria-checked]="Boolean(state.val('settingsLocked', false))"
              [disabled]="!access.can('POST /api/settings')"
              aria-label="Lock settings in UI"
              (click)="
                state.setField(
                  'settingsLocked',
                  !state.val('settingsLocked', false)
                )
              "
            ></button>
          </div>
        </div>

        <div class="s-card" data-testid="card-broadcast">
          <h3 class="s-card-title">Broadcast message to active tasks</h3>
          <p class="s-card-desc">
            Posts an operator comment onto active (<code>running</code>,
            <code>awaiting_reply</code>, <code>queued</code>, and
            <code>awaiting_submit</code>) tasks — waking any tasks currently
            awaiting a reply unless restricted to running tasks only.
          </p>
          <div class="s-toggle-row">
            <div class="s-toggle-copy">
              <span class="s-toggle-name">Running tasks only</span>
              <span class="s-hint"
                >Deliver only to tasks currently running without un-parking
                awaiting_reply or commenting on queued tasks.</span
              >
            </div>
            <button
              type="button"
              class="g-switch"
              role="switch"
              data-testid="switch-broadcast-running-only"
              [attr.aria-checked]="broadcastRunningOnly()"
              [disabled]="!access.can('POST /api/deployment/broadcast')"
              aria-label="Deliver broadcast to running tasks only"
              (click)="broadcastRunningOnly.set(!broadcastRunningOnly())"
            ></button>
          </div>
          <div class="s-inline">
            <input
              class="s-input s-input-inline"
              type="text"
              aria-label="Broadcast message to active tasks"
              data-testid="input-broadcast-message"
              placeholder="Freeze merges on main until 18:00 UTC…"
              [value]="broadcastMessage()"
              [disabled]="!access.can('POST /api/deployment/broadcast')"
              (input)="broadcastMessage.set($any($event.target).value)"
            />
            <button
              type="button"
              class="btn-tonal"
              data-testid="send-broadcast-btn"
              [disabled]="
                !broadcastMessage().trim() ||
                !access.can('POST /api/deployment/broadcast')
              "
              (click)="requestBroadcastConfirm()"
            >
              Broadcast to active tasks
            </button>
          </div>
        </div>
      }

      @case ("preferences") {
        <div class="s-card" data-testid="card-preferences">
          <h3 class="s-card-title">Browser preferences</h3>
          <p class="s-card-desc">
            Stored locally in this browser; never modifies deployment state.
          </p>

          <div class="s-toggle-row">
            <div class="s-toggle-copy">
              <span class="s-toggle-name">Color theme</span>
              <span class="s-hint"
                >Applies Google Material 3 light or dark surface tokens.</span
              >
            </div>
            <div class="segmented" role="group" aria-label="Color theme">
              @for (m of themeModes; track m.value) {
                <button
                  type="button"
                  [attr.aria-pressed]="theme.mode() === m.value"
                  [attr.data-testid]="'pref-theme-' + m.value"
                  (click)="theme.setMode(m.value)"
                >
                  {{ m.label }}
                </button>
              }
            </div>
          </div>

          <div class="s-toggle-row">
            <div class="s-toggle-copy">
              <span class="s-toggle-name">Single-key keyboard shortcuts</span>
              <span class="s-hint"
                >Enable c, /, 1–9, comma, 0, t, ?, and j/k/x queue
                navigation.</span
              >
            </div>
            <button
              type="button"
              class="g-switch"
              role="switch"
              data-testid="pref-shortcuts-switch"
              [attr.aria-checked]="keyboard.enabled()"
              aria-label="Single-key keyboard shortcuts"
              (click)="keyboard.setEnabled(!keyboard.enabled())"
            ></button>
          </div>

          <div class="s-toggle-row">
            <div class="s-toggle-copy">
              <span class="s-toggle-name">Default to modern UI</span>
              <span class="s-hint"
                >Automatically open this Angular UI across Grain routes in this
                browser.</span
              >
            </div>
            <button
              type="button"
              class="g-switch"
              role="switch"
              data-testid="pref-prefer-next-switch"
              [attr.aria-checked]="theme.preferNext()"
              aria-label="Default to modern UI"
              (click)="theme.setPreferNext(!theme.preferNext())"
            ></button>
          </div>

          <div class="s-toggle-row">
            <div class="s-toggle-copy">
              <span class="s-toggle-name">Navigation rail destinations</span>
              <span class="s-hint"
                >Show, hide, or reorder destinations on the left rail.</span
              >
            </div>
          </div>
        </div>
      }

      @case ("cli") {
        <div class="s-card" data-testid="card-cli">
          <h3 class="s-card-title">
            Workstation grain CLI &amp; agent CLI run locations
          </h3>
          <p class="s-card-desc">
            Install the CLI against this Grain server ({{ serverOrigin() }}) and
            choose where each agent CLI executes on dispatch.
          </p>
          <pre class="s-code" data-testid="cli-install-snippet">{{
            cliSnippet()
          }}</pre>

          @for (fw of cliFrameworks; track fw.id) {
            <div class="s-toggle-row">
              <div class="s-toggle-copy">
                <span class="s-toggle-name">{{ fw.label }} CLI execution</span>
                <span class="s-hint">{{ fw.hint }}</span>
              </div>
              <div class="s-inline">
                <g-pill-select
                  [testid]="'select-cli-run-' + fw.id"
                  [ariaLabel]="fw.label + ' CLI execution location'"
                  align="left"
                  [options]="cliLocationOptions"
                  [value]="cliRunLocations()[fw.id] || ''"
                  [disabled]="
                    !access.can('PUT /api/settings/cli-run') ||
                    state.configRepoOnly()
                  "
                  (valueChange)="saveCliRunLocation(fw.id, $event)"
                />
              </div>
            </div>
          }
        </div>
      }

      @case ("caches") {
        <div class="s-card" data-testid="card-caches">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">
                Proxy cache &amp; GitHub Actions cache
              </h3>
              <p class="s-card-desc">
                Shared package download proxy and workflow cache volumes across
                sandboxes.
              </p>
            </div>
            <div class="s-inline">
              <button
                type="button"
                class="btn-tonal s-btn-sm"
                data-testid="flush-package-cache-btn"
                [disabled]="
                  !packageCacheLive() ||
                  !access.can('POST /api/package-cache/flush')
                "
                [gTip]="
                  !access.can('POST /api/package-cache/flush')
                    ? access.deniedReason('POST /api/package-cache/flush')
                    : !packageCacheLive()
                      ? 'Proxy cache is not running on this daemon.'
                      : 'Flush cached packages'
                "
                (click)="confirmFlushPackageCache()"
              >
                Flush proxy cache
              </button>
              <button
                type="button"
                class="btn-tonal s-btn-sm"
                data-testid="flush-actions-cache-btn"
                [disabled]="
                  !actionsCacheLive() ||
                  !access.can('POST /api/actions-cache/flush')
                "
                [gTip]="
                  !access.can('POST /api/actions-cache/flush')
                    ? access.deniedReason('POST /api/actions-cache/flush')
                    : !actionsCacheLive()
                      ? 'Actions cache is not running on this daemon.'
                      : 'Flush GitHub Actions cache archives'
                "
                (click)="confirmFlushActionsCache()"
              >
                Flush Actions cache
              </button>
            </div>
          </div>

          <div class="s-toggle-row">
            <div class="s-toggle-copy">
              <span class="s-toggle-name">Enable package proxy cache</span>
              <span class="s-hint">{{ packageCacheSummary() }}</span>
            </div>
            <button
              type="button"
              class="g-switch"
              role="switch"
              [attr.aria-checked]="
                Boolean(state.val('packageCacheEnabled', true))
              "
              [disabled]="!canWriteSettings()"
              aria-label="Enable package proxy cache"
              (click)="
                state.setField(
                  'packageCacheEnabled',
                  !state.val('packageCacheEnabled', true)
                )
              "
            ></button>
          </div>

          <div class="s-toggle-row">
            <div class="s-toggle-copy">
              <span class="s-toggle-name">Enable Actions cache</span>
              <span class="s-hint">{{ actionsCacheSummary() }}</span>
            </div>
            <button
              type="button"
              class="g-switch"
              role="switch"
              [attr.aria-checked]="
                Boolean(state.val('actionsCacheEnabled', true))
              "
              [disabled]="!canWriteSettings()"
              aria-label="Enable Actions cache"
              (click)="
                state.setField(
                  'actionsCacheEnabled',
                  !state.val('actionsCacheEnabled', true)
                )
              "
            ></button>
          </div>

          <div class="s-grid">
            <div class="s-field">
              <label class="s-label" for="c-pkg-gb"
                >Proxy cache max size (GiB)</label
              >
              <input
                id="c-pkg-gb"
                class="s-input s-num"
                type="number"
                min="0"
                [value]="state.defaultedNumber('packageCacheSizeGb')"
                [placeholder]="state.defaultHint('packageCacheSizeGb')"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setNumber(
                    'packageCacheSizeGb',
                    $any($event.target).value
                  )
                "
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="c-act-gb"
                >Actions cache max size (GiB)</label
              >
              <input
                id="c-act-gb"
                class="s-input s-num"
                type="number"
                min="0"
                [value]="state.defaultedNumber('actionsCacheSizeGb')"
                [placeholder]="state.defaultHint('actionsCacheSizeGb')"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setNumber(
                    'actionsCacheSizeGb',
                    $any($event.target).value
                  )
                "
              />
            </div>
          </div>
        </div>
      }

      @case ("engine") {
        <div class="s-card" data-testid="card-engine">
          <h3 class="s-card-title">
            Dispatch engine concurrency &amp; cadence
          </h3>
          <div class="s-grid">
            <div class="s-field">
              <label class="s-label" for="e-poll">Poll interval</label>
              <input
                id="e-poll"
                class="s-input s-num"
                type="text"
                data-testid="input-poll-interval"
                [placeholder]="state.defaultHint('pollInterval', '10s')"
                [value]="state.val('pollInterval', '')"
                [disabled]="!canWriteSettings()"
                aria-describedby="e-poll-hint"
                (input)="
                  state.setField('pollInterval', $any($event.target).value)
                "
              />
              <span class="s-hint" id="e-poll-hint"
                >Go duration, e.g. 10s. Blank uses grain's default ({{
                  state.val("pollIntervalDefault", "10s")
                }}).</span
              >
            </div>
            <div class="s-field">
              <label class="s-label" for="e-workers">Max worker agents</label>
              <input
                id="e-workers"
                class="s-input s-num"
                type="number"
                min="1"
                data-testid="input-max-workers"
                [value]="state.defaultedNumber('maxWorkers')"
                aria-describedby="e-workers-hint"
                [placeholder]="state.defaultHint('maxWorkers', '10')"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setNumber(
                    'maxWorkers',
                    $any($event.target).value,
                    10,
                    true
                  )
                "
                (change)="
                  state.setNumber('maxWorkers', $any($event.target).value, 10)
                "
              />
              <span class="s-hint" id="e-workers-hint"
                >Ordinary tasks dispatched at once (10 by default; blank
                resets).</span
              >
            </div>
            <div class="s-field">
              <label class="s-label" for="e-mpr"
                >Max merges per repo branch</label
              >
              <input
                id="e-mpr"
                class="s-input s-num"
                type="number"
                min="1"
                data-testid="input-max-merges-per-repo"
                [value]="state.defaultedNumber('maxMergesPerRepo')"
                aria-describedby="e-mpr-hint"
                [placeholder]="state.defaultHint('maxMergesPerRepo', '5')"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setNumber(
                    'maxMergesPerRepo',
                    $any($event.target).value,
                    5,
                    true
                  )
                "
                (change)="
                  state.setNumber(
                    'maxMergesPerRepo',
                    $any($event.target).value,
                    5
                  )
                "
              />
              <span class="s-hint" id="e-mpr-hint"
                >Ready pull requests merged into each branch per queue cycle (5
                by default; blank resets).</span
              >
            </div>
          </div>

          <div class="s-toggle-row">
            <div class="s-toggle-copy">
              <span class="s-toggle-name"
                >Work through backlog newest-first</span
              >
              <span class="s-hint"
                >Within equal priority bands, dispatch newer tasks first.</span
              >
            </div>
            <button
              type="button"
              class="g-switch"
              role="switch"
              [attr.aria-checked]="Boolean(state.val('newestFirst', false))"
              [disabled]="!canWriteSettings()"
              aria-label="Work through backlog newest-first"
              (click)="
                state.setField('newestFirst', !state.val('newestFirst', false))
              "
            ></button>
          </div>
        </div>
      }

      @case ("hub") {
        <div class="s-card" id="registry-status" data-testid="card-registry">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Hub connection</h3>
              <p class="s-card-desc">
                Advertise repository scopes and health heartbeats to a central
                Grain Hub directory.
              </p>
            </div>
            <div class="s-inline">
              <span
                class="status-pill s-mono"
                [class]="hubBadge().tone"
                data-testid="registry-status-pill"
              >
                <g-status-icon [tone]="hubBadge().tone" />
                {{ hubBadge().label }}
              </span>
              <button
                type="button"
                class="btn-tonal s-btn-sm"
                data-testid="registry-heartbeat-btn"
                [disabled]="
                  !hubEnabled() ||
                  heartbeating() ||
                  !access.can('POST /api/registry/heartbeat')
                "
                [gTip]="
                  hubEnabled()
                    ? 'Register and heartbeat to the hub now'
                    : 'Set a Hub URL and entry name first'
                "
                (click)="sendHeartbeat()"
              >
                {{ heartbeating() ? "Sending…" : "Heartbeat now" }}
              </button>
            </div>
          </div>

          @if (hubEnabled() && hubEndpoint()) {
            <span class="s-mono hub-endpoint" data-testid="registry-endpoint">{{
              hubEndpoint()
            }}</span>
          }
          <span class="s-caption" data-testid="registry-heartbeat-line">{{
            hubHeartbeatLine()
          }}</span>

          @if (hubSnapshot()?.lastError; as err) {
            <div
              class="s-banner error hub-banner"
              role="alert"
              data-testid="registry-last-error"
            >
              <g-status-icon tone="error" />
              <div class="hub-banner-body" [gMarkdown]="err"></div>
            </div>
          }
          @if (hubConflicts().length > 0) {
            <div
              class="s-banner warn hub-banner"
              role="status"
              data-testid="registry-scope-conflicts"
            >
              <g-status-icon tone="warn" />
              <div class="hub-banner-body">
                <strong>Scope conflict reported by the hub</strong>
                <ul class="hub-conflict-list">
                  @for (c of hubConflicts(); track $index) {
                    <li>
                      <span class="s-mono">{{ c.repo }}</span>
                      @if (c.path_prefix) {
                        <span class="s-mono"> ({{ c.path_prefix }})</span>
                      }
                      is already claimed as primary by
                      <span class="s-mono">{{
                        c.conflicting_entry || "another entry"
                      }}</span>
                    </li>
                  }
                </ul>
              </div>
            </div>
          }

          <div class="s-subsection">
            <span class="s-label">Advertised scopes</span>
            @if (hubAdvertised().length === 0) {
              <span class="s-hint" data-testid="registry-no-scopes"
                >No repositories advertised yet. Configured repositories are
                reported automatically from Settings → Repositories.</span
              >
            } @else {
              <ul
                class="hub-scope-list"
                data-testid="registry-advertised-scopes"
              >
                @for (
                  sc of hubAdvertised();
                  track sc.repo + ":" + sc.pathPrefix
                ) {
                  <li class="hub-scope-row" data-testid="registry-scope-row">
                    <span class="hub-scope-main">
                      <span class="s-mono hub-scope-repo">{{ sc.repo }}</span>
                      <span class="hub-chip s-mono">{{
                        sc.pathPrefix ? sc.pathPrefix + "/" : "/ (whole repo)"
                      }}</span>
                    </span>
                    <span class="hub-scope-meta">
                      <span
                        class="status-pill"
                        [class.healthy]="sc.role === 'primary'"
                        [class.neutral]="sc.role !== 'primary'"
                        data-testid="registry-scope-role"
                        >{{ sc.role }}</span
                      >
                      <span
                        class="hub-chip hub-origin"
                        data-testid="registry-scope-origin"
                        >{{ hubOriginLabel(sc.origin) }}</span
                      >
                    </span>
                  </li>
                }
              </ul>
            }
          </div>
        </div>

        <div class="s-card" id="registry-config" data-testid="card-hub-config">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Catalog &amp; authentication</h3>
              <p class="s-card-desc">
                Where this grain registers, how its heartbeats authenticate, and
                how its card reads in the hub directory.
              </p>
            </div>
          </div>

          <div class="s-grid">
            <div class="s-field">
              <label class="s-label" for="reg-url">Hub URL</label>
              <input
                id="reg-url"
                class="s-input s-mono"
                type="text"
                autocomplete="off"
                data-testid="input-hub-url"
                placeholder="https://hub.grain.internal"
                [value]="hubField('url')"
                [disabled]="!canWriteSettings()"
                (input)="setHubField('url', $any($event.target).value)"
              />
              <span class="s-hint"
                >Leave empty together with Entry name to turn self-registration
                off.</span
              >
            </div>
            <div class="s-field">
              <label class="s-label" for="reg-entry">Entry name</label>
              <input
                id="reg-entry"
                class="s-input s-mono"
                type="text"
                autocomplete="off"
                data-testid="input-hub-entry"
                placeholder="acme-widgets"
                [value]="hubField('entry')"
                [disabled]="!canWriteSettings()"
                (input)="setHubField('entry', $any($event.target).value)"
              />
              <span class="s-hint"
                >A unique 3–32 character lowercase DNS label in the hub
                directory.</span
              >
            </div>
            <div class="s-field">
              <span class="s-label">Credential mode</span>
              <g-pill-select
                testid="select-hub-auth-mode"
                ariaLabel="Hub credential mode"
                align="left"
                [options]="hubAuthOptions"
                [value]="hubField('authMode') || 'token'"
                [disabled]="!canWriteSettings()"
                (valueChange)="setHubField('authMode', $event)"
              />
              <span class="s-hint"
                >How heartbeats authenticate to the hub.</span
              >
            </div>
            <div class="s-field">
              <label class="s-label" for="reg-oidc-aud">OIDC audience</label>
              <input
                id="reg-oidc-aud"
                class="s-input s-mono"
                type="text"
                autocomplete="off"
                data-testid="input-hub-oidc-audience"
                [placeholder]="hubField('url') || 'defaults to the Hub URL'"
                [value]="hubField('oidcAudience')"
                [disabled]="!canWriteSettings() || hubAuthMode() !== 'oidc'"
                (input)="setHubField('oidcAudience', $any($event.target).value)"
              />
              <span class="s-hint">{{
                hubAuthMode() === "oidc"
                  ? "The audience of the ID token; blank uses the Hub URL."
                  : "Used only when Credential mode is Workload OIDC."
              }}</span>
            </div>
            <div class="s-field">
              <span class="s-label">Platform</span>
              <g-pill-select
                testid="select-hub-host-kind"
                ariaLabel="Hub platform (host kind)"
                align="left"
                [options]="hubHostKindOptions()"
                [value]="hubField('hostKind')"
                [disabled]="!canWriteSettings()"
                (valueChange)="setHubField('hostKind', $event)"
              />
              <span class="s-hint"
                >The host kind this grain reports. Auto-detect follows the
                sandbox backend.</span
              >
            </div>
            <div class="s-field">
              <label class="s-label" for="reg-disp">Display name</label>
              <input
                id="reg-disp"
                class="s-input"
                type="text"
                autocomplete="off"
                data-testid="input-hub-display-name"
                [value]="hubField('displayName')"
                [disabled]="!canWriteSettings()"
                (input)="setHubField('displayName', $any($event.target).value)"
              />
              <span class="s-hint"
                >The title on this grain's card in the hub directory.</span
              >
            </div>
            <div class="s-field hub-field-wide">
              <label class="s-label" for="reg-purpose">Purpose</label>
              <input
                id="reg-purpose"
                class="s-input"
                type="text"
                autocomplete="off"
                data-testid="input-hub-purpose"
                [value]="hubField('purpose')"
                [disabled]="!canWriteSettings()"
                (input)="setHubField('purpose', $any($event.target).value)"
              />
              <span class="s-hint"
                >A short description of the repositories or workflows this grain
                handles.</span
              >
            </div>
          </div>

          <div class="s-toggle-row">
            <div class="s-toggle-copy">
              <span class="s-toggle-name">Listed in hub directory</span>
              <span class="s-hint"
                >When off, this grain stays addressable by name and by route
                queries for its owners, but is left out of default directory
                listings.</span
              >
            </div>
            <button
              type="button"
              class="g-switch"
              role="switch"
              data-testid="switch-hub-listed"
              aria-label="Listed in hub directory"
              [attr.aria-checked]="hubListed()"
              [disabled]="!canWriteSettings()"
              (click)="setHubValue('listed', !hubListed())"
            ></button>
          </div>

          <div class="s-subsection" id="registry-token">
            <span class="s-label">Registration credential</span>
            <span class="s-hint"
              >Minted by <code>grain hub register</code> (or rotated in the hub)
              and stored encrypted in this daemon's secret store. It is never
              exported to <code>grain/config</code>.</span
            >
            <form
              class="s-inline"
              autocomplete="off"
              (submit)="$event.preventDefault(); storeHubToken()"
            >
              <span
                class="status-pill"
                [class.healthy]="hubTokenSet()"
                [class.neutral]="!hubTokenSet()"
                data-testid="registry-token-state"
              >
                <g-status-icon [tone]="hubTokenSet() ? 'healthy' : 'neutral'" />
                {{ hubTokenSet() ? "Stored" : "Not set" }}
              </span>
              <input
                class="s-input s-input-inline s-mono"
                type="password"
                name="grain-hub-registration-token"
                autocomplete="new-password"
                data-lpignore="true"
                data-1p-ignore="true"
                aria-label="Registration token"
                data-testid="input-hub-token"
                [placeholder]="
                  hubTokenSet() ? 'Replace the stored grc_… token' : 'grc_…'
                "
                [value]="hubTokenValue()"
                [disabled]="!canWriteHubToken()"
                (input)="hubTokenValue.set($any($event.target).value)"
              />
              <button
                type="submit"
                class="btn-tonal s-btn-sm"
                data-testid="store-hub-token-btn"
                [disabled]="
                  !canWriteHubToken() || hubTokenValue().trim().length === 0
                "
              >
                {{ hubTokenSet() ? "Replace" : "Store" }}
              </button>
              @if (hubTokenSet()) {
                <button
                  type="button"
                  class="s-btn-danger s-btn-sm"
                  data-testid="clear-hub-token-btn"
                  [disabled]="!canWriteHubToken()"
                  (click)="confirmClearHubToken()"
                >
                  Clear
                </button>
              }
            </form>
            @if (!canWriteHubToken()) {
              <span class="s-hint" data-testid="registry-token-restricted">{{
                access.deniedReason("PUT /api/secrets/{secret}/{key}")
              }}</span>
            }
          </div>
        </div>

        <div class="s-card" id="registry-scopes" data-testid="card-hub-scopes">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Scope overrides</h3>
              <p class="s-card-desc">
                Scopes come from Settings → Repositories and each repository's
                work ownership (<code>on</code> → primary, <code>off</code> →
                secondary). Add a rule only to narrow a repository to a path
                prefix, exclude it from hub routing, or advertise an extra
                scope.
              </p>
            </div>
          </div>

          @if (hubRules().length > 0) {
            <ul class="hub-scope-list" data-testid="registry-scope-rules">
              @for (r of hubRules(); track $index) {
                <li class="hub-scope-row" data-testid="registry-rule-row">
                  <span class="hub-scope-main">
                    <span class="s-mono hub-scope-repo">{{ r.repo }}</span>
                    @if (r.pathPrefix) {
                      <span class="hub-chip s-mono">{{ r.pathPrefix }}/</span>
                    }
                  </span>
                  <span class="hub-scope-meta">
                    <span
                      class="status-pill"
                      [class.healthy]="!r.exclude && r.role !== 'secondary'"
                      [class.neutral]="r.exclude || r.role === 'secondary'"
                      data-testid="registry-rule-mode"
                      >{{ r.exclude ? "excluded" : r.role || "primary" }}</span
                    >
                    <button
                      type="button"
                      class="s-btn-danger s-btn-sm"
                      [attr.data-testid]="'remove-hub-rule-' + $index"
                      [attr.aria-label]="'Remove scope rule for ' + r.repo"
                      [gTip]="'Remove scope rule for ' + r.repo"
                      [disabled]="!canWriteSettings()"
                      (click)="removeHubRule($index)"
                    >
                      Remove
                    </button>
                  </span>
                </li>
              }
            </ul>
          } @else {
            <span class="s-hint" data-testid="registry-no-rules"
              >No overrides. Every configured repository is advertised as its
              work ownership says.</span
            >
          }

          <form
            class="s-subsection hub-rule-form"
            autocomplete="off"
            (submit)="$event.preventDefault(); addHubRule()"
          >
            <div class="s-grid">
              <div class="s-field">
                <label class="s-label" for="hub-rule-repo"
                  >Repository (owner/name)</label
                >
                <input
                  id="hub-rule-repo"
                  class="s-input s-mono"
                  type="text"
                  autocomplete="off"
                  spellcheck="false"
                  data-testid="input-hub-rule-repo"
                  placeholder="acme/monorepo"
                  [value]="newRuleRepo()"
                  [disabled]="!canWriteSettings()"
                  (input)="newRuleRepo.set($any($event.target).value)"
                />
              </div>
              <div class="s-field">
                <label class="s-label" for="hub-rule-prefix"
                  >Path prefix (optional)</label
                >
                <input
                  id="hub-rule-prefix"
                  class="s-input s-mono"
                  type="text"
                  autocomplete="off"
                  spellcheck="false"
                  data-testid="input-hub-rule-prefix"
                  placeholder="services/billing"
                  [value]="newRulePrefix()"
                  [disabled]="!canWriteSettings()"
                  (input)="newRulePrefix.set($any($event.target).value)"
                />
              </div>
              <div class="s-field">
                <span class="s-label">Rule</span>
                <g-pill-select
                  testid="select-hub-rule-mode"
                  ariaLabel="Scope rule"
                  align="left"
                  [options]="hubRuleOptions"
                  [value]="newRuleMode()"
                  [disabled]="!canWriteSettings()"
                  (valueChange)="newRuleMode.set($event)"
                />
              </div>
            </div>
            <div class="s-inline hub-rule-actions">
              <button
                type="submit"
                class="btn-tonal s-btn-sm"
                data-testid="add-hub-rule-btn"
                [disabled]="!canWriteSettings() || !newRuleRepo().trim()"
              >
                Add rule
              </button>
              <span class="s-hint"
                >Rules are staged with the page's other changes; Save changes
                applies them.</span
              >
            </div>
          </form>
        </div>
      }

      @case ("upgrade") {
        <div class="s-card" data-testid="card-upgrade">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Daemon version &amp; live upgrade</h3>
              <p class="s-card-desc">
                Inspect target channels, release notes, and roll forward or
                cancel active upgrades.
              </p>
            </div>
            <span
              class="status-pill healthy s-mono"
              data-testid="current-daemon-version"
            >
              {{ currentVersion() }}
            </span>
          </div>

          @if (selfUpgradeDisabled()) {
            <div
              class="s-banner readonly"
              role="status"
              data-testid="upgrade-externally-managed"
            >
              <span
                >In-VM self-upgrade is disabled on this deployment; upgrades and
                instance lifecycle are managed externally.</span
              >
              @if (managementUrl(); as href) {
                <a
                  class="s-btn-sm"
                  data-testid="upgrade-management-link"
                  [href]="href"
                  target="_blank"
                  rel="noopener noreferrer"
                  >Manage instance</a
                >
              }
            </div>
          } @else if (upgradeUnavailable()) {
            <div
              class="s-banner readonly"
              role="status"
              data-testid="upgrade-unavailable"
            >
              <span
                >Not available: this deployment was started with neither
                -upgrade-image nor -upgrade-src-dir configured, so there is
                nothing here that can cut it over to another branch.</span
              >
            </div>
          }

          @if (scheduledUpgradeBranch()) {
            <div class="s-banner warn" data-testid="scheduled-upgrade-banner">
              <span
                >Upgrade scheduled (waiting for running tasks):
                {{ scheduledUpgradeBranch() }}</span
              >
              <button
                type="button"
                class="s-btn-danger s-btn-sm"
                data-testid="cancel-scheduled-upgrade-btn"
                [disabled]="!access.can('DELETE /api/upgrade/scheduled')"
                (click)="cancelScheduledUpgrade()"
              >
                Cancel scheduled upgrade
              </button>
            </div>
          }

          @if (activeUpgradeState()) {
            <div class="s-banner warn" data-testid="active-upgrade-banner">
              <span>Upgrade in progress: {{ activeUpgradeState() }}</span>
              <button
                type="button"
                class="s-btn-danger s-btn-sm"
                data-testid="cancel-upgrade-btn"
                [disabled]="
                  !upgradeCancellable() ||
                  !access.can('DELETE /api/upgrade/scheduled')
                "
                (click)="cancelUpgrade()"
              >
                Cancel upgrade
              </button>
            </div>
          }

          @if (upgradeControllable()) {
            <div class="s-grid">
              <div class="s-field">
                <span class="s-label">Target channel / branch</span>
                <g-pill-select
                  testid="select-upgrade-target"
                  ariaLabel="Upgrade target branch"
                  align="left"
                  [options]="upgradeTargetOptions()"
                  [value]="selectedUpgradeBranch()"
                  (valueChange)="onSelectUpgradeBranch($event)"
                />
              </div>
              <div class="s-field">
                <span class="s-label">Post-upgrade check mode</span>
                <g-pill-select
                  testid="select-upgrade-check-mode"
                  ariaLabel="Post-upgrade check mode"
                  align="left"
                  [options]="upgradeCheckOptions"
                  [value]="state.val('upgradeCheckMode', 'auto')"
                  [disabled]="!canWriteSettings()"
                  (valueChange)="state.setField('upgradeCheckMode', $event)"
                />
              </div>
            </div>

            <div class="s-toggle-row">
              <div class="s-toggle-copy">
                <span class="s-toggle-name"
                  >Drain running tasks before restarting</span
                >
                <span class="s-hint"
                  >Waits for active agent turns to finish cleanly before
                  switching binaries.</span
                >
              </div>
              <button
                type="button"
                class="g-switch"
                role="switch"
                data-testid="switch-upgrade-drain"
                [attr.aria-checked]="afterTasksFinish()"
                aria-label="Drain running tasks before restarting"
                (click)="afterTasksFinish.set(!afterTasksFinish())"
              ></button>
            </div>

            <div class="s-inline">
              <button
                type="button"
                class="btn-primary"
                data-testid="start-upgrade-btn"
                [disabled]="
                  !selectedUpgradeBranch() || !access.can('POST /api/upgrade')
                "
                (click)="confirmStartUpgrade()"
              >
                Start upgrade
              </button>
              @if (dbBackendLabel()) {
                <span class="s-hint s-num"
                  >Database: {{ dbBackendLabel() }}</span
                >
              }
            </div>

            @if (releaseNotesText()) {
              <pre class="s-code" data-testid="upgrade-release-notes">{{
                releaseNotesText()
              }}</pre>
            }
          } @else if (dbBackendLabel()) {
            <span class="s-hint s-num" data-testid="upgrade-database"
              >Database: {{ dbBackendLabel() }}</span
            >
          }
        </div>
      }
    }

    @if (confirmRequest(); as req) {
      <g-confirm-dialog [request]="req" (done)="onConfirmDone($event)" />
    }
  `,
})
export class SettingsInstanceComponent implements OnInit, OnChanges {
  protected readonly api = inject(GrainApi);
  protected readonly access = inject(AccessService);
  protected readonly state = inject(SettingsStateService);
  protected readonly theme = inject(ThemeService);
  protected readonly keyboard = inject(KeyboardService);
  private readonly navConfig = inject(NavConfigService);
  private readonly snackbar = inject(SnackbarService);
  private readonly doc = inject(DOCUMENT);

  readonly section = input.required<SettingsSectionId>();

  protected readonly Number = Number;
  protected readonly Boolean = Boolean;

  protected readonly confirmRequest = signal<ConfirmRequest | null>(null);

  protected readonly themeModes: readonly {
    value: ThemeMode;
    label: string;
  }[] = [
    { value: "light", label: "Light" },
    { value: "dark", label: "Dark" },
    { value: "auto", label: "System" },
  ];

  protected readonly timezoneOptions: readonly PillOption[] = [
    { value: "local", label: "Browser local time" },
    { value: "UTC", label: "UTC" },
    { value: "America/Los_Angeles", label: "America/Los_Angeles (PT)" },
    { value: "America/New_York", label: "America/New_York (ET)" },
    { value: "Europe/London", label: "Europe/London" },
  ];

  protected readonly cliFrameworks = [
    {
      id: "claude",
      label: "Claude",
      hint: "Where Claude Code CLI sessions execute.",
    },
    {
      id: "codex",
      label: "Codex",
      hint: "Where Codex CLI sessions execute.",
    },
    {
      id: "antigravity",
      label: "Antigravity",
      hint: "Where Antigravity (agy) CLI sessions execute.",
    },
  ] as const;

  protected readonly cliLocationOptions: readonly PillOption[] = [
    { value: "", label: "Deployment default" },
    { value: "host", label: "Host (grain host)" },
    { value: "container", label: "Daemon container" },
    { value: "detached", label: "Detached host session" },
  ];

  protected readonly hubAuthOptions: readonly PillOption[] = [
    { value: "token", label: "Shared bearer token" },
    { value: "oidc", label: "Workload OIDC" },
  ];

  protected readonly upgradeCheckOptions: readonly PillOption[] = [
    { value: "off", label: "Off" },
    { value: "approve", label: "File unapproved check" },
    { value: "auto", label: "File & queue check" },
  ];

  protected readonly timeZone = signal(readStorage(TZ_STORAGE_KEY) || "local");
  protected readonly broadcastMessage = signal("");
  protected readonly broadcastRunningOnly = signal(false);
  protected readonly cliRunLocations = signal<Record<string, string>>({});

  protected readonly packageCacheLive = signal(false);
  protected readonly actionsCacheLive = signal(false);
  protected readonly packageCacheSummary = signal("Shared package proxy cache");
  protected readonly actionsCacheSummary = signal(
    "Workflow step & dependency cache",
  );

  protected readonly hubRuleOptions: readonly PillOption[] = [
    { value: "primary", label: "Primary" },
    { value: "secondary", label: "Secondary" },
    { value: "exclude", label: "Exclude repo" },
  ];

  protected readonly hubSnapshot = signal<HubSnapshot | null>(null);
  protected readonly heartbeating = signal(false);
  protected readonly hubTokenValue = signal("");
  protected readonly newRuleRepo = signal("");
  protected readonly newRulePrefix = signal("");
  protected readonly newRuleMode = signal("primary");
  protected readonly hubOriginLabel = hubOriginLabel;

  // The saved URL and entry turn self-registration on; a staged edit
  // does not until it is saved, so this reads what the server reports
  // and falls back to the stored settings before it answers.
  protected readonly hubEnabled = computed(() => {
    const snap = this.hubSnapshot();
    if (snap) return snap.enabled === true;
    const h = this.state.settings()["hub"] as
      Record<string, unknown> | undefined;
    return Boolean(h?.["url"] && h?.["entry"]);
  });
  protected readonly hubBadge = computed(() =>
    hubStateBadge(this.hubSnapshot()?.state, this.hubEnabled()),
  );
  protected readonly hubEndpoint = computed(() => {
    const h = (this.state.settings()["hub"] ?? {}) as Record<string, unknown>;
    const url = String(h["url"] ?? "");
    const entry = String(h["entry"] ?? "");
    return url && entry ? `${url} · ${entry}` : "";
  });
  protected readonly hubHeartbeatLine = computed(() => {
    const snap = this.hubSnapshot();
    if (!this.hubEnabled()) {
      return "Set a Hub URL and entry name below (or run grain hub register) so this daemon registers with the hub and heartbeats its version, health, task counts and repository scopes.";
    }
    if (!snap?.lastHeartbeatAt) return "Waiting for the first heartbeat…";
    const when = formatWakeTime(
      snap.lastHeartbeatAt,
      effectiveDisplayTimeZone(this.timeZone()),
    );
    const every = snap.heartbeatIntervalSeconds
      ? ` (every ${snap.heartbeatIntervalSeconds}s)`
      : "";
    const health = snap.health || "ok";
    const detail = snap.healthDetail ? ` (${snap.healthDetail})` : "";
    return `Last heartbeat ${when}${every} · health ${health}${detail} · ${snap.activeTasks ?? 0} active, ${snap.awaitingReplyTasks ?? 0} awaiting reply`;
  });
  protected readonly hubConflicts = computed(
    () => this.hubSnapshot()?.scopeConflicts ?? [],
  );
  protected readonly hubAdvertised = computed(
    () => this.hubSnapshot()?.advertisedScopes ?? [],
  );
  protected readonly hubTokenSet = computed(() => {
    const s = this.state.settings();
    return Boolean(s["hubTokenSet"] ?? s["registryTokenSet"]);
  });
  // A saved Hub URL, entry, mode or token changes what GET /api/registry
  // reports (enabled, state, scopes), so the snapshot is read again
  // whenever the stored hub settings change, as the classic page does.
  private lastHubKey: string | null = null;
  private readonly hubReload = effect(() => {
    if (this.section() !== "hub") return;
    const s = this.state.settings();
    const key = JSON.stringify([s["hub"] ?? null, this.hubTokenSet()]);
    if (this.lastHubKey === null) {
      this.lastHubKey = key;
      return;
    }
    if (key === this.lastHubKey) return;
    this.lastHubKey = key;
    untracked(() => void this.loadRegistry());
  });
  protected readonly hubHostKindOptions = computed<PillOption[]>(() => {
    const eff = this.hubSnapshot()?.effectiveHostKind;
    // The effective kind is the chosen one while a kind is chosen, so it
    // says what Auto-detect picks only while nothing is.
    const auto =
      !this.hubField("hostKind") && eff
        ? `Auto-detect (${eff})`
        : "Auto-detect";
    return [
      { value: "", label: auto },
      { value: "workstation", label: "Workstation" },
      { value: "kubernetes", label: "Kubernetes" },
      { value: "other", label: "Other" },
    ];
  });

  // What the daemon reports it runs (GET /api/upgrade's `running`), else
  // the build stamp GET /api/config carries -- an object on current
  // servers, so it is read through versionLabel rather than as a string.
  private readonly upgradeRunningLabel = signal("");
  protected readonly currentVersion = computed(
    () =>
      this.upgradeRunningLabel() ||
      runningVersionLabel(this.state.config()?.version) ||
      "unknown",
  );
  // GET /api/upgrade's `enabled: false`: no upgrader is configured
  // (neither -upgrade-image nor -upgrade-src-dir), so there are no targets
  // to read and nothing Start upgrade could do.
  protected readonly upgradeUnavailable = signal(false);
  protected readonly selfUpgradeDisabled = signal(false);
  protected readonly managementUrl = signal("");
  protected readonly upgradeControllable = computed(
    () => !this.upgradeUnavailable() && !this.selfUpgradeDisabled(),
  );
  protected readonly activeUpgradeState = signal("");
  protected readonly upgradeCancellable = signal(false);
  protected readonly scheduledUpgradeBranch = signal("");
  protected readonly upgradeTargets = signal<UpgradeTargetRow[]>([]);
  protected readonly selectedUpgradeBranch = signal("latest");
  protected readonly afterTasksFinish = signal(true);
  protected readonly releaseNotesText = signal("");
  protected readonly dbBackendLabel = signal("");

  protected upgradeTargetOptions(): PillOption[] {
    const t = this.upgradeTargets();
    if (t.length === 0) {
      return [
        { value: "latest", label: "latest" },
        { value: "main", label: "main" },
      ];
    }
    return t
      .map((row) => {
        const val = row.name || row.branch || "";
        if (!val) return null;
        const extra = row.version || row.ref;
        return {
          value: val,
          label: extra && extra !== val ? `${val} (${extra})` : val,
        };
      })
      .filter((x): x is PillOption => x !== null);
  }

  ngOnInit(): void {
    this.loadForSection();
  }

  ngOnChanges(changes: SimpleChanges): void {
    if (changes["section"] && !changes["section"].firstChange) {
      this.loadForSection();
    }
  }

  protected canWriteSettings(): boolean {
    return (
      this.access.can("POST /api/settings") &&
      !this.state.configRepoOnly() &&
      !this.state.sectionLocked(this.section())
    );
  }

  protected openCustomizeNav(ev: Event): void {
    this.navConfig.openDialog(ev.currentTarget as HTMLElement | null);
  }

  protected setTimeZone(tz: string): void {
    this.timeZone.set(tz);
    writeStorage(TZ_STORAGE_KEY, tz === "local" ? null : tz);
    this.access.tickClock();
  }

  protected serverOrigin(): string {
    return this.doc.defaultView?.location?.origin || "http://localhost:8080";
  }

  // cliSnippet is what a workstation runs to install the grain CLI, sign
  // in and add the agent skills -- the classic Settings -> CLI lines
  // (ui/src/components/settings/CLISection.jsx). The installer is
  // /cli/install.sh (pkg/ui/cli_install.go), never /api/hostd/install.sh:
  // that one installs a host, wants root and a registered name, and is
  // no way to get the CLI onto a laptop.
  protected cliSnippet(): string {
    const origin = this.serverOrigin();
    let host = "";
    try {
      host = new URL(origin).hostname;
    } catch {
      // An origin that is not a URL names no host; the slug is "default".
    }
    // The context name `grain login` gives a deployment when --name is
    // left out (pkg/cli/login.go contextSlug).
    const slug =
      host
        .toLowerCase()
        .replace(/[^a-z0-9]+/g, "-")
        .replace(/^-+|-+$/g, "") || "default";
    return [
      `curl -fsSL ${origin}/cli/install.sh | sh`,
      `grain login ${origin} --name ${slug} --issue-token --scope-set grain/external-dev --expires 30d`,
      `grain skill install --jetski   # or --claude, --codex, --gemini`,
    ].join("\n");
  }

  private loadForSection(): void {
    const sec = this.section();
    if (sec === "cli") {
      if (this.access.can("GET /api/settings/cli-run")) {
        void this.api
          .get<{
            settings?: Record<string, { location?: string }>;
          }>("GET /api/settings/cli-run")
          .then((r) => {
            const map: Record<string, string> = {};
            const raw = r?.settings ?? {};
            for (const fw of this.cliFrameworks) {
              map[fw.id] = String(raw[fw.id]?.location ?? "");
            }
            this.cliRunLocations.set(map);
          })
          .catch(() => undefined);
      }
    } else if (sec === "caches") {
      if (this.access.can("GET /api/package-cache")) {
        void this.api
          .get<{
            enabled?: boolean;
            available?: boolean;
            cache?: { kind: string; entries?: number; bytes?: number }[];
          }>("GET /api/package-cache")
          .then((p) => {
            const live =
              p?.enabled !== false &&
              p?.available !== false &&
              Array.isArray(p?.cache);
            this.packageCacheLive.set(live);
            if (live && Array.isArray(p?.cache)) {
              let entries = 0;
              let bytes = 0;
              for (const item of p.cache) {
                entries += Number(item.entries) || 0;
                bytes += Number(item.bytes) || 0;
              }
              this.packageCacheSummary.set(
                `${entries} cached packages · ${formatBytes(bytes)}`,
              );
            } else if (p?.enabled === false || p?.available === false) {
              this.packageCacheSummary.set("Proxy cache not enabled on daemon");
            }
          })
          .catch(() => undefined);
      }
      if (this.access.can("GET /api/actions-cache")) {
        void this.api
          .get<Record<string, unknown>>("GET /api/actions-cache")
          .then((a) => {
            const live =
              Boolean(a) &&
              a?.["enabled"] !== false &&
              a?.["available"] !== false;
            this.actionsCacheLive.set(live);
            if (!live) {
              this.actionsCacheSummary.set(
                "Actions cache not enabled on daemon",
              );
            } else if (a?.["entries"] !== undefined) {
              const bytes = Number(a["bytes"]) || 0;
              this.actionsCacheSummary.set(
                bytes > 0
                  ? `Cached entries: ${a["entries"]} · ${formatBytes(bytes)}`
                  : `Cached entries: ${a["entries"]}`,
              );
            }
          })
          .catch(() => undefined);
      }
    } else if (sec === "hub") {
      void this.loadRegistry();
    } else if (sec === "upgrade") {
      void this.loadUpgrade();
    }
  }

  protected requestBroadcastConfirm(): void {
    const msg = this.broadcastMessage().trim();
    if (!msg) return;
    const runningOnly = this.broadcastRunningOnly();
    this.confirmRequest.set({
      title: "Broadcast message to active tasks?",
      message: runningOnly
        ? `This posts a comment onto every currently running task: "${msg}"`
        : `This posts a comment onto every active task (running, awaiting_reply, queued, awaiting_submit) and wakes tasks awaiting a reply: "${msg}"`,
      accept: "Broadcast message",
      destructive: false,
      run: () => this.executeBroadcast(msg, runningOnly),
    });
  }

  protected async onConfirmDone(accepted: boolean): Promise<void> {
    const req = this.confirmRequest();
    this.confirmRequest.set(null);
    if (accepted && req) {
      await req.run();
    }
  }

  private async executeBroadcast(
    message: string,
    runningOnly: boolean,
  ): Promise<void> {
    try {
      const res = await this.api.post<{ delivered_count?: number }>(
        "POST /api/deployment/broadcast",
        undefined,
        {
          message,
          ...(runningOnly ? { running_only: true } : {}),
        },
      );
      this.broadcastMessage.set("");
      const n = res?.delivered_count;
      this.snackbar.show(
        typeof n === "number"
          ? `Broadcast delivered to ${n} ${n === 1 ? "task" : "tasks"}`
          : "Broadcast delivered to active tasks",
      );
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async saveCliRunLocation(
    framework: string,
    location: string,
  ): Promise<void> {
    const nextMap = { ...this.cliRunLocations(), [framework]: location };
    this.cliRunLocations.set(nextMap);
    const settingsPayload: Record<string, { location: string }> = {};
    for (const [fw, loc] of Object.entries(nextMap)) {
      if (loc) {
        settingsPayload[fw] = { location: loc };
      }
    }
    try {
      await this.api.put("PUT /api/settings/cli-run", undefined, {
        settings: settingsPayload,
      });
      this.snackbar.show(`Updated ${framework} CLI run location`);
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected confirmFlushPackageCache(): void {
    this.confirmRequest.set({
      title: "Flush package proxy cache?",
      message:
        "Deletes all cached packages across apt, npm, PyPI, Go, and Docker on this controller. Active runs remain safe and fetch from upstream on cache miss.",
      accept: "Flush cache",
      destructive: true,
      run: () => this.flushPackageCache(),
    });
  }

  protected confirmFlushActionsCache(): void {
    this.confirmRequest.set({
      title: "Flush GitHub Actions cache?",
      message:
        "Empties cached CI build archives across all repositories on this controller.",
      accept: "Flush cache",
      destructive: true,
      run: () => this.flushActionsCache(),
    });
  }

  protected async flushPackageCache(): Promise<void> {
    try {
      const res = await this.api.post<{
        cache?: { kind: string; entries?: number; bytes?: number }[];
      }>("POST /api/package-cache/flush", undefined, {});
      if (Array.isArray(res?.cache)) {
        let entries = 0;
        let bytes = 0;
        for (const item of res.cache) {
          entries += Number(item.entries) || 0;
          bytes += Number(item.bytes) || 0;
        }
        this.packageCacheSummary.set(
          `${entries} cached packages · ${formatBytes(bytes)}`,
        );
      }
      this.snackbar.show("Proxy cache flushed");
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async flushActionsCache(): Promise<void> {
    try {
      const res = await this.api.post<{
        cache?: { entries?: number; bytes?: number };
      }>("POST /api/actions-cache/flush", undefined, {});
      if (res?.cache?.entries !== undefined) {
        const bytes = Number(res.cache.bytes) || 0;
        this.actionsCacheSummary.set(
          bytes > 0
            ? `Cached entries: ${res.cache.entries} · ${formatBytes(bytes)}`
            : `Cached entries: ${res.cache.entries}`,
        );
      } else {
        this.actionsCacheSummary.set("Cached entries: 0");
      }
      this.snackbar.show("Actions cache flushed");
    } catch (err) {
      this.state.recordError(err);
    }
  }

  private hubObject(): Record<string, unknown> {
    const h = this.state.val<unknown>("hub", {});
    return h && typeof h === "object" && !Array.isArray(h)
      ? (h as Record<string, unknown>)
      : {};
  }

  protected hubField(key: string): string {
    return String(this.hubObject()[key] ?? "");
  }

  protected setHubField(key: string, val: string): void {
    this.setHubValue(key, val);
  }

  protected setHubValue(key: string, val: unknown): void {
    this.state.setField("hub", { ...this.hubObject(), [key]: val });
  }

  protected hubAuthMode(): string {
    return this.hubField("authMode") || "token";
  }

  // Unset is unlisted: the daemon lists an entry only when this is true.
  protected hubListed(): boolean {
    return this.hubObject()["listed"] === true;
  }

  protected hubRules(): HubScopeRule[] {
    const r = this.hubObject()["scopes"];
    return Array.isArray(r) ? (r as HubScopeRule[]) : [];
  }

  protected addHubRule(): void {
    const repo = this.newRuleRepo().trim();
    if (!repo) return;
    const pathPrefix = this.newRulePrefix()
      .trim()
      .replace(/^\/+|\/+$/g, "");
    const mode = this.newRuleMode();
    const rule: HubScopeRule =
      mode === "exclude"
        ? { repo, pathPrefix, exclude: true }
        : { repo, pathPrefix, role: mode };
    this.setHubValue("scopes", [...this.hubRules(), rule]);
    this.newRuleRepo.set("");
    this.newRulePrefix.set("");
  }

  protected removeHubRule(idx: number): void {
    this.setHubValue(
      "scopes",
      this.hubRules().filter((_, i) => i !== idx),
    );
  }

  protected canWriteHubToken(): boolean {
    return this.access.can("PUT /api/secrets/{secret}/{key}");
  }

  protected async storeHubToken(): Promise<void> {
    const value = this.hubTokenValue().trim();
    if (!value || !this.canWriteHubToken()) return;
    try {
      await this.api.put(
        "PUT /api/secrets/{secret}/{key}",
        { secret: HUB_TOKEN_SECRET, key: HUB_TOKEN_KEY },
        { value },
      );
      this.hubTokenValue.set("");
      this.snackbar.show("Registration token stored");
      await this.refreshHub();
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected confirmClearHubToken(): void {
    this.confirmRequest.set({
      title: "Clear the registration token?",
      message:
        "The stored value cannot be read back, so putting it back means pasting it again. Token-mode heartbeats fail until a new one is stored.",
      accept: "Clear token",
      destructive: true,
      run: () => this.clearHubToken(),
    });
  }

  private async clearHubToken(): Promise<void> {
    try {
      await this.api.delete("DELETE /api/secrets/{secret}/{key}", {
        secret: HUB_TOKEN_SECRET,
        key: HUB_TOKEN_KEY,
      });
      this.snackbar.show("Registration token cleared");
      await this.refreshHub();
    } catch (err) {
      this.state.recordError(err);
    }
  }

  // refreshHub re-reads the settings (hubTokenSet) and the snapshot
  // after the credential changes.
  private async refreshHub(): Promise<void> {
    await this.state.loadCore(true);
    await this.loadRegistry();
  }

  private async loadRegistry(): Promise<void> {
    if (!this.access.can("GET /api/registry")) return;
    try {
      const r = await this.api.get<HubSnapshot>("GET /api/registry");
      if (r && typeof r === "object") this.hubSnapshot.set(r);
    } catch {
      // Non-fatal: the card reads from the stored settings until
      // GET /api/registry answers.
    }
  }

  protected async sendHeartbeat(): Promise<void> {
    this.heartbeating.set(true);
    try {
      const r = await this.api.post<HubSnapshot>(
        "POST /api/registry/heartbeat",
        undefined,
        {},
      );
      if (r && typeof r === "object") this.hubSnapshot.set(r);
      this.snackbar.show("Heartbeat sent");
    } catch (err) {
      this.state.recordError(err);
      await this.loadRegistry();
    } finally {
      this.heartbeating.set(false);
    }
  }

  private applyUpgradeStatus(
    u: Record<string, unknown> | null | undefined,
  ): void {
    if (!u) return;
    const running = u["running"] as
      | { release?: string; ref?: string; commit?: string; image?: string }
      | undefined;
    const ver =
      running?.release ||
      running?.ref ||
      (typeof u["currentVersion"] === "string" ? u["currentVersion"] : "") ||
      (typeof u["version"] === "string" ? u["version"] : "") ||
      (running?.commit ? running.commit.slice(0, 7) : "");
    this.upgradeRunningLabel.set(ver);
    this.selfUpgradeDisabled.set(u["selfUpgradeDisabled"] === true);
    this.upgradeUnavailable.set(u["enabled"] === false);
    this.managementUrl.set(safeManagementUrl(u["managementUrl"]));

    const scheduled = u["scheduled"] as
      { branch?: string; requestedBy?: string } | undefined | null;
    this.scheduledUpgradeBranch.set(
      scheduled ? String(scheduled.branch || "latest") : "",
    );

    const phase = typeof u["phase"] === "string" ? u["phase"] : "";
    const cancellable = Boolean(u["cancellable"]);
    this.upgradeCancellable.set(cancellable);
    const terminalPhases = new Set([
      "",
      "idle",
      "none",
      "done",
      "succeeded",
      "complete",
      "completed",
      "failed",
      "error",
      "cancelled",
      "canceled",
    ]);
    if (cancellable || (phase && !terminalPhases.has(phase.toLowerCase()))) {
      this.activeUpgradeState.set(phase || "running");
    } else {
      this.activeUpgradeState.set("");
    }
  }

  private async loadUpgrade(): Promise<void> {
    if (this.access.can("GET /api/upgrade")) {
      try {
        const u =
          await this.api.get<Record<string, unknown>>("GET /api/upgrade");
        this.applyUpgradeStatus(u);
      } catch {
        // Non-fatal
      }
    }
    if (
      this.upgradeControllable() &&
      this.access.can("GET /api/upgrade/targets")
    ) {
      try {
        const t = await this.api.get<
          | UpgradeTargetRow[]
          | { targets?: UpgradeTargetRow[]; default?: string }
        >("GET /api/upgrade/targets");
        const list = Array.isArray(t) ? t : (t?.targets ?? []);
        const def =
          (!Array.isArray(t) && typeof t?.default === "string" && t.default) ||
          list[0]?.name ||
          list[0]?.branch ||
          "latest";
        this.upgradeTargets.set(list);
        this.selectedUpgradeBranch.set(def);
        void this.loadReleaseNotes(def);
      } catch {
        // Non-fatal
      }
    }
    if (this.access.can("GET /api/database")) {
      try {
        const db =
          await this.api.get<Record<string, unknown>>("GET /api/database");
        if (db?.["driver"] || db?.["backend"]) {
          this.dbBackendLabel.set(String(db["driver"] ?? db["backend"]));
        }
      } catch {
        // Non-fatal
      }
    }
  }

  protected onSelectUpgradeBranch(branch: string): void {
    this.selectedUpgradeBranch.set(branch);
    void this.loadReleaseNotes(branch);
  }

  private formatReleaseNotesPreview(raw: unknown): string {
    if (typeof raw === "string") return raw;
    if (!raw || typeof raw !== "object") return "";
    const rec = raw as {
      notes?: ReleaseNotePreviewItem[] | string;
      message?: string;
      markdown?: string;
    };
    const parts: string[] = [];
    if (typeof rec.message === "string" && rec.message.trim()) {
      parts.push(rec.message.trim());
    }
    if (typeof rec.notes === "string" && rec.notes.trim()) {
      parts.push(rec.notes.trim());
    } else if (Array.isArray(rec.notes)) {
      for (const n of rec.notes) {
        if (!n || typeof n !== "object") continue;
        const head = [n.version, n.title].filter(Boolean).join(" — ");
        const body = (n.body ?? n.text ?? "").trim();
        if (head && body) parts.push(`## ${head}\n${body}`);
        else if (head) parts.push(`## ${head}`);
        else if (body) parts.push(body);
      }
    } else if (typeof rec.markdown === "string" && rec.markdown.trim()) {
      parts.push(rec.markdown.trim());
    }
    return parts.join("\n\n");
  }

  private async loadReleaseNotes(branch: string): Promise<void> {
    if (!this.access.can("GET /api/upgrade/release-notes")) return;
    try {
      const r = await this.api.get<unknown>(
        "GET /api/upgrade/release-notes",
        undefined,
        `branch=${encodeURIComponent(branch)}`,
      );
      this.releaseNotesText.set(this.formatReleaseNotesPreview(r));
    } catch {
      // Non-fatal
    }
  }

  // Starting an upgrade restarts grain onto another build, so it asks
  // first, as the classic Upgrade pane's confirm() does.
  protected confirmStartUpgrade(): void {
    const branch = this.selectedUpgradeBranch();
    if (!branch) return;
    const drain = this.afterTasksFinish();
    this.confirmRequest.set({
      title: `Upgrade grain to ${branch}?`,
      message: drain
        ? `Grain waits for running tasks to finish, then restarts onto ${branch}.`
        : `Grain restarts onto ${branch} now, interrupting agent runs in flight.`,
      accept: drain ? "Schedule upgrade" : "Upgrade now",
      destructive: !drain,
      run: () => this.startUpgrade(),
    });
  }

  protected async startUpgrade(): Promise<void> {
    try {
      const res = await this.api.post<Record<string, unknown>>(
        "POST /api/upgrade",
        undefined,
        {
          branch: this.selectedUpgradeBranch(),
          afterTasksFinish: this.afterTasksFinish(),
        },
      );
      if (res && typeof res === "object") {
        this.applyUpgradeStatus(res);
      }
      if (this.afterTasksFinish() && !this.scheduledUpgradeBranch()) {
        this.scheduledUpgradeBranch.set(this.selectedUpgradeBranch());
      } else if (!this.afterTasksFinish() && !this.activeUpgradeState()) {
        this.activeUpgradeState.set("starting");
        this.upgradeCancellable.set(true);
      }
      this.snackbar.show(
        this.afterTasksFinish()
          ? `Scheduled upgrade to ${this.selectedUpgradeBranch()}`
          : `Started upgrade to ${this.selectedUpgradeBranch()}`,
      );
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async cancelScheduledUpgrade(): Promise<void> {
    try {
      await this.api.delete("DELETE /api/upgrade/scheduled");
      this.scheduledUpgradeBranch.set("");
      this.snackbar.show("Cancelled scheduled upgrade");
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async cancelUpgrade(): Promise<void> {
    try {
      await this.api.delete(
        "DELETE /api/upgrade/scheduled",
        undefined,
        undefined,
        "reason=operator_cancelled",
      );
      this.activeUpgradeState.set("");
      this.upgradeCancellable.set(false);
      this.snackbar.show("Cancelled active upgrade");
    } catch (err) {
      this.state.recordError(err);
    }
  }
}

// runningVersionLabel names the build GET /api/config's `version` stamp
// describes the way the classic pane's "Now running" does: the release,
// or the short commit when the build carries no real release
// (`unversioned`, a Go pseudo-version).
export function runningVersionLabel(v: unknown): string {
  if (v && typeof v === "object") {
    const info = v as VersionInfo;
    if (info.unversioned && info.commit) return info.commit.slice(0, 7);
  }
  return versionLabel(v);
}

// safeManagementUrl keeps only an absolute http(s) link, as the classic
// ExternallyManagedUpgradeNotice does.
export function safeManagementUrl(raw: unknown): string {
  if (typeof raw !== "string" || !raw) return "";
  try {
    const u = new URL(raw);
    return u.protocol === "https:" || u.protocol === "http:" ? u.href : "";
  } catch {
    return "";
  }
}
