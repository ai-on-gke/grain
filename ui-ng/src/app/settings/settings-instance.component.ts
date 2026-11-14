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
  inject,
  input,
  signal,
} from "@angular/core";
import { GrainApi } from "../api/grain-api.service";
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
import type { SettingsSectionId } from "./settings-sections";
import { SettingsStateService } from "./settings-state.service";
import { SettingsUpgradeComponent } from "./settings-upgrade.component";
import { UPGRADE_CHECK_HELP, UPGRADE_CHECK_LABELS } from "./upgrade-view";

const TZ_STORAGE_KEY = "grain.timeZone";

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
    SettingsUpgradeComponent,
  ],
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

        <div class="s-card" id="upgrade-check" data-testid="card-upgrade-check">
          <h3 class="s-card-title">After an upgrade</h3>
          <p class="s-card-desc">
            Whether grain files a task to check that the deployment an upgrade
            brought up actually works, and whether the queue waits for it.
          </p>
          <div class="s-field">
            <span class="s-label">Check the upgrade</span>
            <g-pill-select
              testid="select-upgrade-check-mode"
              ariaLabel="Check the upgrade"
              align="left"
              [options]="upgradeCheckOptions"
              [value]="upgradeCheckMode()"
              [disabled]="!canWriteSettings()"
              (valueChange)="state.setField('upgradeCheckMode', $event)"
            />
            <span class="s-hint">{{ upgradeCheckHelp() }}</span>
          </div>
          <div class="s-toggle-row">
            <div class="s-toggle-copy">
              <span class="s-toggle-name"
                >Hold the queue until the check is done</span
              >
              <span class="s-hint"
                >Off (default): the check runs at the head of the queue with
                everything else still going. On: nothing else is picked up until
                it finishes; runs in flight are left alone, and the hold is
                given up after six hours.</span
              >
            </div>
            <button
              type="button"
              class="g-switch"
              role="switch"
              data-testid="switch-upgrade-check-pauses"
              aria-label="Hold the queue until the upgrade check is done"
              [attr.aria-checked]="
                Boolean(state.val('upgradeCheckPausesPipeline', false))
              "
              [disabled]="!canWriteSettings() || upgradeCheckMode() === 'off'"
              (click)="
                state.setField(
                  'upgradeCheckPausesPipeline',
                  !state.val('upgradeCheckPausesPipeline', false)
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
                [disabled]="!access.can('POST /api/package-cache/flush')"
                (click)="flushPackageCache()"
              >
                Flush proxy cache
              </button>
              <button
                type="button"
                class="btn-tonal s-btn-sm"
                data-testid="flush-actions-cache-btn"
                [disabled]="!access.can('POST /api/actions-cache/flush')"
                (click)="flushActionsCache()"
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
                [value]="state.val('packageCacheSizeGb', 20)"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField(
                    'packageCacheSizeGb',
                    Number($any($event.target).value)
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
                [value]="state.val('actionsCacheSizeGb', 20)"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField(
                    'actionsCacheSizeGb',
                    Number($any($event.target).value)
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
                placeholder="30s"
                [value]="state.val('pollInterval', '30s')"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField('pollInterval', $any($event.target).value)
                "
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="e-workers">Max worker agents</label>
              <input
                id="e-workers"
                class="s-input s-num"
                type="number"
                min="1"
                data-testid="input-max-workers"
                [value]="state.val('maxWorkers', 4)"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField(
                    'maxWorkers',
                    Number($any($event.target).value)
                  )
                "
              />
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
                [value]="state.val('maxMergesPerRepo', 1)"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField(
                    'maxMergesPerRepo',
                    Number($any($event.target).value)
                  )
                "
              />
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

      @case ("registry") {
        <div class="s-card" data-testid="card-registry">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Grain Registry &amp; Hub connection</h3>
              <p class="s-card-desc">
                Advertise repository scopes and health heartbeats to a central
                Grain Hub directory.
              </p>
            </div>
            <div class="s-inline">
              <span
                class="status-pill neutral s-mono"
                data-testid="registry-status-pill"
              >
                {{ registryStateLabel() }}
              </span>
              <button
                type="button"
                class="btn-tonal s-btn-sm"
                data-testid="registry-heartbeat-btn"
                [disabled]="!access.can('POST /api/registry/heartbeat')"
                (click)="sendHeartbeat()"
              >
                Heartbeat now
              </button>
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
              <label class="s-label" for="reg-disp">Catalog display name</label>
              <input
                id="reg-disp"
                class="s-input"
                type="text"
                [value]="hubField('displayName')"
                [disabled]="!canWriteSettings()"
                (input)="setHubField('displayName', $any($event.target).value)"
              />
            </div>
          </div>
        </div>
      }

      @case ("upgrade") {
        <g-settings-upgrade />
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
    { value: "host", label: "Host (grain hostd)" },
    { value: "container", label: "Daemon container" },
    { value: "detached", label: "Detached hostd session" },
  ];

  protected readonly hubAuthOptions: readonly PillOption[] = [
    { value: "token", label: "Shared bearer token" },
    { value: "oidc", label: "Workload OIDC" },
  ];

  protected readonly timeZone = signal(readStorage(TZ_STORAGE_KEY) || "local");
  protected readonly broadcastMessage = signal("");
  protected readonly broadcastRunningOnly = signal(false);
  protected readonly cliRunLocations = signal<Record<string, string>>({});

  protected readonly packageCacheSummary = signal("Shared package proxy cache");
  protected readonly actionsCacheSummary = signal(
    "Workflow step & dependency cache",
  );

  protected readonly registryStateLabel = signal("Not configured");

  // The post-upgrade check is set here, on General, as in the classic
  // Settings; Settings -> Upgrade shows it read-only with a link back.
  protected readonly upgradeCheckOptions: readonly PillOption[] = (
    ["off", "approve", "auto"] as const
  ).map((m) => ({ value: m, label: UPGRADE_CHECK_LABELS[m] }));
  protected readonly upgradeCheckMode = computed(() =>
    String(this.state.val("upgradeCheckMode", "auto")),
  );
  protected readonly upgradeCheckHelp = computed(
    () => UPGRADE_CHECK_HELP[this.upgradeCheckMode()] ?? "",
  );

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

  protected cliSnippet(): string {
    const origin = this.serverOrigin();
    return `curl -fsSL ${origin}/api/hostd/install.sh | sh\ngrain login ${origin}`;
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
            available?: boolean;
            cache?: { kind: string; entries?: number; bytes?: number }[];
          }>("GET /api/package-cache")
          .then((p) => {
            if (Array.isArray(p?.cache)) {
              let entries = 0;
              let bytes = 0;
              for (const item of p.cache) {
                entries += Number(item.entries) || 0;
                bytes += Number(item.bytes) || 0;
              }
              this.packageCacheSummary.set(
                `${entries} cached packages · ${formatBytes(bytes)}`,
              );
            } else if (p?.available === false) {
              this.packageCacheSummary.set("Proxy cache not enabled on daemon");
            }
          })
          .catch(() => undefined);
      }
      if (this.access.can("GET /api/actions-cache")) {
        void this.api
          .get<Record<string, unknown>>("GET /api/actions-cache")
          .then((a) => {
            if (a?.["entries"] !== undefined) {
              this.actionsCacheSummary.set(`Cached entries: ${a["entries"]}`);
            }
          })
          .catch(() => undefined);
      }
    } else if (sec === "registry") {
      void this.loadRegistry();
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
          ? `Broadcast delivered to ${n} task(s)`
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
      await this.api.post("POST /api/actions-cache/flush", undefined, {});
      this.snackbar.show("Actions cache flushed");
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected hubField(key: string): string {
    const h =
      (this.state.val<Record<string, unknown>>("hub", {}) as Record<
        string,
        unknown
      >) || {};
    return String(h[key] ?? "");
  }

  protected setHubField(key: string, val: string): void {
    const cur =
      (this.state.val<Record<string, unknown>>("hub", {}) as Record<
        string,
        unknown
      >) || {};
    this.state.setField("hub", { ...cur, [key]: val });
  }

  private async loadRegistry(): Promise<void> {
    if (!this.access.can("GET /api/registry")) return;
    try {
      const r =
        await this.api.get<Record<string, unknown>>("GET /api/registry");
      const st = String(r?.["state"] ?? "disabled");
      this.registryStateLabel.set(st);
    } catch {
      // Non-fatal
    }
  }

  protected async sendHeartbeat(): Promise<void> {
    try {
      const r = await this.api.post<Record<string, unknown>>(
        "POST /api/registry/heartbeat",
        undefined,
        {},
      );
      if (r?.["state"]) this.registryStateLabel.set(String(r["state"]));
      this.snackbar.show("Heartbeat sent");
    } catch (err) {
      this.state.recordError(err);
    }
  }
}

// safeManagementUrl keeps only an absolute http(s) link, as the classic
// ExternallyManagedUpgradeNotice does.
export { safeHttpUrl as safeManagementUrl } from "./upgrade-view";
