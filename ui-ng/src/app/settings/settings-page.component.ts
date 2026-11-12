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
  OnDestroy,
  OnInit,
  ViewEncapsulation,
  computed,
  inject,
  input,
  signal,
} from "@angular/core";
import { ActivatedRoute, Router, RouterLink } from "@angular/router";
import type { Subscription } from "rxjs";
import { AccessService } from "../services/access.service";
import { CustomizeNavDialogComponent } from "../shell/customize-nav-dialog.component";
import { SettingsCodeComponent } from "./settings-code.component";
import { SettingsComputeComponent } from "./settings-compute.component";
import { SettingsInstanceComponent } from "./settings-instance.component";
import {
  CONFIG_REPO_SECTION_PATHS,
  DEFAULT_SETTINGS_SECTION,
  type SettingsGroupId,
  type SettingsSectionId,
  SETTINGS_GROUPS,
  filterSettingsGroups,
  resolveSettingsSection,
  sectionMeta,
} from "./settings-sections";
import { SettingsSecurityComponent } from "./settings-security.component";
import { SettingsStateService } from "./settings-state.service";
import { SettingsWorkComponent } from "./settings-work.component";

@Component({
  selector: "g-settings-page",
  imports: [
    RouterLink,
    CustomizeNavDialogComponent,
    SettingsComputeComponent,
    SettingsCodeComponent,
    SettingsWorkComponent,
    SettingsSecurityComponent,
    SettingsInstanceComponent,
  ],
  providers: [SettingsStateService],
  encapsulation: ViewEncapsulation.None,
  styleUrls: [
    "../shared/work-surface.css",
    "../shared/pill-select.css",
    "./settings-layout.css",
    "./settings-forms.css",
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <section
      class="view-panel is-active"
      aria-labelledby="settings-stage-heading"
    >
      <div
        class="settings-shell"
        [class.is-mobile-list]="!hasExplicitSection()"
        [class.is-mobile-detail]="hasExplicitSection()"
        data-testid="settings-shell"
      >
        <aside
          class="settings-drawer"
          aria-label="Settings sections"
          data-testid="settings-drawer"
        >
          <div class="settings-drawer-top">
            <div class="settings-drawer-title-row">
              <h2 class="settings-drawer-title">Settings</h2>
              @if (state.configRepoOnly()) {
                <span
                  class="status-pill neutral"
                  data-testid="settings-readonly-chip"
                  title="Config-managed sections are controlled via StateRepo pull requests"
                  >Read-only</span
                >
              }
            </div>
            <label class="settings-filter-wrap">
              <svg
                class="settings-filter-icon"
                width="16"
                height="16"
                viewBox="0 0 24 24"
                aria-hidden="true"
              >
                <path
                  d="M15.5 14h-.79l-.28-.27a6.5 6.5 0 1 0-.7.7l.27.28v.79l5 4.99L20.49 19zm-6 0A4.5 4.5 0 1 1 14 9.5 4.5 4.5 0 0 1 9.5 14z"
                  fill="currentColor"
                />
              </svg>
              <input
                class="settings-filter-input"
                type="search"
                placeholder="Filter settings…"
                aria-label="Filter settings"
                data-testid="settings-filter-input"
                [value]="filterQuery()"
                (input)="filterQuery.set($any($event.target).value)"
              />
            </label>
          </div>

          <nav class="settings-drawer-scroll" aria-label="Settings categories">
            @if (filteredGroups().length === 0) {
              <p class="s-card-desc" data-testid="settings-filter-empty">
                No settings sections match "{{ filterQuery() }}".
              </p>
            }
            @for (grp of filteredGroups(); track grp.id) {
              <div
                class="settings-group"
                [attr.data-testid]="'settings-group-' + grp.id"
              >
                <div class="settings-group-header">
                  <svg
                    class="settings-group-icon"
                    width="18"
                    height="18"
                    viewBox="0 0 24 24"
                    aria-hidden="true"
                  >
                    <path [attr.d]="grp.iconPath" fill="currentColor" />
                  </svg>
                  <span>{{ grp.label }}</span>
                </div>
                <ul class="settings-group-list">
                  @for (sec of grp.sections; track sec.id) {
                    <li>
                      <a
                        class="settings-nav-item"
                        [routerLink]="['/settings', sec.id]"
                        [class.is-active]="activeSection() === sec.id"
                        [attr.aria-current]="
                          activeSection() === sec.id ? 'page' : null
                        "
                        [attr.data-testid]="'settings-nav-' + sec.id"
                        (click)="selectSection(sec.id, $event)"
                      >
                        <span>{{ sec.label }}</span>
                      </a>
                    </li>
                  }
                </ul>
              </div>
            }
          </nav>
        </aside>

        <div class="settings-stage" data-testid="settings-stage">
          <div class="settings-stage-scroll">
            <div class="settings-stage-inner">
              <a
                class="btn-text settings-mobile-back"
                routerLink="/settings"
                data-testid="settings-mobile-back"
                >← All settings</a
              >

              <header class="settings-section-head">
                <div>
                  <h1
                    class="settings-section-title"
                    id="settings-stage-heading"
                    data-testid="settings-section-heading"
                  >
                    {{ activeMeta().label }}
                  </h1>
                  <p class="settings-section-sub">
                    {{ activeMeta().subtitle }}
                  </p>
                </div>
                @if (state.sectionLocked(activeSection())) {
                  <span
                    class="status-pill neutral"
                    data-testid="section-readonly-pill"
                    >Managed in config repo</span
                  >
                }
              </header>

              @if (state.sectionLocked(activeSection())) {
                <div
                  class="s-banner readonly"
                  data-testid="settings-config-repo-banner"
                >
                  <span>
                    This section is read-only because deployment settings are
                    managed in the config repository (<code>{{
                      configPathFor(activeSection())
                    }}</code
                    >). Secrets and API keys remain editable.
                  </span>
                  <a
                    class="btn-tonal s-btn-sm"
                    style="text-decoration: none"
                    [routerLink]="['/settings', 'config-repo']"
                    [queryParams]="{ path: configPathFor(activeSection()) }"
                    data-testid="open-in-config-repo-link"
                    >View in Config repo</a
                  >
                </div>
              }

              @if (state.error(); as err) {
                <div
                  class="s-banner error"
                  role="alert"
                  data-testid="settings-error-banner"
                >
                  <span>{{ err }}</span>
                  <button
                    type="button"
                    class="btn-text s-btn-sm"
                    (click)="state.error.set(null)"
                  >
                    Dismiss
                  </button>
                </div>
              }

              @if (state.planPreview(); as rows) {
                <div class="s-plan-panel" data-testid="settings-plan-preview">
                  <div class="s-card-head">
                    <h2 class="s-card-title">
                      Planned changes ({{ rows.length }})
                    </h2>
                    <button
                      type="button"
                      class="btn-text s-btn-sm"
                      (click)="state.planPreview.set(null)"
                    >
                      Close preview
                    </button>
                  </div>
                  <div class="s-table-wrap">
                    <table class="s-table" aria-label="Planned setting changes">
                      <thead>
                        <tr>
                          <th>Setting</th>
                          <th>Current</th>
                          <th>Planned</th>
                          <th>Normalized</th>
                        </tr>
                      </thead>
                      <tbody>
                        @for (r of rows; track r.field) {
                          <tr data-testid="settings-plan-row">
                            <td>{{ r.label }}</td>
                            <td class="tabular">{{ r.was }}</td>
                            <td class="tabular">{{ r.becomes }}</td>
                            <td class="tabular">
                              {{
                                r.typed
                                  ? "from " + r.typed
                                  : r.changed
                                    ? "changed"
                                    : "unchanged"
                              }}
                            </td>
                          </tr>
                        }
                      </tbody>
                    </table>
                  </div>
                </div>
              }

              @if (ready()) {
                @switch (activeGroup()) {
                  @case ("compute") {
                    <g-settings-compute [section]="activeSection()" />
                  }
                  @case ("code") {
                    <g-settings-code
                      [section]="activeSection()"
                      [initialFilePath]="queryFilePath()"
                    />
                  }
                  @case ("work") {
                    <g-settings-work [section]="activeSection()" />
                  }
                  @case ("security") {
                    <g-settings-security [section]="activeSection()" />
                  }
                  @case ("instance") {
                    <g-settings-instance
                      [section]="activeSection()"
                      (customizeNav)="customizeNavOpen.set(true)"
                    />
                  }
                }
              }
            </div>
          </div>

          @if (state.isDirty()) {
            <div
              class="settings-save-bar"
              role="region"
              aria-label="Unsaved settings changes"
              data-testid="settings-save-bar"
            >
              <span class="tabular">
                {{ state.dirtyCount() }} unsaved
                {{ state.dirtyCount() === 1 ? "change" : "changes" }}
              </span>
              <div class="settings-save-actions">
                <button
                  type="button"
                  class="btn-text"
                  data-testid="settings-discard-btn"
                  [disabled]="state.saving()"
                  (click)="state.discard()"
                >
                  Discard
                </button>
                @if (
                  state.hasSettingsDraft() &&
                  access.can("POST /api/settings/plan")
                ) {
                  <button
                    type="button"
                    class="btn-tonal"
                    data-testid="settings-plan-btn"
                    [disabled]="state.planning() || state.saving()"
                    (click)="state.previewPlan()"
                  >
                    {{
                      state.planning() ? "Planning…" : "Plan / Preview changes"
                    }}
                  </button>
                }
                <button
                  type="button"
                  class="btn-primary"
                  data-testid="settings-save-btn"
                  [disabled]="state.saving() || !canSave()"
                  (click)="state.saveChanges()"
                >
                  {{ state.saving() ? "Saving…" : "Save changes" }}
                </button>
              </div>
            </div>
          }
        </div>
      </div>

      @if (customizeNavOpen()) {
        <g-customize-nav-dialog (closed)="customizeNavOpen.set(false)" />
      }
    </section>
  `,
})
export class SettingsPageComponent implements OnInit, OnDestroy {
  protected readonly access = inject(AccessService);
  protected readonly state = inject(SettingsStateService);
  private readonly route = inject(ActivatedRoute);
  private readonly router = inject(Router);

  readonly section = input<string | undefined>(undefined);
  readonly path = input<string | undefined>(undefined);

  protected readonly filterQuery = signal("");
  protected readonly ready = signal(false);
  protected readonly customizeNavOpen = signal(false);
  private readonly routeSection = signal<string | null>(null);
  private readonly routeFilePath = signal<string>("");

  private subs: Subscription[] = [];

  protected readonly hasExplicitSection = computed(() => {
    const raw = this.section() ?? this.routeSection();
    return Boolean(raw && resolveSettingsSection(raw));
  });

  protected readonly activeSection = computed<SettingsSectionId>(() => {
    const raw = this.section() ?? this.routeSection();
    return resolveSettingsSection(raw) ?? DEFAULT_SETTINGS_SECTION;
  });

  protected readonly queryFilePath = computed(
    () => this.path() ?? this.routeFilePath(),
  );

  protected readonly activeMeta = computed(() =>
    sectionMeta(this.activeSection()),
  );

  protected readonly activeGroup = computed<SettingsGroupId>(() => {
    const sec = this.activeSection();
    const grp = SETTINGS_GROUPS.find((g) =>
      g.sections.some((s) => s.id === sec),
    );
    return grp?.id ?? "compute";
  });

  protected readonly filteredGroups = computed(() =>
    filterSettingsGroups(this.filterQuery()),
  );

  ngOnInit(): void {
    this.subs.push(
      this.route.paramMap.subscribe((pm) => {
        this.routeSection.set(pm.get("section"));
      }),
      this.route.queryParamMap.subscribe((qm) => {
        this.routeFilePath.set(qm.get("path") ?? "");
      }),
    );
    void this.state.loadCore().finally(() => {
      this.ready.set(true);
    });
  }

  ngOnDestroy(): void {
    this.subs.forEach((s) => s.unsubscribe());
  }

  protected selectSection(id: SettingsSectionId, ev: Event): void {
    ev.preventDefault();
    this.routeSection.set(id);
    void this.router.navigate(["/settings", id]);
  }

  protected configPathFor(id: SettingsSectionId): string {
    return CONFIG_REPO_SECTION_PATHS[id]?.[0] ?? "tables/grain_config.json";
  }

  protected canSave(): boolean {
    if (
      this.state.hasSettingsDraft() &&
      !this.access.can("POST /api/settings")
    ) {
      return false;
    }
    if (
      this.state.hasDeployDraft() &&
      !this.access.can("PUT /api/deployment")
    ) {
      return false;
    }
    return true;
  }
}
