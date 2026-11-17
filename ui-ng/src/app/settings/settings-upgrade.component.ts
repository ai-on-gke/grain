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
  DestroyRef,
  type OnInit,
  ViewEncapsulation,
  computed,
  inject,
  signal,
} from "@angular/core";
import { RouterLink } from "@angular/router";
import { GrainApi } from "../api/grain-api.service";
import { type VersionInfo, versionLabel } from "../api/version";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import {
  type UpgradeStatus,
  UpgradeWatchService,
} from "../services/upgrade-watch.service";
import { errorText } from "../sessions/session-helpers";
import { MarkdownDirective } from "../shared/code-highlight";
import {
  ConfirmDialogComponent,
  type ConfirmRequest,
} from "../shared/confirm-dialog.component";
import { SettingsStateService } from "./settings-state.service";
import {
  failedStepError,
  formatElapsed,
  stepElapsedMs,
  upgradeProgress,
} from "./upgrade-progress";

// One entry of GET /api/upgrade/targets (pkg/upgrade/targets.go's
// Target). retracted/discarded mark a version that was pulled from the
// release train; older answers named entries by branch instead.
interface UpgradeTargetRow {
  name?: string;
  branch?: string;
  kind?: string;
  allowed?: boolean;
  retracted?: boolean;
  discarded?: boolean;
  version?: string;
}

interface UpgradeTargets {
  targets?: UpgradeTargetRow[];
  allowedBranches?: string[];
  versionsError?: string;
  default?: string;
  discardedRc?: UpgradeStatus["discardedRc"];
  retractedRelease?: UpgradeStatus["retractedRelease"];
}

interface TargetOption {
  name: string;
  kind: string;
  allowed: boolean;
  retracted: boolean;
  discarded: boolean;
  note: string;
}

interface ReleaseNoteItem {
  version?: string;
  title?: string;
  markdown?: string;
  body?: string;
  text?: string;
}

// TARGET_GROUPS and CHANNEL_NOTES are the classic BranchPicker's: the
// heading each kind of target is listed under, and what each moving name
// means beside it.
const TARGET_GROUPS: Record<string, string> = {
  channel: "Standard branches",
  version: "Released versions",
  allowed: "Allowed on this deployment",
};
const CHANNEL_NOTES: Record<string, string> = {
  latest: "the last promoted release",
  candidate: "the current release candidate (alias: rc)",
  main: "the head of main",
};

const MODE_DESCRIPTIONS: Record<string, string> = {
  image:
    "Pulls the image CI published for the chosen branch, runs it once to prove it works, and then points this deployment at it and restarts. A branch CI has published no image for cannot be upgraded onto, and a pulled image that fails that check changes nothing.",
  kube: "Runs the image CI published for the chosen branch as a throwaway pod, then repoints this deployment's own Deployment at it. Kubernetes does the restart, so the UI is briefly down. An image that fails that check changes nothing.",
  source:
    "Fetches the chosen branch into this deployment's own checkout, rebuilds bin/grain, installs it over the running binary and restarts. A new binary that fails its check is rolled back.",
};

const CHECK_MODE_LABELS: Record<string, string> = {
  off: "Don't check",
  approve: "File a proposal to approve",
  auto: "File a check task and queue it",
};

// OFFLINE_MIGRATION_COMMANDS is the classic DatabaseBackend's: a store is
// moved only with the daemon stopped (docs/database.md).
export const OFFLINE_MIGRATION_COMMANDS = [
  "sudo systemctl stop grain-daemon.service",
  "sudo ./scripts/setup.sh --db-driver postgres",
  "sudo grain db check -data-dir /var/lib/grain/data",
].join("\n");

// PREVIEW_DELAY_MS is how long a typed target sits still before its
// release notes are asked for: asking pulls the target's image.
const PREVIEW_DELAY_MS = 800;

// BREAKING finds a release note's breaking change, however its author
// marked it: "**Breaking:**", a "Breaking changes" heading, BREAKING.
const BREAKING =
  /^#{1,6}\s*breaking\b|(\*\*|__)breaking\b|^\s*(?:[-*]\s*)?breaking(?: changes?)?\s*:/im;

export function hasBreakingChange(markdown: string): boolean {
  return BREAKING.test(markdown);
}

// tagForBranch is pkg/upgrade's TagForBranch: '/' written as "..".
function tagForBranch(branch: string): string {
  return branch.replaceAll("/", "..");
}

function imageTagOf(ref: string): string {
  const last = ref.split("/").pop() ?? "";
  const colon = last.lastIndexOf(":");
  return colon < 0 ? "" : last.slice(colon + 1);
}

// resolvedImage is what an upgrade onto branch would pull: an image named
// with its tag or digest as given, else branch's tag in the repository.
export function resolvedImage(
  image: string,
  repo: string,
  branch: string,
): string {
  const chosen = image.trim();
  if (chosen && (chosen.includes("@") || imageTagOf(chosen))) return chosen;
  const from = chosen || repo;
  const target = branch.trim();
  if (!from || !target) return from;
  return `${from}:${tagForBranch(target)}`;
}

function normalizeTarget(row: UpgradeTargetRow): TargetOption | null {
  const name = row.name || row.branch || "";
  if (!name) return null;
  const allowed = row.allowed !== false;
  const retracted = row.retracted === true;
  const discarded = row.discarded === true;
  const note = !allowed
    ? "not allowed on this deployment"
    : retracted
      ? "retracted"
      : discarded
        ? "discarded candidate"
        : CHANNEL_NOTES[name] ||
          (row.version && row.version !== name ? row.version : "");
  return { name, kind: row.kind || "", allowed, retracted, discarded, note };
}

