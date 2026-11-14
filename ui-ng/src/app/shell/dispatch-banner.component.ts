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
  ViewEncapsulation,
  computed,
  inject,
  input,
  signal,
} from "@angular/core";
import { takeUntilDestroyed } from "@angular/core/rxjs-interop";
import { NavigationEnd, Router, RouterLink } from "@angular/router";
import type { DispatchStatusBanner } from "../shared/dispatch-status";

// Global dispatch-pause / quota-exhaustion banner lazily rendered by AppComponent
// only when development dispatch or model quotas are constrained.
@Component({
  selector: "g-dispatch-banner",
  imports: [RouterLink],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  styles: [
    `
      .dispatch-banner {
        display: flex;
        flex-direction: column;
        gap: 6px;
        padding: 8px 20px;
        border-bottom: 1px solid var(--md-sys-color-outline-variant);
        font: 500 13px/18px var(--grain-font-body);
        font-variant-numeric: tabular-nums;
        flex: none;
      }
      .dispatch-banner.is-error {
        background: var(--md-sys-color-error-container);
        color: var(--md-sys-color-on-error-container);
        border-bottom-color: var(--md-sys-color-error);
      }
      .dispatch-banner.is-warn {
        background: var(--md-sys-color-warning-container);
        color: var(--md-sys-color-on-warning-container);
        border-bottom-color: var(--md-sys-color-warning);
      }
      .dispatch-banner-row {
        display: flex;
        align-items: center;
        flex-wrap: wrap;
        gap: 8px 12px;
      }
      .dispatch-banner-main {
        display: flex;
        align-items: flex-start;
        flex-wrap: nowrap;
        gap: 8px;
        flex: 1 1 320px;
        min-width: 0;
      }
      .dispatch-banner-main svg {
        flex: none;
        margin-top: 1px;
      }
      .dispatch-banner-copy {
        min-width: 0;
      }
      .dispatch-banner-actions {
        display: inline-flex;
        align-items: center;
        flex-wrap: wrap;
        gap: 6px;
        margin-left: auto;
      }
      .dispatch-banner-actions a,
      .dispatch-banner-actions button {
        color: inherit;
        min-height: 28px;
        padding: 0 8px;
        font-size: 12px;
        text-decoration: none;
      }
      .dispatch-banner-list {
        margin: 0;
        padding-left: 20px;
        font-size: 12px;
        line-height: 18px;
      }
    `,
  ],
  template: `
    @let db = banner();
    <div
      class="dispatch-banner"
      [class.is-error]="db.tone === 'error'"
      [class.is-warn]="db.tone === 'warn'"
      role="status"
      data-testid="global-dispatch-banner"
    >
      <div class="dispatch-banner-row">
        <div class="dispatch-banner-main">
          <svg width="16" height="16" viewBox="0 0 24 24" aria-hidden="true">
            <path
              d="M12 2L1 21h22L12 2zm1 14h-2v2h2v-2zm0-6h-2v4h2v-4z"
              fill="currentColor"
            />
          </svg>
          <span class="dispatch-banner-copy">
            <strong>{{ db.headline }}</strong>
            @if (db.resetText) {
              <span> · {{ db.resetText }}</span>
            }
            @if (db.subline) {
              <span> — {{ db.subline }}</span>
            }
          </span>
        </div>
        <div class="dispatch-banner-actions">
          @if (db.details.length > 1) {
            <button
              type="button"
              class="btn-text"
              data-testid="dispatch-banner-toggle"
              [attr.aria-expanded]="expanded()"
              (click)="expanded.set(!expanded())"
            >
              {{
                expanded()
                  ? "Hide details"
                  : "Details (" + db.details.length + ")"
              }}
            </button>
          }
          @if (!hideTelemetry()) {
            <a
              class="btn-text"
              routerLink="/telemetry"
              data-testid="dispatch-banner-telemetry"
              >Telemetry</a
            >
          }
          @if (!hideControls()) {
            <a
              class="btn-text"
              routerLink="/fleet"
              [queryParams]="{ tab: 'controls' }"
              data-testid="dispatch-banner-controls"
              >Controls</a
            >
          }
        </div>
      </div>
      @if (expanded() && db.details.length > 0) {
        <ul class="dispatch-banner-list" data-testid="dispatch-banner-details">
          @for (d of db.details; track $index) {
            <li>
              <strong>{{ d.label }}:</strong> {{ d.reason }}
              @if (d.resetText) {
                <span> · resets {{ d.resetText }}</span>
              }
            </li>
          }
        </ul>
      }
    </div>
  `,
})
export class DispatchBannerComponent {
  private readonly router = inject(Router, { optional: true });
  private readonly destroyRef = inject(DestroyRef);
  readonly banner = input.required<DispatchStatusBanner>();
  protected readonly expanded = signal(false);
  private readonly currentUrl = signal(this.router?.url ?? "");

  protected readonly hideTelemetry = computed(() =>
    this.currentUrl().split(/[?#]/)[0].startsWith("/telemetry"),
  );
  protected readonly hideControls = computed(() => {
    const [path, qs = ""] = this.currentUrl().split("#")[0].split("?");
    return (
      path.startsWith("/fleet") &&
      new URLSearchParams(qs).get("tab") === "controls"
    );
  });

  constructor() {
    this.router?.events
      .pipe(takeUntilDestroyed(this.destroyRef))
      .subscribe((e) => {
        if (e instanceof NavigationEnd) {
          this.currentUrl.set(e.urlAfterRedirects);
        }
      });
  }
}
