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
import { type ThemeMode, ThemeService } from "../services/theme.service";
import {
  ConfirmDialogComponent,
  type ConfirmRequest,
} from "../shared/confirm-dialog.component";
import {
  type PillOption,
  PillSelectComponent,
} from "../shared/pill-select.component";
import { formatStamp } from "../shared/dispatch-status";
import { TipDirective } from "../shared/tip.directive";
import { errorText } from "../sessions/session-helpers";
import {
  type SandboxCommitment,
  SandboxCommitmentNoteComponent,
} from "./sandbox-commitment-note.component";
import type { SettingsSectionId } from "./settings-sections";
import { SettingsStateService } from "./settings-state.service";

const CLI_SKILL_AGENTS = [
  { id: "jetski", flag: "--jetski", label: "Jetski" },
  { id: "claude", flag: "--claude", label: "Claude Code" },
  { id: "codex", flag: "--codex", label: "Codex" },
  { id: "gemini", flag: "--gemini", label: "Gemini CLI" },
] as const;

function contextSlug(origin: string): string {
  try {
    const host = new URL(origin).hostname;
    const s = host
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-+|-+$/g, "");
    return s || "default";
  } catch {
    return "default";
  }
}

// HUB_TOKEN_SECRET is where the hub registration credential (grc_...)
// lives in the secret store (pkg/ui/hub.go).
const HUB_TOKEN_SECRET = "registry-registration-token";

// HubScopeRule is model.HubScopeRule, one Scope overrides rule.
interface HubScopeRule {
  repo: string;
  pathPrefix?: string;
  role?: string;
  exclude?: boolean;
}

// HubSnapshot is GET /api/registry (model.HubSnapshot), as much of it as
// the Hub cards read.
interface HubSnapshot {
  enabled?: boolean;
  state?: string;
  effectiveHostKind?: string;
  lastHeartbeatAt?: string;
  heartbeatIntervalSeconds?: number;
  lastError?: string;
  scopeConflicts?: {
    repo: string;
    path_prefix?: string;
    conflicting_entry?: string;
  }[];
  health?: string;
  healthDetail?: string;
  activeTasks?: number;
  awaitingReplyTasks?: number;
  advertisedScopes?: {
    repo: string;
    pathPrefix?: string;
    role: string;
    origin?: string;
  }[];
}