@Component({
  selector: "g-settings-upgrade",
  imports: [RouterLink, MarkdownDirective, ConfirmDialogComponent],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  styles: [
    `
      .up-stack {
        display: flex;
        flex-direction: column;
        gap: 16px;
      }
      .up-alert {
        display: flex;
        flex-direction: column;
        align-items: stretch;
        gap: 8px;
      }
      .up-alert-actions {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
      }
      .up-alert code,
      .up-image code {
        font-family: var(--grain-font-mono);
        font-size: 12.5px;
      }
      .up-picker {
        position: relative;
      }
      .up-picker-list {
        position: absolute;
        z-index: 20;
        top: calc(100% + 4px);
        left: 0;
        right: 0;
        max-height: 320px;
        overflow: auto;
        margin: 0;
        padding: 4px 0;
        list-style: none;
        border-radius: var(--md-sys-shape-corner-medium);
        border: 1px solid var(--md-sys-color-outline-variant);
        background: var(--md-sys-color-surface-container-high);
        color: var(--md-sys-color-on-surface);
        box-shadow: var(--md-sys-elevation-2, 0 4px 12px rgb(0 0 0 / 0.18));
      }
      .up-picker-group {
        padding: 8px 14px 4px;
        font: 600 11px/16px var(--grain-font-body);
        letter-spacing: 0.04em;
        text-transform: uppercase;
        color: var(--md-sys-color-on-surface-variant);
      }
      .up-picker-option {
        display: flex;
        align-items: center;
        gap: 8px;
        flex-wrap: wrap;
        min-height: 36px;
        padding: 6px 14px;
        cursor: pointer;
        font-size: 14px;
      }
      .up-picker-option.is-active,
      .up-picker-option:hover {
        background: var(--md-sys-color-surface-container-highest);
      }
      .up-picker-option[aria-disabled="true"] {
        cursor: not-allowed;
        opacity: 0.6;
      }
      .up-picker-option .s-mono {
        font-family: var(--grain-font-mono);
      }
      .up-picker-note {
        color: var(--md-sys-color-on-surface-variant);
        font-size: 13px;
      }
      .up-picker-empty {
        padding: 8px 14px;
        color: var(--md-sys-color-on-surface-variant);
        font-size: 13px;
      }
      .up-sub {
        display: flex;
        flex-direction: column;
        gap: 10px;
        padding: 14px 16px;
        border-radius: var(--md-sys-shape-corner-medium);
        border: 1px solid var(--md-sys-color-outline-variant);
      }
      .up-sub-head {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 8px;
        flex-wrap: wrap;
      }
      .up-sub-title {
        margin: 0;
        font: 600 14px/20px var(--grain-font-body);
      }
      .up-chips {
        display: flex;
        flex-wrap: wrap;
        gap: 6px;
      }
      .up-chip {
        min-height: 32px;
        padding: 0 12px;
        border-radius: 999px;
        border: 1px solid var(--md-sys-color-outline);
        background: transparent;
        color: var(--md-sys-color-on-surface);
        font: 13px/18px var(--grain-font-mono);
        cursor: pointer;
      }
      .up-chip[aria-pressed="true"] {
        background: var(--md-sys-color-secondary-container);
        color: var(--md-sys-color-on-secondary-container);
        border-color: transparent;
      }
      .up-chip:disabled {
        cursor: not-allowed;
        opacity: 0.6;
      }
      .up-notes {
        max-height: 360px;
        overflow: auto;
        padding: 4px 16px;
        border-radius: var(--md-sys-shape-corner-medium);
        border: 1px solid var(--md-sys-color-outline-variant);
        font-size: 14px;
        line-height: 21px;
        overflow-wrap: anywhere;
      }
      .up-notes.is-breaking {
        border-color: var(--md-sys-color-warning);
        border-left-width: 4px;
      }
      .up-notes h1,
      .up-notes h2,
      .up-notes h3 {
        font: 600 15px/22px var(--grain-font-body);
        margin: 12px 0 4px;
      }
      .up-notes p,
      .up-notes ul,
      .up-notes ol {
        margin: 6px 0;
      }
      .up-notes code {
        font-family: var(--grain-font-mono);
        font-size: 12.5px;
      }
      .up-progress-bar {
        height: 6px;
        border-radius: 3px;
        background: var(--md-sys-color-surface-container-highest);
        overflow: hidden;
      }
      .up-progress-bar > span {
        display: block;
        height: 100%;
        background: var(--md-sys-color-primary);
        transition: width 200ms ease;
      }
      .up-steps {
        margin: 0;
        padding: 0;
        list-style: none;
        display: flex;
        flex-direction: column;
        gap: 2px;
      }
      .up-step {
        display: grid;
        grid-template-columns: 24px minmax(0, 1fr) auto auto;
        align-items: baseline;
        gap: 4px 10px;
        padding: 6px 0;
        border-bottom: 1px solid var(--md-sys-color-outline-variant);
        font-size: 14px;
        font-variant-numeric: tabular-nums;
      }
      .up-step:last-child {
        border-bottom: 0;
      }
      .up-step-mark {
        text-align: center;
        color: var(--md-sys-color-on-surface-variant);
      }
      .up-step-state {
        color: var(--md-sys-color-on-surface-variant);
        font-size: 13px;
      }
      .up-step-time {
        min-width: 5ch;
        text-align: right;
        color: var(--md-sys-color-on-surface-variant);
        font-size: 13px;
      }
      .up-step-detail {
        grid-column: 2 / -1;
        font-family: var(--grain-font-mono);
        font-size: 12.5px;
        color: var(--md-sys-color-on-surface-variant);
        overflow-wrap: anywhere;
      }
      .up-step.is-running .up-step-mark,
      .up-step.is-running .up-step-state {
        color: var(--md-sys-color-primary);
      }
      .up-step.is-failed .up-step-mark,
      .up-step.is-failed .up-step-state,
      .up-step.is-failed .up-step-detail {
        color: var(--md-sys-color-error);
      }
      .up-step.is-done .up-step-mark {
        color: var(--md-sys-color-tertiary, var(--md-sys-color-primary));
      }
      .up-dl {
        display: grid;
        grid-template-columns: max-content minmax(0, 1fr);
        gap: 4px 16px;
        margin: 0;
        font-size: 14px;
      }
      .up-dl dt {
        color: var(--md-sys-color-on-surface-variant);
      }
      .up-dl dd {
        margin: 0;
        overflow-wrap: anywhere;
      }
      .up-readonly-row {
        display: flex;
        align-items: center;
        gap: 8px 12px;
        flex-wrap: wrap;
        font-size: 14px;
      }
    `,
  ],
  template: `
    <div class="s-card" data-testid="card-upgrade">
      <div class="s-card-head">
        <div>
          <h3 class="s-card-title">Daemon version &amp; live upgrade</h3>
          <p class="s-card-desc">
            @if (status()?.enabled && !selfUpgradeDisabled()) {
              {{ modeDescription() }} One upgrade at a time; each step below
              reports itself as it goes.
            } @else {
              The build this deployment runs, and how it is upgraded.
            }
          </p>
        </div>
        <span
          class="status-pill healthy s-mono"
          data-testid="current-daemon-version"
        >
          {{ currentVersion() }}
        </span>
      </div>

      @if (status() === null) {
        @if (watch.loadError(); as err) {
          <div class="s-banner error" role="alert">
            This deployment's upgrade status could not be read ({{ err }}).
          </div>
        } @else {
          <span class="s-hint" data-testid="upgrade-loading"
            >Reading the upgrade status…</span
          >
        }
      }

      @for (n of status()?.notices ?? []; track $index) {
        <div
          class="s-banner warn up-alert"
          role="status"
          data-testid="upgrade-notice"
        >
          <span>{{ n.message }}</span>
          @if (n.command) {
            <span
              >Run <code class="s-mono">{{ n.command }}</code
              >.</span
            >
          }
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
            -upgrade-image nor -upgrade-src-dir configured, so there is nothing
            here that can cut it over to another branch.</span
          >
        </div>
      }

      @if (controllable()) {
        @if (retractedRelease(); as r) {
          <div
            class="s-banner warn up-alert"
            role="status"
            data-testid="upgrade-retracted-release"
          >
            <span
              ><strong>Release {{ r.running }} was retracted.</strong>
              @if (r.newer) {
                <strong>{{ r.newer }}</strong> has been published to fix forward
                from it.
              } @else {
                No release after it has been published yet.
              }
              This deployment is staying on <code>{{ r.running }}</code
              >: it will never downgrade on its own. Moving back to
              <strong>latest</strong
              >{{ r.latest ? " (" + r.latest + ")" : "" }} is only safe if the
              database schema has not changed since then.</span
            >
            <span class="up-alert-actions">
              @if (r.newer) {
                <button
                  type="button"
                  class="s-btn-sm"
                  [disabled]="running()"
                  (click)="chooseTarget(r.newer)"
                >
                  Upgrade to {{ r.newer }}
                </button>
              }
              <button
                type="button"
                class="s-btn-sm"
                [disabled]="running()"
                (click)="chooseTarget('latest')"
              >
                Move back to latest{{ r.latest ? " (" + r.latest + ")" : "" }}
              </button>
            </span>
          </div>
        }
        @if (discardedRc(); as d) {
          <div
            class="s-banner warn up-alert"
            role="status"
            data-testid="upgrade-discarded-rc"
          >
            <span
              ><strong>Release candidate {{ d.running }} was discarded</strong>
              @if (d.replacementRc) {
                and replaced by <strong>{{ d.replacementRc }}</strong
                >.
              } @else {
                : it failed qualification and will not be promoted.
              }
              This deployment is staying on <code>{{ d.running }}</code> (it
              will never downgrade on its own).</span
            >
            <span class="up-alert-actions">
              @if (d.replacementRc) {
                <button
                  type="button"
                  class="s-btn-sm"
                  [disabled]="running()"
                  (click)="chooseTarget('candidate')"
                >
                  Upgrade to candidate ({{ d.replacementRc }})
                </button>
              }
              <button
                type="button"
                class="s-btn-sm"
                [disabled]="running()"
                (click)="chooseTarget('latest')"
              >
                Move to latest{{ d.latest ? " (" + d.latest + ")" : "" }}
              </button>
            </span>
          </div>
        }
        @if (status()?.imageRepoWarning; as w) {
          <div class="s-banner warn up-alert" role="status">
            <strong
              >This deployment's image repository names no registry
              host.</strong
            >
            <span
              >Configured: <code>{{ status()?.imageRepo }}</code
              >. {{ w }}</span
            >
          </div>
        }

        @if (scheduledBranch(); as sb) {
          <div class="s-banner warn" data-testid="scheduled-upgrade-banner">
            <span>Upgrade scheduled (waiting for running tasks): {{ sb }}</span>
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

        <div class="s-field">
          <label class="s-label" for="up-target">Target channel / branch</label>
          <div class="up-picker">
            <input
              id="up-target"
              class="s-input s-mono"
              type="text"
              role="combobox"
              autocomplete="off"
              spellcheck="false"
              aria-autocomplete="list"
              aria-controls="up-target-list"
              data-testid="select-upgrade-target"
              placeholder="Search or type a branch"
              [attr.aria-expanded]="pickerOpen()"
              [attr.aria-activedescendant]="
                pickerOpen() && activeIndex() >= 0
                  ? 'up-target-opt-' + activeIndex()
                  : null
              "
              [value]="selectedBranch()"
              [disabled]="running()"
              (focus)="pickerOpen.set(true)"
              (click)="pickerOpen.set(true)"
              (input)="onTyped($any($event.target).value)"
              (keydown)="onPickerKey($event)"
              (blur)="pickerOpen.set(false)"
            />
            @if (pickerOpen()) {
              <ul
                id="up-target-list"
                class="up-picker-list"
                role="listbox"
                aria-label="Upgrade targets"
                data-testid="upgrade-target-list"
              >
                @for (o of filteredTargets(); track o.name; let i = $index) {
                  @if (i === 0 || filteredTargets()[i - 1].kind !== o.kind) {
                    @if (groupLabel(o.kind); as g) {
                      <li class="up-picker-group" role="presentation">
                        {{ g }}
                      </li>
                    }
                  }
                  <li
                    class="up-picker-option"
                    role="option"
                    [id]="'up-target-opt-' + i"
                    [class.is-active]="i === activeIndex()"
                    [attr.aria-selected]="o.name === selectedBranch()"
                    [attr.aria-disabled]="!o.allowed"
                    [attr.data-value]="o.name"
                    tabindex="-1"
                    (mousedown)="$event.preventDefault()"
                    (click)="pick(o)"
                    (keydown.enter)="pick(o)"
                  >
                    <span class="s-mono">{{ o.name }}</span>
                    @if (o.retracted) {
                      <span class="status-pill warn">retracted</span>
                    } @else if (o.discarded) {
                      <span class="status-pill warn">discarded</span>
                    } @else if (o.note) {
                      <span class="up-picker-note">— {{ o.note }}</span>
                    }
                  </li>
                } @empty {
                  <li class="up-picker-empty" role="presentation">
                    No listed target matches; Start upgrade takes the branch as
                    typed.
                  </li>
                }
              </ul>
            }
          </div>
          <span class="s-hint">{{ pickerHint() }}</span>
        </div>

        @if (selectedFlag(); as flag) {
          <div
            class="s-banner warn"
            role="status"
            data-testid="upgrade-target-warning"
          >
            @if (flag === "retracted") {
              <span
                ><strong>{{ selectedBranch() }} was retracted.</strong> It was
                pulled from the release train after it shipped; upgrading onto
                it lands a build its own release process took back.</span
              >
            } @else {
              <span
                ><strong
                  >Release candidate {{ selectedBranch() }} was
                  discarded.</strong
                >
                It failed qualification and will not be promoted.</span
              >
            }
          </div>
        }

        @if (status()?.imageRepo) {
          <div class="up-sub up-image" data-testid="card-upgrade-image">
            <div class="up-sub-head">
              <h4 class="up-sub-title">Image source</h4>
              @if (imageLocked(); as why) {
                <span
                  class="status-pill neutral"
                  data-testid="upgrade-image-readonly"
                  [attr.title]="why"
                  >Read-only</span
                >
              }
            </div>
            <span class="s-hint"
              >Pulled from the container registry
              <code>{{ status()?.imageRepo }}</code
              >, the branch's tag. Name another repository or one build to pull
              that instead.</span
            >
            <input
              class="s-input s-mono"
              type="text"
              autocomplete="off"
              spellcheck="false"
              aria-label="Image (optional)"
              data-testid="upgrade-image-input"
              [placeholder]="status()?.imageRepo ?? ''"
              [value]="image()"
              [disabled]="!!imageLocked()"
              (input)="image.set($any($event.target).value)"
            />
            @if ((status()?.imageRepos?.length ?? 0) > 1) {
              <div class="up-chips">
                @for (repo of status()?.imageRepos ?? []; track repo) {
                  <button
                    type="button"
                    class="up-chip"
                    [attr.aria-pressed]="
                      (image().trim() || status()?.imageRepo) === repo
                    "
                    [disabled]="!!imageLocked()"
                    (click)="
                      image.set(repo === status()?.imageRepo ? '' : repo)
                    "
                  >
                    {{
                      repo === status()?.defaultImageRepo
                        ? repo + " (default)"
                        : repo
                    }}
                  </button>
                }
              </div>
            }
            @if (imageLocked(); as why) {
              <span class="s-hint">{{ why }}</span>
            }
            @if (willPull(); as ref) {
              <span class="s-hint" data-testid="upgrade-will-pull"
                >Will pull <code>{{ ref }}</code></span
              >
            }
          </div>
        }

        @if (!running()) {
          <div class="up-stack" data-testid="upgrade-release-notes">
            @if (notes(); as n) {
              @if (n.loading) {
                <span class="s-hint"
                  >Reading the release notes {{ n.target }} carries…</span
                >
              } @else if (n.error) {
                <div class="s-banner warn" role="status">
                  Could not read the release notes {{ n.target }} carries:
                  {{ n.error }}
                </div>
              } @else {
                @if (n.message) {
                  <div class="s-banner readonly" role="status">
                    {{ n.message }}
                  </div>
                }
                @if (n.breaking) {
                  <div
                    class="s-banner warn"
                    role="status"
                    data-testid="upgrade-breaking-warning"
                  >
                    <span
                      ><strong>{{ n.target }} has a breaking change.</strong>
                      Read it below before upgrading.</span
                    >
                  </div>
                }
                @if (n.markdown) {
                  <div
                    class="up-notes msg-body"
                    data-testid="upgrade-release-notes-body"
                    [class.is-breaking]="n.breaking"
                    [gMarkdown]="n.markdown"
                  ></div>
                }
              }
            }
          </div>
        }

        <div class="s-toggle-row">
          <div class="s-toggle-copy">
            <span class="s-toggle-name"
              >Wait until this deployment is idle</span
            >
            <span class="s-hint"
              >On: stop picking new tasks up, let the work already under way
              finish, and upgrade then. Off: upgrade now, discarding whatever is
              running.</span
            >
          </div>
          <button
            type="button"
            class="g-switch"
            role="switch"
            data-testid="switch-upgrade-drain"
            [attr.aria-checked]="afterTasksFinish()"
            aria-label="Wait until this deployment is idle"
            [disabled]="running()"
            (click)="afterTasksFinish.set(!afterTasksFinish())"
          ></button>
        </div>

        @if (!afterTasksFinish() && (status()?.inFlightRuns ?? 0) > 0) {
          <span class="s-hint" data-testid="upgrade-in-flight"
            >{{ status()?.inFlightRuns }} agent
            {{ status()?.inFlightRuns === 1 ? "run is" : "runs are" }} in
            flight; upgrading now interrupts them.</span
          >
        }

        <div class="s-inline">
          <button
            type="button"
            class="btn-primary"
            data-testid="start-upgrade-btn"
            [disabled]="
              !selectedBranch().trim() ||
              running() ||
              !!status()?.cancelBlockedReason ||
              !access.can('POST /api/upgrade')
            "
            (click)="confirmStartUpgrade()"
          >
            {{
              running()
                ? "Upgrading…"
                : afterTasksFinish()
                  ? "Schedule upgrade"
                  : "Start upgrade"
            }}
          </button>
        </div>

        @if (running()) {
          <div class="s-banner warn" data-testid="active-upgrade-banner">
            <span
              >Upgrade in progress: {{ status()?.branch || "running" }}
              @if (status()?.cancelBlockedReason; as why) {
                · {{ why }}
              }
            </span>
            <button
              type="button"
              class="s-btn-danger s-btn-sm"
              data-testid="cancel-upgrade-btn"
              [disabled]="
                !status()?.cancellable ||
                !access.can('DELETE /api/upgrade/current')
              "
              (click)="cancelUpgrade()"
            >
              Cancel upgrade
            </button>
          </div>
        }

        @if (status()?.phase === "failed") {
          <div class="s-banner error" role="alert" data-testid="upgrade-failed">
            <span
              ><strong>Upgrade to {{ status()?.branch }} failed</strong
              >{{ failedError() ? ": " + failedError() : "." }}</span
            >
          </div>
        }

        @if (unchangedTarget(); as t) {
          <div
            class="s-banner readonly"
            role="status"
            data-testid="upgrade-unchanged"
          >
            The version on {{ t }} has not changed, so this deployment was not
            upgraded.
          </div>
        }

        @if (status()?.phase) {
          <dl class="up-dl" data-testid="upgrade-status">
            <dt>Branch</dt>
            <dd class="s-mono">{{ status()?.branch }}</dd>
            <dt>Status</dt>
            <dd data-testid="upgrade-phase">{{ status()?.phase }}</dd>
            @if (status()?.detail) {
              <dt>Detail</dt>
              <dd>{{ status()?.detail }}</dd>
            }
          </dl>
        }

        @if (progress().total > 0) {
          <section
            class="up-stack"
            aria-label="Upgrade progress"
            data-testid="upgrade-steps"
          >
            <div
              class="up-progress-bar"
              role="progressbar"
              aria-label="Upgrade progress"
              aria-valuemin="0"
              aria-valuemax="100"
              [attr.aria-valuenow]="progress().percent"
            >
              <span [style.width.%]="progress().percent"></span>
            </div>
            <span class="s-hint s-num" data-testid="upgrade-current-step">{{
              currentStepLine()
            }}</span>
            <ol class="up-steps">
              @for (step of steps(); track step.name; let i = $index) {
                <li
                  class="up-step"
                  [class]="'up-step is-' + step.state"
                  [attr.data-testid]="'upgrade-step-' + (i + 1)"
                >
                  <span class="up-step-mark" aria-hidden="true">{{
                    stepMark(step.state)
                  }}</span>
                  <span>{{ i + 1 }}. {{ step.name }}</span>
                  <span class="up-step-state">{{ step.state }}</span>
                  <span class="up-step-time">{{ stepTime(step) }}</span>
                  @if (step.detail) {
                    <span class="up-step-detail">{{ step.detail }}</span>
                  }
                </li>
              }
            </ol>
          </section>
        }
      }

      @if (!selfUpgradeDisabled()) {
        <div class="up-readonly-row" data-testid="upgrade-check-mode">
          <span class="s-label">After an upgrade lands</span>
          <span data-testid="upgrade-check-mode-value">{{
            checkModeLabel()
          }}</span>
          <a
            routerLink="/settings/general"
            fragment="updates"
            data-testid="upgrade-check-settings-link"
            >Change in Settings → General</a
          >
        </div>
      }
    </div>

    @if (showDatabase()) {
      <div class="s-card" data-testid="upgrade-database">
        <h3 class="s-card-title">Database backend</h3>
        @if (database(); as db) {
          <p class="s-card-desc" data-testid="upgrade-database-current">
            This deployment runs on
            <strong>{{ isPostgres() ? "PostgreSQL" : "SQLite" }}</strong
            >{{ db.dsn ? " (" + db.dsn + ")" : "" }}.
          </p>
        }
        @if (!selfUpgradeDisabled()) {
          <div
            class="s-banner readonly"
            role="note"
            data-testid="upgrade-database-offline"
          >
            <span
              >The store is moved only with the daemon stopped, never from here:
              a copy taken while grain is running loses everything written after
              it.
              @if (isPostgres()) {
                To move this PostgreSQL store to another server, stop the
                daemon, dump and restore it, check it with grain db check, and
                start the daemon again.
              } @else {
                To move this SQLite store into PostgreSQL, run these on the
                daemon's host. setup.sh stops the daemon, migrates the store and
                starts it again once grain db check finds nothing missing.
              }
              See docs/database.md.</span
            >
          </div>
          @if (!isPostgres()) {
            <pre class="s-code" data-testid="upgrade-database-commands">{{
              migrationCommands
            }}</pre>
          }
        }
      </div>
    }

    @if (confirmRequest(); as req) {
      <g-confirm-dialog [request]="req" (done)="onConfirmDone($event)" />
    }
  `,
})
export class SettingsUpgradeComponent implements OnInit {
  protected readonly api = inject(GrainApi);
  protected readonly access = inject(AccessService);
  protected readonly watch = inject(UpgradeWatchService);
  private readonly state = inject(SettingsStateService);
  private readonly snackbar = inject(SnackbarService);

