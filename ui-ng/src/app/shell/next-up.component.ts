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
import { AccessService } from "../services/access.service";
import { TaskFeedStore } from "../services/task-feed.service";
import { summarizeDispatchStatus } from "../shared/dispatch-status";
import {
  isBlocked,
  isWork,
  jumpsQueue,
  nextUp,
  priorityOf,
} from "../tasks/task-queue-helpers";
import { NextUpPopoverComponent } from "./next-up-popover.component";

// NextUpComponent is the persistent top-bar Next up split chip (spec D-28):
// always rendered at 36px height so top-bar layout never shifts. When the
// queue is empty it shows "Next up · Queue empty" (or "N waiting on deps"
// when all queued tasks are blocked on dependencies) with an explanatory
// popover linking to the backlog.
@Component({
  selector: "g-next-up",
  imports: [RouterLink, NextUpPopoverComponent],
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
      (focusout)="onFocusOut($event)"
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
      @defer (when open()) {
        @if (open()) {
          <g-next-up-popover
            [backlog]="backlog()"
            [headId]="head()?.id ?? null"
            [runningCount]="feed.runningCount()"
            [dispatchPaused]="dispatchPaused()"
            (closed)="open.set(false)"
          />
        }
      }
    </span>
  `,
})
export class NextUpComponent {
  protected readonly feed = inject(TaskFeedStore);
  private readonly access = inject(AccessService);
  protected readonly open = signal(false);
  private readonly caret = viewChild<ElementRef<HTMLButtonElement>>("caret");

  protected readonly dispatchPaused = computed(() =>
    summarizeDispatchStatus(
      this.access.rawConfig(),
      this.access.usage(),
      this.access.clockNow(),
    ),
  );

  // The shared feed's open task list is in server backlog order already.
  protected readonly backlog = computed(() =>
    this.feed.tasks().filter((t) => isWork(t) && t.state === "queued"),
  );
  protected readonly head = computed(() => nextUp(this.backlog()));
  protected readonly blockedCount = computed(
    () => this.backlog().filter((t) => isBlocked(t)).length,
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

  protected toggle(ev: Event): void {
    ev.stopPropagation();
    this.open.update((v) => !v);
  }

  protected onDocClick(ev: Event): void {
    if (!(ev.target as HTMLElement | null)?.closest(".next-up")) {
      this.open.set(false);
    }
  }

  protected onFocusOut(ev: FocusEvent): void {
    const next = ev.relatedTarget as HTMLElement | null;
    if (next && !next.closest(".next-up")) {
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
