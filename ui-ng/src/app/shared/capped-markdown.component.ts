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
  ViewEncapsulation,
  afterRenderEffect,
  effect,
  inject,
  input,
  signal,
  untracked,
  viewChild,
} from "@angular/core";
import { MarkdownDirective } from "./code-highlight";

// g-capped-markdown is a message read in full Markdown (the conversation's
// own renderer: lists, code cards with Copy, links, line breaks) in a box
// of capped height, so a long question cannot push the controls beside it
// off the pane. When the text is taller than the cap the box scrolls on
// its own and offers "Show full <noun>", which lifts
// the cap in place (to a taller one, so the actions below stay on screen).
// A new text closes it again.
@Component({
  selector: "g-capped-markdown",
  changeDetection: ChangeDetectionStrategy.OnPush,
  encapsulation: ViewEncapsulation.None,
  imports: [MarkdownDirective],
  styleUrls: ["./capped-markdown.css"],
  template: `
    <div
      #box
      class="capped-md msg-body"
      [class.is-open]="open()"
      [attr.data-testid]="testid() || null"
      [attr.tabindex]="overflowing() ? 0 : null"
      [attr.role]="overflowing() ? 'region' : null"
      [attr.aria-label]="overflowing() ? label() : null"
      [gMarkdown]="text()"
    ></div>
    @if (overflowing() || open()) {
      <button
        type="button"
        class="btn-text capped-md-toggle"
        [attr.data-testid]="testid() ? testid() + '-toggle' : null"
        [attr.aria-expanded]="open()"
        (click)="open.set(!open())"
      >
        {{ open() ? "Show less" : "Show full " + noun() }}
      </button>
    }
  `,
})
export class CappedMarkdownComponent {
  readonly text = input<string>("");
  // What the text is, for "Show full <noun>" and the scroll region's name.
  readonly noun = input<string>("message");
  readonly label = input<string>("Message");
  readonly testid = input<string>("");

  protected readonly open = signal(false);
  protected readonly overflowing = signal(false);
  private readonly box = viewChild.required<ElementRef<HTMLElement>>("box");

  constructor() {
    effect(() => {
      this.text();
      untracked(() => this.open.set(false));
    });
    afterRenderEffect(() => {
      this.text();
      this.open();
      this.measure();
    });
    const el = inject(ElementRef<HTMLElement>).nativeElement as HTMLElement;
    const RO = el.ownerDocument?.defaultView?.ResizeObserver;
    if (RO) {
      const ro = new RO(() => this.measure());
      ro.observe(el);
      inject(DestroyRef).onDestroy(() => ro.disconnect());
    }
  }

  private measure(): void {
    const box = this.box().nativeElement;
    // Open, the box is no longer clipped by the collapsed cap, but the
    // toggle stays so it can be closed again.
    if (this.open()) return;
    this.overflowing.set(box.scrollHeight > box.clientHeight + 1);
  }
}
