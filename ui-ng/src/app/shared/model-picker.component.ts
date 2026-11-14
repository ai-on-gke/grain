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
  Injector,
  NgZone,
  OnDestroy,
  ViewEncapsulation,
  afterNextRender,
  computed,
  inject,
  input,
  output,
  signal,
  viewChild,
} from "@angular/core";
import {
  type ModelGroup,
  availabilityNote,
  containsModel,
  modelAvailability,
} from "./model-catalog";

interface PickerRow {
  key: string;
  model: string;
  groupId: string;
}

interface PickerGroupView {
  id: string;
  headId: string;
  label: string;
  note: string;
  rows: PickerRow[];
}

let nextModelPickerId = 0;

// g-model-picker is the one control for choosing a model: a pill that
// opens a searchable listbox of the models the deployment's connections
// advertise, grouped by connection. A value no connection advertises
// stays selectable and is flagged instead of being dropped, so a form
// opened on it never silently loses it. It never offers free text: the
// models are what the connections serve (refresh asks them again).
@Component({
  selector: "g-model-picker",
  encapsulation: ViewEncapsulation.None,
  styleUrl: "./model-picker.css",
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: {
    "(document:click)": "onDocClick($event)",
    "(focusout)": "onFocusOut($event)",
  },
  template: `
    <span #wrapEl class="mp-wrap">
      <button
        #btnEl
        type="button"
        class="mp-trigger"
        [class.is-open]="open()"
        [disabled]="disabled()"
        [attr.data-testid]="testid() || null"
        [attr.aria-label]="triggerLabel()"
        [attr.aria-expanded]="open()"
        [attr.aria-controls]="open() ? panelId : null"
        aria-haspopup="dialog"
        (click)="toggle()"
        (keydown)="onTriggerKey($event)"
      >
        <span class="mp-trigger-label" [class.mp-placeholder]="!value()">{{
          value() || placeholder()
        }}</span>
        @if (valueNote()) {
          <span
            class="mp-flag"
            [attr.data-testid]="(testid() || 'mp') + '-flag'"
            >{{ valueNote() }}</span
          >
        }
        <svg
          class="mp-chevron"
          width="14"
          height="14"
          viewBox="0 0 24 24"
          aria-hidden="true"
        >
          <path d="m7 10 5 5 5-5z" fill="currentColor" />
        </svg>
      </button>

      @if (open()) {
        <div
          [id]="panelId"
          class="mp-panel"
          [class.mp-up]="openUp()"
          role="dialog"
          [attr.aria-label]="ariaLabel() || placeholder()"
          [attr.data-testid]="(testid() || 'mp') + '-panel'"
        >
          <input
            #searchEl
            class="s-input mp-search"
            type="text"
            role="combobox"
            autocomplete="off"
            spellcheck="false"
            aria-autocomplete="list"
            [attr.aria-label]="'Search ' + (ariaLabel() || 'models')"
            [attr.aria-expanded]="true"
            [attr.aria-controls]="listId"
            [attr.aria-activedescendant]="activeId()"
            [attr.data-testid]="(testid() || 'mp') + '-search'"
            placeholder="Search models"
            [value]="query()"
            (input)="onQuery($any($event.target).value)"
            (keydown)="onSearchKey($event)"
          />
          <div
            [id]="listId"
            class="mp-list"
            role="listbox"
            [attr.aria-label]="ariaLabel() || 'Models'"
          >
            @for (g of view(); track g.id) {
              <div
                role="group"
                class="mp-group"
                [attr.aria-labelledby]="g.headId"
              >
                <div class="mp-group-head" [id]="g.headId" role="presentation">
                  <span class="mp-group-name">{{ g.label }}</span>
                  @if (g.note) {
                    <span class="mp-group-note">{{ g.note }}</span>
                  }
                </div>
                @for (r of g.rows; track r.key) {
                  @let selected = r.model === value();
                  <div
                    role="option"
                    class="mp-option"
                    tabindex="-1"
                    [id]="r.key"
                    [class.is-active]="r.key === activeId()"
                    [attr.aria-selected]="selected"
                    [attr.data-testid]="(testid() || 'mp') + '-opt'"
                    [attr.data-model]="r.model"
                    [attr.data-group]="r.groupId"
                    (mousedown)="$event.preventDefault()"
                    (mousemove)="activeKey.set(r.key)"
                    (keydown.enter)="choose(r.model)"
                    (click)="choose(r.model)"
                  >
                    <span class="mp-option-label">{{ r.model }}</span>
                    @if (selected) {
                      <svg
                        class="mp-check"
                        width="14"
                        height="14"
                        viewBox="0 0 24 24"
                        aria-hidden="true"
                      >
                        <path
                          d="M9 16.17 4.83 12l-1.42 1.41L9 19 21 7l-1.41-1.41z"
                          fill="currentColor"
                        />
                      </svg>
                    }
                  </div>
                }
              </div>
            } @empty {
              <p class="mp-empty" role="presentation">
                {{
                  query()
                    ? "No advertised model matches “" + query() + "”."
                    : "No connection advertises a model yet."
                }}
              </p>
            }
          </div>
          @if (refreshable()) {
            <div class="mp-foot">
              <span class="mp-foot-note" aria-live="polite">{{
                refreshing() ? "Asking providers…" : refreshNote()
              }}</span>
              <button
                type="button"
                class="btn-text s-btn-sm"
                [disabled]="refreshing()"
                [attr.data-testid]="(testid() || 'mp') + '-refresh'"
                (click)="refresh.emit()"
              >
                Refresh models
              </button>
            </div>
          }
        </div>
      }
    </span>
  `,
})
export class ModelPickerComponent implements OnDestroy {
  private readonly doc = inject(DOCUMENT);
  private readonly zone = inject(NgZone);
  private readonly injector = inject(Injector);
  private readonly uid = ++nextModelPickerId;
  protected readonly panelId = `mp-panel-${this.uid}`;
  protected readonly listId = `mp-list-${this.uid}`;