  protected readonly migrationCommands = OFFLINE_MIGRATION_COMMANDS;
  protected readonly status = this.watch.status;
  protected readonly running = this.watch.running;

  protected readonly targets = signal<TargetOption[]>([]);
  protected readonly targetsMeta = signal<UpgradeTargets | null>(null);
  protected readonly selectedBranch = signal("");
  protected readonly query = signal("");
  protected readonly pickerOpen = signal(false);
  protected readonly activeIndex = signal(-1);
  protected readonly image = signal("");
  protected readonly afterTasksFinish = signal(false);
  protected readonly confirmRequest = signal<ConfirmRequest | null>(null);
  private readonly fallbackDatabase = signal<UpgradeStatus["database"]>(null);
  protected readonly notes = signal<{
    target: string;
    loading: boolean;
    error: string;
    message: string;
    markdown: string;
    breaking: boolean;
  } | null>(null);

  private previewTimer: ReturnType<typeof setTimeout> | null = null;
  private previewSeq = 0;

  constructor() {
    inject(DestroyRef).onDestroy(() => {
      if (this.previewTimer) clearTimeout(this.previewTimer);
    });
  }

  protected readonly selfUpgradeDisabled = computed(
    () => this.status()?.selfUpgradeDisabled === true,
  );
  protected readonly upgradeUnavailable = computed(
    () => this.status()?.enabled === false,
  );
  protected readonly controllable = computed(
    () =>
      this.status() !== null &&
      !this.upgradeUnavailable() &&
      !this.selfUpgradeDisabled(),
  );
  protected readonly managementUrl = computed(() =>
    safeManagementUrl(this.status()?.managementUrl),
  );
  protected readonly modeDescription = computed(
    () =>
      MODE_DESCRIPTIONS[this.status()?.mode ?? ""] ??
      "Upgrades this deployment onto the chosen branch and restarts it.",
  );

