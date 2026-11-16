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
  Injector,
  NgZone,
  type OnDestroy,
  ViewEncapsulation,
  afterNextRender,
  afterRenderEffect,
  computed,
  effect,
  inject,
  signal,
  untracked,
  viewChild,
} from "@angular/core";
import { DomSanitizer, type SafeHtml } from "@angular/platform-browser";
import { MediaViewerService } from "./media-viewer.service";
import { TipDirective } from "./tip.directive";

// svgSize reads a rendered diagram's drawn size from its viewBox.
export function svgSize(svg: string): { w: number; h: number } | null {
  const vb = /<svg[^>]*\sviewBox="([^"]+)"/.exec(svg)?.[1];
  const parts = vb
    ?.trim()
    .split(/[\s,]+/)
    .map(Number);
  if (!parts || parts.length !== 4 || parts.some((x) => !Number.isFinite(x))) {
    return null;
  }
  return parts[2] > 0 && parts[3] > 0 ? { w: parts[2], h: parts[3] } : null;
}

export const ZOOM_STEPS = [50, 75, 100, 125, 150, 200, 300] as const;

// g-media-viewer is the full-screen viewer for attached images and Mermaid
// diagrams: a native modal <dialog> (top layer, page inert). X, a click on
// the backdrop or Escape closes it; arrow keys page through several items
// with an "i / N" counter; + / - / 0 zoom; Download saves the item.
@Component({
  selector: "g-media-viewer",
  imports: [TipDirective],
  encapsulation: ViewEncapsulation.None,
  styleUrl: "./media-viewer.css",
  changeDetection: ChangeDetectionStrategy.OnPush,
  // Escape and the Close button are the keyboard's way out; this is the
  // pointer's click on the backdrop.
  host: { "(click)": "onBackdropClick($event)" },
  template: `
    <dialog
      #dlg
      class="mv-dialog"
      [attr.aria-label]="label()"
      data-testid="media-viewer"
      (cancel)="$event.preventDefault(); viewer.close()"
    >
      @if (item(); as it) {
        <div class="mv-bar">
          <span
            class="mv-title"
            [title]="it.name"
            data-testid="media-viewer-title"
          >
            {{ it.name }}
          </span>
          @if (count() > 1) {
            <span
              class="mv-count tabular"
              aria-live="polite"
              data-testid="media-viewer-count"
              >{{ index() + 1 }} / {{ count() }}</span
            >
          }
          <span class="mv-actions">
            <button
              type="button"
              class="icon-btn"
              aria-label="Zoom out"
              gTip="Zoom out (-)"
              data-testid="media-viewer-zoom-out"
              [disabled]="zoom() <= zoomMin"
              (click)="zoomBy(-1)"
            >
              <svg
                width="20"
                height="20"
                viewBox="0 0 24 24"
                aria-hidden="true"
              >
                <path d="M5 11h14v2H5z" fill="currentColor" />
              </svg>
            </button>
            <button
              type="button"
              class="btn-text mv-zoom tabular"
              aria-label="Reset zoom"
              gTip="Fit to screen (0)"
              data-testid="media-viewer-zoom"
              (click)="zoom.set(100)"
            >
              {{ zoom() }}%
            </button>
            <button
              type="button"
              class="icon-btn"
              aria-label="Zoom in"
              gTip="Zoom in (+)"
              data-testid="media-viewer-zoom-in"
              [disabled]="zoom() >= zoomMax"
              (click)="zoomBy(1)"
            >
              <svg
                width="20"
                height="20"
                viewBox="0 0 24 24"
                aria-hidden="true"
              >
                <path
                  d="M11 5h2v6h6v2h-6v6h-2v-6H5v-2h6z"
                  fill="currentColor"
                />
              </svg>
            </button>
            @if (it.kind === "image") {
              <a
                class="icon-btn"
                [href]="it.src"
                [attr.download]="it.name"
                aria-label="Download"
                gTip="Download"
                data-testid="media-viewer-download"
              >
                <svg
                  width="20"
                  height="20"
                  viewBox="0 0 24 24"
                  aria-hidden="true"
                >
                  <path [attr.d]="downloadPath" fill="currentColor" />
                </svg>
              </a>
            } @else {
              <button
                type="button"
                class="icon-btn"
                aria-label="Download SVG"
                gTip="Download SVG"
                data-testid="media-viewer-download"
                (click)="downloadSvg(it.svg, it.name)"
              >
                <svg
                  width="20"
                  height="20"
                  viewBox="0 0 24 24"
                  aria-hidden="true"
                >
                  <path [attr.d]="downloadPath" fill="currentColor" />
                </svg>
              </button>
            }
            <button
              #closeBtn
              type="button"
              class="icon-btn"
              aria-label="Close viewer"
              gTip="Close (Esc)"
              data-testid="media-viewer-close"
              (click)="viewer.close()"
            >
              <svg
                width="20"
                height="20"
                viewBox="0 0 24 24"
                aria-hidden="true"
              >
                <path
                  d="M19 6.41 17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z"
                  fill="currentColor"
                />
              </svg>
            </button>
          </span>
        </div>
        <div class="mv-body">
          <div #stage class="mv-stage" data-testid="media-viewer-stage">
            @if (it.kind === "image") {
              <img
                class="mv-media mv-img"
                [src]="it.src"
                [alt]="it.name"
                [class.is-sizing]="!size()"
                [style.width.px]="size()?.w"
                [style.height.px]="size()?.h"
                (load)="onImageLoad($event)"
                data-testid="media-viewer-image"
              />
            } @else {
              <div
                class="mv-media mv-svg"
                [style.width.px]="size()?.w"
                [style.height.px]="size()?.h"
                [innerHTML]="svgHtml()"
                data-testid="media-viewer-svg"
              ></div>
            }
          </div>
          @if (count() > 1) {
            <button
              type="button"
              class="mv-nav is-prev"
              [attr.aria-label]="'Previous ' + noun()"
              gTip="Previous (←)"
              data-testid="media-viewer-prev"
              (click)="viewer.step(-1)"
            >
              <svg
                width="24"
                height="24"
                viewBox="0 0 24 24"
                aria-hidden="true"
              >
                <path
                  d="M15.41 7.41 14 6l-6 6 6 6 1.41-1.41L10.83 12z"
                  fill="currentColor"
                />
              </svg>
            </button>
            <button
              type="button"
              class="mv-nav is-next"
              [attr.aria-label]="'Next ' + noun()"
              gTip="Next (→)"
              data-testid="media-viewer-next"
              (click)="viewer.step(1)"
            >
              <svg
                width="24"
                height="24"
                viewBox="0 0 24 24"
                aria-hidden="true"
              >
                <path
                  d="M10 6 8.59 7.41 13.17 12l-4.58 4.59L10 18l6-6z"
                  fill="currentColor"
                />
              </svg>
            </button>
          }
        </div>
      }
    </dialog>
  `,
})
export class MediaViewerComponent implements OnDestroy {
  protected readonly viewer = inject(MediaViewerService);
  private readonly sanitizer = inject(DomSanitizer);
  private readonly zone = inject(NgZone);
  private readonly injector = inject(Injector);
  private readonly dlg = viewChild<ElementRef<HTMLDialogElement>>("dlg");
  private readonly closeBtn =
    viewChild<ElementRef<HTMLButtonElement>>("closeBtn");

