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
  ElementRef,
  computed,
  inject,
  signal,
  viewChild,
} from "@angular/core";
import { RouterLink } from "@angular/router";
import type { Task } from "../api/models";
import { TaskFeedStore } from "../services/task-feed.service";
import {
  isBlocked,
  isWork,
  jumpsQueue,
  levelOf,
  nextUp,
  priorityOf,
} from "../tasks/task-view";

const SHOWN = 7;

// NextUpComponent is the persistent top-bar Next up split chip (spec D-28):
// always rendered at 36px height so top-bar layout never shifts. When the
// queue is empty it shows "Next up · Queue empty" (or "N waiting on deps"
// when all queued tasks are blocked on dependencies) with an explanatory
// popover linking to the backlog.
@Component({
  selector: "g-next-up",
  imports: [RouterLink],
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: {
    "(document:click)": "onDocClick($event)",
    "(document:keydown.escape)": "onEscape($event)",
  },
  template: `
    <span
      class="next-up top-next"
      [class.is-empty]="!head()"
      data-testid="next-up"
    >
      @if (head(); as h) {
        <a
          class="next-up-chip top-next-chip"
          id="top-next-chip"
          routerLink="/tasks"
          [queryParams]="{ tab: 'backlog' }"
          [attr.title]="why()"
          [attr.aria-label]="
            'Next up: #' + h.id + ' ' + h.title + '. Open the backlog'
          "
          ><span class="next-up-label">Next up</span>
          <span class="tabular">#{{ h.id }}</span>
          <span class="next-up-title">{{ h.title }}</span></a
        >
      } @else if (blockedCount() > 0) {
        <a
          class="next-up-chip top-next-chip"
          id="top-next-chip"
          routerLink="/tasks"
          [queryParams]="{ tab: 'backlog' }"
          [attr.title]="why()"
          [attr.aria-label]="
            'Next up: ' +
            blockedCount() +
            ' waiting on dependencies. Open the backlog'
          "
          ><span class="next-up-label">Next up</span>
          <span class="next-up-sep" aria-hidden="true">·</span>
          <span class="next-up-empty-text" data-testid="next-up-blocked"
            >{{ blockedCount() }} waiting on deps</span
          ></a
        >
      } @else {
        <a
          class="next-up-chip top-next-chip"
          id="top-next-chip"
          routerLink="/tasks"
          [queryParams]="{ tab: 'backlog' }"
          title="Nothing is waiting in the backlog. Open the backlog."
          aria-label="Next up: Queue empty. Open the backlog"
          ><span class="next-up-label">Next up</span>
          <span class="next-up-sep" aria-hidden="true">·</span>
          <span class="next-up-empty-text" data-testid="next-up-empty"
            >Queue empty</span
          ></a
        >
      }
      <button
        #caret
        type="button"
        class="next-up-caret top-next-caret"
        [class.is-open]="open()"
        id="top-next-caret"
        aria-controls="top-next-pop"
        [attr.aria-expanded]="open()"
        aria-label="Show what runs next"
        data-testid="next-up-caret"
        (click)="toggle($event)"
      >
        <svg
          class="next-up-chevron"
          width="16"
          height="16"
          viewBox="0 0 24 24"
          aria-hidden="true"
        >
          <path d="m7 10 5 5 5-5z" fill="currentColor" />
        </svg>
      </button>
      @if (open()) {
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
              {{ feed.runningCount() }} running</span
            >
          </div>
          @if (backlog().length === 0) {
            <p class="muted next-up-note" data-testid="next-up-empty-note">
              Nothing is waiting to run. Approved tasks appear here in the order
              Grain will start them.
            </p>
          } @else {
            <ol class="next-up-list">
              @for (t of shown(); track t.id; let i = $index) {
                <li
                  [class.is-head]="head()?.id === t.id"
                  [class.is-blocked]="blocked(t)"
                >
                  <a [routerLink]="['/task', t.id]" (click)="open.set(false)">
                    <span class="tabular muted">{{ i + 1 }}</span>
                    <span class="next-up-row-title"
                      >#{{ t.id }} {{ t.title }}</span
                    >
                    <span class="muted">{{ levelName(t) }}</span>
                  </a>
                </li>
              }
            </ol>
            @if (more() > 0) {
              <p class="muted next-up-more">and {{ more() }} more</p>
            }
            <p class="muted next-up-note">
              Queue order is priority, then manual order. Tasks admitted past
              worker limits start first; a task waiting on a dependency is
              skipped.
            </p>
          }
          <div class="next-pop-foot">
            <a
              class="btn-tonal"
              routerLink="/tasks"
              [queryParams]="{ tab: 'backlog' }"
              data-testid="next-up-open-backlog"
              (click)="open.set(false)"
              >Open the backlog</a
            >
          </div>
        </div>
      }
    </span>
  `,
})
export class NextUpComponent {
  protected readonly feed = inject(TaskFeedStore);
  protected readonly open = signal(false);
  private readonly caret = viewChild<ElementRef<HTMLButtonElement>>("caret");

  // The shared feed's open task list is in server backlog order already.
  protected readonly backlog = computed(() =>
    this.feed.tasks().filter((t) => isWork(t) && t.state === "queued"),
  );
  protected readonly head = computed(() => nextUp(this.backlog()));
  protected readonly blockedCount = computed(
    () => this.backlog().filter((t) => isBlocked(t)).length,
  );
  protected readonly shown = computed(() => this.backlog().slice(0, SHOWN));
  protected readonly more = computed(() =>
    Math.max(0, this.backlog().length - SHOWN),
  );
  protected readonly why = computed(() => {
    const h = this.head();
    if (!h) {
      const b = this.blockedCount();
      return b > 0
        ? `${b} queued task${b === 1 ? " waits" : "s wait"} on open dependencies`
        : "Queue empty";
    }
    const skipped = this.backlog().indexOf(h);
    return (
      `#${h.id} is first in run order` +
      (jumpsQueue(h)
        ? " because it is admitted past worker limits"
        : `, at priority ${priorityOf(h)}`) +
      (skipped > 0 ? `; ${skipped} ahead of it wait on dependencies` : "")
    );
  });

  protected blocked(t: Task): boolean {
    return isBlocked(t);
  }

  protected levelName(t: Task): string {
    const p = priorityOf(t);
    return `${levelOf(p).name} ${p}`;
  }

  protected toggle(ev: Event): void {
    ev.stopPropagation();
    this.open.update((v) => !v);
  }

  protected onDocClick(ev: Event): void {
    if (!(ev.target as HTMLElement | null)?.closest(".next-up")) {
      this.open.set(false);
    }
  }

  protected onEscape(ev: Event): void {
    if (!this.open() || ev.defaultPrevented) return;
    ev.preventDefault();
    ev.stopPropagation();
    this.open.set(false);
    this.caret()?.nativeElement.focus();
  }
}
