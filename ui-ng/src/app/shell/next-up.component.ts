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
import { InboxStore } from "../inbox/inbox.store";
import {
  isWork,
  jumpsQueue,
  levelOf,
  nextUp,
  priorityOf,
} from "../tasks/task-view";

const SHOWN = 7;

// NextUpComponent is the top bar's Next up split chip (spec D-28): the
// label opens the backlog in run order, the caret shows the sequence a
// free worker will take, and the head is the first task not waiting on a
// dependency.
@Component({
  selector: "g-next-up",
  imports: [RouterLink],
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: {
    "(document:click)": "onDocClick($event)",
    "(document:keydown.escape)": "onEscape($event)",
  },
  template: `
    @if (head(); as h) {
      <span class="next-up" data-testid="next-up">
        <a
          class="next-up-chip"
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
        ><button
          #caret
          type="button"
          class="next-up-caret"
          id="top-next-caret"
          aria-controls="top-next-pop"
          [attr.aria-expanded]="open()"
          aria-label="Show the run sequence"
          data-testid="next-up-caret"
          (click)="toggle($event)"
        >
          ▾
        </button>
        @if (open()) {
          <div class="next-up-pop" id="top-next-pop" data-testid="next-up-pop">
            <ol class="next-up-list">
              @for (t of shown(); track t.id; let i = $index) {
                <li [class.is-head]="t.id === h.id">
                  <a [href]="'/tasks/' + t.id" (click)="open.set(false)">
                    <span class="tabular muted">{{
                      jumps(t) ? "↑↑" : i + 1
                    }}</span>
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
              Order: tasks that jump the queue, then priority, then manual
              order. A task waiting on a dependency is skipped.
            </p>
            <a
              class="btn-text"
              routerLink="/tasks"
              [queryParams]="{ tab: 'backlog' }"
              (click)="open.set(false)"
              >Open the backlog</a
            >
          </div>
        }
      </span>
    }
  `,
})
export class NextUpComponent {
  private readonly inbox = inject(InboxStore);
  protected readonly open = signal(false);
  private readonly caret = viewChild<ElementRef<HTMLButtonElement>>("caret");

  // The shell's live task list is in backlog order already.
  private readonly backlog = computed(() =>
    this.inbox.tasks().filter((t) => isWork(t) && t.state === "queued"),
  );
  protected readonly head = computed(() => nextUp(this.backlog()));
  protected readonly shown = computed(() => this.backlog().slice(0, SHOWN));
  protected readonly more = computed(() =>
    Math.max(0, this.backlog().length - SHOWN),
  );
  protected readonly why = computed(() => {
    const h = this.head();
    if (!h) return "";
    const skipped = this.backlog().indexOf(h);
    return (
      `#${h.id} is first in run order` +
      (jumpsQueue(h)
        ? " because it jumps the queue"
        : `, at priority ${priorityOf(h)}`) +
      (skipped > 0 ? `; ${skipped} ahead of it wait on dependencies` : "")
    );
  });

  protected jumps = jumpsQueue;
  protected levelName(t: Parameters<typeof priorityOf>[0]): string {
    const p = priorityOf(t);
    return `${levelOf(p).name} ${p}`;
  }

  protected toggle(ev: Event): void {
    ev.stopPropagation();
    this.open.update((v) => !v);
  }

  protected onDocClick(ev: Event): void {
    if (!(ev.target as HTMLElement | null)?.closest(".next-up-pop")) {
      this.open.set(false);
    }
  }

  protected onEscape(ev: Event): void {
    if (!this.open() || ev.defaultPrevented) return;
    ev.preventDefault();
    this.open.set(false);
    this.caret()?.nativeElement.focus();
  }
}