  protected readonly zoomMin = ZOOM_STEPS[0];
  protected readonly zoomMax = ZOOM_STEPS[ZOOM_STEPS.length - 1];
  protected readonly downloadPath = "M5 20h14v-2H5zM19 9h-4V3H9v6H5l7 7z";

  protected readonly zoom = signal(100);
  /** The item's own size: an image's natural pixels, a diagram's viewBox. */
  private readonly natural = signal<{ w: number; h: number } | null>(null);
  /** The stage's content box, kept current by a ResizeObserver. */
  private readonly room = signal<{ w: number; h: number } | null>(null);
  private readonly stage = viewChild<ElementRef<HTMLElement>>("stage");
  private observer?: ResizeObserver;
  private observed: HTMLElement | null = null;

  // At 100% an item fits the stage: an image never grows past its own
  // pixels, a diagram may grow to twice its drawn size. Zoom scales from
  // there, and the stage scrolls once the item outgrows it.
  protected readonly size = computed(() => {
    const n = this.natural();
    const r = this.room();
    if (!n || !r || n.w <= 0 || n.h <= 0) return null;
    const cap = this.item()?.kind === "svg" ? 2 : 1;
    const fit = Math.min(cap, r.w / n.w, r.h / n.h);
    const z = (fit * this.zoom()) / 100;
    return { w: Math.round(n.w * z), h: Math.round(n.h * z) };
  });
  protected readonly index = computed(() => this.viewer.state()?.index ?? 0);
  protected readonly count = computed(
    () => this.viewer.state()?.items.length ?? 0,
  );
  protected readonly item = computed(() => {
    const s = this.viewer.state();
    return s ? s.items[s.index] : undefined;
  });
  protected readonly noun = computed(() =>
    this.item()?.kind === "svg" ? "diagram" : "image",
  );
  protected readonly label = computed(() => {
    const it = this.item();
    if (!it) return "Viewer";
    return it.kind === "image" ? `Image: ${it.name}` : it.name;
  });
  // The SVG has passed Mermaid's strict mode and sanitizeMermaidSvg
  // (shared/mermaid.ts) before it was handed to the viewer.
  protected readonly svgHtml = computed<SafeHtml>(() => {
    const it = this.item();
    return this.sanitizer.bypassSecurityTrustHtml(
      it?.kind === "svg" ? it.svg : "",
    );
  });