  // What the daemon reports it runs (GET /api/upgrade's `running`), else
  // the build stamp GET /api/config carries.
  protected readonly currentVersion = computed(() => {
    const u = this.status();
    const running = u?.running;
    return (
      running?.release ||
      running?.ref ||
      u?.currentVersion ||
      u?.version ||
      (running?.commit ? running.commit.slice(0, 7) : "") ||
      runningVersionLabel(this.state.config()?.version) ||
      runningVersionLabel(this.access.rawConfig()?.version) ||
      "unknown"
    );
  });

  protected readonly scheduledBranch = computed(() => {
    const s = this.status()?.scheduled;
    return s ? String(s.branch || "latest") : "";
  });

  protected readonly retractedRelease = computed(() => {
    const r =
      this.status()?.retractedRelease ?? this.targetsMeta()?.retractedRelease;
    return r?.running ? r : null;
  });
  protected readonly discardedRc = computed(() => {
    const d = this.status()?.discardedRc ?? this.targetsMeta()?.discardedRc;
    return d?.running ? d : null;
  });

  // The flag on the target picked, so a retracted version or a discarded
  // candidate is never upgraded onto without being told so.
  protected readonly selectedFlag = computed(() => {
    const t = this.targets().find((o) => o.name === this.selectedBranch());
    if (t?.retracted) return "retracted";
    if (t?.discarded) return "discarded";
    return "";
  });

