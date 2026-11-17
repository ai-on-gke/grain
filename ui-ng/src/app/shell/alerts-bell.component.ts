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
  ElementRef,
  computed,
  effect,
  inject,
  signal,
  untracked,
  viewChild,
} from "@angular/core";
import { RouterLink } from "@angular/router";
import { worstSeverity } from "./alerts";
import { AlertsPanelComponent } from "./alerts-panel.component";
import { AlertsService } from "./alerts.service";

// AlertsBellComponent is the bell in the top bar -- on a phone too, where
// the top bar is the bar that stays on screen -- and the popover it opens:
// every standing alert (alerts.ts) in one list, as the classic UI's
// AlertsButton.jsx has it. The bell carries the count, toned by the worst
// alert, and is quiet (no badge) when there are none. A new critical alert
// is also said to a screen reader, politely: it is the one kind that needs
// somebody to act, and a badge changing colour says nothing to somebody
// who cannot see it.
@Component({
  selector: "g-alerts-bell",
  imports: [RouterLink, AlertsPanelComponent],
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: {
    class: "alerts-bell",
    "(document:click)": "onDocClick($event)",
    "(document:keydown.escape)": "onEscape($event)",
  },
  styles: [
    `
      :host {
        position: relative;
        display: inline-flex;
        flex: none;
      }
      .alerts-bell-btn {
        position: relative;
      }
      .alerts-bell-btn[data-severity="critical"] {
        color: var(--md-sys-color-error);
      }
      .alerts-bell-btn[data-severity="warning"] {
        color: var(--md-sys-color-warning);
      }
      .alerts-bell-btn[data-severity="info"] {
        color: var(--md-sys-color-primary);
      }
      .alerts-badge {
        position: absolute;
        top: 3px;
        right: 1px;
        min-width: 16px;
        height: 16px;
        padding: 0 4px;
        box-sizing: border-box;
        border-radius: var(--md-sys-shape-corner-full);
        background: var(--md-sys-color-primary);
        color: var(--md-sys-color-on-primary);
        font: 600 10px/16px var(--grain-font-body);
        text-align: center;
        pointer-events: none;
      }
      .alerts-bell-btn[data-severity="critical"] .alerts-badge {
        background: var(--md-sys-color-error);
        color: var(--md-sys-color-on-error);
      }
      .alerts-bell-btn[data-severity="warning"] .alerts-badge {
        background: var(--md-sys-color-warning-container);
        color: var(--md-sys-color-on-warning-container);
        box-shadow: inset 0 0 0 1px var(--md-sys-color-warning);
      }
      .alerts-pop {
        position: absolute;
        z-index: 60;
        right: 0;
        top: calc(100% + 6px);
        width: 440px;
        max-width: calc(100vw - 32px);
        max-height: 70vh;
        box-sizing: border-box;
        display: flex;
        flex-direction: column;
        border: 1px solid var(--md-sys-color-outline-variant);
        border-radius: var(--md-sys-shape-corner-medium);
        background: var(--md-sys-color-surface);
        box-shadow: var(--md-sys-elevation-dialog);
      }
      :host-context([data-theme="dark"]) .alerts-pop {
        background: var(--md-sys-color-surface-container);
      }
      .alerts-pop-head {
        display: flex;
        align-items: baseline;
        justify-content: space-between;
        gap: 8px;
        padding: 12px 16px 8px;
        font: 600 14px/20px var(--grain-font-body);
      }
      .alerts-pop-body {
        overflow-y: auto;
        padding: 0 12px;
        min-height: 0;
      }
      .alerts-pop-foot {
        display: flex;
        justify-content: flex-end;
        padding: 8px 12px 10px;
      }
      .alerts-pop-foot a {
        text-decoration: none;
        display: inline-flex;
        align-items: center;
      }
      .visually-hidden {
        position: absolute;
        width: 1px;
        height: 1px;
        overflow: hidden;
        clip: rect(0 0 0 0);
        white-space: nowrap;
      }
      @media (max-width: 599.95px) {
        .alerts-pop {
          position: fixed;
          left: 12px;
          right: 12px;
          top: 64px;
          width: auto;
          max-width: none;
        }
      }
    `,
  ],
  template: `
    <button
      #bell
      type="button"
      class="icon-btn alerts-bell-btn"
      data-testid="alerts-button"
      aria-haspopup="dialog"
      aria-controls="alerts-pop"
      [attr.data-severity]="worst() || 'none'"
      [attr.aria-label]="label()"
      [attr.aria-expanded]="open()"
      [attr.title]="label()"
      (click)="toggle($event)"
    >
      <svg width="20" height="20" viewBox="0 0 24 24" aria-hidden="true">
        <path
          d="M12 22c1.1 0 2-.9 2-2h-4c0 1.1.89 2 2 2zm6-6v-5c0-3.07-1.64-5.64-4.5-6.32V4c0-.83-.67-1.5-1.5-1.5s-1.5.67-1.5 1.5v.68C7.63 5.36 6 7.92 6 11v5l-2 2v1h16v-1l-2-2z"
          fill="currentColor"
        />
      </svg>
      @if (count() > 0) {
        <span class="alerts-badge tabular" data-testid="alerts-count">{{
          count() > 99 ? "99+" : count()
        }}</span>
      }
    </button>
    <!-- aria-live alone rather than role="status": this is not the page's
      status, only an announcement. -->
    <span
      class="visually-hidden"
      aria-live="polite"
      aria-atomic="true"
      data-testid="alerts-announcer"
      >{{ announcement() }}</span
    >
    @if (open()) {
      <div
        class="alerts-pop"
        id="alerts-pop"
        role="dialog"
        aria-label="Alerts"
        data-testid="alerts-panel"
      >
        <div class="alerts-pop-head">
          <span>Alerts</span>
          <span class="muted tabular" style="font-weight: 400; font-size: 12px"
            >{{ count() }} active</span
          >
        </div>
        <div class="alerts-pop-body">
          <g-alerts-panel [alerts]="alerts()" (navigated)="open.set(false)" />
        </div>
        <!-- Fleet → Health lists these same alerts beside what the popover
          has no room for, so it is offered whether or not anything is
          alerting. -->
        <div class="alerts-pop-foot">
          <a
            class="btn-text"
            routerLink="/fleet"
            [queryParams]="{ tab: 'health' }"
            data-testid="alerts-see-all"
            (click)="open.set(false)"
            >See all on Fleet → Health</a
          >
        </div>
      </div>
    }
  `,
})
export class AlertsBellComponent {
  private readonly service = inject(AlertsService);
  private readonly host = inject(ElementRef<HTMLElement>);
  private readonly bell = viewChild<ElementRef<HTMLButtonElement>>("bell");

