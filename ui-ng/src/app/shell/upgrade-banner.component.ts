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
  effect,
  inject,
  signal,
} from "@angular/core";
import { takeUntilDestroyed } from "@angular/core/rxjs-interop";
import { NavigationEnd, Router, RouterLink } from "@angular/router";
import { filter } from "rxjs";
import { AccessService } from "../services/access.service";
import { UpgradeWatchService } from "../services/upgrade-watch.service";
import { failedStepError, upgradeStepLine } from "../settings/upgrade-progress";

// UpgradeBannerComponent is the shell's line about this deployment's
// upgrade, under the top bar on every page: how far an upgrade in flight
// has got ("step 2/9 pull image"), the error a failed one stopped on, and,
// once the daemon is back on a new build, the prompt that reloads onto
// it. It is loaded with the shell, never deferred: after an upgrade the
// old bundle's lazy chunks are exactly what can no longer be fetched.
@Component({
  selector: "g-upgrade-banner",
  imports: [RouterLink],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  styles: [
    `
      .upgrade-banner {
        display: flex;
        align-items: center;
        flex-wrap: wrap;
        gap: 6px 12px;
        padding: 8px 20px;
        border-bottom: 1px solid var(--md-sys-color-outline-variant);
        font: 500 13px/18px var(--grain-font-body);
        font-variant-numeric: tabular-nums;
        flex: none;
        overflow-wrap: anywhere;
      }
      .upgrade-banner.is-info {
        background: var(--md-sys-color-secondary-container);
        color: var(--md-sys-color-on-secondary-container);
      }
      .upgrade-banner.is-warn {
        background: var(--md-sys-color-warning-container);
        color: var(--md-sys-color-on-warning-container);
        border-bottom-color: var(--md-sys-color-warning);
      }
      .upgrade-banner.is-error {
        background: var(--md-sys-color-error-container);
        color: var(--md-sys-color-on-error-container);
        border-bottom-color: var(--md-sys-color-error);
      }
      .upgrade-banner-text {
        flex: 1 1 240px;
        min-width: 0;
      }
      .upgrade-banner-actions {
        display: flex;
        align-items: center;
        gap: 8px;
        margin-left: auto;
      }
      .upgrade-banner a,
      .upgrade-banner button {
        font: inherit;
        color: inherit;
      }
      .upgrade-banner a {
        text-decoration: underline;
      }
      .upgrade-banner button {
        min-height: 32px;
        padding: 0 12px;
        border-radius: var(--md-sys-shape-corner-full, 999px);
        border: 1px solid currentColor;
        background: transparent;
        cursor: pointer;
      }
      .upgrade-banner-bar {
        flex: 0 0 96px;
        height: 4px;
        border-radius: 2px;
        background: color-mix(in srgb, currentColor 20%, transparent);
        overflow: hidden;
      }
      .upgrade-banner-bar > span {
        display: block;
        height: 100%;
        background: currentColor;
      }
    `,
  ],
  template: `
    @if (watch.upgradedTo(); as to) {
      <div
        class="upgrade-banner is-info"
        role="status"
        data-testid="upgrade-reload-banner"
      >
        <span class="upgrade-banner-text">
          grain was upgraded to {{ to }}.
          @if (watch.reloadIn(); as left) {
            Reloading in {{ left }}s…
          } @else {
            This page has unsaved changes: save them, then reload.
          }
        </span>
        <span class="upgrade-banner-actions">
          <button
            type="button"
            data-testid="upgrade-reload-now"
            (click)="watch.reloadNow()"
          >
            Reload now
          </button>
        </span>
      </div>
    } @else if (failed()) {
      @let f = failed()!;
      <div
        class="upgrade-banner is-error"
        role="alert"
        data-testid="upgrade-failed-banner"
      >
        <span class="upgrade-banner-text"
          >Upgrade to {{ f.branch }} failed{{
            f.error ? ": " + f.error : ""
          }}</span
        >
        <span class="upgrade-banner-actions">
          @if (!onUpgradePage()) {
            <a routerLink="/settings/upgrade">Details</a>
          }
          <button
            type="button"
            data-testid="upgrade-failed-dismiss"
            (click)="dismissed.set(dismissKey())"
          >
            Dismiss
          </button>
        </span>
      </div>
    } @else if (inFlight() && !onUpgradePage()) {
      @let p = inFlight()!;
      <div
        class="upgrade-banner is-warn"
        role="status"
        data-testid="upgrade-progress-banner"
      >
        <span class="upgrade-banner-text"
          >Upgrading to {{ p.branch }}{{ p.line ? " · " + p.line : "" }}</span
        >
        @if (p.percent !== null) {
          <span class="upgrade-banner-bar" aria-hidden="true"
            ><span [style.width.%]="p.percent"></span
          ></span>
        }
        <span class="upgrade-banner-actions">
          <a routerLink="/settings/upgrade">Details</a>
        </span>
      </div>
    }
  `,
})
export class UpgradeBannerComponent {
  protected readonly watch = inject(UpgradeWatchService);
  private readonly access = inject(AccessService);
  private readonly router = inject(Router);

  protected readonly dismissed = signal("");
  private readonly url = signal(this.router.url);
  protected readonly onUpgradePage = computed(() =>
    /^\/settings\/upgrade(?:[?#]|$)/.test(this.url()),
  );

  protected readonly dismissKey = computed(() => {
    const s = this.watch.status();
    return `${s?.branch ?? ""}@${s?.finishedAt ?? s?.startedAt ?? ""}`;
  });

  protected readonly inFlight = computed(() => {
    const s = this.watch.status();
    if (s?.phase !== "running") return null;
    const steps = s.steps ?? [];
    const settled = steps.filter((x) =>
      ["done", "skipped", "failed", "cancelled"].includes(x.state),
    ).length;
    return {
      branch: s.branch || "a new build",
      line: upgradeStepLine(steps, this.watch.now()),
      percent: steps.length ? Math.round((settled / steps.length) * 100) : null,
    };
  });

  // A failure is the shell's to say only when this tab watched the
  // upgrade run: one that failed last week is the pane's history, not
  // news on every page.
  protected readonly failed = computed(() => {
    const s = this.watch.status();
    if (!this.watch.sawRunning() || s?.phase !== "failed") return null;
    if (this.dismissed() === this.dismissKey()) return null;
    return { branch: s.branch || "a new build", error: failedStepError(s) };
  });

  constructor() {
    this.router.events
      .pipe(
        filter((e): e is NavigationEnd => e instanceof NavigationEnd),
        takeUntilDestroyed(inject(DestroyRef)),
      )
      .subscribe((e) => this.url.set(e.urlAfterRedirects));
    // One read once access is known, so an upgrade already running when
    // this tab opened is shown (and polled) wherever the operator is.
    effect(() => {
      if (this.access.configLoaded()) this.watch.start();
    });
  }
}