  protected readonly filteredTargets = computed(() => {
    const all = this.targets();
    const q = this.query().trim().toLowerCase();
    if (!q || all.some((o) => o.name.toLowerCase() === q)) return all;
    return all.filter((o) => o.name.toLowerCase().includes(q));
  });

  protected readonly pickerHint = computed(() => {
    const meta = this.targetsMeta();
    let hint =
      "latest is the last promoted release, main is the head of main, and candidate (or rc) is the current release candidate. Pick one from the list or type any branch.";
    const allowed = meta?.allowedBranches ?? [];
    if (allowed.length > 0 && !allowed.includes("**")) {
      hint += ` This deployment only upgrades to ${allowed.join(", ")} (-upgrade-allowed-branches).`;
    }
    if (meta?.versionsError) {
      hint += ` Released versions could not be listed: ${meta.versionsError}`;
    }
    return hint;
  });

  // Why the image cannot be changed right now, or "" when it can.
  protected readonly imageLocked = computed(() => {
    if (this.running()) return "An upgrade is running.";
    if (!this.access.can("POST /api/upgrade")) {
      return "Your role cannot start an upgrade.";
    }
    if (this.afterTasksFinish()) {
      return "A scheduled upgrade pulls from the configured repository; choose an image only for an upgrade started now.";
    }
    return "";
  });

