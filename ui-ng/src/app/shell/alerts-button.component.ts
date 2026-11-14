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
  ElementRef,
  computed,
  inject,
  signal,
  viewChild,
} from "@angular/core";
import { RouterLink } from "@angular/router";
import { ShellStatusStore } from "../services/shell-status.store";
import { TaskFeedStore } from "../services/task-feed.service";
import { StatusIconComponent } from "../shared/status-icon.component";
import { TipDirective } from "../shared/tip.directive";
import {
  type AlertSeverity,
  type ShellAlert,
  collectAlerts,
  formatMoment,
  preferredZone,
  worstSeverity,
} from "./alerts";

const SEVERITY_LABEL: Record<AlertSeverity, string> = {
  critical: "Critical",
  warning: "Warning",
  info: "Info",
};

// Status-icon shape per severity, so a row never says it by colour alone.
const SEVERITY_KIND: Record<AlertSeverity, string> = {
  critical: "error",
  warning: "warning",
  info: "neutral",
};

export const BELL_PATH =
  "M12 22a2 2 0 0 0 2-2h-4a2 2 0 0 0 2 2zm6-6v-5c0-3.07-1.64-5.64-4.5-6.32V4a1.5 1.5 0 0 0-3 0v.68C7.63 5.36 6 7.92 6 11v5l-2 2v1h16v-1l-2-2zm-2 1H8v-6c0-2.48 1.51-4.5 4-4.5s4 2.02 4 4.5v6z";

