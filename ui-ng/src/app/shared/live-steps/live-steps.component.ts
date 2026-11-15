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
  ViewEncapsulation,
  computed,
  input,
  signal,
} from "@angular/core";
import { NgTemplateOutlet } from "@angular/common";
import { MarkdownDirective } from "../code-highlight";
import { clipOutput, liveSteps, parseLiveTranscript } from "./live-steps";

// A chat turn's tool calls as one described line each (design book §5,
// "Live turn steps"). While the turn runs (finished=false) every step and
// the agent's own words between them are listed and the newest step is the
// last thing in the thread; once the reply has landed (finished=true) the
// whole list folds into one "N steps" disclosure.
@Component({
  selector: "g-live-steps",
  changeDetection: ChangeDetectionStrategy.OnPush,
  encapsulation: ViewEncapsulation.None,
  imports: [NgTemplateOutlet, MarkdownDirective],
  styleUrls: ["./live-steps.css"],
  host: { class: "g-live-steps" },
  template: `
    @if (finished()) {
      @if (count() > 0) {
        <details class="steps-done" data-testid="live-steps-done">
          <summary class="steps-done-summary">
            {{ count() }} step{{ count() === 1 ? "" : "s" }}
          </summary>
          <ng-container *ngTemplateOutlet="list" />
        </details>
      }
    } @else {
      <ng-container *ngTemplateOutlet="list" />
    }

    <ng-template #list>
      <ol class="steps" aria-label="Steps in this turn">
        @for (it of items(); track $index; let i = $index) {
          @if (it.kind === "text") {
            @if (!finished()) {
              <li class="step-text msg-body" [gMarkdown]="it.text"></li>
            }
          } @else {
            @let s = it.step;
            @let open = expanded().has(i);
            <li
              class="step"
              [class.is-open]="open"
              [class.is-error]="s.isError"
              [class.is-pending]="!s.done"
              data-testid="live-step"
            >
              <button
                type="button"
                class="step-line"
                [attr.aria-expanded]="open"
                [attr.title]="s.label"
                data-testid="live-step-toggle"
                (click)="toggle(i)"
              >
                <span class="step-chevron" aria-hidden="true">{{
                  open ? "▾" : "▸"
                }}</span>
                <span
                  class="step-state"
                  [class.is-error]="s.isError"
                  [class.is-pending]="!s.done"
                  aria-hidden="true"
                ></span>
                <span class="step-label" data-testid="live-step-label">{{
                  s.label
                }}</span>
                <span class="sr-only">{{
                  !s.done ? " (running)" : s.isError ? " (failed)" : ""
                }}</span>
              </button>
              @if (open) {
                <div class="step-detail" data-testid="live-step-detail">
                  <span class="step-detail-label">{{ s.tool }}</span>
                  <pre class="step-call" data-testid="live-step-call">{{
                    s.call
                  }}</pre>
                  @if (s.done) {
                    @let clip = full().has(i) ? null : clipped(s.output);
                    <span class="step-detail-label">{{
                      s.isError ? "Error" : "Output"
                    }}</span>
                    <pre class="step-output" data-testid="live-step-output">{{
                      clip ? clip.text : s.output || "(no output)"
                    }}</pre>
                    @if (clip && clip.hidden > 0) {
                      <button
                        type="button"
                        class="btn-text step-more"
                        data-testid="live-step-show-all"
                        (click)="showAll(i)"
                      >
                        Show all ({{ clip.hidden }} more
                        line{{ clip.hidden === 1 ? "" : "s" }})
                      </button>
                    }
                  } @else {
                    <span class="step-detail-label">Running…</span>
                  }
                </div>
              }
            </li>
          }
        }
      </ol>
    </ng-template>
  `,
})
export class LiveStepsComponent {
  readonly transcript = input<string | null>(null);
  /** Fold the list into one "N steps" disclosure: the turn has ended. */
  readonly finished = input(false);

  protected readonly items = computed(() =>
    parseLiveTranscript(this.transcript()),
  );
  protected readonly count = computed(() => liveSteps(this.items()).length);

  protected readonly expanded = signal<ReadonlySet<number>>(new Set());
  protected readonly full = signal<ReadonlySet<number>>(new Set());

  protected toggle(i: number): void {
    this.expanded.update((s) => flip(s, i));
  }

  protected showAll(i: number): void {
    this.full.update((s) => new Set(s).add(i));
  }

  protected clipped(out: string) {
    const c = clipOutput(out);
    return c.hidden > 0 ? c : { text: out || "(no output)", hidden: 0 };
  }
}

function flip(s: ReadonlySet<number>, i: number): Set<number> {
  const n = new Set(s);
  if (n.has(i)) n.delete(i);
  else n.add(i);
  return n;
}
