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
  AfterViewInit,
  ChangeDetectionStrategy,
  Component,
  ElementRef,
  NgZone,
  OnDestroy,
  OnInit,
  ViewEncapsulation,
  computed,
  inject,
  output,
  signal,
  viewChild,
} from "@angular/core";
import { NavConfigService } from "../services/nav-config.service";
import { SnackbarService } from "../services/snackbar.service";
import { DESTINATIONS, type Destination } from "./nav";

const BY_ID = new Map<string, Destination>(DESTINATIONS.map((d) => [d.id, d]));

@Component({
  selector: "g-customize-nav-dialog",
  encapsulation: ViewEncapsulation.None,
  styleUrl: "./shell-overlays.css",
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="scrim" aria-hidden="true" (click)="closed.emit()"></div>
    <div
      #dialogEl
      class="shell-modal"
      role="dialog"
      aria-modal="true"
      aria-labelledby="customize-nav-title"
      data-testid="customize-nav-dialog"
    >
      <div class="shell-modal-head">
        <div class="shell-modal-title-group">
          <span class="status-pill neutral">Navigation</span>
          <h2 class="shell-modal-title" id="customize-nav-title">
            Customize navigation
          </h2>
        </div>
        <button
          #closeBtn
          type="button"
          class="icon-btn"
          aria-label="Close customize navigation"
          data-testid="customize-nav-close"
          (click)="closed.emit()"
        >
          <svg width="20" height="20" viewBox="0 0 24 24" aria-hidden="true">
            <path
              d="M19 6.41 17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z"
              fill="currentColor"
            />
          </svg>
        </button>
      </div>

      <div class="shell-modal-body" tabindex="0">
        <p class="muted" style="margin: 0; font-size: 13px">
          Choices apply to this browser only. Hidden destinations stay reachable
          from More on the rail and by link. Number keys 1–9 follow your visible
          order.
        </p>

        <ul class="nav-custom-list" aria-label="Rail destinations">
          @for (d of rows(); track d.id; let i = $index) {
            @let locked = d.id === "inbox";
            @let shown = isShown(d.id);
            <li
              class="nav-custom-row"
              [class.is-hidden]="!shown"
              [attr.data-testid]="'nav-row-' + d.id"
            >
              <span class="nav-custom-text">
                <span class="nav-custom-name">{{ d.label }}</span>
                <span class="nav-custom-desc">{{ d.tip }}</span>
              </span>

              @if (locked) {
                <span class="muted nav-custom-lock">Always first</span>
              } @else {
                <span class="nav-custom-move">
                  <button
                    type="button"
                    class="icon-btn"
                    [disabled]="i <= 1"
                    [attr.aria-label]="'Move ' + d.label + ' up'"
                    [attr.data-testid]="'nav-move-up-' + d.id"
                    (click)="move(d.id, -1)"
                  >
                    <svg
                      width="16"
                      height="16"
                      viewBox="0 0 24 24"
                      aria-hidden="true"
                      style="transform: rotate(180deg)"
                    >
                      <path d="m7 10 5 5 5-5z" fill="currentColor" />
                    </svg>
                  </button>
                  <button
                    type="button"
                    class="icon-btn"
                    [disabled]="i === rows().length - 1"
                    [attr.aria-label]="'Move ' + d.label + ' down'"
                    [attr.data-testid]="'nav-move-down-' + d.id"
                    (click)="move(d.id, 1)"
                  >
                    <svg
                      width="16"
                      height="16"
                      viewBox="0 0 24 24"
                      aria-hidden="true"
                    >
                      <path d="m7 10 5 5 5-5z" fill="currentColor" />
                    </svg>
                  </button>
                </span>
                <button
                  type="button"
                  class="g-switch"
                  role="switch"
                  [attr.aria-checked]="shown"
                  [attr.aria-label]="'Show ' + d.label + ' on the rail'"
                  [attr.data-testid]="'nav-toggle-' + d.id"
                  (click)="toggleShown(d.id)"
                ></button>
              }
            </li>
          }
        </ul>

        <p class="muted" style="margin: 0; font-size: 12px">
          New task stays at the top of the rail and Settings stays at the
          bottom, so navigation can always be customized back.
        </p>
      </div>

      <div class="shell-modal-foot">
        <button
          type="button"
          class="btn-text"
          data-testid="nav-reset"
          (click)="resetDraft()"
        >
          Reset to default
        </button>
        <span style="flex: 1"></span>
        <button
          type="button"
          class="btn-text"
          data-testid="nav-cancel"
          (click)="closed.emit()"
        >
          Cancel
        </button>
        <button
          type="button"
          class="btn-primary"
          data-testid="nav-save"
          (click)="save()"
        >
          Save
        </button>
      </div>
    </div>
  `,
})
export class CustomizeNavDialogComponent
  implements OnInit, AfterViewInit, OnDestroy
{
  private readonly navConfig = inject(NavConfigService);
  private readonly snackbar = inject(SnackbarService);
  private readonly doc = inject(DOCUMENT);
  private readonly zone = inject(NgZone);

  readonly closed = output<void>();

  protected readonly draftOrder = signal<string[]>([]);
  protected readonly draftHidden = signal<Set<string>>(new Set());

  private readonly dialogEl = viewChild<ElementRef<HTMLElement>>("dialogEl");
  private readonly closeBtn =
    viewChild<ElementRef<HTMLButtonElement>>("closeBtn");

  private readonly onCaptureKeydown = (ev: KeyboardEvent): void => {
    if (ev.key === "Escape") {
      this.zone.run(() => this.onEscape(ev));
    } else if (ev.key === "Tab") {
      this.zone.run(() => this.onTab(ev));
    }
  };

  protected readonly rows = computed<Destination[]>(() =>
    this.draftOrder()
      .map((id) => BY_ID.get(id))
      .filter((d): d is Destination => !!d),
  );

  ngOnInit(): void {
    this.draftOrder.set([...this.navConfig.order()]);
    this.draftHidden.set(new Set(this.navConfig.hidden()));
  }

  ngAfterViewInit(): void {
    this.zone.runOutsideAngular(() => {
      this.doc.addEventListener("keydown", this.onCaptureKeydown, true);
    });
    this.closeBtn()?.nativeElement.focus();
  }

  ngOnDestroy(): void {
    this.doc.removeEventListener("keydown", this.onCaptureKeydown, true);
  }

  protected isShown(id: string): boolean {
    return id === "inbox" || !this.draftHidden().has(id);
  }

  protected toggleShown(id: string): void {
    if (id === "inbox") return;
    this.draftHidden.update((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  protected move(id: string, delta: -1 | 1): void {
    if (id === "inbox") return;
    this.draftOrder.update((prev) => {
      const next = [...prev];
      const idx = next.indexOf(id);
      const to = idx + delta;
      // Index 0 is reserved for Inbox.
      if (idx < 1 || to < 1 || to >= next.length) return prev;
      next.splice(idx, 1);
      next.splice(to, 0, id);
      return next;
    });
  }

  protected resetDraft(): void {
    this.draftOrder.set(DESTINATIONS.map((d) => d.id));
    this.draftHidden.set(new Set());
  }

  protected save(): void {
    const hidden = [...this.draftHidden()];
    this.navConfig.save(this.draftOrder(), hidden);
    this.snackbar.show(
      hidden.length > 0
        ? `Navigation saved · ${hidden.length} hidden under More`
        : "Navigation saved",
    );
    this.closed.emit();
  }

  protected onEscape(ev: Event): void {
    if (ev.defaultPrevented) return;
    ev.preventDefault();
    ev.stopPropagation();
    this.closed.emit();
  }

  protected onTab(ev: Event): void {
    const ke = ev as KeyboardEvent;
    const root = this.dialogEl()?.nativeElement;
    if (!root) return;
    const focusables = Array.from(
      root.querySelectorAll<HTMLElement>(
        'button:not([disabled]), a[href], input:not([disabled]), [tabindex]:not([tabindex="-1"])',
      ),
    );
    if (!focusables.length) return;
    const first = focusables[0];
    const last = focusables[focusables.length - 1];
    const active = this.doc.activeElement;
    if (ke.shiftKey && (active === first || !root.contains(active))) {
      ke.preventDefault();
      last.focus();
    } else if (!ke.shiftKey && (active === last || !root.contains(active))) {
      ke.preventDefault();
      first.focus();
    }
  }
}