// AlertsButtonComponent is the top bar's bell: how many standing alerts
// the deployment has (shell/alerts.ts, read by ShellStatusStore), coloured
// by the worst of them, and a panel listing each with what it costs and a
// link to the page that fixes it. "See all in Fleet" goes where the
// classic UI's System → Health went (app.routes.ts sends that address to
// Fleet). A row leaves on the next poll after its cause does.
@Component({
  selector: "g-alerts-button",
  imports: [RouterLink, StatusIconComponent, TipDirective],
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: {
    class: "alerts-host",
    "(document:click)": "onDocClick($event)",
    "(keydown.escape)": "onOwnEscape($event)",
    "(document:keydown.escape)": "onEscape($event)",
  },
  template: `
    <button
      #bellBtn
      type="button"
      class="icon-btn alerts-btn"
      data-testid="alerts-bell"
      aria-haspopup="dialog"
      aria-controls="alerts-panel"
      [attr.aria-expanded]="open()"
      [attr.aria-label]="label()"
      [gTip]="label()"
      (click)="toggle()"
    >
      <svg width="20" height="20" viewBox="0 0 24 24" aria-hidden="true">
        <path [attr.d]="bell" fill="currentColor" />
      </svg>
      @if (alerts().length > 0) {
        <span
          class="alerts-badge tabular"
          [attr.data-severity]="worst()"
          data-testid="alerts-count"
          aria-hidden="true"
          >{{ alerts().length }}</span
        >
      }
    </button>
    <span class="sr-only" role="status">{{ announcement() }}</span>
    @if (open()) {
      <div
        #panelEl
        class="alerts-panel"
        id="alerts-panel"
        role="dialog"
        aria-labelledby="alerts-panel-title"
        data-testid="alerts-panel"
        tabindex="-1"
      >
        <div class="alerts-head">
          <h2 class="alerts-title" id="alerts-panel-title">Alerts</h2>
          <span class="alerts-head-count tabular">{{ countLabel() }}</span>
        </div>
        @if (alerts().length === 0) {
          <p class="alerts-empty" data-testid="alerts-empty">
            <g-status-icon kind="healthy" />
            Nothing needs attention: no pauses, sync failures or setup steps
            left.
          </p>
        } @else {
          <ul class="alerts-list">
            @for (a of alerts(); track a.id) {
              <li
                class="alerts-row"
                [attr.data-severity]="a.severity"
                [attr.data-alert-id]="a.id"
                data-testid="alert-row"
              >
                <div class="alerts-row-head">
                  <span class="alerts-sev" [attr.data-severity]="a.severity">
                    <g-status-icon [kind]="kindOf(a)" />
                    {{ severityLabel(a) }}
                  </span>
                  <span class="alerts-row-title">{{ a.title }}</span>
                </div>
                <p class="alerts-row-sentence">{{ a.sentence }}</p>
                @if (a.impact) {
                  <p class="alerts-row-impact">{{ a.impact }}</p>
                }
                <div class="alerts-row-foot">
                  <span class="alerts-row-meta"
                    >{{ a.scope }}
                    @if (sinceOf(a); as s) {
                      · since {{ s }}
                    }
                  </span>
                  @if (a.link; as l) {
                    <a
                      class="alerts-row-link"
                      [routerLink]="l.route"
                      [queryParams]="l.query ?? null"
                      (click)="close(false)"
                      >{{ l.label }}</a
                    >
                  }
                </div>
              </li>
            }
          </ul>
        }
        <div class="alerts-foot">
          <a
            class="alerts-see-all"
            routerLink="/fleet"
            data-testid="alerts-see-all"
            (click)="close(false)"
            >See all in Fleet</a
          >
        </div>
      </div>
    }
  `,
  styles: `
    :host {
      position: relative;
      display: inline-flex;
      flex-shrink: 0;
    }
    .alerts-btn {
      position: relative;
    }
    .alerts-btn[aria-expanded="true"] {
      background: var(--md-sys-color-secondary-container);
      color: var(--md-sys-color-on-secondary-container);
    }
    .alerts-badge {
      position: absolute;
      top: 2px;
      right: 2px;
      min-width: 16px;
      height: 16px;
      padding: 0 4px;
      box-sizing: border-box;
      border-radius: var(--md-sys-shape-corner-full);
      outline: 2px solid var(--md-sys-color-surface-container-low);
      font-size: 10px;
      line-height: 16px;
      font-weight: 600;
      text-align: center;
      background: var(--md-sys-color-primary);
      color: var(--md-sys-color-on-primary);
    }
    .alerts-badge[data-severity="critical"] {
      background: var(--md-sys-color-error);
      color: var(--md-sys-color-on-error);
    }
    .alerts-badge[data-severity="warning"] {
      background: var(--md-sys-color-warning-container);
      color: var(--md-sys-color-on-warning-container);
      box-shadow: inset 0 0 0 1px var(--md-sys-color-warning);
    }
    .alerts-panel {
      position: absolute;
      top: calc(100% + 6px);
      right: 0;
      z-index: 85;
      box-sizing: border-box;
      width: min(400px, calc(100vw - 24px));
      max-height: min(560px, calc(100vh - 120px));
      display: flex;
      flex-direction: column;
      border-radius: var(--md-sys-shape-corner-large);
      border: 1px solid var(--md-sys-color-outline-variant);
      background: var(--md-sys-color-surface);
      color: var(--md-sys-color-on-surface);
      box-shadow: var(--md-sys-elevation-dialog);
      overflow: hidden;
    }
    :host-context(:root[data-theme="dark"]) .alerts-panel {
      background: var(--md-sys-color-surface-container);
    }
    .alerts-head {
      display: flex;
      align-items: baseline;
      justify-content: space-between;
      gap: 12px;
      padding: 14px 16px 10px;
      border-bottom: 1px solid var(--md-sys-color-outline-variant);
    }
    .alerts-title {
      margin: 0;
      font: 600 14px/20px var(--grain-font-display);
    }
    .alerts-head-count {
      font-size: 12px;
      color: var(--md-sys-color-on-surface-variant);
    }
    .alerts-empty {
      display: flex;
      align-items: center;
      gap: 8px;
      margin: 0;
      padding: 20px 16px;
      font-size: 13px;
      line-height: 18px;
      color: var(--md-sys-color-on-surface-variant);
    }
    .alerts-empty g-status-icon {
      color: var(--md-sys-color-tertiary);
    }
    .alerts-list {
      list-style: none;
      margin: 0;
      padding: 0;
      overflow-y: auto;
      min-height: 0;
    }
    .alerts-row {
      padding: 12px 16px;
      border-bottom: 1px solid var(--md-sys-color-outline-variant);
      border-left: 3px solid transparent;
    }
    .alerts-row[data-severity="critical"] {
      border-left-color: var(--md-sys-color-error);
    }
    .alerts-row[data-severity="warning"] {
      border-left-color: var(--md-sys-color-warning);
    }
    .alerts-row[data-severity="info"] {
      border-left-color: var(--md-sys-color-primary);
    }
    .alerts-row-head {
      display: flex;
      align-items: center;
      gap: 8px;
      min-width: 0;
    }
    .alerts-sev {
      display: inline-flex;
      align-items: center;
      gap: 4px;
      flex: none;
      height: 20px;
      padding: 0 8px;
      border-radius: var(--md-sys-shape-corner-full);
      font-size: 11px;
      font-weight: 600;
      background: var(--md-sys-color-secondary-container);
      color: var(--md-sys-color-on-secondary-container);
    }
    .alerts-sev[data-severity="critical"] {
      background: var(--md-sys-color-error-container);
      color: var(--md-sys-color-on-error-container);
    }
    .alerts-sev[data-severity="warning"] {
      background: var(--md-sys-color-warning-container);
      color: var(--md-sys-color-on-warning-container);
    }
    .alerts-row-title {
      font: 600 13px/18px var(--grain-font-body);
      min-width: 0;
    }
    .alerts-row-sentence,
    .alerts-row-impact {
      margin: 6px 0 0;
      font-size: 13px;
      line-height: 18px;
      overflow-wrap: anywhere;
    }
    .alerts-row-impact {
      color: var(--md-sys-color-on-surface-variant);
    }
    .alerts-row-foot {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 12px;
      margin-top: 8px;
    }
    .alerts-row-meta {
      font-size: 12px;
      color: var(--md-sys-color-on-surface-variant);
      min-width: 0;
    }
    .alerts-row-link,
    .alerts-see-all {
      flex: none;
      font-size: 13px;
      font-weight: 500;
      color: var(--md-sys-color-primary);
      text-decoration: none;
    }
    .alerts-row-link:hover,
    .alerts-see-all:hover {
      text-decoration: underline;
    }
    .alerts-foot {
      display: flex;
      justify-content: flex-end;
      padding: 10px 16px;
    }
    @media (max-width: 599.95px) {
      .alerts-panel {
        position: fixed;
        top: calc(
          var(--grain-banner-height, 0px) + var(--grain-topbar-height) + 4px
        );
        right: 12px;
        left: 12px;
        width: auto;
      }
    }
  `,
})
export class AlertsButtonComponent {
  private readonly status = inject(ShellStatusStore);
  private readonly doc = inject(DOCUMENT);
  private readonly host = inject<ElementRef<HTMLElement>>(ElementRef);
  private readonly bellBtn =
    viewChild.required<ElementRef<HTMLButtonElement>>("bellBtn");
  private readonly panelEl = viewChild<ElementRef<HTMLElement>>("panelEl");