  protected readonly willPull = computed(() =>
    resolvedImage(
      this.imageLocked() ? "" : this.image(),
      this.status()?.imageRepo ?? "",
      this.selectedBranch(),
    ),
  );

  protected readonly steps = computed(() => this.status()?.steps ?? []);
  protected readonly progress = computed(() => upgradeProgress(this.steps()));
  protected readonly currentStepLine = computed(() => {
    const p = this.progress();
    if (!p.current) return `${p.settled} of ${p.total} steps finished`;
    const elapsed = formatElapsed(stepElapsedMs(p.current, this.watch.now()));
    return (
      `Step ${p.position} of ${p.total}: ${p.current.name}` +
      (elapsed ? ` (${elapsed})` : "")
    );
  });
  protected readonly failedError = computed(() => {
    const s = this.status();
    return s ? failedStepError(s) : "";
  });
  protected readonly unchangedTarget = computed(() => {
    const s = this.status();
    return s && s.phase !== "running" && s.unchanged ? s.branch || "" : "";
  });

  protected readonly checkModeLabel = computed(() => {
    const mode = String(this.state.settings()["upgradeCheckMode"] ?? "auto");
    return CHECK_MODE_LABELS[mode] ?? mode;
  });

  protected readonly database = computed(
    () => this.status()?.database ?? this.fallbackDatabase() ?? null,
  );
  protected readonly isPostgres = computed(
    () => this.database()?.driver === "postgres",
  );
  protected readonly showDatabase = computed(
    () =>
      this.status() !== null &&
      (!this.selfUpgradeDisabled() || this.isPostgres()),
  );

