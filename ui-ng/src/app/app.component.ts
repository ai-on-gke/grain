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
  Directive,
  ElementRef,
  Injector,
  NgZone,
  OnDestroy,
  OnInit,
  computed,
  effect,
  inject,
  output,
  runInInjectionContext,
  signal,
  viewChild,
} from "@angular/core";
import {
  NavigationEnd,
  NavigationStart,
  Router,
  RouterLink,
  RouterLinkActive,
  RouterOutlet,
} from "@angular/router";
import { GrainApi } from "./api/grain-api.service";
import { CreateTaskService } from "./create/create-task.service";
import type { NewTaskDraft } from "./create/new-task-form";
import { NewTaskSheetComponent } from "./create/new-task-sheet.component";
import type { InboxStore } from "./inbox/inbox.store";
import { AccessService } from "./services/access.service";
import { HelpService } from "./services/help.service";
import { KeyboardService } from "./services/keyboard.service";
import { NavConfigService } from "./services/nav-config.service";
import { SnackbarService } from "./services/snackbar.service";
import { TaskFeedStore } from "./services/task-feed.service";
import { ThemeService } from "./services/theme.service";
import { ViewFilter } from "./services/view-filter.service";
import { CHLADNI_ROSETTE_PATH, MARK_SHEET_32_URL } from "./shared/brand-mark";
import { TipDirective } from "./shared/tip.directive";
import { AccountPopoverComponent } from "./shell/account-popover.component";
import { CustomizeNavDialogComponent } from "./shell/customize-nav-dialog.component";
import { HelpDialogComponent } from "./shell/help-dialog.component";
import { type Destination, SETTINGS, hrefFor } from "./shell/nav";
import { NextUpComponent } from "./shell/next-up.component";
import { SnackbarComponent } from "./shell/snackbar.component";

// Material-style 24px outline paths, drawn for this app.
const ICONS: Record<string, string> = {
  inbox:
    "M19 3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2V5a2 2 0 0 0-2-2zm0 12h-4a3 3 0 0 1-6 0H5V5h14v10z",
  workstreams:
    "M3 4h6v5H3zm12 0h6v5h-6zm0 11h6v5h-6zM9 5.5h6v2H9zM5 9h2v9.5H5zM7 16.5h8v2H7z",
  tasks:
    "M3 5h2v2H3zm4 0h14v2H7zM3 11h2v2H3zm4 0h14v2H7zm-4 6h2v2H3zm4 0h14v2H7z",
  agents:
    "M12 2a4 4 0 1 1 0 8 4 4 0 0 1 0-8zm0 2a2 2 0 1 0 0 4 2 2 0 0 0 0-4zm0 8c4.42 0 8 1.79 8 4v4H4v-4c0-2.21 3.58-4 8-4zm0 2c-3.31 0-6 1.12-6 2v2h12v-2c0-.88-2.69-2-6-2zM19 3h2v2h-2zm0 4h2v2h-2zM3 3h2v2H3zm0 4h2v2H3z",
  sessions:
    "M20 2H4a2 2 0 0 0-2 2v18l4-4h14a2 2 0 0 0 2-2V4a2 2 0 0 0-2-2zm0 14H5.17L4 17.17V4h16v12z",
  ci: "M9 16.17 4.83 12l-1.42 1.41L9 19 21 7l-1.41-1.41z",
  processes:
    "M12 2a10 10 0 1 0 10 10A10 10 0 0 0 12 2zm0 18a8 8 0 1 1 8-8 8 8 0 0 1-8 8zm.5-13H11v6l5.25 3.15.75-1.23-4.5-2.67z",
  telemetry: "M4 19h16v2H4zM6 10h3v7H6zm5-5h3v12h-3zm5 8h3v4h-3z",
  context:
    "M18 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V4a2 2 0 0 0-2-2zM9 4h2v5l-1-.75L9 9V4zm9 16H6V4h1v9l3-2.25L13 13V4h5v16z",
  fleet:
    "M4 4h16v6H4zm2 2v2h2V6zm-2 8h16v6H4zm2 2v2h2v-2zM10 6v2h8V6zm0 10v2h8v-2z",
  settings:
    "M19.14 12.94a7.07 7.07 0 0 0 0-1.88l2.03-1.58-2-3.46-2.39.96a7.03 7.03 0 0 0-1.63-.94L14.8 3.5h-4l-.36 2.54c-.59.24-1.13.56-1.63.94l-2.39-.96-2 3.46 2.03 1.58a7.07 7.07 0 0 0 0 1.88l-2.03 1.58 2 3.46 2.39-.96c.5.38 1.04.7 1.63.94l.36 2.54h4l.36-2.54c.59-.24 1.13-.56 1.63-.94l2.39.96 2-3.46zM12.8 15.5a3.5 3.5 0 1 1 3.5-3.5 3.5 3.5 0 0 1-3.5 3.5z",
  add: "M11 5h2v6h6v2h-6v6h-2v-6H5v-2h6z",
  addTask:
    "M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h7v-2H6V4h7v5h5v3h2V8l-6-6zm4 13v3h3v2h-3v3h-2v-3h-3v-2h3v-3h2z",
  caret: "M7 10l5 5 5-5z",
  more: "M6 10a2 2 0 1 0 2 2 2 2 0 0 0-2-2zm6 0a2 2 0 1 0 2 2 2 2 0 0 0-2-2zm6 0a2 2 0 1 0 2 2 2 2 0 0 0-2-2z",
  moon: "M12 3a9 9 0 1 0 9 9 7 7 0 0 1-9-9z",
  sun: "M12 7a5 5 0 1 0 5 5 5 5 0 0 0-5-5zM11 1h2v3h-2zm0 19h2v3h-2zM3.5 4.9l1.4-1.4 2.1 2.1-1.4 1.4zm13 13 1.4-1.4 2.1 2.1-1.4 1.4zM1 11h3v2H1zm19 0h3v2h-3zM4.9 20.5l-1.4-1.4 2.1-2.1 1.4 1.4zm13-13-1.4-1.4 2.1-2.1 1.4 1.4z",
  search:
    "M15.5 14h-.79l-.28-.27a6.5 6.5 0 1 0-.7.7l.27.28v.79l5 4.99L20.49 19zm-6 0A4.5 4.5 0 1 1 14 9.5 4.5 4.5 0 0 1 9.5 14z",
  help: "M11 18h2v-2h-2v2zm1-16C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm0 18c-4.41 0-8-3.59-8-8s3.59-8 8-8 8 3.59 8 8-3.59 8-8 8zm0-14a4 4 0 0 0-4 4h2a2 2 0 1 1 4 0c0 2-3 1.75-3 5h2c0-2.25 3-2.5 3-5a4 4 0 0 0-4-4z",
  tune: "M3 17v2h6v-2H3zM3 5v2h10V5H3zm10 16v-2h8v-2h-8v-2h-2v6h2zM7 9v2H3v2h4v2h2V9H7zm14 4v-2H11v2h10zm-6-4h2V7h4V5h-4V3h-2v6z",
};

