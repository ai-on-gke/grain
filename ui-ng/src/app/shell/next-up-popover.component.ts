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
  input,
  output,
} from "@angular/core";
import { RouterLink } from "@angular/router";
import type { Task } from "../api/models";
import type { DispatchStatusBanner } from "../shared/dispatch-status";
import { isBlocked, levelOf, priorityOf } from "../tasks/task-view";

const SHOWN = 7;

@Component({
  selector: "g-next-up-popover",
  imports: [RouterLink],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div
      class="next-up-pop top-next-pop"
      id="top-next-pop"
      role="region"
      aria-label="What runs next"
      data-testid="next-up-pop"
    >
      <div class="next-pop-head">
        <span class="next-pop-title">What runs next</span>
        <span class="muted tabular"
          >{{ backlog().length }} in backlog ·
          {{ runningCount() }} running</span
        >
      </div>

      @if (dispatchPaused(); as dp) {
        <p
          class="banner banner-info next-up-note"
          data-testid="next-up-paused-note"
        >
          {{ dp.headline }}
          @if (dp.resetText) {
            · {{ dp.resetText }}
          }
        </p>
      }
      @if (backlog().length === 0) {
        <p class="muted next-up-note" data-testid="next-up-empty-note">
          Nothing is waiting to run. Approved tasks appear here in the order
          Grain will start them.
        </p>
      } @else {
        <ol class="next-up-list">
          @for (t of shown(); track t.id; let i = $index) {
            <li
              [class.is-head]="headId() === t.id"
              [class.is-blocked]="blocked(t)"
            >
              <a [routerLink]="['/task', t.id]" (click)="closed.emit()">
                <span class="tabular muted">{{ i + 1 }}</span>
                <span class="next-up-row-title">#{{ t.id }} {{ t.title }}</span>
                <span class="muted">{{ levelName(t) }}</span>
              </a>
            </li>
          }
        </ol>
        @if (more() > 0) {
          <p class="muted next-up-more">and {{ more() }} more</p>
        }
        <p class="muted next-up-note">
          Queue order is priority, then manual order. Tasks admitted past worker
          limits start first; a task waiting on a dependency is skipped.
        </p>
      }
      <div class="next-pop-foot">
        <a
          class="btn-tonal"
          routerLink="/tasks"
          [queryParams]="{ tab: 'backlog' }"
          data-testid="next-up-open-backlog"
          (click)="closed.emit()"
          >Open the backlog</a
        >
      </div>
    </div>
  `,
})
export class NextUpPopoverComponent {
  readonly backlog = input.required<readonly Task[]>();
  readonly headId = input<string | null>(null);
  readonly runningCount = input(0);
  readonly dispatchPaused = input<DispatchStatusBanner | null>(null);
  readonly closed = output<void>();

  protected readonly shown = computed(() => this.backlog().slice(0, SHOWN));
  protected readonly more = computed(() =>
    Math.max(0, this.backlog().length - SHOWN),
  );

  protected blocked(t: Task): boolean {
    return isBlocked(t);
  }

  protected levelName(t: Task): string {
    const p = priorityOf(t);
    return `${levelOf(p).name} ${p}`;
  }
}