interface UpgradeTargetRow {
  name?: string;
  branch?: string;
  kind?: string;
  allowed?: boolean;
  reason?: string;
  schema?: number;
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
    SandboxCommitmentNoteComponent,
  ],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @switch (section()) {
      @case ("general") {
        <div class="s-card" id="deployment" data-testid="card-general">
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
                [placeholder]="deploymentIdPlaceholder()"
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
                [value]="theme.timeZone()"
                (valueChange)="setTimeZone($event)"
              />
              <span class="s-hint" data-testid="general-timezone-hint"
                >This browser only, like Preferences: it changes how times print
                here and is never saved for the deployment.</span
              >
            </div>
          </div>

          <div class="s-toggle-row">
            <div class="s-toggle-copy">
              <span class="s-toggle-name">Show closed tasks by default</span>
              <span class="s-hint"
                >Include closed tasks when opening task lists by default.</span
              >
            </div>
            <button
              type="button"
              class="g-switch"
              role="switch"
              data-testid="switch-show-closed-default"
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

          <div class="s-toggle-row" id="settings-lock">
            <div class="s-toggle-copy">
              <span class="s-toggle-name"
                >Lock settings in UI (require config repo PRs)</span
              >
              <span class="s-hint">{{ settingsLockHint() }}</span>
            </div>
            <button
              type="button"
              class="g-switch"
              role="switch"
              data-testid="switch-settings-locked"
              [attr.aria-checked]="Boolean(state.val('settingsLocked', false))"
              [disabled]="!canToggleSettingsLock()"
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

        <div class="s-card" id="about" data-testid="card-about">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">About</h3>
              <p class="s-card-desc">Running controller build stamp.</p>
            </div>
            <span
              class="status-pill neutral s-mono"
              data-testid="about-daemon-version"
              >{{ currentVersion() }}</span
            >
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
              <span class="s-toggle-name">Display timezone</span>
              <span class="s-hint"
                >Formats timestamps across tasks, models, access, and audit logs
                in this browser.</span
              >
            </div>
            <g-pill-select
              testid="pref-select-timezone"
              ariaLabel="Display timezone"
              align="right"
              [options]="timezoneOptions"
              [value]="theme.timeZone()"
              (valueChange)="setTimeZone($event)"
            />
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
            <button
              type="button"
              class="btn-tonal s-btn-sm"
              data-testid="pref-customize-nav-btn"
              (click)="openCustomizeNav($event)"
            >
              Customize…
            </button>
          </div>
        </div>
      }

      @case ("cli") {
        <div class="s-card" id="cli-install" data-testid="card-cli">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Install workstation CLI</h3>
              <p class="s-card-desc">
                Downloads
                @if (releaseStamp(); as rel) {
                  <code>grain {{ rel }}</code>
                } @else {
                  the grain release
                }
                from {{ serverOrigin() }}, verifies its SHA-256 checksum, and
                installs <code>grain</code> to <code>~/.local/bin</code>.
              </p>
            </div>
            <button
              type="button"
              class="btn-tonal s-btn-sm"
              data-testid="copy-cli-install"
              (click)="copyCliText(cliInstallCmd(), 'Install command')"
            >
              Copy
            </button>
          </div>
          <pre class="s-code" data-testid="cli-install-snippet">{{
            cliInstallCmd()
          }}</pre>
        </div>

        <div class="s-card" id="cli-login" data-testid="card-cli-login">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Sign in</h3>
              <p class="s-card-desc">
                Signs <code>grain</code> in as you and issues a 30-day token
                scoped to <code>grain/external-dev</code>.
              </p>
            </div>
            <button
              type="button"
              class="btn-tonal s-btn-sm"
              data-testid="copy-cli-login"
              (click)="copyCliText(cliLoginCmd(), 'Sign-in command')"
            >
              Copy
            </button>
          </div>
          @if (!Boolean(state.val("externalAgentApi", true))) {
            <div class="s-banner warn" data-testid="cli-external-agent-api-off">
              <span>
                External agent token sign-in is currently off, so
                <code>--issue-token</code> cannot mint a token yet. Turn on
                Accept external agents in
                <a
                  href="/next/settings/agents#external-agents"
                  data-settings-section="agents"
                  data-settings-fragment="external-agents"
                  data-testid="cli-external-agent-api-link"
                  >Settings → Agents</a
                >
                first.
              </span>
            </div>
          }
          <pre class="s-code" data-testid="cli-login-snippet">{{
            cliLoginCmd()
          }}</pre>
        </div>

        <div class="s-card" id="cli-skills" data-testid="card-cli-skills">
          <h3 class="s-card-title">Agent skills</h3>
          <p class="s-card-desc">
            Writes a <code>SKILL.md</code> teaching your workstation coding
            agent to work through <code>grain</code>.
          </p>
          @for (sk of cliSkillRows(); track sk.id) {
            <div class="s-toggle-row">
              <div class="s-toggle-copy">
                <span class="s-toggle-name">{{ sk.label }}</span>
                <span
                  class="s-hint s-mono"
                  [attr.data-testid]="'cli-skill-command-' + sk.id"
                  >{{ sk.command }}</span
                >
              </div>
              <button
                type="button"
                class="btn-tonal s-btn-sm"
                [attr.data-testid]="'copy-cli-skill-' + sk.id"
                (click)="copyCliText(sk.command, sk.label + ' skill command')"
              >
                Copy
              </button>
            </div>
          }
        </div>

        <div class="s-card" id="cli-run" data-testid="card-cli-run">
          <h3 class="s-card-title">Where each agent CLI runs</h3>
          <p class="s-card-desc">
            Choose where each agent CLI executes on dispatch.
          </p>
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
                [disabled]="!canWriteBootstrapSettings()"
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
          <g-sandbox-commitment-note [commitment]="sandboxCommitment()" />

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

      @case ("registry") {
        <div class="s-card" id="registry-status" data-testid="card-registry">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Grain Hub connection</h3>
              <p class="s-card-desc">
                Advertise repository scopes and health heartbeats to a central
                Grain Hub directory.
              </p>
            </div>
            <div class="s-inline">
              <span
                [class]="'status-pill ' + registryTone()"
                data-testid="registry-status-pill"
                [attr.data-state]="registryState()"
              >
                {{ registryStateLabel() }}
              </span>
              <button
                type="button"
                class="btn-tonal s-btn-sm"
                data-testid="registry-heartbeat-btn"
                [disabled]="
                  !access.can('POST /api/registry/heartbeat') ||
                  !registryEnabled() ||
                  heartbeatSending()
                "
                (click)="sendHeartbeat()"
              >
                {{ heartbeatSending() ? "Sending…" : "Heartbeat now" }}
              </button>
            </div>
          </div>

          @if (registryEnabled()) {
            <span class="s-meta-mono" data-testid="registry-endpoint">
              {{ hubField("url") }} · {{ hubField("entry") }}
            </span>
          }
          <p class="s-hint s-num" data-testid="registry-summary">
            {{ registrySummary() }}
          </p>
          @if (heartbeatResult(); as hr) {
            <div
              class="s-banner"
              [class.ok]="hr.ok"
              [class.error]="!hr.ok"
              role="status"
              data-testid="registry-heartbeat-result"
            >
              <span>{{ hr.text }}</span>
            </div>
          }
          @if (registrySnapshot()?.lastError; as lastErr) {
            <div
              class="s-banner error"
              role="alert"
              data-testid="registry-last-error"
            >
              <span>{{ lastErr }}</span>
            </div>
          }
          @if (scopeConflictText()) {
            <div
              class="s-banner warn"
              role="alert"
              data-testid="registry-scope-conflicts"
            >
              <span
                >Scope conflict reported by the hub:
                {{ scopeConflictText() }}</span
              >
            </div>
          }

          <div class="s-subsection" data-testid="registry-advertised">
            <div class="s-subhead">
              <h4 class="s-subhead-title">Advertised scopes</h4>
            </div>
            @if (advertisedScopes().length === 0) {
              <span class="s-hint" data-testid="registry-advertised-empty"
                >No repositories advertised yet. Configured repositories are
                reported automatically from Settings → Repositories.</span
              >
            } @else {
              <div class="s-table-wrap">
                <table class="s-table" aria-label="Advertised scopes">
                  <thead>
                    <tr>
                      <th>Scope</th>
                      <th>Role</th>
                    </tr>
                  </thead>
                  <tbody>
                    @for (
                      sc of advertisedScopes();
                      track sc.repo + ":" + (sc.pathPrefix || "")
                    ) {
                      <tr data-testid="registry-advertised-row">
                        <td>
                          <div class="s-cell-stack">
                            <span class="s-cell-title s-mono">{{
                              sc.repo
                            }}</span>
                            <span class="s-meta-mono">{{
                              sc.pathPrefix
                                ? sc.pathPrefix + "/"
                                : "/ (whole repo)"
                            }}</span>
                          </div>
                        </td>
                        <td>
                          <div class="s-inline">
                            <span
                              class="status-pill"
                              [class.healthy]="sc.role === 'primary'"
                              [class.neutral]="sc.role !== 'primary'"
                              >{{ sc.role }}</span
                            >
                            <span
                              class="status-pill quiet"
                              data-testid="registry-origin-chip"
                              [attr.data-origin]="sc.origin || 'repo_config'"
                              >{{ originLabel(sc.origin) }}</span
                            >
                          </div>
                        </td>
                      </tr>
                    }
                  </tbody>
                </table>
              </div>
            }
          </div>
        </div>

        <div
          class="s-card"
          id="registry-config"
          data-testid="card-registry-config"
        >
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Catalog &amp; authentication</h3>
              <p class="s-card-desc">
                Where this grain registers, how its heartbeats authenticate, and
                how it is described in the hub directory.
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
                data-testid="input-hub-url"
                placeholder="https://hub.grain.internal"
                [value]="hubField('url')"
                [disabled]="!canWriteSettings()"
                (input)="setHubField('url', $any($event.target).value)"
              />
              <span class="s-hint"
                >Leave empty together with Entry name to disable
                self-registration.</span
              >
            </div>
            <div class="s-field">
              <label class="s-label" for="reg-entry">Entry name</label>
              <input
                id="reg-entry"
                class="s-input s-mono"
                type="text"
                data-testid="input-hub-entry"
                placeholder="dco-core"
                [value]="hubField('entry')"
                [disabled]="!canWriteSettings()"
                (input)="setHubField('entry', $any($event.target).value)"
              />
              <span class="s-hint"
                >Unique 3–32 character lowercase DNS label in the hub
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
            </div>
            <div class="s-field">
              <label class="s-label" for="reg-aud">OIDC audience</label>
              <input
                id="reg-aud"
                class="s-input s-mono"
                type="text"
                data-testid="input-hub-oidc-audience"
                [placeholder]="hubField('url') || 'the Hub URL'"
                [value]="hubField('oidcAudience')"
                [disabled]="
                  !canWriteSettings() || hubField('authMode') !== 'oidc'
                "
                (input)="setHubField('oidcAudience', $any($event.target).value)"
              />
              <span class="s-hint"
                >Used with Workload OIDC; blank uses the Hub URL.</span
              >
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
                >Auto-detect follows the active sandbox backend.</span
              >
            </div>
            <div class="s-field">
              <label class="s-label" for="reg-disp">Catalog display name</label>
              <input
                id="reg-disp"
                class="s-input"
                type="text"
                data-testid="input-hub-display-name"
                [value]="hubField('displayName')"
                [disabled]="!canWriteSettings()"
                (input)="setHubField('displayName', $any($event.target).value)"
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="reg-purpose">Purpose</label>
              <input
                id="reg-purpose"
                class="s-input"
                type="text"
                data-testid="input-hub-purpose"
                placeholder="What repositories or workflows this grain handles"
                [value]="hubField('purpose')"
                [disabled]="!canWriteSettings()"
                (input)="setHubField('purpose', $any($event.target).value)"
              />
            </div>
          </div>
          <div class="s-toggle-row">
            <div class="s-toggle-copy">
              <span class="s-toggle-name">Listed in hub directory</span>
              <span class="s-hint"
                >When off, this grain stays addressable by name and route
                queries for its owners but is left out of default directory
                listings.</span
              >
            </div>
            <button
              type="button"
              class="g-switch"
              role="switch"
              data-testid="switch-hub-listed"
              aria-label="Listed in hub directory"
              [attr.aria-checked]="hubRaw('listed') === true"
              [disabled]="!canWriteSettings()"
              (click)="setHubValue('listed', hubRaw('listed') !== true)"
            ></button>
          </div>

          <div class="s-subsection" data-testid="registry-token">
            <div class="s-subhead">
              <h4 class="s-subhead-title">Registration credential</h4>
              <span
                class="status-pill"
                [class.healthy]="hubTokenSet()"
                [class.neutral]="!hubTokenSet()"
                data-testid="registry-token-state"
                >{{ hubTokenSet() ? "set" : "not set" }}</span
              >
            </div>
            <span class="s-hint"
              >Minted by <code>grain hub register</code> (or rotated in the hub)
              and stored encrypted in this daemon's secret store as
              registry-registration-token/value — never exported to
              grain/config, so it stays editable while settings are managed
              there. Write-only: it is never shown or read back.</span
            >
            <form
              class="s-inline"
              autocomplete="off"
              (submit)="$event.preventDefault(); saveHubToken()"
            >
              <input
                class="s-input s-input-inline s-mono"
                type="password"
                autocomplete="new-password"
                data-lpignore="true"
                data-1p-ignore="true"
                aria-label="Registration token (grc_…)"
                data-testid="input-hub-token"
                [placeholder]="
                  hubTokenSet() ? 'Replace the stored grc_… token' : 'grc_…'
                "
                [value]="hubToken()"
                [disabled]="!canWriteSecrets()"
                (input)="hubToken.set($any($event.target).value)"
              />
              <button
                type="submit"
                class="btn-tonal s-btn-sm"
                data-testid="save-hub-token"
                [disabled]="!canWriteSecrets() || !hubToken().trim()"
              >
                Set token
              </button>
              <button
                type="button"
                class="btn-text s-btn-sm"
                data-testid="clear-hub-token"
                [disabled]="!hubTokenSet() || !canClearSecrets()"
                (click)="confirmClearHubToken()"
              >
                Clear
              </button>
            </form>
            @if (!secretsEnabled()) {
              <span class="s-hint" data-testid="registry-token-no-store"
                >This server has no secret store, so the token cannot be stored
                here; run <code>grain hub register</code> on a daemon that has
                one.</span
              >
            } @else if (!access.can("PUT /api/secrets/{secret}/{key}")) {
              <span class="s-hint">{{
                access.deniedReason("PUT /api/secrets/{secret}/{key}")
              }}</span>
            }
          </div>
        </div>

        <div
          class="s-card"
          id="registry-scopes"
          data-testid="card-registry-scopes"
        >
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Scope overrides</h3>
              <p class="s-card-desc">
                Scopes come automatically from Settings → Repositories and each
                repository's work ownership (on → primary, off → secondary). Add
                a rule only to narrow a repository to a path prefix, exclude it
                from hub routing, or advertise an extra scope.
              </p>
            </div>
          </div>
          @if (hubRules().length > 0) {
            <div class="s-table-wrap">
              <table class="s-table" aria-label="Scope override rules">
                <thead>
                  <tr>
                    <th>Scope</th>
                    <th>Rule</th>
                    <th class="s-actions-cell">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  @for (r of hubRules(); track $index) {
                    <tr data-testid="registry-scope-rule">
                      <td>
                        <div class="s-cell-stack">
                          <span class="s-cell-title s-mono">{{ r.repo }}</span>
                          <span class="s-meta-mono">{{
                            r.pathPrefix ? r.pathPrefix + "/" : "whole repo"
                          }}</span>
                        </div>
                      </td>
                      <td>
                        <span
                          class="status-pill"
                          [class.healthy]="!r.exclude && r.role !== 'secondary'"
                          [class.neutral]="!r.exclude && r.role === 'secondary'"
                          [class.quiet]="r.exclude"
                          >{{
                            r.exclude ? "excluded" : r.role || "primary"
                          }}</span
                        >
                      </td>
                      <td class="s-actions-cell">
                        <div class="s-row-actions">
                          <button
                            type="button"
                            class="btn-text s-btn-sm"
                            [attr.aria-label]="
                              'Remove scope rule for ' + r.repo
                            "
                            [disabled]="!canWriteSettings()"
                            (click)="removeHubRule($index)"
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
          } @else {
            <span class="s-hint" data-testid="registry-scope-rules-empty"
              >No overrides: every configured repository is advertised with its
              own work ownership.</span
            >
          }
          <div class="s-subsection">
            <div class="s-subhead">
              <h4 class="s-subhead-title">Add rule</h4>
            </div>
            <div class="s-grid">
              <div class="s-field">
                <label class="s-label" for="reg-rule-repo"
                  >Repository (owner/name)</label
                >
                <input
                  id="reg-rule-repo"
                  class="s-input s-mono"
                  type="text"
                  data-testid="input-scope-rule-repo"
                  placeholder="acme/monorepo"
                  [value]="newRuleRepo()"
                  [disabled]="!canWriteSettings()"
                  (input)="newRuleRepo.set($any($event.target).value)"
                />
              </div>
              <div class="s-field">
                <label class="s-label" for="reg-rule-prefix"
                  >Path prefix (optional)</label
                >
                <input
                  id="reg-rule-prefix"
                  class="s-input s-mono"
                  type="text"
                  data-testid="input-scope-rule-prefix"
                  placeholder="services/billing"
                  [value]="newRulePrefix()"
                  [disabled]="!canWriteSettings()"
                  (input)="newRulePrefix.set($any($event.target).value)"
                />
              </div>
              <div class="s-field">
                <span class="s-label">Rule</span>
                <g-pill-select
                  testid="select-scope-rule-mode"
                  ariaLabel="Scope rule"
                  align="left"
                  [options]="scopeRuleOptions"
                  [value]="newRuleMode()"
                  [disabled]="!canWriteSettings()"
                  (valueChange)="newRuleMode.set($event)"
                />
              </div>
            </div>
            @if (newRuleError()) {
              <span class="s-field-error" data-testid="scope-rule-error">{{
                newRuleError()
              }}</span>
            }
            <div class="s-row-actions">
              <button
                type="button"
                class="btn-tonal s-btn-sm"
                data-testid="add-scope-rule"
                [disabled]="!canWriteSettings() || !newRuleRepo().trim()"
                (click)="addHubRule()"
              >
                Add rule
              </button>
            </div>
          </div>
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

          @if (access.rawConfig()?.installNotice; as notice) {
            <div
              class="s-banner error"
              role="alert"
              data-testid="install-notice"
            >
              <span
                ><strong>Unmigrated host-mode install.</strong>
                {{ notice.message.charAt(0).toUpperCase()
                }}{{ notice.message.slice(1) }}.
                @if (notice.command) {
                  Run
                  <code data-testid="install-notice-command">{{
                    notice.command
                  }}</code
                  >.
                }
                @if (notice.affectedClis?.length) {
                  CLIs that lost their run location:
                  {{ notice.affectedClis!.join("; ") }}.
                }
                @if (notice.docsUrl) {
                  <a
                    [href]="notice.docsUrl"
                    target="_blank"
                    rel="noopener noreferrer"
                    >Migration docs</a
                  >
                }
              </span>
            </div>
          }

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
              <span class="s-banner-text"
                >Upgrade scheduled (waiting for running tasks):
                {{ scheduledUpgradeBranch() }}
                @if (scheduledUpgradeCountError()) {
                  <br /><span data-testid="scheduled-upgrade-count-error">{{
                    scheduledUpgradeCountError()
                  }}</span>
                }
              </span>
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
                  !access.can('DELETE /api/upgrade/current')
                "
                (click)="cancelUpgrade()"
              >
                Cancel upgrade
              </button>
            </div>
          }

          @if (failedUpgradeDetail()) {
            <div
              class="s-banner error"
              role="alert"
              data-testid="failed-upgrade-banner"
              style="overflow-wrap: anywhere"
            >
              <span
                ><strong
                  >Last upgrade{{
                    failedUpgradeBranch() ? " to " + failedUpgradeBranch() : ""
                  }}
                  failed:</strong
                >
                {{ failedUpgradeDetail() }}</span
              >
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

            @if (selectedTargetRefusal(); as refusal) {
              <div
                class="s-banner error"
                role="alert"
                data-testid="upgrade-target-refusal"
                style="overflow-wrap: anywhere"
              >
                <span>{{ refusal }}</span>
              </div>
            }

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
                  !selectedUpgradeBranch() ||
                  Boolean(selectedTargetRefusal()) ||
                  !access.can('POST /api/upgrade')
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

  // A save of the hub settings (or of its token) changes what the daemon
  // registers as, so the status card re-reads GET /api/registry rather
  // than keep the snapshot from before the save.
  private readonly hubReload = effect(() => {
    void this.state.settings()["hub"];
    this.hubTokenSet();
    if (this.section() === "registry") {
      untracked(() => void this.loadRegistry());
    }
  });

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
    { value: "Asia/Tokyo", label: "Asia/Tokyo (JST)" },
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

  protected readonly broadcastMessage = signal("");
  protected readonly broadcastRunningOnly = signal(false);
  protected readonly cliRunLocations = signal<Record<string, string>>({});

  protected readonly packageCacheLive = signal(false);
  protected readonly actionsCacheLive = signal(false);
  protected readonly packageCacheSummary = signal("Shared package proxy cache");
  protected readonly actionsCacheSummary = signal(
    "Workflow step & dependency cache",
  );

  protected readonly registrySnapshot = signal<HubSnapshot | null>(null);
  protected readonly heartbeatSending = signal(false);
  protected readonly heartbeatResult = signal<{
    ok: boolean;
    text: string;
  } | null>(null);
  protected readonly hubToken = signal("");
  // GET /api/secrets' enabled: a daemon with no secret store (grain demo,
  // a UI with no secrets directory) cannot hold the token at all.
  protected readonly secretsEnabled = signal(true);
  protected readonly newRuleRepo = signal("");
  protected readonly newRulePrefix = signal("");
  protected readonly newRuleMode = signal("primary");
  protected readonly newRuleError = signal("");

  protected readonly scopeRuleOptions: readonly PillOption[] = [
    { value: "primary", label: "Primary" },
    { value: "secondary", label: "Secondary" },
    { value: "exclude", label: "Exclude repo" },
  ];

  // The stored hub settings, or what GET /api/registry reports when the
  // settings have not loaded them: the snapshot's own enabled flag is the
  // daemon's, so a URL and entry staged but not saved do not count.
  protected readonly registryEnabled = computed(() => {
    const snap = this.registrySnapshot();
    if (snap) return Boolean(snap.enabled);
    const h = this.state.settings()["hub"] as Record<string, unknown> | null;
    return Boolean(h?.["url"] && h?.["entry"]);
  });

  protected readonly registryState = computed(() =>
    this.registryEnabled()
      ? String(this.registrySnapshot()?.state || "connecting")
      : "disabled",
  );

  protected readonly registryStateLabel = computed(
    () =>
      ({
        disabled: "Not configured",
        connecting: "Connecting",
        connected: "Connected",
        degraded: "Degraded",
        error: "Heartbeat error",
      })[this.registryState()] ?? this.registryState(),
  );

  protected readonly registryTone = computed(
    () =>
      ({
        connected: "healthy",
        degraded: "setup",
        error: "error",
      })[this.registryState()] ?? "neutral",
  );

  protected readonly registrySummary = computed(() => {
    const snap = this.registrySnapshot();
    if (!this.registryEnabled()) {
      return "Set a Hub URL and entry name below (or run grain hub register) so this daemon registers itself and heartbeats its version, health, task counts and repository scopes.";
    }
    if (!snap?.lastHeartbeatAt) return "Waiting for the first heartbeat…";
    const every = snap.heartbeatIntervalSeconds
      ? ` (every ${snap.heartbeatIntervalSeconds}s)`
      : "";
    const health = `health ${snap.health || "ok"}${snap.healthDetail ? ` — ${snap.healthDetail}` : ""}`;
    return `Last heartbeat ${formatStamp(snap.lastHeartbeatAt, this.state.config()?.timeZone)}${every} · ${health} · ${snap.activeTasks ?? 0} active, ${snap.awaitingReplyTasks ?? 0} awaiting reply`;
  });

  protected readonly advertisedScopes = computed(() => {
    const a = this.registrySnapshot()?.advertisedScopes;
    return Array.isArray(a) ? a : [];
  });

  protected readonly scopeConflictText = computed(() => {
    const c = this.registrySnapshot()?.scopeConflicts;
    if (!Array.isArray(c) || c.length === 0) return "";
    return c
      .map(
        (x) =>
          `${x.repo}${x.path_prefix ? ` (${x.path_prefix})` : ""} already claimed as primary by ${x.conflicting_entry || "another entry"}`,
      )
      .join("; ");
  });

  protected readonly hubTokenSet = computed(() => {
    const s = this.state.settings();
    return Boolean(s["hubTokenSet"] ?? s["registryTokenSet"]);
  });

  protected readonly hubRules = computed<HubScopeRule[]>(() => {
    const r = this.hubRaw("scopes");
    return Array.isArray(r) ? (r as HubScopeRule[]) : [];
  });

  // Platform is the hub's host kind (model.HubConfig.HostKind). Blank is
  // auto-detect, which GET /api/registry resolves as effectiveHostKind
  // while nothing is chosen.
  protected readonly hubHostKindOptions = computed<readonly PillOption[]>(
    () => {
      const eff = this.registrySnapshot()?.effectiveHostKind;
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
    },
  );

  protected readonly sandboxCommitment = computed(
    () =>
      (this.state.settings()["sandboxCommitment"] as
        SandboxCommitment | undefined) ?? null,
  );

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
  protected readonly failedUpgradeBranch = signal("");
  protected readonly failedUpgradeDetail = signal("");
  protected readonly scheduledUpgradeBranch = signal("");
  protected readonly scheduledUpgradeCountError = signal("");
  protected readonly upgradeTargets = signal<UpgradeTargetRow[]>([]);
  protected readonly selectedUpgradeBranch = signal("latest");
  protected readonly selectedTargetRefusal = computed(() => {
    const cur = this.selectedUpgradeBranch().trim();
    if (!cur) return "";
    const row = this.upgradeTargets().find(
      (r) => (r.name || r.branch || "") === cur,
    );
    if (!row || row.allowed !== false) return "";
    return row.reason || "Not allowed on this deployment";
  });
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
      .map((row): PillOption | null => {
        const val = row.name || row.branch || "";
        if (!val) return null;
        const extra = row.version || row.ref;
        const disabled = row.allowed === false;
        let label = extra && extra !== val ? `${val} (${extra})` : val;
        if (typeof row.schema === "number" && row.schema > 0) {
          label += ` · schema ${row.schema}`;
        }
        return {
          value: val,
          label,
          disabled,
          hint: disabled
            ? row.reason || "Not allowed on this deployment"
            : undefined,
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

  protected canWriteBootstrapSettings(): boolean {
    return (
      this.access.can("POST /api/settings") && !this.state.externalConfigRepo()
    );
  }

  protected canToggleSettingsLock(): boolean {
    return (
      this.access.can("POST /api/settings") &&
      this.state.hasInternalConfigRepo()
    );
  }

  protected settingsLockHint(): string {
    if (this.state.externalConfigRepo()) {
      return `Managed in external config repository (${this.state.externalConfigRepo()}).`;
    }
    if (!this.state.hasInternalConfigRepo()) {
      return "Unavailable without a config repository: settings are always edited directly here.";
    }
    return "Makes config-managed sections read-only outside StateRepo pull requests.";
  }

  protected deploymentIdPlaceholder(): string {
    return String(
      this.state.settings()["deploymentIdDefault"] ??
        this.state.settings()["sampleDeploymentId"] ??
        "e.g. 53549e42",
    );
  }

  protected releaseStamp(): string {
    const v = this.state.config()?.version as VersionInfo | string | undefined;
    if (!v || typeof v === "string") return "";
    if (v.unversioned) return "";
    return v.release || "";
  }

  protected openCustomizeNav(ev: Event): void {
    this.navConfig.openDialog(ev.currentTarget as HTMLElement | null);
  }

  protected setTimeZone(tz: string): void {
    this.theme.setTimeZone(tz);
    this.access.tickClock();
  }

  protected serverOrigin(): string {
    return this.doc.defaultView?.location?.origin || "http://localhost:8080";
  }

  protected cliInstallCmd(): string {
    return `curl -fsSL ${this.serverOrigin()}/cli/install.sh | sh`;
  }

  protected cliLoginCmd(): string {
    const origin = this.serverOrigin();
    return `grain login ${origin} --name ${contextSlug(origin)} --issue-token --scope-set grain/external-dev --expires 30d`;
  }

  protected cliSkillRows(): readonly {
    id: string;
    label: string;
    command: string;
  }[] {
    return CLI_SKILL_AGENTS.map((a) => ({
      id: a.id,
      label: a.label,
      command: `grain skill install ${a.flag}`,
    }));
  }

  protected async copyCliText(text: string, label: string): Promise<void> {
    try {
      await this.doc.defaultView?.navigator.clipboard?.writeText(text);
      this.snackbar.show(`Copied ${label.toLowerCase()}`);
    } catch {
      this.snackbar.show(`Could not copy ${label.toLowerCase()}`);
    }
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
    } else if (sec === "registry") {
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

  protected hubField(key: string): string {
    const v = this.hubRaw(key);
    return v === undefined || v === null ? "" : String(v);
  }

  protected hubRaw(key: string): unknown {
    const h = this.state.val<Record<string, unknown> | null>("hub", {}) ?? {};
    return h[key];
  }

  protected setHubField(key: string, val: string): void {
    this.setHubValue(key, val);
  }

  protected setHubValue(key: string, val: unknown): void {
    const cur = this.state.val<Record<string, unknown> | null>("hub", {}) ?? {};
    this.state.setField("hub", { ...cur, [key]: val });
  }

  protected originLabel(origin: string | undefined): string {
    switch (origin) {
      case "override":
        return "override";
      case "added":
        return "added in settings";
      default:
        return "from repo config";
    }
  }

  // addHubRule stages one Scope overrides rule into the hub setting; it
  // saves with the section's Save bar like every other hub field. The
  // path prefix is stored without leading or trailing slashes, the form
  // the daemon matches it in.
  protected addHubRule(): void {
    const repo = this.newRuleRepo().trim();
    if (!repo) return;
    if (!/^[^/\s]+\/[^/\s]+$/.test(repo)) {
      this.newRuleError.set(`Repository must be owner/name, not "${repo}".`);
      return;
    }
    const prefix = this.newRulePrefix()
      .trim()
      .replace(/^\/+|\/+$/g, "");
    const rules = this.hubRules();
    if (rules.some((r) => r.repo === repo && (r.pathPrefix || "") === prefix)) {
      this.newRuleError.set(
        `There is already a rule for ${repo}${prefix ? ` (${prefix})` : ""}.`,
      );
      return;
    }
    const mode = this.newRuleMode();
    const rule: HubScopeRule =
      mode === "exclude"
        ? { repo, pathPrefix: prefix, exclude: true }
        : { repo, pathPrefix: prefix, role: mode };
    this.setHubValue("scopes", [...rules, rule]);
    this.newRuleRepo.set("");
    this.newRulePrefix.set("");
    this.newRuleError.set("");
  }

  protected removeHubRule(idx: number): void {
    this.setHubValue(
      "scopes",
      this.hubRules().filter((_, i) => i !== idx),
    );
  }

  private async loadRegistry(): Promise<void> {
    if (this.access.can("GET /api/secrets")) {
      try {
        const sec = await this.api.get<unknown[] | { enabled?: boolean }>(
          "GET /api/secrets",
        );
        this.secretsEnabled.set(Array.isArray(sec) || sec?.enabled !== false);
      } catch {
        // Non-fatal: the PUT says so if the store is missing.
      }
    }
    if (!this.access.can("GET /api/registry")) return;
    try {
      const r = await this.api.get<HubSnapshot>("GET /api/registry");
      if (r && typeof r === "object") this.registrySnapshot.set(r);
    } catch {
      // Non-fatal: the card reads the stored settings until it answers.
    }
  }

  // sendHeartbeat says what came of it in the card, not only in a
  // snackbar: the reply is the new snapshot, whose lastError is the hub's
  // refusal when it refused.
  protected async sendHeartbeat(): Promise<void> {
    this.heartbeatSending.set(true);
    this.heartbeatResult.set(null);
    try {
      const r = await this.api.post<HubSnapshot>(
        "POST /api/registry/heartbeat",
        undefined,
        {},
      );
      if (r && typeof r === "object") this.registrySnapshot.set(r);
      const at = r?.lastHeartbeatAt
        ? formatStamp(r.lastHeartbeatAt, this.state.config()?.timeZone)
        : "";
      if (r?.lastError) {
        this.heartbeatResult.set({
          ok: false,
          text: `Heartbeat not delivered: ${r.lastError}`,
        });
      } else {
        this.heartbeatResult.set({
          ok: true,
          text: `Heartbeat delivered${at ? ` at ${at}` : ""} — the hub reports this entry ${r?.state || "connected"}.`,
        });
      }
    } catch (err) {
      this.heartbeatResult.set({
        ok: false,
        text: `Heartbeat failed: ${errorText(err)}`,
      });
      await this.loadRegistry();
    } finally {
      this.heartbeatSending.set(false);
    }
  }

  // The registration token lives in the secret store, not grain/config,
  // so a config-repo lock on the settings leaves it editable.
  protected canWriteSecrets(): boolean {
    return (
      this.secretsEnabled() &&
      this.access.can("PUT /api/secrets/{secret}/{key}")
    );
  }

  protected canClearSecrets(): boolean {
    return (
      this.secretsEnabled() &&
      this.access.can("DELETE /api/secrets/{secret}/{key}")
    );
  }

  protected async saveHubToken(): Promise<void> {
    const value = this.hubToken().trim();
    if (!value || !this.canWriteSecrets()) return;
    try {
      await this.api.put(
        "PUT /api/secrets/{secret}/{key}",
        { secret: HUB_TOKEN_SECRET, key: "value" },
        { value },
      );
      this.hubToken.set("");
      await this.state.loadCore(true);
      await this.loadRegistry();
      this.snackbar.show("Registration token stored");
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected confirmClearHubToken(): void {
    this.confirmRequest.set({
      title: "Clear the registration token?",
      message:
        "The stored grc_… token cannot be read back, so putting it back means pasting it again. Heartbeats stop authenticating until it is replaced.",
      accept: "Clear token",
      destructive: true,
      run: () => this.clearHubToken(),
    });
  }

  private async clearHubToken(): Promise<void> {
    try {
      await this.api.delete("DELETE /api/secrets/{secret}/{key}", {
        secret: HUB_TOKEN_SECRET,
        key: "value",
      });
      await this.state.loadCore(true);
      await this.loadRegistry();
      this.snackbar.show("Registration token cleared");
    } catch (err) {
      this.state.recordError(err);
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
      | { branch?: string; requestedBy?: string; countError?: string }
      | undefined
      | null;
    this.scheduledUpgradeBranch.set(
      scheduled ? String(scheduled.branch || "latest") : "",
    );
    // Set when the daemon could not count the runs the schedule waits on:
    // the queue is held and the upgrade will not start until it can.
    this.scheduledUpgradeCountError.set(
      scheduled && typeof scheduled.countError === "string"
        ? scheduled.countError
        : "",
    );

    const phase = typeof u["phase"] === "string" ? u["phase"] : "";
    const cancellable = Boolean(u["cancellable"]);
    this.upgradeCancellable.set(cancellable);
    // "ok" is the phase pkg/upgrade leaves a finished upgrade in (and
    // keeps across the restart), so it is no upgrade "in progress".
    const terminalPhases = new Set([
      "",
      "ok",
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
      this.failedUpgradeBranch.set("");
      this.failedUpgradeDetail.set("");
    } else {
      this.activeUpgradeState.set("");
      if (phase.toLowerCase() === "failed") {
        this.failedUpgradeBranch.set(
          typeof u["branch"] === "string" ? u["branch"] : "",
        );
        this.failedUpgradeDetail.set(
          typeof u["detail"] === "string" && u["detail"].trim()
            ? u["detail"].trim()
            : "Upgrade failed",
        );
      } else {
        this.failedUpgradeBranch.set("");
        this.failedUpgradeDetail.set("");
      }
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
        const defRow = list.find((r) => (r.name || r.branch || "") === def);
        if (!defRow || defRow.allowed !== false) {
          void this.loadReleaseNotes(def);
        } else {
          this.releaseNotesText.set("");
        }
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
    if (!branch || this.selectedTargetRefusal()) return;
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
      // The upgrade in progress is DELETE /api/upgrade/current; the
      // scheduled route only forgets one still waiting for tasks to finish,
      // so calling it here left the running upgrade going while this card
      // said it had been cancelled (docs/test-runs/2026-11-13-upgrade-cancel.md
      // F2).
      await this.api.delete(
        "DELETE /api/upgrade/current",
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
