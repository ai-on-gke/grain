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
  OnChanges,
  OnInit,
  ViewEncapsulation,
  inject,
  input,
  output,
  signal,
} from "@angular/core";
import { GrainApi } from "../api/grain-api.service";
import { AccessService } from "../services/access.service";
import { KeyboardService } from "../services/keyboard.service";
import { SnackbarService } from "../services/snackbar.service";
import {
  type ThemeMode,
  ThemeService,
  readStorage,
  writeStorage,
} from "../services/theme.service";
import {
  type PillOption,
  PillSelectComponent,
} from "../shared/pill-select.component";
import type { SettingsSectionId } from "./settings-sections";
import { SettingsStateService, errText } from "./settings-state.service";

const TZ_STORAGE_KEY = "grain.timeZone";

interface UpgradeTargetRow {
  branch: string;
  version?: string;
  commit?: string;
  channel?: string;
}

@Component({
  selector: "g-settings-instance",
  imports: [PillSelectComponent],
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
                class="s-input tabular"
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
          <h3 class="s-card-title">Operator broadcast banner</h3>
          <p class="s-card-desc">
            Publish a live notice across connected Grain browsers (leave empty
            to clear).
          </p>
          <div class="s-inline">
            <input
              class="s-input"
              style="flex: 1; min-width: 240px"
              type="text"
              aria-label="Broadcast banner message"
              data-testid="input-broadcast-message"
              placeholder="Maintenance window at 18:00 UTC…"
              [value]="broadcastMessage()"
              [disabled]="!access.can('POST /api/deployment/broadcast')"
              (input)="broadcastMessage.set($any($event.target).value)"
            />
            <button
              type="button"
              class="btn-tonal"
              data-testid="send-broadcast-btn"
              [disabled]="!access.can('POST /api/deployment/broadcast')"
              (click)="sendBroadcast()"
            >
              Publish banner
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
            <button
              type="button"
              class="btn-tonal"
              data-testid="pref-customize-nav-btn"
              (click)="customizeNav.emit()"
            >
              Customize navigation…
            </button>
          </div>
        </div>
      }

      @case ("cli") {
        <div class="s-card" data-testid="card-cli">
          <h3 class="s-card-title">Workstation grain CLI setup</h3>
          <p class="s-card-desc">
            Install the CLI and authenticate against this Grain server ({{
              serverOrigin()
            }}).
          </p>
          <pre class="s-code" data-testid="cli-install-snippet">{{
            cliSnippet()
          }}</pre>

          <div class="s-row">
            <div class="s-toggle-copy">
              <span class="s-toggle-name">Default CLI task execution</span>
              <span class="s-hint"
                >Where ad-hoc grain run commands execute by default.</span
              >
            </div>
            <div class="s-inline">
              <g-pill-select
                testid="select-cli-run-location"
                ariaLabel="Default CLI task execution"
                align="left"
                [options]="cliLocationOptions"
                [value]="cliRunLocation()"
                [disabled]="!access.can('PUT /api/settings/cli-run')"
                (valueChange)="saveCliRunLocation($event)"
              />
            </div>
          </div>
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
                class="s-input tabular"
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
                class="s-input tabular"
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
                class="s-input tabular"
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
                class="s-input tabular"
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
              <label class="s-label" for="e-mergers">Max merge agents</label>
              <input
                id="e-mergers"
                class="s-input tabular"
                type="number"
                min="0"
                [value]="state.val('maxMergers', 2)"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField(
                    'maxMergers',
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
                class="s-input tabular"
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
                class="status-pill neutral tabular"
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
                class="s-input tabular"
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
                class="s-input tabular"
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
              class="status-pill healthy tabular"
              data-testid="current-daemon-version"
            >
              {{ currentVersion() }}
            </span>
          </div>

          @if (activeUpgradeState()) {
            <div class="s-banner warn" data-testid="active-upgrade-banner">
              <span>Upgrade in progress: {{ activeUpgradeState() }}</span>
              <button
                type="button"
                class="s-btn-danger s-btn-sm"
                data-testid="cancel-upgrade-btn"
                [disabled]="!access.can('DELETE /api/upgrade/current')"
                (click)="cancelUpgrade()"
              >
                Cancel upgrade
              </button>
            </div>
          }

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
              <span class="s-label">Auto-upgrade check mode</span>
              <g-pill-select
                testid="select-upgrade-check-mode"
                ariaLabel="Auto-upgrade check mode"
                align="left"
                [options]="upgradeCheckOptions"
                [value]="state.val('upgradeCheckMode', 'notify')"
                [disabled]="!access.can('POST /api/settings')"
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
                >Waits for active agent turns to finish cleanly before switching
                binaries.</span
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
              (click)="startUpgrade()"
            >
              Start upgrade
            </button>
            @if (dbBackendLabel()) {
              <span class="s-hint tabular"
                >Database: {{ dbBackendLabel() }}</span
              >
            }
          </div>

          @if (releaseNotesText()) {
            <pre class="s-code" data-testid="upgrade-release-notes">{{
              releaseNotesText()
            }}</pre>
          }
        </div>
      }
    }
  `,
})
export class SettingsInstanceComponent implements OnInit, OnChanges {
  protected readonly api = inject(GrainApi);
  protected readonly access = inject(AccessService);
  protected readonly state = inject(SettingsStateService);
  protected readonly theme = inject(ThemeService);
  protected readonly keyboard = inject(KeyboardService);
  private readonly snackbar = inject(SnackbarService);
  private readonly doc = inject(DOCUMENT);

  readonly section = input.required<SettingsSectionId>();
  readonly customizeNav = output<void>();

  protected readonly Number = Number;
  protected readonly Boolean = Boolean;

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

  protected readonly cliLocationOptions: readonly PillOption[] = [
    { value: "sandbox", label: "Controller sandbox" },
    { value: "hostd", label: "Paired workstation hostd" },
    { value: "local", label: "Caller machine" },
  ];

  protected readonly hubAuthOptions: readonly PillOption[] = [
    { value: "token", label: "Shared bearer token" },
    { value: "oidc", label: "Workload OIDC" },
  ];

  protected readonly upgradeCheckOptions: readonly PillOption[] = [
    { value: "off", label: "Off" },
    { value: "notify", label: "Notify in UI" },
    { value: "auto", label: "Automatic" },
  ];

  protected readonly timeZone = signal(readStorage(TZ_STORAGE_KEY) || "local");
  protected readonly broadcastMessage = signal("");
  protected readonly cliRunLocation = signal("sandbox");

  protected readonly packageCacheSummary = signal("Shared package proxy cache");
  protected readonly actionsCacheSummary = signal(
    "Workflow step & dependency cache",
  );

  protected readonly registryStateLabel = signal("Not configured");

  protected readonly currentVersion = signal("dev");
  protected readonly activeUpgradeState = signal("");
  protected readonly upgradeTargets = signal<UpgradeTargetRow[]>([]);
  protected readonly selectedUpgradeBranch = signal("stable");
  protected readonly afterTasksFinish = signal(true);
  protected readonly releaseNotesText = signal("");
  protected readonly dbBackendLabel = signal("");

  protected upgradeTargetOptions(): PillOption[] {
    const t = this.upgradeTargets();
    if (t.length === 0) {
      return [
        { value: "stable", label: "stable" },
        { value: "main", label: "main" },
      ];
    }
    return t.map((row) => ({
      value: row.branch,
      label: row.version ? `${row.branch} (${row.version})` : row.branch,
    }));
  }

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

  protected setTimeZone(tz: string): void {
    this.timeZone.set(tz);
    writeStorage(TZ_STORAGE_KEY, tz === "local" ? null : tz);
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
          .get<Record<string, unknown>>("GET /api/settings/cli-run")
          .then((r) => {
            const def = (r?.["default"] as { location?: string })?.location;
            if (def) this.cliRunLocation.set(def);
          })
          .catch(() => undefined);
      }
    } else if (sec === "caches") {
      if (this.access.can("GET /api/package-cache")) {
        void this.api
          .get<Record<string, unknown>>("GET /api/package-cache")
          .then((p) => {
            if (p?.["bytes"] !== undefined) {
              this.packageCacheSummary.set(`Current size: ${p["bytes"]} bytes`);
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
    } else if (sec === "upgrade") {
      void this.loadUpgrade();
    }
  }

  protected async sendBroadcast(): Promise<void> {
    try {
      await this.api.post("POST /api/deployment/broadcast", undefined, {
        message: this.broadcastMessage().trim(),
      });
      this.snackbar.show(
        this.broadcastMessage().trim()
          ? "Broadcast banner published"
          : "Broadcast banner cleared",
      );
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  protected async saveCliRunLocation(location: string): Promise<void> {
    this.cliRunLocation.set(location);
    try {
      await this.api.put("PUT /api/settings/cli-run", undefined, {
        settings: { default: { location } },
      });
      this.snackbar.show("Updated CLI execution target");
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  protected async flushPackageCache(): Promise<void> {
    try {
      await this.api.post("POST /api/package-cache/flush", undefined, {});
      this.snackbar.show("Proxy cache flushed");
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  protected async flushActionsCache(): Promise<void> {
    try {
      await this.api.post("POST /api/actions-cache/flush", undefined, {});
      this.snackbar.show("Actions cache flushed");
    } catch (err) {
      this.state.error.set(errText(err));
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
      this.state.error.set(errText(err));
    }
  }

  private async loadUpgrade(): Promise<void> {
    const cfgVer = this.state.config()?.version;
    if (typeof cfgVer === "string" && cfgVer) this.currentVersion.set(cfgVer);
    if (this.access.can("GET /api/upgrade")) {
      try {
        const u =
          await this.api.get<Record<string, unknown>>("GET /api/upgrade");
        if (u?.["currentVersion"] || u?.["version"]) {
          this.currentVersion.set(String(u["currentVersion"] ?? u["version"]));
        }
        const active = u?.["current"] as { state?: string } | undefined;
        this.activeUpgradeState.set(
          active?.state ?? String(u?.["inProgress"] ? "running" : ""),
        );
      } catch {
        // Non-fatal
      }
    }
    if (this.access.can("GET /api/upgrade/targets")) {
      try {
        const t = await this.api.get<
          UpgradeTargetRow[] | { targets?: UpgradeTargetRow[] }
        >("GET /api/upgrade/targets");
        const list = Array.isArray(t) ? t : (t?.targets ?? []);
        this.upgradeTargets.set(list);
        if (list[0]?.branch) {
          this.selectedUpgradeBranch.set(list[0].branch);
          void this.loadReleaseNotes(list[0].branch);
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

  private async loadReleaseNotes(branch: string): Promise<void> {
    if (!this.access.can("GET /api/upgrade/release-notes")) return;
    try {
      const r = await this.api.get<
        string | { notes?: string; markdown?: string }
      >(
        "GET /api/upgrade/release-notes",
        undefined,
        `branch=${encodeURIComponent(branch)}`,
      );
      this.releaseNotesText.set(
        typeof r === "string" ? r : (r?.notes ?? r?.markdown ?? ""),
      );
    } catch {
      // Non-fatal
    }
  }

  protected async startUpgrade(): Promise<void> {
    try {
      await this.api.post("POST /api/upgrade", undefined, {
        branch: this.selectedUpgradeBranch(),
        afterTasksFinish: this.afterTasksFinish(),
      });
      this.activeUpgradeState.set("starting");
      this.snackbar.show(`Started upgrade to ${this.selectedUpgradeBranch()}`);
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  protected async cancelUpgrade(): Promise<void> {
    try {
      await this.api.delete(
        "DELETE /api/upgrade/current",
        undefined,
        undefined,
        "reason=operator_cancelled",
      );
      this.activeUpgradeState.set("");
      this.snackbar.show("Cancelled active upgrade");
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }
}
