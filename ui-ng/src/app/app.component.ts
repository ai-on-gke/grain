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
  ElementRef,
  NgZone,
  OnDestroy,
  OnInit,
  computed,
  inject,
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
import { InboxStore } from "./inbox/inbox.store";
import { AccessService } from "./services/access.service";
import { KeyboardService } from "./services/keyboard.service";
import { ThemeService } from "./services/theme.service";
import { ViewFilter } from "./services/view-filter.service";
import { TipDirective } from "./shared/tip.directive";
import {
  CLASSIC_NEW_TASK,
  DESTINATIONS,
  type Destination,
  SETTINGS,
  hrefFor,
} from "./shell/nav";
import { CreateTaskService } from "./create/create-task.service";
import { NewTaskSheetComponent } from "./create/new-task-sheet.component";
import { NextUpComponent } from "./shell/next-up.component";
import { SnackbarComponent } from "./shell/snackbar.component";

// Material-style 24px outline paths, drawn for this app.
const ICONS: Record<string, string> = {
  inbox:
    "M19 3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2V5a2 2 0 0 0-2-2zm0 12h-4a3 3 0 0 1-6 0H5V5h14v10z",
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
  fleet:
    "M4 4h16v6H4zm2 2v2h2V6zm-2 8h16v6H4zm2 2v2h2v-2zM10 6v2h8V6zm0 10v2h8v-2z",
  settings:
    "M19.14 12.94a7.07 7.07 0 0 0 0-1.88l2.03-1.58-2-3.46-2.39.96a7.03 7.03 0 0 0-1.63-.94L14.8 3.5h-4l-.36 2.54c-.59.24-1.13.56-1.63.94l-2.39-.96-2 3.46 2.03 1.58a7.07 7.07 0 0 0 0 1.88l-2.03 1.58 2 3.46 2.39-.96c.5.38 1.04.7 1.63.94l.36 2.54h4l.36-2.54c.59-.24 1.13-.56 1.63-.94l2.39.96 2-3.46zM12.8 15.5a3.5 3.5 0 1 1 3.5-3.5 3.5 3.5 0 0 1-3.5 3.5z",
  add: "M11 5h2v6h6v2h-6v6h-2v-6H5v-2h6z",
  caret: "M7 10l5 5 5-5z",
  more: "M6 10a2 2 0 1 0 2 2 2 2 0 0 0-2-2zm6 0a2 2 0 1 0 2 2 2 2 0 0 0-2-2zm6 0a2 2 0 1 0 2 2 2 2 0 0 0-2-2z",
  moon: "M12 3a9 9 0 1 0 9 9 7 7 0 0 1-9-9z",
  sun: "M12 7a5 5 0 1 0 5 5 5 5 0 0 0-5-5zM11 1h2v3h-2zm0 19h2v3h-2zM3.5 4.9l1.4-1.4 2.1 2.1-1.4 1.4zm13 13 1.4-1.4 2.1 2.1-1.4 1.4zM1 11h3v2H1zm19 0h3v2h-3zM4.9 20.5l-1.4-1.4 2.1-2.1 1.4 1.4zm13-13-1.4-1.4 2.1-2.1 1.4 1.4z",
  search:
    "M15.5 14h-.79l-.28-.27a6.5 6.5 0 1 0-.7.7l.27.28v.79l5 4.99L20.49 19zm-6 0A4.5 4.5 0 1 1 14 9.5 4.5 4.5 0 0 1 9.5 14z",
  mark: "M12 2c-3 3-5 6-5 10a5 5 0 0 0 10 0c0-4-2-7-5-10zm0 15a3 3 0 0 1-3-3c0-2 1-4 3-6 2 2 3 4 3 6a3 3 0 0 1-3 3z",
};

type CreateItem =
  | { kind: "task"; label: string; tip: string }
  | {
      kind: "route";
      label: string;
      tip: string;
      route: string;
      query?: Record<string, unknown>;
    }
  | { kind: "href"; label: string; tip: string; href: string };