  ngOnInit(): void {
    void this.load();
  }

  private async load(): Promise<void> {
    if (this.access.can("GET /api/upgrade")) {
      await this.watch.refresh();
    }
    const s = this.status();
    if (s && !s.database && this.access.can("GET /api/database")) {
      try {
        const db =
          await this.api.get<Record<string, unknown>>("GET /api/database");
        const driver = String(db?.["driver"] ?? db?.["backend"] ?? "");
        if (driver) {
          this.fallbackDatabase.set({
            driver,
            dsn: typeof db["dsn"] === "string" ? db["dsn"] : "",
          });
        }
      } catch {
        // Non-fatal: the card says what it can without it.
      }
    }
    if (!this.controllable() || !this.access.can("GET /api/upgrade/targets")) {
      return;
    }
    let meta: UpgradeTargets = { targets: [] };
    try {
      const t = await this.api.get<UpgradeTargetRow[] | UpgradeTargets>(
        "GET /api/upgrade/targets",
      );
      meta = Array.isArray(t) ? { targets: t } : (t ?? { targets: [] });
    } catch (err) {
      meta = { targets: [], versionsError: errorText(err) };
    }
    const list = (meta.targets ?? [])
      .map(normalizeTarget)
      .filter((x): x is TargetOption => x !== null);
    this.targetsMeta.set(meta);
    this.targets.set(list);
    if (!this.selectedBranch()) {
      const def = this.defaultTarget(list, meta);
      this.selectedBranch.set(def);
      this.query.set("");
      this.previewNotes(def, 0);
    }
  }

  // defaultTarget is the channel this deployment follows, else the first
  // listed target that is allowed and was not retracted or discarded --
  // never a version its own release process took back.
  private defaultTarget(list: TargetOption[], meta: UpgradeTargets): string {
    const s = this.status();
    const channel = meta.default || s?.channel || "";
    if (channel) return channel;
    const safe = list.find((o) => o.allowed && !o.retracted && !o.discarded);
    return safe?.name || s?.running?.branch || "latest";
  }

  protected groupLabel(kind: string): string {
    return TARGET_GROUPS[kind] ?? "";
  }

  protected onTyped(value: string): void {
    this.selectedBranch.set(value);
    this.query.set(value);
    this.pickerOpen.set(true);
    this.activeIndex.set(-1);
    this.previewNotes(value, PREVIEW_DELAY_MS);
  }

  protected pick(o: TargetOption): void {
    if (!o.allowed) return;
    this.chooseTarget(o.name);
  }

  protected chooseTarget(name: string): void {
    this.selectedBranch.set(name);
    this.query.set("");
    this.pickerOpen.set(false);
    this.activeIndex.set(-1);
    this.previewNotes(name, 0);
  }

  protected onPickerKey(ev: KeyboardEvent): void {
    const list = this.filteredTargets();
    if (ev.key === "ArrowDown" || ev.key === "ArrowUp") {
      ev.preventDefault();
      this.pickerOpen.set(true);
      if (list.length === 0) return;
      const step = ev.key === "ArrowDown" ? 1 : -1;
      let i = this.activeIndex();
      for (let n = 0; n < list.length; n++) {
        i = (i + step + list.length) % list.length;
        if (list[i].allowed) break;
      }
      this.activeIndex.set(i);
    } else if (ev.key === "Enter") {
      const o = list[this.activeIndex()];
      if (this.pickerOpen() && o) {
        ev.preventDefault();
        this.pick(o);
      }
    } else if (ev.key === "Escape" && this.pickerOpen()) {
      ev.preventDefault();
      ev.stopPropagation();
      this.pickerOpen.set(false);
    }
  }