  private readonly onKey = (ev: KeyboardEvent): void => {
    if (!this.viewer.isOpen() || ev.ctrlKey || ev.metaKey || ev.altKey) {
      return;
    }
    let handled = true;
    switch (ev.key) {
      case "Escape":
        this.zone.run(() => this.viewer.close());
        break;
      case "ArrowLeft":
        this.zone.run(() => this.viewer.step(-1));
        break;
      case "ArrowRight":
        this.zone.run(() => this.viewer.step(1));
        break;
      case "+":
      case "=":
        this.zone.run(() => this.zoomBy(1));
        break;
      case "-":
        this.zone.run(() => this.zoomBy(-1));
        break;
      case "0":
        this.zone.run(() => this.zoom.set(100));
        break;
      default:
        handled = false;
    }
    if (handled) {
      // Captured on window, ahead of the page's own document listeners, so
      // Escape closes only the viewer (not a full-screen conversation or
      // a drawer underneath) and arrows do not also move a list selection.
      ev.preventDefault();
      ev.stopImmediatePropagation();
    }
  };

  constructor() {
    this.zone.runOutsideAngular(() =>
      window.addEventListener("keydown", this.onKey, true),
    );
    // A new item starts fitted to the screen.
    effect(() => {
      const it = this.item();
      untracked(() => {
        this.zoom.set(100);
        this.natural.set(it?.kind === "svg" ? svgSize(it.svg) : null);
      });
    });
    afterRenderEffect(() => {
      const el = this.stage()?.nativeElement;
      if (!el || typeof ResizeObserver === "undefined") return;
      if (el === this.observed) return;
      this.observed = el;
      this.observer?.disconnect();
      this.observer = new ResizeObserver(() => this.measure(el));
      this.observer.observe(el);
      this.measure(el);
    });
    effect(() => {
      const open = this.viewer.isOpen();
      const el = this.dlg()?.nativeElement;
      if (!el) return;
      if (open && !el.open) {
        if (typeof el.showModal === "function") el.showModal();
        else el.setAttribute("open", "");
        afterNextRender(() => this.closeBtn()?.nativeElement.focus(), {
          injector: this.injector,
        });
      } else if (!open && el.open) {
        el.close();
      }
    });
  }

  private measure(el: HTMLElement): void {
    const st = getComputedStyle(el);
    const w =
      el.clientWidth - parseFloat(st.paddingLeft) - parseFloat(st.paddingRight);
    const h =
      el.clientHeight -
      parseFloat(st.paddingTop) -
      parseFloat(st.paddingBottom);
    if (w <= 0 || h <= 0) return;
    const cur = this.room();
    if (!cur || cur.w !== w || cur.h !== h) this.room.set({ w, h });
  }

  protected onImageLoad(ev: Event): void {
    const img = ev.target as HTMLImageElement;
    this.natural.set({ w: img.naturalWidth, h: img.naturalHeight });
  }

  ngOnDestroy(): void {
    this.observer?.disconnect();
    window.removeEventListener("keydown", this.onKey, true);
    const el = this.dlg()?.nativeElement;
    if (el?.open) el.close();
  }

  protected zoomBy(dir: 1 | -1): void {
    const cur = this.zoom();
    const next =
      dir > 0
        ? ZOOM_STEPS.find((z) => z > cur)
        : [...ZOOM_STEPS].reverse().find((z) => z < cur);
    if (next !== undefined) this.zoom.set(next);
  }

  // The space around the item is the backdrop: a click there closes.
  protected onBackdropClick(ev: MouseEvent): void {
    const t = ev.target as HTMLElement | null;
    if (
      t?.classList.contains("mv-stage") ||
      t?.classList.contains("mv-body") ||
      t?.classList.contains("mv-svg")
    ) {
      this.viewer.close();
    }
  }

  protected downloadSvg(svg: string, name: string): void {
    const url = URL.createObjectURL(new Blob([svg], { type: "image/svg+xml" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = `${name.replace(/[^\w.-]+/g, "-").toLowerCase() || "diagram"}.svg`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }
}
