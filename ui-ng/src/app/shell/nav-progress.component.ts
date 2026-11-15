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
  inject,
  signal,
} from "@angular/core";
import {
  NavigationCancel,
  NavigationEnd,
  NavigationError,
  NavigationSkipped,
  NavigationStart,
  Router,
} from "@angular/router";

// How long a navigation may take before the bar shows: a page whose chunk
// is already loaded switches well inside this, so ordinary clicks never
// flash it.
export const NAV_PROGRESS_DELAY_MS = 120;

// NavProgressComponent is the thin bar along the top of the window that
// shows while the router is still working on a click -- fetching a page's
// lazy chunk, most often. Without it a navigation stuck behind a slow
// origin changed nothing on screen and the page looked frozen (task 5801).
// It never takes pointer events, so the page under it stays usable.
@Component({
  selector: "g-nav-progress",
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @if (visible()) {
      <div
        class="nav-progress"
        role="progressbar"
        aria-label="Loading page"
        data-testid="nav-progress"
      >
        <span class="nav-progress-bar"></span>
      </div>
    }
  `,
  styles: `
    .nav-progress {
      position: fixed;
      inset: 0 0 auto 0;
      height: 3px;
      z-index: 1000;
      overflow: hidden;
      pointer-events: none;
      background: var(--md-sys-color-primary-container);
    }
    .nav-progress-bar {
      position: absolute;
      inset: 0 auto 0 0;
      width: 40%;
      background: var(--md-sys-color-primary);
      animation: nav-progress-slide 1.1s ease-in-out infinite;
    }
    @keyframes nav-progress-slide {
      from {
        transform: translateX(-100%);
      }
      to {
        transform: translateX(250%);
      }
    }
    @media (prefers-reduced-motion: reduce) {
      .nav-progress-bar {
        animation: none;
        width: 100%;
        opacity: 0.6;
      }
    }
  `,
})
export class NavProgressComponent {
  protected readonly visible = signal(false);
  private timer: ReturnType<typeof setTimeout> | null = null;

  constructor() {
    const sub = inject(Router).events.subscribe((e) => {
      if (e instanceof NavigationStart) {
        this.clear();
        this.timer = setTimeout(() => {
          this.timer = null;
          this.visible.set(true);
        }, NAV_PROGRESS_DELAY_MS);
      } else if (
        e instanceof NavigationEnd ||
        e instanceof NavigationCancel ||
        e instanceof NavigationError ||
        e instanceof NavigationSkipped
      ) {
        this.clear();
        this.visible.set(false);
      }
    });
    inject(DestroyRef).onDestroy(() => {
      sub.unsubscribe();
      this.clear();
    });
  }

  private clear() {
    if (this.timer !== null) clearTimeout(this.timer);
    this.timer = null;
  }
}
