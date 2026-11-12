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
  AfterViewInit,
  ChangeDetectionStrategy,
  Component,
  ElementRef,
  NgZone,
  OnDestroy,
  OnInit,
  ViewEncapsulation,
  computed,
  inject,
  output,
  signal,
  viewChild,
} from "@angular/core";
import { GrainApi } from "../api/grain-api.service";
import { AccessService } from "../services/access.service";
import { KeyboardService } from "../services/keyboard.service";
import { SnackbarService } from "../services/snackbar.service";

@Component({
  selector: "g-account-popover",
  encapsulation: ViewEncapsulation.None,
  styleUrl: "./shell-overlays.css",
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: {
    "(document:click)": "onDocClick($event)",
  },
  template: `
    <div
      #popEl
      class="account-popover"
      id="account-popover"
      role="dialog"
      aria-label="Account and access"
      data-testid="account-popover"
    >
      <div class="account-head">
        <span class="account-avatar-lg" aria-hidden="true">{{
          access.initials()
        }}</span>
        <div class="account-identity">
          <span class="account-name" data-testid="account-identity">{{
            access.identityLabel()
          }}</span>
          @if (secondaryId(); as sub) {
            <span class="account-id tabular">{{ sub }}</span>
          }
          @if (delegatedNote(); as del) {
            <span class="account-id">{{ del }}</span>
          }
        </div>
      </div>

      <div class="account-section">
        <div class="account-role-row">
          <span class="muted">Role</span>
          <span class="status-pill neutral" data-testid="account-role">{{
            access.roleLabel() || "operator"
          }}</span>
        </div>
        <div class="muted" data-testid="account-scopes-summary">
          {{ access.scopesSummary() }}
        </div>
        @if (!access.isFullAccess() && access.scopes().length > 0) {
          <div class="account-scopes" data-testid="account-scope-chips">
            @for (s of shownScopes(); track s) {
              <span class="status-pill neutral">{{ s }}</span>
            }
            @if (extraScopesCount() > 0) {
              <span class="muted">+{{ extraScopesCount() }} more</span>
            }
          </div>
        }
      </div>

      <div class="account-section">
        <div class="account-role-row">
          <span>Single-key shortcuts</span>
          <button
            #firstBtn
            type="button"
            class="g-switch"
            role="switch"
            data-testid="account-shortcuts-switch"
            [attr.aria-checked]="keyboard.enabled()"
            aria-label="Single-key shortcuts"
            (click)="keyboard.setEnabled(!keyboard.enabled())"
          ></button>
        </div>
      </div>

      <div class="account-actions">
        <button
          type="button"
          class="account-menu-btn"
          data-testid="account-customize-nav"
          (click)="customizeNav.emit()"
        >
          <span>Customize navigation…</span>
        </button>
        <button
          type="button"
          class="account-menu-btn is-danger"
          data-testid="account-sign-out"
          [disabled]="signingOut()"
          (click)="signOut()"
        >
          <span>{{ signingOut() ? "Signing out…" : "Sign out" }}</span>
        </button>
      </div>
    </div>
  `,
})
export class AccountPopoverComponent
  implements OnInit, AfterViewInit, OnDestroy
{
  protected readonly access = inject(AccessService);
  protected readonly keyboard = inject(KeyboardService);
  private readonly api = inject(GrainApi);
  private readonly snackbar = inject(SnackbarService);
  private readonly doc = inject(DOCUMENT);
  private readonly zone = inject(NgZone);

  readonly closed = output<boolean>();
  readonly customizeNav = output<void>();

  protected readonly signingOut = signal(false);
  private readonly popEl = viewChild<ElementRef<HTMLElement>>("popEl");
  private readonly firstBtn =
    viewChild<ElementRef<HTMLButtonElement>>("firstBtn");

  private readonly onCaptureKeydown = (ev: KeyboardEvent): void => {
    if (ev.key === "Escape") {
      this.zone.run(() => this.onEscape(ev));
    } else if (ev.key === "Tab") {
      this.zone.run(() => this.onTab(ev));
    }
  };

  protected readonly secondaryId = computed(() => {
    const w = this.access.whoami();
    if (!w) return "";
    const parts: string[] = [];
    if (w.actor?.id && w.actor.id !== this.access.identityLabel()) {
      parts.push(w.actor.id);
    }
    if (w.source) {
      parts.push(w.verified ? `${w.source} · verified` : w.source);
    }
    return parts.join(" · ");
  });

  protected readonly delegatedNote = computed(() => {
    const w = this.access.whoami();
    if (!w) return "";
    if (w.onBehalfOf?.label) {
      return `Acting for ${w.onBehalfOf.label}${w.via ? ` via ${w.via}` : ""}`;
    }
    return w.via ? `Via ${w.via}` : "";
  });

  protected readonly shownScopes = computed(() =>
    this.access.scopes().slice(0, 6),
  );
  protected readonly extraScopesCount = computed(() =>
    Math.max(0, this.access.scopes().length - 6),
  );

  ngOnInit(): void {
    if (!this.access.whoami() && typeof this.api.whoami === "function") {
      void this.api
        .whoami()
        .then((w) => {
          if (w) this.access.setWhoami(w);
        })
        .catch(() => undefined);
    }
  }

  ngAfterViewInit(): void {
    this.zone.runOutsideAngular(() => {
      this.doc.addEventListener("keydown", this.onCaptureKeydown, true);
    });
    this.firstBtn()?.nativeElement.focus();
  }

  ngOnDestroy(): void {
    this.doc.removeEventListener("keydown", this.onCaptureKeydown, true);
  }

  protected async signOut(): Promise<void> {
    if (this.signingOut()) return;
    this.signingOut.set(true);
    try {
      const res =
        typeof this.api.logout === "function"
          ? await this.api.logout()
          : await this.api.post<{ redirect?: string }>("POST /api/auth/logout");
      this.api.clearConditionalCache?.();
      const dest =
        typeof res?.redirect === "string" && /^\/(?!\/)/.test(res.redirect)
          ? res.redirect
          : "/next/";
      this.navigate(dest);
    } catch {
      this.signingOut.set(false);
      this.snackbar.show("Could not sign out. Try again.");
    }
  }

  // Exposed as a method so specs can spy on navigation without reloading jsdom.
  navigate(url: string): void {
    this.doc.defaultView?.location.assign(url);
  }

  protected onDocClick(ev: Event): void {
    const t = ev.target as HTMLElement | null;
    if (
      !t?.closest("#account-popover") &&
      !t?.closest('[data-testid="avatar-btn"]')
    ) {
      this.closed.emit(false);
    }
  }

  protected onEscape(ev: Event): void {
    if (ev.defaultPrevented) return;
    ev.preventDefault();
    ev.stopPropagation();
    this.closed.emit(true);
  }

  protected onTab(ev: KeyboardEvent): void {
    const root = this.popEl()?.nativeElement;
    if (!root) return;
    const focusables = Array.from(
      root.querySelectorAll<HTMLElement>(
        'button:not([disabled]), a[href], input:not([disabled]), [tabindex]:not([tabindex="-1"])',
      ),
    );
    if (!focusables.length) return;
    const first = focusables[0];
    const last = focusables[focusables.length - 1];
    const active = this.doc.activeElement;
    if (ev.shiftKey && (active === first || !root.contains(active))) {
      ev.preventDefault();
      last.focus();
    } else if (!ev.shiftKey && (active === last || !root.contains(active))) {
      ev.preventDefault();
      first.focus();
    }
  }
}
