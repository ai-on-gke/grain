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
  OnDestroy,
  OnInit,
  ViewEncapsulation,
  computed,
  inject,
  signal,
} from "@angular/core";
import { RouterLink } from "@angular/router";
import { GrainApi } from "../api/grain-api.service";
import { type VersionInfo, versionLabel } from "../api/version";
import { errorText } from "../sessions/session-view";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import { MarkdownDirective } from "../shared/code-highlight";
import {
  ConfirmDialogComponent,
  type ConfirmRequest,
} from "../shared/confirm-dialog.component";
import { SettingsStateService } from "./settings-state.service";
import {
  CHANNEL_NOTES,
  OFFLINE_MIGRATION_COMMANDS,
  UPGRADE_CHECK_HELP,
  UPGRADE_CHECK_LABELS,
  type DiscardedRC,
  type ReleaseNotesPreview,
  type RetractedRelease,
  type UpgradeNotice,
  type UpgradeTarget,
  type UpgradeTargets,
  capitalize,
  filterTargets,
  nextRCVersion,
  pickerHelp,
  readDiscardedRC,
  readNotices,
  readReleaseNotes,
  readRetractedRelease,
  readTargets,
  resolvedImage,
  safeHttpUrl,
} from "./upgrade-view";

// PREVIEW_DELAY_MS is how long a typed branch has to sit still before it
// is asked for release notes: asking pulls the target's image, so a branch
// being typed is not asked about one keystroke at a time.
const PREVIEW_DELAY_MS = 600;