@Component({
  selector: "g-root",
  imports: [
    NgTemplateOutlet,
    RouterOutlet,
    RouterLink,
    RouterLinkActive,
    TipDirective,
    SnackbarComponent,
    NewTaskSheetComponent,
    NextUpComponent,
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
        [attr.inert]="createTask.isOpen() ? '' : null"
      >
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
                      context: { $implicit: item, cls: 'create-menu-item' }
                    "
                  />
                </li>
              }
            </ul>
          }
        </div>
        <span class="rail-divider" aria-hidden="true"></span>
        @for (d of destinations; track d.id; let i = $index) {
          <ng-container
            *ngTemplateOutlet="railItem; context: { $implicit: d, key: i + 1 }"
          />
        }
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
            [gTip]="d.tip + ' (' + key + ')'"
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
            [gTip]="d.tip + ' Opens in the classic UI. (' + key + ')'"
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

      <header class="top-bar" [attr.inert]="createTask.isOpen() ? '' : null">
        <div class="top-brand-cluster">
          <svg
            class="mobile-brand-mark"
            width="24"
            height="24"
            viewBox="0 0 24 24"
            aria-hidden="true"
          >
            <path [attr.d]="icons['mark']" fill="currentColor" />
          </svg>
          <a
            class="app-wordmark"
            routerLink="/inbox"
            aria-label="Grain home: Inbox"
            >Grain</a
          >
          <span
            class="next-chip"
            gTip="The new UI, built beside the classic one."
            >Next</span
          >
        </div>
        <div class="top-actions">
          <g-next-up />
          <label class="search-box">
            <span class="search-icon" aria-hidden="true">
              <svg width="18" height="18" viewBox="0 0 24 24">
                <path [attr.d]="icons['search']" fill="currentColor" />
              </svg>
            </span>
            <input
              class="search-input"
              type="search"
              placeholder="Filter this view"
              aria-label="Filter this view"
              data-testid="view-filter"
              [value]="filter.query()"
              (input)="filter.query.set($any($event.target).value)"
            />
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
          @if (access.roleLabel()) {
            <span
              class="role-chip"
              data-testid="role-chip"
              [gTip]="'Signed in with role ' + access.roleLabel() + '.'"
            >
              {{ access.roleLabel() }}
            </span>
          }
        </div>
      </header>

      <main
        class="main-stage"
        id="main"
        [attr.inert]="createTask.isOpen() ? '' : null"
      >
        <router-outlet />
      </main>

      <footer class="bottom-bar" [attr.inert]="createTask.isOpen() ? '' : null">
        <span class="muted bottom-version" [attr.title]="store.version()"
          >grain{{ store.version() ? " " + store.version() : "" }}</span
        >
        <span class="bottom-actions">
          <button
            type="button"
            class="btn-text bottom-link"
            data-testid="shortcuts-toggle"
            [attr.aria-pressed]="keyboard.enabled()"
            gTip="Single-key shortcuts (c, 1-9, comma, t, and j/k/x in the Inbox). Turn them off if they get in the way of speech input or a screen reader."
            (click)="keyboard.setEnabled(!keyboard.enabled())"
          >
            Shortcuts {{ keyboard.enabled() ? "on" : "off" }}
          </button>
          <a class="btn-text bottom-link" href="/">Classic UI</a>
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
        [attr.inert]="createTask.isOpen() ? '' : null"
      >
        @for (d of phoneDestinations; track d.id) {
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
            @for (d of moreDestinations; track d.id) {
              <li>
                <a class="more-sheet-item" [href]="hrefOf(d)">{{ d.label }}</a>
              </li>
            }
            @for (item of createItems; track item.label) {
              <li>
                <ng-container
                  *ngTemplateOutlet="
                    createEntry;
                    context: { $implicit: item, cls: 'more-sheet-item' }
                  "
                />
              </li>
            }
          </ul>
        </nav>
      }

      <ng-template #createEntry let-item let-cls="cls">
        @switch (item.kind) {
          @case ("task") {
            <button
              type="button"
              [class]="cls"
              data-testid="create-new-task"
              [gTip]="item.tip"
              gTipPlacement="right"
              (click)="openNewTask($event)"
            >
              {{ item.label }}
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
              >{{ item.label }}</a
            >
          }
          @default {
            <a
              [class]="cls"
              [href]="item.href"
              [gTip]="item.tip"
              gTipPlacement="right"
              >{{ item.label }}</a
            >
          }
        }
      </ng-template>

      <!-- Loaded the first time it opens, to keep the shell's bundle small. -->
      @defer (when createTask.isOpen(); prefetch on idle) {
        <g-new-task-sheet />
      } @error {
        @if (createTask.isOpen()) {
          <p class="banner banner-error defer-error" role="alert">
            The New task form could not load.
            <a [href]="classicNewTask">Use the classic form</a>
          </p>
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
  protected readonly store = inject(InboxStore);
  protected readonly keyboard = inject(KeyboardService);
  private readonly router = inject(Router);
  private readonly doc = inject(DOCUMENT);
  private readonly zone = inject(NgZone);

  protected readonly icons = ICONS;
  protected readonly destinations = DESTINATIONS;
  protected readonly settings = SETTINGS;
  protected readonly phoneDestinations = DESTINATIONS.filter((d) => d.phone);
  protected readonly moreDestinations = [
    ...DESTINATIONS.filter((d) => !d.phone),
    SETTINGS,
  ];
  // The create menu (spec D-23). Task from template, Run a suite and
  // New board stay on the classic pages until those milestones land.
  protected readonly createItems: CreateItem[] = [
    {
      kind: "task",
      label: "New task",
      tip: "Describe a piece of work for an agent to do.",
    },
    {
      kind: "route",
      label: "New session",
      route: "/sessions",
      query: { new: 1 },
      tip: "Start a standing conversation with an agent.",
    },
    {
      kind: "href",
      label: "Task from template",
      href: "/processes",
      tip: "File a task from a saved template (classic Processes page).",
    },
    {
      kind: "href",
      label: "Run a suite",
      href: "/processes",
      tip: "Run a set of templates together (classic Processes page).",
    },
    {
      kind: "href",
      label: "New board",
      href: "/boards",
      tip: "A saved view of tasks (classic Boards page).",
    },
  ];
  protected readonly createTask = inject(CreateTaskService);
  protected readonly classicNewTask = CLASSIC_NEW_TASK;
  protected readonly onSessions = signal(false);
  protected readonly inboxCount = computed(() => this.store.decisions().length);
  protected readonly createOpen = signal(false);
  protected readonly createPos = signal({ top: 0, left: 0 });
  protected readonly moreOpen = signal(false);
  protected readonly hrefOf = hrefFor;

  private readonly createBtn =
    viewChild<ElementRef<HTMLButtonElement>>("createBtn");
  private readonly moreBtn =
    viewChild<ElementRef<HTMLButtonElement>>("moreBtn");
  private unregister: (() => void)[] = [];
  private navSub: { unsubscribe(): void } | null = null;
  private readonly onViewportChange = (): void => {
    if (this.createOpen()) this.zone.run(() => this.createOpen.set(false));
  };

  ngOnInit(): void {
    const r = (key: string, description: string, run: () => void) =>
      this.unregister.push(this.keyboard.register({ key, description, run }));
    r("c", "New task", () => this.createTask.open());
    DESTINATIONS.slice(0, 9).forEach((d, i) =>
      r(String(i + 1), `Open ${d.label}`, () => this.open(d)),
    );
    r(",", "Open Settings", () => this.open(SETTINGS));
    r("t", "Switch light and dark", () => this.theme.toggle());
    // The rail's Inbox badge and the footer's version come from the Inbox
    // store, so it runs on every page, not only on the Inbox.
    this.store.start();
    // Menus belong to the page they were opened on.
    this.navSub = this.router.events.subscribe((e) => {
      if (e instanceof NavigationStart) {
        this.createOpen.set(false);
        this.moreOpen.set(false);
      }
      if (e instanceof NavigationEnd) {
        // Pages with a composer where the FAB would sit.
        const url = e.urlAfterRedirects;
        this.onSessions.set(
          url.startsWith("/sessions") || url.startsWith("/task/"),
        );
      }
    });
    // The create menu is placed from its button once; a scrolled rail or
    // a resized window would leave it floating, so either closes it.
    this.zone.runOutsideAngular(() => {
      this.doc.defaultView?.addEventListener("resize", this.onViewportChange);
      this.doc
        .querySelector(".nav-rail")
        ?.addEventListener("scroll", this.onViewportChange);
    });
  }

  ngOnDestroy(): void {
    this.store.stop();
    this.unregister.forEach((u) => u());
    this.navSub?.unsubscribe();
    this.doc.defaultView?.removeEventListener("resize", this.onViewportChange);
    this.doc
      .querySelector(".nav-rail")
      ?.removeEventListener("scroll", this.onViewportChange);
  }

  // openNewTask opens the New task sheet from whatever was clicked, so
  // focus goes back there when it closes.
  protected openNewTask(ev: Event): void {
    const opener =
      this.createOpen() || this.moreOpen()
        ? this.moreOpen()
          ? this.moreBtn()?.nativeElement
          : this.createBtn()?.nativeElement
        : (ev.currentTarget as HTMLElement | null);
    this.closeMenus();
    this.createTask.open({}, opener);
  }

  protected closeMenus(): void {
    this.createOpen.set(false);
    this.moreOpen.set(false);
  }

  // open goes to a destination: a route inside this app, or the classic
  // page with a full navigation.
  open(d: Destination): void {
    if (d.route) void this.router.navigateByUrl(d.route);
    else this.go(d.classic);
  }

  private go(href: string): void {
    this.doc.defaultView?.location.assign(href);
  }

  protected toggleCreate(ev: Event): void {
    ev.stopPropagation();
    this.moreOpen.set(false);
    if (this.createOpen()) {
      this.createOpen.set(false);
      return;
    }
    // The rail scrolls, which would clip a menu positioned inside it, so
    // the menu is fixed and placed beside the button that opened it.
    const r = (ev.currentTarget as HTMLElement).getBoundingClientRect();
    this.createPos.set({
      top: Math.round(r.top),
      left: Math.round(r.right + 8),
    });
    this.createOpen.set(true);
  }

  protected toggleMore(ev: Event): void {
    ev.stopPropagation();
    this.createOpen.set(false);
    this.moreOpen.update((v) => !v);
  }

  // closeCreate and closeMore handle Escape: close, put focus back on
  // the button that opened it, and mark the key handled so the page's
  // own Escape (the Inbox closing its inspector) does not also fire.
  protected onEscape(ev: Event): void {
    if (this.createOpen()) this.closeCreate(ev, true);
    else if (this.moreOpen()) this.closeMore(ev, true);
  }

  protected closeCreate(ev: Event, refocus: boolean): void {
    if (!this.createOpen()) return;
    ev.preventDefault();
    ev.stopPropagation();
    this.createOpen.set(false);
    if (refocus) this.createBtn()?.nativeElement.focus();
  }

  protected closeMore(ev: Event, refocus: boolean): void {
    if (!this.moreOpen()) return;
    ev.preventDefault();
    ev.stopPropagation();
    this.moreOpen.set(false);
    if (refocus) this.moreBtn()?.nativeElement.focus();
  }

  // Tabbing out of an open menu closes it rather than leaving it open
  // behind the focus.
  protected onCreateFocusOut(ev: FocusEvent): void {
    const next = ev.relatedTarget as HTMLElement | null;
    if (next && !next.closest(".rail-create")) this.createOpen.set(false);
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

  // A click anywhere outside an open menu closes it.
  protected onDocumentClick(ev: Event): void {
    const t = ev.target as HTMLElement | null;
    if (this.createOpen() && !t?.closest(".rail-create")) {
      this.createOpen.set(false);
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
