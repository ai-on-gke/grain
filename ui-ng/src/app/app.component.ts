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
  computed,
  inject,
} from "@angular/core";
import { RouterLink, RouterLinkActive, RouterOutlet } from "@angular/router";
import { InboxStore } from "./inbox/inbox.store";
import { AccessService } from "./services/access.service";
import { ThemeService } from "./services/theme.service";
import { ViewFilter } from "./services/view-filter.service";

// Destinations not yet moved to this app open in the classic UI: plain
// links to the classic paths, which the daemon serves beside /next/.
interface Destination {
  label: string;
  href: string;
  icon: string;
}

// Material-style 24px outline paths, drawn for this app.
const ICONS: Record<string, string> = {
  inbox:
    "M19 3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2V5a2 2 0 0 0-2-2zm0 12h-4a3 3 0 0 1-6 0H5V5h14v10z",
  tasks:
    "M3 5h2v2H3zm4 0h14v2H7zM3 11h2v2H3zm4 0h14v2H7zm-4 6h2v2H3zm4 0h14v2H7z",
  sessions:
    "M4 4h16a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H8l-4 4V6a2 2 0 0 1 2-2zm0 2v11.17L7.17 16H20V6H4z",
  ci: "M9 16.17 4.83 12l-1.42 1.41L9 19 21 7l-1.41-1.41z",
  processes:
    "M12 2a10 10 0 1 0 10 10A10 10 0 0 0 12 2zm0 18a8 8 0 1 1 8-8 8 8 0 0 1-8 8zm.5-13H11v6l5.25 3.15.75-1.23-4.5-2.67z",
  settings:
    "M19.14 12.94a7.07 7.07 0 0 0 0-1.88l2.03-1.58-2-3.46-2.39.96a7.03 7.03 0 0 0-1.63-.94L14.8 3.5h-4l-.36 2.54c-.59.24-1.13.56-1.63.94l-2.39-.96-2 3.46 2.03 1.58a7.07 7.07 0 0 0 0 1.88l-2.03 1.58 2 3.46 2.39-.96c.5.38 1.04.7 1.63.94l.36 2.54h4l.36-2.54c.59-.24 1.13-.56 1.63-.94l2.39.96 2-3.46zM12.8 15.5a3.5 3.5 0 1 1 3.5-3.5 3.5 3.5 0 0 1-3.5 3.5z",
  moon: "M12 3a9 9 0 1 0 9 9 7 7 0 0 1-9-9z",
  sun: "M12 7a5 5 0 1 0 5 5 5 5 0 0 0-5-5zM11 1h2v3h-2zm0 19h2v3h-2zM3.5 4.9l1.4-1.4 2.1 2.1-1.4 1.4zm13 13 1.4-1.4 2.1 2.1-1.4 1.4zM1 11h3v2H1zm19 0h3v2h-3zM4.9 20.5l-1.4-1.4 2.1-2.1 1.4 1.4zm13-13-1.4-1.4 2.1-2.1 1.4 1.4z",
  search:
    "M15.5 14h-.79l-.28-.27a6.5 6.5 0 1 0-.7.7l.27.28v.79l5 4.99L20.49 19zm-6 0A4.5 4.5 0 1 1 14 9.5 4.5 4.5 0 0 1 9.5 14z",
  mark: "M12 2c-3 3-5 6-5 10a5 5 0 0 0 10 0c0-4-2-7-5-10zm0 15a3 3 0 0 1-3-3c0-2 1-4 3-6 2 2 3 4 3 6a3 3 0 0 1-3 3z",
};

@Component({
  selector: "g-root",
  imports: [RouterOutlet, RouterLink, RouterLinkActive],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="app-shell">
      <nav class="nav-rail" aria-label="Primary destinations">
        <a
          class="brand-mark-btn"
          routerLink="/inbox"
          aria-label="Grain home — Inbox"
        >
          <svg width="28" height="28" viewBox="0 0 24 24" aria-hidden="true">
            <path [attr.d]="icons['mark']" fill="currentColor" />
          </svg>
        </a>
        <span class="rail-divider" aria-hidden="true"></span>
        <a
          class="rail-item"
          routerLink="/inbox"
          routerLinkActive
          #inboxActive="routerLinkActive"
          [attr.aria-current]="inboxActive.isActive ? 'page' : null"
          data-testid="rail-inbox"
        >
          <span class="rail-pill">
            <svg width="20" height="20" viewBox="0 0 24 24" aria-hidden="true">
              <path [attr.d]="icons['inbox']" fill="currentColor" />
            </svg>
            @if (inboxCount() > 0) {
              <span class="rail-badge" data-testid="rail-inbox-count">{{
                inboxCount()
              }}</span>
            }
          </span>
          <span class="rail-label">Inbox</span>
        </a>
        @for (d of classic; track d.href) {
          <a class="rail-item" [href]="d.href">
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
      </nav>

      <header class="top-bar">
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
          <span class="app-wordmark">Grain</span>
          <span
            class="next-chip"
            title="The new UI, built beside the classic one"
            >Next</span
          >
        </div>
        <div class="top-actions">
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
              [title]="'Signed in with role ' + access.roleLabel()"
            >
              {{ access.roleLabel() }}
            </span>
          }
        </div>
      </header>

      <main class="main-stage" id="main">
        <router-outlet />
      </main>

      <footer class="bottom-bar">
        <span class="muted"
          >grain{{ store.version() ? " " + store.version() : "" }}</span
        >
        <a class="btn-text bottom-link" href="/">Classic UI</a>
      </footer>

      <nav class="compact-nav" aria-label="Primary destinations (compact)">
        <a
          class="compact-nav-btn"
          routerLink="/inbox"
          routerLinkActive
          #inboxCompact="routerLinkActive"
          [attr.aria-current]="inboxCompact.isActive ? 'page' : null"
        >
          <span class="compact-nav-pill">
            <svg width="20" height="20" viewBox="0 0 24 24" aria-hidden="true">
              <path [attr.d]="icons['inbox']" fill="currentColor" />
            </svg>
          </span>
          Inbox
        </a>
        @for (d of classic.slice(0, 3); track d.href) {
          <a class="compact-nav-btn" [href]="d.href">
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
      </nav>
    </div>
  `,
})
export class AppComponent {
  protected readonly theme = inject(ThemeService);
  protected readonly access = inject(AccessService);
  protected readonly filter = inject(ViewFilter);
  protected readonly store = inject(InboxStore);
  protected readonly icons = ICONS;
  protected readonly inboxCount = computed(() => this.store.entries().length);
  protected readonly classic: Destination[] = [
    { label: "Tasks", href: "/tasks", icon: "tasks" },
    { label: "Sessions", href: "/sessions", icon: "sessions" },
    { label: "CI", href: "/ci", icon: "ci" },
    { label: "Processes", href: "/processes", icon: "processes" },
    { label: "Settings", href: "/settings", icon: "settings" },
  ];
}
