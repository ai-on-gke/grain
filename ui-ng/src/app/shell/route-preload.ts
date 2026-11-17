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

import { DOCUMENT, Location } from "@angular/common";
import { Injectable, inject } from "@angular/core";
import {
  type NavigationError,
  type PreloadingStrategy,
  RedirectCommand,
  type Route,
  Router,
} from "@angular/router";
import { type Observable, EMPTY, catchError, mergeMap, timer } from "rxjs";
import { SnackbarService } from "../services/snackbar.service";

// How long after the app starts to begin fetching the other pages' lazy
// chunks: long enough that the first page's own reads go out first, short
// enough that the chunks are in before the reader's first click.
export const ROUTE_PRELOAD_DELAY_MS = 1500;

export function saveDataEnabled(): boolean {
  return Boolean(
    (globalThis.navigator as { connection?: { saveData?: boolean } })
      ?.connection?.saveData,
  );
}

// IdleRoutePreload fetches every lazy page's chunk shortly after the app
// starts, so clicking to another page never has to wait on the network.
// Without it a navigation could sit behind a slow or stalled origin (an
// identity proxy holding requests for 10-15s) with nothing changing on
// screen, which reads as a frozen page (task 5801). A chunk that fails to
// load is left for the router to fetch again on the real navigation.
// Readers who asked to save data (navigator.connection.saveData) are left
// with plain on-demand loading.
@Injectable({ providedIn: "root" })
export class IdleRoutePreload implements PreloadingStrategy {
  preload(_route: Route, load: () => Observable<unknown>): Observable<unknown> {
    if (saveDataEnabled()) return EMPTY;
    return timer(ROUTE_PRELOAD_DELAY_MS).pipe(
      mergeMap(() => load()),
      catchError(() => EMPTY),
    );
  }
}

export function resolveReloadHref(
  location: Location,
  dest?: string,
): string | null {
  if (!dest || !/^\/(?![/\\])/.test(dest) || dest.includes("\\")) {
    return null;
  }
  const href = location.prepareExternalUrl(dest);
  if (!/^\/(?![/\\])/.test(href) || href.includes("\\")) {
    return null;
  }
  return href;
}

// staleChunkErrorHandler surfaces a Reload snackbar when a lazy route's chunk
// fails to load (for example a 404 on an open tab after a daemon upgrade) and
// keeps the reader on the already-loaded page without an unhandled rejection.
export function staleChunkErrorHandler(
  e: NavigationError,
): RedirectCommand | void {
  const router = inject(Router);
  const location = inject(Location);
  const doc = inject(DOCUMENT);
  const dest = e.url;
  inject(SnackbarService).show(
    "Page could not load — Grain may have updated. Reload to open it.",
    {
      label: "Reload",
      run: () => {
        const win = doc.defaultView;
        if (!win) return;
        const href = resolveReloadHref(location, dest);
        if (href) win.location.assign(href);
        else win.location.reload();
      },
    },
  );
  if (router.navigated) {
    return new RedirectCommand(router.parseUrl(router.url), {
      skipLocationChange: true,
    });
  }
}