const TERMINAL_PHASES = new Set([
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

interface DatabaseStatus {
  driver: string;
  dsn: string;
}

interface NotesState {
  target: string;
  loading: boolean;
  error: string;
  preview: ReleaseNotesPreview | null;
}

interface PickerOption {
  target: UpgradeTarget;
  index: number;
  note: string;
  tag: string;
}

// SettingsUpgradeComponent is Settings -> Upgrade: the running build, the
// branch (and, on image deployments, the image) an upgrade goes to, what
// that target's release notes say, warnings for a retracted release or a
// discarded candidate, the post-upgrade check setting and the database
// backend -- the classic ui/src/components/UpgradePanel.jsx, read off the
// same API fields.
@Component({
  selector: "g-settings-upgrade",
  imports: [ConfirmDialogComponent, MarkdownDirective, RouterLink],
  styleUrl: "./settings-upgrade.css",
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="s-card" data-testid="card-upgrade">
      <div class="s-card-head">
        <div>
          <h3 class="s-card-title">Daemon version &amp; live upgrade</h3>
          <p class="s-card-desc">{{ modeDescription() }}</p>
        </div>
        <span
          class="status-pill healthy s-mono"
          data-testid="current-daemon-version"
        >
          {{ currentVersion() }}
        </span>
      </div>

      @for (n of notices(); track $index) {
        <div class="s-banner warn upg-banner" data-testid="upgrade-notice">
          <div class="upg-banner-copy">
            <span>{{ n.message }}</span>
            @if (n.command) {
              <span
                >Run <code class="upg-code">{{ n.command }}</code
                >.</span
              >
            }
          </div>
          @if (n.docsUrl) {
            <a
              class="btn-tonal s-btn-sm"
              [href]="n.docsUrl"
              target="_blank"
              rel="noopener noreferrer"
              >Read more</a
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

      @if (upgradeControllable()) {
        @if (discardedRc(); as d) {
          <div
            class="s-banner warn upg-banner"
            role="status"
            data-testid="upgrade-discarded-rc"
          >
            <div class="upg-banner-copy">
              @if (d.replacementRc) {
                <span
                  ><strong
                    >Release candidate {{ d.running }} was discarded</strong
                  >
                  and replaced by <strong>{{ d.replacementRc }}</strong
                  >. This deployment is staying on
                  <code class="upg-code">{{ d.running }}</code> (it will never
                  downgrade on its own).</span
                >
              } @else {
                <span
                  ><strong
                    >Release candidate {{ d.running }} was discarded.</strong
                  >
                  It failed qualification and will not be promoted, and no
                  replacement candidate has been cut yet. This deployment is
                  staying on <code class="upg-code">{{ d.running }}</code> (it
                  will never downgrade on its own). You can wait for
                  <code class="upg-code">{{ nextRC(d.running) }}</code
                  >, or move to latest.</span
                >
              }
            </div>
            <div class="upg-banner-actions">
              @if (d.replacementRc) {
                <button
                  type="button"
                  class="btn-tonal s-btn-sm"
                  data-testid="upgrade-discarded-pick-candidate"
                  [disabled]="upgradeBusy()"
                  (click)="pickBranch('candidate')"
                >
                  Upgrade to candidate ({{ d.replacementRc }})
                </button>
              }
              <button
                type="button"
                class="btn-tonal s-btn-sm"
                data-testid="upgrade-discarded-pick-latest"
                [disabled]="upgradeBusy()"
                (click)="pickBranch('latest')"
              >
                Move to {{ latestLabel(d.latest) }}
              </button>
            </div>
          </div>
        }

        @if (retractedRelease(); as r) {
          <div
            class="s-banner warn upg-banner"
            role="status"
            data-testid="upgrade-retracted-release"
          >
            <div class="upg-banner-copy">
              <span
                ><strong>Release {{ r.running }} was retracted.</strong>
                @if (r.newer) {
                  <strong>{{ r.newer }}</strong> has been published to fix
                  forward from it.
                } @else {
                  No release after it has been published yet.
                }
                This deployment is staying on
                <code class="upg-code">{{ r.running }}</code
                >: it will never downgrade on its own. Moving back to
                {{ latestLabel(r.latest) }} is only safe if the database schema
                has not changed since then &mdash; an older release may not read
                a schema {{ r.running }} migrated to.
                {{
                  r.newer
                    ? "Upgrading forward is the safe move."
                    : "If it has changed, wait for the fix-forward release instead."
                }}</span
              >
            </div>
            <div class="upg-banner-actions">
              @if (r.newer) {
                <button
                  type="button"
                  class="btn-tonal s-btn-sm"
                  data-testid="upgrade-retracted-pick-newer"
                  [disabled]="upgradeBusy()"
                  (click)="pickBranch(r.newer)"
                >
                  Upgrade to {{ r.newer }}
                </button>
              }
              <button
                type="button"
                class="btn-tonal s-btn-sm"
                data-testid="upgrade-retracted-pick-latest"
                [disabled]="upgradeBusy()"
                (click)="pickBranch('latest')"
              >
                Move back to {{ latestLabel(r.latest) }}
              </button>
            </div>
          </div>
        }

        @if (imageRepoWarning()) {
          <div
            class="s-banner warn upg-banner"
            role="status"
            data-testid="upgrade-image-repo-warning"
          >
            <div class="upg-banner-copy">
              <strong
                >This deployment's image repository names no registry
                host.</strong
              >
              <span
                >Configured:
                <code class="upg-code">{{ imageRepo() }}</code>
                @if (defaultImageRepo() && defaultImageRepo() !== imageRepo()) {
                  &middot; This build's default:
                  <code class="upg-code">{{ defaultImageRepo() }}</code>
                }
              </span>
              <span>{{ imageRepoWarning() }}</span>
            </div>
          </div>
        }
      }

      @if (scheduledUpgradeBranch()) {
        <div class="s-banner warn" data-testid="scheduled-upgrade-banner">
          <span
            >Upgrade scheduled (waiting for running tasks):
            {{ scheduledUpgradeBranch() }}</span
          >
          <button
            type="button"
            class="btn-tonal s-btn-sm"
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
            class="btn-tonal s-btn-sm"
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
        <div class="s-field upg-picker">
          <label class="s-label" for="upg-branch">Branch</label>
          <div class="upg-combo">
            <input
              id="upg-branch"
              class="s-input s-mono"
              type="text"
              role="combobox"
              autocomplete="off"
              autocapitalize="off"
              spellcheck="false"
              aria-autocomplete="list"
              aria-controls="upg-branch-list"
              aria-describedby="upg-branch-help"
              data-testid="select-upgrade-target"
              [attr.aria-expanded]="pickerShown()"
              [attr.aria-activedescendant]="
                pickerShown() && activeIndex() >= 0
                  ? 'upg-opt-' + activeIndex()
                  : null
              "
              [placeholder]="branchPlaceholder()"
              [value]="branch()"
              [disabled]="upgradeBusy()"
              (input)="onBranchInput($any($event.target).value)"
              (focus)="openPicker()"
              (click)="openPicker()"
              (keydown)="onPickerKey($event)"
              (blur)="pickerOpen.set(false)"
            />
            <button
              type="button"
              class="upg-combo-toggle"
              tabindex="-1"
              aria-label="Show upgrade targets"
              [disabled]="upgradeBusy()"
              (mousedown)="$event.preventDefault(); togglePicker()"
            >
              <svg
                width="18"
                height="18"
                viewBox="0 0 24 24"
                aria-hidden="true"
              >
                <path d="m7 10 5 5 5-5z" fill="currentColor" />
              </svg>
            </button>
            @if (pickerShown()) {
              <div
                id="upg-branch-list"
                class="upg-listbox"
                role="listbox"
                aria-label="Upgrade targets"
                data-testid="upgrade-target-list"
                (mousedown)="$event.preventDefault()"
              >
                @for (g of pickerGroups(); track g.label) {
                  <div
                    class="upg-group"
                    role="group"
                    [attr.aria-label]="g.label || 'Other branches'"
                  >
                    @if (g.label) {
                      <div class="upg-group-label" aria-hidden="true">
                        {{ g.label }}
                      </div>
                    }
                    @for (o of g.options; track o.target.name) {
                      <div
                        class="upg-option"
                        role="option"
                        [id]="'upg-opt-' + o.index"
                        [class.is-active]="o.index === activeIndex()"
                        [attr.aria-selected]="o.target.name === branch()"
                        [attr.aria-disabled]="!o.target.allowed || null"
                        [attr.data-value]="o.target.name"
                        tabindex="-1"
                        (click)="chooseOption(o.target)"
                        (keydown.enter)="chooseOption(o.target)"
                        (mousemove)="activeIndex.set(o.index)"
                      >
                        <span class="upg-option-name">{{ o.target.name }}</span>
                        @if (o.tag) {
                          <span class="upg-tag is-warn">{{ o.tag }}</span>
                        }
                        @if (o.note) {
                          <span class="upg-option-note">{{ o.note }}</span>
                        }
                      </div>
                    }
                  </div>
                }
              </div>
            }
          </div>
          <span class="s-hint" id="upg-branch-help">{{ helpText() }}</span>
          @if (selectedTargetDisallowed()) {
            <span class="s-hint upg-error" data-testid="upgrade-target-refused"
              >{{ branch() }} is not allowed on this deployment
              (-upgrade-allowed-branches).</span
            >
          }
        </div>

        @if (imageRepo()) {
          <div class="s-field upg-image" data-testid="upgrade-image">
            <label class="s-label" for="upg-image">Image (optional)</label>
            <input
              id="upg-image"
              class="s-input s-mono"
              type="text"
              autocomplete="off"
              autocapitalize="off"
              spellcheck="false"
              aria-describedby="upg-image-help"
              data-testid="input-upgrade-image"
              [placeholder]="imageRepo()"
              [value]="image()"
              [disabled]="upgradeBusy() || afterTasksFinish()"
              (input)="image.set($any($event.target).value)"
            />
            <span class="s-hint" id="upg-image-help">{{ imageHelp() }}</span>
            @if (imageRepos().length > 1) {
              <div
                class="upg-chips"
                role="group"
                aria-label="Image repositories this deployment allows"
              >
                @for (repo of imageRepos(); track repo) {
                  <button
                    type="button"
                    class="upg-chip"
                    [attr.aria-pressed]="chosenRepo() === repo"
                    [disabled]="upgradeBusy() || afterTasksFinish()"
                    (click)="pickRepo(repo)"
                  >
                    {{
                      repo === defaultImageRepo() ? repo + " (default)" : repo
                    }}
                  </button>
                }
              </div>
            }
            @if (willPull()) {
              <span class="upg-will-pull" data-testid="upgrade-will-pull"
                >Will pull <code class="upg-code">{{ willPull() }}</code></span
              >
            }
          </div>
        }

        <div class="s-toggle-row">
          <div class="s-toggle-copy">
            <span class="s-toggle-name"
              >Drain running tasks before restarting</span
            >
            <span class="s-hint"
              >Stops picking new tasks up, lets the work already under way
              finish, then upgrades. Off: upgrade now, interrupting runs in
              flight.</span
            >
          </div>
          <button
            type="button"
            class="g-switch"
            role="switch"
            data-testid="switch-upgrade-drain"
            [attr.aria-checked]="afterTasksFinish()"
            aria-label="Drain running tasks before restarting"
            [disabled]="upgradeBusy()"
            (click)="afterTasksFinish.set(!afterTasksFinish())"
          ></button>
        </div>

        <div class="s-inline">
          <button
            type="button"
            class="btn-primary"
            data-testid="start-upgrade-btn"
            [disabled]="!canStart()"
            (click)="confirmStartUpgrade()"
          >
            Start upgrade
          </button>
          @if (startBlockedReason()) {
            <span class="s-hint">{{ startBlockedReason() }}</span>
          }
        </div>

        @if (notes(); as ns) {
          <section
            class="upg-notes"
            data-testid="upgrade-release-notes"
            aria-labelledby="upg-notes-heading"
          >
            <h4 class="upg-subhead" id="upg-notes-heading">
              What {{ ns.target }} brings
            </h4>
            @if (ns.loading) {
              <span class="s-hint" role="status"
                >Reading the release notes {{ ns.target }} carries…</span
              >
            } @else if (ns.error) {
              <div class="s-banner warn" role="status">
                <span
                  >Could not read the release notes {{ ns.target }} carries:
                  {{ ns.error }}</span
                >
              </div>
            } @else if (ns.preview) {
              @let p = ns.preview;
              @if (p.from || p.version) {
                <span class="s-hint">{{ previewSummary(p) }}</span>
              }
              @if (p.message) {
                <div
                  class="s-banner"
                  [class.readonly]="p.notInThisBuild"
                  [class.warn]="!p.notInThisBuild"
                  role="status"
                >
                  <span>{{ cap(p.message) }}.</span>
                </div>
              }
              @if (breakingVersions().length) {
                <div
                  class="s-banner warn upg-banner"
                  role="status"
                  data-testid="upgrade-breaking-warning"
                >
                  <div class="upg-banner-copy">
                    <strong>This upgrade includes breaking changes.</strong>
                    <span
                      >{{ breakingSentence() }} Read them below before you start
                      the upgrade.</span
                    >
                  </div>
                </div>
              }
              @for (n of p.notes; track $index) {
                <article
                  class="upg-note"
                  [class.is-breaking]="n.breaking"
                  [attr.data-testid]="
                    'upgrade-release-note-' + (n.version || 'notes')
                  "
                >
                  @if (n.version || n.breaking) {
                    <header class="upg-note-head">
                      @if (n.version) {
                        <span class="upg-note-version">{{ n.version }}</span>
                      }
                      @if (n.breaking) {
                        <span class="upg-tag is-warn">Breaking changes</span>
                      }
                    </header>
                  }
                  <div class="upg-note-body" [gMarkdown]="n.markdown"></div>
                </article>
              }
            }
          </section>
        }
      }

      @if (!selfUpgradeDisabled()) {
        <div
          class="s-toggle-row upg-readonly-row"
          data-testid="upgrade-check-mode"
        >
          <div class="s-toggle-copy">
            <span class="s-toggle-name"
              >After an upgrade: {{ checkModeLabel() }}</span
            >
            <span class="s-hint">{{ checkModeHelp() }}</span>
          </div>
          <a
            class="btn-tonal s-btn-sm"
            data-testid="upgrade-check-mode-link"
            routerLink="/settings/general"
            fragment="upgrade-check"
            >Change in General</a
          >
        </div>
      }

      @if (showDatabase()) {
        <section
          class="upg-db"
          data-testid="upgrade-database"
          aria-labelledby="upg-db-heading"
        >
          <h4 class="upg-subhead" id="upg-db-heading">Database backend</h4>
          @if (database(); as db) {
            <span class="upg-db-current" data-testid="upgrade-database-current"
              >This deployment runs on
              <strong>{{ isPostgres() ? "PostgreSQL" : "SQLite" }}</strong
              >{{ db.dsn ? " (" + db.dsn + ")" : "" }}.</span
            >
          }
          @if (!selfUpgradeDisabled()) {
            <div class="s-banner readonly" role="note">
              <span
                >The store is moved only with the daemon stopped, never from
                here: a copy taken while grain is running loses everything
                written after it.
                {{
                  isPostgres()
                    ? "To move this PostgreSQL store to another server, stop the daemon, dump and restore it, check it with grain db check, and start the daemon again."
                    : "To move this SQLite store into PostgreSQL, run these on the daemon's host. setup.sh stops the daemon, migrates the store and starts it again once grain db check finds nothing missing."
                }}
                See docs/database.md.</span
              >
            </div>
            @if (!isPostgres()) {
              <pre
                class="s-code upg-db-commands"
                data-testid="upgrade-database-commands"
                >{{ migrationCommands }}</pre>
            }
          }
        </section>
      }
    </div>

    @if (confirmRequest(); as req) {
      <g-confirm-dialog [request]="req" (done)="onConfirmDone($event)" />
    }
  `,
})
export class SettingsUpgradeComponent implements OnInit, OnDestroy {
  private readonly api = inject(GrainApi);
  protected readonly access = inject(AccessService);
  protected readonly state = inject(SettingsStateService);
  private readonly snackbar = inject(SnackbarService);
  private readonly doc = inject(DOCUMENT);

  protected readonly migrationCommands = OFFLINE_MIGRATION_COMMANDS;
  protected readonly confirmRequest = signal<ConfirmRequest | null>(null);

  // GET /api/upgrade, as last read (or as POST /api/upgrade answered).
  private readonly status = signal<Record<string, unknown>>({});
  private readonly targetsData = signal<UpgradeTargets | null>(null);
  private readonly targetsError = signal("");
  private readonly dbFallback = signal<DatabaseStatus | null>(null);

  protected readonly branch = signal("");
  protected readonly image = signal("");
  protected readonly afterTasksFinish = signal(true);
  protected readonly pickerOpen = signal(false);
  protected readonly activeIndex = signal(-1);
  // What the person typed into the box; the list filters on it. Picking
  // an entry clears it, so reopening the box shows every choice.
  private readonly query = signal("");
  protected readonly notes = signal<NotesState | null>(null);
  // Set by Start upgrade until GET /api/upgrade says otherwise.
  private readonly localActive = signal("");
  private readonly localScheduled = signal<string | null>(null);

  private previewTimer: ReturnType<typeof setTimeout> | null = null;
  private notesSeq = 0;

  protected readonly currentVersion = computed(() => {
    const u = this.status();
    const running = u["running"] as
      { release?: string; ref?: string; commit?: string } | undefined;
    return (
      running?.release ||
      running?.ref ||
      str(u["currentVersion"]) ||
      str(u["version"]) ||
      (running?.commit ? running.commit.slice(0, 7) : "") ||
      runningVersionLabel(this.state.config()?.version) ||
      "unknown"
    );
  });

  protected readonly selfUpgradeDisabled = computed(
    () => this.status()["selfUpgradeDisabled"] === true,
  );
  protected readonly upgradeUnavailable = computed(
    () => this.status()["enabled"] === false,
  );
  protected readonly upgradeControllable = computed(
    () => !this.upgradeUnavailable() && !this.selfUpgradeDisabled(),
  );
  protected readonly managementUrl = computed(() =>
    safeHttpUrl(this.status()["managementUrl"]),
  );
  protected readonly notices = computed<UpgradeNotice[]>(() =>
    readNotices(this.status()["notices"]),
  );

  protected readonly modeDescription = computed(() => {
    switch (str(this.status()["mode"])) {
      case "image":
        return "Pulls the image CI published for the branch below, runs it once to prove it works, then points this deployment at it and restarts. An image that fails that check changes nothing.";
      case "kube":
        return "Runs the image CI published for the branch below as a throwaway pod, then repoints this deployment's own Deployment at it. Kubernetes does the restart, so the UI is briefly down.";
      case "source":
        return "Fetches the branch below into this deployment's checkout, rebuilds and installs bin/grain, and restarts. A binary that fails its check is rolled back.";
      default:
        return "Inspect target channels, release notes, and roll forward or cancel active upgrades.";
    }
  });

  protected readonly scheduledUpgradeBranch = computed(() => {
    const local = this.localScheduled();
    if (local !== null) return local;
    const s = this.status()["scheduled"] as { branch?: string } | null;
    return s ? String(s.branch || "latest") : "";
  });

  protected readonly upgradeCancellable = computed(
    () => Boolean(this.status()["cancellable"]) || this.localActive() !== "",
  );

  protected readonly activeUpgradeState = computed(() => {
    const u = this.status();
    const phase = str(u["phase"]);
    if (
      Boolean(u["cancellable"]) ||
      (phase && !TERMINAL_PHASES.has(phase.toLowerCase()))
    ) {
      return phase || "running";
    }
    return this.localActive();
  });

  // An upgrade under way, or one whose cancel is blocked, leaves nothing
  // to choose: the classic pane disables the form the same way.
  protected readonly upgradeBusy = computed(
    () =>
      str(this.status()["phase"]) === "running" ||
      Boolean(str(this.status()["cancelBlockedReason"])),
  );

  protected readonly discardedRc = computed<DiscardedRC | null>(
    () =>
      readDiscardedRC(this.status()["discardedRc"]) ??
      this.targetsData()?.discardedRc ??
      null,
  );
  protected readonly retractedRelease = computed<RetractedRelease | null>(
    () =>
      readRetractedRelease(this.status()["retractedRelease"]) ??
      this.targetsData()?.retractedRelease ??
      null,
  );

  protected readonly imageRepo = computed(() =>
    str(this.status()["imageRepo"]),
  );
  protected readonly defaultImageRepo = computed(() =>
    str(this.status()["defaultImageRepo"]),
  );
  protected readonly imageRepoWarning = computed(() =>
    str(this.status()["imageRepoWarning"]),
  );
  protected readonly imageRepos = computed(() => {
    const v = this.status()["imageRepos"];
    return Array.isArray(v) ? v.map(String).filter(Boolean) : [];
  });
  protected readonly chosenRepo = computed(
    () => this.image().trim() || this.imageRepo(),
  );
  protected readonly willPull = computed(() =>
    resolvedImage(
      this.afterTasksFinish() ? "" : this.image(),
      this.imageRepo(),
      this.branch(),
    ),
  );
  protected readonly imageHelp = computed(() =>
    this.afterTasksFinish()
      ? "A scheduled upgrade pulls from the repository above; turn off draining to choose an image for an upgrade started now."
      : "A repository to pull this branch's tag from, or one build: repo/image:tag or repo/image@sha256:…",
  );

  protected readonly branchPlaceholder = computed(() => {
    const u = this.status();
    const running = u["running"] as { branch?: string } | undefined;
    return str(u["channel"]) || running?.branch || "latest";
  });

  protected readonly helpText = computed(() =>
    pickerHelp(this.targetsData(), this.targetsError()),
  );

  private readonly pickerOptions = computed(() => {
    const groups = filterTargets(
      this.targetsData()?.targets ?? [],
      this.query(),
    );
    const retracted = this.retractedRelease()?.running ?? "";
    const discarded = this.discardedRc()?.running ?? "";
    let index = 0;
    return groups.map((g) => ({
      label: g.label,
      options: g.options.map<PickerOption>((t) => ({
        target: t,
        index: index++,
        tag:
          t.name === retracted
            ? "Retracted"
            : t.name === discarded
              ? "Discarded"
              : "",
        note: !t.allowed
          ? "not allowed on this deployment"
          : (CHANNEL_NOTES[t.name] ??
            (t.version && t.version !== t.name ? t.version : "")),
      })),
    }));
  });
  protected readonly pickerGroups = this.pickerOptions;
  private readonly flatOptions = computed(() =>
    this.pickerOptions().flatMap((g) => g.options),
  );
  protected readonly pickerShown = computed(
    () => this.pickerOpen() && this.flatOptions().length > 0,
  );

  protected readonly selectedTargetDisallowed = computed(() => {
    const b = this.branch().trim();
    return Boolean(
      b && this.targetsData()?.targets.some((t) => t.name === b && !t.allowed),
    );
  });

  protected readonly startBlockedReason = computed(() => {
    if (!this.access.can("POST /api/upgrade")) {
      return "Your role cannot start an upgrade.";
    }
    const blocked = str(this.status()["cancelBlockedReason"]);
    if (blocked) return blocked;
    if (this.upgradeBusy()) return "An upgrade is already running.";
    if (!this.branch().trim()) return "Choose a branch to upgrade to.";
    return "";
  });
  protected readonly canStart = computed(
    () => !this.startBlockedReason() && !this.selectedTargetDisallowed(),
  );

  protected readonly breakingVersions = computed(() => {
    const p = this.notes()?.preview;
    if (!p) return [];
    return p.notes.filter((n) => n.breaking).map((n) => n.version);
  });
  protected readonly breakingSentence = computed(() => {
    const named = this.breakingVersions().filter(Boolean);
    if (named.length === 0) return "These release notes list breaking changes.";
    if (named.length === 1) return `${named[0]} lists breaking changes.`;
    return `${named.slice(0, -1).join(", ")} and ${named[named.length - 1]} list breaking changes.`;
  });

  protected readonly checkModeLabel = computed(() => {
    const m = String(this.state.val("upgradeCheckMode", "auto"));
    return UPGRADE_CHECK_LABELS[m] ?? m;
  });
  protected readonly checkModeHelp = computed(() => {
    const m = String(this.state.val("upgradeCheckMode", "auto"));
    return UPGRADE_CHECK_HELP[m] ?? "";
  });

  protected readonly database = computed<DatabaseStatus | null>(() => {
    const d = this.status()["database"] as Record<string, unknown> | undefined;
    if (d && (str(d["driver"]) || str(d["backend"]))) {
      return {
        driver: str(d["driver"]) || str(d["backend"]),
        dsn: str(d["dsn"]),
      };
    }
    return this.dbFallback();
  });
  protected readonly isPostgres = computed(
    () => this.database()?.driver === "postgres",
  );
  // As the classic DatabaseBackend: an externally managed SQLite
  // deployment has nothing to say here, since nobody on it runs setup.sh.
  // Nor does a pane that could not read the backend: it would otherwise
  // offer SQLite's migration commands to a PostgreSQL deployment.
  protected readonly showDatabase = computed(
    () =>
      this.database() !== null &&
      !(this.selfUpgradeDisabled() && !this.isPostgres()),
  );

  ngOnInit(): void {
    void this.load();
  }

  protected async onConfirmDone(accepted: boolean): Promise<void> {
    const req = this.confirmRequest();
    this.confirmRequest.set(null);
    if (accepted && req) await req.run();
  }

  ngOnDestroy(): void {
    if (this.previewTimer) clearTimeout(this.previewTimer);
    this.notesSeq++;
  }

  protected nextRC(running: string): string {
    return nextRCVersion(running);
  }

  protected latestLabel(latest?: string): string {
    return latest ? `latest (${latest})` : "latest";
  }

  protected cap(text: string): string {
    return capitalize(text);
  }

  protected previewSummary(p: ReleaseNotesPreview): string {
    const from = p.from
      ? `This deployment runs ${p.from}.`
      : "This deployment's build does not say which version it is, so every version the target has notes for is listed.";
    return p.version ? `${from} The target is ${p.version}.` : from;
  }

  private async load(): Promise<void> {
    if (this.access.can("GET /api/upgrade")) {
      try {
        const u =
          await this.api.get<Record<string, unknown>>("GET /api/upgrade");
        if (u && typeof u === "object") this.status.set(u);
      } catch {
        // Non-fatal: the pane still names the build GET /api/config carries.
      }
    }
    if (!this.database() && this.access.can("GET /api/database")) {
      try {
        const db =
          await this.api.get<Record<string, unknown>>("GET /api/database");
        const driver = str(db?.["driver"]) || str(db?.["backend"]);
        if (driver) this.dbFallback.set({ driver, dsn: str(db?.["dsn"]) });
      } catch {
        // Non-fatal
      }
    }
    if (
      this.upgradeControllable() &&
      this.access.can("GET /api/upgrade/targets")
    ) {
      try {
        const t = readTargets(
          await this.api.get<unknown>("GET /api/upgrade/targets"),
        );
        this.targetsData.set(t);
        const def = t.default || t.targets[0]?.name || this.branchPlaceholder();
        if (!this.branch()) {
          this.branch.set(def);
          void this.loadReleaseNotes(def);
        }
      } catch (err) {
        const status = (err as { status?: number })?.status;
        // An older daemon has no such route; the box still takes a
        // typed branch.
        this.targetsError.set(status === 404 ? "" : errorText(err));
        if (!this.branch()) this.branch.set(this.branchPlaceholder());
      }
    }
  }

  protected openPicker(): void {
    if (this.upgradeBusy()) return;
    this.pickerOpen.set(true);
  }

  protected togglePicker(): void {
    if (this.upgradeBusy()) return;
    if (this.pickerOpen()) {
      this.pickerOpen.set(false);
      return;
    }
    this.query.set("");
    this.pickerOpen.set(true);
    (this.doc.getElementById("upg-branch") as HTMLInputElement | null)?.focus();
  }

  protected onBranchInput(value: string): void {
    this.branch.set(value);
    this.query.set(value);
    this.activeIndex.set(-1);
    this.pickerOpen.set(true);
    this.schedulePreview(value);
  }

  protected onPickerKey(ev: KeyboardEvent): void {
    const opts = this.flatOptions();
    if (ev.key === "ArrowDown" || ev.key === "ArrowUp") {
      ev.preventDefault();
      if (!this.pickerOpen()) {
        this.pickerOpen.set(true);
        return;
      }
      if (opts.length === 0) return;
      const step = ev.key === "ArrowDown" ? 1 : -1;
      const cur = this.activeIndex();
      const next =
        cur < 0
          ? step > 0
            ? 0
            : opts.length - 1
          : (cur + step + opts.length) % opts.length;
      this.activeIndex.set(next);
      this.doc
        .getElementById(`upg-opt-${next}`)
        ?.scrollIntoView({ block: "nearest" });
    } else if (ev.key === "Enter") {
      const active = opts.find((o) => o.index === this.activeIndex());
      if (this.pickerShown() && active) {
        ev.preventDefault();
        this.chooseOption(active.target);
      } else {
        this.pickerOpen.set(false);
      }
    } else if (ev.key === "Escape") {
      if (this.pickerOpen()) {
        ev.preventDefault();
        ev.stopPropagation();
        this.pickerOpen.set(false);
      }
    } else if (ev.key === "Tab") {
      this.pickerOpen.set(false);
    }
  }

  protected chooseOption(t: UpgradeTarget): void {
    if (!t.allowed) return;
    this.pickBranch(t.name);
  }

  // pickBranch is a branch chosen outright -- from the list, or from a
  // retracted/discarded banner's action: the box takes it and its release
  // notes are read at once.
  protected pickBranch(name: string): void {
    this.branch.set(name);
    this.query.set("");
    this.activeIndex.set(-1);
    this.pickerOpen.set(false);
    if (this.previewTimer) clearTimeout(this.previewTimer);
    void this.loadReleaseNotes(name);
  }

  protected pickRepo(repo: string): void {
    this.image.set(repo === this.imageRepo() ? "" : repo);
  }

  private schedulePreview(value: string): void {
    if (this.previewTimer) clearTimeout(this.previewTimer);
    const target = value.trim();
    if (!target) {
      this.notesSeq++;
      this.notes.set(null);
      return;
    }
    this.previewTimer = setTimeout(
      () => void this.loadReleaseNotes(target),
      PREVIEW_DELAY_MS,
    );
  }

  private async loadReleaseNotes(branch: string): Promise<void> {
    const target = branch.trim();
    if (!target || !this.access.can("GET /api/upgrade/release-notes")) return;
    const seq = ++this.notesSeq;
    this.notes.set({ target, loading: true, error: "", preview: null });
    try {
      const r = await this.api.get<unknown>(
        "GET /api/upgrade/release-notes",
        undefined,
        `branch=${encodeURIComponent(target)}`,
      );
      if (seq !== this.notesSeq) return;
      this.notes.set({
        target,
        loading: false,
        error: "",
        preview: readReleaseNotes(r),
      });
    } catch (err) {
      if (seq !== this.notesSeq) return;
      this.notes.set({
        target,
        loading: false,
        error: errorText(err),
        preview: null,
      });
    }
  }

  // Starting an upgrade restarts grain onto another build, so it asks
  // first, as the classic Upgrade pane's confirm() does.
  protected confirmStartUpgrade(): void {
    if (!this.canStart()) return;
    const branch = this.branch().trim();
    const drain = this.afterTasksFinish();
    const image = drain ? "" : this.image().trim();
    const pull = this.imageRepo() ? this.willPull() : "";
    const breaking = this.breakingVersions().length
      ? " Its release notes list breaking changes."
      : "";
    this.confirmRequest.set({
      title: `Upgrade grain to ${branch}?`,
      message:
        (drain
          ? `Grain waits for running tasks to finish, then restarts onto ${branch}.`
          : `Grain restarts onto ${branch} now, interrupting agent runs in flight.`) +
        (pull ? ` It pulls ${pull}.` : "") +
        breaking,
      accept: drain ? "Schedule upgrade" : "Upgrade now",
      destructive: !drain,
      run: () => this.startUpgrade(branch, drain, image),
    });
  }

  private async startUpgrade(
    branch: string,
    afterTasksFinish: boolean,
    image: string,
  ): Promise<void> {
    const body: Record<string, unknown> = { branch, afterTasksFinish };
    if (image) body["image"] = image;
    try {
      const res = await this.api.post<Record<string, unknown>>(
        "POST /api/upgrade",
        undefined,
        body,
      );
      if (res && typeof res === "object" && !Array.isArray(res)) {
        this.status.set(res);
      }
      if (afterTasksFinish && !this.scheduledUpgradeBranch()) {
        this.localScheduled.set(branch);
      } else if (!afterTasksFinish && !this.activeUpgradeState()) {
        this.localActive.set("starting");
      }
      this.snackbar.show(
        afterTasksFinish
          ? `Scheduled upgrade to ${branch}`
          : `Started upgrade to ${branch}`,
      );
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async cancelScheduledUpgrade(): Promise<void> {
    try {
      await this.api.delete("DELETE /api/upgrade/scheduled");
      this.localScheduled.set("");
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
      this.status.update((u) => ({ ...u, phase: "", cancellable: false }));
      this.localActive.set("");
      this.snackbar.show("Cancelled active upgrade");
    } catch (err) {
      this.state.recordError(err);
    }
  }
}

function str(v: unknown): string {
  return typeof v === "string" ? v : "";
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