type CreateItem =
  | {
      kind: "task";
      icon: string;
      label: string;
      sub: string;
      tip: string;
      testid?: string;
      prefill?: Partial<NewTaskDraft>;
    }
  | {
      kind: "route";
      icon: string;
      label: string;
      sub: string;
      tip: string;
      route: string;
      query?: Record<string, unknown>;
    }
  | {
      kind: "href";
      icon: string;
      label: string;
      sub: string;
      tip: string;
      href: string;
    };

@Directive({ selector: "[gDeferError]" })
export class DeferErrorDirective implements OnInit {
  readonly recover = output<void>();

  ngOnInit(): void {
    queueMicrotask(() => this.recover.emit());
  }
}

@Component({
  selector: "g-root",
  imports: [
    NgTemplateOutlet,
    RouterOutlet,
    RouterLink,
    RouterLinkActive,
    TipDirective,
    DeferErrorDirective,
    SnackbarComponent,
    NewTaskSheetComponent,
    NextUpComponent,
    HelpDialogComponent,
    AccountPopoverComponent,
    CustomizeNavDialogComponent,
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: {
    "(document:click)": "onDocumentClick($event)",
    "(document:keydown.escape)": "onEscape($event)",
  },
  template: `
    <div class="app-shell">
      <nav
        class="nav-rail"
        aria-label="Primary destinations"
        [attr.inert]="modalOpen() ? '' : null"
      >
        <a
          routerLink="/inbox"
          class="brand-mark-btn"
          data-testid="brand-mark"
          aria-label="Grain home: Inbox"
          gTip="Grain home: Inbox"
          gTipPlacement="right"
        >
          @if (animateBrandMark()) {
            <span
              class="grain-mark-sheet"
              data-testid="brand-mark-animated"
              aria-hidden="true"
            ></span>
          } @else {
            <svg
              class="brand-mark-svg"
              width="28"
              height="28"
              viewBox="0 0 128 128"
              aria-hidden="true"
            >
              <path
                style="clip-path: circle(44% at 50% 50%)"
                fill="currentColor"
                fill-rule="evenodd"
                [attr.d]="rosettePath"
              />
            </svg>
          }
        </a>

        <button
          type="button"
          class="rail-fab"
          data-testid="rail-new-task"
          aria-label="New task"
          aria-keyshortcuts="c"
          gTip="New task (c): describe the work and Grain files it."
          gTipPlacement="right"
          (click)="openNewTask($event)"
        >
          <svg width="24" height="24" viewBox="0 0 24 24" aria-hidden="true">
            <path [attr.d]="icons['add']" fill="currentColor" />
          </svg>
        </button>

        <div class="rail-create" (focusout)="onCreateFocusOut($event)">
          <button
            #createBtn
            type="button"
            class="rail-new-btn"
            data-testid="rail-create-menu"
            [attr.aria-expanded]="createOpen()"
            aria-controls="create-menu"
            (click)="toggleCreate($event)"
          >
            New
            <svg width="16" height="16" viewBox="0 0 24 24" aria-hidden="true">
              <path [attr.d]="icons['caret']" fill="currentColor" />
            </svg>
          </button>
          @if (createOpen()) {
            <ul
              class="create-menu"
              id="create-menu"
              aria-label="Create"
              data-testid="create-menu"
              [style.top.px]="createPos().top"
              [style.left.px]="createPos().left"
            >
              @for (item of createItems; track item.label) {
                <li>
                  <ng-container
                    *ngTemplateOutlet="
                      createEntry;
                      context: {
                        $implicit: item,
                        cls: 'create-menu-item',
                        rich: true,
                      }
                    "
                  />
                </li>
              }
            </ul>
          }
        </div>

        <span class="rail-divider" aria-hidden="true"></span>

        @for (
          d of navConfig.visibleDestinations();
          track d.id;
          let i = $index
        ) {
          <ng-container
            *ngTemplateOutlet="
              railItem;
              context: { $implicit: d, key: i < 9 ? i + 1 : '' }
            "
          />
        }

        <div class="rail-more-wrap" (focusout)="onRailMoreFocusOut($event)">
          <button
            #railMoreBtn
            type="button"
            class="rail-item rail-more-btn"
            data-testid="rail-more"
            [attr.aria-expanded]="railMoreOpen()"
            aria-controls="rail-more-menu"
            gTip="Hidden destinations and rail customization."
            gTipPlacement="right"
            (click)="toggleRailMore($event)"
          >
            <span class="rail-pill">
              <svg
                width="20"
                height="20"
                viewBox="0 0 24 24"
                aria-hidden="true"
              >
                <path [attr.d]="icons['more']" fill="currentColor" />
              </svg>
              @if (navConfig.hiddenDestinations().length > 0) {
                <span class="rail-badge" data-testid="rail-hidden-count">{{
                  navConfig.hiddenDestinations().length
                }}</span>
              }
            </span>
            <span class="rail-label">More</span>
          </button>

          @if (railMoreOpen()) {
            <ul
              class="create-menu rail-more-menu"
              id="rail-more-menu"
              aria-label="More navigation options"
              data-testid="rail-more-menu"
              [style.top.px]="railMorePos().top"
              [style.left.px]="railMorePos().left"
            >
              @for (d of navConfig.hiddenDestinations(); track d.id) {
                <li>
                  @if (d.route) {
                    <a
                      class="create-menu-item"
                      [routerLink]="d.route"
                      [attr.data-testid]="'rail-hidden-' + d.id"
                      (click)="closeMenus()"
                    >
                      <svg
                        class="cm-icon"
                        width="20"
                        height="20"
                        viewBox="0 0 24 24"
                        aria-hidden="true"
                      >
                        <path [attr.d]="icons[d.icon]" fill="currentColor" />
                      </svg>
                      <span class="cm-copy">
                        <span class="cm-title">{{ d.label }}</span>
                        <span class="cm-sub">{{ d.tip }}</span>
                      </span>
                    </a>
                  } @else {
                    <a
                      class="create-menu-item"
                      [href]="hrefOf(d)"
                      [attr.data-testid]="'rail-hidden-' + d.id"
                    >
                      <svg
                        class="cm-icon"
                        width="20"
                        height="20"
                        viewBox="0 0 24 24"
                        aria-hidden="true"
                      >
                        <path [attr.d]="icons[d.icon]" fill="currentColor" />
                      </svg>
                      <span class="cm-copy">
                        <span class="cm-title">{{ d.label }}</span>
                        <span class="cm-sub">{{ d.tip }}</span>
                      </span>
                    </a>
                  }
                </li>
              }
              <li>
                <button
                  type="button"
                  class="create-menu-item"
                  data-testid="rail-customize-nav"
                  (click)="openCustomizeNav($event)"
                >
                  <svg
                    class="cm-icon"
                    width="20"
                    height="20"
                    viewBox="0 0 24 24"
                    aria-hidden="true"
                  >
                    <path [attr.d]="icons['tune']" fill="currentColor" />
                  </svg>
                  <span class="cm-copy">
                    <span class="cm-title">Customize navigation</span>
                    <span class="cm-sub"
                      >Show, hide, or reorder rail destinations</span
                    >
                  </span>
                </button>
              </li>
            </ul>
          }
        </div>

        <span class="rail-spacer" aria-hidden="true"></span>
        <ng-container
          *ngTemplateOutlet="
            railItem;
            context: { $implicit: settings, key: ',' }
          "
        />
      </nav>

      <ng-template #railItem let-d let-key="key">
        @if (d.route) {
          <a
            class="rail-item"
            [routerLink]="d.route"
            routerLinkActive
            #active="routerLinkActive"
            [attr.aria-current]="active.isActive ? 'page' : null"
            [attr.data-testid]="'rail-' + d.id"
            [gTip]="key ? d.tip + ' (' + key + ')' : d.tip"
            gTipPlacement="right"
          >
            <span class="rail-pill">
              <svg
                width="20"
                height="20"
                viewBox="0 0 24 24"
                aria-hidden="true"
              >
                <path [attr.d]="icons[d.icon]" fill="currentColor" />
              </svg>
              @if (d.id === "inbox" && inboxCount() > 0) {
                <span class="rail-badge" data-testid="rail-inbox-count">{{
                  inboxCount()
                }}</span>
              }
            </span>
            <span class="rail-label">{{ d.label }}</span>
          </a>
        } @else {
          <a
            class="rail-item"
            [href]="d.classic"
            [attr.data-testid]="'rail-' + d.id"
            [gTip]="
              key
                ? d.tip + ' Opens in the classic UI. (' + key + ')'
                : d.tip + ' Opens in the classic UI.'
            "
            gTipPlacement="right"
          >
            <span class="rail-pill">
              <svg
                width="20"
                height="20"
                viewBox="0 0 24 24"
                aria-hidden="true"
              >
                <path [attr.d]="icons[d.icon]" fill="currentColor" />
              </svg>
            </span>
            <span class="rail-label">{{ d.label }}</span>
          </a>
        }
      </ng-template>

      <header class="top-bar" [attr.inert]="modalOpen() ? '' : null">
        <div class="top-brand-cluster">
          <svg
            class="mobile-brand-mark"
            width="24"
            height="24"
            viewBox="0 0 128 128"
            aria-hidden="true"
          >
            <path
              style="clip-path: circle(44% at 50% 50%)"
              fill="currentColor"
              fill-rule="evenodd"
              [attr.d]="rosettePath"
            />
          </svg>
          <a
            class="app-wordmark"
            routerLink="/inbox"
            aria-label="Grain home: Inbox"
            >Grain</a
          >
        </div>
        <div class="top-actions">
          <g-next-up />
          <a
            class="live-pill tabular"
            [class.is-idle]="feed.runningCount() === 0"
            routerLink="/agents"
            data-testid="live-pill"
            [gTip]="livePillTip()"
          >
            <span
              class="live-dot"
              [class.is-idle]="feed.runningCount() === 0"
              aria-hidden="true"
            ></span>
            <span class="live-pill-text"
              >{{ feed.runningCount() }}
              {{ feed.runningCount() === 1 ? "agent" : "agents" }} running</span
            >
          </a>
          <label class="search-box">
            <span class="search-icon" aria-hidden="true">
              <svg width="18" height="18" viewBox="0 0 24 24">
                <path [attr.d]="icons['search']" fill="currentColor" />
              </svg>
            </span>
            <input
              #searchInput
              class="search-input"
              type="search"
              placeholder="Filter this view"
              aria-label="Filter this view"
              data-testid="view-filter"
              [value]="filter.query()"
              (input)="filter.query.set($any($event.target).value)"
            />
            @if (!filter.query()) {
              <span
                class="search-kbd"
                data-testid="search-kbd"
                aria-hidden="true"
                >/</span
              >
            }
          </label>
          <button
            type="button"
            class="icon-btn"
            data-testid="theme-toggle"
            [attr.aria-label]="
              theme.resolved() === 'dark'
                ? 'Switch to light theme'
                : 'Switch to dark theme'
            "
            gTip="Switch between light and dark (t)."
            (click)="theme.toggle()"
          >
            <svg width="20" height="20" viewBox="0 0 24 24" aria-hidden="true">
              <path
                [attr.d]="
                  theme.resolved() === 'dark' ? icons['sun'] : icons['moon']
                "
                fill="currentColor"
              />
            </svg>
          </button>
          <button
            type="button"
            class="icon-btn"
            data-testid="help-trigger"
            aria-label="Help, glossary, and keyboard shortcuts"
            aria-keyshortcuts="?"
            gTip="Help, glossary, and keyboard shortcuts (?)."
            (click)="openHelp($event)"
          >
            <svg width="20" height="20" viewBox="0 0 24 24" aria-hidden="true">
              <path [attr.d]="icons['help']" fill="currentColor" />
            </svg>
          </button>
          <button
            #avatarBtn
            type="button"
            class="avatar-btn"
            data-testid="avatar-btn"
            [attr.aria-expanded]="accountOpen()"
            aria-controls="account-popover"
            [attr.aria-label]="'Account and access: ' + access.identityLabel()"
            aria-keyshortcuts="0"
            [gTip]="avatarTip()"
            (click)="toggleAccount($event)"
          >
            {{ access.initials() }}
          </button>
        </div>
      </header>

      <main class="main-stage" id="main" [attr.inert]="modalOpen() ? '' : null">
        <router-outlet />
      </main>

      <footer class="bottom-bar" [attr.inert]="modalOpen() ? '' : null">
        <span class="bottom-left">
          <span class="muted bottom-version" [attr.title]="version()"
            >grain{{ version() ? " " + version() : "" }}</span
          >
          <span class="bottom-sep" aria-hidden="true">·</span>
          <a
            class="factory-telemetry tabular"
            routerLink="/agents"
            data-testid="factory-telemetry"
          >
            <span
              class="live-dot"
              [class.is-idle]="feed.runningCount() === 0"
              aria-hidden="true"
            ></span>
            <span>{{ feed.runningCount() }} running</span>
            @if (updatedAgoLabel(); as ago) {
              <span class="muted">· Updated {{ ago }}</span>
            }
          </a>
        </span>
        <span class="bottom-actions">
          <button
            type="button"
            class="btn-text bottom-link"
            data-testid="shortcuts-toggle"
            [attr.aria-pressed]="keyboard.enabled()"
            gTip="Single-key shortcuts (c, /, 1-9, comma, 0, t, ?, and j/k/x in the Inbox). Turn them off if they get in the way of speech input or a screen reader."
            (click)="keyboard.setEnabled(!keyboard.enabled())"
          >
            Shortcuts {{ keyboard.enabled() ? "on" : "off" }}
          </button>
        </span>
      </footer>

      <!-- Sessions has a composer where the phone FAB would sit, and is
        itself a place to ask for work (the prototype hides it there too). -->
      @if (!onSessions()) {
        <button
          type="button"
          class="phone-fab"
          data-testid="phone-new-task"
          aria-label="New task"
          (click)="openNewTask($event)"
        >
          <svg width="24" height="24" viewBox="0 0 24 24" aria-hidden="true">
            <path [attr.d]="icons['add']" fill="currentColor" />
          </svg>
        </button>
      }

      <nav
        class="compact-nav"
        aria-label="Primary destinations (compact)"
        [attr.inert]="modalOpen() ? '' : null"
      >
        @for (d of navConfig.phoneDestinations(); track d.id) {
          @if (d.route) {
            <a
              class="compact-nav-btn"
              [routerLink]="d.route"
              routerLinkActive
              #compact="routerLinkActive"
              [attr.aria-current]="compact.isActive ? 'page' : null"
            >
              <span class="compact-nav-pill">
                <svg
                  width="20"
                  height="20"
                  viewBox="0 0 24 24"
                  aria-hidden="true"
                >
                  <path [attr.d]="icons[d.icon]" fill="currentColor" />
                </svg>
              </span>
              {{ d.label }}
            </a>
          } @else {
            <a class="compact-nav-btn" [href]="d.classic">
              <span class="compact-nav-pill">
                <svg
                  width="20"
                  height="20"
                  viewBox="0 0 24 24"
                  aria-hidden="true"
                >
                  <path [attr.d]="icons[d.icon]" fill="currentColor" />
                </svg>
              </span>
              {{ d.label }}
            </a>
          }
        }
        <button
          #moreBtn
          type="button"
          class="compact-nav-btn"
          data-testid="compact-more"
          [attr.aria-expanded]="moreOpen()"
          aria-controls="more-sheet"
          (click)="toggleMore($event)"
        >
          <span class="compact-nav-pill">
            <svg width="20" height="20" viewBox="0 0 24 24" aria-hidden="true">
              <path [attr.d]="icons['more']" fill="currentColor" />
            </svg>
          </span>
          More
        </button>
      </nav>

      @if (moreOpen()) {
        <nav
          class="more-sheet"
          id="more-sheet"
          aria-label="More destinations"
          data-testid="more-sheet"
          (focusout)="onMoreFocusOut($event)"
        >
          <ul class="more-sheet-list">
            @for (d of navConfig.phoneMoreDestinations(); track d.id) {
              <li>
                @if (d.route) {
                  <a
                    class="more-sheet-item"
                    [routerLink]="d.route"
                    (click)="closeMenus()"
                    >{{ d.label }}</a
                  >
                } @else {
                  <a class="more-sheet-item" [href]="hrefOf(d)">{{
                    d.label
                  }}</a>
                }
              </li>
            }
            @for (item of createItems; track item.label) {
              <li>
                <ng-container
                  *ngTemplateOutlet="
                    createEntry;
                    context: {
                      $implicit: item,
                      cls: 'more-sheet-item',
                      rich: false,
                    }
                  "
                />
              </li>
            }
            <li>
              <button
                type="button"
                class="more-sheet-item"
                data-testid="more-customize-nav"
                (click)="openCustomizeNav($event)"
              >
                Customize navigation
              </button>
            </li>
          </ul>
        </nav>
      }

      <ng-template #createEntry let-item let-cls="cls" let-rich="rich">
        @switch (item.kind) {
          @case ("task") {
            <button
              type="button"
              [class]="cls"
              [attr.data-testid]="item.testid ?? 'create-new-task'"
              [gTip]="item.tip"
              gTipPlacement="right"
              (click)="openNewTask($event, item.prefill)"
            >
              @if (rich) {
                <svg
                  class="cm-icon"
                  width="20"
                  height="20"
                  viewBox="0 0 24 24"
                  aria-hidden="true"
                >
                  <path [attr.d]="icons[item.icon]" fill="currentColor" />
                </svg>
                <span class="cm-copy">
                  <span class="cm-title">{{ item.label }}</span>
                  <span class="cm-sub">{{ item.sub }}</span>
                </span>
              } @else {
                {{ item.label }}
              }
            </button>
          }
          @case ("route") {
            <a
              [class]="cls"
              [routerLink]="item.route"
              [queryParams]="item.query"
              [gTip]="item.tip"
              gTipPlacement="right"
              (click)="closeMenus()"
            >
              @if (rich) {
                <svg
                  class="cm-icon"
                  width="20"
                  height="20"
                  viewBox="0 0 24 24"
                  aria-hidden="true"
                >
                  <path [attr.d]="icons[item.icon]" fill="currentColor" />
                </svg>
                <span class="cm-copy">
                  <span class="cm-title">{{ item.label }}</span>
                  <span class="cm-sub">{{ item.sub }}</span>
                </span>
              } @else {
                {{ item.label }}
              }
            </a>
          }
          @default {
            <a
              [class]="cls"
              [href]="item.href"
              [gTip]="item.tip"
              gTipPlacement="right"
            >
              @if (rich) {
                <svg
                  class="cm-icon"
                  width="20"
                  height="20"
                  viewBox="0 0 24 24"
                  aria-hidden="true"
                >
                  <path [attr.d]="icons[item.icon]" fill="currentColor" />
                </svg>
                <span class="cm-copy">
                  <span class="cm-title">{{ item.label }}</span>
                  <span class="cm-sub">{{ item.sub }}</span>
                </span>
              } @else {
                {{ item.label }}
              }
            </a>
          }
        }
      </ng-template>

      <!-- Loaded on demand so shell modals and popovers stay out of the initial bundle. -->
      @defer (when createTask.isOpen(); prefetch on idle) {
        <g-new-task-sheet />
      } @error {
        @if (createTask.isOpen()) {
          <span gDeferError (recover)="onDeferError('create')"></span>
        }
      }

      @defer (when help.isOpen()) {
        @if (help.isOpen()) {
          <g-help-dialog />
        }
      } @error {
        @if (help.isOpen()) {
          <span gDeferError (recover)="onDeferError('help')"></span>
        }
      }

      @defer (when accountOpen()) {
        @if (accountOpen()) {
          <g-account-popover
            (closed)="onAccountClosed($event)"
            (customizeNav)="openCustomizeFromAccount()"
          />
        }
      } @error {
        @if (accountOpen()) {
          <span gDeferError (recover)="onDeferError('account')"></span>
        }
      }

      @defer (when customizeNavOpen()) {
        @if (customizeNavOpen()) {
          <g-customize-nav-dialog (closed)="onCustomizeNavClosed()" />
        }
      } @error {
        @if (customizeNavOpen()) {
          <span gDeferError (recover)="onDeferError('customize')"></span>
        }
      }

      <g-snackbar />
    </div>
  `,
})
export class AppComponent implements OnInit, OnDestroy {
  protected readonly theme = inject(ThemeService);
  protected readonly access = inject(AccessService);
  protected readonly filter = inject(ViewFilter);
  protected readonly feed = inject(TaskFeedStore);
  protected readonly navConfig = inject(NavConfigService);
  protected readonly help = inject(HelpService);
  protected readonly keyboard = inject(KeyboardService);
  private readonly snackbar = inject(SnackbarService);
  private readonly inboxRef = signal<InboxStore | null>(null);
  private destroyed = false;
  private readonly api = inject(GrainApi);
  private readonly router = inject(Router);
  private readonly doc = inject(DOCUMENT);
  private readonly zone = inject(NgZone);
  private readonly injector = inject(Injector);

  protected readonly icons = ICONS;
  protected readonly rosettePath = CHLADNI_ROSETTE_PATH;
  protected readonly settings = SETTINGS;
  protected readonly hrefOf = hrefFor;

  // Rich 2-line create menu (spec D-23).
  protected readonly createItems: CreateItem[] = [
    {
      kind: "task",
      icon: "addTask",
      label: "New task",
      sub: "Describe work for an agent to run",
      tip: "Describe a piece of work for an agent to do.",
    },
    {
      kind: "task",
      icon: "workstreams",
      label: "Start a workstream",
      sub: "Break a goal into connected tasks",
      testid: "create-start-workstream",
      tip: "State a goal; an agent plans it and proposes tasks into one stream.",
      prefill: { workstream: true, origin: "" },
    },
    {
      kind: "route",
      icon: "sessions",
      label: "New session",
      sub: "Start a standing chat with an agent",
      route: "/sessions",
      query: { new: 1 },
      tip: "Start a standing conversation with an agent.",
    },
    {
      kind: "route",
      icon: "processes",
      label: "Task from template",
      sub: "File from a saved process template",
      route: "/processes/templates",
      query: { launch: 1 },
      tip: "File a task from a saved deployment or repository template.",
    },
    {
      kind: "route",
      icon: "ci",
      label: "Run a suite",
      sub: "Launch a multi-pass template suite",
      route: "/processes/suites",
      query: { run: 1 },
      tip: "Run a set of templates together against a repository and branch.",
    },
    {
      kind: "route",
      icon: "processes",
      label: "New schedule",
      sub: "Automate recurring tasks or triggers",
      route: "/processes/schedules",
      query: { new: 1 },
      tip: "Create a recurring schedule or event trigger.",
    },
    {
      kind: "route",
      icon: "tasks",
      label: "New board",
      sub: "Create a saved task view",
      route: "/tasks",
      query: { newBoard: 1 },
      tip: "Save a filtered view of tasks as a reusable board.",
    },
  ];

  protected readonly createTask = inject(CreateTaskService);
  protected readonly onSessions = signal(false);
  protected readonly version = computed(() => this.inboxRef()?.version() ?? "");
  protected readonly inboxCount = computed(
    () => this.inboxRef()?.decisions().length ?? 0,
  );
  protected readonly createOpen = signal(false);
  protected readonly createPos = signal({ top: 0, left: 0 });
  protected readonly railMoreOpen = signal(false);
  protected readonly railMorePos = signal({ top: 0, left: 0 });
  protected readonly moreOpen = signal(false);
  protected readonly accountOpen = signal(false);
  protected readonly customizeNavOpen = signal(false);
  protected readonly prefersReducedMotion = signal(false);
  protected readonly spriteFailed = signal(false);

  protected readonly modalOpen = computed(
    () =>
      this.createTask.isOpen() || this.help.isOpen() || this.customizeNavOpen(),
  );

  protected readonly animateBrandMark = computed(
    () =>
      this.feed.runningCount() > 0 &&
      !this.prefersReducedMotion() &&
      !this.spriteFailed(),
  );

  protected readonly livePillTip = computed(() => {
    const n = this.feed.runningCount();
    return n > 0
      ? `${n} active agent ${n === 1 ? "run" : "runs"}. Open Live Agents.`
      : "No agents running right now. Open Live Agents.";
  });

  protected readonly avatarTip = computed(() => {
    const role = this.access.roleLabel();
    const who = this.access.identityLabel();
    return role ? `${who} · ${role} (0)` : `Account and access: ${who} (0)`;
  });

  protected readonly updatedAgoLabel = computed(() => {
    const ts = this.feed.lastUpdatedAt();
    if (!ts) return "";
    const sec = Math.max(0, Math.round((this.feed.now() - ts) / 1000));
    if (sec < 60) return `${sec}s ago`;
    const min = Math.floor(sec / 60);
    return `${min}m ago`;
  });

  private readonly createBtn =
    viewChild<ElementRef<HTMLButtonElement>>("createBtn");
  private readonly railMoreBtn =
    viewChild<ElementRef<HTMLButtonElement>>("railMoreBtn");
  private readonly moreBtn =
    viewChild<ElementRef<HTMLButtonElement>>("moreBtn");
  private readonly searchInput =
    viewChild<ElementRef<HTMLInputElement>>("searchInput");
  private readonly avatarBtn =
    viewChild<ElementRef<HTMLButtonElement>>("avatarBtn");

  private customizeOpener: HTMLElement | null = null;
  private unregister: (() => void)[] = [];
  private navSub: { unsubscribe(): void } | null = null;
  private motionMql: MediaQueryList | null = null;
  private readonly onMotionChange = (e: MediaQueryListEvent): void => {
    this.zone.run(() => this.prefersReducedMotion.set(e.matches));
  };
  private readonly onViewportChange = (): void => {
    if (this.createOpen() || this.railMoreOpen()) {
      this.zone.run(() => {
        this.createOpen.set(false);
        this.railMoreOpen.set(false);
      });
    }
  };

  ngOnInit(): void {
    this.help.attached.set(true);

    const win = this.doc.defaultView;
    if (win && typeof win.matchMedia === "function") {
      this.motionMql = win.matchMedia("(prefers-reduced-motion: reduce)");
      this.prefersReducedMotion.set(this.motionMql.matches);
      this.motionMql.addEventListener?.("change", this.onMotionChange);
    }
    this.probeBrandSprite();

    if (typeof this.api.whoami === "function") {
      void this.api
        .whoami()
        .then((w) => {
          if (w) this.access.setWhoami(w);
        })
        .catch(() => undefined);
    }

    const r = (key: string, description: string, run: () => void) =>
      this.unregister.push(this.keyboard.register({ key, description, run }));
    r("c", "New task", () => {
      this.closeMenus();
      this.createTask.open();
    });
    r("/", "Filter this view", () => {
      const input = this.searchInput()?.nativeElement;
      input?.focus();
      input?.select?.();
    });
    r("?", "Open Help, Glossary & Shortcuts", () => {
      this.closeMenus();
      this.help.open("about");
    });
    r("0", "Account and access", () => {
      if (this.accountOpen()) {
        this.onAccountClosed(true);
      } else {
        this.closeMenus();
        this.accountOpen.set(true);
      }
    });
    r(",", "Open Settings", () => this.open(SETTINGS));
    r("t", "Switch light and dark", () => this.theme.toggle());

    // Keep number keys 1..9 synchronized with the user's visible rail order.
    effect(
      (onCleanup) => {
        const visible = this.navConfig.visibleDestinations().slice(0, 9);
        const unsubs = visible.map((d, i) =>
          this.keyboard.register({
            key: String(i + 1),
            description: `Open ${d.label}`,
            run: () => this.open(d),
          }),
        );
        onCleanup(() => unsubs.forEach((u) => u()));
      },
      { injector: this.injector },
    );

    // Start the shared task feed immediately for NextUp, live-pill, and
    // footer telemetry; load InboxStore lazily so inbox-classifier stays out
    // of the initial shell bundle.
    this.feed.start(false);
    void import("./inbox/inbox.store").then(({ InboxStore }) => {
      if (this.destroyed) return;
      runInInjectionContext(this.injector, () => {
        const s = inject(InboxStore);
        this.inboxRef.set(s);
        s.start();
      });
    });

    this.navSub = this.router.events.subscribe((e) => {
      if (e instanceof NavigationStart) {
        this.closeMenus();
        this.accountOpen.set(false);
      }
      if (e instanceof NavigationEnd) {
        const url = e.urlAfterRedirects;
        this.onSessions.set(
          url.startsWith("/sessions") || url.startsWith("/task/"),
        );
      }
    });

    this.zone.runOutsideAngular(() => {
      this.doc.defaultView?.addEventListener("resize", this.onViewportChange);
      this.doc
        .querySelector(".nav-rail")
        ?.addEventListener("scroll", this.onViewportChange);
    });
  }

  ngOnDestroy(): void {
    this.destroyed = true;
    this.help.attached.set(false);
    this.feed.stop(false);
    this.inboxRef()?.stop();
    this.unregister.forEach((u) => u());
    this.navSub?.unsubscribe();
    this.motionMql?.removeEventListener?.("change", this.onMotionChange);
    this.doc.defaultView?.removeEventListener("resize", this.onViewportChange);
    this.doc
      .querySelector(".nav-rail")
      ?.removeEventListener("scroll", this.onViewportChange);
  }

  private probeBrandSprite(): void {
    const win = this.doc.defaultView as (Window & typeof globalThis) | null;
    if (!win || typeof win.Image !== "function") return;
    this.zone.runOutsideAngular(() => {
      const img = new win.Image();
      img.onerror = () => {
        this.zone.run(() => this.spriteFailed.set(true));
      };
      img.src = MARK_SHEET_32_URL;
    });
  }

  protected openNewTask(ev: Event, prefill: Partial<NewTaskDraft> = {}): void {
    const opener = this.moreOpen()
      ? this.moreBtn()?.nativeElement
      : this.createOpen()
        ? this.createBtn()?.nativeElement
        : (ev.currentTarget as HTMLElement | null);
    this.closeMenus();
    this.createTask.open(prefill, opener);
  }

  protected openHelp(ev: Event): void {
    const opener = ev.currentTarget as HTMLElement | null;
    this.closeMenus();
    this.help.open("about", opener);
  }

  protected toggleAccount(ev: Event): void {
    ev.stopPropagation();
    const next = !this.accountOpen();
    this.closeMenus();
    this.accountOpen.set(next);
  }

  protected onAccountClosed(restoreFocus: boolean): void {
    this.accountOpen.set(false);
    if (restoreFocus) {
      this.avatarBtn()?.nativeElement.focus();
    }
  }

  protected onDeferError(
    which: "create" | "help" | "account" | "customize",
  ): void {
    if (which === "create") {
      this.createTask.close();
      this.snackbar.show(
        "The New task form could not load. Reload the page and try again.",
      );
    } else if (which === "help") {
      this.help.close();
      this.snackbar.show("Help could not load. Reload the page and try again.");
    } else if (which === "account") {
      this.onAccountClosed(true);
      this.snackbar.show(
        "Account menu could not load. Reload the page and try again.",
      );
    } else {
      this.onCustomizeNavClosed();
      this.snackbar.show(
        "Navigation settings could not load. Reload the page and try again.",
      );
    }
  }

  protected openCustomizeFromAccount(): void {
    this.accountOpen.set(false);
    this.customizeOpener = this.avatarBtn()?.nativeElement ?? null;
    this.customizeNavOpen.set(true);
  }

  protected openCustomizeNav(ev: Event): void {
    this.customizeOpener = this.moreOpen()
      ? (this.moreBtn()?.nativeElement ?? null)
      : this.railMoreOpen()
        ? (this.railMoreBtn()?.nativeElement ?? null)
        : (ev.currentTarget as HTMLElement | null);
    this.closeMenus();
    this.customizeNavOpen.set(true);
  }

  protected onCustomizeNavClosed(): void {
    this.customizeNavOpen.set(false);
    const target = this.customizeOpener;
    this.customizeOpener = null;
    if (target && typeof target.focus === "function") {
      setTimeout(() => target.focus());
    }
  }

  protected closeMenus(): void {
    this.createOpen.set(false);
    this.railMoreOpen.set(false);
    this.moreOpen.set(false);
    this.accountOpen.set(false);
  }

  open(d: Destination): void {
    if (d.route) void this.router.navigateByUrl(d.route);
    else if (d.classic) this.doc.defaultView?.location.assign(d.classic);
  }

  protected toggleCreate(ev: Event): void {
    ev.stopPropagation();
    const next = !this.createOpen();
    this.closeMenus();
    if (!next) return;
    const r = (ev.currentTarget as HTMLElement).getBoundingClientRect();
    const winH = this.doc.defaultView?.innerHeight ?? 900;
    const estH = Math.min(this.createItems.length * 58 + 16, winH - 24);
    this.createPos.set({
      top: Math.max(12, Math.min(Math.round(r.top), winH - estH - 12)),
      left: Math.round(r.right + 8),
    });
    this.createOpen.set(true);
    this.clampFloatingMenu("create-menu", (top) =>
      this.createPos.update((p) => ({ ...p, top })),
    );
  }

  protected toggleRailMore(ev: Event): void {
    ev.stopPropagation();
    const next = !this.railMoreOpen();
    this.closeMenus();
    if (!next) return;
    const r = (ev.currentTarget as HTMLElement).getBoundingClientRect();
    const winH = this.doc.defaultView?.innerHeight ?? 900;
    const count = this.navConfig.hiddenDestinations().length + 1;
    const estH = Math.min(count * 58 + 16, winH - 24);
    this.railMorePos.set({
      top: Math.max(12, Math.min(Math.round(r.top - 40), winH - estH - 12)),
      left: Math.round(r.right + 8),
    });
    this.railMoreOpen.set(true);
    this.clampFloatingMenu("rail-more-menu", (top) =>
      this.railMorePos.update((p) => ({ ...p, top })),
    );
  }

  private clampFloatingMenu(id: string, applyTop: (top: number) => void): void {
    queueMicrotask(() => {
      const menu = this.doc.getElementById(id);
      const winH = this.doc.defaultView?.innerHeight ?? 0;
      if (!menu || !winH) return;
      const rect = menu.getBoundingClientRect();
      if (rect.height > 0 && rect.bottom > winH - 12) {
        applyTop(Math.max(12, Math.round(winH - rect.height - 12)));
      }
    });
  }

  protected toggleMore(ev: Event): void {
    ev.stopPropagation();
    const next = !this.moreOpen();
    this.closeMenus();
    this.moreOpen.set(next);
  }

  protected onEscape(ev: Event): void {
    if (ev.defaultPrevented) return;
    if (this.modalOpen() && !this.doc.querySelector('[aria-modal="true"]')) {
      ev.preventDefault();
      this.createTask.close();
      this.help.close();
      this.onCustomizeNavClosed();
      return;
    }
    if (this.createOpen()) this.closeCreate(ev, true);
    else if (this.railMoreOpen()) this.closeRailMore(ev, true);
    else if (this.moreOpen()) this.closeMore(ev, true);
  }

  protected closeCreate(ev: Event, refocus: boolean): void {
    if (!this.createOpen()) return;
    ev.preventDefault();
    ev.stopPropagation();
    this.createOpen.set(false);
    if (refocus) this.createBtn()?.nativeElement.focus();
  }

  protected closeRailMore(ev: Event, refocus: boolean): void {
    if (!this.railMoreOpen()) return;
    ev.preventDefault();
    ev.stopPropagation();
    this.railMoreOpen.set(false);
    if (refocus) this.railMoreBtn()?.nativeElement.focus();
  }

  protected closeMore(ev: Event, refocus: boolean): void {
    if (!this.moreOpen()) return;
    ev.preventDefault();
    ev.stopPropagation();
    this.moreOpen.set(false);
    if (refocus) this.moreBtn()?.nativeElement.focus();
  }

  protected onCreateFocusOut(ev: FocusEvent): void {
    const next = ev.relatedTarget as HTMLElement | null;
    if (next && !next.closest(".rail-create")) this.createOpen.set(false);
  }

  protected onRailMoreFocusOut(ev: FocusEvent): void {
    const next = ev.relatedTarget as HTMLElement | null;
    if (next && !next.closest(".rail-more-wrap")) this.railMoreOpen.set(false);
  }

  protected onMoreFocusOut(ev: FocusEvent): void {
    const next = ev.relatedTarget as HTMLElement | null;
    if (
      next &&
      !next.closest(".more-sheet") &&
      next !== this.moreBtn()?.nativeElement
    ) {
      this.moreOpen.set(false);
    }
  }

  protected onDocumentClick(ev: Event): void {
    const t = ev.target as HTMLElement | null;
    if (this.createOpen() && !t?.closest(".rail-create")) {
      this.createOpen.set(false);
    }
    if (this.railMoreOpen() && !t?.closest(".rail-more-wrap")) {
      this.railMoreOpen.set(false);
    }
    if (
      this.moreOpen() &&
      !t?.closest(".more-sheet") &&
      !t?.closest('[data-testid="compact-more"]')
    ) {
      this.moreOpen.set(false);
    }
  }
}