  readonly groups = input.required<readonly ModelGroup[]>();
  readonly value = input<string>("");
  /** Models not to offer (already chosen elsewhere in the same list). */
  readonly exclude = input<readonly string[]>([]);
  readonly placeholder = input<string>("Choose a model");
  readonly ariaLabel = input<string>("");
  readonly testid = input<string>("");
  readonly disabled = input<boolean>(false);
  readonly openUp = input<boolean>(false);
  readonly refreshable = input<boolean>(false);
  readonly refreshing = input<boolean>(false);
  readonly refreshNote = input<string>("");

  readonly valueChange = output<string>();
  readonly refresh = output<void>();

  protected readonly open = signal(false);
  protected readonly query = signal("");
  protected readonly activeKey = signal<string | null>(null);

  private readonly wrapEl = viewChild<ElementRef<HTMLElement>>("wrapEl");
  private readonly btnEl = viewChild<ElementRef<HTMLButtonElement>>("btnEl");
  private readonly searchEl =
    viewChild<ElementRef<HTMLInputElement>>("searchEl");

  private readonly onCaptureKeydown = (ev: KeyboardEvent): void => {
    if (ev.key === "Escape" && this.open()) {
      ev.preventDefault();
      ev.stopPropagation();
      this.zone.run(() => this.close(true));
    }
  };

  protected readonly valueNote = computed(() => {
    const v = this.value();
    return v ? availabilityNote(modelAvailability(v, this.groups())) : "";
  });

  protected readonly triggerLabel = computed(() => {
    const base = this.ariaLabel() || "Model";
    const v = this.value();
    if (!v) return `${base}: ${this.placeholder()}`;
    const note = this.valueNote();
    return note ? `${base}: ${v} (${note})` : `${base}: ${v}`;
  });

