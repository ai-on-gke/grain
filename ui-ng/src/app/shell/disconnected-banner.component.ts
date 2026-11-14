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
  type AfterViewInit,
  ChangeDetectionStrategy,
  Component,
  ElementRef,
  NgZone,
  type OnDestroy,
  computed,
  effect,
  inject,
  viewChild,
} from "@angular/core";
import {
  ConnectionService,
  type Disconnection,
} from "../services/connection.service";
import { formatClock, preferredZone } from "./clock";

// bannerText is the banner's sentence: why, since when, and that nothing
// needs doing -- in the classic DisconnectedBanner's words. It says when
// the outage began, not how long ago: it is a live region, and an age
// that ticked over every minute would be a fresh announcement each time.
export function bannerText(d: Disconnection, zone = ""): string {
  const at = formatClock(d.since, zone);
  const since = at ? ` since ${at}` : "";
  return d.restarting
    ? `${d.reason} Unavailable${since} -- retrying automatically; this page will pick back up on its own once grain is back.`
    : `${d.reason} Unreachable${since} -- retrying automatically; this page will pick back up on its own once it answers again.`;
}

// MODAL_HOSTS is what counts as an open modal for the mirror: the shell's
// and pages' role="dialog" aria-modal="true" sheets, and native
// <dialog>s opened with showModal() (the full-screen conversation),
// which make everything outside them inert -- the banner included.
const MODAL_HOSTS = '[aria-modal="true"], dialog[open]';

// DisconnectedBannerComponent is ConnectionService on screen: a
// role="status" strip above the rail and the page while nothing the
// shell asks the deployment gets an answer, gone on the first answer.
// Everything already on screen stays as it was; this is the one thing
// that says it is now as old as "since" says.
//
// role="status" rather than "alert": it is news, not an interruption,
// so a screen reader hears it once rather than over whatever it is
// reading. The live region is always in the document, empty while
// connected, so the text arriving is what gets announced.
//
// While a modal is open, the shell's own copy is inert or covered, so
// the same sentence is mirrored as a strip first inside the topmost
// modal (the classic UI's ModalStatusMirror). The mirror only watches
// the document while the deployment is unreachable.
@Component({
  selector: "g-disconnected-banner",
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { style: "display: contents" },
  template: `
    <div
      #bannerEl
      class="disconnected-banner"
      role="status"
      data-testid="disconnected-banner"
      [class.is-up]="!!text()"
    >
      @if (text(); as t) {
        <svg
          class="disconnected-banner-icon"
          width="16"
          height="16"
          viewBox="0 0 24 24"
          aria-hidden="true"
        >
          <path [attr.d]="cloudOff" fill="currentColor" />
        </svg>
        <span class="disconnected-banner-text">{{ t }}</span>
      }
    </div>
  `,
})
export class DisconnectedBannerComponent implements AfterViewInit, OnDestroy {
  private readonly connection = inject(ConnectionService);
  private readonly doc = inject(DOCUMENT);
  private readonly zone = inject(NgZone);
  private readonly bannerEl =
    viewChild.required<ElementRef<HTMLElement>>("bannerEl");

  protected readonly cloudOff =
    "M19.35 10.04A7.49 7.49 0 0 0 12 4c-1.48 0-2.85.43-4.01 1.17l1.46 1.46A5.47 5.47 0 0 1 12 6c3.04 0 5.5 2.46 5.5 5.5v.5H19c1.66 0 3 1.34 3 3 0 1.13-.64 2.11-1.56 2.62l1.45 1.45C23.16 18.16 24 16.68 24 15c0-2.64-2.05-4.78-4.65-4.96zM3 5.27l2.75 2.74C2.56 8.15 0 10.77 0 14c0 3.31 2.69 6 6 6h11.73l2 2L21 20.73 4.27 4 3 5.27zM7.73 10l8 8H6c-2.21 0-4-1.79-4-4s1.79-4 4-4h1.73z";

  protected readonly text = computed(() => {
    const d = this.connection.disconnection();
    return d ? bannerText(d, preferredZone()) : "";
  });

  private observer: MutationObserver | null = null;
  private resize: ResizeObserver | null = null;
  private mirrorHost: HTMLElement | null = null;
  private mirrorEl: HTMLElement | null = null;
  private syncQueued = false;

  constructor() {
    effect(() => {
      const t = this.text();
      if (t) {
        this.watch();
        this.syncMirror(t);
      } else {
        this.unwatch();
      }
    });
  }

  ngAfterViewInit(): void {
    // --grain-banner-height lets the few fixed-position overlays anchored
    // under the top bar (the account popover, Sessions' canvas) move down
    // with it while the banner is up.
    const el = this.bannerEl().nativeElement;
    const root = this.doc.documentElement;
    const set = () =>
      root.style.setProperty("--grain-banner-height", `${el.offsetHeight}px`);
    set();
    if (typeof ResizeObserver !== "undefined") {
      this.zone.runOutsideAngular(() => {
        this.resize = new ResizeObserver(set);
        this.resize.observe(el);
      });
    }
  }

  ngOnDestroy(): void {
    this.unwatch();
    this.resize?.disconnect();
    this.doc.documentElement.style.removeProperty("--grain-banner-height");
  }

  private watch(): void {
    if (this.observer || typeof MutationObserver === "undefined") return;
    this.zone.runOutsideAngular(() => {
      this.observer = new MutationObserver(() => this.queueSync());
      this.observer.observe(this.doc.body, {
        childList: true,
        subtree: true,
        attributes: true,
        attributeFilter: ["aria-modal", "open", "inert", "hidden"],
      });
    });
  }

  private unwatch(): void {
    this.observer?.disconnect();
    this.observer = null;
    this.mirrorEl?.remove();
    this.mirrorEl = null;
    this.mirrorHost = null;
  }

  private queueSync(): void {
    if (this.syncQueued) return;
    this.syncQueued = true;
    queueMicrotask(() => {
      this.syncQueued = false;
      const t = this.text();
      if (t) this.syncMirror(t);
    });
  }

  // syncMirror puts the sentence first inside the topmost open modal, or
  // takes it out of one that closed.
  private syncMirror(text: string): void {
    const open = Array.from(
      this.doc.querySelectorAll<HTMLElement>(MODAL_HOSTS),
    ).filter(
      (el) =>
        el.isConnected &&
        !el.closest("[hidden]") &&
        !el.parentElement?.closest("[inert]"),
    );
    const host = open[open.length - 1] ?? null;
    if (host !== this.mirrorHost || !this.mirrorEl?.isConnected) {
      this.mirrorEl?.remove();
      this.mirrorEl = null;
      this.mirrorHost = host;
      if (host) {
        const el = this.doc.createElement("div");
        el.className = "disconnected-mirror";
        el.setAttribute("role", "status");
        el.setAttribute("data-testid", "disconnected-banner-mirror");
        el.textContent = text;
        host.prepend(el);
        this.mirrorEl = el;
      }
    } else if (this.mirrorEl && this.mirrorEl.textContent !== text) {
      this.mirrorEl.textContent = text;
    }
  }
}