  protected readonly open = signal(false);
  protected readonly alerts = this.service.alerts;
  protected readonly count = computed(() => this.alerts().length);
  protected readonly worst = computed(() => worstSeverity(this.alerts()));
  protected readonly label = computed(() => {
    const n = this.count();
    if (n === 0) return "Alerts: none";
    return `Alerts: ${n} active${this.worst() === "critical" ? ", critical" : ""}`;
  });
  protected readonly announcement = signal("");
  private seenCritical: Set<string> | null = null;

  constructor() {
    inject(DestroyRef).onDestroy(this.service.attach());
    // The newest critical alert's title, the first poll that carries it.
    // Alerts already there when the page loads are not news; the bell's
    // own label already names them.
    effect(() => {
      const critical = this.alerts().filter((a) => a.severity === "critical");
      untracked(() => {
        const ids = new Set(critical.map((a) => a.id));
        if (this.seenCritical === null) {
          this.seenCritical = ids;
          return;
        }
        const fresh = critical.filter((a) => !this.seenCritical!.has(a.id));
        this.seenCritical = ids;
        if (fresh.length > 0) {
          this.announcement.set(
            `Critical alert: ${fresh.map((a) => a.title).join("; ")}`,
          );
        }
      });
    });
  }

  protected toggle(ev: Event): void {
    ev.stopPropagation();
    this.open.update((v) => !v);
  }

  // A click outside closes the popover -- but not one inside a dialog the
  // panel opened over it (a release's or a force merge's confirm).
  protected onDocClick(ev: Event): void {
    if (!this.open()) return;
    const t = ev.target as HTMLElement | null;
    if (!t || !t.isConnected) return;
    if (this.host.nativeElement.contains(t)) return;
    if (t.closest(".modal-backdrop")) return;
    this.open.set(false);
  }

  protected onEscape(ev: Event): void {
    if (!this.open() || ev.defaultPrevented) return;
    if (this.host.nativeElement.querySelector(".modal-backdrop")) return;
    ev.preventDefault();
    this.open.set(false);
    this.bell()?.nativeElement.focus();
  }
}