  private previewNotes(target: string, delay: number): void {
    if (this.previewTimer) clearTimeout(this.previewTimer);
    const branch = target.trim();
    if (!branch || !this.access.can("GET /api/upgrade/release-notes")) {
      this.notes.set(null);
      return;
    }
    const seq = ++this.previewSeq;
    this.notes.set({
      target: branch,
      loading: true,
      error: "",
      message: "",
      markdown: "",
      breaking: false,
    });
    this.previewTimer = setTimeout(async () => {
      try {
        const r = await this.api.get<unknown>(
          "GET /api/upgrade/release-notes",
          undefined,
          `branch=${encodeURIComponent(branch)}`,
        );
        if (seq !== this.previewSeq) return;
        const { message, markdown } = releaseNotesOf(r);
        this.notes.set({
          target: branch,
          loading: false,
          error: "",
          message,
          markdown,
          breaking: hasBreakingChange(markdown),
        });
      } catch (err) {
        if (seq !== this.previewSeq) return;
        this.notes.set({
          target: branch,
          loading: false,
          error: errorText(err),
          message: "",
          markdown: "",
          breaking: false,
        });
      }
    }, delay);
  }

  protected stepMark(state: string): string {
    return (
      (
        {
          done: "✓",
          running: "●",
          failed: "✕",
          skipped: "–",
          cancelled: "⊘",
        } as Record<string, string>
      )[state] ?? "○"
    );
  }

  protected stepTime(step: {
    startedAt?: string;
    finishedAt?: string;
  }): string {
    return formatElapsed(stepElapsedMs(step as never, this.watch.now()));
  }

  // Starting an upgrade restarts grain onto another build, so it asks
  // first, naming the runs an upgrade started now would interrupt.
  protected confirmStartUpgrade(): void {
    const branch = this.selectedBranch().trim();
    if (!branch) return;
    const drain = this.afterTasksFinish();
    const inFlight = this.status()?.inFlightRuns ?? 0;
    const flag = this.selectedFlag();
    const warnings = [
      flag === "retracted" ? `${branch} was retracted.` : "",
      flag === "discarded" ? `${branch} is a discarded release candidate.` : "",
      this.notes()?.breaking ? `${branch} has a breaking change.` : "",
    ].filter(Boolean);
    const body = drain
      ? `Grain waits for running tasks to finish, then restarts onto ${branch}.`
      : inFlight > 0
        ? `Grain restarts onto ${branch} now, interrupting ${inFlight} agent ${inFlight === 1 ? "run" : "runs"} in flight.`
        : `Grain restarts onto ${branch} now.`;
    this.confirmRequest.set({
      title: `Upgrade grain to ${branch}?`,
      message: [...warnings, body].join(" "),
      accept: drain ? "Schedule upgrade" : "Upgrade now",
      destructive: !drain || warnings.length > 0,
      run: () => this.startUpgrade(),
    });
  }

  protected async onConfirmDone(accepted: boolean): Promise<void> {
    const req = this.confirmRequest();
    this.confirmRequest.set(null);
    if (accepted && req) await req.run();
  }

  protected async startUpgrade(): Promise<void> {
    const branch = this.selectedBranch().trim();
    const drain = this.afterTasksFinish();
    const image = drain ? "" : this.image().trim();
    try {
      const res = await this.api.post<UpgradeStatus>(
        "POST /api/upgrade",
        undefined,
        image
          ? { branch, image, afterTasksFinish: drain }
          : { branch, afterTasksFinish: drain },
      );
      if (!this.watch.apply(res)) await this.watch.refresh();
      this.snackbar.show(
        drain
          ? `Scheduled upgrade to ${branch}`
          : `Started upgrade to ${branch}`,
      );
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async cancelScheduledUpgrade(): Promise<void> {
    try {
      const res = await this.api.delete<UpgradeStatus>(
        "DELETE /api/upgrade/scheduled",
      );
      if (!this.watch.apply(res)) await this.watch.refresh();
      this.snackbar.show("Cancelled scheduled upgrade");
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async cancelUpgrade(): Promise<void> {
    try {
      const res = await this.api.delete<UpgradeStatus>(
        "DELETE /api/upgrade/current",
        undefined,
        { reason: "cancelled from Upgrade pane" },
      );
      if (!this.watch.apply(res)) await this.watch.refresh();
      this.snackbar.show("Cancelled the running upgrade");
    } catch (err) {
      this.state.recordError(err);
      await this.watch.refresh();
    }
  }
}

// releaseNotesOf reads GET /api/upgrade/release-notes's answer --
// pkg/upgrade's preview {from, version, message, notes: [{version,
// markdown}]}, or plain Markdown from an older or stubbed server -- into
// one Markdown document and the server's message about it.
export function releaseNotesOf(raw: unknown): {
  message: string;
  markdown: string;
} {
  if (typeof raw === "string") return { message: "", markdown: raw };
  if (!raw || typeof raw !== "object") return { message: "", markdown: "" };
  const rec = raw as {
    notes?: ReleaseNoteItem[] | string;
    message?: string;
    markdown?: string;
  };
  const message =
    typeof rec.message === "string" && rec.message.trim()
      ? rec.message.trim().replace(/^./, (c) => c.toUpperCase()) + "."
      : "";
  const parts: string[] = [];
  if (typeof rec.notes === "string" && rec.notes.trim()) {
    parts.push(rec.notes.trim());
  } else if (Array.isArray(rec.notes)) {
    for (const n of rec.notes) {
      if (!n || typeof n !== "object") continue;
      const md = (n.markdown ?? n.body ?? n.text ?? "").trim();
      const head = [n.version, n.title].filter(Boolean).join(" — ");
      if (md) parts.push(md);
      else if (head) parts.push(`## ${head}`);
    }
  } else if (typeof rec.markdown === "string" && rec.markdown.trim()) {
    parts.push(rec.markdown.trim());
  }
  return { message, markdown: parts.join("\n\n") };
}

// runningVersionLabel names the build GET /api/config's `version` stamp
// describes the way the classic pane's "Now running" does: the release,
// or the short commit when the build carries no real release.
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