  protected readonly view = computed<PickerGroupView[]>(() => {
    const q = this.query().trim().toLowerCase();
    const exclude = this.exclude();
    const groups = this.groups();
    const out: PickerGroupView[] = [];
    const v = this.value();
    const keep = (m: string): boolean =>
      (!q || m.toLowerCase().includes(q)) &&
      (m === v || !containsModel(exclude, m));
    if (v && modelAvailability(v, groups) === "unadvertised" && keep(v)) {
      out.push({
        id: "__current",
        headId: `mp-${this.uid}-g-current`,
        label: "Current setting",
        note: "not advertised by any connection",
        rows: [{ key: `mp-${this.uid}-current`, model: v, groupId: "" }],
      });
    }
    for (const [gi, g] of groups.entries()) {
      const rows = g.models.filter(keep).map((m, mi) => ({
        key: `mp-${this.uid}-o-${gi}-${mi}`,
        model: m,
        groupId: g.id,
      }));
      if (!rows.length) continue;
      const notes: string[] = [];
      if (g.framework) notes.push(g.framework);
      if (g.disabled) notes.push("disabled");
      if (g.pinned) notes.push("pinned list");
      out.push({
        id: g.id,
        headId: `mp-${this.uid}-g-${gi}`,
        label: g.label,
        note: notes.join(" · "),
        rows,
      });
    }
    return out;
  });

  private readonly rows = computed(() => this.view().flatMap((g) => g.rows));

  protected readonly activeId = computed(() => {
    const k = this.activeKey();
    return k && this.rows().some((r) => r.key === k) ? k : null;
  });

  ngOnDestroy(): void {
    this.doc.removeEventListener("keydown", this.onCaptureKeydown, true);
  }

  protected toggle(): void {
    if (this.disabled()) return;
    if (this.open()) this.close(false);
    else this.openPanel();
  }

  private openPanel(): void {
    this.query.set("");
    this.open.set(true);
    const rows = this.rows();
    const cur = rows.find((r) => r.model === this.value()) ?? rows[0];
    this.activeKey.set(cur?.key ?? null);
    this.zone.runOutsideAngular(() => {
      this.doc.addEventListener("keydown", this.onCaptureKeydown, true);
    });
    afterNextRender(
      () => {
        if (this.open()) this.searchEl()?.nativeElement.focus();
      },
      { injector: this.injector },
    );
  }

  private close(restoreFocus: boolean): void {
    this.open.set(false);
    this.doc.removeEventListener("keydown", this.onCaptureKeydown, true);
    if (restoreFocus) this.btnEl()?.nativeElement.focus();
  }

  protected choose(model: string): void {
    this.close(true);
    if (model !== this.value()) this.valueChange.emit(model);
  }

  protected onQuery(q: string): void {
    this.query.set(q);
    this.activeKey.set(this.rows()[0]?.key ?? null);
  }

  protected onTriggerKey(ev: KeyboardEvent): void {
    if (!this.open() && (ev.key === "ArrowDown" || ev.key === "ArrowUp")) {
      ev.preventDefault();
      this.openPanel();
    }
  }

  protected onSearchKey(ev: KeyboardEvent): void {
    const rows = this.rows();
    const idx = rows.findIndex((r) => r.key === this.activeId());
    let next = -1;
    switch (ev.key) {
      case "ArrowDown":
        next = rows.length ? (idx + 1) % rows.length : -1;
        break;
      case "ArrowUp":
        next = rows.length ? (idx <= 0 ? rows.length - 1 : idx - 1) : -1;
        break;
      case "Home":
        next = rows.length ? 0 : -1;
        break;
      case "End":
        next = rows.length - 1;
        break;
      case "Enter": {
        ev.preventDefault();
        const r = rows[idx];
        if (r) this.choose(r.model);
        return;
      }
      case "Tab":
        this.close(false);
        return;
      default:
        return;
    }
    ev.preventDefault();
    if (next >= 0) {
      this.activeKey.set(rows[next].key);
      this.doc.getElementById(rows[next].key)?.scrollIntoView?.({
        block: "nearest",
      });
    }
  }

  protected onDocClick(ev: Event): void {
    if (!this.open()) return;
    const t = ev.target as Node | null;
    if (t && !this.wrapEl()?.nativeElement.contains(t)) this.close(false);
  }

  protected onFocusOut(ev: FocusEvent): void {
    if (!this.open()) return;
    const next = ev.relatedTarget as Node | null;
    if (next && !this.wrapEl()?.nativeElement.contains(next)) {
      this.close(false);
    }
  }
}