  protected readonly bell = BELL_PATH;
  protected readonly open = signal(false);
  private readonly feed = inject(TaskFeedStore);
  protected readonly alerts = computed(() =>
    collectAlerts(
      this.status.config(),
      this.feed.tasks(),
      preferredZone(),
      new Date(this.feed.now()),
    ),
  );
  protected readonly worst = computed(() => worstSeverity(this.alerts()));

  protected readonly countLabel = computed(() => {
    const n = this.alerts().length;
    return n === 0 ? "None" : `${n} ${n === 1 ? "alert" : "alerts"}`;
  });

  protected readonly label = computed(() => {
    const list = this.alerts();
    if (list.length === 0) return "Alerts: none";
    const w = this.worst();
    const worst = w ? `, worst ${SEVERITY_LABEL[w].toLowerCase()}` : "";
    return `Alerts: ${list.length}${worst}`;
  });

  // What the live region says when the set of alerts changes: the count
  // and the worst one's title, once, rather than every poll.
  protected readonly announcement = computed(() => {
    const list = this.alerts();
    if (list.length === 0) return "";
    return `${list.length} ${list.length === 1 ? "alert" : "alerts"}: ${list[0].title}`;
  });

  protected severityLabel(a: ShellAlert): string {
    return SEVERITY_LABEL[a.severity];
  }

  protected kindOf(a: ShellAlert): string {
    return SEVERITY_KIND[a.severity];
  }

  protected sinceOf(a: ShellAlert): string {
    return a.since ? formatMoment(a.since, preferredZone()) : "";
  }

  protected toggle(): void {
    if (this.open()) {
      this.close(true);
      return;
    }
    this.open.set(true);
    // Refresh on open, so the panel is current rather than one poll old.
    void this.status.refresh();
    setTimeout(() => this.panelEl()?.nativeElement.focus());
  }

  close(returnFocus: boolean): void {
    if (!this.open()) return;
    this.open.set(false);
    if (returnFocus) this.bellBtn().nativeElement.focus();
  }

  protected onDocClick(ev: Event): void {
    if (!this.open()) return;
    const t = ev.target as Node | null;
    if (t && this.host.nativeElement.contains(t)) return;
    // A click that removed its own target (a link re-rendering) is not
    // outside.
    if (t && !this.doc.contains(t)) return;
    this.close(false);
  }

  // Escape pressed inside the bell or its panel closes the panel before
  // any page-level Escape handler (the Inbox clears its selection on it)
  // can see the key and mark it handled.
  protected onOwnEscape(ev: Event): void {
    if (!this.open()) return;
    ev.preventDefault();
    ev.stopPropagation();
    this.close(true);
  }

  protected onEscape(ev: Event): void {
    if (!this.open() || ev.defaultPrevented) return;
    ev.preventDefault();
    ev.stopPropagation();
    this.close(true);
  }
}
